package codexapp

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
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
	if ok, _ := s.Exited(); ok {
		return
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	_ = s.cmd.Process.Kill()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
}

func installCodexAppServerSignalCleanup(server *appServer) func() {
	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			server.Close()
			signal.Stop(sigCh)
			os.Exit(signalExitCode(sig))
		case <-stopCh:
		}
	}()
	return func() {
		signal.Stop(sigCh)
		close(stopCh)
	}
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

func stopProcess(cmd *exec.Cmd, done chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-done:
		return
	default:
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
