package panelaunch

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func (l *Launcher) attachHerdr(req Request, targetPath string) (Result, bool) {
	locked, ok := l.admitHerdrLaunchRequest(req)
	if !ok {
		return Result{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxHerdrRealizeTimeout)
	defer cancel()
	if err := l.Herdr.VerifyOwned(ctx); err != nil {
		return l.failHerdr(req, "verify owned session", err)
	}
	route, err := l.Herdr.LaunchRoute()
	if err != nil {
		return l.failHerdr(req, "resolve owned route", err)
	}
	intent, err := l.realizeHerdrAttached(ctx, req, targetPath, locked, route)
	if err != nil {
		return l.failHerdr(req, "realize attached workspace", err)
	}
	live, err := l.startHerdrAgent(ctx, req, locked, route, intent, os.Environ())
	if err != nil {
		return l.failHerdr(req, "start attached agent", markHerdrAttachedFailure(locked, l.Info.ProjectRoot, intent, err))
	}
	if err := finalizeHerdrAttached(req, locked, l.Info.ProjectRoot, intent, live); err != nil {
		return l.failHerdr(req, "finalize attached agent", err)
	}
	l.Log.Ok("%s: pane %s attached to %s", paneLogLabel(req), live.Ref.Pane, targetPath)
	return Result{PaneID: live.Ref.Pane, Notice: launchNotice(req)}, true
}

func (l *Launcher) realizeHerdrAttached(
	ctx context.Context,
	req Request,
	targetPath string,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
) (state.HerdrIntent, error) {
	launch, prepared, err := l.prepareHerdrAttachedLaunch(req, locked, route)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	result, err := RealizeHerdrCoordinator(ctx, HerdrCoordinatorRequest{
		Parent: ManualParentRef, IssueNum: req.Number,
		ProjectRoot: l.Info.ProjectRoot, SourceRoot: targetPath, CWD: targetPath,
		HerdrSession: route.Session, SocketPath: route.SocketPath, Launch: launch,
	}, l.Herdr, locked, HerdrRealizeHooks{})
	if err != nil && !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		return result.Intent, errors.Join(
			err,
			discardRejectedAttachedLaunch(
				locked, l.Info.ProjectRoot, route, launch, prepared, req.Number,
			),
		)
	}
	return result.Intent, nil
}

func (l *Launcher) prepareHerdrAttachedLaunch(
	req Request,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
) (*state.HerdrLaunch, bool, error) {
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return nil, false, err
	}
	intentID, err := state.HerdrCoordinatorIntentID(ManualParentRef, l.Info.ProjectRoot, req.Number)
	if err != nil {
		return nil, false, err
	}
	if _, found := journal.FindIntent(intentID); found {
		return nil, false, nil
	}
	return l.newHerdrAttachedLaunch(req, route, intentID)
}

func (l *Launcher) newHerdrAttachedLaunch(
	req Request,
	route herdrrun.OwnedLaunchRoute,
	intentID string,
) (*state.HerdrLaunch, bool, error) {
	nonce, err := randomHerdrToken()
	if err != nil {
		return nil, false, err
	}
	spec, err := agent.BuildResolvedLaunchSpec(req.Agent, req.Prompt, backend.Herdr, req.LaunchMode)
	if err != nil {
		return nil, false, err
	}
	environment, err := herdrrun.WorkloadEnvironment(os.Environ(), route.LauncherPath)
	if err != nil {
		return nil, false, err
	}
	envPath, envCount, err := l.Herdr.PrepareWorkloadEnvironment(nonce, environment)
	if err != nil {
		return nil, false, err
	}
	return &state.HerdrLaunch{
		Nonce: nonce, Agent: req.Agent,
		AgentName:  naming.HerdrAgentName(route.GitCommonDir, intentID, nonce),
		Executable: spec.Executable, Args: spec.Args,
		EnvFilePath: envPath, EnvNameCount: envCount,
	}, true, nil
}

func discardRejectedAttachedLaunch(
	locked *state.LockedStore,
	projectRoot string,
	route herdrrun.OwnedLaunchRoute,
	launch *state.HerdrLaunch,
	prepared bool,
	issueNum int,
) error {
	if !prepared {
		return nil
	}
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	intentID, err := state.HerdrCoordinatorIntentID(ManualParentRef, projectRoot, issueNum)
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
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, markHerdrFinalizationFailure(locked, projectRoot, intent, retErr))
		}
	}()
	pane := statePaneForBackend(
		req, live.Ref.Pane, intent.WorktreePath, time.Now().UTC(),
		codexapp.Status{}, backend.Herdr, &live,
	)
	pane.Kind = state.PaneKindAttachedAgent
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

func markHerdrAttachedFailure(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	cause error,
) error {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return errors.Join(cause, err)
	}
	latest, found := journal.FindIntent(intent.ID)
	if !found {
		return cause
	}
	return errors.Join(cause, markHerdrIntentManual(journal, latest, cause))
}
