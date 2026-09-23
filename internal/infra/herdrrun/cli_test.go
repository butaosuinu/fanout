package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	runCommandHelperModeEnv    = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_MODE"
	runCommandHelperPIDsEnv    = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_PIDS"
	runCommandHelperReadyEnv   = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_READY"
	runCommandHelperReleaseEnv = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_RELEASE"
	runCommandHelperLockEnv    = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_LOCK"
)

type runCommandHelperPIDs struct {
	direct     int
	descendant int
	group      int
}

func waitForRunCommandHelperPIDs(path string, timeout time.Duration) (runCommandHelperPIDs, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) != 3 {
				lastErr = fmt.Errorf("helper pid file has %d fields, want 3", len(fields))
			} else {
				values := make([]int, len(fields))
				for i, field := range fields {
					values[i], err = strconv.Atoi(field)
					if err != nil {
						lastErr = fmt.Errorf("parse helper pid %q: %w", field, err)
						break
					}
				}
				if err == nil {
					return runCommandHelperPIDs{direct: values[0], descendant: values[1], group: values[2]}, nil
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("helper pid file was not created")
	}
	return runCommandHelperPIDs{}, lastErr
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("file %q was not created", path)
}

func waitForProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitForHelperLockRelease(path string, timeout time.Duration) (returnErr error) {
	lockFile, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, lockFile.Close())
	}()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		flockErr := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			return syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		}
		if !errors.Is(flockErr, syscall.EWOULDBLOCK) && !errors.Is(flockErr, syscall.EAGAIN) {
			return flockErr
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("descendant still holds helper lock %q", path)
}

func killRunCommandHelper(pids runCommandHelperPIDs) error {
	if pids.group > 1 && pids.group != syscall.Getpgrp() {
		err := syscall.Kill(-pids.group, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if pids.descendant <= 1 || pids.descendant == os.Getpid() {
		return fmt.Errorf("refusing to kill unsafe helper pids %#v", pids)
	}
	err := syscall.Kill(pids.descendant, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func TestOwnedRouteEnvironmentDoesNotInheritAmbientSecrets(t *testing.T) {
	t.Setenv("FANOUT_TEST_SECRET", "must-not-leak")
	t.Setenv("PATH", "/ambient/path")
	control := &controlPlaneEnvironment{
		xdgConfigHome: "/owned/config", xdgStateHome: "/owned/state",
		xdgDataHome: "/owned/data", xdgCacheHome: "/owned/cache",
		configPath: "/owned/config/herdr/config.toml", clientSocketPath: "/owned/client.sock",
	}
	env := routeEnvironment(route{session: "fanout-test", socketPath: "/owned/server.sock"}, control)
	for _, key := range []string{"FANOUT_TEST_SECRET", "PATH", "HOME"} {
		if _, ok := envValue(env, key); ok {
			t.Fatalf("owned environment inherited %s: %v", key, env)
		}
	}
	for key, want := range map[string]string{
		sessionEnv: "fanout-test", socketEnv: "/owned/server.sock", clientSocketEnv: "/owned/client.sock",
		xdgConfigEnv: "/owned/config", xdgStateEnv: "/owned/state", xdgDataEnv: "/owned/data", xdgCacheEnv: "/owned/cache",
		configEnv: "/owned/config/herdr/config.toml",
	} {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Fatalf("owned environment %s = %q (present=%t), want %q", key, got, ok, want)
		}
	}
}

func TestKillCommandProcessTreeFallsBackWithoutHidingGroupFailure(t *testing.T) {
	tests := []struct {
		name            string
		groupErr        error
		directErr       error
		wantDirectCalls int
		wantErrors      []error
	}{
		{name: "group killed", wantDirectCalls: 0},
		{name: "group gone and direct done", groupErr: syscall.ESRCH, directErr: os.ErrProcessDone, wantDirectCalls: 1, wantErrors: []error{os.ErrProcessDone}},
		{name: "group gone but direct killed", groupErr: syscall.ESRCH, wantDirectCalls: 1, wantErrors: []error{syscall.ESRCH}},
		{name: "group denied but direct killed", groupErr: syscall.EPERM, wantDirectCalls: 1, wantErrors: []error{syscall.EPERM}},
		{name: "both kills denied", groupErr: syscall.EPERM, directErr: syscall.EACCES, wantDirectCalls: 1, wantErrors: []error{syscall.EPERM, syscall.EACCES}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directCalls := 0
			err := killCommandProcessTree(
				func() error { return tt.groupErr },
				func() error {
					directCalls++
					return tt.directErr
				},
			)

			if directCalls != tt.wantDirectCalls {
				t.Fatalf("direct kill calls = %d, want %d", directCalls, tt.wantDirectCalls)
			}
			if len(tt.wantErrors) == 0 && err != nil {
				t.Fatalf("killCommandProcessTree() error = %v, want nil", err)
			}
			for _, wantErr := range tt.wantErrors {
				if !errors.Is(err, wantErr) {
					t.Errorf("killCommandProcessTree() error = %v, want errors.Is(_, %v)", err, wantErr)
				}
			}
		})
	}
}

func TestFinalizeCommandErrorPreservesCleanupFailureOnDeadline(t *testing.T) {
	err := finalizeCommandError(exec.ErrWaitDelay, context.DeadlineExceeded, syscall.EPERM)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, syscall.EPERM) {
		t.Fatalf("finalizeCommandError() = %v, want deadline and cleanup errors", err)
	}
	var cleanupErr commandCleanupError
	if !errors.As(err, &cleanupErr) {
		t.Fatalf("finalizeCommandError() type = %T, want commandCleanupError", err)
	}
	if retryableCommandError(err) {
		t.Fatal("deadline plus cleanup failure classified as retryable")
	}
}

func TestRunCommandBoundsInheritedPipeWaitAndKillsProcessGroup(t *testing.T) {
	testBoundedCommandRunner(t, runCommand)
}

func TestRunCommandCombinedBoundsInheritedPipeWaitAndKillsProcessGroup(t *testing.T) {
	testBoundedCommandRunner(t, runCommandCombined)
}

func testBoundedCommandRunner(t *testing.T, runner commandOutput) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	controlDir := t.TempDir()
	pidPath := filepath.Join(controlDir, "helper-pids")
	readyPath := filepath.Join(controlDir, "descendant-ready")
	releasePath := filepath.Join(controlDir, "release-direct")
	lockPath := filepath.Join(controlDir, "descendant.lock")
	env := envWithValue(os.Environ(), runCommandHelperModeEnv, "direct")
	env = envWithValue(env, runCommandHelperPIDsEnv, pidPath)
	env = envWithValue(env, runCommandHelperReadyEnv, readyPath)
	env = envWithValue(env, runCommandHelperReleaseEnv, releasePath)
	env = envWithValue(env, runCommandHelperLockEnv, lockPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type commandResult struct {
		err error
	}
	resultCh := make(chan commandResult, 1)
	go func() {
		_, commandErr := runner(ctx, binary, env, "-test.run=^TestRunCommandInheritedPipeHelper$")
		resultCh <- commandResult{err: commandErr}
	}()

	pids, pidErr := waitForRunCommandHelperPIDs(pidPath, 2*time.Second)
	if pidErr != nil {
		cancel()
		select {
		case <-resultCh:
		case <-time.After(2 * time.Second):
		}
		t.Fatalf("wait for helper pids: %v", pidErr)
	}
	if pids.direct <= 0 || pids.descendant <= 0 || pids.group <= 0 {
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean invalid helper process group: %v", cleanupErr)
		}
		t.Fatalf("helper pids = %#v, want positive values", pids)
	}
	if pids.group == syscall.Getpgrp() {
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean unisolated helper process: %v", cleanupErr)
		}
		t.Fatalf("helper process group %d unexpectedly matches test process group", pids.group)
	}
	if pids.group != pids.direct {
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean mismatched helper process group: %v", cleanupErr)
		}
		t.Fatalf("helper process group = %d, want direct child pid %d", pids.group, pids.direct)
	}
	defer func() {
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean helper process group: %v", cleanupErr)
		}
	}()
	select {
	case result := <-resultCh:
		t.Fatalf("runCommand returned before the direct helper was released: %v", result.err)
	default:
	}
	if err := os.WriteFile(releasePath, []byte("release\n"), 0o600); err != nil {
		t.Fatalf("release direct helper: %v", err)
	}
	if !waitForProcessGone(pids.direct, 2*time.Second) {
		t.Fatalf("direct helper process %d was not reaped", pids.direct)
	}

	cancelledAt := time.Now()
	cancel()
	var result commandResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean timed-out helper process group: %v", cleanupErr)
		}
		select {
		case <-resultCh:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("runCommand did not return after its context deadline while a descendant held its output pipes")
	}

	if elapsed := time.Since(cancelledAt); elapsed > 2*time.Second {
		t.Fatalf("runCommand took %s after cancellation, want at most 2s", elapsed)
	}
	if result.err == nil {
		t.Fatal("runCommand error = nil, want a bounded command or pipe-wait error")
	}
	if err := waitForHelperLockRelease(lockPath, 2*time.Second); err != nil {
		t.Fatalf("descendant process was not killed after runCommand returned: %v", err)
	}
}

func TestRunCommandInheritedPipeHelper(t *testing.T) {
	mode := os.Getenv(runCommandHelperModeEnv)
	if mode == "" {
		return
	}
	if mode == "descendant" {
		lockFile, err := os.OpenFile(os.Getenv(runCommandHelperLockEnv), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "open descendant lock: %v\n", err)
			os.Exit(2)
		}
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "lock descendant file: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv(runCommandHelperReadyEnv), []byte("ready\n"), 0o600); err != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "write descendant ready file: %v\n", err)
			os.Exit(2)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if mode != "direct" {
		t.Fatalf("unknown helper mode %q", mode)
	}

	binary, err := os.Executable()
	if err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "os.Executable: %v\n", err)
		os.Exit(2)
	}
	child := exec.Command(binary, "-test.run=^TestRunCommandInheritedPipeHelper$")
	child.Env = envWithValue(os.Environ(), runCommandHelperModeEnv, "descendant")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if startErr := child.Start(); startErr != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "start descendant: %v\n", startErr)
		os.Exit(2)
	}
	group, err := syscall.Getpgid(child.Process.Pid)
	if err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "get descendant process group: %v\n", err)
		if killErr := child.Process.Kill(); killErr != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "kill descendant after group lookup failure: %v\n", killErr)
		}
		os.Exit(2)
	}
	if err := waitForFile(os.Getenv(runCommandHelperReadyEnv), 2*time.Second); err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "wait for descendant readiness: %v\n", err)
		if killErr := syscall.Kill(-group, syscall.SIGKILL); killErr != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "kill unready helper group: %v\n", killErr)
		}
		os.Exit(2)
	}
	pids := fmt.Sprintf("%d %d %d\n", os.Getpid(), child.Process.Pid, group)
	if err := os.WriteFile(os.Getenv(runCommandHelperPIDsEnv), []byte(pids), 0o600); err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "write helper pids: %v\n", err)
		if killErr := syscall.Kill(-group, syscall.SIGKILL); killErr != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "kill helper group after pid write failure: %v\n", killErr)
		}
		os.Exit(2)
	}
	if err := waitForFile(os.Getenv(runCommandHelperReleaseEnv), 5*time.Second); err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "wait for direct release: %v\n", err)
		if killErr := syscall.Kill(-group, syscall.SIGKILL); killErr != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "kill unreleased helper group: %v\n", killErr)
		}
		os.Exit(2)
	}
	os.Exit(0)
}
