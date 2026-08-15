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
	LaunchRoute() (backend.OwnedLaunchRoute, error)
	PrepareWorkloadEnvironment(string, []string) (string, int, error)
	WaitForLauncher(context.Context, string, string, time.Duration) error
	ProcessInfo(context.Context, string) (backend.PaneProcessInfo, error)
	SendLaunchToken(context.Context, string, string) error
	LivePanes(context.Context) ([]backend.LivePane, error)
	RenameAgent(context.Context, string, string) error
	RemoveWorktree(context.Context, string, string) error
	ReportMetadata(context.Context, backend.MetadataReport) error
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
	live, err := l.startHerdrRequestAgent(
		operation.ctx, req, operation.locked, operation.route, intent, operation.environment,
	)
	if err != nil {
		return l.failHerdr(req, "start agent", l.rollbackFailedHerdrLaunch(operation.locked, intent, err))
	}
	codexStatus, err := awaitHerdrCodexTUI(operation.ctx, req, operation.locked, l.Info.ProjectRoot, intent)
	if err != nil {
		return l.failHerdr(req, "start Codex TUI controller", err)
	}
	if err := finalizeHerdrPane(operation.locked, l.Info.ProjectRoot, intent, func(latest state.HerdrIntent) (state.Pane, error) {
		return herdrAgentStatePane(req, latest, live, codexStatus)
	}); err != nil {
		return l.failHerdr(req, "finalize launch", err)
	}
	l.reportHerdrSidebarMetadata(req, intent)
	l.Log.Ok("%s: pane %s created in %s", paneLogLabel(req), live.Ref.Pane, intent.WorktreePath)
	return Result{PaneID: live.Ref.Pane, Notice: launchNotice(req)}, true
}

type herdrLaunchOperation struct {
	ctx         context.Context
	locked      *state.LockedStore
	route       backend.OwnedLaunchRoute
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
	if err := validateHerdrLaunchRoute(operation.route); err != nil {
		l.Log.Err("%s: %v", paneLogLabel(req), err)
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
	if req.Number < 0 {
		if err := admitHerdrCoordinatorLaunch(locked, l.Info.ProjectRoot, req.Number); err != nil {
			l.Log.Err("%s: %v", paneLogLabel(req), err)
			return nil, false
		}
	}
	return locked, true
}

func admitHerdrCoordinatorLaunch(
	locked *state.LockedStore,
	projectRoot string,
	issueNum int,
) error {
	ownerRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return fmt.Errorf("canonicalize Herdr coordinator launch owner: %w", err)
	}
	intentID, err := state.HerdrCoordinatorIntentID(ManualParentRef, filepath.Clean(ownerRoot), issueNum)
	if err != nil {
		return err
	}
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		return nil
	}
	if intent.Status == state.HerdrIntentManualCleanupRequired {
		return herdrManualCleanupError(intent)
	}
	if intent.Launch == nil || !intent.Launch.TokenIssued {
		return nil
	}
	if remainingHerdrLaunchTime(intent) > 0 {
		return fmt.Errorf("%w: Herdr launch %s already has an issued token", errHerdrLaunchStatePreserved, intent.ID)
	}
	return markHerdrIntentManual(journal, intent, fmt.Errorf("issued Herdr launch expired before finalization"))
}

func herdrCoordinatorIssueNum(req Request) int {
	switch canonicalHerdrParent(req.ParentRef) {
	case ManualParentRef, WatchParentRef:
		return req.Number
	default:
		return 0
	}
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
	printHerdrPaneDryRun(req, l.previewBackendLaunch(req), l.Log, l.Palette)
	return Result{}, true
}

func (l *Launcher) realizeHerdrCoordinator(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
) (state.HerdrIntent, error) {
	result, err := RealizeHerdrCoordinator(ctx, HerdrCoordinatorRequest{
		Parent: req.ParentRef, IssueNum: herdrCoordinatorIssueNum(req), ProjectRoot: l.Info.ProjectRoot,
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
	route backend.OwnedLaunchRoute,
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
	route backend.OwnedLaunchRoute,
) error {
	runtimeParent := herdrCoordinatorRuntimeParent(intent)
	recorded, err := l.findHerdrCoordinatorRow(locked, runtimeParent, intent, route)
	if err != nil || recorded {
		return err
	}
	number := NextSyntheticPaneNumber(locked.Store, ManualParentRef)
	return locked.RecordPane(herdrCoordinatorPane(intent, route, runtimeParent, number))
}

func herdrCoordinatorPane(
	intent state.HerdrIntent,
	route backend.OwnedLaunchRoute,
	runtimeParent string,
	number int,
) state.Pane {
	return state.Pane{
		Parent: ManualParentRef, RuntimeParent: runtimeParent, IssueNum: number,
		Kind: state.PaneKindShell, Slug: fmt.Sprintf("herdr-coordinator-%d", -number),
		Backend: backend.Herdr, PaneID: intent.Resource.PaneID,
		HerdrWorkspaceID: intent.Resource.WorkspaceID, HerdrWorkspaceLabel: intent.Resource.Label,
		HerdrTerminalID: intent.Resource.TerminalID,
		HerdrSession:    route.Session, HerdrSocketPath: route.SocketPath,
		DisplayName: "Herdr coordinator: " + runtimeParent, WorktreePath: intent.Resource.CurrentPath,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func (l *Launcher) findHerdrCoordinatorRow(
	locked *state.LockedStore,
	runtimeParent string,
	intent state.HerdrIntent,
	route backend.OwnedLaunchRoute,
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
	route backend.OwnedLaunchRoute,
) error {
	requirements := []bool{
		pane.Backend == backend.Herdr, pane.IssueNum < 0,
		pane.PaneID == intent.Resource.PaneID,
		pane.HerdrWorkspaceID == intent.Resource.WorkspaceID,
		pane.HerdrWorkspaceLabel == intent.Resource.Label,
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
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.HerdrIntent,
	validate herdrLaunchValidator,
	build herdrLaunchCapsuleBuilder,
) (state.HerdrIntent, error) {
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return intent, err
	}
	if saved, found := journal.FindIntent(intent.ID); found {
		intent = saved
	}
	if intent.Launch == nil {
		return buildAndPersistHerdrLaunch(journal, intent, route.RuntimeDir, validate, build)
	}
	if err := validate(intent.Launch); err != nil {
		return intent, err
	}
	if intent.Launch.TokenIssued {
		return intent, fmt.Errorf("%w: Herdr launch %s already has an issued token", errHerdrLaunchStatePreserved, intent.ID)
	}
	return intent, nil
}

func buildAndPersistHerdrLaunch(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	runtimeDir string,
	validate herdrLaunchValidator,
	build herdrLaunchCapsuleBuilder,
) (state.HerdrIntent, error) {
	if build == nil {
		return intent, validate(nil)
	}
	launch, err := build(intent)
	if err != nil {
		return intent, err
	}
	intent.Launch = launch
	if err := validate(launch); err != nil {
		return intent, errors.Join(err, removeUnpublishedHerdrEnvironment(runtimeDir, launch))
	}
	return persistNewHerdrLaunch(journal, intent, runtimeDir)
}

type resolvedHerdrLaunch struct {
	nonce     string
	agentName string
	emitter   herdrEmitterLaunch
	spec      agent.LaunchSpec
}

func (l *Launcher) resolveHerdrLaunch(
	req Request,
	route backend.OwnedLaunchRoute,
	intent state.HerdrIntent,
) (resolvedHerdrLaunch, error) {
	nonce, err := randomHerdrToken()
	if err != nil {
		return resolvedHerdrLaunch{}, err
	}
	agentName := naming.HerdrAgentName(route.GitCommonDir, intent.ID, nonce)
	emitter, err := newHerdrEmitterLaunch(
		req, route, intent, nonce, agentName, state.Path(l.Info.ProjectRoot),
	)
	if err != nil {
		return resolvedHerdrLaunch{}, err
	}
	spec, err := buildHerdrLaunchSpec(req)
	if len(emitter.backendArgs) != 0 {
		spec, err = agent.BuildResolvedLaunchSpecWithBackendArgs(
			req.Agent, req.Prompt, backend.Herdr, req.LaunchMode, emitter.backendArgs,
		)
	}
	if err != nil {
		return resolvedHerdrLaunch{}, err
	}
	return resolvedHerdrLaunch{nonce: nonce, agentName: agentName, emitter: emitter, spec: spec}, nil
}

func (l *Launcher) prepareHerdrLaunchCapsule(
	req Request,
	route backend.OwnedLaunchRoute,
	intent state.HerdrIntent,
	callerEnvironment []string,
) (*state.HerdrLaunch, error) {
	resolved, err := l.resolveHerdrLaunch(req, route, intent)
	if err != nil {
		return nil, err
	}
	environment, err := herdrrun.WorkloadEnvironment(callerEnvironment, route.LauncherPath)
	if err != nil {
		return nil, err
	}
	environment = append(environment, resolved.emitter.environment...)
	envPath, envCount, err := l.Herdr.PrepareWorkloadEnvironment(resolved.nonce, environment)
	if err != nil {
		return nil, err
	}
	return &state.HerdrLaunch{
		Nonce: resolved.nonce, EmitterNonce: resolved.emitter.nonce, Agent: req.Agent,
		AgentName:  resolved.agentName,
		Executable: resolved.spec.Executable, Args: resolved.spec.Args,
		TeamDBPath:          req.TeamDBPath,
		CodexTeamStatusPath: newHerdrTeamStatusPath(req),
		CodexPlanStatusPath: newHerdrPlanStatusPath(req),
		EnvFilePath:         envPath, EnvNameCount: envCount,
	}, nil
}

func buildHerdrLaunchSpec(req Request) (agent.LaunchSpec, error) {
	if !req.CodexPlanMode() && !req.CodexTeamMode {
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
	if req.CodexPlanMode() {
		return codexapp.PlanLaunchSpec(fanoutPath, codexPath, req.Prompt, req.CodexPlanStatusPath), nil
	}
	return codexapp.TeamLaunchSpec(fanoutPath, codexPath, req.Prompt, codexTeamMember(req), req.ParentRef, req.CodexTeamStatusPath), nil
}

func newHerdrTeamStatusPath(req Request) string {
	if req.CodexTeamMode {
		return req.CodexTeamStatusPath
	}
	return ""
}

func newHerdrPlanStatusPath(req Request) string {
	if req.CodexPlanMode() {
		return req.CodexPlanStatusPath
	}
	return ""
}

func validateHerdrLaunchBinding(req Request, launch *state.HerdrLaunch) error {
	if err := validateHerdrTeamBinding(req, launch); err != nil {
		return err
	}
	boundReq := req
	boundReq.CodexTeamStatusPath = launch.CodexTeamStatusPath
	boundReq.CodexPlanStatusPath = launch.CodexPlanStatusPath
	spec, err := buildHerdrLaunchSpec(boundReq)
	if err != nil {
		return err
	}
	if launch.Agent != req.Agent || launch.Executable != spec.Executable ||
		!slices.Equal(launch.Args, spec.Args) {
		return fmt.Errorf("saved Herdr launch does not match the current agent command")
	}
	return nil
}

func validateHerdrTeamBinding(req Request, launch *state.HerdrLaunch) error {
	requestedTeam := req.TeamDBPath != ""
	savedTeam := launch.TeamDBPath != ""
	switch {
	case requestedTeam != savedTeam:
		return fmt.Errorf("saved Herdr launch does not match the current team mode")
	case requestedTeam && req.TeamDBPath != launch.TeamDBPath:
		return fmt.Errorf("saved Herdr launch does not match the current team DB path")
	case req.CodexTeamMode != (launch.CodexTeamStatusPath != ""):
		return fmt.Errorf("saved Herdr launch does not match the current Codex team mode")
	case req.CodexPlanMode() != (launch.CodexPlanStatusPath != ""):
		return fmt.Errorf("saved Herdr launch does not match the current Codex Plan Mode")
	}
	return nil
}

func waitForHerdrCodexTUI(req Request, intent state.HerdrIntent) (codexapp.Status, error) {
	statusPath := requestedHerdrCodexStatusPath(req)
	if statusPath == "" {
		return codexapp.Status{}, nil
	}
	timeout := min(CodexPlanTUIStartupTimeout, remainingHerdrLaunchTime(intent))
	if timeout <= 0 {
		return codexapp.Status{}, fmt.Errorf("herdr launch expired before Codex TUI controller became ready")
	}
	status, err := codexapp.WaitReady(statusPath, timeout)
	if err != nil {
		return status, err
	}
	// The unique status path has served its launch fence; a removal failure
	// cannot invalidate the ready payload or authorize a later launch.
	_ = os.Remove(statusPath)
	return status, nil
}

func requestedHerdrCodexStatusPath(req Request) string {
	if req.CodexPlanMode() {
		return req.CodexPlanStatusPath
	}
	if req.CodexTeamMode {
		return req.CodexTeamStatusPath
	}
	return ""
}

func herdrCodexStatusPath(req Request, intent state.HerdrIntent) (string, error) {
	if intent.Launch != nil {
		if err := validateHerdrTeamBinding(req, intent.Launch); err != nil {
			return "", err
		}
	}
	if requestedHerdrCodexStatusPath(req) == "" {
		return "", nil
	}
	if intent.Launch == nil {
		return "", fmt.Errorf("saved Herdr Codex launch is missing its status path")
	}
	if req.CodexPlanMode() {
		return intent.Launch.CodexPlanStatusPath, nil
	}
	return intent.Launch.CodexTeamStatusPath, nil
}

func awaitHerdrCodexTUI(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
) (codexapp.Status, error) {
	if requestedHerdrCodexStatusPath(req) == "" {
		return codexapp.Status{}, nil
	}
	journal, latest, err := loadHerdrCodexIntent(locked, projectRoot, intent.ID)
	if err != nil {
		return codexapp.Status{}, err
	}
	req, err = bindHerdrCodexStatusPath(req, latest)
	if err != nil {
		return codexapp.Status{}, errors.Join(err, markHerdrIntentManual(
			journal, latest, fmt.Errorf("codex TUI controller readiness failed: %w", err),
		))
	}
	status, journal, latest, err := waitForHerdrCodexTUIUnlocked(
		ctx, req, locked, projectRoot, latest,
	)
	if err == nil {
		return status, nil
	}
	if journal == nil {
		return status, err
	}
	return status, errors.Join(err, markHerdrIntentManual(
		journal, latest, fmt.Errorf("codex TUI controller readiness failed: %w", err),
	))
}

func bindHerdrCodexStatusPath(req Request, intent state.HerdrIntent) (Request, error) {
	statusPath, err := herdrCodexStatusPath(req, intent)
	if err != nil {
		return req, err
	}
	if req.CodexPlanMode() {
		req.CodexPlanStatusPath = statusPath
	} else {
		req.CodexTeamStatusPath = statusPath
	}
	return req, nil
}

func waitForHerdrCodexTUIUnlocked(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
) (codexapp.Status, *state.LockedHerdrIntents, state.HerdrIntent, error) {
	if err := locked.Unlock(); err != nil {
		return codexapp.Status{}, nil, intent, err
	}
	status, waitErr := waitForHerdrCodexTUI(req, intent)
	if err := reacquireHerdrLaunchLock(locked, projectRoot, intent); err != nil {
		return status, nil, intent, errors.Join(waitErr, err)
	}
	journal, latest, err := loadHerdrCodexIntent(locked, projectRoot, intent.ID)
	if err != nil {
		return status, nil, intent, errors.Join(waitErr, err)
	}
	if waitErr == nil {
		waitErr = ensureHerdrLaunchActive(ctx, latest)
	}
	return status, journal, latest, waitErr
}

func loadHerdrCodexIntent(
	locked *state.LockedStore,
	projectRoot, intentID string,
) (*state.LockedHerdrIntents, state.HerdrIntent, error) {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return nil, state.HerdrIntent{}, err
	}
	latest, found := journal.FindIntent(intentID)
	if !found {
		return nil, state.HerdrIntent{}, fmt.Errorf("codex launch intent %s disappeared", intentID)
	}
	return journal, latest, nil
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
	cause error,
) error {
	responseLost := fmt.Errorf(
		"%w: launch-token outcome is indeterminate",
		errHerdrLaunchResponseLost,
	)
	if cause != nil {
		responseLost = errors.Join(cause, responseLost)
	}
	return markHerdrIntentManual(journal, intent, responseLost)
}

func herdrAgentStatePane(
	req Request,
	intent state.HerdrIntent,
	live backend.LivePane,
	codexStatus codexapp.Status,
) (state.Pane, error) {
	if err := displayname.WriteFanoutMetadata(intent.WorktreePath, displayname.FanoutMetadata{
		Agent: req.Agent, DisplayName: paneTitle(req), BranchName: req.BranchName,
		Slug: req.Slug, WorktreePath: intent.WorktreePath,
		CodexThreadID: codexStatus.ThreadID, CodexSessionID: codexStatus.SessionID,
	}); err != nil {
		return state.Pane{}, err
	}
	pane := statePaneForBackend(
		req, live.Ref.Pane, intent.WorktreePath, time.Now().UTC(), codexStatus, backend.Herdr, &live,
	)
	applyHerdrLaunchOwnership(&pane, intent)
	applyHerdrLaunchTelemetry(&pane, intent)
	pane.HerdrDirectAgentLaunch = !req.CodexPlanMode() && !req.CodexTeamMode
	return pane, nil
}

func latestHerdrLaunchIntent(
	locked *state.LockedStore,
	projectRoot string,
	intentID string,
) (*state.LockedHerdrIntents, state.HerdrIntent, error) {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return nil, state.HerdrIntent{}, err
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		return nil, state.HerdrIntent{}, fmt.Errorf("finalize Herdr launch: intent %s disappeared", intentID)
	}
	return journal, intent, nil
}

func applyHerdrLaunchTelemetry(pane *state.Pane, intent state.HerdrIntent) {
	launch := intent.Launch
	if pane == nil || launch == nil {
		return
	}
	pane.HerdrLaunchExecutable = launch.Executable
	pane.HerdrLaunchArgs = slices.Clone(launch.Args)
	if launch.EmitterNonce == "" {
		return
	}
	pane.EmitterRowKey = intent.ID
	pane.LaunchNonce = launch.Nonce
	pane.EmitterNonce = launch.EmitterNonce
	pane.ReportedState = string(backend.AgentRunning)
	pane.StateRefinement = false
	if launch.PendingReportedState != "" && launch.PendingAgentSession != nil &&
		pane.HerdrAgentSession != nil && *launch.PendingAgentSession == *pane.HerdrAgentSession {
		pane.ReportedState = launch.PendingReportedState
		pane.StateRefinement = true
	}
}

func applyHerdrLaunchOwnership(pane *state.Pane, intent state.HerdrIntent) {
	pane.RuntimeParent = intent.RuntimeParent
	pane.HerdrWorkspaceLabel = intent.WorkspaceLabel
	pane.HerdrBranchCreated = intent.BranchCreated
}

func markHerdrFinalizationFailure(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	cause error,
) error {
	if errors.Is(cause, errHerdrLaunchStatePreserved) {
		return cause
	}
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

func printHerdrPaneDryRun(req Request, backendPreview []string, lg *log.Logger, c log.Palette) {
	if req.BriefingPath != "" || req.BriefingBody != "" {
		fmt.Fprintf(lg.Stdout(), "  %sbriefing size%s: %d bytes\n", c.Dim, c.Reset, len(req.BriefingBody))
	}
	printBackendDryRun(backendPreview, lg, c)
	printPaneHookDryRun(req, lg, c)
	lg.Ok("%s: dry-run complete", paneLogLabel(req))
}
