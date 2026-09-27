package herdrrun

import (
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

func TestListLiveProjectsSnapshotWithoutUsingForegroundCWD(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)

	got, err := b.ListLive()
	if err != nil {
		t.Fatalf("ListLive() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(ListLive()) = %d, want 2", len(got))
	}
	root := got[0]
	if root.Ref != (corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1"}) {
		t.Fatalf("root Ref = %#v", root.Ref)
	}
	if !root.FocusKnown || !root.Focused {
		t.Fatalf("root focus projection = known:%t focused:%t, want true/true", root.FocusKnown, root.Focused)
	}
	if root.CurrentPath != "/repo" || root.NativeAgentState != "unknown" || root.AgentState != "" || root.AgentPresent || root.AgentSession != nil || root.TerminalID != "term-root" || root.RepoKey != "" || root.SessionID != session || root.SocketPath != socket {
		t.Fatalf("root live pane = %#v", root)
	}
	child := got[1]
	if !child.FocusKnown || child.Focused {
		t.Fatalf("child focus projection = known:%t focused:%t, want true/false", child.FocusKnown, child.Focused)
	}
	if child.CurrentPath != "/repo/.fanout/worktrees/child" {
		t.Fatalf("child CurrentPath = %q, want worktree checkout path", child.CurrentPath)
	}
	if child.RepoKey != "/repo/.git" || child.ProjectRoot != "/repo" || child.WorktreePath != "/repo/.fanout/worktrees/child" {
		t.Fatalf("child worktree projection = %#v", child)
	}
	if child.AgentState != corebackend.AgentWorking || child.NativeAgentState != "working" || child.AgentID != "fanout-child" || child.AgentProvider != "codex" || !child.AgentPresent || child.Focused || child.Title != "child title" || child.SocketPath != socket {
		t.Fatalf("child agent projection = %#v", child)
	}
	wantSession := corebackend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-a"}
	if child.AgentSession == nil || *child.AgentSession != wantSession {
		t.Fatalf("child agent session = %#v, want %#v", child.AgentSession, wantSession)
	}
	if gotCalls := len(fake.commands); gotCalls != 3 || commandKey(fake.commands[2].args) != "snapshot" {
		t.Fatalf("ListLive() calls = %#v", fake.commands)
	}
}

func TestListLiveProjectsRestoredSessionWithoutLiveAgent(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.snapshot = strings.Replace(fake.snapshot, `      "agents":[
        {"terminal_id":"term-child","name":"fanout-child","agent":"codex","agent_status":"working","workspace_id":"w2","tab_id":"w2:t1","pane_id":"w2:p1","focused":false,"cwd":"/wrong-saved-cwd","foreground_cwd":"/tmp/other-foreground","revision":2,"agent_session":{"source":"herdr:codex","agent":"codex","kind":"id","value":"session-a"}}
      ]`, `      "agents":[]`, 1)
	b := newTestBackend(t, session, socket, fake)

	got, err := b.ListLive()
	if err != nil {
		t.Fatal(err)
	}
	child := got[1]
	want := corebackend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-a",
	}
	if child.AgentPresent || child.AgentID != "" || child.AgentProvider != "" ||
		child.AgentState != "" || child.AgentSession == nil || *child.AgentSession != want {
		t.Fatalf("restored shell placeholder = %#v", child)
	}
}

func TestListLiveProjectsDuplicateRestoredPlaceholdersForCallerRejection(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.snapshot = strings.Replace(fake.snapshot, `"agent_status":"unknown","revision":1`,
		`"agent_status":"unknown","revision":1,"agent_session":{"source":"herdr:codex","agent":"codex","kind":"id","value":"session-a"}`, 1)
	fake.snapshot = strings.Replace(fake.snapshot, `      "agents":[
        {"terminal_id":"term-child","name":"fanout-child","agent":"codex","agent_status":"working","workspace_id":"w2","tab_id":"w2:t1","pane_id":"w2:p1","focused":false,"cwd":"/wrong-saved-cwd","foreground_cwd":"/tmp/other-foreground","revision":2,"agent_session":{"source":"herdr:codex","agent":"codex","kind":"id","value":"session-a"}}
      ]`, `      "agents":[]`, 1)
	b := newTestBackend(t, session, socket, fake)

	got, err := b.ListLive()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].AgentSession == nil || got[1].AgentSession == nil ||
		*got[0].AgentSession != *got[1].AgentSession || got[0].AgentPresent || got[1].AgentPresent {
		t.Fatalf("duplicate restored placeholders = %#v", got)
	}
}

func TestListLiveRejectsMalformedOrIncompatibleSnapshot(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{
			name: "unexpected envelope id",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"id":"cli:api:snapshot"`, `"id":"other"`, 1)
			},
			wantErr: "unexpected herdr snapshot envelope",
		},
		{
			name: "future version",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"version":"0.7.5"`, `"version":"0.7.6"`, 1)
			},
			wantErr: "unsupported herdr snapshot tuple",
		},
		{
			name: "missing required agents collection",
			mutate: func(snapshot string) string {
				prefix, _, ok := strings.Cut(snapshot, `      "agents":[`)
				if !ok {
					t.Fatal("snapshot fixture is missing agents collection")
				}
				return prefix + "      \"unused\":[]\n    }\n  }\n}\n"
			},
			wantErr: "missing a required collection",
		},
		{
			name: "pane missing required focused field",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"tab_id":"w1:t1","focused":true,"cwd":"/repo"`, `"tab_id":"w1:t1","cwd":"/repo"`, 1)
			},
			wantErr: "pane with incomplete identity",
		},
		{
			name: "worktree missing repo key",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"repo_key":"/repo/.git"`, `"repo_key":""`, 1)
			},
			wantErr: "incomplete worktree provenance",
		},
		{
			name: "worktree missing checkout path",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"checkout_path":"/repo/.fanout/worktrees/child"`, `"checkout_path":""`, 1)
			},
			wantErr: "incomplete worktree provenance",
		},
		{
			name: "worktree missing repo root",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"repo_root":"/repo"`, `"repo_root":""`, 1)
			},
			wantErr: "incomplete worktree provenance",
		},
		{
			name: "pane missing required revision field",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `,"revision":1`, ``, 1)
			},
			wantErr: "pane with incomplete identity",
		},
		{
			name: "duplicate pane id",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"pane_id":"w2:p1"`, `"pane_id":"w1:p1"`, 1)
			},
			wantErr: "duplicate pane id",
		},
		{
			name: "duplicate logical conversation",
			mutate: func(snapshot string) string {
				return strings.Replace(
					snapshot,
					`"agent_status":"unknown","revision":1`,
					`"agent_status":"unknown","revision":1,"agent_session":{"source":"herdr:codex","agent":"codex","kind":"id","value":"session-a"}`,
					1,
				)
			},
			wantErr: "duplicate agent session refs",
		},
		{
			name: "unknown native state",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"title":"child title","agent":"codex","agent_status":"working"`, `"title":"child title","agent":"codex","agent_status":"future"`, 1)
			},
			wantErr: "unknown agent status",
		},
		{
			name: "agent disagrees with pane",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"terminal_id":"term-child","name":"fanout-child"`, `"terminal_id":"other-terminal","name":"fanout-child"`, 1)
			},
			wantErr: "agent identity disagrees",
		},
		{
			name: "agent session ref disagrees with pane",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"value":"session-a"`, `"value":"session-b"`, 1)
			},
			wantErr: "agent session ref disagrees",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			fake.snapshot = tt.mutate(fake.snapshot)
			b := newTestBackend(t, session, socket, fake)
			_, err := b.ListLive()
			if err == nil || err.Error() != methodUnavailable("session.snapshot").Error() {
				t.Fatalf("ListLive() error = %v, want generic unavailable error after %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidNativeAgentState(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "working", want: true},
		{raw: "blocked", want: true},
		{raw: "idle", want: true},
		{raw: "done", want: true},
		{raw: "unknown", want: true},
		{raw: "running", want: false},
	}
	for _, tt := range tests {
		if got := validNativeAgentState(tt.raw); got != tt.want {
			t.Errorf("validNativeAgentState(%q) = %t, want %t", tt.raw, got, tt.want)
		}
	}
}
