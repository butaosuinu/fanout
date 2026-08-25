package panelaunch

import (
	"context"
	"errors"
	"path/filepath"
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

func TestShutdownManagedServerStopsWaitingForLinkedScaffoldLockAtDeadline(t *testing.T) {
	repo, sibling := managedConsoleTestWorktrees(t)
	intent := managedLifecycleTestCoordinatorIntent(t, sibling)
	coordinator := managedCoordinatorPane(intent, backend.OwnedLaunchRoute{
		Session: intent.Session, SocketPath: intent.SocketPath,
	}, intent.RuntimeParent, -2)
	recordRestartStatePane(t, sibling, coordinator)
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
			return testManagedServerIdentity(), nil
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
