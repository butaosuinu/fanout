package panelaunch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
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
		manualHerdrCoordinatorRequest(l.Info.ProjectRoot, targetPath, route, "", number),
		func(state.LaunchIntent) (*state.LaunchCapsule, error) {
			return l.newManualHerdrShellLaunch(route)
		},
	)
	if err != nil {
		return err
	}
	live, err := l.startHerdrAgent(ctx, locked, route, intent, validateHerdrShellLaunch, nil, exactHerdrShellPane, nil)
	if err != nil {
		return markHerdrFinalizationFailure(locked, l.Info.ProjectRoot, intent, err)
	}
	pane := herdrShellStatePane(intent, live, number, slug, title, "")
	return finalizeHerdrPane(locked, l.Info.ProjectRoot, intent, staticHerdrPane(pane))
}

func (l *Launcher) newManualHerdrShellLaunch(
	route backend.OwnedLaunchRoute,
) (*state.LaunchCapsule, error) {
	_, shell, err := resolveHerdrConsoleInputs(l.Info.ProjectRoot, os.Getenv("SHELL"))
	if err != nil {
		return nil, err
	}
	return newHerdrShellLaunch(l.Herdr, route, shell, os.Environ())
}

func herdrShellStatePane(
	intent state.LaunchIntent,
	live backend.LivePane,
	number int,
	slug, title, runtimeParent string,
) state.Pane {
	pane := statePaneForBackend(Request{
		ParentRef: ManualParentRef, Number: number, Slug: slug,
		Agent: state.PaneKindShell, DisplayNameOverride: title,
	}, live.Ref.Pane, intent.WorktreePath, time.Now().UTC(), codexapp.Status{}, backend.Herdr, &live)
	pane.RuntimeParent, pane.Kind = runtimeParent, state.PaneKindShell
	pane.AgentStatus = ""
	return pane
}

func validateHerdrShellLaunch(launch *state.LaunchCapsule) error {
	if launch == nil || launch.Agent != "" || launch.AgentName != "" {
		return fmt.Errorf("herdr shell intent has an invalid launch capsule")
	}
	return nil
}

func exactHerdrShellPane(
	intent state.LaunchIntent,
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
