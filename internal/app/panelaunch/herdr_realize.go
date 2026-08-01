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
)

type HerdrWorktreeRuntime interface {
	WorktreeRoute(context.Context) (herdrrun.OwnedWorktreeRoute, error)
	VerifyWorktreeSetupPolicy(context.Context) error
	ObserveWorkspaces(context.Context) ([]herdrrun.WorkspaceObservation, error)
	MutateWorktree(
		context.Context,
		herdrrun.WorktreeMutationRequest,
	) (herdrrun.WorktreeMutationResult, error)
}

type HerdrRealizeHooks struct {
	Now         func() time.Time
	RandomToken func() (string, error)
}

type HerdrCoordinatorRequest struct {
	Parent       string
	ProjectRoot  string
	SourceRoot   string
	CWD          string
	HerdrSession string
	SocketPath   string
	TotalTimeout time.Duration
}

type HerdrCoordinatorResult struct {
	Intent           state.HerdrIntent
	Row              state.HerdrRow
	Pane             backend.PaneRef
	AlreadyFinalized bool
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
	Intent           state.HerdrIntent
	Row              state.HerdrRow
	Pane             backend.PaneRef
	AlreadyFinalized bool
}

// RealizeHerdrCoordinator creates or recovers the actual-parent coordinator
// workspace under the caller-held combined launch lock. It stops with a
// durable realized intent before launcher work.
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
	timeout, timeoutErr := normalizeHerdrRealizeTimeout(req.TotalTimeout)
	if timeoutErr != nil {
		return result, timeoutErr
	}
	hooks = normalizeHerdrRealizeHooks(hooks)
	if validateErr := validateHerdrCoordinatorRequest(req); validateErr != nil {
		return result, validateErr
	}
	realizeCtx, realizeCancel := context.WithTimeout(ctx, timeout)
	defer realizeCancel()
	if err := realizeCtx.Err(); err != nil {
		return result, err
	}
	realizeDeadline, _ := realizeCtx.Deadline()
	identity, identityErr := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if identityErr != nil {
		return result, identityErr
	}
	projectIdentity, projectIdentityErr := worktree.ResolveHerdrRepoIdentity(req.ProjectRoot)
	if projectIdentityErr != nil {
		return result, projectIdentityErr
	}
	if identity.RepoKey != projectIdentity.RepoKey {
		return result, fmt.Errorf("herdr coordinator roots belong to different repositories")
	}
	cwd, cwdErr := filepath.EvalSymlinks(req.CWD)
	if cwdErr != nil {
		return result, fmt.Errorf("canonicalize Herdr coordinator cwd: %w", cwdErr)
	}
	cwd = filepath.Clean(cwd)
	if cwd != identity.RepoRoot {
		return result, fmt.Errorf("herdr coordinator cwd %s does not match source root %s", cwd, identity.RepoRoot)
	}
	ownerProjectRoot, ownerErr := state.HerdrOwnerProjectRoot(req.Parent, projectIdentity.RepoRoot)
	if ownerErr != nil {
		return result, ownerErr
	}
	controlSnapshot, snapshotErr := state.LoadHerdrControl(req.ProjectRoot)
	if snapshotErr != nil {
		return result, snapshotErr
	}
	runtimeParent, runtimeParentErr := resolveHerdrRuntimeParent(
		req.ProjectRoot,
		req.Parent,
		ownerProjectRoot,
		controlSnapshot,
	)
	if runtimeParentErr != nil {
		return result, runtimeParentErr
	}

	if bindingErr := verifyHerdrStateBindings(
		req.ProjectRoot,
		runtimeParent,
		launchLock.Store,
	); bindingErr != nil {
		return result, bindingErr
	}
	locked, controlErr := launchLock.HerdrControl(req.ProjectRoot)
	if controlErr != nil {
		return result, controlErr
	}
	lockedRuntimeParent, lockedRuntimeParentErr := resolveHerdrRuntimeParent(
		req.ProjectRoot,
		req.Parent,
		ownerProjectRoot,
		locked.HerdrControlStore,
	)
	if lockedRuntimeParentErr != nil {
		return result, lockedRuntimeParentErr
	}
	if lockedRuntimeParent != runtimeParent {
		return result, fmt.Errorf("herdr runtime parent changed while acquiring control lock")
	}
	runtimeOwnerProjectRoot, runtimeOwnerErr := state.HerdrOwnerProjectRoot(
		runtimeParent,
		projectIdentity.RepoRoot,
	)
	if runtimeOwnerErr != nil {
		return result, runtimeOwnerErr
	}
	intentID, intentIDErr := state.HerdrCoordinatorIntentID(
		runtimeParent,
		runtimeOwnerProjectRoot,
	)
	if intentIDErr != nil {
		return result, intentIDErr
	}

	intent, found := locked.FindIntent(intentID)
	operationNow := hooks.Now()
	routeCtx, routeCancel, routeContextErr := herdrRealizeRouteContext(
		realizeCtx,
		intent,
		found,
		operationNow,
	)
	if routeContextErr != nil {
		return result, routeContextErr
	}
	defer routeCancel()
	if routeErr := verifyHerdrRealizeRoute(
		routeCtx,
		runtime,
		identity.RepoKey,
		req.HerdrSession,
		req.SocketPath,
	); routeErr != nil {
		return result, routeErr
	}
	if found {
		if savedErr := validateSavedCoordinatorIntent(
			req,
			cwd,
			runtimeParent,
			runtimeOwnerProjectRoot,
			intent,
		); savedErr != nil {
			return result, savedErr
		}
	} else {
		if row, exists := locked.FindRow(intentID); exists {
			return finalizedHerdrCoordinator(
				req,
				cwd,
				runtimeParent,
				runtimeOwnerProjectRoot,
				row,
			)
		}
		label, labelErr := newHerdrWorkspaceLabel("coordinator", hooks.RandomToken)
		if labelErr != nil {
			return result, labelErr
		}
		intent = state.HerdrIntent{
			ID: intentID, Kind: state.HerdrIntentCoordinator, Status: state.HerdrIntentPlanned,
			Parent: canonicalHerdrParent(req.Parent), RuntimeParent: runtimeParent,
			OwnerProjectRoot: ownerProjectRoot,
			Backend:          backend.Herdr, WorktreePath: cwd,
			WorkspaceLabel: label, Session: req.HerdrSession, SocketPath: req.SocketPath,
			TimeoutMS: timeout.Milliseconds(), ExpiresUnixMS: realizeDeadline.UnixMilli(),
		}
		locked.UpsertIntent(intent)
		if saveErr := locked.Save(); saveErr != nil {
			return result, saveErr
		}
	}

	operationCtx, cancel, contextErr := herdrIntentContext(routeCtx, intent, operationNow)
	if contextErr != nil {
		if errors.Is(contextErr, errHerdrIntentDeadlineExpired) &&
			intent.Status == state.HerdrIntentPlanned {
			return result, rollbackUnissuedHerdrCoordinator(locked, intent, contextErr)
		}
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
	case state.HerdrIntentPlanned:
	default:
		return result, markHerdrIntentManual(
			locked,
			intent,
			fmt.Errorf("unknown Herdr coordinator intent status %q", intent.Status),
		)
	}

	if policyErr := runtime.VerifyWorktreeSetupPolicy(operationCtx); policyErr != nil {
		return result, policyErr
	}
	workspaces, observeErr := runtime.ObserveWorkspaces(operationCtx)
	if observeErr != nil {
		return result, observeErr
	}
	if matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel); len(matches) != 0 {
		return result, markHerdrIntentManual(
			locked,
			intent,
			fmt.Errorf("planned Herdr coordinator label already has %d workspaces", len(matches)),
		)
	}
	intent.Status = state.HerdrIntentIssued
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		return result, saveErr
	}
	mutation, mutationErr := runtime.MutateWorktree(operationCtx, herdrrun.WorktreeMutationRequest{
		Kind:           herdrrun.WorkspaceCreate,
		SourceRoot:     intent.WorktreePath,
		SourceRepoKey:  identity.RepoKey,
		SourceRepoRoot: intent.WorktreePath,
		CWD:            intent.WorktreePath,
		Label:          intent.WorkspaceLabel,
		NoFocus:        true,
	})
	if mutationErr != nil {
		if errors.Is(mutationErr, herdrrun.ErrMutationNotIssued) {
			return result, rollbackUnissuedHerdrCoordinator(locked, intent, mutationErr)
		}
		if operationErr := operationCtx.Err(); operationErr != nil {
			return result, errors.Join(mutationErr, operationErr)
		}
		return recoverHerdrCoordinator(operationCtx, runtime, locked, intent, mutationErr)
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
	timeout, timeoutErr := normalizeHerdrRealizeTimeout(req.TotalTimeout)
	if timeoutErr != nil {
		return result, timeoutErr
	}
	hooks = normalizeHerdrRealizeHooks(hooks)
	if validateErr := validateHerdrWorktreeRequest(req); validateErr != nil {
		return result, validateErr
	}
	realizeCtx, realizeCancel := context.WithTimeout(ctx, timeout)
	defer realizeCancel()
	if err := realizeCtx.Err(); err != nil {
		return result, err
	}
	realizeDeadline, _ := realizeCtx.Deadline()
	source, sourceErr := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if sourceErr != nil {
		return result, sourceErr
	}
	project, projectErr := worktree.ResolveHerdrRepoIdentity(req.ProjectRoot)
	if projectErr != nil {
		return result, projectErr
	}
	if source.RepoKey != project.RepoKey {
		return result, fmt.Errorf("herdr project and source roots belong to different repositories")
	}
	ownerProjectRoot, ownerErr := state.HerdrOwnerProjectRoot(req.Parent, project.RepoRoot)
	if ownerErr != nil {
		return result, ownerErr
	}
	intentID, intentIDErr := state.HerdrWorktreeIntentID(
		req.Parent,
		ownerProjectRoot,
		req.IssueNum,
		req.TaskID,
	)
	if intentIDErr != nil {
		return result, intentIDErr
	}
	controlSnapshot, snapshotErr := state.LoadHerdrControl(req.ProjectRoot)
	if snapshotErr != nil {
		return result, snapshotErr
	}
	runtimeParent, runtimeParentErr := resolveHerdrRuntimeParent(
		req.ProjectRoot,
		req.Parent,
		ownerProjectRoot,
		controlSnapshot,
	)
	if runtimeParentErr != nil {
		return result, runtimeParentErr
	}

	if bindingErr := verifyHerdrStateBindings(
		req.ProjectRoot,
		runtimeParent,
		launchLock.Store,
	); bindingErr != nil {
		return result, bindingErr
	}
	locked, controlErr := launchLock.HerdrControl(req.ProjectRoot)
	if controlErr != nil {
		return result, controlErr
	}
	lockedRuntimeParent, lockedRuntimeParentErr := resolveHerdrRuntimeParent(
		req.ProjectRoot,
		req.Parent,
		ownerProjectRoot,
		locked.HerdrControlStore,
	)
	if lockedRuntimeParentErr != nil {
		return result, lockedRuntimeParentErr
	}
	if lockedRuntimeParent != runtimeParent {
		return result, fmt.Errorf("herdr runtime parent changed while acquiring control lock")
	}
	runtimeOwnerProjectRoot, runtimeOwnerErr := state.HerdrOwnerProjectRoot(
		runtimeParent,
		project.RepoRoot,
	)
	if runtimeOwnerErr != nil {
		return result, runtimeOwnerErr
	}
	coordinatorID, coordinatorIDErr := state.HerdrCoordinatorIntentID(
		runtimeParent,
		runtimeOwnerProjectRoot,
	)
	if coordinatorIDErr != nil {
		return result, coordinatorIDErr
	}

	intent, found := locked.FindIntent(intentID)
	operationNow := hooks.Now()
	routeCtx, routeCancel, routeContextErr := herdrRealizeRouteContext(
		realizeCtx,
		intent,
		found,
		operationNow,
	)
	if routeContextErr != nil {
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
	if !found {
		if row, exists := locked.FindRow(intentID); exists {
			return finalizedHerdrWorktree(
				req,
				source,
				ownerProjectRoot,
				runtimeParent,
				row,
			)
		}
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
		savedProjectRoot, savedRootErr := savedHerdrWorktreeProjectRoot(intent, source)
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
		base, baseErr := worktree.ResolveHerdrBaseContext(realizeCtx, worktree.Options{
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
		label, labelErr := newHerdrWorkspaceLabel("worktree", hooks.RandomToken)
		if labelErr != nil {
			return result, labelErr
		}
		intent = state.HerdrIntent{
			ID: intentID, Kind: state.HerdrIntentWorktree, Status: state.HerdrIntentPlanned,
			Parent: canonicalHerdrParent(req.Parent), RuntimeParent: runtimeParent,
			OwnerProjectRoot: ownerProjectRoot,
			IssueNum:         req.IssueNum,
			TaskID:           req.TaskID, Backend: backend.Herdr,
			Slug: req.Slug, BranchName: req.BranchName, FullBranchRef: fullRef,
			BaseBranch: base.BaseBranch, BaseSHA: base.SHA, ExpectedHead: head,
			WorktreePath: filepath.Clean(req.WorktreePath), BranchExisted: branchExisted,
			WorkspaceLabel: label, Coordinator: coordinator,
			Session: req.HerdrSession, SocketPath: req.SocketPath,
			TimeoutMS: timeout.Milliseconds(), ExpiresUnixMS: realizeDeadline.UnixMilli(),
		}
		locked.UpsertIntent(intent)
		if saveErr := locked.Save(); saveErr != nil {
			return result, saveErr
		}
	}

	classificationOnly := !operationNow.Before(time.UnixMilli(intent.ExpiresUnixMS))
	operationCtx, cancel, contextErr := herdrIntentContext(routeCtx, intent, operationNow)
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
	mutation, mutationErr := runtime.MutateWorktree(operationCtx, herdrrun.WorktreeMutationRequest{
		Kind:            herdrrun.WorktreeCreate,
		Coordinator:     observationResource(intent.Coordinator),
		SourceRoot:      source.RepoRoot,
		SourceRepoKey:   source.RepoKey,
		SourceRepoRoot:  source.RepoRoot,
		ProjectRoot:     req.ProjectRoot,
		FullBranchRef:   intent.FullBranchRef,
		ExpectedHeadSHA: intent.ExpectedHead,
		Branch:          intent.BranchName,
		Base:            baseArg,
		Path:            intent.WorktreePath,
		Label:           intent.WorkspaceLabel,
		NoFocus:         true,
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
		return result, markHerdrIntentManual(locked, intent, finalizeErr)
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
) (context.Context, context.CancelFunc, error) {
	if found && !now.Before(time.UnixMilli(intent.ExpiresUnixMS)) &&
		(intent.Status == state.HerdrIntentIssued || intent.Status == state.HerdrIntentRealized) {
		return herdrIntentContext(parent, intent, now)
	}
	ctx, cancel := context.WithCancel(parent)
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

func resolveHerdrRuntimeParent(
	projectRoot, parent, ownerProjectRoot string,
	control state.HerdrControlStore,
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
	for _, row := range control.Rows {
		if err := observe(row.Parent, row.RuntimeParent, row.OwnerProjectRoot); err != nil {
			return "", err
		}
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

func verifyHerdrStateBindings(projectRoot, parent string, current state.Store) error {
	parent = canonicalHerdrParent(parent)
	currentRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return fmt.Errorf("canonicalize Herdr state owner: %w", err)
	}
	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return fmt.Errorf("list Herdr backend binding roots: %w", err)
	}
	planLocal := strings.HasPrefix(parent, "plan:")
	for _, root := range roots {
		canonicalRoot, resolveErr := filepath.EvalSymlinks(root)
		if resolveErr != nil {
			return fmt.Errorf("canonicalize Herdr backend binding root %s: %w", root, resolveErr)
		}
		store := current
		if filepath.Clean(canonicalRoot) != filepath.Clean(currentRoot) {
			if planLocal {
				continue
			}
			store, err = state.LoadProject(root)
			if err != nil {
				return fmt.Errorf("load Herdr backend bindings from %s: %w", root, err)
			}
		}
		for _, binding := range RuntimeBackendBindings(root, store) {
			if canonicalHerdrParent(binding.Parent) == parent &&
				backend.NormalizeName(binding.Backend) != backend.Herdr {
				return fmt.Errorf(
					"runtime backend for parent %s became %s before Herdr realization",
					parent,
					backend.NormalizeName(binding.Backend),
				)
			}
		}
	}
	return nil
}

func ensureHerdrBranchReservation(
	locked *state.LockedHerdrControl,
	req HerdrWorktreeRequest,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	current, found, err := worktree.ObserveHerdrBranch(req.SourceRoot, intent.FullBranchRef)
	if err != nil {
		return intent, err
	}
	if intent.BranchExisted {
		if !found || current != intent.ExpectedHead {
			return intent, markHerdrIntentManual(
				locked,
				intent,
				fmt.Errorf("adopted Herdr branch moved from %s", intent.ExpectedHead),
			)
		}
		if err := worktree.HerdrBranchAvailable(req.SourceRoot, intent.FullBranchRef); err != nil {
			return intent, err
		}
		return intent, nil
	}
	if intent.BranchCreated {
		if !found || current != intent.ExpectedHead {
			return intent, markHerdrIntentManual(
				locked,
				intent,
				fmt.Errorf("reserved Herdr branch moved from %s", intent.ExpectedHead),
			)
		}
		return intent, nil
	}
	if found {
		return intent, markHerdrIntentManual(
			locked,
			intent,
			fmt.Errorf("herdr branch appeared before reservation completed"),
		)
	}
	if err := worktree.ReserveHerdrBranch(req.SourceRoot, intent.FullBranchRef, intent.BaseSHA); err != nil {
		current, found, observeErr := worktree.ObserveHerdrBranch(req.SourceRoot, intent.FullBranchRef)
		if observeErr != nil {
			return intent, markHerdrIntentManual(locked, intent, errors.Join(err, observeErr))
		}
		if found {
			return intent, markHerdrIntentManual(
				locked,
				intent,
				fmt.Errorf("herdr branch reservation result is ambiguous at %s", current),
			)
		}
		locked.RemoveIntent(intent.ID)
		if saveErr := locked.Save(); saveErr != nil {
			return intent, errors.Join(err, saveErr)
		}
		return intent, err
	}
	intent.BranchCreated = true
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return intent, err
	}
	return intent, nil
}

func rollbackUnissuedHerdrCoordinator(
	locked *state.LockedHerdrControl,
	intent state.HerdrIntent,
	mutationErr error,
) error {
	locked.RemoveIntent(intent.ID)
	if err := locked.Save(); err != nil {
		return errors.Join(mutationErr, err)
	}
	return mutationErr
}

func rollbackUnissuedHerdrWorktree(
	locked *state.LockedHerdrControl,
	req HerdrWorktreeRequest,
	intent state.HerdrIntent,
	mutationErr error,
) error {
	if !intent.BranchExisted && !intent.BranchCreated {
		_, found, err := worktree.ObserveHerdrBranch(req.SourceRoot, intent.FullBranchRef)
		if err != nil || found {
			cause := err
			if cause == nil {
				cause = fmt.Errorf("herdr branch exists without persisted ownership")
			}
			return markHerdrIntentManual(locked, intent, errors.Join(mutationErr, cause))
		}
	}
	if intent.BranchCreated {
		if err := worktree.DeleteReservedHerdrBranch(
			req.SourceRoot,
			intent.FullBranchRef,
			intent.BaseSHA,
		); err != nil {
			return markHerdrIntentManual(
				locked,
				intent,
				errors.Join(mutationErr, err),
			)
		}
	}
	locked.RemoveIntent(intent.ID)
	if err := locked.Save(); err != nil {
		return errors.Join(mutationErr, err)
	}
	return mutationErr
}

func recoverHerdrCoordinator(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedHerdrControl,
	intent state.HerdrIntent,
	mutationErr error,
) (HerdrCoordinatorResult, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return HerdrCoordinatorResult{}, errors.Join(
				mutationErr,
				fmt.Errorf("observe Herdr coordinator recovery: %w", err),
				contextErr,
			)
		}
		return HerdrCoordinatorResult{}, markHerdrIntentManual(
			locked,
			intent,
			errors.Join(mutationErr, fmt.Errorf("observe Herdr coordinator recovery: %w", err)),
		)
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) == 1 {
		if err := validateCoordinatorObservation(intent, matches[0]); err != nil {
			return HerdrCoordinatorResult{}, markHerdrIntentManual(locked, intent, err)
		}
		intent.Resource = stateResource(matches[0])
		intent.Status = state.HerdrIntentRealized
		intent.Failure = ""
		locked.UpsertIntent(intent)
		if err := locked.Save(); err != nil {
			return HerdrCoordinatorResult{}, err
		}
		return coordinatorDeferred(intent)
	}
	if errors.Is(mutationErr, herdrrun.ErrMutationRejected) && len(matches) == 0 {
		locked.RemoveIntent(intent.ID)
		if err := locked.Save(); err != nil {
			return HerdrCoordinatorResult{}, errors.Join(mutationErr, err)
		}
		return HerdrCoordinatorResult{}, mutationErr
	}
	return HerdrCoordinatorResult{}, markHerdrIntentManual(
		locked,
		intent,
		errors.Join(
			mutationErr,
			fmt.Errorf("herdr coordinator label has %d recovery matches", len(matches)),
		),
	)
}

func recoverHerdrWorktree(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedHerdrControl,
	req HerdrWorktreeRequest,
	source worktree.HerdrRepoIdentity,
	intent state.HerdrIntent,
	mutationErr error,
) (HerdrWorktreeResult, error) {
	workspaces, observeErr := runtime.ObserveWorkspaces(ctx)
	if observeErr != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return HerdrWorktreeResult{}, errors.Join(
				mutationErr,
				fmt.Errorf("observe Herdr worktree recovery: %w", observeErr),
				contextErr,
			)
		}
		return HerdrWorktreeResult{}, markHerdrIntentManual(
			locked,
			intent,
			errors.Join(mutationErr, fmt.Errorf("observe Herdr worktree recovery: %w", observeErr)),
		)
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) == 1 {
		if err := finalizeHerdrWorktree(locked, req, source, &intent, matches[0]); err != nil {
			return HerdrWorktreeResult{}, markHerdrIntentManual(locked, intent, err)
		}
		return worktreeDeferred(intent)
	}
	checkout, checkoutErr := worktree.ObserveHerdrCheckout(req.SourceRoot, intent.WorktreePath)
	if checkoutErr != nil {
		return HerdrWorktreeResult{}, markHerdrIntentManual(
			locked,
			intent,
			errors.Join(mutationErr, checkoutErr),
		)
	}
	if errors.Is(mutationErr, herdrrun.ErrMutationRejected) &&
		intent.Resource.WorkspaceID != "" && len(matches) == 0 {
		if _, verifyErr := worktree.VerifyHerdrCheckout(
			req.SourceRoot,
			intent.WorktreePath,
			intent.FullBranchRef,
			intent.ExpectedHead,
			source.RepoKey,
			source.RepoRoot,
		); verifyErr == nil {
			intent.Status = state.HerdrIntentRealized
			intent.Failure = ""
			locked.UpsertIntent(intent)
			if saveErr := locked.Save(); saveErr != nil {
				return HerdrWorktreeResult{}, errors.Join(mutationErr, saveErr)
			}
			return HerdrWorktreeResult{}, mutationErr
		}
	}
	if mutationErr == nil && intent.BranchCreated && len(matches) == 0 &&
		checkout.PathAbsent && !checkout.Registered {
		_, branchFound, branchErr := worktree.ObserveHerdrBranch(
			req.SourceRoot,
			intent.FullBranchRef,
		)
		if branchErr != nil {
			return HerdrWorktreeResult{}, markHerdrIntentManual(
				locked,
				intent,
				branchErr,
			)
		}
		if !branchFound {
			locked.RemoveIntent(intent.ID)
			if saveErr := locked.Save(); saveErr != nil {
				return HerdrWorktreeResult{}, saveErr
			}
			return HerdrWorktreeResult{}, fmt.Errorf(
				"recovered completed Herdr worktree rollback; retry launch",
			)
		}
	}
	if errors.Is(mutationErr, herdrrun.ErrMutationRejected) &&
		len(matches) == 0 && checkout.PathAbsent && !checkout.Registered {
		if intent.BranchCreated {
			if err := worktree.DeleteReservedHerdrBranch(
				req.SourceRoot,
				intent.FullBranchRef,
				intent.BaseSHA,
			); err != nil {
				return HerdrWorktreeResult{}, markHerdrIntentManual(
					locked,
					intent,
					errors.Join(mutationErr, err),
				)
			}
		}
		locked.RemoveIntent(intent.ID)
		if err := locked.Save(); err != nil {
			return HerdrWorktreeResult{}, errors.Join(mutationErr, err)
		}
		return HerdrWorktreeResult{}, mutationErr
	}
	return HerdrWorktreeResult{}, markHerdrIntentManual(
		locked,
		intent,
		errors.Join(
			mutationErr,
			fmt.Errorf(
				"herdr worktree recovery has %d label matches and checkout absent=%t registered=%t",
				len(matches),
				checkout.PathAbsent,
				checkout.Registered,
			),
		),
	)
}

func finalizeHerdrWorktree(
	locked *state.LockedHerdrControl,
	req HerdrWorktreeRequest,
	source worktree.HerdrRepoIdentity,
	intent *state.HerdrIntent,
	observation herdrrun.WorkspaceObservation,
) error {
	if err := validateWorktreeObservation(*intent, source, observation); err != nil {
		return err
	}
	if _, err := worktree.VerifyHerdrCheckout(
		req.SourceRoot,
		intent.WorktreePath,
		intent.FullBranchRef,
		intent.ExpectedHead,
		source.RepoKey,
		source.RepoRoot,
	); err != nil {
		return err
	}
	intent.Resource = stateResource(observation)
	intent.Status = state.HerdrIntentRealized
	intent.Failure = ""
	locked.UpsertIntent(*intent)
	return locked.Save()
}

func verifyRealizedCoordinator(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	intent state.HerdrIntent,
) error {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return err
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) != 1 || !workspaceHasHerdrResource(matches[0], intent.Resource) {
		return fmt.Errorf("realized Herdr coordinator identity changed")
	}
	return nil
}

func resumeRealizedHerdrWorktree(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedHerdrControl,
	req HerdrWorktreeRequest,
	source worktree.HerdrRepoIdentity,
	intent state.HerdrIntent,
	allowOpen bool,
) (HerdrWorktreeResult, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return HerdrWorktreeResult{}, errors.Join(err, contextErr)
		}
		return HerdrWorktreeResult{}, markHerdrIntentManual(locked, intent, err)
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	switch len(matches) {
	case 1:
		if !workspaceHasHerdrResource(matches[0], intent.Resource) {
			return HerdrWorktreeResult{}, markHerdrIntentManual(
				locked,
				intent,
				fmt.Errorf("realized Herdr worktree identity changed"),
			)
		}
		if _, err := worktree.VerifyHerdrCheckout(
			req.SourceRoot,
			intent.WorktreePath,
			intent.FullBranchRef,
			intent.ExpectedHead,
			source.RepoKey,
			source.RepoRoot,
		); err != nil {
			return HerdrWorktreeResult{}, markHerdrIntentManual(locked, intent, err)
		}
		return worktreeDeferred(intent)
	case 0:
	default:
		return HerdrWorktreeResult{}, markHerdrIntentManual(
			locked,
			intent,
			fmt.Errorf("realized Herdr worktree label has %d live matches", len(matches)),
		)
	}

	if _, err := worktree.VerifyHerdrCheckout(
		req.SourceRoot,
		intent.WorktreePath,
		intent.FullBranchRef,
		intent.ExpectedHead,
		source.RepoKey,
		source.RepoRoot,
	); err != nil {
		return HerdrWorktreeResult{}, markHerdrIntentManual(locked, intent, err)
	}
	if coordinatorErr := verifyCoordinatorObservation(intent.Coordinator, workspaces); coordinatorErr != nil {
		return HerdrWorktreeResult{}, markHerdrIntentManual(locked, intent, coordinatorErr)
	}
	if !allowOpen {
		return HerdrWorktreeResult{}, markHerdrIntentManual(
			locked,
			intent,
			fmt.Errorf("expired realized Herdr worktree has no live workspace"),
		)
	}
	if policyErr := runtime.VerifyWorktreeSetupPolicy(ctx); policyErr != nil {
		return HerdrWorktreeResult{}, policyErr
	}

	intent.Status = state.HerdrIntentIssued
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		return HerdrWorktreeResult{}, saveErr
	}
	mutation, mutationErr := runtime.MutateWorktree(ctx, herdrrun.WorktreeMutationRequest{
		Kind:                     herdrrun.WorktreeOpen,
		Coordinator:              observationResource(intent.Coordinator),
		SourceRoot:               source.RepoRoot,
		SourceRepoKey:            source.RepoKey,
		SourceRepoRoot:           source.RepoRoot,
		ProjectRoot:              req.ProjectRoot,
		FullBranchRef:            intent.FullBranchRef,
		ExpectedHeadSHA:          intent.ExpectedHead,
		Path:                     intent.WorktreePath,
		Label:                    intent.WorkspaceLabel,
		ExpectedAlreadyOpenID:    intent.Resource.WorkspaceID,
		ExpectedAlreadyOpenLabel: intent.Resource.Label,
		NoFocus:                  true,
	})
	if mutationErr != nil {
		if errors.Is(mutationErr, herdrrun.ErrMutationNotIssued) {
			intent.Status = state.HerdrIntentRealized
			locked.UpsertIntent(intent)
			if saveErr := locked.Save(); saveErr != nil {
				return HerdrWorktreeResult{}, errors.Join(mutationErr, saveErr)
			}
			return HerdrWorktreeResult{}, mutationErr
		}
		if operationErr := ctx.Err(); operationErr != nil {
			return HerdrWorktreeResult{}, errors.Join(mutationErr, operationErr)
		}
		return recoverHerdrWorktree(ctx, runtime, locked, req, source, intent, mutationErr)
	}
	if finalizeErr := finalizeHerdrWorktree(
		locked,
		req,
		source,
		&intent,
		mutation.WorkspaceObservation,
	); finalizeErr != nil {
		return HerdrWorktreeResult{}, markHerdrIntentManual(locked, intent, finalizeErr)
	}
	return worktreeDeferred(intent)
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
	)
	if err != nil {
		return err
	}
	if intent.ID != wantID || intent.Kind != state.HerdrIntentCoordinator ||
		intent.RuntimeParent != runtimeParent || intent.Backend != backend.Herdr ||
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
		intent.Backend != backend.Herdr ||
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
	if (req.IssueNum > 0) == (strings.TrimSpace(req.TaskID) != "") {
		return fmt.Errorf("herdr worktree request requires exactly one issue number or task id")
	}
	expected := filepath.Join(req.ProjectRoot, ".fanout", "worktrees", req.Slug)
	if filepath.Clean(req.WorktreePath) != filepath.Clean(expected) {
		return fmt.Errorf("herdr worktree path %s does not match slug %s", req.WorktreePath, req.Slug)
	}
	return nil
}

func markHerdrIntentManual(
	locked *state.LockedHerdrControl,
	intent state.HerdrIntent,
	cause error,
) error {
	reason := "result is indeterminate"
	if cause != nil {
		reason = cause.Error()
	}
	intent.Status = state.HerdrIntentManualCleanupRequired
	intent.Failure = reason
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return errors.Join(
			fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, reason),
			err,
		)
	}
	return fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, reason)
}

func herdrManualCleanupError(intent state.HerdrIntent) error {
	return fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, intent.Failure)
}

func coordinatorDeferred(intent state.HerdrIntent) (HerdrCoordinatorResult, error) {
	return HerdrCoordinatorResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
	}, ErrHerdrLauncherReadinessDeferred
}

func finalizedHerdrCoordinator(
	req HerdrCoordinatorRequest,
	cwd string,
	runtimeParent string,
	runtimeOwnerProjectRoot string,
	row state.HerdrRow,
) (HerdrCoordinatorResult, error) {
	wantID, err := state.HerdrCoordinatorIntentID(
		runtimeParent,
		runtimeOwnerProjectRoot,
	)
	if err != nil {
		return HerdrCoordinatorResult{}, err
	}
	if row.ID != wantID || row.Kind != state.HerdrIntentCoordinator ||
		row.RuntimeParent != runtimeParent ||
		!savedHerdrCoordinatorPathMatches(
			runtimeOwnerProjectRoot,
			row.WorktreePath,
			cwd,
		) ||
		row.Session != req.HerdrSession || row.SocketPath != req.SocketPath {
		return HerdrCoordinatorResult{}, fmt.Errorf("finalized herdr coordinator contradicts request")
	}
	return HerdrCoordinatorResult{
		Row:              row,
		Pane:             herdrResourcePane(row.Resource),
		AlreadyFinalized: true,
	}, nil
}

func finalizedHerdrWorktree(
	req HerdrWorktreeRequest,
	source worktree.HerdrRepoIdentity,
	ownerProjectRoot string,
	runtimeParent string,
	row state.HerdrRow,
) (HerdrWorktreeResult, error) {
	wantID, err := state.HerdrWorktreeIntentID(
		req.Parent,
		ownerProjectRoot,
		req.IssueNum,
		req.TaskID,
	)
	if err != nil {
		return HerdrWorktreeResult{}, err
	}
	if row.ID != wantID || row.Kind != state.HerdrIntentWorktree ||
		row.Parent != canonicalHerdrParent(req.Parent) ||
		row.RuntimeParent != runtimeParent ||
		row.OwnerProjectRoot != ownerProjectRoot ||
		row.IssueNum != req.IssueNum || row.TaskID != req.TaskID ||
		!savedHerdrWorktreePathValid(ownerProjectRoot, row.Slug, row.WorktreePath) ||
		!savedHerdrWorktreeRepoMatches(ownerProjectRoot, row.Resource, source) ||
		row.Session != req.HerdrSession || row.SocketPath != req.SocketPath {
		return HerdrWorktreeResult{}, fmt.Errorf("finalized herdr worktree contradicts request")
	}
	return HerdrWorktreeResult{
		Row:              row,
		Pane:             herdrResourcePane(row.Resource),
		AlreadyFinalized: true,
	}, nil
}

func resolvedHerdrCoordinator(
	locked *state.LockedHerdrControl,
	coordinatorID string,
	req HerdrWorktreeRequest,
	repoRoot string,
	runtimeParent string,
	runtimeOwnerProjectRoot string,
) (state.HerdrResource, error) {
	if intent, found := locked.FindIntent(coordinatorID); found {
		if intent.Status != state.HerdrIntentRealized {
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
	if row, found := locked.FindRow(coordinatorID); found {
		if row.Kind != state.HerdrIntentCoordinator ||
			row.RuntimeParent != runtimeParent ||
			row.Session != req.HerdrSession || row.SocketPath != req.SocketPath ||
			!savedHerdrCoordinatorPathMatches(
				runtimeOwnerProjectRoot,
				row.WorktreePath,
				repoRoot,
			) {
			return state.HerdrResource{}, fmt.Errorf("herdr coordinator row contradicts child request")
		}
		return row.Resource, nil
	}
	return state.HerdrResource{}, fmt.Errorf("herdr coordinator %s is not realized", coordinatorID)
}

func worktreeDeferred(intent state.HerdrIntent) (HerdrWorktreeResult, error) {
	return HerdrWorktreeResult{
		Intent: intent,
		Pane: backend.PaneRef{
			Backend: backend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID,
		},
	}, ErrHerdrLauncherReadinessDeferred
}

func herdrResourcePane(resource state.HerdrResource) backend.PaneRef {
	return backend.PaneRef{
		Backend: backend.Herdr, Workspace: resource.WorkspaceID, Pane: resource.PaneID,
	}
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
			timeout := min(
				time.Duration(intent.TimeoutMS)*time.Millisecond,
				maxHerdrRecoveryClassificationTimeout,
			)
			ctx, cancel := context.WithTimeout(parent, timeout)
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

func savedHerdrWorktreeProjectRoot(
	intent state.HerdrIntent,
	source worktree.HerdrRepoIdentity,
) (string, error) {
	savedPath := filepath.Clean(intent.WorktreePath)
	worktreesDir := filepath.Dir(savedPath)
	fanoutDir := filepath.Dir(worktreesDir)
	projectRoot := filepath.Dir(fanoutDir)
	if !filepath.IsAbs(savedPath) || filepath.Base(savedPath) != intent.Slug ||
		filepath.Base(worktreesDir) != "worktrees" || filepath.Base(fanoutDir) != ".fanout" {
		return "", fmt.Errorf("saved Herdr worktree path has no owner project root")
	}
	identity, err := worktree.ResolveHerdrRepoIdentity(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve saved Herdr worktree owner: %w", err)
	}
	if identity.RepoKey != source.RepoKey ||
		(intent.OwnerProjectRoot != "" && identity.RepoRoot != intent.OwnerProjectRoot) {
		return "", fmt.Errorf("saved Herdr worktree owner belongs to a different repository")
	}
	return projectRoot, nil
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
