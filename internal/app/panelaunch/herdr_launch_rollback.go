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

func (l *Launcher) rollbackFailedHerdrLaunch(
	locked *state.LockedStore,
	intent state.HerdrIntent,
	cause error,
) error {
	if errors.Is(cause, errHerdrLaunchStatePreserved) {
		return cause
	}
	if intent.ID == "" {
		return cause
	}
	if intent.Kind == state.HerdrIntentCoordinator {
		journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
		if err != nil {
			return errors.Join(cause, err)
		}
		return errors.Join(cause, markHerdrIntentManual(journal, intent, cause))
	}
	rollbackErr := l.rollbackHerdrLaunch(locked, intent, cause)
	return errors.Join(cause, rollbackErr)
}

func (l *Launcher) rollbackHerdrLaunch(
	locked *state.LockedStore,
	intent state.HerdrIntent,
	cause error,
) error {
	journal, latest, skip, err := loadHerdrRollbackTarget(locked, l.Info.ProjectRoot, intent.ID)
	if err != nil || skip {
		return err
	}
	rollback, err := beginHerdrLaunchRollback(journal, latest, cause)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxHerdrRealizeTimeout)
	defer cancel()
	if err := l.verifyHerdrRollbackTarget(ctx, latest); err != nil {
		return failHerdrRollback(journal, latest, rollback, err)
	}
	if err := l.issueHerdrWorktreeRemoval(ctx, journal, latest, &rollback); err != nil {
		return failHerdrRollback(journal, latest, rollback, err)
	}
	if err := l.finishHerdrRollbackGit(ctx, latest); err != nil {
		return failHerdrRollback(journal, latest, rollback, err)
	}
	journal.RemoveIntent(latest.ID)
	journal.RemoveIntent(rollback.ID)
	return journal.Save()
}

func loadHerdrRollbackTarget(
	locked *state.LockedStore,
	projectRoot, intentID string,
) (*state.LockedHerdrIntents, state.HerdrIntent, bool, error) {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return nil, state.HerdrIntent{}, false, err
	}
	latest, found := journal.FindIntent(intentID)
	if !found {
		return nil, state.HerdrIntent{}, false, fmt.Errorf("failed Herdr launch intent disappeared before rollback")
	}
	if latest.Launch != nil && latest.Launch.TokenIssued {
		return journal, latest, true, nil
	}
	return journal, latest, false, nil
}

func (l *Launcher) issueHerdrWorktreeRemoval(
	ctx context.Context,
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	rollback *state.HerdrIntent,
) error {
	rollback.Status = state.HerdrIntentIssued
	journal.UpsertIntent(*rollback)
	if err := journal.Save(); err != nil {
		return err
	}
	if intent.Launch != nil {
		if err := removeUnpublishedHerdrEnvironment(filepath.Dir(intent.SocketPath), intent.Launch); err != nil {
			return err
		}
	}
	removeErr := l.Herdr.RemoveWorktree(ctx, intent.Resource.WorkspaceID, intent.WorktreePath)
	absentErr := l.verifyHerdrRollbackAbsent(ctx, intent)
	if absentErr != nil {
		return errors.Join(removeErr, absentErr)
	}
	return nil
}

func (l *Launcher) finishHerdrRollbackGit(ctx context.Context, intent state.HerdrIntent) error {
	if !intent.BranchCreated {
		return nil
	}
	return worktree.DeleteReservedBranch(ctx, l.Info.ProjectRoot, intent.FullBranchRef, intent.BaseSHA)
}

func beginHerdrLaunchRollback(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	cause error,
) (state.HerdrIntent, error) {
	id, err := state.HerdrRollbackIntentID(intent.ID)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	if _, found := journal.FindIntent(id); found {
		return state.HerdrIntent{}, fmt.Errorf("%w: Herdr launch rollback is already recorded", ErrHerdrManualCleanupRequired)
	}
	rollback := intent
	rollback.ID = id
	rollback.Kind = state.HerdrIntentRollback
	rollback.Status = state.HerdrIntentPlanned
	rollback.ExpiresUnixMS = time.Now().Add(maxHerdrRealizeTimeout).UnixMilli()
	rollback.Launch = nil
	rollback.Failure = ""
	intent.Status = state.HerdrIntentManualCleanupRequired
	intent.Failure = fmt.Sprintf("launch failed; rollback %s started: %v", id, cause)
	journal.UpsertIntent(intent)
	journal.UpsertIntent(rollback)
	return rollback, journal.Save()
}

func (l *Launcher) verifyHerdrRollbackTarget(ctx context.Context, intent state.HerdrIntent) error {
	if err := l.Herdr.VerifyOwned(ctx); err != nil {
		return err
	}
	workspaces, err := l.Herdr.ObserveWorkspaces(ctx)
	if err != nil {
		return err
	}
	matches := workspacesWithLabel(workspaces, intent.WorkspaceLabel)
	if len(matches) != 1 || !workspaceHasHerdrResource(matches[0], intent.Resource) {
		return fmt.Errorf("herdr rollback target does not match the saved workspace identity")
	}
	_, err = worktree.VerifyCheckout(ctx, l.Info.ProjectRoot, intent.WorktreePath,
		intent.FullBranchRef, intent.ExpectedHead, intent.Resource.RepoKey, intent.Resource.RepoRoot)
	return err
}

func (l *Launcher) verifyHerdrRollbackAbsent(ctx context.Context, intent state.HerdrIntent) error {
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

func failHerdrRollback(
	journal *state.LockedHerdrIntents,
	original, rollback state.HerdrIntent,
	cause error,
) error {
	reason := fmt.Sprintf("Herdr launch rollback requires manual cleanup: %v", cause)
	for _, intent := range []*state.HerdrIntent{&original, &rollback} {
		intent.Status = state.HerdrIntentManualCleanupRequired
		intent.Failure = reason
		journal.UpsertIntent(*intent)
	}
	return errors.Join(fmt.Errorf("%w: %s", ErrHerdrManualCleanupRequired, reason), journal.Save())
}
