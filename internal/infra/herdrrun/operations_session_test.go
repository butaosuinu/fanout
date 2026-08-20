package herdrrun

import (
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

// The owned focus / peek / send / close gate follows the provider's current
// conversation instead of freezing the first observed one, while every other
// identity component still has to match exactly.
func TestEqualOwnedPaneAdmitsReplacedConversation(t *testing.T) {
	session := func(agent, value string) *corebackend.AgentSessionRef {
		return &corebackend.AgentSessionRef{
			Source: corebackend.AgentSessionSource(agent), Agent: agent, Kind: "id", Value: value,
		}
	}
	base := corebackend.OwnedPaneIdentity{
		Ref:        corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		SessionID:  "owned-session",
		SocketPath: "/owned/herdr.sock", WorkspaceLabel: "label-1", TerminalID: "terminal-1",
		RepoKey: "/repo/.git", WorktreePath: "/repo/wt", CurrentPath: "/repo/wt",
		Agent: "claude", AgentID: "fanout-agent", AgentSession: session("claude", "first"),
	}
	tests := []struct {
		name           string
		mutateRecorded func(*corebackend.OwnedPaneIdentity)
		mutate         func(*corebackend.OwnedPaneIdentity)
		want           bool
	}{
		{name: "identical identity", want: true},
		{
			name: "same provider replaced its conversation",
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				i.AgentSession = session("claude", "second")
			},
			want: true,
		},
		{
			name:           "row has not bound a conversation yet",
			mutateRecorded: func(i *corebackend.OwnedPaneIdentity) { i.AgentSession = nil },
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				i.AgentSession = session("claude", "second")
			},
			want: true,
		},
		{
			// The pane's own provider is compared before the conversation, so a
			// reference issued for another provider cannot reach this row even
			// while it has none of its own recorded.
			name: "another provider's conversation",
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				i.AgentSession = session("codex", "second")
			},
		},
		{
			name:           "another provider's conversation on an unbound row",
			mutateRecorded: func(i *corebackend.OwnedPaneIdentity) { i.AgentSession = nil },
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				i.AgentSession = session("codex", "second")
			},
		},
		{
			name: "recorded provider changed under the same conversation",
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				i.Agent = "codex"
			},
		},
		{
			name: "conversation the runtime did not issue",
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				ref := *session("claude", "second")
				ref.Source = "foreign:claude"
				i.AgentSession = &ref
			},
		},
		{
			name: "malformed conversation",
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				ref := *session("claude", "second")
				ref.Kind = "thread"
				i.AgentSession = &ref
			},
		},
		{
			// The per-launch agent record is the fence the conversation is not.
			name: "agent record changed under the same conversation",
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				i.AgentID = "fanout-agent-other"
			},
		},
		{
			name: "terminal changed under the same conversation",
			mutate: func(i *corebackend.OwnedPaneIdentity) {
				i.TerminalID = "terminal-2"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorded := base
			if tt.mutateRecorded != nil {
				tt.mutateRecorded(&recorded)
			}
			current := base
			if tt.mutate != nil {
				tt.mutate(&current)
			}
			if got := equalOwnedPane(recorded, current); got != tt.want {
				t.Fatalf("equalOwnedPane(recorded=%+v, current=%+v) = %t, want %t",
					recorded.AgentSession, current.AgentSession, got, tt.want)
			}
		})
	}
}
