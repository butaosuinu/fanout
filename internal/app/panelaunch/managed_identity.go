package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

var errManagedLauncherIdentityChanged = errors.New("herdr launcher identity changed")

func (l *Launcher) verifyManagedIdleLauncher(
	ctx context.Context,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
) error {
	process, err := l.Managed.ProcessInfo(ctx, intent.Resource.PaneID)
	if err != nil {
		return err
	}
	if verifyErr := verifyManagedLauncherProcess(process, intent, route); verifyErr != nil {
		return fmt.Errorf("%w: %w", errManagedLauncherIdentityChanged, verifyErr)
	}
	panes, err := l.Managed.LivePanes(ctx)
	if err != nil {
		return err
	}
	if !managedIdlePanePresent(intent, panes) {
		return fmt.Errorf("%w: exact idle root pane is not live", errManagedLauncherIdentityChanged)
	}
	return nil
}

func managedIdlePanePresent(intent state.LaunchIntent, panes []backend.LivePane) bool {
	for _, pane := range panes {
		identity := []bool{
			pane.Ref.Backend == backend.Herdr,
			pane.Ref.Workspace == intent.Resource.WorkspaceID,
			intent.Resource.Label != "", pane.WorkspaceLabel == intent.Resource.Label,
			pane.Ref.Pane == intent.Resource.PaneID,
			pane.TerminalID == intent.Resource.TerminalID,
			filepath.Clean(pane.CurrentPath) == filepath.Clean(intent.WorktreePath),
			pane.SessionID == intent.Session,
			pane.SocketPath == intent.SocketPath,
			!pane.AgentPresent,
			pane.AgentID == "",
			pane.AgentSession == nil,
		}
		if !slices.Contains(identity, false) && managedRepoIdentityMatches(intent.Resource, pane) {
			return true
		}
	}
	return false
}

func managedRepoIdentityMatches(resource state.RuntimeResource, pane backend.LivePane) bool {
	if resource.RepoKey == "" {
		return true
	}
	return pane.RepoKey == resource.RepoKey &&
		filepath.Clean(pane.WorktreePath) == filepath.Clean(resource.CurrentPath)
}
