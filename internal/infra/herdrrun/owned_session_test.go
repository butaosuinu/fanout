package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func newOwnedHarness(t *testing.T) *ownedHarness {
	t.Helper()
	return newOwnedHarnessWithDashboardEnvironment(t, nil)
}

func newOwnedHarnessWithDashboardEnvironment(t *testing.T, environment []string) *ownedHarness {
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
	supervisor := &fakeOwnedSupervisor{
		dashboardAuthentication: dashboardAuthenticationFromCaller(environment),
	}
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
				Agent:   pane.AgentProvider,
				AgentID: pane.AgentID, AgentSession: cloneAgentSession(pane.AgentSession),
			}
		}
	}
	h.t.Fatal("owned child pane not found")
	return corebackend.OwnedPaneIdentity{}
}

func ownedPaneBinding(target corebackend.OwnedPaneIdentity) corebackend.PaneBinding {
	return corebackend.PaneBinding{
		Ref: target.Ref, SessionID: target.SessionID, SocketPath: target.SocketPath,
		WorkspaceLabel: target.WorkspaceLabel, TerminalID: target.TerminalID,
		Agent: target.Agent, AgentID: target.AgentID, AgentSession: target.AgentSession,
		RepoKey: target.RepoKey, WorktreePath: target.WorktreePath,
	}
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
	// The runtime names the provider on every agent record, whether or not it
	// is holding a conversation for it.
	provider := target.Agent
	var session *agentSessionJSON
	if target.AgentSession != nil {
		sessionAgent := target.AgentSession.Agent
		source := target.AgentSession.Source
		kind := target.AgentSession.Kind
		value := target.AgentSession.Value
		session = &agentSessionJSON{Source: &source, Agent: &sessionAgent, Kind: &kind, Value: &value}
	}
	agent := agentJSON{
		TerminalID: target.TerminalID, Name: &name, Agent: &provider, AgentStatus: "working",
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
		TerminalID: target.TerminalID, Agent: target.Agent, AgentID: target.AgentID,
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
		TerminalID: target.TerminalID, Agent: target.Agent, AgentID: target.AgentID, AgentSession: target.AgentSession,
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
		TerminalID: target.TerminalID, Agent: target.Agent, AgentID: target.AgentID, AgentSession: target.AgentSession,
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
	target := genericWorkspaceCloseTarget(h)
	respondToGenericWorkspaceClose(h, target)
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

func respondToGenericWorkspaceClose(h *ownedHarness, target corebackend.OwnedPaneIdentity) {
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"workspace", "close", target.Ref.Workspace}) {
			return nil, fmt.Errorf("unexpected close args %v", args)
		}
		h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
			closed := map[string]bool{target.Ref.Workspace: true}
			// Herdr 0.8.2 closes every member when the target is a repo root.
			for _, workspace := range *snapshot.Workspaces {
				if workspace.WorkspaceID != target.Ref.Workspace || workspace.Worktree == nil ||
					workspace.Worktree.IsLinked {
					continue
				}
				for _, member := range *snapshot.Workspaces {
					if member.Worktree != nil && member.Worktree.RepoKey == workspace.Worktree.RepoKey {
						closed[member.WorkspaceID] = true
					}
				}
			}
			workspaces := slices.DeleteFunc(*snapshot.Workspaces, func(w workspaceJSON) bool {
				return closed[w.WorkspaceID]
			})
			panes := slices.DeleteFunc(*snapshot.Panes, func(p paneJSON) bool {
				return closed[p.WorkspaceID]
			})
			agents := slices.DeleteFunc(*snapshot.Agents, func(a agentJSON) bool {
				return closed[a.WorkspaceID]
			})
			snapshot.Workspaces, snapshot.Panes, snapshot.Agents = &workspaces, &panes, &agents
		})
		return nil, nil
	}
}

func TestBoundOwnedWorkspaceCloserRejectsRepositoryGroupClose(t *testing.T) {
	h := newOwnedHarness(t)
	target := genericWorkspaceCloseTarget(h)
	respondToGenericWorkspaceClose(h, target)
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Workspaces {
			workspace := &(*snapshot.Workspaces)[i]
			workspace.Worktree = &worktreeInfoJSON{
				RepoKey: h.commonDir, RepoRoot: target.CurrentPath, CheckoutPath: target.CurrentPath,
			}
		}
	})
	before := h.fake.snapshot
	bound, err := h.session.Backend().BindOwnedWorkspaceClose(target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bound.CloseOwned(corebackend.CloseRequest{Ref: corebackend.PaneRef{Backend: corebackend.Herdr, Pane: target.Ref.Pane}})
	if !errors.Is(err, corebackend.ErrOwnedMutationNotIssued) || h.fake.snapshot != before {
		t.Fatalf("group close must not issue a mutation: error=%v; snapshot unchanged=%t", err, h.fake.snapshot == before)
	}
	assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
}

func TestBoundOwnedWorkspaceCloserWithAddedGitMetadata(t *testing.T) {
	for _, beforeBind := range []bool{true, false} {
		for _, shape := range []string{"repository root", "normalized repository root", "linked worktree", "linked at recorded cwd", "foreign repository root"} {
			t.Run(fmt.Sprintf("%s/before_bind=%t", shape, beforeBind), func(t *testing.T) {
				h := newOwnedHarness(t)
				target := genericWorkspaceCloseTarget(h)
				respondToGenericWorkspaceClose(h, target)
				worktree := &worktreeInfoJSON{
					RepoKey: h.commonDir, RepoRoot: target.CurrentPath, CheckoutPath: target.CurrentPath,
				}
				reject := false
				switch shape {
				case "normalized repository root":
					worktree.CheckoutPath += "/."
				case "linked worktree":
					worktree.CheckoutPath += "-linked"
					reject = true
				case "linked at recorded cwd":
					worktree.RepoRoot += "-root"
					reject = true
				case "foreign repository root":
					worktree.CheckoutPath += "-foreign"
					worktree.RepoRoot = worktree.CheckoutPath
					reject = true
				}
				addCheckout := func() {
					h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
						for i := range *snapshot.Workspaces {
							workspace := &(*snapshot.Workspaces)[i]
							if workspace.WorkspaceID == target.Ref.Workspace {
								workspace.Worktree = worktree
							}
						}
					})
				}
				if beforeBind {
					addCheckout()
				}
				bound, err := h.session.Backend().BindOwnedWorkspaceClose(target)
				if beforeBind && reject && errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
					assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !beforeBind {
					addCheckout()
				}
				result, err := bound.CloseOwned(corebackend.CloseRequest{Ref: corebackend.PaneRef{
					Backend: corebackend.Herdr, Pane: target.Ref.Pane,
				}})
				if reject {
					if !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
						t.Fatalf("CloseOwned() error = %v, want checkout rejection", err)
					}
					assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
					return
				}
				if err != nil || result.Status != corebackend.CloseConfirmed {
					t.Fatalf("CloseOwned() = %+v, %v", result, err)
				}
				closeCalls := 0
				for _, command := range h.fake.commands {
					if slices.Equal(command.args, []string{"workspace", "close", target.Ref.Workspace}) {
						closeCalls++
					}
				}
				if closeCalls != 1 {
					t.Fatalf("workspace close calls = %d, want 1", closeCalls)
				}
			})
		}
	}
}

func TestBoundOwnedWorkspaceCloserRejectsUnadmittedPaneWithoutMutation(t *testing.T) {
	h := newOwnedHarness(t)
	target := genericWorkspaceCloseTarget(h)
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		focused := false
		revision := uint64(3)
		cwd := "/repo/auxiliary"
		*snapshot.Panes = append(*snapshot.Panes, paneJSON{
			PaneID: "w2:p2", TerminalID: "term-auxiliary",
			WorkspaceID: target.Ref.Workspace, TabID: "w2:t2", CWD: &cwd,
			Focused: &focused, AgentStatus: "unknown", Revision: &revision,
		})
	})
	bound, err := h.session.Backend().BindOwnedWorkspaceClose(target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bound.CloseOwned(corebackend.CloseRequest{Ref: corebackend.PaneRef{
		Backend: corebackend.Herdr, Pane: target.Ref.Pane,
	}})
	if !errors.Is(err, corebackend.ErrOwnedWorkspaceHasUnadmittedPane) {
		t.Fatalf("CloseOwned() error = %v, want unadmitted pane rejection", err)
	}
	assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
}

func TestBoundOwnedWorkspaceCloserDoesNotMutateWhenObservationFails(t *testing.T) {
	h := newOwnedHarness(t)
	target := genericWorkspaceCloseTarget(h)
	bound, err := h.session.Backend().BindOwnedWorkspaceClose(target)
	if err != nil {
		t.Fatal(err)
	}
	h.fake.errors["snapshot"] = errors.New("snapshot failed")
	_, err = bound.CloseOwned(corebackend.CloseRequest{Ref: corebackend.PaneRef{
		Backend: corebackend.Herdr, Pane: target.Ref.Pane,
	}})
	if err == nil || errors.Is(err, corebackend.ErrOwnedWorkspaceHasUnadmittedPane) {
		t.Fatalf("CloseOwned() error = %v, want observation failure", err)
	}
	assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
}

func TestBoundOwnedWorkspaceCloserSnapshotFailureTracksCloseDispatch(t *testing.T) {
	for _, afterClose := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_close=%t", afterClose), func(t *testing.T) {
			h := newOwnedHarness(t)
			target := genericWorkspaceCloseTarget(h)
			respondToGenericWorkspaceClose(h, target)
			bound, err := h.session.Backend().BindOwnedWorkspaceClose(target)
			if err != nil {
				t.Fatal(err)
			}
			closeCalls := 0
			respond := h.fake.respond
			h.fake.respond = func(args []string) ([]byte, error) {
				closeCalls++
				out, responseErr := respond(args)
				h.fake.errors["snapshot"] = errors.New("post-close snapshot failed")
				return out, responseErr
			}
			wantCloses := 1
			if !afterClose {
				h.fake.errors["snapshot"] = errors.New("pre-close snapshot failed")
				wantCloses = 0
			}
			result, err := bound.CloseOwned(corebackend.CloseRequest{Ref: corebackend.PaneRef{
				Backend: corebackend.Herdr, Pane: target.Ref.Pane,
			}})
			if err == nil || errors.Is(err, corebackend.ErrOwnedMutationNotIssued) != !afterClose || result.Status != corebackend.CloseFailed {
				t.Fatalf("CloseOwned() = %+v, %v", result, err)
			}
			if closeCalls != wantCloses {
				t.Fatalf("close calls=%d, want %d", closeCalls, wantCloses)
			}
		})
	}
}

func genericWorkspaceCloseTarget(h *ownedHarness) corebackend.OwnedPaneIdentity {
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
	return target
}

func assertNoWorkspaceCloseCommand(t *testing.T, commands []recordedCommand, workspaceID string) {
	t.Helper()
	for _, command := range commands {
		if slices.Equal(command.args, []string{"workspace", "close", workspaceID}) {
			t.Fatalf("workspace close mutation was issued: %v", command.args)
		}
	}
}

func TestBoundOwnedWorkspaceCloserRejectsWorktreeTarget(t *testing.T) {
	h := newOwnedHarness(t)
	_, err := h.session.Backend().BindOwnedWorkspaceClose(h.target())
	if !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
		t.Fatalf("BindOwnedWorkspaceClose() error = %v", err)
	}
}

func TestCloseAttachedWorkspaceAcceptsGenericBinding(t *testing.T) {
	for _, paneLess := range []bool{false, true} {
		for _, metadata := range []bool{false, true} {
			t.Run(fmt.Sprintf("paneLess=%t/metadata=%t", paneLess, metadata), func(t *testing.T) {
				h := newOwnedHarness(t)
				target := h.target()
				binding := ownedPaneBinding(target)
				binding.RepoKey = ""
				h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
					for i := range *snapshot.Panes {
						if (*snapshot.Panes)[i].WorkspaceID == target.Ref.Workspace {
							(*snapshot.Panes)[i].CWD = &h.checkout
						}
					}
					for i := range *snapshot.Workspaces {
						if (*snapshot.Workspaces)[i].WorkspaceID == target.Ref.Workspace && !metadata {
							(*snapshot.Workspaces)[i].Worktree = nil
						}
					}
					if paneLess {
						removeAttachedSnapshotPanes(snapshot, target.Ref.Workspace)
					}
				})
				h.fake.respond = func(args []string) ([]byte, error) {
					if !slices.Equal(args, []string{"workspace", "close", target.Ref.Workspace}) {
						return nil, fmt.Errorf("unexpected close args %v", args)
					}
					h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
						removeAttachedSnapshotPanes(snapshot, target.Ref.Workspace)
						*snapshot.Workspaces = slices.DeleteFunc(*snapshot.Workspaces, func(w workspaceJSON) bool { return w.WorkspaceID == target.Ref.Workspace })
					})
					return nil, nil
				}
				if err := h.session.VerifyAttachedWorkspaceClose(context.Background(), binding); err != nil {
					t.Fatal(err)
				}
				assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
				if err := h.session.CloseAttachedWorkspace(context.Background(), binding); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(h.checkout); err != nil {
					t.Fatalf("checkout lost: %v", err)
				}
			})
		}
	}
}

func removeAttachedSnapshotPanes(snapshot *snapshotJSON, workspace string) {
	*snapshot.Panes = slices.DeleteFunc(*snapshot.Panes, func(p paneJSON) bool { return p.WorkspaceID == workspace })
	*snapshot.Agents = slices.DeleteFunc(*snapshot.Agents, func(a agentJSON) bool { return a.WorkspaceID == workspace })
}

func TestCloseAttachedWorkspaceRejectsChangedGenericBinding(t *testing.T) {
	for name, change := range map[string]func(*corebackend.PaneBinding){
		"session":  func(b *corebackend.PaneBinding) { b.SessionID = "foreign" },
		"socket":   func(b *corebackend.PaneBinding) { b.SocketPath = "/foreign.sock" },
		"terminal": func(b *corebackend.PaneBinding) { b.TerminalID = "foreign" },
		"label":    func(b *corebackend.PaneBinding) { b.WorkspaceLabel = "foreign" },
		"provider": func(b *corebackend.PaneBinding) { b.Agent = "foreign" },
		"agent":    func(b *corebackend.PaneBinding) { b.AgentID = "foreign" },
		"checkout": func(b *corebackend.PaneBinding) { b.WorktreePath = "/foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newOwnedHarness(t)
			target := h.target()
			binding := ownedPaneBinding(target)
			binding.RepoKey = ""
			change(&binding)
			if err := h.session.VerifyAttachedWorkspaceClose(context.Background(), binding); !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
				t.Fatalf("verify = %v", err)
			}
			err := h.session.CloseAttachedWorkspace(context.Background(), binding)
			if !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) || !errors.Is(err, corebackend.ErrMutationNotIssued) {
				t.Fatalf("close = %v", err)
			}
			assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
		})
	}
}

func TestCloseAttachedWorkspaceRejectsAmbiguousGenericWorkspace(t *testing.T) {
	for _, ambiguity := range []string{"label", "extra pane", "repository group"} {
		t.Run(ambiguity, func(t *testing.T) {
			h := newOwnedHarness(t)
			target := h.target()
			binding := ownedPaneBinding(target)
			binding.RepoKey = ""
			h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
				switch ambiguity {
				case "label":
					*snapshot.Workspaces = append(*snapshot.Workspaces, workspaceJSON{WorkspaceID: "duplicate", Label: target.WorkspaceLabel})
				case "extra pane":
					pane := (*snapshot.Panes)[1]
					pane.PaneID, pane.TerminalID = "w2:p2", "extra-terminal"
					*snapshot.Panes = append(*snapshot.Panes, pane)
				case "repository group":
					for i := range *snapshot.Workspaces {
						if (*snapshot.Workspaces)[i].WorkspaceID == target.Ref.Workspace {
							(*snapshot.Workspaces)[i].Worktree.IsLinked = false
						}
					}
					*snapshot.Workspaces = append(*snapshot.Workspaces, workspaceJSON{WorkspaceID: "other", Label: "other", Worktree: &worktreeInfoJSON{RepoKey: h.commonDir, RepoRoot: h.root, CheckoutPath: h.root}})
				}
			})
			if err := h.session.VerifyAttachedWorkspaceClose(context.Background(), binding); err == nil {
				t.Fatal("unsafe workspace passed verification")
			}
			if err := h.session.CloseAttachedWorkspace(context.Background(), binding); !errors.Is(err, corebackend.ErrMutationNotIssued) {
				t.Fatalf("close = %v", err)
			}
			assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
		})
	}
}

func TestCloseAttachedWorkspaceClosesMatchingPaneLessWorkspace(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		panes := slices.DeleteFunc(*snapshot.Panes, func(p paneJSON) bool {
			return p.WorkspaceID == target.Ref.Workspace
		})
		agents := slices.DeleteFunc(*snapshot.Agents, func(a agentJSON) bool {
			return a.WorkspaceID == target.Ref.Workspace
		})
		snapshot.Panes, snapshot.Agents = &panes, &agents
	})
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"workspace", "close", target.Ref.Workspace}) {
			return nil, fmt.Errorf("unexpected close args %v", args)
		}
		h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
			workspaces := slices.DeleteFunc(*snapshot.Workspaces, func(w workspaceJSON) bool {
				return w.WorkspaceID == target.Ref.Workspace
			})
			snapshot.Workspaces = &workspaces
		})
		return nil, nil
	}

	if err := h.session.CloseAttachedWorkspace(context.Background(), ownedPaneBinding(target)); err != nil {
		t.Fatalf("CloseAttachedWorkspace() error = %v", err)
	}
	if _, err := os.Stat(h.checkout); err != nil {
		t.Fatalf("attached workspace close removed checkout: %v", err)
	}
}

func TestCloseAttachedWorkspaceRejectsChangedIdentityBeforeMutation(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	binding := ownedPaneBinding(target)
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Workspaces {
			if (*snapshot.Workspaces)[i].WorkspaceID == target.Ref.Workspace {
				(*snapshot.Workspaces)[i].Label = "replacement-label"
			}
		}
	})
	baseline := len(h.fake.commands)
	err := h.session.CloseAttachedWorkspace(context.Background(), binding)
	if !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) ||
		!errors.Is(err, corebackend.ErrMutationNotIssued) {
		t.Fatalf("CloseAttachedWorkspace() error = %v, want unissued identity mismatch", err)
	}
	for _, command := range h.fake.commands[baseline:] {
		if hasSuffix(command.args, "workspace", "close", target.Ref.Workspace) {
			t.Fatalf("identity mismatch issued workspace close: %v", command.args)
		}
	}
}

func TestCloseAttachedWorkspaceRejectsUnadmittedPaneBeforeMutation(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		focused := false
		revision := uint64(3)
		cwd := "/repo/auxiliary"
		*snapshot.Panes = append(*snapshot.Panes, paneJSON{
			PaneID: "w2:p2", TerminalID: "term-auxiliary",
			WorkspaceID: target.Ref.Workspace, TabID: "w2:t2", CWD: &cwd,
			Focused: &focused, AgentStatus: "unknown", Revision: &revision,
		})
	})

	err := h.session.CloseAttachedWorkspace(context.Background(), ownedPaneBinding(target))
	if !errors.Is(err, corebackend.ErrOwnedWorkspaceHasUnadmittedPane) ||
		!errors.Is(err, corebackend.ErrMutationNotIssued) {
		t.Fatalf("CloseAttachedWorkspace() error = %v, want unissued unadmitted pane rejection", err)
	}
	assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
}

func TestVerifyAttachedWorkspaceCloseRejectsUnadmittedPaneWithoutMutation(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		focused := false
		revision := uint64(3)
		cwd := "/repo/auxiliary"
		*snapshot.Panes = append(*snapshot.Panes, paneJSON{
			PaneID: "w2:p2", TerminalID: "term-auxiliary",
			WorkspaceID: target.Ref.Workspace, TabID: "w2:t2", CWD: &cwd,
			Focused: &focused, AgentStatus: "unknown", Revision: &revision,
		})
	})

	err := h.session.VerifyAttachedWorkspaceClose(context.Background(), ownedPaneBinding(target))
	if !errors.Is(err, corebackend.ErrOwnedWorkspaceHasUnadmittedPane) {
		t.Fatalf("VerifyAttachedWorkspaceClose() error = %v, want unadmitted pane rejection", err)
	}
	assertNoWorkspaceCloseCommand(t, h.fake.commands, target.Ref.Workspace)
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
