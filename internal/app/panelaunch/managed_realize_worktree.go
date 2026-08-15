package panelaunch

// RealizeManagedWorktree: the child-checkout realization flow (branch
// reservation, worktree create, response-loss recovery dispatch) and its
// coordinator-resolution helpers. The shared prologue, the coordinator flow,
// and the recovery classification live in the sibling managed_realize.go and
// managed_recover.go.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// RealizeManagedWorktree reserves the child branch, creates or recovers the
// Herdr checkout workspace under the caller-held combined launch lock and
// stops before launcher readiness.
func RealizeManagedWorktree(
	ctx context.Context,
	req ManagedWorktreeRequest,
	runtime ManagedWorktreeRuntime,
	launchLock *state.LockedStore,
	hooks ManagedRealizeHooks,
) (result ManagedRealizeResult, retErr error) {
	if ctx == nil || runtime == nil || launchLock == nil {
		return result, fmt.Errorf("realize Herdr worktree requires context, runtime, and launch lock")
	}
	if validateErr := validateManagedWorktreeRequest(req); validateErr != nil {
		return result, validateErr
	}
	setup, realizeCancel, setupErr := newManagedRealizeSetup(
		ctx,
		req.Parent,
		req.ProjectRoot,
		req.SourceRoot,
		req.TotalTimeout,
		launchLock,
		hooks,
	)
	if setupErr != nil {
		return result, setupErr
	}
	defer realizeCancel()
	locked := setup.locked
	source := setup.source
	ownerProjectRoot := setup.ownerProjectRoot
	runtimeParent := setup.runtimeParent
	runtimeOwnerProjectRoot := setup.runtimeOwnerProjectRoot
	intentID, intentIDErr := state.WorktreeIntentID(
		req.Parent,
		ownerProjectRoot,
		req.IssueNum,
		req.TaskID,
	)
	if intentIDErr != nil {
		return result, intentIDErr
	}
	intent, found, loadErr := loadManagedWorktreeIntentForRealization(
		setup.ctx, runtime, locked, req, source, ownerProjectRoot, runtimeParent, intentID,
	)
	if loadErr != nil {
		return result, loadErr
	}
	var intentErr error
	coordinatorID, coordinatorIDErr := state.CoordinatorIntentID(
		runtimeParent,
		runtimeOwnerProjectRoot,
		managedCoordinatorSyntheticIssueNum(runtimeParent, req.IssueNum),
	)
	if coordinatorIDErr != nil {
		return result, coordinatorIDErr
	}

	operationNow := setup.hooks.Now()
	routeCtx, operationParent, routeCancel, routeContextErr := managedRealizeRouteContext(
		setup.ctx,
		intent,
		found,
		operationNow,
	)
	if routeContextErr != nil {
		if !found {
			return result, routeContextErr
		}
		if intent.Status == state.IntentPlanned {
			return result, rollbackUnissuedManagedWorktree(
				locked,
				req,
				intent,
				routeContextErr,
			)
		}
		// No mutation and no existence check happened yet; keep the intent so
		// the next run classifies it (canon: recovery on re-execution).
		return result, routeContextErr
	}
	defer routeCancel()
	if routeErr := verifyManagedRealizeRoute(
		routeCtx,
		runtime,
		source.RepoKey,
		req.ManagedSession,
		req.SocketPath,
	); routeErr != nil {
		return result, routeErr
	}
	coordinator, coordinatorErr := resolvedManagedCoordinator(
		locked,
		coordinatorID,
		req,
		source.RepoRoot,
		runtimeParent,
		runtimeOwnerProjectRoot,
	)
	if coordinatorErr != nil {
		return result, coordinatorErr
	}
	coordinatorSource, sourceErr := managedCoordinatorSource(setup.ctx, coordinator, source)
	if sourceErr != nil {
		return result, sourceErr
	}
	source = coordinatorSource
	req.SourceRoot = source.RepoRoot
	if found {
		if savedErr := validateSavedWorktreeIntent(
			req,
			source,
			coordinator,
			ownerProjectRoot,
			runtimeParent,
			intent,
		); savedErr != nil {
			return result, savedErr
		}
		savedProjectRoot, _, savedRootErr := savedManagedWorktreeSource(setup.ctx, intent, source)
		if savedRootErr != nil {
			return result, savedRootErr
		}
		req.ProjectRoot = savedProjectRoot
	} else {
		intent, intentErr = plannedManagedWorktreeIntent(setup, req, intentID, coordinator)
		if intentErr != nil {
			return result, intentErr
		}
		locked.UpsertIntent(intent)
		if saveErr := locked.Save(); saveErr != nil {
			return result, saveErr
		}
	}

	classificationOnly := !operationNow.Before(time.UnixMilli(intent.ExpiresUnixMS))
	operationCtx, cancel, contextErr := managedIntentContext(operationParent, intent, operationNow)
	if contextErr != nil {
		if errors.Is(contextErr, errManagedIntentDeadlineExpired) &&
			intent.Status == state.IntentPlanned {
			return result, rollbackUnissuedManagedWorktree(locked, req, intent, contextErr)
		}
		return result, contextErr
	}
	defer cancel()

	switch intent.Status {
	case state.IntentRealized:
		return resumeRealizedManagedWorktree(
			operationCtx,
			runtime,
			locked,
			req,
			source,
			intent,
			!classificationOnly,
		)
	case state.IntentIssued:
		return recoverManagedWorktree(operationCtx, runtime, locked, req, source, intent, nil)
	case state.IntentPlanned:
	default:
		return result, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("unknown Herdr worktree intent status %q", intent.Status),
		)
	}

	if policyErr := runtime.VerifyWorktreeSetupPolicy(operationCtx); policyErr != nil {
		return result, policyErr
	}
	intent, reservationErr := ensureManagedBranchReservation(operationCtx, locked, req, intent)
	if reservationErr != nil {
		return result, reservationErr
	}
	workspaces, observeErr := runtime.ObserveWorkspaces(operationCtx)
	if observeErr != nil {
		return result, observeErr
	}
	if coordinatorErr := verifyCoordinatorObservation(intent.Coordinator, workspaces); coordinatorErr != nil {
		// The create was never issued (planned): release the child
		// reservation instead of demanding manual cleanup.
		return result, rollbackUnissuedManagedWorktree(locked, req, intent, coordinatorErr)
	}
	if matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel); len(matches) != 0 {
		return result, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("planned Herdr worktree label already has %d workspaces", len(matches)),
		)
	}
	if preconditionErr := verifyManagedWorktreePreconditions(operationCtx, req, source, intent); preconditionErr != nil {
		// The mutation was never issued (planned), so release the reserved
		// branch and the intent instead of demanding manual cleanup; the
		// rollback itself fails closed when the branch ownership no longer
		// verifies.
		return result, rollbackUnissuedManagedWorktree(locked, req, intent, preconditionErr)
	}

	intent.Status = state.IntentIssued
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		return result, saveErr
	}
	baseArg := ""
	if intent.BranchCreated {
		baseArg = intent.BaseSHA
	}
	mutation, mutationErr := runtime.CreateWorktree(operationCtx, backend.WorktreeCreateRequest{
		Coordinator:    observationResource(intent.Coordinator),
		SourceRepoKey:  source.RepoKey,
		SourceRepoRoot: source.RepoRoot,
		Branch:         intent.BranchName,
		Base:           baseArg,
		Path:           intent.WorktreePath,
		Label:          intent.WorkspaceLabel,
	})
	if mutationErr != nil {
		if errors.Is(mutationErr, backend.ErrMutationNotIssued) {
			return result, rollbackUnissuedManagedWorktree(locked, req, intent, mutationErr)
		}
		if operationErr := operationCtx.Err(); operationErr != nil &&
			!errors.Is(mutationErr, backend.ErrMutationRejected) {
			return result, errors.Join(mutationErr, operationErr)
		}
		return recoverManagedWorktree(operationCtx, runtime, locked, req, source, intent, mutationErr)
	}
	if finalizeErr := finalizeManagedWorktree(operationCtx, locked, req, source, &intent, mutation.WorkspaceObservation); finalizeErr != nil {
		return result, handleManagedWorktreeFinalizeError(locked, intent, finalizeErr)
	}
	return realizeDeferred(intent)
}

func resolvedManagedCoordinator(
	locked *state.LockedLaunchJournal,
	coordinatorID string,
	req ManagedWorktreeRequest,
	repoRoot string,
	runtimeParent string,
	runtimeOwnerProjectRoot string,
) (state.RuntimeResource, error) {
	intent, found := locked.FindIntent(coordinatorID)
	if !found || intent.Status != state.IntentRealized {
		return state.RuntimeResource{}, fmt.Errorf("herdr coordinator %s is not realized", coordinatorID)
	}
	if intent.RuntimeParent != runtimeParent ||
		intent.Session != req.ManagedSession || intent.SocketPath != req.SocketPath ||
		!savedManagedCoordinatorPathMatches(
			runtimeOwnerProjectRoot,
			intent.WorktreePath,
			repoRoot,
		) {
		return state.RuntimeResource{}, fmt.Errorf("herdr coordinator intent contradicts child request")
	}
	return intent.Resource, nil
}

func managedCoordinatorSource(
	ctx context.Context,
	coordinator state.RuntimeResource,
	requestSource worktree.RepoIdentity,
) (worktree.RepoIdentity, error) {
	source, err := worktree.ResolveRepoIdentity(ctx, coordinator.CurrentPath)
	if err != nil {
		return worktree.RepoIdentity{}, fmt.Errorf("resolve Herdr coordinator source: %w", err)
	}
	if source.RepoKey != requestSource.RepoKey {
		return worktree.RepoIdentity{}, fmt.Errorf(
			"%w: herdr coordinator source belongs to a different repository",
			errManagedRealizedIdentityChanged,
		)
	}
	return source, nil
}

func savedManagedWorktreeSource(
	ctx context.Context,
	intent state.LaunchIntent,
	source worktree.RepoIdentity,
) (string, worktree.RepoIdentity, error) {
	savedPath := filepath.Clean(intent.WorktreePath)
	worktreesDir := filepath.Dir(savedPath)
	fanoutDir := filepath.Dir(worktreesDir)
	projectRoot := filepath.Dir(fanoutDir)
	if !filepath.IsAbs(savedPath) || filepath.Base(savedPath) != intent.Slug ||
		filepath.Base(worktreesDir) != "worktrees" || filepath.Base(fanoutDir) != ".fanout" {
		return "", worktree.RepoIdentity{}, fmt.Errorf("saved Herdr worktree path has no owner project root")
	}
	identity, err := worktree.ResolveRepoIdentity(ctx, projectRoot)
	if err != nil {
		return "", worktree.RepoIdentity{}, fmt.Errorf("resolve saved Herdr worktree owner: %w", err)
	}
	if identity.RepoKey != source.RepoKey ||
		(intent.OwnerProjectRoot != "" && identity.RepoRoot != intent.OwnerProjectRoot) {
		return "", worktree.RepoIdentity{}, fmt.Errorf("saved Herdr worktree owner belongs to a different repository")
	}
	return projectRoot, identity, nil
}

func managedCoordinatorSyntheticIssueNum(parent string, issueNum int) int {
	switch canonicalManagedParent(parent) {
	case ManualParentRef, WatchParentRef:
		return issueNum
	default:
		return 0
	}
}

// plannedManagedWorktreeIntent runs the fresh-launch Git preconditions (frozen
// base, branch state, absent checkout) and builds the planned intent recorded
// before the branch reservation.
func plannedManagedWorktreeIntent(
	setup managedRealizeSetup,
	req ManagedWorktreeRequest,
	intentID string,
	coordinator state.RuntimeResource,
) (state.LaunchIntent, error) {
	if err := worktree.EnsureLocalExclude(req.SourceRoot); err != nil {
		return state.LaunchIntent{}, err
	}
	if err := worktree.EnsureWorktreeParentDir(req.ProjectRoot, req.WorktreePath); err != nil {
		return state.LaunchIntent{}, err
	}
	base, err := worktree.ResolveLaunchBase(setup.ctx, worktree.Options{
		ProjectRoot: req.SourceRoot, Slug: req.Slug, BranchName: req.BranchName,
		BaseBranch: req.BaseBranch, NoRefresh: req.NoRefresh,
		AllowMissingOrigin: req.AllowMissingOrigin,
	})
	if err != nil {
		return state.LaunchIntent{}, err
	}
	fullRef, err := worktree.LocalBranchRef(setup.ctx, req.SourceRoot, req.BranchName)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	head, branchExisted, err := worktree.ObserveBranch(setup.ctx, req.SourceRoot, fullRef)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	if branchExisted {
		if availableErr := worktree.BranchAvailable(setup.ctx, req.SourceRoot, fullRef); availableErr != nil {
			return state.LaunchIntent{}, availableErr
		}
	} else {
		head = base.SHA
	}
	checkout, err := worktree.ObserveCheckout(setup.ctx, req.SourceRoot, req.WorktreePath)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	if !checkout.PathAbsent || checkout.Registered {
		return state.LaunchIntent{}, fmt.Errorf("herdr worktree path already exists or is registered")
	}
	label, err := newManagedWorkspaceLabel("worktree", setup.hooks.RandomToken)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	return state.LaunchIntent{
		ID: intentID, Kind: state.IntentWorktree, Status: state.IntentPlanned,
		Parent: canonicalManagedParent(req.Parent), RuntimeParent: setup.runtimeParent,
		OwnerProjectRoot: setup.ownerProjectRoot,
		IssueNum:         req.IssueNum,
		TaskID:           req.TaskID,
		Slug:             req.Slug, BranchName: req.BranchName, FullBranchRef: fullRef,
		BaseBranch: base.BaseBranch, BaseSHA: base.SHA, ExpectedHead: head,
		WorktreePath: filepath.Clean(req.WorktreePath), BranchExisted: branchExisted,
		WorkspaceLabel: label, Coordinator: coordinator,
		Session: req.ManagedSession, SocketPath: req.SocketPath,
		ExpiresUnixMS: setup.deadline.UnixMilli(),
	}, nil
}
