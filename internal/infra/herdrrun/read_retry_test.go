package herdrrun

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSnapshotSlowCommandReportsTimeout(t *testing.T) {
	fake := newFakeHerdr("fanout-test", "/private/tmp/herdr.sock")
	b := newTestBackend(t, "fanout-test", "/private/tmp/herdr.sock", fake)
	fake.intercept = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	_, err := b.snapshot(t.Context(), 10*time.Millisecond, probeResult{})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out after 10ms") ||
		strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("snapshot error = %v, want timeout with its cause", err)
	}
}

func TestOwnedLifecyclePreflightRetriesReadTimeouts(t *testing.T) {
	for _, timeoutErr := range []error{context.DeadlineExceeded, exec.ErrWaitDelay} {
		t.Run(timeoutErr.Error(), func(t *testing.T) {
			h := newOwnedHarness(t)
			observed := New(h.session.Session, h.session.SocketPath)
			observed.output = h.fake.output
			clock := installFakeWaitClock(observed)
			calls := map[string]int{}
			h.fake.intercept = func(_ context.Context, key string) error {
				calls[key]++
				if (key == "status" || key == "snapshot") && calls[key] == 1 {
					return timeoutErr
				}
				return nil
			}
			opened, err := openOwned(t.Context(), OwnedOptions{
				GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase,
			}, observed)
			if err != nil {
				t.Fatal(err)
			}
			workspaces, err := opened.ObserveWorkspaces(t.Context())
			if err != nil || len(workspaces) != 2 {
				t.Fatalf("preflight observations = %v, error = %v", workspaces, err)
			}
			if calls["status"] != 3 || calls["snapshot"] != 2 ||
				!slices.Equal(clock.sleeps, []time.Duration{readRetryDelay, readRetryDelay}) {
				t.Fatalf("calls = %v, sleeps = %v", calls, clock.sleeps)
			}
		})
	}
}

func TestOwnedSnapshotTimeoutRetriesOnce(t *testing.T) {
	for _, view := range []bool{false, true} {
		t.Run(map[bool]string{false: "workspaces", true: "target view"}[view], func(t *testing.T) {
			h := newOwnedHarness(t)
			b := h.session.backend
			clock := installFakeWaitClock(b)
			h.fake.commands = nil
			h.fake.errors["snapshot"] = exec.ErrWaitDelay
			var err error
			if view {
				_, err = b.ownedSnapshotView(t.Context(), *b.owner)
			} else {
				_, err = h.session.ObserveWorkspaces(t.Context())
			}
			if !errors.Is(err, exec.ErrWaitDelay) || !strings.Contains(err.Error(), "timed out after 5s") ||
				strings.Contains(err.Error(), "unavailable") {
				t.Fatalf("snapshot error = %v, want timeout with its cause", err)
			}
			if len(h.fake.commands) != 4 || !slices.Equal(clock.sleeps, []time.Duration{readRetryDelay}) {
				t.Fatalf("commands = %v, sleeps = %v", h.fake.commands, clock.sleeps)
			}
		})
	}
}

func TestReadRetryStopsOnPermanentErrorOrCancellation(t *testing.T) {
	for _, cause := range []error{
		context.Canceled,
		errors.New("unknown method"),
		&exec.ExitError{},
		errors.Join(context.DeadlineExceeded, commandCleanupError{err: syscall.EPERM}),
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			fake := newFakeHerdr("fanout-test", "/private/tmp/herdr.sock")
			fake.errors["status"] = cause
			b := newTestBackend(t, "fanout-test", "/private/tmp/herdr.sock", fake)
			clock := installFakeWaitClock(b)
			_, err := b.runReadContext(t.Context(), "fake", route{}, "status", "--json")
			if !errors.Is(err, cause) || len(fake.commands) != 1 || len(clock.sleeps) != 0 {
				t.Fatalf("error = %v, commands = %d, sleeps = %v", err, len(fake.commands), clock.sleeps)
			}
		})
	}
}

func TestReadRetryCancellationDuringDelay(t *testing.T) {
	fake := newFakeHerdr("fanout-test", "/private/tmp/herdr.sock")
	fake.errors["status"] = context.DeadlineExceeded
	b := newTestBackend(t, "fanout-test", "/private/tmp/herdr.sock", fake)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.sleep = func(ctx context.Context, delay time.Duration) error {
		cancel()
		return sleepContext(ctx, delay)
	}
	_, err := b.runReadContext(ctx, "fake", route{}, "status", "--json")
	if !errors.Is(err, context.Canceled) || len(fake.commands) != 1 {
		t.Fatalf("error = %v, commands = %d", err, len(fake.commands))
	}
}

func TestReadRetryDoesNotExtendParentDeadline(t *testing.T) {
	fake := newFakeHerdr("fanout-test", "/private/tmp/herdr.sock")
	b := newTestBackend(t, "fanout-test", "/private/tmp/herdr.sock", fake)
	clock := installFakeWaitClock(b)
	fake.intercept = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := b.runReadContext(ctx, "fake", route{}, "status", "--json")
	if !errors.Is(err, context.DeadlineExceeded) || len(fake.commands) != 1 || len(clock.sleeps) != 0 {
		t.Fatalf("error = %v, commands = %d, sleeps = %v", err, len(fake.commands), clock.sleeps)
	}
}

func TestMutationTimeoutIsNotRetried(t *testing.T) {
	h := newOwnedHarness(t)
	clock := installFakeWaitClock(h.session.backend)
	mutations := 0
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"workspace", "close", "w2"}) {
			t.Fatalf("unexpected command %v", args)
		}
		mutations++
		return nil, context.DeadlineExceeded
	}
	err := h.session.CloseWorkspace(t.Context(), "w2")
	if !errors.Is(err, context.DeadlineExceeded) || mutations != 1 || len(clock.sleeps) != 0 {
		t.Fatalf("error = %v, mutations = %d, sleeps = %v", err, mutations, clock.sleeps)
	}
}
