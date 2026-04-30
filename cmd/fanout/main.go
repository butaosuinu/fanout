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

	if missing := checkDeps(); len(missing) > 0 {
		lg.Err("missing dependencies:")
		for _, d := range missing {
			fmt.Fprintf(lg.Stderr(), "  - %s\n", d)
		}
		os.Exit(int(exitcode.Env))
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
		lg.Info("no sub-issues on #%d. nothing to do.", cfg.Parent)
		return exitcode.OK
	}

	openCount := len(openIssues(loaded.Children))
	lg.Info("sub-issues: %d total, %d OPEN", totalChildren, openCount)
	if openCount == 0 {
		lg.Info("no OPEN sub-issues. nothing to do.")
		return exitcode.OK
	}

	plan := buildPlan(
		cfg,
		loaded.Children,
		rt.config.FannedNumbers(),
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
	Children   []ghissue.Issue
	ParentBody string
}

func loadChildren(cfg *cliflags.Config, gh ghissue.Runner, lg *log.Logger) (childLoadResult, exitcode.Code) {
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
	bodyNums = append(bodyNums, cfg.Include...)

	existing := map[int]bool{cfg.Parent: true}
	for _, s := range subIssues {
		existing[s.Number] = true
	}

	extra := []ghissue.Issue{}
	for _, num := range bodyNums {
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

	children := ghissue.MergeExtra(subIssues, extra)
	if len(extra) > 0 {
		lg.Info("parent body / --include added %d extra child reference(s) not in sub-issue API", len(extra))
	}

	return childLoadResult{Children: children, ParentBody: parentBody}, exitcode.OK
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
			if cfg.SleepBetween > 0 && !cfg.DryRun {
				time.Sleep(time.Duration(cfg.SleepBetween * float64(time.Second)))
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
			overrides = append(overrides, displayname.Override{Num: name.Num, DisplayName: name.DisplayName})
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

// checkDeps mirrors fanout:387-407.
func checkDeps() []string {
	var missing []string
	check := func(cmd, hint string) {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, hint)
		}
	}
	check("gh", "gh (brew install gh)")
	check("tmux", "tmux (brew install tmux)")
	check("pgrep", "pgrep (procps-ng on Linux; preinstalled on macOS)")

	if !ghSubIssueAvailable() {
		missing = append(missing, "gh-sub-issue extension (gh extension install yahsan2/gh-sub-issue)")
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
