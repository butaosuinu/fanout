package herdrrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/naming"
)

const (
	ownedSupervisorHelperRoleEnv     = "FANOUT_TEST_OWNED_SUPERVISOR_ROLE"
	ownedSupervisorHelperMarkerEnv   = "FANOUT_TEST_OWNED_SUPERVISOR_MARKER"
	ownedSupervisorHelperNonceEnv    = "FANOUT_TEST_OWNED_SUPERVISOR_NONCE"
	ownedSupervisorHelperTokenEnv    = "FANOUT_TEST_OWNED_SUPERVISOR_TOKEN"
	ownedSupervisorHelperEvidenceEnv = "FANOUT_TEST_OWNED_SUPERVISOR_EVIDENCE"
	ownedSupervisorHelperIgnoreEnv   = "FANOUT_TEST_OWNED_SUPERVISOR_IGNORE_SIGNALS"
	ownedSupervisorHelperChildEnv    = "FANOUT_TEST_OWNED_SUPERVISOR_SPAWN_CHILD"
	ownedSupervisorHelperReadyEnv    = "FANOUT_TEST_OWNED_SUPERVISOR_CHILD_READY"
	ownedSupervisorHelperExitEnv     = "FANOUT_TEST_OWNED_SUPERVISOR_EXIT_AFTER_READY"
	ownedSupervisorHelperExitCodeEnv = "FANOUT_TEST_OWNED_SUPERVISOR_EXIT_CODE"
	ownedSupervisorHelperExitGateEnv = "FANOUT_TEST_OWNED_SUPERVISOR_EXIT_GATE"
)

type ownedSupervisorEvidence struct {
	Args        []string          `json:"args"`
	Environment map[string]string `json:"environment"`
	PID         int               `json:"pid"`
	ChildPID    int               `json:"child_pid,omitempty"`
}

type ownedSupervisorLifecycleCase struct {
	ignoreSignals  bool
	spawnChild     bool
	repeatSignal   bool
	exitAfterReady bool
	exitCode       int
}

type fakeOwnedSupervisorResource struct {
	lock     *os.File
	listener net.Listener
}

type fakeOwnedSupervisor struct {
	starts    int
	resources []fakeOwnedSupervisorResource
}

func (fake *fakeOwnedSupervisor) start(markerPath, nonce, startToken string) (int, error) {
	fake.starts++
	runtimeDir := filepath.Dir(markerPath)
	lock, acquired, err := tryLockPrivateFile(filepath.Join(runtimeDir, ownedSupervisorLockName))
	if err != nil {
		return 0, err
	}
	if !acquired {
		return 0, os.ErrExist
	}
	pid := os.Getpid()
	marker := ownerMarker{OwnerNonce: nonce, SupervisorStartToken: startToken, SupervisorPID: pid}
	if leaseErr := writeSupervisorLease(lock, marker); leaseErr != nil {
		unlockPrivateFile(lock)
		return 0, leaseErr
	}
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, "herdr.sock"))
	if err != nil {
		unlockPrivateFile(lock)
		return 0, err
	}
	if chmodErr := os.Chmod(filepath.Join(runtimeDir, "herdr.sock"), 0o600); chmodErr != nil {
		_ = listener.Close()
		unlockPrivateFile(lock)
		return 0, chmodErr
	}
	fake.resources = append(fake.resources, fakeOwnedSupervisorResource{lock: lock, listener: listener})
	return pid, nil
}

func (fake *fakeOwnedSupervisor) stopAll() {
	for _, resource := range fake.resources {
		_ = resource.listener.Close()
		unlockPrivateFile(resource.lock)
	}
	fake.resources = nil
}

func TestEnsureOwnedCreatesAndReadoptsPrivateSession(t *testing.T) {
	t.Setenv(sessionEnv, "ambient-session")
	t.Setenv(socketEnv, "/tmp/ambient.sock")
	commonDir := t.TempDir()
	runtimeBase := shortOwnedRuntimeBase(t)
	session := namingForTest(t, commonDir)
	layout, err := prepareOwnedLayout(runtimeBase, session)
	if err != nil {
		t.Fatal(err)
	}
	fakeCLI := newFakeHerdr(session, layout.socketPath)
	backend := newTestBackend(t, session, layout.socketPath, fakeCLI)
	fakeSupervisor := &fakeOwnedSupervisor{}
	t.Cleanup(fakeSupervisor.stopAll)

	owned, err := ensureOwned(
		context.Background(),
		OwnedOptions{GitCommonDir: commonDir, RuntimeBase: runtimeBase},
		backend,
		fakeSupervisor.start,
	)
	if err != nil {
		t.Fatalf("ensureOwned() error = %v", err)
	}
	if fakeSupervisor.starts != 1 {
		t.Fatalf("supervisor starts = %d, want 1", fakeSupervisor.starts)
	}
	if owned.Session != session || owned.SocketPath != layout.socketPath || owned.ClientSocketPath != layout.clientSocketPath || owned.Backend() != backend {
		t.Fatalf("EnsureOwned() = %+v, want session layout %+v", owned, layout)
	}
	assertPrivateMode(t, layout.runtimeBase, 0o700)
	for _, path := range []string{
		layout.runtimeDir,
		layout.xdgConfigHome,
		layout.xdgStateHome,
		layout.xdgDataHome,
		layout.xdgCacheHome,
		filepath.Dir(layout.configPath),
	} {
		assertPrivateMode(t, path, 0o700)
	}
	for _, path := range []string{layout.markerPath, layout.lifecycleLock, layout.supervisorLock, layout.configPath, layout.socketPath} {
		assertPrivateMode(t, path, 0o600)
	}
	marker, found, err := readOwnerMarker(layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = (%+v, %t, %v), want marker", marker, found, err)
	}
	canonicalCommonDir, err := canonicalGitCommonDir(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	if marker.GitCommonDir != canonicalCommonDir || marker.Session != session || marker.OwnerNonce == "" || marker.BinaryVersion != minimumVersion {
		t.Fatalf("owner marker = %+v, want canonical repository admission", marker)
	}

	attach, err := owned.AttachCommand()
	if err != nil {
		t.Fatalf("AttachCommand() error = %v", err)
	}
	for _, required := range []string{
		sessionEnv + "='" + session + "'",
		socketEnv + "='" + layout.socketPath + "'",
		clientSocketEnv + "='" + layout.clientSocketPath + "'",
		configEnv + "='" + layout.configPath + "'",
	} {
		if !strings.Contains(attach, required) {
			t.Errorf("AttachCommand() = %q, want %q", attach, required)
		}
	}
	if strings.Contains(attach, " attach") || !strings.HasSuffix(attach, "'/private/tmp/herdr-0.7.4'") {
		t.Errorf("AttachCommand() = %q, want bare admitted herdr command", attach)
	}
	fakeCLI.status = strings.Replace(fakeCLI.status, `"restart_needed":false`, `"restart_needed":true`, 1)
	if staleAttach, attachErr := owned.AttachCommand(); attachErr == nil || !strings.Contains(attachErr.Error(), "requires a client/server restart") {
		t.Fatalf("AttachCommand() after status drift = (%q, %v), want connected-status rejection", staleAttach, attachErr)
	}

	readoptCLI := newFakeHerdr(session, layout.socketPath)
	readoptBackend := newTestBackend(t, session, layout.socketPath, readoptCLI)
	readopted, err := ensureOwned(
		context.Background(),
		OwnedOptions{GitCommonDir: commonDir, RuntimeBase: runtimeBase},
		readoptBackend,
		fakeSupervisor.start,
	)
	if err != nil {
		t.Fatalf("second ensureOwned() error = %v", err)
	}
	if fakeSupervisor.starts != 1 {
		t.Fatalf("supervisor starts after re-adoption = %d, want 1", fakeSupervisor.starts)
	}
	if readopted.Session != owned.Session || readopted.SocketPath != owned.SocketPath {
		t.Fatalf("re-adopted session = %+v, want %+v", readopted, owned)
	}
	if readopted.Backend() != readoptBackend {
		t.Fatal("re-adoption did not bind the fresh backend")
	}
	markerAgain, found, err := readOwnerMarker(layout.markerPath)
	if err != nil || !found || markerAgain != marker {
		t.Fatalf("re-adopted marker = (%+v, %t, %v), want unchanged %+v", markerAgain, found, err, marker)
	}

	for _, call := range fakeCLI.commands {
		for key, want := range map[string]string{
			xdgConfigEnv:    layout.xdgConfigHome,
			xdgStateEnv:     layout.xdgStateHome,
			xdgDataEnv:      layout.xdgDataHome,
			xdgCacheEnv:     layout.xdgCacheHome,
			configEnv:       layout.configPath,
			sessionEnv:      session,
			socketEnv:       layout.socketPath,
			clientSocketEnv: layout.clientSocketPath,
		} {
			if got, ok := envValue(call.env, key); !ok || got != want {
				t.Errorf("%v env %s = %q, %t; want %q", call.args, key, got, ok, want)
			}
		}
	}
}

func TestEnsureOwnedRestartsStoppedSupervisorWithoutChangingOwnerNonce(t *testing.T) {
	commonDir := t.TempDir()
	runtimeBase := shortOwnedRuntimeBase(t)
	session := namingForTest(t, commonDir)
	layout, err := prepareOwnedLayout(runtimeBase, session)
	if err != nil {
		t.Fatal(err)
	}
	fakeCLI := newFakeHerdr(session, layout.socketPath)
	backend := newTestBackend(t, session, layout.socketPath, fakeCLI)
	fakeSupervisor := &fakeOwnedSupervisor{}
	t.Cleanup(fakeSupervisor.stopAll)
	options := OwnedOptions{GitCommonDir: commonDir, RuntimeBase: runtimeBase}

	if _, ensureErr := ensureOwned(context.Background(), options, backend, fakeSupervisor.start); ensureErr != nil {
		t.Fatalf("first ensureOwned() error = %v", ensureErr)
	}
	before, _, err := readOwnerMarker(layout.markerPath)
	if err != nil {
		t.Fatal(err)
	}
	fakeSupervisor.stopAll()
	if _, ensureErr := ensureOwned(context.Background(), options, backend, fakeSupervisor.start); ensureErr != nil {
		t.Fatalf("restart ensureOwned() error = %v", ensureErr)
	}
	after, _, err := readOwnerMarker(layout.markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if fakeSupervisor.starts != 2 {
		t.Fatalf("supervisor starts = %d, want 2", fakeSupervisor.starts)
	}
	if after.OwnerNonce != before.OwnerNonce {
		t.Errorf("owner nonce changed across restart")
	}
	if after.SupervisorStartToken == before.SupervisorStartToken {
		t.Errorf("supervisor start token was not rotated")
	}
}

func TestEnsureOwnedRejectsForeignSocketBeforeStartingSupervisor(t *testing.T) {
	commonDir := t.TempDir()
	runtimeBase := shortOwnedRuntimeBase(t)
	session := namingForTest(t, commonDir)
	layout, err := prepareOwnedLayout(runtimeBase, session)
	if err != nil {
		t.Fatal(err)
	}
	if baseErr := ensurePrivateDir(layout.runtimeBase); baseErr != nil {
		t.Fatal(baseErr)
	}
	if dirErr := ensurePrivateDir(layout.runtimeDir); dirErr != nil {
		t.Fatal(dirErr)
	}
	if setupErr := ensureOwnedDirectories(layout); setupErr != nil {
		t.Fatal(setupErr)
	}
	listener, err := net.Listen("unix", layout.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if chmodErr := os.Chmod(layout.socketPath, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	fakeCLI := newFakeHerdr(session, layout.socketPath)
	backend := newTestBackend(t, session, layout.socketPath, fakeCLI)
	starts := 0
	_, err = ensureOwned(context.Background(), OwnedOptions{GitCommonDir: commonDir, RuntimeBase: runtimeBase}, backend, func(_, _, _ string) (int, error) {
		starts++
		return os.Getpid(), nil
	})
	if err == nil || !strings.Contains(err.Error(), "foreign socket") {
		t.Fatalf("ensureOwned() error = %v, want foreign socket rejection", err)
	}
	if starts != 0 {
		t.Fatalf("supervisor starts = %d, want 0", starts)
	}
}

func TestEnsureOwnedRejectsStaleClientSocketOnRestart(t *testing.T) {
	commonDir := t.TempDir()
	runtimeBase := shortOwnedRuntimeBase(t)
	session := namingForTest(t, commonDir)
	layout, err := prepareOwnedLayout(runtimeBase, session)
	if err != nil {
		t.Fatal(err)
	}
	fakeCLI := newFakeHerdr(session, layout.socketPath)
	backend := newTestBackend(t, session, layout.socketPath, fakeCLI)
	fakeSupervisor := &fakeOwnedSupervisor{}
	t.Cleanup(fakeSupervisor.stopAll)
	options := OwnedOptions{GitCommonDir: commonDir, RuntimeBase: runtimeBase}
	if _, ensureErr := ensureOwned(context.Background(), options, backend, fakeSupervisor.start); ensureErr != nil {
		t.Fatalf("first ensureOwned() error = %v", ensureErr)
	}
	fakeSupervisor.stopAll()
	client, err := net.Listen("unix", layout.clientSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if chmodErr := os.Chmod(layout.clientSocketPath, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}

	_, err = ensureOwned(context.Background(), options, backend, fakeSupervisor.start)
	if err == nil || !strings.Contains(err.Error(), "foreign socket") || !strings.Contains(err.Error(), layout.clientSocketPath) {
		t.Fatalf("restart ensureOwned() error = %v, want client-socket rejection", err)
	}
	if fakeSupervisor.starts != 1 {
		t.Fatalf("supervisor starts = %d, want no restart", fakeSupervisor.starts)
	}
}

func TestEnsureOwnedRejectsPermissiveRuntimeBase(t *testing.T) {
	runtimeBase := shortOwnedRuntimeBase(t)
	if err := os.Mkdir(runtimeBase, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtimeBase, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ensureOwned(
		context.Background(),
		OwnedOptions{GitCommonDir: t.TempDir(), RuntimeBase: runtimeBase},
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "want 0700") {
		t.Fatalf("ensureOwned() error = %v, want private runtime rejection", err)
	}
}

func TestEnsureOwnedLifecycleLockHonorsDeadline(t *testing.T) {
	commonDir := t.TempDir()
	runtimeBase := shortOwnedRuntimeBase(t)
	session := namingForTest(t, commonDir)
	layout, err := prepareOwnedLayout(runtimeBase, session)
	if err != nil {
		t.Fatal(err)
	}
	if baseErr := ensurePrivateDir(layout.runtimeBase); baseErr != nil {
		t.Fatal(baseErr)
	}
	if dirErr := ensurePrivateDir(layout.runtimeDir); dirErr != nil {
		t.Fatal(dirErr)
	}
	holder, err := lockPrivateFile(layout.lifecycleLock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockPrivateFile(holder)

	ctx, cancel := context.WithTimeout(context.Background(), 2*ownedLockRetryInterval)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, ensureErr := ensureOwned(
			ctx,
			OwnedOptions{GitCommonDir: commonDir, RuntimeBase: runtimeBase},
			nil,
			nil,
		)
		done <- ensureErr
	}()

	select {
	case ensureErr := <-done:
		if !errors.Is(ensureErr, context.DeadlineExceeded) {
			t.Fatalf("ensureOwned() error = %v, want deadline exceeded while lifecycle lock is held", ensureErr)
		}
	case <-time.After(time.Second):
		t.Fatal("ensureOwned() did not honor its deadline while lifecycle lock was held")
	}
}

func TestAcquireOwnedOperationLockHonorsCancellation(t *testing.T) {
	runtimeBase := shortOwnedRuntimeBase(t)
	layout, err := prepareOwnedLayout(runtimeBase, "fanout-lock-cancellation")
	if err != nil {
		t.Fatal(err)
	}
	if baseErr := ensurePrivateDir(layout.runtimeBase); baseErr != nil {
		t.Fatal(baseErr)
	}
	if dirErr := ensurePrivateDir(layout.runtimeDir); dirErr != nil {
		t.Fatal(dirErr)
	}
	holder, err := lockPrivateFile(layout.lifecycleLock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockPrivateFile(holder)
	backend := &Backend{owner: &ownedAdmission{
		marker:     ownerMarker{RuntimeDir: layout.runtimeDir},
		markerPath: layout.markerPath,
		lockPath:   layout.lifecycleLock,
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, lock, acquireErr := backend.acquireOwnedOperation(ctx)
		if lock != nil {
			unlockPrivateFile(lock)
		}
		done <- acquireErr
	}()
	time.Sleep(2 * ownedLockRetryInterval)
	cancel()

	select {
	case acquireErr := <-done:
		if !errors.Is(acquireErr, context.Canceled) {
			t.Fatalf("acquireOwnedOperation() error = %v, want context canceled while lifecycle lock is held", acquireErr)
		}
	case <-time.After(time.Second):
		t.Fatal("acquireOwnedOperation() did not honor cancellation while lifecycle lock was held")
	}
}

func TestPrepareOwnedLayoutBoundsPortableSocketPaths(t *testing.T) {
	session := naming.HerdrSessionName("/tmp/" + strings.Repeat("long-repository-name-", 20) + "/.git")
	if len(session) != naming.MaxHerdrSessionNameLength {
		t.Fatalf("session length = %d, want %d", len(session), naming.MaxHerdrSessionNameLength)
	}
	layout, err := prepareOwnedLayout("", session)
	if err != nil {
		t.Fatalf("prepareOwnedLayout(default) error = %v", err)
	}
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if len(path) > maxUnixSocketPathBytes {
			t.Errorf("default socket path has %d bytes, want <= %d: %s", len(path), maxUnixSocketPathBytes, path)
		}
	}
	_, err = prepareOwnedLayout("/tmp/"+strings.Repeat("x", maxUnixSocketPathBytes), "fanout-test")
	if err == nil || !strings.Contains(err.Error(), "socket path") {
		t.Fatalf("prepareOwnedLayout(long) error = %v, want socket length rejection", err)
	}
}

func TestSupervisorHiddenRequestShape(t *testing.T) {
	if !IsSupervisorRequest([]string{ownedSupervisorCommand, "marker", "nonce", "token"}) {
		t.Fatal("IsSupervisorRequest() = false for hidden command")
	}
	if IsSupervisorRequest([]string{"status"}) {
		t.Fatal("IsSupervisorRequest() = true for public command")
	}
	var stderr strings.Builder
	if code := RunSupervisor(nil, &stderr); code != 2 || !strings.Contains(stderr.String(), "expected marker path") {
		t.Fatalf("RunSupervisor(nil) = %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if code := RunSupervisor([]string{"relative-owner.json", strings.Repeat("a", 64), strings.Repeat("b", 64)}, &stderr); code != 2 || !strings.Contains(stderr.String(), "invalid marker path") {
		t.Fatalf("RunSupervisor(relative marker) = %d, stderr %q", code, stderr.String())
	}
}

func TestClassifyOwnedProcessGroupProbe(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantRunning bool
		wantErr     error
	}{
		{name: "present", wantRunning: true},
		{name: "absent", err: syscall.ESRCH},
		{name: "permission transition stays running", err: syscall.EPERM, wantRunning: true},
		{name: "unexpected failure", err: syscall.EINVAL, wantErr: syscall.EINVAL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			running, err := classifyOwnedProcessGroupProbe(test.err)
			if running != test.wantRunning || !errors.Is(err, test.wantErr) {
				t.Fatalf("classifyOwnedProcessGroupProbe(%v) = (%t, %v), want (%t, %v)", test.err, running, err, test.wantRunning, test.wantErr)
			}
		})
	}
}

func TestOpenPrivateAppendFileRejectsPermissiveExistingLog(t *testing.T) {
	runtimeBase := shortOwnedRuntimeBase(t)
	if err := ensurePrivateDir(runtimeBase); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(runtimeBase, ownedServerLogName)
	if err := os.WriteFile(logPath, []byte("public log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(logPath, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := openPrivateAppendFile(logPath)
	if file != nil {
		_ = file.Close()
		t.Fatal("openPrivateAppendFile() returned a permissive log")
	}
	if err == nil || !strings.Contains(err.Error(), "want 0600") {
		t.Fatalf("openPrivateAppendFile() error = %v, want private-mode rejection", err)
	}
}

func TestWriteOwnerMarkerExclusiveNeverReplacesExistingClaim(t *testing.T) {
	runtimeBase := shortOwnedRuntimeBase(t)
	if err := ensurePrivateDir(runtimeBase); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(runtimeBase, ownedMarkerName)
	first := ownerMarker{OwnerNonce: strings.Repeat("a", 64)}
	if err := writeOwnerMarkerExclusive(markerPath, first); err != nil {
		t.Fatal(err)
	}
	second := ownerMarker{OwnerNonce: strings.Repeat("b", 64)}
	if err := writeOwnerMarkerExclusive(markerPath, second); err == nil || !strings.Contains(err.Error(), "claim herdr ownership marker") {
		t.Fatalf("second writeOwnerMarkerExclusive() error = %v, want exclusive-claim failure", err)
	}
	got, found, err := readOwnerMarker(markerPath)
	if err != nil || !found || got != first {
		t.Fatalf("readOwnerMarker() = (%+v, %t, %v), want original claim %+v", got, found, err, first)
	}
	entries, err := os.ReadDir(runtimeBase)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".owner-") {
			t.Errorf("staging marker was not removed: %s", entry.Name())
		}
	}
}

func TestRunSupervisorBindsLeaseAndExplicitServerEnvironment(t *testing.T) {
	if role := os.Getenv(ownedSupervisorHelperRoleEnv); role != "" {
		runOwnedSupervisorHelper(t, role)
		return
	}

	tests := []struct {
		name   string
		config ownedSupervisorLifecycleCase
	}{
		{name: "graceful server"},
		{
			name: "signal-ignoring server and child are killed after grace",
			config: ownedSupervisorLifecycleCase{
				ignoreSignals: true,
				spawnChild:    true,
			},
		},
		{
			name: "repeated signal escalates without waiting for grace",
			config: ownedSupervisorLifecycleCase{
				ignoreSignals: true,
				spawnChild:    true,
				repeatSignal:  true,
			},
		},
		{
			name: "natural server exit cleans residual child",
			config: ownedSupervisorLifecycleCase{
				spawnChild:     true,
				exitAfterReady: true,
			},
		},
		{
			name: "server crash cleans residual child",
			config: ownedSupervisorLifecycleCase{
				spawnChild:     true,
				exitAfterReady: true,
				exitCode:       7,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runOwnedSupervisorLifecycleCase(t, test.config)
		})
	}
}

func runOwnedSupervisorLifecycleCase(t *testing.T, config ownedSupervisorLifecycleCase) {
	t.Helper()
	commonDir := t.TempDir()
	runtimeBase := shortOwnedRuntimeBase(t)
	session := namingForTest(t, commonDir)
	layout, err := prepareOwnedLayout(runtimeBase, session)
	if err != nil {
		t.Fatal(err)
	}
	if setupErr := ensureOwnedDirectories(layout); setupErr != nil {
		t.Fatal(setupErr)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeBinary := filepath.Join(layout.runtimeDir, "herdr-test-server")
	script := "#!/bin/sh\n" + ownedSupervisorHelperRoleEnv + "=server exec " + shellQuote(executable) +
		" -test.run='^TestRunSupervisorBindsLeaseAndExplicitServerEnvironment$' -- \"$@\"\n"
	if writeErr := os.WriteFile(fakeBinary, []byte(script), 0o700); writeErr != nil {
		t.Fatal(writeErr)
	}
	binaryHash, err := sha256File(fakeBinary)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	startToken, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	canonicalCommonDir, err := canonicalGitCommonDir(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(layout.runtimeDir, "server-environment.json")

	for _, key := range []string{xdgConfigEnv, xdgStateEnv, xdgDataEnv, xdgCacheEnv, configEnv, sessionEnv, socketEnv, clientSocketEnv} {
		t.Setenv(key, "ambient-"+key)
	}
	t.Setenv("HERDR_PANE_ID", "ambient-pane")
	cmd := exec.Command(executable, "-test.run=^TestRunSupervisorBindsLeaseAndExplicitServerEnvironment$")
	cmd.Env = append(os.Environ(),
		ownedSupervisorHelperRoleEnv+"=supervisor",
		ownedSupervisorHelperMarkerEnv+"="+layout.markerPath,
		ownedSupervisorHelperNonceEnv+"="+nonce,
		ownedSupervisorHelperTokenEnv+"="+startToken,
		ownedSupervisorHelperEvidenceEnv+"="+evidencePath,
	)
	childReadyPath := filepath.Join(layout.runtimeDir, "server-child-ready")
	cmd.Env = envWithValue(cmd.Env, ownedSupervisorHelperIgnoreEnv, strconv.FormatBool(config.ignoreSignals))
	cmd.Env = envWithValue(cmd.Env, ownedSupervisorHelperChildEnv, strconv.FormatBool(config.spawnChild))
	cmd.Env = envWithValue(cmd.Env, ownedSupervisorHelperReadyEnv, childReadyPath)
	exitGatePath := filepath.Join(layout.runtimeDir, "server-exit-gate")
	cmd.Env = envWithValue(cmd.Env, ownedSupervisorHelperExitEnv, strconv.FormatBool(config.exitAfterReady))
	cmd.Env = envWithValue(cmd.Env, ownedSupervisorHelperExitCodeEnv, strconv.Itoa(config.exitCode))
	cmd.Env = envWithValue(cmd.Env, ownedSupervisorHelperExitGateEnv, exitGatePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	var evidence ownedSupervisorEvidence
	t.Cleanup(func() {
		if stopped {
			return
		}
		if evidence.PID <= 1 {
			if data, readErr := os.ReadFile(evidencePath); readErr == nil {
				_ = json.Unmarshal(data, &evidence) // Best-effort cleanup after an earlier assertion.
			}
		}
		if evidence.PID > 1 {
			_ = syscall.Kill(-evidence.PID, syscall.SIGKILL) // Best effort after a failed assertion.
		}
		_ = cmd.Process.Kill() // Best effort after a failed assertion.
		select {
		case <-done:
		case <-time.After(ownedShutdownKillWait):
			t.Errorf("supervisor cleanup did not finish within %s", ownedShutdownKillWait)
		}
	})

	marker := markerFor(
		layout,
		canonicalCommonDir,
		session,
		binaryAdmission{path: fakeBinary, sha256: binaryHash, version: minimumVersion, protocol: supportedProtocol},
		nonce,
		cmd.Process.Pid,
		startToken,
	)
	if markerErr := writeOwnerMarkerExclusive(layout.markerPath, marker); markerErr != nil {
		t.Fatal(markerErr)
	}

	deadline := time.Now().Add(ownedReadyTimeout)
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-done:
			stopped = true
			logData, _ := os.ReadFile(filepath.Join(layout.runtimeDir, ownedServerLogName)) // Best-effort diagnostic.
			t.Fatalf("supervisor exited before ready: %v; stderr=%q log=%q", waitErr, stderr.String(), logData)
		default:
		}
		lease, running, leaseErr := inspectSupervisorLease(layout.supervisorLock)
		evidenceData, evidenceErr := os.ReadFile(evidencePath)
		socketErr := validatePrivateSocket(layout.socketPath)
		if leaseErr == nil && running && validateSupervisorLease(marker, lease) == nil && evidenceErr == nil && socketErr == nil {
			if decodeErr := json.Unmarshal(evidenceData, &evidence); decodeErr != nil {
				t.Fatalf("decode server evidence: %v", decodeErr)
			}
			break
		}
		time.Sleep(ownedReadyInterval)
	}
	if len(evidence.Args) == 0 {
		t.Fatal("supervisor did not start the helper server before the readiness deadline")
	}
	if evidence.PID <= 1 {
		t.Fatalf("server PID = %d, want process-group leader PID", evidence.PID)
	}
	if got := evidence.Args[len(evidence.Args)-1]; got != "server" {
		t.Fatalf("server last argument = %q, want server", got)
	}
	for key, want := range map[string]string{
		xdgConfigEnv:    marker.XDGConfigHome,
		xdgStateEnv:     marker.XDGStateHome,
		xdgDataEnv:      marker.XDGDataHome,
		xdgCacheEnv:     marker.XDGCacheHome,
		configEnv:       marker.ConfigPath,
		sessionEnv:      marker.Session,
		socketEnv:       marker.SocketPath,
		clientSocketEnv: marker.ClientSocketPath,
	} {
		if got := evidence.Environment[key]; got != want {
			t.Errorf("server env %s = %q, want %q", key, got, want)
		}
	}
	if got := evidence.Environment["HERDR_PANE_ID"]; got != "" {
		t.Errorf("server inherited ambient HERDR_PANE_ID=%q", got)
	}
	lease, running, err := inspectSupervisorLease(layout.supervisorLock)
	if err != nil || !running {
		t.Fatalf("inspectSupervisorLease() = (%+v, %t, %v), want live lease", lease, running, err)
	}
	if err := validateSupervisorLease(marker, lease); err != nil {
		t.Fatal(err)
	}
	if config.spawnChild {
		if evidence.ChildPID <= 1 {
			t.Fatalf("server child PID = %d, want spawned child", evidence.ChildPID)
		}
		group, groupErr := syscall.Getpgid(evidence.ChildPID)
		if groupErr != nil {
			t.Fatalf("get server child process group: %v", groupErr)
		}
		if group != evidence.PID {
			t.Fatalf("server child process group = %d, want server group %d", group, evidence.PID)
		}
	} else if evidence.ChildPID != 0 {
		t.Fatalf("server child PID = %d, want no child", evidence.ChildPID)
	}

	shutdownStarted := time.Now()
	if config.exitAfterReady {
		if err := os.WriteFile(exitGatePath, []byte("exit\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if config.repeatSignal {
			time.Sleep(100 * time.Millisecond)
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
		}
	}
	select {
	case err := <-done:
		stopped = true
		if config.exitAfterReady && config.exitCode != 0 {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("supervisor crash status = %v, want exit code 1", err)
			}
			if !strings.Contains(stderr.String(), "server exited: exit status 7") {
				t.Fatalf("supervisor crash stderr = %q, want server exit status", stderr.String())
			}
		} else if err != nil {
			logData, _ := os.ReadFile(filepath.Join(layout.runtimeDir, ownedServerLogName)) // Best-effort diagnostic.
			t.Fatalf("supervisor shutdown: %v; stderr=%q log=%q", err, stderr.String(), logData)
		}
	case <-time.After(ownedReadyTimeout + ownedShutdownGrace + ownedShutdownKillWait):
		t.Fatal("supervisor did not finish bounded server process-group cleanup")
	}
	elapsed := time.Since(shutdownStarted)
	if config.ignoreSignals && !config.repeatSignal && elapsed < ownedShutdownGrace-ownedReadyInterval {
		t.Fatalf("signal-ignoring server shutdown took %s, want grace period of about %s", elapsed, ownedShutdownGrace)
	}
	if config.repeatSignal && elapsed >= ownedShutdownGrace {
		t.Fatalf("repeated signal shutdown took %s, want escalation before %s grace", elapsed, ownedShutdownGrace)
	}
	if config.spawnChild && !waitForProcessGone(evidence.ChildPID, ownedShutdownKillWait) {
		t.Fatalf("server child process %d survived supervisor shutdown", evidence.ChildPID)
	}
}

func runOwnedSupervisorHelper(t *testing.T, role string) {
	t.Helper()
	switch role {
	case "supervisor":
		code := RunSupervisor([]string{
			os.Getenv(ownedSupervisorHelperMarkerEnv),
			os.Getenv(ownedSupervisorHelperNonceEnv),
			os.Getenv(ownedSupervisorHelperTokenEnv),
		}, os.Stderr)
		os.Exit(code)
	case "server":
		ignoreSignals := os.Getenv(ownedSupervisorHelperIgnoreEnv) == "true"
		if ignoreSignals {
			signal.Ignore(os.Interrupt, syscall.SIGTERM)
			defer signal.Reset(os.Interrupt, syscall.SIGTERM)
		}
		listener, err := net.Listen("unix", os.Getenv(socketEnv))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		if chmodErr := os.Chmod(os.Getenv(socketEnv), 0o600); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		var signals chan os.Signal
		if !ignoreSignals {
			signals = make(chan os.Signal, 1)
			signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(signals)
		}
		childPID := 0
		var child *exec.Cmd
		if os.Getenv(ownedSupervisorHelperChildEnv) == "true" {
			executable, executableErr := os.Executable()
			if executableErr != nil {
				t.Fatal(executableErr)
			}
			child = exec.Command(executable, "-test.run=^TestRunSupervisorBindsLeaseAndExplicitServerEnvironment$")
			child.Env = envWithValue(os.Environ(), ownedSupervisorHelperRoleEnv, "server-child")
			if startErr := child.Start(); startErr != nil {
				t.Fatal(startErr)
			}
			childPID = child.Process.Pid
			defer func() {
				_ = child.Process.Kill() // Best effort when the helper exits before its process group is killed.
				_ = child.Wait()
			}()
			if readyErr := waitForFile(os.Getenv(ownedSupervisorHelperReadyEnv), ownedReadyTimeout); readyErr != nil {
				t.Fatal(readyErr)
			}
		}
		evidence := ownedSupervisorEvidence{
			Args: os.Args,
			Environment: map[string]string{
				xdgConfigEnv:    os.Getenv(xdgConfigEnv),
				xdgStateEnv:     os.Getenv(xdgStateEnv),
				xdgDataEnv:      os.Getenv(xdgDataEnv),
				xdgCacheEnv:     os.Getenv(xdgCacheEnv),
				configEnv:       os.Getenv(configEnv),
				sessionEnv:      os.Getenv(sessionEnv),
				socketEnv:       os.Getenv(socketEnv),
				clientSocketEnv: os.Getenv(clientSocketEnv),
				"HERDR_PANE_ID": os.Getenv("HERDR_PANE_ID"),
			},
			PID:      os.Getpid(),
			ChildPID: childPID,
		}
		data, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv(ownedSupervisorHelperEvidenceEnv), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if os.Getenv(ownedSupervisorHelperExitEnv) == "true" {
			if err := waitForFile(os.Getenv(ownedSupervisorHelperExitGateEnv), ownedReadyTimeout); err != nil {
				t.Fatal(err)
			}
			exitCode, err := strconv.Atoi(os.Getenv(ownedSupervisorHelperExitCodeEnv))
			if err != nil {
				t.Fatal(err)
			}
			os.Exit(exitCode) //nolint:gocritic // Defers must not reap the residual child under test.
		}
		if ignoreSignals {
			for {
				time.Sleep(time.Hour)
			}
		}
		<-signals
	case "server-child":
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		if err := os.WriteFile(
			os.Getenv(ownedSupervisorHelperReadyEnv),
			[]byte(strconv.Itoa(os.Getpid())+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		t.Fatalf("unknown helper role %q", role)
	}
}

func namingForTest(t *testing.T, commonDir string) string {
	t.Helper()
	canonical, err := canonicalGitCommonDir(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	return naming.HerdrSessionName(canonical)
}

func shortOwnedRuntimeBase(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fho-") //nolint:usetesting // t.TempDir exceeds Darwin's Unix socket path limit.
	if err != nil {
		t.Fatal(err)
	}
	if chmodErr := os.Chmod(root, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	t.Cleanup(func() {
		if strings.HasPrefix(root, "/tmp/fho-") {
			_ = os.RemoveAll(root)
		}
	})
	return filepath.Join(root, "runtime")
}

func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q): %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}
