package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
)

type executionResult struct {
	Created     int
	Failed      int
	CreatedNums []int
}

func logPlanDetails(plan Plan, lg *log.Logger) {
	for _, num := range plan.MissingOnly {
		lg.Warn("--only: #%d not in OPEN child set (ignored)", num)
	}
	for _, num := range plan.BlockedLabelWithoutRefs {
		lg.Warn("#%d: has 'blocked' label but no parseable blocker numbers — treating as unblocked", num)
	}
	for _, row := range plan.BlockedRows {
		lg.Info("deferred: #%d (blocked by %s)", row.Issue.Number, row.Refs)
	}
}

func logAlreadyFanned(skipped []int, lg *log.Logger) {
	if len(skipped) == 0 {
		return
	}
	sort.Ints(skipped)
	parts := make([]string, len(skipped))
	for i, num := range skipped {
		parts[i] = fmt.Sprintf("#%d", num)
	}
	lg.Info("already fanned-out (skipping): %s", strings.Join(parts, " "))
}

func printDryRunPlan(plan Plan, lg *log.Logger, c log.Palette) {
	if len(plan.FilteredOnly) > 0 || len(plan.FilteredSkip) > 0 {
		fmt.Fprintf(lg.Stdout(), "\n%sfiltered out:%s\n", c.Info, c.Reset)
		for _, row := range plan.FilteredOnly {
			fmt.Fprintf(lg.Stdout(), "  #%d %s — not in --only\n", row.Number, row.Title)
		}
		for _, row := range plan.FilteredSkip {
			fmt.Fprintf(lg.Stdout(), "  #%d %s — in --skip\n", row.Number, row.Title)
		}
		fmt.Fprintln(lg.Stdout())
	}

	if len(plan.BlockedRows) > 0 {
		fmt.Fprintf(lg.Stdout(), "\n%sdeferred (blocked):%s\n", c.Info, c.Reset)
		for _, row := range plan.BlockedRows {
			fmt.Fprintf(lg.Stdout(), "  #%d %s — blocked by %s\n", row.Issue.Number, row.Issue.Title, row.Refs)
		}
		fmt.Fprintln(lg.Stdout())
	}
}

func printSummary(plan Plan, result executionResult, cfg *cliflags.Config, lg *log.Logger, c log.Palette, commandName string) {
	fmt.Fprintln(lg.Stdout())
	if result.Created > 0 {
		lg.Ok("created: %d", result.Created)
	}
	if result.Failed > 0 {
		lg.Err("failed:  %d", result.Failed)
	}
	if len(plan.AlreadyFanned) > 0 {
		lg.Info("skipped (already fanned-out): %d", len(plan.AlreadyFanned))
	}
	if total := len(plan.FilteredOnly) + len(plan.FilteredSkip); total > 0 {
		lg.Info("skipped (filtered): %d", total)
	}
	if len(plan.BlockedRows) > 0 {
		lg.Info("deferred (blocked): %d", len(plan.BlockedRows))
	}

	if len(plan.LimitDeferred) > 0 {
		if result.Failed > 0 {
			lg.Info("deferred (--limit): %d", len(plan.LimitDeferred))
			lg.Warn("not printing --limit rerun hint because this run failed before all selected targets completed")
			return
		}
		fmt.Fprintf(lg.Stdout(), "\n%sDeferred %d issue(s) due to --limit. Rerun with:%s\n", c.Info, len(plan.LimitDeferred), c.Reset)
		nums := make([]string, len(plan.LimitDeferred))
		for i, row := range plan.LimitDeferred {
			nums[i] = fmt.Sprintf("#%d", row.Number)
		}
		fmt.Fprintf(lg.Stdout(), "  %s\n", strings.Join(nums, " "))
		deferredCSV := issueCSV(plan.LimitDeferred)
		statusFlag := ""
		if cfg.ParentMode == cliflags.ModeProject && cfg.ProjectStatus != cliflags.DefaultProjectStatus {
			statusFlag = optFlag("--project-status", cfg.ProjectStatus)
		}
		fmt.Fprintf(lg.Stdout(), "  %s %s%s --include %s --only %s%s%s%s%s%s%s%s\n",
			shellQuote(commandName), shellQuote(cfg.ParentRef),
			statusFlag,
			shellQuote(deferredCSV),
			shellQuote(deferredCSV),
			boolFlag(" --unblocked-only", cfg.UnblockedOnly),
			codexPlanModeFlag(cfg),
			settingsFlags(cfg),
			worktreeFlags(cfg),
			nameFlagsFor(cfg, plan.LimitDeferred),
			optFlag("--agent", cfg.Agent),
			optFlag("--session", cfg.Session))
	}
}

func issueCSV(issues []ghissue.Issue) string {
	nums := make([]string, len(issues))
	for i, issue := range issues {
		nums[i] = fmt.Sprintf("%d", issue.Number)
	}
	return strings.Join(nums, ",")
}

func optFlag(flag, value string) string {
	if value == "" {
		return ""
	}
	return " " + flag + " " + shellQuote(value)
}

func boolFlag(flagWithLeadSpace string, on bool) string {
	if on {
		return flagWithLeadSpace
	}
	return ""
}

func codexPlanModeFlag(cfg *cliflags.Config) string {
	if cfg.CodexPlanMode == nil {
		return ""
	}
	if *cfg.CodexPlanMode {
		return " --codex-plan-mode"
	}
	return " --no-codex-plan-mode"
}

func settingsFlags(cfg *cliflags.Config) string {
	return boolSettingFlag("--auto-pr", "--no-auto-pr", cfg.AutoPullRequest) +
		boolSettingFlag("--pr-review-gate", "--no-pr-review-gate", cfg.PRReviewGate) +
		boolSettingFlag("--briefing-code-review", "--no-briefing-code-review", cfg.BriefingCodeReview) +
		boolSettingFlag("--agent-teams-hint", "--no-agent-teams-hint", cfg.AgentTeamsHint) +
		boolSettingFlag("--pr-visualization", "--no-pr-visualization", cfg.PRVisualization) +
		boolSettingFlag("--dashboard-keybind", "--no-dashboard-keybind", cfg.DashboardKeybind)
}

func worktreeFlags(cfg *cliflags.Config) string {
	return optFlag("--base-branch", cfg.BaseBranch) +
		optFlag("--branch-prefix", cfg.BranchPrefix) +
		boolFlag(" --no-refresh", cfg.NoRefresh)
}

func nameFlagsFor(cfg *cliflags.Config, issues []ghissue.Issue) string {
	wanted := map[int]bool{}
	for _, issue := range issues {
		wanted[issue.Number] = true
	}
	var flags []string
	for _, name := range cfg.Names {
		if wanted[name.Num] {
			flags = append(flags, optFlag("--name", renderNameOverride(name)))
		}
	}
	return strings.Join(flags, "")
}

func renderNameOverride(name cliflags.NameOverride) string {
	switch {
	case name.BranchName != "":
		return fmt.Sprintf("%d=%s|%s|%s", name.Num, name.SlugHint, name.DisplayName, name.BranchName)
	case name.DisplayName != "":
		return fmt.Sprintf("%d=%s|%s", name.Num, name.SlugHint, name.DisplayName)
	default:
		return fmt.Sprintf("%d=%s", name.Num, name.SlugHint)
	}
}

func boolSettingFlag(onFlag, offFlag string, v *bool) string {
	if v == nil {
		return ""
	}
	if *v {
		return " " + onFlag
	}
	return " " + offFlag
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == ':' || r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
