package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
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
		CurrentPath: state.CleanRuntimeResourcePath(pane.WorktreePath),
		RepoKey:     state.CleanRuntimeResourcePath(pane.RepoKey),
		RepoRoot:    state.CleanRuntimeResourcePath(pane.RepoRoot),
	}
}

// A finalized attach consumes its launch intent into the row. Direct providers
// have no telemetry nonce; their saved agent record and executable bind the launch.
func validateSharedAttachedWorkspaceIdentity(pane state.Pane) error {
	required := []string{
		pane.Parent, pane.SourceParent, pane.PaneID, pane.WorkspaceID, pane.WorkspaceLabel,
		pane.TerminalID, pane.SessionID, pane.SocketPath, pane.Agent, pane.AgentID,
		pane.WorktreePath, pane.BranchName, pane.LaunchExecutable,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: saved attached launch identity is incomplete", backend.ErrOwnedIdentityMismatch)
		}
	}
	if slices.Contains([]bool{
		workspaceRuntimeRow(pane), pane.IsAttachedAgent(), pane.IssueNum < 0, pane.TaskID == "",
		!pane.BranchCreated, filepath.IsAbs(pane.WorktreePath), filepath.IsAbs(pane.LaunchExecutable),
		validSharedAttachedLaunchGeneration(pane),
	}, false) {
		return fmt.Errorf("%w: saved attached row is not a shared launch", backend.ErrOwnedIdentityMismatch)
	}
	return nil
}

func validSharedAttachedLaunchGeneration(pane state.Pane) bool {
	if pane.EmitterRowKey == "" && pane.LaunchNonce == "" && pane.EmitterNonce == "" {
		return true // Direct providers have no telemetry generation.
	}
	return pane.EmitterRowKey != "" && telemetry.ValidNonce(pane.LaunchNonce) && telemetry.ValidNonce(pane.EmitterNonce)
}

func sharedAttachedLaunchIntent(journal *state.LockedLaunchJournal, projectRoot string, pane state.Pane) (state.LaunchIntent, bool, error) {
	id, err := state.CoordinatorIntentID(panelaunch.ManualParentRef, projectRoot, pane.IssueNum)
	if err != nil {
		return state.LaunchIntent{}, false, err
	}
	intent, found := journal.FindIntent(id)
	if !found {
		return intent, false, nil // Successful finalization consumes this intent.
	}
	savedID, identityErr := state.CoordinatorIntentID(intent.Parent, intent.OwnerProjectRoot, intent.IssueNum)
	if identityErr != nil || savedID != id || !sharedAttachedLaunchMatches(intent, pane) {
		return intent, true, fmt.Errorf("%w: attached launch intent conflicts with its saved row", backend.ErrOwnedIdentityMismatch)
	}
	return intent, true, nil
}

func sharedAttachedLaunchMatches(intent state.LaunchIntent, pane state.Pane) bool {
	launch := intent.Launch
	if launch == nil {
		return false
	}
	return !slices.Contains([]bool{
		intent.Kind == state.IntentCoordinator,
		intent.Status == state.IntentRealized || intent.Status == state.IntentManualCleanupRequired,
		intent.RuntimeParent == pane.RuntimeParent || pane.RuntimeParent == "" && intent.RuntimeParent == panelaunch.ManualParentRef,
		intent.WorkspaceLabel == pane.WorkspaceLabel, launchResourceMatchesCleanupPane(intent.Resource, pane),
		intent.Session == pane.SessionID, intent.SocketPath == pane.SocketPath,
		launch.TokenIssued, launch.Agent == pane.Agent, launch.AgentName == pane.AgentID,
		launch.Executable == pane.LaunchExecutable, slices.Equal(launch.Args, pane.LaunchArgs),
		pane.LaunchNonce == "" || pane.LaunchNonce == launch.Nonce,
		pane.EmitterRowKey == "" || pane.EmitterRowKey == intent.ID,
	}, false)
}

func sharedAttachedSourceMatches(pane, child state.Pane) bool {
	return pane.SourceParent == child.Parent && pane.SourceIssueNum == child.IssueNum &&
		pane.SourceTaskID == child.TaskID
}

func sharedAttachedWorkspaceOwner(locked *state.LockedStore, pane state.Pane) (state.Pane, error) {
	var matches []state.Pane
	for _, candidate := range locked.Panes {
		if sharedAttachedSourceMatches(pane, candidate) && !candidate.IsShell() && !candidate.IsAttachedAgent() {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return state.Pane{}, fmt.Errorf("%w: saved attached source has %d child rows", backend.ErrOwnedIdentityMismatch, len(matches))
	}
	child := matches[0]
	if err := validateWorkspacePaneIdentity(child); err != nil {
		return state.Pane{}, fmt.Errorf("%w: %w", backend.ErrOwnedIdentityMismatch, err)
	}
	if !sharedAttachedCheckoutMatches(pane, child) {
		return state.Pane{}, fmt.Errorf("%w: saved attached source checkout or route changed", backend.ErrOwnedIdentityMismatch)
	}
	return child, nil
}

func sharedAttachedCheckoutMatches(pane, child state.Pane) bool {
	return !slices.Contains([]bool{
		filepath.Clean(pane.WorktreePath) == filepath.Clean(child.WorktreePath),
		pane.BranchName == child.BranchName, pane.SessionID == child.SessionID, pane.SocketPath == child.SocketPath,
		pane.RuntimeParent == "" || pane.RuntimeParent == panelaunch.ManualParentRef || pane.RuntimeParent == child.RuntimeParent,
		pane.RepoKey == "" && pane.RepoRoot == "" || pane.RepoKey == child.RepoKey && pane.RepoRoot == child.RepoRoot,
	}, false)
}

func attachedWorkspacePredicate(resource state.RuntimeResource) workspacePredicateFunc {
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		candidate := workspace.WorkspaceID == resource.WorkspaceID || workspace.Label == resource.Label ||
			slices.ContainsFunc(workspace.Panes, func(pane backend.WorkspacePaneObservation) bool { return pane.TerminalID == resource.TerminalID })
		exact := workspace.WorkspaceID == resource.WorkspaceID && workspace.Label == resource.Label
		// A generic workspace with no panes has no cwd left to report. Its saved
		// label and owned route still fence the workspace; the source owns checkout.
		if !slices.Contains([]bool{
			len(workspace.Panes) == 0, workspace.CWD == "", workspace.Path == "", workspace.RepoKey == "", workspace.RepoRoot == "",
		}, false) {
			return candidate, exact && resource.RepoKey == "" && resource.RepoRoot == ""
		}
		checkout := backend.CheckoutMatchesLive(resource.RepoKey, resource.CurrentPath, backend.LivePane{
			WorktreePath: workspace.Path, CurrentPath: workspace.CWD,
			RepoKey: workspace.RepoKey, ProjectRoot: workspace.RepoRoot,
		})
		return candidate, exact && checkout && (resource.RepoRoot == "" || resource.RepoRoot == workspace.RepoRoot)
	}
}

func resourceFromObservation(observation backend.WorkspaceObservation) state.RuntimeResource {
	return state.RuntimeResourceFromObservation(observation)
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
	return observeWorkspaceCleanupSnapshot(ctx, projectRoot, resource, predicate, workspaces)
}

func observeWorkspaceCleanupSnapshot(
	ctx context.Context,
	projectRoot string,
	resource state.RuntimeResource,
	predicate workspacePredicateFunc,
	workspaces []backend.WorkspaceObservation,
) (workspaceCleanupObservation, error) {
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

func persistManagedPaneLocation(
	locked *state.LockedStore,
	previous state.Pane,
	current state.Pane,
) error {
	index, err := locked.EmitterRowIndex(
		previous.EmitterRowKey, filepath.Clean(previous.WorktreePath), previous.WorkspaceLabel,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", backend.ErrOwnedIdentityMismatch, err)
	}
	if index < 0 || !locked.Panes[index].RuntimeBinding().Equal(previous.RuntimeBinding()) ||
		locked.Panes[index].RepoRoot != previous.RepoRoot {
		return fmt.Errorf("%w: managed pane row identity changed before location update", backend.ErrOwnedIdentityMismatch)
	}
	saved := locked.Panes[index]
	copyManagedPaneLocationFields(&locked.Panes[index], current)
	if err := locked.Save(); err != nil {
		copyManagedPaneLocationFields(&locked.Panes[index], saved)
		return err
	}
	return nil
}

func copyManagedPaneLocationFields(target *state.Pane, source state.Pane) {
	target.WorkspaceID = source.WorkspaceID
	target.PaneID = source.PaneID
	target.TerminalID = source.TerminalID
	target.ReportedState = source.ReportedState
	target.ReportedStateSeq = source.ReportedStateSeq
	target.StateRefinement = source.StateRefinement
	target.EmitterNonce = source.EmitterNonce
	target.EmitterRebindNonce = source.EmitterRebindNonce
	target.EmitterRebindSequence = source.EmitterRebindSequence
}

func reconcileManagedPaneLocation(
	ctx context.Context,
	locked *state.LockedStore,
	pane state.Pane,
	workspaces []backend.WorkspaceObservation,
) (state.Pane, bool, error) {
	current, changed, err := panelaunch.ReconcileManagedPaneLocation(
		pane, workspaces, func() (uint64, error) { return locked.FenceTelemetrySequence(ctx) },
	)
	if err != nil || !changed {
		return pane, false, err
	}
	if err := persistManagedPaneLocation(locked, pane, current); err != nil {
		return pane, false, err
	}
	return current, true, nil
}

func reconcileManagedPaneLocationAfterMismatch(
	ctx context.Context,
	locked *state.LockedStore,
	pane state.Pane,
	workspaces []backend.WorkspaceObservation,
	mismatch error,
) (state.Pane, error) {
	current, changed, err := reconcileManagedPaneLocation(ctx, locked, pane, workspaces)
	if err != nil {
		return pane, err
	}
	if !changed {
		return pane, mismatch
	}
	return current, nil
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
		return nil, fmt.Errorf("%w: herdr workspace identity has %d live matches", backend.ErrOwnedIdentityMismatch, len(candidates))
	}
	_, exact := predicate(candidates[0])
	if !exact {
		return nil, fmt.Errorf("%w: herdr workspace identity does not match the live workspace", backend.ErrOwnedIdentityMismatch)
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
			return fmt.Errorf("%w: saved Herdr terminal identity was reused by a different pane", backend.ErrOwnedIdentityMismatch)
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
	if err != nil {
		if errors.Is(err, worktree.ErrCheckoutMismatch) {
			return fmt.Errorf("%w: %w", backend.ErrOwnedIdentityMismatch, err)
		}
		return err
	}
	return nil
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
