package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/butaosuinu/fanout/internal/cliflags"
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

func printSummary(plan Plan, result executionResult, cfg *cliflags.Config, lg *log.Logger, c log.Palette) {
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
		fmt.Fprintf(lg.Stdout(), "\n%sDeferred %d issue(s) due to --limit. Rerun with:%s\n", c.Info, len(plan.LimitDeferred), c.Reset)
		nums := make([]string, len(plan.LimitDeferred))
		for i, row := range plan.LimitDeferred {
			nums[i] = fmt.Sprintf("#%d", row.Number)
		}
		fmt.Fprintf(lg.Stdout(), "  %s\n", strings.Join(nums, " "))
		fmt.Fprintf(lg.Stdout(), "  fanout %d --limit %d%s%s%s%s\n",
			cfg.Parent, len(plan.LimitDeferred),
			optFlag("--only", cfg.OnlyArg)+optFlag("--skip", cfg.SkipArg),
			boolFlag(" --unblocked-only", cfg.UnblockedOnly),
			optFlag("--agent", cfg.Agent),
			optFlag("--session", cfg.Session))
	}
}

func optFlag(flag, value string) string {
	if value == "" {
		return ""
	}
	return " " + flag + " " + value
}

func boolFlag(flagWithLeadSpace string, on bool) string {
	if on {
		return flagWithLeadSpace
	}
	return ""
}
