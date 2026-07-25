package panelaunch

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type HerdrCoordinatorRequest struct {
	Parent          string
	IssueNum        int
	TaskID          string
	ProjectRoot     string
	SourceRoot      string
	RootCWD         string
	HerdrSession    string
	HerdrSocketPath string
	TotalTimeout    time.Duration
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
	baseID, err := state.HerdrIntentID(req.Parent, req.IssueNum, req.TaskID)
	if err != nil {
		return HerdrCoordinatorResult{}, err
	}
	intentID := baseID + ":coordinator"
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
		if intent.Phase != state.HerdrPhaseWorkspaceReady && hooks.Now().UTC().UnixMilli() >= intent.LaunchExpiresUnixMS {
			return HerdrCoordinatorResult{}, failHerdrIntent(req.ProjectRoot, intent, "herdr coordinator launch deadline expired")
		}
		switch intent.Phase {
		case state.HerdrPhaseWorkspacePlanned:
			intent, err = createHerdrCoordinator(ctx, req, runtime, hooks, intent)
			if err != nil {
				return HerdrCoordinatorResult{}, err
			}
		case state.HerdrPhaseWorkspaceStarting:
			return HerdrCoordinatorResult{}, failHerdrIntent(
				req.ProjectRoot,
				intent,
				"coordinator workspace create may have been issued; refusing blind retry",
			)
		case state.HerdrPhaseWorkspaceRealized:
			if err := verifyHerdrCoordinatorRealized(ctx, req, runtime, hooks, intent); err != nil {
				return HerdrCoordinatorResult{}, err
			}
			return herdrCoordinatorDeferredResult(intent), ErrHerdrLauncherReadinessDeferred
		case state.HerdrPhaseWorkspaceReady:
			if intent.MutationReceipt == nil {
				return HerdrCoordinatorResult{}, failHerdrIntent(req.ProjectRoot, intent, "workspace-ready coordinator has no receipt")
			}
			return HerdrCoordinatorResult{
				Intent: intent,
				Pane: backend.PaneRef{
					Backend:   backend.Herdr,
					Workspace: intent.MutationReceipt.WorkspaceID,
					Pane:      intent.MutationReceipt.PaneID,
				},
			}, nil
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
	sourceRoot, err := physicalPath(req.SourceRoot)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	rootCWD, err := physicalPath(req.RootCWD)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
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
		IssueNum:               req.IssueNum,
		TaskID:                 req.TaskID,
		Backend:                backend.Herdr,
		Operation:              "coordinator-workspace",
		OperationState:         state.HerdrOperationActive,
		Phase:                  state.HerdrPhaseWorkspacePlanned,
		SourceRootPhysical:     sourceRoot,
		WorktreePath:           rootCWD,
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
	callCtx, cancel, err := boundedHerdrCallContext(ctx, intent, hooks.Now())
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	defer cancel()
	observed, err := runtime.ObserveWorkspaces(callCtx)
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, "observe coordinator pre-state: "+err.Error())
	}
	for _, workspace := range observed {
		if workspace.Label == intent.WorktreeOwnershipNonce {
			return intent, failHerdrIntent(req.ProjectRoot, intent, "coordinator ownership label already exists")
		}
	}
	if policyErr := runtime.VerifyWorktreeSetupPolicy(callCtx); policyErr != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, "verify herdr setup-hook policy: "+policyErr.Error())
	}
	preState := mutationPreState(observed, state.HerdrGitPreState{})
	request := state.HerdrMutationRequest{
		Kind:    state.HerdrMutationWorkspaceCreate,
		CWD:     intent.WorktreePath,
		Label:   intent.WorktreeOwnershipNonce,
		NoFocus: true,
	}
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
	result, err := runtime.MutateWorktree(callCtx, toHerdrMutationRequest(request))
	if err != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, "coordinator workspace mutation failed or response was incomplete: "+err.Error())
	}
	if result.AlreadyOpen || result.WorkspaceID == "" || result.Label != intent.WorktreeOwnershipNonce ||
		result.Path != "" || result.RepoKey != "" || result.RepoRoot != "" ||
		filepath.Clean(result.CWD) != filepath.Clean(intent.WorktreePath) ||
		result.Pane.Pane == "" || result.TerminalID == "" {
		return starting, failHerdrIntent(req.ProjectRoot, starting, "coordinator workspace response does not match intent")
	}
	for _, workspace := range preState.Workspaces {
		if workspace.WorkspaceID == result.WorkspaceID {
			return starting, failHerdrIntent(req.ProjectRoot, starting, "coordinator workspace response reused a pre-state workspace id")
		}
	}
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
	callCtx, cancel, err := boundedHerdrCallContext(ctx, intent, hooks.Now())
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
	sourceRoot, err := physicalPath(req.SourceRoot)
	if err != nil {
		return err
	}
	rootCWD, err := physicalPath(req.RootCWD)
	if err != nil {
		return err
	}
	if intent.Backend != backend.Herdr ||
		intent.Operation != "coordinator-workspace" ||
		intent.Parent != req.Parent ||
		intent.IssueNum != req.IssueNum ||
		intent.TaskID != req.TaskID ||
		intent.SourceRootPhysical != sourceRoot ||
		filepath.Clean(intent.WorktreePath) != rootCWD ||
		intent.HerdrSession != req.HerdrSession ||
		intent.HerdrSocketPath != req.HerdrSocketPath ||
		intent.TotalTimeoutMS != req.TotalTimeout.Milliseconds() {
		return fmt.Errorf("saved herdr coordinator intent contradicts the exact launch request")
	}
	return nil
}
