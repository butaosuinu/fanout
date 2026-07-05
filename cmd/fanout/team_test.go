package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

func TestBuildTeamContext(t *testing.T) {
	t.Setenv(team.DBPathEnv, "")
	targets := []ghissue.Issue{
		{Number: 101, Title: "First child"},
		{Number: 102, Title: "Second child"},
	}
	got := buildTeamContext("/repo/project_root", "100", targets)
	if got.ParentLabel != "#100" {
		t.Errorf("ParentLabel = %q, want %q", got.ParentLabel, "#100")
	}
	if got.DBPath != "/tmp/fanout-project_root-100.db" {
		t.Errorf("DBPath = %q, want the team.DBPath convention", got.DBPath)
	}
	if len(got.Siblings) != 2 || got.Siblings[0].Num != 101 || got.Siblings[1].Title != "Second child" {
		t.Errorf("Siblings = %+v, want the targets in plan order", got.Siblings)
	}
}

func TestTeamParentLabel(t *testing.T) {
	for _, tc := range []struct {
		parentRef string
		want      string
	}{
		{"100", "#100"},
		// Leading zeros collapse exactly like team.ParentDBSlug, so the
		// identity line agrees with the DB path shown next to it.
		{"0068", "#68"},
		{"https://github.com/users/butaosuinu/projects/3", "https://github.com/users/butaosuinu/projects/3"},
		{"@manual", "@manual"},
	} {
		if got := teamParentLabel(tc.parentRef); got != tc.want {
			t.Errorf("teamParentLabel(%q) = %q, want %q", tc.parentRef, got, tc.want)
		}
	}
}

func TestSeedTeamRegistryUpsertsCreatedPanes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "team.db")
	st := state.Store{Panes: []state.Pane{
		{Parent: "100", IssueNum: 101, PaneID: "%5", Slug: "first-child-101", Agent: "claude", DisplayName: "first-child-101", WorktreePath: "/repo/.fanout/worktrees/first-child-101"},
		{Parent: "100", IssueNum: 102, PaneID: "%6", Slug: "second-child-102", Agent: "claude", DisplayName: "second-child-102", WorktreePath: "/repo/.fanout/worktrees/second-child-102"},
	}}
	var stdout, stderr bytes.Buffer
	lg := log.NewWith(&stdout, &stderr, false)

	// 103 has no state row: it must warn and not abort the remaining seeds.
	seedTeamRegistry(lg, dbPath, st, "100", []int{101, 103, 102})

	if got := stderr.String(); !strings.Contains(got, "#103: no state row") {
		t.Errorf("stderr = %q, want a missing-row warning for #103", got)
	}
	if got := stdout.String(); !strings.Contains(got, "seeded 2 peer(s)") {
		t.Errorf("stdout = %q, want a seeded 2 peer(s) line", got)
	}

	db, err := team.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen seeded db: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count); err != nil {
		t.Fatalf("count peers: %v", err)
	}
	if count != 2 {
		t.Errorf("peers rows = %d, want 2", count)
	}
	var paneID string
	if err := db.QueryRow("SELECT pane_id FROM peers WHERE issue = ?", 102).Scan(&paneID); err != nil {
		t.Fatalf("select peer 102: %v", err)
	}
	if paneID != "%6" {
		t.Errorf("peer 102 pane_id = %q, want %%6", paneID)
	}
}

func TestSeedTeamRegistryWarnsWhenDBUnavailable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	lg := log.NewWith(&stdout, &stderr, false)
	// A missing parent directory makes team.Open fail; seeding must only warn.
	seedTeamRegistry(lg, filepath.Join(t.TempDir(), "no-such-dir", "team.db"), state.Store{}, "100", []int{101})
	if got := stderr.String(); !strings.Contains(got, "team:") {
		t.Errorf("stderr = %q, want a team: warning", got)
	}
}
