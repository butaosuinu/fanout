package panelaunch

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type HerdrCoordinatorRequest struct {
	Parent           string
	ProjectRoot      string
	SourceRoot       string
	RootCWD          string
	PlanSpecIdentity string
	HerdrSession     string
	HerdrSocketPath  string
	TotalTimeout     time.Duration
}

type HerdrCoordinatorResult struct {
	Intent state.HerdrLaunchIntent
	Pane   backend.PaneRef
}

// RealizeHerdrCoordinator persists workspace-planned before issuing the
// coordinator workspace create and stops at workspace-realized. It shares the
// child runtime adapter but leaves launcher readiness and workspace-ready to
// issue #528.
func RealizeHerdrCoordinator(
	ctx context.Context,
	req HerdrCoordinatorRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
) (HerdrCoordinatorResult, error) {
	if ctx == nil || runtime == nil {
		return HerdrCoordinatorResult{}, fmt.Errorf("realize herdr coordinator requires context and runtime")
	}
	if err := validateHerdrCoordinatorRequest(req); err != nil {
		return HerdrCoordinatorResult{}, err
	}
	hooks = normalizeHerdrWorktreeHooks(hooks)
	sourceIdentity, err := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if err != nil {
		return HerdrCoordinatorResult{}, err
	}
	intentID, err := state.HerdrCoordinatorIntentID(
		req.Parent,
		sourceIdentity.RepoRoot,
		req.PlanSpecIdentity,
	)
	if err != nil {
		return HerdrCoordinatorResult{}, err
	}
	control, err := state.LoadHerdrControl(req.ProjectRoot)
	if err != nil {
		return HerdrCoordinatorResult{}, err
	}
	intent, found := control.FindIntent(intentID)
	if found {
		if validateErr := validateSavedHerdrCoordinatorIntent(req, intent); validateErr != nil {
			return HerdrCoordinatorResult{}, validateErr
		}
	} else {
		intent, err = planHerdrCoordinator(req, intentID, hooks)
		if err != nil {
			return HerdrCoordinatorResult{}, err
		}
		if notifyErr := notifyHerdrPhase(hooks, intent.Phase); notifyErr != nil {
			return HerdrCoordinatorResult{}, notifyErr
		}
	}

	for {
		if intent.OperationState == state.HerdrOperationManualCleanupRequired {
			return HerdrCoordinatorResult{}, fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, intent.FailureReason)
		}
		if intent.OperationState != state.HerdrOperationActive {
			return HerdrCoordinatorResult{}, fmt.Errorf("herdr coordinator intent is terminal: %s", intent.OperationState)
		}
		if hooks.Now().UTC().UnixMilli() >= intent.LaunchExpiresUnixMS {
			return HerdrCoordinatorResult{}, failHerdrIntent(req.ProjectRoot, intent, "herdr coordinator launch deadline expired")
		}
		switch intent.Phase {
		case state.HerdrPhaseWorkspacePlanned:
			intent, err = createHerdrCoordinator(ctx, req, runtime, hooks, intent)
			if err != nil {
				return HerdrCoordinatorResult{}, err
			}
		case state.HerdrPhaseWorkspaceStarting:
			var err error
			intent, err = reconcileHerdrCoordinatorMutation(ctx, req, runtime, hooks, intent)
			if err != nil {
				return HerdrCoordinatorResult{}, failHerdrIntent(
					req.ProjectRoot,
					intent,
					"coordinator workspace result is not provable without retry: "+err.Error(),
				)
			}
		case state.HerdrPhaseWorkspaceRealized:
			if err := verifyHerdrCoordinatorRealized(ctx, req, runtime, hooks, intent); err != nil {
				return HerdrCoordinatorResult{}, err
			}
			return herdrCoordinatorDeferredResult(intent), ErrHerdrLauncherReadinessDeferred
		case state.HerdrPhaseWorkspaceReady:
			return HerdrCoordinatorResult{Intent: intent}, ErrHerdrLauncherReadinessDeferred
		default:
			return HerdrCoordinatorResult{}, failHerdrIntent(req.ProjectRoot, intent, "unsupported coordinator phase "+string(intent.Phase))
		}
	}
}

func planHerdrCoordinator(
	req HerdrCoordinatorRequest,
	intentID string,
	hooks HerdrWorktreeHooks,
) (state.HerdrLaunchIntent, error) {
	projectIdentity, err := worktree.ResolveHerdrRepoIdentity(req.ProjectRoot)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	sourceIdentity, err := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	rootIdentity, err := worktree.ResolveHerdrRepoIdentity(req.RootCWD)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	rootCWD, err := physicalPath(req.RootCWD)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	if projectIdentity.RepoKey != sourceIdentity.RepoKey ||
		rootIdentity.RepoKey != sourceIdentity.RepoKey ||
		rootCWD != sourceIdentity.RepoRoot {
		return state.HerdrLaunchIntent{}, fmt.Errorf("herdr coordinator roots do not identify one source repository root")
	}
	launchNonce, err := hooks.Random()
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	label, err := hooks.Random()
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	now := hooks.Now().UTC()
	intent := state.HerdrLaunchIntent{
		IntentID:               intentID,
		Parent:                 req.Parent,
		Backend:                backend.Herdr,
		Operation:              "coordinator-workspace",
		OperationState:         state.HerdrOperationActive,
		Phase:                  state.HerdrPhaseWorkspacePlanned,
		SourceRootPhysical:     sourceIdentity.RepoRoot,
		PlanSpecIdentity:       req.PlanSpecIdentity,
		WorktreePath:           rootCWD,
		HerdrRepoKey:           sourceIdentity.RepoKey,
		HerdrRepoRoot:          sourceIdentity.RepoRoot,
		HerdrSession:           req.HerdrSession,
		HerdrSocketPath:        req.HerdrSocketPath,
		WorktreeOwnershipNonce: label,
		LaunchNonce:            launchNonce,
		TotalTimeoutMS:         req.TotalTimeout.Milliseconds(),
		LaunchStartedUnixMS:    now.UnixMilli(),
		LaunchExpiresUnixMS:    now.Add(req.TotalTimeout).UnixMilli(),
	}
	locked, err := state.LockHerdrControl(req.ProjectRoot)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	defer func() {
		// Preserve the planning result; Unlock still closes the descriptor.
		_ = locked.Unlock()
	}()
	if _, found := locked.FindIntent(intentID); found {
		return state.HerdrLaunchIntent{}, fmt.Errorf("herdr coordinator intent appeared while planning")
	}
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	return intent, nil
}

func createHerdrCoordinator(
	ctx context.Context,
	req HerdrCoordinatorRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
	intent state.HerdrLaunchIntent,
) (state.HerdrLaunchIntent, error) {
	readCtx, readCancel, err := boundedHerdrReadContext(ctx, intent, hooks.Now())
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	observed, err := runtime.ObserveWorkspaces(readCtx)
	readCancel()
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, "observe coordinator pre-state: "+err.Error())
	}
	for _, workspace := range observed {
		if workspace.Label == intent.WorktreeOwnershipNonce {
			return intent, failHerdrIntent(req.ProjectRoot, intent, "coordinator ownership label already exists")
		}
	}
	policyCtx, policyCancel, err := boundedHerdrReadContext(ctx, intent, hooks.Now())
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	policyErr := runtime.VerifyWorktreeSetupPolicy(policyCtx)
	policyCancel()
	if policyErr != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, "verify herdr setup-hook policy: "+policyErr.Error())
	}
	preState := mutationPreState(observed, state.HerdrGitPreState{})
	request := expectedHerdrCoordinatorMutationRequest(intent)
	mutationCtx, mutationCancel, err := remainingHerdrMutationContext(ctx, intent, hooks.Now())
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	defer mutationCancel()
	starting, err := transitionHerdrIntent(req.ProjectRoot, intent, state.HerdrPhaseWorkspacePlanned, func(next *state.HerdrLaunchIntent) {
		next.MutationRequest = &request
		next.MutationPreState = &preState
		next.Phase = state.HerdrPhaseWorkspaceStarting
	})
	if err != nil {
		return intent, err
	}
	if notifyErr := notifyHerdrPhase(hooks, starting.Phase); notifyErr != nil {
		return starting, notifyErr
	}
	result, err := runtime.MutateWorktree(mutationCtx, toHerdrMutationRequest(request))
	if err != nil {
		reconciled, reconcileErr := reconcileHerdrCoordinatorMutation(ctx, req, runtime, hooks, starting)
		if reconcileErr != nil {
			reason := fmt.Sprintf(
				"coordinator workspace mutation failed or response was incomplete: %v; exact post-state reconciliation failed: %v",
				err,
				reconcileErr,
			)
			return starting, failHerdrIntent(req.ProjectRoot, starting, reason)
		}
		return reconciled, nil
	}
	if validateErr := validateHerdrCoordinatorMutationResult(starting, result); validateErr != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, validateErr.Error())
	}
	return completeHerdrCoordinatorMutation(req, hooks, starting, result)
}

func reconcileHerdrCoordinatorMutation(
	ctx context.Context,
	req HerdrCoordinatorRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
	starting state.HerdrLaunchIntent,
) (state.HerdrLaunchIntent, error) {
	callCtx, cancel, err := boundedHerdrReadContext(ctx, starting, hooks.Now())
	if err != nil {
		return starting, err
	}
	defer cancel()
	observed, err := runtime.ObserveWorkspaces(callCtx)
	if err != nil {
		return starting, fmt.Errorf("observe uncertain coordinator mutation: %w", err)
	}
	result, err := proveHerdrCoordinatorMutationResult(starting, observed)
	if err != nil {
		return starting, err
	}
	return completeHerdrCoordinatorMutation(req, hooks, starting, result)
}

func proveHerdrCoordinatorMutationResult(
	intent state.HerdrLaunchIntent,
	observed []herdrrun.WorkspaceObservation,
) (herdrrun.WorktreeMutationResult, error) {
	if intent.MutationRequest == nil || intent.MutationPreState == nil {
		return herdrrun.WorktreeMutationResult{}, fmt.Errorf("saved coordinator mutation request/pre-state is missing")
	}
	var candidates []herdrrun.WorkspaceObservation
	for _, workspace := range observed {
		if workspace.Label == intent.MutationRequest.Label {
			candidates = append(candidates, workspace)
		}
	}
	if len(candidates) != 1 {
		return herdrrun.WorktreeMutationResult{}, fmt.Errorf(
			"uncertain coordinator mutation has %d nonce candidates",
			len(candidates),
		)
	}
	result := herdrrun.WorktreeMutationResult{WorkspaceObservation: candidates[0]}
	if err := validateHerdrCoordinatorMutationResult(intent, result); err != nil {
		return herdrrun.WorktreeMutationResult{}, err
	}
	return result, nil
}

func validateHerdrCoordinatorMutationResult(
	intent state.HerdrLaunchIntent,
	result herdrrun.WorktreeMutationResult,
) error {
	if intent.MutationRequest == nil || intent.MutationPreState == nil {
		return fmt.Errorf("saved coordinator mutation request/pre-state is missing")
	}
	request := intent.MutationRequest
	if request.Kind != state.HerdrMutationWorkspaceCreate ||
		result.AlreadyOpen ||
		result.WorkspaceID == "" ||
		result.Label != request.Label ||
		result.Path != "" ||
		result.RepoKey != "" ||
		result.RepoRoot != "" ||
		filepath.Clean(result.CWD) != filepath.Clean(request.CWD) ||
		result.Pane.Backend != backend.Herdr ||
		result.Pane.Workspace != result.WorkspaceID ||
		result.Pane.Pane == "" ||
		result.TerminalID == "" {
		return fmt.Errorf("coordinator workspace mutation result does not match saved exact request")
	}
	for _, workspace := range intent.MutationPreState.Workspaces {
		if workspace.WorkspaceID == result.WorkspaceID {
			return fmt.Errorf("coordinator workspace mutation reused a pre-state workspace id")
		}
	}
	return nil
}

func completeHerdrCoordinatorMutation(
	req HerdrCoordinatorRequest,
	hooks HerdrWorktreeHooks,
	starting state.HerdrLaunchIntent,
	result herdrrun.WorktreeMutationResult,
) (state.HerdrLaunchIntent, error) {
	receipt := mutationReceipt(result, "")
	realized, err := transitionHerdrIntent(req.ProjectRoot, starting, state.HerdrPhaseWorkspaceStarting, func(next *state.HerdrLaunchIntent) {
		next.MutationReceipt = &receipt
		next.Phase = state.HerdrPhaseWorkspaceRealized
	})
	if err != nil {
		return starting, err
	}
	if err := notifyHerdrPhase(hooks, realized.Phase); err != nil {
		return realized, err
	}
	return realized, nil
}

func verifyHerdrCoordinatorRealized(
	ctx context.Context,
	req HerdrCoordinatorRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
	intent state.HerdrLaunchIntent,
) error {
	callCtx, cancel, err := boundedHerdrReadContext(ctx, intent, hooks.Now())
	if err != nil {
		return failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	defer cancel()
	if intent.MutationReceipt == nil {
		return failHerdrIntent(req.ProjectRoot, intent, "coordinator receipt is missing")
	}
	observed, err := runtime.ObserveWorkspaces(callCtx)
	if err != nil {
		return failHerdrIntent(req.ProjectRoot, intent, "observe realized coordinator: "+err.Error())
	}
	matches := 0
	for _, workspace := range observed {
		if workspace.WorkspaceID != intent.MutationReceipt.WorkspaceID {
			continue
		}
		matches++
		if workspace.Label != intent.WorktreeOwnershipNonce ||
			workspace.Path != "" || workspace.RepoKey != "" || workspace.RepoRoot != "" ||
			workspace.Pane.Backend != backend.Herdr ||
			workspace.Pane.Workspace != workspace.WorkspaceID ||
			workspace.Pane.Pane != intent.MutationReceipt.PaneID ||
			workspace.TerminalID != intent.MutationReceipt.TerminalID ||
			filepath.Clean(workspace.CWD) != filepath.Clean(intent.WorktreePath) {
			return failHerdrIntent(req.ProjectRoot, intent, "coordinator identity changed before launcher handoff")
		}
	}
	if matches != 1 {
		return failHerdrIntent(req.ProjectRoot, intent, fmt.Sprintf("coordinator has %d matching workspaces", matches))
	}
	return nil
}

func herdrCoordinatorDeferredResult(intent state.HerdrLaunchIntent) HerdrCoordinatorResult {
	return HerdrCoordinatorResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend:   backend.Herdr,
			Workspace: intent.MutationReceipt.WorkspaceID,
			Pane:      intent.MutationReceipt.PaneID,
		},
	}
}

func validateHerdrCoordinatorRequest(req HerdrCoordinatorRequest) error {
	if req.ProjectRoot == "" || req.SourceRoot == "" || req.RootCWD == "" ||
		req.HerdrSession == "" || req.HerdrSocketPath == "" {
		return fmt.Errorf("herdr coordinator request is incomplete")
	}
	if req.TotalTimeout < minHerdrWorktreeTimeout || req.TotalTimeout > maxHerdrWorktreeTimeout ||
		req.TotalTimeout%time.Millisecond != 0 {
		return fmt.Errorf("herdr coordinator total timeout must be 3s..300s at millisecond precision")
	}
	return nil
}

func validateSavedHerdrCoordinatorIntent(req HerdrCoordinatorRequest, intent state.HerdrLaunchIntent) error {
	projectIdentity, err := worktree.ResolveHerdrRepoIdentity(req.ProjectRoot)
	if err != nil {
		return err
	}
	sourceIdentity, err := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if err != nil {
		return err
	}
	rootIdentity, err := worktree.ResolveHerdrRepoIdentity(req.RootCWD)
	if err != nil {
		return err
	}
	rootCWD, err := physicalPath(req.RootCWD)
	if err != nil {
		return err
	}
	if projectIdentity.RepoKey != sourceIdentity.RepoKey ||
		rootIdentity.RepoKey != sourceIdentity.RepoKey ||
		rootCWD != sourceIdentity.RepoRoot {
		return fmt.Errorf("herdr coordinator roots do not identify one source repository root")
	}
	intentID, err := state.HerdrCoordinatorIntentID(
		req.Parent,
		sourceIdentity.RepoRoot,
		req.PlanSpecIdentity,
	)
	if err != nil {
		return err
	}
	if intent.IntentID != intentID ||
		intent.Backend != backend.Herdr ||
		intent.Operation != "coordinator-workspace" ||
		intent.Parent != req.Parent ||
		intent.IssueNum != 0 ||
		intent.TaskID != "" ||
		intent.SourceRootPhysical != sourceIdentity.RepoRoot ||
		intent.PlanSpecIdentity != req.PlanSpecIdentity ||
		intent.HerdrRepoKey != sourceIdentity.RepoKey ||
		intent.HerdrRepoRoot != sourceIdentity.RepoRoot ||
		filepath.Clean(intent.WorktreePath) != rootCWD ||
		intent.HerdrSession != req.HerdrSession ||
		intent.HerdrSocketPath != req.HerdrSocketPath ||
		intent.TotalTimeoutMS != req.TotalTimeout.Milliseconds() {
		return fmt.Errorf("saved herdr coordinator intent contradicts the exact launch request")
	}
	if err := validateSavedHerdrCoordinatorMutationEvidence(intent); err != nil {
		return err
	}
	return nil
}

func validateSavedHerdrCoordinatorMutationEvidence(intent state.HerdrLaunchIntent) error {
	switch intent.Phase {
	case state.HerdrPhaseWorkspaceStarting, state.HerdrPhaseWorkspaceRealized:
		expected := expectedHerdrCoordinatorMutationRequest(intent)
		if intent.MutationRequest == nil ||
			intent.MutationPreState == nil ||
			!reflect.DeepEqual(*intent.MutationRequest, expected) {
			return fmt.Errorf("saved herdr coordinator mutation evidence contradicts the exact intent")
		}
		if intent.Phase == state.HerdrPhaseWorkspaceStarting && intent.MutationReceipt != nil {
			return fmt.Errorf("workspace-starting coordinator already has a mutation receipt")
		}
		if intent.Phase == state.HerdrPhaseWorkspaceRealized && intent.MutationReceipt == nil {
			return fmt.Errorf("workspace-realized coordinator has no mutation receipt")
		}
	case state.HerdrPhaseWorkspacePlanned:
		return nil
	case state.HerdrPhaseWorkspaceReady:
		return ErrHerdrLauncherReadinessDeferred
	case state.HerdrPhaseBranchPlanned,
		state.HerdrPhaseBranchStarting,
		state.HerdrPhaseWorktreePlanned,
		state.HerdrPhaseWorktreeStarting,
		state.HerdrPhaseWorktreeRealized,
		state.HerdrPhaseWorktreeReady:
		return fmt.Errorf("coordinator intent has child worktree phase %q", intent.Phase)
	}
	return nil
}

func expectedHerdrCoordinatorMutationRequest(intent state.HerdrLaunchIntent) state.HerdrMutationRequest {
	return state.HerdrMutationRequest{
		Kind:    state.HerdrMutationWorkspaceCreate,
		CWD:     intent.WorktreePath,
		Label:   intent.WorktreeOwnershipNonce,
		NoFocus: true,
	}
}
