package panelaunch

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

var errManagedPaneLocationUncertain = errors.New("managed pane location is temporarily uncertain")

type TelemetrySequenceFence func() (uint64, error)

// ReconcileManagedPaneLocationFromLive projects one aggregate snapshot onto
// workspace observations, then applies the existing label/provenance matcher.
func ReconcileManagedPaneLocationFromLive(
	pane state.Pane,
	live []backend.LivePane,
	sequenceFence TelemetrySequenceFence,
) (state.Pane, bool, error) {
	return ReconcileManagedPaneLocation(pane, locationWorkspaces(pane, live), sequenceFence)
}

// ManagedPaneLocationChangedFromLive reports whether the same evidence would
// move pane without allocating a telemetry generation fence.
func ManagedPaneLocationChangedFromLive(pane state.Pane, live []backend.LivePane) (bool, error) {
	_, changed, err := managedPaneLocationCandidate(pane, locationWorkspaces(pane, live))
	if err != nil && !errors.Is(err, errManagedPaneLocationUncertain) {
		return false, nil
	}
	return changed, err
}

// IsManagedPaneLocationMismatch reports whether err is an expected matcher rejection.
func IsManagedPaneLocationMismatch(err error) bool {
	return errors.Is(err, backend.ErrOwnedIdentityMismatch) ||
		errors.Is(err, errManagedPaneLocationUncertain)
}

// ReconcileManagedPaneLocation updates only the runtime location of an agent
// row when one live workspace keeps its ownership label and exact checkout
// provenance. Missing workspaces are left recorded; ambiguity fails closed.
func ReconcileManagedPaneLocation(
	pane state.Pane,
	workspaces []backend.WorkspaceObservation,
	sequenceFence TelemetrySequenceFence,
) (state.Pane, bool, error) {
	match, changed, err := managedPaneLocationCandidate(pane, workspaces)
	if err != nil || !changed {
		return pane, false, err
	}
	if sequenceFence == nil {
		return pane, false, fmt.Errorf("managed pane location telemetry fence is not configured")
	}
	sequence, err := sequenceFence()
	if err != nil {
		return pane, false, fmt.Errorf("allocate managed pane location telemetry fence: %w", err)
	}
	return applyManagedPaneLocation(pane, match, sequence)
}

func managedPaneLocationCandidate(
	pane state.Pane,
	workspaces []backend.WorkspaceObservation,
) (backend.WorkspaceObservation, bool, error) {
	if !managedPaneLocationEligible(pane) {
		return backend.WorkspaceObservation{}, false, nil
	}
	resource := managedPaneLocationResource(pane)
	if !managedPaneLocationComplete(pane, resource) {
		return backend.WorkspaceObservation{}, false, fmt.Errorf(
			"%w: saved managed pane location identity is incomplete", backend.ErrOwnedIdentityMismatch,
		)
	}
	match, found, err := managedPaneLocationMatch(pane, resource, workspaces)
	if err != nil || !found {
		return backend.WorkspaceObservation{}, false, err
	}
	return match, pane.WorkspaceID != match.WorkspaceID, nil
}

func applyManagedPaneLocation(
	pane state.Pane,
	match backend.WorkspaceObservation,
	sequenceFence uint64,
) (state.Pane, bool, error) {
	if err := pane.InvalidateTelemetryForLocationRebind(sequenceFence); err != nil {
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
		return backend.WorkspaceObservation{}, false, fmt.Errorf(
			"%w: managed pane label has no live match", errManagedPaneLocationUncertain,
		)
	}
	if len(matches) != 1 {
		return backend.WorkspaceObservation{}, false, fmt.Errorf(
			"%w: %w: managed pane label has %d live matches",
			backend.ErrOwnedIdentityMismatch, errManagedPaneLocationUncertain, len(matches),
		)
	}
	match := matches[0]
	if !managedPaneLocationCheckoutMatches(pane, match, resource) {
		return backend.WorkspaceObservation{}, false, fmt.Errorf(
			"%w: managed pane label does not match checkout provenance or agent evidence", backend.ErrOwnedIdentityMismatch,
		)
	}
	live, err := managedPaneLocationAgentMatch(match, pane)
	if err != nil {
		return backend.WorkspaceObservation{}, false, err
	}
	match.Pane, match.TerminalID = live.Ref, live.TerminalID
	return match, true, nil
}

func managedPaneLocationAgentMatch(
	observation backend.WorkspaceObservation,
	pane state.Pane,
) (backend.LivePane, error) {
	live, matches := uniqueManagedPaneLocationAgent(observation, pane)
	if matches == 1 {
		return live, nil
	}
	if matches > 1 || managedPaneLocationAgentEvidenceMissing(observation.LivePanes) {
		return backend.LivePane{}, fmt.Errorf(
			"%w: %w: managed pane agent evidence is missing or ambiguous",
			backend.ErrOwnedIdentityMismatch, errManagedPaneLocationUncertain,
		)
	}
	return backend.LivePane{}, fmt.Errorf(
		"%w: managed pane label does not match checkout provenance or agent evidence", backend.ErrOwnedIdentityMismatch,
	)
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
	return managedPaneLocationResourceComplete(pane, resource) &&
		strings.TrimSpace(pane.SessionID) != "" && strings.TrimSpace(pane.SocketPath) != "" &&
		strings.TrimSpace(pane.AgentID) != "" && pane.AgentSession != nil
}

func managedPaneLocationResourceComplete(pane state.Pane, resource state.RuntimeResource) bool {
	if !pane.IsAttachedAgent() || resource.RepoKey != "" || resource.RepoRoot != "" {
		return managedWorktreeRestartResourceComplete(resource)
	}
	return !slices.Contains([]string{
		resource.WorkspaceID, resource.Label, resource.PaneID, resource.TerminalID, resource.CurrentPath,
	}, "")
}

func managedPaneLocationCheckoutMatches(pane state.Pane, observation backend.WorkspaceObservation, resource state.RuntimeResource) bool {
	if !pane.IsAttachedAgent() || resource.RepoKey != "" || resource.RepoRoot != "" {
		return workspaceHasExactLocationProvenance(observation, resource)
	}
	return backend.CheckoutMatchesLive("", resource.CurrentPath, backend.LivePane{
		WorktreePath: observation.Path, CurrentPath: observation.CWD,
	})
}

func workspaceHasExactLocationProvenance(
	observation backend.WorkspaceObservation,
	expected state.RuntimeResource,
) bool {
	expected.WorkspaceID = observation.WorkspaceID
	return workspaceHasExactRestartProvenance(observation, expected)
}

func uniqueManagedPaneLocationAgent(
	observation backend.WorkspaceObservation,
	pane state.Pane,
) (backend.LivePane, int) {
	runtime := backend.RequireRuntime(backend.NormalizeName(pane.Backend))
	var matched backend.LivePane
	count := 0
	for _, live := range observation.LivePanes {
		candidate := pane
		candidate.WorkspaceID = observation.WorkspaceID
		candidate.PaneID = live.Ref.Pane
		candidate.TerminalID = live.TerminalID
		current, ok := candidate.RuntimeBinding().UniqueLive(observation.LivePanes, runtime)
		if ok {
			matched = current
			count++
		}
	}
	return matched, count
}

func managedPaneLocationAgentEvidenceMissing(live []backend.LivePane) bool {
	if len(live) == 0 {
		return true
	}
	for _, current := range live {
		if !current.AgentPresent || strings.TrimSpace(current.AgentProvider) == "" ||
			current.AgentSession == nil || current.AgentNamed && strings.TrimSpace(current.AgentID) == "" {
			return true
		}
	}
	return false
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
