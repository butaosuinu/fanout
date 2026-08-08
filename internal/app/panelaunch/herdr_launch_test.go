package panelaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type fakeHerdrLaunchRuntime struct {
	fakeHerdrRealizeRuntime
	live            []backend.LivePane
	metadataReports []herdrrun.MetadataReport
	metadataErr     error
	removeCalls     []string
	remove          func(string, string) error
	launchRoute     herdrrun.OwnedLaunchRoute
	processInfo     herdrrun.PaneProcessInfo
	process         func(context.Context, string) (herdrrun.PaneProcessInfo, error)
	listLive        func(context.Context) ([]backend.LivePane, error)
	processErr      error
	liveErr         error
	wait            func(context.Context, string, string, time.Duration) error
	liveCalls       int
	renameCalls     int
	tokenCalls      int
}

type retryableHerdrObservationError struct{}

func (retryableHerdrObservationError) Error() string { return "transient observation" }

func (retryableHerdrObservationError) RetryableObservation() bool { return true }

func (f *fakeHerdrLaunchRuntime) VerifyOwned(context.Context) error { return nil }
func (f *fakeHerdrLaunchRuntime) LaunchRoute() (herdrrun.OwnedLaunchRoute, error) {
	return f.launchRoute, nil
}

func (f *fakeHerdrLaunchRuntime) PrepareWorkloadEnvironment(string, []string) (string, int, error) {
	return "/tmp/env", 1, nil
}

func (f *fakeHerdrLaunchRuntime) WaitForLauncher(ctx context.Context, paneID, nonce string, timeout time.Duration) error {
	if f.wait != nil {
		return f.wait(ctx, paneID, nonce, timeout)
	}
	return nil
}

func (f *fakeHerdrLaunchRuntime) ProcessInfo(ctx context.Context, paneID string) (herdrrun.PaneProcessInfo, error) {
	if f.process != nil {
		return f.process(ctx, paneID)
	}
	return f.processInfo, f.processErr
}

func (f *fakeHerdrLaunchRuntime) SendLaunchToken(context.Context, string, string) error {
	f.tokenCalls++
	return nil
}

func (f *fakeHerdrLaunchRuntime) LivePanes(ctx context.Context) ([]backend.LivePane, error) {
	f.liveCalls++
	if f.listLive != nil {
		return f.listLive(ctx)
	}
	return append([]backend.LivePane(nil), f.live...), f.liveErr
}

func (f *fakeHerdrLaunchRuntime) RenameAgent(context.Context, string, string) error {
	f.renameCalls++
	return nil
}

func (f *fakeHerdrLaunchRuntime) ReportMetadata(_ context.Context, report herdrrun.MetadataReport) error {
	f.metadataReports = append(f.metadataReports, report)
	return f.metadataErr
}

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
	if saveErr := journal.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	runtime.live = []backend.LivePane{{
		Ref:     backend.PaneRef{Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID},
		AgentID: intent.Launch.AgentName, AgentPresent: true,
	}}
	launcher := &Launcher{
		Cfg: &cliflags.Config{}, Log: log.New(false),
		Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime,
	}
	err = launcher.failClosedIssuedHerdrLaunch(journal, intent, nil)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) ||
		!strings.Contains(err.Error(), "refusing automatic adoption") ||
		!strings.Contains(err.Error(), "launch-token outcome is indeterminate") {
		t.Fatalf("response-loss error = %v", err)
	}
	if runtime.liveCalls != 0 {
		t.Fatalf("response-loss fail-closed performed %d late observations", runtime.liveCalls)
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

func TestIssuedHerdrShellRecoversWithoutTokenReplay(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "consumed-env")
	intent := state.HerdrIntent{
		ID: "console", Status: state.HerdrIntentRealized,
		WorktreePath: "/repo", Session: "fanout-owned", SocketPath: "/tmp/herdr.sock",
		ExpiresUnixMS: time.Now().Add(5 * time.Second).UnixMilli(),
		Resource: state.HerdrResource{
			WorkspaceID: "w1", Label: "fanout-console-owned", PaneID: "w1:p1",
			TerminalID: "term-1", CurrentPath: "/repo",
		},
		Launch: &state.HerdrLaunch{
			Nonce: strings.Repeat("a", 32), Executable: "/bin/zsh",
			EnvFilePath: envPath, EnvNameCount: 1, LauncherReady: true, TokenIssued: true,
		},
	}
	runtime := &fakeHerdrLaunchRuntime{
		processInfo: herdrrun.PaneProcessInfo{
			ShellPID: 42, ForegroundProcessGroup: 42,
			ForegroundProcesses: []herdrrun.PaneProcess{{
				PID: 42, ParentPID: 1, ProcessGroup: 42,
				Executable: "/bin/zsh", Argv0: "/bin/zsh", CWD: "/repo",
			}},
		},
		live: []backend.LivePane{{
			Ref:            backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
			WorkspaceLabel: "fanout-console-owned", TerminalID: "term-1",
			CurrentPath: "/repo", SessionID: "fanout-owned", SocketPath: "/tmp/herdr.sock",
		}},
	}
	launcher := &Launcher{Herdr: runtime}
	live, err := launcher.recoverIssuedHerdrShell(context.Background(), nil, intent)
	if err != nil || live.Ref.Pane != "w1:p1" {
		t.Fatalf("recover issued shell = %+v, %v", live, err)
	}
	if runtime.tokenCalls != 0 {
		t.Fatalf("issued shell replayed %d token(s)", runtime.tokenCalls)
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
	runtimeDir := t.TempDir()
	nonce := strings.Repeat("a", 32)
	envDir := filepath.Join(runtimeDir, "workload-env")
	if err := os.Mkdir(envDir, 0o700); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(envDir, "env-"+nonce+".json")
	if err := os.WriteFile(envPath, []byte("secret=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	intent := state.HerdrIntent{ID: "invalid", Launch: &state.HerdrLaunch{Nonce: nonce, EnvFilePath: envPath}}
	if _, err := persistNewHerdrLaunch(journal, intent, runtimeDir); err == nil {
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
	if saveErr := journal.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if writeErr := os.WriteFile(filepath.Join(intent.WorktreePath, ".fanout"), []byte("block directory"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}}
	stale := intent
	stale.Launch = nil
	err = launcher.finalizeHerdrLaunch(Request{}, locked, stale, backend.LivePane{}, codexapp.Status{})
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

func TestFinalizeHerdrLaunchAppliesPendingTelemetryFromLatestIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	worktreeReq := testHerdrWorktreeRequest(repo, "finalize-telemetry", 531)
	result, err := realizeHerdrWorktree(context.Background(), worktreeReq, runtime, hooks)
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
	intent.Launch.EmitterNonce = strings.Repeat("b", 32)
	intent.Launch.PendingReportedState = string(backend.AgentIdle)
	pendingSession := backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-531",
	}
	intent.Launch.PendingAgentSession = &pendingSession
	launchReq := Request{
		ParentRef: worktreeReq.Parent, Number: worktreeReq.IssueNum,
		Slug: worktreeReq.Slug, BranchName: worktreeReq.BranchName,
		Prompt: "telemetry prompt", Agent: "claude",
		Worktree: worktree.Plan{BaseBranch: worktreeReq.BaseBranch},
	}
	intent.Launch.Args = []string{"--settings", "{}", launchReq.Prompt}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	err = journal.Save()
	if err != nil {
		t.Fatal(err)
	}
	live := backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
		TerminalID: intent.Resource.TerminalID, RepoKey: intent.Resource.RepoKey,
		AgentID: intent.Launch.AgentName, AgentProvider: intent.Launch.Agent,
		AgentSession: &pendingSession,
		SessionID:    intent.Session, SocketPath: intent.SocketPath,
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}}
	stale := intent
	stale.Launch = nil
	err = launcher.finalizeHerdrLaunch(launchReq, locked, stale, live, codexapp.Status{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	pane, found := store.Find(launchReq.ParentRef, launchReq.Number)
	if !found {
		t.Fatal("final pane row was not saved")
	}
	if pane.ReportedState != "idle" || !pane.StateRefinement ||
		pane.EmitterRowKey != intent.ID || pane.LaunchNonce != intent.Launch.Nonce ||
		pane.EmitterNonce != intent.Launch.EmitterNonce || pane.HerdrLaunchExecutable != intent.Launch.Executable ||
		!slices.Equal(pane.HerdrLaunchArgs, intent.Launch.Args) {
		t.Fatalf("final telemetry binding = %+v", pane)
	}
	persisted, err := state.LoadHerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := persisted.FindIntent(intent.ID); found {
		t.Fatal("finalized intent remains in provisional journal")
	}
}

func TestWaitForHerdrAgentRevalidatesConcurrentIntentChanges(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*state.LockedHerdrIntents, state.HerdrIntent) error
		wantErr     error
		wantErrText string
		wantFound   bool
		wantStatus  state.HerdrIntentStatus
		wantPending string
	}{
		{
			name: "pending telemetry",
			mutate: func(journal *state.LockedHerdrIntents, intent state.HerdrIntent) error {
				intent.Launch.PendingReportedState = string(backend.AgentWorking)
				journal.UpsertIntent(intent)
				return journal.Save()
			},
			wantFound: true, wantStatus: state.HerdrIntentRealized,
			wantPending: string(backend.AgentWorking),
		},
		{
			name: "parallel retry marks manual",
			mutate: func(journal *state.LockedHerdrIntents, intent state.HerdrIntent) error {
				err := markHerdrIntentManual(journal, intent, errors.New("parallel retry"))
				if errors.Is(err, ErrHerdrManualCleanupRequired) {
					return nil
				}
				return err
			},
			wantErr: ErrHerdrManualCleanupRequired, wantFound: true,
			wantStatus: state.HerdrIntentManualCleanupRequired,
		},
		{
			name: "launch identity drifts",
			mutate: func(journal *state.LockedHerdrIntents, intent state.HerdrIntent) error {
				intent.Launch.Nonce = strings.Repeat("c", 32)
				journal.UpsertIntent(intent)
				return journal.Save()
			},
			wantErrText: "launch identity changed", wantFound: true,
			wantStatus: state.HerdrIntentRealized,
		},
		{
			name: "intent disappears",
			mutate: func(journal *state.LockedHerdrIntents, intent state.HerdrIntent) error {
				journal.RemoveIntent(intent.ID)
				return journal.Save()
			},
			wantErrText: "intent disappeared",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, locked, intent, live := herdrEmitterWaitFixture(t)
			updated := make(chan error, 1)
			go mutateHerdrIntentAfterUnlock(repo, intent, test.mutate, updated)
			runtime := &fakeHerdrLaunchRuntime{}
			runtime.listLive = func(context.Context) ([]backend.LivePane, error) {
				return []backend.LivePane{live}, <-updated
			}
			launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime}
			_, err := launcher.waitForHerdrAgentUnlocked(
				context.Background(), locked, intent, intent.Launch.AgentName, "",
			)
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("wait error = %v, want %v containing %q", err, test.wantErr, test.wantErrText)
			}
			if test.wantErr == nil && test.wantErrText == "" && err != nil {
				t.Fatalf("wait error = %v, want nil", err)
			}
			if test.wantErrText != "" && (err == nil || !strings.Contains(err.Error(), test.wantErrText)) {
				t.Fatalf("wait error = %v, want text %q", err, test.wantErrText)
			}
			journal, loadErr := locked.HerdrIntents(repo)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			latest, found := journal.FindIntent(intent.ID)
			if found != test.wantFound {
				t.Fatalf("intent found = %t, want %t", found, test.wantFound)
			}
			if found && (latest.Status != test.wantStatus || latest.Launch.PendingReportedState != test.wantPending) {
				t.Fatalf("persisted concurrent intent = %+v", latest)
			}
		})
	}
}

func TestFinishIssuedHerdrAgentPreservesObservedAgentAfterContextExpires(t *testing.T) {
	if herdrLaunchLockReacquireTimeout < maxHerdrRealizeTimeout {
		t.Fatalf("lock recovery timeout = %s, want at least %s", herdrLaunchLockReacquireTimeout, maxHerdrRealizeTimeout)
	}
	repo, locked, intent, live := herdrEmitterWaitFixture(t)
	live.AgentID = intent.Launch.Agent
	held := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		contender, err := state.LockProjectForLaunch(repo)
		if err != nil {
			close(held)
			holderDone <- err
			return
		}
		close(held)
		time.Sleep(250 * time.Millisecond)
		holderDone <- contender.Unlock()
	}()
	runtime := &fakeHerdrLaunchRuntime{}
	runtime.listLive = func(context.Context) ([]backend.LivePane, error) {
		<-held
		return []backend.LivePane{live}, nil
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := launcher.finishIssuedHerdrAgent(ctx, Request{Agent: intent.Launch.Agent}, locked, intent)
	if !errors.Is(err, errHerdrLaunchStatePreserved) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finish error = %v, want preserved launch with expired context", err)
	}
	if errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("observed agent entered manual cleanup: %v", err)
	}
	if holderErr := <-holderDone; holderErr != nil {
		t.Fatal(holderErr)
	}
	journal, loadErr := locked.HerdrIntents(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	latest, found := journal.FindIntent(intent.ID)
	if !found || latest.Status != state.HerdrIntentRealized {
		t.Fatalf("preserved launch intent = (%+v, %t)", latest, found)
	}
}

func herdrEmitterWaitFixture(t *testing.T) (string, *state.LockedStore, state.HerdrIntent, backend.LivePane) {
	t.Helper()
	repo := newHerdrRealizeRepo(t)
	realizeRuntime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, realizeRuntime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, realizeRuntime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "emitter-lock-window", 529), realizeRuntime, hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })
	intent := result.Intent
	intent.Launch = validTestHerdrLaunch()
	intent.Launch.EmitterNonce = strings.Repeat("b", 32)
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	live := testHerdrIdlePane(intent)
	live.AgentPresent = true
	live.AgentProvider = intent.Launch.Agent
	live.AgentID = intent.Launch.AgentName
	return repo, locked, intent, live
}

func mutateHerdrIntentAfterUnlock(
	repo string,
	intent state.HerdrIntent,
	mutate func(*state.LockedHerdrIntents, state.HerdrIntent) error,
	result chan<- error,
) {
	locked, err := state.LockProjectForLaunch(repo)
	if err == nil {
		journal, journalErr := locked.HerdrIntents(repo)
		err = journalErr
		if journalErr == nil {
			latest, found := journal.FindIntent(intent.ID)
			if !found {
				err = errors.New("realized intent disappeared before mutation")
			} else {
				err = mutate(journal, latest)
			}
		}
		err = errors.Join(err, locked.Unlock())
	}
	result <- err
}

func validTestHerdrLaunch() *state.HerdrLaunch {
	return &state.HerdrLaunch{
		Nonce: strings.Repeat("a", 32), Agent: "claude",
		AgentName: "fanout-0123456789abcdef01234567", Executable: "/bin/claude",
		EnvFilePath: "/tmp/env", EnvNameCount: 1, LauncherReady: true, TokenIssued: true,
	}
}

func TestBuildHerdrLaunchSpecStartsCodexTeamBridge(t *testing.T) {
	binDir := t.TempDir()
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	req := Request{
		Number: 568, ParentRef: "524", Agent: "codex", Prompt: "registry migration",
		CodexTeamMode: true, CodexTeamStatusPath: "/tmp/team-status.json",
	}

	spec, err := buildHerdrLaunchSpec(req)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := codexapp.TeamLaunchSpec(
		self, codexPath, req.Prompt, "568", req.ParentRef, req.CodexTeamStatusPath,
	)
	if spec.Executable != want.Executable || !slices.Equal(spec.Args, want.Args) {
		t.Fatalf("Herdr team launch spec = %+v, want %+v", spec, want)
	}
}

func TestBuildHerdrLaunchSpecStartsCodexPlanController(t *testing.T) {
	binDir := t.TempDir()
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	req := Request{
		Number: 554, ParentRef: "524", Agent: "codex", Prompt: "plan the backend",
		LaunchMode: agent.ModePlan, CodexPlanStatusPath: "/tmp/plan-status.json",
	}

	spec, err := buildHerdrLaunchSpec(req)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := codexapp.PlanLaunchSpec(self, codexPath, req.Prompt, req.CodexPlanStatusPath)
	if spec.Executable != want.Executable || !slices.Equal(spec.Args, want.Args) {
		t.Fatalf("Herdr Plan launch spec = %+v, want %+v", spec, want)
	}
}

func TestWaitForHerdrCodexTeamConsumesReadyStatus(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(statusPath, []byte(`{"status":"ready","threadId":"thread-568","sessionId":"session-568"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	req := Request{CodexTeamMode: true, CodexTeamStatusPath: statusPath}
	intent := state.HerdrIntent{ExpiresUnixMS: time.Now().Add(time.Second).UnixMilli()}

	status, err := waitForHerdrCodexTUI(req, intent)
	if err != nil {
		t.Fatal(err)
	}
	if status.ThreadID != "thread-568" || status.SessionID != "session-568" {
		t.Fatalf("Codex team status = %+v", status)
	}
	if _, err := os.Stat(statusPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed Codex team status remains: %v", err)
	}
}

func TestWaitForHerdrCodexTeamRejectsExpiredLaunch(t *testing.T) {
	req := Request{CodexTeamMode: true, CodexTeamStatusPath: filepath.Join(t.TempDir(), "status.json")}
	intent := state.HerdrIntent{ExpiresUnixMS: time.Now().Add(-time.Second).UnixMilli()}

	_, err := waitForHerdrCodexTUI(req, intent)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired Codex team launch error = %v", err)
	}
}

func TestHerdrCodexTeamStatusPathUsesPersistedLaunchIdentity(t *testing.T) {
	savedPath := filepath.Join(t.TempDir(), "saved-status.json")
	teamDBPath := filepath.Join(t.TempDir(), "team.db")
	intent := state.HerdrIntent{Launch: &state.HerdrLaunch{
		TeamDBPath: teamDBPath, CodexTeamStatusPath: savedPath,
	}}
	for _, req := range []Request{
		{Number: 568, TeamDBPath: teamDBPath, CodexTeamMode: true, CodexTeamStatusPath: "/tmp/new-issue-status.json"},
		{TaskID: "registry-migration", TeamDBPath: teamDBPath, CodexTeamMode: true, CodexTeamStatusPath: "/tmp/new-task-status.json"},
	} {
		got, err := herdrCodexStatusPath(req, intent)
		if err != nil {
			t.Fatal(err)
		}
		if got != savedPath {
			t.Fatalf("persisted team status path = %q, want %q", got, savedPath)
		}
	}
}

func TestHerdrCodexPlanStatusPathUsesPersistedLaunchIdentity(t *testing.T) {
	savedPath := filepath.Join(t.TempDir(), "saved-status.json")
	intent := state.HerdrIntent{Launch: &state.HerdrLaunch{CodexPlanStatusPath: savedPath}}
	req := Request{
		Agent: "codex", LaunchMode: agent.ModePlan,
		CodexPlanStatusPath: filepath.Join(t.TempDir(), "regenerated-status.json"),
	}
	got, err := herdrCodexStatusPath(req, intent)
	if err != nil {
		t.Fatal(err)
	}
	if got != savedPath {
		t.Fatalf("persisted Plan status path = %q, want %q", got, savedPath)
	}
}

func TestWaitForHerdrCodexTUIUnlockedReleasesLockAndRechecksDeadline(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "plan-wait", 554), runtime, hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })
	intent := result.Intent
	intent.ExpiresUnixMS = time.Now().Add(time.Second).UnixMilli()
	statusPath := filepath.Join(t.TempDir(), "status.json")
	intent.Launch = validTestHerdrLaunch()
	intent.Launch.Agent = "codex"
	intent.Launch.CodexPlanStatusPath = statusPath
	j, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	j.UpsertIntent(intent)
	if saveErr := j.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	type waitResult struct {
		status codexapp.Status
		err    error
	}
	waited := make(chan waitResult, 1)
	go func() {
		status, _, _, waitErr := waitForHerdrCodexTUIUnlocked(
			context.Background(),
			Request{Agent: "codex", LaunchMode: agent.ModePlan, CodexPlanStatusPath: statusPath},
			locked, repo, intent,
		)
		waited <- waitResult{status: status, err: waitErr}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	contender, err := state.LockProjectForLaunchContext(ctx, repo)
	if err != nil {
		t.Fatalf("readiness wait kept the launch lock: %v", err)
	}
	if writeErr := os.WriteFile(statusPath, []byte(`{"status":"ready","threadId":"thread-554","sessionId":"session-554"}`), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if unlockErr := contender.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	got := <-waited
	if got.err != nil || got.status.ThreadID != "thread-554" {
		t.Fatalf("unlocked readiness wait = %+v, err %v", got.status, got.err)
	}

	statusPath = filepath.Join(t.TempDir(), "expired-status.json")
	intent.ExpiresUnixMS = time.Now().Add(500 * time.Millisecond).UnixMilli()
	intent.Launch.CodexPlanStatusPath = statusPath
	j, err = locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	j.UpsertIntent(intent)
	if saveErr := j.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	go func() {
		status, _, _, waitErr := waitForHerdrCodexTUIUnlocked(
			context.Background(),
			Request{Agent: "codex", LaunchMode: agent.ModePlan, CodexPlanStatusPath: statusPath},
			locked, repo, intent,
		)
		waited <- waitResult{status: status, err: waitErr}
	}()
	expiredCtx, expiredCancel := context.WithTimeout(context.Background(), time.Second)
	defer expiredCancel()
	contender, err = state.LockProjectForLaunchContext(expiredCtx, repo)
	if err != nil {
		t.Fatalf("expired readiness wait kept the launch lock: %v", err)
	}
	if writeErr := os.WriteFile(statusPath, []byte(`{"status":"ready","threadId":"expired-thread","sessionId":"expired-session"}`), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	time.Sleep(time.Until(time.UnixMilli(intent.ExpiresUnixMS)) + 20*time.Millisecond)
	if unlockErr := contender.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	got = <-waited
	if got.err == nil || !strings.Contains(got.err.Error(), "expired") {
		t.Fatalf("expired readiness wait = %+v, err %v", got.status, got.err)
	}
}

func TestValidateHerdrLaunchBindingRejectsCodexPlanModeChange(t *testing.T) {
	launch := validTestHerdrLaunch()
	launch.Agent = "codex"
	launch.CodexPlanStatusPath = "/tmp/status.json"
	if err := validateHerdrLaunchBinding(Request{Agent: "codex"}, launch); err == nil ||
		!strings.Contains(err.Error(), "current Codex Plan Mode") {
		t.Fatalf("Plan mode mismatch error = %v", err)
	}
}

func TestPrepareHerdrLaunchRejectsTeamBindingChange(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		savedDBPath, requestedDBPath string
		savedCodex, requestedCodex   bool
		want                         string
	}{
		{
			name: "team to non-team", savedDBPath: "/tmp/team-a.db", savedCodex: true,
			want: "current team mode",
		},
		{
			name: "non-team to team", requestedDBPath: "/tmp/team-a.db", requestedCodex: true,
			want: "current team mode",
		},
		{
			name: "Claude team DB changed", savedDBPath: "/tmp/team-a.db", requestedDBPath: "/tmp/team-b.db",
			want: "current team DB path",
		},
		{
			name: "Codex team DB changed", savedDBPath: "/tmp/team-a.db", requestedDBPath: "/tmp/team-b.db",
			savedCodex: true, requestedCodex: true, want: "current team DB path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newHerdrRealizeRepo(t)
			runtime := &fakeHerdrLaunchRuntime{}
			installSuccessfulHerdrMutations(t, repo, &runtime.fakeHerdrRealizeRuntime)
			hooks := deterministicHerdrRealizeHooks()
			realizeTestHerdrCoordinator(t, repo, &runtime.fakeHerdrRealizeRuntime, hooks)
			result, err := realizeHerdrWorktree(
				context.Background(), testHerdrWorktreeRequest(repo, "team-mode", 568),
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
			intent.Launch.TokenIssued = false
			intent.Launch.TeamDBPath = tc.savedDBPath
			if tc.savedCodex {
				intent.Launch.Agent = "codex"
				intent.Launch.CodexTeamStatusPath = filepath.Join(t.TempDir(), "saved-status.json")
			}
			journal, err := locked.HerdrIntents(repo)
			if err != nil {
				t.Fatal(err)
			}
			journal.UpsertIntent(intent)
			if saveErr := journal.Save(); saveErr != nil {
				t.Fatal(saveErr)
			}

			_, err = (&Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime}).prepareHerdrLaunch(
				Request{
					Agent: "codex", TeamDBPath: tc.requestedDBPath, CodexTeamMode: tc.requestedCodex,
					CodexTeamStatusPath: filepath.Join(t.TempDir(), "requested-status.json"),
				}, locked, herdrrun.OwnedLaunchRoute{}, intent, nil,
			)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("team mode mismatch error = %v", err)
			}
			if runtime.tokenCalls != 0 {
				t.Fatalf("team mode mismatch issued %d launch token(s)", runtime.tokenCalls)
			}
		})
	}
}

func TestAwaitHerdrCodexTeamFailureRequiresManualCleanup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		writeStatus func(*testing.T, string)
		remaining   time.Duration
		wantFailure string
	}{
		{
			name: "failed status", remaining: time.Second, wantFailure: "watcher boom",
			writeStatus: func(t *testing.T, path string) {
				t.Helper()
				if err := codexapp.WriteFailedStatus(path, errors.New("watcher boom")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{name: "status timeout", remaining: 2 * time.Second, wantFailure: "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newHerdrRealizeRepo(t)
			runtime := &fakeHerdrRealizeRuntime{}
			installSuccessfulHerdrMutations(t, repo, runtime)
			hooks := deterministicHerdrRealizeHooks()
			realizeTestHerdrCoordinator(t, repo, runtime, hooks)
			req := testHerdrWorktreeRequest(repo, "team-failure", 568)
			result, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
			if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
				t.Fatal(err)
			}
			locked, err := state.LockProjectForLaunch(repo)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = locked.Unlock() })
			intent := result.Intent
			statusPath := filepath.Join(t.TempDir(), "status.json")
			intent.Launch = validTestHerdrLaunch()
			intent.Launch.Agent = "codex"
			intent.Launch.TeamDBPath = "/tmp/team.db"
			intent.Launch.CodexTeamStatusPath = statusPath
			intent.ExpiresUnixMS = time.Now().Add(tc.remaining).UnixMilli()
			journal, err := locked.HerdrIntents(repo)
			if err != nil {
				t.Fatal(err)
			}
			journal.UpsertIntent(intent)
			if saveErr := journal.Save(); saveErr != nil {
				t.Fatal(saveErr)
			}
			if tc.writeStatus != nil {
				tc.writeStatus(t, statusPath)
			}

			_, err = awaitHerdrCodexTUI(
				context.Background(),
				Request{
					TeamDBPath:          "/tmp/team.db",
					CodexTeamMode:       true,
					CodexTeamStatusPath: filepath.Join(t.TempDir(), "regenerated-status.json"),
				}, locked, repo, intent,
			)
			if !errors.Is(err, ErrHerdrManualCleanupRequired) {
				t.Fatalf("Codex team readiness error = %v, want manual cleanup", err)
			}
			journal, err = locked.HerdrIntents(repo)
			if err != nil {
				t.Fatal(err)
			}
			saved, found := journal.FindIntent(intent.ID)
			if !found || saved.Status != state.HerdrIntentManualCleanupRequired ||
				saved.Launch == nil || !saved.Launch.TokenIssued ||
				!strings.Contains(saved.Failure, tc.wantFailure) {
				t.Fatalf("saved failed team intent = (%+v, %t)", saved, found)
			}
			if store, loadErr := state.LoadProject(repo); loadErr != nil {
				t.Fatal(loadErr)
			} else if _, found := store.Find(req.Parent, req.IssueNum); found {
				t.Fatal("failed team launch wrote a final state row")
			}
			mutationCount := len(runtime.mutations)
			if err := locked.Unlock(); err != nil {
				t.Fatal(err)
			}
			if _, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, ErrHerdrManualCleanupRequired) {
				t.Fatalf("retry error = %v, want saved manual cleanup", err)
			}
			if len(runtime.mutations) != mutationCount {
				t.Fatalf("retry issued %d new mutation(s)", len(runtime.mutations)-mutationCount)
			}
		})
	}
}

func TestHerdrCoordinatorRuntimeParentProjectsWatcherIssue(t *testing.T) {
	intent := state.HerdrIntent{RuntimeParent: WatchParentRef, IssueNum: 528}
	if got := herdrCoordinatorRuntimeParent(intent); got != "528" {
		t.Fatalf("runtime parent = %q, want 528", got)
	}
}

func TestRecordHerdrCoordinatorReusesLinkedWorktreeStateRow(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "linked-row", sibling, "HEAD")
	route := herdrrun.OwnedLaunchRoute{Session: "fanout-test", SocketPath: "/tmp/fanout-test.sock"}
	intent := state.HerdrIntent{
		RuntimeParent: "528",
		Resource: state.HerdrResource{
			WorkspaceID: "workspace-1", PaneID: "workspace-1:pane-1",
			TerminalID: "terminal-1", CurrentPath: repo,
		},
	}

	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}}
	if recordErr := launcher.recordHerdrCoordinator(locked, intent, route); recordErr != nil {
		t.Fatal(recordErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	locked, err = state.LockProjectForLaunch(sibling)
	if err != nil {
		t.Fatal(err)
	}
	launcher.Info.ProjectRoot = sibling
	if recordErr := launcher.recordHerdrCoordinator(locked, intent, route); recordErr != nil {
		t.Fatal(recordErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	owner, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	other, err := state.LoadProject(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.Panes) != 1 || owner.Panes[0].WorktreePath != repo || len(other.Panes) != 0 {
		t.Fatalf("coordinator rows = owner %+v, sibling %+v", owner.Panes, other.Panes)
	}
}

func TestRecordHerdrCoordinatorScopesPlanSlugToOwnerRoot(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "linked-plan-row", sibling, "HEAD")
	route := herdrrun.OwnedLaunchRoute{Session: "fanout-test", SocketPath: "/tmp/fanout-test.sock"}

	for index, root := range []string{repo, sibling} {
		locked, err := state.LockProjectForLaunch(root)
		if err != nil {
			t.Fatal(err)
		}
		intent := state.HerdrIntent{
			Parent: "plan:demo", RuntimeParent: "plan:demo", OwnerProjectRoot: root,
			Resource: state.HerdrResource{
				WorkspaceID: fmt.Sprintf("workspace-%d", index+1),
				PaneID:      fmt.Sprintf("workspace-%d:pane-1", index+1),
				TerminalID:  fmt.Sprintf("terminal-%d", index+1),
				CurrentPath: root,
			},
		}
		launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: root}}
		if recordErr := launcher.recordHerdrCoordinator(locked, intent, route); recordErr != nil {
			t.Fatal(recordErr)
		}
		if err := locked.Unlock(); err != nil {
			t.Fatal(err)
		}
	}

	owner, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	other, err := state.LoadProject(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.Panes) != 1 || owner.Panes[0].HerdrWorkspaceID != "workspace-1" ||
		len(other.Panes) != 1 || other.Panes[0].HerdrWorkspaceID != "workspace-2" {
		t.Fatalf("plan coordinator rows = owner %+v, sibling %+v", owner.Panes, other.Panes)
	}
}

func TestOptionalHerdrAgentSession(t *testing.T) {
	valid := &backend.AgentSessionRef{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-1"}
	foreign := &backend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-2"}
	foreignSource := &backend.AgentSessionRef{Source: "foreign:claude", Agent: "claude", Kind: "id", Value: "session-3"}
	invalid := &backend.AgentSessionRef{Agent: "claude"}
	tests := []struct {
		name    string
		session *backend.AgentSessionRef
		want    bool
	}{
		{name: "not reported", want: true},
		{name: "reported exact", session: valid, want: true},
		{name: "foreign agent", session: foreign},
		{name: "foreign source", session: foreignSource},
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

func TestRetryHerdrObservationRetriesOnlyMarkedFailure(t *testing.T) {
	intent := state.HerdrIntent{ExpiresUnixMS: time.Now().Add(5 * time.Second).UnixMilli()}
	calls := 0
	err := retryHerdrObservation(context.Background(), intent, func(context.Context) error {
		calls++
		if calls == 1 {
			return retryableHerdrObservationError{}
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("retry observation = calls %d, err %v; want one retry", calls, err)
	}
}

func TestObserveExactHerdrAgentReturnsPermanentObservationError(t *testing.T) {
	runtime := &fakeHerdrLaunchRuntime{liveErr: errors.New("malformed snapshot")}
	launcher := &Launcher{Herdr: runtime}
	intent := state.HerdrIntent{ExpiresUnixMS: time.Now().Add(time.Second).UnixMilli()}

	_, found, err := launcher.observeExactHerdrAgent(context.Background(), intent, "agent-1")
	if err == nil || found {
		t.Fatalf("permanent observation = found %t, err %v; want immediate failure", found, err)
	}
}

func TestExactHerdrLaunchPaneRequiresProviderAndAcceptsOptionalSession(t *testing.T) {
	intent := state.HerdrIntent{
		WorktreePath: "/repo/.fanout/worktrees/child",
		Session:      "fanout-owned", SocketPath: "/tmp/fanout-owned/herdr.sock",
		Resource: state.HerdrResource{
			WorkspaceID: "w1", PaneID: "w1:p1", TerminalID: "term-1",
			RepoKey: "/repo/.git", CurrentPath: "/repo/.fanout/worktrees/child",
		},
		Launch: &state.HerdrLaunch{Agent: "codex"},
	}
	live := testHerdrIdlePane(intent)
	live.AgentID = "fanout-child"
	live.AgentProvider = "codex"
	live.AgentPresent = true
	live.AgentSession = &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "thread-1",
	}
	if _, found := exactHerdrLaunchPane(intent, []backend.LivePane{live}, "fanout-child"); !found {
		t.Fatal("exactHerdrLaunchPane() rejected a valid optional session")
	}
	live.AgentProvider = "claude"
	if _, found := exactHerdrLaunchPane(intent, []backend.LivePane{live}, "fanout-child"); found {
		t.Fatal("exactHerdrLaunchPane() accepted a different provider")
	}
}

func TestVerifyHerdrAgentProcessAcceptsDirectAndInterpreterChains(t *testing.T) {
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
			PID: 42, ParentPID: 1, ProcessGroup: 42, Executable: intent.Launch.Executable,
			CWD:   intent.WorktreePath,
			Argv0: intent.Launch.Executable, Argv: intent.Launch.Args,
		}},
	}
	if err := verifyHerdrAgentProcess(info, intent); err != nil {
		t.Fatalf("exact process rejected: %v", err)
	}

	info.ForegroundProcesses = []herdrrun.PaneProcess{
		{
			PID: 42, ParentPID: 1, ProcessGroup: 42, Executable: "/usr/bin/node",
			CWD: intent.WorktreePath, Argv0: "/usr/bin/node",
			Argv: append([]string{intent.Launch.Executable}, intent.Launch.Args...),
		},
		{
			PID: 43, ParentPID: 42, ProcessGroup: 42, Executable: "/opt/lib/codex",
			CWD: intent.WorktreePath, Argv0: "/opt/lib/codex", Argv: intent.Launch.Args,
		},
	}
	if err := verifyHerdrAgentProcess(info, intent); err != nil {
		t.Fatalf("interpreter process chain rejected: %v", err)
	}
}

func TestVerifyHerdrAgentProcessRejectsAmbiguousOrForeignChains(t *testing.T) {
	intent := state.HerdrIntent{
		WorktreePath: "/repo/worktree",
		Launch: &state.HerdrLaunch{
			Executable: "/opt/bin/codex",
			Args:       []string{"prompt with spaces"},
		},
	}
	root := herdrrun.PaneProcess{
		PID: 42, ParentPID: 1, ProcessGroup: 42, Executable: "/usr/bin/node",
		CWD: intent.WorktreePath, Argv0: "/usr/bin/node",
		Argv: append([]string{intent.Launch.Executable}, intent.Launch.Args...),
	}
	child := herdrrun.PaneProcess{
		PID: 43, ParentPID: 42, ProcessGroup: 42, Executable: "/opt/lib/codex",
		CWD: intent.WorktreePath, Argv0: "/opt/lib/codex", Argv: intent.Launch.Args,
	}
	info := herdrrun.PaneProcessInfo{
		ShellPID: 42, ForegroundProcessGroup: 42,
		ForegroundProcesses: []herdrrun.PaneProcess{root, child},
	}

	foreignRoot := root
	foreignRoot.Argv = []string{"/foreign/codex", intent.Launch.Args[0]}
	info.ForegroundProcesses[0] = foreignRoot
	if err := verifyHerdrAgentProcess(info, intent); err == nil {
		t.Fatal("foreign interpreter entrypoint was accepted")
	}

	info.ForegroundProcesses = []herdrrun.PaneProcess{root, child}
	info.ForegroundProcesses[0].Executable = "/foreign/not-node"
	if err := verifyHerdrAgentProcess(info, intent); err == nil {
		t.Fatal("interpreter argv0 from a different OS executable was accepted")
	}

	info.ForegroundProcesses = []herdrrun.PaneProcess{root, child}
	info.ForegroundProcesses[1].Executable = "/foreign/not-codex"
	if err := verifyHerdrAgentProcess(info, intent); err == nil {
		t.Fatal("agent argv0 from a different OS executable was accepted")
	}

	info.ForegroundProcesses = []herdrrun.PaneProcess{root, child}
	info.ForegroundProcesses[1].ParentPID = 99
	if err := verifyHerdrAgentProcess(info, intent); err == nil {
		t.Fatal("unrelated child process was accepted")
	}

	duplicate := child
	duplicate.PID = 44
	info.ForegroundProcesses = []herdrrun.PaneProcess{root, child, duplicate}
	if err := verifyHerdrAgentProcess(info, intent); err == nil {
		t.Fatal("ambiguous native children were accepted")
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

func TestHerdrLaunchDoesNotIssueTokenAfterLauncherWaitExpires(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrLaunchRuntime{}
	installSuccessfulHerdrMutations(t, repo, &runtime.fakeHerdrRealizeRuntime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, &runtime.fakeHerdrRealizeRuntime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "wait-expired", 533),
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
	intent.ExpiresUnixMS = time.Now().Add(40 * time.Millisecond).UnixMilli()
	intent.Launch = validTestHerdrLaunch()
	intent.Launch.TokenIssued = false
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if saveErr := journal.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	runtime.wait = func(context.Context, string, string, time.Duration) error {
		time.Sleep(60 * time.Millisecond)
		return nil
	}
	_, err = (&Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime}).startHerdrAgent(
		context.Background(), Request{Agent: "claude"}, locked,
		herdrrun.OwnedLaunchRoute{}, intent, nil,
	)
	if err == nil || runtime.tokenCalls != 0 {
		t.Fatalf("expired launch error/token calls = %v/%d, want error/0", err, runtime.tokenCalls)
	}
}

func TestHerdrCodexTeamFailedStatusStopsAgentWait(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrLaunchRuntime{}
	installSuccessfulHerdrMutations(t, repo, &runtime.fakeHerdrRealizeRuntime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, &runtime.fakeHerdrRealizeRuntime, hooks)
	result, err := realizeHerdrWorktree(
		context.Background(), testHerdrWorktreeRequest(repo, "team-start-failure", 568),
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
	intent.ExpiresUnixMS = time.Now().Add(time.Minute).UnixMilli()
	statusPath := filepath.Join(t.TempDir(), "status.json")
	intent.Launch = validTestHerdrLaunch()
	intent.Launch.Agent = "codex"
	intent.Launch.TokenIssued = false
	intent.Launch.TeamDBPath = "/tmp/team.db"
	intent.Launch.CodexTeamStatusPath = statusPath
	route := herdrrun.OwnedLaunchRoute{LauncherPath: "/owned/fanout"}
	runtime.processInfo = testHerdrLauncherProcess(intent, route.LauncherPath)
	runtime.live = []backend.LivePane{testHerdrIdlePane(intent)}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if saveErr := journal.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if statusErr := codexapp.WriteFailedStatus(statusPath, errors.New("owner mismatch")); statusErr != nil {
		t.Fatal(statusErr)
	}

	_, err = (&Launcher{Info: &fanoutruntime.Info{ProjectRoot: repo}, Herdr: runtime}).startHerdrAgent(
		context.Background(), Request{
			Agent: "codex", TeamDBPath: "/tmp/team.db", CodexTeamMode: true,
			CodexTeamStatusPath: filepath.Join(t.TempDir(), "regenerated-status.json"),
		}, locked, route, intent, nil,
	)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) || !strings.Contains(err.Error(), "owner mismatch") {
		t.Fatalf("failed team agent start error = %v", err)
	}
	if runtime.tokenCalls != 1 || runtime.liveCalls != 1 {
		t.Fatalf("failed team token/live calls = %d/%d, want 1/1", runtime.tokenCalls, runtime.liveCalls)
	}
	journal, err = locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, found := journal.FindIntent(intent.ID)
	if !found || saved.Status != state.HerdrIntentManualCleanupRequired ||
		!strings.Contains(saved.Failure, "owner mismatch") {
		t.Fatalf("saved failed team start = (%+v, %t)", saved, found)
	}
}

func TestHerdrLaunchDoesNotRenameAfterProcessCheckExpires(t *testing.T) {
	runtime := &fakeHerdrLaunchRuntime{}
	intent := state.HerdrIntent{
		WorktreePath: "/repo/worktree", ExpiresUnixMS: time.Now().Add(40 * time.Millisecond).UnixMilli(),
		Resource: state.HerdrResource{PaneID: "w1:p1"},
		Launch: &state.HerdrLaunch{
			AgentName: "fanout-child", Executable: "/bin/claude", Args: []string{"prompt"},
		},
	}
	runtime.process = func(context.Context, string) (herdrrun.PaneProcessInfo, error) {
		time.Sleep(60 * time.Millisecond)
		return herdrrun.PaneProcessInfo{
			ShellPID: 42, ForegroundProcessGroup: 42,
			ForegroundProcesses: []herdrrun.PaneProcess{{
				PID: 42, CWD: intent.WorktreePath, Argv: []string{"/bin/claude", "prompt"},
			}},
		}, nil
	}
	err := (&Launcher{Herdr: runtime}).verifyAndRenameHerdrAgent(context.Background(), intent)
	if err == nil || runtime.renameCalls != 0 {
		t.Fatalf("expired process check error/rename calls = %v/%d, want error/0", err, runtime.renameCalls)
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
	if saveErr := journal.Save(); saveErr != nil {
		t.Fatal(saveErr)
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
				WorktreePath: result.CWD, Session: runtime.launchRoute.Session,
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

func TestHerdrCoordinatorRetriesTransientIdentityObservation(t *testing.T) {
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
				WorktreePath: result.CWD, Session: runtime.launchRoute.Session,
				SocketPath: runtime.launchRoute.SocketPath,
				Resource:   stateResource(result.WorkspaceObservation),
			}
			runtime.processInfo = testHerdrLauncherProcess(intent, runtime.launchRoute.LauncherPath)
			runtime.live = []backend.LivePane{testHerdrIdlePane(intent)}
		}
		return result, err
	}
	processCalls := 0
	runtime.process = func(context.Context, string) (herdrrun.PaneProcessInfo, error) {
		processCalls++
		if processCalls == 1 {
			return herdrrun.PaneProcessInfo{}, retryableHerdrObservationError{}
		}
		return runtime.processInfo, nil
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
	intent, err := launcher.realizeHerdrCoordinator(
		context.Background(), Request{ParentRef: "425"}, locked, runtime.launchRoute,
	)
	if err != nil || intent.ID == "" || processCalls != 2 {
		t.Fatalf("coordinator realization = intent %+v, calls %d, err %v; want one successful retry", intent, processCalls, err)
	}
}

func TestHerdrCoordinatorReusesExpiredIntentWithinCurrentObservationBudget(t *testing.T) {
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
				WorktreePath: result.CWD, Session: runtime.launchRoute.Session,
				SocketPath: runtime.launchRoute.SocketPath,
				Resource:   stateResource(result.WorkspaceObservation),
			}
			runtime.processInfo = testHerdrLauncherProcess(intent, runtime.launchRoute.LauncherPath)
			runtime.live = []backend.LivePane{testHerdrIdlePane(intent)}
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
	intent, err := launcher.realizeHerdrCoordinator(
		context.Background(), Request{ParentRef: "425"}, locked, runtime.launchRoute,
	)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent.ExpiresUnixMS = time.Now().Add(-time.Second).UnixMilli()
	journal.UpsertIntent(intent)
	err = journal.Save()
	if err != nil {
		t.Fatal(err)
	}
	mutationCount := len(runtime.mutations)

	reused, err := launcher.realizeHerdrCoordinator(
		context.Background(), Request{ParentRef: "425"}, locked, runtime.launchRoute,
	)
	if err != nil || reused.ID != intent.ID || len(runtime.mutations) != mutationCount {
		t.Fatalf(
			"expired coordinator reuse = %+v, mutations=%d, err=%v",
			reused,
			len(runtime.mutations),
			err,
		)
	}
}

func TestHerdrCoordinatorRecordConflictRetainsManualCleanupIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrLaunchRuntime{}
	installSuccessfulHerdrMutations(t, repo, &runtime.fakeHerdrRealizeRuntime)
	runtime.launchRoute = herdrrun.OwnedLaunchRoute{
		GitCommonDir: runtime.route.GitCommonDir,
		Session:      "fanout-test", SocketPath: "/private/tmp/fanout-test/herdr.sock",
		LauncherPath: "/owned/fanout",
	}
	mutate := runtime.mutate
	runtime.mutate = func(req herdrTestMutation) (herdrrun.WorktreeMutationResult, error) {
		result, err := mutate(req)
		if err == nil && req.Kind == herdrrun.WorkspaceCreate {
			intent := state.HerdrIntent{
				WorktreePath: result.CWD, Session: runtime.launchRoute.Session,
				SocketPath: runtime.launchRoute.SocketPath,
				Resource:   stateResource(result.WorkspaceObservation),
			}
			runtime.processInfo = testHerdrLauncherProcess(intent, runtime.launchRoute.LauncherPath)
			runtime.live = []backend.LivePane{testHerdrIdlePane(intent)}
		}
		return result, err
	}
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	if recordErr := locked.RecordPane(state.Pane{
		Parent: ManualParentRef, RuntimeParent: "425", IssueNum: -1,
		Kind: state.PaneKindShell, Backend: backend.Herdr,
		PaneID: "foreign:pane", HerdrWorkspaceID: "foreign",
		HerdrTerminalID: "foreign-terminal", HerdrSession: runtime.launchRoute.Session,
		HerdrSocketPath: runtime.launchRoute.SocketPath, WorktreePath: repo,
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	launcher := &Launcher{
		Cfg: &cliflags.Config{}, Log: log.NewWith(io.Discard, io.Discard, false),
		Info: &fanoutruntime.Info{ProjectRoot: repo}, Recorder: locked, Herdr: runtime,
	}
	intent, realizeErr := launcher.realizeHerdrLaunch(Request{ParentRef: "425"}, herdrLaunchOperation{
		ctx: context.Background(), locked: locked, route: runtime.launchRoute,
	})
	if realizeErr == nil || intent.ID == "" || intent.Kind != state.HerdrIntentCoordinator {
		t.Fatalf("realize result = (%+v, %v), want retained coordinator conflict", intent, realizeErr)
	}
	rollbackErr := launcher.rollbackFailedHerdrLaunch(locked, intent, realizeErr)
	if !errors.Is(rollbackErr, ErrHerdrManualCleanupRequired) {
		t.Fatalf("rollback error = %v, want manual cleanup", rollbackErr)
	}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, found := journal.FindIntent(intent.ID)
	if !found || saved.Status != state.HerdrIntentManualCleanupRequired ||
		!strings.Contains(saved.Failure, "record coordinator") {
		t.Fatalf("saved coordinator = (%+v, %t), want record conflict requiring cleanup", saved, found)
	}
}

func TestHerdrLaunchRollbackUsesSavedIdentityAfterLauncherExit(t *testing.T) {
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
	runtime.processErr = errors.New("launcher exited")
	runtime.liveErr = errors.New("launcher exited")
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
	if rollbackErr := launcher.rollbackHerdrLaunch(locked, intent, errors.New("launcher exited")); rollbackErr != nil {
		t.Fatal(rollbackErr)
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
			PID: 42, CWD: intent.WorktreePath, Argv0: launcherPath,
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
