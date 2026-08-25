package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/displayname"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type ManagedLaunchRuntime interface {
	ManagedWorktreeRuntime
	VerifyOwned(context.Context) error
	LaunchRoute() (backend.OwnedLaunchRoute, error)
	WorkloadEnvironment([]string, string) ([]string, error)
	PrepareWorkloadEnvironment(string, []string) (string, int, error)
	WaitForLauncher(context.Context, string, string, time.Duration) error
	ProcessInfo(context.Context, string) (backend.PaneProcessInfo, error)
	SendLaunchToken(context.Context, string, string) error
	LivePanes(context.Context) ([]backend.LivePane, error)
	RenameAgent(context.Context, string, string) error
	RemoveWorktree(context.Context, string, string) error
	ReportMetadata(context.Context, backend.MetadataReport) error
	// MetadataReportBudget bounds one ReportMetadata call. The runtime derives
	// it from its own command timeout; the app only spends it.
	MetadataReportBudget() time.Duration
}

var errManagedLaunchResponseLost = errors.New("herdr agent launch response was lost; refusing automatic adoption")

func (l *Launcher) launchManaged(req Request) (Result, bool) {
	l.preflightClaudeLaunchMode(&req)
	if l.Cfg.DryRun {
		return l.dryRunManaged(req)
	}
	operation, ok := l.prepareManagedOperation(req)
	if !ok {
		return Result{}, false
	}
	defer operation.cancel()
	intent, err := l.realizeManagedLaunch(req, operation)
	if err != nil {
		return l.failManaged(req, "realize launch", l.rollbackFailedManagedLaunch(operation.locked, intent, err))
	}
	live, err := l.startManagedRequestAgent(
		operation.ctx, req, operation.locked, operation.route, intent, operation.environment,
	)
	if err != nil {
		return l.failManaged(req, "start agent", l.rollbackFailedManagedLaunch(operation.locked, intent, err))
	}
	codexStatus, err := awaitManagedCodexTUI(operation.ctx, req, operation.locked, l.Info.ProjectRoot, intent)
	if err != nil {
		return l.failManaged(req, "start Codex TUI controller", err)
	}
	if err := finalizeManagedPane(operation.locked, l.Info.ProjectRoot, intent, func(latest state.LaunchIntent) (state.Pane, error) {
		return managedAgentStatePane(req, latest, live, codexStatus)
	}); err != nil {
		return l.failManaged(req, "finalize launch", err)
	}
	l.reportManagedSidebarMetadata(req, intent)
	l.Log.Ok("%s: pane %s created in %s", paneLogLabel(req), live.Ref.Pane, intent.WorktreePath)
	return Result{PaneID: live.Ref.Pane, Notice: launchNotice(req)}, true
}

type managedLaunchOperation struct {
	ctx         context.Context
	locked      *state.LockedStore
	route       backend.OwnedLaunchRoute
	environment []string
	cancel      context.CancelFunc
}

func (l *Launcher) prepareManagedOperation(req Request) (managedLaunchOperation, bool) {
	locked, ok := l.admitManagedLaunchRequest(req)
	if !ok {
		return managedLaunchOperation{}, false
	}
	operation := managedLaunchOperation{
		ctx: context.Background(), locked: locked,
		environment: append([]string(nil), os.Environ()...),
	}
	if err := l.Managed.VerifyOwned(operation.ctx); err != nil {
		l.Log.Err("%s: verify owned Herdr session: %v", paneLogLabel(req), err)
		return managedLaunchOperation{}, false
	}
	var err error
	operation.route, err = l.Managed.LaunchRoute()
	if err != nil {
		l.Log.Err("%s: resolve owned Herdr route: %v", paneLogLabel(req), err)
		return managedLaunchOperation{}, false
	}
	if err := validateManagedLaunchRoute(operation.route); err != nil {
		l.Log.Err("%s: %v", paneLogLabel(req), err)
		return managedLaunchOperation{}, false
	}
	if req.BriefingPath != "" && !l.writeBriefing(req) {
		return managedLaunchOperation{}, false
	}
	logPaneRequest(req, l.Log)
	operation.ctx, operation.cancel = context.WithTimeout(context.Background(), maxManagedRealizeTimeout)
	return operation, true
}

func (l *Launcher) admitManagedLaunchRequest(req Request) (*state.LockedStore, bool) {
	locked, ok := l.Recorder.(*state.LockedStore)
	if !ok || l.Managed == nil {
		l.Log.Err("%s: Herdr launch requires an owned session and combined launch lock", paneLogLabel(req))
		return nil, false
	}
	if req.Number < 0 {
		if err := admitManagedCoordinatorLaunch(locked, l.Info.ProjectRoot, req.Number); err != nil {
			l.Log.Err("%s: %v", paneLogLabel(req), err)
			return nil, false
		}
	}
	return locked, true
}

func admitManagedCoordinatorLaunch(
	locked *state.LockedStore,
	projectRoot string,
	issueNum int,
) error {
	ownerRoot, err := canonicalManualCoordinatorOwner(projectRoot)
	if err != nil {
		return err
	}
	intentID, err := state.CoordinatorIntentID(ManualParentRef, ownerRoot, issueNum)
	if err != nil {
		return err
	}
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return err
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		return nil
	}
	if intent.Status == state.IntentManualCleanupRequired {
		return manualCleanupError(intent)
	}
	if intent.Launch == nil || !intent.Launch.TokenIssued {
		return nil
	}
	if remainingManagedLaunchTime(intent) > 0 {
		return fmt.Errorf("%w: Herdr launch %s already has an issued token", errManagedLaunchStatePreserved, intent.ID)
	}
	return markManagedIntentManual(journal, intent, fmt.Errorf("issued Herdr launch expired before finalization"))
}

func managedCoordinatorIssueNum(req Request) int {
	switch canonicalManagedParent(req.ParentRef) {
	case ManualParentRef, WatchParentRef:
		return req.Number
	default:
		return 0
	}
}

func (l *Launcher) realizeManagedLaunch(
	req Request,
	operation managedLaunchOperation,
) (state.LaunchIntent, error) {
	coordinator, livePanes, err := l.realizeManagedCoordinator(operation.ctx, req, operation.locked, operation.route)
	if err != nil {
		return state.LaunchIntent{}, fmt.Errorf("realize coordinator: %w", err)
	}
	if recordErr := l.recordManagedCoordinator(operation.locked, coordinator, operation.route, livePanes); recordErr != nil {
		return coordinator, fmt.Errorf("record coordinator: %w", recordErr)
	}
	intent, err := l.realizeManagedChild(operation.ctx, req, operation.locked, operation.route)
	if err != nil {
		return state.LaunchIntent{}, fmt.Errorf("realize worktree: %w", err)
	}
	result := hooks.RunBlocking(hooks.WorktreeCreated, paneHookContext(req, l.Info.ProjectRoot, intent.WorktreePath, ""), req.Hooks, l.Log)
	if !result.OK() {
		printPaneHookOutput(result, l.Log)
		return intent, fmt.Errorf("run worktree-created hook: %w", result.Err)
	}
	hooks.RunBackground(hooks.BeforePaneCreate, paneHookContext(req, l.Info.ProjectRoot, intent.WorktreePath, intent.Resource.PaneID), req.Hooks, l.Log)
	return intent, nil
}

func (l *Launcher) failManaged(req Request, action string, err error) (Result, bool) {
	l.Log.Err("%s: %s: %v", paneLogLabel(req), action, err)
	return Result{}, false
}

func (l *Launcher) dryRunManaged(req Request) (Result, bool) {
	agentCmd, err := buildAgentCommandForBackend(l.Cfg, req, l.CommandName, backend.Herdr)
	if err != nil {
		return l.failManaged(req, "build agent command", err)
	}
	req.AgentCommand = agentCmd
	logPaneRequest(req, l.Log)
	printManagedPaneDryRun(req, l.previewBackendLaunch(req), l.Log, l.Palette)
	return Result{}, true
}

func (l *Launcher) realizeManagedCoordinator(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
) (state.LaunchIntent, []backend.LivePane, error) {
	result, err := RealizeManagedCoordinator(ctx, ManagedCoordinatorRequest{
		Parent: req.ParentRef, IssueNum: managedCoordinatorIssueNum(req), ProjectRoot: l.Info.ProjectRoot,
		SourceRoot: l.Info.ProjectRoot, CWD: l.Info.ProjectRoot,
		ManagedSession: route.Session, SocketPath: route.SocketPath,
	}, l.Managed, locked, ManagedRealizeHooks{})
	if err != nil && !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		return state.LaunchIntent{}, nil, err
	}
	livePanes, verifyErr := l.observeIdleManagedLauncher(ctx, result.Intent, route)
	if verifyErr != nil {
		if !errors.Is(verifyErr, errManagedLauncherIdentityChanged) {
			return state.LaunchIntent{}, nil, verifyErr
		}
		return state.LaunchIntent{}, nil, l.markManagedCoordinatorLauncherManual(locked, result.Intent, verifyErr)
	}
	return result.Intent, livePanes, nil
}

// observeIdleManagedLauncher retries the idle-launcher observation and returns
// the live panes the successful verification saw, so the caller can reconcile
// saved rows against the same snapshot.
func (l *Launcher) observeIdleManagedLauncher(
	ctx context.Context,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
) ([]backend.LivePane, error) {
	var livePanes []backend.LivePane
	err := retryManagedObservation(ctx, managedCoordinatorObservationIntent(ctx, intent), func(observeCtx context.Context) error {
		panes, observeErr := l.verifyManagedIdleLauncher(observeCtx, intent, route)
		if observeErr == nil {
			livePanes = panes
		}
		return observeErr
	})
	return livePanes, err
}

// markManagedCoordinatorLauncherManual pins a realized coordinator to manual
// cleanup after its launcher identity changed under observation.
func (l *Launcher) markManagedCoordinatorLauncherManual(
	locked *state.LockedStore,
	intent state.LaunchIntent,
	cause error,
) error {
	journal, journalErr := locked.LaunchJournal(l.Info.ProjectRoot)
	if journalErr != nil {
		return errors.Join(cause, journalErr)
	}
	return markManagedIntentManual(journal, intent, cause)
}

func managedCoordinatorObservationIntent(
	ctx context.Context,
	intent state.LaunchIntent,
) state.LaunchIntent {
	// A coordinator outlives its creating launch; only this observation uses the current budget.
	deadline := time.Now().Add(maxManagedRealizeTimeout)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	intent.ExpiresUnixMS = deadline.UnixMilli()
	return intent
}

func (l *Launcher) realizeManagedChild(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
) (state.LaunchIntent, error) {
	result, err := RealizeManagedWorktree(ctx, ManagedWorktreeRequest{
		Parent: req.ParentRef, IssueNum: req.Number, TaskID: req.TaskID,
		ProjectRoot: l.Info.ProjectRoot, SourceRoot: l.Info.ProjectRoot,
		Slug: req.Slug, BranchName: req.BranchName, BaseBranch: req.Worktree.BaseBranch,
		NoRefresh: l.Cfg.NoRefresh, AllowMissingOrigin: req.Worktree.AllowMissingOrigin,
		WorktreePath:   req.Worktree.WorktreePath,
		ManagedSession: route.Session, SocketPath: route.SocketPath,
	}, l.Managed, locked, ManagedRealizeHooks{})
	if err != nil && !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		return state.LaunchIntent{}, err
	}
	return result.Intent, nil
}

func (l *Launcher) recordManagedCoordinator(
	locked *state.LockedStore,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
	livePanes []backend.LivePane,
) error {
	if err := pruneDeadManagedCoordinatorRows(locked, route, livePanes); err != nil {
		return err
	}
	runtimeParent := managedCoordinatorRuntimeParent(intent)
	recorded, err := l.findManagedCoordinatorRow(locked, runtimeParent, intent, route)
	if err != nil || recorded {
		return err
	}
	return recordManagedCoordinatorRow(locked, intent, route, runtimeParent)
}

func recordManagedCoordinatorRow(
	locked *state.LockedStore,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
	runtimeParent string,
) error {
	number := NextSyntheticPaneNumber(locked.Store, ManualParentRef)
	if number == intent.IssueNum {
		// The launch's own agent row lands on (ManualParentRef, IssueNum) at
		// finalization and would silently replace the coordinator row — the
		// state upsert keys on (parent, issue number). Keep the scaffolding
		// row off that key.
		number--
	}
	return locked.RecordPane(managedCoordinatorPane(intent, route, runtimeParent, number))
}

// pruneDeadManagedCoordinatorRows removes this owned session's coordinator
// scaffolding rows whose workspace no longer exists. Closing a coordinator
// workspace from the Herdr side is an external cleanup; the launch-time live
// pane observation proves the absence. Only rows in the exact coordinator role
// shape qualify — agent, shell, and attached-agent rows are never touched.
func pruneDeadManagedCoordinatorRows(
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	livePanes []backend.LivePane,
) error {
	// An empty observation proves nothing about absence.
	if len(livePanes) == 0 {
		return nil
	}
	for _, pane := range slices.Clone(locked.Panes) {
		if !deadManagedCoordinatorRow(pane, route, livePanes) {
			continue
		}
		if err := locked.RemovePane(pane.Parent, pane.IssueNum); err != nil {
			return err
		}
	}
	return nil
}

func deadManagedCoordinatorRow(
	pane state.Pane,
	route backend.OwnedLaunchRoute,
	livePanes []backend.LivePane,
) bool {
	return managedCoordinatorRowRole(pane) &&
		strings.HasPrefix(pane.WorkspaceLabel, managedWorkspaceLabelPrefix(managedCoordinatorLabelKind)) &&
		pane.SessionID == route.Session && pane.SocketPath == route.SocketPath &&
		!managedWorkspaceRowLive(pane, livePanes)
}

// managedWorkspaceRowLive matches on the label — a per-intent nonce that
// Herdr-side moves and restarts change workspace ids without touching — or on
// the recorded workspace id, so a relabeled-but-live workspace keeps its row,
// mirroring the recreate guard. A row wrongly pruned by a snapshot that missed
// its panes is re-recorded by the owning intent's next launch, and the
// workspace itself is never touched.
func managedWorkspaceRowLive(pane state.Pane, livePanes []backend.LivePane) bool {
	for _, live := range livePanes {
		if live.WorkspaceLabel == pane.WorkspaceLabel || live.Ref.Workspace == pane.WorkspaceID {
			return true
		}
	}
	return false
}

func managedCoordinatorPane(
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
	runtimeParent string,
	number int,
) state.Pane {
	return state.Pane{
		Parent: ManualParentRef, RuntimeParent: runtimeParent, IssueNum: number,
		Kind: state.PaneKindShell, Slug: fmt.Sprintf("herdr-coordinator-%d", -number),
		Backend: backend.Herdr, PaneID: intent.Resource.PaneID,
		WorkspaceID: intent.Resource.WorkspaceID, WorkspaceLabel: intent.Resource.Label,
		TerminalID: intent.Resource.TerminalID,
		SessionID:  route.Session, SocketPath: route.SocketPath,
		DisplayName: "Herdr coordinator: " + runtimeParent, WorktreePath: intent.Resource.CurrentPath,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func (l *Launcher) findManagedCoordinatorRow(
	locked *state.LockedStore,
	runtimeParent string,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
) (bool, error) {
	roots, err := managedCoordinatorRowRoots(l.Info.ProjectRoot, runtimeParent, intent.OwnerProjectRoot)
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		store, loadErr := loadManagedCoordinatorStore(l.Info.ProjectRoot, root, locked)
		if loadErr != nil {
			return false, loadErr
		}
		pane, found := findManagedCoordinatorPane(store, runtimeParent, intent)
		if found {
			return true, l.reconcileManagedCoordinatorRow(locked, root, runtimeParent, pane, intent, route)
		}
	}
	return false, nil
}

// reconcileManagedCoordinatorRow settles one root's saved row for this intent.
// The label nonce proves the row belongs to this intent, so a disagreeing copy
// is staleness left by a Herdr-side move or restart, not a foreign
// coordinator: an intact copy stands, the launch root's stale copy is healed
// in place, and a sibling root's stale copy heals when a launch runs from that
// root — its store is not under this launch's lock.
func (l *Launcher) reconcileManagedCoordinatorRow(
	locked *state.LockedStore,
	root, runtimeParent string,
	pane state.Pane,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
) error {
	if validateManagedCoordinatorPane(pane, intent, route) == nil {
		return nil
	}
	if filepath.Clean(root) != filepath.Clean(l.Info.ProjectRoot) {
		return nil
	}
	return locked.RecordPane(managedCoordinatorPane(intent, route, runtimeParent, pane.IssueNum))
}

// ReconcileManagedCoordinatorReplanRow records the recreated coordinator after
// lifecycle replanning reused RealizeManagedCoordinator. The previous label
// nonce locates an existing owned row; a pruned row is safely recreated under
// a fresh synthetic state key.
func ReconcileManagedCoordinatorReplanRow(
	locked *state.LockedStore,
	previous state.RuntimeResource,
	current state.LaunchIntent,
) error {
	if locked == nil || !validManagedCoordinatorReplan(previous, current) {
		return fmt.Errorf("replanned Herdr coordinator intent changed generation")
	}
	runtimeParent := managedCoordinatorRuntimeParent(current)
	previousIntent := state.LaunchIntent{Resource: previous}
	pane, found := findManagedCoordinatorPane(locked.Store, runtimeParent, previousIntent)
	if !found {
		pane, found = findManagedCoordinatorPane(locked.Store, runtimeParent, current)
	}
	route := backend.OwnedLaunchRoute{Session: current.Session, SocketPath: current.SocketPath}
	if !found {
		return recordManagedCoordinatorRow(locked, current, route, runtimeParent)
	}
	return reconcileReplannedCoordinatorRow(locked, pane, previous, current, route, runtimeParent)
}

func reconcileReplannedCoordinatorRow(
	locked *state.LockedStore,
	pane state.Pane,
	previous state.RuntimeResource,
	current state.LaunchIntent,
	route backend.OwnedLaunchRoute,
	runtimeParent string,
) error {
	if err := validateReplannedCoordinatorRow(pane, previous, current, route); err != nil {
		return err
	}
	if validateManagedCoordinatorPane(pane, current, route) == nil {
		return nil
	}
	return locked.RecordPane(managedCoordinatorPane(current, route, runtimeParent, pane.IssueNum))
}

func validManagedCoordinatorReplan(previous state.RuntimeResource, current state.LaunchIntent) bool {
	return !slices.Contains([]bool{
		current.Kind == state.IntentCoordinator, current.Status == state.IntentRealized,
		current.Parent == ManualParentRef, current.RuntimeParent == ManualParentRef,
		current.IssueNum < 0, strings.TrimSpace(current.ID) != "",
		strings.TrimSpace(current.OwnerProjectRoot) != "",
		strings.TrimSpace(current.Session) != "", strings.TrimSpace(current.SocketPath) != "",
		managedCoordinatorResourceComplete(previous), managedCoordinatorResourceComplete(current.Resource),
		filepath.Clean(previous.CurrentPath) == filepath.Clean(current.WorktreePath),
		filepath.Clean(current.Resource.CurrentPath) == filepath.Clean(current.WorktreePath),
	}, false)
}

func managedCoordinatorResourceComplete(resource state.RuntimeResource) bool {
	return strings.TrimSpace(resource.WorkspaceID) != "" && strings.TrimSpace(resource.Label) != "" &&
		strings.TrimSpace(resource.PaneID) != "" && strings.TrimSpace(resource.TerminalID) != "" &&
		strings.TrimSpace(resource.CurrentPath) != ""
}

func validateReplannedCoordinatorRow(
	pane state.Pane,
	previous state.RuntimeResource,
	current state.LaunchIntent,
	route backend.OwnedLaunchRoute,
) error {
	if pane.WorkspaceLabel == current.Resource.Label {
		return validateManagedCoordinatorPane(pane, current, route)
	}
	return validateManagedCoordinatorPane(pane, state.LaunchIntent{Resource: previous}, route)
}

func managedCoordinatorRowRoots(projectRoot, runtimeParent, ownerProjectRoot string) ([]string, error) {
	owner, err := state.IntentOwnerProjectRoot(runtimeParent, ownerProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve Herdr coordinator owner: %w", err)
	}
	if owner != "" {
		matches, matchErr := sameManagedCoordinatorOwner(projectRoot, owner)
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

func sameManagedCoordinatorOwner(projectRoot, owner string) (bool, error) {
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

func loadManagedCoordinatorStore(projectRoot, root string, locked *state.LockedStore) (state.Store, error) {
	if filepath.Clean(root) == filepath.Clean(projectRoot) {
		return locked.Store, nil
	}
	store, err := state.LoadProject(root)
	if err != nil {
		return state.Store{}, fmt.Errorf("load Herdr coordinator state from %s: %w", root, err)
	}
	return store, nil
}

// findManagedCoordinatorPane returns the saved row carrying this coordinator
// intent's workspace label. The label is the per-intent ownership nonce, so
// rows for other coordinators, manual agent panes, and attached agents under
// the same runtime parent never match — only the row this intent itself
// recorded can come back, and a disagreeing copy of it is real corruption.
func findManagedCoordinatorPane(
	store state.Store,
	runtimeParent string,
	intent state.LaunchIntent,
) (state.Pane, bool) {
	if intent.Resource.Label == "" {
		return state.Pane{}, false
	}
	for _, pane := range store.Panes {
		if managedCoordinatorRowRole(pane) && pane.RuntimeParent == runtimeParent &&
			pane.WorkspaceLabel == intent.Resource.Label {
			return pane, true
		}
	}
	return state.Pane{}, false
}

// managedCoordinatorRowRole reports whether a state row has the exact role
// shape managedCoordinatorPane emits. managedShutdownCoordinatorRow layers the
// retire-only requirements on top of it.
func managedCoordinatorRowRole(pane state.Pane) bool {
	requirements := []bool{
		pane.Parent == ManualParentRef, pane.IssueNum < 0,
		pane.TaskID == "", pane.Kind == state.PaneKindShell,
		pane.Agent == "", pane.BranchName == "",
		pane.RepoKey == "", pane.RepoRoot == "",
		pane.AgentID == "", pane.AgentSession == nil,
	}
	return !slices.Contains(requirements, false)
}

func managedCoordinatorRuntimeParent(intent state.LaunchIntent) string {
	if intent.RuntimeParent == WatchParentRef {
		return strconv.Itoa(intent.IssueNum)
	}
	return intent.RuntimeParent
}

func validateManagedCoordinatorPane(
	pane state.Pane,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
) error {
	requirements := []bool{
		pane.Backend == backend.Herdr, pane.IssueNum < 0,
		pane.PaneID == intent.Resource.PaneID,
		pane.WorkspaceID == intent.Resource.WorkspaceID,
		pane.WorkspaceLabel == intent.Resource.Label,
		pane.TerminalID == intent.Resource.TerminalID,
		pane.SessionID == route.Session, pane.SocketPath == route.SocketPath,
		filepath.Clean(pane.WorktreePath) == filepath.Clean(intent.Resource.CurrentPath),
	}
	if slices.Contains(requirements, false) {
		return fmt.Errorf("saved Herdr coordinator row contradicts realized intent")
	}
	return nil
}

func (l *Launcher) prepareManagedLaunch(
	locked *state.LockedStore,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	validate managedLaunchValidator,
	build managedLaunchCapsuleBuilder,
) (state.LaunchIntent, error) {
	journal, err := locked.LaunchJournal(l.Info.ProjectRoot)
	if err != nil {
		return intent, err
	}
	if saved, found := journal.FindIntent(intent.ID); found {
		intent = saved
	}
	if intent.Launch == nil {
		return buildAndPersistManagedLaunch(l.Managed, journal, intent, route.RuntimeDir, validate, build)
	}
	if err := validate(intent.Launch); err != nil {
		return intent, err
	}
	if intent.Launch.TokenIssued {
		return intent, fmt.Errorf("%w: Herdr launch %s already has an issued token", errManagedLaunchStatePreserved, intent.ID)
	}
	return intent, nil
}

func buildAndPersistManagedLaunch(
	runtime ManagedLaunchRuntime,
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	runtimeDir string,
	validate managedLaunchValidator,
	build managedLaunchCapsuleBuilder,
) (state.LaunchIntent, error) {
	if build == nil {
		return intent, validate(nil)
	}
	launch, err := build(intent)
	if err != nil {
		return intent, err
	}
	intent.Launch = launch
	if err := validate(launch); err != nil {
		return intent, errors.Join(err, removeUnpublishedManagedEnvironment(runtime, runtimeDir, launch))
	}
	return persistNewManagedLaunch(runtime, journal, intent, runtimeDir)
}

type resolvedManagedLaunch struct {
	nonce     string
	agentName string
	emitter   managedEmitterLaunch
	spec      agent.LaunchSpec
}

func (l *Launcher) resolveManagedLaunch(
	req Request,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
) (resolvedManagedLaunch, error) {
	nonce, err := randomManagedToken()
	if err != nil {
		return resolvedManagedLaunch{}, err
	}
	agentName := naming.ManagedAgentName(route.GitCommonDir, intent.ID, nonce)
	emitter, err := newManagedEmitterLaunch(
		req, route, intent, nonce, agentName, state.Path(l.Info.ProjectRoot),
	)
	if err != nil {
		return resolvedManagedLaunch{}, err
	}
	spec, err := buildManagedLaunchSpecForRoute(req, route)
	if err != nil {
		return resolvedManagedLaunch{}, err
	}
	return resolvedManagedLaunch{nonce: nonce, agentName: agentName, emitter: emitter, spec: spec}, nil
}

func (l *Launcher) prepareManagedLaunchCapsule(
	req Request,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	callerEnvironment []string,
) (*state.LaunchCapsule, error) {
	resolved, err := l.resolveManagedLaunch(req, route, intent)
	if err != nil {
		return nil, err
	}
	environment, err := l.Managed.WorkloadEnvironment(callerEnvironment, route.LauncherPath)
	if err != nil {
		return nil, err
	}
	environment = append(environment, resolved.emitter.environment...)
	envPath, envCount, err := l.Managed.PrepareWorkloadEnvironment(resolved.nonce, environment)
	if err != nil {
		return nil, err
	}
	return &state.LaunchCapsule{
		Nonce: resolved.nonce, EmitterNonce: resolved.emitter.nonce, Agent: req.Agent,
		AgentName:  resolved.agentName,
		Executable: resolved.spec.Executable, Args: resolved.spec.Args,
		TeamDBPath:          req.TeamDBPath,
		CodexTeamStatusPath: newManagedTeamStatusPath(req),
		CodexPlanStatusPath: newManagedPlanStatusPath(req),
		EnvFilePath:         envPath, EnvNameCount: envCount,
	}, nil
}

// buildManagedLaunchSpecForRoute builds the agent command a managed launch
// records, including the arguments the telemetry emitter contributes. Recording
// a launch and verifying it later both go through here so the two can never
// disagree.
func buildManagedLaunchSpecForRoute(req Request, route backend.OwnedLaunchRoute) (agent.LaunchSpec, error) {
	backendArgs, err := managedEmitterBackendArgs(req, route)
	if err != nil {
		return agent.LaunchSpec{}, err
	}
	if len(backendArgs) == 0 {
		return buildManagedLaunchSpec(req)
	}
	return agent.BuildResolvedLaunchSpecWithBackendArgs(
		req.Agent, req.Prompt, backend.Herdr, req.LaunchMode, backendArgs,
	)
}

func buildManagedLaunchSpec(req Request) (agent.LaunchSpec, error) {
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

func newManagedTeamStatusPath(req Request) string {
	if req.CodexTeamMode {
		return req.CodexTeamStatusPath
	}
	return ""
}

func newManagedPlanStatusPath(req Request) string {
	if req.CodexPlanMode() {
		return req.CodexPlanStatusPath
	}
	return ""
}

func validateManagedLaunchBinding(
	req Request,
	launch *state.LaunchCapsule,
	route backend.OwnedLaunchRoute,
) error {
	if err := validateManagedTeamBinding(req, launch); err != nil {
		return err
	}
	boundReq := req
	boundReq.CodexTeamStatusPath = launch.CodexTeamStatusPath
	boundReq.CodexPlanStatusPath = launch.CodexPlanStatusPath
	spec, err := buildManagedLaunchSpecForRoute(boundReq, route)
	if err != nil {
		return err
	}
	if launch.Agent != req.Agent || launch.Executable != spec.Executable ||
		!slices.Equal(launch.Args, spec.Args) {
		return fmt.Errorf("saved Herdr launch does not match the current agent command")
	}
	return nil
}

func validateManagedTeamBinding(req Request, launch *state.LaunchCapsule) error {
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

func waitForManagedCodexTUI(req Request, intent state.LaunchIntent) (codexapp.Status, error) {
	statusPath := requestedManagedCodexStatusPath(req)
	if statusPath == "" {
		return codexapp.Status{}, nil
	}
	timeout := min(CodexPlanTUIStartupTimeout, remainingManagedLaunchTime(intent))
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

func requestedManagedCodexStatusPath(req Request) string {
	if req.CodexPlanMode() {
		return req.CodexPlanStatusPath
	}
	if req.CodexTeamMode {
		return req.CodexTeamStatusPath
	}
	return ""
}

func managedCodexStatusPath(req Request, intent state.LaunchIntent) (string, error) {
	if intent.Launch != nil {
		if err := validateManagedTeamBinding(req, intent.Launch); err != nil {
			return "", err
		}
	}
	if requestedManagedCodexStatusPath(req) == "" {
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

func awaitManagedCodexTUI(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	projectRoot string,
	intent state.LaunchIntent,
) (codexapp.Status, error) {
	if requestedManagedCodexStatusPath(req) == "" {
		return codexapp.Status{}, nil
	}
	journal, latest, err := loadManagedCodexIntent(locked, projectRoot, intent.ID)
	if err != nil {
		return codexapp.Status{}, err
	}
	req, err = bindManagedCodexStatusPath(req, latest)
	if err != nil {
		return codexapp.Status{}, errors.Join(err, markManagedIntentManual(
			journal, latest, fmt.Errorf("codex TUI controller readiness failed: %w", err),
		))
	}
	status, journal, latest, err := waitForManagedCodexTUIUnlocked(
		ctx, req, locked, projectRoot, latest,
	)
	if err == nil {
		return status, nil
	}
	if journal == nil {
		return status, err
	}
	return status, errors.Join(err, markManagedIntentManual(
		journal, latest, fmt.Errorf("codex TUI controller readiness failed: %w", err),
	))
}

func bindManagedCodexStatusPath(req Request, intent state.LaunchIntent) (Request, error) {
	statusPath, err := managedCodexStatusPath(req, intent)
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

func waitForManagedCodexTUIUnlocked(
	ctx context.Context,
	req Request,
	locked *state.LockedStore,
	projectRoot string,
	intent state.LaunchIntent,
) (codexapp.Status, *state.LockedLaunchJournal, state.LaunchIntent, error) {
	if err := locked.Unlock(); err != nil {
		return codexapp.Status{}, nil, intent, err
	}
	status, waitErr := waitForManagedCodexTUI(req, intent)
	if err := reacquireManagedLaunchLock(locked, projectRoot, intent); err != nil {
		return status, nil, intent, errors.Join(waitErr, err)
	}
	journal, latest, err := loadManagedCodexIntent(locked, projectRoot, intent.ID)
	if err != nil {
		return status, nil, intent, errors.Join(waitErr, err)
	}
	if waitErr == nil {
		waitErr = ensureManagedLaunchActive(ctx, latest)
	}
	return status, journal, latest, waitErr
}

func loadManagedCodexIntent(
	locked *state.LockedStore,
	projectRoot, intentID string,
) (*state.LockedLaunchJournal, state.LaunchIntent, error) {
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return nil, state.LaunchIntent{}, err
	}
	latest, found := journal.FindIntent(intentID)
	if !found {
		return nil, state.LaunchIntent{}, fmt.Errorf("codex launch intent %s disappeared", intentID)
	}
	return journal, latest, nil
}

func persistNewManagedLaunch(
	runtime ManagedLaunchRuntime,
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	runtimeDir string,
) (state.LaunchIntent, error) {
	persisted, err := persistManagedLaunch(journal, intent)
	if err == nil {
		return persisted, nil
	}
	return intent, errors.Join(err, removeUnpublishedManagedEnvironment(runtime, runtimeDir, intent.Launch))
}

func removeUnpublishedManagedEnvironment(
	runtime ManagedWorktreeRuntime,
	runtimeDir string,
	launch *state.LaunchCapsule,
) error {
	return runtime.DiscardWorkloadEnvironment(runtimeDir, launch)
}

func persistManagedLaunch(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
) (state.LaunchIntent, error) {
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		return intent, err
	}
	return intent, nil
}

func (l *Launcher) failClosedIssuedManagedLaunch(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	cause error,
) error {
	responseLost := fmt.Errorf(
		"%w: launch-token outcome is indeterminate",
		errManagedLaunchResponseLost,
	)
	if cause != nil {
		responseLost = errors.Join(cause, responseLost)
	}
	return markManagedIntentManual(journal, intent, responseLost)
}

func managedAgentStatePane(
	req Request,
	intent state.LaunchIntent,
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
	applyManagedLaunchOwnership(&pane, intent)
	applyManagedLaunchTelemetry(&pane, intent)
	pane.DirectAgentLaunch = !req.CodexPlanMode() && !req.CodexTeamMode
	return pane, nil
}

func latestManagedLaunchIntent(
	locked *state.LockedStore,
	projectRoot string,
	intentID string,
) (*state.LockedLaunchJournal, state.LaunchIntent, error) {
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return nil, state.LaunchIntent{}, err
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		return nil, state.LaunchIntent{}, fmt.Errorf("finalize Herdr launch: intent %s disappeared", intentID)
	}
	return journal, intent, nil
}

func applyManagedLaunchTelemetry(pane *state.Pane, intent state.LaunchIntent) {
	launch := intent.Launch
	if pane == nil || launch == nil {
		return
	}
	pane.LaunchExecutable = launch.Executable
	pane.LaunchArgs = slices.Clone(launch.Args)
	if launch.EmitterNonce == "" {
		return
	}
	pane.EmitterRowKey = intent.ID
	pane.LaunchNonce = launch.Nonce
	pane.EmitterNonce = launch.EmitterNonce
	pane.ReportedState = string(backend.AgentRunning)
	pane.ReportedStateSeq = 0
	pane.StateRefinement = false
	if launch.PendingReportedState != "" && launch.PendingAgentSession != nil &&
		pane.AgentSession != nil && *launch.PendingAgentSession == *pane.AgentSession {
		pane.ReportedState = launch.PendingReportedState
		pane.ReportedStateSeq = launch.PendingReportedSeq
		pane.StateRefinement = true
	}
}

func applyManagedLaunchOwnership(pane *state.Pane, intent state.LaunchIntent) {
	pane.RuntimeParent = intent.RuntimeParent
	pane.WorkspaceLabel = intent.WorkspaceLabel
	pane.BranchCreated = intent.BranchCreated
}

func markManagedFinalizationFailure(
	locked *state.LockedStore,
	projectRoot string,
	intent state.LaunchIntent,
	cause error,
) error {
	if errors.Is(cause, errManagedLaunchStatePreserved) {
		return cause
	}
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return err
	}
	latest, found := journal.FindIntent(intent.ID)
	if !found {
		return fmt.Errorf("finalize Herdr launch: intent %s disappeared", intent.ID)
	}
	return markManagedIntentManual(journal, latest, fmt.Errorf("finalize Herdr launch: %w", cause))
}

func printManagedPaneDryRun(req Request, backendPreview []string, lg *log.Logger, c log.Palette) {
	if req.BriefingPath != "" || req.BriefingBody != "" {
		fmt.Fprintf(lg.Stdout(), "  %sbriefing size%s: %d bytes\n", c.Dim, c.Reset, len(req.BriefingBody))
	}
	printBackendDryRun(backendPreview, lg, c)
	printPaneHookDryRun(req, lg, c)
	lg.Ok("%s: dry-run complete", paneLogLabel(req))
}
