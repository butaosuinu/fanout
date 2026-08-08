package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func (l *Launcher) shellHerdr(
	locked *state.LockedStore,
	targetPath string,
	number int,
	slug, title string,
) error {
	if l.Herdr == nil {
		return fmt.Errorf("Herdr terminal launch requires an owned session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxHerdrRealizeTimeout)
	defer cancel()
	route, err := verifyHerdrConsoleRoute(ctx, l.Herdr)
	if err != nil {
		return err
	}
	intent, err := l.realizeHerdrShell(ctx, locked, route, targetPath, number)
	if err != nil {
		return err
	}
	live, err := l.startHerdrShell(ctx, locked, route, intent)
	if err != nil {
		return markHerdrAttachedFailure(locked, l.Info.ProjectRoot, intent, err)
	}
	pane := herdrShellStatePane(intent, live, number, slug, title)
	return finalizeHerdrShell(locked, l.Info.ProjectRoot, intent, pane)
}

func herdrShellStatePane(
	intent state.HerdrIntent,
	live backend.LivePane,
	number int,
	slug, title string,
) state.Pane {
	return state.Pane{
		Parent: ManualParentRef, IssueNum: number, Kind: state.PaneKindShell,
		Slug: slug, Backend: backend.Herdr, PaneID: live.Ref.Pane,
		HerdrWorkspaceID: live.Ref.Workspace, HerdrWorkspaceLabel: live.WorkspaceLabel,
		HerdrTerminalID: live.TerminalID, HerdrRepoKey: live.RepoKey,
		HerdrSession: live.SessionID, HerdrSocketPath: live.SocketPath,
		Agent: state.PaneKindShell, DisplayName: title, WorktreePath: intent.WorktreePath,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func (l *Launcher) realizeHerdrShell(
	ctx context.Context,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	targetPath string,
	number int,
) (state.HerdrIntent, error) {
	launch, prepared, err := l.prepareManualHerdrShell(locked, route, number)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	result, err := RealizeHerdrCoordinator(ctx, HerdrCoordinatorRequest{
		Parent: ManualParentRef, IssueNum: number,
		ProjectRoot: l.Info.ProjectRoot, SourceRoot: targetPath, CWD: targetPath,
		HerdrSession: route.Session, SocketPath: route.SocketPath, Launch: launch,
	}, l.Herdr, locked, HerdrRealizeHooks{})
	if err != nil && !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		return result.Intent, errors.Join(
			err,
			discardRejectedAttachedLaunch(
				locked, l.Info.ProjectRoot, route, launch, prepared, number,
			),
		)
	}
	return result.Intent, nil
}

func (l *Launcher) prepareManualHerdrShell(
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	number int,
) (*state.HerdrLaunch, bool, error) {
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return nil, false, err
	}
	intentID, err := state.HerdrCoordinatorIntentID(ManualParentRef, l.Info.ProjectRoot, number)
	if err != nil {
		return nil, false, err
	}
	if _, found := journal.FindIntent(intentID); found {
		return nil, false, nil
	}
	shell := os.Getenv("SHELL")
	_, shell, err = resolveHerdrConsoleInputs(l.Info.ProjectRoot, shell)
	if err != nil {
		return nil, false, err
	}
	return newHerdrShellLaunch(l.Herdr, route, shell, os.Environ())
}

func finalizeHerdrShell(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	pane state.Pane,
) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, markHerdrFinalizationFailure(locked, projectRoot, intent, retErr))
		}
	}()
	if err := locked.RecordPane(pane); err != nil {
		return err
	}
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	journal.RemoveIntent(intent.ID)
	return journal.Save()
}
