package herdrrun

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
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

// A provider that restarts its conversation in place makes the runtime hold
// the agent record without a name. The row keeps working, and fanout puts its
// own name back the next time it mutates the pane.
func TestFocusOwnedRestoresDroppedAgentName(t *testing.T) {
	h := newOwnedHarness(t)
	minted := naming.ManagedAgentName("/repo/.git", "row", strings.Repeat("a", 32))
	setAgentName(h, "w2:p1", minted)
	target := h.target()
	setAgentUnnamed(h, "w2:p1")

	renamed := 0
	h.fake.respond = func(args []string) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"agent", "rename", target.Ref.Pane, minted}):
			renamed++
			setAgentName(h, "w2:p1", minted)
			return []byte(`{"id":"cli:agent:rename","result":{"type":"ok"}}`), nil
		case slices.Equal(args, []string{"agent", "focus", target.Ref.Pane}):
			focusOwnedTestPane(h, target.Ref.Pane)
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected mutation args %v", args)
	}
	bound, err := h.session.Backend().BindOwnedTarget(target)
	if err != nil {
		t.Fatalf("BindOwnedTarget() on an unnamed agent record = %v, want admitted", err)
	}
	if renamed != 0 {
		t.Fatal("binding renamed the agent; only a mutation may rename")
	}
	if err := bound.Focus(target.Ref); err != nil {
		t.Fatalf("Focus() after dropped agent name = %v, want recovery", err)
	}
	if renamed != 1 {
		t.Fatalf("agent rename issued %d times, want exactly 1", renamed)
	}
}

// Reads are served through the same admission, and the dashboard exposes them
// on GET routes, so nothing on that path may rename anything.
func TestReadOwnedNeverRenamesAgent(t *testing.T) {
	h := newOwnedHarness(t)
	minted := naming.ManagedAgentName("/repo/.git", "row", strings.Repeat("a", 32))
	setAgentName(h, "w2:p1", minted)
	target := h.target()
	setAgentUnnamed(h, "w2:p1")

	h.fake.respond = func(args []string) ([]byte, error) {
		if slices.Equal(args, []string{"pane", "read", "w2:p1", "--source", "visible", "--format", "text"}) {
			return []byte("viewport" + "\n"), nil
		}
		return nil, fmt.Errorf("read path issued a mutation: %v", args)
	}
	bound, err := h.session.Backend().BindOwnedTarget(target)
	if err != nil {
		t.Fatalf("BindOwnedTarget() on an unnamed agent record = %v, want admitted", err)
	}
	if _, err := bound.Read(target.Ref, 0); err != nil {
		t.Fatalf("Read() on an unnamed agent record = %v, want the pane contents", err)
	}
}

// The rename repairs fanout's own invariant; it never claims a record that
// answers to something else, and never asserts a name fanout did not mint.
func TestFocusOwnedRefusesToClaimAnotherAgentName(t *testing.T) {
	minted := naming.ManagedAgentName("/repo/.git", "row", strings.Repeat("a", 32))
	tests := []struct {
		name string
		// recordMinted names the agent before the target is taken, so the row
		// records a name fanout would otherwise be allowed to re-assert.
		recordMinted bool
		drift        func(*ownedHarness)
		wantAdmitted bool
	}{
		{
			name:         "live agent answers to its own name",
			recordMinted: true,
			drift:        func(h *ownedHarness) { setAgentName(h, "w2:p1", "someone-else") },
		},
		{
			// The fixture's "fanout-child" shares the prefix but not the shape
			// ManagedAgentName mints, so it is not fanout's to re-assert.
			name:  "recorded name is not one fanout minted",
			drift: func(h *ownedHarness) { setAgentUnnamed(h, "w2:p1") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newOwnedHarness(t)
			if tt.recordMinted {
				setAgentName(h, "w2:p1", minted)
			}
			target := h.target()
			tt.drift(h)
			h.fake.respond = func(args []string) ([]byte, error) {
				return nil, fmt.Errorf("unexpected mutation args %v", args)
			}
			if _, err := h.session.Backend().BindOwnedTarget(target); err == nil {
				t.Fatal("BindOwnedTarget() succeeded, want identity mismatch")
			}
		})
	}
}

// The prompt response is checked after the prompt was delivered, so it admits
// the same record the preflight did. Refusing an anonymous record there turns a
// delivered nudge into a reported failure, and the retry sends it twice.
func TestAgentPromptResponseAdmitsUnnamedRecord(t *testing.T) {
	minted := naming.ManagedAgentName("/repo/.git", "row", strings.Repeat("a", 32))
	h := newOwnedHarness(t)
	setAgentName(h, "w2:p1", minted)
	target := h.target()

	if target.Agent != "codex" {
		t.Fatalf("fixture provider = %q, want codex so the other-provider case differs", target.Agent)
	}
	tests := []struct {
		name    string
		mutate  func(*agentJSON)
		wantErr bool
	}{
		{name: "record still answers to the recorded name"},
		{
			name:   "runtime dropped the name",
			mutate: func(a *agentJSON) { a.Name = nil },
		},
		{
			name: "record answers to another name",
			mutate: func(a *agentJSON) {
				other := "someone-else"
				a.Name = &other
			},
			wantErr: true,
		},
		{
			// An anonymous record is admitted on the recorded name's shape, so
			// the provider is what keeps a prompt delivered to a different
			// agent from being reported as a success.
			name: "anonymous record belongs to another provider",
			mutate: func(a *agentJSON) {
				other := "claude"
				a.Name, a.Agent = nil, &other
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAgentPromptResponse(agentPromptResponse(target, tt.mutate), target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateAgentPromptResponse() = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func focusOwnedTestPane(h *ownedHarness, paneID string) {
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Panes {
			focused := (*snapshot.Panes)[i].PaneID == paneID
			(*snapshot.Panes)[i].Focused = &focused
		}
		for i := range *snapshot.Agents {
			focused := (*snapshot.Agents)[i].PaneID == paneID
			(*snapshot.Agents)[i].Focused = &focused
		}
	})
}

func setAgentUnnamed(h *ownedHarness, paneID string) {
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Agents {
			if (*snapshot.Agents)[i].PaneID == paneID {
				(*snapshot.Agents)[i].Name = nil
			}
		}
	})
}

func setAgentName(h *ownedHarness, paneID, name string) {
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Agents {
			if (*snapshot.Agents)[i].PaneID == paneID {
				(*snapshot.Agents)[i].Name = &name
			}
		}
	})
}
