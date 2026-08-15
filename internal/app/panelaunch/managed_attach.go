package panelaunch

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type managedLaunchCapsuleBuilder func(state.LaunchIntent) (*state.LaunchCapsule, error)

func (l *Launcher) attachManaged(
	req Request,
	targetPath string,
	locked *state.LockedStore,
) (Result, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), maxManagedRealizeTimeout)
	defer cancel()
	route, err := verifyManagedConsoleRoute(ctx, l.Herdr)
	if err != nil {
		return l.failManaged(req, "verify owned route", err)
	}
	intent, err := l.prepareManagedAttachedIntent(ctx, req, targetPath, locked, route)
	if err != nil {
		return l.failManaged(req, "realize attached workspace", err)
	}
	live, err := l.startManagedRequestAgent(ctx, req, locked, route, intent, os.Environ())
	if err != nil {
		return l.failManaged(req, "start attached agent", markManagedFinalizationFailure(
			locked, l.Info.ProjectRoot, intent, err,
		))
	}
	codexStatus, err := awaitManagedCodexTUI(ctx, req, locked, l.Info.ProjectRoot, intent)
	if err != nil {
		return l.failManaged(req, "start Codex TUI controller", err)
	}
	if err := finalizeManagedAttachedAgent(req, locked, l.Info.ProjectRoot, intent, live, codexStatus); err != nil {
		return l.failManaged(req, "finalize attached agent", err)
	}
	l.Log.Ok("%s: pane %s attached to %s", paneLogLabel(req), live.Ref.Pane, targetPath)
	return Result{PaneID: live.Ref.Pane, Notice: launchNotice(req)}, true
}

func (l *Launcher) prepareManagedAttachedIntent(
	ctx context.Context,
	req Request,
	targetPath string,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
) (state.LaunchIntent, error) {
	build := func(intent state.LaunchIntent) (*state.LaunchCapsule, error) {
		return l.prepareManagedLaunchCapsule(req, route, intent, os.Environ())
	}
	return realizeManagedInteractive(
		ctx, l.Herdr, locked, route,
		manualManagedCoordinatorRequest(
			l.Info.ProjectRoot, targetPath, route, req.RuntimeParent, req.Number,
		), build,
	)
}

func finalizeManagedAttachedAgent(
	req Request,
	locked *state.LockedStore,
	projectRoot string,
	intent state.LaunchIntent,
	live backend.LivePane,
	codexStatus codexapp.Status,
) error {
	return finalizeManagedPane(locked, projectRoot, intent, managedAttachedPaneBuilder(req, live, codexStatus))
}

func managedAttachedPaneBuilder(req Request, live backend.LivePane, codexStatus codexapp.Status) func(state.LaunchIntent) (state.Pane, error) {
	return func(latest state.LaunchIntent) (state.Pane, error) {
		pane := managedAttachedStatePane(req, latest, live, codexStatus)
		applyManagedLaunchTelemetry(&pane, latest)
		return pane, nil
	}
}

func managedAttachedStatePane(req Request, intent state.LaunchIntent, live backend.LivePane, codexStatus codexapp.Status) state.Pane {
	pane := statePaneForBackend(
		req, live.Ref.Pane, intent.WorktreePath, time.Now().UTC(), codexStatus, backend.Herdr, &live,
	)
	pane.Kind = state.PaneKindAttachedAgent
	return pane
}

func manualManagedCoordinatorRequest(
	projectRoot, targetPath string,
	route backend.OwnedLaunchRoute,
	runtimeParent string,
	number int,
) ManagedCoordinatorRequest {
	return ManagedCoordinatorRequest{
		Parent: ManualParentRef, RuntimeParent: runtimeParent, IssueNum: number,
		ProjectRoot: projectRoot, SourceRoot: targetPath, CWD: targetPath,
		ManagedSession: route.Session, SocketPath: route.SocketPath,
	}
}

//nolint:funlen // Keep capsule admission, realization, and rejected-launch cleanup in one lock-held transaction.
func realizeManagedInteractive(
	ctx context.Context,
	runtime ManagedLaunchRuntime,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	req ManagedCoordinatorRequest,
	build managedLaunchCapsuleBuilder,
) (state.LaunchIntent, error) {
	intentID, err := managedInteractiveIntentID(req)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	journal, err := locked.LaunchJournal(req.ProjectRoot)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	var launch *state.LaunchCapsule
	prepared := false
	if _, found := journal.FindIntent(intentID); !found {
		launch, err = build(state.LaunchIntent{ID: intentID})
		prepared = err == nil
	}
	if err != nil {
		return state.LaunchIntent{}, err
	}
	req.Launch = launch
	result, err := RealizeManagedCoordinator(ctx, req, runtime, locked, ManagedRealizeHooks{})
	if err != nil && !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		return result.Intent, errors.Join(
			err,
			discardRejectedManagedLaunch(
				runtime, locked, req.ProjectRoot, route, intentID, launch, prepared,
			),
		)
	}
	return result.Intent, nil
}

func managedInteractiveIntentID(req ManagedCoordinatorRequest) (string, error) {
	ownerProjectRoot := req.ProjectRoot
	if req.Parent == ManagedConsoleRuntimeParent {
		ownerProjectRoot = ""
	}
	return state.CoordinatorIntentID(req.Parent, ownerProjectRoot, req.IssueNum)
}

func discardRejectedManagedLaunch(
	runtime ManagedLaunchRuntime,
	locked *state.LockedStore,
	projectRoot string,
	route backend.OwnedLaunchRoute,
	intentID string,
	launch *state.LaunchCapsule,
	prepared bool,
) error {
	if !prepared {
		return nil
	}
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return err
	}
	if _, found := journal.FindIntent(intentID); found {
		return nil
	}
	return runtime.DiscardWorkloadEnvironment(route.RuntimeDir, launch)
}

func finalizeManagedPane(
	locked *state.LockedStore,
	projectRoot string,
	intent state.LaunchIntent,
	build func(state.LaunchIntent) (state.Pane, error),
) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, markManagedFinalizationFailure(locked, projectRoot, intent, retErr))
		}
	}()
	journal, latest, err := latestManagedLaunchIntent(locked, projectRoot, intent.ID)
	if err != nil {
		return err
	}
	pane, err := build(latest)
	if err != nil {
		return err
	}
	if recordErr := locked.RecordPane(pane); recordErr != nil {
		return recordErr
	}
	journal.RemoveIntent(latest.ID)
	return journal.Save()
}

func staticManagedPane(pane state.Pane) func(state.LaunchIntent) (state.Pane, error) {
	return func(state.LaunchIntent) (state.Pane, error) { return pane, nil }
}
