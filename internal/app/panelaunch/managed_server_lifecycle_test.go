package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func TestManagedShutdownScaffoldsRejectsChildRowsAcrossLinkedWorktrees(t *testing.T) {
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

	current, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = managedShutdownScaffolds(repo, current)
	if err == nil || !strings.Contains(err.Error(), filepath.Clean(sibling)) {
		t.Fatalf("managedShutdownScaffolds() error = %v", err)
	}
}

func TestManagedShutdownScaffoldsLeavesTmuxStateUnchanged(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	locked, err := state.Lock(state.Path(repo))
	if err != nil {
		t.Fatal(err)
	}
	err = locked.RecordPane(state.Pane{
		Parent: "637", IssueNum: 638, Backend: backend.Tmux, PaneID: "%1",
	})
	if err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	err = locked.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	current, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedShutdownScaffolds(repo, current); err != nil {
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

func TestShutdownManagedServerReleasesAbsentManualCleanupCoordinator(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	intent := managedLifecycleTestCoordinatorIntent(t, repo)
	intent.Status = state.IntentManualCleanupRequired
	intent.Failure = "saved coordinator identity needs manual cleanup"
	saveManagedLifecycleTestIntent(t, repo, intent)

	harness := &managedServerTestHarness{}
	if err := ShutdownManagedServer(context.Background(), repo, harness.io()); err != nil {
		t.Fatal(err)
	}
	journal, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Intents) != 0 || harness.shutdownCalls != 1 {
		t.Fatalf("shutdown result = intents:%+v calls:%d, want released and called once", journal.Intents, harness.shutdownCalls)
	}
}

func TestShutdownManagedServerRetainsUnprovenManualCleanupCoordinator(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	intent := managedLifecycleTestCoordinatorIntent(t, repo)
	intent.Status = state.IntentManualCleanupRequired
	intent.Failure = "saved coordinator identity needs manual cleanup"
	saveManagedLifecycleTestIntent(t, repo, intent)

	workspace := observationResource(intent.Resource)
	workspace.Label = "foreign-label"
	harness := &managedServerTestHarness{workspaces: []backend.WorkspaceObservation{workspace}}
	err := ShutdownManagedServer(context.Background(), repo, harness.io())
	if err == nil || !strings.Contains(err.Error(), "1 active Herdr intent rows remain") {
		t.Fatalf("ShutdownManagedServer() error = %v, want active-intent rejection", err)
	}
	assertManagedLifecycleIntentStatus(t, repo, intent.ID, state.IntentManualCleanupRequired)
	if harness.shutdownCalls != 0 {
		t.Fatalf("ShutdownManagedServer() shutdown calls = %d, want 0", harness.shutdownCalls)
	}
}

func TestShutdownManagedServerRetainsManualCleanupCoordinatorOnSnapshotFailure(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	intent := managedLifecycleTestCoordinatorIntent(t, repo)
	intent.Status = state.IntentManualCleanupRequired
	intent.Failure = "saved coordinator identity needs manual cleanup"
	saveManagedLifecycleTestIntent(t, repo, intent)

	harness := &managedServerTestHarness{observeErr: errors.New("snapshot unavailable")}
	err := ShutdownManagedServer(context.Background(), repo, harness.io())
	if err == nil || !strings.Contains(err.Error(), "1 active Herdr intent rows remain") {
		t.Fatalf("ShutdownManagedServer() error = %v, want active-intent rejection", err)
	}
	assertManagedLifecycleIntentStatus(t, repo, intent.ID, state.IntentManualCleanupRequired)
	if harness.shutdownCalls != 0 {
		t.Fatalf("ShutdownManagedServer() shutdown calls = %d, want 0", harness.shutdownCalls)
	}
}

func assertManagedLifecycleIntentStatus(
	t *testing.T,
	repo string,
	intentID string,
	want state.LaunchIntentStatus,
) {
	t.Helper()
	journal, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found || intent.Status != want {
		t.Fatalf("saved intent = (%+v,%t), want status %q", intent, found, want)
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
	resumed, created, err := ensureManagedServerIntent(journal, state.IntentRestart, ManagedServerIO{})
	if err != nil || created || resumed.ID != intent.ID {
		t.Fatalf("ensure resumed restart = (%+v, created:%t, %v)", resumed, created, err)
	}
	if err = releaseRejectedManagedRestart(journal, resumed, created, cause); !errors.Is(err, cause) {
		t.Fatalf("release resumed live restart error = %v", err)
	}
	if _, found, intentErr := journal.ServerLifecycleIntent(); intentErr != nil || !found {
		t.Fatalf("resumed live restart intent = found:%t err:%v", found, intentErr)
	}
	if _, _, err = currentManagedServerIntent(journal, state.IntentShutdown); err == nil ||
		!strings.Contains(err.Error(), "restart is pending; refusing shutdown") {
		t.Fatalf("mutation after rejected restart error = %v", err)
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

func TestShutdownManagedServerPrunesAbsentRealizedIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	intent := managedLifecycleTestCoordinatorIntent(t, repo)
	saveManagedLifecycleTestIntent(t, repo, intent)
	harness := &managedServerTestHarness{}

	if err := ShutdownManagedServer(context.Background(), repo, harness.io()); err != nil {
		t.Fatal(err)
	}
	if harness.shutdownCalls != 1 || harness.issueCalls != 1 {
		t.Fatalf("ShutdownManagedServer() calls = shutdown:%d issue:%d, want 1/1", harness.shutdownCalls, harness.issueCalls)
	}
	journal, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Intents) != 0 {
		t.Fatalf("ShutdownManagedServer() intents = %+v, want empty", journal.Intents)
	}
}

func TestShutdownManagedServerRetainsRealizedIntentWhenObservationFails(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	intent := managedLifecycleTestCoordinatorIntent(t, repo)
	saveManagedLifecycleTestIntent(t, repo, intent)
	harness := &managedServerTestHarness{observeErr: errors.New("snapshot unavailable")}

	err := ShutdownManagedServer(context.Background(), repo, harness.io())
	if err == nil || !strings.Contains(err.Error(), "1 active Herdr intent rows remain") {
		t.Fatalf("ShutdownManagedServer() error = %v, want active-intent rejection", err)
	}
	journal, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, found := journal.FindIntent(intent.ID); !found || harness.shutdownCalls != 0 {
		t.Fatalf("ShutdownManagedServer() retained = %t, shutdown calls = %d, want true/0", found, harness.shutdownCalls)
	}
}

func TestShutdownManagedServerRetainsAllRealizedIntentsWhenOneResourceRemains(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	coordinator := realizeTestManagedCoordinator(t, repo, runtime, hooks)
	req := testManagedWorktreeRequest(repo, "retained-child", 712)
	child, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	harness := &managedServerTestHarness{}

	err = ShutdownManagedServer(context.Background(), repo, harness.io())
	if err == nil || !strings.Contains(err.Error(), "2 active Herdr intent rows remain") {
		t.Fatalf("ShutdownManagedServer() error = %v, want two-intent rejection", err)
	}
	journal, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for _, intentID := range []string{coordinator.ID, child.Intent.ID} {
		if _, found := journal.FindIntent(intentID); !found {
			t.Fatalf("ShutdownManagedServer() removed retained intent %s", intentID)
		}
	}
	if harness.shutdownCalls != 0 {
		t.Fatalf("ShutdownManagedServer() shutdown calls = %d, want 0", harness.shutdownCalls)
	}
}

func TestShutdownManagedServerRetainsRealizedIntentOnWorkspaceIdentityMismatch(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	intent := managedLifecycleTestCoordinatorIntent(t, repo)
	saveManagedLifecycleTestIntent(t, repo, intent)
	harness := &managedServerTestHarness{workspaces: []backend.WorkspaceObservation{{
		WorkspaceID: intent.Resource.WorkspaceID,
		Label:       "foreign-label",
	}}}

	err := ShutdownManagedServer(context.Background(), repo, harness.io())
	if err == nil || !strings.Contains(err.Error(), "1 active Herdr intent rows remain") {
		t.Fatalf("ShutdownManagedServer() error = %v, want identity-mismatch rejection", err)
	}
	journal, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, found := journal.FindIntent(intent.ID); !found || harness.shutdownCalls != 0 {
		t.Fatalf("ShutdownManagedServer() retained = %t, shutdown calls = %d, want true/0", found, harness.shutdownCalls)
	}
}

func TestShutdownManagedServerRetainsCreatedBranchWhenStateRowRejects(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	req := testManagedWorktreeRequest(repo, "state-blocked-child", 713)
	child, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	runtime.workspaces = nil
	gitCmdTest(t, repo, "worktree", "remove", req.WorktreePath)
	recordRestartStatePane(t, repo, state.Pane{
		Parent: "713", IssueNum: 714, Backend: backend.Herdr, PaneID: "workspace-child:p1",
	})

	err = ShutdownManagedServer(context.Background(), repo, (&managedServerTestHarness{}).io())
	if err == nil || !strings.Contains(err.Error(), "active Herdr state row remains") {
		t.Fatalf("ShutdownManagedServer() error = %v, want state-row rejection", err)
	}
	journal, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, found := journal.FindIntent(child.Intent.ID); !found {
		t.Fatal("ShutdownManagedServer() removed child intent before state-row preflight")
	}
	_, branchFound, branchErr := worktree.ObserveBranch(
		context.Background(), repo, child.Intent.FullBranchRef,
	)
	if branchErr != nil {
		t.Fatal(branchErr)
	}
	if !branchFound {
		t.Fatal("ShutdownManagedServer() deleted child branch before state-row preflight")
	}
}

func TestShutdownManagedServerDiscardsUnconsumedEnvironmentBeforeIntentRelease(t *testing.T) {
	tests := []struct {
		name         string
		discardErr   error
		wantIntent   bool
		wantShutdown int
	}{
		{name: "discard succeeds", wantShutdown: 1},
		{name: "discard fails", discardErr: errors.New("discard unavailable"), wantIntent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			intent := managedLifecycleTestCoordinatorIntent(t, repo)
			intent.Launch = validTestManagedLaunch()
			saveManagedLifecycleTestIntent(t, repo, intent)
			harness := &managedServerTestHarness{discardErr: test.discardErr}

			err := ShutdownManagedServer(context.Background(), repo, harness.io())
			if test.discardErr == nil && err != nil {
				t.Fatal(err)
			}
			if test.discardErr != nil && !errors.Is(err, test.discardErr) {
				t.Fatalf("ShutdownManagedServer() error = %v, want %v", err, test.discardErr)
			}
			journal, loadErr := state.LoadLaunchJournal(repo)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			_, found := journal.FindIntent(intent.ID)
			if found != test.wantIntent {
				t.Fatalf("ShutdownManagedServer() intent found = %t, want %t", found, test.wantIntent)
			}
			identity := testManagedServerIdentity()
			if harness.discardCalls != 1 || harness.discardRuntimeDir != identity.RuntimeDir ||
				harness.discardLaunch == nil || harness.discardLaunch.Nonce != intent.Launch.Nonce {
				t.Fatalf("ShutdownManagedServer() discard = calls:%d runtime:%q launch:%+v", harness.discardCalls, harness.discardRuntimeDir, harness.discardLaunch)
			}
			if harness.shutdownCalls != test.wantShutdown {
				t.Fatalf("ShutdownManagedServer() shutdown calls = %d, want %d", harness.shutdownCalls, test.wantShutdown)
			}
		})
	}
}

func TestShutdownManagedServerRetiresConsoleAndCoordinatorScaffolds(t *testing.T) {
	repo, sibling := managedConsoleTestWorktrees(t)
	console := managedConsoleTestPane(repo, "workspace-console", "pane-console")
	coordinatorIntent := managedLifecycleTestCoordinatorIntent(t, sibling)
	coordinator := managedCoordinatorPane(coordinatorIntent, backend.OwnedLaunchRoute{
		Session: coordinatorIntent.Session, SocketPath: coordinatorIntent.SocketPath,
	}, coordinatorIntent.RuntimeParent, -2)
	recordRestartStatePane(t, repo, console)
	recordRestartStatePane(t, sibling, coordinator)
	harness := &managedServerTestHarness{}

	if err := ShutdownManagedServer(context.Background(), repo, harness.io()); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{repo, sibling} {
		store, err := state.LoadProject(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(store.Panes) != 0 {
			t.Fatalf("ShutdownManagedServer(%s) panes = %+v, want empty", root, store.Panes)
		}
	}
}

func TestShutdownManagedServerRetiresManualCoordinatorScaffold(t *testing.T) {
	repo, _ := managedConsoleTestWorktrees(t)
	intent := managedLifecycleTestCoordinatorIntent(t, repo)
	coordinator := managedCoordinatorPane(intent, backend.OwnedLaunchRoute{
		Session: intent.Session, SocketPath: intent.SocketPath,
	}, ManualParentRef, -2)
	recordRestartStatePane(t, repo, coordinator)
	harness := &managedServerTestHarness{}

	if err := ShutdownManagedServer(context.Background(), repo, harness.io()); err != nil {
		t.Fatal(err)
	}
	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 0 || harness.shutdownCalls != 1 {
		t.Fatalf(
			"ShutdownManagedServer() panes = %+v, shutdown calls = %d, want empty/1",
			store.Panes, harness.shutdownCalls,
		)
	}
}

func TestShutdownManagedServerRejectsManualNonCoordinatorRow(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		agent string
	}{
		{name: "agent", agent: "codex"},
		{name: "shell", kind: state.PaneKindShell, agent: state.PaneKindShell},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, _ := managedConsoleTestWorktrees(t)
			intent := managedLifecycleTestCoordinatorIntent(t, repo)
			pane := managedCoordinatorPane(intent, backend.OwnedLaunchRoute{
				Session: intent.Session, SocketPath: intent.SocketPath,
			}, ManualParentRef, -2)
			pane.Kind = test.kind
			pane.Agent = test.agent
			pane.Slug = "manual-" + test.name
			pane.WorkspaceLabel = "fanout-" + test.name + "-token"
			recordRestartStatePane(t, repo, pane)
			harness := &managedServerTestHarness{}

			err := ShutdownManagedServer(context.Background(), repo, harness.io())
			if err == nil || !strings.Contains(err.Error(), "active Herdr state row remains") {
				t.Fatalf("ShutdownManagedServer() error = %v, want state-row rejection", err)
			}
			store, loadErr := state.LoadProject(repo)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if len(store.Panes) != 1 || harness.shutdownCalls != 0 {
				t.Fatalf(
					"ShutdownManagedServer() panes = %+v, shutdown calls = %d, want retained/0",
					store.Panes, harness.shutdownCalls,
				)
			}
		})
	}
}

func TestShutdownManagedServerRetiresAbsentManualShells(t *testing.T) {
	for _, count := range []int{1, 3} {
		for _, fromLinked := range []bool{false, true} {
			t.Run(fmt.Sprintf("count=%d/linked=%t", count, fromLinked), func(t *testing.T) {
				repo, sibling := managedConsoleTestWorktrees(t)
				cwd := t.TempDir()
				file := filepath.Join(cwd, "keep.txt")
				if err := os.WriteFile(file, []byte("keep shell files\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				for i := range count {
					root, path := repo, repo
					if i%2 == 0 {
						root, path = sibling, cwd
					}
					pane, _ := managedLifecycleTestShell(t, root, path, -3-i)
					recordRestartStatePane(t, root, pane)
				}
				if count > 1 {
					recordRestartStatePane(t, repo, managedConsoleTestPane(repo, "console", "console-pane"))
					intent := managedLifecycleTestCoordinatorIntent(t, sibling)
					recordRestartStatePane(t, sibling, managedCoordinatorPane(intent, backend.OwnedLaunchRoute{
						Session: intent.Session, SocketPath: intent.SocketPath,
					}, intent.RuntimeParent, -2))
				}
				caller := repo
				if fromLinked {
					caller = sibling
				}
				harness := &managedServerTestHarness{}
				if err := ShutdownManagedServer(context.Background(), caller, harness.io()); err != nil {
					t.Fatal(err)
				}
				for _, root := range []string{repo, sibling} {
					assertManagedShutdownPanes(t, root, nil)
				}
				journal, err := state.LoadLaunchJournal(repo)
				if err != nil || len(journal.Intents) != 0 || harness.issueCalls != 1 {
					t.Fatalf("shutdown = journal:%+v, err:%v, signals:%d", journal, err, harness.issueCalls)
				}
				contents, err := os.ReadFile(file)
				if err != nil || string(contents) != "keep shell files\n" {
					t.Fatalf("shell files changed: %q, %v", contents, err)
				}
			})
		}
	}
}

func TestShutdownManagedServerRejectsChangedShellIdentity(t *testing.T) {
	changes := map[string]func(*state.Pane){
		"child":           func(p *state.Pane) { p.Parent = "808" },
		"issue":           func(p *state.Pane) { p.IssueNum = 808 },
		"task":            func(p *state.Pane) { p.TaskID = "task" },
		"runtime parent":  func(p *state.Pane) { p.RuntimeParent = "808" },
		"kind":            func(p *state.Pane) { p.Kind = "" },
		"manual agent":    func(p *state.Pane) { p.Agent = "codex" },
		"branch":          func(p *state.Pane) { p.BranchName = "fanout/child" },
		"repo key":        func(p *state.Pane) { p.RepoKey = "/repo/.git" },
		"repo root":       func(p *state.Pane) { p.RepoRoot = "/repo" },
		"agent ID":        func(p *state.Pane) { p.AgentID = "agent" },
		"agent session":   func(p *state.Pane) { p.AgentSession = &backend.AgentSessionRef{} },
		"attached":        func(p *state.Pane) { p.SourceParent = "808" },
		"source issue":    func(p *state.Pane) { p.SourceIssueNum = 808 },
		"source task":     func(p *state.Pane) { p.SourceTaskID = "task" },
		"pane":            func(p *state.Pane) { p.PaneID = "" },
		"workspace":       func(p *state.Pane) { p.WorkspaceID = "" },
		"label":           func(p *state.Pane) { p.WorkspaceLabel = "" },
		"terminal":        func(p *state.Pane) { p.TerminalID = "" },
		"session":         func(p *state.Pane) { p.SessionID = "foreign-session" },
		"socket":          func(p *state.Pane) { p.SocketPath = "/foreign.sock" },
		"missing session": func(p *state.Pane) { p.SessionID = "" },
		"missing socket":  func(p *state.Pane) { p.SocketPath = "" },
		"cwd":             func(p *state.Pane) { p.WorktreePath = "" },
	}
	for name, change := range changes {
		for _, afterSnapshot := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after-snapshot=%t", name, afterSnapshot), func(t *testing.T) {
				repo, sibling := managedConsoleTestWorktrees(t)
				pane, _ := managedLifecycleTestShell(t, sibling, sibling, -2)
				changed := pane
				change(&changed)
				harness := &managedServerTestHarness{}
				io := harness.io()
				if afterSnapshot {
					recordRestartStatePane(t, sibling, pane)
					io.ObserveWorkspaces = func(context.Context) ([]backend.WorkspaceObservation, error) {
						owner, err := state.LockProject(sibling)
						if err != nil {
							return nil, err
						}
						owner.Panes = []state.Pane{changed}
						return nil, errors.Join(owner.Save(), owner.Unlock())
					}
				} else {
					recordRestartStatePane(t, sibling, changed)
				}
				if err := ShutdownManagedServer(context.Background(), repo, io); err == nil {
					t.Fatal("shutdown accepted a changed shell identity")
				}
				assertManagedShutdownPanes(t, sibling, []state.Pane{changed})
				if harness.shutdownCalls != 0 {
					t.Fatal("shutdown called before row validation")
				}
			})
		}
	}
}

func TestShutdownManagedServerKeepsShellOnFailedPreflight(t *testing.T) {
	for _, reason := range []string{"live shell", "foreign workspace", "snapshot", "inspect", "cancel", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			repo, _ := managedConsoleTestWorktrees(t)
			pane, intent := managedLifecycleTestShell(t, repo, repo, -2)
			recordRestartStatePane(t, repo, pane)
			harness := &managedServerTestHarness{}
			io := harness.io()
			ctx := context.Background()
			switch reason {
			case "live shell":
				harness.workspaces = []backend.WorkspaceObservation{observationResource(intent.Resource)}
			case "foreign workspace":
				harness.workspaces = []backend.WorkspaceObservation{{WorkspaceID: "foreign"}}
			case "snapshot":
				harness.observeErr = errors.New("snapshot unavailable")
			case "inspect":
				io.InspectServer = func() (state.RuntimeServerIdentity, error) {
					return state.RuntimeServerIdentity{}, errors.New("owner generation mismatch")
				}
			case "cancel", "deadline":
				var cancel context.CancelFunc
				if reason == "cancel" {
					ctx, cancel = context.WithCancel(context.Background())
				} else {
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				}
				cancel()
			}
			if err := ShutdownManagedServer(ctx, repo, io); err == nil {
				t.Fatal("shutdown accepted failed preflight")
			}
			assertManagedShutdownPanes(t, repo, []state.Pane{pane})
			if harness.shutdownCalls != 0 {
				t.Fatal("shutdown called before preflight")
			}
		})
	}
}

func TestShutdownManagedServerPreservesShellIntentReleaseConditions(t *testing.T) {
	for _, status := range []state.LaunchIntentStatus{state.IntentRealized, state.IntentPlanned, state.IntentIssued, state.IntentManualCleanupRequired} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired=%t", status, expired), func(t *testing.T) {
				repo, _ := managedConsoleTestWorktrees(t)
				pane, intent := managedLifecycleTestShell(t, repo, repo, -2)
				intent.Status = status
				if status == state.IntentManualCleanupRequired {
					intent.Failure = "workspace creation response lost"
				}
				if expired {
					intent.ExpiresUnixMS = time.Now().Add(-time.Minute).UnixMilli()
				}
				// No saved resource proof makes manual cleanup ambiguous too.
				if status != state.IntentRealized {
					intent.Resource = state.RuntimeResource{}
				}
				recordRestartStatePane(t, repo, pane)
				saveManagedLifecycleTestIntent(t, repo, intent)
				harness := &managedServerTestHarness{}
				err := ShutdownManagedServer(context.Background(), repo, harness.io())
				if status == state.IntentRealized {
					if err != nil || harness.shutdownCalls != 1 {
						t.Fatalf("proven absent shell shutdown = %v, calls:%d", err, harness.shutdownCalls)
					}
					assertManagedShutdownPanes(t, repo, nil)
					return
				}
				if err == nil || harness.shutdownCalls != 0 {
					t.Fatalf("ambiguous shell shutdown = %v, calls:%d", err, harness.shutdownCalls)
				}
				assertManagedShutdownPanes(t, repo, []state.Pane{pane})
				assertManagedLifecycleIntentStatus(t, repo, intent.ID, status)
			})
		}
	}
}

func managedLifecycleTestShell(t *testing.T, root, cwd string, number int) (state.Pane, state.LaunchIntent) {
	t.Helper()
	intent := managedLifecycleTestCoordinatorIntent(t, cwd)
	id, err := state.CoordinatorIntentID(ManualParentRef, root, number)
	if err != nil {
		t.Fatal(err)
	}
	intent.ID, intent.Parent, intent.RuntimeParent = id, ManualParentRef, ManualParentRef
	intent.OwnerProjectRoot, intent.IssueNum = root, number
	intent.WorkspaceLabel = fmt.Sprintf("fanout-manual-%d", -number)
	intent.Resource.WorkspaceID = fmt.Sprintf("workspace-%d", -number)
	intent.Resource.Label = intent.WorkspaceLabel
	intent.Resource.PaneID = fmt.Sprintf("pane-%d", -number)
	intent.Resource.TerminalID = fmt.Sprintf("terminal-%d", -number)
	live := backend.LivePane{
		Ref:            backend.PaneRef{Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID},
		WorkspaceLabel: intent.WorkspaceLabel, TerminalID: intent.Resource.TerminalID,
		CurrentPath: cwd, SessionID: intent.Session, SocketPath: intent.SocketPath,
	}
	return managedShellStatePane(intent, live, number, "manual-shell", "shell", ""), intent
}

func TestShutdownManagedServerShellSaveFailuresRemainRecoverable(t *testing.T) {
	for _, stage := range []string{"release intent", "save state", "save shutdown intent"} {
		t.Run(stage, func(t *testing.T) {
			repo, sibling := managedConsoleTestWorktrees(t)
			pane, intent := managedLifecycleTestShell(t, sibling, sibling, -2)
			recordRestartStatePane(t, sibling, pane)
			journalPath, err := state.LaunchJournalPath(repo)
			if err != nil {
				t.Fatal(err)
			}
			if stage != "save shutdown intent" {
				saveManagedLifecycleTestIntent(t, repo, intent)
			}
			blockedDir := filepath.Dir(journalPath)
			if stage == "save state" {
				blockedDir = filepath.Dir(state.Path(sibling))
			}
			t.Cleanup(func() {
				if chmodErr := os.Chmod(blockedDir, 0o700); chmodErr != nil {
					t.Error(chmodErr)
				}
			})
			harness := &managedServerTestHarness{}
			io := harness.io()
			io.ObserveWorkspaces = func(context.Context) ([]backend.WorkspaceObservation, error) {
				return nil, os.Chmod(blockedDir, 0o500)
			}
			if err = ShutdownManagedServer(context.Background(), repo, io); err == nil || harness.shutdownCalls != 0 {
				t.Fatalf("save failure = %v, shutdown calls:%d", err, harness.shutdownCalls)
			}
			if err = os.Chmod(blockedDir, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := state.LoadLaunchJournal(repo)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "release intent" {
				assertManagedLifecycleIntentStatus(t, repo, intent.ID, state.IntentRealized)
			} else if len(journal.Intents) != 0 {
				t.Fatalf("intent release did not precede state retirement: %+v", journal.Intents)
			}
			if stage == "save shutdown intent" {
				assertManagedShutdownPanes(t, sibling, nil)
			} else {
				assertManagedShutdownPanes(t, sibling, []state.Pane{pane})
			}
			if err = ShutdownManagedServer(context.Background(), repo, harness.io()); err != nil {
				t.Fatal(err)
			}
			assertManagedShutdownPanes(t, sibling, nil)
			if harness.issueCalls != 1 {
				t.Fatalf("recovery signals = %d, want 1", harness.issueCalls)
			}
		})
	}
}

func TestShutdownManagedServerShellRetryDoesNotReissueOrRetireUnrelatedRows(t *testing.T) {
	repo, _ := managedConsoleTestWorktrees(t)
	pane, _ := managedLifecycleTestShell(t, repo, repo, -2)
	recordRestartStatePane(t, repo, pane)
	harness := &managedServerTestHarness{}
	io := harness.io()
	shutdown := io.ShutdownServer
	lost := errors.New("shutdown response lost")
	io.ShutdownServer = func(ctx context.Context, identity state.RuntimeServerIdentity, markIssued func() error) error {
		return errors.Join(shutdown(ctx, identity, markIssued), lost)
	}
	if err := ShutdownManagedServer(context.Background(), repo, io); !errors.Is(err, lost) {
		t.Fatalf("first shutdown error = %v", err)
	}
	assertManagedShutdownPanes(t, repo, nil)
	id, err := state.ServerIntentID(state.IntentShutdown)
	if err != nil {
		t.Fatal(err)
	}
	assertManagedLifecycleIntentStatus(t, repo, id, state.IntentIssued)
	// Even a row written outside normal launch admission cannot join the saved
	// shutdown transaction on cancellation, retry, or completion replay.
	pane.Agent = "codex"
	recordRestartStatePane(t, repo, pane)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = ShutdownManagedServer(ctx, repo, io); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retry = %v", err)
	}
	if err = ShutdownManagedServer(context.Background(), repo, io); !errors.Is(err, lost) {
		t.Fatalf("unresolved retry = %v", err)
	}
	if err = ShutdownManagedServer(context.Background(), repo, harness.io()); err != nil {
		t.Fatal(err)
	}
	if err = ShutdownManagedServer(context.Background(), repo, harness.io()); err == nil {
		t.Fatal("completion replay accepted an unrelated row")
	}
	assertManagedShutdownPanes(t, repo, []state.Pane{pane})
	if harness.issueCalls != 1 {
		t.Fatalf("shutdown signals = %d, want 1", harness.issueCalls)
	}
}

func assertManagedShutdownPanes(t *testing.T, root string, expected []state.Pane) {
	t.Helper()
	store, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != len(expected) || len(expected) != 0 && !reflect.DeepEqual(store.Panes, expected) {
		t.Fatalf("saved panes = %+v, want %+v", store.Panes, expected)
	}
}

func TestShutdownManagedServerStopsWaitingForLinkedScaffoldLockAtDeadline(t *testing.T) {
	repo, sibling := managedConsoleTestWorktrees(t)
	pane, _ := managedLifecycleTestShell(t, sibling, sibling, -2)
	recordRestartStatePane(t, sibling, pane)
	owner, err := state.LockProject(sibling)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := owner.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	harness := &managedServerTestHarness{}
	err = ShutdownManagedServer(ctx, repo, harness.io())
	if !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "lock linked Herdr state in "+sibling) {
		t.Fatalf("ShutdownManagedServer() error = %v, want context deadline", err)
	}
	if harness.shutdownCalls != 0 {
		t.Fatalf("ShutdownManagedServer() shutdown calls = %d, want 0", harness.shutdownCalls)
	}
	assertManagedShutdownPanes(t, sibling, []state.Pane{pane})
}

type managedServerTestHarness struct {
	workspaces        []backend.WorkspaceObservation
	observeErr        error
	discardErr        error
	discardCalls      int
	discardRuntimeDir string
	discardLaunch     *state.LaunchCapsule
	shutdownCalls     int
	issueCalls        int
}

func (h *managedServerTestHarness) io() ManagedServerIO {
	return ManagedServerIO{
		InspectServer: func() (state.RuntimeServerIdentity, error) {
			identity := testManagedServerIdentity()
			identity.SocketPath = "/tmp/fanout-owned.sock"
			return identity, nil
		},
		ObserveWorkspaces: func(context.Context) ([]backend.WorkspaceObservation, error) {
			return h.workspaces, h.observeErr
		},
		DiscardEnvironment: func(runtimeDir string, launch *state.LaunchCapsule) error {
			h.discardCalls++
			h.discardRuntimeDir = runtimeDir
			h.discardLaunch = launch
			return h.discardErr
		},
		ShutdownServer: func(_ context.Context, _ state.RuntimeServerIdentity, markIssued func() error) error {
			if markIssued != nil {
				h.issueCalls++
				if err := markIssued(); err != nil {
					return err
				}
			}
			h.shutdownCalls++
			return nil
		},
	}
}

func managedLifecycleTestCoordinatorIntent(t *testing.T, root string) state.LaunchIntent {
	t.Helper()
	id, err := state.CoordinatorIntentID("425", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	label := "fanout-coordinator-shutdown"
	return state.LaunchIntent{
		ID: id, Kind: state.IntentCoordinator, Status: state.IntentRealized,
		Parent: "425", RuntimeParent: "425", WorktreePath: root, WorkspaceLabel: label,
		Resource: state.RuntimeResource{
			WorkspaceID: "workspace-shutdown", Label: label, PaneID: "pane-shutdown",
			TerminalID: "terminal-shutdown", CurrentPath: root,
		},
		Session: "fanout-owned", SocketPath: "/tmp/fanout-owned.sock",
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(),
	}
}

func saveManagedLifecycleTestIntent(t *testing.T, repo string, intent state.LaunchIntent) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(repo)
	if err == nil {
		journal.UpsertIntent(intent)
		err = journal.Save()
	}
	err = errors.Join(err, locked.Unlock())
	if err != nil {
		t.Fatal(err)
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
