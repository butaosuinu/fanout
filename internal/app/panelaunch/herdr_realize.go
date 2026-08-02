package panelaunch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const (
	minHerdrRealizeTimeout                = 3 * time.Second
	maxHerdrRealizeTimeout                = 300 * time.Second
	maxHerdrRecoveryClassificationTimeout = 5 * time.Second
)

var (
	ErrHerdrManualCleanupRequired     = errors.New("herdr launch requires manual cleanup")
	ErrHerdrLauncherReadinessDeferred = errors.New("herdr launcher readiness is deferred to issue #528")
	errHerdrIntentDeadlineExpired     = errors.New("herdr realization deadline expired")
	errHerdrRealizedIntentSave        = errors.New("save realized Herdr worktree intent")
)

type HerdrWorktreeRuntime interface {
	WorktreeRoute(context.Context) (herdrrun.OwnedWorktreeRoute, error)
	VerifyWorktreeSetupPolicy(context.Context) error
	ObserveWorkspaces(context.Context) ([]herdrrun.WorkspaceObservation, error)
	CreateWorkspace(
		context.Context,
		herdrrun.WorkspaceCreateRequest,
	) (herdrrun.WorktreeMutationResult, error)
	CreateWorktree(
		context.Context,
		herdrrun.WorktreeCreateRequest,
	) (herdrrun.WorktreeMutationResult, error)
	OpenWorktree(
		context.Context,
		herdrrun.WorktreeOpenRequest,
	) (herdrrun.WorktreeMutationResult, error)
}

type HerdrRealizeHooks struct {
	Now         func() time.Time
	RandomToken func() (string, error)
}

type HerdrCoordinatorRequest struct {
	Parent       string
	IssueNum     int
	ProjectRoot  string
	SourceRoot   string
	CWD          string
	HerdrSession string
	SocketPath   string
	TotalTimeout time.Duration
}

type HerdrCoordinatorResult struct {
	Intent state.HerdrIntent
	Pane   backend.PaneRef
}

type HerdrWorktreeRequest struct {
	Parent      string
	IssueNum    int
	TaskID      string
	ProjectRoot string
	SourceRoot  string
	Slug        string
	BranchName  string
	BaseBranch  string
	NoRefresh   bool

	AllowMissingOrigin bool
	WorktreePath       string
	HerdrSession       string
	SocketPath         string
	TotalTimeout       time.Duration
}

type HerdrWorktreeResult struct {
	Intent state.HerdrIntent
	Pane   backend.PaneRef
}

// herdrRealizeSetup is the shared realize prologue: the bounded operation
// context, resolved repository identity, and the journal view read under the
// caller-held combined launch lock.
type herdrRealizeSetup struct {
	ctx                     context.Context
	deadline                time.Time
	source                  worktree.HerdrRepoIdentity
	ownerProjectRoot        string
	runtimeParent           string
	runtimeOwnerProjectRoot string
	locked                  *state.LockedHerdrIntents
	hooks                   HerdrRealizeHooks
}

func newHerdrRealizeSetup(
	ctx context.Context,
	parent, projectRoot, sourceRoot string,
	totalTimeout time.Duration,
	launchLock *state.LockedStore,
	hooks HerdrRealizeHooks,
) (herdrRealizeSetup, context.CancelFunc, error) {
	setup := herdrRealizeSetup{hooks: normalizeHerdrRealizeHooks(hooks)}
	timeout, timeoutErr := normalizeHerdrRealizeTimeout(totalTimeout)
	if timeoutErr != nil {
		return setup, nil, timeoutErr
	}
	realizeCtx, cancel := context.WithTimeout(ctx, timeout)
	fail := func(err error) (herdrRealizeSetup, context.CancelFunc, error) {
		cancel()
		return setup, nil, err
	}
	if err := realizeCtx.Err(); err != nil {
		return fail(err)
	}
	setup.ctx = realizeCtx
	setup.deadline, _ = realizeCtx.Deadline()
	source, sourceErr := worktree.ResolveHerdrRepoIdentity(sourceRoot)
	if sourceErr != nil {
		return fail(sourceErr)
	}
	project, projectErr := worktree.ResolveHerdrRepoIdentity(projectRoot)
	if projectErr != nil {
		return fail(projectErr)
	}
	if source.RepoKey != project.RepoKey {
		return fail(fmt.Errorf("herdr project and source roots belong to different repositories"))
	}
	setup.source = source
	ownerProjectRoot, ownerErr := state.HerdrOwnerProjectRoot(parent, project.RepoRoot)
	if ownerErr != nil {
		return fail(ownerErr)
	}
	setup.ownerProjectRoot = ownerProjectRoot
	locked, lockedErr := launchLock.HerdrIntents(projectRoot)
	if lockedErr != nil {
		return fail(lockedErr)
	}
	setup.locked = locked
	runtimeParent, runtimeParentErr := resolveHerdrRuntimeParent(
		projectRoot,
		parent,
		ownerProjectRoot,
		locked.HerdrIntents,
	)
	if runtimeParentErr != nil {
		return fail(runtimeParentErr)
	}
	setup.runtimeParent = runtimeParent
	runtimeOwnerProjectRoot, runtimeOwnerErr := state.HerdrOwnerProjectRoot(
		runtimeParent,
		project.RepoRoot,
	)
	if runtimeOwnerErr != nil {
		return fail(runtimeOwnerErr)
	}
	setup.runtimeOwnerProjectRoot = runtimeOwnerProjectRoot
	return setup, cancel, nil
}

// RealizeHerdrCoordinator creates or recovers the actual-parent coordinator
// workspace under the caller-held combined launch lock. It stops with a
// durable realized intent before launcher work. The coordinator mutation has
// no non-issuance proof, so there is no planned stage: the intent is saved as
// issued immediately before the one workspace create.
func RealizeHerdrCoordinator(
	ctx context.Context,
	req HerdrCoordinatorRequest,
	runtime HerdrWorktreeRuntime,
	launchLock *state.LockedStore,
	hooks HerdrRealizeHooks,
) (result HerdrCoordinatorResult, retErr error) {
	if ctx == nil || runtime == nil || launchLock == nil {
		return result, fmt.Errorf("realize Herdr coordinator requires context, runtime, and launch lock")
	}
	if validateErr := validateHerdrCoordinatorRequest(req); validateErr != nil {
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
	cwd, cwdErr := filepath.EvalSymlinks(req.CWD)
	if cwdErr != nil {
		return result, fmt.Errorf("canonicalize Herdr coordinator cwd: %w", cwdErr)
	}
	cwd = filepath.Clean(cwd)
	if cwd != setup.source.RepoRoot {
		return result, fmt.Errorf("herdr coordinator cwd %s does not match source root %s", cwd, setup.source.RepoRoot)
	}
	intentID, intentIDErr := state.HerdrCoordinatorIntentID(
		setup.runtimeParent,
		setup.runtimeOwnerProjectRoot,
		req.IssueNum,
	)
	if intentIDErr != nil {
		return result, intentIDErr
	}

	intent, found := locked.FindIntent(intentID)
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
		return result, markHerdrIntentManual(locked, intent, routeContextErr)
	}
	defer routeCancel()
	if routeErr := verifyHerdrRealizeRoute(
		routeCtx,
		runtime,
		setup.source.RepoKey,
		req.HerdrSession,
		req.SocketPath,
	); routeErr != nil {
		return result, routeErr
	}
	if found {
		if savedErr := validateSavedCoordinatorIntent(
			req,
			cwd,
			setup.runtimeParent,
			setup.runtimeOwnerProjectRoot,
			intent,
		); savedErr != nil {
			return result, savedErr
		}
		operationCtx, cancel, contextErr := herdrIntentContext(operationParent, intent, operationNow)
		if contextErr != nil {
			return result, markHerdrIntentManual(locked, intent, contextErr)
		}
		defer cancel()
		switch intent.Status {
		case state.HerdrIntentRealized:
			if verifyErr := verifyRealizedCoordinator(operationCtx, runtime, intent); verifyErr != nil {
				if operationErr := operationCtx.Err(); operationErr != nil {
					return result, errors.Join(verifyErr, operationErr)
				}
				return result, markHerdrIntentManual(locked, intent, verifyErr)
			}
			return coordinatorDeferred(intent)
		case state.HerdrIntentIssued:
			return recoverHerdrCoordinator(operationCtx, runtime, locked, intent, nil)
		case state.HerdrIntentManualCleanupRequired:
			return result, herdrManualCleanupError(intent)
		default:
			return result, markHerdrIntentManual(
				locked,
				intent,
				fmt.Errorf("unknown Herdr coordinator intent status %q", intent.Status),
			)
		}
	}

	if policyErr := runtime.VerifyWorktreeSetupPolicy(operationParent); policyErr != nil {
		return result, policyErr
	}
	label, labelErr := newHerdrWorkspaceLabel("coordinator", setup.hooks.RandomToken)
	if labelErr != nil {
		return result, labelErr
	}
	intent = state.HerdrIntent{
		ID: intentID, Kind: state.HerdrIntentCoordinator, Status: state.HerdrIntentIssued,
		Parent: canonicalHerdrParent(req.Parent), RuntimeParent: setup.runtimeParent,
		OwnerProjectRoot: setup.ownerProjectRoot,
		IssueNum:         req.IssueNum,
		WorktreePath:     cwd,
		WorkspaceLabel:   label, Session: req.HerdrSession, SocketPath: req.SocketPath,
		ExpiresUnixMS: setup.deadline.UnixMilli(),
	}
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		return result, saveErr
	}
	mutation, mutationErr := runtime.CreateWorkspace(operationParent, herdrrun.WorkspaceCreateRequest{
		CWD:           intent.WorktreePath,
		SourceRepoKey: setup.source.RepoKey,
		Label:         intent.WorkspaceLabel,
	})
	if mutationErr != nil {
		if errors.Is(mutationErr, herdrrun.ErrMutationNotIssued) {
			return result, releaseHerdrIntent(locked, intent.ID, mutationErr)
		}
		if operationErr := operationParent.Err(); operationErr != nil {
			return result, errors.Join(mutationErr, operationErr)
		}
		return recoverHerdrCoordinator(operationParent, runtime, locked, intent, mutationErr)
	}
	if err := validateCoordinatorObservation(intent, mutation.WorkspaceObservation); err != nil {
		return result, markHerdrIntentManual(locked, intent, err)
	}
	intent.Resource = stateResource(mutation.WorkspaceObservation)
	intent.Status = state.HerdrIntentRealized
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return result, err
	}
	return coordinatorDeferred(intent)
}

// RealizeHerdrWorktree reserves the child branch, creates or recovers the
// Herdr checkout workspace under the caller-held combined launch lock and
// stops before launcher readiness.
func RealizeHerdrWorktree(
	ctx context.Context,
	req HerdrWorktreeRequest,
	runtime HerdrWorktreeRuntime,
	launchLock *state.LockedStore,
	hooks HerdrRealizeHooks,
) (result HerdrWorktreeResult, retErr error) {
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
	intentID, intentIDErr := state.HerdrWorktreeIntentID(
		req.Parent,
		ownerProjectRoot,
		req.IssueNum,
		req.TaskID,
	)
	if intentIDErr != nil {
		return result, intentIDErr
	}
	intent, found := locked.FindIntent(intentID)
	coordinatorID, coordinatorIDErr := state.HerdrCoordinatorIntentID(
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
		if intent.Status == state.HerdrIntentPlanned {
			return result, rollbackUnissuedHerdrWorktree(locked, req, intent, routeContextErr)
		}
		return result, markHerdrIntentManual(locked, intent, routeContextErr)
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
	coordinatorSource, sourceErr := herdrCoordinatorSource(coordinator, source)
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
		savedProjectRoot, _, savedRootErr := savedHerdrWorktreeSource(intent, source)
		if savedRootErr != nil {
			return result, savedRootErr
		}
		req.ProjectRoot = savedProjectRoot
	} else {
		if excludeErr := worktree.EnsureLocalExclude(req.SourceRoot); excludeErr != nil {
			return result, excludeErr
		}
		if parentErr := worktree.EnsureHerdrWorktreeParent(req.ProjectRoot, req.WorktreePath); parentErr != nil {
			return result, parentErr
		}
		base, baseErr := worktree.ResolveHerdrBaseContext(setup.ctx, worktree.Options{
			ProjectRoot: req.SourceRoot, Slug: req.Slug, BranchName: req.BranchName,
			BaseBranch: req.BaseBranch, NoRefresh: req.NoRefresh,
			AllowMissingOrigin: req.AllowMissingOrigin,
		})
		if baseErr != nil {
			return result, baseErr
		}
		fullRef, refErr := worktree.HerdrBranchRef(req.SourceRoot, req.BranchName)
		if refErr != nil {
			return result, refErr
		}
		head, branchExisted, branchErr := worktree.ObserveHerdrBranch(req.SourceRoot, fullRef)
		if branchErr != nil {
			return result, branchErr
		}
		if branchExisted {
			if availableErr := worktree.HerdrBranchAvailable(req.SourceRoot, fullRef); availableErr != nil {
				return result, availableErr
			}
		} else {
			head = base.SHA
		}
		checkout, checkoutErr := worktree.ObserveHerdrCheckout(req.SourceRoot, req.WorktreePath)
		if checkoutErr != nil {
			return result, checkoutErr
		}
		if !checkout.PathAbsent || checkout.Registered {
			return result, fmt.Errorf("herdr worktree path already exists or is registered")
		}
		label, labelErr := newHerdrWorkspaceLabel("worktree", setup.hooks.RandomToken)
		if labelErr != nil {
			return result, labelErr
		}
		intent = state.HerdrIntent{
			ID: intentID, Kind: state.HerdrIntentWorktree, Status: state.HerdrIntentPlanned,
			Parent: canonicalHerdrParent(req.Parent), RuntimeParent: runtimeParent,
			OwnerProjectRoot: ownerProjectRoot,
			IssueNum:         req.IssueNum,
			TaskID:           req.TaskID,
			Slug:             req.Slug, BranchName: req.BranchName, FullBranchRef: fullRef,
			BaseBranch: base.BaseBranch, BaseSHA: base.SHA, ExpectedHead: head,
			WorktreePath: filepath.Clean(req.WorktreePath), BranchExisted: branchExisted,
			WorkspaceLabel: label, Coordinator: coordinator,
			Session: req.HerdrSession, SocketPath: req.SocketPath,
			ExpiresUnixMS: setup.deadline.UnixMilli(),
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
			intent.Status == state.HerdrIntentPlanned {
			return result, rollbackUnissuedHerdrWorktree(locked, req, intent, contextErr)
		}
		return result, markHerdrIntentManual(locked, intent, contextErr)
	}
	defer cancel()

	switch intent.Status {
	case state.HerdrIntentRealized:
		return resumeRealizedHerdrWorktree(
			operationCtx,
			runtime,
			locked,
			req,
			source,
			intent,
			!classificationOnly,
		)
	case state.HerdrIntentIssued:
		return recoverHerdrWorktree(operationCtx, runtime, locked, req, source, intent, nil)
	case state.HerdrIntentManualCleanupRequired:
		return result, herdrManualCleanupError(intent)
	case state.HerdrIntentPlanned:
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
	intent, reservationErr := ensureHerdrBranchReservation(locked, req, intent)
	if reservationErr != nil {
		return result, reservationErr
	}
	workspaces, observeErr := runtime.ObserveWorkspaces(operationCtx)
	if observeErr != nil {
		return result, observeErr
	}
	if coordinatorErr := verifyCoordinatorObservation(intent.Coordinator, workspaces); coordinatorErr != nil {
		return result, markHerdrIntentManual(locked, intent, coordinatorErr)
	}
	if matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel); len(matches) != 0 {
		return result, markHerdrIntentManual(
			locked,
			intent,
			fmt.Errorf("planned Herdr worktree label already has %d workspaces", len(matches)),
		)
	}
	if preconditionErr := verifyHerdrWorktreePreconditions(req, source, intent); preconditionErr != nil {
		return result, markHerdrIntentManual(locked, intent, preconditionErr)
	}

	intent.Status = state.HerdrIntentIssued
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		return result, saveErr
	}
	baseArg := ""
	if intent.BranchCreated {
		baseArg = intent.BaseSHA
	}
	mutation, mutationErr := runtime.CreateWorktree(operationCtx, herdrrun.WorktreeCreateRequest{
		Coordinator:    observationResource(intent.Coordinator),
		SourceRepoKey:  source.RepoKey,
		SourceRepoRoot: source.RepoRoot,
		Branch:         intent.BranchName,
		Base:           baseArg,
		Path:           intent.WorktreePath,
		Label:          intent.WorkspaceLabel,
	})
	if mutationErr != nil {
		if errors.Is(mutationErr, herdrrun.ErrMutationNotIssued) {
			return result, rollbackUnissuedHerdrWorktree(locked, req, intent, mutationErr)
		}
		if operationErr := operationCtx.Err(); operationErr != nil {
			return result, errors.Join(mutationErr, operationErr)
		}
		return recoverHerdrWorktree(operationCtx, runtime, locked, req, source, intent, mutationErr)
	}
	if finalizeErr := finalizeHerdrWorktree(locked, req, source, &intent, mutation.WorkspaceObservation); finalizeErr != nil {
		return result, handleHerdrWorktreeFinalizeError(locked, intent, finalizeErr)
	}
	return worktreeDeferred(intent)
}

func verifyHerdrRealizeRoute(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	repoKey, session, socketPath string,
) error {
	route, err := runtime.WorktreeRoute(ctx)
	if err != nil {
		return fmt.Errorf("verify Herdr owned worktree route: %w", err)
	}
	if route.GitCommonDir != repoKey || route.Session != session || route.SocketPath != socketPath {
		return fmt.Errorf("herdr realization route does not match owned session")
	}
	return nil
}

func herdrRealizeRouteContext(
	parent context.Context,
	intent state.HerdrIntent,
	found bool,
	now time.Time,
) (context.Context, context.Context, context.CancelFunc, error) {
	operationParent := parent
	operationCancel := func() {}
	if found {
		var err error
		operationParent, operationCancel, err = herdrIntentContext(parent, intent, now)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	ctx, cancel := context.WithTimeout(operationParent, maxHerdrRecoveryClassificationTimeout)
	if err := ctx.Err(); err != nil {
		cancel()
		operationCancel()
		return nil, nil, nil, err
	}
	return ctx, operationParent, func() {
		cancel()
		operationCancel()
	}, nil
}

func resolveHerdrRuntimeParent(
	projectRoot, parent, ownerProjectRoot string,
	control state.HerdrIntents,
) (string, error) {
	parent = canonicalHerdrParent(parent)
	saved := ""
	observe := func(savedParent, savedRuntimeParent, savedOwnerProjectRoot string) error {
		if canonicalHerdrParent(savedParent) != parent {
			return nil
		}
		if filepath.Clean(savedOwnerProjectRoot) != filepath.Clean(ownerProjectRoot) {
			return nil
		}
		runtimeParent := canonicalHerdrParent(savedRuntimeParent)
		if saved != "" && saved != runtimeParent {
			return fmt.Errorf(
				"saved Herdr runtime parents for %s disagree: %s and %s",
				parent,
				saved,
				runtimeParent,
			)
		}
		saved = runtimeParent
		return nil
	}
	for _, intent := range control.Intents {
		if err := observe(intent.Parent, intent.RuntimeParent, intent.OwnerProjectRoot); err != nil {
			return "", err
		}
	}
	if saved != "" {
		return saved, nil
	}
	if planSlug, ok := strings.CutPrefix(parent, "plan:"); ok && planSlug != "" {
		parent = canonicalHerdrParent(SavedPlanRuntimeParentRef(projectRoot, planSlug))
	}
	if parent == "" {
		return "", fmt.Errorf("resolve Herdr runtime parent: empty parent")
	}
	return parent, nil
}

func herdrCoordinatorSyntheticIssueNum(parent string, issueNum int) int {
	switch canonicalHerdrParent(parent) {
	case ManualParentRef, WatchParentRef:
		return issueNum
	default:
		return 0
	}
}

func verifyHerdrWorktreePreconditions(
	req HerdrWorktreeRequest,
	source worktree.HerdrRepoIdentity,
	intent state.HerdrIntent,
) error {
	if err := worktree.VerifyHerdrWorktreeParent(req.ProjectRoot, intent.WorktreePath); err != nil {
		return err
	}
	branch, found, branchErr := worktree.ObserveHerdrBranch(req.SourceRoot, intent.FullBranchRef)
	if branchErr != nil {
		return branchErr
	}
	if !found || branch != intent.ExpectedHead {
		return fmt.Errorf("herdr branch %s does not match saved head", intent.FullBranchRef)
	}
	if availableErr := worktree.HerdrBranchAvailable(req.SourceRoot, intent.FullBranchRef); availableErr != nil {
		return availableErr
	}
	checkout, err := worktree.ObserveHerdrCheckout(req.SourceRoot, intent.WorktreePath)
	if err != nil {
		return err
	}
	if !checkout.PathAbsent || checkout.Registered {
		return fmt.Errorf("herdr checkout appeared before mutation")
	}
	current, err := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if err != nil {
		return err
	}
	if current != source {
		return fmt.Errorf("herdr source repository identity changed")
	}
	return nil
}

func validateCoordinatorObservation(
	intent state.HerdrIntent,
	observation herdrrun.WorkspaceObservation,
) error {
	if observation.WorkspaceID == "" || observation.Label != intent.WorkspaceLabel ||
		observation.Path != "" || observation.RepoKey != "" || observation.RepoRoot != "" ||
		observation.Pane.Backend != backend.Herdr || observation.Pane.Workspace != observation.WorkspaceID ||
		observation.Pane.Pane == "" || observation.TerminalID == "" ||
		filepath.Clean(observation.CWD) != filepath.Clean(intent.WorktreePath) {
		return fmt.Errorf("herdr coordinator postcondition does not match intent")
	}
	return nil
}

func validateWorktreeObservation(
	intent state.HerdrIntent,
	source worktree.HerdrRepoIdentity,
	observation herdrrun.WorkspaceObservation,
) error {
	if observation.WorkspaceID == "" || observation.Label != intent.WorkspaceLabel ||
		filepath.Clean(observation.Path) != filepath.Clean(intent.WorktreePath) ||
		observation.RepoKey != source.RepoKey || observation.RepoRoot != source.RepoRoot ||
		observation.Pane.Backend != backend.Herdr ||
		observation.Pane.Workspace != observation.WorkspaceID || observation.Pane.Pane == "" ||
		observation.TerminalID == "" ||
		filepath.Clean(observation.CWD) != filepath.Clean(intent.WorktreePath) {
		return fmt.Errorf("herdr worktree postcondition does not match intent")
	}
	return nil
}

func verifyCoordinatorObservation(
	expected state.HerdrResource,
	workspaces []herdrrun.WorkspaceObservation,
) error {
	for _, workspace := range workspaces {
		if workspace.WorkspaceID != expected.WorkspaceID {
			continue
		}
		if workspaceHasHerdrResource(workspace, expected) {
			return nil
		}
		return fmt.Errorf("herdr coordinator identity changed before child mutation")
	}
	return fmt.Errorf("herdr coordinator workspace %s is not live", expected.WorkspaceID)
}

func validateSavedCoordinatorIntent(
	req HerdrCoordinatorRequest,
	cwd string,
	runtimeParent string,
	runtimeOwnerProjectRoot string,
	intent state.HerdrIntent,
) error {
	wantID, err := state.HerdrCoordinatorIntentID(
		runtimeParent,
		runtimeOwnerProjectRoot,
		req.IssueNum,
	)
	if err != nil {
		return err
	}
	if intent.ID != wantID || intent.Kind != state.HerdrIntentCoordinator ||
		intent.RuntimeParent != runtimeParent ||
		intent.IssueNum != req.IssueNum ||
		!savedHerdrCoordinatorPathMatches(
			runtimeOwnerProjectRoot,
			intent.WorktreePath,
			cwd,
		) ||
		intent.Session != req.HerdrSession || intent.SocketPath != req.SocketPath {
		return fmt.Errorf("saved Herdr coordinator intent contradicts request")
	}
	return nil
}

func validateSavedWorktreeIntent(
	req HerdrWorktreeRequest,
	source worktree.HerdrRepoIdentity,
	coordinator state.HerdrResource,
	ownerProjectRoot string,
	runtimeParent string,
	intent state.HerdrIntent,
) error {
	wantID, err := state.HerdrWorktreeIntentID(
		req.Parent,
		ownerProjectRoot,
		req.IssueNum,
		req.TaskID,
	)
	if err != nil {
		return err
	}
	if intent.ID != wantID || intent.Kind != state.HerdrIntentWorktree ||
		intent.Parent != canonicalHerdrParent(req.Parent) ||
		intent.RuntimeParent != runtimeParent ||
		intent.OwnerProjectRoot != ownerProjectRoot ||
		intent.IssueNum != req.IssueNum || intent.TaskID != req.TaskID ||
		!savedHerdrWorktreePathValid(
			ownerProjectRoot,
			intent.Slug,
			intent.WorktreePath,
		) ||
		intent.Session != req.HerdrSession || intent.SocketPath != req.SocketPath ||
		!sameHerdrResource(intent.Coordinator, coordinator) {
		return fmt.Errorf("saved Herdr worktree intent contradicts request")
	}
	if intent.Resource.RepoKey != "" &&
		!savedHerdrWorktreeRepoMatches(ownerProjectRoot, intent.Resource, source) {
		return fmt.Errorf("saved Herdr worktree intent belongs to a different repository")
	}
	return nil
}

func validateHerdrCoordinatorRequest(req HerdrCoordinatorRequest) error {
	if strings.TrimSpace(req.Parent) == "" || req.ProjectRoot == "" || req.SourceRoot == "" ||
		req.CWD == "" || req.HerdrSession == "" || req.SocketPath == "" {
		return fmt.Errorf("herdr coordinator request is incomplete")
	}
	return nil
}

func validateHerdrWorktreeRequest(req HerdrWorktreeRequest) error {
	if strings.TrimSpace(req.Parent) == "" || req.ProjectRoot == "" || req.SourceRoot == "" ||
		req.Slug == "" || req.BranchName == "" || req.WorktreePath == "" ||
		req.HerdrSession == "" || req.SocketPath == "" {
		return fmt.Errorf("herdr worktree request is incomplete")
	}
	issueKey := req.IssueNum > 0 ||
		canonicalHerdrParent(req.Parent) == ManualParentRef && req.IssueNum < 0
	if issueKey == (strings.TrimSpace(req.TaskID) != "") {
		return fmt.Errorf("herdr worktree request requires exactly one issue number or task id")
	}
	expected := filepath.Join(req.ProjectRoot, ".fanout", "worktrees", req.Slug)
	if filepath.Clean(req.WorktreePath) != filepath.Clean(expected) {
		return fmt.Errorf("herdr worktree path %s does not match slug %s", req.WorktreePath, req.Slug)
	}
	return nil
}

func coordinatorDeferred(intent state.HerdrIntent) (HerdrCoordinatorResult, error) {
	return HerdrCoordinatorResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
	}, ErrHerdrLauncherReadinessDeferred
}

func resolvedHerdrCoordinator(
	locked *state.LockedHerdrIntents,
	coordinatorID string,
	req HerdrWorktreeRequest,
	repoRoot string,
	runtimeParent string,
	runtimeOwnerProjectRoot string,
) (state.HerdrResource, error) {
	intent, found := locked.FindIntent(coordinatorID)
	if !found || intent.Status != state.HerdrIntentRealized {
		return state.HerdrResource{}, fmt.Errorf("herdr coordinator %s is not realized", coordinatorID)
	}
	if intent.RuntimeParent != runtimeParent ||
		intent.Session != req.HerdrSession || intent.SocketPath != req.SocketPath ||
		!savedHerdrCoordinatorPathMatches(
			runtimeOwnerProjectRoot,
			intent.WorktreePath,
			repoRoot,
		) {
		return state.HerdrResource{}, fmt.Errorf("herdr coordinator intent contradicts child request")
	}
	return intent.Resource, nil
}

func herdrCoordinatorSource(
	coordinator state.HerdrResource,
	requestSource worktree.HerdrRepoIdentity,
) (worktree.HerdrRepoIdentity, error) {
	source, err := worktree.ResolveHerdrRepoIdentity(coordinator.CurrentPath)
	if err != nil {
		return worktree.HerdrRepoIdentity{}, fmt.Errorf("resolve Herdr coordinator source: %w", err)
	}
	if source.RepoKey != requestSource.RepoKey {
		return worktree.HerdrRepoIdentity{}, fmt.Errorf("herdr coordinator source belongs to a different repository")
	}
	return source, nil
}

func worktreeDeferred(intent state.HerdrIntent) (HerdrWorktreeResult, error) {
	return HerdrWorktreeResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
	}, ErrHerdrLauncherReadinessDeferred
}

func workspacesWithLabel(
	workspaces []herdrrun.WorkspaceObservation,
	label string,
) []herdrrun.WorkspaceObservation {
	var matches []herdrrun.WorkspaceObservation
	for _, workspace := range workspaces {
		if workspace.Label == label {
			matches = append(matches, workspace)
		}
	}
	return matches
}

func stateResource(observation herdrrun.WorkspaceObservation) state.HerdrResource {
	return state.HerdrResource{
		WorkspaceID: observation.WorkspaceID,
		Label:       observation.Label,
		PaneID:      observation.Pane.Pane,
		TerminalID:  observation.TerminalID,
		CurrentPath: observation.CWD,
		RepoKey:     observation.RepoKey,
		RepoRoot:    observation.RepoRoot,
	}
}

func observationResource(resource state.HerdrResource) herdrrun.WorkspaceObservation {
	return herdrrun.WorkspaceObservation{
		WorkspaceID: resource.WorkspaceID,
		Label:       resource.Label,
		RepoKey:     resource.RepoKey,
		RepoRoot:    resource.RepoRoot,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: resource.WorkspaceID, Pane: resource.PaneID,
		},
		TerminalID: resource.TerminalID,
		CWD:        resource.CurrentPath,
	}
}

func sameHerdrResource(left, right state.HerdrResource) bool {
	return left == right
}

func workspaceHasHerdrResource(
	observation herdrrun.WorkspaceObservation,
	expected state.HerdrResource,
) bool {
	if observation.WorkspaceID != expected.WorkspaceID ||
		observation.Label != expected.Label ||
		observation.RepoKey != expected.RepoKey ||
		observation.RepoRoot != expected.RepoRoot {
		return false
	}
	if sameHerdrResource(expected, stateResource(observation)) {
		return true
	}
	for _, pane := range observation.Panes {
		if pane.Pane.Backend == backend.Herdr &&
			pane.Pane.Workspace == expected.WorkspaceID &&
			pane.Pane.Pane == expected.PaneID &&
			pane.TerminalID == expected.TerminalID &&
			pane.CWD == expected.CurrentPath {
			return true
		}
	}
	return false
}

func normalizeHerdrRealizeHooks(hooks HerdrRealizeHooks) HerdrRealizeHooks {
	if hooks.Now == nil {
		hooks.Now = time.Now
	}
	if hooks.RandomToken == nil {
		hooks.RandomToken = randomHerdrToken
	}
	return hooks
}

func normalizeHerdrRealizeTimeout(timeout time.Duration) (time.Duration, error) {
	if timeout == 0 {
		return maxHerdrRealizeTimeout, nil
	}
	if timeout < minHerdrRealizeTimeout || timeout > maxHerdrRealizeTimeout ||
		timeout%time.Millisecond != 0 {
		return 0, fmt.Errorf("herdr realization timeout must be 3s..300s at millisecond precision")
	}
	return timeout, nil
}

func herdrIntentContext(
	parent context.Context,
	intent state.HerdrIntent,
	now time.Time,
) (context.Context, context.CancelFunc, error) {
	deadline := time.UnixMilli(intent.ExpiresUnixMS)
	if !now.Before(deadline) {
		switch intent.Status {
		case state.HerdrIntentPlanned:
			return nil, nil, errHerdrIntentDeadlineExpired
		case state.HerdrIntentIssued, state.HerdrIntentRealized:
			// An expired launch never receives another full total_timeout.
			// Bound this invocation to a short existence classification.
			ctx, cancel := context.WithTimeout(parent, maxHerdrRecoveryClassificationTimeout)
			if err := ctx.Err(); err != nil {
				cancel()
				return nil, nil, err
			}
			return ctx, cancel, nil
		default:
			return nil, nil, errHerdrIntentDeadlineExpired
		}
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

func savedHerdrCoordinatorPathMatches(ownerProjectRoot, savedPath, requestPath string) bool {
	savedPath = filepath.Clean(savedPath)
	if !filepath.IsAbs(savedPath) {
		return false
	}
	return ownerProjectRoot == "" || savedPath == filepath.Clean(requestPath)
}

func savedHerdrWorktreePathValid(ownerProjectRoot, savedSlug, savedPath string) bool {
	savedPath = filepath.Clean(savedPath)
	if !filepath.IsAbs(savedPath) {
		return false
	}
	if ownerProjectRoot == "" {
		return true
	}
	worktreesDir := filepath.Dir(savedPath)
	fanoutDir := filepath.Dir(worktreesDir)
	if filepath.Base(savedPath) != savedSlug || filepath.Base(worktreesDir) != "worktrees" ||
		filepath.Base(fanoutDir) != ".fanout" {
		return false
	}
	savedRoot, err := filepath.EvalSymlinks(filepath.Dir(fanoutDir))
	return err == nil && filepath.Clean(savedRoot) == filepath.Clean(ownerProjectRoot)
}

func savedHerdrWorktreeSource(
	intent state.HerdrIntent,
	source worktree.HerdrRepoIdentity,
) (string, worktree.HerdrRepoIdentity, error) {
	savedPath := filepath.Clean(intent.WorktreePath)
	worktreesDir := filepath.Dir(savedPath)
	fanoutDir := filepath.Dir(worktreesDir)
	projectRoot := filepath.Dir(fanoutDir)
	if !filepath.IsAbs(savedPath) || filepath.Base(savedPath) != intent.Slug ||
		filepath.Base(worktreesDir) != "worktrees" || filepath.Base(fanoutDir) != ".fanout" {
		return "", worktree.HerdrRepoIdentity{}, fmt.Errorf("saved Herdr worktree path has no owner project root")
	}
	identity, err := worktree.ResolveHerdrRepoIdentity(projectRoot)
	if err != nil {
		return "", worktree.HerdrRepoIdentity{}, fmt.Errorf("resolve saved Herdr worktree owner: %w", err)
	}
	if identity.RepoKey != source.RepoKey ||
		(intent.OwnerProjectRoot != "" && identity.RepoRoot != intent.OwnerProjectRoot) {
		return "", worktree.HerdrRepoIdentity{}, fmt.Errorf("saved Herdr worktree owner belongs to a different repository")
	}
	return projectRoot, identity, nil
}

func savedHerdrWorktreeRepoMatches(
	ownerProjectRoot string,
	resource state.HerdrResource,
	source worktree.HerdrRepoIdentity,
) bool {
	if resource.RepoKey != source.RepoKey {
		return false
	}
	return ownerProjectRoot == "" || resource.RepoRoot == source.RepoRoot
}

func newHerdrWorkspaceLabel(
	kind string,
	randomToken func() (string, error),
) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("create Herdr %s workspace label: %w", kind, err)
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return "", fmt.Errorf("create Herdr %s workspace label: invalid random token", kind)
	}
	return "fanout-" + kind + "-" + token, nil
}

func randomHerdrToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func canonicalHerdrParent(parent string) string {
	return parentref.Canon(strings.TrimSpace(parent))
}
