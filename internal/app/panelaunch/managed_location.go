package panelaunch

import (
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// ReconcileManagedPaneLocationFromLive projects one aggregate snapshot onto
// workspace observations, then applies the existing label/provenance matcher.
func ReconcileManagedPaneLocationFromLive(
	pane state.Pane,
	live []backend.LivePane,
) (state.Pane, bool, error) {
	return ReconcileManagedPaneLocation(pane, locationWorkspaces(pane, live))
}

// ReconcileManagedPaneLocation updates only the runtime location of an agent
// row when one live workspace keeps its ownership label and exact checkout
// provenance. Missing workspaces are left recorded; ambiguity fails closed.
func ReconcileManagedPaneLocation(
	pane state.Pane,
	workspaces []backend.WorkspaceObservation,
) (state.Pane, bool, error) {
	if !managedPaneLocationEligible(pane) {
		return pane, false, nil
	}
	resource := managedPaneLocationResource(pane)
	if !managedPaneLocationComplete(pane, resource) {
		return pane, false, fmt.Errorf("%w: saved managed pane location identity is incomplete", backend.ErrOwnedIdentityMismatch)
	}
	match, found, err := managedPaneLocationMatch(pane, resource, workspaces)
	if err != nil || !found {
		return pane, false, err
	}
	return applyManagedPaneLocation(pane, match)
}

func applyManagedPaneLocation(
	pane state.Pane,
	match backend.WorkspaceObservation,
) (state.Pane, bool, error) {
	if pane.WorkspaceID == match.WorkspaceID {
		return pane, false, nil
	}
	if err := pane.InvalidateTelemetry(); err != nil {
		return pane, false, fmt.Errorf("invalidate telemetry after managed pane location change: %w", err)
	}
	pane.WorkspaceID, pane.PaneID, pane.TerminalID = match.WorkspaceID, match.Pane.Pane, match.TerminalID
	return pane, true, nil
}

func managedPaneLocationMatch(
	pane state.Pane,
	resource state.RuntimeResource,
	workspaces []backend.WorkspaceObservation,
) (backend.WorkspaceObservation, bool, error) {
	matches := workspacesWithLabel(workspaces, pane.WorkspaceLabel)
	if len(matches) == 0 {
		return backend.WorkspaceObservation{}, false, nil
	}
	if len(matches) != 1 {
		return backend.WorkspaceObservation{}, false, fmt.Errorf(
			"%w: managed pane label has %d live matches", backend.ErrOwnedIdentityMismatch, len(matches),
		)
	}
	match := adoptableCoordinatorObservation(matches[0], pane.WorktreePath)
	if !workspaceHasExactLocationProvenance(match, resource) || !managedPaneLocationAgentMatches(match, pane) {
		return backend.WorkspaceObservation{}, false, fmt.Errorf(
			"%w: managed pane label does not match checkout provenance or agent evidence", backend.ErrOwnedIdentityMismatch,
		)
	}
	return match, true, nil
}

func managedPaneLocationEligible(pane state.Pane) bool {
	return backend.LiveIdentityModelOf(pane.Backend) == backend.LiveIdentityRecordedBinding &&
		!pane.IsShell() && strings.TrimSpace(pane.Agent) != ""
}

func managedPaneLocationResource(pane state.Pane) state.RuntimeResource {
	return state.RuntimeResource{
		WorkspaceID: pane.WorkspaceID,
		Label:       pane.WorkspaceLabel,
		PaneID:      pane.PaneID,
		TerminalID:  pane.TerminalID,
		CurrentPath: pane.WorktreePath,
		RepoKey:     pane.RepoKey,
		RepoRoot:    pane.RepoRoot,
	}
}

func managedPaneLocationComplete(pane state.Pane, resource state.RuntimeResource) bool {
	return managedWorktreeRestartResourceComplete(resource) &&
		strings.TrimSpace(pane.SessionID) != "" && strings.TrimSpace(pane.SocketPath) != ""
}

func workspaceHasExactLocationProvenance(
	observation backend.WorkspaceObservation,
	expected state.RuntimeResource,
) bool {
	expected.WorkspaceID = observation.WorkspaceID
	return workspaceHasExactRestartProvenance(observation, expected)
}

func managedPaneLocationAgentMatches(observation backend.WorkspaceObservation, pane state.Pane) bool {
	pane.WorkspaceID = observation.WorkspaceID
	pane.PaneID = observation.Pane.Pane
	pane.TerminalID = observation.TerminalID
	runtime := backend.RequireRuntime(backend.NormalizeName(pane.Backend))
	_, ok := pane.RuntimeBinding().UniqueLive(observation.LivePanes, runtime)
	return ok
}

func locationWorkspaces(pane state.Pane, live []backend.LivePane) []backend.WorkspaceObservation {
	wantBackend := backend.NormalizeName(pane.Backend)
	indexes := map[string]int{}
	var workspaces []backend.WorkspaceObservation
	for _, current := range live {
		if current.Ref.Backend != wantBackend || current.SessionID != pane.SessionID || current.SocketPath != pane.SocketPath {
			continue
		}
		workspaces = appendLocationWorkspace(workspaces, indexes, current)
	}
	projectSinglePaneLocations(workspaces)
	return workspaces
}

func appendLocationWorkspace(
	workspaces []backend.WorkspaceObservation,
	indexes map[string]int,
	pane backend.LivePane,
) []backend.WorkspaceObservation {
	key := locationWorkspaceKey(pane)
	index, found := indexes[key]
	if !found {
		index = len(workspaces)
		indexes[key] = index
		workspaces = append(workspaces, locationWorkspace(pane))
	}
	workspaces[index].Panes = append(workspaces[index].Panes, backend.WorkspacePaneObservation{
		Pane: pane.Ref, TerminalID: pane.TerminalID, CWD: pane.WorktreePath,
	})
	workspaces[index].LivePanes = append(workspaces[index].LivePanes, pane)
	return workspaces
}

func projectSinglePaneLocations(workspaces []backend.WorkspaceObservation) {
	for index := range workspaces {
		if len(workspaces[index].Panes) == 1 {
			pane := workspaces[index].Panes[0]
			workspaces[index].Pane, workspaces[index].TerminalID, workspaces[index].CWD = pane.Pane, pane.TerminalID, pane.CWD
		}
	}
}

func locationWorkspaceKey(pane backend.LivePane) string {
	values := []string{
		pane.Ref.Workspace, pane.WorkspaceLabel, pane.WorktreePath, pane.RepoKey, pane.ProjectRoot,
	}
	return strings.Join(values, "\x00")
}

func locationWorkspace(pane backend.LivePane) backend.WorkspaceObservation {
	return backend.WorkspaceObservation{
		WorkspaceID: pane.Ref.Workspace,
		Label:       pane.WorkspaceLabel,
		Path:        pane.WorktreePath,
		RepoKey:     pane.RepoKey,
		RepoRoot:    pane.ProjectRoot,
	}
}
