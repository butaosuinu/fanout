package panelaunch

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
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

func TestReleaseRejectedHerdrRestartDropsOnlyFreshLiveIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newHerdrServerIntent(state.HerdrIntentRestart, testHerdrServerIdentity())
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err = journal.Save(); err != nil {
		t.Fatal(err)
	}
	cause := herdrrun.ErrOwnedGenerationStillLive
	if err = releaseRejectedHerdrRestart(journal, intent, true, cause); !errors.Is(err, cause) {
		t.Fatalf("release fresh live restart error = %v", err)
	}
	if _, found, intentErr := journal.ServerLifecycleIntent(); intentErr != nil || found {
		t.Fatalf("fresh live restart intent remains: found=%t err=%v", found, intentErr)
	}

	journal.UpsertIntent(intent)
	if err = journal.Save(); err != nil {
		t.Fatal(err)
	}
	if err = releaseRejectedHerdrRestart(journal, intent, false, cause); !errors.Is(err, cause) {
		t.Fatalf("release resumed live restart error = %v", err)
	}
	if _, found, err := journal.ServerLifecycleIntent(); err != nil || !found {
		t.Fatalf("resumed live restart intent = found:%t err:%v", found, err)
	}
}

func TestHerdrShutdownIssueCallbackPersistsOnlyWhenInvokedAndDoesNotReissue(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		_ = locked.Unlock() // The journal error is authoritative.
		t.Fatal(err)
	}
	intent, err := newHerdrServerIntent(state.HerdrIntentShutdown, testHerdrServerIdentity())
	if err != nil {
		_ = locked.Unlock() // The intent construction error is authoritative.
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err = journal.Save(); err != nil {
		_ = locked.Unlock() // The journal save error is authoritative.
		t.Fatal(err)
	}
	markIssued, err := herdrShutdownIssueCallback(journal, intent)
	if err != nil || markIssued == nil || intent.Status != state.HerdrIntentPlanned {
		_ = locked.Unlock() // The callback assertion below is authoritative.
		t.Fatalf("planned callback = (%+v, %t, %v)", intent, markIssued != nil, err)
	}
	if err = markIssued(); err != nil {
		_ = locked.Unlock() // The issued-state save error is authoritative.
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}

	stored, err := state.LoadHerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found, err := stored.ServerLifecycleIntent()
	if err != nil || !found || intent.Status != state.HerdrIntentIssued {
		t.Fatalf("stored issued shutdown = (%+v, %t, %v)", intent, found, err)
	}
	locked, err = state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err = locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	markIssued, err = herdrShutdownIssueCallback(journal, intent)
	if err != nil || markIssued != nil {
		t.Fatalf("issued retry callback = (%t, %v), want nil", markIssued != nil, err)
	}
}

func testHerdrServerIdentity() state.HerdrServerIdentity {
	return state.HerdrServerIdentity{
		GitCommonDir: "/repo/.git", RuntimeDir: "/tmp/fanout-herdr", Session: "fanout-owned",
		SocketPath: "/tmp/fanout-herdr/herdr.sock", ClientSocketPath: "/tmp/fanout-herdr/herdr-client.sock",
		OwnerNonce: strings.Repeat("a", 64), SupervisorPID: 42,
		SupervisorStartToken: strings.Repeat("b", 64), ServerPID: 43,
		BinaryPath: "/usr/local/bin/herdr", BinarySHA256: strings.Repeat("c", 64), BinaryVersion: "0.7.5",
		LauncherPath: "/usr/local/bin/fanout", LauncherSHA256: strings.Repeat("d", 64),
	}
}

func TestStaleHerdrRowsAfterRestartUpdatesLinkedState(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "sibling", sibling, "HEAD")
	current := restartStatePane("w1", "p1", "old-1")
	linked := restartStatePane("w2", "p2", "old-2")
	recordRestartStatePane(t, repo, current)
	recordRestartStatePane(t, sibling, linked)

	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	live := []backend.LivePane{restartLivePane(current, "new-1"), restartLivePane(linked, "new-2")}
	if err = staleHerdrRowsAfterRestart(context.Background(), repo, locked, live); err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err = locked.Save(); err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	assertRestartStatePaneStale(t, repo, current)
	assertRestartStatePaneStale(t, sibling, linked)
}

func TestStaleHerdrRowsAfterRestartRejectsUnchangedTerminal(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	pane := restartStatePane("w1", "p1", "old-1")
	recordRestartStatePane(t, repo, pane)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	err = staleHerdrRowsAfterRestart(
		context.Background(), repo, locked, []backend.LivePane{restartLivePane(pane, pane.HerdrTerminalID)},
	)
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	if err == nil || !strings.Contains(err.Error(), "not stale") {
		t.Fatalf("staleHerdrRowsAfterRestart() error = %v", err)
	}
	loaded, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	got, found := loaded.Find(pane.Parent, pane.IssueNum)
	if !found || got.ReportedState != pane.ReportedState || got.EmitterNonce != pane.EmitterNonce {
		t.Fatalf("rejected row changed = %+v (found=%t)", got, found)
	}
}

func restartStatePane(workspaceID, paneID, terminalID string) state.Pane {
	return state.Pane{
		Parent: "637", IssueNum: len(workspaceID + paneID), Backend: backend.Herdr,
		PaneID: workspaceID + ":" + paneID, HerdrWorkspaceID: workspaceID,
		HerdrTerminalID: terminalID, HerdrSession: "fanout-owned",
		HerdrSocketPath: "/runtime/herdr.sock", ReportedState: "working",
		StateRefinement: true, EmitterNonce: strings.Repeat("b", 32),
	}
}

func restartLivePane(saved state.Pane, terminalID string) backend.LivePane {
	return backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: saved.HerdrWorkspaceID, Pane: saved.PaneID,
		},
		TerminalID: terminalID, SessionID: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
	}
}

func recordRestartStatePane(t *testing.T, root string, pane state.Pane) {
	t.Helper()
	locked, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(pane); err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func assertRestartStatePaneStale(t *testing.T, root string, before state.Pane) {
	t.Helper()
	loaded, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	got, found := loaded.Find(before.Parent, before.IssueNum)
	if !found || got.ReportedState != "" || got.StateRefinement ||
		got.EmitterNonce == before.EmitterNonce || !telemetry.ValidNonce(got.EmitterNonce) {
		t.Fatalf("stale restart row = %+v (found=%t)", got, found)
	}
}
