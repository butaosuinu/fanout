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
	if err := setup.resolveIdentity(realizeCtx, parent, projectRoot, sourceRoot, launchLock); err != nil {
		return fail(err)
	}
	return setup, cancel, nil
}

// resolveIdentity fills the repository, owner, journal, and runtime-parent
// identity every realize flow shares, under the caller-held launch lock.
func (setup *managedRealizeSetup) resolveIdentity(
	ctx context.Context,
	parent, projectRoot, sourceRoot string,
	launchLock *state.LockedStore,
) error {
	source, project, reposErr := resolveManagedRealizeRepos(ctx, projectRoot, sourceRoot)
	if reposErr != nil {
		return reposErr
	}
	setup.source = source
	ownerProjectRoot, ownerErr := state.IntentOwnerProjectRoot(parent, project.RepoRoot)
	if ownerErr != nil {
		return ownerErr
	}
	setup.ownerProjectRoot = ownerProjectRoot
	locked, lockedErr := launchLock.LaunchJournal(projectRoot)
	if lockedErr != nil {
		return lockedErr
	}
	setup.locked = locked
	runtimeParent, runtimeOwner, runtimeErr := resolveManagedRuntimeIdentity(
		projectRoot, parent, ownerProjectRoot, project.RepoRoot, locked,
	)
	if runtimeErr != nil {
		return runtimeErr
	}
	setup.runtimeParent = runtimeParent
	setup.runtimeOwnerProjectRoot = runtimeOwner
	return nil
}

// resolveManagedRealizeRepos resolves both realize roots and fails closed when
// they belong to different repositories.
func resolveManagedRealizeRepos(
	ctx context.Context,
	projectRoot, sourceRoot string,
) (worktree.RepoIdentity, worktree.RepoIdentity, error) {
	var none worktree.RepoIdentity
	source, sourceErr := worktree.ResolveRepoIdentity(ctx, sourceRoot)
	if sourceErr != nil {
		return none, none, sourceErr
	}
	project, projectErr := worktree.ResolveRepoIdentity(ctx, projectRoot)
	if projectErr != nil {
		return none, none, projectErr
	}
	if source.RepoKey != project.RepoKey {
		return none, none, fmt.Errorf("herdr project and source roots belong to different repositories")
	}
	return source, project, nil
}

// resolveManagedRuntimeIdentity picks the runtime parent the launch journal
// already agrees on and resolves the owner project root that follows from it.
func resolveManagedRuntimeIdentity(
	projectRoot, parent, ownerProjectRoot, repoRoot string,
	locked *state.LockedLaunchJournal,
) (string, string, error) {
	runtimeParent, runtimeParentErr := resolveManagedRuntimeParent(
		projectRoot,
		parent,
		ownerProjectRoot,
		managedCoordinatorIdentityIntents(parent, locked.LaunchJournal),
	)
	if runtimeParentErr != nil {
		return "", "", runtimeParentErr
	}
	runtimeOwnerProjectRoot, runtimeOwnerErr := state.IntentOwnerProjectRoot(
		runtimeParent,
		repoRoot,
	)
	if runtimeOwnerErr != nil {
		return "", "", runtimeOwnerErr
	}
	return runtimeParent, runtimeOwnerProjectRoot, nil
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
		ctx, req.Parent, req.ProjectRoot, req.SourceRoot, req.TotalTimeout, launchLock, hooks,
	)
	if setupErr != nil {
		return result, setupErr
	}
	defer realizeCancel()
	cwd, cwdErr := managedCoordinatorCWD(req, setup.source)
	if cwdErr != nil {
		return result, cwdErr
	}
	intentID, intentIDErr := state.CoordinatorIntentID(
		setup.runtimeParent,
		setup.runtimeOwnerProjectRoot,
		req.IssueNum,
	)
	if intentIDErr != nil {
		return result, intentIDErr
	}
	return realizeManagedCoordinatorIntent(setup, req, runtime, cwd, intentID)
}

// managedCoordinatorCWD canonicalizes the request cwd and fails closed when it
// is not the coordinator's source root.
func managedCoordinatorCWD(
	req ManagedCoordinatorRequest,
	source worktree.RepoIdentity,
) (string, error) {
	cwd, err := filepath.EvalSymlinks(req.CWD)
	if err != nil {
		return "", fmt.Errorf("canonicalize Herdr coordinator cwd: %w", err)
	}
	cwd = filepath.Clean(cwd)
	if cwd != source.RepoRoot {
		return "", fmt.Errorf("herdr coordinator cwd %s does not match source root %s", cwd, source.RepoRoot)
	}
	return cwd, nil
}

// realizeManagedCoordinatorIntent verifies the owned route and then either
// resumes the coordinator intent the journal already holds or creates a fresh
// one.
func realizeManagedCoordinatorIntent(
	setup managedRealizeSetup,
	req ManagedCoordinatorRequest,
	runtime ManagedWorktreeRuntime,
	cwd, intentID string,
) (result ManagedRealizeResult, retErr error) {
	intent, found := setup.locked.FindIntent(intentID)
	if found && intent.Status == state.IntentManualCleanupRequired {
		// Terminal regardless of expiry: surface the saved failure.
		return result, manualCleanupError(intent)
	}
	operationNow := setup.hooks.Now()
	routeCtx, operationParent, routeCancel, routeContextErr := managedRealizeRouteContext(
		setup.ctx, intent, found, operationNow,
	)
	if routeContextErr != nil {
		// No mutation and no existence check happened yet; keep the intent so
		// the next run classifies it (canon: recovery on re-execution).
		return result, routeContextErr
	}
	defer routeCancel()
	routeErr := verifyManagedRealizeRoute(
		routeCtx, runtime, setup.source.RepoKey, req.ManagedSession, req.SocketPath,
	)
	if routeErr != nil {
		return result, routeErr
	}
	if found {
		return resumeSavedManagedCoordinator(operationParent, runtime, setup, req, cwd, intent, operationNow)
	}
	return createManagedCoordinator(operationParent, runtime, setup, req, cwd, intentID)
}

// resumeSavedManagedCoordinator classifies a coordinator intent the journal
// already holds: adopt a realized workspace, recover an issued one, or fail
// closed on an unknown status.
func resumeSavedManagedCoordinator(
	operationParent context.Context,
	runtime ManagedWorktreeRuntime,
	setup managedRealizeSetup,
	req ManagedCoordinatorRequest,
	cwd string,
	intent state.LaunchIntent,
	operationNow time.Time,
) (result ManagedRealizeResult, retErr error) {
	locked := setup.locked
	runtimeParent := managedCoordinatorRequestRuntimeParent(req, setup.runtimeParent)
	savedErr := validateSavedCoordinatorIntent(req, cwd, runtimeParent, setup.runtimeOwnerProjectRoot, intent)
	if savedErr != nil {
		return result, savedErr
	}
	operationCtx, cancel, contextErr := managedIntentContext(operationParent, intent, operationNow)
	if contextErr != nil {
		return result, contextErr
	}
	defer cancel()
	switch intent.Status {
	case state.IntentRealized:
		return adoptRealizedManagedCoordinator(operationCtx, runtime, locked, intent, setup.source)
	case state.IntentIssued:
		return recoverManagedCoordinator(operationCtx, runtime, locked, intent, setup.source, nil)
	default:
		return result, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("unknown Herdr coordinator intent status %q", intent.Status),
		)
	}
}

// adoptRealizedManagedCoordinator re-verifies a realized coordinator workspace
// and hands it to the agent-start phase.
func adoptRealizedManagedCoordinator(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	source worktree.RepoIdentity,
) (ManagedRealizeResult, error) {
	if verifyErr := verifyRealizedCoordinator(ctx, runtime, intent, source); verifyErr != nil {
		if errors.Is(verifyErr, errManagedRealizedIdentityChanged) {
			return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, verifyErr)
		}
		// The snapshot itself failed; nothing was classified, so the
		// realized intent stays retryable.
		return ManagedRealizeResult{}, verifyErr
	}
	return realizeDeferred(intent)
}

// createManagedCoordinator journals a fresh issued intent and performs the one
// workspace create it fences.
func createManagedCoordinator(
	operationParent context.Context,
	runtime ManagedWorktreeRuntime,
	setup managedRealizeSetup,
	req ManagedCoordinatorRequest,
	cwd, intentID string,
) (result ManagedRealizeResult, retErr error) {
	locked := setup.locked
	if policyErr := runtime.VerifyWorktreeSetupPolicy(operationParent); policyErr != nil {
		return result, policyErr
	}
	intent, intentErr := newManagedCoordinatorIntent(setup, req, cwd, intentID)
	if intentErr != nil {
		return result, intentErr
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
		return classifyManagedCoordinatorCreateError(
			operationParent, runtime, locked, intent, setup.source, mutationErr,
		)
	}
	return finalizeManagedCoordinator(locked, intent, mutation.WorkspaceObservation)
}

// newManagedCoordinatorIntent builds the issued intent journaled immediately
// before the coordinator workspace create.
func newManagedCoordinatorIntent(
	setup managedRealizeSetup,
	req ManagedCoordinatorRequest,
	cwd, intentID string,
) (state.LaunchIntent, error) {
	label, labelErr := managedCoordinatorWorkspaceLabel(req, setup.hooks.RandomToken)
	if labelErr != nil {
		return state.LaunchIntent{}, labelErr
	}
	return state.LaunchIntent{
		ID: intentID, Kind: state.IntentCoordinator, Status: state.IntentIssued,
		Parent:           canonicalManagedParent(req.Parent),
		RuntimeParent:    managedCoordinatorRequestRuntimeParent(req, setup.runtimeParent),
		OwnerProjectRoot: setup.ownerProjectRoot,
		IssueNum:         req.IssueNum,
		WorktreePath:     cwd,
		WorkspaceLabel:   label, Session: req.ManagedSession, SocketPath: req.SocketPath,
		ExpiresUnixMS: setup.deadline.UnixMilli(),
		Launch:        cloneManagedLaunch(req.Launch),
	}, nil
}

// classifyManagedCoordinatorCreateError routes a failed coordinator create:
// release on proven non-issuance, keep an unclassified expiry retryable, and
// otherwise recover from the observed label.
func classifyManagedCoordinatorCreateError(
	operationParent context.Context,
	runtime ManagedWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	source worktree.RepoIdentity,
	mutationErr error,
) (ManagedRealizeResult, error) {
	if errors.Is(mutationErr, backend.ErrMutationNotIssued) {
		return ManagedRealizeResult{}, releaseManagedIntent(locked, intent.ID, mutationErr)
	}
	if expiryErr := unclassifiedManagedMutationExpiry(operationParent, mutationErr); expiryErr != nil {
		return ManagedRealizeResult{}, expiryErr
	}
	return recoverManagedCoordinator(operationParent, runtime, locked, intent, source, mutationErr)
}

// finalizeManagedCoordinator records the created workspace as the realized
// coordinator, or fails closed when the postcondition does not match.
func finalizeManagedCoordinator(
	locked *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	observation backend.WorkspaceObservation,
) (ManagedRealizeResult, error) {
	if err := validateWorkspacePostcondition(intent, nil, observation); err != nil {
		return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, err)
	}
	intent.Resource = stateResource(observation)
	intent.Status = state.IntentRealized
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return ManagedRealizeResult{}, err
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
	saved, savedErr := savedManagedRuntimeParent(parent, ownerProjectRoot, control)
	if savedErr != nil {
		return "", savedErr
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

// savedManagedRuntimeParent returns the runtime parent the journal already
// agrees on for this parent and owner, "" when none is recorded, and an error
// when saved intents disagree.
func savedManagedRuntimeParent(
	parent, ownerProjectRoot string,
	control state.LaunchJournal,
) (string, error) {
	saved := ""
	for _, intent := range control.Intents {
		if canonicalManagedParent(intent.Parent) != parent {
			continue
		}
		if filepath.Clean(intent.OwnerProjectRoot) != filepath.Clean(ownerProjectRoot) {
			continue
		}
		runtimeParent := canonicalManagedParent(intent.RuntimeParent)
		if saved != "" && saved != runtimeParent {
			return "", fmt.Errorf(
				"saved Herdr runtime parents for %s disagree: %s and %s",
				parent,
				saved,
				runtimeParent,
			)
		}
		saved = runtimeParent
	}
	return saved, nil
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
