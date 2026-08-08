package stateemitter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type fakeObserver struct {
	observation Observation
	err         error
	targets     []RuntimeTarget
}

func (f *fakeObserver) Observe(_ context.Context, target RuntimeTarget) (Observation, error) {
	f.targets = append(f.targets, target)
	return f.observation, f.err
}

func TestEmitUpdatesOnlyFinalRowTelemetry(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "working" || !got.StateRefinement {
		t.Fatalf("telemetry = (%q, %t), want working refinement", got.ReportedState, got.StateRefinement)
	}
	if got.AgentStatus != pane.AgentStatus || got.PaneID != pane.PaneID || got.BranchName != pane.BranchName {
		t.Fatalf("authoritative fields changed: got %+v, before %+v", got, pane)
	}
	if len(observer.targets) != 1 || observer.targets[0].PaneID != pane.PaneID {
		t.Fatalf("observer targets = %+v", observer.targets)
	}
}

func TestEmitFinalRowFailsClosedOnBindingMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.Pane, *telemetry.Signal, *fakeObserver)
		count  int
	}{
		{name: "zero rows", count: 0},
		{name: "multiple rows", count: 2},
		{name: "old generation", count: 1, mutate: func(_ *state.Pane, signal *telemetry.Signal, _ *fakeObserver) {
			signal.LaunchNonce = strings.Repeat("c", 32)
		}},
		{name: "wrong backend", count: 1, mutate: func(_ *state.Pane, signal *telemetry.Signal, _ *fakeObserver) {
			signal.Backend = backend.Tmux
		}},
		{name: "saved pane ref mismatch", count: 1, mutate: func(_ *state.Pane, signal *telemetry.Signal, _ *fakeObserver) {
			signal.PaneID = "workspace-1:pane-2"
		}},
		{name: "current pane ref mismatch", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.Panes[0].Ref.Pane = "workspace-1:pane-2"
		}},
		{name: "process mismatch", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.ProcessInfo.ForegroundProcesses[0].Argv = []string{"wrong"}
		}},
		{name: "process executable mismatch", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.ProcessInfo.ForegroundProcesses[0].Executable = "/foreign/not-claude"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newEmitterRepo(t)
			pane, signal, observer := finalEmitterFixture(t, repo)
			if test.mutate != nil {
				test.mutate(&pane, &signal, observer)
			}
			panes := make([]state.Pane, test.count)
			for i := range panes {
				panes[i] = pane
			}
			saveEmitterPanes(t, repo, panes...)
			if err := Emit(context.Background(), signal, observer); err == nil {
				t.Fatal("Emit() succeeded")
			}
			if test.count > 0 && loadEmitterPane(t, repo).ReportedState != "running" {
				t.Fatal("failed signal changed reported state")
			}
		})
	}
}

func TestEmitFinalRowInvalidatesTelemetryWhenTerminalChanges(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)
	observer.observation.Panes[0].TerminalID = "replacement-terminal"

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "" || got.StateRefinement {
		t.Fatalf("invalidated telemetry = (%q, %t), want empty and unrefined", got.ReportedState, got.StateRefinement)
	}
	if got.EmitterNonce == pane.EmitterNonce || !telemetry.ValidNonce(got.EmitterNonce) {
		t.Fatalf("rotated emitter nonce = %q, old %q", got.EmitterNonce, pane.EmitterNonce)
	}
}

func TestEmitFinalRowDoesNotInvalidateForForeignAgentOnChangedTerminal(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)
	observer.observation.Panes[0].TerminalID = "replacement-terminal"
	observer.observation.Panes[0].AgentID = "foreign-agent"

	if err := Emit(context.Background(), signal, observer); err == nil {
		t.Fatal("Emit() invalidated telemetry without an exact non-terminal identity")
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != pane.ReportedState || got.EmitterNonce != pane.EmitterNonce {
		t.Fatalf("foreign agent changed telemetry binding: %+v", got)
	}
}

func TestEmitPendingIntentPersistsDoneUntilFinalSave(t *testing.T) {
	repo := newEmitterRepo(t)
	intent, signal, observer := pendingEmitterFixture(t, repo)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}

	for _, reported := range []backend.AgentState{backend.AgentIdle, backend.AgentDone, backend.AgentWorking} {
		signal.State = reported
		if err := Emit(context.Background(), signal, observer); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := state.LoadHerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := stored.FindIntent(intent.ID)
	if !found || got.Launch.PendingReportedState != "done" {
		t.Fatalf("pending intent = (%+v, %t), want done", got, found)
	}
}

func TestRunSignalHandsConcurrentPreFinalReportsToPendingIntent(t *testing.T) {
	repo := newEmitterRepo(t)
	intent, signal, observer := pendingEmitterFixture(t, repo)
	saveEmitterIntent(t, repo, intent)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()

	errs := make(chan error, 2)
	for _, reported := range []backend.AgentState{backend.AgentWorking, backend.AgentDone} {
		report := signal
		report.State = reported
		go func() { errs <- runSignal(report, observer) }()
	}
	waitForEmitterHandoffs(t, signal.StatePath, signal.EmitterNonce, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	completed, err := locked.YieldForEmitter(ctx, repo, signal.StatePath, signal.EmitterNonce)
	if err != nil || !completed {
		t.Fatalf("YieldForEmitter() = (%t, %v), want completed", completed, err)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := journal.FindIntent(intent.ID)
	if !found || got.Launch.PendingReportedState != "done" {
		t.Fatalf("handed-off pending intent = (%+v, %t), want done", got, found)
	}
}

func TestEmitPendingIntentRejectsIncompleteIdentityMatch(t *testing.T) {
	repo := newEmitterRepo(t)
	intent, signal, observer := pendingEmitterFixture(t, repo)
	saveEmitterIntent(t, repo, intent)
	signal.TerminalID = "replacement-terminal"

	if err := Emit(context.Background(), signal, observer); err == nil {
		t.Fatal("Emit() succeeded with a mismatched provisional terminal")
	}
	stored, err := state.LoadHerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := stored.FindIntent(intent.ID)
	if !found || got.Launch.PendingReportedState != "" {
		t.Fatalf("mismatched provisional signal changed intent: (%+v, %t)", got, found)
	}
}

func TestEmitPendingIntentRejectsExpiredLaunchWithoutMutation(t *testing.T) {
	repo := newEmitterRepo(t)
	intent, signal, observer := pendingEmitterFixture(t, repo)
	intent.ExpiresUnixMS = time.Now().Add(-time.Second).UnixMilli()
	saveEmitterIntent(t, repo, intent)

	if err := Emit(context.Background(), signal, observer); err == nil {
		t.Fatal("Emit() succeeded with an expired provisional intent")
	}
	stored, err := state.LoadHerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := stored.FindIntent(intent.ID)
	if !found || got.Launch.PendingReportedState != "" {
		t.Fatalf("expired provisional signal changed intent: (%+v, %t)", got, found)
	}
}

func finalEmitterFixture(t *testing.T, repo string) (state.Pane, telemetry.Signal, *fakeObserver) {
	t.Helper()
	worktree := filepath.Join(repo, ".fanout", "worktrees", "telemetry")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	pane := state.Pane{
		Parent: "524", IssueNum: 529, Slug: "telemetry", BranchName: "fanout/telemetry",
		Backend: backend.Herdr, PaneID: "workspace-1:pane-1", Agent: "claude",
		WorktreePath: worktree, AgentStatus: "running", ReportedState: "running",
		HerdrWorkspaceID: "workspace-1", HerdrTerminalID: "terminal-1",
		HerdrRepoKey: filepath.Join(repo, ".git"), HerdrAgentID: "fanout-agent",
		HerdrSession: "fanout-owned", HerdrSocketPath: "/tmp/fanout-owned/herdr.sock",
		EmitterRowKey: "issue:3:524:529", LaunchNonce: strings.Repeat("a", 32),
		EmitterNonce: strings.Repeat("b", 32), HerdrLaunchExecutable: "/opt/bin/claude",
		HerdrLaunchArgs: []string{"--settings", "{}", "prompt"},
	}
	signal := signalForPane(repo, pane)
	return pane, signal, exactObserver(pane)
}

func pendingEmitterFixture(t *testing.T, repo string) (state.HerdrIntent, telemetry.Signal, *fakeObserver) {
	t.Helper()
	pane, signal, observer := finalEmitterFixture(t, repo)
	intent := state.HerdrIntent{
		ID: signal.RowKey, Kind: state.HerdrIntentWorktree, Status: state.HerdrIntentRealized,
		Parent: "524", RuntimeParent: "524", IssueNum: 529,
		Slug: pane.Slug, BranchName: pane.BranchName, FullBranchRef: "refs/heads/" + pane.BranchName,
		BaseBranch: "main", BaseSHA: strings.Repeat("1", 40), ExpectedHead: strings.Repeat("1", 40),
		WorktreePath: pane.WorktreePath, WorkspaceLabel: "fanout-worktree-telemetry",
		Resource: state.HerdrResource{
			WorkspaceID: pane.HerdrWorkspaceID, Label: "fanout-worktree-telemetry",
			PaneID: pane.PaneID, TerminalID: pane.HerdrTerminalID, CurrentPath: pane.WorktreePath,
			RepoKey: pane.HerdrRepoKey, RepoRoot: pane.WorktreePath,
		},
		Coordinator: state.HerdrResource{
			WorkspaceID: "coordinator", Label: "fanout-coordinator",
			PaneID: "coordinator:pane", TerminalID: "coordinator-terminal", CurrentPath: repo,
		},
		Session: pane.HerdrSession, SocketPath: pane.HerdrSocketPath,
		ExpiresUnixMS: time.Now().Add(time.Hour).UnixMilli(),
		Launch: &state.HerdrLaunch{
			Nonce: pane.LaunchNonce, EmitterNonce: pane.EmitterNonce,
			Agent: pane.Agent, AgentName: pane.HerdrAgentID,
			Executable: pane.HerdrLaunchExecutable, Args: pane.HerdrLaunchArgs,
			EnvFilePath: "/tmp/fanout-env.json", EnvNameCount: 1,
			LauncherReady: true, TokenIssued: true,
		},
	}
	return intent, signal, observer
}

func signalForPane(repo string, pane state.Pane) telemetry.Signal {
	return telemetry.Signal{
		StatePath: state.Path(repo), RowKey: pane.EmitterRowKey,
		LaunchNonce: pane.LaunchNonce, EmitterNonce: pane.EmitterNonce,
		Backend: backend.Herdr, Session: pane.HerdrSession, SocketPath: pane.HerdrSocketPath,
		WorkspaceID: pane.HerdrWorkspaceID, PaneID: pane.PaneID,
		TerminalID: pane.HerdrTerminalID, Agent: pane.Agent, AgentID: pane.HerdrAgentID,
		State: backend.AgentWorking,
	}
}

func exactObserver(pane state.Pane) *fakeObserver {
	process := herdrrun.PaneProcess{
		PID: 42, ParentPID: 1, ProcessGroup: 42, Executable: pane.HerdrLaunchExecutable,
		Argv0: pane.HerdrLaunchExecutable, Argv: pane.HerdrLaunchArgs, CWD: pane.WorktreePath,
	}
	live := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Herdr, Workspace: pane.HerdrWorkspaceID, Pane: pane.PaneID},
		CurrentPath: pane.WorktreePath, WorktreePath: pane.WorktreePath,
		TerminalID: pane.HerdrTerminalID, RepoKey: pane.HerdrRepoKey,
		AgentPresent: true, AgentProvider: pane.Agent, AgentID: pane.HerdrAgentID,
		SessionID: pane.HerdrSession, SocketPath: pane.HerdrSocketPath,
	}
	return &fakeObserver{observation: Observation{
		Panes: []backend.LivePane{live},
		ProcessInfo: herdrrun.PaneProcessInfo{
			PaneID: pane.PaneID, ShellPID: 42, ForegroundProcessGroup: 42,
			ForegroundProcesses: []herdrrun.PaneProcess{process},
		},
	}}
}

func saveEmitterPanes(t *testing.T, repo string, panes ...state.Pane) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	locked.Panes = append([]state.Pane(nil), panes...)
	if err := locked.Save(); err != nil {
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func saveEmitterIntent(t *testing.T, repo string, intent state.HerdrIntent) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func loadEmitterPane(t *testing.T, repo string) state.Pane {
	t.Helper()
	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) == 0 {
		t.Fatal("state has no pane")
	}
	return store.Panes[0]
}

func waitForEmitterHandoffs(t *testing.T, statePath, launchNonce string, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		paths, err := state.EmitterHandoffs(statePath, launchNonce)
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d emitter handoffs", count)
}

func newEmitterRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return repo
}
