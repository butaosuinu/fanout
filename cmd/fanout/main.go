package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/naming"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/worktree"
)

const fanoutTagPrefix = "[fanout #"

var (
	version            = "dev"
	commit             = "none"
	sleepBetweenIssues = time.Sleep
)

func main() {
	lg := log.New(false)
	commandName := invokedCommandName(os.Args)

	if isVersionRequest(os.Args[1:]) {
		fmt.Fprintln(os.Stdout, versionLine())
		os.Exit(int(exitcode.OK))
	}
	if isUpdateRequest(os.Args[1:]) {
		os.Exit(int(cmdUpdate(os.Args[2:], version, ghissue.Runner{}, lg)))
	}
	if isCheckUpdateRequest(os.Args[1:]) {
		os.Exit(int(cmdCheckUpdate(version, ghissue.Runner{}, lg)))
	}
	if isTUIRequest(os.Args[1:]) {
		os.Exit(int(cmdTUI(commandName, lg)))
	}
	if isCodexPlanTUIRequest(os.Args[1:]) {
		os.Exit(int(cmdCodexPlanTUI(os.Args[2:], lg)))
	}
	if isDashboardRequest(os.Args[1:]) {
		os.Exit(int(cmdDashboard(os.Args[2:], lg)))
	}

	pr := cliflags.Parse(os.Args[1:], lg, os.Stdout)
	if pr.Code != exitcode.OK || pr.Config == nil {
		os.Exit(int(pr.Code))
	}
	cfg := pr.Config
	if cfg.Debug {
		lg = log.New(true)
	}

	if missing := checkDeps(cfg); len(missing) > 0 {
		lg.Err("missing dependencies:")
		for _, d := range missing {
			fmt.Fprintf(lg.Stderr(), "  - %s\n", d)
		}
		os.Exit(int(exitcode.Env))
	}

	if cfg.StatusMode {
		os.Exit(int(cmdStatus(cfg, lg)))
	}
	if cfg.CloseNum > 0 {
		os.Exit(int(cmdClose(cfg, lg)))
	}
	if cfg.MergeNum > 0 {
		os.Exit(int(cmdMerge(cfg, lg)))
	}
	if cfg.CleanupMode {
		os.Exit(int(cmdCleanup(cfg, lg)))
	}

	os.Exit(int(run(cfg, lg, commandName)))
}

func isVersionRequest(args []string) bool {
	return len(args) == 1 && (args[0] == "--version" || args[0] == "-V")
}

func isCheckUpdateRequest(args []string) bool {
	return len(args) == 1 && (args[0] == "--check-update" || args[0] == "check-update")
}

func isUpdateRequest(args []string) bool {
	return len(args) > 0 && args[0] == "update"
}

func versionLine() string {
	return fmt.Sprintf("fanout %s (%s)", version, commit)
}

func invokedCommandName(args []string) string {
	if len(args) == 0 || args[0] == "" {
		return "fanout"
	}
	name := filepath.Base(args[0])
	if name == "." || name == string(os.PathSeparator) {
		return "fanout"
	}
	return name
}

func run(cfg *cliflags.Config, lg *log.Logger, commandName string) exitcode.Code {
	rt, code := resolveRuntime(cfg, lg)
	if code != exitcode.OK {
		return code
	}
	resolvedSettings := settings.Resolve(rt.info.ProjectRoot, settings.CLIOverrides{
		AutoPullRequest:    cfg.AutoPullRequest,
		PRReviewGate:       cfg.PRReviewGate,
		BriefingCodeReview: cfg.BriefingCodeReview,
		AgentTeamsHint:     cfg.AgentTeamsHint,
		PRVisualization:    cfg.PRVisualization,
		DashboardKeybind:   cfg.DashboardKeybind,
	}, lg.Warn)

	loaded, code := loadChildren(cfg, rt.gh, lg)
	if code != exitcode.OK {
		return code
	}

	totalChildren := len(loaded.Children)
	if totalChildren == 0 {
		if cfg.ParentMode == cliflags.ModeProject {
			lg.Info("no items in Project (after status/repo filter). nothing to do.")
		} else {
			lg.Info("no sub-issues on #%d. nothing to do.", cfg.Parent)
		}
		return exitcode.OK
	}

	openCount := len(openIssues(loaded.Children))
	lg.Info("%s: %d total, %d OPEN", loaded.ChildNoun, totalChildren, openCount)
	if openCount == 0 {
		lg.Info("no OPEN %s. nothing to do.", loaded.ChildNoun)
		return exitcode.OK
	}

	store, recorder, code := loadRunState(cfg, rt.info.ProjectRoot, lg)
	if code != exitcode.OK {
		return code
	}
	if recorder != nil {
		defer func() {
			if err := recorder.Unlock(); err != nil {
				lg.Warn("unlock fanout state: %v", err)
			}
		}()
	}

	sameParentFanned := store.FannedNumbersForParent(cfg.ParentRef)
	otherParentFanned := store.FannedNumbersForOtherParents(cfg.ParentRef)
	worktreeFallbackFanned := existingWorktreeFanned(cfg, rt.info.ProjectRoot, loaded.Children, otherParentFanned)

	plan := buildPlan(
		cfg,
		loaded.Children,
		mergeFanned(sameParentFanned, worktreeFallbackFanned),
		loaded.ParentBody,
		func(issue *ghissue.Issue) {
			if err := rt.gh.HydrateBodyLabels(issue); err != nil {
				lg.Warn("#%d: could not fetch body/labels for blocker check; treating as unblocked", issue.Number)
			}
		},
		func(num int) string {
			state, _ := rt.gh.IssueState(num)
			return state
		},
	)
	logPlanDetails(plan, lg)

	if plan.OpenAfterFilter == 0 {
		lg.Info("all OPEN sub-issues filtered out by --only/--skip. nothing to do.")
		return exitcode.OK
	}
	if plan.UnfannedCount == 0 {
		lg.Ok("all %d OPEN sub-issue(s) already have a fanout pane. nothing to do.", len(plan.AlreadyFanned))
		return exitcode.OK
	}

	logAlreadyFanned(plan.AlreadyFanned, lg)
	lg.Info("will create %d pane(s); deferred (blocked): %d; deferred (--limit): %d",
		len(plan.Targets), len(plan.BlockedRows), len(plan.LimitDeferred))

	c := lg.Colors()
	if cfg.DryRun {
		printDryRunPlan(plan, lg, c)
	}

	result := executePlan(cfg, lg, rt.info, rt.gh, plan.Targets, resolvedSettings, recorder, otherParentFanned, c, commandName)
	printSummary(plan, result, cfg, lg, c, commandName)

	// Register the tmux keybinding so the user can pop the read-only dashboard
	// (prefix + D) from any fanout pane. The binding resolves the repo from the
	// pressing pane at keypress, so it works from child worktree panes and across
	// repos. Best-effort, live runs only.
	if !cfg.DryRun && result.Created > 0 {
		bindDashboardKey(lg, resolvedSettings.DashboardKeybind)
	}

	if result.Failed > 0 {
		return exitcode.Env
	}
	return exitcode.OK
}

type runtimeInfo struct {
	info *fanoutruntime.Info
	gh   ghissue.Runner
}

func resolveRuntime(cfg *cliflags.Config, lg *log.Logger) (*runtimeInfo, exitcode.Code) {
	info, err := fanoutruntime.Resolve(cfg.Session)
	if err != nil {
		lg.Err("%s", err.Error())
		return nil, exitcode.Env
	}

	if cfg.Agent == "" {
		cfg.Agent = os.Getenv("FANOUT_AGENT")
	}
	if cfg.Agent == "" {
		lg.Err("agent is required; pass --agent <name> or set FANOUT_AGENT")
		return nil, exitcode.Env
	}
	if err := agent.ValidateKnown(cfg.Agent); err != nil {
		lg.Err("%s", err.Error())
		return nil, exitcode.Env
	}
	if cfg.CodexPlanModeEnabled() && cfg.Agent != "codex" {
		lg.Err("--codex-plan-mode requires --agent codex")
		return nil, exitcode.Env
	}
	if !cfg.DryRun {
		if err := agent.ValidateInstalled(cfg.Agent); err != nil {
			lg.Err("%s", err.Error())
			return nil, exitcode.Env
		}
	}

	lg.Info("tmux session: %s", info.Session)
	lg.Info("tmux target:  %s", info.Target)
	lg.Info("project root: %s", info.ProjectRoot)

	if !isGitWorkTree(info.ProjectRoot) {
		lg.Err("project root %s is not a git work tree; cannot resolve GitHub repo", info.ProjectRoot)
		return nil, exitcode.Env
	}
	return &runtimeInfo{
		info: info,
		gh:   ghissue.Runner{Cwd: info.ProjectRoot},
	}, exitcode.OK
}

type childLoadResult struct {
	Children       []ghissue.Issue
	ParentBody     string
	StrongChildren map[int]bool
	ChildNoun      string
}

func loadChildren(cfg *cliflags.Config, gh ghissue.Runner, lg *log.Logger) (childLoadResult, exitcode.Code) {
	if cfg.ParentMode == cliflags.ModeProject {
		return loadProjectChildren(cfg, gh, lg)
	}
	return loadIssueChildren(cfg, gh, lg)
}

func loadIssueChildren(cfg *cliflags.Config, gh ghissue.Runner, lg *log.Logger) (childLoadResult, exitcode.Code) {
	lg.Info("fetching sub-issues of #%d", cfg.Parent)
	subIssues, err := gh.SubIssueList(cfg.Parent)
	if err != nil {
		lg.Err("gh sub-issue list failed: %v", err)
		return childLoadResult{}, exitcode.Env
	}

	parentBody, err := gh.ParentBody(cfg.Parent)
	if err != nil {
		lg.Warn("could not read parent body (#%d); skipping task-list scan", cfg.Parent)
		parentBody = ""
	}

	bodyNums := ghissue.TaskListNumbers(parentBody)
	loaded, added := mergeExtraChildren(cfg, gh, subIssues, bodyNums, true, lg)
	assignTaskListWaves(loaded, ghissue.TaskListWaves(parentBody))
	if added > 0 {
		lg.Info("parent body / --include added %d extra child reference(s) not in sub-issue API", added)
	}

	return childLoadResult{
		Children:       loaded,
		ParentBody:     parentBody,
		StrongChildren: issueSet(subIssues),
		ChildNoun:      "sub-issues",
	}, exitcode.OK
}

func loadProjectChildren(cfg *cliflags.Config, gh ghissue.Runner, lg *log.Logger) (childLoadResult, exitcode.Code) {
	repo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("could not resolve repo via 'gh repo view' in project root (required for project mode cross-repo filter)")
		return childLoadResult{}, exitcode.Env
	}
	lg.Info("mode: project (status=%s, repo=%s)", cfg.ProjectStatus, repo)
	lg.Info("fetching items from Project %s", cfg.ParentRef)

	res, err := gh.ProjectItems(cfg.ProjectOwnerType, cfg.ProjectOwner, cfg.ProjectNumber, repo, cfg.ProjectStatus)
	if err != nil {
		lg.Err("%v", err)
		return childLoadResult{}, exitcode.Env
	}
	if res.MissingStatus && cfg.ProjectStatus != "all" {
		lg.Warn("Project '%s' has no Status field; ignoring --project-status %s and falling back to all items", res.ProjectTitle, cfg.ProjectStatus)
		cfg.ProjectStatus = "all"
	}
	for _, cross := range res.CrossRepoWarnings {
		lg.Warn("skipping cross-repo project item: %s (project root repo: %s)", cross, repo)
	}

	loaded, added := mergeExtraChildren(cfg, gh, res.Issues, nil, false, lg)
	if added > 0 {
		lg.Info("--include added %d extra child reference(s) not in project items", added)
	}

	return childLoadResult{
		Children:       loaded,
		StrongChildren: issueSet(res.Issues),
		ChildNoun:      "project items",
	}, exitcode.OK
}

func mergeExtraChildren(cfg *cliflags.Config, gh ghissue.Runner, base []ghissue.Issue, bodyNums []int, skipParent bool, lg *log.Logger) ([]ghissue.Issue, int) {
	existing := map[int]bool{}
	if skipParent {
		existing[cfg.Parent] = true
	}
	for _, s := range base {
		existing[s.Number] = true
	}

	extraNums := append([]int{}, bodyNums...)
	extraNums = append(extraNums, cfg.Include...)
	var extra []ghissue.Issue
	for _, num := range extraNums {
		if existing[num] {
			continue
		}
		detail, err := gh.IssueDetail(num)
		if err != nil {
			lg.Warn("parent body / --include references #%d but issue lookup failed; skipping", num)
			continue
		}
		extra = append(extra, detail)
		existing[num] = true
	}
	return ghissue.MergeExtra(base, extra), len(extra)
}

func assignTaskListWaves(issues []ghissue.Issue, waves map[int]string) {
	for i := range issues {
		if wave := waves[issues[i].Number]; wave != "" {
			issues[i].Wave = wave
		}
	}
}

func issueSet(issues []ghissue.Issue) map[int]bool {
	out := map[int]bool{}
	for _, issue := range issues {
		out[issue.Number] = true
	}
	return out
}

func mergeFanned(primary, fallback map[int]bool) map[int]bool {
	out := map[int]bool{}
	for num := range primary {
		out[num] = true
	}
	for num := range fallback {
		out[num] = true
	}
	return out
}

func loadRunState(cfg *cliflags.Config, projectRoot string, lg *log.Logger) (state.Store, *state.LockedStore, exitcode.Code) {
	if cfg.DryRun {
		store, err := state.LoadProject(projectRoot)
		if err != nil {
			lg.Err("%v", err)
			return state.Store{}, nil, exitcode.Env
		}
		return store, nil, exitcode.OK
	}
	if err := worktree.EnsureLocalExclude(projectRoot); err != nil {
		lg.Err("prepare local git exclude: %v", err)
		return state.Store{}, nil, exitcode.Env
	}
	locked, err := state.LockProject(projectRoot)
	if err != nil {
		lg.Err("%v", err)
		return state.Store{}, nil, exitcode.Env
	}
	return locked.Store, locked, exitcode.OK
}

func existingWorktreeFanned(cfg *cliflags.Config, projectRoot string, issues []ghissue.Issue, sharedAcrossParents map[int]bool) map[int]bool {
	out := map[int]bool{}
	worktreeNames := existingWorktreeNames(filepath.Join(projectRoot, ".fanout", "worktrees"))
	for _, issue := range issues {
		slug := naming.Slug(issue.Title, issue.Number)
		slugOverridden := false
		if name := cfg.FindName(issue.Number); name != nil && name.SlugHint != "" {
			slug = naming.EnsureIssueSuffix(name.SlugHint, issue.Number)
			slugOverridden = true
		}
		if sharedAcrossParents[issue.Number] {
			if !slugOverridden {
				slug = naming.QualifySlugForParent(slug, cfg.ParentRef, issue.Number)
			}
			if worktreeNameMatchesExact(worktreeNames, slug) {
				out[issue.Number] = true
			}
			continue
		}
		if worktreeNameMatchesIssue(worktreeNames, slug, issue.Number) {
			out[issue.Number] = true
		}
	}
	return out
}

func existingWorktreeNames(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

func worktreeNameMatchesExact(names []string, slug string) bool {
	return slices.Contains(names, slug)
}

func worktreeNameMatchesIssue(names []string, exactSlug string, issueNum int) bool {
	issueSuffix := fmt.Sprintf("-%d", issueNum)
	for _, name := range names {
		if name == exactSlug || strings.HasSuffix(name, issueSuffix) {
			return true
		}
	}
	return false
}

func executePlan(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, gh ghissue.Runner, targets []ghissue.Issue, resolvedSettings settings.Settings, recorder paneStateRecorder, sharedAcrossParents map[int]bool, c log.Palette, commandName string) executionResult {
	var result executionResult
	for i, issue := range targets {
		// Hydrate body lazily for issues that came from the Sub-issues API
		// path (body=""), unless --unblocked-only already did it upfront.
		if issue.Body == "" {
			if detail, err := gh.IssueDetail(issue.Number); err == nil {
				issue.Body = detail.Body
			}
		}
		// Fail fast: stop after the first failed child launch.
		if !createPaneForIssue(cfg, lg, info, issue, resolvedSettings, recorder, sharedAcrossParents[issue.Number], c, commandName) {
			result.Failed++
			break
		}
		result.Created++
		result.CreatedNums = append(result.CreatedNums, issue.Number)
		if i < len(targets)-1 {
			if cfg.SleepBetween > 0 {
				sleepBetweenIssues(time.Duration(cfg.SleepBetween * float64(time.Second)))
			}
		}
	}
	return result
}

func isGitWorkTree(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = path
	return cmd.Run() == nil
}

func checkDeps(cfg *cliflags.Config) []string {
	var missing []string
	check := func(cmd, hint string) {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, hint)
		}
	}
	check("git", "git")

	lifecycle := cfg.CloseNum > 0 || cfg.MergeNum > 0 || cfg.CleanupMode
	if cfg.StatusMode || cfg.CleanupMode || !lifecycle {
		check("gh", "gh (brew install gh)")
		check("jq", "jq (brew install jq)")
	}

	if !cfg.StatusMode && !lifecycle {
		check("tmux", "tmux (brew install tmux)")
		if cfg.ParentMode == cliflags.ModeIssue && !ghSubIssueAvailable() {
			missing = append(missing, "gh-sub-issue extension (gh extension install yahsan2/gh-sub-issue)")
		}
	}
	return missing
}

func ghSubIssueAvailable() bool {
	out, err := exec.Command("gh", "extension", "list").Output()
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if first, _, ok := strings.Cut(line, "\t"); ok && first == "gh sub-issue" {
			return true
		}
	}
	return false
}
