package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func newTUIWatcher(projectRoot, session, commandName string, resolvedSettings settings.Settings, hookConfig hooks.Config) (fanouttui.WatcherRunner, time.Duration, string, error) {
	if !resolvedSettings.Watcher {
		return nil, 0, "", nil
	}
	gh := ghissue.Runner{Cwd: projectRoot}
	if err := gh.EnsureLabel(resolvedSettings.WatcherRunningLabel); err != nil {
		return nil, 0, "", fmt.Errorf("ensure running label %q: %w", resolvedSettings.WatcherRunningLabel, err)
	}
	livePanes := &watchLivePaneCache{}
	io := watch.IO{
		ListLabeled: gh.ListOpenIssuesWithLabel,
		CountChildren: func(issue ghissue.Issue) (watch.ChildCounts, error) {
			return countWatchChildTargets(projectRoot, gh, issue.Number)
		},
		SwapLabels: func(issue ghissue.Issue, removeLabel, addLabel string) error {
			return gh.SwapIssueLabels(issue.Number, removeLabel, addLabel)
		},
		LoadState: func() (state.Store, error) {
			return state.LoadProject(projectRoot)
		},
		PaneAlive: livePanes.Alive,
		LaunchStandalone: func(issue ghissue.Issue) error {
			return launchWatchStandalone(projectRoot, session, commandName, resolvedSettings, hookConfig, issue)
		},
		LaunchParent: func(issue ghissue.Issue, limit int) (watch.ParentLaunchResult, error) {
			return launchWatchParent(projectRoot, session, commandName, resolvedSettings, issue, limit)
		},
	}
	cfg := watch.Config{
		TriggerLabel: resolvedSettings.WatcherTriggerLabel,
		RunningLabel: resolvedSettings.WatcherRunningLabel,
		MaxSessions:  resolvedSettings.WatcherMaxSessions,
	}
	interval := time.Duration(resolvedSettings.WatcherIntervalSeconds) * time.Second
	return &tuiWatcher{engine: watch.NewEngine(cfg, io), livePanes: livePanes}, interval, resolvedSettings.WatcherTriggerLabel, nil
}

type tuiWatcher struct {
	engine    *watch.Engine
	livePanes *watchLivePaneCache
}

func (w *tuiWatcher) RunCycle() (watch.Report, error) {
	if w == nil || w.engine == nil {
		return watch.Report{}, fmt.Errorf("watcher is nil")
	}
	w.livePanes.Reset()
	return w.engine.RunCycle()
}

func launchWatchStandalone(projectRoot, session, commandName string, resolvedSettings settings.Settings, hookConfig hooks.Config, issue ghissue.Issue) error {
	cfg := newWatchLaunchConfig(resolvedSettings, issue.Number, 0)
	return launchStandaloneIssuePane(projectRoot, session, commandName, cfg, resolvedSettings, hookConfig, issue)
}

// launchStandaloneIssuePane creates one pane for a single issue with no OPEN
// children. The watcher and the TUI issue launcher share it; cfg carries the
// caller's agent selection.
func launchStandaloneIssuePane(projectRoot, session, commandName string, cfg *cliflags.Config, resolvedSettings settings.Settings, hookConfig hooks.Config, issue ghissue.Issue) error {
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	store, recorder, code := loadRunState(cfg, projectRoot, launchLogger)
	if code != exitcode.OK {
		return bufferedLaunchError(stdout, stderr, "load fanout state")
	}
	if recorder != nil {
		defer func() {
			_ = recorder.Unlock()
		}()
	}
	if hasRecordedIssuePane(store, issue.Number) {
		return watch.ErrAlreadyFanned
	}
	info := &fanoutruntime.Info{
		Session:     session,
		Target:      tuiLaunchTarget(session),
		ProjectRoot: projectRoot,
	}
	req := panelaunch.NewWatchRequest(cfg, projectRoot, issue, resolvedSettings, hookConfig)
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: info, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
	if !launcher.LaunchOK(req) {
		return bufferedLaunchError(stdout, stderr, "create watch pane")
	}
	return nil
}

func launchWatchParent(projectRoot, session, commandName string, resolvedSettings settings.Settings, issue ghissue.Issue, limit int) (watch.ParentLaunchResult, error) {
	cfg := newWatchLaunchConfig(resolvedSettings, issue.Number, limit)
	return launchParentIssueFanout(projectRoot, session, commandName, cfg)
}

// launchParentIssueFanout runs the full issue-mode fan-out for cfg.Parent
// against a synthesized runtime targeting the TUI session. The watcher and
// the TUI issue launcher share it.
func launchParentIssueFanout(projectRoot, session, commandName string, cfg *cliflags.Config) (watch.ParentLaunchResult, error) {
	gh := ghissue.Runner{Cwd: projectRoot}
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	rt := &runtimeInfo{
		info: &fanoutruntime.Info{
			Session:     session,
			Target:      tuiLaunchTarget(session),
			ProjectRoot: projectRoot,
		},
		gh: gh,
	}
	if code := runWithRuntime(cfg, launchLogger, rt, commandName); code != exitcode.OK {
		return watch.ParentLaunchResult{}, bufferedLaunchError(stdout, stderr, "launch parent")
	}
	return watchParentResultAfterLaunch(projectRoot, cfg, gh), nil
}

func newWatchLaunchConfig(resolvedSettings settings.Settings, parent, limit int) *cliflags.Config {
	return &cliflags.Config{
		Parent:          parent,
		ParentRef:       strconv.Itoa(parent),
		ParentMode:      cliflags.ModeIssue,
		Agent:           watcherAgent(resolvedSettings),
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
	return len(openIssues(loaded.Children)), nil
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
	list   func() ([]tmuxrun.LivePane, error)
	loaded bool
	panes  []tmuxrun.LivePane
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
	for _, live := range panes {
		if watchPaneMatchesLive(pane, live) {
			return true, nil
		}
	}
	return false, nil
}

func (c *watchLivePaneCache) load() ([]tmuxrun.LivePane, error) {
	if c == nil {
		return tmuxrun.ListLivePanes()
	}
	if !c.loaded {
		list := c.list
		if list == nil {
			list = tmuxrun.ListLivePanes
		}
		c.panes, c.err = list()
		c.loaded = true
	}
	return c.panes, c.err
}

func watchPaneMatchesLive(pane state.Pane, live tmuxrun.LivePane) bool {
	if pane.PaneID != live.ID {
		return false
	}
	if pane.IsShell() {
		return pane.ShellKey != "" && pane.ShellKey == live.ShellKey
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

func buildWatchParentPlan(projectRoot string, cfg *cliflags.Config, gh ghissue.Runner) (Plan, error) {
	loaded, err := loadWatchParentChildren(gh, cfg.Parent)
	if err != nil {
		return Plan{}, err
	}
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return Plan{}, fmt.Errorf("load fanout state: %w", err)
	}
	sameParentFanned := store.FannedNumbersForParent(cfg.ParentRef)
	otherParentFanned := store.FannedNumbersForOtherParents(cfg.ParentRef)
	worktreeFallbackFanned := existingWorktreeFanned(cfg, projectRoot, loaded.Children, otherParentFanned)
	return buildPlan(
		cfg,
		loaded.Children,
		mergeFanned(sameParentFanned, worktreeFallbackFanned),
		loaded.ParentBody,
		func(issue *ghissue.Issue) {
			// Match runWithRuntime: a hydration failure degrades blocker checks
			// for this recomputation but should not make a completed launch fail.
			_ = gh.HydrateBodyLabels(issue)
		},
		func(num int) string {
			stateName, _ := gh.IssueState(num)
			return stateName
		},
	), nil
}

func loadWatchParentChildren(gh ghissue.Runner, parent int) (childLoadResult, error) {
	var stdout, stderr bytes.Buffer
	lg := log.NewWith(&stdout, &stderr, false)
	cfg := newWatchPlanConfig(parent, 0)
	lg.Info("fetching sub-issues of #%d", cfg.Parent)
	subIssues, err := gh.SubIssueList(cfg.Parent)
	if err != nil {
		lg.Err("sub-issues fetch failed: %v", err)
		return childLoadResult{}, bufferedLaunchError(stdout, stderr, "load child issues")
	}
	parentBody, err := gh.ParentBody(cfg.Parent)
	if err != nil {
		lg.Err("parent body fetch failed: %v", err)
		return childLoadResult{}, bufferedLaunchError(stdout, stderr, "load child issues")
	}
	bodyNums := ghissue.TaskListNumbers(parentBody)
	loaded, added := mergeWatchExtraChildren(cfg, gh, subIssues, bodyNums, lg)
	assignTaskListWaves(loaded, ghissue.TaskListWaves(parentBody))
	if added > 0 {
		lg.Info("parent body added %d extra child reference(s) not in sub-issue API", added)
	}
	return childLoadResult{
		Children:       loaded,
		ParentBody:     parentBody,
		StrongChildren: issueSet(subIssues),
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

func hasRecordedIssuePane(store state.Store, issueNum int) bool {
	for _, pane := range store.Panes {
		if pane.IssueNum == issueNum {
			return true
		}
	}
	return false
}
