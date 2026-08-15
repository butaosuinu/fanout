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

func (l *Launcher) shellManaged(
	locked *state.LockedStore,
	targetPath string,
	number int,
	slug, title string,
) error {
	if l.Managed == nil {
		return fmt.Errorf("herdr terminal launch requires an owned session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxManagedRealizeTimeout)
	defer cancel()
	route, err := verifyManagedConsoleRoute(ctx, l.Managed)
	if err != nil {
		return err
	}
	intent, err := realizeManagedInteractive(
		ctx, l.Managed, locked, route,
		manualManagedCoordinatorRequest(l.Info.ProjectRoot, targetPath, route, "", number),
		func(state.LaunchIntent) (*state.LaunchCapsule, error) {
			return l.newManualManagedShellLaunch(route)
		},
	)
	if err != nil {
		return err
	}
	live, err := l.startManagedAgent(ctx, locked, route, intent, validateManagedShellLaunch, nil, exactManagedShellPane, nil)
	if err != nil {
		return markManagedFinalizationFailure(locked, l.Info.ProjectRoot, intent, err)
	}
	pane := managedShellStatePane(intent, live, number, slug, title, "")
	return finalizeManagedPane(locked, l.Info.ProjectRoot, intent, staticManagedPane(pane))
}

func (l *Launcher) newManualManagedShellLaunch(
	route backend.OwnedLaunchRoute,
) (*state.LaunchCapsule, error) {
	_, shell, err := resolveManagedConsoleInputs(l.Info.ProjectRoot, os.Getenv("SHELL"))
	if err != nil {
		return nil, err
	}
	return newManagedShellLaunch(l.Managed, route, shell, os.Environ())
}

func managedShellStatePane(
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

func validateManagedShellLaunch(launch *state.LaunchCapsule) error {
	if launch == nil || launch.Agent != "" || launch.AgentName != "" {
		return fmt.Errorf("herdr shell intent has an invalid launch capsule")
	}
	return nil
}

func exactManagedShellPane(
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
