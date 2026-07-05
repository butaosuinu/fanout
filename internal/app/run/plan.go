package run

import (
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/blockers"
	"github.com/butaosuinu/fanout/internal/core/fanset"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
)

type blockedRow struct {
	Issue ghissue.Issue
	Refs  string
}

type Plan struct {
	TotalChildren           int
	OpenCount               int
	OpenAfterFilter         int
	UnfannedCount           int
	Targets                 []ghissue.Issue
	AlreadyFanned           []int
	FilteredOnly            []ghissue.Issue
	FilteredSkip            []ghissue.Issue
	MissingOnly             []int
	BlockedRows             []blockedRow
	BlockedLabelWithoutRefs []int
	LimitDeferred           []ghissue.Issue
}

func issueNumber(issue ghissue.Issue) int { return issue.Number }

func BuildPlan(
	cfg *cliflags.Config,
	children []ghissue.Issue,
	fanned map[int]bool,
	parentBody string,
	hydrateIssue func(*ghissue.Issue),
	issueState func(int) string,
) Plan {
	openChildren := OpenIssues(children)
	plan := Plan{
		TotalChildren: len(children),
		OpenCount:     len(openChildren),
	}
	if plan.OpenCount == 0 {
		return plan
	}

	openChildren, plan.FilteredOnly, plan.FilteredSkip, plan.MissingOnly = fanset.FilterOnlySkip(openChildren, issueNumber, cfg.Only, cfg.Skip)
	plan.OpenAfterFilter = len(openChildren)
	if plan.OpenAfterFilter == 0 {
		return plan
	}

	if cfg.UnblockedOnly {
		hydrateIssues(openChildren, hydrateIssue)
	}

	targets, skipped := fanset.SplitFanned(openChildren, issueNumber, fanned)
	plan.Targets = targets
	plan.AlreadyFanned = skipped
	plan.UnfannedCount = len(targets)
	if plan.UnfannedCount == 0 {
		return plan
	}

	if cfg.UnblockedOnly {
		plan.Targets, plan.BlockedRows, plan.BlockedLabelWithoutRefs = splitBlocked(plan.Targets, parentBody, issueState)
	}

	plan.Targets, plan.LimitDeferred = fanset.ApplyLimit(plan.Targets, cfg.Limit)
	return plan
}

func OpenIssues(issues []ghissue.Issue) []ghissue.Issue {
	open := make([]ghissue.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.State == "OPEN" {
			open = append(open, issue)
		}
	}
	return open
}

func hydrateIssues(issues []ghissue.Issue, hydrateIssue func(*ghissue.Issue)) {
	if hydrateIssue == nil {
		return
	}
	for i := range issues {
		if issues[i].Body != "" {
			continue
		}
		hydrateIssue(&issues[i])
	}
}

func splitBlocked(issues []ghissue.Issue, parentBody string, issueState func(int) string) (kept []ghissue.Issue, blocked []blockedRow, blockedLabelWithoutRefs []int) {
	if issueState == nil {
		issueState = func(int) string { return "UNKNOWN" }
	}

	stateCache := map[int]string{}
	for _, issue := range issues {
		childBlockers := blockers.FromChildBody(issue.Body)
		parentBlockers := blockers.FromParentRow(parentBody, issue.Number)
		allBlockers := blockers.Dedupe(childBlockers, parentBlockers)
		openBlockers := openBlockerRefs(allBlockers, stateCache, issueState)

		if hasLabel(issue, "blocked") && len(allBlockers) == 0 {
			blockedLabelWithoutRefs = append(blockedLabelWithoutRefs, issue.Number)
		}
		if len(openBlockers) > 0 {
			blocked = append(blocked, blockedRow{Issue: issue, Refs: formatOpenBlockers(openBlockers)})
			continue
		}
		kept = append(kept, issue)
	}
	return kept, blocked, blockedLabelWithoutRefs
}

func openBlockerRefs(blockerNums []int, cache map[int]string, issueState func(int) string) []int {
	var open []int
	for _, num := range blockerNums {
		state, ok := cache[num]
		if !ok {
			state = issueState(num)
			cache[num] = state
		}
		if state == "OPEN" {
			open = append(open, num)
		}
	}
	return open
}

func hasLabel(issue ghissue.Issue, name string) bool {
	for _, label := range issue.Labels {
		if label.Name == name {
			return true
		}
	}
	return false
}

func formatOpenBlockers(blockers []int) string {
	parts := make([]string, len(blockers))
	for i, num := range blockers {
		parts[i] = fmt.Sprintf("OPEN #%d", num)
	}
	return strings.Join(parts, ", ")
}
