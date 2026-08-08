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
	setupErr                 error
	removeErr                error
	keepWorkspaceAfterRemove bool

	verifyCalls int
	setupCalls  int
	openCalls   int
	removeCalls int
	closeCalls  int
}

func (f *fakeHerdrLifecycleRuntime) VerifyOwned(context.Context) error {
	f.verifyCalls++
	return f.verifyErr
}

func (f *fakeHerdrLifecycleRuntime) VerifyWorktreeSetupPolicy(context.Context) error {
	f.setupCalls++
	return f.setupErr
}

func (f *fakeHerdrLifecycleRuntime) ObserveWorkspaces(context.Context) ([]herdrrun.WorkspaceObservation, error) {
	return append([]herdrrun.WorkspaceObservation(nil), f.workspaces...), nil
}

func (f *fakeHerdrLifecycleRuntime) OpenWorktree(_ context.Context, req herdrrun.WorktreeOpenRequest) (herdrrun.WorktreeMutationResult, error) {
	f.openCalls++
	workspace := herdrLifecycleWorkspace("w-reopened", req.Label, req.Path, req.SourceRepoKey, req.SourceRepoRoot)
	f.workspaces = append(f.workspaces, workspace)
	return herdrrun.WorktreeMutationResult{WorkspaceObservation: workspace}, nil
}

func (f *fakeHerdrLifecycleRuntime) RemoveWorktree(ctx context.Context, workspaceID, path string) error {
	f.removeCalls++
	cmd := exec.CommandContext(ctx, "git", "-C", f.projectRoot, "worktree", "remove", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.Join(f.removeErr, errors.New(strings.TrimSpace(string(out))), err)
	}
	if !f.keepWorkspaceAfterRemove {
		f.removeWorkspace(workspaceID)
	}
	return f.removeErr
}

func (f *fakeHerdrLifecycleRuntime) CloseWorkspace(_ context.Context, workspaceID string) error {
	f.closeCalls++
	f.removeWorkspace(workspaceID)
	return nil
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

	opts := Options{
		ProjectRoot: fixture.projectRoot,
		StatePath:   state.Path(fixture.projectRoot),
		Hooks:       hooks.EmptyConfig(),
	}
	if got := Merge(opts, fixture.pane.Parent, fixture.pane.IssueNum, nopLogger{}); got != exitcode.OK {
		t.Fatalf("Merge() = %d, want %d", got, exitcode.OK)
	}
	want := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	got := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.projectRoot, "rev-parse", "HEAD"))
	if got != want {
		t.Fatalf("merged HEAD = %s, want child %s", got, want)
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

func TestHerdrCloseReopensCheckoutOnlyStateBeforeRemoval(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	coordinator := herdrLifecycleWorkspace("w-coordinator", "coordinator-label", fixture.projectRoot,
		fixture.pane.HerdrRepoKey, fixture.pane.HerdrRepoRoot)
	coordinator.Pane.Pane = "w-coordinator:p1"
	coordinator.TerminalID = "terminal-coordinator"
	coordinator.CWD = fixture.projectRoot
	coordinator.Panes = []herdrrun.WorkspacePaneObservation{{
		Pane: coordinator.Pane, TerminalID: coordinator.TerminalID, CWD: fixture.projectRoot,
	}}
	recordLifecyclePane(t, fixture.projectRoot, state.Pane{
		Parent: "@manual", RuntimeParent: fixture.pane.RuntimeParent, IssueNum: -1,
		Kind: state.PaneKindShell, Backend: backend.Herdr,
		PaneID: coordinator.Pane.Pane, HerdrWorkspaceID: coordinator.WorkspaceID,
		HerdrWorkspaceLabel: coordinator.Label, HerdrTerminalID: coordinator.TerminalID,
		HerdrSession: fixture.pane.HerdrSession, HerdrSocketPath: fixture.pane.HerdrSocketPath,
		WorktreePath: fixture.projectRoot,
	})
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

func TestHerdrCloseDoesNotReplayIssuedRemoveAfterCrash(t *testing.T) {
	fixture := newHerdrLifecycleFixture(t)
	head := strings.TrimSpace(runHerdrLifecycleGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	runHerdrLifecycleGit(t, fixture.projectRoot, "worktree", "remove", fixture.worktreePath)
	intentID, err := herdrCleanupIntentID(fixture.projectRoot, fixture.pane)
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

func runHerdrLifecycleGit(t *testing.T, root string, args ...string) {
	t.Helper()
	_ = runHerdrLifecycleGitOutput(t, root, args...)
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
	if len(journal.Intents) != 0 {
		t.Fatalf("cleanup intent remains after completion: %#v", journal.Intents)
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
