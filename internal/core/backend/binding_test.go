package backend

import "testing"

// boundPane is a complete agent row: every identity component the matcher
// requires is recorded, so a case can remove exactly one of them.
func boundPane() PaneBinding {
	return PaneBinding{
		Row:       PaneRowKey{Parent: "528", IssueNum: 529},
		Ref:       PaneRef{Backend: Herdr, Workspace: "workspace-a", Pane: "workspace-a:p1"},
		SessionID: "session-a", SocketPath: "/tmp/herdr-a.sock",
		WorkspaceLabel: "owned-label-a", TerminalID: "terminal-a",
		Agent: "codex", AgentID: "agent-a",
		AgentSession: &AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-value-a",
		},
		RepoKey: "/repo/.git", WorktreePath: "/repo/.fanout/worktrees/child",
		Launch: LaunchGeneration{
			RowKey: "row-a", Nonce: "nonce-a", EmitterNonce: "emitter-a",
			Executable: "/usr/bin/codex", Args: []string{"--sandbox"},
		},
	}
}

func boundLive(b PaneBinding) LivePane {
	var session *AgentSessionRef
	if b.AgentSession != nil {
		cloned := *b.AgentSession
		session = &cloned
	}
	return LivePane{
		Ref: b.Ref, CurrentPath: "/unrelated/saved-cwd",
		WorkspaceLabel: b.WorkspaceLabel, TerminalID: b.TerminalID,
		AgentID: b.AgentID, AgentProvider: b.Agent, AgentSession: session, AgentPresent: true,
		RepoKey: b.RepoKey, ProjectRoot: "/repo", WorktreePath: b.WorktreePath,
		SessionID: b.SessionID, SocketPath: b.SocketPath,
	}
}

func TestPaneBindingMatchesLive(t *testing.T) {
	tests := []struct {
		name        string
		mutateBound func(*PaneBinding)
		mutateLive  func(*LivePane)
		opts        []MatchOption
		want        bool
	}{
		{name: "complete identity matches", want: true},
		{
			name:       "workspace label changed",
			mutateLive: func(l *LivePane) { l.WorkspaceLabel = "foreign-label" },
		},
		{
			name:        "recorded workspace label missing",
			mutateBound: func(b *PaneBinding) { b.WorkspaceLabel = "" },
			mutateLive:  func(l *LivePane) { l.WorkspaceLabel = "" },
		},
		{
			name:       "terminal id changed",
			mutateLive: func(l *LivePane) { l.TerminalID = "terminal-reused" },
		},
		{
			name:        "recorded terminal id missing",
			mutateBound: func(b *PaneBinding) { b.TerminalID = "" },
			mutateLive:  func(l *LivePane) { l.TerminalID = "" },
		},
		{
			name:       "observed terminal id missing",
			mutateLive: func(l *LivePane) { l.TerminalID = "" },
		},
		{
			name:       "workspace changed",
			mutateLive: func(l *LivePane) { l.Ref.Workspace = "workspace-reused" },
		},
		{name: "pane changed", mutateLive: func(l *LivePane) { l.Ref.Pane = "workspace-a:p9" }},
		{name: "session changed", mutateLive: func(l *LivePane) { l.SessionID = "session-reused" }},
		{
			name:       "socket changed",
			mutateLive: func(l *LivePane) { l.SocketPath = "/tmp/herdr-reused.sock" },
		},
		{name: "agent record changed", mutateLive: func(l *LivePane) { l.AgentID = "agent-reused" }},
		{name: "provider changed", mutateLive: func(l *LivePane) { l.AgentProvider = "claude" }},
		{name: "provider missing", mutateLive: func(l *LivePane) { l.AgentProvider = "" }},
		{
			name: "recorded agent disappeared",
			mutateLive: func(l *LivePane) {
				l.AgentID, l.AgentSession, l.AgentPresent = "", nil, false
			},
		},
		{
			name: "conversation changed",
			mutateLive: func(l *LivePane) {
				changed := *l.AgentSession
				changed.Value = "session-value-reused"
				l.AgentSession = &changed
			},
		},
		{name: "observed conversation missing", mutateLive: func(l *LivePane) { l.AgentSession = nil }},
		{
			// A recorded agent with no runtime record cannot be compared at all.
			name:        "recorded agent record missing",
			mutateBound: func(b *PaneBinding) { b.AgentID, b.AgentSession = "", nil },
		},
		{
			name:        "recorded conversation unbound but observed",
			mutateBound: func(b *PaneBinding) { b.AgentSession = nil },
		},
		{
			name:        "neither side recorded a conversation",
			mutateBound: func(b *PaneBinding) { b.AgentSession = nil },
			mutateLive:  func(l *LivePane) { l.AgentSession = nil },
			want:        true,
		},
		{
			name:        "recorded conversation is not a valid runtime reference",
			mutateBound: func(b *PaneBinding) { b.AgentSession.Kind = "name" },
			mutateLive:  func(l *LivePane) { l.AgentSession.Kind = "name" },
		},
		{
			name:        "row without an agent must observe none",
			mutateBound: func(b *PaneBinding) { b.Agent, b.AgentID, b.AgentSession = "", "", nil },
		},
		{
			name:        "row without an agent matches a bare pane",
			mutateBound: func(b *PaneBinding) { b.Agent, b.AgentID, b.AgentSession = "", "", nil },
			mutateLive: func(l *LivePane) {
				l.AgentID, l.AgentProvider, l.AgentSession, l.AgentPresent = "", "", nil, false
			},
			want: true,
		},
		{
			name: "shell row matches a pane with no agent",
			mutateBound: func(b *PaneBinding) {
				b.Shell, b.AgentID, b.AgentSession = true, "", nil
			},
			mutateLive: func(l *LivePane) {
				l.AgentID, l.AgentProvider, l.AgentSession, l.AgentPresent = "", "", nil, false
			},
			want: true,
		},
		{
			name:        "shell row rejects an observed agent",
			mutateBound: func(b *PaneBinding) { b.Shell, b.AgentID, b.AgentSession = true, "", nil },
		},
		{
			name:       "repository identity changed",
			mutateLive: func(l *LivePane) { l.RepoKey = "/other/.git" },
		},
		{
			name:        "recorded repository identity missing against observed provenance",
			mutateBound: func(b *PaneBinding) { b.RepoKey = "" },
			mutateLive:  func(l *LivePane) { l.RepoKey = "" },
		},
		{
			name:       "observed repository identity missing",
			mutateLive: func(l *LivePane) { l.RepoKey = "" },
		},
		{
			name:       "observed project root missing",
			mutateLive: func(l *LivePane) { l.ProjectRoot = "" },
		},
		{
			// Foreground cwd never authorizes a pane that reports provenance.
			name: "worktree provenance changed despite matching saved cwd",
			mutateLive: func(l *LivePane) {
				l.CurrentPath = "/repo/.fanout/worktrees/child"
				l.WorktreePath = "/repo/.fanout/worktrees/reused"
			},
		},
		{
			name:        "recorded worktree path missing",
			mutateBound: func(b *PaneBinding) { b.WorktreePath = "" },
		},
		{
			name:        "workspace without provenance matches its saved cwd",
			mutateBound: func(b *PaneBinding) { b.RepoKey, b.WorktreePath = "", "/repo/saved-cwd" },
			mutateLive: func(l *LivePane) {
				l.RepoKey, l.ProjectRoot, l.WorktreePath = "", "", ""
				l.CurrentPath = "/repo/saved-cwd"
			},
			want: true,
		},
		{
			name:        "workspace without provenance rejects a saved cwd subdirectory",
			mutateBound: func(b *PaneBinding) { b.RepoKey, b.WorktreePath = "", "/repo/saved-cwd" },
			mutateLive: func(l *LivePane) {
				l.RepoKey, l.ProjectRoot, l.WorktreePath = "", "", ""
				l.CurrentPath = "/repo/saved-cwd/subdir"
			},
		},
		{
			name: "required runtime rejects another recorded runtime",
			mutateBound: func(b *PaneBinding) {
				b.Ref.Backend = Tmux
			},
			mutateLive: func(l *LivePane) { l.Ref.Backend = Tmux },
			opts:       []MatchOption{RequireRuntime(Herdr)},
		},
		{
			name: "required runtime accepts the recorded runtime",
			opts: []MatchOption{RequireRuntime(Herdr)},
			want: true,
		},
		{
			name:        "first bind admits an unrecorded conversation",
			mutateBound: func(b *PaneBinding) { b.AgentSession = nil },
			opts:        []MatchOption{RequireRuntime(Herdr), AllowUnboundAgentSession()},
			want:        true,
		},
		{
			name:        "first bind still requires a reference issued for the provider",
			mutateBound: func(b *PaneBinding) { b.AgentSession = nil },
			mutateLive:  func(l *LivePane) { l.AgentSession.Source = "foreign:codex" },
			opts:        []MatchOption{RequireRuntime(Herdr), AllowUnboundAgentSession()},
		},
		{
			name:        "first bind rejects a pane with no conversation at all",
			mutateBound: func(b *PaneBinding) { b.AgentSession = nil },
			mutateLive:  func(l *LivePane) { l.AgentSession = nil },
			opts:        []MatchOption{RequireRuntime(Herdr), AllowUnboundAgentSession()},
		},
		{
			// The recorded conversation is not consulted while first-binding, so a
			// row already bound elsewhere still counts as a claimant.
			name:       "first bind ignores the recorded conversation",
			mutateLive: func(l *LivePane) { l.AgentSession.Value = "session-value-late" },
			opts:       []MatchOption{RequireRuntime(Herdr), AllowUnboundAgentSession()},
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bound := boundPane()
			live := boundLive(bound)
			if tt.mutateBound != nil {
				tt.mutateBound(&bound)
			}
			if tt.mutateLive != nil {
				tt.mutateLive(&live)
			}
			if got := bound.MatchesLive(live, tt.opts...); got != tt.want {
				t.Fatalf("MatchesLive(%+v) = %t, want %t", live, got, tt.want)
			}
		})
	}
}

func TestPaneBindingUniqueLive(t *testing.T) {
	bound := boundPane()
	live := boundLive(bound)
	other := live
	other.Ref.Pane = "workspace-a:p2"

	tests := []struct {
		name  string
		panes []LivePane
		want  bool
	}{
		{name: "no observation", want: false},
		{name: "one observation", panes: []LivePane{other, live}, want: true},
		{name: "ambiguous observations", panes: []LivePane{live, live}, want: false},
		{name: "no matching observation", panes: []LivePane{other}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, ok := bound.UniqueLive(tt.panes)
			if ok != tt.want {
				t.Fatalf("UniqueLive(%d panes) ok = %t, want %t", len(tt.panes), ok, tt.want)
			}
			if ok && matched.Ref != live.Ref {
				t.Fatalf("UniqueLive() matched %+v, want %+v", matched.Ref, live.Ref)
			}
		})
	}
}

func TestPaneBindingEqual(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PaneBinding)
		want   bool
	}{
		{name: "same projection", want: true},
		{name: "row parent", mutate: func(b *PaneBinding) { b.Row.Parent = "999" }},
		{name: "row issue", mutate: func(b *PaneBinding) { b.Row.IssueNum = 999 }},
		{name: "row task", mutate: func(b *PaneBinding) { b.Row.TaskID = "task-9" }},
		{name: "recorded runtime", mutate: func(b *PaneBinding) { b.Ref.Backend = Tmux }},
		{name: "workspace", mutate: func(b *PaneBinding) { b.Ref.Workspace = "workspace-b" }},
		{name: "pane", mutate: func(b *PaneBinding) { b.Ref.Pane = "workspace-a:p2" }},
		{name: "session", mutate: func(b *PaneBinding) { b.SessionID = "session-b" }},
		{name: "socket", mutate: func(b *PaneBinding) { b.SocketPath = "/tmp/herdr-b.sock" }},
		{name: "workspace label", mutate: func(b *PaneBinding) { b.WorkspaceLabel = "owned-label-b" }},
		{name: "terminal", mutate: func(b *PaneBinding) { b.TerminalID = "terminal-b" }},
		{name: "provider", mutate: func(b *PaneBinding) { b.Agent = "claude" }},
		{name: "agent record", mutate: func(b *PaneBinding) { b.AgentID = "agent-b" }},
		{name: "conversation", mutate: func(b *PaneBinding) { b.AgentSession = nil }},
		{name: "repository", mutate: func(b *PaneBinding) { b.RepoKey = "/other/.git" }},
		{name: "worktree", mutate: func(b *PaneBinding) { b.WorktreePath = "/repo/other" }},
		{name: "launch row key", mutate: func(b *PaneBinding) { b.Launch.RowKey = "row-b" }},
		{name: "launch nonce", mutate: func(b *PaneBinding) { b.Launch.Nonce = "nonce-b" }},
		{name: "emitter nonce", mutate: func(b *PaneBinding) { b.Launch.EmitterNonce = "emitter-b" }},
		{name: "launch executable", mutate: func(b *PaneBinding) { b.Launch.Executable = "/bin/other" }},
		{name: "launch args", mutate: func(b *PaneBinding) { b.Launch.Args = []string{"--other"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right := boundPane(), boundPane()
			if tt.mutate != nil {
				tt.mutate(&right)
			}
			if got := left.Equal(right); got != tt.want {
				t.Fatalf("Equal(%+v) = %t, want %t", right, got, tt.want)
			}
		})
	}
}

func TestExpectedAgentSession(t *testing.T) {
	valid := &AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "v"}
	tests := []struct {
		name     string
		ref      *AgentSessionRef
		provider string
		want     bool
	}{
		{name: "reference issued for the provider", ref: valid, provider: "codex", want: true},
		{name: "absent reference", provider: "codex"},
		{
			name:     "foreign source",
			ref:      &AgentSessionRef{Source: "foreign:codex", Agent: "codex", Kind: "id", Value: "v"},
			provider: "codex",
		},
		{name: "provider mismatch", ref: valid, provider: "claude"},
		{
			name:     "unsupported reference kind",
			ref:      &AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "name", Value: "v"},
			provider: "codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExpectedAgentSession(tt.ref, tt.provider); got != tt.want {
				t.Fatalf("ExpectedAgentSession(%+v, %q) = %t, want %t", tt.ref, tt.provider, got, tt.want)
			}
		})
	}
}
