package lifecycle

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

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/backendtest"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type fakeCoordinatorCloser struct {
	backend.Backend
	runtime *fakeHerdrLifecycleRuntime
	target  backend.OwnedPaneIdentity
}

func (f *fakeHerdrLifecycleRuntime) BindOwnedCoordinatorClose(intent state.LaunchIntent) (backend.OwnedClosingBackend, error) {
	target := backend.OwnedPaneIdentity{
		Ref:       backend.PaneRef{Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID},
		SessionID: intent.Session, SocketPath: intent.SocketPath, WorkspaceLabel: intent.Resource.Label,
		TerminalID: intent.Resource.TerminalID, CurrentPath: intent.WorktreePath,
	}
	return fakeCoordinatorCloser{Backend: backendtest.New(), runtime: f, target: target}, nil
}

func (f fakeCoordinatorCloser) CloseOwned(req backend.CloseRequest) (backend.CloseResult, error) {
	failed := backend.CloseResult{Status: backend.CloseFailed}
	if req != (backend.CloseRequest{Ref: backend.PaneRef{Backend: f.target.Ref.Backend, Pane: f.target.Ref.Pane}}) {
		return failed, backend.ErrOwnedIdentityMismatch
	}
	resource := state.RuntimeResource{
		WorkspaceID: f.target.Ref.Workspace, Label: f.target.WorkspaceLabel,
		PaneID: f.target.Ref.Pane, TerminalID: f.target.TerminalID, CurrentPath: f.target.CurrentPath,
	}
	workspaces, err := f.runtime.ObserveWorkspaces(context.Background())
	if err != nil {
		return failed, fmt.Errorf("%w: %w", backend.ErrOwnedMutationNotIssued, err)
	}
	workspace, err := findUniqueWorkspace(workspaces, false, coordinatorWorkspacePredicate(resource))
	if err != nil {
		return failed, fmt.Errorf("%w: %w", backend.ErrOwnedMutationNotIssued, err)
	}
	if len(workspace.Panes) != 1 {
		return failed, fmt.Errorf("%w: %w", backend.ErrOwnedMutationNotIssued, backend.ErrOwnedWorkspaceHasUnadmittedPane)
	}
	if workspace.Path != "" && (filepath.Clean(workspace.Path) != filepath.Clean(workspace.RepoRoot) ||
		filepath.Clean(workspace.Path) != filepath.Clean(f.target.CurrentPath)) {
		return failed, fmt.Errorf("%w: %w", backend.ErrOwnedMutationNotIssued, backend.ErrOwnedIdentityMismatch)
	}
	if err := f.runtime.closeWorkspace(workspace.WorkspaceID, false); err != nil {
		return failed, err
	}
	if _, err := f.runtime.ObserveWorkspaces(context.Background()); err != nil {
		return failed, err
	}
	return backend.CloseResult{Status: backend.CloseConfirmed}, nil
}

func newPlanCoordinatorFixture(t *testing.T) (herdrLifecycleFixture, state.Pane, *fakeHerdrLifecycleRuntime) {
	t.Helper()
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.Parent, fixture.pane.RuntimeParent = "plan:demo", "plan:demo"
	fixture.pane.TaskID, fixture.pane.IssueNum = "task-a", 0
	workspace := herdrLifecycleWorkspace("w-coordinator", "fanout-coordinator-nonce", fixture.projectRoot, "", "")
	workspace.Path = ""
	pane := manualLifecycleCoordinatorPane(fixture.pane, workspace, -1)
	replaceLifecyclePanes(t, fixture.projectRoot, pane)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, workspace)
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot, workspaces: []backend.WorkspaceObservation{workspace}}
	return fixture, pane, runtime
}

func TestCleanupPlanRetiresCoordinatorAndAllowsShutdown(t *testing.T) {
	for _, phase := range []string{"no tasks", "repository root metadata", "merged task", "completed cleanup", "expired coordinator"} {
		t.Run(phase, func(t *testing.T) {
			fixture, pane, runtime := newPlanCoordinatorFixture(t)
			switch phase {
			case "repository root metadata":
				runtime.workspaces[0].Path = fixture.projectRoot
				runtime.workspaces[0].RepoRoot = fixture.projectRoot
				runtime.workspaces[0].RepoKey = filepath.Join(fixture.projectRoot, ".git")
			case "merged task":
				recordLifecyclePane(t, fixture.projectRoot, fixture.pane)
				runtime.workspaces = append(runtime.workspaces, fixture.workspace)
				installLifecycleCleanupGH(t)
			case "completed cleanup":
				recordLifecyclePane(t, fixture.projectRoot, fixture.pane)
				recordActiveHerdrCleanupIntent(t, fixture, state.CleanupRemove)
				runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
				updatePlanTestIntents(t, fixture.projectRoot, func(intent *state.LaunchIntent) {
					if intent.Kind == state.IntentCleanup {
						intent.Status, intent.CleanupHookPhase = state.IntentRealized, state.CleanupHookCompleted
						intent.CleanupWorktreeRemovedRequired = new(true)
					}
				})
			case "expired coordinator":
				updatePlanTestIntents(t, fixture.projectRoot, func(intent *state.LaunchIntent) {
					intent.ExpiresUnixMS = time.Now().Add(-time.Hour).UnixMilli()
				})
			}
			opts := herdrLifecycleOptions(fixture, runtime)
			lg := &captureLogger{}
			if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != exitcode.OK {
				t.Fatalf("CleanupPlan() = %d; errors=%v", got, lg.errors)
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, pane, false)
			if runtime.closeCalls != 1 || len(runtime.workspaces) != 0 {
				t.Fatalf("close calls=%d; workspaces=%v", runtime.closeCalls, runtime.workspaces)
			}
			if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != exitcode.OK || runtime.closeCalls != 1 {
				t.Fatalf("completed replay=%d; close calls=%d", got, runtime.closeCalls)
			}
			assertPlanCoordinatorShutdown(t, fixture.projectRoot, runtime)
		})
	}
}

func TestCleanupPlanCoordinatorPreservesOtherPlan(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			fixture, pane, runtime := newPlanCoordinatorFixture(t)
			otherTarget := fixture.pane
			otherTarget.Parent, otherTarget.RuntimeParent = "plan:beta", "plan:beta"
			otherWorkspace := herdrLifecycleWorkspace("w-beta", "fanout-coordinator-beta", fixture.projectRoot, "", "")
			otherWorkspace.Path = ""
			other := manualLifecycleCoordinatorPane(otherTarget, otherWorkspace, -2)
			recordLifecyclePane(t, fixture.projectRoot, other)
			recordLifecycleCoordinatorIntent(t, fixture.projectRoot, otherTarget, otherWorkspace)
			runtime.workspaces = append(runtime.workspaces, otherWorkspace)
			if metadata {
				for i := range runtime.workspaces {
					runtime.workspaces[i].Path, runtime.workspaces[i].RepoRoot = fixture.projectRoot, fixture.projectRoot
					runtime.workspaces[i].RepoKey = filepath.Join(fixture.projectRoot, ".git")
				}
			}
			opts := herdrLifecycleOptions(fixture, runtime)
			if got := CleanupPlan(opts, pane.RuntimeParent, nopLogger{}); got != exitcode.OK {
				t.Fatalf("alpha cleanup=%d", got)
			}
			store, err := state.LoadProject(fixture.projectRoot)
			if err != nil || len(store.Panes) != 1 || store.Panes[0].WorkspaceID != other.WorkspaceID {
				t.Fatalf("remaining rows=%+v; error=%v", store.Panes, err)
			}
			journal, err := state.LoadLaunchJournal(fixture.projectRoot)
			if err != nil || len(journal.Intents) != 1 || journal.Intents[0].RuntimeParent != "plan:beta" {
				t.Fatalf("remaining intents=%+v; error=%v", journal.Intents, err)
			}
			if len(runtime.workspaces) != 1 || runtime.workspaces[0].WorkspaceID != other.WorkspaceID {
				t.Fatalf("Beta was closed: %+v", runtime.workspaces)
			}
			if got := CleanupPlan(opts, other.RuntimeParent, nopLogger{}); got != exitcode.OK {
				t.Fatalf("beta cleanup=%d", got)
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, other, false)
			assertPlanCoordinatorShutdown(t, fixture.projectRoot, runtime)
		})
	}
}

func TestCleanupPlanCoordinatorSaveFailureDoesNotRepeatMutation(t *testing.T) {
	for _, failure := range []string{"pending", "row retirement", "intent retirement"} {
		t.Run(failure, func(t *testing.T) {
			fixture, pane, runtime := newPlanCoordinatorFixture(t)
			path, err := state.LaunchJournalPath(fixture.projectRoot)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "row retirement" {
				path = state.Path(fixture.projectRoot)
			}
			dir := filepath.Dir(path)
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			restore := func() {
				if err := os.Chmod(dir, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(restore)
			deny := func() {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
			}
			wantCloses := 1
			if failure == "pending" {
				deny()
				wantCloses = 0
			} else {
				runtime.afterClose = func(string) { deny() }
			}
			opts := herdrLifecycleOptions(fixture, runtime)
			if got := CleanupPlan(opts, pane.RuntimeParent, nopLogger{}); got != exitcode.Env {
				t.Fatalf("save failure=%d", got)
			}
			restore()
			runtime.afterClose = nil
			assertPlanCoordinatorState(t, fixture.projectRoot, pane, true)
			if runtime.closeCalls != wantCloses {
				t.Fatalf("close calls=%d want=%d", runtime.closeCalls, wantCloses)
			}
			if got := CleanupPlan(opts, pane.RuntimeParent, nopLogger{}); got != exitcode.OK || runtime.closeCalls != 1 {
				t.Fatalf("recovery=%d; close calls=%d", got, runtime.closeCalls)
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, pane, false)
		})
	}
}

func TestCleanupPlanPreservesCoordinatorWhileTasksRemain(t *testing.T) {
	fixture, pane, runtime := newPlanCoordinatorFixture(t)
	fixture.pane.BranchName = "" // An ineligible task remains recorded without querying GitHub.
	recordLifecyclePane(t, fixture.projectRoot, fixture.pane)
	if got := CleanupPlan(herdrLifecycleOptions(fixture, runtime), pane.RuntimeParent, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CleanupPlan() = %d", got)
	}
	assertPlanCoordinatorState(t, fixture.projectRoot, pane, true)
	if runtime.observeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("coordinator was touched: observations=%d closes=%d", runtime.observeCalls, runtime.closeCalls)
	}
}

func TestCleanupPlanCoordinatorAuxiliaryPaneRequiresManualCleanup(t *testing.T) {
	fixture, pane, runtime := newPlanCoordinatorFixture(t)
	runtime.workspaces[0].Panes = append(runtime.workspaces[0].Panes, backend.WorkspacePaneObservation{
		Pane:       backend.PaneRef{Backend: backend.Herdr, Workspace: pane.WorkspaceID, Pane: "auxiliary"},
		TerminalID: "auxiliary-terminal", CWD: fixture.projectRoot,
	})
	before, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	lg := &captureLogger{}
	opts := herdrLifecycleOptions(fixture, runtime)
	if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != exitcode.Env || !strings.Contains(strings.Join(lg.errors, " "), "manual cleanup") {
		t.Fatalf("CleanupPlan() = %d; errors=%v", got, lg.errors)
	}
	after, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil || !reflect.DeepEqual(before, after) || runtime.closeCalls != 0 {
		t.Fatalf("unsafe mutation: journal=%+v error=%v closes=%d", after, err, runtime.closeCalls)
	}
	assertPlanCoordinatorState(t, fixture.projectRoot, pane, true)
	runtime.workspaces[0].Panes = runtime.workspaces[0].Panes[:1]
	if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != exitcode.OK {
		t.Fatalf("retry after auxiliary removal=%d; errors=%v", got, lg.errors)
	}
}

func TestCleanupPlanCoordinatorRetriesAfterCheckoutGuardRejection(t *testing.T) {
	fixture, pane, runtime := newPlanCoordinatorFixture(t)
	runtime.workspaces[0].Path = fixture.projectRoot
	runtime.workspaces[0].RepoRoot = filepath.Dir(fixture.projectRoot)
	runtime.workspaces[0].RepoKey = filepath.Join(runtime.workspaces[0].RepoRoot, ".git")
	opts := herdrLifecycleOptions(fixture, runtime)
	lg := &captureLogger{}
	if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != exitcode.Env {
		t.Fatalf("guard rejection=%d; errors=%v", got, lg.errors)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil || len(journal.Intents) != 1 || journal.Intents[0].Status != state.IntentRealized || journal.Intents[0].Failure != "" {
		t.Fatalf("intent was not restored: journal=%+v; error=%v", journal, err)
	}
	message := strings.Join(lg.errors, " ")
	if !strings.Contains(message, ErrManualCleanupRequired.Error()) || !strings.Contains(message, backend.ErrOwnedIdentityMismatch.Error()) {
		t.Fatalf("missing manual cleanup reason: %s", message)
	}
	assertPlanCoordinatorState(t, fixture.projectRoot, pane, true)
	if runtime.closeCalls != 0 {
		t.Fatalf("close calls after guard rejection=%d", runtime.closeCalls)
	}
	runtime.workspaces[0].Path, runtime.workspaces[0].RepoRoot, runtime.workspaces[0].RepoKey = "", "", ""
	if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != exitcode.OK {
		t.Fatalf("retry after metadata removal=%d; errors=%v", got, lg.errors)
	}
	if runtime.closeCalls != 1 || len(runtime.workspaces) != 0 {
		t.Fatalf("close calls=%d; workspaces=%v", runtime.closeCalls, runtime.workspaces)
	}
	assertPlanCoordinatorState(t, fixture.projectRoot, pane, false)
	assertPlanCoordinatorShutdown(t, fixture.projectRoot, runtime)
}

func TestCleanupPlanCoordinatorSnapshotFailureTracksCloseDispatch(t *testing.T) {
	for _, afterClose := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_close=%t", afterClose), func(t *testing.T) {
			fixture, pane, runtime := newPlanCoordinatorFixture(t)
			runtime.observeErr = errors.New("temporary snapshot failure")
			runtime.observeErrAtCall = 2 // The coordinator closer's snapshot after Bind.
			wantStatus, wantFailure, wantCloses := state.IntentRealized, "", 0
			if afterClose {
				runtime.observeErrAtCall = 3
				saved := runtime.workspaces[0]
				runtime.afterClose = func(string) { runtime.workspaces = append(runtime.workspaces, saved) }
				wantStatus, wantFailure, wantCloses = state.IntentManualCleanupRequired, panelaunch.ManagedCoordinatorClosePending, 1
			}
			opts := herdrLifecycleOptions(fixture, runtime)
			lg := &captureLogger{}
			if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != exitcode.Env {
				t.Fatalf("snapshot failure=%d; errors=%v", got, lg.errors)
			}
			journal, err := state.LoadLaunchJournal(fixture.projectRoot)
			if err != nil || len(journal.Intents) != 1 || journal.Intents[0].Status != wantStatus || journal.Intents[0].Failure != wantFailure {
				t.Fatalf("intent after snapshot failure: journal=%+v; error=%v", journal, err)
			}
			if runtime.closeCalls != wantCloses || runtime.observeCalls != runtime.observeErrAtCall {
				t.Fatalf("closes=%d; observations=%d", runtime.closeCalls, runtime.observeCalls)
			}
			if !afterClose && !strings.Contains(strings.Join(lg.errors, " "), ErrManualCleanupRequired.Error()) {
				t.Fatalf("missing manual cleanup reason: %v", lg.errors)
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, pane, true)
			runtime.observeErr, runtime.observeErrAtCall, runtime.afterClose = nil, 0, nil
			want := exitcode.OK
			if afterClose {
				want = exitcode.Env
			}
			if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != want || runtime.closeCalls != 1 {
				t.Fatalf("retry=%d want=%d; close calls=%d", got, want, runtime.closeCalls)
			}
			if afterClose {
				runtime.workspaces = nil // Manual cleanup resolves an unconfirmed close.
				if got := CleanupPlan(opts, pane.RuntimeParent, lg); got != exitcode.OK || runtime.closeCalls != 1 {
					t.Fatalf("absence recovery=%d; close calls=%d", got, runtime.closeCalls)
				}
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, pane, false)
			assertPlanCoordinatorShutdown(t, fixture.projectRoot, runtime)
		})
	}
}

func TestCleanupPlanCoordinatorResponseLossNeverReissuesClose(t *testing.T) {
	for _, remains := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "still present"}[remains], func(t *testing.T) {
			fixture, pane, runtime := newPlanCoordinatorFixture(t)
			runtime.closeErr = errors.New("lost close response")
			if remains {
				saved := runtime.workspaces[0]
				runtime.afterClose = func(string) { runtime.workspaces = append(runtime.workspaces, saved) }
			}
			opts := herdrLifecycleOptions(fixture, runtime)
			if got := CleanupPlan(opts, pane.RuntimeParent, nopLogger{}); got != exitcode.Env {
				t.Fatalf("initial cleanup=%d", got)
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, pane, true)
			want := exitcode.OK
			if remains {
				want = exitcode.Env
			}
			if got := CleanupPlan(opts, pane.RuntimeParent, nopLogger{}); got != want || runtime.closeCalls != 1 {
				t.Fatalf("retry=%d want=%d; close calls=%d", got, want, runtime.closeCalls)
			}
			runtime.workspaces = nil // Simulate manual close when the original outcome stayed uncertain.
			if got := CleanupPlan(opts, pane.RuntimeParent, nopLogger{}); got != exitcode.OK || runtime.closeCalls != 1 {
				t.Fatalf("absence recovery=%d; close calls=%d", got, runtime.closeCalls)
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, pane, false)
		})
	}
}

func TestCleanupPlanCoordinatorRejectsChangedIdentityAndObservationFailure(t *testing.T) {
	for _, change := range []string{"saved terminal", "live terminal", "live label", "label reused", "checkout", "snapshot", "owner"} {
		t.Run(change, func(t *testing.T) {
			fixture, pane, runtime := newPlanCoordinatorFixture(t)
			switch change {
			case "saved terminal":
				pane.TerminalID = "foreign-terminal"
				replaceLifecyclePanes(t, fixture.projectRoot, pane)
			case "live terminal":
				runtime.workspaces[0].Panes[0].TerminalID = "foreign-terminal"
			case "live label":
				runtime.workspaces[0].Label = "foreign-label"
			case "label reused":
				runtime.workspaces[0].WorkspaceID = "foreign-workspace"
			case "checkout":
				runtime.workspaces[0].Path = fixture.worktreePath
			case "snapshot":
				runtime.observeErr, runtime.observeErrAtCall = errors.New("snapshot failed"), 1
			case "owner":
				runtime.verifyErr = errors.New("owner changed")
			}
			if got := CleanupPlan(herdrLifecycleOptions(fixture, runtime), pane.RuntimeParent, nopLogger{}); got != exitcode.Env {
				t.Fatalf("CleanupPlan() = %d", got)
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, pane, true)
			if runtime.closeCalls != 0 {
				t.Fatalf("close calls=%d", runtime.closeCalls)
			}
		})
	}
}

func TestCleanupPlanLeavesOtherCoordinatorAndAtomicLaneUnchanged(t *testing.T) {
	fixture, pane, runtime := newPlanCoordinatorFixture(t)
	for _, change := range []string{"other plan", "issue coordinator", "atomic backend"} {
		t.Run(change, func(t *testing.T) {
			other := pane
			switch change {
			case "other plan":
				other.RuntimeParent = "plan:other"
			case "issue coordinator":
				other.RuntimeParent = "425"
			case "atomic backend":
				other.Backend = backend.Tmux
			}
			replaceLifecyclePanes(t, fixture.projectRoot, other)
			opts := herdrLifecycleOptions(fixture, runtime)
			opts.WorkspaceRuntime = nil
			if got := CleanupPlan(opts, pane.RuntimeParent, nopLogger{}); got != exitcode.OK {
				t.Fatalf("CleanupPlan() = %d", got)
			}
			assertPlanCoordinatorState(t, fixture.projectRoot, other, true)
		})
	}
}

func updatePlanTestIntents(t *testing.T, root string, update func(*state.LaunchIntent)) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err := locked.LaunchJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range journal.Intents {
		update(&journal.Intents[i])
	}
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
}

func assertPlanCoordinatorState(t *testing.T, root string, pane state.Pane, want bool) {
	t.Helper()
	store, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := store.Find(pane.Parent, pane.IssueNum); found != want {
		t.Fatalf("coordinator row found=%t, want %t", found, want)
	}
	journal, err := state.LoadLaunchJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if (len(journal.Intents) != 0) != want {
		t.Fatalf("intents=%+v, want present=%t", journal.Intents, want)
	}
}

func assertPlanCoordinatorShutdown(t *testing.T, root string, runtime *fakeHerdrLifecycleRuntime) {
	t.Helper()
	called := false
	err := panelaunch.ShutdownManagedServer(context.Background(), root, panelaunch.ManagedServerIO{
		ObserveWorkspaces: runtime.ObserveWorkspaces,
		InspectServer: func() (state.RuntimeServerIdentity, error) {
			return state.RuntimeServerIdentity{
				GitCommonDir: filepath.Join(root, ".git"), RuntimeDir: "/tmp/fanout-owned", Session: "fanout-owned",
				SocketPath: "/tmp/fanout-owned/herdr.sock", ClientSocketPath: "/tmp/fanout-owned/client.sock",
				OwnerNonce: strings.Repeat("a", 64), SupervisorPID: 42, SupervisorStartToken: strings.Repeat("d", 64), ServerPID: 43,
				BinaryPath: "/bin/herdr", BinarySHA256: strings.Repeat("b", 64), BinaryVersion: "0.8.2",
				LauncherPath: "/bin/fanout", LauncherSHA256: strings.Repeat("c", 64),
			}, nil
		},
		ShutdownServer: func(_ context.Context, _ state.RuntimeServerIdentity, markIssued func() error) error {
			called = true
			return markIssued()
		},
	})
	if err != nil || !called {
		t.Fatalf("shutdown called=%t; error=%v", called, err)
	}
}
