package lifecycle

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type workspaceCleanupObservation struct {
	workspace *backend.WorkspaceObservation
	checkout  worktree.CheckoutObservation
}

// workspaceRuntimeRow reports whether the recorded row has to be operated
// through a WorkspaceRuntime: its runtime settles container mutations as
// journaled requests rather than as one local atomic call, so closing the row
// means driving the cleanup intent journal instead of closing a pane directly.
//
// The recorded runtime name is the row's own durable record of that lane, and
// it is deliberately the criterion here. A row read back from state.json has no
// Backend instance left to ask, and the row's identity fields cannot answer
// either: a row naming the journaled runtime with an incomplete recorded
// identity must still be refused by this lane rather than fall back to an
// atomic pane close, and a row naming the atomic runtime must never be routed
// at a journaled runtime because it happens to carry session fields.
func workspaceRuntimeRow(pane state.Pane) bool {
	return backend.NormalizeName(pane.Backend) == backend.Herdr
}

func validateWorkspacePaneIdentity(pane state.Pane) error {
	required := []string{
		pane.RuntimeParent,
		pane.PaneID,
		pane.WorkspaceID,
		pane.WorkspaceLabel,
		pane.TerminalID,
		pane.RepoKey,
		pane.RepoRoot,
		pane.SessionID,
		pane.SocketPath,
		pane.WorktreePath,
		pane.BranchName,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("saved Herdr lifecycle identity is incomplete")
		}
	}
	if !workspaceRuntimeRow(pane) {
		return fmt.Errorf("saved pane is not a Herdr row")
	}
	return nil
}

func resourceFromPane(pane state.Pane) state.RuntimeResource {
	return state.RuntimeResource{
		WorkspaceID: pane.WorkspaceID,
		Label:       pane.WorkspaceLabel,
		PaneID:      pane.PaneID,
		TerminalID:  pane.TerminalID,
		CurrentPath: filepath.Clean(pane.WorktreePath),
		RepoKey:     filepath.Clean(pane.RepoKey),
		RepoRoot:    filepath.Clean(pane.RepoRoot),
	}
}

// This projection duplicates panelaunch.stateResource until a follow-up can
// move the shared Herdr observation mapping below both app packages.
func resourceFromObservation(observation backend.WorkspaceObservation) state.RuntimeResource {
	return state.RuntimeResource{
		WorkspaceID: observation.WorkspaceID,
		Label:       observation.Label,
		PaneID:      observation.Pane.Pane,
		TerminalID:  observation.TerminalID,
		CurrentPath: filepath.Clean(observation.CWD),
		RepoKey:     filepath.Clean(observation.RepoKey),
		RepoRoot:    filepath.Clean(observation.RepoRoot),
	}
}

func observeWorkspaceCleanup(
	ctx context.Context,
	runtime WorkspaceRuntime,
	projectRoot string,
	resource state.RuntimeResource,
) (workspaceCleanupObservation, error) {
	return observeWorkspaceCleanupMatching(
		ctx,
		runtime,
		projectRoot,
		resource,
		workspacePredicate(resource),
	)
}

func observeLabelBoundWorkspaceCleanup(
	ctx context.Context,
	runtime WorkspaceRuntime,
	projectRoot string,
	intent state.LaunchIntent,
) (workspaceCleanupObservation, error) {
	return observeWorkspaceCleanupMatching(
		ctx,
		runtime,
		projectRoot,
		intent.Resource,
		workspaceLabelPredicate(
			intent.WorkspaceLabel,
			intent.WorktreePath,
			intent.Resource.RepoKey,
			intent.Resource.RepoRoot,
		),
	)
}

func observeWorkspaceCleanupMatching(
	ctx context.Context,
	runtime WorkspaceRuntime,
	projectRoot string,
	resource state.RuntimeResource,
	predicate workspacePredicateFunc,
) (workspaceCleanupObservation, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return workspaceCleanupObservation{}, err
	}
	workspace, err := findUniqueWorkspace(workspaces, true, predicate)
	if err != nil {
		return workspaceCleanupObservation{}, err
	}
	checkout, err := worktree.ObserveCheckout(ctx, projectRoot, resource.CurrentPath)
	if err != nil {
		return workspaceCleanupObservation{}, err
	}
	return workspaceCleanupObservation{workspace: workspace, checkout: checkout}, nil
}

func workspacePredicate(resource state.RuntimeResource) workspacePredicateFunc {
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		candidate := workspace.WorkspaceID == resource.WorkspaceID || workspace.Label == resource.Label ||
			workspaceMatchesProvenance(
				workspace,
				resource.CurrentPath,
				resource.RepoKey,
				resource.RepoRoot,
			)
		return candidate, workspaceMatchesResource(workspace, resource)
	}
}

func workspaceLabelPredicate(label, path, repoKey, repoRoot string) workspacePredicateFunc {
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		provenance := workspaceMatchesProvenance(workspace, path, repoKey, repoRoot)
		return workspace.Label == label || provenance, workspace.Label == label && provenance
	}
}

type workspacePredicateFunc func(backend.WorkspaceObservation) (candidate, exact bool)

func findUniqueWorkspace(
	workspaces []backend.WorkspaceObservation,
	allowAbsent bool,
	predicate workspacePredicateFunc,
) (*backend.WorkspaceObservation, error) {
	var candidates []backend.WorkspaceObservation
	for _, workspace := range workspaces {
		candidate, _ := predicate(workspace)
		if candidate {
			candidates = append(candidates, workspace)
		}
	}
	if len(candidates) == 0 && allowAbsent {
		return nil, nil
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("herdr workspace identity has %d live matches", len(candidates))
	}
	_, exact := predicate(candidates[0])
	if !exact {
		return nil, fmt.Errorf("herdr workspace identity does not match the live workspace")
	}
	return &candidates[0], nil
}

func workspaceMatchesResource(
	workspace backend.WorkspaceObservation,
	resource state.RuntimeResource,
) bool {
	return workspace.WorkspaceID == resource.WorkspaceID &&
		workspace.Label == resource.Label &&
		filepath.Clean(workspace.Path) == filepath.Clean(resource.CurrentPath) &&
		filepath.Clean(workspace.RepoKey) == filepath.Clean(resource.RepoKey) &&
		filepath.Clean(workspace.RepoRoot) == filepath.Clean(resource.RepoRoot)
}

func workspaceMatchesProvenance(
	workspace backend.WorkspaceObservation,
	path, repoKey, repoRoot string,
) bool {
	return filepath.Clean(workspace.Path) == filepath.Clean(path) &&
		filepath.Clean(workspace.RepoKey) == filepath.Clean(repoKey) &&
		filepath.Clean(workspace.RepoRoot) == filepath.Clean(repoRoot)
}

func adoptMovedWorkspaceCleanupResource(
	resource state.RuntimeResource,
	workspace backend.WorkspaceObservation,
) state.RuntimeResource {
	resource.WorkspaceID = workspace.WorkspaceID
	if workspace.Pane.Pane != "" && workspace.TerminalID != "" {
		resource.PaneID = workspace.Pane.Pane
		resource.TerminalID = workspace.TerminalID
	}
	return resource
}

func verifyTerminalInvalidation(
	workspace backend.WorkspaceObservation,
	resource state.RuntimeResource,
) error {
	for _, pane := range workspace.Panes {
		if pane.TerminalID != resource.TerminalID {
			continue
		}
		if pane.Pane != (backend.PaneRef{Backend: backend.Herdr, Workspace: resource.WorkspaceID, Pane: resource.PaneID}) ||
			filepath.Clean(pane.CWD) != filepath.Clean(resource.CurrentPath) {
			return fmt.Errorf("saved Herdr terminal identity was reused by a different pane")
		}
		return nil
	}
	return nil
}

func verifyCleanupCheckout(
	ctx context.Context,
	projectRoot, fullRef, expectedHead string,
	resource state.RuntimeResource,
) error {
	_, err := worktree.VerifyCheckout(
		ctx,
		projectRoot,
		resource.CurrentPath,
		fullRef,
		expectedHead,
		resource.RepoKey,
		resource.RepoRoot,
	)
	return err
}

func findCoordinatorIntent(
	locked *state.LockedStore,
	projectRoot string,
	target state.Pane,
) (state.LaunchIntent, error) {
	id, runtimeOwnerRoot, err := coordinatorIntentIdentity(projectRoot, target)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	intent, found := journal.FindIntent(id)
	if !found {
		return state.LaunchIntent{}, fmt.Errorf("saved Herdr coordinator intent is not recorded")
	}
	if !coordinatorIntentMatches(intent, target, runtimeOwnerRoot, projectRoot) {
		return state.LaunchIntent{}, fmt.Errorf("saved Herdr coordinator intent does not match the child row")
	}
	return intent, nil
}

func coordinatorIntentIdentity(projectRoot string, target state.Pane) (string, string, error) {
	projectRoot = filepath.Clean(projectRoot)
	runtimeOwnerRoot, err := state.IntentOwnerProjectRoot(target.RuntimeParent, projectRoot)
	if err != nil {
		return "", "", err
	}
	issueNum := 0
	if target.RuntimeParent == "@manual" || target.RuntimeParent == watcherStandaloneParent {
		issueNum = target.IssueNum
	}
	id, err := state.CoordinatorIntentID(target.RuntimeParent, runtimeOwnerRoot, issueNum)
	return id, runtimeOwnerRoot, err
}

func coordinatorIntentMatches(
	intent state.LaunchIntent,
	target state.Pane,
	runtimeOwnerRoot, projectRoot string,
) bool {
	return intent.Kind == state.IntentCoordinator &&
		intent.Status == state.IntentRealized &&
		intent.RuntimeParent == target.RuntimeParent &&
		savedCoordinatorPathMatches(runtimeOwnerRoot, intent.WorktreePath, projectRoot) &&
		intent.Session == target.SessionID &&
		intent.SocketPath == target.SocketPath
}

func savedCoordinatorPathMatches(ownerProjectRoot, savedPath, projectRoot string) bool {
	savedPath = filepath.Clean(savedPath)
	if !filepath.IsAbs(savedPath) {
		return false
	}
	return ownerProjectRoot == "" || savedPath == filepath.Clean(projectRoot)
}

func observeCoordinator(
	ctx context.Context,
	runtime WorkspaceRuntime,
	resource state.RuntimeResource,
) (backend.WorkspaceObservation, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return backend.WorkspaceObservation{}, err
	}
	workspace, err := findUniqueWorkspace(workspaces, false, coordinatorWorkspacePredicate(resource))
	if err != nil {
		return backend.WorkspaceObservation{}, err
	}
	projected, ok := projectCoordinatorPane(*workspace, resource)
	if !ok {
		return backend.WorkspaceObservation{}, fmt.Errorf("herdr coordinator pane projection changed after matching")
	}
	return projected, nil
}

func coordinatorWorkspacePredicate(resource state.RuntimeResource) workspacePredicateFunc {
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		candidate := workspace.WorkspaceID == resource.WorkspaceID || workspace.Label == resource.Label
		_, exact := projectCoordinatorPane(workspace, resource)
		return candidate, exact
	}
}

func projectCoordinatorPane(
	workspace backend.WorkspaceObservation,
	resource state.RuntimeResource,
) (backend.WorkspaceObservation, bool) {
	if workspace.WorkspaceID != resource.WorkspaceID || workspace.Label != resource.Label {
		return backend.WorkspaceObservation{}, false
	}
	want := backend.PaneRef{Backend: backend.Herdr, Workspace: resource.WorkspaceID, Pane: resource.PaneID}
	for _, pane := range workspace.Panes {
		if pane.Pane != want || pane.TerminalID != resource.TerminalID ||
			filepath.Clean(pane.CWD) != filepath.Clean(resource.CurrentPath) {
			continue
		}
		workspace.Pane, workspace.TerminalID, workspace.CWD = pane.Pane, pane.TerminalID, pane.CWD
		return workspace, true
	}
	return backend.WorkspaceObservation{}, false
}
