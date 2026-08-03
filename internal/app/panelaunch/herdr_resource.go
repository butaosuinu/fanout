package panelaunch

// Projections between the journal's HerdrResource identity and herdrrun's
// workspace observations, plus the ownership-nonce label and result shapes.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func coordinatorDeferred(intent state.HerdrIntent) (HerdrCoordinatorResult, error) {
	return HerdrCoordinatorResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
	}, ErrHerdrLauncherReadinessDeferred
}

func worktreeDeferred(intent state.HerdrIntent) (HerdrWorktreeResult, error) {
	return HerdrWorktreeResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
	}, ErrHerdrLauncherReadinessDeferred
}

func workspacesWithLabel(
	workspaces []herdrrun.WorkspaceObservation,
	label string,
) []herdrrun.WorkspaceObservation {
	var matches []herdrrun.WorkspaceObservation
	for _, workspace := range workspaces {
		if workspace.Label == label {
			matches = append(matches, workspace)
		}
	}
	return matches
}

func stateResource(observation herdrrun.WorkspaceObservation) state.HerdrResource {
	return state.HerdrResource{
		WorkspaceID: observation.WorkspaceID,
		Label:       observation.Label,
		PaneID:      observation.Pane.Pane,
		TerminalID:  observation.TerminalID,
		CurrentPath: observation.CWD,
		RepoKey:     observation.RepoKey,
		RepoRoot:    observation.RepoRoot,
	}
}

func observationResource(resource state.HerdrResource) herdrrun.WorkspaceObservation {
	return herdrrun.WorkspaceObservation{
		WorkspaceID: resource.WorkspaceID,
		Label:       resource.Label,
		RepoKey:     resource.RepoKey,
		RepoRoot:    resource.RepoRoot,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: resource.WorkspaceID, Pane: resource.PaneID,
		},
		TerminalID: resource.TerminalID,
		CWD:        resource.CurrentPath,
	}
}

func sameHerdrResource(left, right state.HerdrResource) bool {
	return left == right
}

func workspaceHasHerdrResource(
	observation herdrrun.WorkspaceObservation,
	expected state.HerdrResource,
) bool {
	if observation.WorkspaceID != expected.WorkspaceID ||
		observation.Label != expected.Label ||
		observation.RepoKey != expected.RepoKey ||
		observation.RepoRoot != expected.RepoRoot {
		return false
	}
	if sameHerdrResource(expected, stateResource(observation)) {
		return true
	}
	for _, pane := range observation.Panes {
		if pane.Pane.Backend == backend.Herdr &&
			pane.Pane.Workspace == expected.WorkspaceID &&
			pane.Pane.Pane == expected.PaneID &&
			pane.TerminalID == expected.TerminalID &&
			pane.CWD == expected.CurrentPath {
			return true
		}
	}
	return false
}

func newHerdrWorkspaceLabel(
	kind string,
	randomToken func() (string, error),
) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("create Herdr %s workspace label: %w", kind, err)
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return "", fmt.Errorf("create Herdr %s workspace label: invalid random token", kind)
	}
	return "fanout-" + kind + "-" + token, nil
}

func randomHerdrToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func canonicalHerdrParent(parent string) string {
	return parentref.Canon(strings.TrimSpace(parent))
}
