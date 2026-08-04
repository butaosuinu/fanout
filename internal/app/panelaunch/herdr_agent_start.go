package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func (l *Launcher) startHerdrAgent(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
	callerEnvironment []string,
) (live backend.LivePane, retErr error) {
	if err := admitHerdrAgentStartDeadline(locked, l.Info.ProjectRoot, intent); err != nil {
		return live, err
	}
	intent, err := l.prepareHerdrLaunch(req, locked, route, intent, callerEnvironment)
	if err != nil {
		return live, err
	}
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return live, err
	}
	if err := l.admitHerdrLauncher(ctx, journal, route, &intent); err != nil {
		return live, err
	}
	intent.Launch.TokenIssued = true
	if err := saveHerdrLaunchPhase(journal, intent); err != nil {
		return live, err
	}
	return l.finishIssuedHerdrAgent(ctx, req, journal, intent)
}

func admitHerdrAgentStartDeadline(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
) error {
	if remainingHerdrLaunchTime(intent) > 0 {
		return nil
	}
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	return markHerdrIntentManual(journal, intent, fmt.Errorf("Herdr agent-start intent expired before launcher admission"))
}

func (l *Launcher) admitHerdrLauncher(
	ctx context.Context,
	journal *state.LockedHerdrIntents,
	route herdrrun.OwnedLaunchRoute,
	intent *state.HerdrIntent,
) error {
	if err := l.Herdr.WaitForLauncher(ctx, intent.Resource.PaneID, intent.Launch.Nonce, remainingHerdrLaunchTime(*intent)); err != nil {
		return err
	}
	process, err := l.Herdr.ProcessInfo(ctx, intent.Resource.PaneID)
	if err != nil {
		return err
	}
	if err := verifyHerdrLauncherProcess(process, *intent, route); err != nil {
		return err
	}
	intent.Launch.LauncherReady = true
	return saveHerdrLaunchPhase(journal, *intent)
}

func (l *Launcher) finishIssuedHerdrAgent(
	ctx context.Context,
	req Request,
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
) (live backend.LivePane, retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, l.failClosedIssuedHerdrLaunch(journal, intent))
		}
	}()
	if err := l.Herdr.SendLaunchToken(ctx, intent.Resource.PaneID, intent.Launch.Nonce); err != nil {
		return live, err
	}
	live, err := l.adoptHerdrAgent(ctx, req, intent)
	if err != nil {
		return live, err
	}
	if _, err := os.Lstat(intent.Launch.EnvFilePath); !errors.Is(err, os.ErrNotExist) {
		return live, fmt.Errorf("Herdr workload environment capsule was not consumed")
	}
	return live, nil
}

func (l *Launcher) adoptHerdrAgent(
	ctx context.Context,
	req Request,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	live, err := l.waitForHerdrAgent(ctx, intent, req.Agent)
	if err != nil {
		return live, err
	}
	process, err := l.Herdr.ProcessInfo(ctx, intent.Resource.PaneID)
	if err != nil {
		return live, err
	}
	if err := verifyHerdrAgentProcess(process, intent); err != nil {
		return live, err
	}
	if err := l.Herdr.RenameAgent(ctx, intent.Resource.PaneID, intent.Launch.AgentName); err != nil {
		return live, err
	}
	return l.waitForHerdrAgent(ctx, intent, intent.Launch.AgentName)
}

func saveHerdrLaunchPhase(journal *state.LockedHerdrIntents, intent state.HerdrIntent) error {
	journal.UpsertIntent(intent)
	return journal.Save()
}

func remainingHerdrLaunchTime(intent state.HerdrIntent) time.Duration {
	remaining := time.Until(time.UnixMilli(intent.ExpiresUnixMS))
	if remaining <= 0 {
		return 0
	}
	if remaining > herdrrun.DefaultWaitTimeout {
		return herdrrun.DefaultWaitTimeout
	}
	return remaining
}

func verifyHerdrLauncherProcess(
	info herdrrun.PaneProcessInfo,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) error {
	if info.ShellPID <= 1 || info.ForegroundProcessGroup <= 1 {
		return fmt.Errorf("Herdr launcher process group is incomplete")
	}
	for _, process := range info.ForegroundProcesses {
		if process.PID == info.ShellPID && process.CWD == intent.WorktreePath &&
			len(process.Argv) == 1 && process.Argv[0] == route.LauncherPath {
			return nil
		}
	}
	return fmt.Errorf("Herdr launcher process identity does not match the bundled fanout executable")
}

func verifyHerdrAgentProcess(info herdrrun.PaneProcessInfo, intent state.HerdrIntent) error {
	wantArgv := append([]string{intent.Launch.Executable}, intent.Launch.Args...)
	for _, process := range info.ForegroundProcesses {
		if process.CWD == intent.WorktreePath && matchesHerdrLaunchArgv(process.Argv, wantArgv) &&
			(process.PID == info.ForegroundProcessGroup || process.PID == info.ShellPID) {
			return nil
		}
	}
	return fmt.Errorf("Herdr agent process identity does not match launch intent")
}

func matchesHerdrLaunchArgv(got, want []string) bool {
	if slices.Equal(got, want) {
		return true
	}
	return len(got) == len(want)+1 && got[1] == want[0] && slices.Equal(got[2:], want[1:])
}

func (l *Launcher) waitForHerdrAgent(
	ctx context.Context,
	intent state.HerdrIntent,
	wantAgentID string,
) (backend.LivePane, error) {
	deadline := time.UnixMilli(intent.ExpiresUnixMS)
	for time.Now().Before(deadline) {
		panes, err := l.Herdr.LivePanes(ctx)
		if err == nil {
			if live, found := exactHerdrLaunchPane(intent, panes, wantAgentID); found {
				return live, nil
			}
		}
		select {
		case <-ctx.Done():
			return backend.LivePane{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return backend.LivePane{}, fmt.Errorf("timed out waiting for exact Herdr agent identity")
}

func exactHerdrLaunchPane(
	intent state.HerdrIntent,
	panes []backend.LivePane,
	wantAgentID string,
) (backend.LivePane, bool) {
	for _, pane := range panes {
		identity := []bool{
			pane.Ref.Backend == backend.Herdr,
			pane.Ref.Workspace == intent.Resource.WorkspaceID,
			pane.Ref.Pane == intent.Resource.PaneID,
			pane.TerminalID == intent.Resource.TerminalID,
			pane.RepoKey == intent.Resource.RepoKey,
			filepath.Clean(pane.CurrentPath) == filepath.Clean(intent.WorktreePath),
			pane.SessionID == intent.Session, pane.SocketPath == intent.SocketPath,
			pane.AgentPresent, pane.AgentID == wantAgentID,
		}
		if !slices.Contains(identity, false) && validOptionalHerdrAgentSession(pane.AgentSession, intent.Launch.Agent) {
			return pane, true
		}
	}
	return backend.LivePane{}, false
}

func validOptionalHerdrAgentSession(session *backend.AgentSessionRef, agentName string) bool {
	return session == nil || session.Valid() && session.Agent == agentName
}
