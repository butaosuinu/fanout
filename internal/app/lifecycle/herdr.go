package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const herdrCleanupTimeout = 5 * time.Minute

var ErrHerdrManualCleanupRequired = errors.New("herdr lifecycle requires manual cleanup")

// HerdrRuntime is the mutation surface lifecycle needs from one existing owned
// session. The composition root supplies a route-bound implementation.
type HerdrRuntime interface {
	VerifyOwned(context.Context) error
	VerifyWorktreeSetupPolicy(context.Context) error
	ObserveWorkspaces(context.Context) ([]herdrrun.WorkspaceObservation, error)
	OpenWorktree(context.Context, herdrrun.WorktreeOpenRequest) (herdrrun.WorktreeMutationResult, error)
	RemoveWorktree(context.Context, string, string) error
	CloseWorkspace(context.Context, string) error
}

type HerdrRuntimeFactory func(context.Context, state.Pane) (HerdrRuntime, error)

func validateHerdrMergeOperation(opts Options, pane state.Pane) error {
	if backend.NormalizeName(pane.Backend) != backend.Herdr {
		return nil
	}
	if err := validateHerdrPaneIdentity(pane); err != nil {
		return err
	}
	if opts.HerdrRuntime == nil {
		return fmt.Errorf("herdr lifecycle runtime is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), herdrCleanupTimeout)
	defer cancel()
	runtime, err := opts.HerdrRuntime(ctx, pane)
	if err != nil {
		return err
	}
	if err := runtime.VerifyOwned(ctx); err != nil {
		return err
	}
	return verifyHerdrMergeTarget(ctx, opts.ProjectRoot, runtime, pane)
}

func verifyHerdrMergeTarget(ctx context.Context, projectRoot string, runtime HerdrRuntime, pane state.Pane) error {
	resource := herdrResourceFromPane(pane)
	observation, err := observeHerdrCleanup(ctx, runtime, projectRoot, resource)
	if err != nil {
		return err
	}
	if observation.checkout.PathAbsent || !observation.checkout.Registered {
		return fmt.Errorf("saved Herdr merge checkout is absent or unregistered")
	}
	if observation.workspace != nil {
		if terminalErr := verifyHerdrTerminalInvalidation(*observation.workspace, resource); terminalErr != nil {
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
	return verifyHerdrCheckout(ctx, projectRoot, fullRef, expectedHead, resource)
}

func validateHerdrCloseOperation(opts Options, pane state.Pane, mode CloseMode, lg Logger) bool {
	switch {
	case !mode.removesWorktree():
		lg.Err("%s: Herdr child close must keep lifecycle ownership by removing its worktree", paneLabel(pane))
		return false
	case pane.IsShell() || pane.IsAttachedAgent():
		lg.Err("%s: Herdr lifecycle close supports owned child worktrees only", paneLabel(pane))
		return false
	case opts.HerdrRuntime == nil:
		lg.Err("%s: Herdr lifecycle runtime is not configured", paneLabel(pane))
		return false
	case validateHerdrPaneIdentity(pane) != nil:
		lg.Err("%s: saved Herdr lifecycle identity is incomplete; preserving workspace, worktree, and state", paneLabel(pane))
		return false
	default:
		return true
	}
}

func closeHerdrWorktree(
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	mode CloseMode,
	lg Logger,
) bool {
	ctx, cancel := context.WithTimeout(context.Background(), herdrCleanupTimeout)
	defer cancel()
	if err := runHerdrCleanup(ctx, opts, locked, pane, mode, lg); err != nil {
		lg.Err("%s: Herdr worktree cleanup failed; preserving state: %v", paneLabel(pane), err)
		return false
	}
	return true
}

func runHerdrCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	mode CloseMode,
	lg Logger,
) error {
	runtime, err := opts.HerdrRuntime(ctx, pane)
	if err != nil {
		return err
	}
	if verifyErr := runtime.VerifyOwned(ctx); verifyErr != nil {
		return verifyErr
	}
	journal, intent, worktreeIntentID, err := loadHerdrCleanupIntent(ctx, opts, locked, runtime, pane, mode)
	if err != nil {
		return err
	}
	return driveHerdrCleanup(ctx, opts, journal, runtime, pane, intent, worktreeIntentID, lg)
}

func loadHerdrCleanupIntent(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	runtime HerdrRuntime,
	pane state.Pane,
	mode CloseMode,
) (*state.LockedHerdrIntents, state.HerdrIntent, string, error) {
	journal, err := locked.HerdrIntents(opts.ProjectRoot)
	if err != nil {
		return nil, state.HerdrIntent{}, "", err
	}
	worktreeIntentID, intentID, err := herdrCleanupIntentIDs(opts.ProjectRoot, pane)
	if err != nil {
		return nil, state.HerdrIntent{}, "", err
	}
	if validationErr := validateHerdrLaunchIntentForCleanup(journal, worktreeIntentID, opts.ProjectRoot, pane); validationErr != nil {
		return nil, state.HerdrIntent{}, "", validationErr
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		intent, err = beginHerdrCleanup(ctx, opts, locked, runtime, pane, mode, intentID)
	} else {
		err = validateSavedHerdrCleanup(intent, opts.ProjectRoot, pane, mode)
	}
	if err != nil {
		return nil, state.HerdrIntent{}, "", err
	}
	return journal, intent, worktreeIntentID, nil
}

func herdrCleanupIntentIDs(projectRoot string, pane state.Pane) (string, string, error) {
	ownerRoot, err := state.HerdrOwnerProjectRoot(pane.Parent, filepath.Clean(projectRoot))
	if err != nil {
		return "", "", err
	}
	worktreeID, err := state.HerdrWorktreeIntentID(pane.Parent, ownerRoot, pane.IssueNum, pane.TaskID)
	if err != nil {
		return "", "", err
	}
	cleanupID, err := state.HerdrCleanupIntentID(worktreeID)
	return worktreeID, cleanupID, err
}

func validateHerdrLaunchIntentForCleanup(
	journal *state.LockedHerdrIntents,
	worktreeIntentID, projectRoot string,
	pane state.Pane,
) error {
	intent, found := journal.FindIntent(worktreeIntentID)
	if !found {
		return nil
	}
	ownerRoot, err := state.HerdrOwnerProjectRoot(pane.Parent, filepath.Clean(projectRoot))
	if err != nil {
		return err
	}
	allowedStatus := intent.Status == state.HerdrIntentRealized ||
		intent.Status == state.HerdrIntentManualCleanupRequired
	if slices.Contains([]bool{
		intent.Kind == state.HerdrIntentWorktree, allowedStatus, herdrIntentMatchesPane(intent, pane, ownerRoot),
		intent.Resource == herdrResourceFromPane(pane), intent.Launch != nil && intent.Launch.TokenIssued,
	}, false) {
		return fmt.Errorf("saved Herdr launch intent does not match the child row")
	}
	return nil
}

func beginHerdrCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	runtime HerdrRuntime,
	pane state.Pane,
	mode CloseMode,
	intentID string,
) (state.HerdrIntent, error) {
	resource := herdrResourceFromPane(pane)
	observation, err := observeHerdrCleanup(ctx, runtime, opts.ProjectRoot, resource)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	fullRef, err := worktree.LocalBranchRef(ctx, opts.ProjectRoot, pane.BranchName)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	intent, err := newHerdrCleanupIntent(ctx, opts, pane, mode, intentID, fullRef, resource, observation)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	if intent.CleanupPhase == state.HerdrCleanupReopen {
		intent, err = attachHerdrCleanupCoordinator(ctx, locked, runtime, opts.ProjectRoot, pane, intent)
		if err != nil {
			return state.HerdrIntent{}, err
		}
	}
	return persistNewHerdrCleanup(locked, opts.ProjectRoot, intent)
}

func persistNewHerdrCleanup(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	journal.UpsertIntent(intent)
	return intent, journal.Save()
}

func attachHerdrCleanupCoordinator(
	ctx context.Context,
	locked *state.LockedStore,
	runtime HerdrRuntime,
	projectRoot string,
	pane state.Pane,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	coordinator, err := findHerdrCoordinatorIntent(locked, projectRoot, pane)
	if err != nil {
		return intent, err
	}
	live, err := observeHerdrCoordinator(ctx, runtime, coordinator.Resource)
	if err != nil {
		return intent, err
	}
	intent.Coordinator = herdrCoordinatorResource(live)
	return intent, nil
}

func herdrCoordinatorResource(workspace herdrrun.WorkspaceObservation) state.HerdrResource {
	return state.HerdrResource{
		WorkspaceID: workspace.WorkspaceID,
		Label:       workspace.Label,
		PaneID:      workspace.Pane.Pane,
		TerminalID:  workspace.TerminalID,
		CurrentPath: filepath.Clean(workspace.CWD),
	}
}

func newHerdrCleanupIntent(
	ctx context.Context,
	opts Options,
	pane state.Pane,
	mode CloseMode,
	intentID, fullRef string,
	resource state.HerdrResource,
	observation herdrCleanupObservation,
) (state.HerdrIntent, error) {
	phase, err := classifyFreshHerdrCleanup(ctx, opts.ProjectRoot, fullRef, resource, observation)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	ownerRoot, expectedHead, branchFound, err := herdrCleanupMetadata(ctx, opts.ProjectRoot, pane.Parent, fullRef)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	intent := state.HerdrIntent{
		ID: intentID, Kind: state.HerdrIntentCleanup, Status: state.HerdrIntentPlanned,
		Parent: pane.Parent, RuntimeParent: pane.RuntimeParent, OwnerProjectRoot: ownerRoot,
		IssueNum: pane.IssueNum, TaskID: pane.TaskID, Slug: pane.Slug,
		BranchName: pane.BranchName, FullBranchRef: fullRef, BaseBranch: pane.BaseBranch,
		ExpectedHead: expectedHead, WorktreePath: resource.CurrentPath,
		BranchExisted: !pane.HerdrBranchCreated, BranchCreated: pane.HerdrBranchCreated,
		WorkspaceLabel: resource.Label, Resource: resource,
		Session: pane.HerdrSession, SocketPath: pane.HerdrSocketPath,
		ExpiresUnixMS: time.Now().Add(herdrCleanupTimeout).UnixMilli(),
		CleanupPhase:  phase, CleanupDeleteBranch: mode == CloseEverything && pane.HerdrBranchCreated && branchFound,
	}
	if observation.workspace == nil && observation.checkout.PathAbsent && !observation.checkout.Registered {
		intent.Status = state.HerdrIntentRealized
	}
	return intent, nil
}

func herdrCleanupMetadata(
	ctx context.Context,
	projectRoot, parent, fullRef string,
) (string, string, bool, error) {
	ownerRoot, err := state.HerdrOwnerProjectRoot(parent, filepath.Clean(projectRoot))
	if err != nil {
		return "", "", false, err
	}
	expectedHead, found, err := worktree.ObserveBranch(ctx, projectRoot, fullRef)
	return ownerRoot, expectedHead, found, err
}

func classifyFreshHerdrCleanup(
	ctx context.Context,
	projectRoot, fullRef string,
	resource state.HerdrResource,
	observation herdrCleanupObservation,
) (state.HerdrCleanupPhase, error) {
	workspacePresent := observation.workspace != nil
	checkoutPresent := !observation.checkout.PathAbsent || observation.checkout.Registered
	switch {
	case workspacePresent && checkoutPresent:
		if err := verifyHerdrTerminalInvalidation(*observation.workspace, resource); err != nil {
			return "", err
		}
		if err := verifyHerdrCheckout(ctx, projectRoot, fullRef, observation.checkout.HeadSHA, resource); err != nil {
			return "", err
		}
		return state.HerdrCleanupRemove, nil
	case workspacePresent:
		return state.HerdrCleanupWorkspaceClose, nil
	case checkoutPresent:
		if err := verifyHerdrCheckout(ctx, projectRoot, fullRef, observation.checkout.HeadSHA, resource); err != nil {
			return "", err
		}
		return state.HerdrCleanupReopen, nil
	default:
		return state.HerdrCleanupRemove, nil
	}
}

func validateSavedHerdrCleanup(intent state.HerdrIntent, projectRoot string, pane state.Pane, mode CloseMode) error {
	ownerRoot, err := state.HerdrOwnerProjectRoot(pane.Parent, filepath.Clean(projectRoot))
	if err != nil {
		return err
	}
	deleteBranch := mode == CloseEverything && pane.HerdrBranchCreated && intent.ExpectedHead != ""
	if slices.Contains([]bool{
		intent.Kind == state.HerdrIntentCleanup, herdrIntentMatchesPane(intent, pane, ownerRoot),
		intent.CleanupDeleteBranch == deleteBranch, herdrCleanupResourceMatchesPane(intent, pane),
	}, false) {
		return fmt.Errorf("saved Herdr cleanup intent does not match the selected state row")
	}
	return nil
}

func herdrIntentMatchesPane(intent state.HerdrIntent, pane state.Pane, ownerRoot string) bool {
	return !slices.Contains([]bool{
		intent.Parent == pane.Parent, intent.RuntimeParent == pane.RuntimeParent,
		intent.OwnerProjectRoot == ownerRoot, intent.IssueNum == pane.IssueNum,
		intent.TaskID == pane.TaskID, intent.Slug == pane.Slug,
		intent.BranchName == pane.BranchName, intent.FullBranchRef == "refs/heads/"+pane.BranchName,
		intent.BaseBranch == pane.BaseBranch, filepath.Clean(intent.WorktreePath) == filepath.Clean(pane.WorktreePath),
		intent.WorkspaceLabel == pane.HerdrWorkspaceLabel, intent.Session == pane.HerdrSession,
		intent.SocketPath == pane.HerdrSocketPath, intent.BranchCreated == pane.HerdrBranchCreated,
		intent.BranchExisted == !pane.HerdrBranchCreated,
	}, false)
}

func herdrCleanupResourceMatchesPane(intent state.HerdrIntent, pane state.Pane) bool {
	saved := herdrResourceFromPane(pane)
	current := intent.Resource
	if current.Label != saved.Label ||
		filepath.Clean(current.CurrentPath) != filepath.Clean(saved.CurrentPath) ||
		filepath.Clean(current.RepoKey) != filepath.Clean(saved.RepoKey) ||
		filepath.Clean(current.RepoRoot) != filepath.Clean(saved.RepoRoot) {
		return false
	}
	// A checkout-only recovery creates a replacement workspace. Its stable
	// nonce and Git provenance remain bound to the original row, while its
	// runtime IDs are intentionally replaced and recorded in the intent.
	return intent.Coordinator != (state.HerdrResource{}) || current == saved
}

func driveHerdrCleanup(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	pane state.Pane,
	intent state.HerdrIntent,
	worktreeIntentID string,
	lg Logger,
) error {
	intent, err := resumeHerdrCleanup(ctx, opts, journal, runtime, intent)
	if err != nil {
		return err
	}
	switch intent.Status {
	case state.HerdrIntentRealized:
		// Post-mutation Git cleanup continues below.
	case state.HerdrIntentPlanned:
		phaseCtx, cancel := context.WithDeadline(ctx, time.UnixMilli(intent.ExpiresUnixMS))
		intent, err = executeHerdrCleanupPhase(phaseCtx, opts, journal, runtime, intent)
		cancel()
	default:
		err = fmt.Errorf("unsupported Herdr cleanup status %q", intent.Status)
	}
	if err != nil {
		return err
	}
	return finalizeHerdrCleanup(ctx, opts.ProjectRoot, journal, pane, intent, worktreeIntentID, lg)
}

func resumeHerdrCleanup(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	if intent.Status == state.HerdrIntentPlanned && time.Now().UnixMilli() >= intent.ExpiresUnixMS {
		return recoverExpiredPlannedHerdrCleanup(ctx, opts, journal, runtime, intent)
	}
	if intent.Status == state.HerdrIntentManualCleanupRequired || intent.Status == state.HerdrIntentIssued {
		return recoverHerdrCleanup(ctx, opts, journal, runtime, intent)
	}
	return intent, nil
}

func recoverExpiredPlannedHerdrCleanup(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	observation, err := observeHerdrCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if err != nil {
		return intent, err
	}
	if herdrCleanupAbsent(observation) {
		return realizeHerdrCleanup(journal, intent)
	}
	journal.RemoveIntent(intent.ID)
	if err := journal.Save(); err != nil {
		return intent, err
	}
	return intent, fmt.Errorf("saved Herdr cleanup intent expired before mutation; retry to replan")
}

func finalizeHerdrCleanup(
	ctx context.Context,
	projectRoot string,
	journal *state.LockedHerdrIntents,
	pane state.Pane,
	intent state.HerdrIntent,
	worktreeIntentID string,
	lg Logger,
) error {
	if intent.Status != state.HerdrIntentRealized {
		return fmt.Errorf("herdr cleanup did not reach a confirmed postcondition")
	}
	if branchErr := finishHerdrBranchCleanup(ctx, projectRoot, intent); branchErr != nil {
		lg.Warn("%s: %v; leaving branch in place", paneLabel(pane), branchErr)
	}
	if err := discardSavedHerdrLaunchEnvironment(journal, worktreeIntentID); err != nil {
		return err
	}
	journal.RemoveIntent(intent.ID)
	journal.RemoveIntent(worktreeIntentID)
	return journal.Save()
}

func discardSavedHerdrLaunchEnvironment(journal *state.LockedHerdrIntents, worktreeIntentID string) error {
	intent, found := journal.FindIntent(worktreeIntentID)
	if !found {
		return nil
	}
	return herdrrun.DiscardWorkloadEnvironment(filepath.Dir(intent.SocketPath), intent.Launch)
}

func executeHerdrCleanupPhase(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	switch intent.CleanupPhase {
	case state.HerdrCleanupReopen:
		return executeHerdrReopen(ctx, opts, journal, runtime, intent)
	case state.HerdrCleanupRemove:
		return executeHerdrRemove(ctx, opts, journal, runtime, intent)
	case state.HerdrCleanupWorkspaceClose:
		return executeHerdrWorkspaceClose(ctx, opts, journal, runtime, intent)
	default:
		return intent, fmt.Errorf("unknown Herdr cleanup phase %q", intent.CleanupPhase)
	}
}

func markHerdrCleanupManual(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	cause error,
) error {
	intent.Status = state.HerdrIntentManualCleanupRequired
	intent.Failure = cause.Error()
	journal.UpsertIntent(intent)
	return errors.Join(ErrHerdrManualCleanupRequired, cause, journal.Save())
}

func saveHerdrCleanupIntent(journal *state.LockedHerdrIntents, intent state.HerdrIntent) error {
	journal.UpsertIntent(intent)
	return journal.Save()
}

func herdrCleanupAbsent(observation herdrCleanupObservation) bool {
	return observation.workspace == nil && observation.checkout.PathAbsent && !observation.checkout.Registered
}

func finishHerdrBranchCleanup(ctx context.Context, projectRoot string, intent state.HerdrIntent) error {
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
	if _, err := gitLifecycle(projectRoot, "merge-base", "--is-ancestor", current, "HEAD"); err != nil {
		return fmt.Errorf("herdr branch %s is not an ancestor of HEAD", intent.BranchName)
	}
	if err := worktree.DeleteReservedBranch(ctx, projectRoot, intent.FullBranchRef, current); err != nil {
		return fmt.Errorf("compare-and-delete Herdr branch %s: %w", intent.BranchName, err)
	}
	return nil
}
