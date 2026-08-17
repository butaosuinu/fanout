package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const ownedPaneUnavailable = "pane is not in this repository's fanout-owned Herdr session"

// managedPaneIdentity projects one saved row onto the owned-pane identity every
// managed action is admitted against. backend.Herdr appears here as persisted
// data: the row records which runtime issued the pane, and a row from any other
// runtime cannot be addressed through an owned session at all.
func managedPaneIdentity(pane state.Pane) (backend.OwnedPaneIdentity, error) {
	if backend.NormalizeName(pane.Backend) != backend.Herdr {
		return backend.OwnedPaneIdentity{}, fmt.Errorf("%s", ownedPaneUnavailable)
	}
	worktreePath := ""
	if strings.TrimSpace(pane.RepoKey) != "" {
		worktreePath = pane.WorktreePath
	}
	identity := backend.OwnedPaneIdentity{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: pane.WorkspaceID, Pane: pane.PaneID,
		},
		SessionID: pane.SessionID, SocketPath: pane.SocketPath,
		WorkspaceLabel: pane.WorkspaceLabel, TerminalID: pane.TerminalID,
		RepoKey: pane.RepoKey, WorktreePath: worktreePath,
		CurrentPath: pane.WorktreePath, AgentID: pane.AgentID,
		AgentSession: cloneAgentSessionRef(pane.AgentSession),
	}
	if strings.TrimSpace(identity.WorkspaceLabel) == "" {
		return backend.OwnedPaneIdentity{}, fmt.Errorf("saved Herdr pane has no ownership label")
	}
	return identity, nil
}

func cloneAgentSessionRef(ref *backend.AgentSessionRef) *backend.AgentSessionRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}

func managedActionDisabled(owned paneruntime.ManagedSession, pane state.Pane) string {
	if owned == nil {
		return ownedPaneUnavailable
	}
	if pane.PaneID == "" && pane.Backend == "" {
		return ""
	}
	identity, err := managedPaneIdentity(pane)
	if err != nil {
		return err.Error()
	}
	if identity.SessionID != owned.Session ||
		filepath.Clean(identity.SocketPath) != filepath.Clean(owned.SocketPath) {
		return ownedPaneUnavailable
	}
	return ""
}

func bindManagedPane(
	owned paneruntime.ManagedSession,
	pane state.Pane,
) (backend.Backend, backend.PaneRef, error) {
	if reason := managedActionDisabled(owned, pane); reason != "" {
		return nil, backend.PaneRef{}, fmt.Errorf("%s", reason)
	}
	identity, err := managedPaneIdentity(pane)
	if err != nil {
		return nil, backend.PaneRef{}, err
	}
	ownedBackend := owned.Backend()
	if ownedBackend == nil {
		return nil, backend.PaneRef{}, fmt.Errorf("%s", ownedPaneUnavailable)
	}
	bound, err := ownedBackend.BindOwnedTarget(identity)
	if err != nil {
		return nil, backend.PaneRef{}, err
	}
	return bound, identity.Ref, nil
}

func bindManagedWorkspaceClose(
	owned panelaunch.ManagedSessionRuntime,
	pane state.Pane,
) (backend.OwnedClosingBackend, error) {
	if owned == nil {
		return nil, fmt.Errorf("%s", ownedPaneUnavailable)
	}
	identity, err := managedPaneIdentity(pane)
	if err != nil {
		return nil, err
	}
	return owned.BindOwnedWorkspaceClose(identity)
}
