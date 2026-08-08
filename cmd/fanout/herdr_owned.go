package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const ownedHerdrUnavailable = "pane is not in this repository's fanout-owned Herdr session"

func ownedHerdrPaneIdentity(pane state.Pane) (herdrrun.OwnedPaneIdentity, error) {
	if backend.NormalizeName(pane.Backend) != backend.Herdr {
		return herdrrun.OwnedPaneIdentity{}, fmt.Errorf("%s", ownedHerdrUnavailable)
	}
	worktreePath := ""
	if strings.TrimSpace(pane.HerdrRepoKey) != "" {
		worktreePath = pane.WorktreePath
	}
	identity := herdrrun.OwnedPaneIdentity{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: pane.HerdrWorkspaceID, Pane: pane.PaneID,
		},
		SessionID: pane.HerdrSession, SocketPath: pane.HerdrSocketPath,
		WorkspaceLabel: pane.HerdrWorkspaceLabel, TerminalID: pane.HerdrTerminalID,
		RepoKey: pane.HerdrRepoKey, WorktreePath: worktreePath,
		CurrentPath: pane.WorktreePath, AgentID: pane.HerdrAgentID,
		AgentSession: cloneAgentSessionRef(pane.HerdrAgentSession),
	}
	if strings.TrimSpace(identity.WorkspaceLabel) == "" {
		return herdrrun.OwnedPaneIdentity{}, fmt.Errorf("saved Herdr pane has no ownership label")
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

func ownedHerdrActionDisabled(owned *herdrrun.OwnedSession, pane state.Pane) string {
	if owned == nil {
		return ownedHerdrUnavailable
	}
	if pane.PaneID == "" && pane.Backend == "" {
		return ""
	}
	identity, err := ownedHerdrPaneIdentity(pane)
	if err != nil {
		return err.Error()
	}
	if identity.SessionID != owned.Session ||
		filepath.Clean(identity.SocketPath) != filepath.Clean(owned.SocketPath) {
		return ownedHerdrUnavailable
	}
	return ""
}

func bindOwnedHerdrPane(
	owned *herdrrun.OwnedSession,
	pane state.Pane,
) (*herdrrun.Backend, backend.PaneRef, error) {
	if reason := ownedHerdrActionDisabled(owned, pane); reason != "" {
		return nil, backend.PaneRef{}, fmt.Errorf("%s", reason)
	}
	identity, err := ownedHerdrPaneIdentity(pane)
	if err != nil {
		return nil, backend.PaneRef{}, err
	}
	bound, err := owned.Backend().BindOwnedTarget(identity)
	if err != nil {
		return nil, backend.PaneRef{}, err
	}
	return bound, identity.Ref, nil
}

func bindOwnedHerdrWorkspaceClose(
	owned *herdrrun.OwnedSession,
	pane state.Pane,
) (*herdrrun.Backend, error) {
	if owned == nil {
		return nil, fmt.Errorf("%s", ownedHerdrUnavailable)
	}
	identity, err := ownedHerdrPaneIdentity(pane)
	if err != nil {
		return nil, err
	}
	return owned.Backend().BindOwnedWorkspaceClose(identity)
}
