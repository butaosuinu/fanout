package panelaunch

// Projections between the journal's RuntimeResource identity and herdrrun's
// workspace observations, plus the ownership-nonce label and result shapes.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
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

// restartedManagedWorktreeResource admits the one cold-restart change: a new
// terminal identity on the exact saved workspace, pane, label, checkout, and
// repository provenance.
func restartedManagedWorktreeResource(
	observation backend.WorkspaceObservation,
	expected state.RuntimeResource,
) (state.RuntimeResource, bool) {
	if !workspaceHasExactRestartProvenance(observation, expected) {
		return state.RuntimeResource{}, false
	}
	terminals := restartedManagedTerminals(observation, expected)
	if len(terminals) != 1 {
		return state.RuntimeResource{}, false
	}
	expected.TerminalID = terminals[0]
	return expected, true
}

func workspaceHasExactRestartProvenance(
	observation backend.WorkspaceObservation,
	expected state.RuntimeResource,
) bool {
	requirements := []bool{
		managedWorktreeRestartResourceComplete(expected),
		observation.WorkspaceID == expected.WorkspaceID,
		observation.Label == expected.Label,
		filepath.Clean(observation.Path) == filepath.Clean(expected.CurrentPath),
		observation.RepoKey == expected.RepoKey, observation.RepoRoot == expected.RepoRoot,
	}
	return !slices.Contains(requirements, false)
}

func managedWorktreeRestartResourceComplete(resource state.RuntimeResource) bool {
	requirements := []string{
		resource.WorkspaceID, resource.Label, resource.PaneID, resource.TerminalID,
		resource.CurrentPath, resource.RepoKey, resource.RepoRoot,
	}
	return !slices.ContainsFunc(requirements, func(value string) bool {
		return strings.TrimSpace(value) == ""
	})
}

func restartedManagedTerminals(
	observation backend.WorkspaceObservation,
	expected state.RuntimeResource,
) []string {
	var terminals []string
	if len(observation.Panes) == 0 {
		if terminalID, found := restartedManagedTerminal(
			observation.Pane, observation.TerminalID, observation.CWD, expected,
		); found {
			terminals = append(terminals, terminalID)
		}
	}
	for _, pane := range observation.Panes {
		if terminalID, found := restartedManagedTerminal(pane.Pane, pane.TerminalID, pane.CWD, expected); found {
			terminals = append(terminals, terminalID)
		}
	}
	return terminals
}

func restartedManagedTerminal(
	ref backend.PaneRef,
	terminalID, cwd string,
	expected state.RuntimeResource,
) (string, bool) {
	if strings.TrimSpace(terminalID) == "" || terminalID == expected.TerminalID {
		return "", false
	}
	restarted := expected
	restarted.TerminalID = terminalID
	if !paneHasManagedResource(backend.WorkspacePaneObservation{
		Pane: ref, TerminalID: terminalID, CWD: cwd,
	}, restarted) {
		return "", false
	}
	return terminalID, true
}

// restartedManagedCoordinatorResource follows an already-healed coordinator
// only when the child intent's saved snapshot differs by terminal identity.
func restartedManagedCoordinatorResource(
	current state.RuntimeResource,
	expected state.RuntimeResource,
) (state.RuntimeResource, bool) {
	terminalID := current.TerminalID
	current.TerminalID = expected.TerminalID
	if !managedCoordinatorResourceComplete(expected) || strings.TrimSpace(terminalID) == "" ||
		terminalID == expected.TerminalID || current != expected {
		return state.RuntimeResource{}, false
	}
	expected.TerminalID = terminalID
	return expected, true
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
