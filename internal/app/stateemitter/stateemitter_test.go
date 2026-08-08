package stateemitter

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

type blockingObserver struct {
	observation Observation
	entered     chan struct{}
	release     chan struct{}
}

func (b *blockingObserver) Observe(ctx context.Context, _ RuntimeTarget) (Observation, error) {
	close(b.entered)
	select {
	case <-b.release:
		return b.observation, nil
	case <-ctx.Done():
		return Observation{}, ctx.Err()
	}
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

func TestEmitFinalRowPersistsDoneAgainstLateSignal(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)

	for _, reported := range []backend.AgentState{backend.AgentDone, backend.AgentIdle, backend.AgentWorking} {
		signal.State = reported
		if err := Emit(context.Background(), signal, observer); err != nil {
			t.Fatal(err)
		}
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "done" || !got.StateRefinement {
		t.Fatalf("telemetry = (%q, %t), want done refinement", got.ReportedState, got.StateRefinement)
	}
}

func TestEmitObservesOutsideLockAndRevalidatesBeforeSave(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, exact := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)
	observer := &blockingObserver{
		observation: exact.observation,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	emitErr := make(chan error, 1)
	go func() {
		emitErr <- Emit(context.Background(), signal, observer)
	}()

	select {
	case <-observer.entered:
	case <-time.After(time.Second):
		t.Fatal("observer was not called")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	locked, err := state.LockProjectForLaunchContext(ctx, repo)
	if err != nil {
		t.Fatalf("lock while observing: %v", err)
	}
	locked.Panes[0].EmitterNonce = strings.Repeat("c", 32)
	if err := locked.Save(); err != nil {
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	close(observer.release)

	if err := <-emitErr; err == nil {
		t.Fatal("Emit() accepted a stale generation")
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "running" || got.EmitterNonce != strings.Repeat("c", 32) {
		t.Fatalf("stale signal changed telemetry row = %+v", got)
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
		{name: "process observation failure", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.ProcessError = errors.New("process replaced")
		}},
		{name: "foreign late session", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.Panes[0].AgentSession = &backend.AgentSessionRef{
				Source: "foreign:claude", Agent: "claude", Kind: "id", Value: "session-a",
			}
		}},
		{name: "invalid late session", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.Panes[0].AgentSession = &backend.AgentSessionRef{
				Source: "herdr:claude", Agent: "claude", Kind: "id",
			}
		}},
		{name: "ambiguous late session observations", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			ref := backend.AgentSessionRef{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-a"}
			observer.observation.Panes[0].AgentSession = &ref
			observer.observation.Panes = append(observer.observation.Panes, observer.observation.Panes[0])
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

func TestEmitFinalRowBindsOnlyFirstLateSession(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	first := backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-first",
	}
	observer.observation.Panes[0].AgentSession = &first
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.HerdrAgentSession == nil || *got.HerdrAgentSession != first || got.ReportedState != "working" {
		t.Fatalf("late-bound telemetry row = %+v", got)
	}

	second := first
	second.Value = "session-second"
	observer.observation.Panes[0].AgentSession = &second
	signal.State = backend.AgentIdle
	if err := Emit(context.Background(), signal, observer); err == nil {
		t.Fatal("Emit() accepted a replacement late session")
	}
	got = loadEmitterPane(t, repo)
	if got.HerdrAgentSession == nil || *got.HerdrAgentSession != first || got.ReportedState != "working" {
		t.Fatalf("replacement session changed telemetry row = %+v", got)
	}
}

func TestEmitFinalRowRejectsLateSessionSharedByMultipleRows(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	ref := backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-first",
	}
	observer.observation.Panes[0].AgentSession = &ref
	duplicate := pane
	duplicate.EmitterRowKey = "issue:3:524:530"
	duplicate.IssueNum = 530
	saveEmitterPanes(t, repo, pane, duplicate)

	if err := Emit(context.Background(), signal, observer); err == nil {
		t.Fatal("Emit() accepted a late session shared by multiple rows")
	}
	got := loadEmitterPane(t, repo)
	if got.HerdrAgentSession != nil || got.ReportedState != "running" {
		t.Fatalf("ambiguous late session changed telemetry row = %+v", got)
	}
}

func TestEmitFinalRowInvalidatesTelemetryWhenTerminalChanges(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)
	observer.observation.Panes[0].TerminalID = "replacement-terminal"
	observer.observation.ProcessError = errors.New("process replaced")

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

func TestEmitFinalRowInvalidatesChangedTerminalWithForeignAgentRecord(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)
	observer.observation.Panes[0].TerminalID = "replacement-terminal"
	observer.observation.Panes[0].AgentID = "foreign-agent"

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "" || got.StateRefinement || got.EmitterNonce == pane.EmitterNonce {
		t.Fatalf("changed terminal retained telemetry binding: %+v", got)
	}
}

func TestEmitStopsAtContextDeadlineWhileLaunchLockIsHeld(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := Emit(ctx, signal, observer); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Emit() error = %v, want context deadline", err)
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
	err = journal.Save()
	if err != nil {
		t.Fatal(err)
	}
	err = locked.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	for _, reported := range []backend.AgentState{backend.AgentIdle, backend.AgentDone, backend.AgentWorking} {
		signal.State = reported
		err = Emit(context.Background(), signal, observer)
		if err != nil {
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
		ProjectRoot:  filepath.Dir(pane.HerdrRepoKey),
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

func newEmitterRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return repo
}
