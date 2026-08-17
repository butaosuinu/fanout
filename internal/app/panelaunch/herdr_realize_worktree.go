package panelaunch

// RealizeHerdrWorktree: the child-checkout realization flow (branch
// reservation, worktree create, response-loss recovery dispatch) and its
// coordinator-resolution helpers. The shared prologue, the coordinator flow,
// and the recovery classification live in the sibling herdr_realize.go and
// herdr_recover.go.

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

// RealizeHerdrWorktree reserves the child branch, creates or recovers the
// Herdr checkout workspace under the caller-held combined launch lock and
// stops before launcher readiness.
func RealizeHerdrWorktree(
	ctx context.Context,
	req HerdrWorktreeRequest,
	runtime HerdrWorktreeRuntime,
	launchLock *state.LockedStore,
	hooks HerdrRealizeHooks,
) (result HerdrRealizeResult, retErr error) {
	if ctx == nil || runtime == nil || launchLock == nil {
		return result, fmt.Errorf("realize Herdr worktree requires context, runtime, and launch lock")
	}
	if validateErr := validateHerdrWorktreeRequest(req); validateErr != nil {
		return result, validateErr
	}
	setup, realizeCancel, setupErr := newHerdrRealizeSetup(
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
	intent, found, loadErr := loadHerdrWorktreeIntentForRealization(
		setup.ctx, runtime, locked, req, source, ownerProjectRoot, runtimeParent, intentID,
	)
	if loadErr != nil {
		return result, loadErr
	}
	var intentErr error
	coordinatorID, coordinatorIDErr := state.CoordinatorIntentID(
		runtimeParent,
		runtimeOwnerProjectRoot,
		herdrCoordinatorSyntheticIssueNum(runtimeParent, req.IssueNum),
	)
	if coordinatorIDErr != nil {
		return result, coordinatorIDErr
	}

	operationNow := setup.hooks.Now()
	routeCtx, operationParent, routeCancel, routeContextErr := herdrRealizeRouteContext(
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
			return result, rollbackUnissuedHerdrWorktree(
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
	if routeErr := verifyHerdrRealizeRoute(
		routeCtx,
		runtime,
		source.RepoKey,
		req.HerdrSession,
		req.SocketPath,
	); routeErr != nil {
		return result, routeErr
	}
	coordinator, coordinatorErr := resolvedHerdrCoordinator(
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
	coordinatorSource, sourceErr := herdrCoordinatorSource(setup.ctx, coordinator, source)
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
		savedProjectRoot, _, savedRootErr := savedHerdrWorktreeSource(setup.ctx, intent, source)
		if savedRootErr != nil {
			return result, savedRootErr
		}
		req.ProjectRoot = savedProjectRoot
	} else {
		intent, intentErr = plannedHerdrWorktreeIntent(setup, req, intentID, coordinator)
		if intentErr != nil {
			return result, intentErr
		}
		locked.UpsertIntent(intent)
		if saveErr := locked.Save(); saveErr != nil {
			return result, saveErr
		}
	}

	classificationOnly := !operationNow.Before(time.UnixMilli(intent.ExpiresUnixMS))
	operationCtx, cancel, contextErr := herdrIntentContext(operationParent, intent, operationNow)
	if contextErr != nil {
		if errors.Is(contextErr, errHerdrIntentDeadlineExpired) &&
			intent.Status == state.IntentPlanned {
			return result, rollbackUnissuedHerdrWorktree(locked, req, intent, contextErr)
		}
		return result, contextErr
	}
	defer cancel()

	switch intent.Status {
	case state.IntentRealized:
		return resumeRealizedHerdrWorktree(
			operationCtx,
			runtime,
			locked,
			req,
			source,
			intent,
			!classificationOnly,
		)
	case state.IntentIssued:
		return recoverHerdrWorktree(operationCtx, runtime, locked, req, source, intent, nil)
	case state.IntentPlanned:
	default:
		return result, markHerdrIntentManual(
			locked,
			intent,
			fmt.Errorf("unknown Herdr worktree intent status %q", intent.Status),
		)
	}

	if policyErr := runtime.VerifyWorktreeSetupPolicy(operationCtx); policyErr != nil {
		return result, policyErr
	}
	intent, reservationErr := ensureHerdrBranchReservation(operationCtx, locked, req, intent)
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
		return result, rollbackUnissuedHerdrWorktree(locked, req, intent, coordinatorErr)
	}
	if matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel); len(matches) != 0 {
		return result, markHerdrIntentManual(
			locked,
			intent,
			fmt.Errorf("planned Herdr worktree label already has %d workspaces", len(matches)),
		)
	}
	if preconditionErr := verifyHerdrWorktreePreconditions(operationCtx, req, source, intent); preconditionErr != nil {
		// The mutation was never issued (planned), so release the reserved
		// branch and the intent instead of demanding manual cleanup; the
		// rollback itself fails closed when the branch ownership no longer
		// verifies.
		return result, rollbackUnissuedHerdrWorktree(locked, req, intent, preconditionErr)
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
			return result, rollbackUnissuedHerdrWorktree(locked, req, intent, mutationErr)
		}
		if operationErr := operationCtx.Err(); operationErr != nil &&
			!errors.Is(mutationErr, backend.ErrMutationRejected) {
			return result, errors.Join(mutationErr, operationErr)
		}
		return recoverHerdrWorktree(operationCtx, runtime, locked, req, source, intent, mutationErr)
	}
	if finalizeErr := finalizeHerdrWorktree(operationCtx, locked, req, source, &intent, mutation.WorkspaceObservation); finalizeErr != nil {
		return result, handleHerdrWorktreeFinalizeError(locked, intent, finalizeErr)
	}
	return realizeDeferred(intent)
}

func resolvedHerdrCoordinator(
	locked *state.LockedLaunchJournal,
	coordinatorID string,
	req HerdrWorktreeRequest,
	repoRoot string,
	runtimeParent string,
	runtimeOwnerProjectRoot string,
) (state.RuntimeResource, error) {
	intent, found := locked.FindIntent(coordinatorID)
	if !found || intent.Status != state.IntentRealized {
		return state.RuntimeResource{}, fmt.Errorf("herdr coordinator %s is not realized", coordinatorID)
	}
	if intent.RuntimeParent != runtimeParent ||
		intent.Session != req.HerdrSession || intent.SocketPath != req.SocketPath ||
		!savedHerdrCoordinatorPathMatches(
			runtimeOwnerProjectRoot,
			intent.WorktreePath,
			repoRoot,
		) {
		return state.RuntimeResource{}, fmt.Errorf("herdr coordinator intent contradicts child request")
	}
	return intent.Resource, nil
}

func herdrCoordinatorSource(
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
			errHerdrRealizedIdentityChanged,
		)
	}
	return source, nil
}

func savedHerdrWorktreeSource(
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

func herdrCoordinatorSyntheticIssueNum(parent string, issueNum int) int {
	switch canonicalHerdrParent(parent) {
	case ManualParentRef, WatchParentRef:
		return issueNum
	default:
		return 0
	}
}

// plannedHerdrWorktreeIntent runs the fresh-launch Git preconditions (frozen
// base, branch state, absent checkout) and builds the planned intent recorded
// before the branch reservation.
func plannedHerdrWorktreeIntent(
	setup herdrRealizeSetup,
	req HerdrWorktreeRequest,
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
	label, err := newHerdrWorkspaceLabel("worktree", setup.hooks.RandomToken)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	return state.LaunchIntent{
		ID: intentID, Kind: state.IntentWorktree, Status: state.IntentPlanned,
		Parent: canonicalHerdrParent(req.Parent), RuntimeParent: setup.runtimeParent,
		OwnerProjectRoot: setup.ownerProjectRoot,
		IssueNum:         req.IssueNum,
		TaskID:           req.TaskID,
		Slug:             req.Slug, BranchName: req.BranchName, FullBranchRef: fullRef,
		BaseBranch: base.BaseBranch, BaseSHA: base.SHA, ExpectedHead: head,
		WorktreePath: filepath.Clean(req.WorktreePath), BranchExisted: branchExisted,
		WorkspaceLabel: label, Coordinator: coordinator,
		Session: req.HerdrSession, SocketPath: req.SocketPath,
		ExpiresUnixMS: setup.deadline.UnixMilli(),
	}, nil
}
