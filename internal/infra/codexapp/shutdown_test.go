package codexapp

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

const codexShutdownHelperEnv = "FANOUT_CODEXAPP_SHUTDOWN_HELPER"

func TestCodexShutdownHelper(t *testing.T) {
	mode := os.Getenv(codexShutdownHelperEnv)
	if mode == "" {
		return
	}
	switch mode {
	case "child":
		waitForHelperSignal()
	case "launcher":
		runShutdownLauncherHelper()
	case "finish-hup":
		session := &codexRemoteTUISession{}
		_ = session.finish(newCodexSignalError(syscall.SIGHUP))
		os.Exit(99)
	default:
		os.Exit(98)
	}
}

func TestStopProcessLetsLauncherReapNativeChild(t *testing.T) {
	cmd, done, childPID := startShutdownLauncher(t, false)

	stopProcess(cmd, done, false)

	assertProcessGone(t, cmd.Process.Pid)
	assertProcessGone(t, childPID)
}

func TestRemoteTUISessionCloseIsConcurrentSafe(t *testing.T) {
	tui, tuiDone, tuiChildPID := startShutdownLauncher(t, false)
	serverCmd, serverDone, serverChildPID := startShutdownLauncher(t, true)
	server := &appServer{
		cmd:  serverCmd,
		done: make(chan struct{}),
		logs: &lockedBuffer{},
	}
	go func() {
		err := <-serverDone
		server.mu.Lock()
		server.err = err
		server.mu.Unlock()
		close(server.done)
	}()
	session := &codexRemoteTUISession{
		tui:     tui,
		tuiDone: tuiDone,
		server:  server,
	}

	var callers sync.WaitGroup
	finishDone := make(chan error, 1)
	callers.Go(func() {
		finishDone <- session.finish(newCodexSignalError(syscall.SIGINT))
	})
	for range 7 {
		callers.Go(func() {
			session.Close()
		})
	}
	callers.Wait()
	if code, ok := SignalErrorExitCode(<-finishDone); !ok || code != 130 {
		t.Fatalf("concurrent signal finish = (%d, %t), want (130, true)", code, ok)
	}

	assertProcessGone(t, tui.Process.Pid)
	assertProcessGone(t, tuiChildPID)
	assertProcessGone(t, serverCmd.Process.Pid)
	assertProcessGone(t, serverChildPID)
}

func TestAppServerCloseEscalatesIgnoredTERMToProcessGroupKILL(t *testing.T) {
	cmd, processDone, childPID := startShutdownLauncher(t, true, "FANOUT_CODEXAPP_IGNORE_TERM=1")
	server := &appServer{
		cmd:  cmd,
		done: make(chan struct{}),
		logs: &lockedBuffer{},
	}
	go func() {
		err := <-processDone
		server.mu.Lock()
		server.err = err
		server.mu.Unlock()
		close(server.done)
	}()

	started := time.Now()
	server.Close()
	if elapsed := time.Since(started); elapsed < processShutdownTimeout || elapsed > 3*processShutdownTimeout {
		t.Fatalf("app-server close took %s, want bounded TERM grace then KILL", elapsed)
	}
	assertProcessGone(t, cmd.Process.Pid)
	assertProcessGone(t, childPID)
}

func TestAppServerCloseReapsNativeWhenLauncherDropsTERM(t *testing.T) {
	cmd, processDone, childPID := startShutdownLauncher(t, true, "FANOUT_CODEXAPP_DROP_TERM=1")
	server := &appServer{
		cmd:  cmd,
		done: make(chan struct{}),
		logs: &lockedBuffer{},
	}
	go func() {
		err := <-processDone
		server.mu.Lock()
		server.err = err
		server.mu.Unlock()
		close(server.done)
	}()

	server.Close()
	assertProcessGone(t, cmd.Process.Pid)
	assertProcessGone(t, childPID)
}

func TestFinishHUPKillsPaneProcessGroupAfterCleanup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodexShutdownHelper$")
	cmd.Env = append(os.Environ(), codexShutdownHelperEnv+"=finish-hup")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper error = %v, want signal exit", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("helper status = %v, want SIGKILL", exitErr.Sys())
	}
}

func TestSignalErrorExitCodePreservesSIGINTStatus(t *testing.T) {
	err := newCodexSignalError(syscall.SIGINT)
	code, ok := SignalErrorExitCode(err)
	if !ok || code != 130 {
		t.Fatalf("SignalErrorExitCode(SIGINT) = (%d, %t), want (130, true)", code, ok)
	}
}

func TestFinishCapturesSignalThatArrivesDuringClose(t *testing.T) {
	signals := make(chan os.Signal, 1)
	observerDone := make(chan struct{})
	session := &codexRemoteTUISession{
		signals:      signals,
		observerDone: observerDone,
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		signals <- syscall.SIGINT
		time.Sleep(10 * time.Millisecond)
		close(observerDone)
	}()

	err := session.finish(nil)
	if code, ok := SignalErrorExitCode(err); !ok || code != 130 {
		t.Fatalf("finish() = %v, code = (%d, %t), want SIGINT/130", err, code, ok)
	}
}

func TestConnectAppServerReturnsStartupSignalPromptly(t *testing.T) {
	server := &appServer{
		Addr: "ws://127.0.0.1:1",
		done: make(chan struct{}),
		logs: &lockedBuffer{},
	}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGINT

	started := time.Now()
	connected, err := connectAppServerWithSignals(server, 10*time.Second, signals)
	if connected != nil {
		connected.Close()
		t.Fatal("connection succeeded, want startup interrupt")
	}
	if got := signalFromError(err); got != syscall.SIGINT {
		t.Fatalf("connect error = %v, signal = %v, want SIGINT", err, got)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("signal-aware connect took %s, want prompt return", elapsed)
	}
	close(server.done)
}

func TestTeamBridgeReturnsSignalWithoutInjectionWarning(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	bridge := &teamBridge{
		signals:  signals,
		tuiDone:  make(chan error),
		received: make(chan teamReceivedMessage),
		polls:    make(chan time.Time),
	}

	tuiExited, err := bridge.run()
	if tuiExited {
		t.Fatal("tuiExited = true, want false")
	}
	if got := signalFromError(err); got != syscall.SIGTERM {
		t.Fatalf("bridge.run() error = %v, signal = %v, want SIGTERM", err, got)
	}
}

func startShutdownLauncher(t *testing.T, setProcessGroup bool, extraEnv ...string) (*exec.Cmd, chan error, int) {
	t.Helper()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodexShutdownHelper$")
	cmd.Env = append(os.Environ(),
		codexShutdownHelperEnv+"=launcher",
		"FANOUT_CODEXAPP_CHILD_PID_PATH="+pidPath,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	if setProcessGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shutdown launcher: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	childPID := waitForHelperChildPID(t, pidPath)
	t.Cleanup(func() {
		if setProcessGroup {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		} else {
			_ = cmd.Process.Kill()
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})
	return cmd, done, childPID
}

func waitForHelperChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(raw))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper did not write child PID to %s", path)
	return 0
}

func runShutdownLauncherHelper() {
	child := exec.Command(os.Args[0], "-test.run=^TestCodexShutdownHelper$")
	child.Env = append(os.Environ(), codexShutdownHelperEnv+"=child")
	if err := child.Start(); err != nil {
		os.Exit(97)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	pidPath := os.Getenv("FANOUT_CODEXAPP_CHILD_PID_PATH")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		signal.Stop(signals)
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(96)
	}
	sig := <-signals
	if os.Getenv("FANOUT_CODEXAPP_DROP_TERM") == "1" && sig == syscall.SIGTERM {
		signal.Stop(signals)
		os.Exit(0)
	}
	if os.Getenv("FANOUT_CODEXAPP_IGNORE_TERM") == "1" && sig == syscall.SIGTERM {
		select {}
	}
	signal.Stop(signals)
	_ = child.Process.Signal(sig)
	_ = child.Wait()
	os.Exit(0)
}

func waitForHelperSignal() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	sig := <-signals
	if os.Getenv("FANOUT_CODEXAPP_IGNORE_TERM") == "1" && sig == syscall.SIGTERM {
		select {}
	}
	signal.Stop(signals)
	os.Exit(0)
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive", pid)
}
