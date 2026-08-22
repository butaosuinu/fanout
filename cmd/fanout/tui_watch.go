package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/app/sessionbinding"
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/fanset"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func newTUIWatcher(projectRoot, session, commandName string, resolvedSettings settings.Settings, hookConfig hooks.Config, includeAmbientRoute, interactiveLaunch bool) (fanouttui.WatcherRunner, time.Duration, string, error) {
	if !resolvedSettings.Watcher || !interactiveLaunch {
		return nil, 0, "", nil
	}
	preflightCfg := newWatcherPreflightConfig(resolvedSettings)
	if _, err := resolveTUILaunchRuntime(projectRoot, session, preflightCfg); err != nil {
		return nil, 0, "", err
	}
	gh := ghissue.Runner{Cwd: projectRoot}
	if err := gh.EnsureLabel(resolvedSettings.WatcherRunningLabel); err != nil {
		return nil, 0, "", fmt.Errorf("ensure running label %q: %w", resolvedSettings.WatcherRunningLabel, err)
	}
	livePanes := &watchLivePaneCache{list: runtimeListLiveForProject(projectRoot, includeAmbientRoute)}
	watcher := &tuiWatcher{livePanes: livePanes}
	io := watch.IO{
		ListLabeled: gh.ListOpenIssuesWithLabel,
		PlanChildren: func(issue ghissue.Issue) (watch.ChildPlan, error) {
			return newWatchParentChildPlan(projectRoot, session, commandName, resolvedSettings, watcher, gh, issue)
		},
		SwapLabels: func(issue ghissue.Issue, removeLabel, addLabel string) error {
			cfg := newWatchLaunchConfig(resolvedSettings, issue.Number, 0)
			if _, err := resolveTUILaunchRuntime(projectRoot, session, cfg); err != nil {
				return err
			}
			return gh.SwapIssueLabels(issue.Number, removeLabel, addLabel)
		},
		LoadState: func() (state.Store, error) {
			return state.LoadProject(projectRoot)
		},
		PaneAlive: livePanes.Alive,
		LaunchStandalone: func(issue ghissue.Issue) error {
			notice, err := launchWatchStandalone(projectRoot, session, commandName, resolvedSettings, hookConfig, issue)
			watcher.addNotice(notice)
			return err
		},
		PlanLinkedIssueNums: func(store state.Store) map[int]bool {
			return panelaunch.PlanLinkedIssueNums(projectRoot, store)
		},
	}
	cfg := watch.Config{
		TriggerLabel: resolvedSettings.WatcherTriggerLabel,
		RunningLabel: resolvedSettings.WatcherRunningLabel,
		MaxSessions:  resolvedSettings.WatcherMaxSessions,
	}
	interval := time.Duration(resolvedSettings.WatcherIntervalSeconds) * time.Second
	watcher.engine = watch.NewEngine(cfg, io)
	return watcher, interval, resolvedSettings.WatcherTriggerLabel, nil
}

func newWatchParentChildPlan(projectRoot, session, commandName string, resolvedSettings settings.Settings, watcher *tuiWatcher, gh ghissue.Runner, issue ghissue.Issue) (watch.ChildPlan, error) {
	prepared, counts, err := prepareWatchParentPlan(projectRoot, gh, issue.Number)
	if err != nil {
		return watch.ChildPlan{}, err
	}
	return watch.ChildPlan{
		Counts: counts,
		LaunchParent: func(limit int) (watch.ParentLaunchResult, error) {
			cfg := newWatchLaunchConfig(resolvedSettings, issue.Number, limit)
			result, err := launchParentIssueFanoutWithPlanInput(projectRoot, session, commandName, cfg, prepared.runInput())
			watcher.addNotice(result.Notice)
			return result, err
		},
	}, nil
}

type tuiWatcher struct {
	engine    *watch.Engine
	livePanes *watchLivePaneCache
	notices   []string
}

func (w *tuiWatcher) RunCycle() (watch.Report, error) {
	if w == nil || w.engine == nil {
		return watch.Report{}, fmt.Errorf("watcher is nil")
	}
	w.livePanes.Reset()
	w.notices = nil
	report, err := w.engine.RunCycle()
	report.Notices = append(report.Notices, w.notices...)
	return report, err
}

func (w *tuiWatcher) addNotice(notice string) {
	if w == nil || strings.TrimSpace(notice) == "" || slices.Contains(w.notices, notice) {
		return
	}
	w.notices = append(w.notices, notice)
}

func launchWatchStandalone(projectRoot, session, commandName string, resolvedSettings settings.Settings, hookConfig hooks.Config, issue ghissue.Issue) (string, error) {
	cfg := newWatchLaunchConfig(resolvedSettings, issue.Number, 0)
	return launchStandaloneIssuePane(projectRoot, session, commandName, cfg, resolvedSettings, hookConfig, issue)
}

// launchStandaloneIssuePane creates one pane for a single issue with no OPEN
// children. The watcher and the TUI issue launcher share it; cfg carries the
// caller's agent selection.
func launchStandaloneIssuePane(projectRoot, session, commandName string, cfg *cliflags.Config, resolvedSettings settings.Settings, hookConfig hooks.Config, issue ghissue.Issue) (string, error) {
	result, err := launchStandaloneIssuePaneWithResult(projectRoot, session, commandName, cfg, resolvedSettings, hookConfig, issue)
	return result.Notice, err
}

// launchStandaloneIssuePaneWithResult is the TUI-facing standalone launch
// path. The watcher keeps the notice-and-error wrapper above so background
// launches never gain foreground-focus behavior.
func launchStandaloneIssuePaneWithResult(projectRoot, session, commandName string, cfg *cliflags.Config, resolvedSettings settings.Settings, hookConfig hooks.Config, issue ghissue.Issue) (panelaunch.Result, error) {
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	rt, err := resolveTUILaunchRuntime(projectRoot, session, cfg)
	if err != nil {
		return panelaunch.Result{}, err
	}
	store, recorder, code := run.LoadState(cfg.DryRun, projectRoot, launchLogger)
	if code != exitcode.OK {
		return panelaunch.Result{}, bufferedLaunchError(stdout, stderr, "load fanout state")
	}
	if recorder != nil {
		defer func() {
			_ = recorder.Unlock()
		}()
	}
	if err := admitStandaloneIssueRuntime(projectRoot, cfg, rt, store, issue.Number); err != nil {
		return panelaunch.Result{}, err
	}
	req := panelaunch.NewWatchRequest(cfg, projectRoot, issue, resolvedSettings, hookConfig)
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: rt.Info, Backend: rt.Backend, Managed: rt.Managed, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
	result, ok := launcher.LaunchWithResult(req)
	if !ok {
		return panelaunch.Result{}, bufferedLaunchError(stdout, stderr, "create watch pane")
	}
	result.Notice = combinedLaunchNotice([]string{result.Notice}, bufferedLaunchNotice(stderr))
	return result, nil
}

func admitStandaloneIssueRuntime(projectRoot string, cfg *cliflags.Config, rt *run.Runtime, store state.Store, issueNum int) error {
	if rt.VerifyBackend != nil {
		if err := rt.VerifyBackend(cfg.ParentRef, store); err != nil {
			return fmt.Errorf("runtime backend: %w", err)
		}
	}
	if hasRecordedIssuePane(projectRoot, store, issueNum) {
		return watch.ErrAlreadyFanned
	}
	if err := validateStandaloneIssueAgent(cfg, issueNum); err != nil {
		return err
	}
	if err := rt.PrepareLaunchBackend(); err != nil {
		return fmt.Errorf("runtime backend: %w", err)
	}
	return nil
}

func validateStandaloneIssueAgent(cfg *cliflags.Config, issueNum int) error {
	agentName := cfg.EffectiveAgentForIssue(issueNum)
	if agentName == "" {
		return fmt.Errorf("#%d: agent is required", issueNum)
	}
	if err := agent.ValidateKnown(agentName); err != nil {
		return err
	}
	if cfg.DryRun {
		return nil
	}
	return agent.ValidateInstalled(agentName)
}

func launchParentIssueFanoutWithPlanInput(projectRoot, session, commandName string, cfg *cliflags.Config, input run.IssuePlanInput) (watch.ParentLaunchResult, error) {
	result, err := launchParentIssueFanoutWithPlanInputResult(projectRoot, session, commandName, cfg, &input, nil, nil)
	result.Watch.Notice = result.Notice
	return result.Watch, err
}

// launchParentIssueFanout runs the full issue-mode fan-out for cfg.Parent
// against a synthesized runtime targeting the TUI session. The watcher and
// the TUI issue launcher share it.
func launchParentIssueFanout(projectRoot, session, commandName string, cfg *cliflags.Config) (watch.ParentLaunchResult, error) {
	result, err := launchParentIssueFanoutWithPlanInputResult(projectRoot, session, commandName, cfg, nil, nil, nil)
	result.Watch.Notice = result.Notice
	return result.Watch, err
}

type parentIssueFanoutResult struct {
	Watch               watch.ParentLaunchResult
	CreatedPaneIDs      []string
	CreatedBindings     []backend.PaneBinding
	OrchestratorBinding backend.PaneBinding
	Notice              string
	runtimeBackend      backend.Backend
	managed             panelaunch.ManagedSessionRuntime
}

type tuiIssueReadyFunc func(
	state.Store,
	panelaunch.StateRecorder,
	backend.Backend,
	panelaunch.ManagedSessionRuntime,
) error

type tuiIssueAfterFunc func(
	state.Store,
	panelaunch.StateRecorder,
	backend.Backend,
	panelaunch.ManagedSessionRuntime,
	run.IssueAfterContext,
) error

// launchParentIssueFanoutWithResult preserves the exact pane ids returned by
// tmux for the foreground TUI launch. The watcher calls the wrapper above and
// deliberately discards them so it cannot steal focus.
func launchParentIssueFanoutWithResult(projectRoot, session, commandName string, cfg *cliflags.Config, ready tuiIssueReadyFunc) (parentIssueFanoutResult, error) {
	return launchParentIssueFanoutWithPlanInputResult(projectRoot, session, commandName, cfg, nil, ready, nil)
}

func launchParentIssueFanoutWithCallbacks(projectRoot, session, commandName string, cfg *cliflags.Config, ready tuiIssueReadyFunc, after tuiIssueAfterFunc) (parentIssueFanoutResult, error) {
	return launchParentIssueFanoutWithPlanInputResult(projectRoot, session, commandName, cfg, nil, ready, after)
}

//nolint:funlen // Keep runtime resolution, readiness injection, and result projection in one backend transaction boundary.
func launchParentIssueFanoutWithPlanInputResult(projectRoot, session, commandName string, cfg *cliflags.Config, input *run.IssuePlanInput, ready tuiIssueReadyFunc, after tuiIssueAfterFunc) (parentIssueFanoutResult, error) {
	// A plan session for this issue (a coordinator, or the tasks it fanned out)
	// must finish or be closed before the child fan-out lane runs, or the two
	// decompose the same work twice. Best-effort read: a state read failure
	// degrades to the pre-existing unguarded behavior.
	if store, err := state.LoadProject(projectRoot); err == nil && issuePlanRecorded(projectRoot, store, cfg.Parent) {
		return parentIssueFanoutResult{}, fmt.Errorf("issue #%d already has a plan session; close it before fanning out children", cfg.Parent)
	}
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	rt, err := resolveTUILaunchRuntime(projectRoot, session, cfg)
	if err != nil {
		return parentIssueFanoutResult{}, err
	}
	var runReady run.IssueReadyFunc
	if ready != nil {
		runReady = func(store state.Store, recorder panelaunch.StateRecorder) error {
			return ready(store, recorder, rt.Backend, rt.Managed)
		}
	}
	var runAfter run.IssueAfterFunc
	if after != nil {
		runAfter = func(store state.Store, recorder panelaunch.StateRecorder, progress run.IssueAfterContext) error {
			return after(store, recorder, rt.Backend, rt.Managed, progress)
		}
	}
	var execution run.IssueExecutionResult
	var code exitcode.Code
	if input == nil {
		execution, code = run.IssuesWithResultWhenReady(cfg, launchLogger, rt, commandName, bindDashboardKey, runReady, runAfter)
	} else {
		execution, code = run.IssuesWithPlanInputResultWhenReady(cfg, launchLogger, rt, commandName, bindDashboardKey, *input, runReady, runAfter)
	}
	result := parentIssueFanoutResult{
		CreatedPaneIDs: execution.CreatedPaneIDs, CreatedBindings: execution.CreatedBindings,
		Notice:         combinedLaunchNotice(execution.Notices, bufferedLaunchNotice(stderr)),
		runtimeBackend: rt.Backend, managed: rt.Managed,
	}
	if code != exitcode.OK {
		return result, bufferedLaunchError(stdout, stderr, "launch parent")
	}
	result.Watch = watchParentLaunchResult(execution.Plan, execution.CreatedIssueNums)
	return result, nil
}

func newWatchLaunchConfig(resolvedSettings settings.Settings, parent, limit int) *cliflags.Config {
	return &cliflags.Config{
		Parent:          parent,
		ParentRef:       strconv.Itoa(parent),
		ParentMode:      cliflags.ModeIssue,
		Agent:           watcherAgent(resolvedSettings),
		PlanMode:        new(resolvedSettings.ChildPlanMode),
		Limit:           limit,
		SleepBetween:    cliflags.DefaultSleepBetween,
		PopupTimeoutSec: cliflags.DefaultPopupTimeout,
		ProjectStatus:   cliflags.DefaultProjectStatus,
		Format:          cliflags.DefaultFormat,
		UnblockedOnly:   true,
	}
}

func newWatcherPreflightConfig(resolvedSettings settings.Settings) *cliflags.Config {
	cfg := newWatchLaunchConfig(resolvedSettings, 0, 0)
	cfg.ParentRef = tuiWatcherPreflightRef
	return cfg
}

func watcherAgent(resolvedSettings settings.Settings) string {
	if agentName := strings.TrimSpace(resolvedSettings.WatcherAgent); agentName != "" {
		return agentName
	}
	return defaultTUIAgent()
}

func countOpenChildTargets(gh ghissue.Runner, parent int) (int, error) {
	loaded, err := loadWatchParentChildren(gh, parent)
	if err != nil {
		return 0, err
	}
	return len(run.OpenIssues(loaded.Children)), nil
}

func countWatchChildTargets(projectRoot string, gh ghissue.Runner, parent int) (watch.ChildCounts, error) {
	_, counts, err := prepareWatchParentPlan(projectRoot, gh, parent)
	return counts, err
}

type watchParentPlan struct {
	loaded     run.ChildLoadResult
	gh         ghissue.Runner
	hydrations map[int]watchIssueHydration
	states     map[int]watchIssueState
}

type watchIssueHydration struct {
	body   string
	labels []ghissue.Label
	err    error
}

type watchIssueState struct {
	state string
	err   error
}

func prepareWatchParentPlan(projectRoot string, gh ghissue.Runner, parent int) (*watchParentPlan, watch.ChildCounts, error) {
	loaded, err := loadWatchParentChildren(gh, parent)
	if err != nil {
		return nil, watch.ChildCounts{}, err
	}
	prepared := &watchParentPlan{
		loaded:     loaded,
		gh:         gh,
		hydrations: map[int]watchIssueHydration{},
		states:     map[int]watchIssueState{},
	}
	for _, issue := range loaded.Children {
		if !loaded.StrongChildren[issue.Number] {
			prepared.hydrations[issue.Number] = watchIssueHydration{
				body:   issue.Body,
				labels: slices.Clone(issue.Labels),
			}
		}
	}
	cfg := newWatchPlanConfig(parent, 0)
	plan, err := buildWatchParentPlan(projectRoot, cfg, prepared)
	if err != nil {
		return nil, watch.ChildCounts{}, err
	}
	return prepared, watch.ChildCounts{
		Open:       plan.OpenCount,
		Launchable: len(plan.Targets),
		Unfanned:   plan.UnfannedCount,
	}, nil
}

func (p *watchParentPlan) runInput() run.IssuePlanInput {
	return run.IssuePlanInput{
		Loaded:            p.loaded,
		HydrateBodyLabels: p.hydrateBodyLabels,
		IssueState:        p.issueState,
	}
}

func (p *watchParentPlan) hydrateBodyLabels(issue *ghissue.Issue) error {
	hydration, ok := p.hydrations[issue.Number]
	if !ok {
		hydrated := *issue
		err := p.gh.HydrateBodyLabels(&hydrated)
		hydration = watchIssueHydration{
			body:   hydrated.Body,
			labels: slices.Clone(hydrated.Labels),
			err:    err,
		}
		p.hydrations[issue.Number] = hydration
	}
	issue.Body = hydration.body
	issue.Labels = slices.Clone(hydration.labels)
	return hydration.err
}

func (p *watchParentPlan) issueState(num int) (string, error) {
	result, ok := p.states[num]
	if !ok {
		result.state, result.err = p.gh.IssueState(num)
		p.states[num] = result
	}
	return result.state, result.err
}

type watchLivePaneCache struct {
	list   func() ([]backend.LivePane, error)
	loaded bool
	panes  []backend.LivePane
	err    error
}

func (c *watchLivePaneCache) Reset() {
	if c == nil {
		return
	}
	c.loaded = false
	c.panes = nil
	c.err = nil
}

func (c *watchLivePaneCache) Alive(pane state.Pane) (bool, error) {
	if strings.TrimSpace(pane.PaneID) == "" {
		return false, nil
	}
	panes, err := c.load()
	if err != nil {
		return false, err
	}
	recordedKey := strings.TrimSpace(pane.ShellKey)
	for _, live := range panes {
		if backend.NormalizeName(pane.Backend) == backend.NormalizeName(live.Ref.Backend) &&
			pane.PaneID == live.Ref.Pane && recordedKey != "" && strings.TrimSpace(live.ShellKey) == "" {
			return false, fmt.Errorf("pane %s liveness key is unavailable", pane.PaneID)
		}
		if watchPaneMatchesLive(pane, live) {
			return true, nil
		}
	}
	return false, nil
}

func (c *watchLivePaneCache) load() ([]backend.LivePane, error) {
	if c == nil {
		return nil, fmt.Errorf("runtime backend live-pane collector is not configured")
	}
	if !c.loaded {
		if c.list == nil {
			return nil, fmt.Errorf("runtime backend live-pane collector is not configured")
		}
		c.panes, c.err = c.list()
		c.loaded = true
	}
	return c.panes, c.err
}

func watchPaneMatchesLive(pane state.Pane, live backend.LivePane) bool {
	if backend.NormalizeName(pane.Backend) != backend.NormalizeName(live.Ref.Backend) || pane.PaneID != live.Ref.Pane {
		return false
	}
	// The saved row records which runtime issued the pane, and an owned row is
	// matched on its recorded binding rather than on a path the pane can change.
	if backend.NormalizeName(pane.Backend) == backend.Herdr {
		return watchManagedPaneMatchesLive(pane, live)
	}
	if shellKey := strings.TrimSpace(pane.ShellKey); shellKey != "" {
		return shellKey == live.ShellKey
	}
	if pane.IsShell() {
		return false
	}
	worktree := strings.TrimSpace(pane.WorktreePath)
	if worktree == "" {
		return true
	}
	wt := filepath.Clean(worktree)
	cp := filepath.Clean(live.CurrentPath)
	return cp == wt || strings.HasPrefix(cp, wt+string(filepath.Separator))
}

func watchManagedPaneMatchesLive(pane state.Pane, live backend.LivePane) bool {
	if pane.AgentSession == nil && live.AgentSession != nil {
		return sessionbinding.FirstBindMatches(pane, live)
	}
	return pane.RuntimeBinding().MatchesLive(live)
}

func watchParentLaunchResult(plan run.Plan, created []int) watch.ParentLaunchResult {
	createdSet := map[int]bool{}
	for _, num := range created {
		createdSet[num] = true
	}
	for _, target := range plan.Targets {
		if !createdSet[target.Number] {
			return watch.ParentLaunchResult{Deferred: true}
		}
	}
	return watch.ParentLaunchResult{
		Deferred: len(plan.BlockedRows) > 0 || len(plan.LimitDeferred) > 0,
	}
}

func buildWatchParentPlan(projectRoot string, cfg *cliflags.Config, prepared *watchParentPlan) (run.Plan, error) {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return run.Plan{}, fmt.Errorf("load fanout state: %w", err)
	}
	sameParentFanned := store.FannedNumbersForParent(cfg.ParentRef)
	otherParentFanned := store.FannedNumbersForOtherParents(cfg.ParentRef)
	worktreeFallbackFanned := run.ExistingWorktreeFanned(cfg, projectRoot, prepared.loaded.Children, otherParentFanned)
	// Match run.Issues: plan-owned children never become targets, so the
	// watcher's capacity planning agrees with what a launch would create.
	planOwnedFanned := panelaunch.PlanLinkedIssueNums(projectRoot, store)
	return run.BuildPlan(
		cfg,
		prepared.loaded.Children,
		fanset.Union(sameParentFanned, worktreeFallbackFanned, planOwnedFanned),
		prepared.loaded.ParentBody,
		func(issue *ghissue.Issue) {
			// Match run.Issues: a failed hydration degrades blocker checks to
			// unblocked; the launch plan reports the same cached error as a warning.
			_ = prepared.hydrateBodyLabels(issue)
		},
		func(num int) string {
			stateName, _ := prepared.issueState(num)
			return stateName
		},
	), nil
}

func loadWatchParentChildren(gh ghissue.Runner, parent int) (run.ChildLoadResult, error) {
	var stdout, stderr bytes.Buffer
	lg := log.NewWith(&stdout, &stderr, false)
	cfg := newWatchPlanConfig(parent, 0)
	lg.Info("fetching sub-issues of #%d", cfg.Parent)
	subIssues, err := gh.SubIssueList(cfg.Parent)
	if err != nil {
		lg.Err("sub-issues fetch failed: %v", err)
		return run.ChildLoadResult{}, bufferedLaunchError(stdout, stderr, "load child issues")
	}
	parentBody, err := gh.ParentBody(cfg.Parent)
	if err != nil {
		lg.Err("parent body fetch failed: %v", err)
		return run.ChildLoadResult{}, bufferedLaunchError(stdout, stderr, "load child issues")
	}
	bodyNums := ghissue.TaskListNumbers(parentBody)
	loaded, added := mergeWatchExtraChildren(cfg, gh, subIssues, bodyNums, lg)
	run.AssignTaskListWaves(loaded, ghissue.TaskListWaves(parentBody))
	if added > 0 {
		lg.Info("parent body added %d extra child reference(s) not in sub-issue API", added)
	}
	return run.ChildLoadResult{
		Children:       loaded,
		ParentBody:     parentBody,
		StrongChildren: run.IssueSet(subIssues),
		ChildNoun:      "sub-issues",
	}, nil
}

func newWatchPlanConfig(parent, limit int) *cliflags.Config {
	return &cliflags.Config{
		Parent:        parent,
		ParentRef:     strconv.Itoa(parent),
		ParentMode:    cliflags.ModeIssue,
		Limit:         limit,
		UnblockedOnly: true,
	}
}

func mergeWatchExtraChildren(cfg *cliflags.Config, gh ghissue.Runner, base []ghissue.Issue, bodyNums []int, lg *log.Logger) ([]ghissue.Issue, int) {
	existing := map[int]bool{cfg.Parent: true}
	for _, s := range base {
		existing[s.Number] = true
	}

	var extra []ghissue.Issue
	for _, num := range bodyNums {
		if existing[num] {
			continue
		}
		detail, err := gh.IssueDetail(num)
		if err != nil {
			lg.Warn("parent body references #%d but issue lookup failed; skipping", num)
			continue
		}
		extra = append(extra, detail)
		existing[num] = true
	}
	return ghissue.MergeExtra(base, extra), len(extra)
}

func hasRecordedIssuePane(projectRoot string, store state.Store, issueNum int) bool {
	for _, pane := range store.Panes {
		if pane.IssueNum == issueNum {
			return true
		}
		// The worktree-suffix fallback matches legacy and other-parent rows the
		// watcher's alreadyFanned treats as fanned, so the standalone and plan
		// lanes refuse the same set of issues.
		if pane.IssueNum > 0 && watch.PaneWorktreeMatchesIssue(pane, issueNum) {
			return true
		}
		if num, ok := panelaunch.OrchestratorPaneIssueNum(pane); ok && num == issueNum {
			return true
		}
	}
	// Plan-lane rows bind to their issue only through the coordinator slug or
	// the saved spec's declared source. Without this, a standalone launch for
	// the issue would run alongside the plan session and duplicate the work.
	return issuePlanRecorded(projectRoot, store, issueNum)
}
