package panelaunch

// Response-loss and crash recovery for Herdr realization. Classification
// follows the canon's three outcomes: adopt on a unique label match with
// matching Git postconditions, delete the intent only when non-issuance or
// completed rollback is proven, and otherwise fail closed with
// manual_cleanup_required.

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// releaseManagedIntent deletes an intent whose mutation is proven unissued or
// whose rollback is proven complete, and returns cause after the journal save.
func releaseManagedIntent(
	locked *state.LockedLaunchJournal,
	intentID string,
	cause error,
) error {
	locked.RemoveIntent(intentID)
	if err := locked.Save(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// absentRealizedManagedIntents classifies the whole active journal without
// writing it. Shutdown may release the returned set only after every later
// state and workspace preflight also succeeds, so a rejected shutdown never
// leaves a partially consumed recovery journal.
func absentRealizedManagedIntents(
	ctx context.Context,
	journal state.LaunchJournal,
	observe func(context.Context) ([]backend.WorkspaceObservation, error),
) ([]state.LaunchIntent, bool) {
	candidates, allRealized := allManagedIntentsRealized(journal)
	if !allRealized {
		return nil, false
	}
	if len(candidates) == 0 {
		return nil, true
	}
	workspaces, err := observe(ctx)
	if err != nil {
		return nil, false
	}
	if !allRealizedManagedIntentsAbsent(ctx, candidates, workspaces) {
		return nil, false
	}
	return candidates, true
}

func allRealizedManagedIntentsAbsent(
	ctx context.Context,
	intents []state.LaunchIntent,
	workspaces []backend.WorkspaceObservation,
) bool {
	for _, intent := range intents {
		absent, classifyErr := realizedManagedIntentAbsent(ctx, intent, workspaces)
		if classifyErr != nil || !absent {
			return false
		}
	}
	return true
}

func allManagedIntentsRealized(journal state.LaunchJournal) ([]state.LaunchIntent, bool) {
	candidates := realizedManagedIntents(journal)
	return candidates, len(candidates) == len(journal.Intents)
}

func realizedManagedIntents(journal state.LaunchJournal) []state.LaunchIntent {
	var intents []state.LaunchIntent
	for _, intent := range journal.Intents {
		if intent.Status == state.IntentRealized {
			intents = append(intents, intent)
		}
	}
	return intents
}

func realizedManagedIntentAbsent(
	ctx context.Context,
	intent state.LaunchIntent,
	workspaces []backend.WorkspaceObservation,
) (bool, error) {
	if realizedManagedRuntimePresent(workspaces, intent) {
		return false, nil
	}
	switch intent.Kind {
	case state.IntentCoordinator:
		return true, nil
	case state.IntentWorktree, state.IntentResume:
		checkout, err := worktree.ObserveCheckout(ctx, intent.Resource.RepoRoot, intent.WorktreePath)
		return err == nil && checkout.PathAbsent && !checkout.Registered, err
	default:
		return false, nil
	}
}

func realizedManagedRuntimePresent(
	workspaces []backend.WorkspaceObservation,
	intent state.LaunchIntent,
) bool {
	return len(workspacesWithLabel(workspaces, intent.WorkspaceLabel)) != 0 ||
		managedWorkspaceIDPresent(workspaces, intent.Resource.WorkspaceID)
}

func managedWorkspaceIDPresent(
	workspaces []backend.WorkspaceObservation,
	workspaceID string,
) bool {
	return slices.ContainsFunc(workspaces, func(workspace backend.WorkspaceObservation) bool {
		return workspace.WorkspaceID == workspaceID
	})
}

func releaseAbsentManagedIntents(
	locked *state.LockedLaunchJournal,
	intents []state.LaunchIntent,
) error {
	if len(intents) == 0 {
		return nil
	}
	for _, intent := range intents {
		if !locked.RemoveIntent(intent.ID) {
			return fmt.Errorf("realized Herdr intent %s disappeared before release", intent.ID)
		}
	}
	return locked.Save()
}

func ensureManagedBranchReservation(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	current, found, err := worktree.ObserveBranch(ctx, req.SourceRoot, intent.FullBranchRef)
	if err != nil {
		return intent, err
	}
	if intent.BranchExisted {
		return intent, verifyAdoptedManagedBranch(ctx, locked, req, intent, current, found)
	}
	if intent.BranchCreated {
		return intent, verifyReservedManagedBranch(locked, intent, current, found)
	}
	if found {
		return intent, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("herdr branch appeared before reservation completed"),
		)
	}
	return reserveManagedBranch(ctx, locked, req, intent)
}

// verifyAdoptedManagedBranch re-checks a branch the launch adopted rather than
// created: it must still sit at the tip the intent recorded and carry no
// checkout.
func verifyAdoptedManagedBranch(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	intent state.LaunchIntent,
	current string,
	found bool,
) error {
	if !found || current != intent.ExpectedHead {
		// The adopted branch is not fanout-owned and the create was never
		// issued; release so a fresh retry records the current tip.
		return releaseManagedIntent(locked, intent.ID, fmt.Errorf(
			"adopted Herdr branch moved from %s; retry launch", intent.ExpectedHead,
		))
	}
	return worktree.BranchAvailable(ctx, req.SourceRoot, intent.FullBranchRef)
}

// verifyReservedManagedBranch re-checks a branch this launch reserved: a
// vanished branch proves a completed rollback, a moved one needs a human.
func verifyReservedManagedBranch(
	locked *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	current string,
	found bool,
) error {
	if !found {
		// The reserved branch is gone while the create was never issued:
		// rollback is provably complete, so release and retry fresh.
		return releaseManagedIntent(locked, intent.ID, fmt.Errorf(
			"reserved Herdr branch disappeared; retry launch",
		))
	}
	if current != intent.ExpectedHead {
		return markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("reserved Herdr branch moved from %s", intent.ExpectedHead),
		)
	}
	return nil
}

// reserveManagedBranch creates the child branch and persists the reservation,
// classifying a failure whose branch state is ambiguous as manual cleanup.
func reserveManagedBranch(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if err := worktree.ReserveBranch(ctx, req.SourceRoot, intent.FullBranchRef, intent.BaseSHA); err != nil {
		current, found, observeErr := worktree.ObserveBranch(ctx, req.SourceRoot, intent.FullBranchRef)
		if observeErr != nil {
			// The branch state was not classified; keep the intent retryable.
			return intent, errors.Join(err, observeErr)
		}
		if found {
			return intent, markManagedIntentManual(
				locked,
				intent,
				fmt.Errorf("herdr branch reservation result is ambiguous at %s", current),
			)
		}
		return intent, releaseManagedIntent(locked, intent.ID, err)
	}
	intent.BranchCreated = true
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return intent, err
	}
	return intent, nil
}

func rollbackUnissuedManagedWorktree(
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	intent state.LaunchIntent,
	mutationErr error,
) error {
	// Rollback gets an independent finite budget when the launch context
	// is already canceled.
	rollbackCtx, cancel := context.WithTimeout(
		context.Background(),
		maxManagedRecoveryClassificationTimeout,
	)
	defer cancel()
	if !intent.BranchExisted && !intent.BranchCreated {
		if err := verifyUnownedManagedBranchAbsent(rollbackCtx, locked, req, intent, mutationErr); err != nil {
			return err
		}
	}
	if intent.BranchCreated {
		if err := deleteReservedManagedBranch(rollbackCtx, locked, req, intent, mutationErr); err != nil {
			return err
		}
	}
	return releaseManagedIntent(locked, intent.ID, mutationErr)
}

// verifyUnownedManagedBranchAbsent fails closed when a branch stands where the
// journal recorded no ownership. A nil return means the rollback may proceed.
func verifyUnownedManagedBranchAbsent(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	intent state.LaunchIntent,
	mutationErr error,
) error {
	_, found, err := worktree.ObserveBranch(ctx, req.SourceRoot, intent.FullBranchRef)
	if err != nil {
		// The branch state was not classified; keep the intent retryable.
		return errors.Join(mutationErr, err)
	}
	if found {
		return markManagedIntentManual(locked, intent, errors.Join(
			mutationErr,
			fmt.Errorf("herdr branch exists without persisted ownership"),
		))
	}
	return nil
}

// deleteReservedManagedBranch drops the branch this launch reserved. A nil
// return means the rollback may proceed.
func deleteReservedManagedBranch(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	intent state.LaunchIntent,
	mutationErr error,
) error {
	err := worktree.DeleteReservedBranch(
		ctx,
		req.SourceRoot,
		intent.FullBranchRef,
		intent.BaseSHA,
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, worktree.ErrBranchRollbackBlocked) {
		return markManagedIntentManual(locked, intent, errors.Join(mutationErr, err))
	}
	// The observation failed before the delete; retry later.
	return errors.Join(mutationErr, err)
}

func recoverManagedCoordinator(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	requestSource worktree.RepoIdentity,
	mutationErr error,
) (ManagedRealizeResult, error) {
	// A structured rejection proves the workspace was not created; release
	// the intent without depending on a snapshot that may fail transiently.
	if errors.Is(mutationErr, backend.ErrMutationRejected) {
		return ManagedRealizeResult{}, releaseManagedIntent(locked, intent.ID, mutationErr)
	}
	// A failed snapshot classifies nothing: keep the issued intent so the
	// next run can classify it (canon: adoption or fail-closed needs an
	// observed state, not an observation failure).
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return ManagedRealizeResult{}, errors.Join(
			mutationErr,
			fmt.Errorf("observe Herdr coordinator recovery: %w", err),
			ctx.Err(),
		)
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) == 1 {
		return adoptRecoveredManagedCoordinator(ctx, locked, intent, requestSource, matches[0], mutationErr)
	}
	return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, errors.Join(
		mutationErr,
		fmt.Errorf("herdr coordinator label has %d recovery matches", len(matches)),
	))
}

// adoptRecoveredManagedCoordinator adopts the single workspace carrying the
// intent's label after a lost coordinator create response.
func adoptRecoveredManagedCoordinator(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	requestSource worktree.RepoIdentity,
	match backend.WorkspaceObservation,
	mutationErr error,
) (ManagedRealizeResult, error) {
	if err := validateWorkspacePostcondition(intent, nil, match); err != nil {
		return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, err)
	}
	resource := stateResource(match)
	if _, sourceErr := managedCoordinatorSource(ctx, resource, requestSource); sourceErr != nil {
		if errors.Is(sourceErr, errManagedRealizedIdentityChanged) {
			return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, sourceErr)
		}
		return ManagedRealizeResult{}, errors.Join(mutationErr, sourceErr)
	}
	intent.Resource = resource
	intent.Status = state.IntentRealized
	intent.Failure = ""
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return ManagedRealizeResult{}, err
	}
	return realizeDeferred(intent)
}

func recoverManagedWorktree(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	mutationErr error,
) (ManagedRealizeResult, error) {
	if errors.Is(mutationErr, backend.ErrMutationRejected) {
		return ManagedRealizeResult{}, classifyRejectedManagedWorktree(locked, req, source, intent, mutationErr)
	}
	// A failed snapshot classifies nothing: keep the issued intent so the
	// next run can classify it.
	workspaces, observeErr := runtime.ObserveWorkspaces(ctx)
	if observeErr != nil {
		return ManagedRealizeResult{}, errors.Join(
			mutationErr,
			fmt.Errorf("observe Herdr worktree recovery: %w", observeErr),
			ctx.Err(),
		)
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) == 1 {
		return adoptRecoveredManagedWorktree(ctx, locked, req, source, intent, matches[0])
	}
	return classifyLostManagedWorktree(ctx, locked, req, intent, len(matches), mutationErr)
}

// classifyRejectedManagedWorktree runs the local-Git classification of a
// structured rejection under an independent finite budget: the rejection proves
// the mutation created nothing, but the launch context may already be
// exhausted.
func classifyRejectedManagedWorktree(
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	mutationErr error,
) error {
	recoveryCtx, cancel := context.WithTimeout(
		context.Background(),
		maxManagedRecoveryClassificationTimeout,
	)
	defer cancel()
	return recoverRejectedManagedWorktree(recoveryCtx, locked, req, source, intent, mutationErr)
}

// adoptRecoveredManagedWorktree finalizes the single workspace carrying the
// intent's label after a lost worktree create response.
func adoptRecoveredManagedWorktree(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	match backend.WorkspaceObservation,
) (ManagedRealizeResult, error) {
	if err := finalizeManagedWorktree(ctx, locked, req, source, &intent, match); err != nil {
		return ManagedRealizeResult{}, handleManagedWorktreeFinalizeError(locked, intent, err)
	}
	return realizeDeferred(intent)
}

// classifyLostManagedWorktree decides what a create with no unique label match
// left behind: an unread checkout stays retryable, a provably completed
// rollback releases the intent, anything else needs a human.
func classifyLostManagedWorktree(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	intent state.LaunchIntent,
	matchCount int,
	mutationErr error,
) (ManagedRealizeResult, error) {
	checkout, checkoutErr := worktree.ObserveCheckout(ctx, req.SourceRoot, intent.WorktreePath)
	if checkoutErr != nil {
		if errors.Is(checkoutErr, worktree.ErrCheckoutMismatch) {
			return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, errors.Join(mutationErr, checkoutErr))
		}
		return ManagedRealizeResult{}, errors.Join(mutationErr, checkoutErr)
	}
	if mutationErr == nil && intent.BranchCreated && matchCount == 0 &&
		checkout.PathAbsent && !checkout.Registered {
		if done, err := releasedRolledBackManagedWorktree(ctx, locked, req, intent); done {
			return ManagedRealizeResult{}, err
		}
	}
	return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, errors.Join(
		mutationErr,
		fmt.Errorf(
			"herdr worktree recovery has %d label matches and checkout absent=%t registered=%t",
			matchCount,
			checkout.PathAbsent,
			checkout.Registered,
		),
	))
}

// releasedRolledBackManagedWorktree reports whether the branch reservation is
// provably gone — a completed rollback — and the terminal error that settles
// the intent. done is false when classification must continue.
func releasedRolledBackManagedWorktree(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	intent state.LaunchIntent,
) (bool, error) {
	_, branchFound, branchErr := worktree.ObserveBranch(
		ctx,
		req.SourceRoot,
		intent.FullBranchRef,
	)
	if branchErr != nil {
		// The branch state was not classified; keep the intent retryable.
		return true, branchErr
	}
	if !branchFound {
		return true, releaseManagedIntent(locked, intent.ID, fmt.Errorf(
			"recovered completed Herdr worktree rollback; retry launch",
		))
	}
	return false, nil
}

// recoverRejectedManagedWorktree classifies a structured rejection from local
// Git state: restore a still-valid realized checkout, or release the reserved
// branch and the intent when nothing was created.
func recoverRejectedManagedWorktree(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	mutationErr error,
) error {
	if intent.Resource.WorkspaceID != "" {
		return restoreRejectedManagedWorktree(ctx, locked, req, source, intent, mutationErr)
	}
	checkout, checkoutErr := worktree.ObserveCheckout(ctx, req.SourceRoot, intent.WorktreePath)
	if checkoutErr != nil {
		if errors.Is(checkoutErr, worktree.ErrCheckoutMismatch) {
			return markManagedIntentManual(locked, intent, errors.Join(mutationErr, checkoutErr))
		}
		return errors.Join(mutationErr, checkoutErr)
	}
	if !checkout.PathAbsent || checkout.Registered {
		return markManagedIntentManual(
			locked,
			intent,
			errors.Join(mutationErr, fmt.Errorf("checkout exists after rejected Herdr create")),
		)
	}
	if intent.BranchCreated {
		if err := deleteReservedManagedBranch(ctx, locked, req, intent, mutationErr); err != nil {
			return err
		}
	}
	return releaseManagedIntent(locked, intent.ID, mutationErr)
}

// restoreRejectedManagedWorktree restores a realized checkout the rejected
// create left intact, and fails closed when it no longer verifies.
func restoreRejectedManagedWorktree(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	mutationErr error,
) error {
	_, verifyErr := worktree.VerifyCheckout(
		ctx,
		req.SourceRoot,
		intent.WorktreePath,
		intent.FullBranchRef,
		intent.ExpectedHead,
		source.RepoKey,
		source.RepoRoot,
	)
	if verifyErr == nil {
		intent.Status = state.IntentRealized
		intent.Failure = ""
		locked.UpsertIntent(intent)
		if saveErr := locked.Save(); saveErr != nil {
			return errors.Join(mutationErr, saveErr)
		}
		return mutationErr
	}
	if !errors.Is(verifyErr, worktree.ErrCheckoutMismatch) {
		// The verification itself failed; nothing was classified.
		return errors.Join(mutationErr, verifyErr)
	}
	return markManagedIntentManual(locked, intent, errors.Join(mutationErr, verifyErr))
}

func finalizeManagedWorktree(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent *state.LaunchIntent,
	observation backend.WorkspaceObservation,
) error {
	if err := validateWorkspacePostcondition(*intent, &source, observation); err != nil {
		// The snapshot succeeded, so the mismatch is confirmed.
		return errors.Join(errManagedRealizedIdentityChanged, err)
	}
	if _, err := worktree.VerifyCheckout(
		ctx,
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
	intent.Status = state.IntentRealized
	intent.Failure = ""
	locked.UpsertIntent(*intent)
	if saveErr := locked.Save(); saveErr != nil {
		return errors.Join(errManagedRealizedIntentSave, saveErr)
	}
	return nil
}

func handleManagedWorktreeFinalizeError(
	locked *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	err error,
) error {
	if errors.Is(err, errManagedRealizedIdentityChanged) || errors.Is(err, worktree.ErrCheckoutMismatch) {
		return markManagedIntentManual(locked, intent, err)
	}
	// Save failures and transient Git reads classified nothing; keep the
	// intent retryable.
	return err
}

func verifyRealizedCoordinator(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
	intent state.LaunchIntent,
	requestSource worktree.RepoIdentity,
) error {
	if _, err := managedCoordinatorSource(ctx, intent.Resource, requestSource); err != nil {
		return err
	}
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return err
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) != 1 || !workspaceHasManagedResource(matches[0], intent.Resource) {
		return fmt.Errorf("%w: coordinator", errManagedRealizedIdentityChanged)
	}
	return nil
}

func resumeRealizedManagedWorktree(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	allowOpen bool,
) (ManagedRealizeResult, error) {
	// A failed snapshot classifies nothing: keep the realized intent
	// retryable instead of pinning it to manual cleanup.
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return ManagedRealizeResult{}, errors.Join(err, ctx.Err())
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	switch len(matches) {
	case 1:
		return adoptLiveManagedWorktree(ctx, locked, req, source, intent, matches[0])
	case 0:
		return reviveRealizedManagedWorktree(ctx, runtime, locked, req, source, intent, workspaces, allowOpen)
	default:
		return ManagedRealizeResult{}, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("realized Herdr worktree label has %d live matches", len(matches)),
		)
	}
}

// adoptLiveManagedWorktree keeps a realized intent whose workspace is still
// live, once its identity and checkout both re-verify.
func adoptLiveManagedWorktree(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	match backend.WorkspaceObservation,
) (ManagedRealizeResult, error) {
	if !workspaceHasManagedResource(match, intent.Resource) {
		return ManagedRealizeResult{}, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("realized Herdr worktree identity changed"),
		)
	}
	if err := verifyRealizedManagedCheckout(ctx, locked, req, source, intent); err != nil {
		return ManagedRealizeResult{}, err
	}
	return realizeDeferred(intent)
}

// verifyRealizedManagedCheckout re-verifies the recorded checkout and pins the
// intent to manual cleanup only when the mismatch is confirmed.
func verifyRealizedManagedCheckout(
	ctx context.Context,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
) error {
	if _, err := worktree.VerifyCheckout(
		ctx,
		req.SourceRoot,
		intent.WorktreePath,
		intent.FullBranchRef,
		intent.ExpectedHead,
		source.RepoKey,
		source.RepoRoot,
	); err != nil {
		if errors.Is(err, worktree.ErrCheckoutMismatch) {
			return markManagedIntentManual(locked, intent, err)
		}
		return err
	}
	return nil
}

// reviveRealizedManagedWorktree handles a realized intent whose workspace is
// gone: the checkout and the coordinator must still stand, and only a launch
// still inside its deadline may reopen the workspace.
func reviveRealizedManagedWorktree(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	workspaces []backend.WorkspaceObservation,
	allowOpen bool,
) (ManagedRealizeResult, error) {
	if checkoutErr := verifyRealizedManagedCheckout(ctx, locked, req, source, intent); checkoutErr != nil {
		return ManagedRealizeResult{}, checkoutErr
	}
	if coordinatorErr := verifyCoordinatorObservation(intent.Coordinator, workspaces); coordinatorErr != nil {
		return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, coordinatorErr)
	}
	if !allowOpen {
		return ManagedRealizeResult{}, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("expired realized Herdr worktree has no live workspace"),
		)
	}
	if policyErr := runtime.VerifyWorktreeSetupPolicy(ctx); policyErr != nil {
		return ManagedRealizeResult{}, policyErr
	}
	return reopenRealizedManagedWorktree(ctx, runtime, locked, req, source, intent)
}

// reopenRealizedManagedWorktree re-issues the intent and opens the recorded
// checkout as a fresh workspace.
func reopenRealizedManagedWorktree(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
) (ManagedRealizeResult, error) {
	intent.Status = state.IntentIssued
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		return ManagedRealizeResult{}, saveErr
	}
	mutation, mutationErr := runtime.OpenWorktree(ctx, backend.WorktreeOpenRequest{
		Coordinator:              observationResource(intent.Coordinator),
		SourceRepoKey:            source.RepoKey,
		SourceRepoRoot:           source.RepoRoot,
		Path:                     intent.WorktreePath,
		Label:                    intent.WorkspaceLabel,
		ExpectedAlreadyOpenID:    intent.Resource.WorkspaceID,
		ExpectedAlreadyOpenLabel: intent.Resource.Label,
	})
	if mutationErr != nil {
		return classifyManagedWorktreeOpenError(ctx, runtime, locked, req, source, intent, mutationErr)
	}
	finalizeErr := finalizeManagedWorktree(ctx, locked, req, source, &intent, mutation.WorkspaceObservation)
	if finalizeErr != nil {
		return ManagedRealizeResult{}, handleManagedWorktreeFinalizeError(locked, intent, finalizeErr)
	}
	return realizeDeferred(intent)
}

// classifyManagedWorktreeOpenError routes a failed reopen: restore the realized
// status on proven non-issuance, keep an unclassified expiry retryable, and
// otherwise recover from the observed label.
func classifyManagedWorktreeOpenError(
	ctx context.Context,
	runtime ManagedWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
	mutationErr error,
) (ManagedRealizeResult, error) {
	if errors.Is(mutationErr, backend.ErrMutationNotIssued) {
		intent.Status = state.IntentRealized
		locked.UpsertIntent(intent)
		if saveErr := locked.Save(); saveErr != nil {
			return ManagedRealizeResult{}, errors.Join(mutationErr, saveErr)
		}
		return ManagedRealizeResult{}, mutationErr
	}
	if expiryErr := unclassifiedManagedMutationExpiry(ctx, mutationErr); expiryErr != nil {
		return ManagedRealizeResult{}, expiryErr
	}
	return recoverManagedWorktree(ctx, runtime, locked, req, source, intent, mutationErr)
}

// unclassifiedManagedMutationExpiry reports the joined error for a mutation
// that failed after its operation context expired without a structured
// rejection to classify from. A structured rejection is a durable non-creation
// proof, so it is classified even past the deadline.
func unclassifiedManagedMutationExpiry(ctx context.Context, mutationErr error) error {
	if operationErr := ctx.Err(); operationErr != nil &&
		!errors.Is(mutationErr, backend.ErrMutationRejected) {
		return errors.Join(mutationErr, operationErr)
	}
	return nil
}

func markManagedIntentManual(
	locked *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	cause error,
) error {
	reason := "result is indeterminate"
	if cause != nil {
		reason = cause.Error()
	}
	intent.Status = state.IntentManualCleanupRequired
	intent.Failure = reason
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return errors.Join(
			fmt.Errorf("%w: %s", ErrManualCleanupRequired, reason),
			err,
		)
	}
	return fmt.Errorf("%w: %s", ErrManualCleanupRequired, reason)
}

func manualCleanupError(intent state.LaunchIntent) error {
	return fmt.Errorf("%w: %s", ErrManualCleanupRequired, intent.Failure)
}
