package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const (
	minManagedRealizeTimeout                = 3 * time.Second
	maxManagedRealizeTimeout                = 300 * time.Second
	maxManagedRecoveryClassificationTimeout = 5 * time.Second
)

var (
	ErrManualCleanupRequired            = errors.New("herdr launch requires manual cleanup")
	ErrManagedLauncherReadinessDeferred = errors.New("herdr launcher readiness is deferred to the agent-start phase")
	errManagedIntentDeadlineExpired     = errors.New("herdr realization deadline expired")
	errManagedRealizedIntentSave        = errors.New("save realized Herdr worktree intent")
	errManagedRealizedIdentityChanged   = errors.New("realized Herdr identity changed")
)

type ManagedWorktreeRuntime interface {
	WorktreeRoute(context.Context) (backend.OwnedWorktreeRoute, error)
	VerifyWorktreeSetupPolicy(context.Context) error
	ObserveWorkspaces(context.Context) ([]backend.WorkspaceObservation, error)
	CreateWorkspace(
		context.Context,
		backend.WorkspaceCreateRequest,
	) (backend.WorktreeMutationResult, error)
	CreateWorktree(
		context.Context,
		backend.WorktreeCreateRequest,
	) (backend.WorktreeMutationResult, error)
	OpenWorktree(
		context.Context,
		backend.WorktreeOpenRequest,
	) (backend.WorktreeMutationResult, error)
	// DiscardWorkloadEnvironment drops a capsule the realization never
	// published, so every rollback path reaches it through this surface.
	DiscardWorkloadEnvironment(string, *state.LaunchCapsule) error
}

type ManagedRealizeHooks struct {
	Now         func() time.Time
	RandomToken func() (string, error)
}

type ManagedCoordinatorRequest struct {
	Parent         string
	RuntimeParent  string
	IssueNum       int
	ProjectRoot    string
	SourceRoot     string
	CWD            string
	ManagedSession string
	SocketPath     string
	TotalTimeout   time.Duration
	// Launch, when non-nil, is journaled before workspace creation so the pane
	// launcher can start a console or attached workload without an unfenced
	// post-create mutation window. Normal coordinators leave it nil and idle.
	Launch *state.LaunchCapsule
}

// ManagedRealizeResult is the realized outcome shared by the coordinator and
// worktree flows before the agent-start phase.
type ManagedRealizeResult struct {
	Intent state.LaunchIntent
	Pane   backend.PaneRef
}

type ManagedWorktreeRequest struct {
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
	ManagedSession     string
	SocketPath         string
	TotalTimeout       time.Duration
}

// managedRealizeSetup is the shared realize prologue: the bounded operation
// context, resolved repository identity, and the journal view read under the
// caller-held combined launch lock.
type managedRealizeSetup struct {
	ctx                     context.Context
	deadline                time.Time
	source                  worktree.RepoIdentity
	ownerProjectRoot        string
	runtimeParent           string
	runtimeOwnerProjectRoot string
	locked                  *state.LockedLaunchJournal
	hooks                   ManagedRealizeHooks
}

func newManagedRealizeSetup(
	ctx context.Context,
	parent, projectRoot, sourceRoot string,
	totalTimeout time.Duration,
	launchLock *state.LockedStore,
	hooks ManagedRealizeHooks,
) (managedRealizeSetup, context.CancelFunc, error) {
	setup := managedRealizeSetup{hooks: normalizeManagedRealizeHooks(hooks)}
	timeout, timeoutErr := normalizeManagedRealizeTimeout(totalTimeout)
	if timeoutErr != nil {
		return setup, nil, timeoutErr
	}
	realizeCtx, cancel := context.WithTimeout(ctx, timeout)
	fail := func(err error) (managedRealizeSetup, context.CancelFunc, error) {
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
	ownerProjectRoot, ownerErr := state.IntentOwnerProjectRoot(parent, project.RepoRoot)
	if ownerErr != nil {
		return fail(ownerErr)
	}
	setup.ownerProjectRoot = ownerProjectRoot
	locked, lockedErr := launchLock.LaunchJournal(projectRoot)
	if lockedErr != nil {
		return fail(lockedErr)
	}
	setup.locked = locked
	runtimeParent, runtimeParentErr := resolveManagedRuntimeParent(
		projectRoot,
		parent,
		ownerProjectRoot,
		managedCoordinatorIdentityIntents(parent, locked.LaunchJournal),
	)
	if runtimeParentErr != nil {
		return fail(runtimeParentErr)
	}
	setup.runtimeParent = runtimeParent
	runtimeOwnerProjectRoot, runtimeOwnerErr := state.IntentOwnerProjectRoot(
		runtimeParent,
		project.RepoRoot,
	)
	if runtimeOwnerErr != nil {
		return fail(runtimeOwnerErr)
	}
	setup.runtimeOwnerProjectRoot = runtimeOwnerProjectRoot
	return setup, cancel, nil
}

// RealizeManagedCoordinator creates or recovers the actual-parent coordinator
// workspace under the caller-held combined launch lock. It stops with a
// durable realized intent before launcher work. The coordinator mutation has
// no non-issuance proof, so there is no planned stage: the intent is saved as
// issued immediately before the one workspace create.
func RealizeManagedCoordinator(
	ctx context.Context,
	req ManagedCoordinatorRequest,
	runtime ManagedWorktreeRuntime,
	launchLock *state.LockedStore,
	hooks ManagedRealizeHooks,
) (result ManagedRealizeResult, retErr error) {
	if ctx == nil || runtime == nil || launchLock == nil {
		return result, fmt.Errorf("realize Herdr coordinator requires context, runtime, and launch lock")
	}
	if validateErr := validateManagedCoordinatorRequest(req); validateErr != nil {
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
	cwd, cwdErr := filepath.EvalSymlinks(req.CWD)
	if cwdErr != nil {
		return result, fmt.Errorf("canonicalize Herdr coordinator cwd: %w", cwdErr)
	}
	cwd = filepath.Clean(cwd)
	if cwd != setup.source.RepoRoot {
		return result, fmt.Errorf("herdr coordinator cwd %s does not match source root %s", cwd, setup.source.RepoRoot)
	}
	intentID, intentIDErr := state.CoordinatorIntentID(
		setup.runtimeParent,
		setup.runtimeOwnerProjectRoot,
		req.IssueNum,
	)
	if intentIDErr != nil {
		return result, intentIDErr
	}

	intent, found := locked.FindIntent(intentID)
	if found && intent.Status == state.IntentManualCleanupRequired {
		// Terminal regardless of expiry: surface the saved failure.
		return result, manualCleanupError(intent)
	}
	operationNow := setup.hooks.Now()
	routeCtx, operationParent, routeCancel, routeContextErr := managedRealizeRouteContext(
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
	if routeErr := verifyManagedRealizeRoute(
		routeCtx,
		runtime,
		setup.source.RepoKey,
		req.ManagedSession,
		req.SocketPath,
	); routeErr != nil {
		return result, routeErr
	}
	if found {
		if savedErr := validateSavedCoordinatorIntent(
			req,
			cwd,
			managedCoordinatorRequestRuntimeParent(req, setup.runtimeParent),
			setup.runtimeOwnerProjectRoot,
			intent,
		); savedErr != nil {
			return result, savedErr
		}
		operationCtx, cancel, contextErr := managedIntentContext(operationParent, intent, operationNow)
		if contextErr != nil {
			return result, contextErr
		}
		defer cancel()
		switch intent.Status {
		case state.IntentRealized:
			if verifyErr := verifyRealizedCoordinator(
				operationCtx,
				runtime,
				intent,
				setup.source,
			); verifyErr != nil {
				if errors.Is(verifyErr, errManagedRealizedIdentityChanged) {
					return result, markManagedIntentManual(locked, intent, verifyErr)
				}
				// The snapshot itself failed; nothing was classified, so the
				// realized intent stays retryable.
				return result, verifyErr
			}
			return realizeDeferred(intent)
		case state.IntentIssued:
			return recoverManagedCoordinator(
				operationCtx,
				runtime,
				locked,
				intent,
				setup.source,
				nil,
			)
		default:
			return result, markManagedIntentManual(
				locked,
				intent,
				fmt.Errorf("unknown Herdr coordinator intent status %q", intent.Status),
			)
		}
	}

	if policyErr := runtime.VerifyWorktreeSetupPolicy(operationParent); policyErr != nil {
		return result, policyErr
	}
	label, labelErr := managedCoordinatorWorkspaceLabel(req, setup.hooks.RandomToken)
	if labelErr != nil {
		return result, labelErr
	}
	intent = state.LaunchIntent{
		ID: intentID, Kind: state.IntentCoordinator, Status: state.IntentIssued,
		Parent:           canonicalManagedParent(req.Parent),
		RuntimeParent:    managedCoordinatorRequestRuntimeParent(req, setup.runtimeParent),
		OwnerProjectRoot: setup.ownerProjectRoot,
		IssueNum:         req.IssueNum,
		WorktreePath:     cwd,
		WorkspaceLabel:   label, Session: req.ManagedSession, SocketPath: req.SocketPath,
		ExpiresUnixMS: setup.deadline.UnixMilli(),
		Launch:        cloneManagedLaunch(req.Launch),
	}
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		return result, saveErr
	}
	mutation, mutationErr := runtime.CreateWorkspace(operationParent, backend.WorkspaceCreateRequest{
		CWD:           intent.WorktreePath,
		SourceRepoKey: setup.source.RepoKey,
		Label:         intent.WorkspaceLabel,
	})
	if mutationErr != nil {
		if errors.Is(mutationErr, backend.ErrMutationNotIssued) {
			return result, releaseManagedIntent(locked, intent.ID, mutationErr)
		}
		// A structured rejection is a durable non-creation proof; classify it
		// even when the operation context has already expired.
		if operationErr := operationParent.Err(); operationErr != nil &&
			!errors.Is(mutationErr, backend.ErrMutationRejected) {
			return result, errors.Join(mutationErr, operationErr)
		}
		return recoverManagedCoordinator(
			operationParent,
			runtime,
			locked,
			intent,
			setup.source,
			mutationErr,
		)
	}
	if err := validateWorkspacePostcondition(intent, nil, mutation.WorkspaceObservation); err != nil {
		return result, markManagedIntentManual(locked, intent, err)
	}
	intent.Resource = stateResource(mutation.WorkspaceObservation)
	intent.Status = state.IntentRealized
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return result, err
	}
	return realizeDeferred(intent)
}

func managedCoordinatorWorkspaceLabel(
	req ManagedCoordinatorRequest,
	randomToken func() (string, error),
) (string, error) {
	if req.Launch == nil {
		return newManagedWorkspaceLabel("coordinator", randomToken)
	}
	kind := "manual"
	if canonicalManagedParent(req.Parent) == ManagedConsoleRuntimeParent {
		kind = "console"
	}
	return newManagedWorkspaceLabel(kind, func() (string, error) {
		return req.Launch.Nonce, nil
	})
}

func cloneManagedLaunch(launch *state.LaunchCapsule) *state.LaunchCapsule {
	if launch == nil {
		return nil
	}
	cloned := *launch
	cloned.Args = append([]string(nil), launch.Args...)
	return &cloned
}

func verifyManagedRealizeRoute(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
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

func managedRealizeRouteContext(
	parent context.Context,
	intent state.LaunchIntent,
	found bool,
	now time.Time,
) (context.Context, context.Context, context.CancelFunc, error) {
	operationParent := parent
	operationCancel := func() {}
	if found {
		var err error
		operationParent, operationCancel, err = managedIntentContext(parent, intent, now)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	ctx, cancel := context.WithTimeout(operationParent, maxManagedRecoveryClassificationTimeout)
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

func resolveManagedRuntimeParent(
	projectRoot, parent, ownerProjectRoot string,
	control state.LaunchJournal,
) (string, error) {
	parent = canonicalManagedParent(parent)
	saved := ""
	observe := func(savedParent, savedRuntimeParent, savedOwnerProjectRoot string) error {
		if canonicalManagedParent(savedParent) != parent {
			return nil
		}
		if filepath.Clean(savedOwnerProjectRoot) != filepath.Clean(ownerProjectRoot) {
			return nil
		}
		runtimeParent := canonicalManagedParent(savedRuntimeParent)
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
		parent = canonicalManagedParent(SavedPlanRuntimeParentRef(projectRoot, planSlug))
	}
	if parent == "" {
		return "", fmt.Errorf("resolve Herdr runtime parent: empty parent")
	}
	return parent, nil
}

func managedCoordinatorIdentityIntents(parent string, control state.LaunchJournal) state.LaunchJournal {
	if canonicalManagedParent(parent) != ManualParentRef {
		return control
	}
	identity := control
	identity.Intents = slices.DeleteFunc(slices.Clone(control.Intents), func(intent state.LaunchIntent) bool {
		return intent.Parent == ManualParentRef && intent.RuntimeParent != ManualParentRef
	})
	return identity
}

func managedCoordinatorRequestRuntimeParent(req ManagedCoordinatorRequest, fallback string) string {
	if req.RuntimeParent != "" {
		return canonicalManagedParent(req.RuntimeParent)
	}
	return fallback
}

func normalizeManagedRealizeHooks(hooks ManagedRealizeHooks) ManagedRealizeHooks {
	if hooks.Now == nil {
		hooks.Now = time.Now
	}
	if hooks.RandomToken == nil {
		hooks.RandomToken = randomManagedToken
	}
	return hooks
}

func normalizeManagedRealizeTimeout(timeout time.Duration) (time.Duration, error) {
	if timeout == 0 {
		return maxManagedRealizeTimeout, nil
	}
	if timeout < minManagedRealizeTimeout || timeout > maxManagedRealizeTimeout ||
		timeout%time.Millisecond != 0 {
		return 0, fmt.Errorf("herdr realization timeout must be 3s..300s at millisecond precision")
	}
	return timeout, nil
}

func managedIntentContext(
	parent context.Context,
	intent state.LaunchIntent,
	now time.Time,
) (context.Context, context.CancelFunc, error) {
	deadline := time.UnixMilli(intent.ExpiresUnixMS)
	if !now.Before(deadline) {
		switch intent.Status {
		case state.IntentPlanned:
			return nil, nil, errManagedIntentDeadlineExpired
		case state.IntentIssued, state.IntentRealized:
			// An expired launch never receives another full total_timeout.
			// Bound this invocation to a short existence classification.
			ctx, cancel := context.WithTimeout(parent, maxManagedRecoveryClassificationTimeout)
			if err := ctx.Err(); err != nil {
				cancel()
				return nil, nil, err
			}
			return ctx, cancel, nil
		default:
			return nil, nil, errManagedIntentDeadlineExpired
		}
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}
