package stateemitter

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
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
	wantGitCommonDir, err := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if observer.targets[0].GitCommonDir != wantGitCommonDir {
		t.Fatalf("observer Git common dir = %q, want %q", observer.targets[0].GitCommonDir, wantGitCommonDir)
	}
}

func TestEmitResolvesSyntheticRowCollisionByStableIdentity(t *testing.T) {
	repo := newEmitterRepo(t)
	target, signal, observer := finalEmitterFixture(t, repo)
	foreign := target
	foreign.Parent, foreign.IssueNum = "525", -1
	foreign.WorkspaceID, foreign.WorkspaceLabel = "workspace-2", "owned-label-2"
	foreign.PaneID, foreign.TerminalID = "workspace-2:pane-1", "terminal-2"
	foreign.ReportedState = string(backend.AgentBlocked)
	foreign.ReportedStateSeq, foreign.StateRefinement = 2, true
	saveEmitterPanes(t, repo, foreign, target)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	gotTarget, found := store.Find(target.Parent, target.IssueNum)
	if !found || gotTarget.ReportedState != string(backend.AgentWorking) {
		t.Fatalf("target telemetry = (%+v, %t), want working", gotTarget, found)
	}
	gotForeign, found := store.Find(foreign.Parent, foreign.IssueNum)
	if !found || gotForeign.ReportedState != string(backend.AgentBlocked) ||
		gotForeign.ReportedStateSeq != 2 {
		t.Fatalf("foreign telemetry = (%+v, %t), want unchanged blocked", gotForeign, found)
	}
}

func TestEmitRejectsAmbiguousStableRowIdentityWithoutMutation(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	duplicate := pane
	duplicate.Parent, duplicate.IssueNum = "525", -1
	saveEmitterPanes(t, repo, pane, duplicate)

	if err := Emit(context.Background(), signal, observer); err == nil {
		t.Fatal("Emit() accepted ambiguous stable row identity")
	}
	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range store.Panes {
		if saved.ReportedState != string(backend.AgentRunning) || saved.StateRefinement {
			t.Fatalf("ambiguous signal changed row telemetry: %+v", saved)
		}
	}
}

func TestRunSequenceWritesMonotonicValues(t *testing.T) {
	repo := newEmitterRepo(t)
	getenv := func(key string) string {
		if key == telemetry.EmitterPathEnv {
			return state.Path(repo)
		}
		return ""
	}
	for _, want := range []string{"1\n", "2\n"} {
		var out, errOut bytes.Buffer
		if code := RunSequence(getenv, &out, &errOut); code != 0 {
			t.Fatalf("RunSequence() = %d, stderr %q", code, errOut.String())
		}
		if out.String() != want {
			t.Fatalf("RunSequence() output = %q, want %q", out.String(), want)
		}
	}
}

func TestEmitFinalRowDiscardsOlderClaudeSequence(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)

	signal.State, signal.Sequence = backend.AgentBlocked, 2
	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	signal.State, signal.Sequence = backend.AgentWorking, 1
	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "blocked" || got.ReportedStateSeq != 2 || !got.StateRefinement {
		t.Fatalf("telemetry = (%q, %d, %t), want blocked at sequence 2", got.ReportedState, got.ReportedStateSeq, got.StateRefinement)
	}
}

func TestEmitInvalidatesSequencedClaudeRowWithMissingWatermark(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	pane.ReportedState = string(backend.AgentBlocked)
	pane.ReportedStateSeq = 0
	pane.StateRefinement = true
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "" || got.ReportedStateSeq != 0 || got.StateRefinement ||
		got.EmitterNonce == pane.EmitterNonce {
		t.Fatalf("missing watermark was not fenced: %+v", got)
	}
}

func TestEmitLegacyWriterCannotEraseSequencedWatermark(t *testing.T) {
	repo := newEmitterRepo(t)
	current, _, _ := finalEmitterFixture(t, repo)
	current.LaunchArgs = []string{
		"--settings", `{"command":"__fanout-emitter-sequence"}`, "prompt",
	}
	legacy := current
	legacy.Parent, legacy.IssueNum = "525", 530
	legacy.PaneID, legacy.WorkspaceID = "workspace-2:pane-1", "workspace-2"
	legacy.WorkspaceLabel, legacy.TerminalID = "owned-label-2", "terminal-2"
	legacy.EmitterRowKey = "issue:3:525:530"
	legacy.LaunchNonce, legacy.EmitterNonce = strings.Repeat("c", 32), strings.Repeat("d", 32)
	legacy.LaunchArgs = []string{"--settings", `{}`, "prompt"}
	saveEmitterPanes(t, repo, legacy)

	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.RecordPane(current); err != nil {
		t.Fatal(errors.Join(err, locked.Unlock()))
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}

	signal := signalForPane(repo, current)
	signal.State, signal.Sequence = backend.AgentBlocked, 2
	if err := Emit(context.Background(), signal, exactObserver(current)); err != nil {
		t.Fatal(err)
	}
	legacySignal := signalForPane(repo, legacy)
	if err := Emit(context.Background(), legacySignal, exactObserver(legacy)); err == nil {
		t.Fatal("Emit() accepted a legacy emitter after the sequence fence")
	}
	got := loadEmitterPaneByRowKey(t, repo, current.EmitterRowKey)
	if got.ReportedState != string(backend.AgentBlocked) || got.ReportedStateSeq != 2 {
		t.Fatalf("sequenced watermark after legacy signal = (%q, %d), want blocked at 2", got.ReportedState, got.ReportedStateSeq)
	}
}

func TestEmitStaleFinalSequenceStillRebindsReplacedSession(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	first := backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-first",
	}
	second := first
	second.Value = "session-second"
	pane.AgentSession = &first
	pane.ReportedState = string(backend.AgentBlocked)
	pane.ReportedStateSeq = 2
	pane.StateRefinement = true
	observer.observation.Panes[0].AgentSession = &second
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.AgentSession == nil || *got.AgentSession != second ||
		got.ReportedState != string(backend.AgentBlocked) || got.ReportedStateSeq != 2 {
		t.Fatalf("stale sequence did not preserve telemetry and rebind session: %+v", got)
	}
}

func TestEmitStaleFinalSequenceStillInvalidatesTerminalReplacement(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	pane.ReportedState = string(backend.AgentBlocked)
	pane.ReportedStateSeq = 2
	pane.StateRefinement = true
	observer.observation.Panes[0].TerminalID = "terminal-replacement"
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "" || got.ReportedStateSeq != 0 || got.StateRefinement ||
		got.EmitterNonce == pane.EmitterNonce {
		t.Fatalf("stale sequence did not invalidate replaced terminal: %+v", got)
	}
}

func TestEmitRejectsMissingClaudeSequenceWithoutMutation(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	signal.Sequence = 0
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err == nil {
		t.Fatal("Emit() accepted a missing Claude sequence")
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != pane.ReportedState || got.StateRefinement || got.ReportedStateSeq != 0 {
		t.Fatalf("missing sequence changed telemetry row: %+v", got)
	}
}

func TestEmitUpdatesCodexPlanRowThroughExactControllerProcess(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, _, _ := finalEmitterFixture(t, repo)
	pane.Agent = "codex"
	pane.PlanMode = true
	pane.LaunchExecutable = "/opt/fanout"
	pane.LaunchArgs = []string{
		codexapp.PlanTUICommand, "--codex", "/opt/codex",
		"--prompt", "plan it", "--status-file", "/tmp/status.json",
	}
	signal := signalForPane(repo, pane)
	signal.State = backend.AgentPlan
	observer := exactObserver(pane)
	observer.observation.ProcessInfo.ForegroundProcesses = append(
		observer.observation.ProcessInfo.ForegroundProcesses,
		backend.PaneProcess{
			PID: 43, ParentPID: 42, ProcessGroup: 42, Executable: "/opt/codex",
			Argv0: "/opt/codex", Argv: []string{
				"-c", "check_for_update_on_startup=false", "--remote", "ws://127.0.0.1:1234",
			},
			CWD: pane.WorktreePath,
		},
	)
	observer.observation.Panes[0].AgentProvider = "codex"
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "plan" || !got.StateRefinement {
		t.Fatalf("Codex Plan telemetry = (%q, %t)", got.ReportedState, got.StateRefinement)
	}
}

func TestEmitUpdatesGenericWorkspaceFinalRowTelemetry(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := genericFinalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "working" || !got.StateRefinement {
		t.Fatalf("generic workspace telemetry = (%q, %t)", got.ReportedState, got.StateRefinement)
	}
}

func TestEmitInvalidatesGenericWorkspaceOnCurrentPathChange(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := genericFinalEmitterFixture(t, repo)
	observer.observation.Panes[0].CurrentPath = filepath.Join(repo, "other")
	saveEmitterPanes(t, repo, pane)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "" || got.StateRefinement || got.EmitterNonce == pane.EmitterNonce {
		t.Fatalf("changed generic workspace retained telemetry binding: %+v", got)
	}
}

func TestEmitFinalRowPersistsDoneAgainstLateSignal(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, signal, observer := finalEmitterFixture(t, repo)
	saveEmitterPanes(t, repo, pane)

	for index, reported := range []backend.AgentState{backend.AgentDone, backend.AgentIdle, backend.AgentWorking} {
		signal.State = reported
		signal.Sequence = uint64(index + 1)
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
		name      string
		mutate    func(*state.Pane, *telemetry.Signal, *fakeObserver)
		count     int
		wantStale bool
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
		}, wantStale: true},
		{name: "current workspace label mismatch", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.Panes[0].WorkspaceLabel = "foreign-label"
		}, wantStale: true},
		{name: "process mismatch", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.ProcessInfo.ForegroundProcesses[0].Argv = []string{"wrong"}
		}, wantStale: true},
		{name: "process executable mismatch", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.ProcessInfo.ForegroundProcesses[0].Executable = "/foreign/not-claude"
		}, wantStale: true},
		{name: "process observation failure", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.ProcessError = errors.New("process replaced")
		}},
		{name: "foreign late session", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.Panes[0].AgentSession = &backend.AgentSessionRef{
				Source: "foreign:claude", Agent: "claude", Kind: "id", Value: "session-a",
			}
		}, wantStale: true},
		{name: "invalid late session", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			observer.observation.Panes[0].AgentSession = &backend.AgentSessionRef{
				Source: "herdr:claude", Agent: "claude", Kind: "id",
			}
		}, wantStale: true},
		{name: "ambiguous late session observations", count: 1, mutate: func(_ *state.Pane, _ *telemetry.Signal, observer *fakeObserver) {
			ref := backend.AgentSessionRef{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-a"}
			observer.observation.Panes[0].AgentSession = &ref
			observer.observation.Panes = append(observer.observation.Panes, observer.observation.Panes[0])
		}, wantStale: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newEmitterRepo(t)
			pane, signal, observer := finalEmitterFixture(t, repo)
			pane.ReportedState = string(backend.AgentBlocked)
			pane.StateRefinement = true
			if test.mutate != nil {
				test.mutate(&pane, &signal, observer)
			}
			panes := make([]state.Pane, test.count)
			for i := range panes {
				panes[i] = pane
			}
			saveEmitterPanes(t, repo, panes...)
			err := Emit(context.Background(), signal, observer)
			if test.wantStale {
				if err != nil {
					t.Fatal(err)
				}
				got := loadEmitterPane(t, repo)
				if got.ReportedState != "" || got.StateRefinement || got.EmitterNonce == pane.EmitterNonce {
					t.Fatalf("stale telemetry row = %+v", got)
				}
				return
			}
			if err == nil {
				t.Fatal("Emit() succeeded")
			}
			if test.count > 0 {
				got := loadEmitterPane(t, repo)
				if got.ReportedState != string(backend.AgentBlocked) || !got.StateRefinement ||
					got.EmitterNonce != pane.EmitterNonce {
					t.Fatalf("failed signal changed telemetry row = %+v", got)
				}
			}
		})
	}
}

// A provider that replaces its conversation mid-pane keeps reporting state:
// the row follows the replacement instead of being invalidated into a rotated
// emitter nonce the live agent can never match again.
func TestEmitFinalRowRebindsReplacedSession(t *testing.T) {
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
	if got.AgentSession == nil || *got.AgentSession != first || got.ReportedState != "working" {
		t.Fatalf("late-bound telemetry row = %+v", got)
	}

	second := first
	second.Value = "session-second"
	observer.observation.Panes[0].AgentSession = &second
	signal.State = backend.AgentIdle
	signal.Sequence++
	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got = loadEmitterPane(t, repo)
	if got.AgentSession == nil || *got.AgentSession != second || got.ReportedState != "idle" ||
		!got.StateRefinement || got.EmitterNonce != pane.EmitterNonce {
		t.Fatalf("replacement session did not rebind telemetry row = %+v", got)
	}

	// A conversation from another provider is not a replacement: it stales the
	// row exactly as an identity change always has.
	foreign := backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-foreign",
	}
	observer.observation.Panes[0].AgentSession = &foreign
	signal.Sequence++
	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got = loadEmitterPane(t, repo)
	if got.AgentSession == nil || *got.AgentSession != second || got.ReportedState != "" ||
		got.StateRefinement || got.EmitterNonce == pane.EmitterNonce {
		t.Fatalf("foreign-provider session did not stale telemetry row = %+v", got)
	}
}

// The runtime drops the agent name when a provider restarts its conversation
// in place. Telemetry must keep reporting: invalidating the row here rotates
// the emitter nonce, which the live agent can never match again.
func TestEmitFinalRowSurvivesDroppedAgentName(t *testing.T) {
	repo := newEmitterRepo(t)
	pane, _, _ := finalEmitterFixture(t, repo)
	// Only a name fanout minted may stand in for an anonymous record, so the
	// row has to carry one for this drift to be recoverable at all.
	pane.AgentID = naming.ManagedAgentName(repo, "row", strings.Repeat("a", 32))
	signal := signalForPane(repo, pane)
	observer := exactObserver(pane)
	saveEmitterPanes(t, repo, pane)

	observer.observation.Panes[0].AgentID = "claude"
	observer.observation.Panes[0].AgentNamed = false
	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got := loadEmitterPane(t, repo)
	if got.ReportedState != "working" || !got.StateRefinement ||
		got.EmitterNonce != pane.EmitterNonce {
		t.Fatalf("dropped agent name invalidated telemetry row = %+v", got)
	}

	// A record that answers to some other name is a different agent, and still
	// stales the row.
	observer.observation.Panes[0].AgentID = "someone-else"
	observer.observation.Panes[0].AgentNamed = true
	signal.Sequence++
	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	got = loadEmitterPane(t, repo)
	if got.EmitterNonce == pane.EmitterNonce {
		t.Fatalf("renamed agent record did not stale telemetry row = %+v", got)
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
	if got.AgentSession != nil || got.ReportedState != "running" {
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
	journal, err := locked.LaunchJournal(repo)
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

	for index, reported := range []backend.AgentState{backend.AgentIdle, backend.AgentDone, backend.AgentWorking} {
		signal.State = reported
		signal.Sequence = uint64(index + 1)
		err = Emit(context.Background(), signal, observer)
		if err != nil {
			t.Fatal(err)
		}
	}
	stored, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := stored.FindIntent(intent.ID)
	wantSession := observer.observation.Panes[0].AgentSession
	if !found || got.Launch.PendingReportedState != "done" || got.Launch.PendingReportedSeq != 3 ||
		got.Launch.PendingAgentSession == nil ||
		wantSession == nil || *got.Launch.PendingAgentSession != *wantSession {
		t.Fatalf("pending intent = (%+v, %t), want done", got, found)
	}
}

func TestEmitStalePendingSequenceStillRebindsReplacedSession(t *testing.T) {
	repo := newEmitterRepo(t)
	intent, signal, observer := pendingEmitterFixture(t, repo)
	first := *observer.observation.Panes[0].AgentSession
	second := first
	second.Value = "pending-session-replaced"
	intent.Launch.PendingReportedState = string(backend.AgentBlocked)
	intent.Launch.PendingReportedSeq = 2
	intent.Launch.PendingAgentSession = &first
	observer.observation.Panes[0].AgentSession = &second
	saveEmitterIntent(t, repo, intent)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	stored, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := stored.FindIntent(intent.ID)
	if !found || got.Launch.PendingAgentSession == nil || *got.Launch.PendingAgentSession != second ||
		got.Launch.PendingReportedState != string(backend.AgentBlocked) || got.Launch.PendingReportedSeq != 2 {
		t.Fatalf("stale provisional sequence did not preserve telemetry and rebind session: (%+v, %t)", got, found)
	}
}

func TestEmitUpdatesGenericWorkspacePendingIntent(t *testing.T) {
	repo := newEmitterRepo(t)
	intent, signal, observer := genericPendingEmitterFixture(t, repo)
	saveEmitterIntent(t, repo, intent)

	if err := Emit(context.Background(), signal, observer); err != nil {
		t.Fatal(err)
	}
	stored, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := stored.FindIntent(intent.ID)
	if !found || got.Launch.PendingReportedState != "working" || got.Launch.PendingAgentSession == nil {
		t.Fatalf("generic provisional telemetry = (%+v, %t)", got, found)
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
	stored, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := stored.FindIntent(intent.ID)
	if !found || got.Launch.PendingReportedState != "" {
		t.Fatalf("mismatched provisional signal changed intent: (%+v, %t)", got, found)
	}
}

func TestEmitPendingIntentRejectsWorkspaceLabelReplacement(t *testing.T) {
	repo := newEmitterRepo(t)
	intent, signal, observer := pendingEmitterFixture(t, repo)
	intent.Launch.PendingReportedState = string(backend.AgentBlocked)
	intent.Launch.PendingReportedSeq = 2
	intent.Launch.PendingAgentSession = observer.observation.Panes[0].AgentSession
	saveEmitterIntent(t, repo, intent)
	observer.observation.Panes[0].WorkspaceLabel = "foreign-label"

	if err := Emit(context.Background(), signal, observer); err == nil {
		t.Fatal("Emit() succeeded with a replaced provisional workspace label")
	}
	stored, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := stored.FindIntent(intent.ID)
	if !found || got.Launch.PendingReportedState != string(backend.AgentBlocked) ||
		got.Launch.PendingReportedSeq != 2 {
		t.Fatalf("replaced provisional workspace changed intent: (%+v, %t)", got, found)
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
	stored, err := state.LoadLaunchJournal(repo)
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
	repoKey, err := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".fanout", "worktrees", "telemetry")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	pane := state.Pane{
		Parent: "524", IssueNum: 529, Slug: "telemetry", BranchName: "fanout/telemetry",
		Backend: backend.Herdr, PaneID: "workspace-1:pane-1", Agent: "claude",
		WorktreePath: worktree, AgentStatus: "running", ReportedState: "running",
		WorkspaceID: "workspace-1", WorkspaceLabel: "owned-label-1",
		TerminalID: "terminal-1",
		RepoKey:    repoKey, AgentID: "fanout-agent",
		SessionID: "fanout-owned", SocketPath: "/tmp/fanout-owned/herdr.sock",
		EmitterRowKey: "issue:3:524:529", LaunchNonce: strings.Repeat("a", 32),
		EmitterNonce: strings.Repeat("b", 32), LaunchExecutable: "/opt/bin/claude",
		LaunchArgs: []string{
			"--settings", `{"command":"` + telemetry.SequenceCommand + `"}`, "prompt",
		},
	}
	signal := signalForPane(repo, pane)
	return pane, signal, exactObserver(pane)
}

func genericFinalEmitterFixture(t *testing.T, repo string) (state.Pane, telemetry.Signal, *fakeObserver) {
	t.Helper()
	pane, _, _ := finalEmitterFixture(t, repo)
	pane.Parent, pane.RuntimeParent, pane.IssueNum = "@manual", "524", -1
	pane.Kind = state.PaneKindAttachedAgent
	pane.Slug, pane.BranchName, pane.WorktreePath, pane.RepoKey = "orchestrator-524", "", repo, ""
	pane.EmitterRowKey, _ = state.CoordinatorIntentID("@manual", repo, pane.IssueNum)
	observer := exactObserver(pane)
	observer.observation.Panes[0].WorktreePath = ""
	return pane, signalForPane(repo, pane), observer
}

func pendingEmitterFixture(t *testing.T, repo string) (state.LaunchIntent, telemetry.Signal, *fakeObserver) {
	t.Helper()
	pane, signal, observer := finalEmitterFixture(t, repo)
	session := backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "pending-session",
	}
	observer.observation.Panes[0].AgentSession = &session
	intent := state.LaunchIntent{
		ID: signal.RowKey, Kind: state.IntentWorktree, Status: state.IntentRealized,
		Parent: "524", RuntimeParent: "524", IssueNum: 529,
		Slug: pane.Slug, BranchName: pane.BranchName, FullBranchRef: "refs/heads/" + pane.BranchName,
		BaseBranch: "main", BaseSHA: strings.Repeat("1", 40), ExpectedHead: strings.Repeat("1", 40),
		WorktreePath: pane.WorktreePath, WorkspaceLabel: "fanout-worktree-telemetry",
		Resource: state.RuntimeResource{
			WorkspaceID: pane.WorkspaceID, Label: "fanout-worktree-telemetry",
			PaneID: pane.PaneID, TerminalID: pane.TerminalID, CurrentPath: pane.WorktreePath,
			RepoKey: pane.RepoKey, RepoRoot: pane.WorktreePath,
		},
		Coordinator: state.RuntimeResource{
			WorkspaceID: "coordinator", Label: "fanout-coordinator",
			PaneID: "coordinator:pane", TerminalID: "coordinator-terminal", CurrentPath: repo,
		},
		Session: pane.SessionID, SocketPath: pane.SocketPath,
		ExpiresUnixMS: time.Now().Add(time.Hour).UnixMilli(),
		Launch: &state.LaunchCapsule{
			Nonce: pane.LaunchNonce, EmitterNonce: pane.EmitterNonce,
			Agent: pane.Agent, AgentName: pane.AgentID,
			Executable: pane.LaunchExecutable, Args: pane.LaunchArgs,
			EnvFilePath: "/tmp/fanout-env.json", EnvNameCount: 1,
			LauncherReady: true, TokenIssued: true,
		},
	}
	signal.WorkspaceLabel = intent.Resource.Label
	observer.observation.Panes[0].WorkspaceLabel = intent.Resource.Label
	return intent, signal, observer
}

func genericPendingEmitterFixture(t *testing.T, repo string) (state.LaunchIntent, telemetry.Signal, *fakeObserver) {
	t.Helper()
	intent, signal, observer := pendingEmitterFixture(t, repo)
	intent.ID, _ = state.CoordinatorIntentID("@manual", repo, -1)
	intent.Kind, intent.Parent, intent.RuntimeParent, intent.IssueNum = state.IntentCoordinator, "@manual", "524", -1
	intent.OwnerProjectRoot = repo
	intent.Slug, intent.BranchName, intent.FullBranchRef = "", "", ""
	intent.BaseBranch, intent.BaseSHA, intent.ExpectedHead = "", "", ""
	intent.WorktreePath, intent.WorkspaceLabel = repo, intent.Resource.Label
	intent.Resource.CurrentPath, intent.Resource.RepoKey, intent.Resource.RepoRoot = repo, "", ""
	intent.Coordinator = state.RuntimeResource{}
	signal.RowKey = intent.ID
	signal.WorktreePath = intent.WorktreePath
	live := &observer.observation.Panes[0]
	live.CurrentPath, live.WorktreePath, live.RepoKey, live.ProjectRoot = repo, "", "", ""
	observer.observation.ProcessInfo.ForegroundProcesses[0].CWD = repo
	return intent, signal, observer
}

func signalForPane(repo string, pane state.Pane) telemetry.Signal {
	signal := telemetry.Signal{
		StatePath: state.Path(repo), RowKey: pane.EmitterRowKey,
		LaunchNonce: pane.LaunchNonce, EmitterNonce: pane.EmitterNonce,
		Backend: backend.Herdr, Session: pane.SessionID, SocketPath: pane.SocketPath,
		WorkspaceID: pane.WorkspaceID, WorkspaceLabel: pane.WorkspaceLabel,
		WorktreePath: pane.WorktreePath, PaneID: pane.PaneID,
		TerminalID: pane.TerminalID, Agent: pane.Agent, AgentID: pane.AgentID,
		State: backend.AgentWorking,
	}
	if pane.Agent == "claude" {
		signal.Sequence = 1
	}
	return signal
}

func exactObserver(pane state.Pane) *fakeObserver {
	process := backend.PaneProcess{
		PID: 42, ParentPID: 1, ProcessGroup: 42, Executable: pane.LaunchExecutable,
		Argv0: pane.LaunchExecutable, Argv: pane.LaunchArgs, CWD: pane.WorktreePath,
	}
	live := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Herdr, Workspace: pane.WorkspaceID, Pane: pane.PaneID},
		CurrentPath: pane.WorktreePath, WorktreePath: pane.WorktreePath,
		WorkspaceLabel: pane.WorkspaceLabel,
		TerminalID:     pane.TerminalID, RepoKey: pane.RepoKey,
		ProjectRoot:  filepath.Dir(pane.RepoKey),
		AgentPresent: true, AgentProvider: pane.Agent, AgentID: pane.AgentID,
		SessionID: pane.SessionID, SocketPath: pane.SocketPath,
	}
	return &fakeObserver{observation: Observation{
		Panes: []backend.LivePane{live},
		ProcessInfo: backend.PaneProcessInfo{
			PaneID: pane.PaneID, ShellPID: 42, ForegroundProcessGroup: 42,
			ForegroundProcesses: []backend.PaneProcess{process},
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

func saveEmitterIntent(t *testing.T, repo string, intent state.LaunchIntent) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(repo)
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

func loadEmitterPaneByRowKey(t *testing.T, repo, rowKey string) state.Pane {
	t.Helper()
	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range store.Panes {
		if pane.EmitterRowKey == rowKey {
			return pane
		}
	}
	t.Fatalf("state has no pane for row key %q", rowKey)
	return state.Pane{}
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
