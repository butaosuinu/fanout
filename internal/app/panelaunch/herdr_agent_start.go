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
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const herdrLaunchStepTimeout = 5 * time.Second

const herdrLaunchObservationInterval = 2 * time.Second

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
	if err := ensureHerdrLaunchActive(ctx, intent); err != nil {
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
	return markHerdrIntentManual(journal, intent, fmt.Errorf("herdr agent-start intent expired before launcher admission"))
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
	verifyErr := retryHerdrObservation(ctx, *intent, func(observeCtx context.Context) error {
		return l.verifyHerdrIdleLauncher(observeCtx, *intent, route)
	})
	if verifyErr != nil {
		if errors.Is(verifyErr, errHerdrLauncherIdentityChanged) {
			return markHerdrIntentManual(journal, *intent, verifyErr)
		}
		return verifyErr
	}
	if err := ensureHerdrLaunchActive(ctx, *intent); err != nil {
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
			retErr = errors.Join(retErr, l.failClosedIssuedHerdrLaunch(journal, intent, retErr))
		}
	}()
	stepCtx, cancel, err := herdrLaunchStepContext(ctx, intent)
	if err != nil {
		return live, err
	}
	tokenErr := herdrLaunchStepResult(
		stepCtx, cancel,
		l.Herdr.SendLaunchToken(stepCtx, intent.Resource.PaneID, intent.Launch.Nonce),
	)
	if tokenErr != nil {
		return live, tokenErr
	}
	live, err = l.adoptHerdrAgent(ctx, req, intent)
	if err != nil {
		return live, err
	}
	if _, err := os.Lstat(intent.Launch.EnvFilePath); !errors.Is(err, os.ErrNotExist) {
		return live, fmt.Errorf("herdr workload environment capsule was not consumed")
	}
	return live, nil
}

func (l *Launcher) adoptHerdrAgent(
	ctx context.Context,
	req Request,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	statusPath, err := herdrCodexTeamStatusPath(req, intent)
	if err != nil {
		return backend.LivePane{}, err
	}
	live, err := l.waitForHerdrAgent(ctx, intent, req.Agent, statusPath)
	if err != nil {
		return live, err
	}
	if err := l.verifyAndRenameHerdrAgent(ctx, intent); err != nil {
		return live, err
	}
	return l.waitForHerdrAgent(ctx, intent, intent.Launch.AgentName, statusPath)
}

func (l *Launcher) verifyAndRenameHerdrAgent(
	ctx context.Context,
	intent state.HerdrIntent,
) error {
	var process herdrrun.PaneProcessInfo
	err := retryHerdrObservation(ctx, intent, func(observeCtx context.Context) error {
		var processErr error
		process, processErr = l.Herdr.ProcessInfo(observeCtx, intent.Resource.PaneID)
		return processErr
	})
	if err != nil {
		return err
	}
	if verifyErr := verifyHerdrAgentProcess(process, intent); verifyErr != nil {
		return verifyErr
	}
	stepCtx, cancel, err := herdrLaunchStepContext(ctx, intent)
	if err != nil {
		return err
	}
	return herdrLaunchStepResult(
		stepCtx, cancel,
		l.Herdr.RenameAgent(stepCtx, intent.Resource.PaneID, intent.Launch.AgentName),
	)
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

func ensureHerdrLaunchActive(ctx context.Context, intent state.HerdrIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(time.UnixMilli(intent.ExpiresUnixMS)) {
		return fmt.Errorf("herdr agent-start intent expired")
	}
	return nil
}

func herdrLaunchStepContext(
	ctx context.Context,
	intent state.HerdrIntent,
) (context.Context, context.CancelFunc, error) {
	if err := ensureHerdrLaunchActive(ctx, intent); err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(herdrLaunchStepTimeout)
	expires := time.UnixMilli(intent.ExpiresUnixMS)
	if expires.Before(deadline) {
		deadline = expires
	}
	stepCtx, cancel := context.WithDeadline(ctx, deadline)
	return stepCtx, cancel, nil
}

func herdrLaunchStepResult(
	stepCtx context.Context,
	cancel context.CancelFunc,
	operationErr error,
) error {
	contextErr := stepCtx.Err()
	cancel()
	return errors.Join(operationErr, contextErr)
}

func retryHerdrObservation(
	ctx context.Context,
	intent state.HerdrIntent,
	observe func(context.Context) error,
) error {
	for {
		stepCtx, cancel, err := herdrLaunchStepContext(ctx, intent)
		if err != nil {
			return err
		}
		stepErr := herdrLaunchStepResult(stepCtx, cancel, observe(stepCtx))
		if stepErr == nil || !herdrrun.IsRetryableObservationError(stepErr) {
			return stepErr
		}
		if err := waitForHerdrObservationRetry(ctx, intent); err != nil {
			return err
		}
	}
}

func waitForHerdrObservationRetry(ctx context.Context, intent state.HerdrIntent) error {
	remaining := remainingHerdrLaunchTime(intent)
	if remaining <= 0 {
		return fmt.Errorf("herdr agent-start intent expired")
	}
	timer := time.NewTimer(min(herdrLaunchObservationInterval, remaining))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ensureHerdrLaunchActive(ctx, intent)
	}
}

func verifyHerdrLauncherProcess(
	info herdrrun.PaneProcessInfo,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) error {
	if info.ShellPID <= 1 || info.ForegroundProcessGroup <= 1 {
		return fmt.Errorf("herdr launcher process group is incomplete")
	}
	for _, process := range info.ForegroundProcesses {
		if process.PID == info.ShellPID && process.CWD == intent.WorktreePath &&
			process.Argv0 == route.LauncherPath && len(process.Argv) == 0 {
			return nil
		}
	}
	return fmt.Errorf("herdr launcher process identity does not match the bundled fanout executable")
}

func verifyHerdrAgentProcess(info herdrrun.PaneProcessInfo, intent state.HerdrIntent) error {
	root, processes, ok := herdrAgentProcessRoot(info, intent)
	if !ok {
		return fmt.Errorf("herdr agent process identity does not match launch intent")
	}
	if directHerdrAgentProcess(root, intent) {
		return nil
	}
	if !interpreterHerdrAgentProcess(root, intent) {
		return fmt.Errorf("herdr agent process identity does not match launch intent")
	}
	if countHerdrAgentDescendants(info, intent, root, processes) == 1 {
		return nil
	}
	return fmt.Errorf("herdr agent process identity does not match launch intent")
}

func herdrAgentProcessRoot(
	info herdrrun.PaneProcessInfo,
	intent state.HerdrIntent,
) (herdrrun.PaneProcess, map[int]herdrrun.PaneProcess, bool) {
	if intent.Launch == nil || info.ShellPID <= 1 || info.ForegroundProcessGroup <= 1 {
		return herdrrun.PaneProcess{}, nil, false
	}
	processes, ok := indexHerdrAgentProcesses(info.ForegroundProcesses)
	if !ok {
		return herdrrun.PaneProcess{}, nil, false
	}
	root, found := processes[info.ShellPID]
	valid := found && root.ProcessGroup == info.ForegroundProcessGroup &&
		root.CWD == intent.WorktreePath
	return root, processes, valid
}

func indexHerdrAgentProcesses(
	observed []herdrrun.PaneProcess,
) (map[int]herdrrun.PaneProcess, bool) {
	processes := make(map[int]herdrrun.PaneProcess, len(observed))
	for _, process := range observed {
		if !validObservedHerdrProcess(process) || processes[process.PID].PID != 0 {
			return nil, false
		}
		processes[process.PID] = process
	}
	return processes, true
}

func validObservedHerdrProcess(process herdrrun.PaneProcess) bool {
	return process.PID > 1 && process.ParentPID >= 0 &&
		process.ProcessGroup > 1 && process.Executable != ""
}

func directHerdrAgentProcess(process herdrrun.PaneProcess, intent state.HerdrIntent) bool {
	return process.Argv0 == intent.Launch.Executable && slices.Equal(process.Argv, intent.Launch.Args)
}

func interpreterHerdrAgentProcess(process herdrrun.PaneProcess, intent state.HerdrIntent) bool {
	want := append([]string{intent.Launch.Executable}, intent.Launch.Args...)
	return process.Argv0 != intent.Launch.Executable && slices.Equal(process.Argv, want)
}

func countHerdrAgentDescendants(
	info herdrrun.PaneProcessInfo,
	intent state.HerdrIntent,
	root herdrrun.PaneProcess,
	processes map[int]herdrrun.PaneProcess,
) int {
	matches := 0
	for _, process := range processes {
		if matchesHerdrAgentDescendant(info, intent, root, process, processes) {
			matches++
		}
	}
	return matches
}

func matchesHerdrAgentDescendant(
	info herdrrun.PaneProcessInfo,
	intent state.HerdrIntent,
	root, process herdrrun.PaneProcess,
	processes map[int]herdrrun.PaneProcess,
) bool {
	return process.PID != root.PID && process.CWD == intent.WorktreePath &&
		process.ProcessGroup == info.ForegroundProcessGroup &&
		slices.Equal(process.Argv, intent.Launch.Args) &&
		herdrProcessDescendsFrom(process.PID, root.PID, processes)
}

func herdrProcessDescendsFrom(
	pid, rootPID int,
	processes map[int]herdrrun.PaneProcess,
) bool {
	seen := map[int]bool{}
	for pid != rootPID {
		if pid <= 1 || seen[pid] {
			return false
		}
		seen[pid] = true
		process, found := processes[pid]
		if !found {
			return false
		}
		pid = process.ParentPID
	}
	return true
}

func (l *Launcher) waitForHerdrAgent(
	ctx context.Context,
	intent state.HerdrIntent,
	wantAgentID string,
	codexTeamStatusPath string,
) (backend.LivePane, error) {
	deadline := time.UnixMilli(intent.ExpiresUnixMS)
	for time.Now().Before(deadline) {
		if err := codexapp.StartupFailure(codexTeamStatusPath); err != nil {
			return backend.LivePane{}, err
		}
		live, found, err := l.observeExactHerdrAgent(ctx, intent, wantAgentID)
		if err != nil {
			return backend.LivePane{}, err
		}
		if found {
			return live, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		pause := min(herdrLaunchObservationInterval, remaining)
		select {
		case <-ctx.Done():
			return backend.LivePane{}, ctx.Err()
		case <-time.After(pause):
		}
	}
	return backend.LivePane{}, fmt.Errorf("timed out waiting for exact Herdr agent identity")
}

func (l *Launcher) observeExactHerdrAgent(
	ctx context.Context,
	intent state.HerdrIntent,
	wantAgentID string,
) (backend.LivePane, bool, error) {
	stepCtx, cancel, err := herdrLaunchStepContext(ctx, intent)
	if err != nil {
		return backend.LivePane{}, false, err
	}
	panes, operationErr := l.Herdr.LivePanes(stepCtx)
	stepErr := herdrLaunchStepResult(stepCtx, cancel, operationErr)
	if err := ctx.Err(); err != nil {
		return backend.LivePane{}, false, err
	}
	if stepErr != nil {
		if herdrrun.IsRetryableObservationError(stepErr) {
			return backend.LivePane{}, false, nil
		}
		return backend.LivePane{}, false, stepErr
	}
	live, found := exactHerdrLaunchPane(intent, panes, wantAgentID)
	return live, found, nil
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
			pane.AgentProvider == intent.Launch.Agent,
		}
		if !slices.Contains(identity, false) && validOptionalHerdrAgentSession(pane.AgentSession, intent.Launch.Agent) {
			return pane, true
		}
	}
	return backend.LivePane{}, false
}

func validOptionalHerdrAgentSession(session *backend.AgentSessionRef, agentName string) bool {
	return session == nil || session.Valid() && session.Agent == agentName && session.Source == "herdr:"+agentName
}
