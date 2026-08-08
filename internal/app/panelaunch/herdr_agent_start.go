package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/butaosuinu/fanout/internal/app/herdrprocess"
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
	return l.finishIssuedHerdrAgent(ctx, req, locked, intent)
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
	locked *state.LockedStore,
	intent state.HerdrIntent,
) (live backend.LivePane, retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, l.failClosedLatestIssuedHerdrLaunch(locked, intent))
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
	live, err = l.adoptHerdrAgent(ctx, req, locked, intent)
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
	locked *state.LockedStore,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	statusPath, err := herdrCodexTeamStatusPath(req, intent)
	if err != nil {
		return backend.LivePane{}, err
	}
	live, err := l.waitForHerdrAgentUnlocked(ctx, locked, intent, req.Agent, statusPath)
	if err != nil {
		return live, err
	}
	if err := l.verifyAndRenameHerdrAgent(ctx, intent); err != nil {
		return live, err
	}
	return l.waitForHerdrAgentUnlocked(ctx, locked, intent, intent.Launch.AgentName, statusPath)
}

func (l *Launcher) waitForHerdrAgentUnlocked(
	ctx context.Context,
	locked *state.LockedStore,
	intent state.HerdrIntent,
	wantAgentID string,
	codexTeamStatusPath string,
) (backend.LivePane, error) {
	if intent.Launch == nil || intent.Launch.EmitterNonce == "" {
		return l.waitForHerdrAgent(ctx, intent, wantAgentID, codexTeamStatusPath)
	}
	if err := locked.Unlock(); err != nil {
		return backend.LivePane{}, err
	}
	live, waitErr := l.waitForHerdrAgent(ctx, intent, wantAgentID, codexTeamStatusPath)
	reloaded, lockErr := state.LockProjectForLaunch(l.Info.ProjectRoot)
	if lockErr == nil {
		*locked = *reloaded
		lockErr = validateReacquiredHerdrLaunch(locked, l.Info.ProjectRoot, intent)
	} else {
		lockErr = fmt.Errorf("reacquire launch lock after Herdr agent wait: %w", lockErr)
	}
	return live, errors.Join(waitErr, lockErr)
}

func validateReacquiredHerdrLaunch(
	locked *state.LockedStore,
	projectRoot string,
	want state.HerdrIntent,
) error {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	latest, found := journal.FindIntent(want.ID)
	if !found {
		return fmt.Errorf("issued Herdr launch intent disappeared during agent wait")
	}
	if latest.Status == state.HerdrIntentManualCleanupRequired {
		return herdrManualCleanupError(latest)
	}
	if !sameHerdrLaunchGeneration(latest, want) {
		return fmt.Errorf("issued Herdr launch identity changed during agent wait")
	}
	return nil
}

func sameHerdrLaunchGeneration(latest, want state.HerdrIntent) bool {
	if latest.Status != state.HerdrIntentRealized || latest.Launch == nil || want.Launch == nil {
		return false
	}
	latestLaunch := *latest.Launch
	wantLaunch := *want.Launch
	latestLaunch.PendingReportedState = ""
	wantLaunch.PendingReportedState = ""
	latest.Launch = &latestLaunch
	want.Launch = &wantLaunch
	return reflect.DeepEqual(latest, want)
}

func (l *Launcher) failClosedLatestIssuedHerdrLaunch(
	locked *state.LockedStore,
	intent state.HerdrIntent,
) error {
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return err
	}
	latest, found := journal.FindIntent(intent.ID)
	if !found {
		return fmt.Errorf("issued Herdr launch intent disappeared during agent wait")
	}
	if latest.Status == state.HerdrIntentManualCleanupRequired {
		return herdrManualCleanupError(latest)
	}
	return l.failClosedIssuedHerdrLaunch(journal, latest)
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
	if err := herdrprocess.VerifyAgent(info, herdrLaunchProcessIdentity(intent)); err != nil {
		return fmt.Errorf("herdr agent process identity does not match launch intent")
	}
	return nil
}

func herdrLaunchProcessIdentity(intent state.HerdrIntent) herdrprocess.Identity {
	if intent.Launch == nil {
		return herdrprocess.Identity{}
	}
	return herdrprocess.Identity{
		WorktreePath: intent.WorktreePath,
		Executable:   intent.Launch.Executable, Args: intent.Launch.Args,
	}
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
