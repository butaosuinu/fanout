package run

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/peermsg"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

func TestPreseedTaskTeamRegistryMakesEarlyMessageReplyAddressable(t *testing.T) {
	const parent = "plan:demo"
	dbPath := filepath.Join(t.TempDir(), "team.db")
	t.Setenv(team.DBPathEnv, dbPath)
	tasks := []planspec.Task{
		{ID: "task-a", Title: "Task A"},
		{ID: "task-b", Title: "Task B", DisplayName: "Second task"},
	}
	cfg := &cliflags.Config{
		Agent: "claude",
		AgentOverrides: []cliflags.AgentOverride{
			{Target: "task-b", Name: "codex"},
		},
	}
	if !planHasCodexTeamBridge(cfg, tasks) {
		t.Fatal("mixed cohort did not enable Codex team preseed")
	}
	if err := preseedTaskTeamRegistry(dbPath, parent, tasks, cfg); err != nil {
		t.Fatalf("preseedTaskTeamRegistry: %v", err)
	}

	db, err := team.Open(dbPath)
	if err != nil {
		t.Fatalf("team.Open: %v", err)
	}
	if schemaErr := team.EnsureSchema(db); schemaErr != nil {
		t.Fatalf("team.EnsureSchema: %v", schemaErr)
	}
	store, err := msgstore.New(db, parent)
	if err != nil {
		t.Fatalf("msgstore.New: %v", err)
	}
	if _, sendErr := store.Send(
		team.TaskPeerNum(parent, "task-b"),
		team.TaskPeerNum(parent, "task-a"),
		"note", "sent before the final pane starts", team.Now(),
	); sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close sender db: %v", closeErr)
	}
	db, err = team.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen team db for Herdr final row: %v", err)
	}
	if err := team.UpsertPeer(db, state.Pane{
		Parent: parent, TaskID: "task-a", Backend: backend.Herdr,
		PaneID: "w1:p1", HerdrWorkspaceID: "w1", HerdrTerminalID: "terminal-a",
		Agent: "claude", WorktreePath: "/repo/.fanout/worktrees/demo-task-a",
	}, team.Now()); err != nil {
		t.Fatalf("replace provisional task with Herdr row: %v", err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close Herdr final row db: %v", closeErr)
	}

	var stdout, stderr bytes.Buffer
	watcher, code := peermsg.OpenWatcher(
		peermsg.Request{SelfRaw: "task-a", Parent: parent},
		peermsg.Deps{},
		log.NewWith(&stdout, &stderr, false),
	)
	if code != exitcode.OK {
		t.Fatalf("OpenWatcher code = %d, want OK; stderr=%s", code, stderr.String())
	}
	events, pollErr := watcher.Poll()
	watcher.Close()
	if pollErr != nil {
		t.Fatalf("Poll: %v", pollErr)
	}
	if len(events) != 1 {
		t.Fatalf("Poll events = %d, want 1", len(events))
	}
	line := events[0].HumanLine()
	if !strings.Contains(line, "task-b -> task-a") {
		t.Fatalf("HumanLine = %q, want reply-addressable task ids", line)
	}

	if cleanupErr := cleanupUncreatedTaskPeers(dbPath, parent, tasks, []string{"task-a"}); cleanupErr != nil {
		t.Fatalf("cleanupUncreatedTaskPeers: %v", cleanupErr)
	}
	db, err = team.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen team db: %v", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close verification db: %v", closeErr)
		}
	}()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM peers WHERE task_id = 'task-b'").Scan(&count); err != nil {
		t.Fatalf("count task-b peers: %v", err)
	}
	if count != 0 {
		t.Fatalf("task-b provisional peers = %d, want 0 after fail-fast cleanup", count)
	}
	var paneID string
	if err := db.QueryRow("SELECT pane_id FROM peers WHERE task_id = 'task-a'").Scan(&paneID); err != nil {
		t.Fatalf("select created Herdr task peer: %v", err)
	}
	if paneID != "w1:p1" {
		t.Fatalf("created Herdr task pane = %q, want w1:p1", paneID)
	}
}

func TestPreseedTaskTeamRegistryRejectsWrongDBOwner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "team.db")
	db, err := team.Open(dbPath)
	if err != nil {
		t.Fatalf("team.Open: %v", err)
	}
	if schemaErr := team.EnsureSchema(db); schemaErr != nil {
		t.Fatalf("team.EnsureSchema: %v", schemaErr)
	}
	if _, ownerErr := msgstore.New(db, "plan:other"); ownerErr != nil {
		t.Fatalf("claim other owner: %v", ownerErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close owner db: %v", closeErr)
	}

	cfg := &cliflags.Config{Agent: "codex"}
	err = preseedTaskTeamRegistry(
		dbPath,
		"plan:demo",
		[]planspec.Task{{ID: "task-a", Title: "Task A"}},
		cfg,
	)
	if err == nil || !strings.Contains(err.Error(), "owned by parent plan:other") {
		t.Fatalf("preseed owner error = %v, want owner mismatch", err)
	}
}

func TestPlanHasCodexTeamBridgeIgnoresClaudeOnlyCohort(t *testing.T) {
	if planHasCodexTeamBridge(
		&cliflags.Config{Agent: "claude"},
		[]planspec.Task{{ID: "task-a"}, {ID: "task-b"}},
	) {
		t.Fatal("Claude-only cohort enabled Codex team preseed")
	}
}
