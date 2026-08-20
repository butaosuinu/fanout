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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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

func TestNormalizeStatDeviceSupportsDarwinAndLinuxWidths(t *testing.T) {
	if got := normalizeStatDevice(int32(42)); got != 42 {
		t.Fatalf("normalizeStatDevice(int32) = %d, want 42", got)
	}
	if got := normalizeStatDevice(uint64(81)); got != 81 {
		t.Fatalf("normalizeStatDevice(uint64) = %d, want 81", got)
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
		pid: supervisorPID,
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
	_, commonIdentity, err := openCanonicalGitCommonDir(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	sessionName := naming.ManagedSessionName(commonIdentity.device, commonIdentity.inode)
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
	session := strings.Repeat("s", naming.MaxManagedSessionNameLength)
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

func TestOpenOwnedDoesNotCreateMissingOwnedLayout(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, "repo.git")
	if err := os.Mkdir(commonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeBase := filepath.Join(root, "runtime")
	if _, err := OpenOwned(context.Background(), OwnedOptions{
		GitCommonDir: commonDir, RuntimeBase: runtimeBase,
	}); err == nil {
		t.Fatal("OpenOwned() succeeded without an existing owner layout")
	}
	if _, err := os.Lstat(runtimeBase); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenOwned() created runtime layout: %v", err)
	}
}

func TestOpenOwnedReadoptsExistingOwnedSession(t *testing.T) {
	h := newOwnedHarness(t)
	observed := New(h.session.Session, h.session.SocketPath)
	observed.output = h.fake.output
	opened, err := openOwned(context.Background(), OwnedOptions{
		GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase,
	}, observed)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Session != h.session.Session || opened.SocketPath != h.session.SocketPath ||
		opened.GitCommonDir != h.session.GitCommonDir {
		t.Fatalf("opened session = %+v, want route from %+v", opened, h.session)
	}
	if opened.EmitterPath != opened.LauncherPath {
		t.Fatalf("opened current launcher route = %+v", opened)
	}
	panes, err := opened.LivePanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) == 0 {
		t.Fatal("opened session returned no live panes")
	}
}

func TestPhysicalRepositoryAliasesShareOwnedSessionName(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, "repo.git")
	if err := os.Mkdir(commonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "repository-alias")
	if err := os.Symlink(commonDir, alias); err != nil {
		t.Fatal(err)
	}
	_, directIdentity, err := openCanonicalGitCommonDir(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	_, aliasIdentity, err := openCanonicalGitCommonDir(alias)
	if err != nil {
		t.Fatal(err)
	}
	direct := naming.ManagedSessionName(directIdentity.device, directIdentity.inode)
	aliased := naming.ManagedSessionName(aliasIdentity.device, aliasIdentity.inode)
	if direct != aliased {
		t.Fatalf("same repository aliases selected %q and %q", direct, aliased)
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
	return ensureOwned(
		context.Background(),
		OwnedOptions{GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase},
		h.backend(),
		h.supervisor.start,
	)
}

func (h *ownedHarness) backend() *Backend {
	h.t.Helper()
	b := New(h.layout.runtimeDir[strings.LastIndex(h.layout.runtimeDir, string(os.PathSeparator))+1:], h.layout.socketPath)
	b.lookPath = func(string) (string, error) { return h.binary, nil }
	b.output = h.fake.output
	return b
}

func (h *ownedHarness) target() corebackend.OwnedPaneIdentity {
	h.t.Helper()
	panes, err := h.session.Backend().ListLive()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, pane := range panes {
		if pane.Ref.Pane == "w2:p1" {
			return corebackend.OwnedPaneIdentity{
				Ref: pane.Ref, SessionID: pane.SessionID, SocketPath: pane.SocketPath,
				WorkspaceLabel: h.nonce, TerminalID: pane.TerminalID, RepoKey: pane.RepoKey,
				WorktreePath: pane.WorktreePath, CurrentPath: pane.CurrentPath,
				AgentID: pane.AgentID, AgentSession: cloneAgentSession(pane.AgentSession),
			}
		}
	}
	h.t.Fatal("owned child pane not found")
	return corebackend.OwnedPaneIdentity{}
}

func (h *ownedHarness) closeRequest(target corebackend.OwnedPaneIdentity) OwnedCloseRequest {
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

func agentPromptResponse(target corebackend.OwnedPaneIdentity, mutate func(*agentJSON)) []byte {
	focused := false
	revision := uint64(3)
	name := target.AgentID
	agentName := ""
	var session *agentSessionJSON
	if target.AgentSession != nil {
		agentName = target.AgentSession.Agent
		source := target.AgentSession.Source
		kind := target.AgentSession.Kind
		value := target.AgentSession.Value
		session = &agentSessionJSON{Source: &source, Agent: &agentName, Kind: &kind, Value: &value}
	}
	agent := agentJSON{
		TerminalID: target.TerminalID, Name: &name, Agent: &agentName, AgentStatus: "working",
		WorkspaceID: target.Ref.Workspace, TabID: "w2:t1", PaneID: target.Ref.Pane,
		Focused: &focused, Revision: &revision,
		AgentSession: session,
	}
	if mutate != nil {
		mutate(&agent)
	}
	data, err := json.Marshal(agentPromptEnvelope{
		ID: "cli:agent:prompt", Result: &agentPromptResult{Type: "agent_prompted", Agent: agent},
	})
	if err != nil {
		panic(err)
	}
	return data
}

func TestOwnedSessionNudgeAllowsUnreportedAgentSession(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	target.AgentSession = nil
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"agent", "prompt", target.Ref.Pane, "nudge"}) {
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
		return agentPromptResponse(target, nil), nil
	}
	nudgeTarget := corebackend.NudgeTarget{
		Ref: target.Ref, SessionID: target.SessionID, SocketPath: target.SocketPath,
		TerminalID: target.TerminalID, AgentID: target.AgentID,
	}
	if err := h.session.Nudge(context.Background(), nudgeTarget, "nudge"); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedNudgeIssuesOnlyPromptAfterPreparation(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"agent", "prompt", target.Ref.Pane, "nudge"}) {
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
		return agentPromptResponse(target, nil), nil
	}
	nudgeTarget := corebackend.NudgeTarget{
		Ref: target.Ref, SessionID: target.SessionID, SocketPath: target.SocketPath,
		TerminalID: target.TerminalID, AgentID: target.AgentID, AgentSession: target.AgentSession,
	}
	beforePreparation := len(h.fake.commands)
	prompt, err := h.session.PrepareNudge(context.Background(), nudgeTarget, "nudge")
	if err != nil {
		t.Fatal(err)
	}
	preparedCommands := len(h.fake.commands)
	preflight := h.fake.commands[beforePreparation:preparedCommands]
	if len(preflight) == 0 {
		t.Fatal("PrepareNudge() issued no ownership preflight")
	}
	for _, command := range preflight {
		if key := commandKey(command.args); key != "version" && key != "status" {
			t.Fatalf("PrepareNudge() command = %v, want only version/status preflight", command.args)
		}
	}
	if err := prompt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.commands[preparedCommands:]; len(got) != 1 ||
		!slices.Equal(got[0].args, []string{"agent", "prompt", target.Ref.Pane, "nudge"}) {
		t.Fatalf("commands after final gate = %v, want one agent prompt", got)
	}
}

func TestEnsureOwnedCreatesAndIdempotentlyReadoptsSession(t *testing.T) {
	h := newOwnedHarness(t)
	first := h.session
	second := h.ensure()
	third, err := openOwned(context.Background(), OwnedOptions{
		GitCommonDir: h.commonDir,
		RuntimeBase:  h.runtimeBase,
	}, h.backend())
	if err != nil {
		t.Fatal(err)
	}
	if h.supervisor.starts != 1 {
		t.Fatalf("supervisor starts = %d, want 1", h.supervisor.starts)
	}
	if first.Session != second.Session || first.SocketPath != second.SocketPath || first.ClientSocketPath != second.ClientSocketPath {
		t.Fatalf("re-adopted session differs: first=%+v second=%+v", first, second)
	}
	if third.Session != first.Session || third.SocketPath != first.SocketPath {
		t.Fatalf("lifecycle re-adopted session differs: first=%+v third=%+v", first, third)
	}
	command, _, err := second.AttachForms(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, "session attach") || !strings.Contains(command, socketEnv+"='") || !strings.Contains(command, h.layout.binaryDir) {
		t.Fatalf("AttachForms() command = %q", command)
	}
}

func TestAttachFormsBuildBothLanesFromOneAdmission(t *testing.T) {
	h := newOwnedHarness(t)
	command, spec, err := h.session.AttachForms([]string{
		"PATH=/usr/bin",
		sessionEnv + "=stale-session",
		"HERDR_STRAY=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.Path, h.layout.binaryDir) || !slices.Equal(spec.Argv, []string{spec.Path}) {
		t.Fatalf("AttachForms() image = %+v", spec)
	}
	if slices.Contains(spec.Env, sessionEnv+"=stale-session") || slices.Contains(spec.Env, "HERDR_STRAY=1") {
		t.Fatalf("AttachForms() kept stale caller routing: %v", spec.Env)
	}
	if len(spec.Env) == 0 || spec.Env[0] != "PATH=/usr/bin" {
		t.Fatalf("AttachForms() dropped the caller environment: %v", spec.Env)
	}
	// Both lanes come from the same verified marker: every owned routing value
	// in the exec image must appear, shell-quoted, in the printed command.
	for _, entry := range spec.Env[1:] {
		name, value, _ := strings.Cut(entry, "=")
		if !strings.Contains(command, name+"="+shellQuote(value)) {
			t.Fatalf("attach command %q is missing exec value %q", command, entry)
		}
	}
	if !strings.HasSuffix(command, shellQuote(spec.Path)) {
		t.Fatalf("attach command %q does not end with the exec binary %q", command, spec.Path)
	}
}

func TestMergeAttachEnvironment(t *testing.T) {
	assignments := [][2]string{{"HERDR_SESSION", "owned"}, {"XDG_CONFIG_HOME", "/xdg"}}
	owned := []string{"HERDR_SESSION=owned", "XDG_CONFIG_HOME=/xdg"}
	tests := []struct {
		name string
		base []string
		want []string
	}{
		{
			name: "appends owned routing after preserved caller entries",
			base: []string{"PATH=/usr/bin", "TERM=xterm"},
			want: append([]string{"PATH=/usr/bin", "TERM=xterm"}, owned...),
		},
		{
			name: "replaces same-named caller entries with owned values",
			base: []string{"XDG_CONFIG_HOME=/caller", "PATH=/usr/bin"},
			want: append([]string{"PATH=/usr/bin"}, owned...),
		},
		{
			name: "drops stray caller HERDR_ names outside the assignments",
			base: []string{"HERDR_ENV=1", "PATH=/usr/bin"},
			want: append([]string{"PATH=/usr/bin"}, owned...),
		},
		{
			// execve delivers entries without '=' verbatim; the exec image
			// passes them through the same way pasting the command would.
			name: "keeps malformed caller entries verbatim",
			base: []string{"ODDBALL", "PATH=/usr/bin"},
			want: append([]string{"ODDBALL", "PATH=/usr/bin"}, owned...),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeAttachEnvironment(tt.base, assignments); !slices.Equal(got, tt.want) {
				t.Fatalf("mergeAttachEnvironment(%v) = %v, want %v", tt.base, got, tt.want)
			}
		})
	}
}

func TestOpenOwnedMissingSessionIsReadOnly(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, "repo.git")
	if err := os.Mkdir(commonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	nonce, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	runtimeBase := filepath.Join("/tmp", "fanout-open-owned-"+nonce[:12])
	t.Cleanup(func() {
		_ = os.RemoveAll(runtimeBase) // Clean up only if a failed read-only admission created it.
	})
	_, err = OpenOwned(context.Background(), OwnedOptions{
		GitCommonDir: commonDir,
		RuntimeBase:  runtimeBase,
	})
	if !errors.Is(err, corebackend.ErrOwnedSessionNotFound) {
		t.Fatalf("OpenOwned() error = %v, want corebackend.ErrOwnedSessionNotFound", err)
	}
	if _, statErr := os.Lstat(runtimeBase); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("OpenOwned() created runtime path: %v", statErr)
	}
}

func TestReopenedOwnedBackendAdmitsPinnedBinary(t *testing.T) {
	h := newOwnedHarness(t)
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %t, %v", marker, found, err)
	}
	admitted := binaryAdmission{
		path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
	}
	backend := newReopenedOwnedBackend(h.layout, marker, admitted, nil)
	backend.output = h.fake.output
	probed, err := backend.probeOwned(context.Background(), *backend.owner)
	if err != nil {
		t.Fatal(err)
	}
	if probed.binary != marker.BinaryPath || probed.sha256 != marker.BinarySHA256 {
		t.Fatalf("reopened admission = %+v, want marker binary identity", probed)
	}
}

func TestEnsureOwnedReadoptsPinnedLauncherAfterFanoutUpdate(t *testing.T) {
	h := newOwnedHarness(t)
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", marker, found, err)
	}
	legacySource := filepath.Join(h.root, "legacy-fanout")
	err = os.WriteFile(legacySource, []byte("legacy fanout launcher\n"), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath, legacyHash, err := stageExecutable(legacySource, h.layout.launcherDir)
	if err != nil {
		t.Fatal(err)
	}
	marker.LauncherPath, marker.LauncherSHA256 = legacyPath, legacyHash
	if removeErr := os.Remove(h.layout.markerPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	if markerErr := writeOwnerMarkerExclusive(h.layout.markerPath, marker); markerErr != nil {
		t.Fatal(markerErr)
	}
	if removeErr := os.Remove(h.layout.configPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	if configErr := ensureOwnedConfig(h.layout, legacyPath); configErr != nil {
		t.Fatal(configErr)
	}

	reused := h.ensure()
	if reused.LauncherPath != legacyPath || h.supervisor.starts != 1 {
		t.Fatalf("re-adopted launcher = %q, starts=%d, want %q and one start", reused.LauncherPath, h.supervisor.starts, legacyPath)
	}
	if reused.EmitterPath == legacyPath || reused.EmitterPath == "" {
		t.Fatalf("re-adopted emitter = %q, want current content-addressed fanout", reused.EmitterPath)
	}
	observed := New(reused.Session, reused.SocketPath)
	observed.output = h.fake.output
	opened, err := openOwned(context.Background(), OwnedOptions{
		GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase,
	}, observed)
	if err != nil {
		t.Fatal(err)
	}
	if opened.EmitterPath == "" || opened.EmitterPath == opened.LauncherPath {
		t.Fatalf("opened route = %+v, want read-only current-launcher mismatch", opened)
	}
}

func TestOwnedReadinessRequiresPrivateServerAndClientSockets(t *testing.T) {
	tests := []struct {
		name string
		path func(ownedLayout) string
	}{
		{name: "server", path: func(layout ownedLayout) string { return layout.socketPath }},
		{name: "client", path: func(layout ownedLayout) string { return layout.clientSocketPath }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedHarness(t)
			path := test.path(h.layout)
			if err := os.Chmod(path, 0o660); err != nil {
				t.Fatal(err)
			}
			err := validateOwnedReady(context.Background(), h.session.Backend())
			if err == nil || !strings.Contains(err.Error(), "not an owner-only Unix socket") {
				t.Fatalf("validateOwnedReady() error = %v", err)
			}
		})
	}
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

func TestValidatePublishedPinnedBinaryWaitsForConcurrentLinkToSettle(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\nexit 0\n")
	hash := sha256.Sum256(content)
	digest := hex.EncodeToString(hash[:])
	target := filepath.Join(root, "herdr-"+digest)
	if err := os.WriteFile(target, content, 0o500); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(root, ".herdr-stage-concurrent.tmp")
	if err := os.Link(target, temporary); err != nil {
		t.Fatal(err)
	}

	waits := 0
	err := validatePublishedPinnedBinaryWithWait(target, digest, root, func(delay time.Duration) {
		waits++
		if delay != concurrentStageValidationRetryDelay {
			t.Errorf("retry delay = %v, want %v", delay, concurrentStageValidationRetryDelay)
		}
		if err := os.Remove(temporary); err != nil {
			t.Errorf("remove concurrent stage link: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if waits != 1 {
		t.Fatalf("retry waits = %d, want 1", waits)
	}
}

func TestValidatePublishedPinnedBinaryRejectsPersistentExtraLink(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\nexit 0\n")
	hash := sha256.Sum256(content)
	digest := hex.EncodeToString(hash[:])
	target := filepath.Join(root, "herdr-"+digest)
	if err := os.WriteFile(target, content, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(root, ".herdr-stage-stale.tmp")); err != nil {
		t.Fatal(err)
	}

	waits := 0
	err := validatePublishedPinnedBinaryWithWait(target, digest, root, func(time.Duration) {
		waits++
	})
	if !errors.Is(err, errPinnedBinaryPhysicalIdentity) {
		t.Fatalf("persistent extra link error = %v", err)
	}
	if waits != concurrentStageValidationAttempts-1 {
		t.Fatalf("retry waits = %d, want %d", waits, concurrentStageValidationAttempts-1)
	}
}

func TestAdmissionSourceOwnerPolicy(t *testing.T) {
	tests := []struct {
		name       string
		ownerUID   int
		currentUID int
		mode       os.FileMode
		want       bool
	}{
		{name: "current user", ownerUID: 501, currentUID: 501, mode: 0o700, want: true},
		{name: "current user group writable", ownerUID: 501, currentUID: 501, mode: 0o770, want: false},
		{name: "current user world writable", ownerUID: 501, currentUID: 501, mode: 0o707, want: false},
		{name: "root installed", ownerUID: 0, currentUID: 501, mode: 0o755, want: true},
		{name: "root group writable", ownerUID: 0, currentUID: 501, mode: 0o775, want: false},
		{name: "other user", ownerUID: 502, currentUID: 501, mode: 0o755, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTrustedAdmissionSourceOwner(tt.ownerUID, tt.currentUID, tt.mode)
			if got != tt.want {
				t.Fatalf("admission source policy = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestOwnerOnlySocketModes(t *testing.T) {
	for _, tt := range []struct {
		mode os.FileMode
		want bool
	}{
		{mode: 0o600, want: true},
		{mode: 0o700, want: true},
		{mode: 0o660, want: false},
		{mode: 0o707, want: false},
	} {
		if got := isOwnerOnlySocketMode(tt.mode); got != tt.want {
			t.Errorf("isOwnerOnlySocketMode(%#o) = %t, want %t", tt.mode, got, tt.want)
		}
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
	previous, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", previous, found, err)
	}
	displaced := h.commonDir + "-displaced"
	renameErr := os.Rename(h.commonDir, displaced)
	if renameErr != nil {
		t.Fatal(renameErr)
	}
	mkdirErr := os.Mkdir(h.commonDir, 0o700)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	_, replacementIdentity, err := openCanonicalGitCommonDir(h.commonDir)
	if err != nil {
		t.Fatal(err)
	}
	replacementSession := naming.ManagedSessionName(replacementIdentity.device, replacementIdentity.inode)
	if replacementSession == previous.Session {
		t.Fatalf("recreated repository reused owned session %q", replacementSession)
	}
	current, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found || current != previous {
		t.Fatalf("old repository marker changed = %+v, %v, %v", current, found, err)
	}
}

func TestOwnedBindingsRejectForeignRouteAndImmutableTargetReplacement(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	closeRequest := h.closeRequest(target)
	baseline := len(h.fake.commands)
	foreign := target
	foreign.SocketPath = filepath.Join(h.root, "foreign.sock")
	if _, err := h.session.Backend().BindOwnedTarget(foreign); !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
		t.Fatalf("BindOwnedTarget(foreign) error = %v", err)
	}
	foreignClose := closeRequest
	foreignClose.Target = foreign
	if _, err := h.session.Backend().BindOwnedClose(foreignClose); !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
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
		if callErr := call(); !errors.Is(callErr, corebackend.ErrOwnedIdentityMismatch) {
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
		case slices.Equal(args, []string{"pane", "read", "w2:p1", "--source", "visible", "--format", "text"}):
			return []byte("current viewport\n"), nil
		case slices.Equal(args, []string{"agent", "prompt", "w2:p1", "hello"}):
			return agentPromptResponse(target, nil), nil
		case slices.Equal(args, []string{"agent", "prompt", "w2:p1", "nudge"}):
			return agentPromptResponse(target, nil), nil
		case slices.Equal(args, []string{"agent", "focus", target.Ref.Pane}):
			h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
				for i := range *snapshot.Panes {
					focused := (*snapshot.Panes)[i].PaneID == target.Ref.Pane
					(*snapshot.Panes)[i].Focused = &focused
				}
				for i := range *snapshot.Agents {
					focused := (*snapshot.Agents)[i].PaneID == target.Ref.Pane
					(*snapshot.Agents)[i].Focused = &focused
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
	content, err = h.session.ReadOwnedPane(context.Background(), target, 0)
	if err != nil || content != "current viewport\n" {
		t.Fatalf("ReadOwnedPane(visible) = %q, %v", content, err)
	}
	if err := bound.SendLine(target.Ref, "hello"); err != nil {
		t.Fatal(err)
	}
	nudgeTarget := corebackend.NudgeTarget{
		Ref: target.Ref, SessionID: target.SessionID, SocketPath: target.SocketPath,
		TerminalID: target.TerminalID, AgentID: target.AgentID, AgentSession: target.AgentSession,
	}
	if err := h.session.Nudge(context.Background(), nudgeTarget, "nudge"); err != nil {
		t.Fatal(err)
	}
	if err := bound.Focus(target.Ref); err != nil {
		t.Fatal(err)
	}
	if err := bound.Close(target.Ref); err != nil {
		t.Fatal(err)
	}
}

func TestBoundOwnedBackendReportsGenericUnavailableMethodErrors(t *testing.T) {
	tests := []struct {
		name   string
		method string
		call   func(*Backend, corebackend.OwnedPaneIdentity) error
	}{
		{
			name: "read", method: "pane.read",
			call: func(bound *Backend, target corebackend.OwnedPaneIdentity) error {
				_, err := bound.Read(target.Ref, 1)
				return err
			},
		},
		{name: "send", method: "agent.prompt", call: func(bound *Backend, target corebackend.OwnedPaneIdentity) error {
			return bound.SendLine(target.Ref, "hello")
		}},
		{name: "focus", method: "agent.focus", call: func(bound *Backend, target corebackend.OwnedPaneIdentity) error {
			return bound.Focus(target.Ref)
		}},
		{name: "close", method: "pane.close", call: func(bound *Backend, target corebackend.OwnedPaneIdentity) error {
			return bound.Close(target.Ref)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedHarness(t)
			target := h.target()
			bound, err := h.session.Backend().BindOwnedTarget(target)
			if err != nil {
				t.Fatal(err)
			}
			h.fake.respond = func([]string) ([]byte, error) {
				return nil, errors.New("unknown command")
			}
			if err := test.call(bound, target); err == nil || err.Error() != methodUnavailable(test.method).Error() {
				t.Fatalf("%s error = %v", test.name, err)
			}
		})
	}
}

func TestBoundOwnedBackendRejectsAgentFocusWithoutTargetPaneFocus(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"agent", "focus", target.Ref.Pane}) {
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
		return nil, nil
	}
	bound, err := h.session.Backend().BindOwnedTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := bound.Focus(target.Ref); !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
		t.Fatalf("Focus() unfocused target error = %v", err)
	}
}

func TestBoundOwnedBackendFocusesWorkspaceWithoutLiveAgentIdentity(t *testing.T) {
	h := newOwnedHarness(t)
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Panes {
			if (*snapshot.Panes)[i].PaneID == "w2:p1" {
				(*snapshot.Panes)[i].AgentSession = nil
			}
		}
		agents := slices.DeleteFunc(*snapshot.Agents, func(agent agentJSON) bool { return agent.PaneID == "w2:p1" })
		snapshot.Agents = &agents
	})
	target := h.target()
	bound, err := h.session.Backend().BindOwnedTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"workspace", "focus", target.Ref.Workspace}) {
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
		h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
			for i := range *snapshot.Panes {
				focused := (*snapshot.Panes)[i].PaneID == target.Ref.Pane
				(*snapshot.Panes)[i].Focused = &focused
			}
		})
		return nil, nil
	}
	if err := bound.Focus(target.Ref); err != nil {
		t.Fatalf("Focus() without live agent error = %v", err)
	}
}

func TestBoundOwnedBackendRejectsMismatchedAgentPromptResponse(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"agent", "prompt", "w2:p1", "hello"}) {
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
		return agentPromptResponse(target, func(agent *agentJSON) { agent.PaneID = "w2:p9" }), nil
	}
	bound, err := h.session.Backend().BindOwnedTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := bound.SendLine(target.Ref, "hello"); err == nil || err.Error() != methodUnavailable("agent.prompt").Error() {
		t.Fatalf("SendLine() mismatched prompt response error = %v", err)
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
	if !errors.Is(err, corebackend.ErrOwnedCheckoutRetained) || result.Status != corebackend.CloseFailed {
		t.Fatalf("CloseOwned() = %+v, %v", result, err)
	}
	if _, err := os.Stat(h.checkout); err != nil {
		t.Fatalf("retained checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.worktreeGitDir, worktreeOwnershipMarkerName)); err != nil {
		t.Fatalf("retained worktree marker: %v", err)
	}
}

func TestBoundOwnedWorkspaceCloserClosesExactGenericWorkspace(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	target.RepoKey = ""
	target.WorktreePath = ""
	target.CurrentPath = "/wrong-saved-cwd"
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Workspaces {
			if (*snapshot.Workspaces)[i].WorkspaceID == target.Ref.Workspace {
				(*snapshot.Workspaces)[i].Worktree = nil
			}
		}
	})
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"workspace", "close", target.Ref.Workspace}) {
			return nil, fmt.Errorf("unexpected close args %v", args)
		}
		h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
			workspaces := slices.DeleteFunc(*snapshot.Workspaces, func(w workspaceJSON) bool {
				return w.WorkspaceID == target.Ref.Workspace
			})
			panes := slices.DeleteFunc(*snapshot.Panes, func(p paneJSON) bool {
				return p.WorkspaceID == target.Ref.Workspace
			})
			agents := slices.DeleteFunc(*snapshot.Agents, func(a agentJSON) bool {
				return a.WorkspaceID == target.Ref.Workspace
			})
			snapshot.Workspaces, snapshot.Panes, snapshot.Agents = &workspaces, &panes, &agents
		})
		return nil, nil
	}
	bound, err := h.session.Backend().BindOwnedWorkspaceClose(target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := bound.CloseOwned(corebackend.CloseRequest{Ref: corebackend.PaneRef{
		Backend: corebackend.Herdr,
		Pane:    target.Ref.Pane,
	}})
	if err != nil || result.Status != corebackend.CloseConfirmed {
		t.Fatalf("CloseOwned() = %+v, %v", result, err)
	}
}

func TestBoundOwnedWorkspaceCloserRejectsWorktreeTarget(t *testing.T) {
	h := newOwnedHarness(t)
	_, err := h.session.Backend().BindOwnedWorkspaceClose(h.target())
	if !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
		t.Fatalf("BindOwnedWorkspaceClose() error = %v", err)
	}
}

func TestBoundOwnedCloserReportsGenericUnavailableMethodError(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	bound, err := h.session.Backend().BindOwnedClose(h.closeRequest(target))
	if err != nil {
		t.Fatal(err)
	}
	h.fake.respond = func([]string) ([]byte, error) {
		return nil, errors.New("unknown command")
	}
	_, err = bound.CloseOwned(corebackend.CloseRequest{Ref: target.Ref, WorktreePath: target.WorktreePath, ShellKey: target.TerminalID})
	if err == nil || err.Error() != methodUnavailable("workspace.close").Error() {
		t.Fatalf("CloseOwned() error = %v", err)
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
