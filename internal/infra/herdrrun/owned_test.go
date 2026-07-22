package herdrrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
)

type ownedHarness struct {
	t              *testing.T
	root           string
	commonDir      string
	runtimeBase    string
	binary         string
	layout         ownedLayout
	nonce          string
	checkout       string
	worktreeGitDir string
	fake           *fakeHerdr
	supervisor     *fakeOwnedSupervisor
	session        *OwnedSession
}

type fakeOwnedSupervisor struct {
	starts    int
	lock      *os.File
	listeners []net.Listener
}

func testBehaviorProfile(binarySHA256, schemaSHA256 string) behaviorProfile {
	profile := productionBehaviorProfile()
	profile.source = "test-fixture"
	profile.goos = runtime.GOOS
	profile.goarch = runtime.GOARCH
	profile.binarySHA256 = binarySHA256
	profile.schemaSHA256 = schemaSHA256
	profile.manifestSetDigest = manifestFixtureDigest(profile.manifests, binarySHA256)
	return profile
}

func (s *fakeOwnedSupervisor) start(markerPath, nonce, startToken string) (int, error) {
	s.starts++
	runtimeDir := filepath.Dir(markerPath)
	lock, err := os.OpenFile(filepath.Join(runtimeDir, ownedSupervisorLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = lock.Close()
		return 0, err
	}
	lease := supervisorLease{SchemaID: ownedMarkerSchemaID, OwnerNonce: nonce, StartToken: startToken, PID: os.Getpid()}
	data, err := json.Marshal(lease)
	if err != nil {
		return 0, err
	}
	_, err = lock.WriteAt(data, 0)
	if err != nil {
		return 0, err
	}
	err = lock.Sync()
	if err != nil {
		return 0, err
	}
	s.lock = lock
	for _, path := range []string{filepath.Join(runtimeDir, "herdr.sock"), filepath.Join(runtimeDir, "herdr-client.sock")} {
		listener, err := net.Listen("unix", path)
		if err != nil {
			return 0, err
		}
		err = os.Chmod(path, 0o600)
		if err != nil {
			_ = listener.Close()
			return 0, err
		}
		s.listeners = append(s.listeners, listener)
	}
	return os.Getpid(), nil
}

func (s *fakeOwnedSupervisor) close() {
	for _, listener := range s.listeners {
		_ = listener.Close()
	}
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
	}
}

func newOwnedHarness(t *testing.T) *ownedHarness {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fho-") //nolint:usetesting // Darwin Unix socket paths are limited to 103 bytes.
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
	sessionName := naming.HerdrSessionName(commonDir)
	layout, err := prepareOwnedLayout(runtimeBase, sessionName)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "herdr")
	if err := os.WriteFile(binary, []byte("fake herdr 0.7.5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("c", 64)
	checkout := filepath.Join(root, "checkout")
	worktreeGitDir := filepath.Join(commonDir, "worktrees", "child")
	for _, dir := range []string{checkout, worktreeGitDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fake := newFakeHerdr(sessionName, layout.socketPath)
	fake.snapshot = ownedSnapshotFixture(fake.snapshot, commonDir, root, checkout, nonce)
	supervisor := &fakeOwnedSupervisor{}
	h := &ownedHarness{
		t: t, root: root, commonDir: commonDir, runtimeBase: runtimeBase, binary: binary,
		layout: layout, nonce: nonce, checkout: checkout, worktreeGitDir: worktreeGitDir,
		fake: fake, supervisor: supervisor,
	}
	t.Cleanup(supervisor.close)
	h.session = h.ensure()
	return h
}

func TestPrepareOwnedLayoutUsesShortDefaultWithLongTMPDIR(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join("/private/var/folders", strings.Repeat("long-segment", 20)))
	session := strings.Repeat("s", naming.MaxHerdrSessionNameLength)
	layout, err := prepareOwnedLayout("", session)
	if err != nil {
		t.Fatal(err)
	}
	runtimeParent, err := filepath.EvalSymlinks(defaultRuntimeParent)
	if err != nil {
		t.Fatal(err)
	}
	wantBase := filepath.Join(runtimeParent, "fhr-"+strconv.Itoa(os.Getuid()))
	if layout.runtimeBase != wantBase {
		t.Fatalf("runtime base = %q, want %q", layout.runtimeBase, wantBase)
	}
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if len(path) > maxUnixSocketPathBytes {
			t.Fatalf("default socket path is %d bytes, want at most %d: %s", len(path), maxUnixSocketPathBytes, path)
		}
	}
}

func (h *ownedHarness) ensure() *OwnedSession {
	h.t.Helper()
	session, err := h.tryEnsure()
	if err != nil {
		h.t.Fatal(err)
	}
	return session
}

func (h *ownedHarness) tryEnsure() (*OwnedSession, error) {
	h.t.Helper()
	b := New(h.layout.runtimeDir[strings.LastIndex(h.layout.runtimeDir, string(os.PathSeparator))+1:], h.layout.socketPath)
	b.lookPath = func(string) (string, error) { return h.binary, nil }
	binaryHash, err := sha256File(h.binary)
	if err != nil {
		h.t.Fatal(err)
	}
	schemaHash := sha256.Sum256([]byte(h.fake.schema))
	b.behavior = testBehaviorProfile(binaryHash, hex.EncodeToString(schemaHash[:]))
	b.output = h.fake.output
	b.helpOutput = func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		for _, surface := range requiredCommandSurfaces {
			if len(args) == len(surface.args)+1 && slices.Equal(args[:len(surface.args)], surface.args) {
				return []byte(strings.Join(surface.required, "\n")), nil
			}
		}
		return nil, fmt.Errorf("unexpected help args %v", args)
	}
	return ensureOwned(context.Background(), OwnedOptions{GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase}, b, h.supervisor.start)
}

func (h *ownedHarness) target() OwnedPaneIdentity {
	h.t.Helper()
	panes, err := h.session.Backend().ListLive()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, pane := range panes {
		if pane.Ref.Pane == "w2:p1" {
			return OwnedPaneIdentity{
				Ref: pane.Ref, SessionID: pane.SessionID, SocketPath: pane.SocketPath,
				WorkspaceLabel: h.nonce, TerminalID: pane.TerminalID, RepoKey: pane.RepoKey,
				WorktreePath: pane.WorktreePath, CurrentPath: pane.CurrentPath,
				AgentID: pane.AgentID, AgentSession: cloneAgentSession(pane.AgentSession),
			}
		}
	}
	h.t.Fatal("owned child pane not found")
	return OwnedPaneIdentity{}
}

func (h *ownedHarness) closeRequest(target OwnedPaneIdentity) OwnedCloseRequest {
	h.t.Helper()
	marker := worktreeOwnershipMarker{
		Nonce: h.nonce, WorkspaceID: target.Ref.Workspace, RepoKey: target.RepoKey,
		CheckoutPath: target.WorktreePath, GitDir: h.worktreeGitDir,
	}
	data, err := json.Marshal(marker)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.worktreeGitDir, worktreeOwnershipMarkerName), data, 0o600); err != nil {
		h.t.Fatal(err)
	}
	return OwnedCloseRequest{Target: target, WorktreeOwnershipNonce: h.nonce, WorktreeGitDir: h.worktreeGitDir}
}

func ownedSnapshotFixture(source, commonDir, repoRoot, checkout, nonce string) string {
	source = strings.ReplaceAll(source, "/repo/.git", commonDir)
	source = strings.ReplaceAll(source, "/repo/.fanout/worktrees/child", checkout)
	source = strings.ReplaceAll(source, `"repo_root":"/repo"`, `"repo_root":`+strconvQuote(repoRoot))
	return strings.Replace(source, `"label":"child"`, `"label":`+strconvQuote(nonce), 1)
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func mutateSnapshot(source string, mutate func(*snapshotJSON)) string {
	var envelope snapshotEnvelope
	if err := json.Unmarshal([]byte(source), &envelope); err != nil {
		panic(err)
	}
	mutate(&envelope.Result.Snapshot)
	data, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestEnsureOwnedCreatesAndIdempotentlyReadoptsSession(t *testing.T) {
	h := newOwnedHarness(t)
	first := h.session
	second := h.ensure()
	if h.supervisor.starts != 1 {
		t.Fatalf("supervisor starts = %d, want 1", h.supervisor.starts)
	}
	if first.Session != second.Session || first.SocketPath != second.SocketPath || first.ClientSocketPath != second.ClientSocketPath {
		t.Fatalf("re-adopted session differs: first=%+v second=%+v", first, second)
	}
	command, err := second.AttachCommand()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, "session attach") || !strings.Contains(command, socketEnv+"='") || !strings.Contains(command, h.layout.binaryDir) {
		t.Fatalf("AttachCommand() = %q", command)
	}
}

func TestEnsureOwnedFailsClosedAfterDeadSupervisor(t *testing.T) {
	h := newOwnedHarness(t)
	previous, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", previous, found, err)
	}
	h.supervisor.close()
	_, ensureErr := h.tryEnsure()
	if !errors.Is(ensureErr, errOwnedSupervisorNotRunning) {
		t.Fatalf("ensure after dead supervisor error = %v, want fail-closed terminal state", ensureErr)
	}
	current, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found || current != previous || h.supervisor.starts != 1 {
		t.Fatalf("owner marker after refused restart = %+v, %v, %v; starts=%d", current, found, err, h.supervisor.starts)
	}
}

func TestStageExecutablePinsOpenedBytesBeforeCommands(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(root, "stage")
	if err := os.Mkdir(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "herdr")
	original := []byte("#!/bin/sh\nprintf 'original\\n'\n")
	if err := os.WriteFile(source, original, 0o700); err != nil {
		t.Fatal(err)
	}
	pinned, digest, err := stageExecutable(source, stageDir)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(original)
	if digest != hex.EncodeToString(wantHash[:]) || pinned == source {
		t.Fatalf("stageExecutable() = %q, %q", pinned, digest)
	}
	err = os.WriteFile(source, []byte("#!/bin/sh\nprintf 'replacement\\n'\n"), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(pinned).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "original\n" {
		t.Fatalf("pinned executable output = %q", out)
	}
	info, err := os.Lstat(pinned)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode().Perm() != 0o500 || stat.Nlink != 1 {
		t.Fatalf("pinned executable identity = mode %v, stat %#v", info.Mode(), info.Sys())
	}
}

func TestOwnedOperationRejectsPinnedBinaryTampering(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", marker, found, err)
	}
	if marker.BinaryPath == h.binary || !strings.HasPrefix(marker.BinaryPath, h.layout.binaryDir+string(os.PathSeparator)) {
		t.Fatalf("owned binary path = %q, want content-addressed bundle", marker.BinaryPath)
	}
	if err := os.Chmod(marker.BinaryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker.BinaryPath, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.session.Backend().BindOwnedTarget(target); err == nil || !strings.Contains(err.Error(), "binary identity changed") {
		t.Fatalf("BindOwnedTarget() pinned binary error = %v", err)
	}
}

func TestEnsureOwnedRejectsRecreatedGitCommonDirectory(t *testing.T) {
	h := newOwnedHarness(t)
	displaced := h.commonDir + "-displaced"
	renameErr := os.Rename(h.commonDir, displaced)
	if renameErr != nil {
		t.Fatal(renameErr)
	}
	mkdirErr := os.Mkdir(h.commonDir, 0o700)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if _, err := h.tryEnsure(); err == nil || !strings.Contains(err.Error(), "runtime layout") {
		t.Fatalf("ensure after git common directory replacement error = %v", err)
	}
}

func TestEnsureOwnedRejectsBehaviorProfileBeforeRuntimeCreation(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "fho-profile-") //nolint:usetesting // Keep Darwin Unix socket paths below the kernel limit.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	commonDir := filepath.Join(root, "repo.git")
	err = os.Mkdir(commonDir, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	runtimeBase := filepath.Join(root, "runtime")
	session := naming.HerdrSessionName(commonDir)
	layout, err := prepareOwnedLayout(runtimeBase, session)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeHerdr(session, layout.socketPath)
	b := newTestBackend(t, session, layout.socketPath, fake)
	schemaHash := sha256.Sum256([]byte(fake.schema))
	b.behavior = testBehaviorProfile(strings.Repeat("b", 64), hex.EncodeToString(schemaHash[:]))
	if _, err := ensureOwned(context.Background(), OwnedOptions{GitCommonDir: commonDir, RuntimeBase: runtimeBase}, b, func(string, string, string) (int, error) {
		t.Fatal("supervisor started before behavior admission")
		return 0, nil
	}); err == nil || !strings.Contains(err.Error(), "outside owned behavior profile") {
		t.Fatalf("EnsureOwned() behavior error = %v", err)
	}
	if _, err := os.Stat(runtimeBase); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime base exists before behavior admission: %v", err)
	}
}

func TestOwnedPrimitivesRejectActiveManifestDrift(t *testing.T) {
	for _, operation := range []string{"read", "send", "focus", "close", "owned close"} {
		t.Run(operation, func(t *testing.T) {
			h := newOwnedHarness(t)
			target := h.target()
			var call func() error
			if operation == "owned close" {
				bound, err := h.session.Backend().BindOwnedClose(h.closeRequest(target))
				if err != nil {
					t.Fatal(err)
				}
				call = func() error {
					_, closeErr := bound.CloseOwned(corebackend.CloseRequest{Ref: target.Ref, WorktreePath: target.WorktreePath, ShellKey: target.TerminalID})
					return closeErr
				}
			} else {
				bound, err := h.session.Backend().BindOwnedTarget(target)
				if err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "read":
					call = func() error { _, readErr := bound.Read(target.Ref, 1); return readErr }
				case "send":
					call = func() error { return bound.SendLine(target.Ref, "hello") }
				case "focus":
					call = func() error { return bound.Focus(target.Ref) }
				case "close":
					call = func() error { return bound.Close(target.Ref) }
				}
			}
			h.fake.manifests = strings.Replace(h.fake.manifests, "2026.07.18.1", "2099.01.01.1", 1)
			baseline := len(h.fake.commands)
			if err := call(); err == nil || !strings.Contains(err.Error(), "active manifest set") {
				t.Fatalf("%s manifest drift error = %v", operation, err)
			}
			for _, command := range h.fake.commands[baseline:] {
				if commandKey(command.args) == "" {
					t.Fatalf("manifest drift allowed %s command: %v", operation, command.args)
				}
			}
		})
	}
}

func TestOwnedBindingsRejectForeignRouteAndImmutableTargetReplacement(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	closeRequest := h.closeRequest(target)
	baseline := len(h.fake.commands)
	foreign := target
	foreign.SocketPath = filepath.Join(h.root, "foreign.sock")
	if _, err := h.session.Backend().BindOwnedTarget(foreign); !errors.Is(err, ErrOwnedIdentityMismatch) {
		t.Fatalf("BindOwnedTarget(foreign) error = %v", err)
	}
	foreignClose := closeRequest
	foreignClose.Target = foreign
	if _, err := h.session.Backend().BindOwnedClose(foreignClose); !errors.Is(err, ErrOwnedIdentityMismatch) {
		t.Fatalf("BindOwnedClose(foreign) error = %v", err)
	}
	if len(h.fake.commands) != baseline {
		t.Fatalf("foreign bindings invoked herdr: before=%d after=%d", baseline, len(h.fake.commands))
	}

	bound, err := h.session.Backend().BindOwnedTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	boundClose, err := h.session.Backend().BindOwnedClose(closeRequest)
	if err != nil {
		t.Fatal(err)
	}
	baseline = len(h.fake.commands)
	foreignRef := target.Ref
	foreignRef.Pane = "w2:p-foreign"
	calls := []func() error{
		func() error { _, err := bound.Read(foreignRef, 1); return err },
		func() error { return bound.SendLine(foreignRef, "hello") },
		func() error { return bound.Focus(foreignRef) },
		func() error { return bound.Close(foreignRef) },
		func() error {
			_, err := boundClose.CloseOwned(corebackend.CloseRequest{Ref: foreignRef, WorktreePath: target.WorktreePath, ShellKey: target.TerminalID})
			return err
		},
	}
	for index, call := range calls {
		if callErr := call(); !errors.Is(callErr, ErrOwnedIdentityMismatch) {
			t.Errorf("immutable operation %d error = %v", index, callErr)
		}
	}
	if len(h.fake.commands) != baseline {
		t.Fatalf("immutable target replacement invoked herdr: before=%d after=%d", baseline, len(h.fake.commands))
	}
}

func TestBoundOwnedBackendUses075PaneTargetedPrimitives(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	h.fake.respond = func(args []string) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"pane", "read", "w2:p1", "--source", "recent-unwrapped", "--lines", "2", "--format", "text"}):
			return []byte("one\ntwo\n"), nil
		case slices.Equal(args, []string{"agent", "prompt", "w2:p1", "hello"}):
			return nil, nil
		case slices.Equal(args, []string{"workspace", "focus", "w2"}):
			h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
				for i := range *snapshot.Workspaces {
					focused := (*snapshot.Workspaces)[i].WorkspaceID == "w2"
					(*snapshot.Workspaces)[i].Focused = &focused
				}
			})
			return nil, nil
		case slices.Equal(args, []string{"pane", "close", "w2:p1"}):
			h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
				panes := slices.DeleteFunc(*snapshot.Panes, func(p paneJSON) bool { return p.PaneID == "w2:p1" })
				agents := slices.DeleteFunc(*snapshot.Agents, func(a agentJSON) bool { return a.PaneID == "w2:p1" })
				snapshot.Panes, snapshot.Agents = &panes, &agents
			})
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
	}
	bound, err := h.session.Backend().BindOwnedTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	content, err := bound.Read(target.Ref, 2)
	if err != nil || content != "one\ntwo\n" {
		t.Fatalf("Read() = %q, %v", content, err)
	}
	if err := bound.SendLine(target.Ref, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := bound.Focus(target.Ref); err != nil {
		t.Fatal(err)
	}
	if err := bound.Close(target.Ref); err != nil {
		t.Fatal(err)
	}
}

func TestBoundOwnedCloserClosesWorkspaceButRetainsCheckoutForManualReconciliation(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	request := h.closeRequest(target)
	h.fake.respond = func(args []string) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"workspace", "close", "w2"}):
			h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
				workspaces := slices.DeleteFunc(*snapshot.Workspaces, func(w workspaceJSON) bool { return w.WorkspaceID == "w2" })
				panes := slices.DeleteFunc(*snapshot.Panes, func(p paneJSON) bool { return p.WorkspaceID == "w2" })
				agents := slices.DeleteFunc(*snapshot.Agents, func(a agentJSON) bool { return a.WorkspaceID == "w2" })
				snapshot.Workspaces, snapshot.Panes, snapshot.Agents = &workspaces, &panes, &agents
			})
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected close args %v", args)
		}
	}
	bound, err := h.session.Backend().BindOwnedClose(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := bound.CloseOwned(corebackend.CloseRequest{Ref: target.Ref, WorktreePath: target.WorktreePath, ShellKey: target.TerminalID})
	if !errors.Is(err, ErrOwnedCheckoutRetained) || result.Status != corebackend.CloseFailed {
		t.Fatalf("CloseOwned() = %+v, %v", result, err)
	}
	if _, err := os.Stat(h.checkout); err != nil {
		t.Fatalf("retained checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.worktreeGitDir, worktreeOwnershipMarkerName)); err != nil {
		t.Fatalf("retained worktree marker: %v", err)
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
