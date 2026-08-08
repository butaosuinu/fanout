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

type herdrLaunchCapsuleBuilder func(string) (*state.HerdrLaunch, error)

func (l *Launcher) attachHerdr(req Request, targetPath string) (Result, bool) {
	locked, ok := l.admitHerdrLaunchRequest(req)
	if !ok {
		return Result{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxHerdrRealizeTimeout)
	defer cancel()
	route, err := verifyHerdrConsoleRoute(ctx, l.Herdr)
	if err != nil {
		return l.failHerdr(req, "verify owned route", err)
	}
	intent, err := realizeHerdrInteractive(
		ctx, l.Herdr, locked, route,
		manualHerdrCoordinatorRequest(l.Info.ProjectRoot, targetPath, route, req.Number),
		func(intentID string) (*state.HerdrLaunch, error) {
			return l.buildHerdrAgentLaunch(req, route, intentID, os.Environ())
		},
	)
	if err != nil {
		return l.failHerdr(req, "realize attached workspace", err)
	}
	live, err := l.startHerdrRequestAgent(ctx, req, locked, route, intent, os.Environ())
	if err != nil {
		return l.failHerdr(req, "start attached agent", markHerdrFinalizationFailure(
			locked, l.Info.ProjectRoot, intent, err,
		))
	}
	if err := finalizeHerdrAttached(req, locked, l.Info.ProjectRoot, intent, live); err != nil {
		return l.failHerdr(req, "finalize attached agent", err)
	}
	l.Log.Ok("%s: pane %s attached to %s", paneLogLabel(req), live.Ref.Pane, targetPath)
	return Result{PaneID: live.Ref.Pane, Notice: launchNotice(req)}, true
}

func manualHerdrCoordinatorRequest(
	projectRoot, targetPath string,
	route herdrrun.OwnedLaunchRoute,
	number int,
) HerdrCoordinatorRequest {
	return HerdrCoordinatorRequest{
		Parent: ManualParentRef, IssueNum: number,
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
		launch, err = build(intentID)
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

func finalizeHerdrAttached(
	req Request,
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	live backend.LivePane,
) (retErr error) {
	pane := statePaneForBackend(
		req, live.Ref.Pane, intent.WorktreePath, time.Now().UTC(),
		codexapp.Status{}, backend.Herdr, &live,
	)
	pane.Kind = state.PaneKindAttachedAgent
	return finalizeHerdrInteractive(locked, projectRoot, intent, pane)
}

func finalizeHerdrInteractive(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	pane state.Pane,
) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, markHerdrFinalizationFailure(locked, projectRoot, intent, retErr))
		}
	}()
	if err := locked.RecordPane(pane); err != nil {
		return err
	}
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	journal.RemoveIntent(intent.ID)
	return journal.Save()
}
