package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
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
	ErrHerdrLauncherReadinessDeferred = errors.New("herdr launcher readiness is deferred to the agent-start phase")
	errHerdrIntentDeadlineExpired     = errors.New("herdr realization deadline expired")
	errHerdrRealizedIntentSave        = errors.New("save realized Herdr worktree intent")
	errHerdrRealizedIdentityChanged   = errors.New("realized Herdr identity changed")
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
	// Launch, when non-nil, is journaled before workspace creation so the pane
	// launcher can start a console or attached workload without an unfenced
	// post-create mutation window. Normal coordinators leave it nil and idle.
	Launch *state.HerdrLaunch
}

// HerdrRealizeResult is the realized outcome shared by the coordinator and
// worktree flows before the agent-start phase.
type HerdrRealizeResult struct {
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

// herdrRealizeSetup is the shared realize prologue: the bounded operation
// context, resolved repository identity, and the journal view read under the
// caller-held combined launch lock.
type herdrRealizeSetup struct {
	ctx                     context.Context
	deadline                time.Time
	source                  worktree.RepoIdentity
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
	source, sourceErr := worktree.ResolveRepoIdentity(realizeCtx, sourceRoot)
	if sourceErr != nil {
		return fail(sourceErr)
	}
	project, projectErr := worktree.ResolveRepoIdentity(realizeCtx, projectRoot)
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
) (result HerdrRealizeResult, retErr error) {
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
	if found && intent.Status == state.HerdrIntentManualCleanupRequired {
		// Terminal regardless of expiry: surface the saved failure.
		return result, herdrManualCleanupError(intent)
	}
	operationNow := setup.hooks.Now()
	routeCtx, operationParent, routeCancel, routeContextErr := herdrRealizeRouteContext(
		setup.ctx,
		intent,
		found,
		operationNow,
	)
	if routeContextErr != nil {
		// No mutation and no existence check happened yet; keep the intent so
		// the next run classifies it (canon: recovery on re-execution).
		return result, routeContextErr
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
			return result, contextErr
		}
		defer cancel()
		switch intent.Status {
		case state.HerdrIntentRealized:
			if verifyErr := verifyRealizedCoordinator(
				operationCtx,
				runtime,
				intent,
				setup.source,
			); verifyErr != nil {
				if errors.Is(verifyErr, errHerdrRealizedIdentityChanged) {
					return result, markHerdrIntentManual(locked, intent, verifyErr)
				}
				// The snapshot itself failed; nothing was classified, so the
				// realized intent stays retryable.
				return result, verifyErr
			}
			return realizeDeferred(intent)
		case state.HerdrIntentIssued:
			return recoverHerdrCoordinator(
				operationCtx,
				runtime,
				locked,
				intent,
				setup.source,
				nil,
			)
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
	label, labelErr := herdrCoordinatorWorkspaceLabel(req, setup.hooks.RandomToken)
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
		Launch:        cloneHerdrLaunch(req.Launch),
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
		// A structured rejection is a durable non-creation proof; classify it
		// even when the operation context has already expired.
		if operationErr := operationParent.Err(); operationErr != nil &&
			!errors.Is(mutationErr, herdrrun.ErrMutationRejected) {
			return result, errors.Join(mutationErr, operationErr)
		}
		return recoverHerdrCoordinator(
			operationParent,
			runtime,
			locked,
			intent,
			setup.source,
			mutationErr,
		)
	}
	if err := validateWorkspacePostcondition(intent, nil, mutation.WorkspaceObservation); err != nil {
		return result, markHerdrIntentManual(locked, intent, err)
	}
	intent.Resource = stateResource(mutation.WorkspaceObservation)
	intent.Status = state.HerdrIntentRealized
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return result, err
	}
	return realizeDeferred(intent)
}

func herdrCoordinatorWorkspaceLabel(
	req HerdrCoordinatorRequest,
	randomToken func() (string, error),
) (string, error) {
	if req.Launch == nil {
		return newHerdrWorkspaceLabel("coordinator", randomToken)
	}
	kind := "manual"
	if canonicalHerdrParent(req.Parent) == HerdrConsoleRuntimeParent {
		kind = "console"
	}
	return newHerdrWorkspaceLabel(kind, func() (string, error) {
		return req.Launch.Nonce, nil
	})
}

func cloneHerdrLaunch(launch *state.HerdrLaunch) *state.HerdrLaunch {
	if launch == nil {
		return nil
	}
	cloned := *launch
	cloned.Args = append([]string(nil), launch.Args...)
	return &cloned
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
