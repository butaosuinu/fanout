package panelaunch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const (
	minHerdrWorktreeTimeout = 3 * time.Second
	maxHerdrWorktreeTimeout = 300 * time.Second
	herdrReadStartInterval  = 2 * time.Second
)

var (
	ErrHerdrManualCleanupRequired     = errors.New("herdr launch requires manual cleanup")
	ErrHerdrLauncherReadinessDeferred = errors.New("herdr launcher readiness is deferred to issue #528")
)

type HerdrWorktreeRuntime interface {
	VerifyWorktreeSetupPolicy(context.Context) error
	ObserveWorkspaces(context.Context) ([]herdrrun.WorkspaceObservation, error)
	MutateWorktree(context.Context, herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error)
}

type HerdrWorktreeRequest struct {
	Parent           string
	IssueNum         int
	TaskID           string
	ProjectRoot      string
	SourceRoot       string
	PlanSpecIdentity string
	Slug             string
	BranchName       string
	BaseBranch       string
	NoRefresh        bool
	WorktreePath     string

	CoordinatorWorkspaceID string
	HerdrSession           string
	HerdrSocketPath        string
	TotalTimeout           time.Duration
}

type HerdrWorktreeResult struct {
	Intent state.HerdrLaunchIntent
	Pane   backend.PaneRef
}

// HerdrWorktreeHooks is an internal seam for deterministic tests and future
// operation-lease wiring. Production callers normally leave it zero-valued.
type HerdrWorktreeHooks struct {
	Now        func() time.Time
	Random     func() (string, error)
	PhaseSaved func(state.HerdrLaunchPhase) error
	Sleep      func(context.Context, time.Duration) error
}

// RealizeHerdrWorktree advances through worktree-realized. It deliberately
// fails closed at the launcher boundary: issue #528 owns launcher readiness,
// token issuance, agent start, and the transition to worktree-ready.
func RealizeHerdrWorktree(
	ctx context.Context,
	req HerdrWorktreeRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
) (HerdrWorktreeResult, error) {
	if ctx == nil {
		return HerdrWorktreeResult{}, fmt.Errorf("realize herdr worktree requires a context")
	}
	if runtime == nil {
		return HerdrWorktreeResult{}, fmt.Errorf("realize herdr worktree requires a runtime")
	}
	if err := validateHerdrWorktreeRequest(req); err != nil {
		return HerdrWorktreeResult{}, err
	}
	hooks = normalizeHerdrWorktreeHooks(hooks)

	sourceIdentity, err := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if err != nil {
		return HerdrWorktreeResult{}, err
	}
	intentID, err := state.HerdrIntentID(
		req.Parent,
		req.IssueNum,
		req.TaskID,
		sourceIdentity.RepoRoot,
		sourceIdentity.GitDir,
		sourceIdentity.GitDirDevice,
		sourceIdentity.GitDirInode,
		req.PlanSpecIdentity,
	)
	if err != nil {
		return HerdrWorktreeResult{}, err
	}
	control, err := state.LoadHerdrControl(req.ProjectRoot)
	if err != nil {
		return HerdrWorktreeResult{}, err
	}
	intent, found := control.FindIntent(intentID)
	if found {
		if validateErr := validateSavedHerdrWorktreeIntent(req, intent); validateErr != nil {
			return HerdrWorktreeResult{}, validateErr
		}
	} else {
		intent, err = planFreshHerdrWorktree(req, intentID, hooks)
		if err != nil {
			return HerdrWorktreeResult{}, err
		}
		if notifyErr := notifyHerdrPhase(hooks, intent.Phase); notifyErr != nil {
			return HerdrWorktreeResult{}, notifyErr
		}
	}
	operationCtx, operationCancel, err := herdrOperationContext(ctx, intent, hooks.Now())
	if err != nil {
		return HerdrWorktreeResult{}, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	defer operationCancel()
	return advanceHerdrWorktree(operationCtx, req, runtime, hooks, intent)
}

func planFreshHerdrWorktree(
	req HerdrWorktreeRequest,
	intentID string,
	hooks HerdrWorktreeHooks,
) (state.HerdrLaunchIntent, error) {
	projectIdentity, err := worktree.ResolveHerdrRepoIdentity(req.ProjectRoot)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	repoIdentity, err := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	if projectIdentity.RepoKey != repoIdentity.RepoKey {
		return state.HerdrLaunchIntent{}, fmt.Errorf("herdr project root and source root belong to different repositories")
	}
	if err := worktree.EnsureHerdrWorktreeParent(req.ProjectRoot, req.WorktreePath); err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	if ensureErr := worktree.EnsureLocalExclude(req.SourceRoot); ensureErr != nil {
		return state.HerdrLaunchIntent{}, ensureErr
	}
	base, err := worktree.ResolveHerdrBase(req.SourceRoot, req.BaseBranch, req.NoRefresh)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	fullBranchRef, err := worktree.HerdrBranchRef(req.SourceRoot, req.BranchName)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	if oid, found, observeErr := worktree.ObserveBranch(req.SourceRoot, fullBranchRef); observeErr != nil {
		return state.HerdrLaunchIntent{}, observeErr
	} else if found {
		return state.HerdrLaunchIntent{}, fmt.Errorf("herdr branch %s already exists at %s", fullBranchRef, oid)
	}
	pathAbsent, registered, headSHA, err := worktree.CheckoutGitState(req.SourceRoot, req.WorktreePath, fullBranchRef)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	if !pathAbsent || registered {
		return state.HerdrLaunchIntent{}, fmt.Errorf("herdr worktree path %s already exists or is registered", req.WorktreePath)
	}
	launchNonce, err := hooks.Random()
	if err != nil {
		return state.HerdrLaunchIntent{}, fmt.Errorf("create herdr launch nonce: %w", err)
	}
	ownershipNonce, err := hooks.Random()
	if err != nil {
		return state.HerdrLaunchIntent{}, fmt.Errorf("create herdr worktree ownership nonce: %w", err)
	}
	lineageID := herdrLineageID(intentID, launchNonce)
	now := hooks.Now().UTC()
	intent := state.HerdrLaunchIntent{
		IntentID:       intentID,
		Parent:         req.Parent,
		IssueNum:       req.IssueNum,
		TaskID:         req.TaskID,
		Backend:        backend.Herdr,
		Operation:      "child-worktree",
		OperationState: state.HerdrOperationActive,
		Phase:          state.HerdrPhaseBranchPlanned,

		SourceRootPhysical:   repoIdentity.RepoRoot,
		SourceGitDirPhysical: repoIdentity.GitDir,
		SourceGitDirDevice:   repoIdentity.GitDirDevice,
		SourceGitDirInode:    repoIdentity.GitDirInode,
		PlanSpecIdentity:     req.PlanSpecIdentity,
		Slug:                 req.Slug,
		BranchName:           req.BranchName,
		FullBranchRef:        fullBranchRef,
		WorktreePath:         filepath.Clean(req.WorktreePath),
		LineageID:            lineageID,

		ResolvedBaseRef:     base.ResolvedRef,
		ResolvedBaseName:    base.ResolvedName,
		EffectiveBaseBranch: base.EffectiveBase,
		PRBaseName:          base.PRBaseName,
		LineageBaseSHA:      base.SHA,
		LaunchHeadSHA:       base.SHA,

		HerdrSession:           req.HerdrSession,
		HerdrSocketPath:        req.HerdrSocketPath,
		HerdrRepoKey:           repoIdentity.RepoKey,
		HerdrRepoRoot:          repoIdentity.RepoRoot,
		WorktreeOwnershipNonce: ownershipNonce,
		LaunchNonce:            launchNonce,
		TotalTimeoutMS:         req.TotalTimeout.Milliseconds(),
		LaunchStartedUnixMS:    now.UnixMilli(),
		LaunchExpiresUnixMS:    now.Add(req.TotalTimeout).UnixMilli(),
		BranchRequest: &state.HerdrBranchRequest{
			FullRef: fullBranchRef,
			NewOID:  base.SHA,
			OldOID:  "",
		},
		BranchPreState: &state.HerdrGitPreState{
			BranchAbsent:       true,
			PathAbsent:         pathAbsent,
			CheckoutRegistered: registered,
			ObservedHeadSHA:    headSHA,
		},
	}

	locked, err := state.LockHerdrControl(req.ProjectRoot)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	defer func() {
		// The state mutation result is authoritative; an unlock error cannot be
		// repaired here and the process still closes the descriptor on return.
		_ = locked.Unlock()
	}()
	if _, exists := locked.FindIntent(intentID); exists {
		return state.HerdrLaunchIntent{}, fmt.Errorf("herdr intent %s appeared while planning", intentID)
	}
	coordinatorID, err := state.HerdrCoordinatorIntentID(
		req.Parent,
		repoIdentity.RepoRoot,
		repoIdentity.GitDir,
		repoIdentity.GitDirDevice,
		repoIdentity.GitDirInode,
		req.PlanSpecIdentity,
	)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	coordinator, exists := locked.FindIntent(coordinatorID)
	if !exists {
		return state.HerdrLaunchIntent{}, fmt.Errorf("herdr coordinator intent %s does not exist", coordinatorID)
	}
	binding, err := coordinatorBindingForChild(
		req.Parent,
		repoIdentity.RepoRoot,
		repoIdentity.GitDir,
		repoIdentity.GitDirDevice,
		repoIdentity.GitDirInode,
		req.PlanSpecIdentity,
		repoIdentity.RepoKey,
		repoIdentity.RepoRoot,
		req.HerdrSession,
		req.HerdrSocketPath,
		coordinatorID,
		req.CoordinatorWorkspaceID,
		coordinator,
	)
	if err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	intent.Coordinator = &binding
	for _, existing := range locked.Intents {
		if existing.Operation != "child-worktree" ||
			(existing.OperationState != state.HerdrOperationActive &&
				existing.OperationState != state.HerdrOperationManualCleanupRequired) {
			continue
		}
		if existing.FullBranchRef == fullBranchRef ||
			filepath.Clean(existing.WorktreePath) == filepath.Clean(req.WorktreePath) {
			return state.HerdrLaunchIntent{}, fmt.Errorf(
				"herdr branch/path is already reserved by intent %s",
				existing.IntentID,
			)
		}
	}
	for _, lineage := range locked.Lineages {
		if lineage.FullBranchRef == fullBranchRef || filepath.Clean(lineage.WorktreePath) == filepath.Clean(req.WorktreePath) {
			return state.HerdrLaunchIntent{}, fmt.Errorf("herdr branch/path is already owned by lineage %s", lineage.LineageID)
		}
	}
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return state.HerdrLaunchIntent{}, err
	}
	return intent, nil
}

func advanceHerdrWorktree(
	ctx context.Context,
	req HerdrWorktreeRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
	intent state.HerdrLaunchIntent,
) (HerdrWorktreeResult, error) {
	for {
		if intent.OperationState == state.HerdrOperationManualCleanupRequired {
			return HerdrWorktreeResult{}, fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, intent.FailureReason)
		}
		if intent.OperationState != state.HerdrOperationActive {
			return HerdrWorktreeResult{}, fmt.Errorf("herdr intent %s is terminal: %s", intent.IntentID, intent.OperationState)
		}
		if err := verifyHerdrOperationDeadline(ctx, intent, hooks.Now()); err != nil {
			return HerdrWorktreeResult{}, failHerdrIntent(req.ProjectRoot, intent, err.Error())
		}
		switch intent.Phase {
		case state.HerdrPhaseBranchPlanned:
			var err error
			intent, err = reserveHerdrBranch(ctx, req, hooks, intent)
			if err != nil {
				return HerdrWorktreeResult{}, err
			}
		case state.HerdrPhaseBranchStarting:
			return HerdrWorktreeResult{}, failHerdrIntent(
				req.ProjectRoot,
				intent,
				"branch reservation request may have been issued; refusing blind retry",
			)
		case state.HerdrPhaseWorktreePlanned:
			var err error
			intent, err = realizeHerdrMutation(ctx, req, runtime, hooks, intent)
			if err != nil {
				return HerdrWorktreeResult{}, err
			}
		case state.HerdrPhaseWorktreeStarting:
			var err error
			intent, err = reconcileHerdrWorktreeMutation(ctx, req, runtime, hooks, intent)
			if err != nil {
				if errors.Is(err, ErrHerdrManualCleanupRequired) {
					return HerdrWorktreeResult{}, err
				}
				return HerdrWorktreeResult{}, failHerdrIntent(
					req.ProjectRoot,
					intent,
					"worktree create/open result is not provable without retry: "+err.Error(),
				)
			}
		case state.HerdrPhaseWorktreeRealized:
			if err := verifyHerdrWorktreeRealized(ctx, req, runtime, hooks, intent); err != nil {
				return HerdrWorktreeResult{}, err
			}
			if err := verifyHerdrOperationDeadline(ctx, intent, hooks.Now()); err != nil {
				return HerdrWorktreeResult{}, failHerdrIntent(req.ProjectRoot, intent, err.Error())
			}
			return herdrWorktreeDeferredResult(intent), ErrHerdrLauncherReadinessDeferred
		case state.HerdrPhaseWorktreeReady:
			return HerdrWorktreeResult{Intent: intent}, ErrHerdrLauncherReadinessDeferred
		default:
			return HerdrWorktreeResult{}, failHerdrIntent(req.ProjectRoot, intent, "unsupported child worktree phase "+string(intent.Phase))
		}
	}
}

func reserveHerdrBranch(
	ctx context.Context,
	req HerdrWorktreeRequest,
	hooks HerdrWorktreeHooks,
	intent state.HerdrLaunchIntent,
) (state.HerdrLaunchIntent, error) {
	if err := worktree.VerifyReservedBranchBase(req.SourceRoot, intent.ResolvedBaseRef, intent.LineageBaseSHA); err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	if oid, found, err := worktree.ObserveBranch(req.SourceRoot, intent.FullBranchRef); err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	} else if found {
		return intent, failHerdrIntent(
			req.ProjectRoot,
			intent,
			fmt.Sprintf("herdr branch %s appeared at %s before reservation", intent.FullBranchRef, oid),
		)
	}
	if err := verifyHerdrOperationDeadline(ctx, intent, hooks.Now()); err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	starting, err := transitionHerdrIntent(req.ProjectRoot, intent, state.HerdrPhaseBranchPlanned, func(next *state.HerdrLaunchIntent) {
		next.Phase = state.HerdrPhaseBranchStarting
	})
	if err != nil {
		return intent, err
	}
	if notifyErr := notifyHerdrPhase(hooks, starting.Phase); notifyErr != nil {
		return starting, notifyErr
	}
	if deadlineErr := verifyHerdrOperationDeadline(ctx, starting, hooks.Now()); deadlineErr != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, deadlineErr.Error())
	}
	if reserveErr := worktree.ReserveBranch(req.SourceRoot, starting.FullBranchRef, starting.LineageBaseSHA); reserveErr != nil {
		oid, found, observeErr := worktree.ObserveBranch(req.SourceRoot, starting.FullBranchRef)
		reason := fmt.Sprintf("branch reservation failed: %v", reserveErr)
		if observeErr != nil || found {
			if observeErr != nil {
				reason += fmt.Sprintf("; observe reservation: %v", observeErr)
			} else {
				reason += fmt.Sprintf("; observed ref %s", oid)
			}
			return starting, failHerdrIntent(req.ProjectRoot, starting, reason)
		}
		if removeErr := removeUnissuedHerdrIntent(req.ProjectRoot, starting); removeErr != nil {
			return starting, errors.Join(reserveErr, removeErr)
		}
		return starting, reserveErr
	}

	if deadlineErr := verifyHerdrOperationDeadline(ctx, starting, hooks.Now()); deadlineErr != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, deadlineErr.Error())
	}
	planned, err := completeHerdrBranchReservation(req.ProjectRoot, starting)
	if err != nil {
		return starting, err
	}
	if err := notifyHerdrPhase(hooks, planned.Phase); err != nil {
		return planned, err
	}
	return planned, nil
}

func realizeHerdrMutation(
	ctx context.Context,
	req HerdrWorktreeRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
	intent state.HerdrLaunchIntent,
) (state.HerdrLaunchIntent, error) {
	if intent.BranchReceipt == nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, "worktree-planned intent has no branch reservation receipt")
	}
	if err := worktree.VerifyHerdrWorktreeParent(req.ProjectRoot, intent.WorktreePath); err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	if verifyErr := worktree.VerifyReservedBranch(req.SourceRoot, intent.ResolvedBaseRef, intent.LineageBaseSHA, intent.FullBranchRef); verifyErr != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, verifyErr.Error())
	}
	pathAbsent, registered, headSHA, err := worktree.CheckoutGitState(req.SourceRoot, intent.WorktreePath, intent.FullBranchRef)
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	if !pathAbsent || registered {
		return intent, failHerdrIntent(req.ProjectRoot, intent, "fresh herdr worktree path already exists or is registered")
	}
	observed, err := observeHerdrWorkspacesWithRetry(ctx, runtime, hooks, intent)
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, "observe worktree pre-state: "+err.Error())
	}
	if verifyErr := verifyHerdrCoordinatorBinding(req.ProjectRoot, intent, observed); verifyErr != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, verifyErr.Error())
	}
	for _, workspace := range observed {
		if workspace.Label == intent.WorktreeOwnershipNonce || filepath.Clean(workspace.Path) == filepath.Clean(intent.WorktreePath) {
			return intent, failHerdrIntent(req.ProjectRoot, intent, "worktree pre-state contains a conflicting workspace")
		}
	}
	policyErr := retryHerdrRead(ctx, intent, hooks, runtime.VerifyWorktreeSetupPolicy)
	if policyErr != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, "verify herdr setup-hook policy: "+policyErr.Error())
	}
	preState := mutationPreState(observed, state.HerdrGitPreState{
		PathAbsent:         pathAbsent,
		CheckoutRegistered: registered,
		ObservedHeadSHA:    headSHA,
	})
	request := expectedHerdrWorktreeMutationRequest(intent)
	mutationCtx, mutationCancel, err := remainingHerdrMutationContext(ctx)
	if err != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	defer mutationCancel()
	if deadlineErr := verifyHerdrOperationDeadline(ctx, intent, hooks.Now()); deadlineErr != nil {
		return intent, failHerdrIntent(req.ProjectRoot, intent, deadlineErr.Error())
	}
	starting, err := transitionHerdrIntent(req.ProjectRoot, intent, state.HerdrPhaseWorktreePlanned, func(next *state.HerdrLaunchIntent) {
		next.MutationPreState = &preState
		next.MutationRequest = &request
		next.Phase = state.HerdrPhaseWorktreeStarting
	})
	if err != nil {
		return intent, err
	}
	if notifyErr := notifyHerdrPhase(hooks, starting.Phase); notifyErr != nil {
		return starting, notifyErr
	}
	if deadlineErr := verifyHerdrOperationDeadline(ctx, starting, hooks.Now()); deadlineErr != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, deadlineErr.Error())
	}

	result, err := runtime.MutateWorktree(mutationCtx, toHerdrMutationRequest(request))
	if err != nil {
		reconciled, reconcileErr := reconcileHerdrWorktreeMutation(ctx, req, runtime, hooks, starting)
		if reconcileErr != nil {
			if errors.Is(reconcileErr, ErrHerdrManualCleanupRequired) {
				return starting, reconcileErr
			}
			reason := fmt.Sprintf(
				"worktree mutation failed or response was incomplete: %v; exact post-state reconciliation failed: %v",
				err,
				reconcileErr,
			)
			return starting, failHerdrIntent(req.ProjectRoot, starting, reason)
		}
		return reconciled, nil
	}
	if validateErr := validateHerdrWorktreeMutationResult(starting, result); validateErr != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, validateErr.Error())
	}
	return completeHerdrWorktreeMutation(ctx, req, hooks, starting, result)
}

func reconcileHerdrWorktreeMutation(
	ctx context.Context,
	req HerdrWorktreeRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
	starting state.HerdrLaunchIntent,
) (state.HerdrLaunchIntent, error) {
	observed, err := observeHerdrWorkspacesWithRetry(ctx, runtime, hooks, starting)
	if err != nil {
		return starting, fmt.Errorf("observe uncertain worktree mutation: %w", err)
	}
	err = verifyHerdrCoordinatorBinding(req.ProjectRoot, starting, observed)
	if err != nil {
		return starting, fmt.Errorf("verify coordinator during worktree reconciliation: %w", err)
	}
	result, err := proveHerdrWorktreeMutationResult(starting, observed)
	if err != nil {
		return starting, err
	}
	return completeHerdrWorktreeMutation(ctx, req, hooks, starting, result)
}

func proveHerdrWorktreeMutationResult(
	intent state.HerdrLaunchIntent,
	observed []herdrrun.WorkspaceObservation,
) (herdrrun.WorktreeMutationResult, error) {
	if intent.MutationRequest == nil || intent.MutationPreState == nil {
		return herdrrun.WorktreeMutationResult{}, fmt.Errorf("saved worktree mutation request/pre-state is missing")
	}
	request := intent.MutationRequest
	var candidates []herdrrun.WorkspaceObservation
	for _, workspace := range observed {
		if workspace.Label == request.Label || filepath.Clean(workspace.Path) == filepath.Clean(request.Path) {
			candidates = append(candidates, workspace)
		}
	}
	if len(candidates) != 1 {
		return herdrrun.WorktreeMutationResult{}, fmt.Errorf(
			"uncertain worktree mutation has %d nonce/path candidates",
			len(candidates),
		)
	}
	candidate := candidates[0]
	alreadyOpen := request.Kind == state.HerdrMutationWorktreeOpen &&
		candidate.WorkspaceID == intent.MutationPreState.ExpectedAlreadyOpenID &&
		candidate.Label == intent.MutationPreState.ExpectedAlreadyOpenLabel
	result := herdrrun.WorktreeMutationResult{
		WorkspaceObservation: candidate,
		AlreadyOpen:          alreadyOpen,
	}
	if err := validateHerdrWorktreeMutationResult(intent, result); err != nil {
		return herdrrun.WorktreeMutationResult{}, err
	}
	return result, nil
}

func validateHerdrWorktreeMutationResult(
	intent state.HerdrLaunchIntent,
	result herdrrun.WorktreeMutationResult,
) error {
	if intent.MutationRequest == nil {
		return fmt.Errorf("saved worktree mutation request is missing")
	}
	request := intent.MutationRequest
	if result.WorkspaceID == "" ||
		result.Label != request.Label ||
		filepath.Clean(result.Path) != filepath.Clean(request.Path) ||
		result.RepoKey != request.ExpectedRepoKey ||
		result.RepoRoot != request.ExpectedRepoRoot ||
		filepath.Clean(result.CWD) != filepath.Clean(request.Path) ||
		result.Pane.Backend != backend.Herdr ||
		result.Pane.Workspace != result.WorkspaceID ||
		result.Pane.Pane == "" ||
		result.TerminalID == "" {
		return fmt.Errorf("worktree mutation result does not match saved exact request")
	}
	return validateAlreadyOpen(intent, result)
}

func completeHerdrWorktreeMutation(
	ctx context.Context,
	req HerdrWorktreeRequest,
	hooks HerdrWorktreeHooks,
	starting state.HerdrLaunchIntent,
	result herdrrun.WorktreeMutationResult,
) (state.HerdrLaunchIntent, error) {
	if deadlineErr := verifyHerdrOperationDeadline(ctx, starting, hooks.Now()); deadlineErr != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, deadlineErr.Error())
	}
	markerPath, err := worktree.EnsureHerdrOwnershipMarker(
		req.ProjectRoot,
		starting.WorktreePath,
		starting.FullBranchRef,
		starting.LaunchHeadSHA,
		starting.WorktreeOwnershipNonce,
	)
	if err != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, err.Error())
	}
	receipt := mutationReceipt(result, markerPath)
	if verifyErr := verifyHerdrWorktreePostState(req.ProjectRoot, starting, receipt); verifyErr != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, verifyErr.Error())
	}
	if deadlineErr := verifyHerdrOperationDeadline(ctx, starting, hooks.Now()); deadlineErr != nil {
		return starting, failHerdrIntent(req.ProjectRoot, starting, deadlineErr.Error())
	}
	realized, err := transitionHerdrIntent(req.ProjectRoot, starting, state.HerdrPhaseWorktreeStarting, func(next *state.HerdrLaunchIntent) {
		next.MutationReceipt = &receipt
		next.Phase = state.HerdrPhaseWorktreeRealized
	})
	if err != nil {
		return starting, err
	}
	if err := notifyHerdrPhase(hooks, realized.Phase); err != nil {
		return realized, err
	}
	return realized, nil
}

func verifyHerdrWorktreeRealized(
	ctx context.Context,
	req HerdrWorktreeRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
	intent state.HerdrLaunchIntent,
) error {
	if intent.MutationReceipt == nil {
		return failHerdrIntent(req.ProjectRoot, intent, "worktree-realized intent has no mutation receipt")
	}
	if verifyErr := verifyHerdrWorktreePostState(req.ProjectRoot, intent, *intent.MutationReceipt); verifyErr != nil {
		return failHerdrIntent(req.ProjectRoot, intent, verifyErr.Error())
	}
	observed, err := observeHerdrWorkspacesWithRetry(ctx, runtime, hooks, intent)
	if err != nil {
		return failHerdrIntent(req.ProjectRoot, intent, "observe realized worktree: "+err.Error())
	}
	matches := 0
	for _, workspace := range observed {
		if workspace.WorkspaceID != intent.MutationReceipt.WorkspaceID {
			continue
		}
		matches++
		if workspace.Label != intent.WorktreeOwnershipNonce ||
			filepath.Clean(workspace.Path) != filepath.Clean(intent.WorktreePath) ||
			workspace.RepoKey != intent.HerdrRepoKey ||
			workspace.RepoRoot != intent.HerdrRepoRoot ||
			workspace.Pane.Backend != backend.Herdr ||
			workspace.Pane.Workspace != workspace.WorkspaceID ||
			workspace.Pane.Pane != intent.MutationReceipt.PaneID ||
			workspace.TerminalID != intent.MutationReceipt.TerminalID ||
			filepath.Clean(workspace.CWD) != filepath.Clean(intent.WorktreePath) {
			return failHerdrIntent(req.ProjectRoot, intent, "realized herdr workspace identity changed before launcher handoff")
		}
	}
	if matches != 1 {
		return failHerdrIntent(req.ProjectRoot, intent, fmt.Sprintf("realized herdr workspace has %d matching observations", matches))
	}
	if err := verifyHerdrOperationDeadline(ctx, intent, hooks.Now()); err != nil {
		return failHerdrIntent(req.ProjectRoot, intent, err.Error())
	}
	return nil
}

func herdrWorktreeDeferredResult(intent state.HerdrLaunchIntent) HerdrWorktreeResult {
	return HerdrWorktreeResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend:   backend.Herdr,
			Workspace: intent.MutationReceipt.WorkspaceID,
			Pane:      intent.MutationReceipt.PaneID,
		},
	}
}

func verifyHerdrWorktreePostState(projectRoot string, intent state.HerdrLaunchIntent, receipt state.HerdrMutationReceipt) error {
	if receipt.WorkspaceID == "" || receipt.WorkspaceLabel != intent.WorktreeOwnershipNonce ||
		receipt.PaneID == "" || receipt.TerminalID == "" ||
		filepath.Clean(receipt.Path) != filepath.Clean(intent.WorktreePath) ||
		filepath.Clean(receipt.CWD) != filepath.Clean(intent.WorktreePath) ||
		receipt.RepoKey != intent.HerdrRepoKey ||
		receipt.RepoRoot != intent.HerdrRepoRoot {
		return fmt.Errorf("herdr worktree mutation receipt does not match intent")
	}
	if err := worktree.VerifyHerdrOwnershipMarker(
		projectRoot,
		intent.WorktreePath,
		intent.FullBranchRef,
		intent.LaunchHeadSHA,
		receipt.GitDirMarkerPath,
		intent.WorktreeOwnershipNonce,
	); err != nil {
		return err
	}
	pathAbsent, registered, headSHA, err := worktree.CheckoutGitState(projectRoot, intent.WorktreePath, intent.FullBranchRef)
	if err != nil {
		return err
	}
	if pathAbsent || !registered || headSHA != intent.LaunchHeadSHA {
		return fmt.Errorf(
			"herdr checkout post-state = absent:%t registered:%t head:%s, want present/registered/%s",
			pathAbsent,
			registered,
			headSHA,
			intent.LaunchHeadSHA,
		)
	}
	branchOID, found, err := worktree.ObserveBranch(projectRoot, intent.FullBranchRef)
	if err != nil {
		return err
	}
	if !found || branchOID != headSHA {
		return fmt.Errorf("herdr checkout HEAD and reserved branch disagree")
	}
	return nil
}

func validateAlreadyOpen(intent state.HerdrLaunchIntent, result herdrrun.WorktreeMutationResult) error {
	if intent.MutationRequest == nil || intent.MutationPreState == nil {
		return fmt.Errorf("herdr worktree mutation is missing saved request/pre-state")
	}
	wasPresent := false
	wasBound := false
	for _, workspace := range intent.MutationPreState.Workspaces {
		if workspace.WorkspaceID != result.WorkspaceID {
			continue
		}
		wasPresent = true
		if workspace.WorkspaceID == intent.MutationPreState.ExpectedAlreadyOpenID &&
			workspace.Label == intent.MutationPreState.ExpectedAlreadyOpenLabel &&
			workspace.Label == intent.WorktreeOwnershipNonce &&
			filepath.Clean(workspace.Path) == filepath.Clean(intent.WorktreePath) &&
			workspace.RepoKey == intent.HerdrRepoKey &&
			workspace.RepoRoot == intent.HerdrRepoRoot {
			wasBound = true
		}
	}
	switch intent.MutationRequest.Kind {
	case state.HerdrMutationWorktreeCreate:
		if result.AlreadyOpen {
			return fmt.Errorf("worktree create returned already_open")
		}
		if wasPresent {
			return fmt.Errorf("worktree create response reused a pre-state workspace id")
		}
	case state.HerdrMutationWorktreeOpen:
		if result.AlreadyOpen && !wasBound {
			return fmt.Errorf("worktree open already_open identity was not bound in pre-state")
		}
		if !result.AlreadyOpen && wasPresent {
			return fmt.Errorf("worktree open response reused a pre-state workspace id without already_open")
		}
	default:
		return fmt.Errorf("unsupported saved herdr worktree mutation %q", intent.MutationRequest.Kind)
	}
	return nil
}

func transitionHerdrIntent(
	projectRoot string,
	previous state.HerdrLaunchIntent,
	expectedPhase state.HerdrLaunchPhase,
	mutate func(*state.HerdrLaunchIntent),
) (state.HerdrLaunchIntent, error) {
	locked, err := state.LockHerdrControl(projectRoot)
	if err != nil {
		return previous, err
	}
	defer func() {
		// Preserve the transition error; Unlock still closes the descriptor.
		_ = locked.Unlock()
	}()
	current, found := locked.FindIntent(previous.IntentID)
	if !found {
		return previous, fmt.Errorf("herdr intent %s disappeared", previous.IntentID)
	}
	if current.OperationState != state.HerdrOperationActive || current.Phase != expectedPhase || !reflect.DeepEqual(current, previous) {
		return previous, fmt.Errorf("herdr intent %s changed concurrently at phase %s", previous.IntentID, expectedPhase)
	}
	mutate(&current)
	locked.UpsertIntent(current)
	if err := locked.Save(); err != nil {
		return previous, err
	}
	return current, nil
}

func removeUnissuedHerdrIntent(projectRoot string, starting state.HerdrLaunchIntent) error {
	locked, err := state.LockHerdrControl(projectRoot)
	if err != nil {
		return err
	}
	defer func() {
		// Preserve the removal error; Unlock still closes the descriptor.
		_ = locked.Unlock()
	}()
	current, found := locked.FindIntent(starting.IntentID)
	if !found || current.OperationState != state.HerdrOperationActive ||
		current.Phase != state.HerdrPhaseBranchStarting ||
		!reflect.DeepEqual(current, starting) {
		return fmt.Errorf("herdr intent %s changed before non-issued reservation cleanup", starting.IntentID)
	}
	if !locked.RemoveIntent(starting.IntentID) {
		return fmt.Errorf("herdr intent %s disappeared during non-issued reservation cleanup", starting.IntentID)
	}
	if err := locked.Save(); err != nil {
		return err
	}
	return nil
}

func failHerdrIntent(projectRoot string, previous state.HerdrLaunchIntent, reason string) error {
	if previous.OperationState == state.HerdrOperationManualCleanupRequired {
		return fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, previous.FailureReason)
	}
	_, err := transitionHerdrIntent(projectRoot, previous, previous.Phase, func(next *state.HerdrLaunchIntent) {
		next.OperationState = state.HerdrOperationManualCleanupRequired
		next.FailureReason = reason
	})
	if err != nil {
		return errors.Join(fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, reason), err)
	}
	return fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, reason)
}

func completeHerdrBranchReservation(
	projectRoot string,
	starting state.HerdrLaunchIntent,
) (state.HerdrLaunchIntent, error) {
	locked, err := state.LockHerdrControl(projectRoot)
	if err != nil {
		return starting, err
	}
	defer func() {
		// Preserve the receipt-save error; Unlock still closes the descriptor.
		_ = locked.Unlock()
	}()
	current, found := locked.FindIntent(starting.IntentID)
	if !found || current.OperationState != state.HerdrOperationActive ||
		current.Phase != state.HerdrPhaseBranchStarting ||
		!reflect.DeepEqual(current, starting) {
		return starting, fmt.Errorf("herdr intent %s changed before branch receipt save", starting.IntentID)
	}
	current.BranchReceipt = &state.HerdrBranchReceipt{
		FullRef: current.FullBranchRef,
		NewOID:  current.LineageBaseSHA,
		OldOID:  "",
	}
	current.Phase = state.HerdrPhaseWorktreePlanned
	locked.UpsertIntent(current)
	locked.UpsertLineage(state.HerdrBranchLineage{
		LineageID:           current.LineageID,
		IntentID:            current.IntentID,
		Parent:              current.Parent,
		IssueNum:            current.IssueNum,
		TaskID:              current.TaskID,
		FullBranchRef:       current.FullBranchRef,
		WorktreePath:        current.WorktreePath,
		ResolvedBaseRef:     current.ResolvedBaseRef,
		ResolvedBaseName:    current.ResolvedBaseName,
		EffectiveBaseBranch: current.EffectiveBaseBranch,
		PRBaseName:          current.PRBaseName,
		LineageBaseSHA:      current.LineageBaseSHA,
		LastOwnedHeadSHA:    current.LaunchHeadSHA,
		State:               "active",
	})
	if err := locked.Save(); err != nil {
		return starting, err
	}
	return current, nil
}

func validateHerdrWorktreeRequest(req HerdrWorktreeRequest) error {
	if req.TotalTimeout < minHerdrWorktreeTimeout || req.TotalTimeout > maxHerdrWorktreeTimeout ||
		req.TotalTimeout%time.Millisecond != 0 {
		return fmt.Errorf("herdr worktree total timeout must be 3s..300s at millisecond precision")
	}
	if req.ProjectRoot == "" || req.SourceRoot == "" || req.Slug == "" || req.BranchName == "" ||
		req.BaseBranch == "" || req.WorktreePath == "" || req.CoordinatorWorkspaceID == "" ||
		req.HerdrSession == "" || req.HerdrSocketPath == "" {
		return fmt.Errorf("herdr worktree request is incomplete")
	}
	expectedPath := filepath.Join(req.ProjectRoot, ".fanout", "worktrees", req.Slug)
	if filepath.Clean(req.WorktreePath) != filepath.Clean(expectedPath) {
		return fmt.Errorf("herdr worktree path %s is not deterministic path %s", req.WorktreePath, expectedPath)
	}
	return nil
}

func validateSavedHerdrWorktreeIntent(req HerdrWorktreeRequest, intent state.HerdrLaunchIntent) error {
	projectIdentity, err := worktree.ResolveHerdrRepoIdentity(req.ProjectRoot)
	if err != nil {
		return err
	}
	repoIdentity, err := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if err != nil {
		return err
	}
	if projectIdentity.RepoKey != repoIdentity.RepoKey {
		return fmt.Errorf("herdr project root and source root belong to different repositories")
	}
	if err := worktree.VerifyHerdrWorktreeParent(req.ProjectRoot, req.WorktreePath); err != nil {
		return err
	}
	fullBranchRef, err := worktree.HerdrBranchRef(req.SourceRoot, req.BranchName)
	if err != nil {
		return err
	}
	intentID, err := state.HerdrIntentID(
		req.Parent,
		req.IssueNum,
		req.TaskID,
		repoIdentity.RepoRoot,
		repoIdentity.GitDir,
		repoIdentity.GitDirDevice,
		repoIdentity.GitDirInode,
		req.PlanSpecIdentity,
	)
	if err != nil {
		return err
	}
	coordinatorID, err := state.HerdrCoordinatorIntentID(
		req.Parent,
		repoIdentity.RepoRoot,
		repoIdentity.GitDir,
		repoIdentity.GitDirDevice,
		repoIdentity.GitDirInode,
		req.PlanSpecIdentity,
	)
	if err != nil {
		return err
	}
	if intent.Backend != backend.Herdr ||
		intent.IntentID != intentID ||
		intent.Operation != "child-worktree" ||
		intent.Parent != req.Parent ||
		intent.IssueNum != req.IssueNum ||
		intent.TaskID != req.TaskID ||
		intent.SourceRootPhysical != repoIdentity.RepoRoot ||
		intent.SourceGitDirPhysical != repoIdentity.GitDir ||
		intent.SourceGitDirDevice != repoIdentity.GitDirDevice ||
		intent.SourceGitDirInode != repoIdentity.GitDirInode ||
		intent.PlanSpecIdentity != req.PlanSpecIdentity ||
		intent.Slug != req.Slug ||
		intent.BranchName != req.BranchName ||
		intent.FullBranchRef != fullBranchRef ||
		filepath.Clean(intent.WorktreePath) != filepath.Clean(req.WorktreePath) ||
		intent.EffectiveBaseBranch != strings.TrimSpace(req.BaseBranch) ||
		intent.HerdrSession != req.HerdrSession ||
		intent.HerdrSocketPath != req.HerdrSocketPath ||
		intent.HerdrRepoKey != repoIdentity.RepoKey ||
		intent.HerdrRepoRoot != repoIdentity.RepoRoot ||
		intent.Coordinator == nil ||
		intent.Coordinator.IntentID != coordinatorID ||
		intent.Coordinator.WorkspaceID != req.CoordinatorWorkspaceID ||
		intent.TotalTimeoutMS != req.TotalTimeout.Milliseconds() {
		return fmt.Errorf("saved herdr intent %s contradicts the exact launch request", intent.IntentID)
	}
	control, err := state.LoadHerdrControl(req.ProjectRoot)
	if err != nil {
		return err
	}
	coordinator, found := control.FindIntent(intent.Coordinator.IntentID)
	if !found {
		return fmt.Errorf("bound herdr coordinator intent %s disappeared", intent.Coordinator.IntentID)
	}
	binding, err := coordinatorBindingForChild(
		req.Parent,
		repoIdentity.RepoRoot,
		repoIdentity.GitDir,
		repoIdentity.GitDirDevice,
		repoIdentity.GitDirInode,
		req.PlanSpecIdentity,
		repoIdentity.RepoKey,
		repoIdentity.RepoRoot,
		req.HerdrSession,
		req.HerdrSocketPath,
		intent.Coordinator.IntentID,
		req.CoordinatorWorkspaceID,
		coordinator,
	)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(binding, *intent.Coordinator) {
		return fmt.Errorf("saved herdr coordinator binding changed before replay")
	}
	if err := validateSavedHerdrWorktreeMutationEvidence(intent); err != nil {
		return err
	}
	return nil
}

func validateSavedHerdrWorktreeMutationEvidence(intent state.HerdrLaunchIntent) error {
	switch intent.Phase {
	case state.HerdrPhaseWorktreeStarting, state.HerdrPhaseWorktreeRealized:
		expected := expectedHerdrWorktreeMutationRequest(intent)
		if intent.MutationRequest == nil ||
			intent.MutationPreState == nil ||
			!reflect.DeepEqual(*intent.MutationRequest, expected) {
			return fmt.Errorf("saved herdr worktree mutation evidence contradicts the exact intent")
		}
		if intent.Phase == state.HerdrPhaseWorktreeStarting && intent.MutationReceipt != nil {
			return fmt.Errorf("worktree-starting intent already has a mutation receipt")
		}
		if intent.Phase == state.HerdrPhaseWorktreeRealized && intent.MutationReceipt == nil {
			return fmt.Errorf("worktree-realized intent has no mutation receipt")
		}
	case state.HerdrPhaseBranchPlanned,
		state.HerdrPhaseBranchStarting,
		state.HerdrPhaseWorktreePlanned:
		return nil
	case state.HerdrPhaseWorktreeReady:
		return ErrHerdrLauncherReadinessDeferred
	case state.HerdrPhaseWorkspacePlanned,
		state.HerdrPhaseWorkspaceStarting,
		state.HerdrPhaseWorkspaceRealized,
		state.HerdrPhaseWorkspaceReady:
		return fmt.Errorf("child worktree intent has coordinator phase %q", intent.Phase)
	}
	return nil
}

func coordinatorBindingForChild(
	parent string,
	sourceRoot, sourceGitDir string,
	sourceGitDirDevice, sourceGitDirInode uint64,
	planSpecIdentity, repoKey, repoRoot string,
	herdrSession, herdrSocketPath, intentID, workspaceID string,
	coordinator state.HerdrLaunchIntent,
) (state.HerdrCoordinatorBinding, error) {
	if coordinator.Backend != backend.Herdr ||
		coordinator.Operation != "coordinator-workspace" ||
		coordinator.OperationState != state.HerdrOperationActive ||
		coordinator.Phase != state.HerdrPhaseWorkspaceRealized ||
		coordinator.IntentID != intentID ||
		coordinator.Parent != parent ||
		coordinator.IssueNum != 0 ||
		coordinator.TaskID != "" ||
		coordinator.SourceRootPhysical != sourceRoot ||
		coordinator.SourceGitDirPhysical != sourceGitDir ||
		coordinator.SourceGitDirDevice != sourceGitDirDevice ||
		coordinator.SourceGitDirInode != sourceGitDirInode ||
		coordinator.PlanSpecIdentity != planSpecIdentity ||
		coordinator.HerdrRepoKey != repoKey ||
		coordinator.HerdrRepoRoot != repoRoot ||
		coordinator.HerdrSession != herdrSession ||
		coordinator.HerdrSocketPath != herdrSocketPath ||
		coordinator.MutationReceipt == nil {
		return state.HerdrCoordinatorBinding{}, fmt.Errorf("herdr coordinator intent %s is not a realized exact owner for the child", intentID)
	}
	if err := validateSavedHerdrCoordinatorMutationEvidence(coordinator); err != nil {
		return state.HerdrCoordinatorBinding{}, fmt.Errorf("herdr coordinator intent %s has invalid mutation evidence: %w", intentID, err)
	}
	receipt := coordinator.MutationReceipt
	if receipt.WorkspaceID != workspaceID ||
		receipt.WorkspaceLabel != coordinator.WorktreeOwnershipNonce ||
		receipt.PaneID == "" ||
		receipt.TerminalID == "" ||
		filepath.Clean(receipt.CWD) != filepath.Clean(coordinator.WorktreePath) ||
		receipt.Path != "" ||
		receipt.RepoKey != "" ||
		receipt.RepoRoot != "" {
		return state.HerdrCoordinatorBinding{}, fmt.Errorf("herdr coordinator receipt %s does not match the requested owner workspace", intentID)
	}
	return state.HerdrCoordinatorBinding{
		IntentID:       coordinator.IntentID,
		WorkspaceID:    receipt.WorkspaceID,
		WorkspaceLabel: receipt.WorkspaceLabel,
		PaneID:         receipt.PaneID,
		TerminalID:     receipt.TerminalID,
		CWD:            filepath.Clean(receipt.CWD),
	}, nil
}

func verifyHerdrCoordinatorBinding(
	projectRoot string,
	child state.HerdrLaunchIntent,
	observed []herdrrun.WorkspaceObservation,
) error {
	if child.Coordinator == nil {
		return fmt.Errorf("herdr child intent has no coordinator binding")
	}
	control, err := state.LoadHerdrControl(projectRoot)
	if err != nil {
		return fmt.Errorf("reload herdr coordinator binding: %w", err)
	}
	coordinator, found := control.FindIntent(child.Coordinator.IntentID)
	if !found {
		return fmt.Errorf("bound herdr coordinator intent %s disappeared", child.Coordinator.IntentID)
	}
	binding, err := coordinatorBindingForChild(
		child.Parent,
		child.SourceRootPhysical,
		child.SourceGitDirPhysical,
		child.SourceGitDirDevice,
		child.SourceGitDirInode,
		child.PlanSpecIdentity,
		child.HerdrRepoKey,
		child.HerdrRepoRoot,
		child.HerdrSession,
		child.HerdrSocketPath,
		child.Coordinator.IntentID,
		child.Coordinator.WorkspaceID,
		coordinator,
	)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(binding, *child.Coordinator) {
		return fmt.Errorf("bound herdr coordinator identity changed before child mutation")
	}
	matches := 0
	for _, workspace := range observed {
		if workspace.WorkspaceID != binding.WorkspaceID {
			continue
		}
		matches++
		if workspace.Label != binding.WorkspaceLabel ||
			workspace.Path != "" ||
			workspace.RepoKey != "" ||
			workspace.RepoRoot != "" ||
			workspace.Pane.Backend != backend.Herdr ||
			workspace.Pane.Workspace != workspace.WorkspaceID ||
			workspace.Pane.Pane != binding.PaneID ||
			workspace.TerminalID != binding.TerminalID ||
			filepath.Clean(workspace.CWD) != filepath.Clean(binding.CWD) {
			return fmt.Errorf("live herdr coordinator identity changed before child mutation")
		}
	}
	if matches != 1 {
		return fmt.Errorf("bound herdr coordinator has %d matching live observations", matches)
	}
	return nil
}

func mutationPreState(observed []herdrrun.WorkspaceObservation, git state.HerdrGitPreState) state.HerdrMutationPreState {
	workspaces := make([]state.HerdrWorkspaceBinding, 0, len(observed))
	for _, workspace := range observed {
		workspaces = append(workspaces, state.HerdrWorkspaceBinding{
			WorkspaceID: workspace.WorkspaceID,
			Label:       workspace.Label,
			Path:        workspace.Path,
			RepoKey:     workspace.RepoKey,
			RepoRoot:    workspace.RepoRoot,
		})
	}
	slices.SortFunc(workspaces, func(a, b state.HerdrWorkspaceBinding) int {
		return strings.Compare(a.WorkspaceID, b.WorkspaceID)
	})
	return state.HerdrMutationPreState{Workspaces: workspaces, Git: git}
}

func mutationReceipt(result herdrrun.WorktreeMutationResult, markerPath string) state.HerdrMutationReceipt {
	return state.HerdrMutationReceipt{
		WorkspaceID:      result.WorkspaceID,
		WorkspaceLabel:   result.Label,
		PaneID:           result.Pane.Pane,
		TerminalID:       result.TerminalID,
		CWD:              result.CWD,
		Path:             result.Path,
		RepoKey:          result.RepoKey,
		RepoRoot:         result.RepoRoot,
		AlreadyOpen:      result.AlreadyOpen,
		GitDirMarkerPath: markerPath,
	}
}

func expectedHerdrWorktreeMutationRequest(intent state.HerdrLaunchIntent) state.HerdrMutationRequest {
	return state.HerdrMutationRequest{
		Kind:                      state.HerdrMutationWorktreeCreate,
		WorkspaceID:               intent.Coordinator.WorkspaceID,
		CoordinatorWorkspaceLabel: intent.Coordinator.WorkspaceLabel,
		CoordinatorPaneID:         intent.Coordinator.PaneID,
		CoordinatorTerminalID:     intent.Coordinator.TerminalID,
		CoordinatorWorkspaceCWD:   intent.Coordinator.CWD,
		ExpectedRepoKey:           intent.HerdrRepoKey,
		ExpectedRepoRoot:          intent.HerdrRepoRoot,
		Branch:                    intent.BranchName,
		Base:                      intent.LineageBaseSHA,
		Path:                      intent.WorktreePath,
		Label:                     intent.WorktreeOwnershipNonce,
		NoFocus:                   true,
	}
}

func toHerdrMutationRequest(req state.HerdrMutationRequest) herdrrun.WorktreeMutationRequest {
	kind := herdrrun.WorktreeMutationKind(req.Kind)
	return herdrrun.WorktreeMutationRequest{
		Kind:                      kind,
		WorkspaceID:               req.WorkspaceID,
		CoordinatorWorkspaceLabel: req.CoordinatorWorkspaceLabel,
		CoordinatorPaneID:         req.CoordinatorPaneID,
		CoordinatorTerminalID:     req.CoordinatorTerminalID,
		CoordinatorWorkspaceCWD:   req.CoordinatorWorkspaceCWD,
		ExpectedRepoKey:           req.ExpectedRepoKey,
		ExpectedRepoRoot:          req.ExpectedRepoRoot,
		CWD:                       req.CWD,
		Branch:                    req.Branch,
		Base:                      req.Base,
		Path:                      req.Path,
		Label:                     req.Label,
		NoFocus:                   req.NoFocus,
	}
}

func normalizeHerdrWorktreeHooks(hooks HerdrWorktreeHooks) HerdrWorktreeHooks {
	if hooks.Now == nil {
		hooks.Now = time.Now
	}
	if hooks.Random == nil {
		hooks.Random = randomHerdrToken
	}
	if hooks.Sleep == nil {
		hooks.Sleep = sleepHerdrReadInterval
	}
	return hooks
}

func notifyHerdrPhase(hooks HerdrWorktreeHooks, phase state.HerdrLaunchPhase) error {
	if hooks.PhaseSaved == nil {
		return nil
	}
	return hooks.PhaseSaved(phase)
}

func randomHerdrToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func herdrLineageID(intentID, nonce string) string {
	sum := sha256.Sum256([]byte("fanout.herdr-lineage.v1\x00" + intentID + "\x00" + nonce))
	return hex.EncodeToString(sum[:])
}

func physicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func herdrOperationContext(
	parent context.Context,
	intent state.HerdrLaunchIntent,
	now time.Time,
) (context.Context, context.CancelFunc, error) {
	if err := parent.Err(); err != nil {
		return nil, nil, herdrOperationContextError(err)
	}
	totalTimeout := time.Duration(intent.TotalTimeoutMS) * time.Millisecond
	remaining := time.UnixMilli(intent.LaunchExpiresUnixMS).Sub(now.UTC())
	remaining = min(totalTimeout, remaining)
	if remaining <= 0 {
		return nil, nil, fmt.Errorf("herdr launch deadline expired")
	}
	operationCtx, cancel := context.WithTimeout(parent, remaining)
	if err := operationCtx.Err(); err != nil {
		cancel()
		return nil, nil, herdrOperationContextError(err)
	}
	return operationCtx, cancel, nil
}

func verifyHerdrOperationDeadline(
	ctx context.Context,
	intent state.HerdrLaunchIntent,
	now time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return herdrOperationContextError(err)
	}
	if now.UTC().UnixMilli() >= intent.LaunchExpiresUnixMS {
		return fmt.Errorf("herdr launch deadline expired")
	}
	return nil
}

func herdrOperationContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("herdr launch deadline expired: %w", err)
	}
	return fmt.Errorf("herdr launch canceled: %w", err)
}

func boundedHerdrReadContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if err := parent.Err(); err != nil {
		return nil, nil, herdrOperationContextError(err)
	}
	if _, ok := parent.Deadline(); !ok {
		return nil, nil, fmt.Errorf("herdr launch operation has no deadline")
	}
	callCtx, cancel := context.WithTimeout(parent, 5*time.Second)
	return callCtx, cancel, nil
}

func remainingHerdrMutationContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if err := parent.Err(); err != nil {
		return nil, nil, herdrOperationContextError(err)
	}
	if _, ok := parent.Deadline(); !ok {
		return nil, nil, fmt.Errorf("herdr launch operation has no deadline")
	}
	callCtx, cancel := context.WithCancel(parent)
	return callCtx, cancel, nil
}

func observeHerdrWorkspacesWithRetry(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	hooks HerdrWorktreeHooks,
	intent state.HerdrLaunchIntent,
) ([]herdrrun.WorkspaceObservation, error) {
	var observed []herdrrun.WorkspaceObservation
	err := retryHerdrRead(ctx, intent, hooks, func(callCtx context.Context) error {
		var err error
		observed, err = runtime.ObserveWorkspaces(callCtx)
		return err
	})
	return observed, err
}

func retryHerdrRead(
	ctx context.Context,
	intent state.HerdrLaunchIntent,
	hooks HerdrWorktreeHooks,
	call func(context.Context) error,
) error {
	totalTimeout := time.Duration(intent.TotalTimeoutMS) * time.Millisecond
	callLimit := max(1, int((totalTimeout+herdrReadStartInterval-1)/herdrReadStartInterval))
	var lastErr error
	for attempt := range callLimit {
		started := hooks.Now().UTC()
		callCtx, cancel, err := boundedHerdrReadContext(ctx)
		if err != nil {
			if lastErr != nil {
				return errors.Join(lastErr, err)
			}
			return err
		}
		lastErr = call(callCtx)
		cancel()
		if lastErr == nil || !errors.Is(lastErr, herdrrun.ErrRetryableRead) {
			return lastErr
		}
		if attempt+1 == callLimit {
			return lastErr
		}
		delay := started.Add(herdrReadStartInterval).Sub(hooks.Now().UTC())
		if delay <= 0 {
			continue
		}
		waitCtx, waitCancel, err := remainingHerdrMutationContext(ctx)
		if err != nil {
			return errors.Join(lastErr, err)
		}
		sleepErr := hooks.Sleep(waitCtx, delay)
		waitCancel()
		if sleepErr != nil {
			return errors.Join(lastErr, sleepErr)
		}
	}
	return lastErr
}

func sleepHerdrReadInterval(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
