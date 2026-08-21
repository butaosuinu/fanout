package panelaunch

// Projections between the journal's RuntimeResource identity and herdrrun's
// workspace observations, plus the ownership-nonce label and result shapes.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// realizeDeferred hands a realized workspace to the agent-start phase.
func realizeDeferred(intent state.LaunchIntent) (ManagedRealizeResult, error) {
	return ManagedRealizeResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
	}, ErrManagedLauncherReadinessDeferred
}

func workspacesWithLabel(
	workspaces []backend.WorkspaceObservation,
	label string,
) []backend.WorkspaceObservation {
	var matches []backend.WorkspaceObservation
	for _, workspace := range workspaces {
		if workspace.Label == label {
			matches = append(matches, workspace)
		}
	}
	return matches
}

func stateResource(observation backend.WorkspaceObservation) state.RuntimeResource {
	return state.RuntimeResource{
		WorkspaceID: observation.WorkspaceID,
		Label:       observation.Label,
		PaneID:      observation.Pane.Pane,
		TerminalID:  observation.TerminalID,
		CurrentPath: observation.CWD,
		RepoKey:     observation.RepoKey,
		RepoRoot:    observation.RepoRoot,
	}
}

func observationResource(resource state.RuntimeResource) backend.WorkspaceObservation {
	return backend.WorkspaceObservation{
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

// adoptableCoordinatorObservation projects a workspace observation onto the
// one pane an adoption may bind: the top-level triple when herdrrun reported
// one (a single-pane workspace), otherwise the unique pane at the
// coordinator's recorded path. An ambiguous workspace comes back unchanged,
// so the postcondition check fails closed on it.
func adoptableCoordinatorObservation(
	observation backend.WorkspaceObservation,
	worktreePath string,
) backend.WorkspaceObservation {
	if observation.Pane.Pane != "" {
		return observation
	}
	var match *backend.WorkspacePaneObservation
	for index := range observation.Panes {
		pane := &observation.Panes[index]
		if filepath.Clean(pane.CWD) != filepath.Clean(worktreePath) {
			continue
		}
		if match != nil {
			return observation
		}
		match = pane
	}
	if match == nil {
		return observation
	}
	observation.Pane = match.Pane
	observation.TerminalID = match.TerminalID
	observation.CWD = match.CWD
	return observation
}

func workspaceHasManagedResource(
	observation backend.WorkspaceObservation,
	expected state.RuntimeResource,
) bool {
	if observation.WorkspaceID != expected.WorkspaceID ||
		observation.Label != expected.Label ||
		!workspaceProvenanceMatches(observation, expected) {
		return false
	}
	if expected == stateResource(observation) {
		return true
	}
	for _, pane := range observation.Panes {
		if paneHasManagedResource(pane, expected) {
			return true
		}
	}
	return false
}

// paneHasManagedResource reports whether one observed pane still carries the
// pane identity the journal recorded for the workspace.
func paneHasManagedResource(
	pane backend.WorkspacePaneObservation,
	expected state.RuntimeResource,
) bool {
	return pane.Pane.Backend == backend.Herdr &&
		pane.Pane.Workspace == expected.WorkspaceID &&
		pane.Pane.Pane == expected.PaneID &&
		pane.TerminalID == expected.TerminalID &&
		pane.CWD == expected.CurrentPath
}

func workspaceProvenanceMatches(
	observation backend.WorkspaceObservation,
	expected state.RuntimeResource,
) bool {
	return (expected.RepoKey == "" || observation.RepoKey == expected.RepoKey) &&
		(expected.RepoRoot == "" || observation.RepoRoot == expected.RepoRoot)
}

// managedCoordinatorLabelKind names the coordinator lane in workspace labels;
// managedWorkspaceLabelPrefix(managedCoordinatorLabelKind) is the shape saved
// rows are matched against, so label creation and matching cannot drift.
const managedCoordinatorLabelKind = "coordinator"

func managedWorkspaceLabelPrefix(kind string) string {
	return "fanout-" + kind + "-"
}

func newManagedWorkspaceLabel(
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
	return managedWorkspaceLabelPrefix(kind) + token, nil
}

func randomManagedToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func canonicalManagedParent(parent string) string {
	return parentref.Canon(strings.TrimSpace(parent))
}
