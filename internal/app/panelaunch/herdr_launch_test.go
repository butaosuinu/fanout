package panelaunch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type fakeHerdrLaunchRuntime struct {
	fakeHerdrRealizeRuntime
	live        []backend.LivePane
	removeCalls []string
	remove      func(string, string) error
	launchRoute herdrrun.OwnedLaunchRoute
	processInfo herdrrun.PaneProcessInfo
	processErr  error
	liveErr     error
	tokenCalls  int
}

func (f *fakeHerdrLaunchRuntime) VerifyOwned(context.Context) error { return nil }
func (f *fakeHerdrLaunchRuntime) LaunchRoute() (herdrrun.OwnedLaunchRoute, error) {
	return f.launchRoute, nil
}

func (f *fakeHerdrLaunchRuntime) PrepareWorkloadEnvironment(string, []string) (string, int, error) {
	return "/tmp/env", 1, nil
}

func (f *fakeHerdrLaunchRuntime) WaitForLauncher(context.Context, string, string, time.Duration) error {
	return nil
}

func (f *fakeHerdrLaunchRuntime) ProcessInfo(context.Context, string) (herdrrun.PaneProcessInfo, error) {
	return f.processInfo, f.processErr
}

func (f *fakeHerdrLaunchRuntime) SendLaunchToken(context.Context, string, string) error {
	f.tokenCalls++
	return nil
}

func (f *fakeHerdrLaunchRuntime) LivePanes(context.Context) ([]backend.LivePane, error) {
	return append([]backend.LivePane(nil), f.live...), f.liveErr
}
func (f *fakeHerdrLaunchRuntime) RenameAgent(context.Context, string, string) error { return nil }

func (f *fakeHerdrLaunchRuntime) RemoveWorktree(_ context.Context, workspaceID, path string) error {
	f.removeCalls = append(f.removeCalls, workspaceID)
	if f.remove != nil {
		return f.remove(workspaceID, path)
	}
	return nil
}

func TestIssuedHerdrLaunchWithMatchingNameStillFailsClosed(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrLaunchRuntime{}
	installSuccessfulHerdrMutations(t, repo, &runtime.fakeHerdrRealizeRuntime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, &runtime.fakeHerdrRealizeRuntime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "response-loss", 528),
		&runtime.fakeHerdrRealizeRuntime, hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := journal.FindIntent(result.Intent.ID)
	if !found {
		t.Fatal("realized intent is missing")
	}
	intent.Launch = &state.HerdrLaunch{
		Nonce: strings.Repeat("a", 32), Agent: "codex",
		AgentName:  "fanout-0123456789abcdef01234567",
		Executable: "/bin/codex", Args: []string{},
		EnvFilePath: "/tmp/env", EnvNameCount: 1,
		LauncherReady: true, TokenIssued: true,
	}
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	runtime.live = []backend.LivePane{{
		Ref:     backend.PaneRef{Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID},
		AgentID: intent.Launch.AgentName, AgentPresent: true,
	}}
	launcher := &Launcher{
		Cfg: &cliflags.Config{}, Log: log.New(false),
		Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime,
	}
	err = launcher.failClosedIssuedHerdrLaunch(journal, intent)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) ||
		!strings.Contains(err.Error(), "refusing automatic adoption") ||
		!strings.Contains(err.Error(), "operation-bound agent name is present") {
		t.Fatalf("response-loss error = %v", err)
	}
	persisted, err := state.LoadHerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, found := persisted.FindIntent(intent.ID)
	if !found || saved.Status != state.HerdrIntentManualCleanupRequired {
		t.Fatalf("saved response-loss intent = %+v, found=%t", saved, found)
	}
	if err := launcher.rollbackHerdrLaunch(locked, intent, errHerdrLaunchResponseLost); err != nil {
		t.Fatal(err)
	}
	if len(runtime.removeCalls) != 0 {
		t.Fatalf("issued launch rollback removed a workspace: %v", runtime.removeCalls)
	}
}

func TestUnpublishedHerdrLaunchRemovesEnvironmentCapsule(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(t.TempDir(), "env.json")
	if err := os.WriteFile(envPath, []byte("secret=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	intent := state.HerdrIntent{ID: "invalid", Launch: &state.HerdrLaunch{EnvFilePath: envPath}}
	if _, err := persistNewHerdrLaunch(journal, intent); err == nil {
		t.Fatal("invalid unpublished launch was saved")
	}
	if _, err := os.Stat(envPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unpublished environment capsule remains: %v", err)
	}
}

func TestFinalizeHerdrLaunchFailureBecomesManualCleanupRequired(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "finalize-failure", 530), runtime, hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	intent := result.Intent
	intent.Launch = validTestHerdrLaunch()
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(intent.WorktreePath, ".fanout"), []byte("block directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}}
	stale := intent
	stale.Launch = nil
	err = launcher.finalizeHerdrLaunch(Request{}, locked, stale, backend.LivePane{})
	if !errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("finalization error = %v, want manual cleanup", err)
	}
	persisted, err := state.LoadHerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, found := persisted.FindIntent(intent.ID)
	if !found || saved.Status != state.HerdrIntentManualCleanupRequired {
		t.Fatalf("saved finalization intent = (%+v, %t), want manual cleanup", saved, found)
	}
	if saved.Launch == nil || !saved.Launch.TokenIssued || saved.Launch.Nonce != intent.Launch.Nonce {
		t.Fatalf("saved finalization launch = %+v, want latest issued capsule", saved.Launch)
	}
}

func validTestHerdrLaunch() *state.HerdrLaunch {
	return &state.HerdrLaunch{
		Nonce: strings.Repeat("a", 32), Agent: "claude",
		AgentName: "fanout-0123456789abcdef01234567", Executable: "/bin/claude",
		EnvFilePath: "/tmp/env", EnvNameCount: 1, LauncherReady: true, TokenIssued: true,
	}
}

func TestHerdrCoordinatorRuntimeParentProjectsWatcherIssue(t *testing.T) {
	intent := state.HerdrIntent{RuntimeParent: WatchParentRef, IssueNum: 528}
	if got := herdrCoordinatorRuntimeParent(intent); got != "528" {
		t.Fatalf("runtime parent = %q, want 528", got)
	}
}

func TestOptionalHerdrAgentSession(t *testing.T) {
	valid := &backend.AgentSessionRef{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-1"}
	foreign := &backend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-2"}
	invalid := &backend.AgentSessionRef{Agent: "claude"}
	tests := []struct {
		name    string
		session *backend.AgentSessionRef
		want    bool
	}{
		{name: "not reported", want: true},
		{name: "reported exact", session: valid, want: true},
		{name: "foreign agent", session: foreign},
		{name: "incomplete", session: invalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validOptionalHerdrAgentSession(test.session, "claude"); got != test.want {
				t.Fatalf("valid = %t, want %t", got, test.want)
			}
		})
	}
}

func TestVerifyHerdrAgentProcessAcceptsInterpreterWrapper(t *testing.T) {
	intent := state.HerdrIntent{
		WorktreePath: "/repo/worktree",
		Launch: &state.HerdrLaunch{
			Executable: "/opt/bin/codex",
			Args:       []string{"prompt with spaces"},
		},
	}
	info := herdrrun.PaneProcessInfo{
		ShellPID: 42, ForegroundProcessGroup: 42,
		ForegroundProcesses: []herdrrun.PaneProcess{{
			PID: 42, CWD: intent.WorktreePath,
			Argv: []string{"node", intent.Launch.Executable, intent.Launch.Args[0]},
		}},
	}
	if err := verifyHerdrAgentProcess(info, intent); err != nil {
		t.Fatalf("wrapper process rejected: %v", err)
	}

	info.ForegroundProcesses[0].Argv = []string{"node", "/foreign/codex", intent.Launch.Args[0]}
	if err := verifyHerdrAgentProcess(info, intent); err == nil {
		t.Fatal("foreign wrapper entrypoint was accepted")
	}
}

func TestExpiredHerdrAgentStartBecomesManualCleanupRequired(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "expired-start", 529), runtime, hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	intent := result.Intent
	intent.ExpiresUnixMS = time.Now().Add(-time.Second).UnixMilli()
	err = admitHerdrAgentStartDeadline(locked, repo, intent)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("error = %v, want manual cleanup", err)
	}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, found := journal.FindIntent(intent.ID)
	if !found || saved.Status != state.HerdrIntentManualCleanupRequired {
		t.Fatalf("saved intent = (%+v, %t), want manual cleanup", saved, found)
	}
}

func TestAdmitHerdrLauncherFencesExactTerminalBeforeToken(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrLaunchRuntime{}
	installSuccessfulHerdrMutations(t, repo, &runtime.fakeHerdrRealizeRuntime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, &runtime.fakeHerdrRealizeRuntime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "terminal-fence", 531),
		&runtime.fakeHerdrRealizeRuntime, hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	intent := result.Intent
	intent.Launch = validTestHerdrLaunch()
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	runtime.launchRoute = herdrrun.OwnedLaunchRoute{LauncherPath: "/owned/fanout"}
	runtime.processInfo = testHerdrLauncherProcess(intent, runtime.launchRoute.LauncherPath)
	runtime.live = []backend.LivePane{testHerdrIdlePane(intent)}
	runtime.live[0].TerminalID = "reused-terminal"
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime}

	err = launcher.admitHerdrLauncher(context.Background(), journal, runtime.launchRoute, &intent)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("admission error = %v, want manual cleanup", err)
	}
	if runtime.tokenCalls != 0 {
		t.Fatalf("token calls = %d, want 0", runtime.tokenCalls)
	}
	saved, found := journal.FindIntent(intent.ID)
	if !found || saved.Status != state.HerdrIntentManualCleanupRequired {
		t.Fatalf("saved intent = (%+v, %t), want manual cleanup", saved, found)
	}
}

func TestHerdrCoordinatorIdentityMismatchFailsBeforeStateRow(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrLaunchRuntime{}
	installSuccessfulHerdrMutations(t, repo, &runtime.fakeHerdrRealizeRuntime)
	runtime.launchRoute = herdrrun.OwnedLaunchRoute{
		Session: "fanout-test", SocketPath: "/private/tmp/fanout-test/herdr.sock",
		LauncherPath: "/owned/fanout",
	}
	mutate := runtime.mutate
	runtime.mutate = func(req herdrTestMutation) (herdrrun.WorktreeMutationResult, error) {
		result, err := mutate(req)
		if err == nil && req.Kind == herdrrun.WorkspaceCreate {
			intent := state.HerdrIntent{
				WorktreePath: repo, Session: runtime.launchRoute.Session,
				SocketPath: runtime.launchRoute.SocketPath,
				Resource:   stateResource(result.WorkspaceObservation),
			}
			runtime.processInfo = testHerdrLauncherProcess(intent, runtime.launchRoute.LauncherPath)
			runtime.live = []backend.LivePane{testHerdrIdlePane(intent)}
			runtime.live[0].TerminalID = "foreign-terminal"
		}
		return result, err
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	launcher := &Launcher{
		Cfg: &cliflags.Config{}, Log: log.NewWith(io.Discard, io.Discard, false),
		Info: &fanoutruntime.Info{ProjectRoot: repo}, Recorder: locked, Herdr: runtime,
	}
	_, err = launcher.realizeHerdrCoordinator(
		context.Background(), Request{ParentRef: "425"}, locked, runtime.launchRoute,
	)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("coordinator realization error = %v, want manual cleanup", err)
	}
	if len(locked.Panes) != 0 {
		t.Fatalf("coordinator row was recorded before exact identity verification: %+v", locked.Panes)
	}
}

func TestHerdrLaunchRollbackRemovesExactResourceAfterResponseLoss(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrLaunchRuntime{}
	installSuccessfulHerdrMutations(t, repo, &runtime.fakeHerdrRealizeRuntime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, &runtime.fakeHerdrRealizeRuntime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "launch-rollback", 532),
		&runtime.fakeHerdrRealizeRuntime, hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	intent := result.Intent
	runtime.launchRoute = herdrrun.OwnedLaunchRoute{LauncherPath: "/owned/fanout"}
	runtime.processInfo = testHerdrLauncherProcess(intent, runtime.launchRoute.LauncherPath)
	runtime.live = []backend.LivePane{testHerdrIdlePane(intent)}
	runtime.remove = func(workspaceID, path string) error {
		if workspaceID != intent.Resource.WorkspaceID || path != intent.WorktreePath {
			t.Fatalf("remove target = %s %s", workspaceID, path)
		}
		gitCmdTest(t, repo, "worktree", "remove", path)
		kept := runtime.workspaces[:0]
		for _, workspace := range runtime.workspaces {
			if workspace.WorkspaceID != workspaceID {
				kept = append(kept, workspace)
			}
		}
		runtime.workspaces = kept
		runtime.live = nil
		return errors.New("remove response lost")
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime}
	if err := launcher.rollbackHerdrLaunch(locked, intent, errors.New("launcher exited")); err != nil {
		t.Fatal(err)
	}
	if len(runtime.removeCalls) != 1 {
		t.Fatalf("remove calls = %v, want one", runtime.removeCalls)
	}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	rollbackID, _ := state.HerdrRollbackIntentID(intent.ID)
	if _, found := journal.FindIntent(intent.ID); found {
		t.Fatal("launch intent remains after verified rollback")
	}
	if _, found := journal.FindIntent(rollbackID); found {
		t.Fatal("rollback intent remains after verified rollback")
	}
	if _, found, err := worktree.ObserveBranch(context.Background(), repo, intent.FullBranchRef); err != nil || found {
		t.Fatalf("rolled back branch = found %t, err %v", found, err)
	}
}

func TestPrepareHerdrOperationSetsOneSharedLaunchDeadline(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	runtime := &fakeHerdrLaunchRuntime{}
	launcher := &Launcher{
		Cfg: &cliflags.Config{}, Log: log.NewWith(io.Discard, io.Discard, false),
		Info: &fanoutruntime.Info{ProjectRoot: repo}, Recorder: locked, Herdr: runtime,
	}
	operation, ok := launcher.prepareHerdrOperation(Request{ParentRef: "425", Number: 528, Agent: "codex"})
	if !ok {
		t.Fatal("operation preparation failed")
	}
	defer operation.cancel()
	deadline, found := operation.ctx.Deadline()
	remaining := time.Until(deadline)
	if !found || remaining <= 0 || remaining > maxHerdrRealizeTimeout {
		t.Fatalf("shared deadline = %v, found=%t, remaining=%v", deadline, found, remaining)
	}
}

func TestLaunchHerdrRunsClaudeModePreflightBeforeBackendAdmission(t *testing.T) {
	installClaudeVersionExecutable(t, "2.1.206 (Claude Code)")
	var stderr bytes.Buffer
	launcher := &Launcher{
		Cfg: &cliflags.Config{}, Log: log.NewWith(io.Discard, &stderr, false),
	}
	_, ok := launcher.launchHerdr(Request{
		ParentRef: "425", Number: 528, Agent: "claude", LaunchMode: agent.ModePlan,
	})
	if ok || !strings.Contains(stderr.String(), "omitting mode flags") {
		t.Fatalf("launch result = %t, stderr = %q", ok, stderr.String())
	}
}

func testHerdrLauncherProcess(intent state.HerdrIntent, launcherPath string) herdrrun.PaneProcessInfo {
	return herdrrun.PaneProcessInfo{
		PaneID: intent.Resource.PaneID, ShellPID: 42, ForegroundProcessGroup: 42,
		ForegroundProcesses: []herdrrun.PaneProcess{{
			PID: 42, CWD: intent.WorktreePath, Argv: []string{launcherPath},
		}},
	}
}

func testHerdrIdlePane(intent state.HerdrIntent) backend.LivePane {
	return backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
		CurrentPath: intent.WorktreePath, TerminalID: intent.Resource.TerminalID,
		RepoKey: intent.Resource.RepoKey, WorktreePath: intent.WorktreePath,
		SessionID: intent.Session, SocketPath: intent.SocketPath,
	}
}
