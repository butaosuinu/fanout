package panelaunch

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestReconcileManagedPaneLocationAdoptsUniqueMovedWorkspace(t *testing.T) {
	pane := managedLocationPane()
	moved := managedLocationWorkspace(pane, "workspace-next", "pane-next", "terminal-next")
	want := pane
	want.WorkspaceID, want.PaneID, want.TerminalID = moved.WorkspaceID, moved.Pane.Pane, moved.TerminalID
	want.ReportedState, want.ReportedStateSeq, want.StateRefinement = "", 0, false

	got, changed, err := ReconcileManagedPaneLocation(pane, []backend.WorkspaceObservation{moved})
	if err != nil || !changed {
		t.Fatalf("ReconcileManagedPaneLocation() changed=%t err=%v", changed, err)
	}
	if got.EmitterNonce == pane.EmitterNonce || !telemetry.ValidNonce(got.EmitterNonce) {
		t.Fatalf("emitter nonce = %q, want a fresh valid nonce", got.EmitterNonce)
	}
	want.EmitterNonce = got.EmitterNonce
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconciled pane = %#v, want location and telemetry fence %#v", got, want)
	}
}

func TestReconcileManagedPaneLocationAdmitsLateSameProviderSession(t *testing.T) {
	pane := managedLocationPane()
	moved := managedLocationWorkspace(pane, "workspace-next", "pane-next", "terminal-next")
	late := *pane.AgentSession
	late.Value = "session-late"
	moved.LivePanes[0].AgentSession = &late

	got, changed, err := ReconcileManagedPaneLocation(pane, []backend.WorkspaceObservation{moved})
	if err != nil || !changed || got.WorkspaceID != moved.WorkspaceID {
		t.Fatalf("late session reconciliation = %#v changed=%t err=%v", got, changed, err)
	}
}

func TestReconcileManagedPaneLocationRequiresCompleteSavedAgentIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*state.Pane)
	}{
		{name: "missing agent ID", change: func(pane *state.Pane) { pane.AgentID = "" }},
		{name: "missing agent session", change: func(pane *state.Pane) { pane.AgentSession = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pane := managedLocationPane()
			test.change(&pane)
			moved := managedLocationWorkspace(pane, "workspace-next", "pane-next", "terminal-next")

			got, changed, err := ReconcileManagedPaneLocation(pane, []backend.WorkspaceObservation{moved})
			if changed || !reflect.DeepEqual(got, pane) {
				t.Fatalf("incomplete identity changed pane: changed=%t pane=%#v", changed, got)
			}
			if !errors.Is(err, backend.ErrOwnedIdentityMismatch) {
				t.Fatalf("incomplete identity error = %v, want identity mismatch", err)
			}
		})
	}
}

func TestReconcileManagedPaneLocationRefusesUnsafeMatches(t *testing.T) {
	pane := managedLocationPane()
	moved := managedLocationWorkspace(pane, "workspace-next", "pane-next", "terminal-next")
	duplicate := managedLocationWorkspace(pane, "workspace-other", "pane-other", "terminal-other")
	wrongRepo := moved
	wrongRepo.RepoKey = "/repo/foreign.git"
	withoutEvidence := moved
	withoutEvidence.LivePanes = nil
	wrongAgent := moved
	wrongAgent.LivePanes = append([]backend.LivePane(nil), moved.LivePanes...)
	wrongAgent.LivePanes[0].AgentID = "fanout-codex-foreign"
	wrongProvider := moved
	wrongProvider.LivePanes = append([]backend.LivePane(nil), moved.LivePanes...)
	wrongProvider.LivePanes[0].AgentProvider = "claude"
	missingSession := moved
	missingSession.LivePanes = append([]backend.LivePane(nil), moved.LivePanes...)
	missingSession.LivePanes[0].AgentSession = nil

	for _, test := range []struct {
		name       string
		workspaces []backend.WorkspaceObservation
		wantErr    bool
	}{
		{name: "zero matches", workspaces: nil},
		{name: "duplicate labels", workspaces: []backend.WorkspaceObservation{moved, duplicate}, wantErr: true},
		{name: "provenance mismatch", workspaces: []backend.WorkspaceObservation{wrongRepo}, wantErr: true},
		{name: "missing agent evidence", workspaces: []backend.WorkspaceObservation{withoutEvidence}, wantErr: true},
		{name: "agent mismatch", workspaces: []backend.WorkspaceObservation{wrongAgent}, wantErr: true},
		{name: "provider mismatch", workspaces: []backend.WorkspaceObservation{wrongProvider}, wantErr: true},
		{name: "session missing", workspaces: []backend.WorkspaceObservation{missingSession}, wantErr: true},
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

func TestReconcileManagedPaneLocationLeavesSameWorkspaceDriftStale(t *testing.T) {
	pane := managedLocationPane()
	for _, test := range []struct {
		name       string
		paneID     string
		terminalID string
	}{
		{name: "pane only", paneID: "pane-next", terminalID: pane.TerminalID},
		{name: "terminal only", paneID: pane.PaneID, terminalID: "terminal-next"},
	} {
		t.Run(test.name, func(t *testing.T) {
			restarted := managedLocationWorkspace(pane, pane.WorkspaceID, test.paneID, test.terminalID)
			got, changed, err := ReconcileManagedPaneLocation(pane, []backend.WorkspaceObservation{restarted})
			if err != nil || changed || !reflect.DeepEqual(got, pane) {
				t.Fatalf("same-workspace drift changed: changed=%t pane=%#v err=%v", changed, got, err)
			}
		})
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
		AgentID: "fanout-codex", AgentSession: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-child",
		},
		EmitterRowKey: "row-key", LaunchNonce: "launch-nonce", BranchName: "fanout/child",
		EmitterNonce: strings.Repeat("e", 32), ReportedState: "idle", ReportedStateSeq: 7, StateRefinement: true,
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
		LivePanes: []backend.LivePane{{
			Ref: ref, CurrentPath: pane.WorktreePath,
			WorkspaceLabel: pane.WorkspaceLabel, TerminalID: terminalID,
			AgentID: pane.AgentID, AgentNamed: true, AgentProvider: pane.Agent,
			AgentSession: pane.AgentSession, AgentPresent: true,
			RepoKey: pane.RepoKey, ProjectRoot: pane.RepoRoot, WorktreePath: pane.WorktreePath,
			SessionID: pane.SessionID, SocketPath: pane.SocketPath,
		}},
	}
}
