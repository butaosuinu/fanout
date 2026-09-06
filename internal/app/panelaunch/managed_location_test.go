package panelaunch

import (
	"errors"
	"reflect"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestReconcileManagedPaneLocationAdoptsUniqueMovedWorkspace(t *testing.T) {
	pane := managedLocationPane()
	moved := managedLocationWorkspace(pane, "workspace-next", "pane-next", "terminal-next")
	want := pane
	want.WorkspaceID, want.PaneID, want.TerminalID = moved.WorkspaceID, moved.Pane.Pane, moved.TerminalID

	got, changed, err := ReconcileManagedPaneLocation(pane, []backend.WorkspaceObservation{moved})
	if err != nil || !changed {
		t.Fatalf("ReconcileManagedPaneLocation() changed=%t err=%v", changed, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconciled pane = %#v, want only location changed to %#v", got, want)
	}
}

func TestReconcileManagedPaneLocationRefusesUnsafeMatches(t *testing.T) {
	pane := managedLocationPane()
	moved := managedLocationWorkspace(pane, "workspace-next", "pane-next", "terminal-next")
	duplicate := managedLocationWorkspace(pane, "workspace-other", "pane-other", "terminal-other")
	wrongRepo := moved
	wrongRepo.RepoKey = "/repo/foreign.git"

	for _, test := range []struct {
		name       string
		workspaces []backend.WorkspaceObservation
		wantErr    bool
	}{
		{name: "zero matches", workspaces: nil},
		{name: "duplicate labels", workspaces: []backend.WorkspaceObservation{moved, duplicate}, wantErr: true},
		{name: "provenance mismatch", workspaces: []backend.WorkspaceObservation{wrongRepo}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, changed, err := ReconcileManagedPaneLocation(pane, test.workspaces)
			if changed || !reflect.DeepEqual(got, pane) {
				t.Fatalf("unsafe match changed pane: changed=%t pane=%#v", changed, got)
			}
			if test.wantErr != errors.Is(err, backend.ErrOwnedIdentityMismatch) {
				t.Fatalf("identity mismatch error = %v, want %t", err, test.wantErr)
			}
		})
	}
}

func TestReconcileManagedPaneLocationProjectsUniquePaneAtCheckout(t *testing.T) {
	pane := managedLocationPane()
	moved := managedLocationWorkspace(pane, "workspace-next", "pane-next", "terminal-next")
	moved.Pane, moved.TerminalID, moved.CWD = backend.PaneRef{}, "", ""
	moved.Panes = append(moved.Panes, backend.WorkspacePaneObservation{
		Pane:       backend.PaneRef{Backend: backend.Herdr, Workspace: moved.WorkspaceID, Pane: "pane-other"},
		TerminalID: "terminal-other",
		CWD:        "/repo/other",
	})

	got, changed, err := ReconcileManagedPaneLocation(pane, []backend.WorkspaceObservation{moved})
	if err != nil || !changed || got.PaneID != "pane-next" || got.TerminalID != "terminal-next" {
		t.Fatalf("projected location = %#v changed=%t err=%v", got, changed, err)
	}
}

func TestReconcileManagedPaneLocationLeavesAtomicRuntimeRowsUnchanged(t *testing.T) {
	pane := managedLocationPane()
	pane.Backend = backend.Tmux
	moved := managedLocationWorkspace(pane, "workspace-next", "pane-next", "terminal-next")

	got, changed, err := ReconcileManagedPaneLocation(pane, []backend.WorkspaceObservation{moved})
	if err != nil || changed || !reflect.DeepEqual(got, pane) {
		t.Fatalf("atomic row changed: changed=%t pane=%#v err=%v", changed, got, err)
	}
}

func managedLocationPane() state.Pane {
	return state.Pane{
		Parent: "740", IssueNum: 741, Backend: backend.Herdr,
		WorkspaceID: "workspace-old", WorkspaceLabel: "fanout-agent-nonce",
		PaneID: "pane-old", TerminalID: "terminal-old",
		RepoKey: "/repo/.git", RepoRoot: "/repo", WorktreePath: "/repo/.fanout/worktrees/child",
		SessionID: "session-owned", SocketPath: "/tmp/herdr-owned.sock", Agent: "codex",
		EmitterRowKey: "row-key", LaunchNonce: "launch-nonce", BranchName: "fanout/child",
	}
}

func managedLocationWorkspace(
	pane state.Pane,
	workspaceID, paneID, terminalID string,
) backend.WorkspaceObservation {
	ref := backend.PaneRef{Backend: backend.Herdr, Workspace: workspaceID, Pane: paneID}
	return backend.WorkspaceObservation{
		WorkspaceID: workspaceID, Label: pane.WorkspaceLabel,
		Path: pane.WorktreePath, RepoKey: pane.RepoKey, RepoRoot: pane.RepoRoot,
		Pane: ref, TerminalID: terminalID, CWD: pane.WorktreePath,
		Panes: []backend.WorkspacePaneObservation{{Pane: ref, TerminalID: terminalID, CWD: pane.WorktreePath}},
	}
}
