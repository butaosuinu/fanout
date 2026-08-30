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

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type fakeHerdrLifecycleRuntime struct {
	projectRoot              string
	workspaces               []backend.WorkspaceObservation
	verifyErr                error
	verifyErrAtCall          int
	setupErr                 error
	createErr                error
	openErr                  error
	removeErr                error
	closeErr                 error
	observeErr               error
	observeErrAtCall         int
	observeAfterMutationErr  error
	keepWorkspaceAfterRemove bool
	mutationDispatched       bool
	session                  string
	socketPath               string

	createCalls  int
	verifyCalls  int
	setupCalls   int
	openCalls    int
	removeCalls  int
	closeCalls   int
	observeCalls int
	afterRemove  func()
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

func (f *fakeHerdrLifecycleRuntime) WorktreeRoute(ctx context.Context) (backend.OwnedWorktreeRoute, error) {
	identity, err := worktree.ResolveRepoIdentity(ctx, f.projectRoot)
	if err != nil {
		return backend.OwnedWorktreeRoute{}, err
	}
	return backend.OwnedWorktreeRoute{
		GitCommonDir: identity.RepoKey,
		Session:      f.session,
		SocketPath:   f.socketPath,
	}, nil
}

func (f *fakeHerdrLifecycleRuntime) CreateWorkspace(
	ctx context.Context,
	req backend.WorkspaceCreateRequest,
) (backend.WorktreeMutationResult, error) {
	f.createCalls++
	if mutationDefinitelyNotIssued(f.createErr) || errors.Is(f.createErr, backend.ErrMutationRejected) {
		return backend.WorktreeMutationResult{}, f.createErr
	}
	identity, err := worktree.ResolveRepoIdentity(ctx, f.projectRoot)
	if err != nil {
		return backend.WorktreeMutationResult{}, err
	}
	workspace := herdrLifecycleWorkspace(
		"w-coordinator-recreated", req.Label, req.CWD, req.SourceRepoKey, identity.RepoRoot,
	)
	workspace.Path = ""
	workspace.RepoKey = ""
	workspace.RepoRoot = ""
	f.workspaces = append(f.workspaces, workspace)
	if f.createErr != nil {
		f.mutationDispatched = true
	}
	return backend.WorktreeMutationResult{WorkspaceObservation: workspace}, f.createErr
}

func (f *fakeHerdrLifecycleRuntime) CreateWorktree(
	context.Context,
	backend.WorktreeCreateRequest,
) (backend.WorktreeMutationResult, error) {
	return backend.WorktreeMutationResult{}, errors.New("unexpected worktree create")
}

func (f *fakeHerdrLifecycleRuntime) ObserveWorkspaces(context.Context) ([]backend.WorkspaceObservation, error) {
	f.observeCalls++
	if f.observeErrAtCall > 0 && f.observeErrAtCall == f.observeCalls {
		return nil, f.observeErr
	}
	if f.mutationDispatched && f.observeAfterMutationErr != nil {
		return nil, f.observeAfterMutationErr
	}
	return append([]backend.WorkspaceObservation(nil), f.workspaces...), nil
}

func (f *fakeHerdrLifecycleRuntime) OpenWorktree(_ context.Context, req backend.WorktreeOpenRequest) (backend.WorktreeMutationResult, error) {
	f.openCalls++
	if mutationDefinitelyNotIssued(f.openErr) {
		return backend.WorktreeMutationResult{}, f.openErr
	}
	workspace := herdrLifecycleWorkspace("w-reopened", req.Label, req.Path, req.SourceRepoKey, req.SourceRepoRoot)
	f.workspaces = append(f.workspaces, workspace)
	f.mutationDispatched = true
	return backend.WorktreeMutationResult{WorkspaceObservation: workspace}, f.openErr
}

func (f *fakeHerdrLifecycleRuntime) RemoveWorktree(ctx context.Context, workspaceID, path string) error {
	f.removeCalls++
	if mutationDefinitelyNotIssued(f.removeErr) {
		return f.removeErr
	}
	cmd := exec.CommandContext(ctx, "git", "-C", f.projectRoot, "worktree", "remove", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.Join(f.removeErr, errors.New(strings.TrimSpace(string(out))), err)
	}
	if f.afterRemove != nil {
		f.afterRemove()
	}
	f.mutationDispatched = true
	if !f.keepWorkspaceAfterRemove {
		f.removeWorkspace(workspaceID)
	}
	return f.removeErr
}

func (f *fakeHerdrLifecycleRuntime) CloseWorkspace(_ context.Context, workspaceID string) error {
	f.closeCalls++
	if mutationDefinitelyNotIssued(f.closeErr) {
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

func (f *fakeHerdrLifecycleRuntime) setMutationError(phase state.CleanupPhase, err error) {
	switch phase {
	case state.CleanupReopen:
		f.openErr = err
	case state.CleanupRemove:
		f.removeErr = err
	case state.CleanupWorkspaceClose:
		f.closeErr = err
	}
}

func (f *fakeHerdrLifecycleRuntime) phaseMutationCalls(phase state.CleanupPhase) int {
	switch phase {
	case state.CleanupReopen:
		return f.openCalls
	case state.CleanupRemove:
		return f.removeCalls
	case state.CleanupWorkspaceClose:
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
	workspace    backend.WorkspaceObservation
}

func TestHerdrCloseRemovesOwnedWorktreeAndStateButKeepsBranch(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
		workspaces:  []backend.WorkspaceObservation{workspace},
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
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
	fixture.pane.WorkspaceLabel = ""
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
		workspaces:  []backend.WorkspaceObservation{foreignHerdrWorkspaceAtSameCheckout(fixture)},
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
		workspaces:  []backend.WorkspaceObservation{foreignHerdrWorkspaceAtSameCheckout(fixture)},
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
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	foreign := foreignHerdrWorkspaceAtSameCheckout(fixture)
	predicate := workspaceLabelPredicate(
		fixture.workspace.Label,
		fixture.worktreePath,
		fixture.pane.RepoKey,
		fixture.pane.RepoRoot,
	)
	for name, workspaces := range map[string][]backend.WorkspaceObservation{
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
		workspaces:               []backend.WorkspaceObservation{fixture.workspace},
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
		fixture.pane.RepoKey, fixture.pane.RepoRoot)
	coordinator.Pane.Pane = "w-coordinator:p1"
	coordinator.TerminalID = "terminal-coordinator"
	coordinator.CWD = fixture.projectRoot
	coordinator.Panes = []backend.WorkspacePaneObservation{{
		Pane: coordinator.Pane, TerminalID: coordinator.TerminalID, CWD: fixture.projectRoot,
	}}
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	coordinator.Panes = append(coordinator.Panes, backend.WorkspacePaneObservation{
		Pane:       backend.PaneRef{Backend: backend.Herdr, Workspace: coordinator.WorkspaceID, Pane: "w-coordinator:p2"},
		TerminalID: "terminal-coordinator-extra", CWD: filepath.Join(fixture.projectRoot, "subdir"),
	})
	coordinator.Pane = backend.PaneRef{}
	coordinator.TerminalID = ""
	coordinator.CWD = ""
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{coordinator},
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

func TestHerdrCloseRejectsCheckoutOnlyContentsBeforeReopen(t *testing.T) {
	tests := []struct {
		name      string
		wantError string
		prepare   func(*testing.T, string)
	}{
		{
			name:      "dirty",
			wantError: "tracked or untracked changes",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "ignored-only",
			wantError: "ignored files only",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runHerdrLifecycleGit(t, path, "add", ".gitignore")
				runHerdrLifecycleGit(t, path, "commit", "-m", "ignore dependencies")
				ignoredPath := filepath.Join(path, "node_modules", "pkg")
				if err := os.MkdirAll(ignoredPath, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(ignoredPath, "index.js"), []byte("ignored\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHerdrLifecycleFixture(t)
			tt.prepare(t, fixture.worktreePath)
			runtime := prepareHerdrCleanupPhase(t, fixture, state.CleanupReopen)
			lg := &captureLogger{}

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, lg); got != exitcode.Env {
				t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
			}
			assertHerdrLifecyclePreserved(t, fixture)
			assertHerdrCleanupIntentStatus(t, fixture, state.IntentPlanned, true)
			if runtime.setupCalls != 0 || runtime.openCalls != 0 || runtime.removeCalls != 0 || runtime.closeCalls != 0 {
				t.Fatalf(
					"checkout-only cleanup calls = setup %d/open %d/remove %d/close %d, want 0/0/0/0",
					runtime.setupCalls, runtime.openCalls, runtime.removeCalls, runtime.closeCalls,
				)
			}
			if len(lg.errors) == 0 || !strings.Contains(lg.errors[len(lg.errors)-1], tt.wantError) {
				t.Fatalf("checkout-only cleanup errors = %v, want %q", lg.errors, tt.wantError)
			}
		})
	}
}

func TestHerdrCloseReusesIssueCoordinatorAcrossLinkedPlanOwners(t *testing.T) {
	base := newHerdrLifecycleFixture(t)
	secondRoot := filepath.Join(t.TempDir(), "second-plan")
	runHerdrLifecycleGit(t, base.projectRoot, "worktree", "add", "-b", "plan-second-owner", secondRoot, "HEAD")
	childPath := filepath.Join(secondRoot, ".fanout", "worktrees", "shared-child")
	if err := os.MkdirAll(filepath.Dir(childPath), 0o755); err != nil {
		t.Fatal(err)
	}
	branch := "fanout/shared-child"
	runHerdrLifecycleGit(t, secondRoot, "worktree", "add", "-b", branch, childPath)
	identity, err := worktree.ResolveRepoIdentity(context.Background(), secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	workspace := herdrLifecycleWorkspace("w-shared-child", "shared-child-label", childPath, identity.RepoKey, identity.RepoRoot)
	pane := state.Pane{
		Parent: "plan:second", RuntimeParent: "425", TaskID: "shared-child", Slug: "shared-child",
		Backend: backend.Herdr, PaneID: workspace.Pane.Pane,
		WorkspaceID: workspace.WorkspaceID, WorkspaceLabel: workspace.Label,
		TerminalID: workspace.TerminalID, RepoKey: identity.RepoKey,
		RepoRoot: identity.RepoRoot, SessionID: "fanout-owned",
		SocketPath: "/tmp/fanout-owned.sock", BranchName: branch,
		BaseBranch: "main", WorktreePath: childPath,
	}
	recordLifecyclePane(t, secondRoot, pane)
	fixture := herdrLifecycleFixture{
		projectRoot: secondRoot, worktreePath: childPath, branch: branch,
		pane: pane, workspace: workspace,
	}
	coordinator := herdrLifecycleWorkspace(
		"w-coordinator", "coordinator-label", base.projectRoot, identity.RepoKey, identity.RepoRoot,
	)
	firstPlan := pane
	firstPlan.Parent = "plan:first"
	recordLifecycleCoordinatorIntent(t, base.projectRoot, firstPlan, coordinator)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: secondRoot,
		workspaces:  []backend.WorkspaceObservation{coordinator},
	}

	lg := &captureLogger{}
	if got := CloseTask(herdrLifecycleOptions(fixture, runtime), pane.Parent, pane.TaskID, lg); got != exitcode.OK {
		t.Fatalf("CloseTask() = %d, want %d; errors=%v", got, exitcode.OK, lg.errors)
	}
	assertHerdrLifecycleRemoved(t, fixture)
	if runtime.openCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf("shared coordinator calls = open %d/remove %d, want 1/1", runtime.openCalls, runtime.removeCalls)
	}
}

func TestFindHerdrCoordinatorIntentPreservesPlanOwnerScope(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	target := fixture.pane
	target.Parent = "plan:demo"
	coordinator := herdrLifecycleWorkspace("w-coordinator", "coordinator-label", fixture.projectRoot,
		target.RepoKey, target.RepoRoot)
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

	intent, err := findCoordinatorIntent(locked, fixture.projectRoot, target)
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
		workspaces:  []backend.WorkspaceObservation{workspace},
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
		workspaces:  []backend.WorkspaceObservation{workspace},
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
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
				workspaces:      []backend.WorkspaceObservation{fixture.workspace},
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
					workspaces:  []backend.WorkspaceObservation{fixture.workspace},
				}
			},
			beforeTry: func(*fakeHerdrLifecycleRuntime) {},
			wantRetry: exitcode.Env,
		},
		{
			name: "issued reopen",
			prepare: func(t *testing.T, fixture herdrLifecycleFixture) *fakeHerdrLifecycleRuntime {
				t.Helper()
				runtime := prepareHerdrCleanupPhase(t, fixture, state.CleanupReopen)
				runtime.observeAfterMutationErr = errors.New("observation temporarily unavailable")
				return runtime
			},
			beforeTry: func(runtime *fakeHerdrLifecycleRuntime) {
				runtime.observeAfterMutationErr = nil
			},
			wantRetry: exitcode.OK,
		},
		{
			name: "checkout-only content gate",
			prepare: func(t *testing.T, fixture herdrLifecycleFixture) *fakeHerdrLifecycleRuntime {
				t.Helper()
				if err := os.WriteFile(filepath.Join(fixture.worktreePath, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return prepareHerdrCleanupPhase(t, fixture, state.CleanupReopen)
			},
			beforeTry: func(*fakeHerdrLifecycleRuntime) {},
			wantRetry: exitcode.Env,
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

func TestHerdrClosePreservesUserChangesBeforeMutation(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}
	lg := &captureLogger{}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, lg); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrLifecyclePreserved(t, fixture)
	if runtime.removeCalls != 0 {
		t.Fatalf("dirty cleanup remove calls = %d, want 0", runtime.removeCalls)
	}
	if len(lg.errors) == 0 || !strings.Contains(lg.errors[len(lg.errors)-1], "tracked or untracked changes") {
		t.Fatalf("dirty cleanup errors = %v", lg.errors)
	}
	assertHerdrCleanupIntentStatus(t, fixture, state.IntentPlanned, true)

	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "tracked.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "preserve tracked work")
	runtime.workspaces = []backend.WorkspaceObservation{movedHerdrWorkspace(fixture, "w-moved")}
	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.removeCalls != 1 {
		t.Fatalf("retry cleanup remove calls = %d, want 1", runtime.removeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseReplanRejectsDuplicateWorkspaceLabels(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "tracked.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "preserve tracked work")
	moved := movedHerdrWorkspace(fixture, "w-moved")
	duplicate := movedHerdrWorkspace(fixture, "w-duplicate")
	runtime.workspaces = []backend.WorkspaceObservation{moved, duplicate}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("duplicate-label cleanup calls = remove %d/close %d, want 0/0", runtime.removeCalls, runtime.closeCalls)
	}
	assertHerdrLifecyclePreserved(t, fixture)
	assertHerdrCleanupIntentStatus(t, fixture, state.IntentPlanned, true)
}

func TestHerdrCloseRetriesMovedWorkspaceAfterReplanObservationFailure(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}
	opts := herdrLifecycleOptions(fixture, runtime)

	if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "tracked.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "preserve tracked work")
	runtime.workspaces = []backend.WorkspaceObservation{movedHerdrWorkspace(fixture, "w-moved")}
	runtime.observeErr = errors.New("observation temporarily unavailable")
	runtime.observeErrAtCall = runtime.observeCalls + 3

	if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 0 {
		t.Fatalf("failed observation issued %d remove(s)", runtime.removeCalls)
	}
	assertHerdrCleanupIntentStatus(t, fixture, state.IntentPlanned, true)
	runtime.observeErrAtCall = 0

	if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("second retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.removeCalls != 1 {
		t.Fatalf("second retry cleanup remove calls = %d, want 1", runtime.removeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseReplansMovedWorkspaceWithoutTopLevelPane(t *testing.T) {
	for _, tt := range []struct {
		name      string
		workspace func(herdrLifecycleFixture) backend.WorkspaceObservation
	}{
		{name: "pane-less", workspace: movedPaneLessHerdrWorkspace},
		{name: "multi-pane", workspace: movedMultiPaneHerdrWorkspace},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHerdrLifecycleFixture(t)
			if err := os.WriteFile(filepath.Join(fixture.worktreePath, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runtime := &fakeHerdrLifecycleRuntime{
				projectRoot: fixture.projectRoot,
				workspaces:  []backend.WorkspaceObservation{fixture.workspace},
			}

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
				t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
			}
			runHerdrLifecycleGit(t, fixture.worktreePath, "add", "tracked.txt")
			runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "preserve tracked work")
			runtime.workspaces = []backend.WorkspaceObservation{tt.workspace(fixture)}

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
				t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
			}
			if runtime.removeCalls != 1 {
				t.Fatalf("retry cleanup remove calls = %d, want 1", runtime.removeCalls)
			}
			assertHerdrLifecycleRemoved(t, fixture)
		})
	}
}

func TestHerdrCloseReportsIgnoredOnlyCheckoutBeforeMutation(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", ".gitignore")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "ignore dependencies")
	ignoredPath := filepath.Join(fixture.worktreePath, "node_modules", "pkg")
	if err := os.MkdirAll(ignoredPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignoredPath, "index.js"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}
	lg := &captureLogger{}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, lg); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 0 {
		t.Fatalf("ignored-only cleanup remove calls = %d, want 0", runtime.removeCalls)
	}
	if len(lg.errors) == 0 || !strings.Contains(lg.errors[len(lg.errors)-1], "ignored files only") {
		t.Fatalf("ignored-only cleanup errors = %v", lg.errors)
	}
	assertHerdrCleanupIntentStatus(t, fixture, state.IntentPlanned, true)

	if err := os.RemoveAll(filepath.Join(fixture.worktreePath, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
	}
}

func TestHerdrCloseReplansLegacyDirtyManualIntent(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "committed.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "committed.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "preserve work before cleanup")
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{movedHerdrWorkspace(fixture, "w-moved")},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.removeCalls != 1 {
		t.Fatalf("replanned cleanup remove calls = %d, want 1", runtime.removeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseReplansLegacyDirtyManualIntentAfterCoordinatorRecreate(t *testing.T) {
	fixture := newManualHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace(
		"w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	dirtyPath := filepath.Join(fixture.worktreePath, "uncommitted.txt")
	if err := os.WriteFile(dirtyPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		session:     fixture.pane.SessionID,
		socketPath:  fixture.pane.SocketPath,
	}
	lg := &captureLogger{}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, lg); got != exitcode.Env {
		t.Fatalf("dirty Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.createCalls != 1 || runtime.openCalls != 0 || runtime.removeCalls != 0 {
		t.Fatalf(
			"dirty replan calls = create %d/open %d/remove %d, want 1/0/0",
			runtime.createCalls, runtime.openCalls, runtime.removeCalls,
		)
	}
	if len(lg.errors) == 0 || !strings.Contains(lg.errors[len(lg.errors)-1], "tracked or untracked changes") {
		t.Fatalf("dirty replan errors = %v", lg.errors)
	}

	if err := os.Remove(dirtyPath); err != nil {
		t.Fatal(err)
	}
	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("clean retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.createCalls != 1 || runtime.openCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf(
			"clean retry calls = create %d/open %d/remove %d, want 1/1/1",
			runtime.createCalls, runtime.openCalls, runtime.removeCalls,
		)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrClosePersistsRecreatedManualCoordinatorOnCleanLegacyReplan(t *testing.T) {
	fixture := newManualHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace(
		"w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		session:     fixture.pane.SessionID,
		socketPath:  fixture.pane.SocketPath,
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.createCalls != 1 || runtime.openCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf(
			"clean replan calls = create %d/open %d/remove %d, want 1/1/1",
			runtime.createCalls, runtime.openCalls, runtime.removeCalls,
		)
	}
	assertRecreatedManualCoordinatorRecorded(t, fixture, coordinator)
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseRecreatesAfterSavedManualCoordinatorMovedAndClosed(t *testing.T) {
	fixture := newManualHerdrLifecycleFixture(t)
	coordinatorA := herdrLifecycleWorkspace(
		"w-coordinator-a", "coordinator-label", fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinatorA)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	dirtyPath := filepath.Join(fixture.worktreePath, "uncommitted.txt")
	if err := os.WriteFile(dirtyPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{coordinatorA},
		session:     fixture.pane.SessionID,
		socketPath:  fixture.pane.SocketPath,
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("dirty Close() = %d, want %d", got, exitcode.Env)
	}
	coordinatorB := herdrLifecycleWorkspace(
		"w-coordinator-b", coordinatorA.Label, fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinatorB)
	runtime.workspaces = nil
	if err := os.Remove(dirtyPath); err != nil {
		t.Fatal(err)
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.createCalls != 1 || runtime.openCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf(
			"moved coordinator retry calls = create %d/open %d/remove %d, want 1/1/1",
			runtime.createCalls, runtime.openCalls, runtime.removeCalls,
		)
	}
	assertRecreatedManualCoordinatorRecorded(t, fixture, coordinatorB)
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseRecreatesManualCoordinatorForPlannedReopenRetry(t *testing.T) {
	for _, tt := range []struct {
		name    string
		expired bool
	}{
		{name: "active"},
		{name: "expired", expired: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newManualHerdrLifecycleFixture(t)
			coordinator := herdrLifecycleWorkspace(
				"w-coordinator", "coordinator-label", fixture.projectRoot,
				fixture.pane.RepoKey, fixture.pane.RepoRoot,
			)
			recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
			if tt.expired {
				recordExpiredHerdrCleanupIntent(t, fixture, state.CleanupReopen)
			} else {
				recordActiveHerdrCleanupIntent(t, fixture, state.CleanupReopen)
			}
			replaceSavedHerdrCleanupCoordinator(t, fixture, coordinatorResource(coordinator))
			runtime := &fakeHerdrLifecycleRuntime{
				projectRoot: fixture.projectRoot,
				session:     fixture.pane.SessionID,
				socketPath:  fixture.pane.SocketPath,
			}

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
				t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
			}
			if runtime.createCalls != 1 || runtime.openCalls != 1 || runtime.removeCalls != 1 {
				t.Fatalf(
					"reopen retry calls = create %d/open %d/remove %d, want 1/1/1",
					runtime.createCalls, runtime.openCalls, runtime.removeCalls,
				)
			}
			assertRecreatedManualCoordinatorRecorded(t, fixture, coordinator)
			assertHerdrLifecycleRemoved(t, fixture)
		})
	}
}

func TestHerdrCloseRetriesAfterRecreatedManualCoordinatorObservationFailure(t *testing.T) {
	fixture := newManualHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace(
		"w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot:      fixture.projectRoot,
		observeErr:       errors.New("coordinator observation failed"),
		observeErrAtCall: 4,
		session:          fixture.pane.SessionID,
		socketPath:       fixture.pane.SocketPath,
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("observation failure Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.createCalls != 1 || runtime.openCalls != 0 || runtime.removeCalls != 0 {
		t.Fatalf(
			"observation failure calls = create %d/open %d/remove %d, want 1/0/0",
			runtime.createCalls, runtime.openCalls, runtime.removeCalls,
		)
	}
	assertSingleManualCoordinatorRow(t, fixture, "w-coordinator-recreated")
	runtime.observeErrAtCall = 0

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.createCalls != 1 || runtime.openCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf(
			"observation retry calls = create %d/open %d/remove %d, want 1/1/1",
			runtime.createCalls, runtime.openCalls, runtime.removeCalls,
		)
	}
	assertSingleManualCoordinatorRow(t, fixture, "w-coordinator-recreated")
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseKeepsRejectingUnconfirmedManualCoordinatorRow(t *testing.T) {
	fixture := newManualHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace(
		"w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	stale := manualLifecycleCoordinatorPane(fixture.pane, coordinator, fixture.pane.IssueNum-1)
	stale.WorkspaceID = "foreign"
	replaceLifecyclePanes(t, fixture.projectRoot, fixture.pane, stale)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		session:     fixture.pane.SessionID,
		socketPath:  fixture.pane.SocketPath,
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
			t.Fatalf("Close() attempt %d = %d, want %d", attempt, got, exitcode.Env)
		}
		if runtime.createCalls != 1 || runtime.openCalls != 0 || runtime.removeCalls != 0 {
			t.Fatalf(
				"attempt %d calls = create %d/open %d/remove %d, want 1/0/0",
				attempt, runtime.createCalls, runtime.openCalls, runtime.removeCalls,
			)
		}
		assertSingleManualCoordinatorRow(t, fixture, "foreign")
		assertHerdrLifecyclePreserved(t, fixture)
	}
}

func TestHerdrCloseRetriesReleasedManualCoordinatorCreate(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "not issued", err: backend.MutationNotIssuedError{Cause: errors.New("disconnected")}},
		{name: "rejected", err: backend.MutationRejectedError{Code: "workspace_create_failed", Message: "rejected"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newManualHerdrLifecycleFixture(t)
			coordinator := herdrLifecycleWorkspace(
				"w-coordinator", "coordinator-label", fixture.projectRoot,
				fixture.pane.RepoKey, fixture.pane.RepoRoot,
			)
			recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
			recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
			runtime := &fakeHerdrLifecycleRuntime{
				projectRoot: fixture.projectRoot, createErr: tt.err,
				session: fixture.pane.SessionID, socketPath: fixture.pane.SocketPath,
			}

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
				t.Fatalf("failed create Close() = %d, want %d", got, exitcode.Env)
			}
			assertManualCoordinatorIntentStatus(t, fixture, "", false)
			runtime.createErr = nil
			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
				t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
			}
			if runtime.createCalls != 2 || runtime.openCalls != 1 || runtime.removeCalls != 1 {
				t.Fatalf(
					"retry calls = create %d/open %d/remove %d, want 2/1/1",
					runtime.createCalls, runtime.openCalls, runtime.removeCalls,
				)
			}
			assertRecreatedManualCoordinatorRecorded(t, fixture, coordinator)
			assertHerdrLifecycleRemoved(t, fixture)
		})
	}
}

func TestHerdrCloseResumesIssuedManualCoordinatorCreate(t *testing.T) {
	fixture := newManualHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace(
		"w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot:             fixture.projectRoot,
		createErr:               errors.New("create response lost"),
		observeAfterMutationErr: errors.New("recovery observation failed"),
		session:                 fixture.pane.SessionID,
		socketPath:              fixture.pane.SocketPath,
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("response-loss Close() = %d, want %d", got, exitcode.Env)
	}
	assertManualCoordinatorIntentStatus(t, fixture, state.IntentIssued, true)
	runtime.createErr = nil
	runtime.observeAfterMutationErr = nil
	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.createCalls != 1 || runtime.openCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf(
			"response-loss retry calls = create %d/open %d/remove %d, want 1/1/1",
			runtime.createCalls, runtime.openCalls, runtime.removeCalls,
		)
	}
	assertRecreatedManualCoordinatorRecorded(t, fixture, coordinator)
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseRecreatesPrunedManualCoordinatorRow(t *testing.T) {
	fixture := newManualHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace(
		"w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	replaceLifecyclePanes(t, fixture.projectRoot, fixture.pane)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		session:     fixture.pane.SessionID,
		socketPath:  fixture.pane.SocketPath,
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	assertRecreatedManualCoordinatorRecorded(t, fixture, coordinator)
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseDoesNotRecreateAmbiguousManualCoordinator(t *testing.T) {
	fixture := newManualHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace(
		"w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.RepoKey, fixture.pane.RepoRoot,
	)
	recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	coordinator.Label = "changed-coordinator-label"
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{coordinator},
		session:     fixture.pane.SessionID,
		socketPath:  fixture.pane.SocketPath,
	}
	lg := &captureLogger{}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, lg); got != exitcode.Env {
		t.Fatalf(
			"Close() = %d, want %d; calls=create %d/open %d/remove %d; errors=%v",
			got, exitcode.Env, runtime.createCalls, runtime.openCalls, runtime.removeCalls, lg.errors,
		)
	}
	if runtime.createCalls != 0 || runtime.openCalls != 0 || runtime.removeCalls != 0 {
		t.Fatalf(
			"ambiguous coordinator calls = create %d/open %d/remove %d, want 0/0/0; errors=%v",
			runtime.createCalls, runtime.openCalls, runtime.removeCalls, lg.errors,
		)
	}
	assertHerdrLifecyclePreserved(t, fixture)
}

func TestHerdrCloseReplansLegacyDirtyManualIntentAfterManualWorktreeRemoval(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	recordManualHerdrCleanupIntent(t, fixture, legacyDirtyWorktreeFailure())
	runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.removeCalls != 0 || runtime.closeCalls != 1 {
		t.Fatalf("residual cleanup calls = remove %d/close %d, want 0/1", runtime.removeCalls, runtime.closeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseDoesNotReplanMalformedManualIntentContainingDirtyCode(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	recordManualHerdrCleanupIntent(
		t,
		fixture,
		"response lost after dirty_worktree_requires_force",
	)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("malformed manual cleanup calls = remove %d/close %d, want 0/0", runtime.removeCalls, runtime.closeCalls)
	}
	assertHerdrLifecyclePreserved(t, fixture)
}

func TestLegacyDirtyWorktreeRejectionRequiresCompleteEnvelope(t *testing.T) {
	valid := legacyDirtyWorktreeFailure()
	captured := capturedLegacyDirtyWorktreeFailure(t)
	tests := []struct {
		name    string
		phase   state.CleanupPhase
		failure string
		want    bool
	}{
		{name: "valid", phase: state.CleanupRemove, failure: valid, want: true},
		{name: "captured from herdr 0.8.2", phase: state.CleanupRemove, failure: captured, want: true},
		{
			name:  "unknown fields",
			phase: state.CleanupRemove,
			failure: strings.Replace(
				valid,
				`"message":"checkout has changes"},"id"`,
				`"message":"checkout has changes","details":{"dirty_paths":1}},"hint":"use --force","id"`,
				1,
			),
			want: true,
		},
		{name: "null result", phase: state.CleanupRemove, failure: strings.Replace(valid, `,"id"`, `,"result":null,"id"`, 1), want: true},
		{name: "wrong phase", phase: state.CleanupWorkspaceClose, failure: valid},
		{name: "wrong id", phase: state.CleanupRemove, failure: strings.Replace(valid, "cli:worktree:remove", "cli:worktree:create", 1)},
		{name: "wrong code", phase: state.CleanupRemove, failure: strings.Replace(valid, "dirty_worktree_requires_force", "workspace_not_found", 1)},
		{name: "empty message", phase: state.CleanupRemove, failure: strings.Replace(valid, "checkout has changes", "", 1)},
		{name: "result present", phase: state.CleanupRemove, failure: strings.Replace(valid, `,"id"`, `,"result":{},"id"`, 1)},
		{name: "malformed", phase: state.CleanupRemove, failure: "response lost after dirty_worktree_requires_force"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := state.LaunchIntent{CleanupPhase: tt.phase, Failure: tt.failure}
			if got := isLegacyDirtyWorktreeRejection(intent); got != tt.want {
				t.Fatalf("isLegacyDirtyWorktreeRejection() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestHerdrCloseAcceptsResponseLossOnlyAfterAbsence(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
		workspaces:               []backend.WorkspaceObservation{fixture.workspace},
		removeErr:                errors.New("response lost"),
		keepWorkspaceAfterRemove: true,
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 1 || runtime.closeCalls != 0 {
		t.Fatalf("ambiguous cleanup calls = remove %d/close %d, want 1/0", runtime.removeCalls, runtime.closeCalls)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found || intent.Status != state.IntentManualCleanupRequired {
		t.Fatalf("ambiguous cleanup intent = %#v (found=%t), want manual cleanup", intent, found)
	}
	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 1 || runtime.closeCalls != 0 {
		t.Fatalf("retry ambiguous cleanup calls = remove %d/close %d, want 1/0", runtime.removeCalls, runtime.closeCalls)
	}
}

func TestHerdrCloseRemovesResidualLaunchIntentAndEnvironment(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtimeDir := filepath.Join(fixture.projectRoot, "herdr-runtime")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "workload-env"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.pane.SocketPath = filepath.Join(runtimeDir, "herdr.sock")
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	worktreeIntentID, envPath := recordResidualHerdrLaunchIntent(t, fixture, runtimeDir)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}

	if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Close() = %d, want %d", got, exitcode.OK)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
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
	fixture.pane.SocketPath = filepath.Join(runtimeDir, "herdr.sock")
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	worktreeIntentID, _ := recordResidualHerdrLaunchIntent(t, fixture, runtimeDir)
	rewriteResidualLaunchLabel(t, fixture.projectRoot, worktreeIntentID, "foreign-workspace")
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(fixture.projectRoot)
	if err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	journal.UpsertIntent(state.LaunchIntent{
		ID: intentID, Kind: state.IntentCleanup, Status: state.IntentIssued,
		Parent: fixture.pane.Parent, RuntimeParent: fixture.pane.RuntimeParent,
		IssueNum: fixture.pane.IssueNum, Slug: fixture.pane.Slug,
		BranchName: fixture.branch, FullBranchRef: "refs/heads/" + fixture.branch,
		BaseBranch: fixture.pane.BaseBranch, ExpectedHead: head,
		WorktreePath: fixture.worktreePath, BranchExisted: true,
		WorkspaceLabel: fixture.workspace.Label, Resource: resourceFromPane(fixture.pane),
		Session: fixture.pane.SessionID, SocketPath: fixture.pane.SocketPath,
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(), CleanupPhase: state.CleanupRemove,
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
	intent := state.LaunchIntent{
		Kind: state.IntentCleanup, Parent: fixture.pane.Parent,
		RuntimeParent: fixture.pane.RuntimeParent, IssueNum: fixture.pane.IssueNum,
		Slug: fixture.pane.Slug, BranchName: fixture.pane.BranchName,
		FullBranchRef: "refs/heads/" + fixture.pane.BranchName, BaseBranch: fixture.pane.BaseBranch,
		WorktreePath: fixture.pane.WorktreePath, BranchExisted: true,
		WorkspaceLabel: fixture.pane.WorkspaceLabel, Resource: resourceFromPane(fixture.pane),
		Session: fixture.pane.SessionID, SocketPath: fixture.pane.SocketPath,
	}
	intent.Resource.TerminalID = "reused-terminal"
	if err := validateSavedWorkspaceCleanup(intent, fixture.projectRoot, fixture.pane, CloseWorktree); err == nil {
		t.Fatal("saved cleanup accepted a changed runtime identity")
	}
}

func TestSavedHerdrCleanupRejectsDeleteBranchEscalation(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	head := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	intent := state.LaunchIntent{
		Kind: state.IntentCleanup, Parent: fixture.pane.Parent,
		RuntimeParent: fixture.pane.RuntimeParent, IssueNum: fixture.pane.IssueNum,
		Slug: fixture.pane.Slug, BranchName: fixture.pane.BranchName,
		FullBranchRef: "refs/heads/" + fixture.pane.BranchName, BaseBranch: fixture.pane.BaseBranch,
		ExpectedHead: head, WorktreePath: fixture.pane.WorktreePath,
		BranchCreated: true, CleanupDeleteBranch: true,
		WorkspaceLabel: fixture.pane.WorkspaceLabel, Resource: resourceFromPane(fixture.pane),
		Session: fixture.pane.SessionID, SocketPath: fixture.pane.SocketPath,
	}
	if err := validateSavedWorkspaceCleanup(intent, fixture.projectRoot, fixture.pane, CloseWorktree); err == nil {
		t.Fatal("saved cleanup retained branch deletion outside CloseEverything")
	}
}

func TestHerdrCleanupDiscardsPlanAfterDefiniteNonMutation(t *testing.T) {
	errorsByName := map[string]error{
		"not-issued": backend.MutationNotIssuedError{Cause: errors.New("dispatch unavailable")},
		"rejected":   backend.MutationRejectedError{Code: "rejected", Message: "request refused"},
	}
	for _, phase := range []state.CleanupPhase{
		state.CleanupReopen,
		state.CleanupRemove,
		state.CleanupWorkspaceClose,
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

func TestMovedHerdrCleanupRetriesAfterDefiniteNonMutation(t *testing.T) {
	errorsByName := map[string]error{
		"not-issued": backend.MutationNotIssuedError{Cause: errors.New("dispatch unavailable")},
		"rejected":   backend.MutationRejectedError{Code: "rejected", Message: "request refused"},
	}
	for _, phase := range []state.CleanupPhase{state.CleanupRemove, state.CleanupWorkspaceClose} {
		for errorName, mutationErr := range errorsByName {
			t.Run(string(phase)+"/"+errorName, func(t *testing.T) {
				fixture := newHerdrLifecycleFixture(t)
				runtimeDir := filepath.Join(fixture.projectRoot, "herdr-runtime")
				if err := os.MkdirAll(filepath.Join(runtimeDir, "workload-env"), 0o700); err != nil {
					t.Fatal(err)
				}
				fixture.pane.SocketPath = filepath.Join(runtimeDir, "herdr.sock")
				recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
				worktreeIntentID, _ := recordResidualHerdrLaunchIntent(t, fixture, runtimeDir)
				recordActiveHerdrCleanupIntent(t, fixture, phase)
				runtime := prepareHerdrCleanupPhase(t, fixture, phase)
				runtime.workspaces = []backend.WorkspaceObservation{movedHerdrWorkspace(fixture, "w-moved")}
				runtime.setMutationError(phase, mutationErr)

				if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
					t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
				}
				assertHerdrCleanupIntentStatus(t, fixture, "", false)
				assertMovedHerdrCleanupIdentity(t, fixture, worktreeIntentID, "w-moved")

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
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
		removeErr:   backend.MutationRejectedError{Code: "dirty", Message: "request refused"},
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
	for _, phase := range []state.CleanupPhase{
		state.CleanupReopen,
		state.CleanupRemove,
		state.CleanupWorkspaceClose,
	} {
		t.Run(string(phase), func(t *testing.T) {
			fixture := newHerdrLifecycleFixture(t)
			runtime := prepareHerdrCleanupPhase(t, fixture, phase)
			runtime.observeAfterMutationErr = errors.New("observation temporarily unavailable")

			if got := Close(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
				t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
			}
			assertHerdrCleanupIntentStatus(t, fixture, state.IntentIssued, true)
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
	for _, phase := range []state.CleanupPhase{
		state.CleanupReopen,
		state.CleanupRemove,
		state.CleanupWorkspaceClose,
	} {
		t.Run(string(phase), func(t *testing.T) {
			fixture := newHerdrLifecycleFixture(t)
			recordExpiredHerdrCleanupIntent(t, fixture, phase)
			runtime := &fakeHerdrLifecycleRuntime{
				projectRoot: fixture.projectRoot,
				workspaces:  []backend.WorkspaceObservation{fixture.workspace},
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
	recordExpiredHerdrCleanupIntent(t, fixture, state.CleanupRemove)
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

func TestExpiredPlannedHerdrCleanupRebindsMovedWorkspaceWithoutMutation(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtimeDir := filepath.Join(fixture.projectRoot, "herdr-runtime")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "workload-env"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.pane.SocketPath = filepath.Join(runtimeDir, "herdr.sock")
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	worktreeIntentID, _ := recordResidualHerdrLaunchIntent(t, fixture, runtimeDir)
	recordExpiredHerdrCleanupIntent(t, fixture, state.CleanupRemove)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{movedHerdrWorkspace(fixture, "w-moved")},
	}
	opts := herdrLifecycleOptions(fixture, runtime)

	if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.Env {
		t.Fatalf("Close() = %d, want %d", got, exitcode.Env)
	}
	if runtime.setupCalls != 0 || runtime.openCalls != 0 || runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("expired moved cleanup issued mutations: setup %d/open %d/remove %d/close %d", runtime.setupCalls, runtime.openCalls, runtime.removeCalls, runtime.closeCalls)
	}
	assertHerdrCleanupIntentStatus(t, fixture, "", false)
	store, err := state.Load(state.Path(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	pane, found := store.Find(fixture.pane.Parent, fixture.pane.IssueNum)
	if !found || pane.WorkspaceID != "w-moved" {
		t.Fatalf("rebound pane = %#v (found=%t), want workspace w-moved", pane, found)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	launchIntent, found := journal.FindIntent(worktreeIntentID)
	if !found || launchIntent.Resource.WorkspaceID != "w-moved" {
		t.Fatalf("rebound launch intent = %#v (found=%t), want workspace w-moved", launchIntent, found)
	}

	if got := Close(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry Close() = %d, want %d", got, exitcode.OK)
	}
	if runtime.removeCalls != 1 {
		t.Fatalf("retry cleanup remove calls = %d, want 1", runtime.removeCalls)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestExpiredReopenedHerdrCleanupPreservesReplacementIdentityAndRefreshesHead(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := prepareHerdrCleanupPhase(t, fixture, state.CleanupReopen)
	runtime.removeErr = backend.MutationNotIssuedError{Cause: errors.New("remove dispatch unavailable")}

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

func TestHerdrCloseEverythingDeletesUnmergedFanoutCreatedBranchWhenTipMatches(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "unmerged.txt"), []byte("unmerged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLifecycleGit(t, fixture.worktreePath, "add", "unmerged.txt")
	runHerdrLifecycleGit(t, fixture.worktreePath, "commit", "-m", "unmerged child work")
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}

	if got := CloseWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	if localBranchExists(fixture.projectRoot, fixture.branch) {
		t.Fatalf("fanout-created branch %s was not compare-deleted", fixture.branch)
	}
}

func TestHerdrCloseEverythingLeavesMovedFanoutCreatedBranchAndWarns(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	expected := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", fixture.branch))
	tree := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", expected+"^{tree}"))
	moved := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "commit-tree", tree, "-p", expected, "-m", "branch moved"))
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
		afterRemove: func() {
			runHerdrLifecycleGit(t, fixture.projectRoot, "update-ref", "refs/heads/"+fixture.branch, moved, expected)
		},
	}
	logger := &captureLogger{}

	if got := CloseWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, logger); got != exitcode.OK {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	if got := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", fixture.branch)); got != moved {
		t.Fatalf("moved branch tip = %s, want %s", got, moved)
	}
	if warnings := strings.Join(logger.warnings, "\n"); !strings.Contains(warnings, "branch tip moved from "+expected+" to "+moved) ||
		!strings.Contains(warnings, "leaving branch in place") {
		t.Fatalf("warnings = %q, want moved-tip branch preservation warning", warnings)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseEverythingLeavesBranchWhenTipCannotBeConfirmedAndWarns(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	marker := installFailingBranchObservationGit(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
		afterRemove: func() {
			if err := os.WriteFile(marker, []byte("fail\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	logger := &captureLogger{}

	if got := CloseWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, logger); got != exitcode.OK {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	if !localBranchExists(fixture.projectRoot, fixture.branch) {
		t.Fatalf("unconfirmed branch %s was deleted", fixture.branch)
	}
	if warnings := strings.Join(logger.warnings, "\n"); !strings.Contains(warnings, "verify Herdr branch before compare-and-delete") ||
		!strings.Contains(warnings, "leaving branch in place") {
		t.Fatalf("warnings = %q, want unconfirmed branch preservation warning", warnings)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseEverythingLeavesPreexistingBranch(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot: fixture.projectRoot,
		workspaces:  []backend.WorkspaceObservation{fixture.workspace},
	}

	if got := CloseWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	if !localBranchExists(fixture.projectRoot, fixture.branch) {
		t.Fatalf("preexisting branch %s was deleted", fixture.branch)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseEverythingLeavesBranchRecreatedBeforeFreshCleanup(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	removeHerdrLifecycleResources(t, fixture)
	parent := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", parent+"^{tree}"))
	replacement := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "commit-tree", tree, "-p", parent, "-m", "replacement branch"))
	runHerdrLifecycleGit(t, fixture.projectRoot, "update-ref", "refs/heads/"+fixture.branch, replacement)
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot}

	if got := CloseWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	if got := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", fixture.branch)); got != replacement {
		t.Fatalf("recreated branch tip = %s, want %s", got, replacement)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseEverythingDisarmsBranchDeleteWhenCheckoutDisappearsBeforeReplan(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	original := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", fixture.branch))
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot:      fixture.projectRoot,
		workspaces:       []backend.WorkspaceObservation{fixture.workspace},
		observeErr:       errors.New("observation temporarily unavailable"),
		observeErrAtCall: 3,
	}
	opts := herdrLifecycleOptions(fixture, runtime)

	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.Env {
		t.Fatalf("first CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found || !intent.CleanupDeleteBranch || intent.ExpectedHead != original {
		t.Fatalf("initial cleanup intent = %#v (found=%t), want branch deletion fenced to %s", intent, found, original)
	}
	rewriteSavedHerdrCleanupAsLegacy(t, fixture)

	removeHerdrLifecycleResources(t, fixture)
	tree := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", original+"^{tree}"))
	replacement := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "commit-tree", tree, "-p", original, "-m", "replacement branch"))
	runHerdrLifecycleGit(t, fixture.projectRoot, "update-ref", "refs/heads/"+fixture.branch, replacement)
	runtime.observeErrAtCall = 0
	runtime.observeAfterMutationErr = errors.New("post-close observation temporarily unavailable")

	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.Env {
		t.Fatalf("replan CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	journal, err = state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found = journal.FindIntent(intentID)
	if !found || intent.Status != state.IntentIssued || intent.CleanupDeleteBranch ||
		intent.CleanupDeleteBranchRequested == nil || !*intent.CleanupDeleteBranchRequested {
		t.Fatalf("disarmed cleanup intent = %#v (found=%t), want retryable normalized CloseEverything intent", intent, found)
	}

	runtime.observeAfterMutationErr = nil
	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("recovery CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	if runtime.closeCalls != 1 {
		t.Fatalf("workspace close calls = %d, want 1", runtime.closeCalls)
	}
	if got := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", fixture.branch)); got != replacement {
		t.Fatalf("recreated branch tip = %s, want %s", got, replacement)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseEverythingDoesNotRearmBranchDeleteAfterBranchReappears(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	removeHerdrLifecycleResources(t, fixture)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot:      fixture.projectRoot,
		workspaces:       []backend.WorkspaceObservation{fixture.workspace},
		observeErr:       errors.New("observation temporarily unavailable"),
		observeErrAtCall: 3,
	}
	opts := herdrLifecycleOptions(fixture, runtime)

	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.Env {
		t.Fatalf("first CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	runHerdrLifecycleGit(t, fixture.projectRoot, "branch", fixture.branch, "HEAD")
	runtime.observeErrAtCall = runtime.observeCalls + 3
	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.Env {
		t.Fatalf("replan CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found || intent.ExpectedHead == "" || intent.CleanupDeleteBranch {
		t.Fatalf("replanned cleanup intent = %#v (found=%t), want refreshed head without branch deletion", intent, found)
	}

	runtime.observeErrAtCall = 0
	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("retry CloseWithMode() = %d, want %d", got, exitcode.OK)
	}
	if runtime.closeCalls != 1 {
		t.Fatalf("workspace close calls = %d, want 1", runtime.closeCalls)
	}
	if !localBranchExists(fixture.projectRoot, fixture.branch) {
		t.Fatalf("reappeared branch %s was deleted", fixture.branch)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func TestHerdrCloseEverythingRejectsCloseWorktreeIntent(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot:      fixture.projectRoot,
		workspaces:       []backend.WorkspaceObservation{fixture.workspace},
		observeErr:       errors.New("observation temporarily unavailable"),
		observeErrAtCall: 3,
	}
	opts := herdrLifecycleOptions(fixture, runtime)

	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseWorktree, nopLogger{}); got != exitcode.Env {
		t.Fatalf("CloseWorktree CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrCleanupIntentStatus(t, fixture, state.IntentPlanned, true)
	runtime.observeErrAtCall = 0
	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.Env {
		t.Fatalf("CloseEverything CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("mode escalation mutation calls = remove %d/close %d, want 0/0", runtime.removeCalls, runtime.closeCalls)
	}
	if !localBranchExists(fixture.projectRoot, fixture.branch) {
		t.Fatalf("mode escalation deleted branch %s", fixture.branch)
	}
	assertHerdrLifecyclePreserved(t, fixture)
}

func TestHerdrCloseEverythingRejectsWorkspaceCloseWorktreeIntent(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
	recordLifecyclePaneReplacing(t, fixture.projectRoot, fixture.pane)
	runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
	runtime := &fakeHerdrLifecycleRuntime{
		projectRoot:      fixture.projectRoot,
		workspaces:       []backend.WorkspaceObservation{fixture.workspace},
		observeErr:       errors.New("observation temporarily unavailable"),
		observeErrAtCall: 3,
	}
	opts := herdrLifecycleOptions(fixture, runtime)

	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseWorktree, nopLogger{}); got != exitcode.Env {
		t.Fatalf("CloseWorktree CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	assertHerdrCleanupIntentStatus(t, fixture, state.IntentPlanned, true)
	runtime.observeErrAtCall = 0
	if got := CloseWithMode(opts, fixture.pane.Parent, fixture.pane.IssueNum, CloseEverything, nopLogger{}); got != exitcode.Env {
		t.Fatalf("CloseEverything CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	if runtime.removeCalls != 0 || runtime.closeCalls != 0 {
		t.Fatalf("mode escalation mutation calls = remove %d/close %d, want 0/0", runtime.removeCalls, runtime.closeCalls)
	}
	if !localBranchExists(fixture.projectRoot, fixture.branch) {
		t.Fatalf("mode escalation deleted branch %s", fixture.branch)
	}
	assertHerdrStateRowPresent(t, fixture)
	assertHerdrCleanupIntentStatus(t, fixture, state.IntentPlanned, true)
}

func TestHerdrCloseEverythingReapsIssueStateAfterResourcesAreAlreadyAbsent(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	fixture.pane.BranchCreated = true
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
	fixture.pane.BranchCreated = true
	replaceLifecyclePanes(t, fixture.projectRoot, fixture.pane)
	removeHerdrLifecycleResources(t, fixture)
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot}

	if got := CloseTaskWithMode(herdrLifecycleOptions(fixture, runtime), fixture.pane.Parent, fixture.pane.TaskID, CloseEverything, nopLogger{}); got != exitcode.OK {
		t.Fatalf("CloseTaskWithMode() = %d, want %d", got, exitcode.OK)
	}
	assertHerdrLifecycleRemoved(t, fixture)
}

func herdrLifecycleOptions(fixture herdrLifecycleFixture, runtime WorkspaceRuntime) Options {
	return Options{
		ProjectRoot: fixture.projectRoot,
		StatePath:   state.Path(fixture.projectRoot),
		Hooks:       hooks.EmptyConfig(),
		WorkspaceRuntime: func(context.Context, state.Pane) (WorkspaceRuntime, error) {
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
		WorkspaceID: workspace.WorkspaceID, WorkspaceLabel: workspace.Label,
		TerminalID: workspace.TerminalID, RepoKey: identity.RepoKey,
		RepoRoot: identity.RepoRoot, SessionID: "fanout-owned",
		SocketPath: "/tmp/fanout-owned.sock", BranchName: branch,
		BaseBranch: "main", WorktreePath: worktreePath,
	}
	recordLifecyclePane(t, projectRoot, pane)
	return herdrLifecycleFixture{
		projectRoot: projectRoot, worktreePath: worktreePath, branch: branch,
		pane: pane, workspace: workspace,
	}
}

func newManualHerdrLifecycleFixture(t *testing.T) herdrLifecycleFixture {
	t.Helper()
	fixture := newHerdrLifecycleFixture(t)
	projectRoot, err := filepath.EvalSymlinks(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	worktreePath, err := filepath.EvalSymlinks(fixture.worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.projectRoot = projectRoot
	fixture.worktreePath = worktreePath
	fixture.workspace.Path = worktreePath
	fixture.workspace.CWD = worktreePath
	fixture.workspace.Panes[0].CWD = worktreePath
	fixture.pane.Parent = panelaunch.ManualParentRef
	fixture.pane.RuntimeParent = panelaunch.ManualParentRef
	fixture.pane.IssueNum = -1
	fixture.pane.WorktreePath = worktreePath
	replaceLifecyclePanes(t, fixture.projectRoot, fixture.pane)
	return fixture
}

func herdrLifecycleWorkspace(id, label, path, repoKey, repoRoot string) backend.WorkspaceObservation {
	ref := backend.PaneRef{Backend: backend.Herdr, Workspace: id, Pane: id + ":p1"}
	terminalID := "terminal-" + id
	return backend.WorkspaceObservation{
		WorkspaceID: id, Label: label, Path: path, RepoKey: repoKey, RepoRoot: repoRoot,
		Pane: ref, TerminalID: terminalID, CWD: path,
		Panes: []backend.WorkspacePaneObservation{{Pane: ref, TerminalID: terminalID, CWD: path}},
	}
}

func foreignHerdrWorkspaceAtSameCheckout(fixture herdrLifecycleFixture) backend.WorkspaceObservation {
	return herdrLifecycleWorkspace(
		"w-foreign",
		"foreign-label",
		fixture.worktreePath,
		fixture.pane.RepoKey,
		fixture.pane.RepoRoot,
	)
}

func movedHerdrWorkspace(fixture herdrLifecycleFixture, id string) backend.WorkspaceObservation {
	return herdrLifecycleWorkspace(
		id,
		fixture.workspace.Label,
		fixture.worktreePath,
		fixture.pane.RepoKey,
		fixture.pane.RepoRoot,
	)
}

func movedPaneLessHerdrWorkspace(fixture herdrLifecycleFixture) backend.WorkspaceObservation {
	workspace := movedHerdrWorkspace(fixture, "w-moved")
	workspace.Pane = backend.PaneRef{}
	workspace.TerminalID = ""
	workspace.CWD = ""
	workspace.Panes = nil
	return workspace
}

func movedMultiPaneHerdrWorkspace(fixture herdrLifecycleFixture) backend.WorkspaceObservation {
	workspace := movedHerdrWorkspace(fixture, "w-moved")
	workspace.Panes = append(workspace.Panes, backend.WorkspacePaneObservation{
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: workspace.WorkspaceID, Pane: workspace.WorkspaceID + ":p2",
		},
		TerminalID: "terminal-w-moved-p2",
		CWD:        fixture.worktreePath,
	})
	workspace.Pane = backend.PaneRef{}
	workspace.TerminalID = ""
	workspace.CWD = ""
	return workspace
}

func prepareHerdrCleanupPhase(
	t *testing.T,
	fixture herdrLifecycleFixture,
	phase state.CleanupPhase,
) *fakeHerdrLifecycleRuntime {
	t.Helper()
	runtime := &fakeHerdrLifecycleRuntime{projectRoot: fixture.projectRoot}
	switch phase {
	case state.CleanupReopen:
		coordinator := herdrLifecycleWorkspace(
			"w-coordinator", "coordinator-label", fixture.projectRoot,
			fixture.pane.RepoKey, fixture.pane.RepoRoot,
		)
		recordLifecycleCoordinatorIntent(t, fixture.projectRoot, fixture.pane, coordinator)
		runtime.workspaces = []backend.WorkspaceObservation{coordinator}
	case state.CleanupRemove:
		runtime.workspaces = []backend.WorkspaceObservation{fixture.workspace}
	case state.CleanupWorkspaceClose:
		runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
		runtime.workspaces = []backend.WorkspaceObservation{fixture.workspace}
	default:
		t.Fatalf("unsupported cleanup phase %q", phase)
	}
	return runtime
}

func recordLifecycleCoordinatorIntent(
	t *testing.T,
	projectRoot string,
	target state.Pane,
	workspace backend.WorkspaceObservation,
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
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtimeOwnerRoot, err := state.IntentOwnerProjectRoot(target.RuntimeParent, filepath.Clean(projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	ownerRoot, err := state.IntentOwnerProjectRoot(target.Parent, filepath.Clean(projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	issueNum := 0
	if target.RuntimeParent == panelaunch.ManualParentRef || target.RuntimeParent == watcherStandaloneParent {
		issueNum = target.IssueNum
	}
	id, err := state.CoordinatorIntentID(target.RuntimeParent, runtimeOwnerRoot, issueNum)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(state.LaunchIntent{
		ID: id, Kind: state.IntentCoordinator, Status: state.IntentRealized,
		Parent: target.Parent, RuntimeParent: target.RuntimeParent, OwnerProjectRoot: ownerRoot,
		IssueNum:     issueNum,
		WorktreePath: workspace.CWD, WorkspaceLabel: workspace.Label,
		Resource: state.RuntimeResource{
			WorkspaceID: workspace.WorkspaceID, Label: workspace.Label,
			PaneID: workspace.Pane.Pane, TerminalID: workspace.TerminalID,
			CurrentPath: workspace.CWD,
		},
		Session: target.SessionID, SocketPath: target.SocketPath,
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(),
	})
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	if target.RuntimeParent == panelaunch.ManualParentRef {
		if err := locked.RecordPane(manualLifecycleCoordinatorPane(target, workspace, issueNum-1)); err != nil {
			t.Fatal(err)
		}
	}
}

func manualLifecycleCoordinatorPane(
	target state.Pane,
	workspace backend.WorkspaceObservation,
	issueNum int,
) state.Pane {
	return state.Pane{
		Parent: panelaunch.ManualParentRef, RuntimeParent: target.RuntimeParent,
		IssueNum: issueNum, Kind: state.PaneKindShell, Slug: "herdr-coordinator-test",
		Backend: backend.Herdr, PaneID: workspace.Pane.Pane,
		WorkspaceID: workspace.WorkspaceID, WorkspaceLabel: workspace.Label,
		TerminalID: workspace.TerminalID, SessionID: target.SessionID,
		SocketPath: target.SocketPath, DisplayName: "Herdr coordinator: test",
		WorktreePath: workspace.CWD, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func recordExpiredHerdrCleanupIntent(
	t *testing.T,
	fixture herdrLifecycleFixture,
	phase state.CleanupPhase,
) {
	t.Helper()
	recordHerdrCleanupIntent(t, fixture, phase, time.Now().Add(-time.Minute))
}

func recordActiveHerdrCleanupIntent(
	t *testing.T,
	fixture herdrLifecycleFixture,
	phase state.CleanupPhase,
) {
	t.Helper()
	recordHerdrCleanupIntent(t, fixture, phase, time.Now().Add(time.Minute))
}

func recordHerdrCleanupIntent(
	t *testing.T,
	fixture herdrLifecycleFixture,
	phase state.CleanupPhase,
	expires time.Time,
) {
	t.Helper()
	ownerRoot, err := state.IntentOwnerProjectRoot(fixture.pane.Parent, filepath.Clean(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	intent := state.LaunchIntent{
		ID: intentID, Kind: state.IntentCleanup, Status: state.IntentPlanned,
		Parent: fixture.pane.Parent, RuntimeParent: fixture.pane.RuntimeParent, OwnerProjectRoot: ownerRoot,
		IssueNum: fixture.pane.IssueNum, TaskID: fixture.pane.TaskID, Slug: fixture.pane.Slug,
		BranchName: fixture.pane.BranchName, FullBranchRef: "refs/heads/" + fixture.pane.BranchName,
		BaseBranch: fixture.pane.BaseBranch, WorktreePath: filepath.Clean(fixture.pane.WorktreePath),
		BranchCreated: fixture.pane.BranchCreated, BranchExisted: !fixture.pane.BranchCreated,
		WorkspaceLabel: fixture.pane.WorkspaceLabel, Resource: resourceFromPane(fixture.pane),
		Session: fixture.pane.SessionID, SocketPath: fixture.pane.SocketPath,
		ExpiresUnixMS: expires.UnixMilli(), CleanupPhase: phase,
	}
	if phase == state.CleanupReopen {
		intent.Coordinator = state.RuntimeResource{
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
	journal, err := locked.LaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if saveErr := journal.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
}

func recordManualHerdrCleanupIntent(t *testing.T, fixture herdrLifecycleFixture, failure string) {
	t.Helper()
	recordExpiredHerdrCleanupIntent(t, fixture, state.CleanupRemove)
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
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
	journal, err := locked.LaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		t.Fatal("cleanup intent is absent")
	}
	intent.Status = state.IntentManualCleanupRequired
	intent.ExpectedHead = strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	intent.ExpiresUnixMS = time.Now().Add(time.Minute).UnixMilli()
	intent.Failure = failure
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
}

func replaceSavedHerdrCleanupCoordinator(
	t *testing.T,
	fixture herdrLifecycleFixture,
	coordinator state.RuntimeResource,
) {
	t.Helper()
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
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
	journal, err := locked.LaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		t.Fatal("cleanup intent is absent")
	}
	intent.Coordinator = coordinator
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
}

func legacyDirtyWorktreeFailure() string {
	return `exit status 1: {"error":{"code":"dirty_worktree_requires_force","message":"checkout has changes"},"id":"cli:worktree:remove"}` +
		"\nherdr worktree remove did not establish absence"
}

func capturedLegacyDirtyWorktreeFailure(t *testing.T) string {
	t.Helper()
	// captured from real herdr 0.8.2
	payload, err := os.ReadFile(filepath.Join("testdata", "herdr-0.8.2-dirty-worktree-remove.json"))
	if err != nil {
		t.Fatal(err)
	}
	return legacyRemoveFailurePrefix + strings.TrimSpace(string(payload)) + legacyRemoveFailureSuffix
}

func expireSavedHerdrCleanupIntent(t *testing.T, fixture herdrLifecycleFixture) {
	t.Helper()
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
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
	journal, err := locked.LaunchJournal(fixture.projectRoot)
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

func rewriteSavedHerdrCleanupAsLegacy(t *testing.T, fixture herdrLifecycleFixture) {
	t.Helper()
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
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
	journal, err := locked.LaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		t.Fatal("cleanup intent is absent")
	}
	intent.CleanupDeleteBranchRequested = nil
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
	worktreeIntentID, _, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
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
	journal, err := locked.LaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(state.LaunchIntent{
		ID: worktreeIntentID, Kind: state.IntentWorktree, Status: state.IntentRealized,
		Parent: fixture.pane.Parent, RuntimeParent: fixture.pane.RuntimeParent,
		IssueNum: fixture.pane.IssueNum, Slug: fixture.pane.Slug,
		BranchName: fixture.pane.BranchName, FullBranchRef: "refs/heads/" + fixture.pane.BranchName,
		BaseBranch: fixture.pane.BaseBranch, BaseSHA: head, ExpectedHead: head,
		WorktreePath: fixture.pane.WorktreePath, BranchExisted: true,
		WorkspaceLabel: fixture.pane.WorkspaceLabel,
		Coordinator:    state.RuntimeResource{WorkspaceID: "coordinator"},
		Resource:       resourceFromPane(fixture.pane),
		Session:        fixture.pane.SessionID, SocketPath: fixture.pane.SocketPath,
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(),
		Launch: &state.LaunchCapsule{
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
	journal, err := locked.LaunchJournal(projectRoot)
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

func installFailingBranchObservationGit(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "fail-branch-observation")
	script := `#!/bin/sh
if [ -f "$FANOUT_TEST_GIT_FAIL_MARKER" ] && [ "$1" = "rev-parse" ] && [ "$2" = "--verify" ] && [ "$3" = "--quiet" ]; then
  exit 2
fi
exec "$FANOUT_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FANOUT_TEST_GIT_FAIL_MARKER", marker)
	t.Setenv("FANOUT_TEST_REAL_GIT", realGit)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
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
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range journal.Intents {
		if intent.Kind != state.IntentCoordinator {
			t.Fatalf("child lifecycle intent remains after completion: %#v", journal.Intents)
		}
	}
}

func assertRecreatedManualCoordinatorRecorded(
	t *testing.T,
	fixture herdrLifecycleFixture,
	previous backend.WorkspaceObservation,
) {
	t.Helper()
	intentID, _, err := coordinatorIntentIdentity(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if !found || intent.Status != state.IntentRealized ||
		intent.Resource.WorkspaceID != "w-coordinator-recreated" ||
		intent.Resource.Label == previous.Label {
		t.Fatalf("recreated coordinator intent = (%+v, %t)", intent, found)
	}
	store, err := state.Load(state.Path(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range store.Panes {
		if pane.Kind == state.PaneKindShell && pane.RuntimeParent == panelaunch.ManualParentRef &&
			pane.IssueNum != fixture.pane.IssueNum &&
			pane.WorkspaceID == intent.Resource.WorkspaceID && pane.WorkspaceLabel == intent.Resource.Label &&
			pane.PaneID == intent.Resource.PaneID && pane.TerminalID == intent.Resource.TerminalID {
			return
		}
	}
	t.Fatalf("recreated coordinator row not found in %+v", store.Panes)
}

func assertSingleManualCoordinatorRow(t *testing.T, fixture herdrLifecycleFixture, workspaceID string) {
	t.Helper()
	store, err := state.Load(state.Path(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	var rows []state.Pane
	for _, pane := range store.Panes {
		if pane.Parent == panelaunch.ManualParentRef &&
			pane.RuntimeParent == panelaunch.ManualParentRef && pane.Kind == state.PaneKindShell {
			rows = append(rows, pane)
		}
	}
	if len(rows) != 1 || rows[0].WorkspaceID != workspaceID {
		t.Fatalf("manual coordinator rows = %+v, want one row for %s", rows, workspaceID)
	}
}

func assertManualCoordinatorIntentStatus(
	t *testing.T,
	fixture herdrLifecycleFixture,
	want state.LaunchIntentStatus,
	wantFound bool,
) {
	t.Helper()
	intentID, _, err := coordinatorIntentIdentity(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(intentID)
	if found != wantFound || found && intent.Status != want {
		t.Fatalf("manual coordinator intent = (%+v, %t), want status %q found %t", intent, found, want, wantFound)
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

func assertMovedHerdrCleanupIdentity(
	t *testing.T,
	fixture herdrLifecycleFixture,
	worktreeIntentID, workspaceID string,
) {
	t.Helper()
	store, err := state.Load(state.Path(fixture.projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	pane, found := store.Find(fixture.pane.Parent, fixture.pane.IssueNum)
	if !found || pane.WorkspaceID != workspaceID {
		t.Fatalf("rebound pane = %#v (found=%t), want workspace %s", pane, found, workspaceID)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(worktreeIntentID)
	if !found || intent.Resource.WorkspaceID != workspaceID {
		t.Fatalf("rebound launch intent = %#v (found=%t), want workspace %s", intent, found, workspaceID)
	}
}

func assertHerdrCleanupIntentStatus(
	t *testing.T,
	fixture herdrLifecycleFixture,
	want state.LaunchIntentStatus,
	wantFound bool,
) {
	t.Helper()
	_, intentID, err := workspaceCleanupIntentIDs(fixture.projectRoot, fixture.pane)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := state.LoadLaunchJournal(fixture.projectRoot)
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

// DiscardWorkloadEnvironment delegates to the real capsule removal so cleanup
// tests keep asserting the identity checks the live path performs.
func (f *fakeHerdrLifecycleRuntime) DiscardWorkloadEnvironment(
	runtimeDir string,
	launch *state.LaunchCapsule,
) error {
	return herdrrun.DiscardWorkloadEnvironment(runtimeDir, launch)
}
