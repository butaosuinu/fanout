package codexapp

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// appServer is a Codex app-server subprocess listening on a localhost websocket
// address.
type appServer struct {
	Addr string

	cmd  *exec.Cmd
	done chan struct{}
	logs *lockedBuffer

	mu  sync.Mutex
	err error

	closeOnce sync.Once
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// startAppServer launches `<codexPath> app-server --listen ws://127.0.0.1:<port>`
// on a freshly reserved localhost port.
func startAppServer(codexPath string) (*appServer, error) {
	port, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	addr := "ws://127.0.0.1:" + strconv.Itoa(port)
	logs := &lockedBuffer{}
	cmd := exec.Command(codexPath, "app-server", "--listen", addr)
	cmd.Stdout = logs
	cmd.Stderr = logs
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s app-server: %w", codexPath, err)
	}
	server := &appServer{
		Addr: addr,
		cmd:  cmd,
		done: make(chan struct{}),
		logs: logs,
	}
	go func() {
		err := cmd.Wait()
		server.mu.Lock()
		server.err = err
		server.mu.Unlock()
		close(server.done)
	}()
	return server, nil
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve localhost port for Codex app-server: %w", err)
	}
	// Close error is irrelevant: the listener only reserved the port.
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("reserve localhost port for Codex app-server: unexpected address %s", ln.Addr())
	}
	return addr.Port, nil
}

// connectAppServer dials the server's websocket address, retrying until timeout.
func connectAppServer(server *appServer, timeout time.Duration) (*client, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if ok, err := server.Exited(); ok {
			return nil, fmt.Errorf("codex app-server exited before websocket connection: %w%s", err, serverLogSuffix(server))
		}
		client, err := dialClient(server.Addr, time.Second)
		if err == nil {
			return client, nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = contextDeadlineExceeded(timeout)
	}
	return nil, fmt.Errorf("connect Codex app-server websocket %s: %w%s", server.Addr, lastErr, serverLogSuffix(server))
}

func contextDeadlineExceeded(timeout time.Duration) error {
	return fmt.Errorf("timed out after %s", timeout)
}

func serverLogSuffix(server *appServer) string {
	if server == nil || server.logs == nil {
		return ""
	}
	logs := strings.TrimSpace(server.logs.String())
	if logs == "" {
		return ""
	}
	return "; app-server output: " + logs
}

func (s *appServer) Exited() (bool, error) {
	if s == nil {
		return true, nil
	}
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return true, s.err
	default:
		return false, nil
	}
}

// Done is closed once the app-server process has exited. A nil receiver
// reports done immediately, matching the nil tolerance of Exited and Close.
func (s *appServer) Done() <-chan struct{} {
	if s == nil {
		return closedDone
	}
	return s.done
}

var closedDone = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

func (s *appServer) Close() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	s.closeOnce.Do(func() {
		processGroup := s.cmd.Process.Pid
		launcherExited, _ := s.Exited()
		if !launcherExited {
			// Signal the Node launcher first. The Codex wrapper forwards SIGTERM to
			// its native child; killing the group immediately would skip that path.
			_ = s.cmd.Process.Signal(syscall.SIGTERM)
			launcherExited = waitForAppServerExit(s.done, processShutdownTimeout)
		}
		if launcherExited && waitForProcessGroupExit(processGroup, interruptShutdownGrace) {
			return
		}
		// The launcher may have exited before installing its forwarding handler.
		// app-server has a dedicated process group, so give a surviving native
		// child its own graceful TERM window before escalating.
		_ = syscall.Kill(-processGroup, syscall.SIGTERM)
		if waitForProcessGroupExit(processGroup, processShutdownTimeout) {
			_ = waitForAppServerExit(s.done, processShutdownTimeout)
			return
		}
		_ = syscall.Kill(-processGroup, syscall.SIGKILL)
		_ = s.cmd.Process.Kill()
		_ = waitForAppServerExit(s.done, processShutdownTimeout)
		_ = waitForProcessGroupExit(processGroup, processShutdownTimeout)
	})
}

func signalExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func stopProcess(cmd *exec.Cmd, done <-chan error, interruptAlreadyDelivered bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if processDone(done) {
		return
	}
	if interruptAlreadyDelivered && waitForProcessExit(done, interruptShutdownGrace) {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if waitForProcessExit(done, processShutdownTimeout) {
		return
	}
	_ = cmd.Process.Kill()
	_ = waitForProcessExit(done, processShutdownTimeout)
}

func waitForProcessExit(done <-chan error, timeout time.Duration) bool {
	if done == nil {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func processDone(done <-chan error) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func waitForAppServerExit(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func waitForProcessGroupExit(processGroup int, timeout time.Duration) bool {
	if processGroup <= 0 || processGroupExited(processGroup) {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if processGroupExited(processGroup) {
				return true
			}
		case <-timer.C:
			return processGroupExited(processGroup)
		}
	}
}

func processGroupExited(processGroup int) bool {
	err := syscall.Kill(-processGroup, 0)
	return errors.Is(err, syscall.ESRCH)
}
