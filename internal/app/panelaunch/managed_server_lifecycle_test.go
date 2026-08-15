package panelaunch

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestRejectActiveManagedRowsChecksLinkedWorktrees(t *testing.T) {
	repo := newManagedRealizeRepo(t)
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

	err = rejectActiveManagedRows(repo)
	if err == nil || !strings.Contains(err.Error(), filepath.Clean(sibling)) {
		t.Fatalf("rejectActiveManagedRows() error = %v", err)
	}
}

func TestRejectActiveManagedRowsLeavesTmuxStateUnchanged(t *testing.T) {
	repo := newManagedRealizeRepo(t)
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
	if err := rejectActiveManagedRows(repo); err != nil {
		t.Fatal(err)
	}
}

func TestRejectActiveManagedIntentsRequiresEmptyJournal(t *testing.T) {
	journal := state.LaunchJournal{
		SchemaVersion: state.LaunchJournalSchemaVersion,
		Intents:       []state.LaunchIntent{{ID: "pending"}},
	}
	if err := rejectActiveManagedIntents(journal); err == nil || !strings.Contains(err.Error(), "1 active") {
		t.Fatalf("rejectActiveManagedIntents() error = %v", err)
	}
	journal.Intents = nil
	if err := rejectActiveManagedIntents(journal); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseRejectedManagedRestartDropsOnlyFreshLiveIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err := locked.LaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newManagedServerIntent(state.IntentRestart, testManagedServerIdentity())
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err = journal.Save(); err != nil {
		t.Fatal(err)
	}
	cause := backend.ErrOwnedGenerationStillLive
	if err = releaseRejectedManagedRestart(journal, intent, true, cause); !errors.Is(err, cause) {
		t.Fatalf("release fresh live restart error = %v", err)
	}
	if _, found, intentErr := journal.ServerLifecycleIntent(); intentErr != nil || found {
		t.Fatalf("fresh live restart intent remains: found=%t err=%v", found, intentErr)
	}

	journal.UpsertIntent(intent)
	if err = journal.Save(); err != nil {
		t.Fatal(err)
	}
	if err = releaseRejectedManagedRestart(journal, intent, false, cause); !errors.Is(err, cause) {
		t.Fatalf("release resumed live restart error = %v", err)
	}
	if _, found, err := journal.ServerLifecycleIntent(); err != nil || !found {
		t.Fatalf("resumed live restart intent = found:%t err:%v", found, err)
	}
}

func TestMarkPlannedManagedReopenCleanupManual(t *testing.T) {
	journal := &state.LockedLaunchJournal{LaunchJournal: state.LaunchJournal{
		SchemaVersion: state.LaunchJournalSchemaVersion,
		Intents: []state.LaunchIntent{
			{Kind: state.IntentCleanup, CleanupPhase: state.CleanupReopen, Status: state.IntentPlanned},
			{Kind: state.IntentCleanup, CleanupPhase: state.CleanupReopen, Status: state.IntentIssued},
			{Kind: state.IntentCleanup, CleanupPhase: state.CleanupRemove, Status: state.IntentPlanned},
			{Kind: state.IntentRestart, Status: state.IntentPlanned},
		},
	}}

	markPlannedManagedReopenCleanupManual(journal)

	got := journal.Intents[0]
	if got.Status != state.IntentManualCleanupRequired ||
		!strings.Contains(got.Failure, "invalidated the saved cleanup coordinator identity") {
		t.Fatalf("planned reopen cleanup = %+v", got)
	}
	wantStatuses := []state.LaunchIntentStatus{
		state.IntentIssued, state.IntentPlanned, state.IntentPlanned,
	}
	for i, want := range wantStatuses {
		if got := journal.Intents[i+1].Status; got != want {
			t.Fatalf("unaffected intent %d status = %q, want %q", i+1, got, want)
		}
	}
}

func TestManagedShutdownIssueCallbackPersistsOnlyWhenInvokedAndDoesNotReissue(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(repo)
	if err != nil {
		_ = locked.Unlock() // The journal error is authoritative.
		t.Fatal(err)
	}
	intent, err := newManagedServerIntent(state.IntentShutdown, testManagedServerIdentity())
	if err != nil {
		_ = locked.Unlock() // The intent construction error is authoritative.
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err = journal.Save(); err != nil {
		_ = locked.Unlock() // The journal save error is authoritative.
		t.Fatal(err)
	}
	markIssued, err := managedShutdownIssueCallback(journal, intent)
	if err != nil || markIssued == nil || intent.Status != state.IntentPlanned {
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

	stored, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found, err := stored.ServerLifecycleIntent()
	if err != nil || !found || intent.Status != state.IntentIssued {
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
	journal, err = locked.LaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	markIssued, err = managedShutdownIssueCallback(journal, intent)
	if err != nil || markIssued != nil {
		t.Fatalf("issued retry callback = (%t, %v), want nil", markIssued != nil, err)
	}
}

func testManagedServerIdentity() state.RuntimeServerIdentity {
	return state.RuntimeServerIdentity{
		GitCommonDir: "/repo/.git", RuntimeDir: "/tmp/fanout-herdr", Session: "fanout-owned",
		SocketPath: "/tmp/fanout-herdr/herdr.sock", ClientSocketPath: "/tmp/fanout-herdr/herdr-client.sock",
		OwnerNonce: strings.Repeat("a", 64), SupervisorPID: 42,
		SupervisorStartToken: strings.Repeat("b", 64), ServerPID: 43,
		BinaryPath: "/usr/local/bin/herdr", BinarySHA256: strings.Repeat("c", 64), BinaryVersion: "0.7.5",
		LauncherPath: "/usr/local/bin/fanout", LauncherSHA256: strings.Repeat("d", 64),
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
