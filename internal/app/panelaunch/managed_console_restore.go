package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func managedConsoleJournal(locked *state.LockedStore, root string) (*state.LockedLaunchJournal, error) {
	journal, err := locked.LaunchJournal(root)
	if err != nil {
		return nil, err
	}
	if pending, found, err := journal.ServerLifecycleIntent(); err != nil || found {
		return nil, errors.Join(err, fmt.Errorf("herdr server lifecycle %s is pending", pending.Kind))
	}
	return journal, nil
}

// A completed workload can outlive a failed row/journal save. Observe it even
// after expiry, but never give an issued token another chance to run.
func realizeManagedConsole(
	ctx context.Context,
	owned ManagedLaunchRuntime,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	req ManagedCoordinatorRequest,
	build managedLaunchCapsuleBuilder,
) (state.LaunchIntent, error) {
	journal, err := managedConsoleJournal(locked, req.ProjectRoot)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	id, err := managedInteractiveIntentID(req)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	if intent, found := journal.FindIntent(id); found && intent.Launch != nil && intent.Launch.TokenIssued {
		return intent, nil
	}
	return realizeManagedInteractive(ctx, owned, locked, route, req, build)
}

// Restore only the exact saved console. Its row, synthetic number and owning
// worktree already survived restart; recovery only needs a new launch intent.
func restoreManagedConsole(
	ctx context.Context,
	locked *state.LockedStore,
	root string,
	owned ManagedSessionRuntime,
	pane state.Pane,
	shell string,
	environment []string,
) error {
	route, err := verifyManagedConsoleRoute(ctx, owned)
	if err != nil {
		return err
	}
	intent, err := savedManagedConsoleIntent(locked, root, pane, route)
	if err != nil {
		return err
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: root}, Managed: owned}
	if intent.Launch.Nonce == "" {
		process, processErr := owned.ProcessInfo(ctx, pane.PaneID)
		if processErr != nil {
			return processErr
		}
		if processErr = classifyManagedConsoleProcess(process, intent, route, shell); !errors.Is(processErr, managedLaunchTransitionPending{}) {
			return processErr
		}
		intent, err = prepareRestoredManagedConsole(ctx, launcher, locked, route, intent, shell, environment)
		if err != nil {
			return err
		}
	}
	_, err = launcher.startOrAdoptManagedConsole(ctx, locked, route, intent, shell)
	return err
}

func savedManagedConsoleIntent(
	locked *state.LockedStore,
	root string,
	pane state.Pane,
	route backend.OwnedLaunchRoute,
) (state.LaunchIntent, error) {
	journal, err := managedConsoleJournal(locked, root)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	id, err := state.CoordinatorIntentID(ManagedConsoleRuntimeParent, "", 0)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	if intent, found := journal.FindIntent(id); found {
		// A manual status with an issued token may be resolved by observation.
		binding := intent
		binding.Status = state.IntentRealized
		if !completedManagedConsoleIntentMatchesPane(binding, pane) {
			return intent, fmt.Errorf("saved Herdr console intent does not match saved pane")
		}
		return intent, validateManagedConsoleLaunch(route)(intent.Launch)
	}
	return managedConsoleIntentForPane(id, pane, route), nil
}

func managedConsoleIntentForPane(id string, pane state.Pane, route backend.OwnedLaunchRoute) state.LaunchIntent {
	return state.LaunchIntent{
		ID: id, Kind: state.IntentCoordinator, Status: state.IntentRealized,
		Parent: ManagedConsoleRuntimeParent, RuntimeParent: ManagedConsoleRuntimeParent,
		WorktreePath: pane.WorktreePath, WorkspaceLabel: pane.WorkspaceLabel,
		Session: pane.SessionID, SocketPath: pane.SocketPath,
		Resource: state.RuntimeResource{
			WorkspaceID: pane.WorkspaceID, Label: pane.WorkspaceLabel,
			PaneID: pane.PaneID, TerminalID: pane.TerminalID, CurrentPath: pane.WorktreePath,
		},
		Launch: &state.LaunchCapsule{Executable: route.LauncherPath, Args: []string{ManagedConsoleWorkloadArg}},
	}
}

func prepareRestoredManagedConsole(
	ctx context.Context,
	launcher *Launcher,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	shell string,
	environment []string,
) (state.LaunchIntent, error) {
	if _, err := launcher.verifyManagedIdleLauncher(ctx, intent, route); err != nil {
		return intent, err
	}
	journal, err := managedConsoleJournal(locked, launcher.Info.ProjectRoot)
	if err != nil {
		return intent, err
	}
	intent.Launch, err = newManagedConsoleLaunch(launcher.Managed, route, shell, environment)
	if err != nil {
		return intent, err
	}
	intent.ExpiresUnixMS = managedRestartResumeDeadline(ctx, maxManagedRealizeTimeout).UnixMilli()
	return persistNewManagedLaunch(launcher.Managed, journal, intent, route.RuntimeDir)
}

func (l *Launcher) startOrAdoptManagedConsole(
	ctx context.Context,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	shell string,
) (backend.LivePane, error) {
	if err := validateManagedConsoleLaunch(route)(intent.Launch); err != nil {
		return backend.LivePane{}, err
	}
	if intent.Session != route.Session || intent.SocketPath != route.SocketPath {
		return backend.LivePane{}, fmt.Errorf("saved Herdr console launch route changed")
	}
	if intent.Launch.TokenIssued {
		return l.recoverStartedManagedConsole(ctx, locked, route, intent, shell)
	}
	if intent.Status != state.IntentRealized {
		return backend.LivePane{}, manualCleanupError(intent)
	}
	return l.startManagedAgent(
		ctx, locked, route, intent, validateManagedConsoleLaunch(route), nil, exactManagedConsolePane,
		func(adoptCtx context.Context, _ *state.LockedStore, issued state.LaunchIntent) (backend.LivePane, error) {
			return l.adoptManagedConsolePane(adoptCtx, issued, route, shell)
		},
	)
}

func (l *Launcher) recoverStartedManagedConsole(
	ctx context.Context,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	shell string,
) (backend.LivePane, error) {
	// Bound observation separately; expiry must never renew the saved launch.
	observation := intent
	observation.ExpiresUnixMS = time.Now().Add(maxManagedRecoveryClassificationTimeout).UnixMilli()
	live, err := l.adoptManagedConsolePane(ctx, observation, route, shell)
	if err != nil {
		return live, err
	}
	if _, statErr := os.Lstat(intent.Launch.EnvFilePath); !errors.Is(statErr, os.ErrNotExist) {
		return live, fmt.Errorf("herdr console environment capsule was not consumed")
	}
	journal, err := managedConsoleJournal(locked, l.Info.ProjectRoot)
	if err != nil {
		return live, err
	}
	intent.Status, intent.Failure = state.IntentRealized, ""
	return live, saveManagedLaunchPhase(journal, intent)
}

func exactManagedConsolePane(intent state.LaunchIntent, panes []backend.LivePane) (backend.LivePane, bool) {
	count := 0
	for _, pane := range panes {
		if pane.Ref.Workspace == intent.Resource.WorkspaceID {
			count++
		}
	}
	if count != 1 {
		return backend.LivePane{}, false
	}
	return exactManagedShellPane(intent, panes)
}
