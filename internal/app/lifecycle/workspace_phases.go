package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const (
	legacyRemoveFailurePrefix = "exit status 1: "
	legacyRemoveFailureSuffix = "\nherdr worktree remove did not establish absence"
)

type legacyRemoveRejection struct {
	ID     string           `json:"id"`
	Result *json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func recoverWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if intent.Status == state.IntentIssued && intent.CleanupPhase == state.CleanupReopen {
		return recoverIssuedReopen(ctx, opts, journal, runtime, intent)
	}
	if intent.Status == state.IntentManualCleanupRequired && isLegacyDirtyWorktreeRejection(intent) {
		return replanCurrentWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent)
	}
	observation, err := observeWorkspaceCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if err != nil {
		return intent, err
	}
	if workspaceCleanupAbsent(observation) {
		return realizeWorkspaceCleanup(journal, intent)
	}
	if intent.Status == state.IntentManualCleanupRequired {
		return recoverManualWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent, observation)
	}
	cause := fmt.Errorf("issued Herdr %s outcome remains ambiguous", intent.CleanupPhase)
	return intent, markWorkspaceCleanupManual(journal, intent, cause)
}

func recoverManualWorkspaceCleanup(
	ctx context.Context,
	opts Options,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	pane state.Pane,
	intent state.LaunchIntent,
	observation workspaceCleanupObservation,
) (state.LaunchIntent, error) {
	if !isLegacyDirtyWorktreeRejection(intent) {
		return intent, fmt.Errorf("%w: %s", ErrManualCleanupRequired, intent.Failure)
	}
	return replanObservedWorkspaceCleanup(ctx, opts, locked, journal, runtime, pane, intent, observation)
}

func isLegacyDirtyWorktreeRejection(intent state.LaunchIntent) bool {
	if intent.CleanupPhase != state.CleanupRemove {
		return false
	}
	payload, ok := strings.CutPrefix(intent.Failure, legacyRemoveFailurePrefix)
	if !ok {
		return false
	}
	payload, ok = strings.CutSuffix(payload, legacyRemoveFailureSuffix)
	if !ok {
		return false
	}
	envelope, ok := decodeLegacyRemoveRejection(payload)
	if !ok || envelope.Error == nil {
		return false
	}
	return !slices.Contains([]bool{
		envelope.ID == "cli:worktree:remove",
		envelope.Result == nil,
		envelope.Error.Code == "dirty_worktree_requires_force",
		strings.TrimSpace(envelope.Error.Message) != "",
	}, false)
}

func decodeLegacyRemoveRejection(payload string) (legacyRemoveRejection, bool) {
	var envelope legacyRemoveRejection
	decoder := json.NewDecoder(strings.NewReader(payload))
	if err := decoder.Decode(&envelope); err != nil {
		return legacyRemoveRejection{}, false
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return legacyRemoveRejection{}, false
	}
	return envelope, true
}

func recoverIssuedReopen(
	ctx context.Context,
	opts Options,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return intent, err
	}
	workspace, err := findUniqueWorkspace(
		workspaces, true, workspaceLabelPredicate(
			intent.WorkspaceLabel,
			intent.WorktreePath,
			intent.Resource.RepoKey,
			intent.Resource.RepoRoot,
		))
	if err != nil {
		return intent, markWorkspaceCleanupManual(journal, intent, err)
	}
	checkout, err := worktree.ObserveCheckout(ctx, opts.ProjectRoot, intent.WorktreePath)
	if err != nil {
		return intent, err
	}
	if workspace == nil && checkout.PathAbsent && !checkout.Registered {
		return realizeWorkspaceCleanup(journal, intent)
	}
	if workspace == nil {
		cause := fmt.Errorf("issued Herdr worktree reopen has no adoptable workspace")
		return intent, markWorkspaceCleanupManual(journal, intent, cause)
	}
	return adoptReopenedWorkspace(ctx, opts, journal, intent, *workspace, checkout)
}

func adoptReopenedWorkspace(
	ctx context.Context,
	opts Options,
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	workspace backend.WorkspaceObservation,
	checkout worktree.CheckoutObservation,
) (state.LaunchIntent, error) {
	resource := resourceFromObservation(workspace)
	if err := verifyCleanupCheckout(ctx, opts.ProjectRoot, intent.FullBranchRef, intent.ExpectedHead, resource); err != nil {
		return intent, markWorkspaceCleanupManual(journal, intent, err)
	}
	if checkout.HeadSHA != intent.ExpectedHead {
		cause := fmt.Errorf("reopened Herdr checkout HEAD changed from %s to %s", intent.ExpectedHead, checkout.HeadSHA)
		return intent, markWorkspaceCleanupManual(journal, intent, cause)
	}
	intent.Resource = resource
	intent.CleanupPhase = state.CleanupRemove
	intent.Status = state.IntentPlanned
	intent.Failure = ""
	return intent, saveWorkspaceCleanupIntent(journal, intent)
}

func executeReopen(
	ctx context.Context,
	opts Options,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if err := verifyReopenPreconditions(ctx, opts, runtime, intent); err != nil {
		return intent, err
	}
	coordinator, err := currentCoordinator(ctx, runtime, intent.Coordinator)
	if err != nil {
		return intent, err
	}
	issued, mutationErr := issueReopen(ctx, journal, runtime, coordinator, &intent)
	if !issued {
		return intent, mutationErr
	}
	if mutationDefinitelyNotIssued(mutationErr) {
		return resetUnissuedCleanup(journal, intent, mutationErr)
	}
	recovered, err := recoverIssuedReopen(ctx, opts, journal, runtime, intent)
	if err != nil {
		return intent, errors.Join(mutationErr, err)
	}
	if recovered.Status == state.IntentRealized {
		return recovered, nil
	}
	return executeRemove(ctx, opts, journal, runtime, recovered)
}

func issueReopen(
	ctx context.Context,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	coordinator backend.WorkspaceObservation,
	intent *state.LaunchIntent,
) (bool, error) {
	if err := runtime.VerifyWorktreeSetupPolicy(ctx); err != nil {
		return false, err
	}
	return issueCleanupMutation(ctx, journal, intent, func() error {
		_, err := runtime.OpenWorktree(ctx, backend.WorktreeOpenRequest{
			Coordinator: coordinator, SourceRepoKey: intent.Resource.RepoKey,
			SourceRepoRoot: intent.Resource.RepoRoot, Path: intent.WorktreePath,
			Label: intent.WorkspaceLabel,
		})
		return err
	})
}

func verifyReopenPreconditions(
	ctx context.Context,
	opts Options,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
) error {
	observation, err := observeWorkspaceCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if err != nil {
		return err
	}
	if observation.workspace != nil || observation.checkout.PathAbsent || !observation.checkout.Registered {
		return fmt.Errorf("herdr cleanup reopen preconditions changed")
	}
	return verifyCleanupCheckout(
		ctx,
		opts.ProjectRoot,
		intent.FullBranchRef,
		intent.ExpectedHead,
		intent.Resource,
	)
}

func currentCoordinator(
	ctx context.Context,
	runtime WorkspaceRuntime,
	resource state.RuntimeResource,
) (backend.WorkspaceObservation, error) {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return backend.WorkspaceObservation{}, err
	}
	workspace, err := findUniqueWorkspace(workspaces, false, coordinatorWorkspacePredicate(resource))
	if err != nil {
		return backend.WorkspaceObservation{}, err
	}
	return *workspace, nil
}

func executeRemove(
	ctx context.Context,
	opts Options,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if err := verifyRemovePreconditions(ctx, opts, runtime, intent); err != nil {
		return intent, err
	}
	issued, mutationErr := issueCleanupMutation(ctx, journal, &intent, func() error {
		return runtime.RemoveWorktree(ctx, intent.Resource.WorkspaceID, intent.WorktreePath)
	})
	if !issued {
		return intent, mutationErr
	}
	if mutationDefinitelyNotIssued(mutationErr) {
		return resetUnissuedCleanup(journal, intent, mutationErr)
	}
	return recoverRemoveMutation(ctx, opts, journal, runtime, intent, mutationErr)
}

func recoverRemoveMutation(
	ctx context.Context,
	opts Options,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
	mutationErr error,
) (state.LaunchIntent, error) {
	observation, observeErr := observeWorkspaceCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if observeErr != nil {
		return intent, errors.Join(mutationErr, observeErr)
	}
	if workspaceCleanupAbsent(observation) {
		return realizeWorkspaceCleanup(journal, intent)
	}
	if observation.workspace != nil && observation.checkout.PathAbsent && !observation.checkout.Registered {
		if mutationErr != nil {
			cause := errors.Join(mutationErr, fmt.Errorf("ambiguous Herdr worktree remove left a residual workspace"))
			return intent, markWorkspaceCleanupManual(journal, intent, cause)
		}
		intent.Status = state.IntentPlanned
		intent.CleanupPhase = state.CleanupWorkspaceClose
		if err := saveWorkspaceCleanupIntent(journal, intent); err != nil {
			return intent, err
		}
		return executeWorkspaceClose(ctx, opts, journal, runtime, intent)
	}
	cause := errors.Join(mutationErr, fmt.Errorf("herdr worktree remove did not establish absence"))
	return intent, markWorkspaceCleanupManual(journal, intent, cause)
}

func verifyRemovePreconditions(
	ctx context.Context,
	opts Options,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
) error {
	observation, err := observeWorkspaceCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if err != nil {
		return err
	}
	if observation.workspace == nil || observation.checkout.PathAbsent || !observation.checkout.Registered {
		return fmt.Errorf("herdr worktree remove preconditions changed")
	}
	if err := verifyTerminalInvalidation(*observation.workspace, intent.Resource); err != nil {
		return err
	}
	if err := verifyCleanupCheckout(
		ctx,
		opts.ProjectRoot,
		intent.FullBranchRef,
		intent.ExpectedHead,
		intent.Resource,
	); err != nil {
		return err
	}
	return verifyRemovableCheckoutContents(ctx, intent.WorktreePath)
}

func verifyRemovableCheckoutContents(ctx context.Context, path string) error {
	contentState, err := worktree.ObserveCheckoutContentState(ctx, path)
	if err != nil {
		return err
	}
	switch contentState {
	case worktree.CheckoutClean:
		return nil
	case worktree.CheckoutIgnoredOnly:
		return fmt.Errorf("herdr checkout %s contains ignored files only; remove them before retrying cleanup", path)
	default:
		return fmt.Errorf("herdr checkout %s has tracked or untracked changes; preserve or remove them before retrying cleanup", path)
	}
}

func executeWorkspaceClose(
	ctx context.Context,
	opts Options,
	journal *state.LockedLaunchJournal,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	if err := verifyWorkspaceClosePreconditions(ctx, opts, runtime, intent); err != nil {
		return intent, err
	}
	issued, mutationErr := issueCleanupMutation(ctx, journal, &intent, func() error {
		return runtime.CloseWorkspace(ctx, intent.Resource.WorkspaceID)
	})
	if !issued {
		return intent, mutationErr
	}
	if mutationDefinitelyNotIssued(mutationErr) {
		return resetUnissuedCleanup(journal, intent, mutationErr)
	}
	observation, observeErr := observeWorkspaceCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if observeErr != nil {
		return intent, errors.Join(mutationErr, observeErr)
	}
	if workspaceCleanupAbsent(observation) {
		return realizeWorkspaceCleanup(journal, intent)
	}
	cause := errors.Join(mutationErr, fmt.Errorf("herdr residual workspace close did not establish absence"))
	return intent, markWorkspaceCleanupManual(journal, intent, cause)
}

func verifyWorkspaceClosePreconditions(
	ctx context.Context,
	opts Options,
	runtime WorkspaceRuntime,
	intent state.LaunchIntent,
) error {
	observation, err := observeWorkspaceCleanup(ctx, runtime, opts.ProjectRoot, intent.Resource)
	if err != nil {
		return err
	}
	if observation.workspace == nil || !observation.checkout.PathAbsent || observation.checkout.Registered {
		return fmt.Errorf("herdr residual workspace close preconditions changed")
	}
	return verifyTerminalInvalidation(*observation.workspace, intent.Resource)
}

func mutationDefinitelyNotIssued(err error) bool {
	return errors.Is(err, backend.ErrMutationNotIssued) || errors.Is(err, backend.ErrMutationRejected)
}

func restorePlannedCleanup(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	cause error,
) (state.LaunchIntent, error) {
	intent.Status = state.IntentPlanned
	intent.Failure = ""
	return intent, errors.Join(cause, saveWorkspaceCleanupIntent(journal, intent))
}

func resetUnissuedCleanup(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	cause error,
) (state.LaunchIntent, error) {
	if intent.Coordinator == (state.RuntimeResource{}) || intent.CleanupPhase == state.CleanupReopen {
		journal.RemoveIntent(intent.ID)
		return intent, errors.Join(cause, journal.Save())
	}
	intent.Status = state.IntentPlanned
	intent.ExpiresUnixMS = time.Now().UnixMilli()
	intent.Failure = ""
	return intent, errors.Join(cause, saveWorkspaceCleanupIntent(journal, intent))
}

func issueCleanupMutation(
	ctx context.Context,
	journal *state.LockedLaunchJournal,
	intent *state.LaunchIntent,
	mutate func() error,
) (bool, error) {
	if err := ensureCleanupMutationFresh(ctx, *intent); err != nil {
		return false, err
	}
	intent.Status = state.IntentIssued
	if err := saveWorkspaceCleanupIntent(journal, *intent); err != nil {
		intent.Status = state.IntentPlanned
		return false, err
	}
	if err := ensureCleanupMutationFresh(ctx, *intent); err != nil {
		intent.Status = state.IntentPlanned
		_, restoreErr := restorePlannedCleanup(journal, *intent, err)
		return false, restoreErr
	}
	return true, mutate()
}

func ensureCleanupMutationFresh(ctx context.Context, intent state.LaunchIntent) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("herdr cleanup mutation context ended: %w", err)
	}
	if time.Now().UnixMilli() >= intent.ExpiresUnixMS {
		return fmt.Errorf("saved Herdr cleanup intent expired before mutation")
	}
	return nil
}

func realizeWorkspaceCleanup(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	intent.Status = state.IntentRealized
	intent.Failure = ""
	return intent, saveWorkspaceCleanupIntent(journal, intent)
}
