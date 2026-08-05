package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

var errHerdrLauncherIdentityChanged = errors.New("herdr launcher identity changed")

func (l *Launcher) verifyHerdrIdleLauncher(
	ctx context.Context,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) error {
	process, err := l.Herdr.ProcessInfo(ctx, intent.Resource.PaneID)
	if err != nil {
		return err
	}
	if verifyErr := verifyHerdrLauncherProcess(process, intent, route); verifyErr != nil {
		return fmt.Errorf("%w: %w", errHerdrLauncherIdentityChanged, verifyErr)
	}
	panes, err := l.Herdr.LivePanes(ctx)
	if err != nil {
		return err
	}
	if !herdrIdlePanePresent(intent, panes) {
		return fmt.Errorf("%w: exact idle root pane is not live", errHerdrLauncherIdentityChanged)
	}
	return nil
}

func herdrIdlePanePresent(intent state.HerdrIntent, panes []backend.LivePane) bool {
	for _, pane := range panes {
		identity := []bool{
			pane.Ref.Backend == backend.Herdr,
			pane.Ref.Workspace == intent.Resource.WorkspaceID,
			pane.Ref.Pane == intent.Resource.PaneID,
			pane.TerminalID == intent.Resource.TerminalID,
			filepath.Clean(pane.CurrentPath) == filepath.Clean(intent.WorktreePath),
			pane.SessionID == intent.Session,
			pane.SocketPath == intent.SocketPath,
			!pane.AgentPresent,
			pane.AgentID == "",
			pane.AgentSession == nil,
		}
		if !slices.Contains(identity, false) && herdrRepoIdentityMatches(intent.Resource, pane) {
			return true
		}
	}
	return false
}

func herdrRepoIdentityMatches(resource state.HerdrResource, pane backend.LivePane) bool {
	if resource.RepoKey == "" {
		return true
	}
	return pane.RepoKey == resource.RepoKey &&
		filepath.Clean(pane.WorktreePath) == filepath.Clean(resource.CurrentPath)
}
