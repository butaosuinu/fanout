package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func recoverHerdrCleanup(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	if intent.Status == state.HerdrIntentIssued && intent.CleanupPhase == state.HerdrCleanupReopen {
		return recoverIssuedHerdrReopen(ctx, opts, journal, runtime, intent)
	}
	observation, err := observeHerdrCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if err != nil {
		return intent, err
	}
	if herdrCleanupAbsent(observation) {
		return realizeHerdrCleanup(journal, intent)
	}
	if intent.Status == state.HerdrIntentManualCleanupRequired {
		return intent, fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, intent.Failure)
	}
	cause := fmt.Errorf("issued Herdr %s outcome remains ambiguous", intent.CleanupPhase)
	return intent, markHerdrCleanupManual(journal, intent, cause)
}

func recoverIssuedHerdrReopen(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return intent, err
	}
	workspace, err := matchHerdrWorkspaceByLabel(
		workspaces,
		intent.WorkspaceLabel,
		intent.WorktreePath,
		intent.Resource.RepoKey,
		intent.Resource.RepoRoot,
	)
	if err != nil {
		return intent, markHerdrCleanupManual(journal, intent, err)
	}
	checkout, err := worktree.ObserveCheckout(ctx, opts.ProjectRoot, intent.WorktreePath)
	if err != nil {
		return intent, err
	}
	if workspace == nil && checkout.PathAbsent && !checkout.Registered {
		return realizeHerdrCleanup(journal, intent)
	}
	if workspace == nil {
		cause := fmt.Errorf("issued Herdr worktree reopen has no adoptable workspace")
		return intent, markHerdrCleanupManual(journal, intent, cause)
	}
	return adoptReopenedHerdrWorkspace(ctx, opts, journal, intent, *workspace, checkout)
}

func adoptReopenedHerdrWorkspace(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	workspace herdrrun.WorkspaceObservation,
	checkout worktree.CheckoutObservation,
) (state.HerdrIntent, error) {
	resource := herdrResourceFromObservation(workspace)
	if err := verifyHerdrCheckout(ctx, opts.ProjectRoot, intent.FullBranchRef, intent.ExpectedHead, resource); err != nil {
		return intent, markHerdrCleanupManual(journal, intent, err)
	}
	if checkout.HeadSHA != intent.ExpectedHead {
		cause := fmt.Errorf("reopened Herdr checkout HEAD changed from %s to %s", intent.ExpectedHead, checkout.HeadSHA)
		return intent, markHerdrCleanupManual(journal, intent, cause)
	}
	intent.Resource = resource
	intent.CleanupPhase = state.HerdrCleanupRemove
	intent.Status = state.HerdrIntentPlanned
	intent.Failure = ""
	return intent, saveHerdrCleanupIntent(journal, intent)
}

func executeHerdrReopen(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	if err := verifyHerdrReopenPreconditions(ctx, opts, runtime, intent); err != nil {
		return intent, err
	}
	coordinator, err := currentHerdrCoordinator(ctx, runtime, intent.Coordinator)
	if err != nil {
		return intent, err
	}
	mutationErr := issueHerdrReopen(ctx, journal, runtime, coordinator, &intent)
	recovered, err := recoverIssuedHerdrReopen(ctx, opts, journal, runtime, intent)
	if err != nil {
		return intent, errors.Join(mutationErr, err)
	}
	if recovered.Status == state.HerdrIntentRealized {
		return recovered, nil
	}
	return executeHerdrRemove(ctx, opts, journal, runtime, recovered)
}

func issueHerdrReopen(
	ctx context.Context,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	coordinator herdrrun.WorkspaceObservation,
	intent *state.HerdrIntent,
) error {
	if err := runtime.VerifyWorktreeSetupPolicy(ctx); err != nil {
		return err
	}
	intent.Status = state.HerdrIntentIssued
	if err := saveHerdrCleanupIntent(journal, *intent); err != nil {
		return err
	}
	_, err := runtime.OpenWorktree(ctx, herdrrun.WorktreeOpenRequest{
		Coordinator: coordinator, SourceRepoKey: intent.Resource.RepoKey,
		SourceRepoRoot: intent.Resource.RepoRoot, Path: intent.WorktreePath,
		Label: intent.WorkspaceLabel,
	})
	return err
}

func verifyHerdrReopenPreconditions(
	ctx context.Context,
	opts Options,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) error {
	observation, err := observeHerdrCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if err != nil {
		return err
	}
	if observation.workspace != nil || observation.checkout.PathAbsent || !observation.checkout.Registered {
		return fmt.Errorf("Herdr cleanup reopen preconditions changed")
	}
	return verifyHerdrCheckout(
		ctx,
		opts.ProjectRoot,
		intent.FullBranchRef,
		intent.ExpectedHead,
		intent.Resource,
	)
}

func currentHerdrCoordinator(
	ctx context.Context,
	runtime HerdrRuntime,
	resource state.HerdrResource,
) (herdrrun.WorkspaceObservation, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return herdrrun.WorkspaceObservation{}, err
	}
	workspace, err := matchHerdrCoordinatorWorkspace(workspaces, resource)
	if err != nil {
		return herdrrun.WorkspaceObservation{}, err
	}
	return *workspace, nil
}

func executeHerdrRemove(
	ctx context.Context,
	opts Options,
	journal *state.LockedHerdrIntents,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	if err := verifyHerdrRemovePreconditions(ctx, opts, runtime, intent); err != nil {
		return intent, err
	}
	intent.Status = state.HerdrIntentIssued
	if err := saveHerdrCleanupIntent(journal, intent); err != nil {
		return intent, err
	}
	mutationErr := runtime.RemoveWorktree(ctx, intent.Resource.WorkspaceID, intent.WorktreePath)
	observation, observeErr := observeHerdrCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if observeErr != nil {
		return intent, markHerdrCleanupManual(journal, intent, errors.Join(mutationErr, observeErr))
	}
	if herdrCleanupAbsent(observation) {
		return realizeHerdrCleanup(journal, intent)
	}
	if observation.workspace != nil && observation.checkout.PathAbsent && !observation.checkout.Registered {
		intent.Status = state.HerdrIntentPlanned
		intent.CleanupPhase = state.HerdrCleanupWorkspaceClose
		if err := saveHerdrCleanupIntent(journal, intent); err != nil {
			return intent, err
		}
		return executeHerdrWorkspaceClose(ctx, opts, journal, runtime, intent)
	}
	cause := errors.Join(mutationErr, fmt.Errorf("Herdr worktree remove did not establish absence"))
	return intent, markHerdrCleanupManual(journal, intent, cause)
}

func verifyHerdrRemovePreconditions(
	ctx context.Context,
	opts Options,
	runtime HerdrRuntime,
	intent state.HerdrIntent,
) error {
	observation, err := observeHerdrCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if err != nil {
		return err
	}
	if observation.workspace == nil || observation.checkout.PathAbsent || !observation.checkout.Registered {
		return fmt.Errorf("Herdr worktree remove preconditions changed")
	}
	if err := verifyHerdrTerminalInvalidation(*observation.workspace, intent.Resource); err != nil {
		return err
	}
	return verifyHerdrCheckout(
		ctx,
		opts.ProjectRoot,
		intent.FullBranchRef,
		intent.ExpectedHead,
		intent.Resource,
	)
}

func executeHerdrWorkspaceClose(
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
	if observation.workspace == nil || !observation.checkout.PathAbsent || observation.checkout.Registered {
		return intent, fmt.Errorf("Herdr residual workspace close preconditions changed")
	}
	if err := verifyHerdrTerminalInvalidation(*observation.workspace, intent.Resource); err != nil {
		return intent, err
	}
	intent.Status = state.HerdrIntentIssued
	if err := saveHerdrCleanupIntent(journal, intent); err != nil {
		return intent, err
	}
	mutationErr := runtime.CloseWorkspace(ctx, intent.Resource.WorkspaceID)
	observation, observeErr := observeHerdrCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if observeErr == nil && herdrCleanupAbsent(observation) {
		return realizeHerdrCleanup(journal, intent)
	}
	cause := errors.Join(mutationErr, observeErr, fmt.Errorf("Herdr residual workspace close did not establish absence"))
	return intent, markHerdrCleanupManual(journal, intent, cause)
}

func realizeHerdrCleanup(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	intent.Status = state.HerdrIntentRealized
	intent.Failure = ""
	return intent, saveHerdrCleanupIntent(journal, intent)
}
