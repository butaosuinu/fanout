package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func (l *Launcher) rollbackFailedManagedLaunch(
	locked *state.LockedStore,
	intent state.LaunchIntent,
	cause error,
) error {
	if errors.Is(cause, errManagedLaunchStatePreserved) {
		return cause
	}
	if intent.ID == "" {
		return cause
	}
	if intent.Kind == state.IntentCoordinator {
		journal, err := locked.LaunchJournal(l.Info.ProjectRoot)
		if err != nil {
			return errors.Join(cause, err)
		}
		return errors.Join(cause, markManagedIntentManual(journal, intent, cause))
	}
	rollbackErr := l.rollbackManagedLaunch(locked, intent, cause)
	return errors.Join(cause, rollbackErr)
}

func (l *Launcher) rollbackManagedLaunch(
	locked *state.LockedStore,
	intent state.LaunchIntent,
	cause error,
) error {
	journal, latest, skip, err := loadManagedRollbackTarget(locked, l.Info.ProjectRoot, intent.ID)
	if err != nil || skip {
		return err
	}
	rollback, err := beginManagedLaunchRollback(journal, latest, cause)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxManagedRealizeTimeout)
	defer cancel()
	if err := l.verifyManagedRollbackTarget(ctx, latest); err != nil {
		return failManagedRollback(journal, latest, rollback, err)
	}
	if err := l.issueManagedWorktreeRemoval(ctx, journal, latest, &rollback); err != nil {
		return failManagedRollback(journal, latest, rollback, err)
	}
	if err := l.finishManagedRollbackGit(ctx, latest); err != nil {
		return failManagedRollback(journal, latest, rollback, err)
	}
	journal.RemoveIntent(latest.ID)
	journal.RemoveIntent(rollback.ID)
	return journal.Save()
}

func loadManagedRollbackTarget(
	locked *state.LockedStore,
	projectRoot, intentID string,
) (*state.LockedLaunchJournal, state.LaunchIntent, bool, error) {
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return nil, state.LaunchIntent{}, false, err
	}
	latest, found := journal.FindIntent(intentID)
	if !found {
		return nil, state.LaunchIntent{}, false, fmt.Errorf("failed Herdr launch intent disappeared before rollback")
	}
	if latest.Launch != nil && latest.Launch.TokenIssued {
		return journal, latest, true, nil
	}
	return journal, latest, false, nil
}

func (l *Launcher) issueManagedWorktreeRemoval(
	ctx context.Context,
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	rollback *state.LaunchIntent,
) error {
	rollback.Status = state.IntentIssued
	journal.UpsertIntent(*rollback)
	if err := journal.Save(); err != nil {
		return err
	}
	if intent.Launch != nil {
		if err := removeUnpublishedManagedEnvironment(l.Herdr, filepath.Dir(intent.SocketPath), intent.Launch); err != nil {
			return err
		}
	}
	removeErr := l.Herdr.RemoveWorktree(ctx, intent.Resource.WorkspaceID, intent.WorktreePath)
	absentErr := l.verifyManagedRollbackAbsent(ctx, intent)
	if absentErr != nil {
		return errors.Join(removeErr, absentErr)
	}
	return nil
}

func (l *Launcher) finishManagedRollbackGit(ctx context.Context, intent state.LaunchIntent) error {
	if !intent.BranchCreated {
		return nil
	}
	return worktree.DeleteReservedBranch(ctx, l.Info.ProjectRoot, intent.FullBranchRef, intent.BaseSHA)
}

func beginManagedLaunchRollback(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	cause error,
) (state.LaunchIntent, error) {
	id, err := state.RollbackIntentID(intent.ID)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	if _, found := journal.FindIntent(id); found {
		return state.LaunchIntent{}, fmt.Errorf("%w: Herdr launch rollback is already recorded", ErrManualCleanupRequired)
	}
	rollback := intent
	rollback.ID = id
	rollback.Kind = state.IntentRollback
	rollback.Status = state.IntentPlanned
	rollback.ExpiresUnixMS = time.Now().Add(maxManagedRealizeTimeout).UnixMilli()
	rollback.Launch = nil
	rollback.Failure = ""
	intent.Status = state.IntentManualCleanupRequired
	intent.Failure = fmt.Sprintf("launch failed; rollback %s started: %v", id, cause)
	journal.UpsertIntent(intent)
	journal.UpsertIntent(rollback)
	return rollback, journal.Save()
}

func (l *Launcher) verifyManagedRollbackTarget(ctx context.Context, intent state.LaunchIntent) error {
	if err := l.Herdr.VerifyOwned(ctx); err != nil {
		return err
	}
	workspaces, err := l.Herdr.ObserveWorkspaces(ctx)
	if err != nil {
		return err
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) != 1 || !workspaceHasManagedResource(matches[0], intent.Resource) {
		return fmt.Errorf("herdr rollback target does not match the saved workspace identity")
	}
	_, err = worktree.VerifyCheckout(ctx, l.Info.ProjectRoot, intent.WorktreePath,
		intent.FullBranchRef, intent.ExpectedHead, intent.Resource.RepoKey, intent.Resource.RepoRoot)
	return err
}

func (l *Launcher) verifyManagedRollbackAbsent(ctx context.Context, intent state.LaunchIntent) error {
	workspaces, err := l.Herdr.ObserveWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == intent.Resource.WorkspaceID || workspace.Label == intent.WorkspaceLabel {
			return fmt.Errorf("herdr rollback target remains live")
		}
	}
	checkout, err := worktree.ObserveCheckout(ctx, l.Info.ProjectRoot, intent.WorktreePath)
	if err != nil {
		return err
	}
	if !checkout.PathAbsent || checkout.Registered {
		return fmt.Errorf("herdr rollback checkout remains registered")
	}
	return nil
}

func failManagedRollback(
	journal *state.LockedLaunchJournal,
	original, rollback state.LaunchIntent,
	cause error,
) error {
	reason := fmt.Sprintf("Herdr launch rollback requires manual cleanup: %v", cause)
	for _, intent := range []*state.LaunchIntent{&original, &rollback} {
		intent.Status = state.IntentManualCleanupRequired
		intent.Failure = reason
		journal.UpsertIntent(*intent)
	}
	return errors.Join(fmt.Errorf("%w: %s", ErrManualCleanupRequired, reason), journal.Save())
}
