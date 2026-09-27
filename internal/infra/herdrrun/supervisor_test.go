package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/naming"
)

type fakeOwnedSupervisor struct {
	starts                  int
	dashboardAuthentication dashboardAuthentication
	lock                    *os.File
	listeners               []net.Listener
}

const ownedSupervisorTestHerdrCommand = "__herdr-test-herdr"

func TestMain(m *testing.M) {
	switch {
	case IsSupervisorRequest(os.Args[1:]):
		os.Exit(RunSupervisor(os.Args[2:], os.Stderr))
	case len(os.Args) > 1 && os.Args[1] == ownedSupervisorTestHerdrCommand:
		os.Exit(runOwnedSupervisorTestHerdr(os.Args[2:]))
	default:
		os.Exit(m.Run())
	}
}

func runOwnedSupervisorTestHerdr(args []string) int {
	switch {
	case slices.Equal(args, []string{"--version"}):
		_, _ = fmt.Fprintln(os.Stdout, "herdr 0.7.5")
		return 0
	case len(args) >= 2 && slices.Equal(args[len(args)-2:], []string{"status", "--json"}):
		return 1
	case slices.Equal(args, []string{"server"}):
		return runOwnedSupervisorTestServer()
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unexpected test herdr args: %q\n", args)
		return 2
	}
}

func runOwnedSupervisorTestServer() int {
	var listeners []net.Listener
	for _, path := range []string{os.Getenv(socketEnv), os.Getenv(clientSocketEnv)} {
		listener, err := net.Listen("unix", path)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "listen test herdr socket %s: %v\n", path, err)
			return 1
		}
		if err := os.Chmod(path, 0o700); err != nil {
			_ = listener.Close()
			_, _ = fmt.Fprintf(os.Stderr, "chmod test herdr socket %s: %v\n", path, err)
			return 1
		}
		listeners = append(listeners, listener)
	}
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	pidPath := filepath.Join(os.Getenv(xdgStateEnv), "test-server.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write test herdr pid: %v\n", err)
		return 1
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestNewOwnedSupervisorCommandRelaysDashboardAuthenticationByAlias(t *testing.T) {
	t.Setenv(dashboardGHTokenEnv, "gh-secret")
	t.Setenv(dashboardGitHubTokenEnv, "github-secret")
	cmd, authentication := newOwnedSupervisorCommand("/fanout", "/runtime/owner.json", "nonce", "start")
	wantEnvironment := []string{dashboardRelayTokenEnv + "=gh-secret"}
	if !slices.Equal(cmd.Env, wantEnvironment) {
		t.Fatalf("supervisor environment = %q, want %q", cmd.Env, wantEnvironment)
	}
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, dashboardGHTokenEnv+"=") ||
			strings.HasPrefix(entry, dashboardGitHubTokenEnv+"=") {
			t.Fatalf("supervisor environment retained raw authentication name %q", entry)
		}
	}
	want := dashboardAuthenticationFromCaller(os.Environ())
	if authentication != want || dashboardAuthenticationFromSupervisor(cmd.Env) != want ||
		!slices.Equal(dashboardInheritedAuthenticationEnvironment(cmd.Env), cmd.Env) {
		t.Fatalf("dashboard authentication = %+v env=%+v, want %+v", authentication, cmd.Env, want)
	}
}

func (s *fakeOwnedSupervisor) start(markerPath, nonce, startToken string) (*startedSupervisor, error) {
	s.starts++
	supervisorPID := 1_000_000_000 + s.starts*2
	serverPID := supervisorPID + 1
	runtimeDir := filepath.Dir(markerPath)
	lock, err := os.OpenFile(filepath.Join(runtimeDir, ownedSupervisorLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	lease := supervisorLease{
		SchemaID: ownedMarkerSchemaID, OwnerNonce: nonce, StartToken: startToken,
		PID: supervisorPID, ServerPID: serverPID,
	}
	data, err := json.Marshal(lease)
	if err != nil {
		return nil, err
	}
	_, err = lock.WriteAt(data, 0)
	if err != nil {
		return nil, err
	}
	err = lock.Sync()
	if err != nil {
		return nil, err
	}
	s.lock = lock
	for _, path := range []string{filepath.Join(runtimeDir, "herdr.sock"), filepath.Join(runtimeDir, "herdr-client.sock")} {
		listener, err := net.Listen("unix", path)
		if err != nil {
			return nil, err
		}
		err = os.Chmod(path, 0o700)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		s.listeners = append(s.listeners, listener)
	}
	return &startedSupervisor{
		pid:                     supervisorPID,
		dashboardAuthentication: s.dashboardAuthentication,
		signal: func(os.Signal) error {
			s.close()
			return nil
		},
		wait: func() error { return nil },
	}, nil
}

func (s *fakeOwnedSupervisor) close() {
	s.closeSockets()
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
}

func (s *fakeOwnedSupervisor) closeSockets() {
	for _, listener := range s.listeners {
		_ = listener.Close()
	}
	s.listeners = nil
}

type processOwnedHarness struct {
	commonDir   string
	runtimeBase string
	layout      ownedLayout
	backend     *Backend
	pidPath     string
}

func newProcessOwnedHarness(t *testing.T) processOwnedHarness {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fho-stop-") //nolint:usetesting // Darwin Unix socket paths are limited to 103 bytes.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	err = os.Chmod(root, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	commonDir := filepath.Join(root, "repo.git")
	err = os.Mkdir(commonDir, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	runtimeBase := filepath.Join(root, "runtime")
	_, identity, err := openCanonicalGitCommonDir(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	session := naming.ManagedSessionName(identity.device, identity.inode)
	layout, err := prepareOwnedLayout(runtimeBase, session)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	herdr := filepath.Join(root, "herdr")
	script := "#!/bin/sh\nexec " + shellQuote(executable) + " " + ownedSupervisorTestHerdrCommand + " \"$@\"\n"
	if err := os.WriteFile(herdr, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := New(session, layout.socketPath)
	backend.lookPath = func(string) (string, error) { return herdr, nil }
	return processOwnedHarness{
		commonDir: commonDir, runtimeBase: runtimeBase, layout: layout, backend: backend,
		pidPath: filepath.Join(layout.xdgStateHome, "test-server.pid"),
	}
}

func waitForTestServerPID(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return strconv.Atoi(strings.TrimSpace(string(data)))
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0, fmt.Errorf("test herdr server did not publish its pid")
}

func assertProcessOwnedRetired(t *testing.T, harness processOwnedHarness, serverPID int, ensureErr error) {
	t.Helper()
	if err := syscall.Kill(serverPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("test herdr server pid %d still exists: %v", serverPID, err)
	}
	if err := validateRetiredOwnedSession(harness.layout); err != nil {
		t.Fatalf("retired owned session validation: %v; ensure error: %v", err, ensureErr)
	}
}

func TestFreshReadinessFailureGracefullyStopsServerProcessGroup(t *testing.T) {
	harness := newProcessOwnedHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type readyResult struct {
		pid int
		err error
	}
	ready := make(chan readyResult, 1)
	go func() {
		pid, err := waitForTestServerPID(harness.pidPath, 3*time.Second)
		cancel()
		ready <- readyResult{pid: pid, err: err}
	}()

	_, ensureErr := ensureOwned(
		ctx,
		OwnedOptions{GitCommonDir: harness.commonDir, RuntimeBase: harness.runtimeBase},
		harness.backend,
		startOwnedSupervisor,
	)
	result := <-ready
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !errors.Is(ensureErr, context.Canceled) {
		t.Fatalf("ensureOwned() error = %v, want context cancellation after server start", ensureErr)
	}
	lease, _, err := inspectExistingSupervisorLease(harness.layout.supervisorLock)
	if err != nil || lease.ServerPID != result.pid {
		t.Fatalf("supervisor lease server pid = %d, err=%v, want %d", lease.ServerPID, err, result.pid)
	}
	assertProcessOwnedRetired(t, harness, result.pid, ensureErr)
}

func TestPublishedMarkerFailureGracefullyStopsObservedServer(t *testing.T) {
	harness := newProcessOwnedHarness(t)
	injectedErr := errors.New("injected post-link owner marker verification failure")
	serverPID := 0
	writeMarker := func(path string, marker ownerMarker) error {
		if err := writeOwnerMarkerExclusive(path, marker); err != nil {
			return err
		}
		pid, err := waitForTestServerPID(harness.pidPath, 3*time.Second)
		if err != nil {
			return err
		}
		serverPID = pid
		return injectedErr
	}

	_, ensureErr := ensureOwned(
		context.Background(),
		OwnedOptions{GitCommonDir: harness.commonDir, RuntimeBase: harness.runtimeBase},
		harness.backend,
		startOwnedSupervisor,
		writeMarker,
	)
	if !errors.Is(ensureErr, injectedErr) {
		t.Fatalf("ensureOwned() error = %v, want injected marker failure", ensureErr)
	}
	assertProcessOwnedRetired(t, harness, serverPID, ensureErr)
}

func TestVerifiedSupervisorShutdownRetiresMarkerAndAllowsRestart(t *testing.T) {
	h := newOwnedHarness(t)
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", marker, found, err)
	}
	h.supervisor.closeSockets()
	if err := retireOwnedSession(h.layout, marker, h.supervisor.lock); err != nil {
		t.Fatal(err)
	}
	h.supervisor.close()
	if _, found, err := readOwnerMarker(h.layout.markerPath); err != nil || found {
		t.Fatalf("retired owner marker found=%t err=%v", found, err)
	}
	restarted := h.ensure()
	if restarted.Session != h.session.Session || h.supervisor.starts != 2 {
		t.Fatalf("restarted session = %+v; starts=%d", restarted, h.supervisor.starts)
	}
}

func TestSupervisorRequestRequiresReadyHandshakeFD(t *testing.T) {
	if !IsSupervisorRequest([]string{ownedSupervisorCommand, "marker"}) {
		t.Fatal("IsSupervisorRequest() rejected hidden command")
	}
	var stderr strings.Builder
	if code := RunSupervisor(nil, &stderr); code != 2 || !strings.Contains(stderr.String(), "ready fd") {
		t.Fatalf("RunSupervisor(nil) = %d, %q", code, stderr.String())
	}
}
