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
		if !found || current != intent.ExpectedHead {
			// The adopted branch is not fanout-owned and the create was never
			// issued; release so a fresh retry records the current tip.
			return intent, releaseManagedIntent(locked, intent.ID, fmt.Errorf(
				"adopted Herdr branch moved from %s; retry launch", intent.ExpectedHead,
			))
		}
		if err := worktree.BranchAvailable(ctx, req.SourceRoot, intent.FullBranchRef); err != nil {
			return intent, err
		}
		return intent, nil
	}
	if intent.BranchCreated {
		if !found {
			// The reserved branch is gone while the create was never issued:
			// rollback is provably complete, so release and retry fresh.
			return intent, releaseManagedIntent(locked, intent.ID, fmt.Errorf(
				"reserved Herdr branch disappeared; retry launch",
			))
		}
		if current != intent.ExpectedHead {
			return intent, markManagedIntentManual(
				locked,
				intent,
				fmt.Errorf("reserved Herdr branch moved from %s", intent.ExpectedHead),
			)
		}
		return intent, nil
	}
	if found {
		return intent, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("herdr branch appeared before reservation completed"),
		)
	}
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
	rollbackCtx, cancel := context.WithTimeout(
		context.Background(),
		maxManagedRecoveryClassificationTimeout,
	)
	defer cancel()
	if !intent.BranchExisted && !intent.BranchCreated {
		// Rollback gets an independent finite budget when the launch context
		// is already canceled.
		_, found, err := worktree.ObserveBranch(rollbackCtx, req.SourceRoot, intent.FullBranchRef)
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
	}
	if intent.BranchCreated {
		if err := worktree.DeleteReservedBranch(
			rollbackCtx,
			req.SourceRoot,
			intent.FullBranchRef,
			intent.BaseSHA,
		); err != nil {
			if errors.Is(err, worktree.ErrBranchRollbackBlocked) {
				return markManagedIntentManual(locked, intent, errors.Join(mutationErr, err))
			}
			// The observation failed before the delete; retry later.
			return errors.Join(mutationErr, err)
		}
	}
	return releaseManagedIntent(locked, intent.ID, mutationErr)
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
		if err := validateWorkspacePostcondition(intent, nil, matches[0]); err != nil {
			return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, err)
		}
		resource := stateResource(matches[0])
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
	return ManagedRealizeResult{}, markManagedIntentManual(
		locked,
		intent,
		errors.Join(
			mutationErr,
			fmt.Errorf("herdr coordinator label has %d recovery matches", len(matches)),
		),
	)
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
	// A structured rejection proves the mutation created nothing; classify
	// from local Git state under an independent finite budget (the launch
	// context may already be exhausted).
	if errors.Is(mutationErr, backend.ErrMutationRejected) {
		recoveryCtx, cancel := context.WithTimeout(
			context.Background(),
			maxManagedRecoveryClassificationTimeout,
		)
		defer cancel()
		return ManagedRealizeResult{}, recoverRejectedManagedWorktree(
			recoveryCtx,
			locked,
			req,
			source,
			intent,
			mutationErr,
		)
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
		if err := finalizeManagedWorktree(ctx, locked, req, source, &intent, matches[0]); err != nil {
			return ManagedRealizeResult{}, handleManagedWorktreeFinalizeError(locked, intent, err)
		}
		return realizeDeferred(intent)
	}
	checkout, checkoutErr := worktree.ObserveCheckout(ctx, req.SourceRoot, intent.WorktreePath)
	if checkoutErr != nil {
		if errors.Is(checkoutErr, worktree.ErrCheckoutMismatch) {
			return ManagedRealizeResult{}, markManagedIntentManual(
				locked,
				intent,
				errors.Join(mutationErr, checkoutErr),
			)
		}
		return ManagedRealizeResult{}, errors.Join(mutationErr, checkoutErr)
	}
	if mutationErr == nil && intent.BranchCreated && len(matches) == 0 &&
		checkout.PathAbsent && !checkout.Registered {
		_, branchFound, branchErr := worktree.ObserveBranch(
			ctx,
			req.SourceRoot,
			intent.FullBranchRef,
		)
		if branchErr != nil {
			// The branch state was not classified; keep the intent retryable.
			return ManagedRealizeResult{}, branchErr
		}
		if !branchFound {
			return ManagedRealizeResult{}, releaseManagedIntent(locked, intent.ID, fmt.Errorf(
				"recovered completed Herdr worktree rollback; retry launch",
			))
		}
	}
	return ManagedRealizeResult{}, markManagedIntentManual(
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
		return markManagedIntentManual(
			locked,
			intent,
			errors.Join(mutationErr, verifyErr),
		)
	}
	checkout, checkoutErr := worktree.ObserveCheckout(ctx, req.SourceRoot, intent.WorktreePath)
	if checkoutErr != nil {
		if errors.Is(checkoutErr, worktree.ErrCheckoutMismatch) {
			return markManagedIntentManual(
				locked,
				intent,
				errors.Join(mutationErr, checkoutErr),
			)
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
		if err := worktree.DeleteReservedBranch(
			ctx,
			req.SourceRoot,
			intent.FullBranchRef,
			intent.BaseSHA,
		); err != nil {
			if errors.Is(err, worktree.ErrBranchRollbackBlocked) {
				return markManagedIntentManual(
					locked,
					intent,
					errors.Join(mutationErr, err),
				)
			}
			// The observation failed before the delete; retry later.
			return errors.Join(mutationErr, err)
		}
	}
	return releaseManagedIntent(locked, intent.ID, mutationErr)
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
		if !workspaceHasManagedResource(matches[0], intent.Resource) {
			return ManagedRealizeResult{}, markManagedIntentManual(
				locked,
				intent,
				fmt.Errorf("realized Herdr worktree identity changed"),
			)
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
			if errors.Is(err, worktree.ErrCheckoutMismatch) {
				return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, err)
			}
			return ManagedRealizeResult{}, err
		}
		return realizeDeferred(intent)
	case 0:
	default:
		return ManagedRealizeResult{}, markManagedIntentManual(
			locked,
			intent,
			fmt.Errorf("realized Herdr worktree label has %d live matches", len(matches)),
		)
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
		if errors.Is(err, worktree.ErrCheckoutMismatch) {
			return ManagedRealizeResult{}, markManagedIntentManual(locked, intent, err)
		}
		return ManagedRealizeResult{}, err
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
		if errors.Is(mutationErr, backend.ErrMutationNotIssued) {
			intent.Status = state.IntentRealized
			locked.UpsertIntent(intent)
			if saveErr := locked.Save(); saveErr != nil {
				return ManagedRealizeResult{}, errors.Join(mutationErr, saveErr)
			}
			return ManagedRealizeResult{}, mutationErr
		}
		if operationErr := ctx.Err(); operationErr != nil &&
			!errors.Is(mutationErr, backend.ErrMutationRejected) {
			return ManagedRealizeResult{}, errors.Join(mutationErr, operationErr)
		}
		return recoverManagedWorktree(ctx, runtime, locked, req, source, intent, mutationErr)
	}
	if finalizeErr := finalizeManagedWorktree(
		ctx,
		locked,
		req,
		source,
		&intent,
		mutation.WorkspaceObservation,
	); finalizeErr != nil {
		return ManagedRealizeResult{}, handleManagedWorktreeFinalizeError(locked, intent, finalizeErr)
	}
	return realizeDeferred(intent)
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
