package panelaunch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
		return fmt.Errorf("herdr terminal launch requires an owned session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxHerdrRealizeTimeout)
	defer cancel()
	route, err := verifyHerdrConsoleRoute(ctx, l.Herdr)
	if err != nil {
		return err
	}
	intent, err := realizeHerdrInteractive(
		ctx, l.Herdr, locked, route,
		manualHerdrCoordinatorRequest(l.Info.ProjectRoot, targetPath, route, number),
		func(string) (*state.HerdrLaunch, error) {
			return l.newManualHerdrShellLaunch(route)
		},
	)
	if err != nil {
		return err
	}
	live, err := l.startHerdrShell(ctx, locked, route, intent)
	if err != nil {
		return markHerdrFinalizationFailure(locked, l.Info.ProjectRoot, intent, err)
	}
	pane := herdrShellStatePane(intent, live, number, slug, title, "")
	return finalizeHerdrInteractive(locked, l.Info.ProjectRoot, intent, pane)
}

func (l *Launcher) newManualHerdrShellLaunch(
	route herdrrun.OwnedLaunchRoute,
) (*state.HerdrLaunch, error) {
	_, shell, err := resolveHerdrConsoleInputs(l.Info.ProjectRoot, os.Getenv("SHELL"))
	if err != nil {
		return nil, err
	}
	return newHerdrShellLaunch(l.Herdr, route, shell, os.Environ())
}

func herdrShellStatePane(
	intent state.HerdrIntent,
	live backend.LivePane,
	number int,
	slug, title, runtimeParent string,
) state.Pane {
	return state.Pane{
		Parent: ManualParentRef, RuntimeParent: runtimeParent,
		IssueNum: number, Kind: state.PaneKindShell,
		Slug: slug, Backend: backend.Herdr, PaneID: live.Ref.Pane,
		HerdrWorkspaceID: live.Ref.Workspace, HerdrWorkspaceLabel: live.WorkspaceLabel,
		HerdrTerminalID: live.TerminalID, HerdrRepoKey: live.RepoKey,
		HerdrSession: live.SessionID, HerdrSocketPath: live.SocketPath,
		Agent: state.PaneKindShell, DisplayName: title, WorktreePath: intent.WorktreePath,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func (l *Launcher) startHerdrShell(
	ctx context.Context,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	return l.startHerdrAgent(
		ctx, locked, route, intent, l.prepareHerdrShellStart, exactHerdrShellPane, nil,
	)
}

func (l *Launcher) prepareHerdrShellStart(
	locked *state.LockedStore,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return intent, err
	}
	if saved, found := journal.FindIntent(intent.ID); found {
		intent = saved
	}
	if intent.Launch == nil || intent.Launch.Agent != "" || intent.Launch.AgentName != "" {
		return intent, fmt.Errorf("herdr shell intent has an invalid launch capsule")
	}
	if intent.Launch.TokenIssued {
		return intent, l.failClosedIssuedHerdrLaunch(journal, intent, nil)
	}
	return intent, nil
}

func exactHerdrShellPane(
	intent state.HerdrIntent,
	panes []backend.LivePane,
) (backend.LivePane, bool) {
	for _, pane := range panes {
		identity := []bool{
			pane.Ref.Backend == backend.Herdr,
			pane.Ref.Workspace == intent.Resource.WorkspaceID,
			pane.Ref.Pane == intent.Resource.PaneID,
			pane.WorkspaceLabel == intent.Resource.Label,
			pane.TerminalID == intent.Resource.TerminalID,
			filepath.Clean(pane.CurrentPath) == filepath.Clean(intent.WorktreePath),
			pane.SessionID == intent.Session, pane.SocketPath == intent.SocketPath,
			!pane.AgentPresent,
		}
		if !slices.Contains(identity, false) {
			return pane, true
		}
	}
	return backend.LivePane{}, false
}
