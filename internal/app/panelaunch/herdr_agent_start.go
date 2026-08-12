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

const herdrLaunchLockReacquireTimeout = maxHerdrRealizeTimeout

var errHerdrLaunchStatePreserved = errors.New("issued Herdr launch state preserved")

type herdrLaunchValidator func(*state.HerdrLaunch) error

type herdrPaneSelector func(state.HerdrIntent, []backend.LivePane) (backend.LivePane, bool)

type herdrAgentAdoptFunc func(
	context.Context,
	*state.LockedStore,
	state.HerdrIntent,
) (backend.LivePane, error)

type herdrLaunchTransitionPending struct{}

func (herdrLaunchTransitionPending) Error() string {
	return "herdr launcher is still starting the workload"
}

func (herdrLaunchTransitionPending) RetryableObservation() bool { return true }

func (l *Launcher) startHerdrRequestAgent(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
	callerEnvironment []string,
) (backend.LivePane, error) {
	return l.startHerdrAgent(
		ctx, locked, route, intent,
		func(launch *state.HerdrLaunch) error {
			return validateHerdrLaunchBinding(req, launch)
		},
		func(intent state.HerdrIntent) (*state.HerdrLaunch, error) {
			return l.prepareHerdrLaunchCapsule(req, route, intent, callerEnvironment)
		},
		func(intent state.HerdrIntent, panes []backend.LivePane) (backend.LivePane, bool) {
			return exactHerdrLaunchPane(intent, panes, intent.Launch.AgentName)
		},
		func(ctx context.Context, locked *state.LockedStore, intent state.HerdrIntent) (backend.LivePane, error) {
			return l.adoptHerdrAgent(ctx, req, locked, intent)
		},
	)
}

func (l *Launcher) startHerdrAgent(
	ctx context.Context,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
	validate herdrLaunchValidator,
	build herdrLaunchCapsuleBuilder,
	expected herdrPaneSelector,
	adopt herdrAgentAdoptFunc,
) (live backend.LivePane, retErr error) {
	if err := admitHerdrAgentStartDeadline(locked, l.Info.ProjectRoot, intent); err != nil {
		return live, err
	}
	intent, err := l.prepareHerdrLaunch(locked, route, intent, validate, build)
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
	return l.finishIssuedHerdrAgent(ctx, locked, route, intent, expected, adopt)
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
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
	expected herdrPaneSelector,
	adopt herdrAgentAdoptFunc,
) (live backend.LivePane, retErr error) {
	defer func() {
		if retErr != nil && !errors.Is(retErr, errHerdrLaunchStatePreserved) {
			retErr = errors.Join(retErr, l.failClosedLatestIssuedHerdrLaunch(locked, intent, retErr))
		}
	}()
	if err := l.sendHerdrLaunchToken(ctx, intent); err != nil {
		return live, err
	}
	live, err := l.observeStartedHerdrPane(ctx, locked, route, intent, expected, adopt)
	if err != nil {
		return live, err
	}
	if _, err := os.Lstat(intent.Launch.EnvFilePath); !errors.Is(err, os.ErrNotExist) {
		return live, fmt.Errorf("herdr workload environment capsule was not consumed")
	}
	return live, nil
}

func (l *Launcher) observeStartedHerdrPane(
	ctx context.Context,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
	expected herdrPaneSelector,
	adopt herdrAgentAdoptFunc,
) (backend.LivePane, error) {
	if adopt != nil {
		return adopt(ctx, locked, intent)
	}
	if err := l.waitForHerdrLaunchProcess(ctx, intent, route); err != nil {
		return backend.LivePane{}, err
	}
	return l.waitForHerdrPane(ctx, intent, expected, intent.Launch.CodexTeamStatusPath)
}

func (l *Launcher) adoptHerdrAgent(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	statusPath, err := herdrCodexStatusPath(req, intent)
	if err != nil {
		return backend.LivePane{}, err
	}
	live, err := l.waitForHerdrPaneUnlocked(ctx, locked, intent, func(intent state.HerdrIntent, panes []backend.LivePane) (backend.LivePane, bool) {
		return exactHerdrLaunchPane(intent, panes, req.Agent)
	}, statusPath)
	if err != nil {
		return live, err
	}
	if err := l.verifyAndRenameHerdrAgent(ctx, intent); err != nil {
		return live, err
	}
	return l.waitForHerdrPaneUnlocked(ctx, locked, intent, func(intent state.HerdrIntent, panes []backend.LivePane) (backend.LivePane, bool) {
		return exactHerdrLaunchPane(intent, panes, intent.Launch.AgentName)
	}, statusPath)
}

func (l *Launcher) waitForHerdrPaneUnlocked(
	ctx context.Context,
	locked *state.LockedStore,
	intent state.HerdrIntent,
	expected herdrPaneSelector,
	codexTeamStatusPath string,
) (backend.LivePane, error) {
	if intent.Launch == nil || intent.Launch.EmitterNonce == "" {
		return l.waitForHerdrPane(ctx, intent, expected, codexTeamStatusPath)
	}
	if err := locked.Unlock(); err != nil {
		return backend.LivePane{}, err
	}
	live, waitErr := l.waitForHerdrPane(ctx, intent, expected, codexTeamStatusPath)
	lockErr := reacquireHerdrLaunchLock(locked, l.Info.ProjectRoot, intent)
	if waitErr == nil && lockErr == nil && ctx.Err() != nil {
		waitErr = fmt.Errorf(
			"%w: launch context expired after current agent observation: %w",
			errHerdrLaunchStatePreserved,
			ctx.Err(),
		)
	}
	return live, errors.Join(waitErr, lockErr)
}

func reacquireHerdrLaunchLock(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), herdrLaunchLockReacquireTimeout)
	defer cancel()
	reloaded, err := state.LockProjectForLaunchContext(ctx, projectRoot)
	if err != nil {
		return fmt.Errorf(
			"%w: reacquire Herdr launch lock after runtime wait: %w",
			errHerdrLaunchStatePreserved,
			err,
		)
	}
	*locked = *reloaded
	return validateReacquiredHerdrLaunch(locked, projectRoot, intent)
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
		return fmt.Errorf("issued Herdr launch intent disappeared during runtime wait")
	}
	if latest.Status == state.HerdrIntentManualCleanupRequired {
		return herdrManualCleanupError(latest)
	}
	if !sameHerdrLaunchGeneration(latest, want) {
		return fmt.Errorf("issued Herdr launch identity changed during runtime wait")
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
	latestLaunch.PendingAgentSession = nil
	wantLaunch.PendingReportedState = ""
	wantLaunch.PendingAgentSession = nil
	latest.Launch = &latestLaunch
	want.Launch = &wantLaunch
	return reflect.DeepEqual(latest, want)
}

func (l *Launcher) failClosedLatestIssuedHerdrLaunch(
	locked *state.LockedStore,
	intent state.HerdrIntent,
	cause error,
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
	return l.failClosedIssuedHerdrLaunch(journal, latest, cause)
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

func (l *Launcher) sendHerdrLaunchToken(ctx context.Context, intent state.HerdrIntent) error {
	stepCtx, cancel, err := herdrLaunchStepContext(ctx, intent)
	if err != nil {
		return err
	}
	return herdrLaunchStepResult(
		stepCtx, cancel,
		l.Herdr.SendLaunchToken(stepCtx, intent.Resource.PaneID, intent.Launch.Nonce),
	)
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
			process.ProcessGroup == info.ForegroundProcessGroup && process.Executable == route.LauncherPath &&
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
		Executable:   intent.Launch.Executable, Args: intent.Launch.Args, Agent: intent.Launch.Agent,
	}
}

func (l *Launcher) waitForHerdrLaunchProcess(
	ctx context.Context,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) error {
	return retryHerdrObservation(ctx, intent, func(observeCtx context.Context) error {
		process, err := l.Herdr.ProcessInfo(observeCtx, intent.Resource.PaneID)
		if err != nil {
			return err
		}
		processErr := verifyHerdrAgentProcess(process, intent)
		if processErr == nil || verifyHerdrLauncherProcess(process, intent, route) != nil {
			return processErr
		}
		return herdrLaunchTransitionPending{}
	})
}

func (l *Launcher) waitForHerdrPane(
	ctx context.Context,
	intent state.HerdrIntent,
	expected herdrPaneSelector,
	codexTeamStatusPath string,
) (backend.LivePane, error) {
	deadline := time.UnixMilli(intent.ExpiresUnixMS)
	for time.Now().Before(deadline) {
		if err := codexapp.StartupFailure(codexTeamStatusPath); err != nil {
			return backend.LivePane{}, err
		}
		live, found, err := l.observeExactHerdrPane(ctx, intent, expected)
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
	return backend.LivePane{}, fmt.Errorf("timed out waiting for exact Herdr pane identity")
}

func (l *Launcher) observeExactHerdrPane(
	ctx context.Context,
	intent state.HerdrIntent,
	expected herdrPaneSelector,
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
	live, found := expected(intent, panes)
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
			intent.Resource.Label != "", pane.WorkspaceLabel == intent.Resource.Label,
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
