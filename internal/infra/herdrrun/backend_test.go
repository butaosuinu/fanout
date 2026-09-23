package herdrrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

func TestName(t *testing.T) {
	b := New("fanout-test", "/tmp/herdr.sock")
	if got := b.Name(); got != corebackend.Herdr {
		t.Fatalf("Name() = %q, want %q", got, corebackend.Herdr)
	}
}

// Every herdr mutation crosses the server, so the launch orchestration must
// take the journaled lane. The preview backend declares the same model: a dry
// run describes the mutations the journaled lane would issue.
func TestMutationModelIsJournaled(t *testing.T) {
	tests := []struct {
		name    string
		backend *Backend
	}{
		{name: "owned session backend", backend: New("fanout-test", "/tmp/herdr.sock")},
		{name: "mutation-free preview backend", backend: NewPreview()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.backend.MutationModel(); got != corebackend.MutationJournaled {
				t.Fatalf("MutationModel() = %d, want MutationJournaled", got)
			}
		})
	}
}

// Pane decoration, liveness stamps, and the window grid are tmux-only pane
// concerns the herdr launch lane never reaches — herdr arranges its own
// workspace. The capabilities must stay unimplemented so the launcher skips or
// fails closed by contract instead of at call time.
func TestBackendOffersNoTmuxOnlyPaneCapability(t *testing.T) {
	b := New("fanout-test", "/tmp/herdr.sock")
	if _, ok := corebackend.AsPaneDecorator(b); ok {
		t.Fatal("AsPaneDecorator(herdr backend) reported a capability, want absent")
	}
	if _, ok := corebackend.AsLivenessStamper(b); ok {
		t.Fatal("AsLivenessStamper(herdr backend) reported a capability, want absent")
	}
	if _, ok := corebackend.AsLayoutManager(b); ok {
		t.Fatal("AsLayoutManager(herdr backend) reported a capability, want absent")
	}
}

// Console restore rebinds, recreates, and rewrites durable state rows against
// evidence only a pane multiplexer can produce: a strict identity sweep and the
// two clocks that prove a recorded pane id was never reused. Herdr persists and
// rearranges its own sessions, so it offers none of that — which is what makes
// restore structurally tmux-only instead of gated on a backend name check.
func TestBackendOffersNoRestoreCapability(t *testing.T) {
	b := New("fanout-test", "/tmp/herdr.sock")
	if _, ok := corebackend.AsRestoreOps(b); ok {
		t.Fatal("AsRestoreOps(herdr backend) reported a capability, want absent")
	}
	if _, ok := corebackend.AsPaneLocator(b); ok {
		t.Fatal("AsPaneLocator(herdr backend) reported a capability, want absent")
	}
}

// Popups, the broad host shortcut set, and viewer-scoped focus are the host
// surfaces fanout's own console asks a runtime for. Herdr offers none; its
// dashboard-only binding remains a separate capability.
func TestBackendOffersNoConsoleHostCapability(t *testing.T) {
	b := New("fanout-test", "/tmp/herdr.sock")
	if _, ok := corebackend.AsPopupHost(b); ok {
		t.Fatal("AsPopupHost(herdr backend) reported a capability, want absent")
	}
	if _, ok := corebackend.AsShortcutBinder(b); ok {
		t.Fatal("AsShortcutBinder(herdr backend) reported a capability, want absent")
	}
	if _, ok := corebackend.AsDashboardShortcutBinder(b); !ok {
		t.Fatal("AsDashboardShortcutBinder(herdr backend) is absent")
	}
	if _, ok := corebackend.AsConsoleFocus(b); ok {
		t.Fatal("AsConsoleFocus(herdr backend) reported a capability, want absent")
	}
}

// The console's session entry — create a session, put the console pane in it,
// attach the operator's terminal — has no herdr counterpart: the repository
// owns a session that outlives the run, and fanout hands out an attach command
// for it instead. The absent capability is what routes the console to that
// managed path instead of a backend name check.
func TestBackendOffersNoConsoleSessionCapability(t *testing.T) {
	b := New("fanout-test", "/tmp/herdr.sock")
	if _, ok := corebackend.AsConsoleHost(b); ok {
		t.Fatal("AsConsoleHost(herdr backend) reported a capability, want absent")
	}
}

// A pane in an owned session reports its state through the launch route's
// emitter and is read back through the owned session, both of which need a
// route a self-exec controller does not carry. Offering neither capability
// keeps a controller from reporting into a pane it cannot address.
func TestBackendOffersNoPaneSelfCapability(t *testing.T) {
	b := New("fanout-test", "/tmp/herdr.sock")
	if _, ok := corebackend.AsAgentStateReporter(b); ok {
		t.Fatal("AsAgentStateReporter(herdr backend) reported a capability, want absent")
	}
	if _, ok := corebackend.AsPlanCapture(b); ok {
		t.Fatal("AsPlanCapture(herdr backend) reported a capability, want absent")
	}
}

// The dry-run preview is pinned byte-for-byte by
// tests/golden/scenario-herdr-dry-run.dry-run.txt, so these expectations are
// full lines, not substrings.
func TestDryRunPreviewRendersHerdrCommands(t *testing.T) {
	previewer, ok := corebackend.AsDryRunPreviewer(NewPreview())
	if !ok {
		t.Fatal("AsDryRunPreviewer(herdr backend) reported no capability")
	}

	got := previewer.PreviewLaunch(corebackend.LaunchPreview{
		ProjectRoot:  "/repo/project root",
		WorktreePath: "/repo/project root/.fanout/worktrees/first-child-101",
		BranchName:   "fanout/first-child-101",
		Command:      "claude --permission-mode auto '[fanout #101 of #100] first-child-101: begin.'",
		// Pane title and label are tmux-only decoration; herdr must not echo them.
		PaneTitle: "first-child-101",
		PaneLabel: "#100 · first-child-101",
	})

	want := []string{
		`$ herdr workspace create --cwd '/repo/project root' --label <coordinator_nonce> --no-focus`,
		`$ herdr worktree create --workspace <coordinator_id> --branch fanout/first-child-101 --path '/repo/project root/.fanout/worktrees/first-child-101' --label <worktree_nonce> --no-focus`,
		`# wait for the operation-bound fanout launcher marker, issue one token, and verify the exact agent session`,
		`# agent argv: claude --permission-mode auto '[fanout #101 of #100] first-child-101: begin.'`,
		`# would write coordinator and child Herdr identities to .fanout/state.json`,
		`# would report display-only sidebar tokens with --source fanout to the child workspace and pane`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("PreviewLaunch() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWaitStatusValues(t *testing.T) {
	got := []corebackend.WaitStatus{corebackend.WaitMatched, corebackend.WaitTimedOut, corebackend.WaitCancelled, corebackend.WaitFailed}
	want := []corebackend.WaitStatus{"matched", "timed_out", "cancelled", "failed"} //nolint:misspell // Herdr contract spells the terminal result "cancelled".
	if !slices.Equal(got, want) {
		t.Fatalf("wait statuses = %q, want %q", got, want)
	}
}

func TestWaitRejectsInvalidInputsWithoutInvokingHerdr(t *testing.T) {
	validMatch := func([]corebackend.LivePane) bool { return true }
	tests := []struct {
		name    string
		ctx     context.Context
		timeout time.Duration
		match   func([]corebackend.LivePane) bool
		wantErr string
	}{
		{
			name:    "nil context",
			ctx:     nil,
			timeout: minimumWaitTimeout,
			match:   validMatch,
			wantErr: "requires a context",
		},
		{
			name:    "nil predicate",
			ctx:     context.Background(),
			timeout: minimumWaitTimeout,
			match:   nil,
			wantErr: "requires a snapshot predicate",
		},
		{
			name:    "negative timeout",
			ctx:     context.Background(),
			timeout: -time.Second,
			match:   validMatch,
			wantErr: "whole number of seconds at least 3",
		},
		{
			name:    "timeout below minimum",
			ctx:     context.Background(),
			timeout: 2 * time.Second,
			match:   validMatch,
			wantErr: "whole number of seconds at least 3",
		},
		{
			name:    "fractional timeout",
			ctx:     context.Background(),
			timeout: minimumWaitTimeout + time.Nanosecond,
			match:   validMatch,
			wantErr: "whole number of seconds at least 3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr("fanout-test", "/private/tmp/fanout-test/herdr.sock")
			b := newTestBackend(t, "fanout-test", "/private/tmp/fanout-test/herdr.sock", fake)

			got := b.Wait(tt.ctx, tt.timeout, tt.match)

			if got.Status != corebackend.WaitFailed || got.Err == nil || !strings.Contains(got.Err.Error(), tt.wantErr) || got.Panes != nil {
				t.Fatalf("Wait() = %#v, want failed with nil panes and error containing %q", got, tt.wantErr)
			}
			if len(fake.commands) != 0 {
				t.Fatalf("invalid Wait() invoked herdr: %#v", fake.commands)
			}
		})
	}
}

func TestWaitImmediateMatchUsesVerifiedSocket(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 5*time.Second, func(panes []corebackend.LivePane) bool {
		matchCalls++
		matched := len(panes) == 2 && panes[0].FocusKnown && panes[0].Focused && panes[1].FocusKnown && !panes[1].Focused
		panes[0] = corebackend.LivePane{}
		panes[1].Title = "mutated by predicate"
		if panes[1].AgentSession == nil {
			t.Fatal("predicate snapshot child AgentSession = nil")
		}
		panes[1].AgentSession.Value = "mutated by predicate"
		return matched
	})

	if got.Status != corebackend.WaitMatched || got.Err != nil || len(got.Panes) != 2 {
		t.Fatalf("Wait() = %#v, want matched with two panes and no error", got)
	}
	if got.Panes[0].Ref.Pane != "w1:p1" || got.Panes[1].Title != "child title" ||
		got.Panes[1].AgentSession == nil || got.Panes[1].AgentSession.Value != "session-a" {
		t.Fatalf("matched panes were mutated through predicate slice: %#v", got.Panes)
	}
	if matchCalls != 1 {
		t.Fatalf("predicate calls = %d, want 1", matchCalls)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("immediate match sleeps = %v, want none", clock.sleeps)
	}
	wantCommands := []string{"version", "status", "snapshot"}
	if len(fake.commands) != len(wantCommands) {
		t.Fatalf("command count = %d, want %d", len(fake.commands), len(wantCommands))
	}
	for i, call := range fake.commands {
		if key := commandKey(call.args); key != wantCommands[i] {
			t.Fatalf("command %d = %q (%v), want %q", i, key, call.args, wantCommands[i])
		}
		if slices.Contains(call.args, "--session") {
			t.Fatalf("verified-socket command unexpectedly used --session: %v", call.args)
		}
		if gotSocket, ok := envValue(call.env, socketEnv); !ok || gotSocket != socket {
			t.Fatalf("%v %s = %q (present=%t), want %q", call.args, socketEnv, gotSocket, ok, socket)
		}
	}
}

func TestWaitSnapshotCallLimitsIntervalsAndCommandTimeouts(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name          string
		totalTimeout  time.Duration
		budget        time.Duration
		wantSnapshots int
	}{
		{name: "3 seconds", totalTimeout: 3 * time.Second, budget: 3 * time.Second, wantSnapshots: 2},
		{name: "4 seconds", totalTimeout: 4 * time.Second, budget: 4 * time.Second, wantSnapshots: 2},
		{name: "5 seconds", totalTimeout: 5 * time.Second, budget: 5 * time.Second, wantSnapshots: 3},
		{name: "default 300 seconds", totalTimeout: 0, budget: 300 * time.Second, wantSnapshots: 150},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			b := newTestBackend(t, session, socket, fake)
			clock := installFakeWaitClock(b)
			matchCalls := 0

			got := b.Wait(context.Background(), tt.totalTimeout, func(panes []corebackend.LivePane) bool {
				matchCalls++
				if panes[1].AgentSession == nil {
					t.Fatal("predicate snapshot child AgentSession = nil")
				}
				panes[1].AgentSession.Value = "mutated by predicate"
				return false
			})

			if got.Status != corebackend.WaitTimedOut || got.Err != nil || len(got.Panes) != 2 {
				t.Fatalf("Wait() = %#v, want timed_out with last two panes and no error", got)
			}
			if got.Panes[1].AgentSession == nil || got.Panes[1].AgentSession.Value != "session-a" {
				t.Fatalf("timed-out panes were mutated through predicate session ref: %#v", got.Panes)
			}
			if matchCalls != tt.wantSnapshots {
				t.Fatalf("predicate calls = %d, want %d", matchCalls, tt.wantSnapshots)
			}
			if len(fake.commands) != 2+tt.wantSnapshots {
				t.Fatalf("command count = %d, want %d", len(fake.commands), 2+tt.wantSnapshots)
			}
			for i, wantKey := range []string{"version", "status"} {
				if key := commandKey(fake.commands[i].args); key != wantKey {
					t.Fatalf("probe command %d = %q, want %q", i, key, wantKey)
				}
				assertCommandTimeout(t, fake.commands[i], commandTimeout)
			}
			for i, call := range fake.commands[2:] {
				if key := commandKey(call.args); key != "snapshot" {
					t.Fatalf("poll command %d = %q (%v), want snapshot", i, key, call.args)
				}
				if gotSocket, ok := envValue(call.env, socketEnv); !ok || gotSocket != socket {
					t.Fatalf("snapshot %d %s = %q (present=%t), want %q", i, socketEnv, gotSocket, ok, socket)
				}
				remaining := tt.budget - time.Duration(i)*waitInterval
				assertCommandTimeout(t, call, min(commandTimeout, remaining))
			}
			if len(clock.sleeps) != tt.wantSnapshots-1 {
				t.Fatalf("sleep count = %d, want %d", len(clock.sleeps), tt.wantSnapshots-1)
			}
			for i, delay := range clock.sleeps {
				if delay != waitInterval {
					t.Fatalf("sleep %d = %s, want %s", i, delay, waitInterval)
				}
			}
		})
	}
}

func TestWaitRetryableSnapshotErrorThenValidSnapshotTimesOut(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.snapshotResults = []fakeSnapshotResult{
		{err: context.DeadlineExceeded},
		{output: validSnapshot()},
	}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 3*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != corebackend.WaitTimedOut || got.Err != nil || len(got.Panes) != 2 {
		t.Fatalf("Wait() = %#v, want timed_out with recovered snapshot", got)
	}
	if matchCalls != 1 {
		t.Fatalf("predicate calls = %d, want 1", matchCalls)
	}
	if len(fake.commands) != 4 || !slices.Equal(clock.sleeps, []time.Duration{waitInterval}) {
		t.Fatalf("commands = %d sleeps = %v, want 4 commands and one interval", len(fake.commands), clock.sleeps)
	}
}

func TestWaitValidSnapshotThenFinalRetryableErrorFails(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.snapshotResults = []fakeSnapshotResult{
		{output: validSnapshot()},
		{err: context.DeadlineExceeded},
	}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 3*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != corebackend.WaitFailed || !errors.Is(got.Err, context.DeadlineExceeded) ||
		!strings.Contains(got.Err.Error(), "timed out after 1s") || strings.Contains(got.Err.Error(), "unavailable") || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want timeout error and nil panes", got)
	}
	if matchCalls != 1 {
		t.Fatalf("predicate calls = %d, want 1", matchCalls)
	}
	if len(fake.commands) != 4 || !slices.Equal(clock.sleeps, []time.Duration{waitInterval}) {
		t.Fatalf("commands = %d sleeps = %v, want 4 commands and one interval", len(fake.commands), clock.sleeps)
	}
}

func TestWaitPermanentCommandErrorFailsWithoutRetry(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	permanent := &os.PathError{Op: "fork/exec", Path: "/private/tmp/herdr-0.7.5", Err: syscall.ENOENT}
	fake := newFakeHerdr(session, socket)
	fake.snapshotResults = []fakeSnapshotResult{{err: permanent}}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != corebackend.WaitFailed || got.Err == nil || got.Err.Error() != methodUnavailable("session.snapshot").Error() || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want immediate generic unavailable error", got)
	}
	if matchCalls != 0 || len(fake.commands) != 3 || len(clock.sleeps) != 0 {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/3/none", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitCommandCleanupFailureOverridesRetryableCommandErrors(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	exitErr := exec.Command("/bin/sh", "-c", "exit 7").Run()
	var typedExitErr *exec.ExitError
	if !errors.As(exitErr, &typedExitErr) {
		t.Fatalf("helper error type = %T, want *exec.ExitError", exitErr)
	}
	for _, tt := range []struct {
		name       string
		commandErr error
		wantErr    string
	}{
		{name: "non-zero exit", commandErr: exitErr, wantErr: `method "session.snapshot" is unavailable`},
		{name: "command deadline", commandErr: context.DeadlineExceeded, wantErr: "timed out after 5s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			fake.snapshotResults = []fakeSnapshotResult{{
				err: errors.Join(tt.commandErr, commandCleanupError{err: syscall.EPERM}),
			}}
			b := newTestBackend(t, session, socket, fake)
			clock := installFakeWaitClock(b)
			matchCalls := 0

			got := b.Wait(context.Background(), 5*time.Second, func([]corebackend.LivePane) bool {
				matchCalls++
				return false
			})

			if got.Status != corebackend.WaitFailed || got.Err == nil || !strings.Contains(got.Err.Error(), tt.wantErr) || got.Panes != nil {
				t.Fatalf("Wait() = %#v, want immediate %q error", got, tt.wantErr)
			}
			if matchCalls != 0 || len(fake.commands) != 3 || len(clock.sleeps) != 0 {
				t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/3/none", matchCalls, len(fake.commands), clock.sleeps)
			}
		})
	}
}

func TestWaitMalformedSnapshotFailsImmediately(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.snapshotResults = []fakeSnapshotResult{{output: "{"}}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != corebackend.WaitFailed || got.Err == nil || got.Err.Error() != methodUnavailable("session.snapshot").Error() || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want immediate generic unavailable error with nil panes", got)
	}
	if matchCalls != 0 || len(fake.commands) != 3 || len(clock.sleeps) != 0 {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/3/none", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitDoesNotPreflightSnapshotProtocol(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	incompatible := strings.Replace(validSnapshot(), `"protocol":17`, `"protocol":18`, 1)
	fake.snapshotResults = []fakeSnapshotResult{{output: incompatible}}
	b := newTestBackend(t, session, socket, fake)
	installFakeWaitClock(b)

	got := b.Wait(context.Background(), 5*time.Second, func([]corebackend.LivePane) bool {
		return true
	})

	if got.Status != corebackend.WaitMatched || got.Err != nil || len(got.Panes) != 2 {
		t.Fatalf("Wait() = %#v, want matched without protocol preflight", got)
	}
}

func TestWaitPreCancelledContextDoesNotInvokeHerdr(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(ctx, 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != corebackend.WaitCancelled || !errors.Is(got.Err, context.Canceled) || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want canceled with context.Canceled and nil panes", got)
	}
	if matchCalls != 0 || len(fake.commands) != 0 || len(clock.sleeps) != 0 {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/0/none", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitCancellationDuringSleepStopsBeforeNextSnapshot(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	b.sleep = func(waitCtx context.Context, delay time.Duration) error {
		clock.sleeps = append(clock.sleeps, delay)
		cancel()
		<-waitCtx.Done()
		return waitCtx.Err()
	}
	matchCalls := 0

	got := b.Wait(ctx, 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != corebackend.WaitCancelled || !errors.Is(got.Err, context.Canceled) || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want canceled with context.Canceled and nil panes", got)
	}
	if matchCalls != 1 || len(fake.commands) != 3 || !slices.Equal(clock.sleeps, []time.Duration{waitInterval}) {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 1/3/one interval", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitCancellationAfterSnapshotOrPredicateCannotMatch(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name           string
		cancelSnapshot bool
		wantMatchCalls int
	}{
		{name: "after successful snapshot", cancelSnapshot: true, wantMatchCalls: 0},
		{name: "inside predicate", wantMatchCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := newFakeHerdr(session, socket)
			if tt.cancelSnapshot {
				fake.intercept = func(_ context.Context, key string) error {
					if key == "snapshot" {
						cancel()
					}
					return nil
				}
			}
			b := newTestBackend(t, session, socket, fake)
			installFakeWaitClock(b)
			matchCalls := 0

			got := b.Wait(ctx, 5*time.Second, func([]corebackend.LivePane) bool {
				matchCalls++
				if !tt.cancelSnapshot {
					cancel()
				}
				return true
			})

			if got.Status != corebackend.WaitCancelled || !errors.Is(got.Err, context.Canceled) || got.Panes != nil {
				t.Fatalf("Wait() = %#v, want canceled result instead of matched", got)
			}
			if matchCalls != tt.wantMatchCalls || len(fake.commands) != 3 {
				t.Fatalf("predicate calls = %d commands = %d, want %d/3", matchCalls, len(fake.commands), tt.wantMatchCalls)
			}
		})
	}
}

func TestWaitDeadlineCrossingAfterSnapshotOrPredicateCannotMatch(t *testing.T) {
	const (
		session      = "fanout-test"
		socket       = "/private/tmp/fanout-test/herdr.sock"
		totalTimeout = 5 * time.Second
	)
	tests := []struct {
		name            string
		advanceSnapshot bool
		wantMatchCalls  int
	}{
		{name: "after successful snapshot", advanceSnapshot: true, wantMatchCalls: 0},
		{name: "inside predicate", wantMatchCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			b := newTestBackend(t, session, socket, fake)
			clock := installFakeWaitClock(b)
			if tt.advanceSnapshot {
				fake.intercept = func(_ context.Context, key string) error {
					if key == "snapshot" {
						clock.now = clock.now.Add(totalTimeout)
					}
					return nil
				}
			}
			matchCalls := 0

			got := b.Wait(context.Background(), totalTimeout, func([]corebackend.LivePane) bool {
				matchCalls++
				if !tt.advanceSnapshot {
					clock.now = clock.now.Add(totalTimeout)
				}
				return true
			})

			if got.Status != corebackend.WaitTimedOut || got.Err != nil || len(got.Panes) != 2 {
				t.Fatalf("Wait() = %#v, want timed_out with the last compatible snapshot", got)
			}
			if matchCalls != tt.wantMatchCalls || len(fake.commands) != 3 {
				t.Fatalf("predicate calls = %d commands = %d, want %d/3", matchCalls, len(fake.commands), tt.wantMatchCalls)
			}
		})
	}
}

func TestWaitCancellationStopsProbeOrSnapshotImmediately(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name         string
		cancelOn     string
		wantCommands []string
	}{
		{name: "during probe", cancelOn: "version", wantCommands: []string{"version"}},
		{name: "during snapshot", cancelOn: "snapshot", wantCommands: []string{"version", "status", "snapshot"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := newFakeHerdr(session, socket)
			fake.intercept = func(callCtx context.Context, key string) error {
				if key != tt.cancelOn {
					return nil
				}
				cancel()
				<-callCtx.Done()
				return callCtx.Err()
			}
			b := newTestBackend(t, session, socket, fake)
			clock := installFakeWaitClock(b)
			matchCalls := 0

			got := b.Wait(ctx, 5*time.Second, func([]corebackend.LivePane) bool {
				matchCalls++
				return false
			})

			if got.Status != corebackend.WaitCancelled || !errors.Is(got.Err, context.Canceled) || got.Panes != nil {
				t.Fatalf("Wait() = %#v, want canceled with context.Canceled and nil panes", got)
			}
			if matchCalls != 0 || len(clock.sleeps) != 0 {
				t.Fatalf("predicate calls = %d sleeps = %v, want 0/none", matchCalls, clock.sleeps)
			}
			if len(fake.commands) != len(tt.wantCommands) {
				t.Fatalf("commands = %d, want %d", len(fake.commands), len(tt.wantCommands))
			}
			for i, call := range fake.commands {
				if key := commandKey(call.args); key != tt.wantCommands[i] {
					t.Fatalf("command %d = %q (%v), want %q", i, key, call.args, tt.wantCommands[i])
				}
			}
		})
	}
}

func TestUnsupportedOperationsNeverInvokeHerdr(t *testing.T) {
	fake := newFakeHerdr("fanout-test", "/private/tmp/fanout-test/herdr.sock")
	b := newTestBackend(t, "fanout-test", "/private/tmp/fanout-test/herdr.sock", fake)
	ref := corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1"}

	var errs []error
	_, err := b.Launch(corebackend.LaunchRequest{})
	errs = append(errs, err)
	errs = append(errs, b.ReleaseStartGate("gate"))
	_, err = b.Read(ref, 100)
	errs = append(errs, err)
	errs = append(errs, b.SendLine(ref, "text"))
	errs = append(errs, b.Focus(ref))
	errs = append(errs, b.Close(ref))
	_, err = b.CloseOwned(corebackend.CloseRequest{Ref: ref})
	errs = append(errs, err)

	for i, err := range errs {
		if !errors.Is(err, corebackend.ErrUnsupported) || !corebackend.IsUnsupported(err) {
			t.Errorf("unsupported error %d = %v", i, err)
		}
	}
	if len(fake.commands) != 0 {
		t.Fatalf("unsupported operations invoked herdr: %#v", fake.commands)
	}
}
