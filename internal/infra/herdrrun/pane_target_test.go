package herdrrun

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
)

// A provider that restarts its conversation in place makes the runtime hold
// the agent record without a name. The row keeps working, and fanout puts its
// own name back the next time it mutates the pane.
func TestFocusOwnedRestoresDroppedAgentName(t *testing.T) {
	h := newOwnedHarness(t)
	minted := naming.ManagedAgentName("/repo/.git", "row", strings.Repeat("a", 32))
	setAgentName(h, "w2:p1", minted)
	target := h.target()
	setAgentUnnamed(h, "w2:p1")
	replaceAgentSession(h, "w2:p1", "session-after-clear")

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

func TestReadOwnedCoordinatorWithLaterCheckoutProvenance(t *testing.T) {
	for _, tt := range []struct {
		name         string
		checkoutPath string
		want         bool
	}{
		{name: "no checkout metadata", want: true},
		{name: "same checkout", checkoutPath: "/repo", want: true},
		{name: "other checkout", checkoutPath: "/other"},
		{name: "checkout subdirectory", checkoutPath: "/repo/subdir"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newOwnedHarness(t)
			target := corebackend.OwnedPaneIdentity{
				Ref:       corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1"},
				SessionID: h.session.Session, SocketPath: h.session.SocketPath,
				WorkspaceLabel: "root", TerminalID: "term-root", CurrentPath: "/repo",
			}
			bound, err := h.session.Backend().BindOwnedTarget(target)
			if err != nil {
				t.Fatal(err)
			}
			if tt.checkoutPath != "" {
				h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
					(*snapshot.Workspaces)[0].Worktree = &worktreeInfoJSON{
						RepoKey: h.commonDir, RepoRoot: "/repo", CheckoutPath: tt.checkoutPath,
					}
				})
			}
			reads := 0
			h.fake.respond = func(args []string) ([]byte, error) {
				if slices.Equal(args, []string{"pane", "read", target.Ref.Pane, "--source", "visible", "--format", "text"}) {
					reads++
					return []byte("coordinator\n"), nil
				}
				return nil, fmt.Errorf("unexpected command: %v", args)
			}
			_, bindErr := h.session.Backend().BindOwnedTarget(target)
			text, readErr := bound.Read(target.Ref, 0)
			if tt.want {
				if bindErr != nil || readErr != nil || text != "coordinator\n" || reads != 1 {
					t.Fatalf("bind=%v read=%v text=%q reads=%d", bindErr, readErr, text, reads)
				}
			} else if !errors.Is(bindErr, corebackend.ErrOwnedIdentityMismatch) ||
				!errors.Is(readErr, corebackend.ErrOwnedIdentityMismatch) || reads != 0 {
				t.Fatalf("mismatched checkout admitted: bind=%v read=%v reads=%d", bindErr, readErr, reads)
			}
		})
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

// A bound backend runs on a copy of the session's transport: the injected
// command runner and the binary admissions it already proved carry over.
func TestBindOwnedTargetKeepsInjectedTransport(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	source := h.session.Backend()
	bound, err := source.BindOwnedTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	if admitted := bound.(*boundBackend).admitted; len(source.admitted) == 0 || !maps.Equal(admitted, source.admitted) {
		t.Fatalf("bound admitted = %v, want session admissions %v", admitted, source.admitted)
	}
	h.fake.respond = func(args []string) ([]byte, error) {
		if slices.Equal(args, []string{"pane", "read", target.Ref.Pane, "--source", "visible", "--format", "text"}) {
			return []byte("from fake\n"), nil
		}
		return nil, fmt.Errorf("unexpected args %v", args)
	}
	if content, err := bound.Read(target.Ref, 0); err != nil || content != "from fake\n" {
		t.Fatalf("bound Read() = %q, %v, want the injected fake output", content, err)
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

func replaceAgentSession(h *ownedHarness, paneID, value string) {
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Panes {
			pane := &(*snapshot.Panes)[i]
			if pane.PaneID == paneID && pane.AgentSession != nil {
				pane.AgentSession.Value = &value
			}
		}
		for i := range *snapshot.Agents {
			agent := &(*snapshot.Agents)[i]
			if agent.PaneID == paneID && agent.AgentSession != nil {
				agent.AgentSession.Value = &value
			}
		}
	})
}
