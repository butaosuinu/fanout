package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/log"
	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
	"github.com/butaosuinu/fanout/internal/watch"
	"github.com/butaosuinu/fanout/internal/worktree"
)

const tuiPaneTitle = "fanout tui"

func isTUIRequest(args []string) bool {
	return len(args) == 0
}

func cmdTUI(commandName string, lg *log.Logger) exitcode.Code {
	projectRoot, err := tuiProjectRoot()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}

	if !tmuxrun.InsideTmux() {
		return enterTUISession(projectRoot, commandName, lg)
	}

	session, err := tmuxrun.CurrentSession()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
	hookConfig := hooks.LoadUserConfig(lg)
	watcher, watchInterval, watchLabel, err := newTUIWatcher(projectRoot, session, commandName, resolvedSettings, hookConfig)
	if err != nil {
		lg.Err("watcher: %v", err)
		return exitcode.Env
	}
	notifier, err := fanoutnotify.New(fanoutnotify.Config{
		Channels:        resolvedSettings.Notifications,
		TmuxTarget:      session,
		NtfyURL:         resolvedSettings.NtfyURL,
		SlackWebhookURL: resolvedSettings.SlackWebhookURL,
		BellWriter:      os.Stdout,
	})
	if err != nil {
		lg.Err("notifications: %v", err)
		return exitcode.Env
	}
	restoreTitle := markTUIRunning(projectRoot)
	defer restoreTitle()
	if err := fanouttui.Run(fanouttui.Options{
		ProjectRoot:         projectRoot,
		Session:             session,
		StateInterval:       2 * time.Second,
		GHInterval:          20 * time.Second,
		Watcher:             watcher,
		WatchInterval:       watchInterval,
		WatchLabel:          watchLabel,
		DefaultAgent:        defaultTUIAgent(),
		WatcherRunningLabel: resolvedSettings.WatcherRunningLabel,
		Hooks:               hookConfig,
		LaunchPane:          newTUILaunchPaneFunc(projectRoot, session, commandName, hookConfig),
		LaunchShell:         newTUILaunchShellFunc(projectRoot, session),
		Notifier:            notifier,
	}); err != nil {
		lg.Err("tui: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

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
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	cfg := newWatchLaunchConfig(resolvedSettings, issue.Number, 0)
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
	req := newWatchPaneRequest(cfg, projectRoot, issue, resolvedSettings, hookConfig)
	if !createPane(cfg, launchLogger, info, req, recorder, log.Palette{}, commandName) {
		return bufferedLaunchError(stdout, stderr, "create watch pane")
	}
	return nil
}

func launchWatchParent(projectRoot, session, commandName string, resolvedSettings settings.Settings, issue ghissue.Issue, limit int) (watch.ParentLaunchResult, error) {
	gh := ghissue.Runner{Cwd: projectRoot}
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	cfg := newWatchLaunchConfig(resolvedSettings, issue.Number, limit)
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

func newTUILaunchPaneFunc(projectRoot, session, commandName string, hookConfig hooks.Config) fanouttui.LaunchFunc {
	return func(req fanouttui.LaunchRequest) (string, error) {
		return launchManualPaneFromTUI(projectRoot, session, commandName, hookConfig, req)
	}
}

func launchManualPaneFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, req fanouttui.LaunchRequest) (string, error) {
	prompt := normalizeTUIPrompt(req.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	agentNames := normalizeTUIAgents(req.Agents)
	for _, agentName := range agentNames {
		if err := agent.ValidateKnown(agentName); err != nil {
			return "", err
		}
		if err := agent.ValidateInstalled(agentName); err != nil {
			return "", err
		}
	}
	slug, err := normalizeTUISlug(req.Slug)
	if err != nil {
		return "", err
	}

	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	cfg := manualPaneConfigForTUIAgent(agentNames[0])
	_, recorder, code := loadRunState(cfg, projectRoot, launchLogger)
	if code != exitcode.OK {
		return "", bufferedLaunchError(stdout, stderr, "load fanout state")
	}
	if recorder != nil {
		defer func() {
			_ = recorder.Unlock()
		}()
	}

	info := &fanoutruntime.Info{
		Session:     session,
		Target:      tuiLaunchTarget(session),
		ProjectRoot: projectRoot,
	}
	createdCount := 0
	for i, agentName := range agentNames {
		cfg = manualPaneConfigForTUIAgent(agentName)
		paneSlug := manualPaneSlugForAgent(slug, agentName, i, agentNames)
		paneReq := newManualPaneRequest(cfg, projectRoot, recorder.Store, hookConfig, manualPaneOptionsForTUI(prompt, paneSlug, agentName))
		if createPane(cfg, launchLogger, info, paneReq, recorder, log.Palette{}, commandName) {
			createdCount++
			continue
		}
		if createdCount > 0 {
			return partialManualLaunchNotice(createdCount, stderr), nil
		}
		return "", bufferedLaunchError(stdout, stderr, "create pane")
	}
	return bufferedLaunchNotice(stderr), nil
}

func partialManualLaunchNotice(createdCount int, stderr bytes.Buffer) string {
	notice := fmt.Sprintf("created %d new agent pane(s); stopped after a later pane failed", createdCount)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		return notice + ": " + compactLaunchError(s)
	}
	return notice
}

func compactLaunchError(s string) string {
	lines := strings.Split(s, "\n")
	for _, raw := range slices.Backward(lines) {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[err ]") {
			return line
		}
	}
	if len(s) > 180 {
		return s[:180] + "..."
	}
	return s
}

// bufferedLaunchNotice extracts the tolerated base-refresh skip line, if any,
// from a successful launch's buffered log so the TUI can show it on success.
func bufferedLaunchNotice(stderr bytes.Buffer) string {
	for line := range strings.SplitSeq(stderr.String(), "\n") {
		if i := strings.Index(line, baseRefreshSkippedNotice); i >= 0 {
			return strings.TrimSpace(line[i:])
		}
	}
	return ""
}

func manualPaneConfigForTUIAgent(agentName string) *cliflags.Config {
	cfg := &cliflags.Config{Agent: agentName}
	if agentName == "codex" {
		codexPlanMode := true
		cfg.CodexPlanMode = &codexPlanMode
	}
	return cfg
}

func newTUILaunchShellFunc(projectRoot, session string) fanouttui.ShellLaunchFunc {
	return func(req fanouttui.ShellLaunchRequest) error {
		return launchShellPaneFromTUI(projectRoot, session, req)
	}
}

func launchShellPaneFromTUI(projectRoot, session string, req fanouttui.ShellLaunchRequest) error {
	rawPath := strings.TrimSpace(req.TargetPath)
	if rawPath == "" {
		return fmt.Errorf("terminal path is required")
	}
	targetPath, err := filepath.Abs(rawPath)
	if err != nil {
		return fmt.Errorf("resolve terminal path: %w", err)
	}
	st, statErr := os.Stat(targetPath)
	if statErr != nil {
		return fmt.Errorf("terminal path: %w", statErr)
	} else if !st.IsDir() {
		return fmt.Errorf("terminal path is not a directory: %s", targetPath)
	}

	if excludeErr := worktree.EnsureLocalExclude(projectRoot); excludeErr != nil {
		return fmt.Errorf("prepare local git exclude: %w", excludeErr)
	}

	recorder, err := state.LockProject(projectRoot)
	if err != nil {
		return err
	}
	defer func() {
		_ = recorder.Unlock()
	}()

	number := nextSyntheticPaneNumber(recorder.Store, manualPaneParentRef)
	slug := shellPaneSlug(targetPath, req.Root, number)
	title := shellPaneTitle(targetPath, req.Root)
	shellKey, err := newShellPaneKey()
	if err != nil {
		return err
	}
	paneID, err := tmuxrun.SplitPane(tuiLaunchTarget(session), targetPath)
	if err != nil {
		return err
	}
	if err := tmuxrun.SetPaneShellKey(paneID, shellKey); err != nil {
		_ = tmuxrun.KillPane(paneID)
		return err
	}
	// Shell pane ergonomics are best-effort; the recorded pane id is enough to
	// keep the terminal usable when tmux metadata/layout updates fail.
	_ = tmuxrun.SetPaneTitle(paneID, title)
	_ = tmuxrun.SetPaneProjectRoot(paneID, projectRoot)
	_ = tmuxrun.SelectTiled(tuiLaunchTarget(session))
	if err := recorder.RecordPane(state.Pane{
		Parent:       manualPaneParentRef,
		IssueNum:     number,
		Kind:         state.PaneKindShell,
		Slug:         slug,
		PaneID:       paneID,
		ShellKey:     shellKey,
		Agent:        state.PaneKindShell,
		DisplayName:  title,
		WorktreePath: targetPath,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		_ = tmuxrun.KillPane(paneID)
		return fmt.Errorf("write fanout state: %w", err)
	}
	return nil
}

func newShellPaneKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate terminal identity: %w", err)
	}
	return "shell-" + hex.EncodeToString(b[:]), nil
}

func shellPaneSlug(targetPath string, root bool, number int) string {
	base := "root"
	if !root {
		base = sanitizeSessionPart(filepath.Base(targetPath))
	}
	if base == "" {
		base = "terminal"
	}
	n := number
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("terminal-%s-%d", base, n)
}

func shellPaneTitle(targetPath string, root bool) string {
	if root {
		return "root terminal"
	}
	base := strings.TrimSpace(filepath.Base(targetPath))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "worktree"
	}
	return "terminal " + base
}

func manualPaneOptionsForTUI(prompt, slug, agentName string) manualPaneOptions {
	title := firstPromptLine(prompt)
	opts := manualPaneOptions{
		Title:  title,
		Slug:   slug,
		Agent:  agentName,
		Prompt: title,
	}
	if strings.Contains(prompt, "\n") {
		opts.Body = prompt
	}
	return opts
}

func normalizeTUIAgents(raw []string) []string {
	var agents []string
	for _, agentName := range raw {
		agentName = strings.TrimSpace(agentName)
		if agentName != "" {
			agents = append(agents, agentName)
		}
	}
	if len(agents) == 0 {
		return []string{defaultTUIAgent()}
	}
	return agents
}

func manualPaneSlugForAgent(slug, agentName string, index int, agents []string) string {
	if slug == "" || len(agents) == 1 {
		return slug
	}
	suffix := agentName
	seen := 0
	totalForAgent := 0
	for i, name := range agents {
		if name != agentName {
			continue
		}
		totalForAgent++
		if i <= index {
			seen++
		}
	}
	if totalForAgent > 1 {
		suffix = fmt.Sprintf("%s-%s", agentName, launchOrdinal(seen))
	}
	return strings.TrimRight(slug, "-") + "-" + suffix
}

func launchOrdinal(n int) string {
	switch n {
	case 1:
		return "one"
	case 2:
		return "two"
	case 3:
		return "three"
	default:
		return fmt.Sprintf("run%d", n)
	}
}

func normalizeTUIPrompt(raw string) string {
	prompt := strings.ReplaceAll(raw, "\r\n", "\n")
	prompt = strings.ReplaceAll(prompt, "\r", "\n")
	return strings.TrimSpace(prompt)
}

func firstPromptLine(prompt string) string {
	for line := range strings.Lines(prompt) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(prompt)
}

func tuiLaunchTarget(session string) string {
	if pane := strings.TrimSpace(os.Getenv("TMUX_PANE")); pane != "" {
		return pane
	}
	return session
}

func defaultTUIAgent() string {
	return tuiAgentOrDefault(os.Getenv("FANOUT_AGENT"))
}

func tuiAgentOrDefault(agentName string) string {
	if strings.TrimSpace(agentName) == "codex" {
		return "codex"
	}
	return "claude"
}

func normalizeTUISlug(raw string) (string, error) {
	slug := strings.TrimSpace(raw)
	if slug == "" {
		return "", nil
	}
	if !isKebabSlug(slug) {
		return "", fmt.Errorf("slug must be lowercase kebab-case (alnum+hyphens, starting with alnum), got: %q", slug)
	}
	if hasIssueLikeNumericSuffix(slug) {
		return "", fmt.Errorf("manual slug must not end with an issue-like numeric suffix: %q", slug)
	}
	return slug, nil
}

func isKebabSlug(slug string) bool {
	for i, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return slug != ""
}

func hasIssueLikeNumericSuffix(slug string) bool {
	i := strings.LastIndex(slug, "-")
	if i < 0 || i == len(slug)-1 {
		return false
	}
	for _, r := range slug[i+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func bufferedLaunchError(stdout, stderr bytes.Buffer, fallback string) error {
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = strings.TrimSpace(stdout.String())
	}
	if msg == "" {
		msg = fallback
	}
	return fmt.Errorf("%s", msg)
}

func enterTUISession(projectRoot, commandName string, lg *log.Logger) exitcode.Code {
	session := fanoutTUISessionName(projectRoot)
	created := false
	if !tmuxrun.HasSession(session) {
		if err := tmuxrun.NewSession(session, projectRoot); err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
		created = true
	}

	pane, running, err := findTUIPane(session)
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if !running {
		if created {
			pane, err = firstSessionPane(session)
		} else {
			pane, err = tmuxrun.NewWindow(session, tuiPaneTitle, projectRoot)
		}
		if err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
		if err := tmuxrun.SendKeys(pane.ID, tuiLaunchCommand(commandName, projectRoot), "Enter"); err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
	}
	if err := tmuxrun.FocusPane(pane); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if err := tmuxrun.AttachOrSwitch(session); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	return exitcode.OK
}

func markTUIRunning(projectRoot string) func() {
	paneID := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if paneID == "" {
		return func() {}
	}
	_ = tmuxrun.SetPaneProjectRoot(paneID, projectRoot) // Best-effort dashboard keybinding hint.
	originalTitle, err := tmuxrun.PaneTitle(paneID)
	if err != nil {
		originalTitle = "fanout"
	}
	_ = tmuxrun.SetPaneTitle(paneID, tuiPaneTitle)
	return func() {
		_ = tmuxrun.SetPaneTitle(paneID, originalTitle)
	}
}

func findTUIPane(session string) (tmuxrun.PaneInfo, bool, error) {
	panes, err := tmuxrun.ListPanes(session)
	if err != nil {
		return tmuxrun.PaneInfo{}, false, err
	}
	for _, pane := range panes {
		if pane.Title == tuiPaneTitle {
			return pane, true, nil
		}
	}
	return tmuxrun.PaneInfo{}, false, nil
}

func firstSessionPane(session string) (tmuxrun.PaneInfo, error) {
	panes, err := tmuxrun.ListPanes(session)
	if err != nil {
		return tmuxrun.PaneInfo{}, err
	}
	if len(panes) == 0 {
		return tmuxrun.PaneInfo{}, fmt.Errorf("tmux session %s has no panes", session)
	}
	return panes[0], nil
}

func tuiProjectRoot() (string, error) {
	return resolveDisplayProjectRoot()
}

func fanoutTUISessionName(projectRoot string) string {
	sum := sha1.Sum([]byte(projectRoot))
	base := sanitizeSessionPart(filepath.Base(projectRoot))
	return "fanout-" + base + "-" + hex.EncodeToString(sum[:])[:8]
}

func sanitizeSessionPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "repo"
	}
	return out
}

func tuiLaunchCommand(commandName, projectRoot string) string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = commandName
	}
	return "cd " + shellQuote(projectRoot) + " && " + shellQuote(exe)
}
