package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/displayname"
	"github.com/butaosuinu/fanout/internal/dmuxconfig"
	"github.com/butaosuinu/fanout/internal/dmuxsession"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
)

const fanoutTagPrefix = "[fanout #"

var sleepBetweenIssues = time.Sleep

func main() {
	lg := log.New(false)

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

	os.Exit(int(run(cfg, lg)))
}

func run(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	rt, code := resolveRuntime(cfg, lg)
	if code != exitcode.OK {
		return code
	}

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

	claim := loaded.StrongChildren
	if migrated, ok := migrateLegacyPaneTags(cfg, rt.info.ConfigPath, loaded.StrongChildren, rt.config, lg); ok {
		if migrated > 0 && !cfg.DryRun {
			reloaded, err := dmuxconfig.Load(rt.info.ConfigPath)
			if err == nil {
				rt.config = reloaded
			}
		}
	} else {
		claim = nil
	}

	plan := buildPlan(
		cfg,
		loaded.Children,
		rt.config.FannedNumbersForParent(cfg.ParentRef, claim),
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

	result := executePlan(cfg, lg, rt.info, rt.gh, plan.Targets, c)
	applyDisplayNameOverrides(cfg, rt.info.ConfigPath, result.CreatedNums, lg, c)
	printSummary(plan, result, cfg, lg, c)

	if result.Failed > 0 {
		return exitcode.Env
	}
	return exitcode.OK
}

type runtimeInfo struct {
	info   *dmuxsession.Info
	config *dmuxconfig.Config
	gh     ghissue.Runner
}

func resolveRuntime(cfg *cliflags.Config, lg *log.Logger) (*runtimeInfo, exitcode.Code) {
	info, err := dmuxsession.Resolve(cfg.Session)
	if err != nil {
		lg.Err("%s", err.Error())
		return nil, exitcode.Env
	}

	if _, err := os.Stat(info.ConfigPath); err != nil {
		lg.Err("dmux config not found at %s (session reports it but file is missing)", info.ConfigPath)
		return nil, exitcode.Env
	}

	lg.Info("dmux session: %s", info.Session)
	lg.Info("control pane: %s", info.ControlPane)
	lg.Info("project root: %s", info.ProjectRoot)
	lg.Info("config:       %s", info.ConfigPath)

	dcfg, err := dmuxconfig.Load(info.ConfigPath)
	if err != nil {
		lg.Err("%s", err.Error())
		return nil, exitcode.Env
	}

	if cfg.Agent == "" {
		if pid := os.Getenv("TMUX_PANE"); pid != "" {
			if a := dcfg.AgentForPane(pid); a != "" {
				cfg.Agent = a
				lg.Info("auto-detected agent: %s (from calling pane %s)", cfg.Agent, pid)
			}
		}
	}

	if !isGitWorkTree(info.ProjectRoot) {
		lg.Err("project root %s is not a git work tree; cannot resolve GitHub repo", info.ProjectRoot)
		return nil, exitcode.Env
	}

	return &runtimeInfo{
		info:   info,
		config: dcfg,
		gh:     ghissue.Runner{Cwd: info.ProjectRoot},
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

func issueSet(issues []ghissue.Issue) map[int]bool {
	out := map[int]bool{}
	for _, issue := range issues {
		out[issue.Number] = true
	}
	return out
}

func migrateLegacyPaneTags(cfg *cliflags.Config, configPath string, strong map[int]bool, loaded *dmuxconfig.Config, lg *log.Logger) (int, bool) {
	count := loaded.LegacyMigrationCount(strong)
	if count == 0 {
		return 0, true
	}
	if cfg.DryRun {
		lg.Info("would migrate %d legacy [fanout #N] pane tag(s) to include parent #%s", count, cfg.ParentRef)
		return count, true
	}
	migrated, err := dmuxconfig.MigrateLegacyPaneTags(configPath, cfg.ParentRef, strong)
	if err == nil {
		lg.Info("migrated %d legacy [fanout #N] pane tag(s) to include parent #%s", migrated, cfg.ParentRef)
		return migrated, true
	}
	lg.Warn("could not migrate legacy pane tags in %s; falling back to strict idempotency so legacy panes don't mask new --status entries", configPath)
	return 0, false
}

func executePlan(cfg *cliflags.Config, lg *log.Logger, info *dmuxsession.Info, gh ghissue.Runner, targets []ghissue.Issue, c log.Palette) executionResult {
	var result executionResult
	for i, issue := range targets {
		// Hydrate body lazily for issues that came from the Sub-issues API
		// path (body=""), unless --unblocked-only already did it upfront.
		if issue.Body == "" {
			if detail, err := gh.IssueDetail(issue.Number); err == nil {
				issue.Body = detail.Body
			}
		}
		if createPaneForIssue(cfg, lg, info, issue, c) {
			result.Created++
			result.CreatedNums = append(result.CreatedNums, issue.Number)
		} else {
			result.Failed++
			break
		}
		if i < len(targets)-1 {
			if cfg.SleepBetween > 0 {
				sleepBetweenIssues(time.Duration(cfg.SleepBetween * float64(time.Second)))
			}
		}
	}
	return result
}

func applyDisplayNameOverrides(cfg *cliflags.Config, configPath string, createdNums []int, lg *log.Logger, c log.Palette) {
	if len(createdNums) == 0 || !cfg.HasAnyDisplayName() {
		return
	}
	var overrides []displayname.Override
	for _, num := range createdNums {
		if name := cfg.FindName(num); name != nil && name.DisplayName != "" {
			overrides = append(overrides, displayname.Override{Num: name.Num, ParentRef: cfg.ParentRef, DisplayName: name.DisplayName})
		}
	}
	displayname.ApplyAll(configPath, overrides, cfg.DryRun, lg.Stdout(), c, displayname.LogFns{
		Info: lg.Info, Warn: lg.Warn, Dim: lg.Dim, Err: lg.Err,
	})
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
	check("gh", "gh (brew install gh)")
	check("jq", "jq (brew install jq)")

	if !cfg.StatusMode || os.Getenv("DMUX_CONFIG_PATH") == "" {
		check("tmux", "tmux (brew install tmux)")
	}

	if !cfg.StatusMode {
		check("pgrep", "pgrep (procps-ng on Linux; preinstalled on macOS)")
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
	for _, line := range strings.Split(string(out), "\n") {
		if first, _, ok := strings.Cut(line, "\t"); ok && first == "gh sub-issue" {
			return true
		}
	}
	return false
}
