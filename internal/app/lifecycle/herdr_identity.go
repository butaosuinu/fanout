package lifecycle

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type herdrCleanupObservation struct {
	workspace *herdrrun.WorkspaceObservation
	checkout  worktree.CheckoutObservation
}

func validateHerdrPaneIdentity(pane state.Pane) error {
	required := []string{
		pane.RuntimeParent,
		pane.PaneID,
		pane.HerdrWorkspaceID,
		pane.HerdrWorkspaceLabel,
		pane.HerdrTerminalID,
		pane.HerdrRepoKey,
		pane.HerdrRepoRoot,
		pane.HerdrSession,
		pane.HerdrSocketPath,
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

func herdrResourceFromPane(pane state.Pane) state.HerdrResource {
	return state.HerdrResource{
		WorkspaceID: pane.HerdrWorkspaceID,
		Label:       pane.HerdrWorkspaceLabel,
		PaneID:      pane.PaneID,
		TerminalID:  pane.HerdrTerminalID,
		CurrentPath: filepath.Clean(pane.WorktreePath),
		RepoKey:     filepath.Clean(pane.HerdrRepoKey),
		RepoRoot:    filepath.Clean(pane.HerdrRepoRoot),
	}
}

func herdrResourceFromObservation(observation herdrrun.WorkspaceObservation) state.HerdrResource {
	return state.HerdrResource{
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
	resource state.HerdrResource,
) (herdrCleanupObservation, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return herdrCleanupObservation{}, err
	}
	workspace, err := matchHerdrWorkspace(workspaces, resource)
	if err != nil {
		return herdrCleanupObservation{}, err
	}
	checkout, err := worktree.ObserveCheckout(ctx, projectRoot, resource.CurrentPath)
	if err != nil {
		return herdrCleanupObservation{}, err
	}
	return herdrCleanupObservation{workspace: workspace, checkout: checkout}, nil
}

func matchHerdrWorkspace(
	workspaces []herdrrun.WorkspaceObservation,
	resource state.HerdrResource,
) (*herdrrun.WorkspaceObservation, error) {
	var candidates []herdrrun.WorkspaceObservation
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == resource.WorkspaceID || workspace.Label == resource.Label {
			candidates = append(candidates, workspace)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("saved Herdr workspace identity has %d live matches", len(candidates))
	}
	workspace := candidates[0]
	if !herdrWorkspaceMatchesResource(workspace, resource) {
		return nil, fmt.Errorf("saved Herdr workspace identity does not match the live workspace")
	}
	return &workspace, nil
}

func matchHerdrWorkspaceByLabel(
	workspaces []herdrrun.WorkspaceObservation,
	label, path, repoKey, repoRoot string,
) (*herdrrun.WorkspaceObservation, error) {
	var matches []herdrrun.WorkspaceObservation
	for _, workspace := range workspaces {
		if workspace.Label == label {
			matches = append(matches, workspace)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("Herdr workspace label %q has %d live matches", label, len(matches))
	}
	workspace := matches[0]
	if filepath.Clean(workspace.Path) != filepath.Clean(path) ||
		filepath.Clean(workspace.RepoKey) != filepath.Clean(repoKey) ||
		filepath.Clean(workspace.RepoRoot) != filepath.Clean(repoRoot) {
		return nil, fmt.Errorf("reopened Herdr workspace does not match saved Git provenance")
	}
	return &workspace, nil
}

func herdrWorkspaceMatchesResource(
	workspace herdrrun.WorkspaceObservation,
	resource state.HerdrResource,
) bool {
	return workspace.WorkspaceID == resource.WorkspaceID &&
		workspace.Label == resource.Label &&
		filepath.Clean(workspace.Path) == filepath.Clean(resource.CurrentPath) &&
		filepath.Clean(workspace.RepoKey) == filepath.Clean(resource.RepoKey) &&
		filepath.Clean(workspace.RepoRoot) == filepath.Clean(resource.RepoRoot)
}

func verifyHerdrTerminalInvalidation(
	workspace herdrrun.WorkspaceObservation,
	resource state.HerdrResource,
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
	resource state.HerdrResource,
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

func herdrCoordinatorRuntimeParent(pane state.Pane) string {
	if pane.RuntimeParent == watcherStandaloneParent {
		return strconv.Itoa(pane.IssueNum)
	}
	return pane.RuntimeParent
}

func findHerdrCoordinatorPane(locked *state.LockedStore, target state.Pane) (state.Pane, error) {
	want := herdrCoordinatorRuntimeParent(target)
	var matches []state.Pane
	for _, pane := range locked.Panes {
		if pane.Parent == "@manual" && pane.IsShell() && backend.NormalizeName(pane.Backend) == backend.Herdr &&
			pane.RuntimeParent == want {
			matches = append(matches, pane)
		}
	}
	if len(matches) != 1 {
		return state.Pane{}, fmt.Errorf("saved Herdr coordinator identity has %d state matches", len(matches))
	}
	if err := validateHerdrCoordinatorIdentity(matches[0]); err != nil {
		return state.Pane{}, err
	}
	return matches[0], nil
}

func validateHerdrCoordinatorIdentity(pane state.Pane) error {
	required := []string{
		pane.PaneID,
		pane.HerdrWorkspaceID,
		pane.HerdrWorkspaceLabel,
		pane.HerdrTerminalID,
		pane.HerdrSession,
		pane.HerdrSocketPath,
		pane.WorktreePath,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("saved Herdr coordinator identity is incomplete")
		}
	}
	return nil
}

func observeHerdrCoordinator(
	ctx context.Context,
	runtime HerdrRuntime,
	pane state.Pane,
) (herdrrun.WorkspaceObservation, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return herdrrun.WorkspaceObservation{}, err
	}
	resource := state.HerdrResource{
		WorkspaceID: pane.HerdrWorkspaceID,
		Label:       pane.HerdrWorkspaceLabel,
		PaneID:      pane.PaneID,
		TerminalID:  pane.HerdrTerminalID,
		CurrentPath: filepath.Clean(pane.WorktreePath),
	}
	workspace, err := matchHerdrCoordinatorWorkspace(workspaces, resource)
	if err != nil {
		return herdrrun.WorkspaceObservation{}, err
	}
	return *workspace, nil
}

func matchHerdrCoordinatorWorkspace(
	workspaces []herdrrun.WorkspaceObservation,
	resource state.HerdrResource,
) (*herdrrun.WorkspaceObservation, error) {
	var matches []herdrrun.WorkspaceObservation
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == resource.WorkspaceID || workspace.Label == resource.Label {
			matches = append(matches, workspace)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("saved Herdr coordinator identity has %d live matches", len(matches))
	}
	workspace := matches[0]
	if workspace.WorkspaceID != resource.WorkspaceID || workspace.Label != resource.Label ||
		filepath.Clean(workspace.CWD) != filepath.Clean(resource.CurrentPath) ||
		workspace.Pane.Pane != resource.PaneID || workspace.TerminalID != resource.TerminalID {
		return nil, fmt.Errorf("saved Herdr coordinator identity does not match the live workspace")
	}
	return &workspace, nil
}
