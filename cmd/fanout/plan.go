package main

import (
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
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

func buildPlan(
	cfg *cliflags.Config,
	children []ghissue.Issue,
	fanned map[int]bool,
	parentBody string,
	hydrateIssue func(*ghissue.Issue),
	issueState func(int) string,
) Plan {
	openChildren := openIssues(children)
	plan := Plan{
		TotalChildren: len(children),
		OpenCount:     len(openChildren),
	}
	if plan.OpenCount == 0 {
		return plan
	}

	openChildren, plan.FilteredOnly, plan.FilteredSkip, plan.MissingOnly = filterOnlySkip(openChildren, cfg.Only, cfg.Skip)
	plan.OpenAfterFilter = len(openChildren)
	if plan.OpenAfterFilter == 0 {
		return plan
	}

	if cfg.UnblockedOnly {
		hydrateIssues(openChildren, hydrateIssue)
	}

	targets, skipped := splitAlreadyFanned(openChildren, fanned)
	plan.Targets = targets
	plan.AlreadyFanned = skipped
	plan.UnfannedCount = len(targets)
	if plan.UnfannedCount == 0 {
		return plan
	}

	if cfg.UnblockedOnly {
		plan.Targets, plan.BlockedRows, plan.BlockedLabelWithoutRefs = splitBlocked(plan.Targets, parentBody, issueState)
	}

	plan.Targets, plan.LimitDeferred = applyLimit(plan.Targets, cfg.Limit)
	return plan
}

func openIssues(issues []ghissue.Issue) []ghissue.Issue {
	open := make([]ghissue.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.State == "OPEN" {
			open = append(open, issue)
		}
	}
	return open
}

func filterOnlySkip(issues []ghissue.Issue, only, skip []int) (kept, filteredOnly, filteredSkip []ghissue.Issue, missingOnly []int) {
	if len(only) == 0 && len(skip) == 0 {
		return issues, nil, nil, nil
	}

	openSet := map[int]bool{}
	for _, issue := range issues {
		openSet[issue.Number] = true
	}
	for _, num := range only {
		if !openSet[num] {
			missingOnly = append(missingOnly, num)
		}
	}

	onlySet := intSet(only)
	skipSet := intSet(skip)
	for _, issue := range issues {
		switch {
		case len(only) > 0 && !onlySet[issue.Number]:
			filteredOnly = append(filteredOnly, issue)
		case len(skip) > 0 && skipSet[issue.Number]:
			filteredSkip = append(filteredSkip, issue)
		default:
			kept = append(kept, issue)
		}
	}
	return kept, filteredOnly, filteredSkip, missingOnly
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

func splitAlreadyFanned(issues []ghissue.Issue, fanned map[int]bool) (targets []ghissue.Issue, skipped []int) {
	for _, issue := range issues {
		if fanned[issue.Number] {
			skipped = append(skipped, issue.Number)
			continue
		}
		targets = append(targets, issue)
	}
	return targets, skipped
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

func applyLimit(issues []ghissue.Issue, limit int) (targets, deferred []ghissue.Issue) {
	if limit > 0 && len(issues) > limit {
		return issues[:limit], issues[limit:]
	}
	return issues, nil
}

func intSet(nums []int) map[int]bool {
	set := make(map[int]bool, len(nums))
	for _, num := range nums {
		set[num] = true
	}
	return set
}
