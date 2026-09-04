package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const workspaceCleanupTimeout = 5 * time.Minute

const (
	sharedAttachedWorkspaceCloseFailure  = "shared attached workspace close pending"
	sharedAttachedWorkspaceCloseComplete = "shared attached workspace close complete"
	sharedAttachedWorkspaceHookPrefix    = "cleanup:attached:"
	workspacePreHookIdentityFailure      = "before_worktree_remove identity preflight failed"
)

var (
	ErrManualCleanupRequired               = errors.New("herdr lifecycle requires manual cleanup")
	errSharedAttachedHookDispatchUncertain = errors.New("shared attached workspace hook dispatch outcome is uncertain")
)

// WorkspaceRuntime is the mutation surface lifecycle needs from one existing owned
// session. The composition root supplies a route-bound implementation.
type WorkspaceRuntime interface {
	panelaunch.ManagedWorktreeRuntime
	VerifyOwned(context.Context) error
	RemoveWorktree(context.Context, string, string) error
	CloseWorkspace(context.Context, string) error
	CloseAttachedWorkspace(context.Context, backend.PaneBinding) error
}

type WorkspaceRuntimeFactory func(context.Context, state.Pane) (WorkspaceRuntime, error)

func validateWorkspaceMergeOperation(opts Options, pane state.Pane) error {
	if !workspaceRuntimeRow(pane) {
		return nil
	}
	if err := validateWorkspacePaneIdentity(pane); err != nil {
		return err
	}
	if opts.WorkspaceRuntime == nil {
		return fmt.Errorf("herdr lifecycle runtime is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	runtime, err := opts.WorkspaceRuntime(ctx, pane)
	if err != nil {
		return err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return verifyErr
	}
	return verifyWorkspaceMergeTarget(ctx, opts.ProjectRoot, runtime, pane)
}

func verifyWorkspaceMergeTarget(ctx context.Context, projectRoot string, runtime WorkspaceRuntime, pane state.Pane) error {
	resource := resourceFromPane(pane)
	observation, err := observeWorkspaceCleanup(ctx, runtime, projectRoot, resource)
	if err != nil {
		return err
	}
	if observation.checkout.PathAbsent || !observation.checkout.Registered {
		return fmt.Errorf("saved Herdr merge checkout is absent or unregistered")
	}
	if observation.workspace != nil {
		if terminalErr := verifyTerminalInvalidation(*observation.workspace, resource); terminalErr != nil {
			return terminalErr
		}
	}
	fullRef, err := worktree.LocalBranchRef(ctx, projectRoot, pane.BranchName)
	if err != nil {
		return err
	}
	expectedHead, found, err := worktree.ObserveBranch(ctx, projectRoot, fullRef)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("saved Herdr merge branch is absent")
	}
	return verifyCleanupCheckout(ctx, projectRoot, fullRef, expectedHead, resource)
}

func validateWorkspaceCloseOperation(opts Options, pane state.Pane, mode CloseMode, lg Logger) bool {
	switch {
	case !mode.removesWorktree():
		lg.Err("%s: Herdr child close must keep lifecycle ownership by removing its worktree", paneLabel(pane))
		return false
	case pane.IsShell() || pane.IsAttachedAgent():
		lg.Err("%s: Herdr lifecycle close supports owned child worktrees only", paneLabel(pane))
		return false
	case opts.WorkspaceRuntime == nil:
		lg.Err("%s: Herdr lifecycle runtime is not configured", paneLabel(pane))
		return false
	case validateWorkspacePaneIdentity(pane) != nil:
		lg.Err("%s: saved Herdr lifecycle identity is incomplete; preserving workspace, worktree, and state", paneLabel(pane))
		return false
	}
	if err := verifyWorkspaceClosePreflight(opts, pane, mode); err != nil {
		lg.Err("%s: Herdr lifecycle preflight failed; preserving workspace, worktree, and state: %v", paneLabel(pane), err)
		return false
	}
	return true
}

func verifyWorkspaceClosePreflight(opts Options, pane state.Pane, mode CloseMode) error {
	_, err := inspectWorkspaceClosePreflight(opts, pane, mode, nil)
	return err
}

func inspectWorkspaceClosePreflight(
	opts Options,
	pane state.Pane,
	mode CloseMode,
	attached []state.Pane,
) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	runtime, err := opts.WorkspaceRuntime(ctx, pane)
	if err != nil {
		return false, err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return false, verifyErr
	}
	resource, predicate, reopened, cleanupStarted, err := workspaceClosePreflightIdentity(opts, pane, mode)
	if err != nil {
		return false, err
	}
	if len(attached) > 0 {
		predicate = sharedChildWorkspacePredicate(resource, attached)
	}
	if _, err := verifyWorkspaceCloseTarget(ctx, opts.ProjectRoot, runtime, pane, resource, predicate, reopened); err != nil {
		return false, err
	}
	return cleanupStarted, nil
}

func prepareWorkspaceCleanupHook(opts Options, locked *state.LockedStore, pane state.Pane, mode CloseMode) (*state.LockedLaunchJournal, state.LaunchIntent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	runtime, err := opts.WorkspaceRuntime(ctx, pane)
	if err != nil {
		return nil, state.LaunchIntent{}, err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return nil, state.LaunchIntent{}, verifyErr
	}
	resource := resourceFromPane(pane)
	attached := sharedAttachedWorkspaceRows(locked.Panes, pane.WorktreePath)
	predicate := workspacePredicate(resource)
	if len(attached) > 0 {
		predicate = sharedChildWorkspacePredicate(resource, attached)
	}
	journal, intent, _, err := loadWorkspaceCleanupIntent(ctx, opts, locked, runtime, pane, mode, predicate)
	if err != nil {
		return nil, state.LaunchIntent{}, err
	}
	intent, err = verifyWorkspaceCleanupHookIdentity(
		ctx, opts, locked, journal, runtime, pane, attached, intent,
	)
	if err != nil {
		return nil, state.LaunchIntent{}, err
	}
	return journal, intent, nil
}

func verifyWorkspaceCleanupHookIdentity(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	attached []state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	predicate := workspaceLabelPredicate(
		intent.WorkspaceLabel, intent.WorktreePath, intent.Resource.RepoKey, intent.Resource.RepoRoot,
	)
	if len(attached) > 0 {
		predicate = sharedChildWorkspacePredicate(intent.Resource, attached)
	}
	reopened := intent.Status == state.IntentIssued && intent.CleanupPhase == state.CleanupReopen
	observation, err := verifyWorkspaceCloseTarget(
		ctx, opts.ProjectRoot, runtime, pane, intent.Resource, predicate, reopened,
	)
	if err != nil {
		return intent, err
	}
	return rebindWorkspaceCleanupHookIdentity(
		locked, journal, opts.ProjectRoot, pane, intent, observation.workspace,
	)
}

func rebindWorkspaceCleanupHookIdentity(
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	projectRoot string,
	pane state.Pane,
	intent state.LaunchIntent,
	workspace *backend.WorkspaceObservation,
) (state.LaunchIntent, error) {
	previousResource := intent.Resource
	intent, err := rebindObservedWorkspaceCleanupIdentity(
		locked, journal, projectRoot, pane, intent, workspace,
	)
	if err != nil || intent.Resource == previousResource {
		return intent, err
	}
	return intent, saveWorkspaceCleanupIntent(journal, intent)
}

func verifyWorkspaceCloseTarget(
	ctx context.Context,
	projectRoot string,
	runtime WorkspaceRuntime,
	pane state.Pane,
	resource state.RuntimeResource,
	predicate workspacePredicateFunc,
	reopened bool,
) (workspaceCleanupObservation, error) {
	observation, err := observeWorkspaceCleanupMatching(ctx, runtime, projectRoot, resource, predicate)
	if err != nil {
		return workspaceCleanupObservation{}, err
	}
	if observation.workspace != nil && !reopened {
		if terminalErr := verifyTerminalInvalidation(*observation.workspace, resource); terminalErr != nil {
			return workspaceCleanupObservation{}, terminalErr
		}
	}
	if observation.checkout.PathAbsent && !observation.checkout.Registered {
		return observation, nil
	}
	fullRef, err := worktree.LocalBranchRef(ctx, projectRoot, pane.BranchName)
	if err != nil {
		return workspaceCleanupObservation{}, err
	}
	if err := verifyCleanupCheckout(ctx, projectRoot, fullRef, observation.checkout.HeadSHA, resource); err != nil {
		return workspaceCleanupObservation{}, err
	}
	return observation, nil
}

func workspaceClosePreflightIdentity(
	opts Options,
	pane state.Pane,
	mode CloseMode,
) (state.RuntimeResource, workspacePredicateFunc, bool, bool, error) {
	resource := resourceFromPane(pane)
	journal, err := state.LoadLaunchJournal(opts.ProjectRoot)
	if err != nil {
		return state.RuntimeResource{}, nil, false, false, err
	}
	_, cleanupID, err := workspaceCleanupIntentIDs(opts.ProjectRoot, pane)
	if err != nil {
		return state.RuntimeResource{}, nil, false, false, err
	}
	intent, found := journal.FindIntent(cleanupID)
	if !found {
		return resource, workspacePredicate(resource), false, false, nil
	}
	if err := validateSavedWorkspaceCleanup(intent, opts.ProjectRoot, pane, mode); err != nil {
		return state.RuntimeResource{}, nil, false, false, err
	}
	resource = intent.Resource
	reopened := intent.Status == state.IntentIssued && intent.CleanupPhase == state.CleanupReopen
	predicate := workspaceLabelPredicate(
		intent.WorkspaceLabel,
		intent.WorktreePath,
		intent.Resource.RepoKey,
		intent.Resource.RepoRoot,
	)
	return resource, predicate, reopened, !workspacePreHookIdentityFence(intent), nil
}

func closeWorkspaceWorktree(
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	mode CloseMode,
	lg Logger,
) bool {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	if err := runWorkspaceCleanup(ctx, opts, locked, pane, mode, lg); err != nil {
		lg.Err("%s: Herdr worktree cleanup failed; preserving state: %v", paneLabel(pane), err)
		return false
	}
	return true
}

func runWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	mode CloseMode,
	lg Logger,
) error {
	runtime, err := opts.WorkspaceRuntime(ctx, pane)
	if err != nil {
		return err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return verifyErr
	}
	journal, intent, worktreeIntentID, err := loadWorkspaceCleanupIntent(
		ctx, opts, locked, runtime, pane, mode, workspacePredicate(resourceFromPane(pane)),
	)
	if err != nil {
		return err
	}
	return driveWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent, worktreeIntentID, lg)
}

func closeSharedAttachedWorkspaceRows(
	opts Options,
	locked *state.LockedStore,
	panes []state.Pane,
	mode CloseMode,
	lg Logger,
) bool {
	if !mode.removesWorktree() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	for _, pane := range panes {
		if !workspaceRuntimeRow(pane) || pane.IsShell() || pane.IsAttachedAgent() {
			continue
		}
		if err := closeSharedAttachedWorkspaces(ctx, opts, locked, pane, mode, lg); err != nil {
			lg.Err("%s: shared attached workspace close failed; preserving child worktree: %v", paneLabel(pane), err)
			return false
		}
	}
	return true
}

func closeSharedAttachedWorkspaces(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	mode CloseMode,
	lg Logger,
) error {
	attached := sharedAttachedWorkspaceRows(locked.Panes, child.WorktreePath)
	if len(attached) == 0 {
		journal, err := locked.LaunchJournal(opts.ProjectRoot)
		if err != nil {
			return err
		}
		return retireSharedAttachedWorkspaceClose(ctx, opts, locked, journal, child, mode)
	}
	journal, intent, issueClose, err := prepareSharedAttachedWorkspaceClose(
		ctx, opts, locked, child, attached, mode,
	)
	if err != nil {
		return err
	}
	targets, err := inspectSharedAttachedWorkspaces(ctx, opts, locked, child, attached)
	if err != nil {
		if errors.Is(err, backend.ErrOwnedIdentityMismatch) {
			return markSharedAttachedWorkspaceCloseManual(journal, intent, err)
		}
		return err
	}
	return driveSharedAttachedWorkspaceCloses(
		ctx, opts, locked, journal, intent, child, attached, targets, issueClose, lg,
	)
}

func retireSharedAttachedWorkspaceClose(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	child state.Pane,
	mode CloseMode,
) error {
	_, cleanupID, err := workspaceCleanupIntentIDs(opts.ProjectRoot, child)
	if err != nil {
		return err
	}
	intent, found := journal.FindIntent(cleanupID)
	if !found || intent.Status != state.IntentManualCleanupRequired ||
		intent.Failure != sharedAttachedWorkspaceCloseFailure {
		return nil
	}
	if err := validateSavedWorkspaceCleanup(intent, opts.ProjectRoot, child, mode); err != nil {
		return err
	}
	return completeSharedAttachedWorkspaceClose(ctx, opts, locked, journal, child, nil, intent)
}

func driveSharedAttachedWorkspaceCloses(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	child state.Pane,
	attached []state.Pane,
	targets []sharedAttachedWorkspaceCloseTarget,
	issueClose bool,
	lg Logger,
) error {
	intent, err := beginVerifiedSharedAttachedWorkspaceClose(
		ctx, opts, locked, journal, child, attached, intent, issueClose,
	)
	if err != nil {
		return err
	}
	for _, target := range targets {
		issued, err := closeSharedAttachedWorkspace(ctx, opts, locked, journal, target, issueClose, lg)
		if err != nil {
			return handleSharedAttachedWorkspaceCloseError(journal, intent, issueClose, issued, err)
		}
	}
	return completeSharedAttachedWorkspaceClose(ctx, opts, locked, journal, child, attached, intent)
}

func handleSharedAttachedWorkspaceCloseError(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	issueClose, issued bool,
	err error,
) error {
	manual := errors.Is(err, backend.ErrOwnedIdentityMismatch) ||
		errors.Is(err, backend.ErrOwnedWorkspaceHasUnadmittedPane)
	if manual {
		return markSharedAttachedWorkspaceCloseManual(journal, intent, err)
	}
	if errors.Is(err, errSharedAttachedHookDispatchUncertain) {
		return errors.Join(ErrManualCleanupRequired, err)
	}
	if issueClose && !issued {
		return resetSharedAttachedWorkspaceClose(journal, intent, err)
	}
	return errors.Join(ErrManualCleanupRequired, err)
}

func beginVerifiedSharedAttachedWorkspaceClose(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	child state.Pane,
	attached []state.Pane,
	intent state.LaunchIntent,
	issueClose bool,
) (state.LaunchIntent, error) {
	if !issueClose {
		return intent, nil
	}
	intent, err := preflightSharedAttachedChildCleanup(
		ctx, opts, locked, journal, child, attached, intent,
	)
	if err != nil {
		return intent, err
	}
	return beginSharedAttachedWorkspaceClose(ctx, journal, intent)
}

func preflightSharedAttachedChildCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	child state.Pane,
	attached []state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	runtime, err := opts.WorkspaceRuntime(ctx, child)
	if err != nil {
		return intent, err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return intent, verifyErr
	}
	intent, observation, err := observeReboundSharedAttachedChildCleanup(
		ctx, opts, locked, journal, runtime, child, attached, intent,
	)
	if err != nil {
		return intent, err
	}
	return replanObservedWorkspaceCleanup(
		ctx, opts, locked, journal, runtime, child, intent, observation,
	)
}

func beginSharedAttachedWorkspaceClose(
	ctx context.Context,
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	planned := intent.Status == state.IntentPlanned
	if planned {
		if err := ensureCleanupMutationFresh(ctx, intent); err != nil {
			return intent, err
		}
	}
	intent.Status, intent.Failure = state.IntentManualCleanupRequired, sharedAttachedWorkspaceCloseFailure
	if err := saveWorkspaceCleanupIntent(journal, intent); err != nil {
		return intent, err
	}
	if planned {
		if err := ensureCleanupMutationFresh(ctx, intent); err != nil {
			return intent, resetSharedAttachedWorkspaceClose(journal, intent, err)
		}
	}
	return intent, nil
}

func completeSharedAttachedWorkspaceClose(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	child state.Pane,
	attached []state.Pane,
	intent state.LaunchIntent,
) error {
	runtime, err := opts.WorkspaceRuntime(ctx, child)
	if err != nil {
		return err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return verifyErr
	}
	intent, observation, err := observeReboundSharedAttachedChildCleanup(
		ctx, opts, locked, journal, runtime, child, attached, intent,
	)
	if err != nil {
		return err
	}
	if err := verifySharedAttachedChildCleanup(ctx, opts.ProjectRoot, intent, observation); err != nil {
		return err
	}
	intent.ExpiresUnixMS = time.Now().Add(workspaceCleanupTimeout).UnixMilli()
	intent.Status, intent.Failure = state.IntentPlanned, sharedAttachedWorkspaceCloseComplete
	if workspaceCleanupAbsent(observation) {
		intent.Status, intent.Failure = state.IntentRealized, ""
	}
	return saveCompletedSharedAttachedWorkspaceClose(journal, child.WorktreePath, intent)
}

func saveCompletedSharedAttachedWorkspaceClose(
	journal *state.LockedLaunchJournal,
	worktreePath string,
	intent state.LaunchIntent,
) error {
	if err := retireSharedAttachedWorkspaceHookIntents(journal, worktreePath); err != nil {
		return err
	}
	return saveWorkspaceCleanupIntent(journal, intent)
}

func observeReboundSharedAttachedChildCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	child state.Pane,
	attached []state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, workspaceCleanupObservation, error) {
	observation, err := observeWorkspaceCleanupMatching(
		ctx, runtime, opts.ProjectRoot, intent.Resource,
		sharedChildWorkspacePredicate(intent.Resource, attached),
	)
	if err != nil {
		return intent, observation, err
	}
	intent, err = rebindObservedWorkspaceCleanupIdentity(
		locked, journal, opts.ProjectRoot, child, intent, observation.workspace,
	)
	return intent, observation, err
}

func verifySharedAttachedChildCleanup(
	ctx context.Context,
	projectRoot string,
	intent state.LaunchIntent,
	observation workspaceCleanupObservation,
) error {
	if observation.workspace != nil {
		if err := verifyTerminalInvalidation(*observation.workspace, intent.Resource); err != nil {
			return err
		}
	}
	if observation.checkout.PathAbsent && !observation.checkout.Registered {
		return nil
	}
	return verifyCleanupCheckout(
		ctx, projectRoot, intent.FullBranchRef, intent.ExpectedHead, intent.Resource,
	)
}

func prepareSharedAttachedWorkspaceClose(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	attached []state.Pane,
	mode CloseMode,
) (*state.LockedLaunchJournal, state.LaunchIntent, bool, error) {
	journal, intent, runtime, err := loadSharedAttachedWorkspaceCloseIntent(
		ctx, opts, locked, child, attached, mode,
	)
	if err != nil {
		return nil, intent, false, err
	}
	intent, issueClose, err := admitSharedAttachedWorkspaceClose(
		ctx, opts, locked, journal, runtime, child, attached, intent,
	)
	return journal, intent, issueClose, err
}

func loadSharedAttachedWorkspaceCloseIntent(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	attached []state.Pane,
	mode CloseMode,
) (*state.LockedLaunchJournal, state.LaunchIntent, WorkspaceRuntime, error) {
	runtime, err := opts.WorkspaceRuntime(ctx, child)
	if err != nil {
		return nil, state.LaunchIntent{}, nil, err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return nil, state.LaunchIntent{}, nil, verifyErr
	}
	resource := resourceFromPane(child)
	journal, intent, _, err := loadWorkspaceCleanupIntent(
		ctx, opts, locked, runtime, child, mode, sharedChildWorkspacePredicate(resource, attached),
	)
	if err != nil {
		return nil, intent, nil, err
	}
	return journal, intent, runtime, nil
}

func persistWorkspacePreflightIdentityFailure(
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	attached []state.Pane,
	mode CloseMode,
	sharedFailure bool,
	preHook bool,
	cause error,
) error {
	if !errors.Is(cause, backend.ErrOwnedIdentityMismatch) {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	journal, intent, useSharedFailure, prepareErr := workspacePreflightManualIntent(
		ctx, opts, locked, child, attached, mode, sharedFailure,
	)
	if journal == nil {
		return errors.Join(cause, prepareErr)
	}
	failure := cause.Error()
	if preHook {
		failure = workspacePreHookIdentityFailure
	} else if useSharedFailure {
		failure = sharedAttachedWorkspaceCloseFailure
	}
	return errors.Join(
		prepareErr,
		markWorkspaceCleanupManualPreservingFence(journal, intent, failure, cause),
	)
}

func persistWorkspaceCleanupHookPreflightFailure(
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	mode CloseMode,
	hook hooks.Type,
	cause error,
) error {
	attached := sharedAttachedWorkspaceRows(locked.Panes, pane.WorktreePath)
	return persistWorkspacePreflightIdentityFailure(
		opts, locked, pane, attached, mode, false, hook == hooks.BeforeWorktreeRemove, cause,
	)
}

func completeWorkspacePreHookFence(
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	mode CloseMode,
) error {
	if !workspaceRuntimeRow(pane) {
		return nil
	}
	journal, err := locked.LaunchJournal(opts.ProjectRoot)
	if err != nil {
		return err
	}
	_, intentID, err := workspaceCleanupIntentIDs(opts.ProjectRoot, pane)
	if err != nil {
		return err
	}
	intent, found := journal.FindIntent(intentID)
	if !found || !workspacePreHookIdentityFence(intent) {
		return nil
	}
	if err := validateSavedWorkspaceCleanup(intent, opts.ProjectRoot, pane, mode); err != nil {
		return err
	}
	attached := sharedAttachedWorkspaceRows(locked.Panes, pane.WorktreePath)
	return rebuildWorkspaceCleanupAfterHook(opts, locked, pane, attached, mode, intent.ID)
}

func rebuildWorkspaceCleanupAfterHook(
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	attached []state.Pane,
	mode CloseMode,
	intentID string,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	runtime, err := opts.WorkspaceRuntime(ctx, pane)
	if err != nil {
		return err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return verifyErr
	}
	resource := resourceFromPane(pane)
	predicate := workspacePredicate(resource)
	if len(attached) > 0 {
		predicate = sharedChildWorkspacePredicate(resource, attached)
	}
	_, err = beginWorkspaceCleanupAtHookPhase(
		ctx, opts, locked, runtime, pane, mode, intentID, predicate,
		state.CleanupHookBeforeWorktreeRemove,
	)
	return err
}

func workspacePreHookIdentityFence(intent state.LaunchIntent) bool {
	return intent.Status == state.IntentManualCleanupRequired &&
		intent.Failure == workspacePreHookIdentityFailure
}

func workspacePreflightManualIntent(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	attached []state.Pane,
	mode CloseMode,
	sharedFailure bool,
) (*state.LockedLaunchJournal, state.LaunchIntent, bool, error) {
	journal, intent, found, err := savedWorkspacePreflightCleanupIntent(opts, locked, child, mode)
	if err != nil || found {
		return journal, intent, sharedFailure, err
	}
	if sharedFailure {
		loaded, planned, _, loadErr := loadSharedAttachedWorkspaceCloseIntent(
			ctx, opts, locked, child, attached, mode,
		)
		if loadErr == nil {
			return loaded, planned, true, nil
		}
		intent, err = newUnresolvedWorkspaceCleanupIntent(opts.ProjectRoot, child, mode)
		return journal, intent, false, errors.Join(loadErr, err)
	}
	intent, err = newUnresolvedWorkspaceCleanupIntent(opts.ProjectRoot, child, mode)
	return journal, intent, false, err
}

func savedWorkspacePreflightCleanupIntent(
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	mode CloseMode,
) (*state.LockedLaunchJournal, state.LaunchIntent, bool, error) {
	journal, err := locked.LaunchJournal(opts.ProjectRoot)
	if err != nil {
		return nil, state.LaunchIntent{}, false, err
	}
	worktreeIntentID, intentID, err := workspaceCleanupIntentIDs(opts.ProjectRoot, pane)
	if err != nil {
		return nil, state.LaunchIntent{}, false, err
	}
	if validationErr := validateLaunchIntentForCleanup(journal, worktreeIntentID, opts.ProjectRoot, pane); validationErr != nil {
		return nil, state.LaunchIntent{}, false, validationErr
	}
	intent, found := journal.FindIntent(intentID)
	if found {
		err = validateSavedWorkspaceCleanup(intent, opts.ProjectRoot, pane, mode)
	}
	return journal, intent, found, err
}

func newUnresolvedWorkspaceCleanupIntent(
	projectRoot string,
	pane state.Pane,
	mode CloseMode,
) (state.LaunchIntent, error) {
	ownerRoot, err := state.IntentOwnerProjectRoot(pane.Parent, filepath.Clean(projectRoot))
	if err != nil {
		return state.LaunchIntent{}, err
	}
	_, intentID, err := workspaceCleanupIntentIDs(projectRoot, pane)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	deleteBranchRequested := mode == CloseEverything && pane.BranchCreated
	return state.LaunchIntent{
		ID: intentID, Kind: state.IntentCleanup, Status: state.IntentPlanned,
		Parent: pane.Parent, RuntimeParent: pane.RuntimeParent, OwnerProjectRoot: ownerRoot,
		IssueNum: pane.IssueNum, TaskID: pane.TaskID, Slug: pane.Slug,
		BranchName: pane.BranchName, FullBranchRef: "refs/heads/" + pane.BranchName,
		BaseBranch: pane.BaseBranch, WorktreePath: filepath.Clean(pane.WorktreePath),
		BranchCreated: pane.BranchCreated, BranchExisted: !pane.BranchCreated,
		WorkspaceLabel: pane.WorkspaceLabel, Resource: resourceFromPane(pane),
		Session: pane.SessionID, SocketPath: pane.SocketPath,
		ExpiresUnixMS: time.Now().Add(workspaceCleanupTimeout).UnixMilli(),
		CleanupPhase:  state.CleanupRemove, CleanupDeleteBranchRequested: &deleteBranchRequested,
		CleanupHookPhase: state.CleanupHookPending,
	}, nil
}

func admitSharedAttachedWorkspaceClose(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	child state.Pane,
	attached []state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, bool, error) {
	if intent.Status == state.IntentPlanned {
		if freshnessErr := ensureCleanupMutationFresh(ctx, intent); freshnessErr != nil {
			return recoverExpiredSharedAttachedWorkspaceClose(
				ctx, opts, locked, journal, runtime, child, attached, intent, freshnessErr,
			)
		}
		return intent, true, nil
	}
	if intent.Status == state.IntentRealized {
		return intent, true, nil
	}
	if intent.Status == state.IntentManualCleanupRequired && intent.Failure == sharedAttachedWorkspaceCloseFailure {
		return intent, false, nil
	}
	if intent.Status == state.IntentManualCleanupRequired {
		return intent, false, fmt.Errorf("%w: %s", ErrManualCleanupRequired, intent.Failure)
	}
	cause := fmt.Errorf("shared attached workspace close cannot adopt cleanup status %s", intent.Status)
	return intent, false, markSharedAttachedWorkspaceCloseManual(journal, intent, cause)
}

func recoverExpiredSharedAttachedWorkspaceClose(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	child state.Pane,
	attached []state.Pane,
	intent state.LaunchIntent,
	freshnessErr error,
) (state.LaunchIntent, bool, error) {
	intent, observation, recoverErr := observeReboundSharedAttachedChildCleanup(
		ctx, opts, locked, journal, runtime, child, attached, intent,
	)
	if recoverErr == nil {
		intent, recoverErr = recoverExpiredObservedWorkspaceCleanup(
			ctx, opts, locked, journal, runtime, child, intent, observation,
		)
	}
	if recoverErr == nil {
		recoverErr = fmt.Errorf("saved Herdr cleanup intent expired before shared attached workspace close; retry")
	}
	return intent, false, errors.Join(freshnessErr, recoverErr)
}

func sharedAttachedWorkspaceRows(panes []state.Pane, worktreePath string) []state.Pane {
	path := normalizedWorktreePath(worktreePath)
	var attached []state.Pane
	for _, pane := range panes {
		if workspaceRuntimeRow(pane) && pane.IsAttachedAgent() &&
			normalizedWorktreePath(pane.WorktreePath) == path {
			attached = append(attached, pane)
		}
	}
	return attached
}

type sharedAttachedWorkspaceCloseTarget struct {
	pane      state.Pane
	runtime   WorkspaceRuntime
	workspace *backend.WorkspaceObservation
}

func inspectSharedAttachedWorkspaces(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	panes []state.Pane,
) ([]sharedAttachedWorkspaceCloseTarget, error) {
	targets := make([]sharedAttachedWorkspaceCloseTarget, 0, len(panes))
	for _, pane := range panes {
		if pane.WorkspaceID == child.WorkspaceID || pane.WorkspaceLabel == child.WorkspaceLabel {
			return nil, fmt.Errorf("%w: attached workspace identity aliases its child", backend.ErrOwnedIdentityMismatch)
		}
		current, runtime, workspace, err := inspectSharedAttachedWorkspace(ctx, opts, locked, pane)
		if err != nil {
			return nil, err
		}
		targets = append(targets, sharedAttachedWorkspaceCloseTarget{current, runtime, workspace})
	}
	return targets, nil
}

func validatedSharedAttachedWorkspaceRows(
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	panes []state.Pane,
) ([]state.Pane, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	targets, err := inspectSharedAttachedWorkspaces(ctx, opts, locked, child, panes)
	if err != nil {
		return nil, err
	}
	rows := make([]state.Pane, 0, len(targets))
	for _, target := range targets {
		rows = append(rows, target.pane)
	}
	return rows, nil
}

func validateSharedAttachedWorkspaceHookPreflight(
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	attached []state.Pane,
	mode CloseMode,
) error {
	if len(attached) == 0 {
		return nil
	}
	if _, err := validatedSharedAttachedWorkspaceRows(opts, locked, child, attached); err != nil {
		return persistWorkspacePreflightIdentityFailure(opts, locked, child, attached, mode, true, true, err)
	}
	return validateSharedAttachedWorkspaceHookFence(opts, locked, child, mode)
}

func validateSharedAttachedWorkspaceHookFence(
	opts Options,
	locked *state.LockedStore,
	child state.Pane,
	mode CloseMode,
) error {
	_, intent, found, err := savedWorkspacePreflightCleanupIntent(opts, locked, child, mode)
	if err != nil || !found {
		return err
	}
	if intent.Status == state.IntentIssued {
		return fmt.Errorf("%w: saved shared attached cleanup is already issued", ErrManualCleanupRequired)
	}
	if intent.Status == state.IntentManualCleanupRequired &&
		intent.Failure != sharedAttachedWorkspaceCloseFailure && !workspacePreHookIdentityFence(intent) {
		return fmt.Errorf("%w: %s", ErrManualCleanupRequired, intent.Failure)
	}
	return nil
}

func closeSharedAttachedWorkspace(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	target sharedAttachedWorkspaceCloseTarget,
	issueClose bool,
	lg Logger,
) (bool, error) {
	if target.workspace != nil && !issueClose {
		return false, errors.New(sharedAttachedWorkspaceCloseFailure)
	}
	if issueClose {
		var err error
		target, err = revalidateSharedAttachedWorkspaceTarget(ctx, opts, locked, target.pane)
		if err != nil {
			return false, err
		}
		if err := runSharedAttachedWorkspaceHook(
			opts, locked, journal, target.pane, hooks.BeforePaneClose, lg,
		); err != nil {
			return false, err
		}
	}
	return closeRevalidatedSharedAttachedWorkspace(ctx, opts, locked, journal, target, lg)
}

func closeRevalidatedSharedAttachedWorkspace(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	target sharedAttachedWorkspaceCloseTarget,
	lg Logger,
) (bool, error) {
	if target.workspace == nil {
		if err := completeSharedAttachedWorkspaceHooks(opts, locked, journal, target.pane, lg); err != nil {
			return false, err
		}
		return false, locked.RemovePane(target.pane.Parent, target.pane.IssueNum)
	}
	mutationErr := target.runtime.CloseAttachedWorkspace(ctx, target.pane.RuntimeBinding())
	if mutationDefinitelyNotIssued(mutationErr) {
		return false, mutationErr
	}
	_, _, workspace, verifyErr := inspectSharedAttachedWorkspace(ctx, opts, locked, target.pane)
	if verifyErr != nil || workspace != nil {
		return true, errors.Join(
			mutationErr, verifyErr,
			fmt.Errorf("shared attached workspace close did not establish absence"),
		)
	}
	if err := completeSharedAttachedWorkspaceHooks(opts, locked, journal, target.pane, lg); err != nil {
		return true, err
	}
	return true, locked.RemovePane(target.pane.Parent, target.pane.IssueNum)
}

func completeSharedAttachedWorkspaceHooks(
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	pane state.Pane,
	lg Logger,
) error {
	intent, found, err := sharedAttachedWorkspaceHookIntent(opts, locked, journal, pane)
	if err != nil || !found {
		return err
	}
	if err := dispatchSharedAttachedWorkspaceHook(
		opts, journal, &intent, pane, hooks.PaneClosed,
		state.CleanupHookPaneClosedIssued, state.CleanupHookPaneClosed, lg,
	); err != nil {
		return err
	}
	if !persistCleanupHookPhase(journal, &intent, state.CleanupHookCompleted, pane, lg) {
		return errors.New("persist completed shared attached workspace hooks")
	}
	return nil
}

func runSharedAttachedWorkspaceHook(
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	pane state.Pane,
	hook hooks.Type,
	lg Logger,
) error {
	intent, found, err := sharedAttachedWorkspaceHookIntent(opts, locked, journal, pane)
	if err != nil || !found {
		return err
	}
	issued, completed := cleanupHookCheckpoint(hook)
	return dispatchSharedAttachedWorkspaceHook(opts, journal, &intent, pane, hook, issued, completed, lg)
}

func dispatchSharedAttachedWorkspaceHook(
	opts Options,
	journal *state.LockedLaunchJournal,
	intent *state.LaunchIntent,
	pane state.Pane,
	hook hooks.Type,
	issued, completed state.CleanupHookPhase,
	lg Logger,
) error {
	if rejectAmbiguousCleanupHookPhase(intent.CleanupHookPhase, pane, lg) {
		return errSharedAttachedHookDispatchUncertain
	}
	if len(opts.Hooks.Events[hook]) == 0 || cleanupHookPhaseReached(intent.CleanupHookPhase, completed) {
		return nil
	}
	previous := *intent
	if !persistCleanupHookPhase(journal, intent, issued, pane, lg) {
		journal.UpsertIntent(previous)
		return fmt.Errorf("persist shared attached workspace %s hook issuance", hook)
	}
	runWorkspaceBackgroundHook(hook, opts, pane, "", lg)
	if !persistCleanupHookPhase(journal, intent, completed, pane, lg) {
		return fmt.Errorf("%w: persist shared attached workspace %s hook completion", errSharedAttachedHookDispatchUncertain, hook)
	}
	return nil
}

func sharedAttachedWorkspaceHookIntent(
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	pane state.Pane,
) (state.LaunchIntent, bool, error) {
	intentID, err := sharedAttachedWorkspaceHookIntentID(locked, pane)
	if err != nil {
		return state.LaunchIntent{}, false, err
	}
	intent, found := journal.FindIntent(intentID)
	if found {
		return intent, true, validateSharedAttachedWorkspaceHookIntent(opts.ProjectRoot, pane, intent)
	}
	if len(opts.Hooks.Events[hooks.BeforePaneClose]) == 0 && len(opts.Hooks.Events[hooks.PaneClosed]) == 0 {
		return state.LaunchIntent{}, false, nil
	}
	intent, err = newSharedAttachedWorkspaceHookIntent(opts.ProjectRoot, pane, intentID)
	if err != nil {
		return state.LaunchIntent{}, false, err
	}
	if err := saveWorkspaceCleanupIntent(journal, intent); err != nil {
		journal.RemoveIntent(intent.ID)
		return state.LaunchIntent{}, false, err
	}
	return intent, true, nil
}

func sharedAttachedWorkspaceHookIntentID(locked *state.LockedStore, pane state.Pane) (string, error) {
	path := normalizedWorktreePath(pane.WorktreePath)
	index, err := locked.EmitterRowIndex(pane.EmitterRowKey, path, pane.WorkspaceLabel)
	if err != nil {
		return "", fmt.Errorf("%w: %w", backend.ErrOwnedIdentityMismatch, err)
	}
	if index < 0 || !locked.Panes[index].RuntimeBinding().Equal(pane.RuntimeBinding()) {
		return "", fmt.Errorf("%w: shared attached hook row identity changed", backend.ErrOwnedIdentityMismatch)
	}
	digest := sha256.Sum256([]byte(pane.EmitterRowKey + "\x00" + path + "\x00" + pane.WorkspaceLabel))
	return fmt.Sprintf("%s%x", sharedAttachedWorkspaceHookPrefix, digest), nil
}

func newSharedAttachedWorkspaceHookIntent(
	projectRoot string,
	pane state.Pane,
	intentID string,
) (state.LaunchIntent, error) {
	hookPane := sharedAttachedWorkspaceHookPane(pane)
	intent, err := newUnresolvedWorkspaceCleanupIntent(projectRoot, hookPane, CloseWorktree)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	worktreeRemovedRequired := false
	intent.ID, intent.Status = intentID, state.IntentRealized
	intent.CleanupPhase = state.CleanupWorkspaceClose
	intent.CleanupDeleteBranchVerified = true
	intent.CleanupWorktreeRemovedRequired = &worktreeRemovedRequired
	return intent, nil
}

func validateSharedAttachedWorkspaceHookIntent(
	projectRoot string,
	pane state.Pane,
	intent state.LaunchIntent,
) error {
	if err := validateSavedWorkspaceCleanup(intent, projectRoot, sharedAttachedWorkspaceHookPane(pane), CloseWorktree); err != nil {
		return err
	}
	if intent.Status != state.IntentRealized || intent.CleanupPhase != state.CleanupWorkspaceClose ||
		intent.CleanupWorktreeRemovedRequired == nil || *intent.CleanupWorktreeRemovedRequired {
		return fmt.Errorf("saved shared attached workspace hook checkpoint is invalid")
	}
	return nil
}

func sharedAttachedWorkspaceHookPane(pane state.Pane) state.Pane {
	pane.Parent, pane.RuntimeParent = panelaunch.ManualParentRef, panelaunch.ManualParentRef
	pane.IssueNum, pane.TaskID = -1, ""
	return pane
}

func retireSharedAttachedWorkspaceHookIntents(
	journal *state.LockedLaunchJournal,
	worktreePath string,
) error {
	path := normalizedWorktreePath(worktreePath)
	for _, intent := range slices.Clone(journal.Intents) {
		if !strings.HasPrefix(intent.ID, sharedAttachedWorkspaceHookPrefix) ||
			normalizedWorktreePath(intent.WorktreePath) != path {
			continue
		}
		if intent.CleanupHookPhase != state.CleanupHookCompleted {
			return fmt.Errorf("%w: shared attached workspace hook checkpoint %q is incomplete", ErrManualCleanupRequired, intent.CleanupHookPhase)
		}
		journal.RemoveIntent(intent.ID)
	}
	return nil
}

func revalidateSharedAttachedWorkspaceTarget(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
) (sharedAttachedWorkspaceCloseTarget, error) {
	current, runtime, workspace, err := inspectSharedAttachedWorkspace(ctx, opts, locked, pane)
	return sharedAttachedWorkspaceCloseTarget{current, runtime, workspace}, err
}

func inspectSharedAttachedWorkspace(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
) (state.Pane, WorkspaceRuntime, *backend.WorkspaceObservation, error) {
	current, err := currentSharedAttachedWorkspaceRow(locked, pane)
	if err != nil {
		return state.Pane{}, nil, nil, err
	}
	runtime, err := opts.WorkspaceRuntime(ctx, current)
	if err != nil {
		return state.Pane{}, nil, nil, err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return state.Pane{}, nil, nil, verifyErr
	}
	resource := resourceFromPane(current)
	observation, err := verifyWorkspaceCloseTarget(
		ctx, opts.ProjectRoot, runtime, current, resource,
		workspaceResourcePredicate(resource), false,
	)
	return current, runtime, observation.workspace, err
}

func currentSharedAttachedWorkspaceRow(locked *state.LockedStore, pane state.Pane) (state.Pane, error) {
	if err := validateWorkspacePaneIdentity(pane); err != nil {
		return state.Pane{}, fmt.Errorf("%w: %w", backend.ErrOwnedIdentityMismatch, err)
	}
	binding := pane.RuntimeBinding()
	index := -1
	for i := range locked.Panes {
		current := locked.Panes[i]
		if !current.IsAttachedAgent() || !current.RuntimeBinding().Equal(binding) {
			continue
		}
		if index >= 0 {
			return state.Pane{}, fmt.Errorf("%w: saved shared attached workspace row identity is not unique", backend.ErrOwnedIdentityMismatch)
		}
		index = i
	}
	if index < 0 {
		return state.Pane{}, fmt.Errorf("%w: saved shared attached workspace row identity changed", backend.ErrOwnedIdentityMismatch)
	}
	return locked.Panes[index], nil
}

func workspaceResourcePredicate(resource state.RuntimeResource) workspacePredicateFunc {
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		candidate := workspace.WorkspaceID == resource.WorkspaceID || workspace.Label == resource.Label
		return candidate, workspaceMatchesResource(workspace, resource)
	}
}

func sharedChildWorkspacePredicate(resource state.RuntimeResource, attached []state.Pane) workspacePredicateFunc {
	base := workspaceLabelPredicate(resource.Label, resource.CurrentPath, resource.RepoKey, resource.RepoRoot)
	return func(workspace backend.WorkspaceObservation) (bool, bool) {
		candidate, exact := base(workspace)
		if !workspaceMatchesAttachedRowIdentity(workspace, attached) {
			return candidate, exact
		}
		if exact {
			return true, false
		}
		return false, false
	}
}

func workspaceMatchesAttachedRowIdentity(workspace backend.WorkspaceObservation, attached []state.Pane) bool {
	return slices.ContainsFunc(attached, func(pane state.Pane) bool {
		return workspace.WorkspaceID == pane.WorkspaceID || workspace.Label == pane.WorkspaceLabel
	})
}

func markSharedAttachedWorkspaceCloseManual(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	cause error,
) error {
	return markWorkspaceCleanupManualPreservingFence(
		journal, intent, sharedAttachedWorkspaceCloseFailure, cause,
	)
}

func markWorkspaceCleanupManualPreservingFence(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	failure string,
	cause error,
) error {
	allowed := intent.Status == state.IntentPlanned || intent.Status == state.IntentRealized ||
		intent.Status == state.IntentManualCleanupRequired && intent.Failure == failure
	if !allowed {
		return errors.Join(ErrManualCleanupRequired, cause)
	}
	intent.Status, intent.Failure = state.IntentManualCleanupRequired, failure
	return errors.Join(ErrManualCleanupRequired, cause, saveWorkspaceCleanupIntent(journal, intent))
}

func resetSharedAttachedWorkspaceClose(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	cause error,
) error {
	intent.Status, intent.Failure = state.IntentPlanned, ""
	return errors.Join(cause, saveWorkspaceCleanupIntent(journal, intent))
}

func loadWorkspaceCleanupIntent(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	runtime WorkspaceRuntime,
	pane state.Pane,
	mode CloseMode,
	predicate workspacePredicateFunc,
) (*state.LockedLaunchJournal, state.LaunchIntent, string, error) {
	journal, err := locked.LaunchJournal(opts.ProjectRoot)
	if err != nil {
		return nil, state.LaunchIntent{}, "", err
	}
	worktreeIntentID, intentID, err := workspaceCleanupIntentIDs(opts.ProjectRoot, pane)
	if err != nil {
		return nil, state.LaunchIntent{}, "", err
	}
	if validationErr := validateLaunchIntentForCleanup(journal, worktreeIntentID, opts.ProjectRoot, pane); validationErr != nil {
		return nil, state.LaunchIntent{}, "", validationErr
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		intent, err = beginWorkspaceCleanup(ctx, opts, locked, runtime, pane, mode, intentID, predicate)
	} else {
		err = validateSavedWorkspaceCleanup(intent, opts.ProjectRoot, pane, mode)
	}
	if err != nil {
		return nil, state.LaunchIntent{}, "", err
	}
	return normalizeLoadedWorkspaceCleanupIntent(journal, intent, worktreeIntentID, mode, pane)
}

func normalizeLoadedWorkspaceCleanupIntent(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	worktreeIntentID string,
	mode CloseMode,
	pane state.Pane,
) (*state.LockedLaunchJournal, state.LaunchIntent, string, error) {
	intent, err := normalizeWorkspaceCleanupBranchDelete(journal, intent, mode, pane)
	if err != nil {
		return nil, state.LaunchIntent{}, "", err
	}
	return journal, intent, worktreeIntentID, nil
}

func normalizeWorkspaceCleanupBranchDelete(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	mode CloseMode,
	pane state.Pane,
) (state.LaunchIntent, error) {
	if intent.CleanupDeleteBranchVerified {
		return intent, nil
	}
	if intent.CleanupDeleteBranchRequested == nil {
		deleteBranchRequested := mode == CloseEverything && pane.BranchCreated
		intent.CleanupDeleteBranchRequested = &deleteBranchRequested
	}
	intent.CleanupDeleteBranch = false
	intent.CleanupDeleteBranchVerified = true
	return intent, saveWorkspaceCleanupIntent(journal, intent)
}

func workspaceCleanupIntentIDs(projectRoot string, pane state.Pane) (string, string, error) {
	ownerRoot, err := state.IntentOwnerProjectRoot(pane.Parent, filepath.Clean(projectRoot))
	if err != nil {
		return "", "", err
	}
	worktreeID, err := state.WorktreeIntentID(pane.Parent, ownerRoot, pane.IssueNum, pane.TaskID)
	if err != nil {
		return "", "", err
	}
	cleanupID, err := state.CleanupIntentID(worktreeID)
	return worktreeID, cleanupID, err
}

func validateLaunchIntentForCleanup(
	journal *state.LockedLaunchJournal,
	worktreeIntentID, projectRoot string,
	pane state.Pane,
) error {
	intent, found := journal.FindIntent(worktreeIntentID)
	if !found {
		return nil
	}
	ownerRoot, err := state.IntentOwnerProjectRoot(pane.Parent, filepath.Clean(projectRoot))
	if err != nil {
		return err
	}
	allowedStatus := intent.Status == state.IntentRealized ||
		intent.Status == state.IntentManualCleanupRequired
	if slices.Contains([]bool{
		intent.Kind == state.IntentWorktree, allowedStatus, intentMatchesPane(intent, pane, ownerRoot),
		launchResourceMatchesCleanupPane(intent.Resource, pane), intent.Launch != nil && intent.Launch.TokenIssued,
	}, false) {
		return fmt.Errorf("saved Herdr launch intent does not match the child row")
	}
	return nil
}

func launchResourceMatchesCleanupPane(resource state.RuntimeResource, pane state.Pane) bool {
	saved := resourceFromPane(pane)
	return resource == saved || resource.WorkspaceID != saved.WorkspaceID &&
		resource.Label == saved.Label &&
		filepath.Clean(resource.CurrentPath) == filepath.Clean(saved.CurrentPath) &&
		filepath.Clean(resource.RepoKey) == filepath.Clean(saved.RepoKey) &&
		filepath.Clean(resource.RepoRoot) == filepath.Clean(saved.RepoRoot)
}

func beginWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	runtime WorkspaceRuntime,
	pane state.Pane,
	mode CloseMode,
	intentID string,
	predicate workspacePredicateFunc,
) (state.LaunchIntent, error) {
	return beginWorkspaceCleanupAtHookPhase(
		ctx, opts, locked, runtime, pane, mode, intentID, predicate, state.CleanupHookPending,
	)
}

func beginWorkspaceCleanupAtHookPhase(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	runtime WorkspaceRuntime,
	pane state.Pane,
	mode CloseMode,
	intentID string,
	predicate workspacePredicateFunc,
	hookPhase state.CleanupHookPhase,
) (state.LaunchIntent, error) {
	resource := resourceFromPane(pane)
	observation, err := observeWorkspaceCleanupMatching(ctx, runtime, opts.ProjectRoot, resource, predicate)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	fullRef, err := worktree.LocalBranchRef(ctx, opts.ProjectRoot, pane.BranchName)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	intent, err := newWorkspaceCleanupIntent(ctx, opts, pane, mode, intentID, fullRef, resource, observation)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	intent.CleanupHookPhase = hookPhase
	if intent.CleanupPhase == state.CleanupReopen {
		intent, err = attachWorkspaceCleanupCoordinator(ctx, locked, runtime, opts.ProjectRoot, pane, intent)
		if err != nil {
			return state.LaunchIntent{}, err
		}
	}
	return persistNewWorkspaceCleanup(locked, opts.ProjectRoot, intent)
}

func persistNewWorkspaceCleanup(
	locked *state.LockedStore,
	projectRoot string,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	journal.UpsertIntent(intent)
	return intent, journal.Save()
}

func attachWorkspaceCleanupCoordinator(
	ctx context.Context,
	locked *state.LockedStore,
	runtime WorkspaceRuntime,
	projectRoot string,
	pane state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	coordinator, err := findCoordinatorIntent(locked, projectRoot, pane)
	if err != nil {
		return intent, err
	}
	live, err := observeCoordinator(ctx, runtime, coordinator.Resource)
	if err != nil {
		return intent, err
	}
	intent.Coordinator = coordinatorResource(live)
	return intent, nil
}

func attachReplannedWorkspaceCleanupCoordinator(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	projectRoot string,
	pane state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if pane.RuntimeParent != panelaunch.ManualParentRef {
		return attachWorkspaceCleanupCoordinator(ctx, locked, runtime, projectRoot, pane, intent)
	}
	return attachReplannedManualCoordinator(ctx, locked, journal, runtime, projectRoot, pane, intent)
}

func attachReplannedManualCoordinator(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	projectRoot string,
	pane state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	predecessors, intent, err := preserveReplannedManualCoordinator(journal, projectRoot, pane, intent)
	if err != nil {
		return intent, err
	}
	realized, err := realizeReplannedManualCoordinator(ctx, locked, runtime, projectRoot, pane)
	if err != nil {
		return intent, err
	}
	return adoptReplannedManualCoordinator(ctx, locked, journal, runtime, predecessors, realized, intent)
}

func adoptReplannedManualCoordinator(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	predecessors []state.RuntimeResource,
	realized state.LaunchIntent,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if err := panelaunch.ReconcileManagedCoordinatorReplanRow(locked, predecessors, realized); err != nil {
		return intent, err
	}
	live, err := observeCoordinator(ctx, runtime, realized.Resource)
	if err != nil {
		return intent, err
	}
	journal.UpsertIntent(realized)
	intent.Coordinator = coordinatorResource(live)
	return intent, nil
}

func preserveReplannedManualCoordinator(
	journal *state.LockedLaunchJournal,
	projectRoot string,
	pane state.Pane,
	intent state.LaunchIntent,
) ([]state.RuntimeResource, state.LaunchIntent, error) {
	coordinator, found, err := findRealizedReplannedManualCoordinator(journal, projectRoot, pane)
	if err != nil {
		return nil, intent, err
	}
	predecessors := replannedManualCoordinatorPredecessors(intent.Coordinator, coordinator, found)
	if len(predecessors) == 0 {
		return nil, intent, fmt.Errorf("saved Herdr coordinator intent is not recorded")
	}
	if intent.Coordinator == (state.RuntimeResource{}) {
		intent.Coordinator = coordinator.Resource
		return predecessors, intent, saveWorkspaceCleanupIntent(journal, intent)
	}
	return predecessors, intent, nil
}

func replannedManualCoordinatorPredecessors(
	cleanup state.RuntimeResource,
	coordinator state.LaunchIntent,
	found bool,
) []state.RuntimeResource {
	var predecessors []state.RuntimeResource
	if cleanup != (state.RuntimeResource{}) {
		predecessors = append(predecessors, cleanup)
	}
	if found && coordinator.Resource != cleanup {
		predecessors = append(predecessors, coordinator.Resource)
	}
	return predecessors
}

func findRealizedReplannedManualCoordinator(
	journal *state.LockedLaunchJournal,
	projectRoot string,
	pane state.Pane,
) (state.LaunchIntent, bool, error) {
	id, runtimeOwnerRoot, err := coordinatorIntentIdentity(projectRoot, pane)
	if err != nil {
		return state.LaunchIntent{}, false, err
	}
	coordinator, found := journal.FindIntent(id)
	if !found || coordinator.Status != state.IntentRealized {
		return state.LaunchIntent{}, false, nil
	}
	if !coordinatorIntentMatches(coordinator, pane, runtimeOwnerRoot, projectRoot) {
		return state.LaunchIntent{}, false, fmt.Errorf("saved Herdr coordinator intent does not match the child row")
	}
	return coordinator, true, nil
}

func realizeReplannedManualCoordinator(
	ctx context.Context,
	locked *state.LockedStore,
	runtime WorkspaceRuntime,
	projectRoot string,
	pane state.Pane,
) (state.LaunchIntent, error) {
	result, err := panelaunch.RealizeManagedCoordinator(ctx, panelaunch.ManagedCoordinatorRequest{
		Parent: panelaunch.ManualParentRef, RuntimeParent: pane.RuntimeParent,
		IssueNum: pane.IssueNum, ProjectRoot: projectRoot,
		SourceRoot: projectRoot, CWD: projectRoot,
		ManagedSession: pane.SessionID, SocketPath: pane.SocketPath,
	}, runtime, locked, panelaunch.ManagedRealizeHooks{})
	if err != nil && !errors.Is(err, panelaunch.ErrManagedLauncherReadinessDeferred) {
		return state.LaunchIntent{}, err
	}
	return result.Intent, nil
}

func coordinatorResource(workspace backend.WorkspaceObservation) state.RuntimeResource {
	return state.RuntimeResource{
		WorkspaceID: workspace.WorkspaceID,
		Label:       workspace.Label,
		PaneID:      workspace.Pane.Pane,
		TerminalID:  workspace.TerminalID,
		CurrentPath: filepath.Clean(workspace.CWD),
	}
}

//nolint:funlen // Keep the persisted cleanup identity and hook obligations visible in one constructor.
func newWorkspaceCleanupIntent(
	ctx context.Context,
	opts Options,
	pane state.Pane,
	mode CloseMode,
	intentID, fullRef string,
	resource state.RuntimeResource,
	observation workspaceCleanupObservation,
) (state.LaunchIntent, error) {
	phase, err := classifyFreshWorkspaceCleanup(ctx, opts.ProjectRoot, fullRef, resource, observation, false)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	ownerRoot, expectedHead, branchFound, err := workspaceCleanupMetadata(ctx, opts.ProjectRoot, pane.Parent, fullRef)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	deleteBranchRequested := mode == CloseEverything && pane.BranchCreated
	worktreeRemovedRequired := recordedWorktreeExists(pane) && len(opts.Hooks.Events[hooks.WorktreeRemoved]) != 0
	intent := state.LaunchIntent{
		ID: intentID, Kind: state.IntentCleanup, Status: freshWorkspaceCleanupStatus(observation),
		Parent: pane.Parent, RuntimeParent: pane.RuntimeParent, OwnerProjectRoot: ownerRoot,
		IssueNum: pane.IssueNum, TaskID: pane.TaskID, Slug: pane.Slug,
		BranchName: pane.BranchName, FullBranchRef: fullRef, BaseBranch: pane.BaseBranch,
		ExpectedHead: expectedHead, WorktreePath: resource.CurrentPath,
		BranchExisted: !pane.BranchCreated, BranchCreated: pane.BranchCreated,
		WorkspaceLabel: resource.Label, Resource: resource,
		Session: pane.SessionID, SocketPath: pane.SocketPath,
		ExpiresUnixMS: time.Now().Add(workspaceCleanupTimeout).UnixMilli(),
		CleanupPhase:  phase, CleanupDeleteBranch: deleteBranchRequested && branchFound && workspaceCleanupCheckoutPresent(observation),
		CleanupDeleteBranchRequested:   &deleteBranchRequested,
		CleanupDeleteBranchVerified:    true,
		CleanupHookPhase:               state.CleanupHookPending,
		CleanupWorktreeRemovedRequired: &worktreeRemovedRequired,
	}
	return intent, nil
}

func workspaceCleanupCheckoutPresent(observation workspaceCleanupObservation) bool {
	return !observation.checkout.PathAbsent || observation.checkout.Registered
}

func freshWorkspaceCleanupStatus(observation workspaceCleanupObservation) state.LaunchIntentStatus {
	if workspaceCleanupAbsent(observation) {
		return state.IntentRealized
	}
	return state.IntentPlanned
}

func workspaceCleanupMetadata(
	ctx context.Context,
	projectRoot, parent, fullRef string,
) (string, string, bool, error) {
	ownerRoot, err := state.IntentOwnerProjectRoot(parent, filepath.Clean(projectRoot))
	if err != nil {
		return "", "", false, err
	}
	expectedHead, found, err := worktree.ObserveBranch(ctx, projectRoot, fullRef)
	return ownerRoot, expectedHead, found, err
}

func classifyFreshWorkspaceCleanup(
	ctx context.Context,
	projectRoot, fullRef string,
	resource state.RuntimeResource,
	observation workspaceCleanupObservation,
	verifyContents bool,
) (state.CleanupPhase, error) {
	workspacePresent := observation.workspace != nil
	checkoutPresent := workspaceCleanupCheckoutPresent(observation)
	switch {
	case workspacePresent && checkoutPresent:
		if err := verifyTerminalInvalidation(*observation.workspace, resource); err != nil {
			return "", err
		}
		if err := verifyCleanupCheckout(ctx, projectRoot, fullRef, observation.checkout.HeadSHA, resource); err != nil {
			return "", err
		}
		return state.CleanupRemove, verifyFreshWorkspaceCleanupContents(ctx, resource, verifyContents)
	case workspacePresent:
		return state.CleanupWorkspaceClose, nil
	case checkoutPresent:
		if err := verifyCleanupCheckout(ctx, projectRoot, fullRef, observation.checkout.HeadSHA, resource); err != nil {
			return "", err
		}
		return state.CleanupReopen, verifyFreshWorkspaceCleanupContents(ctx, resource, verifyContents)
	default:
		return state.CleanupRemove, nil
	}
}

func verifyFreshWorkspaceCleanupContents(
	ctx context.Context,
	resource state.RuntimeResource,
	verify bool,
) error {
	if !verify {
		return nil
	}
	return verifyRemovableCheckoutContents(ctx, resource.CurrentPath)
}

func validateSavedWorkspaceCleanup(intent state.LaunchIntent, projectRoot string, pane state.Pane, mode CloseMode) error {
	ownerRoot, err := state.IntentOwnerProjectRoot(pane.Parent, filepath.Clean(projectRoot))
	if err != nil {
		return err
	}
	deleteBranchRequested := mode == CloseEverything && pane.BranchCreated
	if slices.Contains([]bool{
		intent.Kind == state.IntentCleanup, intentMatchesPane(intent, pane, ownerRoot),
		workspaceCleanupBranchDeleteMatches(intent, deleteBranchRequested), cleanupResourceMatchesPane(intent, pane),
	}, false) {
		return fmt.Errorf("saved Herdr cleanup intent does not match the selected state row")
	}
	return nil
}

func workspaceCleanupBranchDeleteMatches(intent state.LaunchIntent, requested bool) bool {
	if intent.CleanupDeleteBranchVerified {
		deleteBranch := requested && intent.ExpectedHead != ""
		return intent.CleanupDeleteBranchRequested != nil &&
			*intent.CleanupDeleteBranchRequested == requested && (!intent.CleanupDeleteBranch || deleteBranch)
	}
	if intent.CleanupDeleteBranchRequested != nil {
		return *intent.CleanupDeleteBranchRequested == requested
	}
	return requested || !intent.CleanupDeleteBranch
}

func intentMatchesPane(intent state.LaunchIntent, pane state.Pane, ownerRoot string) bool {
	return !slices.Contains([]bool{
		intent.Parent == pane.Parent, intent.RuntimeParent == pane.RuntimeParent,
		intent.OwnerProjectRoot == ownerRoot, intent.IssueNum == pane.IssueNum,
		intent.TaskID == pane.TaskID, intent.Slug == pane.Slug,
		intent.BranchName == pane.BranchName, intent.FullBranchRef == "refs/heads/"+pane.BranchName,
		intent.BaseBranch == pane.BaseBranch, filepath.Clean(intent.WorktreePath) == filepath.Clean(pane.WorktreePath),
		intent.WorkspaceLabel == pane.WorkspaceLabel, intent.Session == pane.SessionID,
		intent.SocketPath == pane.SocketPath, intent.BranchCreated == pane.BranchCreated,
		intent.BranchExisted == !pane.BranchCreated,
	}, false)
}

func cleanupResourceMatchesPane(intent state.LaunchIntent, pane state.Pane) bool {
	saved := resourceFromPane(pane)
	current := intent.Resource
	if current.Label != saved.Label ||
		filepath.Clean(current.CurrentPath) != filepath.Clean(saved.CurrentPath) ||
		filepath.Clean(current.RepoKey) != filepath.Clean(saved.RepoKey) ||
		filepath.Clean(current.RepoRoot) != filepath.Clean(saved.RepoRoot) {
		return false
	}
	// Reopen recovery and Herdr-side moves can replace the workspace and its
	// subordinate runtime IDs. A terminal-only mismatch remains invalid.
	return intent.Coordinator != (state.RuntimeResource{}) || current == saved ||
		current.WorkspaceID != saved.WorkspaceID
}

func driveWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
	worktreeIntentID string,
	lg Logger,
) error {
	intent, err := resumeWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent)
	if err != nil {
		return err
	}
	switch intent.Status {
	case state.IntentRealized:
		// Post-mutation Git cleanup continues below.
	case state.IntentPlanned:
		phaseCtx, cancel := context.WithDeadline(ctx, time.UnixMilli(intent.ExpiresUnixMS))
		intent, err = executeWorkspaceCleanupPhase(phaseCtx, opts, journal, runtime, intent)
		cancel()
	default:
		err = fmt.Errorf("unsupported Herdr cleanup status %q", intent.Status)
	}
	if err != nil {
		return err
	}
	return finalizeWorkspaceCleanup(ctx, opts.ProjectRoot, journal, runtime, pane, intent, worktreeIntentID, lg)
}

func resumeWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if intent.Status == state.IntentPlanned && intent.Failure == sharedAttachedWorkspaceCloseComplete {
		if time.Now().UnixMilli() >= intent.ExpiresUnixMS {
			intent.ExpiresUnixMS = time.Now().Add(workspaceCleanupTimeout).UnixMilli()
			return intent, saveWorkspaceCleanupIntent(journal, intent)
		}
		return intent, nil
	}
	if intent.Status == state.IntentPlanned && time.Now().UnixMilli() >= intent.ExpiresUnixMS {
		return recoverExpiredPlannedWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent)
	}
	if intent.Status == state.IntentPlanned {
		return replanCurrentWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent)
	}
	if intent.Status == state.IntentManualCleanupRequired || intent.Status == state.IntentIssued {
		return recoverWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent)
	}
	return intent, nil
}

func replanCurrentWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	observation, err := observeLabelBoundWorkspaceCleanup(ctx, runtime, opts.ProjectRoot, intent)
	if err != nil {
		return intent, err
	}
	return replanObservedWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent, observation)
}

func recoverExpiredPlannedWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	observation, err := observeLabelBoundWorkspaceCleanup(ctx, runtime, opts.ProjectRoot, intent)
	if err != nil {
		return intent, err
	}
	return recoverExpiredObservedWorkspaceCleanup(
		ctx, opts, locked, journal, runtime, pane, intent, observation,
	)
}

func recoverExpiredObservedWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
	observation workspaceCleanupObservation,
) (state.LaunchIntent, error) {
	if workspaceCleanupAbsent(observation) {
		return realizeReplannedWorkspaceCleanup(journal, intent)
	}
	intent, err := rebindObservedWorkspaceCleanupIdentity(
		locked, journal, opts.ProjectRoot, pane, intent, observation.workspace,
	)
	if err != nil {
		return intent, err
	}
	return settleExpiredObservedWorkspaceCleanup(
		ctx, opts, locked, journal, runtime, pane, intent, observation,
	)
}

func settleExpiredObservedWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
	observation workspaceCleanupObservation,
) (state.LaunchIntent, error) {
	if pane.RuntimeParent == panelaunch.ManualParentRef && workspaceCleanupCheckoutOnly(observation) {
		return replanObservedWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent, observation)
	}
	if intent.Coordinator != (state.RuntimeResource{}) && intent.CleanupPhase != state.CleanupReopen {
		return replanWorkspaceCleanup(ctx, opts, journal, intent, observation)
	}
	intent, err := replanObservedWorkspaceCleanup(
		ctx, opts, locked, journal, runtime, pane, intent, observation,
	)
	if err != nil {
		return intent, err
	}
	return intent, fmt.Errorf("saved Herdr cleanup intent expired before mutation; retry to continue the replanned cleanup")
}

func rebindMovedWorkspaceCleanupIdentity(
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	projectRoot string,
	pane state.Pane,
	resource state.RuntimeResource,
) error {
	worktreeIntentID, _, err := workspaceCleanupIntentIDs(projectRoot, pane)
	if err != nil {
		return err
	}
	if launchIntent, found := journal.FindIntent(worktreeIntentID); found {
		launchIntent.Resource = resource
		journal.UpsertIntent(launchIntent)
	}
	pane.WorkspaceID = resource.WorkspaceID
	pane.PaneID = resource.PaneID
	pane.TerminalID = resource.TerminalID
	return locked.RecordPane(pane)
}

func rebindObservedWorkspaceCleanupIdentity(
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	projectRoot string,
	pane state.Pane,
	intent state.LaunchIntent,
	workspace *backend.WorkspaceObservation,
) (state.LaunchIntent, error) {
	if workspace == nil || workspace.WorkspaceID == intent.Resource.WorkspaceID {
		return intent, nil
	}
	intent.Resource = adoptMovedWorkspaceCleanupResource(intent.Resource, *workspace)
	return intent, rebindMovedWorkspaceCleanupIdentity(locked, journal, projectRoot, pane, intent.Resource)
}

func replanWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	observation workspaceCleanupObservation,
) (state.LaunchIntent, error) {
	phase, err := classifyFreshWorkspaceCleanup(
		ctx,
		opts.ProjectRoot,
		intent.FullBranchRef,
		intent.Resource, observation, true,
	)
	if err != nil {
		return intent, err
	}
	_, expectedHead, branchFound, err := workspaceCleanupMetadata(
		ctx,
		opts.ProjectRoot,
		intent.Parent,
		intent.FullBranchRef,
	)
	if err != nil {
		return intent, err
	}
	intent.Status = state.IntentPlanned
	intent.CleanupPhase = phase
	intent.ExpectedHead = expectedHead
	intent = ratchetWorkspaceCleanupBranchDelete(intent, branchFound && workspaceCleanupCheckoutPresent(observation))
	intent.ExpiresUnixMS = time.Now().Add(workspaceCleanupTimeout).UnixMilli()
	intent.Failure = ""
	return intent, saveWorkspaceCleanupIntent(journal, intent)
}

func replanObservedWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
	observation workspaceCleanupObservation,
) (state.LaunchIntent, error) {
	if workspaceCleanupAbsent(observation) {
		return realizeReplannedWorkspaceCleanup(journal, intent)
	}
	intent, err := rebindObservedWorkspaceCleanupIdentity(
		locked, journal, opts.ProjectRoot, pane, intent, observation.workspace,
	)
	if err != nil {
		return intent, err
	}
	checkoutOnly := workspaceCleanupCheckoutOnly(observation)
	intent, err = attachReplannedWorkspaceCoordinatorIfNeeded(
		ctx, locked, journal, runtime, opts.ProjectRoot, pane, intent, checkoutOnly,
	)
	if err != nil {
		return intent, err
	}
	return replanWorkspaceCleanup(ctx, opts, journal, intent, observation)
}

func attachReplannedWorkspaceCoordinatorIfNeeded(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	projectRoot string,
	pane state.Pane,
	intent state.LaunchIntent,
	checkoutOnly bool,
) (state.LaunchIntent, error) {
	if !needsReplannedWorkspaceCoordinator(checkoutOnly, pane, intent) {
		return intent, nil
	}
	return attachReplannedWorkspaceCleanupCoordinator(
		ctx, locked, journal, runtime, projectRoot, pane, intent,
	)
}

func needsReplannedWorkspaceCoordinator(checkoutOnly bool, pane state.Pane, intent state.LaunchIntent) bool {
	manualReplan := pane.RuntimeParent == panelaunch.ManualParentRef
	return checkoutOnly && (intent.Coordinator == (state.RuntimeResource{}) || manualReplan)
}

func workspaceCleanupCheckoutOnly(observation workspaceCleanupObservation) bool {
	return observation.workspace == nil && workspaceCleanupCheckoutPresent(observation)
}

func finalizeWorkspaceCleanup(
	ctx context.Context,
	projectRoot string,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
	worktreeIntentID string,
	lg Logger,
) error {
	if intent.Status != state.IntentRealized {
		return fmt.Errorf("herdr cleanup did not reach a confirmed postcondition")
	}
	branchIntent := intent
	intent, err := consumeWorkspaceCleanupBranchDelete(journal, intent)
	if err != nil {
		return err
	}
	if branchErr := finishBranchCleanup(ctx, projectRoot, branchIntent); branchErr != nil {
		lg.Warn("%s: %v; leaving branch in place", paneLabel(pane), branchErr)
	}
	if err := discardSavedLaunchEnvironment(journal, runtime, worktreeIntentID); err != nil {
		return err
	}
	return nil
}

func discardSavedLaunchEnvironment(
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	worktreeIntentID string,
) error {
	intent, found := journal.FindIntent(worktreeIntentID)
	if !found {
		return nil
	}
	return runtime.DiscardWorkloadEnvironment(filepath.Dir(intent.SocketPath), intent.Launch)
}

func consumeWorkspaceCleanupBranchDelete(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if !intent.CleanupDeleteBranch {
		return intent, nil
	}
	intent = ratchetWorkspaceCleanupBranchDelete(intent, false)
	return intent, saveWorkspaceCleanupIntent(journal, intent)
}

func executeWorkspaceCleanupPhase(
	ctx context.Context,
	opts Options,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	switch intent.CleanupPhase {
	case state.CleanupReopen:
		return executeReopen(ctx, opts, journal, runtime, intent)
	case state.CleanupRemove:
		return executeRemove(ctx, opts, journal, runtime, intent)
	case state.CleanupWorkspaceClose:
		return executeWorkspaceClose(ctx, opts, journal, runtime, intent)
	default:
		return intent, fmt.Errorf("unknown Herdr cleanup phase %q", intent.CleanupPhase)
	}
}

func markWorkspaceCleanupManual(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	cause error,
) error {
	intent.Status = state.IntentManualCleanupRequired
	intent.Failure = cause.Error()
	journal.UpsertIntent(intent)
	return errors.Join(ErrManualCleanupRequired, cause, journal.Save())
}

func saveWorkspaceCleanupIntent(journal *state.LockedLaunchJournal, intent state.LaunchIntent) error {
	journal.UpsertIntent(intent)
	return journal.Save()
}

func workspaceCleanupAbsent(observation workspaceCleanupObservation) bool {
	return observation.workspace == nil && !workspaceCleanupCheckoutPresent(observation)
}

func realizeReplannedWorkspaceCleanup(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	intent = ratchetWorkspaceCleanupBranchDelete(intent, false)
	return realizeWorkspaceCleanup(journal, intent)
}

func ratchetWorkspaceCleanupBranchDelete(intent state.LaunchIntent, retain bool) state.LaunchIntent {
	if intent.CleanupDeleteBranch && !retain && intent.CleanupDeleteBranchRequested == nil {
		requested := true
		intent.CleanupDeleteBranchRequested = &requested
	}
	intent.CleanupDeleteBranch = intent.CleanupDeleteBranch && retain
	return intent
}

func finishBranchCleanup(ctx context.Context, projectRoot string, intent state.LaunchIntent) error {
	if !intent.CleanupDeleteBranch || !intent.BranchCreated || strings.TrimSpace(intent.ExpectedHead) == "" {
		return nil
	}
	current, found, err := worktree.ObserveBranch(ctx, projectRoot, intent.FullBranchRef)
	if err != nil {
		return fmt.Errorf("verify Herdr branch before compare-and-delete: %w", err)
	}
	if !found {
		return nil
	}
	if current != intent.ExpectedHead {
		return fmt.Errorf("herdr branch tip moved from %s to %s", intent.ExpectedHead, current)
	}
	if err := worktree.DeleteReservedBranch(ctx, projectRoot, intent.FullBranchRef, current); err != nil {
		return fmt.Errorf("compare-and-delete Herdr branch %s: %w", intent.BranchName, err)
	}
	return nil
}
