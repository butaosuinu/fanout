package panelaunch

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestRejectActiveHerdrRowsChecksLinkedWorktrees(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "sibling", sibling, "HEAD")
	locked, err := state.Lock(state.Path(sibling))
	if err != nil {
		t.Fatal(err)
	}
	err = locked.RecordPane(state.Pane{
		Parent: "637", IssueNum: 638, Backend: backend.Herdr, PaneID: "w1:p1",
	})
	if err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}

	err = rejectActiveHerdrRows(repo)
	if err == nil || !strings.Contains(err.Error(), filepath.Clean(sibling)) {
		t.Fatalf("rejectActiveHerdrRows() error = %v", err)
	}
}

func TestRejectActiveHerdrRowsLeavesTmuxStateUnchanged(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	locked, err := state.Lock(state.Path(repo))
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.RecordPane(state.Pane{
		Parent: "637", IssueNum: 638, Backend: backend.Tmux, PaneID: "%1",
	}); err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := rejectActiveHerdrRows(repo); err != nil {
		t.Fatal(err)
	}
}

func TestRejectActiveHerdrIntentsRequiresEmptyJournal(t *testing.T) {
	journal := state.HerdrIntents{
		SchemaVersion: state.HerdrIntentsSchemaVersion,
		Intents:       []state.HerdrIntent{{ID: "pending"}},
	}
	if err := rejectActiveHerdrIntents(journal); err == nil || !strings.Contains(err.Error(), "1 active") {
		t.Fatalf("rejectActiveHerdrIntents() error = %v", err)
	}
	journal.Intents = nil
	if err := rejectActiveHerdrIntents(journal); err != nil {
		t.Fatal(err)
	}
}
