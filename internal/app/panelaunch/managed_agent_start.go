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

	"github.com/butaosuinu/fanout/internal/app/agentprocess"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const managedLaunchStepTimeout = 5 * time.Second

const managedLaunchObservationInterval = 2 * time.Second

const managedLaunchLockReacquireTimeout = maxManagedRealizeTimeout

var errManagedLaunchStatePreserved = errors.New("issued Herdr launch state preserved")

type managedLaunchValidator func(*state.LaunchCapsule) error

type managedPaneSelector func(state.LaunchIntent, []backend.LivePane) (backend.LivePane, bool)

type managedAgentAdoptFunc func(
	context.Context,
	*state.LockedStore,
	state.LaunchIntent,
) (backend.LivePane, error)

type managedLaunchTransitionPending struct{}

func (managedLaunchTransitionPending) Error() string {
	return "herdr launcher is still starting the workload"
}

func (managedLaunchTransitionPending) RetryableObservation() bool { return true }

func (l *Launcher) startManagedRequestAgent(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	callerEnvironment []string,
) (backend.LivePane, error) {
	return l.startManagedAgent(
		ctx, locked, route, intent,
		func(launch *state.LaunchCapsule) error {
			return validateManagedLaunchBinding(req, launch, route)
		},
		func(intent state.LaunchIntent) (*state.LaunchCapsule, error) {
			return l.prepareManagedLaunchCapsule(req, route, intent, callerEnvironment)
		},
		func(intent state.LaunchIntent, panes []backend.LivePane) (backend.LivePane, bool) {
			return exactManagedLaunchPane(intent, panes, intent.Launch.AgentName)
		},
		func(ctx context.Context, locked *state.LockedStore, intent state.LaunchIntent) (backend.LivePane, error) {
			return l.adoptManagedAgent(ctx, req, locked, intent)
		},
	)
}

func (l *Launcher) startManagedAgent(
	ctx context.Context,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	validate managedLaunchValidator,
	build managedLaunchCapsuleBuilder,
	expected managedPaneSelector,
	adopt managedAgentAdoptFunc,
) (live backend.LivePane, retErr error) {
	if err := admitManagedAgentStartDeadline(locked, l.Info.ProjectRoot, intent); err != nil {
		return live, err
	}
	intent, err := l.prepareManagedLaunch(locked, route, intent, validate, build)
	if err != nil {
		return live, err
	}
	journal, err := locked.LaunchJournal(l.Info.ProjectRoot)
	if err != nil {
		return live, err
	}
	if err := l.admitManagedLauncher(ctx, journal, route, &intent); err != nil {
		return live, err
	}
	if err := ensureManagedLaunchActive(ctx, intent); err != nil {
		return live, err
	}
	intent.Launch.TokenIssued = true
	if err := saveManagedLaunchPhase(journal, intent); err != nil {
		return live, err
	}
	return l.finishIssuedManagedAgent(ctx, locked, route, intent, expected, adopt)
}

func admitManagedAgentStartDeadline(
	locked *state.LockedStore,
	projectRoot string,
	intent state.LaunchIntent,
) error {
	if remainingManagedLaunchTime(intent) > 0 {
		return nil
	}
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return err
	}
	return markManagedIntentManual(journal, intent, fmt.Errorf("herdr agent-start intent expired before launcher admission"))
}

func (l *Launcher) admitManagedLauncher(
	ctx context.Context,
	journal *state.LockedLaunchJournal,
	route backend.OwnedLaunchRoute,
	intent *state.LaunchIntent,
) error {
	if err := l.Managed.WaitForLauncher(ctx, intent.Resource.PaneID, intent.Launch.Nonce, remainingManagedLaunchTime(*intent)); err != nil {
		return err
	}
	verifyErr := retryManagedObservation(ctx, *intent, func(observeCtx context.Context) error {
		_, err := l.verifyManagedIdleLauncher(observeCtx, *intent, route)
		return err
	})
	if verifyErr != nil {
		if errors.Is(verifyErr, errManagedLauncherIdentityChanged) {
			return markManagedIntentManual(journal, *intent, verifyErr)
		}
		return verifyErr
	}
	if err := ensureManagedLaunchActive(ctx, *intent); err != nil {
		return err
	}
	intent.Launch.LauncherReady = true
	return saveManagedLaunchPhase(journal, *intent)
}

func (l *Launcher) finishIssuedManagedAgent(
	ctx context.Context,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	expected managedPaneSelector,
	adopt managedAgentAdoptFunc,
) (live backend.LivePane, retErr error) {
	defer func() {
		if retErr != nil && !errors.Is(retErr, errManagedLaunchStatePreserved) {
			retErr = errors.Join(retErr, l.failClosedLatestIssuedManagedLaunch(locked, intent, retErr))
		}
	}()
	if err := l.sendManagedLaunchToken(ctx, intent); err != nil {
		return live, err
	}
	live, err := l.observeStartedManagedPane(ctx, locked, route, intent, expected, adopt)
	if err != nil {
		return live, err
	}
	if _, err := os.Lstat(intent.Launch.EnvFilePath); !errors.Is(err, os.ErrNotExist) {
		return live, fmt.Errorf("herdr workload environment capsule was not consumed")
	}
	return live, nil
}

func (l *Launcher) observeStartedManagedPane(
	ctx context.Context,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	expected managedPaneSelector,
	adopt managedAgentAdoptFunc,
) (backend.LivePane, error) {
	if adopt != nil {
		return adopt(ctx, locked, intent)
	}
	if err := l.waitForManagedLaunchProcess(ctx, intent, route); err != nil {
		return backend.LivePane{}, err
	}
	return l.waitForManagedPane(ctx, intent, expected, intent.Launch.CodexTeamStatusPath)
}

func (l *Launcher) adoptManagedAgent(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	intent state.LaunchIntent,
) (backend.LivePane, error) {
	statusPath, err := managedCodexStatusPath(req, intent)
	if err != nil {
		return backend.LivePane{}, err
	}
	live, err := l.waitForManagedPaneUnlocked(ctx, locked, intent, func(intent state.LaunchIntent, panes []backend.LivePane) (backend.LivePane, bool) {
		return exactManagedLaunchPane(intent, panes, req.Agent)
	}, statusPath)
	if err != nil {
		return live, err
	}
	processIdentity, err := l.verifyAndRenameManagedAgent(ctx, intent)
	if err != nil {
		return live, err
	}
	live, err = l.waitForManagedPaneUnlocked(ctx, locked, intent, func(intent state.LaunchIntent, panes []backend.LivePane) (backend.LivePane, bool) {
		return exactManagedLaunchPane(intent, panes, intent.Launch.AgentName)
	}, statusPath)
	if err == nil {
		live.ProcessIdentity = &processIdentity
	}
	return live, err
}

func (l *Launcher) waitForManagedPaneUnlocked(
	ctx context.Context,
	locked *state.LockedStore,
	intent state.LaunchIntent,
	expected managedPaneSelector,
	codexTeamStatusPath string,
) (backend.LivePane, error) {
	if intent.Launch == nil || intent.Launch.EmitterNonce == "" {
		return l.waitForManagedPane(ctx, intent, expected, codexTeamStatusPath)
	}
	if err := locked.Unlock(); err != nil {
		return backend.LivePane{}, err
	}
	live, waitErr := l.waitForManagedPane(ctx, intent, expected, codexTeamStatusPath)
	lockErr := reacquireManagedLaunchLock(locked, l.Info.ProjectRoot, intent)
	if waitErr == nil && lockErr == nil && ctx.Err() != nil {
		waitErr = fmt.Errorf(
			"%w: launch context expired after current agent observation: %w",
			errManagedLaunchStatePreserved,
			ctx.Err(),
		)
	}
	return live, errors.Join(waitErr, lockErr)
}

func reacquireManagedLaunchLock(
	locked *state.LockedStore,
	projectRoot string,
	intent state.LaunchIntent,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), managedLaunchLockReacquireTimeout)
	defer cancel()
	reloaded, err := state.LockProjectForLaunchContext(ctx, projectRoot)
	if err != nil {
		return fmt.Errorf(
			"%w: reacquire Herdr launch lock after runtime wait: %w",
			errManagedLaunchStatePreserved,
			err,
		)
	}
	*locked = *reloaded
	return validateReacquiredManagedLaunch(locked, projectRoot, intent)
}

func validateReacquiredManagedLaunch(
	locked *state.LockedStore,
	projectRoot string,
	want state.LaunchIntent,
) error {
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return err
	}
	latest, found := journal.FindIntent(want.ID)
	if !found {
		return fmt.Errorf("issued Herdr launch intent disappeared during runtime wait")
	}
	if latest.Status == state.IntentManualCleanupRequired {
		return manualCleanupError(latest)
	}
	if !sameManagedLaunchGeneration(latest, want) {
		return fmt.Errorf("issued Herdr launch identity changed during runtime wait")
	}
	return nil
}

func sameManagedLaunchGeneration(latest, want state.LaunchIntent) bool {
	if latest.Status != state.IntentRealized || latest.Launch == nil || want.Launch == nil {
		return false
	}
	latestLaunch := *latest.Launch
	wantLaunch := *want.Launch
	latestLaunch.PendingReportedState = ""
	latestLaunch.PendingReportedSeq = 0
	latestLaunch.PendingAgentSession = nil
	wantLaunch.PendingReportedState = ""
	wantLaunch.PendingReportedSeq = 0
	wantLaunch.PendingAgentSession = nil
	latest.Launch = &latestLaunch
	want.Launch = &wantLaunch
	return reflect.DeepEqual(latest, want)
}

func (l *Launcher) failClosedLatestIssuedManagedLaunch(
	locked *state.LockedStore,
	intent state.LaunchIntent,
	cause error,
) error {
	journal, err := locked.LaunchJournal(l.Info.ProjectRoot)
	if err != nil {
		return err
	}
	latest, found := journal.FindIntent(intent.ID)
	if !found {
		return fmt.Errorf("issued Herdr launch intent disappeared during agent wait")
	}
	if latest.Status == state.IntentManualCleanupRequired {
		return manualCleanupError(latest)
	}
	return l.failClosedIssuedManagedLaunch(journal, latest, cause)
}

func (l *Launcher) verifyAndRenameManagedAgent(
	ctx context.Context,
	intent state.LaunchIntent,
) (backend.ProcessIdentity, error) {
	var process backend.PaneProcessInfo
	err := retryManagedObservation(ctx, intent, func(observeCtx context.Context) error {
		var processErr error
		process, processErr = l.Managed.ProcessInfo(observeCtx, intent.Resource.PaneID)
		return processErr
	})
	if err != nil {
		return backend.ProcessIdentity{}, err
	}
	identity, verifyErr := matchManagedAgentProcess(process, intent)
	if verifyErr != nil {
		return backend.ProcessIdentity{}, verifyErr
	}
	stepCtx, cancel, err := managedLaunchStepContext(ctx, intent)
	if err != nil {
		return backend.ProcessIdentity{}, err
	}
	err = managedLaunchStepResult(
		stepCtx, cancel,
		l.Managed.RenameAgent(stepCtx, intent.Resource.PaneID, intent.Launch.AgentName),
	)
	return identity, err
}

func saveManagedLaunchPhase(journal *state.LockedLaunchJournal, intent state.LaunchIntent) error {
	journal.UpsertIntent(intent)
	return journal.Save()
}

func (l *Launcher) sendManagedLaunchToken(ctx context.Context, intent state.LaunchIntent) error {
	stepCtx, cancel, err := managedLaunchStepContext(ctx, intent)
	if err != nil {
		return err
	}
	return managedLaunchStepResult(
		stepCtx, cancel,
		l.Managed.SendLaunchToken(stepCtx, intent.Resource.PaneID, intent.Launch.Nonce),
	)
}

func remainingManagedLaunchTime(intent state.LaunchIntent) time.Duration {
	remaining := time.Until(time.UnixMilli(intent.ExpiresUnixMS))
	if remaining <= 0 {
		return 0
	}
	if remaining > backend.DefaultWaitTimeout {
		return backend.DefaultWaitTimeout
	}
	return remaining
}

func ensureManagedLaunchActive(ctx context.Context, intent state.LaunchIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(time.UnixMilli(intent.ExpiresUnixMS)) {
		return fmt.Errorf("herdr agent-start intent expired")
	}
	return nil
}

func managedLaunchStepContext(
	ctx context.Context,
	intent state.LaunchIntent,
) (context.Context, context.CancelFunc, error) {
	if err := ensureManagedLaunchActive(ctx, intent); err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(managedLaunchStepTimeout)
	expires := time.UnixMilli(intent.ExpiresUnixMS)
	if expires.Before(deadline) {
		deadline = expires
	}
	stepCtx, cancel := context.WithDeadline(ctx, deadline)
	return stepCtx, cancel, nil
}

func managedLaunchStepResult(
	stepCtx context.Context,
	cancel context.CancelFunc,
	operationErr error,
) error {
	contextErr := stepCtx.Err()
	cancel()
	return errors.Join(operationErr, contextErr)
}

func retryManagedObservation(
	ctx context.Context,
	intent state.LaunchIntent,
	observe func(context.Context) error,
) error {
	for {
		stepCtx, cancel, err := managedLaunchStepContext(ctx, intent)
		if err != nil {
			return err
		}
		stepErr := managedLaunchStepResult(stepCtx, cancel, observe(stepCtx))
		if stepErr == nil || !backend.IsRetryableObservationError(stepErr) {
			return stepErr
		}
		if err := waitForManagedObservationRetry(ctx, intent); err != nil {
			return err
		}
	}
}

func waitForManagedObservationRetry(ctx context.Context, intent state.LaunchIntent) error {
	remaining := remainingManagedLaunchTime(intent)
	if remaining <= 0 {
		return fmt.Errorf("herdr agent-start intent expired")
	}
	timer := time.NewTimer(min(managedLaunchObservationInterval, remaining))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ensureManagedLaunchActive(ctx, intent)
	}
}

func verifyManagedLauncherProcess(
	info backend.PaneProcessInfo,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
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

func verifyManagedAgentProcess(info backend.PaneProcessInfo, intent state.LaunchIntent) error {
	_, err := matchManagedAgentProcess(info, intent)
	return err
}

func matchManagedAgentProcess(
	info backend.PaneProcessInfo,
	intent state.LaunchIntent,
) (backend.ProcessIdentity, error) {
	identity, err := agentprocess.MatchAgent(info, managedLaunchProcessIdentity(intent))
	if err != nil {
		return backend.ProcessIdentity{}, fmt.Errorf("herdr agent process identity does not match launch intent")
	}
	return identity, nil
}

func managedLaunchProcessIdentity(intent state.LaunchIntent) agentprocess.Identity {
	if intent.Launch == nil {
		return agentprocess.Identity{}
	}
	return agentprocess.Identity{
		WorktreePath: intent.WorktreePath,
		Executable:   intent.Launch.Executable, Args: intent.Launch.Args, Agent: intent.Launch.Agent,
	}
}

func (l *Launcher) waitForManagedLaunchProcess(
	ctx context.Context,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
) error {
	return retryManagedObservation(ctx, intent, func(observeCtx context.Context) error {
		process, err := l.Managed.ProcessInfo(observeCtx, intent.Resource.PaneID)
		if err != nil {
			return err
		}
		processErr := verifyManagedAgentProcess(process, intent)
		pending := verifyManagedLauncherProcess(process, intent, route) == nil ||
			agentprocess.InterpreterLaunchPending(process, managedLaunchProcessIdentity(intent))
		if processErr == nil || !pending {
			return processErr
		}
		return managedLaunchTransitionPending{}
	})
}

func (l *Launcher) waitForManagedPane(
	ctx context.Context,
	intent state.LaunchIntent,
	expected managedPaneSelector,
	codexTeamStatusPath string,
) (backend.LivePane, error) {
	deadline := time.UnixMilli(intent.ExpiresUnixMS)
	for time.Now().Before(deadline) {
		if err := codexapp.StartupFailure(codexTeamStatusPath); err != nil {
			return backend.LivePane{}, err
		}
		live, found, err := l.observeExactManagedPane(ctx, intent, expected)
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
		pause := min(managedLaunchObservationInterval, remaining)
		select {
		case <-ctx.Done():
			return backend.LivePane{}, ctx.Err()
		case <-time.After(pause):
		}
	}
	return backend.LivePane{}, fmt.Errorf("timed out waiting for exact Herdr pane identity")
}

func (l *Launcher) observeExactManagedPane(
	ctx context.Context,
	intent state.LaunchIntent,
	expected managedPaneSelector,
) (backend.LivePane, bool, error) {
	stepCtx, cancel, err := managedLaunchStepContext(ctx, intent)
	if err != nil {
		return backend.LivePane{}, false, err
	}
	panes, operationErr := l.Managed.LivePanes(stepCtx)
	stepErr := managedLaunchStepResult(stepCtx, cancel, operationErr)
	if err := ctx.Err(); err != nil {
		return backend.LivePane{}, false, err
	}
	if stepErr != nil {
		if backend.IsRetryableObservationError(stepErr) {
			return backend.LivePane{}, false, nil
		}
		return backend.LivePane{}, false, stepErr
	}
	live, found := expected(intent, panes)
	return live, found, nil
}

func exactManagedLaunchPane(
	intent state.LaunchIntent,
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
		if !slices.Contains(identity, false) && validOptionalManagedAgentSession(pane.AgentSession, intent.Launch.Agent) {
			return pane, true
		}
	}
	return backend.LivePane{}, false
}

func validOptionalManagedAgentSession(session *backend.AgentSessionRef, agentName string) bool {
	return session == nil || session.Valid() && session.Agent == agentName &&
		session.Source == backend.AgentSessionSource(agentName)
}
