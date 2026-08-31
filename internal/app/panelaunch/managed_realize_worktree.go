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
		ctx, req.Parent, req.ProjectRoot, req.SourceRoot, req.TotalTimeout, launchLock, hooks,
	)
	if setupErr != nil {
		return result, setupErr
	}
	defer realizeCancel()
	intentID, intentIDErr := state.WorktreeIntentID(
		req.Parent,
		setup.ownerProjectRoot,
		req.IssueNum,
		req.TaskID,
	)
	if intentIDErr != nil {
		return result, intentIDErr
	}
	realization := &managedWorktreeRealization{
		setup: setup, runtime: runtime, req: req, intentID: intentID, source: setup.source,
	}
	return realization.realize()
}

// managedWorktreeRealization carries the child-checkout realization across its
// phases: the shared prologue, the request as narrowed by the coordinator and
// the saved intent, and the coordinator the checkout attaches to.
type managedWorktreeRealization struct {
	setup        managedRealizeSetup
	runtime      ManagedWorktreeRuntime
	req          ManagedWorktreeRequest
	intentID     string
	source       worktree.RepoIdentity
	coordinator  state.RuntimeResource
	intentHealed bool
}

// realize loads the journal's view of this child, verifies the owned route, and
// hands the intent to status classification.
func (r *managedWorktreeRealization) realize() (result ManagedRealizeResult, retErr error) {
	setup := r.setup
	locked := setup.locked
	intent, found, loadErr := loadManagedWorktreeIntentForRealization(
		setup.ctx, r.runtime, locked, r.req, r.source, setup.ownerProjectRoot, setup.runtimeParent, r.intentID,
	)
	if loadErr != nil {
		return result, loadErr
	}
	coordinatorID, coordinatorIDErr := state.CoordinatorIntentID(
		setup.runtimeParent,
		setup.runtimeOwnerProjectRoot,
		managedCoordinatorSyntheticIssueNum(setup.runtimeParent, r.req.IssueNum),
	)
	if coordinatorIDErr != nil {
		return result, coordinatorIDErr
	}
	operationNow := setup.hooks.Now()
	routeCtx, operationParent, routeCancel, routeContextErr := managedRealizeRouteContext(
		setup.ctx, intent, found, operationNow,
	)
	if routeContextErr != nil {
		return result, r.routeContextError(intent, found, routeContextErr)
	}
	defer routeCancel()
	routeErr := verifyManagedRealizeRoute(
		routeCtx, r.runtime, r.source.RepoKey, r.req.ManagedSession, r.req.SocketPath,
	)
	if routeErr != nil {
		return result, routeErr
	}
	return r.classifyIntent(operationParent, coordinatorID, intent, found, operationNow)
}

// routeContextError classifies a route-context failure before any mutation: a
// planned intent releases its unissued reservation, anything else stays
// retryable.
func (r *managedWorktreeRealization) routeContextError(
	intent state.LaunchIntent,
	found bool,
	cause error,
) error {
	if !found {
		return cause
	}
	if intent.Status == state.IntentPlanned {
		return rollbackUnissuedManagedWorktree(r.setup.locked, r.req, intent, cause)
	}
	// No mutation and no existence check happened yet; keep the intent so
	// the next run classifies it (canon: recovery on re-execution).
	return cause
}

// classifyIntent attaches the child to its coordinator, settles the intent it
// will act on, and dispatches on that intent's status.
func (r *managedWorktreeRealization) classifyIntent(
	operationParent context.Context,
	coordinatorID string,
	saved state.LaunchIntent,
	found bool,
	operationNow time.Time,
) (result ManagedRealizeResult, retErr error) {
	if err := r.resolveCoordinator(coordinatorID); err != nil {
		return result, err
	}
	intent, intentErr := r.resolveIntent(saved, found)
	if intentErr != nil {
		return result, intentErr
	}
	classificationOnly := !operationNow.Before(time.UnixMilli(intent.ExpiresUnixMS))
	operationCtx, cancel, contextErr := managedIntentContext(operationParent, intent, operationNow)
	if contextErr != nil {
		return result, r.intentContextError(intent, contextErr)
	}
	defer cancel()
	locked := r.setup.locked
	switch intent.Status {
	case state.IntentRealized:
		return resumeRealizedManagedWorktree(
			operationCtx, r.runtime, locked, r.req, r.source, intent, !classificationOnly, r.intentHealed,
		)
	case state.IntentIssued:
		return recoverManagedWorktree(operationCtx, r.runtime, locked, r.req, r.source, intent, nil)
	case state.IntentPlanned:
	default:
		return result, markManagedIntentManual(locked, intent, fmt.Errorf("unknown Herdr worktree intent status %q", intent.Status))
	}
	return r.createPlannedWorktree(operationCtx, intent)
}

// resolveCoordinator attaches the realization to its realized coordinator and
// narrows the request to the coordinator's source repository.
func (r *managedWorktreeRealization) resolveCoordinator(coordinatorID string) error {
	coordinator, coordinatorErr := resolvedManagedCoordinator(
		r.setup.locked,
		coordinatorID,
		r.req,
		r.source.RepoRoot,
		r.setup.runtimeParent,
		r.setup.runtimeOwnerProjectRoot,
	)
	if coordinatorErr != nil {
		return coordinatorErr
	}
	source, sourceErr := managedCoordinatorSource(r.setup.ctx, coordinator, r.source)
	if sourceErr != nil {
		return sourceErr
	}
	r.coordinator = coordinator
	r.source = source
	r.req.SourceRoot = source.RepoRoot
	return nil
}

// resolveIntent re-verifies the intent the journal holds, or records a fresh
// planned one, and returns the intent the status dispatch classifies.
func (r *managedWorktreeRealization) resolveIntent(
	saved state.LaunchIntent,
	found bool,
) (state.LaunchIntent, error) {
	if !found {
		return r.planIntent()
	}
	if err := r.adoptSavedIntent(&saved); err != nil {
		return saved, err
	}
	return saved, nil
}

// adoptSavedIntent re-verifies a journal-held intent against the request and
// narrows the request to the owner project root its saved path implies.
func (r *managedWorktreeRealization) adoptSavedIntent(intent *state.LaunchIntent) error {
	if intent.Coordinator != r.coordinator {
		coordinator, found := restartedManagedCoordinatorResource(r.coordinator, intent.Coordinator)
		if !found {
			return fmt.Errorf("saved Herdr worktree intent contradicts request")
		}
		intent.Coordinator = coordinator
		r.intentHealed = true
	}
	if savedErr := validateSavedWorktreeIntent(
		r.req,
		r.source,
		r.coordinator,
		r.setup.ownerProjectRoot,
		r.setup.runtimeParent,
		*intent,
	); savedErr != nil {
		return savedErr
	}
	savedProjectRoot, _, savedRootErr := savedManagedWorktreeSource(r.setup.ctx, *intent, r.source)
	if savedRootErr != nil {
		return savedRootErr
	}
	r.req.ProjectRoot = savedProjectRoot
	return nil
}

// planIntent records the planned intent that fences the branch reservation.
func (r *managedWorktreeRealization) planIntent() (state.LaunchIntent, error) {
	intent, intentErr := plannedManagedWorktreeIntent(r.setup, r.req, r.intentID, r.coordinator)
	if intentErr != nil {
		return intent, intentErr
	}
	r.setup.locked.UpsertIntent(intent)
	if saveErr := r.setup.locked.Save(); saveErr != nil {
		return intent, saveErr
	}
	return intent, nil
}

// intentContextError releases a planned reservation whose deadline expired
// before any mutation, and otherwise keeps the intent retryable.
func (r *managedWorktreeRealization) intentContextError(
	intent state.LaunchIntent,
	cause error,
) error {
	if errors.Is(cause, errManagedIntentDeadlineExpired) &&
		intent.Status == state.IntentPlanned {
		return rollbackUnissuedManagedWorktree(r.setup.locked, r.req, intent, cause)
	}
	return cause
}

// createPlannedWorktree runs the planned lane: reserve the branch, fence the
// create with its preflight, and finalize the realized checkout.
func (r *managedWorktreeRealization) createPlannedWorktree(
	ctx context.Context,
	intent state.LaunchIntent,
) (result ManagedRealizeResult, retErr error) {
	locked := r.setup.locked
	if policyErr := r.runtime.VerifyWorktreeSetupPolicy(ctx); policyErr != nil {
		return result, policyErr
	}
	intent, reservationErr := ensureManagedBranchReservation(ctx, locked, r.req, intent)
	if reservationErr != nil {
		return result, reservationErr
	}
	if preflightErr := r.preflightPlannedWorktree(ctx, intent); preflightErr != nil {
		return result, preflightErr
	}
	intent.Status = state.IntentIssued
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		return result, saveErr
	}
	mutation, mutationErr := r.runtime.CreateWorktree(ctx, managedWorktreeCreateRequest(intent, r.source))
	if mutationErr != nil {
		return r.classifyWorktreeCreateError(ctx, intent, mutationErr)
	}
	finalizeErr := finalizeManagedWorktree(ctx, locked, r.req, r.source, &intent, mutation.WorkspaceObservation)
	if finalizeErr != nil {
		return result, handleManagedWorktreeFinalizeError(locked, intent, finalizeErr)
	}
	return realizeDeferred(intent)
}

// preflightPlannedWorktree runs the checks fencing the planned create, already
// classified: an unissued rollback for a lost coordinator or a broken Git
// precondition, manual cleanup for a label collision.
func (r *managedWorktreeRealization) preflightPlannedWorktree(
	ctx context.Context,
	intent state.LaunchIntent,
) error {
	locked := r.setup.locked
	workspaces, observeErr := r.runtime.ObserveWorkspaces(ctx)
	if observeErr != nil {
		return observeErr
	}
	if coordinatorErr := verifyCoordinatorObservation(intent.Coordinator, workspaces); coordinatorErr != nil {
		// The create was never issued (planned): release the child
		// reservation instead of demanding manual cleanup.
		return rollbackUnissuedManagedWorktree(locked, r.req, intent, coordinatorErr)
	}
	if matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel); len(matches) != 0 {
		return markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("planned Herdr worktree label already has %d workspaces", len(matches)),
		)
	}
	if preconditionErr := verifyManagedWorktreePreconditions(ctx, r.req, r.source, intent); preconditionErr != nil {
		// The mutation was never issued (planned), so release the reserved
		// branch and the intent instead of demanding manual cleanup; the
		// rollback itself fails closed when the branch ownership no longer
		// verifies.
		return rollbackUnissuedManagedWorktree(locked, r.req, intent, preconditionErr)
	}
	return nil
}

// classifyWorktreeCreateError routes a failed create: release on proven
// non-issuance, keep an unclassified expiry retryable, and otherwise recover
// from the observed label.
func (r *managedWorktreeRealization) classifyWorktreeCreateError(
	ctx context.Context,
	intent state.LaunchIntent,
	mutationErr error,
) (ManagedRealizeResult, error) {
	locked := r.setup.locked
	if errors.Is(mutationErr, backend.ErrMutationNotIssued) {
		return ManagedRealizeResult{}, rollbackUnissuedManagedWorktree(locked, r.req, intent, mutationErr)
	}
	if expiryErr := unclassifiedManagedMutationExpiry(ctx, mutationErr); expiryErr != nil {
		return ManagedRealizeResult{}, expiryErr
	}
	return recoverManagedWorktree(ctx, r.runtime, locked, r.req, r.source, intent, mutationErr)
}

// managedWorktreeCreateRequest is the create the planned intent describes. Only
// a branch this launch reserved carries a base, so an adopted branch keeps its
// own tip.
func managedWorktreeCreateRequest(
	intent state.LaunchIntent,
	source worktree.RepoIdentity,
) backend.WorktreeCreateRequest {
	baseArg := ""
	if intent.BranchCreated {
		baseArg = intent.BaseSHA
	}
	return backend.WorktreeCreateRequest{
		Coordinator:    observationResource(intent.Coordinator),
		SourceRepoKey:  source.RepoKey,
		SourceRepoRoot: source.RepoRoot,
		Branch:         intent.BranchName,
		Base:           baseArg,
		Path:           intent.WorktreePath,
		Label:          intent.WorkspaceLabel,
	}
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

// managedWorktreePlan is the Git state a planned intent freezes: the resolved
// launch base, the child branch ref and the head it must still carry, and
// whether that branch predates this launch.
type managedWorktreePlan struct {
	baseBranch    string
	baseSHA       string
	fullRef       string
	head          string
	branchExisted bool
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
	plan, planErr := planManagedWorktreeGitState(setup.ctx, req)
	if planErr != nil {
		return state.LaunchIntent{}, planErr
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
		Slug:             req.Slug, BranchName: req.BranchName, FullBranchRef: plan.fullRef,
		BaseBranch: plan.baseBranch, BaseSHA: plan.baseSHA, ExpectedHead: plan.head,
		WorktreePath: filepath.Clean(req.WorktreePath), BranchExisted: plan.branchExisted,
		WorkspaceLabel: label, Coordinator: coordinator,
		Session: req.ManagedSession, SocketPath: req.SocketPath,
		ExpiresUnixMS: setup.deadline.UnixMilli(),
	}, nil
}

// planManagedWorktreeGitState freezes the Git facts the planned intent records,
// and fails closed when the branch or the checkout path is not launchable.
func planManagedWorktreeGitState(
	ctx context.Context,
	req ManagedWorktreeRequest,
) (managedWorktreePlan, error) {
	var plan managedWorktreePlan
	if err := prepareManagedWorktreeParentDir(req); err != nil {
		return plan, err
	}
	base, err := worktree.ResolveLaunchBase(ctx, worktree.Options{
		ProjectRoot: req.SourceRoot, Slug: req.Slug, BranchName: req.BranchName,
		BaseBranch: req.BaseBranch, NoRefresh: req.NoRefresh,
		AllowMissingOrigin: req.AllowMissingOrigin,
	})
	if err != nil {
		return plan, err
	}
	fullRef, err := worktree.LocalBranchRef(ctx, req.SourceRoot, req.BranchName)
	if err != nil {
		return plan, err
	}
	head, branchExisted, headErr := resolveManagedWorktreeHead(ctx, req.SourceRoot, fullRef, base.SHA)
	if headErr != nil {
		return plan, headErr
	}
	if err := verifyAbsentManagedCheckout(ctx, req); err != nil {
		return plan, err
	}
	return managedWorktreePlan{
		baseBranch: base.BaseBranch, baseSHA: base.SHA,
		fullRef: fullRef, head: head, branchExisted: branchExisted,
	}, nil
}

// prepareManagedWorktreeParentDir makes the source repository ignore the
// worktree root and creates the parent directory the checkout needs.
func prepareManagedWorktreeParentDir(req ManagedWorktreeRequest) error {
	if err := worktree.EnsureLocalExclude(req.SourceRoot); err != nil {
		return err
	}
	return worktree.EnsureWorktreeParentDir(req.ProjectRoot, req.WorktreePath)
}

// resolveManagedWorktreeHead returns the head the planned intent must still see
// on the child branch: the tip of an adopted branch, or the frozen base when
// the launch creates the branch itself.
func resolveManagedWorktreeHead(
	ctx context.Context,
	sourceRoot, fullRef, baseSHA string,
) (string, bool, error) {
	head, branchExisted, err := worktree.ObserveBranch(ctx, sourceRoot, fullRef)
	if err != nil {
		return "", false, err
	}
	if !branchExisted {
		return baseSHA, false, nil
	}
	if availableErr := worktree.BranchAvailable(ctx, sourceRoot, fullRef); availableErr != nil {
		return "", false, availableErr
	}
	return head, true, nil
}

// verifyAbsentManagedCheckout fails closed when the planned worktree path is
// already present or registered.
func verifyAbsentManagedCheckout(ctx context.Context, req ManagedWorktreeRequest) error {
	checkout, err := worktree.ObserveCheckout(ctx, req.SourceRoot, req.WorktreePath)
	if err != nil {
		return err
	}
	if !checkout.PathAbsent || checkout.Registered {
		return fmt.Errorf("herdr worktree path already exists or is registered")
	}
	return nil
}
