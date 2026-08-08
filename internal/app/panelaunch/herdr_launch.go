package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/butaosuinu/fanout/internal/infra/worktree"
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
	codexStatus, err := waitForHerdrCodexTeam(req, intent)
	if err != nil {
		return l.failHerdr(req, "start Codex team TUI", l.rollbackFailedHerdrLaunch(operation.locked, intent, err))
	}
	if err := l.finalizeHerdrLaunch(req, operation.locked, intent, live, codexStatus); err != nil {
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
	if req.CodexPlanMode() {
		l.Log.Err("%s: %v", paneLogLabel(req), backend.Unsupported(backend.Herdr, "Codex Plan Mode child launch until issue #554"))
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
	if recordErr := l.recordHerdrCoordinator(operation.locked, coordinator, operation.route); recordErr != nil {
		return coordinator, fmt.Errorf("record coordinator: %w", recordErr)
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
	observationIntent := herdrCoordinatorObservationIntent(ctx, result.Intent)
	verifyErr := retryHerdrObservation(ctx, observationIntent, func(observeCtx context.Context) error {
		return l.verifyHerdrIdleLauncher(observeCtx, result.Intent, route)
	})
	if err := verifyErr; err != nil {
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

func herdrCoordinatorObservationIntent(
	ctx context.Context,
	intent state.HerdrIntent,
) state.HerdrIntent {
	// A coordinator outlives its creating launch; only this observation uses the current budget.
	deadline := time.Now().Add(maxHerdrRealizeTimeout)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	intent.ExpiresUnixMS = deadline.UnixMilli()
	return intent
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
	locked *state.LockedStore,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) error {
	runtimeParent := herdrCoordinatorRuntimeParent(intent)
	recorded, err := l.findHerdrCoordinatorRow(locked, runtimeParent, intent, route)
	if err != nil || recorded {
		return err
	}
	number := NextSyntheticPaneNumber(locked.Store, ManualParentRef)
	pane := state.Pane{
		Parent: ManualParentRef, RuntimeParent: runtimeParent, IssueNum: number,
		Kind: state.PaneKindShell, Slug: fmt.Sprintf("herdr-coordinator-%d", -number),
		Backend: backend.Herdr, PaneID: intent.Resource.PaneID,
		HerdrWorkspaceID: intent.Resource.WorkspaceID, HerdrTerminalID: intent.Resource.TerminalID,
		HerdrSession: route.Session, HerdrSocketPath: route.SocketPath,
		DisplayName: "Herdr coordinator: " + runtimeParent, WorktreePath: intent.Resource.CurrentPath,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return locked.RecordPane(pane)
}

func (l *Launcher) findHerdrCoordinatorRow(
	locked *state.LockedStore,
	runtimeParent string,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) (bool, error) {
	roots, err := herdrCoordinatorRowRoots(l.Info.ProjectRoot, runtimeParent, intent.OwnerProjectRoot)
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		store, loadErr := loadHerdrCoordinatorStore(l.Info.ProjectRoot, root, locked)
		if loadErr != nil {
			return false, loadErr
		}
		pane, found := findHerdrCoordinatorPane(store, runtimeParent)
		if found {
			return true, validateHerdrCoordinatorPane(pane, intent, route)
		}
	}
	return false, nil
}

func herdrCoordinatorRowRoots(projectRoot, runtimeParent, ownerProjectRoot string) ([]string, error) {
	owner, err := state.HerdrOwnerProjectRoot(runtimeParent, ownerProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve Herdr coordinator owner: %w", err)
	}
	if owner != "" {
		matches, matchErr := sameHerdrCoordinatorOwner(projectRoot, owner)
		if matchErr != nil {
			return nil, matchErr
		}
		if !matches {
			return nil, fmt.Errorf("herdr coordinator owner %s does not match launch root %s", owner, projectRoot)
		}
		return []string{projectRoot}, nil
	}
	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("list linked worktrees for Herdr coordinator: %w", err)
	}
	return roots, nil
}

func sameHerdrCoordinatorOwner(projectRoot, owner string) (bool, error) {
	projectInfo, err := os.Stat(projectRoot)
	if err != nil {
		return false, fmt.Errorf("stat Herdr coordinator launch root: %w", err)
	}
	ownerInfo, err := os.Stat(owner)
	if err != nil {
		return false, fmt.Errorf("stat Herdr coordinator owner: %w", err)
	}
	return os.SameFile(projectInfo, ownerInfo), nil
}

func loadHerdrCoordinatorStore(projectRoot, root string, locked *state.LockedStore) (state.Store, error) {
	if filepath.Clean(root) == filepath.Clean(projectRoot) {
		return locked.Store, nil
	}
	store, err := state.LoadProject(root)
	if err != nil {
		return state.Store{}, fmt.Errorf("load Herdr coordinator state from %s: %w", root, err)
	}
	return store, nil
}

func findHerdrCoordinatorPane(store state.Store, runtimeParent string) (state.Pane, bool) {
	for _, pane := range store.Panes {
		if pane.Parent == ManualParentRef && pane.RuntimeParent == runtimeParent {
			return pane, true
		}
	}
	return state.Pane{}, false
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
		filepath.Clean(pane.WorktreePath) == filepath.Clean(intent.Resource.CurrentPath),
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
	spec, err := buildHerdrLaunchSpec(req)
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
	return persistNewHerdrLaunch(journal, intent, route.RuntimeDir)
}

func buildHerdrLaunchSpec(req Request) (agent.LaunchSpec, error) {
	if !req.CodexTeamMode {
		return agent.BuildResolvedLaunchSpec(req.Agent, req.Prompt, backend.Herdr, req.LaunchMode)
	}
	codexPath, err := agent.ResolveExecutable("codex")
	if err != nil {
		return agent.LaunchSpec{}, err
	}
	fanoutPath, err := os.Executable()
	if err != nil {
		return agent.LaunchSpec{}, fmt.Errorf("resolve fanout executable: %w", err)
	}
	return codexapp.TeamLaunchSpec(
		fanoutPath, codexPath, req.Prompt, codexTeamMember(req), req.ParentRef, req.CodexTeamStatusPath,
	), nil
}

func waitForHerdrCodexTeam(req Request, intent state.HerdrIntent) (codexapp.Status, error) {
	if !req.CodexTeamMode {
		return codexapp.Status{}, nil
	}
	timeout := min(CodexPlanTUIStartupTimeout, remainingHerdrLaunchTime(intent))
	if timeout <= 0 {
		return codexapp.Status{}, fmt.Errorf("Herdr launch expired before Codex team TUI became ready")
	}
	status, err := codexapp.WaitReady(req.CodexTeamStatusPath, timeout)
	if err != nil {
		return status, err
	}
	// The unique status path has served its launch fence; a removal failure
	// cannot invalidate the ready payload or authorize a later launch.
	_ = os.Remove(req.CodexTeamStatusPath)
	return status, nil
}

func persistNewHerdrLaunch(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	runtimeDir string,
) (state.HerdrIntent, error) {
	persisted, err := persistHerdrLaunch(journal, intent)
	if err == nil {
		return persisted, nil
	}
	return intent, errors.Join(err, removeUnpublishedHerdrEnvironment(runtimeDir, intent.Launch))
}

func removeUnpublishedHerdrEnvironment(runtimeDir string, launch *state.HerdrLaunch) error {
	return herdrrun.DiscardWorkloadEnvironment(runtimeDir, launch)
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
	return markHerdrIntentManual(journal, intent, fmt.Errorf(
		"%w: launch-token outcome is indeterminate",
		errHerdrLaunchResponseLost,
	))
}

func (l *Launcher) finalizeHerdrLaunch(
	req Request,
	locked *state.LockedStore,
	intent state.HerdrIntent,
	live backend.LivePane,
	codexStatus codexapp.Status,
) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, markHerdrFinalizationFailure(locked, l.Info.ProjectRoot, intent, retErr))
		}
	}()
	if err := displayname.WriteFanoutMetadata(intent.WorktreePath, displayname.FanoutMetadata{
		Agent: req.Agent, DisplayName: paneTitle(req), BranchName: req.BranchName,
		Slug: req.Slug, WorktreePath: intent.WorktreePath,
		CodexThreadID: codexStatus.ThreadID, CodexSessionID: codexStatus.SessionID,
	}); err != nil {
		return err
	}
	pane := statePaneForBackend(req, live.Ref.Pane, intent.WorktreePath, time.Now().UTC(), codexStatus, backend.Herdr, &live)
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
