package team

import (
	"database/sql"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestUpsertPeerInsertsAndRewritesOnConflict(t *testing.T) {
	db, _ := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	first := state.Pane{
		IssueNum:     71,
		PaneID:       "%5",
		Slug:         "team-fanout-integration-71",
		WorktreePath: "/repo/.fanout/worktrees/team-fanout-integration-71",
		Agent:        "claude",
		DisplayName:  "Team integration",
	}
	if err := UpsertPeer(db, first, "2026-06-13T01:00:00Z"); err != nil {
		t.Fatalf("UpsertPeer insert: %v", err)
	}

	// Simulate a live pane before the re-fanout: last_seen must not survive
	// the conflict rewrite below.
	if _, err := db.Exec("UPDATE peers SET last_seen = ? WHERE issue = ?", "2026-06-13T01:30:00Z", 71); err != nil {
		t.Fatalf("seed last_seen: %v", err)
	}

	refanned := first
	refanned.PaneID = "%9"
	refanned.WorktreePath = "/repo/.fanout/worktrees/team-fanout-integration-71-redo"
	refanned.Agent = "codex"
	if err := UpsertPeer(db, refanned, "2026-06-13T02:00:00Z"); err != nil {
		t.Fatalf("UpsertPeer conflict update: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count); err != nil {
		t.Fatalf("count peers: %v", err)
	}
	if count != 1 {
		t.Fatalf("peers rows = %d, want 1 (issue is the PK)", count)
	}

	var (
		paneID, slug, worktree, agent, display, joinedAt string
		lastSeen                                         sql.NullString
	)
	err := db.QueryRow(
		"SELECT pane_id, slug, worktree_path, agent, display_name, joined_at, last_seen FROM peers WHERE issue = ?", 71,
	).Scan(&paneID, &slug, &worktree, &agent, &display, &joinedAt, &lastSeen)
	if err != nil {
		t.Fatalf("select peer: %v", err)
	}
	if paneID != "%9" || worktree != refanned.WorktreePath || agent != "codex" {
		t.Errorf("peer identity = (%q, %q, %q), want the re-fanned pane's values", paneID, worktree, agent)
	}
	if slug != refanned.Slug || display != refanned.DisplayName {
		t.Errorf("peer naming = (%q, %q), want (%q, %q)", slug, display, refanned.Slug, refanned.DisplayName)
	}
	if joinedAt != "2026-06-13T02:00:00Z" {
		t.Errorf("joined_at = %q, want the re-fanout timestamp", joinedAt)
	}
	if lastSeen.Valid {
		t.Errorf("last_seen = %q, want NULL after the conflict rewrite", lastSeen.String)
	}
}

func TestUpsertPeerKeysPlanTaskBySyntheticNumber(t *testing.T) {
	db, _ := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	pane := state.Pane{
		Parent:       "plan:launch-plan",
		IssueNum:     0,
		TaskID:       "base-types",
		PaneID:       "%5",
		Slug:         "launch-plan-base-types",
		WorktreePath: "/repo/.fanout/worktrees/launch-plan-base-types",
		Agent:        "claude",
		DisplayName:  "Base types",
	}
	if err := UpsertPeer(db, pane, "2026-06-13T01:00:00Z"); err != nil {
		t.Fatalf("UpsertPeer plan task: %v", err)
	}

	wantNum := TaskPeerNum(pane.Parent, pane.TaskID)
	if wantNum >= 0 {
		t.Fatalf("synthetic peer number = %d, want negative", wantNum)
	}
	var (
		issue  int
		taskID sql.NullString
	)
	if err := db.QueryRow("SELECT issue, task_id FROM peers WHERE issue = ?", wantNum).Scan(&issue, &taskID); err != nil {
		t.Fatalf("select plan peer: %v", err)
	}
	if issue != wantNum {
		t.Errorf("peer issue = %d, want synthetic %d", issue, wantNum)
	}
	if !taskID.Valid || taskID.String != "base-types" {
		t.Errorf("peer task_id = %v, want base-types", taskID)
	}
}

func TestDeleteProvisionalTaskPeerRequiresSyntheticNumberAndTaskID(t *testing.T) {
	db, _ := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	const parent = "plan:launch-plan"
	pane := state.Pane{Parent: parent, TaskID: "base-types"}
	if err := UpsertPeer(db, pane, "2026-06-13T01:00:00Z"); err != nil {
		t.Fatalf("UpsertPeer: %v", err)
	}
	if err := DeleteProvisionalTaskPeer(db, parent, "other-task"); err != nil {
		t.Fatalf("DeleteProvisionalTaskPeer(other-task): %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count); err != nil {
		t.Fatalf("count peers after guarded delete: %v", err)
	}
	if count != 1 {
		t.Fatalf("peers after guarded delete = %d, want 1", count)
	}
	if _, err := db.Exec("UPDATE peers SET pane_id = '%9'"); err != nil {
		t.Fatalf("mark peer live: %v", err)
	}
	if err := DeleteProvisionalTaskPeer(db, parent, pane.TaskID); err != nil {
		t.Fatalf("DeleteProvisionalTaskPeer(live base-types): %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count); err != nil {
		t.Fatalf("count live peers after delete: %v", err)
	}
	if count != 1 {
		t.Fatalf("live peers after provisional delete = %d, want 1", count)
	}
	if _, err := db.Exec("UPDATE peers SET pane_id = ''"); err != nil {
		t.Fatalf("restore provisional peer: %v", err)
	}
	if err := DeleteProvisionalTaskPeer(db, parent, pane.TaskID); err != nil {
		t.Fatalf("DeleteProvisionalTaskPeer(base-types): %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count); err != nil {
		t.Fatalf("count peers after matching delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("peers after matching delete = %d, want 0", count)
	}
}

func TestUpsertPeersRollsBackWholeCohort(t *testing.T) {
	db, _ := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_second_task
BEFORE INSERT ON peers WHEN NEW.task_id = 'task-b'
BEGIN SELECT RAISE(FAIL, 'reject task-b'); END`); err != nil {
		t.Fatalf("create rejecting trigger: %v", err)
	}

	panes := []state.Pane{
		{Parent: "plan:demo", TaskID: "task-a"},
		{Parent: "plan:demo", TaskID: "task-b"},
	}
	if err := UpsertPeers(db, panes, "2026-06-13T01:00:00Z"); err == nil {
		t.Fatal("UpsertPeers succeeded, want trigger failure")
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count); err != nil {
		t.Fatalf("count peers: %v", err)
	}
	if count != 0 {
		t.Fatalf("peers after failed batch = %d, want 0", count)
	}
}
