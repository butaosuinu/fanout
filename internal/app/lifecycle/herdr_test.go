package lifecycle

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type fakeHerdrLifecycleRuntime struct {
	projectRoot              string
	workspaces               []herdrrun.WorkspaceObservation
	verifyErr                error
	verifyErrAtCall          int
	setupErr                 error
	openErr                  error
	removeErr                error
	closeErr                 error
	observeAfterMutationErr  error
	keepWorkspaceAfterRemove bool
	mutationDispatched       bool

	verifyCalls int
	setupCalls  int
	openCalls   int
	removeCalls int
	closeCalls  int
}

func (f *fakeHerdrLifecycleRuntime) VerifyOwned(context.Context) error {
	f.verifyCalls++
	if f.verifyErrAtCall > 0 && f.verifyErrAtCall != f.verifyCalls {
		return nil
	}
	return f.verifyErr
}

func (f *fakeHerdrLifecycleRuntime) VerifyWorktreeSetupPolicy(context.Context) error {
	f.setupCalls++
	return f.setupErr
}

func (f *fakeHerdrLifecycleRuntime) ObserveWorkspaces(context.Context) ([]herdrrun.WorkspaceObservation, error) {
	if f.mutationDispatched && f.observeAfterMutationErr != nil {
		return nil, f.observeAfterMutationErr
	}
	return append([]herdrrun.WorkspaceObservation(nil), f.workspaces...), nil
}

func (f *fakeHerdrLifecycleRuntime) OpenWorktree(_ context.Context, req herdrrun.WorktreeOpenRequest) (herdrrun.WorktreeMutationResult, error) {
	f.openCalls++
	if herdrMutationDefinitelyNotIssued(f.openErr) {
		return herdrrun.WorktreeMutationResult{}, f.openErr
	}
	workspace := herdrLifecycleWorkspace("w-reopened", req.Label, req.Path, req.SourceRepoKey, req.SourceRepoRoot)
	f.workspaces = append(f.workspaces, workspace)
	f.mutationDispatched = true
	return herdrrun.WorktreeMutationResult{WorkspaceObservation: workspace}, f.openErr
}

func (f *fakeHerdrLifecycleRuntime) RemoveWorktree(ctx context.Context, workspaceID, path string) error {
	f.removeCalls++
	if herdrMutationDefinitelyNotIssued(f.removeErr) {
		return f.removeErr
	}
	cmd := exec.CommandContext(ctx, "git", "-C", f.projectRoot, "worktree", "remove", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.Join(f.removeErr, errors.New(strings.TrimSpace(string(out))), err)
	}
	f.mutationDispatched = true
	if !f.keepWorkspaceAfterRemove {
		f.removeWorkspace(workspaceID)
	}
	return f.removeErr
}

func (f *fakeHerdrLifecycleRuntime) CloseWorkspace(_ context.Context, workspaceID string) error {
	f.closeCalls++
	if herdrMutationDefinitelyNotIssued(f.closeErr) {
		return f.closeErr
	}
	f.mutationDispatched = true
	f.removeWorkspace(workspaceID)
	return f.closeErr
}

func (f *fakeHerdrLifecycleRuntime) removeWorkspace(workspaceID string) {
	kept := f.workspaces[:0]
	for _, workspace := range f.workspaces {
		if workspace.WorkspaceID != workspaceID {
			kept = append(kept, workspace)
		}
	}
	f.workspaces = kept
}

func (f *fakeHerdrLifecycleRuntime) setMutationError(phase state.HerdrCleanupPhase, err error) {
	switch phase {
	case state.HerdrCleanupReopen:
		f.openErr = err
	case state.HerdrCleanupRemove:
		f.removeErr = err
	case state.HerdrCleanupWorkspaceClose:
		f.closeErr = err
	}
}

func (f *fakeHerdrLifecycleRuntime) phaseMutationCalls(phase state.HerdrCleanupPhase) int {
	switch phase {
	case state.HerdrCleanupReopen:
		return f.openCalls
	case state.HerdrCleanupRemove:
		return f.removeCalls
	case state.HerdrCleanupWorkspaceClose:
		return f.closeCalls
	default:
		return 0
	}
}

type herdrLifecycleFixture struct {
	projectRoot  string
	worktreePath string
	branch       string
	pane         state.Pane
	workspace    herdrrun.WorkspaceObservation
}

func TestHerdrCloseRemovesOwnedWorktreeAndStateButKeepsBranch(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}
	opts := herdrLifecycleOptions(fixture, runtime)

	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseWorktree, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	assertHerdrLifecycleRemoved(t, fixture)
	if !localBranchExists(fixture.projectRoot, fixture.branch) {
		t.Fatalf("CloseWorktree deleted branch %s", fixture.branch)
	}
	if runtime.removeCalls != 1 || runtime.closeCalls != 0 {
		t.Fatalf("mutation calls = remove %d/close %d, want 1/0", runtime.removeCalls, runtime.closeCalls)
	}
}

func TestHerdrCloseRemovesCompletedPaneLessWorkspace(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	workspace := fixture.workspace
	workspace.Pane = backend.PaneRef{}
	workspace.TerminalID = ""
	workspace.CWD = ""
	workspace.Panes = nil
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	assertHerdrLifecycleRemoved(t, fixture)
	if runtime.removeCalls != 1 {
		t.Fatalf("pane-less cleanup remove calls = %d, want 1", runtime.removeCalls)
	}
}

func TestHerdrCleanupRemovesEligibleOwnedWorktree(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	installLifecycleCleanupGH(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}

	if got := Cleanup(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Cleanup() = %d, want %d", got, exitcode.OK)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseHonorsCustomStatePath(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	customState := filepath.Join(t.TempDir(), "state.json")
	if err := os.Rename(state.Path(fixture.projectRoot), customState); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}
	opts := herdrLifecycleOptions(fixture, runtime)
	opts.StatePath = customState

	if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	store, err := state.Load(customState)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := store.Find(fixture.pane.Parent, fixture.pane.IssueNum); found {
		t.Fatal("custom state row remains after Herdr cleanup")
	}
	if _, err := os.Stat(fixture.worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after custom-state cleanup: %v", err)
	}
}

func TestHerdrMergeFastForwardsRecordedBranch(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "merged.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "merged.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "child")

	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}
	opts := herdrLifecycleOptions(fixture, runtime)
	if got := Merge(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Merge() = %d, want %d", got, exitcode.OK)
	}
	want := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	got := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", "HEAD"))
	if got != want {
		t.Fatalf("merged HEAD = %s, want child %s", got, want)
	}
}

func TestHerdrMergeRejectsIncompleteIdentityBeforeGitMutation(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "untrusted.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "untrusted.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "untrusted child")
	fixture.pane.HerdrWorkspaceLabel = ""
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}
	before := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", "HEAD"))

	if got := Merge(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Merge() = %d, want %d", got, exitcode.Env)
	}
	after := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", "HEAD"))
	if after != before || runtime.verifyCalls != 0 {
		t.Fatalf("incomplete merge mutated or contacted runtime: HEAD %s -> %s, verify calls %d", before, after, runtime.verifyCalls)
	}
}

func TestHerdrMergeRejectsForeignWorkspaceAtSavedCheckout(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "untrusted.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "untrusted.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "untrusted child")
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{foreignHerdrWorkspaceAtSameCheckout(fixture)},
	}
	before := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", "HEAD"))

	if got := Merge(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Merge() = %d, want %d", got, exitcode.Env)
	}
	after := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", "HEAD"))
	if after != before {
		t.Fatalf("foreign workspace collision changed HEAD from %s to %s", before, after)
	}
}

func TestHerdrCloseRejectsForeignWorkspaceAtSavedCheckout(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	hookPath := filepath.Join(t.TempDir(), "before-worktree")
	t.Setenv("FANOUT_TEST_BEFORE_WORKTREE", hookPath)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{foreignHerdrWorkspaceAtSameCheckout(fixture)},
	}
	opts := herdrLifecycleOptions(fixture, runtime)
	opts.Hooks = hooks.Config{Events: map[hooks.Type][]hooks.Command{
		hooks.BeforeWorktreeRemove: {{
			Command: `printf called > "$FANOUT_TEST_BEFORE_WORKTREE"`, Timeout: time.Second,
		}},
	}}

	if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrLifecyclePreserved(t, fixture)
	if _, err := os.Stat(hookPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hook ran for a foreign workspace collision: %v", err)
	}
	if runtime.openCalls != 0 || runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("foreign workspace collision issued mutations: open %d/remove %d/close %d", runtime.openCalls, runtime.removeCalls, runtime.closeCalls)
	}
}

func TestHerdrReopenMatcherRejectsForeignWorkspaceAtSavedCheckout(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	expected := herdrLifecycleWorkspace(
		"w-reopened", fixture.workspace.Label, fixture.worktreePath,
		fixture.pane.HerdrRepoKey, fixture.pane.HerdrRepoRoot,
	)
	foreign := foreignHerdrWorkspaceAtSameCheckout(fixture)
	predicate := herdrWorkspaceLabelPredicate(
		fixture.workspace.Label,
		fixture.worktreePath,
		fixture.pane.HerdrRepoKey,
		fixture.pane.HerdrRepoRoot,
	)
	for name, workspaces := range map[string][]herdrrun.WorkspaceObservation{
		"foreign only":         {foreign},
		"expected and foreign": {expected, foreign},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := findUniqueWorkspace(workspaces, true, predicate); err == nil {
				t.Fatal("same-checkout foreign workspace was treated as absent or unique")
			}
		})
	}
}

func TestHerdrCloseUsesResidualWorkspaceClose(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot:              fixture.projectRoot,
		workspaces:               []herdrrun.WorkspaceObservation{fixture.workspace},
		keepWorkspaceAfterRemove: true,
	}

	lg := &captureLogger{}
	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, lg); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d; errors=%v", got, exitcode.OK, lg.errors)
	}
	assertHerdrLifecycleRemoved(t, fixture)
	if runtime.removeCalls != 1 || runtime.closeCalls != 1 {
		t.Fatalf("mutation calls = remove %d/close %d, want 1/1", runtime.removeCalls, runtime.closeCalls)
	}
}

func TestHerdrCloseReopensCheckoutOnlyStateWithMultiPaneCoordinator(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace("w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.HerdrRepoKey, fixture.pane.HerdrRepoRoot)
	coordinator.Pane.Pane = "w-coordinator:p1"
	coordinator.TerminalID = "terminal-coordinator"
	coordinator.CWD = fixture.projectRoot
	coordinator.Panes = []herdrrun.WorkspacePaneObservation{{
		Pane: coordinator.Pane, TerminalID: coordinator.TerminalID, CWD: fixture.projectRoot,
	}}
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	coordinator.Panes = append(coordinator.Panes, herdrrun.WorkspacePaneObservation{
		Pane:       backend.PaneRef{Backend: backend.Herdr, Workspace: coordinator.WorkspaceID, Pane: "w-coordinator:p2"},
		TerminalID: "terminal-coordinator-extra", CWD: filepath.Join(fixture.projectRoot, "subdir"),
	})
	coordinator.Pane = backend.PaneRef{}
	coordinator.TerminalID = ""
	coordinator.CWD = ""
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{coordinator},
	}

	lg := &captureLogger{}
	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, lg); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d; errors=%v", got, exitcode.OK, lg.errors)
	}
	assertHerdrLifecycleRemoved(t, fixture)
	if runtime.setupCalls != 1 || runtime.openCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf("reopen calls = setup %d/open %d/remove %d, want 1/1/1", runtime.setupCalls, runtime.openCalls, runtime.removeCalls)
	}
}

func TestFindHerdrCoordinatorIntentPreservesPlanOwnerScope(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	target := fixture.pane
	target.Parent = "plan:demo"
	coordinator := herdrLifecycleWorkspace("w-coordinator", "coordinator-label", fixture.projectRoot,
		target.HerdrRepoKey, target.HerdrRepoRoot)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, target, coordinator)
	locked, err := state.LockProjectForLaunch(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()

	intent, err := findHerdrCoordinatorIntent(locked, fixture.projectRoot, target)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Parent != target.Parent || intent.OwnerProjectRoot != fixture.projectRoot {
		t.Fatalf("plan coordinator identity = parent %q owner %q", intent.Parent, intent.OwnerProjectRoot)
	}
}

func TestHerdrCloseFailsClosedOnTerminalIdentityMismatch(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	workspace := fixture.workspace
	workspace.Panes[0].Pane.Pane = "w2:foreign"
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrLifecyclePreserved(t, fixture)
	if runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("mismatch issued mutations: remove %d/close %d", runtime.removeCalls, runtime.closeCalls)
	}
}

func TestHerdrCloseVerifiesTerminalBeforeClosingResidualWorkspace(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
	workspace := fixture.workspace
	workspace.Panes[0].Pane.Pane = "w2:foreign"
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	store, err := state.Load(state.Path(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := store.Find(fixture.pane.Parent, fixture.pane.IssueNum); !found {
		t.Fatal("state row was removed after residual-workspace identity mismatch")
	}
	if runtime.closeCalls != 0 {
		t.Fatalf("identity mismatch issued %d workspace close(s)", runtime.closeCalls)
	}
}

func TestHerdrCloseFailsClosedWhenOwnedSessionCannotBeVerified(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
		verifyErr:   errors.New("foreign owner marker"),
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrLifecyclePreserved(t, fixture)
	if runtime.removeCalls != 0 {
		t.Fatalf("unowned cleanup issued %d remove(s)", runtime.removeCalls)
	}
}

func TestHerdrHooksRequireFreshIdentityPreflight(t *testing.T) {
	for _, tt := range []struct {
		name               string
		verifyErrAtCall    int
		wantWorktreeHooked bool
	}{
		{name: "before worktree remove", verifyErrAtCall: 2},
		{name: "before pane close", verifyErrAtCall: 3, wantWorktreeHooked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHerdrLifecycleFixture(t)
			worktreeHookPath := filepath.Join(t.TempDir(), "before-worktree")
			paneHookPath := filepath.Join(t.TempDir(), "before-pane")
			t.Setenv("FANOUT_TEST_BEFORE_WORKTREE", worktreeHookPath)
			t.Setenv("FANOUT_TEST_BEFORE_PANE", paneHookPath)
			runtime := &fakeHerdrLifecycleRuntime{
				projectRoot:     fixture.projectRoot,
				workspaces:      []herdrrun.WorkspaceObservation{fixture.workspace},
				verifyErr:       errors.New("owned route changed"),
				verifyErrAtCall: tt.verifyErrAtCall,
			}
			opts := herdrLifecycleOptions(fixture, runtime)
			opts.Hooks = hooks.Config{Events: map[hooks.Type][]hooks.Command{
				hooks.BeforeWorktreeRemove: {{
					Command: `printf called > "$FANOUT_TEST_BEFORE_WORKTREE"`, Timeout: time.Second,
				}},
				hooks.BeforePaneClose: {{
					Command: `printf called > "$FANOUT_TEST_BEFORE_PANE"`, Timeout: time.Second,
				}},
			}}

			if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
				t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
			}
			_, worktreeHookErr := os.Stat(worktreeHookPath)
			if got := worktreeHookErr == nil; got != tt.wantWorktreeHooked {
				t.Fatalf("before_worktree_remove ran = %t, want %t", got, tt.wantWorktreeHooked)
			}
			if _, err := os.Stat(paneHookPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("before_pane_close ran without fresh identity: %v", err)
			}
			assertHerdrLifecyclePreserved(t, fixture)
			if runtime.removeCalls != 0 || runtime.closeCalls != 0 {
				t.Fatalf("failed hook preflight issued mutations: remove %d/close %d", runtime.removeCalls, runtime.closeCalls)
			}
		})
	}
}

func TestHerdrCleanupRetryDoesNotRepeatHooks(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*testing.T, herdrLifecycleFixture) *fakeHerdrLifecycleRuntime
		beforeTry func(*fakeHerdrLifecycleRuntime)
		wantRetry exitcode.Code
	}{
		{
			name: "manual cleanup required",
			prepare: func(t *testing.T, fixture herdrLifecycleFixture) *fakeHerdrLifecycleRuntime {
				t.Helper()
				if err := os.WriteFile(filepath.Join(fixture.worktreePath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return &fakeHerdrLifecycleRuntime{
					projectRoot: fixture.projectRoot,
					workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
				}
			},
			beforeTry: func(*fakeHerdrLifecycleRuntime) {},
			wantRetry: exitcode.Env,
		},
		{
			name: "issued reopen",
			prepare: func(t *testing.T, fixture herdrLifecycleFixture) *fakeHerdrLifecycleRuntime {
				t.Helper()
				runtime := prepareHerdrCleanupPhase(t, fixture, state.HerdrCleanupReopen)
				runtime.observeAfterMutationErr = errors.New("observation temporarily unavailable")
				return runtime
			},
			beforeTry: func(runtime *fakeHerdrLifecycleRuntime) {
				runtime.observeAfterMutationErr = nil
			},
			wantRetry: exitcode.OK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHerdrLifecycleFixture(t)
			hookPath := filepath.Join(t.TempDir(), "before-worktree")
			t.Setenv("FANOUT_TEST_BEFORE_WORKTREE", hookPath)
			runtime := tt.prepare(t, fixture)
			opts := herdrLifecycleOptions(fixture, runtime)
			opts.Hooks = hooks.Config{Events: map[hooks.Type][]hooks.Command{
				hooks.BeforeWorktreeRemove: {{
					Command: `printf 'called\n' >> "$FANOUT_TEST_BEFORE_WORKTREE"`, Timeout: time.Second,
				}},
			}}

			if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
				t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
			}
			assertHerdrHookCalls(t, hookPath, 1)

			tt.beforeTry(runtime)
			if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != tt.wantRetry {
				t.Fatalf("retry Close() = %d, want %d", got, tt.wantRetry)
			}
			assertHerdrHookCalls(t, hookPath, 1)
		})
	}
}

func assertHerdrHookCalls(t *testing.T, path string, want int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "called\n"); got != want {
		t.Fatalf("hook calls = %d, want %d", got, want)
	}
}

func TestHerdrCloseDoesNotForceDirtyCheckout(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrLifecyclePreserved(t, fixture)
	if runtime.removeCalls != 1 {
		t.Fatalf("dirty cleanup remove calls = %d, want one non-force attempt", runtime.removeCalls)
	}
	journal, err := state.LoadHerdrIntents(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Intents) != 1 || journal.Intents[0].Status != state.HerdrIntentManualCleanupRequired {
		t.Fatalf("dirty cleanup journal = %#v, want manual_cleanup_required", journal.Intents)
	}
}

func TestHerdrCloseAcceptsResponseLossOnlyAfterAbsence(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
		removeErr:   errors.New("response lost"),
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	assertHerdrLifecycleRemoved(t, fixture)
	if runtime.removeCalls != 1 {
		t.Fatalf("response-loss cleanup remove calls = %d, want 1", runtime.removeCalls)
	}
}

func TestHerdrCloseDoesNotChainWorkspaceCloseAfterAmbiguousRemove(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot:              fixture.projectRoot,
		workspaces:               []herdrrun.WorkspaceObservation{fixture.workspace},
		removeErr:                errors.New("response lost"),
		keepWorkspaceAfterRemove: true,
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 1 || runtime.closeCalls != 0 {
		t.Fatalf("ambiguous cleanup calls = remove %d/close %d, want 1/0", runtime.removeCalls, runtime.closeCalls)
	}
	journal, err := state.LoadHerdrIntents(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, intentID, err := herdrCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found || intent.Status != state.HerdrIntentManualCleanupRequired {
		t.Fatalf("ambiguous cleanup intent = %#v (found=%t), want manual cleanup", intent, found)
	}
}

func TestHerdrCloseRemovesResidualLaunchIntentAndEnvironment(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtimeDir := filepath.Join(fixture.projectRoot, "herdr-runtime")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "workload-env"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.pane.HerdrSocketPath = filepath.Join(runtimeDir, "herdr.sock")
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	worktreeIntentID, envPath := recordResidualHerdrLaunchIntent(t, fixture, runtimeDir)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	journal, err := state.LoadHerdrIntents(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := journal.FindIntent(worktreeIntentID); found {
		t.Fatal("residual Herdr launch intent remains after cleanup")
	}
	if _, err := os.Lstat(envPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("residual Herdr launch environment remains: %v", err)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseRejectsResidualLaunchIntentForDifferentWorkspace(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtimeDir := filepath.Join(fixture.projectRoot, "herdr-runtime")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "workload-env"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.pane.HerdrSocketPath = filepath.Join(runtimeDir, "herdr.sock")
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	worktreeIntentID, _ := recordResidualHerdrLaunchIntent(t, fixture, runtimeDir)
	rewriteResidualLaunchLabel(t, fixture.projectRoot, worktreeIntentID, "foreign-workspace")
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrLifecyclePreserved(t, fixture)
	if runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("mismatched launch intent issued mutations: remove %d/close %d", runtime.removeCalls, runtime.closeCalls)
	}
}

func TestHerdrCloseDoesNotReplayIssuedRemoveAfterCrash(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	head := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
	_, intentID, err := herdrCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.HerdrIntents(fixture.projectRoot)
	if err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	journal.UpsertIntent(state.HerdrIntent{
		ID: intentID, Kind: state.HerdrIntentCleanup, Status: state.HerdrIntentIssued,
		Parent: fixture.pane.Parent, RuntimeParent: fixture.pane.RuntimeParent,
		IssueNum: fixture.pane.IssueNum, Slug: fixture.pane.Slug,
		BranchName: fixture.branch, FullBranchRef: "refs/heads/" + fixture.branch,
		BaseBranch: fixture.pane.BaseBranch, ExpectedHead: head,
		WorktreePath: fixture.worktreePath, BranchExisted: true,
		WorkspaceLabel: fixture.workspace.Label, Resource: herdrResourceFromPane(fixture.pane),
		Session: fixture.pane.HerdrSession, SocketPath: fixture.pane.HerdrSocketPath,
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(), CleanupPhase: state.HerdrCleanupRemove,
	})
	if err := journal.Save(); err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("issued cleanup was replayed: remove %d/close %d", runtime.removeCalls, runtime.closeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestSavedHerdrCleanupRejectsChangedRuntimeIdentity(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	intent := state.HerdrIntent{
		Kind: state.HerdrIntentCleanup, Parent: fixture.pane.Parent,
		RuntimeParent: fixture.pane.RuntimeParent, IssueNum: fixture.pane.IssueNum,
		Slug: fixture.pane.Slug, BranchName: fixture.pane.BranchName,
		FullBranchRef: "refs/heads/" + fixture.pane.BranchName, BaseBranch: fixture.pane.BaseBranch,
		WorktreePath: fixture.pane.WorktreePath, BranchExisted: true,
		WorkspaceLabel: fixture.pane.HerdrWorkspaceLabel, Resource: herdrResourceFromPane(fixture.pane),
		Session: fixture.pane.HerdrSession, SocketPath: fixture.pane.HerdrSocketPath,
	}
	intent.Resource.TerminalID = "reused-terminal"
	if err := validateSavedHerdrCleanup(intent, fixture.projectRoot, fixture.pane, CloseWorktree); err == nil {
		t.Fatal("saved cleanup accepted a changed runtime identity")
	}
}

func TestHerdrCleanupDiscardsPlanAfterDefiniteNonMutation(t *testing.T) {
	errorsByName := map[string]error{
		"not-issued": herdrrun.MutationNotIssuedError{Cause: errors.New("dispatch unavailable")},
		"rejected":   herdrrun.MutationRejectedError{Code: "rejected", Message: "request refused"},
	}
	for _, phase := range []state.HerdrCleanupPhase{
		state.HerdrCleanupReopen,
		state.HerdrCleanupRemove,
		state.HerdrCleanupWorkspaceClose,
	} {
		for errorName, mutationErr := range errorsByName {
			t.Run(string(phase)+"/"+errorName, func(t *testing.T) {
				fixture := newHerdrLifecycleFixture(t)
				runtime := prepareHerdrCleanupPhase(t, fixture, phase)
				runtime.setMutationError(phase, mutationErr)

				if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
					t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
				}
				assertHerdrCleanupIntentStatus(t, fixture, "", false)
				assertHerdrStateRowPresent(t, fixture)
				if runtime.phaseMutationCalls(phase) != 1 {
					t.Fatalf("%s mutation calls = %d, want 1", phase, runtime.phaseMutationCalls(phase))
				}

				runtime.setMutationError(phase, nil)
				if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
					t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
				}
				if runtime.phaseMutationCalls(phase) != 2 {
					t.Fatalf("retry %s mutation calls = %d, want 2", phase, runtime.phaseMutationCalls(phase))
				}
				assertHerdrLifecycleRemoved(t, fixture)
			})
		}
	}
}

func TestHerdrCleanupReplansAfterRejectedRemoveAndHeadChange(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
		removeErr:   herdrrun.MutationRejectedError{Code: "dirty", Message: "request refused"},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrCleanupIntentStatus(t, fixture, "", false)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "retry.txt"), []byte("retry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "retry.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "retry cleanup")

	runtime.removeErr = nil
	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.removeCalls != 2 {
		t.Fatalf("remove calls = %d, want 2", runtime.removeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCleanupLeavesIssuedAfterPostMutationObservationFailure(t *testing.T) {
	for _, phase := range []state.HerdrCleanupPhase{
		state.HerdrCleanupReopen,
		state.HerdrCleanupRemove,
		state.HerdrCleanupWorkspaceClose,
	} {
		t.Run(string(phase), func(t *testing.T) {
			fixture := newHerdrLifecycleFixture(t)
			runtime := prepareHerdrCleanupPhase(t, fixture, phase)
			runtime.observeAfterMutationErr = errors.New("observation temporarily unavailable")

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
				t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
			}
			assertHerdrCleanupIntentStatus(t, fixture, state.HerdrIntentIssued, true)
			assertHerdrStateRowPresent(t, fixture)
			if runtime.phaseMutationCalls(phase) != 1 {
				t.Fatalf("%s mutation calls = %d, want 1", phase, runtime.phaseMutationCalls(phase))
			}

			runtime.observeAfterMutationErr = nil
			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
				t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
			}
			if runtime.phaseMutationCalls(phase) != 1 {
				t.Fatalf("retry replayed %s mutation: calls = %d", phase, runtime.phaseMutationCalls(phase))
			}
			assertHerdrLifecycleRemoved(t, fixture)
		})
	}
}

func TestExpiredPlannedHerdrCleanupDoesNotIssueMutation(t *testing.T) {
	for _, phase := range []state.HerdrCleanupPhase{
		state.HerdrCleanupReopen,
		state.HerdrCleanupRemove,
		state.HerdrCleanupWorkspaceClose,
	} {
		t.Run(string(phase), func(t *testing.T) {
			fixture := newHerdrLifecycleFixture(t)
			recordExpiredHerdrCleanupIntent(t, fixture, phase)
			runtime := &fakeHerdrLifecycleRuntime{
				projectRoot: fixture.projectRoot,
				workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
			}

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
				t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
			}
			if runtime.setupCalls != 0 || runtime.openCalls != 0 || runtime.removeCalls != 0 || runtime.closeCalls != 0 {
				t.Fatalf("expired %s mutation calls = setup %d/open %d/remove %d/close %d", phase, runtime.setupCalls, runtime.openCalls, runtime.removeCalls, runtime.closeCalls)
			}
			assertHerdrLifecyclePreserved(t, fixture)
			assertHerdrCleanupIntentStatus(t, fixture, "", false)

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
				t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
			}
			assertHerdrLifecycleRemoved(t, fixture)
		})
	}
}

func TestExpiredPlannedHerdrCleanupFinalizesAlreadyAbsentResources(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	recordExpiredHerdrCleanupIntent(t, fixture, state.HerdrCleanupRemove)
	runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.openCalls != 0 || runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("expired absent cleanup issued mutations: open %d/remove %d/close %d", runtime.openCalls, runtime.removeCalls, runtime.closeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestExpiredReopenedHerdrCleanupPreservesReplacementIdentityAndRefreshesHead(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := prepareHerdrCleanupPhase(t, fixture, state.HerdrCleanupReopen)
	runtime.removeErr = herdrrun.MutationNotIssuedError{Cause: errors.New("remove dispatch unavailable")}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("first Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.openCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf("first cleanup calls = open %d/remove %d, want 1/1", runtime.openCalls, runtime.removeCalls)
	}
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "reopened-retry.txt"), []byte("retry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "reopened-retry.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "retry reopened cleanup")
	expireSavedHerdrCleanupIntent(t, fixture)
	runtime.removeErr = nil

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.openCalls != 1 || runtime.removeCalls != 2 {
		t.Fatalf("retry cleanup calls = open %d/remove %d, want 1/2", runtime.openCalls, runtime.removeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseEverythingCompareDeletesOnlyFanoutCreatedBranch(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.HerdrBranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []herdrrun.WorkspaceObservation{fixture.workspace},
	}

	if got := CloseWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	if localBranchExists(fixture.projectRoot, fixture.branch) {
		t.Fatalf("fanout-created branch %s was not compare-deleted", fixture.branch)
	}
}

func TestHerdrCloseEverythingReapsIssueStateAfterResourcesAreAlreadyAbsent(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.HerdrBranchCreated = true
	replaceLifecyclePanes(t, fixture.projectRoot, fixture.pane)
	removeHerdrLifecycleResources(t, fixture)
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot}

	if got := CloseWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseEverythingReapsTaskStateAfterResourcesAreAlreadyAbsent(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.Parent = "plan:demo"
	fixture.pane.IssueNum = 0
	fixture.pane.TaskID = "task-a"
	fixture.pane.HerdrBranchCreated = true
	replaceLifecyclePanes(t, fixture.projectRoot, fixture.pane)
	removeHerdrLifecycleResources(t, fixture)
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot}

	if got := CloseTaskWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.TaskID, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CloseTaskWithMode() = %d, want %d", got, exitcode.OK)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func herdrLifecycleOptions(fixture herdrLifecycleFixture, runtime HerdrRuntime) Options {
	return Options{
		ProjectRoot: fixture.projectRoot,
		StatePath:   state.Path(fixture.projectRoot),
		Hooks:       hooks.EmptyConfig(),
		HerdrRuntime: func(context.Context, state.Pane) (HerdrRuntime, error) {
			return runtime, nil
		},
	}
}

func newHerdrLifecycleFixture(t *testing.T) herdrLifecycleFixture {
	t.Helper()
	projectRoot := t.TempDir()
	runHerdrLifecycleGit(t, projectRoot, "init", "--initial-branch", "main")
	runHerdrLifecycleGit(t, projectRoot, "config", "user.email", "fanout@example.com")
	runHerdrLifecycleGit(t, projectRoot, "config", "user.name", "fanout test")
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, projectRoot, "add", "tracked.txt")
	runHerdrLifecycleGit(t, projectRoot, "commit", "-m", "base")
	worktreePath := filepath.Join(projectRoot, ".fanout", "worktrees", "child")
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatal(err)
	}
	branch := "fanout/child"
	runHerdrLifecycleGit(t, projectRoot, "worktree", "add", "-b", branch, worktreePath)
	identity, err := worktree.ResolveRepoIdentity(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	workspace := herdrLifecycleWorkspace("w2", "fanout-child-nonce", worktreePath, identity.RepoKey, identity.RepoRoot)
	pane := state.Pane{
		Parent: "423", RuntimeParent: "423", IssueNum: 425, Slug: "child",
		Backend: backend.Herdr, PaneID: workspace.Pane.Pane,
		HerdrWorkspaceID: workspace.WorkspaceID, HerdrWorkspaceLabel: workspace.Label,
		HerdrTerminalID: workspace.TerminalID, HerdrRepoKey: identity.RepoKey,
		HerdrRepoRoot: identity.RepoRoot, HerdrSession: "fanout-owned",
		HerdrSocketPath: "/tmp/fanout-owned.sock", BranchName: branch,
		BaseBranch: "main", WorktreePath: worktreePath,
	}
	recordLifecyclePane(t, projectRoot, pane)
	return herdrLifecycleFixture{
		projectRoot: projectRoot, worktreePath: worktreePath, branch: branch,
		pane: pane, workspace: workspace,
	}
}

func herdrLifecycleWorkspace(id, label, path, repoKey, repoRoot string) herdrrun.WorkspaceObservation {
	ref := backend.PaneRef{Backend: backend.Herdr, Workspace: id, Pane: id + ":p1"}
	terminalID := "terminal-" + id
	return herdrrun.WorkspaceObservation{
		WorkspaceID: id, Label: label, Path: path, RepoKey: repoKey, RepoRoot: repoRoot,
		Pane: ref, TerminalID: terminalID, CWD: path,
		Panes: []herdrrun.WorkspacePaneObservation{{Pane: ref, TerminalID: terminalID, CWD: path}},
	}
}

func foreignHerdrWorkspaceAtSameCheckout(fixture herdrLifecycleFixture) herdrrun.WorkspaceObservation {
	return herdrLifecycleWorkspace(
		"w-foreign",
		"foreign-label",
		fixture.worktreePath,
		fixture.pane.HerdrRepoKey,
		fixture.pane.HerdrRepoRoot,
	)
}

func prepareHerdrCleanupPhase(
	t *testing.T,
	fixture herdrLifecycleFixture,
	phase state.HerdrCleanupPhase,
) *fakeHerdrLifecycleRuntime {
	t.Helper()
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot}
	switch phase {
	case state.HerdrCleanupReopen:
		coordinator := herdrLifecycleWorkspace(
			"w-coordinator", "coordinator-label", fixture.projectRoot,
			fixture.pane.HerdrRepoKey, fixture.pane.HerdrRepoRoot,
		)
		recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
		runtime.workspaces = []herdrrun.WorkspaceObservation{coordinator}
	case state.HerdrCleanupRemove:
		runtime.workspaces = []herdrrun.WorkspaceObservation{fixture.workspace}
	case state.HerdrCleanupWorkspaceClose:
		runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
		runtime.workspaces = []herdrrun.WorkspaceObservation{fixture.workspace}
	default:
		t.Fatalf("unsupported cleanup phase %q", phase)
	}
	return runtime
}

func recordLifecycleCoordinatorIntent(
	t *testing.T,
	projectRoot string,
	target state.Pane,
	workspace herdrrun.WorkspaceObservation,
) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtimeOwnerRoot, err := state.HerdrOwnerProjectRoot(target.RuntimeParent, filepath.Clean(projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	ownerRoot, err := state.HerdrOwnerProjectRoot(target.Parent, filepath.Clean(projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	id, err := state.HerdrCoordinatorIntentID(target.RuntimeParent, runtimeOwnerRoot, 0)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(state.HerdrIntent{
		ID: id, Kind: state.HerdrIntentCoordinator, Status: state.HerdrIntentRealized,
		Parent: target.Parent, RuntimeParent: target.RuntimeParent, OwnerProjectRoot: ownerRoot,
		WorktreePath: workspace.CWD, WorkspaceLabel: workspace.Label,
		Resource: state.HerdrResource{
			WorkspaceID: workspace.WorkspaceID, Label: workspace.Label,
			PaneID: workspace.Pane.Pane, TerminalID: workspace.TerminalID,
			CurrentPath: workspace.CWD,
		},
		Session: target.HerdrSession, SocketPath: target.HerdrSocketPath,
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(),
	})
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
}

func recordExpiredHerdrCleanupIntent(
	t *testing.T,
	fixture herdrLifecycleFixture,
	phase state.HerdrCleanupPhase,
) {
	t.Helper()
	ownerRoot, err := state.HerdrOwnerProjectRoot(fixture.pane.Parent, filepath.Clean(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	_, intentID, err := herdrCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	intent := state.HerdrIntent{
		ID: intentID, Kind: state.HerdrIntentCleanup, Status: state.HerdrIntentPlanned,
		Parent: fixture.pane.Parent, RuntimeParent: fixture.pane.RuntimeParent, OwnerProjectRoot: ownerRoot,
		IssueNum: fixture.pane.IssueNum, TaskID: fixture.pane.TaskID, Slug: fixture.pane.Slug,
		BranchName: fixture.pane.BranchName, FullBranchRef: "refs/heads/" + fixture.pane.BranchName,
		BaseBranch: fixture.pane.BaseBranch, WorktreePath: filepath.Clean(fixture.pane.WorktreePath),
		BranchCreated: fixture.pane.HerdrBranchCreated, BranchExisted: !fixture.pane.HerdrBranchCreated,
		WorkspaceLabel: fixture.pane.HerdrWorkspaceLabel, Resource: herdrResourceFromPane(fixture.pane),
		Session: fixture.pane.HerdrSession, SocketPath: fixture.pane.HerdrSocketPath,
		ExpiresUnixMS: time.Now().Add(-time.Minute).UnixMilli(), CleanupPhase: phase,
	}
	if phase == state.HerdrCleanupReopen {
		intent.Coordinator = state.HerdrResource{
			WorkspaceID: "coordinator", Label: "coordinator-label", PaneID: "coordinator:p1",
			TerminalID: "coordinator-terminal", CurrentPath: filepath.Clean(fixture.projectRoot),
		}
	}
	locked, err := state.LockProjectForLaunch(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err := locked.HerdrIntents(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if saveErr := journal.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
}

func expireSavedHerdrCleanupIntent(t *testing.T, fixture herdrLifecycleFixture) {
	t.Helper()
	_, intentID, err := herdrCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err := locked.HerdrIntents(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		t.Fatal("cleanup intent is absent")
	}
	intent.ExpiresUnixMS = time.Now().Add(-time.Minute).UnixMilli()
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
}

func recordResidualHerdrLaunchIntent(
	t *testing.T,
	fixture herdrLifecycleFixture,
	runtimeDir string,
) (string, string) {
	t.Helper()
	worktreeIntentID, _, err := herdrCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	nonce := strings.Repeat("a", 32)
	envPath := filepath.Join(runtimeDir, "workload-env", "env-"+nonce+".json")
	if writeErr := os.WriteFile(envPath, []byte("{}\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	locked, err := state.LockProjectForLaunch(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err := locked.HerdrIntents(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(state.HerdrIntent{
		ID: worktreeIntentID, Kind: state.HerdrIntentWorktree, Status: state.HerdrIntentRealized,
		Parent: fixture.pane.Parent, RuntimeParent: fixture.pane.RuntimeParent,
		IssueNum: fixture.pane.IssueNum, Slug: fixture.pane.Slug,
		BranchName: fixture.pane.BranchName, FullBranchRef: "refs/heads/" + fixture.pane.BranchName,
		BaseBranch: fixture.pane.BaseBranch, BaseSHA: head, ExpectedHead: head,
		WorktreePath: fixture.pane.WorktreePath, BranchExisted: true,
		WorkspaceLabel: fixture.pane.HerdrWorkspaceLabel,
		Coordinator:    state.HerdrResource{WorkspaceID: "coordinator"},
		Resource:       herdrResourceFromPane(fixture.pane),
		Session:        fixture.pane.HerdrSession, SocketPath: fixture.pane.HerdrSocketPath,
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(),
		Launch: &state.HerdrLaunch{
			Nonce: nonce, Agent: "claude", AgentName: "fanout-" + strings.Repeat("a", 24),
			Executable: "/usr/bin/true", EnvFilePath: envPath, EnvNameCount: 1,
			LauncherReady: true, TokenIssued: true,
		},
	})
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	return worktreeIntentID, envPath
}

func rewriteResidualLaunchLabel(t *testing.T, projectRoot, intentID, label string) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		t.Fatal("residual Herdr launch intent is missing")
	}
	intent.WorkspaceLabel = label
	intent.Resource.Label = label
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
}

func runHerdrLifecycleGit(t *testing.T, root string, args ...string) {
	t.Helper()
	_ = runHerdrLifecycleGitOutput(t, root, args...)
}

func removeHerdrLifecycleResources(t *testing.T, fixture herdrLifecycleFixture) {
	t.Helper()
	runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
	runHerdrLifecycleGit(t, fixture.projectRoot, "branch", "-D", fixture.branch)
}

func runHerdrLifecycleGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func recordLifecyclePaneReplacing(t *testing.T, projectRoot string, pane state.Pane) {
	t.Helper()
	locked, err := state.Lock(state.Path(projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.RecordPane(pane); err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func replaceLifecyclePanes(t *testing.T, projectRoot string, panes ...state.Pane) {
	t.Helper()
	locked, err := state.Lock(state.Path(projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	locked.Panes = append([]state.Pane(nil), panes...)
	if err := locked.Save(); err != nil {
		t.Fatal(errors.Join(err, locked.Unlock()))
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func assertHerdrLifecycleRemoved(t *testing.T, fixture herdrLifecycleFixture) {
	t.Helper()
	if _, err := os.Stat(fixture.worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after cleanup: %v", err)
	}
	store, err := state.Load(state.Path(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := store.Find(fixture.pane.Parent, fixture.pane.IssueNum); found {
		t.Fatal("state row remains after confirmed Herdr cleanup")
	}
	journal, err := state.LoadHerdrIntents(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range journal.Intents {
		if intent.Kind != state.HerdrIntentCoordinator {
			t.Fatalf("child lifecycle intent remains after completion: %#v", journal.Intents)
		}
	}
}

func assertHerdrLifecyclePreserved(t *testing.T, fixture herdrLifecycleFixture) {
	t.Helper()
	if _, err := os.Stat(fixture.worktreePath); err != nil {
		t.Fatalf("worktree was not preserved: %v", err)
	}
	store, err := state.Load(state.Path(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := store.Find(fixture.pane.Parent, fixture.pane.IssueNum); !found {
		t.Fatal("state row was not preserved")
	}
}

func assertHerdrStateRowPresent(t *testing.T, fixture herdrLifecycleFixture) {
	t.Helper()
	store, err := state.Load(state.Path(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := store.Find(fixture.pane.Parent, fixture.pane.IssueNum); !found {
		t.Fatal("state row was not preserved")
	}
}

func assertHerdrCleanupIntentStatus(
	t *testing.T,
	fixture herdrLifecycleFixture,
	want state.HerdrIntentStatus,
	wantFound bool,
) {
	t.Helper()
	_, intentID, err := herdrCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := state.LoadHerdrIntents(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if found != wantFound {
		t.Fatalf("cleanup intent found = %t, want %t", found, wantFound)
	}
	if found && (intent.Status != want || intent.Failure != "") {
		t.Fatalf("cleanup intent status/failure = %q/%q, want %q/empty", intent.Status, intent.Failure, want)
	}
}
