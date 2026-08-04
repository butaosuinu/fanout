package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/displayname"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type HerdrLaunchRuntime interface {
	HerdrWorktreeRuntime
	VerifyOwned(context.Context) error
	LaunchRoute() (herdrrun.OwnedLaunchRoute, error)
	PrepareWorkloadEnvironment(string, []string) (string, int, error)
	WaitForLauncher(context.Context, string, string, time.Duration) error
	ProcessInfo(context.Context, string) (herdrrun.PaneProcessInfo, error)
	SendLaunchToken(context.Context, string, string) error
	LivePanes(context.Context) ([]backend.LivePane, error)
	RenameAgent(context.Context, string, string) error
	RemoveWorktree(context.Context, string, string) error
}

var errHerdrLaunchResponseLost = errors.New("herdr agent launch response was lost; refusing automatic adoption")

func (l *Launcher) launchHerdr(req Request) (Result, bool) {
	l.preflightClaudeLaunchMode(&req)
	if l.Cfg.DryRun {
		return l.dryRunHerdr(req)
	}
	operation, ok := l.prepareHerdrOperation(req)
	if !ok {
		return Result{}, false
	}
	defer operation.cancel()
	intent, err := l.realizeHerdrLaunch(req, operation)
	if err != nil {
		return l.failHerdr(req, "realize launch", l.rollbackFailedHerdrLaunch(operation.locked, intent, err))
	}
	live, err := l.startHerdrAgent(operation.ctx, req, operation.locked, operation.route, intent, operation.environment)
	if err != nil {
		return l.failHerdr(req, "start agent", l.rollbackFailedHerdrLaunch(operation.locked, intent, err))
	}
	if err := l.finalizeHerdrLaunch(req, operation.locked, intent, live); err != nil {
		return l.failHerdr(req, "finalize launch", err)
	}
	l.Log.Ok("%s: pane %s created in %s", paneLogLabel(req), live.Ref.Pane, intent.WorktreePath)
	return Result{PaneID: live.Ref.Pane, Notice: launchNotice(req)}, true
}

type herdrLaunchOperation struct {
	ctx         context.Context
	locked      *state.LockedStore
	route       herdrrun.OwnedLaunchRoute
	environment []string
	cancel      context.CancelFunc
}

func (l *Launcher) prepareHerdrOperation(req Request) (herdrLaunchOperation, bool) {
	locked, ok := l.admitHerdrLaunchRequest(req)
	if !ok {
		return herdrLaunchOperation{}, false
	}
	operation := herdrLaunchOperation{
		ctx: context.Background(), locked: locked,
		environment: append([]string(nil), os.Environ()...),
	}
	if err := l.Herdr.VerifyOwned(operation.ctx); err != nil {
		l.Log.Err("%s: verify owned Herdr session: %v", paneLogLabel(req), err)
		return herdrLaunchOperation{}, false
	}
	var err error
	operation.route, err = l.Herdr.LaunchRoute()
	if err != nil {
		l.Log.Err("%s: resolve owned Herdr route: %v", paneLogLabel(req), err)
		return herdrLaunchOperation{}, false
	}
	if req.BriefingPath != "" && !l.writeBriefing(req) {
		return herdrLaunchOperation{}, false
	}
	logPaneRequest(req, l.Log)
	operation.ctx, operation.cancel = context.WithTimeout(context.Background(), maxHerdrRealizeTimeout)
	return operation, true
}

func (l *Launcher) admitHerdrLaunchRequest(req Request) (*state.LockedStore, bool) {
	locked, ok := l.Recorder.(*state.LockedStore)
	if !ok || l.Herdr == nil {
		l.Log.Err("%s: Herdr launch requires an owned session and combined launch lock", paneLogLabel(req))
		return nil, false
	}
	operation := ""
	if req.CodexPlanMode() {
		operation = "Codex Plan Mode child launch until issue #554"
	} else if req.CodexTeamRequested || req.CodexTeamMode {
		operation = "--team launch until issue #568"
	}
	if operation != "" {
		l.Log.Err("%s: %v", paneLogLabel(req), backend.Unsupported(backend.Herdr, operation))
		return nil, false
	}
	return locked, true
}

func (l *Launcher) realizeHerdrLaunch(
	req Request,
	operation herdrLaunchOperation,
) (state.HerdrIntent, error) {
	coordinator, err := l.realizeHerdrCoordinator(operation.ctx, req, operation.locked, operation.route)
	if err != nil {
		return state.HerdrIntent{}, fmt.Errorf("realize coordinator: %w", err)
	}
	if err := l.recordHerdrCoordinator(req, operation.locked, coordinator, operation.route); err != nil {
		return state.HerdrIntent{}, fmt.Errorf("record coordinator: %w", err)
	}
	intent, err := l.realizeHerdrChild(operation.ctx, req, operation.locked, operation.route)
	if err != nil {
		return state.HerdrIntent{}, fmt.Errorf("realize worktree: %w", err)
	}
	result := hooks.RunBlocking(hooks.WorktreeCreated, paneHookContext(req, l.Info.ProjectRoot, intent.WorktreePath, ""), req.Hooks, l.Log)
	if !result.OK() {
		printPaneHookOutput(result, l.Log)
		return intent, fmt.Errorf("run worktree-created hook: %w", result.Err)
	}
	hooks.RunBackground(hooks.BeforePaneCreate, paneHookContext(req, l.Info.ProjectRoot, intent.WorktreePath, intent.Resource.PaneID), req.Hooks, l.Log)
	return intent, nil
}

func (l *Launcher) failHerdr(req Request, action string, err error) (Result, bool) {
	l.Log.Err("%s: %s: %v", paneLogLabel(req), action, err)
	return Result{}, false
}

func (l *Launcher) dryRunHerdr(req Request) (Result, bool) {
	agentCmd, err := buildAgentCommandForBackend(l.Cfg, req, l.CommandName, backend.Herdr)
	if err != nil {
		return l.failHerdr(req, "build agent command", err)
	}
	req.AgentCommand = agentCmd
	logPaneRequest(req, l.Log)
	printHerdrPaneDryRun(req, l.Log, l.Palette)
	return Result{}, true
}

func (l *Launcher) realizeHerdrCoordinator(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
) (state.HerdrIntent, error) {
	issueNum := 0
	if canonicalHerdrParent(req.ParentRef) == WatchParentRef {
		issueNum = req.Number
	}
	result, err := RealizeHerdrCoordinator(ctx, HerdrCoordinatorRequest{
		Parent: req.ParentRef, IssueNum: issueNum, ProjectRoot: l.Info.ProjectRoot,
		SourceRoot: l.Info.ProjectRoot, CWD: l.Info.ProjectRoot,
		HerdrSession: route.Session, SocketPath: route.SocketPath,
	}, l.Herdr, locked, HerdrRealizeHooks{})
	if err != nil && !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		return state.HerdrIntent{}, err
	}
	if err := l.verifyHerdrIdleLauncher(ctx, result.Intent, route); err != nil {
		if !errors.Is(err, errHerdrLauncherIdentityChanged) {
			return state.HerdrIntent{}, err
		}
		journal, journalErr := locked.HerdrIntents(l.Info.ProjectRoot)
		if journalErr != nil {
			return state.HerdrIntent{}, errors.Join(err, journalErr)
		}
		return state.HerdrIntent{}, markHerdrIntentManual(journal, result.Intent, err)
	}
	return result.Intent, nil
}

func (l *Launcher) realizeHerdrChild(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
) (state.HerdrIntent, error) {
	result, err := RealizeHerdrWorktree(ctx, HerdrWorktreeRequest{
		Parent: req.ParentRef, IssueNum: req.Number, TaskID: req.TaskID,
		ProjectRoot: l.Info.ProjectRoot, SourceRoot: l.Info.ProjectRoot,
		Slug: req.Slug, BranchName: req.BranchName, BaseBranch: req.Worktree.BaseBranch,
		NoRefresh: l.Cfg.NoRefresh, AllowMissingOrigin: req.Worktree.AllowMissingOrigin,
		WorktreePath: req.Worktree.WorktreePath,
		HerdrSession: route.Session, SocketPath: route.SocketPath,
	}, l.Herdr, locked, HerdrRealizeHooks{})
	if err != nil && !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		return state.HerdrIntent{}, err
	}
	return result.Intent, nil
}

func (l *Launcher) recordHerdrCoordinator(
	req Request,
	locked *state.LockedStore,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) error {
	runtimeParent := herdrCoordinatorRuntimeParent(intent)
	for _, pane := range locked.Panes {
		if pane.Parent == ManualParentRef && pane.RuntimeParent == runtimeParent {
			return validateHerdrCoordinatorPane(pane, intent, route)
		}
	}
	number := NextSyntheticPaneNumber(locked.Store, ManualParentRef)
	pane := state.Pane{
		Parent: ManualParentRef, RuntimeParent: runtimeParent, IssueNum: number,
		Kind: state.PaneKindShell, Slug: fmt.Sprintf("herdr-coordinator-%d", -number),
		Backend: backend.Herdr, PaneID: intent.Resource.PaneID,
		HerdrWorkspaceID: intent.Resource.WorkspaceID, HerdrTerminalID: intent.Resource.TerminalID,
		HerdrSession: route.Session, HerdrSocketPath: route.SocketPath,
		DisplayName: "Herdr coordinator: " + runtimeParent, WorktreePath: l.Info.ProjectRoot,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return locked.RecordPane(pane)
}

func herdrCoordinatorRuntimeParent(intent state.HerdrIntent) string {
	if intent.RuntimeParent == WatchParentRef {
		return strconv.Itoa(intent.IssueNum)
	}
	return intent.RuntimeParent
}

func validateHerdrCoordinatorPane(
	pane state.Pane,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) error {
	requirements := []bool{
		pane.Backend == backend.Herdr, pane.IssueNum < 0,
		pane.PaneID == intent.Resource.PaneID,
		pane.HerdrWorkspaceID == intent.Resource.WorkspaceID,
		pane.HerdrTerminalID == intent.Resource.TerminalID,
		pane.HerdrSession == route.Session, pane.HerdrSocketPath == route.SocketPath,
	}
	if slices.Contains(requirements, false) {
		return fmt.Errorf("saved Herdr coordinator row contradicts realized intent")
	}
	return nil
}

func (l *Launcher) prepareHerdrLaunch(
	req Request,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
	callerEnvironment []string,
) (state.HerdrIntent, error) {
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return intent, err
	}
	if saved, found := journal.FindIntent(intent.ID); found {
		intent = saved
	}
	if intent.Launch != nil {
		if intent.Launch.TokenIssued {
			return intent, l.failClosedIssuedHerdrLaunch(journal, intent)
		}
		return intent, nil
	}
	return l.newHerdrLaunch(req, journal, route, intent, callerEnvironment)
}

func (l *Launcher) newHerdrLaunch(
	req Request,
	journal *state.LockedHerdrIntents,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
	callerEnvironment []string,
) (state.HerdrIntent, error) {
	nonce, err := randomHerdrToken()
	if err != nil {
		return intent, err
	}
	spec, err := agent.BuildResolvedLaunchSpec(req.Agent, req.Prompt, backend.Herdr, req.LaunchMode)
	if err != nil {
		return intent, err
	}
	environment, err := herdrrun.WorkloadEnvironment(callerEnvironment, route.LauncherPath)
	if err != nil {
		return intent, err
	}
	envPath, envCount, err := l.Herdr.PrepareWorkloadEnvironment(nonce, environment)
	if err != nil {
		return intent, err
	}
	intent.Launch = &state.HerdrLaunch{
		Nonce: nonce, Agent: req.Agent,
		AgentName:  naming.HerdrAgentName(route.GitCommonDir, intent.ID, nonce),
		Executable: spec.Executable, Args: spec.Args,
		EnvFilePath: envPath, EnvNameCount: envCount,
	}
	return persistNewHerdrLaunch(journal, intent)
}

func persistNewHerdrLaunch(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	persisted, err := persistHerdrLaunch(journal, intent)
	if err == nil {
		return persisted, nil
	}
	return intent, errors.Join(err, removeUnpublishedHerdrEnvironment(intent.Launch.EnvFilePath))
}

func removeUnpublishedHerdrEnvironment(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func persistHerdrLaunch(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
) (state.HerdrIntent, error) {
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		return intent, err
	}
	return intent, nil
}

func (l *Launcher) failClosedIssuedHerdrLaunch(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
) error {
	panes, observeErr := l.Herdr.LivePanes(context.Background())
	detail := "no exact live agent was observed"
	if observeErr != nil {
		detail = "live agent observation failed: " + observeErr.Error()
	} else if herdrLaunchNamePresent(intent, panes) {
		detail = "the operation-bound agent name is present"
	}
	return markHerdrIntentManual(journal, intent, fmt.Errorf("%w: %s", errHerdrLaunchResponseLost, detail))
}

func herdrLaunchNamePresent(intent state.HerdrIntent, panes []backend.LivePane) bool {
	for _, pane := range panes {
		if pane.Ref.Pane == intent.Resource.PaneID && pane.AgentID == intent.Launch.AgentName {
			return true
		}
	}
	return false
}

func (l *Launcher) finalizeHerdrLaunch(
	req Request,
	locked *state.LockedStore,
	intent state.HerdrIntent,
	live backend.LivePane,
) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, markHerdrFinalizationFailure(locked, l.Info.ProjectRoot, intent, retErr))
		}
	}()
	if err := displayname.WriteFanoutMetadata(intent.WorktreePath, displayname.FanoutMetadata{
		Agent: req.Agent, DisplayName: paneTitle(req), BranchName: req.BranchName,
		Slug: req.Slug, WorktreePath: intent.WorktreePath,
	}); err != nil {
		return err
	}
	pane := statePaneForBackend(req, live.Ref.Pane, intent.WorktreePath, time.Now().UTC(), codexapp.Status{}, backend.Herdr, &live)
	if err := locked.RecordPane(pane); err != nil {
		return err
	}
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return err
	}
	journal.RemoveIntent(intent.ID)
	return journal.Save()
}

func markHerdrFinalizationFailure(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	cause error,
) error {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	latest, found := journal.FindIntent(intent.ID)
	if !found {
		return fmt.Errorf("finalize Herdr launch: intent %s disappeared", intent.ID)
	}
	return markHerdrIntentManual(journal, latest, fmt.Errorf("finalize Herdr launch: %w", cause))
}

func printHerdrPaneDryRun(req Request, lg *log.Logger, c log.Palette) {
	if req.BriefingPath != "" || req.BriefingBody != "" {
		fmt.Fprintf(lg.Stdout(), "  %sbriefing size%s: %d bytes\n", c.Dim, c.Reset, len(req.BriefingBody))
	}
	fmt.Fprintf(lg.Stdout(), "    %s$ herdr workspace create --cwd %s --label <coordinator_nonce> --no-focus%s\n", c.Dim, shellQuote(req.Worktree.ProjectRoot), c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s$ herdr worktree create --workspace <coordinator_id> --branch %s --path %s --label <worktree_nonce> --no-focus%s\n", c.Dim, shellQuote(req.BranchName), shellQuote(req.Worktree.WorktreePath), c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s# wait for the operation-bound fanout launcher marker, issue one token, and verify the exact agent session%s\n", c.Dim, c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s# agent argv: %s%s\n", c.Dim, req.AgentCommand, c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s# would write coordinator and child Herdr identities to .fanout/state.json%s\n", c.Dim, c.Reset)
	printPaneHookDryRun(req, lg, c)
	lg.Ok("%s: dry-run complete", paneLogLabel(req))
}
