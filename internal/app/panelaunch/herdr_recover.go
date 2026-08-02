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

	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// releaseHerdrIntent deletes an intent whose mutation is proven unissued or
// whose rollback is proven complete, and returns cause after the journal save.
func releaseHerdrIntent(
	locked *state.LockedHerdrIntents,
	intentID string,
	cause error,
) error {
	locked.RemoveIntent(intentID)
	if err := locked.Save(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func ensureHerdrBranchReservation(
	ctx context.Context,
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	current, found, err := worktree.ObserveBranch(ctx, req.SourceRoot, intent.FullBranchRef)
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
		if err := worktree.BranchAvailable(ctx, req.SourceRoot, intent.FullBranchRef); err != nil {
			return intent, err
		}
		return intent, nil
	}
	if intent.BranchCreated {
		if !found {
			// The reserved branch is gone while the create was never issued:
			// rollback is provably complete, so release and retry fresh.
			return intent, releaseHerdrIntent(locked, intent.ID, fmt.Errorf(
				"reserved Herdr branch disappeared; retry launch",
			))
		}
		if current != intent.ExpectedHead {
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
	if err := worktree.ReserveBranch(req.SourceRoot, intent.FullBranchRef, intent.BaseSHA); err != nil {
		current, found, observeErr := worktree.ObserveBranch(ctx, req.SourceRoot, intent.FullBranchRef)
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
		return intent, releaseHerdrIntent(locked, intent.ID, err)
	}
	intent.BranchCreated = true
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		return intent, err
	}
	return intent, nil
}

func rollbackUnissuedHerdrWorktree(
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	intent state.HerdrIntent,
	mutationErr error,
) error {
	if !intent.BranchExisted && !intent.BranchCreated {
		// Rollback must complete even when the launch context is canceled.
		_, found, err := worktree.ObserveBranch(context.Background(), req.SourceRoot, intent.FullBranchRef)
		if err != nil || found {
			cause := err
			if cause == nil {
				cause = fmt.Errorf("herdr branch exists without persisted ownership")
			}
			return markHerdrIntentManual(locked, intent, errors.Join(mutationErr, cause))
		}
	}
	if intent.BranchCreated {
		if err := worktree.DeleteReservedBranch(
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
	return releaseHerdrIntent(locked, intent.ID, mutationErr)
}

func recoverHerdrCoordinator(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	mutationErr error,
) (HerdrCoordinatorResult, error) {
	// A structured rejection proves the workspace was not created; release
	// the intent without depending on a snapshot that may fail transiently.
	if errors.Is(mutationErr, herdrrun.ErrMutationRejected) {
		return HerdrCoordinatorResult{}, releaseHerdrIntent(locked, intent.ID, mutationErr)
	}
	// A failed snapshot classifies nothing: keep the issued intent so the
	// next run can classify it (canon: adoption or fail-closed needs an
	// observed state, not an observation failure).
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return HerdrCoordinatorResult{}, errors.Join(
			mutationErr,
			fmt.Errorf("observe Herdr coordinator recovery: %w", err),
			ctx.Err(),
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
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.HerdrIntent,
	mutationErr error,
) (HerdrWorktreeResult, error) {
	// A structured rejection proves the mutation created nothing; classify
	// from local Git state without depending on a snapshot.
	if errors.Is(mutationErr, herdrrun.ErrMutationRejected) {
		return recoverRejectedHerdrWorktree(ctx, locked, req, source, intent, mutationErr)
	}
	// A failed snapshot classifies nothing: keep the issued intent so the
	// next run can classify it.
	workspaces, observeErr := runtime.ObserveWorkspaces(ctx)
	if observeErr != nil {
		return HerdrWorktreeResult{}, errors.Join(
			mutationErr,
			fmt.Errorf("observe Herdr worktree recovery: %w", observeErr),
			ctx.Err(),
		)
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) == 1 {
		if err := finalizeHerdrWorktree(ctx, locked, req, source, &intent, matches[0]); err != nil {
			return HerdrWorktreeResult{}, handleHerdrWorktreeFinalizeError(locked, intent, err)
		}
		return worktreeDeferred(intent)
	}
	checkout, checkoutErr := worktree.ObserveCheckout(ctx, req.SourceRoot, intent.WorktreePath)
	if checkoutErr != nil {
		return HerdrWorktreeResult{}, errors.Join(mutationErr, checkoutErr)
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
			return HerdrWorktreeResult{}, branchErr
		}
		if !branchFound {
			return HerdrWorktreeResult{}, releaseHerdrIntent(locked, intent.ID, fmt.Errorf(
				"recovered completed Herdr worktree rollback; retry launch",
			))
		}
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

// recoverRejectedHerdrWorktree classifies a structured rejection from local
// Git state: restore a still-valid realized checkout, or release the reserved
// branch and the intent when nothing was created.
func recoverRejectedHerdrWorktree(
	ctx context.Context,
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.HerdrIntent,
	mutationErr error,
) (HerdrWorktreeResult, error) {
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
			intent.Status = state.HerdrIntentRealized
			intent.Failure = ""
			locked.UpsertIntent(intent)
			if saveErr := locked.Save(); saveErr != nil {
				return HerdrWorktreeResult{}, errors.Join(mutationErr, saveErr)
			}
			return HerdrWorktreeResult{}, mutationErr
		}
		if !errors.Is(verifyErr, worktree.ErrCheckoutMismatch) {
			// The verification itself failed; nothing was classified.
			return HerdrWorktreeResult{}, errors.Join(mutationErr, verifyErr)
		}
		return HerdrWorktreeResult{}, markHerdrIntentManual(
			locked,
			intent,
			errors.Join(mutationErr, verifyErr),
		)
	}
	checkout, checkoutErr := worktree.ObserveCheckout(ctx, req.SourceRoot, intent.WorktreePath)
	if checkoutErr != nil {
		return HerdrWorktreeResult{}, errors.Join(mutationErr, checkoutErr)
	}
	if !checkout.PathAbsent || checkout.Registered {
		return HerdrWorktreeResult{}, markHerdrIntentManual(
			locked,
			intent,
			errors.Join(mutationErr, fmt.Errorf("checkout exists after rejected Herdr create")),
		)
	}
	if intent.BranchCreated {
		if err := worktree.DeleteReservedBranch(
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
	return HerdrWorktreeResult{}, releaseHerdrIntent(locked, intent.ID, mutationErr)
}

func finalizeHerdrWorktree(
	ctx context.Context,
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	intent *state.HerdrIntent,
	observation herdrrun.WorkspaceObservation,
) error {
	if err := validateWorktreeObservation(*intent, source, observation); err != nil {
		// The snapshot succeeded, so the mismatch is confirmed.
		return errors.Join(errHerdrRealizedIdentityChanged, err)
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
	intent.Status = state.HerdrIntentRealized
	intent.Failure = ""
	locked.UpsertIntent(*intent)
	if saveErr := locked.Save(); saveErr != nil {
		return errors.Join(errHerdrRealizedIntentSave, saveErr)
	}
	return nil
}

func handleHerdrWorktreeFinalizeError(
	locked *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	err error,
) error {
	if errors.Is(err, errHerdrRealizedIdentityChanged) || errors.Is(err, worktree.ErrCheckoutMismatch) {
		return markHerdrIntentManual(locked, intent, err)
	}
	// Save failures and transient Git reads classified nothing; keep the
	// intent retryable.
	return err
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
		return fmt.Errorf("%w: coordinator", errHerdrRealizedIdentityChanged)
	}
	return nil
}

func resumeRealizedHerdrWorktree(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.HerdrIntent,
	allowOpen bool,
) (HerdrWorktreeResult, error) {
	// A failed snapshot classifies nothing: keep the realized intent
	// retryable instead of pinning it to manual cleanup.
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return HerdrWorktreeResult{}, errors.Join(err, ctx.Err())
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
				return HerdrWorktreeResult{}, markHerdrIntentManual(locked, intent, err)
			}
			return HerdrWorktreeResult{}, err
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
			return HerdrWorktreeResult{}, markHerdrIntentManual(locked, intent, err)
		}
		return HerdrWorktreeResult{}, err
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
	mutation, mutationErr := runtime.OpenWorktree(ctx, herdrrun.WorktreeOpenRequest{
		Coordinator:              observationResource(intent.Coordinator),
		SourceRepoKey:            source.RepoKey,
		SourceRepoRoot:           source.RepoRoot,
		Path:                     intent.WorktreePath,
		Label:                    intent.WorkspaceLabel,
		ExpectedAlreadyOpenID:    intent.Resource.WorkspaceID,
		ExpectedAlreadyOpenLabel: intent.Resource.Label,
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
		ctx,
		locked,
		req,
		source,
		&intent,
		mutation.WorkspaceObservation,
	); finalizeErr != nil {
		return HerdrWorktreeResult{}, handleHerdrWorktreeFinalizeError(locked, intent, finalizeErr)
	}
	return worktreeDeferred(intent)
}

func markHerdrIntentManual(
	locked *state.LockedHerdrIntents,
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
