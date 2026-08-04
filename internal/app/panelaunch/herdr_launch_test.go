package panelaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type fakeHerdrLaunchRuntime struct {
	fakeHerdrRealizeRuntime
	live []backend.LivePane
}

func (f *fakeHerdrLaunchRuntime) VerifyOwned(context.Context) error { return nil }
func (f *fakeHerdrLaunchRuntime) LaunchRoute() (herdrrun.OwnedLaunchRoute, error) {
	return herdrrun.OwnedLaunchRoute{}, nil
}

func (f *fakeHerdrLaunchRuntime) PrepareWorkloadEnvironment(string, []string) (string, int, error) {
	return "/tmp/env", 1, nil
}

func (f *fakeHerdrLaunchRuntime) WaitForLauncher(context.Context, string, string, time.Duration) error {
	return nil
}

func (f *fakeHerdrLaunchRuntime) ProcessInfo(context.Context, string) (herdrrun.PaneProcessInfo, error) {
	return herdrrun.PaneProcessInfo{}, nil
}

func (f *fakeHerdrLaunchRuntime) SendLaunchToken(context.Context, string, string) error {
	return nil
}

func (f *fakeHerdrLaunchRuntime) LivePanes(context.Context) ([]backend.LivePane, error) {
	return append([]backend.LivePane(nil), f.live...), nil
}
func (f *fakeHerdrLaunchRuntime) RenameAgent(context.Context, string, string) error { return nil }

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
	err = launcher.finalizeHerdrLaunch(Request{}, locked, intent, backend.LivePane{})
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
