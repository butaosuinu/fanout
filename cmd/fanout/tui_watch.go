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
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/fanset"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func newTUIWatcher(projectRoot, session, commandName string, resolvedSettings settings.Settings, hookConfig hooks.Config) (fanouttui.WatcherRunner, time.Duration, string, error) {
	if !resolvedSettings.Watcher {
		return nil, 0, "", nil
	}
	preflightCfg := &cliflags.Config{ParentRef: tuiWatcherPreflightRef}
	if _, err := resolveTUILaunchRuntime(projectRoot, session, preflightCfg); err != nil {
		return nil, 0, "", err
	}
	gh := ghissue.Runner{Cwd: projectRoot}
	if err := gh.EnsureLabel(resolvedSettings.WatcherRunningLabel); err != nil {
		return nil, 0, "", fmt.Errorf("ensure running label %q: %w", resolvedSettings.WatcherRunningLabel, err)
	}
	livePanes := &watchLivePaneCache{list: tmuxbackend.New().ListLiveForIdentity}
	watcher := &tuiWatcher{livePanes: livePanes}
	io := watch.IO{
		ListLabeled: gh.ListOpenIssuesWithLabel,
		CountChildren: func(issue ghissue.Issue) (watch.ChildCounts, error) {
			return countWatchChildTargets(projectRoot, gh, issue.Number)
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
		LaunchParent: func(issue ghissue.Issue, limit int) (watch.ParentLaunchResult, error) {
			result, err := launchWatchParent(projectRoot, session, commandName, resolvedSettings, issue, limit)
			watcher.addNotice(result.Notice)
			return result, err
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
	if rt.VerifyBackend != nil {
		if err := rt.VerifyBackend(cfg.ParentRef, store); err != nil {
			return panelaunch.Result{}, fmt.Errorf("runtime backend: %w", err)
		}
	}
	if hasRecordedIssuePane(projectRoot, store, issue.Number) {
		return panelaunch.Result{}, watch.ErrAlreadyFanned
	}
	req := panelaunch.NewWatchRequest(cfg, projectRoot, issue, resolvedSettings, hookConfig)
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: rt.Info, Backend: rt.Backend, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
	result, ok := launcher.LaunchWithResult(req)
	if !ok {
		return panelaunch.Result{}, bufferedLaunchError(stdout, stderr, "create watch pane")
	}
	return result, nil
}

func launchWatchParent(projectRoot, session, commandName string, resolvedSettings settings.Settings, issue ghissue.Issue, limit int) (watch.ParentLaunchResult, error) {
	cfg := newWatchLaunchConfig(resolvedSettings, issue.Number, limit)
	return launchParentIssueFanout(projectRoot, session, commandName, cfg)
}

// launchParentIssueFanout runs the full issue-mode fan-out for cfg.Parent
// against a synthesized runtime targeting the TUI session. The watcher and
// the TUI issue launcher share it.
func launchParentIssueFanout(projectRoot, session, commandName string, cfg *cliflags.Config) (watch.ParentLaunchResult, error) {
	result, err := launchParentIssueFanoutWithResult(projectRoot, session, commandName, cfg, nil)
	result.Watch.Notice = result.Notice
	return result.Watch, err
}

type parentIssueFanoutResult struct {
	Watch          watch.ParentLaunchResult
	CreatedPaneIDs []string
	Notice         string
	runtimeBackend backend.Backend
}

type tuiIssueReadyFunc func(state.Store, panelaunch.StateRecorder, backend.Backend) error

// launchParentIssueFanoutWithResult preserves the exact pane ids returned by
// tmux for the foreground TUI launch. The watcher calls the wrapper above and
// deliberately discards them so it cannot steal focus.
func launchParentIssueFanoutWithResult(projectRoot, session, commandName string, cfg *cliflags.Config, ready tuiIssueReadyFunc) (parentIssueFanoutResult, error) {
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
			return ready(store, recorder, rt.Backend)
		}
	}
	execution, code := run.IssuesWithResultWhenReady(cfg, launchLogger, rt, commandName, bindDashboardKey, runReady)
	result := parentIssueFanoutResult{
		CreatedPaneIDs: execution.CreatedPaneIDs,
		Notice:         combinedLaunchNotice(execution.Notices, bufferedLaunchNotice(stderr)),
		runtimeBackend: rt.Backend,
	}
	if code != exitcode.OK {
		return result, bufferedLaunchError(stdout, stderr, "launch parent")
	}
	result.Watch = watchParentResultAfterLaunch(projectRoot, cfg, rt.GH)
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
	cfg := newWatchPlanConfig(parent, 0)
	plan, err := buildWatchParentPlan(projectRoot, cfg, gh)
	if err != nil {
		return watch.ChildCounts{}, err
	}
	return watch.ChildCounts{
		Open:       plan.OpenCount,
		Launchable: len(plan.Targets),
		Unfanned:   plan.UnfannedCount,
	}, nil
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

func watchParentResultAfterLaunch(projectRoot string, cfg *cliflags.Config, gh ghissue.Runner) watch.ParentLaunchResult {
	deferred, err := watchParentHasRemainingTargets(projectRoot, cfg, gh)
	if err != nil {
		// The parent fan-out already completed. Keep the parent retriable instead
		// of reporting the completed launch as failed.
		return watch.ParentLaunchResult{Deferred: true}
	}
	return watch.ParentLaunchResult{Deferred: deferred}
}

func watchParentHasRemainingTargets(projectRoot string, cfg *cliflags.Config, gh ghissue.Runner) (bool, error) {
	plan, err := buildWatchParentPlan(projectRoot, cfg, gh)
	if err != nil {
		return false, err
	}
	return len(plan.Targets) > 0 || len(plan.BlockedRows) > 0 || len(plan.LimitDeferred) > 0, nil
}

func buildWatchParentPlan(projectRoot string, cfg *cliflags.Config, gh ghissue.Runner) (run.Plan, error) {
	loaded, err := loadWatchParentChildren(gh, cfg.Parent)
	if err != nil {
		return run.Plan{}, err
	}
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return run.Plan{}, fmt.Errorf("load fanout state: %w", err)
	}
	sameParentFanned := store.FannedNumbersForParent(cfg.ParentRef)
	otherParentFanned := store.FannedNumbersForOtherParents(cfg.ParentRef)
	worktreeFallbackFanned := run.ExistingWorktreeFanned(cfg, projectRoot, loaded.Children, otherParentFanned)
	// Match run.Issues: plan-owned children never become targets, so the
	// watcher's capacity planning and post-launch remaining-target recompute
	// agree with what a launch would actually create.
	planOwnedFanned := panelaunch.PlanLinkedIssueNums(projectRoot, store)
	return run.BuildPlan(
		cfg,
		loaded.Children,
		fanset.Union(sameParentFanned, worktreeFallbackFanned, planOwnedFanned),
		loaded.ParentBody,
		func(issue *ghissue.Issue) {
			// Match run.Issues: a hydration failure degrades blocker checks
			// for this recomputation but should not make a completed launch fail.
			_ = gh.HydrateBodyLabels(issue)
		},
		func(num int) string {
			stateName, _ := gh.IssueState(num)
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
