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

type herdrCleanupObservation struct {
	workspace *backend.WorkspaceObservation
	checkout  worktree.CheckoutObservation
}

func validateHerdrPaneIdentity(pane state.Pane) error {
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
	if backend.NormalizeName(pane.Backend) != backend.Herdr {
		return fmt.Errorf("saved pane is not a Herdr row")
	}
	return nil
}

func herdrResourceFromPane(pane state.Pane) state.RuntimeResource {
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
func herdrResourceFromObservation(observation backend.WorkspaceObservation) state.RuntimeResource {
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

func observeHerdrCleanup(
	ctx context.Context,
	runtime HerdrRuntime,
	projectRoot string,
	resource state.RuntimeResource,
) (herdrCleanupObservation, error) {
	return observeHerdrCleanupMatching(
		ctx,
		runtime,
		projectRoot,
		resource,
		herdrWorkspacePredicate(resource),
	)
}

func observeHerdrCleanupMatching(
	ctx context.Context,
	runtime HerdrRuntime,
	projectRoot string,
	resource state.RuntimeResource,
	predicate herdrWorkspacePredicateFunc,
) (herdrCleanupObservation, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return herdrCleanupObservation{}, err
	}
	workspace, err := findUniqueWorkspace(workspaces, true, predicate)
	if err != nil {
		return herdrCleanupObservation{}, err
	}
	checkout, err := worktree.ObserveCheckout(ctx, projectRoot, resource.CurrentPath)
	if err != nil {
		return herdrCleanupObservation{}, err
	}
	return herdrCleanupObservation{workspace: workspace, checkout: checkout}, nil
}

func herdrWorkspacePredicate(resource state.RuntimeResource) herdrWorkspacePredicateFunc {
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		candidate := workspace.WorkspaceID == resource.WorkspaceID || workspace.Label == resource.Label ||
			herdrWorkspaceMatchesProvenance(
				workspace,
				resource.CurrentPath,
				resource.RepoKey,
				resource.RepoRoot,
			)
		return candidate, herdrWorkspaceMatchesResource(workspace, resource)
	}
}

func herdrWorkspaceLabelPredicate(label, path, repoKey, repoRoot string) herdrWorkspacePredicateFunc {
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		provenance := herdrWorkspaceMatchesProvenance(workspace, path, repoKey, repoRoot)
		return workspace.Label == label || provenance, workspace.Label == label && provenance
	}
}

type herdrWorkspacePredicateFunc func(backend.WorkspaceObservation) (candidate, exact bool)

func findUniqueWorkspace(
	workspaces []backend.WorkspaceObservation,
	allowAbsent bool,
	predicate herdrWorkspacePredicateFunc,
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

func herdrWorkspaceMatchesResource(
	workspace backend.WorkspaceObservation,
	resource state.RuntimeResource,
) bool {
	return workspace.WorkspaceID == resource.WorkspaceID &&
		workspace.Label == resource.Label &&
		filepath.Clean(workspace.Path) == filepath.Clean(resource.CurrentPath) &&
		filepath.Clean(workspace.RepoKey) == filepath.Clean(resource.RepoKey) &&
		filepath.Clean(workspace.RepoRoot) == filepath.Clean(resource.RepoRoot)
}

func herdrWorkspaceMatchesProvenance(
	workspace backend.WorkspaceObservation,
	path, repoKey, repoRoot string,
) bool {
	return filepath.Clean(workspace.Path) == filepath.Clean(path) &&
		filepath.Clean(workspace.RepoKey) == filepath.Clean(repoKey) &&
		filepath.Clean(workspace.RepoRoot) == filepath.Clean(repoRoot)
}

func verifyHerdrTerminalInvalidation(
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

func verifyHerdrCheckout(
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

func findHerdrCoordinatorIntent(
	locked *state.LockedStore,
	projectRoot string,
	target state.Pane,
) (state.LaunchIntent, error) {
	id, runtimeOwnerRoot, err := herdrCoordinatorIntentIdentity(projectRoot, target)
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
	if !herdrCoordinatorIntentMatches(intent, target, runtimeOwnerRoot, projectRoot) {
		return state.LaunchIntent{}, fmt.Errorf("saved Herdr coordinator intent does not match the child row")
	}
	return intent, nil
}

func herdrCoordinatorIntentIdentity(projectRoot string, target state.Pane) (string, string, error) {
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

func herdrCoordinatorIntentMatches(
	intent state.LaunchIntent,
	target state.Pane,
	runtimeOwnerRoot, projectRoot string,
) bool {
	return intent.Kind == state.IntentCoordinator &&
		intent.Status == state.IntentRealized &&
		intent.RuntimeParent == target.RuntimeParent &&
		savedHerdrCoordinatorPathMatches(runtimeOwnerRoot, intent.WorktreePath, projectRoot) &&
		intent.Session == target.SessionID &&
		intent.SocketPath == target.SocketPath
}

func savedHerdrCoordinatorPathMatches(ownerProjectRoot, savedPath, projectRoot string) bool {
	savedPath = filepath.Clean(savedPath)
	if !filepath.IsAbs(savedPath) {
		return false
	}
	return ownerProjectRoot == "" || savedPath == filepath.Clean(projectRoot)
}

func observeHerdrCoordinator(
	ctx context.Context,
	runtime HerdrRuntime,
	resource state.RuntimeResource,
) (backend.WorkspaceObservation, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return backend.WorkspaceObservation{}, err
	}
	workspace, err := findUniqueWorkspace(workspaces, false, herdrCoordinatorWorkspacePredicate(resource))
	if err != nil {
		return backend.WorkspaceObservation{}, err
	}
	projected, ok := projectHerdrCoordinatorPane(*workspace, resource)
	if !ok {
		return backend.WorkspaceObservation{}, fmt.Errorf("herdr coordinator pane projection changed after matching")
	}
	return projected, nil
}

func herdrCoordinatorWorkspacePredicate(resource state.RuntimeResource) herdrWorkspacePredicateFunc {
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		candidate := workspace.WorkspaceID == resource.WorkspaceID || workspace.Label == resource.Label
		_, exact := projectHerdrCoordinatorPane(workspace, resource)
		return candidate, exact
	}
}

func projectHerdrCoordinatorPane(
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
