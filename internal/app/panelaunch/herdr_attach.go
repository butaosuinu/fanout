package panelaunch

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type herdrLaunchCapsuleBuilder func(state.HerdrIntent) (*state.HerdrLaunch, error)

func (l *Launcher) attachHerdr(
	req Request,
	targetPath string,
	locked *state.LockedStore,
) (Result, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), maxHerdrRealizeTimeout)
	defer cancel()
	route, err := verifyHerdrConsoleRoute(ctx, l.Herdr)
	if err != nil {
		return l.failHerdr(req, "verify owned route", err)
	}
	intent, err := l.prepareHerdrAttachedIntent(ctx, req, targetPath, locked, route)
	if err != nil {
		return l.failHerdr(req, "realize attached workspace", err)
	}
	live, err := l.startHerdrRequestAgent(ctx, req, locked, route, intent, os.Environ())
	if err != nil {
		return l.failHerdr(req, "start attached agent", markHerdrFinalizationFailure(
			locked, l.Info.ProjectRoot, intent, err,
		))
	}
	codexStatus, err := awaitHerdrCodexTUI(ctx, req, locked, l.Info.ProjectRoot, intent)
	if err != nil {
		return l.failHerdr(req, "start Codex TUI controller", err)
	}
	if err := finalizeHerdrAttachedAgent(req, locked, l.Info.ProjectRoot, intent, live, codexStatus); err != nil {
		return l.failHerdr(req, "finalize attached agent", err)
	}
	l.Log.Ok("%s: pane %s attached to %s", paneLogLabel(req), live.Ref.Pane, targetPath)
	return Result{PaneID: live.Ref.Pane, Notice: launchNotice(req)}, true
}

func (l *Launcher) prepareHerdrAttachedIntent(
	ctx context.Context,
	req Request,
	targetPath string,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
) (state.HerdrIntent, error) {
	build := func(intent state.HerdrIntent) (*state.HerdrLaunch, error) {
		return l.prepareHerdrLaunchCapsule(req, route, intent, os.Environ())
	}
	return realizeHerdrInteractive(
		ctx, l.Herdr, locked, route,
		manualHerdrCoordinatorRequest(
			l.Info.ProjectRoot, targetPath, route, req.RuntimeParent, req.Number,
		), build,
	)
}

func finalizeHerdrAttachedAgent(
	req Request,
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	live backend.LivePane,
	codexStatus codexapp.Status,
) error {
	return finalizeHerdrPane(locked, projectRoot, intent, herdrAttachedPaneBuilder(req, live, codexStatus))
}

func herdrAttachedPaneBuilder(req Request, live backend.LivePane, codexStatus codexapp.Status) func(state.HerdrIntent) (state.Pane, error) {
	return func(latest state.HerdrIntent) (state.Pane, error) {
		pane := herdrAttachedStatePane(req, latest, live, codexStatus)
		applyHerdrLaunchTelemetry(&pane, latest)
		return pane, nil
	}
}

func herdrAttachedStatePane(req Request, intent state.HerdrIntent, live backend.LivePane, codexStatus codexapp.Status) state.Pane {
	pane := statePaneForBackend(
		req, live.Ref.Pane, intent.WorktreePath, time.Now().UTC(), codexStatus, backend.Herdr, &live,
	)
	pane.Kind = state.PaneKindAttachedAgent
	return pane
}

func manualHerdrCoordinatorRequest(
	projectRoot, targetPath string,
	route herdrrun.OwnedLaunchRoute,
	runtimeParent string,
	number int,
) HerdrCoordinatorRequest {
	return HerdrCoordinatorRequest{
		Parent: ManualParentRef, RuntimeParent: runtimeParent, IssueNum: number,
		ProjectRoot: projectRoot, SourceRoot: targetPath, CWD: targetPath,
		HerdrSession: route.Session, SocketPath: route.SocketPath,
	}
}

//nolint:funlen // Keep capsule admission, realization, and rejected-launch cleanup in one lock-held transaction.
func realizeHerdrInteractive(
	ctx context.Context,
	runtime HerdrLaunchRuntime,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	req HerdrCoordinatorRequest,
	build herdrLaunchCapsuleBuilder,
) (state.HerdrIntent, error) {
	intentID, err := herdrInteractiveIntentID(req)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	journal, err := locked.HerdrIntents(req.ProjectRoot)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	var launch *state.HerdrLaunch
	prepared := false
	if _, found := journal.FindIntent(intentID); !found {
		launch, err = build(state.HerdrIntent{ID: intentID})
		prepared = err == nil
	}
	if err != nil {
		return state.HerdrIntent{}, err
	}
	req.Launch = launch
	result, err := RealizeHerdrCoordinator(ctx, req, runtime, locked, HerdrRealizeHooks{})
	if err != nil && !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		return result.Intent, errors.Join(
			err,
			discardRejectedHerdrLaunch(
				locked, req.ProjectRoot, route, intentID, launch, prepared,
			),
		)
	}
	return result.Intent, nil
}

func herdrInteractiveIntentID(req HerdrCoordinatorRequest) (string, error) {
	ownerProjectRoot := req.ProjectRoot
	if req.Parent == HerdrConsoleRuntimeParent {
		ownerProjectRoot = ""
	}
	return state.HerdrCoordinatorIntentID(req.Parent, ownerProjectRoot, req.IssueNum)
}

func discardRejectedHerdrLaunch(
	locked *state.LockedStore,
	projectRoot string,
	route herdrrun.OwnedLaunchRoute,
	intentID string,
	launch *state.HerdrLaunch,
	prepared bool,
) error {
	if !prepared {
		return nil
	}
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	if _, found := journal.FindIntent(intentID); found {
		return nil
	}
	return herdrrun.DiscardWorkloadEnvironment(route.RuntimeDir, launch)
}

func finalizeHerdrPane(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	build func(state.HerdrIntent) (state.Pane, error),
) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, markHerdrFinalizationFailure(locked, projectRoot, intent, retErr))
		}
	}()
	journal, latest, err := latestHerdrLaunchIntent(locked, projectRoot, intent.ID)
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

func staticHerdrPane(pane state.Pane) func(state.HerdrIntent) (state.Pane, error) {
	return func(state.HerdrIntent) (state.Pane, error) { return pane, nil }
}
