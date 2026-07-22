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

func (s *fakeOwnedSupervisor) start(markerPath, nonce, startToken string) (int, error) {
	s.starts++
	runtimeDir := filepath.Dir(markerPath)
	lock, err := os.OpenFile(filepath.Join(runtimeDir, ownedSupervisorLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return 0, err
	}
	lease := supervisorLease{SchemaID: ownedMarkerSchemaID, OwnerNonce: nonce, StartToken: startToken, PID: os.Getpid()}
	data, err := json.Marshal(lease)
	if err != nil {
		return 0, err
	}
	if _, err := lock.WriteAt(data, 0); err != nil {
		return 0, err
	}
	if err := lock.Sync(); err != nil {
		return 0, err
	}
	s.lock = lock
	for _, path := range []string{filepath.Join(runtimeDir, "herdr.sock"), filepath.Join(runtimeDir, "herdr-client.sock")} {
		listener, err := net.Listen("unix", path)
		if err != nil {
			return 0, err
		}
		if err := os.Chmod(path, 0o600); err != nil {
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
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	commonDir := filepath.Join(root, "repo.git")
	if err := os.Mkdir(commonDir, 0o700); err != nil {
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

func (h *ownedHarness) ensure() *OwnedSession {
	h.t.Helper()
	b := New(h.layout.runtimeDir[strings.LastIndex(h.layout.runtimeDir, string(os.PathSeparator))+1:], h.layout.socketPath)
	b.lookPath = func(string) (string, error) { return h.binary, nil }
	b.output = h.fake.output
	b.helpOutput = func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		for _, surface := range requiredCommandSurfaces {
			if len(args) == len(surface.args)+1 && slices.Equal(args[:len(surface.args)], surface.args) {
				return []byte(strings.Join(surface.required, "\n")), nil
			}
		}
		return nil, fmt.Errorf("unexpected help args %v", args)
	}
	session, err := ensureOwned(context.Background(), OwnedOptions{GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase}, b, h.supervisor.start)
	if err != nil {
		h.t.Fatal(err)
	}
	return session
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
	if strings.Contains(command, "session attach") || !strings.Contains(command, socketEnv+"='") || !strings.Contains(command, h.binary) {
		t.Fatalf("AttachCommand() = %q", command)
	}
}

func TestOwnedOperationsRejectForeignRouteBeforeMutation(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	closeRequest := h.closeRequest(target)
	baseline := len(h.fake.commands)
	foreign := target
	foreign.SocketPath = filepath.Join(h.root, "foreign.sock")
	calls := []func() error{
		func() error {
			_, err := h.session.Backend().ReadOwned(context.Background(), ReadRequest{Target: foreign, Lines: 1})
			return err
		},
		func() error {
			return h.session.Backend().SendLineOwned(context.Background(), SendLineRequest{Target: foreign, Line: "hello"})
		},
		func() error {
			return h.session.Backend().FocusOwned(context.Background(), FocusRequest{Target: foreign})
		},
		func() error {
			return h.session.Backend().ClosePaneOwned(context.Background(), ClosePaneRequest{Target: foreign})
		},
		func() error {
			closeRequest.Target = foreign
			_, err := h.session.Backend().CloseOwnedSession(context.Background(), closeRequest)
			return err
		},
	}
	for i, call := range calls {
		if err := call(); !errors.Is(err, ErrOwnedIdentityMismatch) {
			t.Errorf("foreign operation %d error = %v", i, err)
		}
	}
	if len(h.fake.commands) != baseline {
		t.Fatalf("foreign operations invoked herdr: before=%d after=%d", baseline, len(h.fake.commands))
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

func TestBoundOwnedCloserRemovesWorktreeThenClosesResidualWorkspace(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	request := h.closeRequest(target)
	h.fake.respond = func(args []string) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"worktree", "remove", "--workspace", "w2", "--json"}):
			if err := os.RemoveAll(h.checkout); err != nil {
				return nil, err
			}
			h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
				for i := range *snapshot.Workspaces {
					if (*snapshot.Workspaces)[i].WorkspaceID == "w2" {
						(*snapshot.Workspaces)[i].Worktree = nil
					}
				}
			})
			return []byte(fmt.Sprintf(`{"id":"cli:worktree:remove","result":{"type":"worktree_removed","workspace_id":"w2","path":%s,"forced":false}}`, strconvQuote(h.checkout))), nil
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
	if err != nil || result.Status != corebackend.CloseConfirmed {
		t.Fatalf("CloseOwned() = %+v, %v", result, err)
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
