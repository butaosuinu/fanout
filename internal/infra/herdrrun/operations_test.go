package herdrrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type ownedOperationHarness struct {
	backend        *Backend
	fake           *fakeHerdr
	marker         ownerMarker
	calls          [][]string
	respond        func([]string) ([]byte, error)
	baseSnapshot   string
	snapshot       string
	repoKey        string
	repoRoot       string
	worktreePath   string
	worktreeGitDir string
	worktreeNonce  string
}

func newOwnedOperationHarness(t *testing.T) *ownedOperationHarness {
	t.Helper()
	const session = "fanout-operations-test"
	fixtureRoot := t.TempDir()
	repoRoot := filepath.Join(fixtureRoot, "repo")
	repoKey := filepath.Join(repoRoot, ".git")
	worktreePath := filepath.Join(fixtureRoot, "child")
	worktreeGitDir := filepath.Join(repoKey, "worktrees", "child")
	for _, path := range []string{repoKey, worktreePath, worktreeGitDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, path := range map[string]*string{
		"repository root":          &repoRoot,
		"repository git directory": &repoKey,
		"worktree":                 &worktreePath,
		"worktree git directory":   &worktreeGitDir,
	} {
		canonical, err := filepath.EvalSymlinks(*path)
		if err != nil {
			t.Fatalf("canonicalize %s: %v", name, err)
		}
		*path = filepath.Clean(canonical)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, ".git"), []byte("gitdir: "+worktreeGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeNonce := strings.Repeat("c", 64)
	worktreeMarker := worktreeOwnershipMarker{
		SchemaVersion: worktreeOwnershipMarkerSchema,
		Nonce:         worktreeNonce,
		WorkspaceID:   "w2",
		RepoKey:       repoKey,
		CheckoutPath:  worktreePath,
		GitDir:        worktreeGitDir,
	}
	worktreeMarkerData, err := json.Marshal(worktreeMarker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worktreeOwnershipMarkerPath(worktreeGitDir), append(worktreeMarkerData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	layout, layoutErr := prepareOwnedLayout(shortOwnedRuntimeBase(t), session)
	if layoutErr != nil {
		t.Fatal(layoutErr)
	}
	if prepareErr := ensureOwnedDirectories(layout); prepareErr != nil {
		t.Fatal(prepareErr)
	}

	binary := filepath.Join(layout.runtimeDir, "herdr-test-bin")
	if writeErr := os.WriteFile(binary, []byte("herdr operations test binary\n"), 0o700); writeErr != nil {
		t.Fatal(writeErr)
	}
	hash, hashErr := sha256File(binary)
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	token, tokenErr := randomToken()
	if tokenErr != nil {
		t.Fatal(tokenErr)
	}
	layout.socketPath = filepath.Join("/tmp", "fanout-herdr-ops-"+token[:12]+".sock")
	listener, listenErr := net.Listen("unix", layout.socketPath)
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(layout.socketPath)
	})
	if chmodErr := os.Chmod(layout.socketPath, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}

	admitted := binaryAdmission{path: binary, sha256: hash, version: minimumVersion, protocol: supportedProtocol}
	marker := markerFor(layout, repoKey, session, admitted, strings.Repeat("a", 64), os.Getpid(), strings.Repeat("b", 64))
	if markerErr := writeOwnerMarkerExclusive(layout.markerPath, marker); markerErr != nil {
		t.Fatal(markerErr)
	}
	supervisorLock, lockErr := lockPrivateFile(layout.supervisorLock)
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	if leaseErr := writeSupervisorLease(supervisorLock, marker); leaseErr != nil {
		unlockPrivateFile(supervisorLock)
		t.Fatal(leaseErr)
	}
	t.Cleanup(func() { unlockPrivateFile(supervisorLock) })

	fake := newFakeHerdr(session, layout.socketPath)
	backend := New(session, layout.socketPath)
	backend.lookPath = func(name string) (string, error) {
		if name != commandName {
			t.Fatalf("LookPath(%q), want %q", name, commandName)
		}
		return binary, nil
	}
	backend.hashFile = sha256File
	backend.control = &controlPlaneEnvironment{
		xdgConfigHome:    marker.XDGConfigHome,
		xdgStateHome:     marker.XDGStateHome,
		xdgDataHome:      marker.XDGDataHome,
		xdgCacheHome:     marker.XDGCacheHome,
		configPath:       marker.ConfigPath,
		clientSocketPath: marker.ClientSocketPath,
	}
	backend.owner = &ownedAdmission{marker: marker, markerPath: layout.markerPath, lockPath: layout.lifecycleLock}
	backend.admitted[binary+"\x00"+hash] = admitted

	baseSnapshot := mutateSnapshot(t, validSnapshot(), func(snapshot map[string]any) {
		for _, raw := range snapshot["workspaces"].([]any) {
			workspace := raw.(map[string]any)
			if workspace["workspace_id"] != "w2" {
				continue
			}
			workspace["label"] = worktreeNonce
			worktree := workspace["worktree"].(map[string]any)
			worktree["repo_key"] = repoKey
			worktree["repo_root"] = repoRoot
			worktree["checkout_path"] = worktreePath
		}
	})
	harness := &ownedOperationHarness{
		backend:        backend,
		fake:           fake,
		marker:         marker,
		baseSnapshot:   baseSnapshot,
		snapshot:       baseSnapshot,
		repoKey:        repoKey,
		repoRoot:       repoRoot,
		worktreePath:   worktreePath,
		worktreeGitDir: worktreeGitDir,
		worktreeNonce:  worktreeNonce,
	}
	backend.output = harness.output
	return harness
}

func (h *ownedOperationHarness) output(ctx context.Context, binary string, env []string, args ...string) (commandStreams, error) {
	if isOwnedOperationCommand(args) {
		h.calls = append(h.calls, slices.Clone(args))
		if h.respond == nil {
			return commandStreams{}, fmt.Errorf("unexpected owned operation: %v", args)
		}
		out, err := h.respond(args)
		return commandStreams{stdout: out}, err
	}
	h.fake.snapshot = h.snapshot
	return h.fake.output(ctx, binary, env, args...)
}

func isOwnedOperationCommand(args []string) bool {
	if len(args) < 2 || args[1] == "--help" {
		return false
	}
	switch args[0] {
	case "pane":
		return args[1] == "read" || args[1] == "run" || args[1] == "close"
	case "workspace":
		return args[1] == "focus" || args[1] == "close"
	case "worktree":
		return args[1] == "remove"
	default:
		return false
	}
}

func (h *ownedOperationHarness) childTarget() OwnedPaneIdentity {
	return OwnedPaneIdentity{
		Ref:            corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w2", Pane: "w2:p1"},
		SessionID:      h.marker.Session,
		SocketPath:     h.marker.SocketPath,
		WorkspaceLabel: h.worktreeNonce,
		TerminalID:     "term-child",
		RepoKey:        h.repoKey,
		WorktreePath:   h.worktreePath,
		CurrentPath:    h.worktreePath,
		AgentID:        "fanout-child",
		AgentSession:   &corebackend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-a"},
	}
}

func (h *ownedOperationHarness) ownedCloseRequest() OwnedCloseRequest {
	return h.ownedCloseRequestFor(h.childTarget())
}

func (h *ownedOperationHarness) ownedCloseRequestFor(target OwnedPaneIdentity) OwnedCloseRequest {
	return OwnedCloseRequest{
		Target:                 target,
		WorktreeOwnershipNonce: h.worktreeNonce,
		WorktreeGitDir:         h.worktreeGitDir,
	}
}

func (h *ownedOperationHarness) removeCheckout(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(h.worktreePath); err != nil {
		t.Fatal(err)
	}
}

func (h *ownedOperationHarness) writeWorktreeMarker(t *testing.T, marker worktreeOwnershipMarker, mode os.FileMode) {
	t.Helper()
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	path := worktreeOwnershipMarkerPath(h.worktreeGitDir)
	if err := os.WriteFile(path, append(data, '\n'), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func (h *ownedOperationHarness) writeRawWorktreeMarker(t *testing.T, data []byte, mode os.FileMode) {
	t.Helper()
	path := worktreeOwnershipMarkerPath(h.worktreeGitDir)
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func (h *ownedOperationHarness) worktreeMarker() worktreeOwnershipMarker {
	return worktreeOwnershipMarker{
		SchemaVersion: worktreeOwnershipMarkerSchema,
		Nonce:         h.worktreeNonce,
		WorkspaceID:   "w2",
		RepoKey:       h.repoKey,
		CheckoutPath:  h.worktreePath,
		GitDir:        h.worktreeGitDir,
	}
}

type ownedOperationTestCall struct {
	name string
	call func() error
}

func typedOwnedOperationTestCalls(h *ownedOperationHarness, target OwnedPaneIdentity) []ownedOperationTestCall {
	return []ownedOperationTestCall{
		{name: "read", call: func() error {
			_, err := h.backend.ReadOwned(context.Background(), ReadRequest{Target: target, Lines: 1})
			return err
		}},
		{name: "send", call: func() error {
			return h.backend.SendLineOwned(context.Background(), SendLineRequest{Target: target, Line: "hello"})
		}},
		{name: "focus", call: func() error {
			return h.backend.FocusOwned(context.Background(), FocusRequest{Target: target})
		}},
		{name: "close pane", call: func() error {
			return h.backend.ClosePaneOwned(context.Background(), ClosePaneRequest{Target: target})
		}},
		{name: "close owned", call: func() error {
			result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequestFor(target))
			if err != nil {
				return err
			}
			return fmt.Errorf("unexpected close status %d", result.Status)
		}},
	}
}

func TestOwnedOperationsRejectUnownedBackendBeforeCLI(t *testing.T) {
	b := New("fanout-unowned", "/tmp/unowned.sock")
	called := 0
	b.output = func(context.Context, string, []string, ...string) (commandStreams, error) {
		called++
		return commandStreams{}, nil
	}
	target := OwnedPaneIdentity{
		Ref:            corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		SessionID:      "fanout-unowned",
		SocketPath:     "/tmp/unowned.sock",
		WorkspaceLabel: "owner-nonce",
		TerminalID:     "term-1",
		RepoKey:        "/repo/.git",
		WorktreePath:   "/repo/wt",
	}

	checks := []struct {
		name string
		call func() error
	}{
		{name: "read", call: func() error {
			_, err := b.ReadOwned(context.Background(), ReadRequest{Target: target, Lines: 1})
			return err
		}},
		{name: "send", call: func() error {
			return b.SendLineOwned(context.Background(), SendLineRequest{Target: target, Line: "hello"})
		}},
		{name: "focus", call: func() error { return b.FocusOwned(context.Background(), FocusRequest{Target: target}) }},
		{name: "close pane", call: func() error { return b.ClosePaneOwned(context.Background(), ClosePaneRequest{Target: target}) }},
		{name: "close owned", call: func() error {
			_, err := b.CloseOwnedSession(context.Background(), OwnedCloseRequest{
				Target:                 target,
				WorktreeOwnershipNonce: strings.Repeat("c", 64),
				WorktreeGitDir:         "/repo/.git/worktrees/w1",
			})
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			err := check.call()
			if err == nil || !strings.Contains(err.Error(), "fanout-owned session") {
				t.Fatalf("error = %v, want owned-session rejection", err)
			}
		})
	}
	if called != 0 {
		t.Fatalf("CLI calls = %d, want 0", called)
	}
}

func TestOwnedOperationsRejectForeignRouteBeforeCLI(t *testing.T) {
	h := newOwnedOperationHarness(t)
	target := h.childTarget()
	target.SocketPath = "/tmp/foreign.sock"

	checks := []struct {
		name string
		call func() error
	}{
		{name: "read", call: func() error {
			_, err := h.backend.ReadOwned(context.Background(), ReadRequest{Target: target, Lines: 1})
			return err
		}},
		{name: "send", call: func() error {
			return h.backend.SendLineOwned(context.Background(), SendLineRequest{Target: target, Line: "hello"})
		}},
		{name: "focus", call: func() error { return h.backend.FocusOwned(context.Background(), FocusRequest{Target: target}) }},
		{name: "close pane", call: func() error { return h.backend.ClosePaneOwned(context.Background(), ClosePaneRequest{Target: target}) }},
		{name: "close owned", call: func() error {
			_, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequestFor(target))
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			err := check.call()
			if err == nil || !strings.Contains(err.Error(), "foreign session or socket") {
				t.Fatalf("error = %v, want foreign-route rejection", err)
			}
		})
	}
	if len(h.fake.commands) != 0 || len(h.calls) != 0 {
		t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
	}
}

func TestOwnedOperationsRequireSavedWorkspaceOwnershipBeforeCLI(t *testing.T) {
	for _, operation := range []string{"read", "send", "focus", "close pane", "close owned"} {
		t.Run(operation, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			target := h.childTarget()
			target.WorkspaceLabel = ""
			for _, check := range typedOwnedOperationTestCalls(h, target) {
				if check.name != operation {
					continue
				}
				err := check.call()
				if err == nil || !strings.Contains(err.Error(), "saved workspace ownership label") {
					t.Fatalf("error = %v, want missing workspace ownership rejection", err)
				}
			}
			if len(h.fake.commands) != 0 || len(h.calls) != 0 {
				t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
			}
		})
	}
}

func TestOwnedOperationsRejectForeignRepositoryBeforeCLI(t *testing.T) {
	for _, operation := range []string{"read", "send", "focus", "close pane", "close owned"} {
		t.Run(operation, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			target := h.childTarget()
			target.RepoKey = "/other/.git"
			for _, check := range typedOwnedOperationTestCalls(h, target) {
				if check.name != operation {
					continue
				}
				err := check.call()
				if err == nil || !strings.Contains(err.Error(), "foreign repository provenance") {
					t.Fatalf("error = %v, want foreign repository rejection", err)
				}
			}
			if len(h.fake.commands) != 0 || len(h.calls) != 0 {
				t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
			}
		})
	}
}

func TestOwnedOperationsRejectChangedWorkspaceOwnershipBeforeMutation(t *testing.T) {
	for _, operation := range []string{"read", "send", "focus", "close pane", "close owned"} {
		t.Run(operation, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			h.snapshot = mutateSnapshot(t, h.baseSnapshot, func(snapshot map[string]any) {
				for _, workspace := range snapshot["workspaces"].([]any) {
					item := workspace.(map[string]any)
					if item["workspace_id"] == "w2" {
						item["label"] = "foreign-owner"
					}
				}
			})
			for _, check := range typedOwnedOperationTestCalls(h, h.childTarget()) {
				if check.name != operation {
					continue
				}
				if err := check.call(); !errors.Is(err, ErrOwnedIdentityMismatch) {
					t.Fatalf("error = %v, want workspace ownership mismatch", err)
				}
			}
			if len(h.calls) != 0 {
				t.Fatalf("mutation calls = %v, want none", h.calls)
			}
		})
	}
}

func TestReadOwnedChecksIdentityBeforeAndAfter(t *testing.T) {
	h := newOwnedOperationHarness(t)
	h.respond = func(args []string) ([]byte, error) {
		want := []string{"pane", "read", "w2:p1", "--source", "recent-unwrapped", "--lines", "40", "--format", "text"}
		if !slices.Equal(args, want) {
			t.Fatalf("pane read args = %v, want %v", args, want)
		}
		return []byte("private pane content"), nil
	}
	h.fake.snapshotResults = []fakeSnapshotResult{
		{output: h.baseSnapshot},
		{output: mutateSnapshot(t, h.baseSnapshot, func(snapshot map[string]any) {
			for _, pane := range snapshot["panes"].([]any) {
				item := pane.(map[string]any)
				if item["pane_id"] == "w2:p1" {
					item["terminal_id"] = "term-reused"
				}
			}
			for _, agent := range snapshot["agents"].([]any) {
				item := agent.(map[string]any)
				if item["pane_id"] == "w2:p1" {
					item["terminal_id"] = "term-reused"
				}
			}
		})},
	}

	got, err := h.backend.ReadOwned(context.Background(), ReadRequest{Target: h.childTarget(), Lines: 40})
	if !errors.Is(err, ErrOwnedIdentityMismatch) {
		t.Fatalf("ReadOwned error = %v, want identity mismatch", err)
	}
	if got != "" {
		t.Fatalf("ReadOwned content = %q, want discarded", got)
	}
	if len(h.calls) != 1 {
		t.Fatalf("pane read calls = %d, want 1", len(h.calls))
	}
}

func TestReadOwnedDiscardsContentWhenWorkspaceOwnershipChanges(t *testing.T) {
	h := newOwnedOperationHarness(t)
	h.respond = func([]string) ([]byte, error) { return []byte("private pane content"), nil }
	h.fake.snapshotResults = []fakeSnapshotResult{
		{output: h.baseSnapshot},
		{output: mutateSnapshot(t, h.baseSnapshot, func(snapshot map[string]any) {
			for _, workspace := range snapshot["workspaces"].([]any) {
				item := workspace.(map[string]any)
				if item["workspace_id"] == "w2" {
					item["label"] = "replacement-owner"
				}
			}
		})},
	}

	got, err := h.backend.ReadOwned(context.Background(), ReadRequest{Target: h.childTarget(), Lines: 1})
	if !errors.Is(err, ErrOwnedIdentityMismatch) {
		t.Fatalf("ReadOwned error = %v, want workspace ownership mismatch", err)
	}
	if got != "" {
		t.Fatalf("ReadOwned content = %q, want discarded", got)
	}
	if len(h.calls) != 1 {
		t.Fatalf("pane read calls = %d, want 1", len(h.calls))
	}
}

func TestOwnedOperationsRejectStalePreIdentityWithoutMutation(t *testing.T) {
	tests := []struct {
		name string
		call func(*ownedOperationHarness, OwnedPaneIdentity) error
	}{
		{name: "read", call: func(h *ownedOperationHarness, target OwnedPaneIdentity) error {
			_, err := h.backend.ReadOwned(context.Background(), ReadRequest{Target: target, Lines: 1})
			return err
		}},
		{name: "send", call: func(h *ownedOperationHarness, target OwnedPaneIdentity) error {
			return h.backend.SendLineOwned(context.Background(), SendLineRequest{Target: target, Line: "hello"})
		}},
		{name: "focus", call: func(h *ownedOperationHarness, target OwnedPaneIdentity) error {
			return h.backend.FocusOwned(context.Background(), FocusRequest{Target: target})
		}},
		{name: "close pane", call: func(h *ownedOperationHarness, target OwnedPaneIdentity) error {
			return h.backend.ClosePaneOwned(context.Background(), ClosePaneRequest{Target: target})
		}},
		{name: "close owned", call: func(h *ownedOperationHarness, target OwnedPaneIdentity) error {
			_, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequestFor(target))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			target := h.childTarget()
			target.TerminalID = "term-stale"
			err := test.call(h, target)
			if !errors.Is(err, ErrOwnedIdentityMismatch) {
				t.Fatalf("error = %v, want identity mismatch", err)
			}
			if len(h.calls) != 0 {
				t.Fatalf("mutation calls = %v, want none", h.calls)
			}
		})
	}
}

func TestSendAndFocusDiscardPostIdentityMismatch(t *testing.T) {
	tests := []struct {
		name     string
		response []byte
		call     func(*ownedOperationHarness) error
	}{
		{
			name:     "send",
			response: nil,
			call: func(h *ownedOperationHarness) error {
				return h.backend.SendLineOwned(context.Background(), SendLineRequest{Target: h.childTarget(), Line: "hello"})
			},
		},
		{
			name:     "focus",
			response: workspaceFocusedResponse("w2"),
			call: func(h *ownedOperationHarness) error {
				return h.backend.FocusOwned(context.Background(), FocusRequest{Target: h.childTarget()})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			h.respond = func([]string) ([]byte, error) { return test.response, nil }
			h.fake.snapshotResults = []fakeSnapshotResult{
				{output: h.baseSnapshot},
				{output: mutateSnapshot(t, h.baseSnapshot, func(snapshot map[string]any) {
					for _, pane := range snapshot["panes"].([]any) {
						item := pane.(map[string]any)
						if item["pane_id"] == "w2:p1" {
							item["terminal_id"] = "term-reused"
						}
					}
					for _, agent := range snapshot["agents"].([]any) {
						item := agent.(map[string]any)
						if item["pane_id"] == "w2:p1" {
							item["terminal_id"] = "term-reused"
						}
					}
				})},
			}
			if err := test.call(h); !errors.Is(err, ErrOwnedIdentityMismatch) {
				t.Fatalf("error = %v, want post-operation identity mismatch", err)
			}
			if len(h.calls) != 1 {
				t.Fatalf("mutation calls = %v, want one without retry", h.calls)
			}
		})
	}
}

func TestSendLineOwnedRejectsControlNewlinesBeforeCLI(t *testing.T) {
	h := newOwnedOperationHarness(t)
	for _, line := range []string{"one\ntwo", "one\rtwo", "one\x00two"} {
		err := h.backend.SendLineOwned(context.Background(), SendLineRequest{Target: h.childTarget(), Line: line})
		if err == nil || !strings.Contains(err.Error(), "NUL, CR, or LF") {
			t.Fatalf("SendLineOwned(%q) error = %v", line, err)
		}
	}
	if len(h.fake.commands) != 0 || len(h.calls) != 0 {
		t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
	}
}

func TestOwnedReadSendAndFocusUseVerifiedCommands(t *testing.T) {
	tests := []struct {
		name     string
		want     []string
		response []byte
		focus    bool
		call     func(*ownedOperationHarness) error
	}{
		{
			name:     "send",
			want:     []string{"pane", "run", "w2:p1", "hello literally"},
			response: nil,
			call: func(h *ownedOperationHarness) error {
				return h.backend.SendLineOwned(context.Background(), SendLineRequest{Target: h.childTarget(), Line: "hello literally"})
			},
		},
		{
			name:     "workspace focus",
			want:     []string{"workspace", "focus", "w2"},
			response: workspaceFocusedResponse("w2"),
			focus:    true,
			call: func(h *ownedOperationHarness) error {
				return h.backend.FocusOwned(context.Background(), FocusRequest{Target: h.childTarget()})
			},
		},
		{
			name:     "read",
			want:     []string{"pane", "read", "w2:p1", "--source", "visible", "--format", "text"},
			response: []byte("ok"),
			call: func(h *ownedOperationHarness) error {
				_, err := h.backend.ReadOwned(context.Background(), ReadRequest{Target: h.childTarget()})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			h.respond = func(args []string) ([]byte, error) {
				if !slices.Equal(args, test.want) {
					t.Fatalf("operation args = %v, want %v", args, test.want)
				}
				if test.focus {
					h.snapshot = snapshotWithFocusedWorkspace(t, h.baseSnapshot, "w2")
				}
				return test.response, nil
			}
			if err := test.call(h); err != nil {
				t.Fatal(err)
			}
			if len(h.calls) != 1 {
				t.Fatalf("operation calls = %d, want 1", len(h.calls))
			}
		})
	}
}

func TestClosePaneOwnedClosesOnlyPaneAndConfirmsAbsence(t *testing.T) {
	h := newOwnedOperationHarness(t)
	h.respond = func(args []string) ([]byte, error) {
		want := []string{"pane", "close", "w2:p1"}
		if !slices.Equal(args, want) {
			t.Fatalf("pane close args = %v, want %v", args, want)
		}
		return okOperationResponse("cli:pane:close"), nil
	}
	h.fake.snapshotResults = []fakeSnapshotResult{
		{output: h.baseSnapshot},
		{output: snapshotWithoutWorkspaceResources(t, h.baseSnapshot, "w2")},
	}

	if err := h.backend.ClosePaneOwned(context.Background(), ClosePaneRequest{Target: h.childTarget()}); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0][0] != "pane" {
		t.Fatalf("operation calls = %v, want pane close only", h.calls)
	}
}

func TestOwnedCloseRejectsDanglingSnapshotCollections(t *testing.T) {
	t.Run("pane close", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) { return okOperationResponse("cli:pane:close"), nil }
		h.fake.snapshotResults = []fakeSnapshotResult{
			{output: h.baseSnapshot},
			{output: snapshotWithDanglingWorkspaceResources(t, h.baseSnapshot, "w2")},
		}

		err := h.backend.ClosePaneOwned(context.Background(), ClosePaneRequest{Target: h.childTarget()})
		if err == nil || !strings.Contains(err.Error(), "references unknown workspace") {
			t.Fatalf("ClosePaneOwned() error = %v, want dangling snapshot rejection", err)
		}
		if len(h.calls) != 1 {
			t.Fatalf("mutation calls = %v, want one pane close", h.calls)
		}
	})

	t.Run("owned close", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func(args []string) ([]byte, error) {
			switch args[0] {
			case "worktree":
				h.removeCheckout(t)
				return worktreeRemovedResponse("w2", h.worktreePath, false), nil
			case "workspace":
				return okOperationResponse("cli:workspace:close"), nil
			default:
				return nil, fmt.Errorf("unexpected args: %v", args)
			}
		}
		h.fake.snapshotResults = []fakeSnapshotResult{
			{output: h.baseSnapshot},
			{output: h.baseSnapshot},
			{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
			{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
			{output: snapshotWithDanglingWorkspaceResources(t, h.baseSnapshot, "w2")},
		}

		result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
		if err == nil || result.Status != corebackend.CloseFailed || !strings.Contains(err.Error(), "references unknown workspace") {
			t.Fatalf("CloseOwnedSession() = %#v, %v; want dangling snapshot rejection", result, err)
		}
		if len(h.calls) != 2 {
			t.Fatalf("mutation calls = %v, want remove then one workspace close", h.calls)
		}
	})
}

func TestCloseOwnedRemovesWorktreeThenResidualWorkspaceWithoutBranchMutation(t *testing.T) {
	h := newOwnedOperationHarness(t)
	h.respond = func(args []string) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"worktree", "remove", "--workspace", "w2", "--json"}):
			h.removeCheckout(t)
			return worktreeRemovedResponse("w2", h.worktreePath, false), nil
		case slices.Equal(args, []string{"workspace", "close", "w2"}):
			return okOperationResponse("cli:workspace:close"), nil
		default:
			return nil, fmt.Errorf("unexpected args: %v", args)
		}
	}
	h.fake.snapshotResults = []fakeSnapshotResult{
		{output: h.baseSnapshot},
		{output: h.baseSnapshot},
		{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
		{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
		{output: snapshotWithoutWorkspaceResources(t, h.baseSnapshot, "w2")},
	}

	result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != corebackend.CloseConfirmed {
		t.Fatalf("CloseOwnedSession status = %d, want confirmed", result.Status)
	}
	want := [][]string{
		{"worktree", "remove", "--workspace", "w2", "--json"},
		{"workspace", "close", "w2"},
	}
	if !slices.EqualFunc(h.calls, want, slices.Equal[[]string]) {
		t.Fatalf("operation calls = %v, want %v", h.calls, want)
	}
}

func TestCloseOwnedForceIsExplicit(t *testing.T) {
	t.Run("typed force", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func(args []string) ([]byte, error) {
			want := []string{"worktree", "remove", "--workspace", "w2", "--force", "--json"}
			if !slices.Equal(args, want) {
				t.Fatalf("remove args = %v, want %v", args, want)
			}
			h.removeCheckout(t)
			return worktreeRemovedResponse("w2", h.worktreePath, true), nil
		}
		h.fake.snapshotResults = []fakeSnapshotResult{
			{output: h.baseSnapshot},
			{output: h.baseSnapshot},
			{output: snapshotWithoutWorkspaceResources(t, h.baseSnapshot, "w2")},
		}
		request := h.ownedCloseRequest()
		request.Force = true
		result, err := h.backend.CloseOwnedSession(context.Background(), request)
		if err != nil || result.Status != corebackend.CloseConfirmed {
			t.Fatalf("CloseOwnedSession = %#v, %v", result, err)
		}
	})
}

func TestCoreCloseOwnedFailsClosedWithoutWorktreeOwnershipIdentity(t *testing.T) {
	h := newOwnedOperationHarness(t)
	result, err := h.backend.CloseOwned(corebackend.CloseRequest{
		Ref:          h.childTarget().Ref,
		WorktreePath: h.childTarget().WorktreePath,
		ShellKey:     h.childTarget().TerminalID,
	})
	if !errors.Is(err, corebackend.ErrUnsupported) || result.Status != corebackend.CloseFailed {
		t.Fatalf("CloseOwned() = %#v, %v; want unsupported missing-identity failure", result, err)
	}
	if len(h.fake.commands) != 0 || len(h.calls) != 0 {
		t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
	}
}

func TestBoundCoreCloseOwnedRunsSavedCompositeOnlyForExactFingerprint(t *testing.T) {
	t.Run("exact request", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		bound, err := h.backend.BindOwnedClose(h.ownedCloseRequest())
		if err != nil {
			t.Fatal(err)
		}
		h.respond = func(args []string) ([]byte, error) {
			switch args[0] {
			case "worktree":
				h.removeCheckout(t)
				return worktreeRemovedResponse("w2", h.worktreePath, false), nil
			case "workspace":
				return okOperationResponse("cli:workspace:close"), nil
			default:
				return nil, fmt.Errorf("unexpected operation %v", args)
			}
		}
		h.fake.snapshotResults = []fakeSnapshotResult{
			{output: h.baseSnapshot},
			{output: h.baseSnapshot},
			{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
			{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
			{output: snapshotWithoutWorkspaceResources(t, h.baseSnapshot, "w2")},
		}
		target := h.childTarget()
		result, err := bound.CloseOwned(corebackend.CloseRequest{
			Ref:          target.Ref,
			WorktreePath: target.WorktreePath,
			ShellKey:     target.TerminalID,
		})
		if err != nil || result.Status != corebackend.CloseConfirmed {
			t.Fatalf("CloseOwned() = %#v, %v", result, err)
		}
	})

	for _, field := range []string{"ref", "worktree path", "shell key"} {
		t.Run("reject changed "+field, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			bound, err := h.backend.BindOwnedClose(h.ownedCloseRequest())
			if err != nil {
				t.Fatal(err)
			}
			target := h.childTarget()
			request := corebackend.CloseRequest{
				Ref:          target.Ref,
				WorktreePath: target.WorktreePath,
				ShellKey:     target.TerminalID,
			}
			switch field {
			case "ref":
				request.Ref.Pane = "w2:replacement"
			case "worktree path":
				request.WorktreePath += "-replacement"
			case "shell key":
				request.ShellKey += "-replacement"
			}
			result, err := bound.CloseOwned(request)
			if !errors.Is(err, ErrOwnedIdentityMismatch) || result.Status != corebackend.CloseFailed {
				t.Fatalf("CloseOwned() = %#v, %v; want exact-fingerprint rejection", result, err)
			}
			if len(h.fake.commands) != 0 || len(h.calls) != 0 {
				t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
			}
		})
	}
}

func TestBindOwnedCloseRejectsForceAndDeepClonesAdmission(t *testing.T) {
	t.Run("force", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		request := h.ownedCloseRequest()
		request.Force = true
		bound, err := h.backend.BindOwnedClose(request)
		if err == nil || bound != nil || !strings.Contains(err.Error(), "cannot bind force") {
			t.Fatalf("BindOwnedClose() = %p, %v; want force rejection", bound, err)
		}
		if len(h.fake.commands) != 0 || len(h.calls) != 0 {
			t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
		}
	})

	t.Run("deep clone", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		request := h.ownedCloseRequest()
		bound, err := h.backend.BindOwnedClose(request)
		if err != nil {
			t.Fatal(err)
		}
		if bound == h.backend || bound.probeGate == h.backend.probeGate || bound.control == h.backend.control || bound.owner == h.backend.owner {
			t.Fatal("BindOwnedClose reused mutable backend state")
		}
		request.Target.AgentSession.Value = "mutated-caller-session"
		if got := bound.targetAdmission.target.AgentSession.Value; got != "session-a" {
			t.Fatalf("bound agent session = %q, want immutable session-a", got)
		}
		if got := bound.targetAdmission.closeRequest.Target.AgentSession.Value; got != "session-a" {
			t.Fatalf("bound close-request agent session = %q, want immutable session-a", got)
		}
		boundControlPath := bound.control.configPath
		h.backend.control.configPath = "mutated-source-control"
		if bound.control.configPath != boundControlPath {
			t.Fatal("bound backend shares control environment with source")
		}
		boundOwnerSession := bound.owner.marker.Session
		h.backend.owner.marker.Session = "mutated-source-owner"
		if bound.owner.marker.Session != boundOwnerSession {
			t.Fatal("bound backend shares owner admission with source")
		}
		h.backend.admitted["source-only"] = binaryAdmission{path: "source-only"}
		if _, shared := bound.admitted["source-only"]; shared {
			t.Fatal("bound backend shares admitted map with source")
		}
		bound.admitted["bound-only"] = binaryAdmission{path: "bound-only"}
		if _, shared := h.backend.admitted["bound-only"]; shared {
			t.Fatal("source backend shares admitted map with bound backend")
		}
	})
}

func TestCloseOwnedRequiresLinkedWorktree(t *testing.T) {
	h := newOwnedOperationHarness(t)
	h.snapshot = mutateSnapshot(t, h.baseSnapshot, func(snapshot map[string]any) {
		for _, raw := range snapshot["workspaces"].([]any) {
			workspace := raw.(map[string]any)
			if workspace["workspace_id"] == "w2" {
				workspace["worktree"].(map[string]any)["is_linked_worktree"] = false
			}
		}
	})
	result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
	if !errors.Is(err, ErrOwnedIdentityMismatch) || result.Status != corebackend.CloseFailed {
		t.Fatalf("CloseOwnedSession() = %#v, %v; want linked-worktree rejection", result, err)
	}
	if len(h.calls) != 0 {
		t.Fatalf("mutation calls = %v, want none", h.calls)
	}
}

func TestCloseOwnedRechecksWorkspaceImmediatelyBeforeWorktreeRemoval(t *testing.T) {
	h := newOwnedOperationHarness(t)
	h.fake.snapshotResults = []fakeSnapshotResult{
		{output: h.baseSnapshot},
		{output: mutateSnapshot(t, h.baseSnapshot, func(snapshot map[string]any) {
			for _, raw := range snapshot["workspaces"].([]any) {
				workspace := raw.(map[string]any)
				if workspace["workspace_id"] == "w2" {
					workspace["label"] = strings.Repeat("d", 64)
				}
			}
		})},
	}
	result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
	if !errors.Is(err, ErrOwnedIdentityMismatch) || result.Status != corebackend.CloseFailed {
		t.Fatalf("CloseOwnedSession() = %#v, %v; want immediate ownership mismatch", result, err)
	}
	if len(h.calls) != 0 {
		t.Fatalf("mutation calls = %v, want none", h.calls)
	}
}

func TestCloseOwnedWorktreeMarkerGatesFailClosedBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *ownedOperationHarness, *OwnedCloseRequest)
	}{
		{
			name: "missing saved nonce",
			mutate: func(_ *testing.T, _ *ownedOperationHarness, request *OwnedCloseRequest) {
				request.WorktreeOwnershipNonce = ""
			},
		},
		{
			name: "saved nonce differs from workspace label",
			mutate: func(_ *testing.T, _ *ownedOperationHarness, request *OwnedCloseRequest) {
				request.WorktreeOwnershipNonce = strings.Repeat("d", 64)
			},
		},
		{
			name: "missing saved git directory",
			mutate: func(_ *testing.T, _ *ownedOperationHarness, request *OwnedCloseRequest) {
				request.WorktreeGitDir = ""
			},
		},
		{
			name: "saved git directory mismatch",
			mutate: func(_ *testing.T, h *ownedOperationHarness, request *OwnedCloseRequest) {
				request.WorktreeGitDir = h.repoKey
			},
		},
		{
			name: "missing marker",
			mutate: func(t *testing.T, h *ownedOperationHarness, _ *OwnedCloseRequest) {
				t.Helper()
				if err := os.Remove(worktreeOwnershipMarkerPath(h.worktreeGitDir)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink marker",
			mutate: func(t *testing.T, h *ownedOperationHarness, _ *OwnedCloseRequest) {
				t.Helper()
				path := worktreeOwnershipMarkerPath(h.worktreeGitDir)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "marker.json")
				if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory marker",
			mutate: func(t *testing.T, h *ownedOperationHarness, _ *OwnedCloseRequest) {
				t.Helper()
				path := worktreeOwnershipMarkerPath(h.worktreeGitDir)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "permissive marker mode",
			mutate: func(t *testing.T, h *ownedOperationHarness, _ *OwnedCloseRequest) {
				t.Helper()
				h.writeWorktreeMarker(t, h.worktreeMarker(), 0o644)
			},
		},
		{
			name: "unknown JSON field",
			mutate: func(t *testing.T, h *ownedOperationHarness, _ *OwnedCloseRequest) {
				t.Helper()
				data, err := json.Marshal(h.worktreeMarker())
				if err != nil {
					t.Fatal(err)
				}
				data = append(data[:len(data)-1], []byte(`,"unknown":true}`)...)
				h.writeRawWorktreeMarker(t, append(data, '\n'), 0o600)
			},
		},
		{
			name: "duplicate nonce",
			mutate: func(t *testing.T, h *ownedOperationHarness, _ *OwnedCloseRequest) {
				t.Helper()
				marker := h.worktreeMarker()
				data := fmt.Appendf(nil, `{"schema_version":1,"nonce":%q,"nonce":%q,"workspace_id":%q,"repo_key":%q,"checkout_path":%q,"git_dir":%q}`+"\n",
					marker.Nonce, marker.Nonce, marker.WorkspaceID, marker.RepoKey, marker.CheckoutPath, marker.GitDir)
				h.writeRawWorktreeMarker(t, data, 0o600)
			},
		},
		{
			name: "trailing JSON value",
			mutate: func(t *testing.T, h *ownedOperationHarness, _ *OwnedCloseRequest) {
				t.Helper()
				data, err := json.Marshal(h.worktreeMarker())
				if err != nil {
					t.Fatal(err)
				}
				h.writeRawWorktreeMarker(t, append(data, []byte("\n{}\n")...), 0o600)
			},
		},
		{
			name: "marker nonce mismatch",
			mutate: func(t *testing.T, h *ownedOperationHarness, _ *OwnedCloseRequest) {
				t.Helper()
				marker := h.worktreeMarker()
				marker.Nonce = strings.Repeat("d", 64)
				h.writeWorktreeMarker(t, marker, 0o600)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			request := h.ownedCloseRequest()
			test.mutate(t, h, &request)
			result, err := h.backend.CloseOwnedSession(context.Background(), request)
			if err == nil || result.Status != corebackend.CloseFailed {
				t.Fatalf("CloseOwnedSession() = %#v, %v; want marker-gate failure", result, err)
			}
			if len(h.calls) != 0 {
				t.Fatalf("mutation calls = %v, want none", h.calls)
			}
		})
	}
}

func TestCloseOwnedRequiresWorkspaceFingerprintFields(t *testing.T) {
	t.Run("pre-remove number", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.snapshot = mutateSnapshot(t, h.baseSnapshot, func(snapshot map[string]any) {
			for _, workspace := range snapshot["workspaces"].([]any) {
				item := workspace.(map[string]any)
				if item["workspace_id"] == "w2" {
					delete(item, "number")
				}
			}
		})
		result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
		if err == nil || result.Status != corebackend.CloseFailed || !strings.Contains(err.Error(), "workspace with incomplete required fields") {
			t.Fatalf("CloseOwnedSession() = %#v, %v; want missing pre-remove fingerprint failure", result, err)
		}
		if len(h.calls) != 0 {
			t.Fatalf("mutation calls = %v, want none", h.calls)
		}
	})

	t.Run("post-remove label", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) {
			h.removeCheckout(t)
			return worktreeRemovedResponse("w2", h.worktreePath, false), nil
		}
		postRemoval := snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")
		h.fake.snapshotResults = []fakeSnapshotResult{
			{output: h.baseSnapshot},
			{output: h.baseSnapshot},
			{output: mutateSnapshot(t, postRemoval, func(snapshot map[string]any) {
				for _, workspace := range snapshot["workspaces"].([]any) {
					item := workspace.(map[string]any)
					if item["workspace_id"] == "w2" {
						delete(item, "label")
					}
				}
			})},
		}
		result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
		if err == nil || result.Status != corebackend.CloseFailed || !strings.Contains(err.Error(), "workspace with incomplete required fields") {
			t.Fatalf("CloseOwnedSession() = %#v, %v; want missing post-remove fingerprint failure", result, err)
		}
		if len(h.calls) != 1 || h.calls[0][0] != "worktree" {
			t.Fatalf("mutation calls = %v, want one remove and no workspace close", h.calls)
		}
	})
}

func TestCloseOwnedDoesNotRetryLostMutationResponse(t *testing.T) {
	tests := []struct {
		name          string
		failWorkspace bool
		wantCalls     int
	}{
		{name: "worktree remove", wantCalls: 1},
		{name: "workspace close", failWorkspace: true, wantCalls: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			h.respond = func(args []string) ([]byte, error) {
				if args[0] == "worktree" {
					if !test.failWorkspace {
						return nil, errors.New("response lost")
					}
					h.removeCheckout(t)
					return worktreeRemovedResponse("w2", h.worktreePath, false), nil
				}
				return nil, errors.New("response lost")
			}
			h.fake.snapshotResults = []fakeSnapshotResult{
				{output: h.baseSnapshot},
				{output: h.baseSnapshot},
				{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
				{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
			}
			result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
			if err == nil || !strings.Contains(err.Error(), "not retried") {
				t.Fatalf("error = %v, want response-loss failure", err)
			}
			if result.Status != corebackend.CloseFailed {
				t.Fatalf("status = %d, want failed", result.Status)
			}
			if len(h.calls) != test.wantCalls {
				t.Fatalf("operation calls = %v, want %d without retry", h.calls, test.wantCalls)
			}
		})
	}
}

func TestCloseOwnedRequiresCheckoutAbsenceBeforeWorkspaceClose(t *testing.T) {
	h := newOwnedOperationHarness(t)
	h.respond = func([]string) ([]byte, error) {
		return worktreeRemovedResponse("w2", h.worktreePath, false), nil
	}
	result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
	if err == nil || result.Status != corebackend.CloseFailed || !strings.Contains(err.Error(), "checkout") || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("CloseOwnedSession() = %#v, %v; want checkout-presence failure", result, err)
	}
	if len(h.calls) != 1 || h.calls[0][0] != "worktree" {
		t.Fatalf("mutation calls = %v, want one remove and no workspace close", h.calls)
	}
}

func TestOwnedMutationResponseGatesFailClosed(t *testing.T) {
	t.Run("pane run must be empty", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) { return okOperationResponse("cli:pane:close"), nil }
		err := h.backend.SendLineOwned(context.Background(), SendLineRequest{Target: h.childTarget(), Line: "hello"})
		if err == nil || !strings.Contains(err.Error(), "unexpected output") {
			t.Fatalf("error = %v, want pane run output rejection", err)
		}
		if len(h.calls) != 1 {
			t.Fatalf("mutation calls = %v, want one", h.calls)
		}
	})

	t.Run("workspace focus exact target", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) { return workspaceFocusedResponse("w1"), nil }
		err := h.backend.FocusOwned(context.Background(), FocusRequest{Target: h.childTarget()})
		if err == nil || !strings.Contains(err.Error(), "workspace focus response") {
			t.Fatalf("error = %v, want focus envelope rejection", err)
		}
	})

	t.Run("workspace focus exact ownership label", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) {
			return workspaceFocusedResponseWithLabel("w2", "replacement-owner"), nil
		}
		err := h.backend.FocusOwned(context.Background(), FocusRequest{Target: h.childTarget()})
		if err == nil || !strings.Contains(err.Error(), "workspace focus response") {
			t.Fatalf("error = %v, want focus ownership envelope rejection", err)
		}
	})

	t.Run("workspace focus requires complete response", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) {
			return []byte(`{"id":"cli:workspace:focus","result":{"type":"workspace_info","workspace":{"workspace_id":"w2"}}}`), nil
		}
		err := h.backend.FocusOwned(context.Background(), FocusRequest{Target: h.childTarget()})
		if err == nil || !strings.Contains(err.Error(), "missing required fields") {
			t.Fatalf("error = %v, want incomplete focus response rejection", err)
		}
	})

	t.Run("workspace focus verifies focused postcondition", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) { return workspaceFocusedResponse("w2"), nil }
		err := h.backend.FocusOwned(context.Background(), FocusRequest{Target: h.childTarget()})
		if err == nil || !strings.Contains(err.Error(), "is not focused") {
			t.Fatalf("error = %v, want unfocused postcondition rejection", err)
		}
	})

	t.Run("pane close exact id and type", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) { return okOperationResponse("cli:workspace:close"), nil }
		err := h.backend.ClosePaneOwned(context.Background(), ClosePaneRequest{Target: h.childTarget()})
		if err == nil || !strings.Contains(err.Error(), "pane close response") {
			t.Fatalf("error = %v, want pane close envelope rejection", err)
		}
	})

	t.Run("worktree remove exact id", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func([]string) ([]byte, error) {
			return bytes.Replace(worktreeRemovedResponse("w2", h.worktreePath, false), []byte("cli:worktree:remove"), []byte("cli:wrong"), 1), nil
		}
		result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
		if err == nil || !strings.Contains(err.Error(), "worktree remove response") || result.Status != corebackend.CloseFailed {
			t.Fatalf("CloseOwnedSession = %#v, %v; want failed response gate", result, err)
		}
		if len(h.calls) != 1 {
			t.Fatalf("mutation calls = %v, want no retry or workspace close", h.calls)
		}
	})

	t.Run("workspace close exact id and type", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		h.respond = func(args []string) ([]byte, error) {
			if args[0] == "worktree" {
				h.removeCheckout(t)
				return worktreeRemovedResponse("w2", h.worktreePath, false), nil
			}
			return okOperationResponse("cli:pane:close"), nil
		}
		h.fake.snapshotResults = []fakeSnapshotResult{
			{output: h.baseSnapshot},
			{output: h.baseSnapshot},
			{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
			{output: snapshotAfterWorktreeRemoval(t, h.baseSnapshot, "w2")},
		}
		result, err := h.backend.CloseOwnedSession(context.Background(), h.ownedCloseRequest())
		if err == nil || !strings.Contains(err.Error(), "workspace close response") || result.Status != corebackend.CloseFailed {
			t.Fatalf("CloseOwnedSession = %#v, %v; want failed response gate", result, err)
		}
		if len(h.calls) != 2 {
			t.Fatalf("mutation calls = %v, want remove then one close", h.calls)
		}
	})
}

func TestRunOwnedOperationRechecksExecutableImmediatelyBeforeMutation(t *testing.T) {
	h := newOwnedOperationHarness(t)
	drifted := false
	h.backend.hashFile = func(path string) (string, error) {
		if path != h.marker.BinaryPath {
			t.Fatalf("hashFile(%q), want %q", path, h.marker.BinaryPath)
		}
		if drifted {
			return strings.Repeat("c", 64), nil
		}
		return h.marker.BinarySHA256, nil
	}
	originalOutput := h.backend.output
	h.backend.output = func(ctx context.Context, binary string, env []string, args ...string) (commandStreams, error) {
		out, err := originalOutput(ctx, binary, env, args...)
		if commandKey(args) == "status" {
			drifted = true
		}
		return out, err
	}
	admission := *h.backend.owner
	previous := probeResult{
		binary:   h.marker.BinaryPath,
		sha256:   h.marker.BinarySHA256,
		version:  h.marker.BinaryVersion,
		protocol: supportedProtocol,
		route:    route{session: h.marker.Session, socketPath: h.marker.SocketPath},
	}
	_, err := h.backend.runOwnedOperation(context.Background(), admission, previous, "pane", "run", "w2:p1", "hello")
	if err == nil || !strings.Contains(err.Error(), "executable drifted after admission") {
		t.Fatalf("runOwnedOperation() error = %v, want pre-mutation executable drift", err)
	}
	if len(h.calls) != 0 {
		t.Fatalf("mutation calls = %v, want none", h.calls)
	}
}

func TestBoundCoreBackendMethodsUseImmutableTarget(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		bound, err := h.backend.BindOwnedTarget(h.childTarget())
		if err != nil {
			t.Fatal(err)
		}
		h.respond = func([]string) ([]byte, error) { return []byte("saved content"), nil }
		got, err := bound.Read(h.childTarget().Ref, 1)
		if err != nil || got != "saved content" {
			t.Fatalf("Read() = %q, %v", got, err)
		}
	})

	t.Run("send line", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		bound, err := h.backend.BindOwnedTarget(h.childTarget())
		if err != nil {
			t.Fatal(err)
		}
		h.respond = func([]string) ([]byte, error) { return nil, nil }
		if err := bound.SendLine(h.childTarget().Ref, "hello"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("focus", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		bound, err := h.backend.BindOwnedTarget(h.childTarget())
		if err != nil {
			t.Fatal(err)
		}
		h.respond = func([]string) ([]byte, error) {
			h.snapshot = snapshotWithFocusedWorkspace(t, h.baseSnapshot, "w2")
			return workspaceFocusedResponse("w2"), nil
		}
		if err := bound.Focus(h.childTarget().Ref); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("close pane", func(t *testing.T) {
		h := newOwnedOperationHarness(t)
		bound, err := h.backend.BindOwnedTarget(h.childTarget())
		if err != nil {
			t.Fatal(err)
		}
		h.respond = func([]string) ([]byte, error) { return okOperationResponse("cli:pane:close"), nil }
		h.fake.snapshotResults = []fakeSnapshotResult{
			{output: h.baseSnapshot},
			{output: snapshotWithoutWorkspaceResources(t, h.baseSnapshot, "w2")},
		}
		if err := bound.Close(h.childTarget().Ref); err != nil {
			t.Fatal(err)
		}
		if len(h.calls) != 1 || h.calls[0][0] != "pane" {
			t.Fatalf("operations = %v, want pane close only", h.calls)
		}
	})
}

func TestBoundCoreBackendMethodsRejectDifferentRefBeforeCLI(t *testing.T) {
	tests := []struct {
		name string
		call func(*Backend, corebackend.PaneRef) error
	}{
		{name: "read", call: func(b *Backend, ref corebackend.PaneRef) error {
			_, err := b.Read(ref, 1)
			return err
		}},
		{name: "send", call: func(b *Backend, ref corebackend.PaneRef) error { return b.SendLine(ref, "hello") }},
		{name: "focus", call: func(b *Backend, ref corebackend.PaneRef) error { return b.Focus(ref) }},
		{name: "close", call: func(b *Backend, ref corebackend.PaneRef) error { return b.Close(ref) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			bound, err := h.backend.BindOwnedTarget(h.childTarget())
			if err != nil {
				t.Fatal(err)
			}
			foreign := h.childTarget().Ref
			foreign.Pane = "w2:replacement"
			if err := test.call(bound, foreign); !errors.Is(err, ErrOwnedIdentityMismatch) {
				t.Fatalf("error = %v, want target admission mismatch", err)
			}
			if len(h.fake.commands) != 0 || len(h.calls) != 0 {
				t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
			}
		})
	}
}

func TestCoreBackendMethodsFailClosedWithoutSavedIdentity(t *testing.T) {
	tests := []struct {
		name string
		call func(*Backend, corebackend.PaneRef) error
	}{
		{
			name: "read",
			call: func(b *Backend, ref corebackend.PaneRef) error {
				_, err := b.Read(ref, 3)
				return err
			},
		},
		{
			name: "send",
			call: func(b *Backend, ref corebackend.PaneRef) error { return b.SendLine(ref, "hello") },
		},
		{
			name: "focus",
			call: func(b *Backend, ref corebackend.PaneRef) error { return b.Focus(ref) },
		},
		{
			name: "close",
			call: func(b *Backend, ref corebackend.PaneRef) error { return b.Close(ref) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedOperationHarness(t)
			if err := test.call(h.backend, h.childTarget().Ref); !errors.Is(err, corebackend.ErrUnsupported) {
				t.Fatalf("error = %v, want unsupported saved-identity failure", err)
			}
			if len(h.fake.commands) != 0 || len(h.calls) != 0 {
				t.Fatalf("commands = base:%d operation:%d, want none", len(h.fake.commands), len(h.calls))
			}
		})
	}
}

func worktreeRemovedResponse(workspace, path string, forced bool) []byte {
	return fmt.Appendf(nil,
		`{"id":"cli:worktree:remove","result":{"type":"worktree_removed","workspace_id":%q,"path":%q,"forced":%t}}`+"\n",
		workspace,
		path,
		forced,
	)
}

func okOperationResponse(id string) []byte {
	return fmt.Appendf(nil, `{"id":%q,"result":{"type":"ok"}}`+"\n", id)
}

func workspaceFocusedResponse(workspace string) []byte {
	return workspaceFocusedResponseWithLabel(workspace, strings.Repeat("c", 64))
}

func workspaceFocusedResponseWithLabel(workspace, label string) []byte {
	return fmt.Appendf(nil,
		`{"id":"cli:workspace:focus","result":{"type":"workspace_info","workspace":{"workspace_id":%q,"number":2,"label":%q,"focused":true,"pane_count":1,"tab_count":1,"active_tab_id":"w2:t1","agent_status":"working"}}}`+"\n",
		workspace,
		label,
	)
}

func snapshotWithFocusedWorkspace(t *testing.T, source, workspaceID string) string {
	t.Helper()
	return mutateSnapshot(t, source, func(snapshot map[string]any) {
		activeTabID := ""
		for _, workspace := range snapshot["workspaces"].([]any) {
			item := workspace.(map[string]any)
			focused := item["workspace_id"] == workspaceID
			item["focused"] = focused
			if focused {
				activeTabID = item["active_tab_id"].(string)
			}
		}
		if activeTabID == "" {
			t.Fatalf("snapshot does not contain workspace %q", workspaceID)
		}

		focusedPaneID := ""
		for _, layout := range snapshot["layouts"].([]any) {
			item := layout.(map[string]any)
			if item["tab_id"] == activeTabID {
				focusedPaneID = item["focused_pane_id"].(string)
			}
		}
		if focusedPaneID == "" {
			t.Fatalf("snapshot does not contain layout for active tab %q", activeTabID)
		}

		for _, tab := range snapshot["tabs"].([]any) {
			item := tab.(map[string]any)
			item["focused"] = item["tab_id"] == activeTabID
		}
		for _, pane := range snapshot["panes"].([]any) {
			item := pane.(map[string]any)
			item["focused"] = item["pane_id"] == focusedPaneID
		}
		for _, layout := range snapshot["layouts"].([]any) {
			item := layout.(map[string]any)
			for _, pane := range item["panes"].([]any) {
				layoutPane := pane.(map[string]any)
				layoutPane["focused"] = layoutPane["pane_id"] == focusedPaneID
			}
		}
		for _, agent := range snapshot["agents"].([]any) {
			item := agent.(map[string]any)
			item["focused"] = item["pane_id"] == focusedPaneID
		}
		snapshot["focused_workspace_id"] = workspaceID
		snapshot["focused_tab_id"] = activeTabID
		snapshot["focused_pane_id"] = focusedPaneID
	})
}

func mutateSnapshot(t *testing.T, source string, mutate func(map[string]any)) string {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal([]byte(source), &envelope); err != nil {
		t.Fatal(err)
	}
	result := envelope["result"].(map[string]any)
	snapshot := result["snapshot"].(map[string]any)
	mutate(snapshot)
	out, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return string(out) + "\n"
}

func snapshotWithoutWorkspaceResources(t *testing.T, source, workspace string) string {
	t.Helper()
	return mutateSnapshot(t, source, func(snapshot map[string]any) {
		snapshot["workspaces"] = filterSnapshotObjects(snapshot["workspaces"].([]any), "workspace_id", workspace)
		snapshot["tabs"] = filterSnapshotObjects(snapshot["tabs"].([]any), "workspace_id", workspace)
		snapshot["panes"] = filterSnapshotObjects(snapshot["panes"].([]any), "workspace_id", workspace)
		snapshot["layouts"] = filterSnapshotObjects(snapshot["layouts"].([]any), "workspace_id", workspace)
		snapshot["agents"] = filterSnapshotObjects(snapshot["agents"].([]any), "workspace_id", workspace)
	})
}

func snapshotWithDanglingWorkspaceResources(t *testing.T, source, workspace string) string {
	t.Helper()
	return mutateSnapshot(t, source, func(snapshot map[string]any) {
		snapshot["workspaces"] = filterSnapshotObjects(snapshot["workspaces"].([]any), "workspace_id", workspace)
		snapshot["panes"] = filterSnapshotObjects(snapshot["panes"].([]any), "workspace_id", workspace)
		snapshot["agents"] = filterSnapshotObjects(snapshot["agents"].([]any), "workspace_id", workspace)
	})
}

func snapshotAfterWorktreeRemoval(t *testing.T, source, workspace string) string {
	t.Helper()
	return mutateSnapshot(t, source, func(snapshot map[string]any) {
		for _, raw := range snapshot["workspaces"].([]any) {
			item := raw.(map[string]any)
			if item["workspace_id"] == workspace {
				delete(item, "worktree")
			}
		}
	})
}

func filterSnapshotObjects(objects []any, key, value string) []any {
	filtered := make([]any, 0, len(objects))
	for _, raw := range objects {
		if raw.(map[string]any)[key] != value {
			filtered = append(filtered, raw)
		}
	}
	return filtered
}
