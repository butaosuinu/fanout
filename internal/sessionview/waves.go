package sessionview

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/ghissue"
)

// WaveInfo is one child's dependency summary: the DAG depth (1-based; 0 means
// unknown), the parent-body wave heading label if any, and the resolved
// blocker rows.
type WaveInfo struct {
	Wave      int
	WaveLabel string
	Blockers  []blockers.Status
	Blocked   bool // at least one blocker is still OPEN
	// Degraded marks rows whose wave/blocker inputs could not all be read
	// (child-body hydration, parent body, or a blocker's state lookup
	// failed): the fields may be missing data. Consumers can keep last-known
	// display data for exactly these rows instead of treating "no blockers"
	// as freshly confirmed.
	Degraded bool
}

// IssueGraphClient is the gh surface FetchWaveGraph needs. ghissue.Runner
// satisfies it; tests inject fakes.
type IssueGraphClient interface {
	ParentBody(parent int) (string, error)
	SubIssueList(parent int) ([]ghissue.Issue, error)
	IssueDetail(num int) (ghissue.Issue, error)
	IssueState(num int) (string, error)
}

// WaveGraph is the resolved child set for one parent plus per-child wave and
// blocker info keyed by issue number.
type WaveGraph struct {
	Children []ghissue.Issue
	Info     map[int]WaveInfo
}

// FetchWaveGraph loads a parent's children (Sub-issues API ∪ parent task-list
// rows ∪ recordedNums for a numeric parent; recordedNums only for @manual or
// Project parents), hydrates missing bodies, and computes blocker statuses
// plus dependency waves. Per-issue failures (including blocker IssueState
// lookups) are joined and partial results kept so consumers degrade instead
// of going blank. The blocker state cache is pre-seeded with the states the
// child fetches already returned, so only out-of-set blockers resolve lazily
// through IssueState.
func FetchWaveGraph(c IssueGraphClient, parent string, recordedNums []int) (WaveGraph, error) {
	children, parentBody, includeParentRows, hydrated, parentBodyFailed, loadErr := fetchWaveChildren(c, parent, recordedNums)
	failed, hydrateErr := hydrateIssueBodies(c, children, hydrated)
	loadErr = errors.Join(loadErr, hydrateErr)

	// Pre-seed with in-set states: SubIssueList/IssueDetail already carry State,
	// so re-fetching it per blocker would burn gh API budget for nothing. Empty
	// states stay out so stateOf can still fall back to a live lookup.
	stateCache := map[int]string{}
	for _, issue := range children {
		if issue.State != "" {
			stateCache[issue.Number] = issue.State
		}
	}
	var stateErr error
	failedStates := map[int]bool{}
	stateOf := func(num int) string {
		state, ok := stateCache[num]
		if !ok {
			var err error
			state, err = c.IssueState(num)
			if err != nil {
				// Downstream renders the empty state as UNKNOWN; surface the
				// failure so consumers can flag degradation instead of showing
				// UNKNOWN with no indicator.
				stateErr = errors.Join(stateErr, fmt.Errorf("blocker state #%d: %w", num, err))
				failedStates[num] = true
			}
			stateCache[num] = state
		}
		return state
	}
	childRows := make([]blockers.Child, len(children))
	childNums := make([]int, len(children))
	for i, issue := range children {
		childRows[i] = blockers.Child{Num: issue.Number, Body: issue.Body}
		childNums[i] = issue.Number
	}
	deps := blockers.Dependencies(childRows, includeParentRows, parentBody, stateOf)
	waves := blockers.Waves(childNums, deps)

	info := make(map[int]WaveInfo, len(children))
	for _, issue := range children {
		// A row is degraded when any input to its wave/blocker computation
		// could not be read: its own body, the parent body (task-list rows),
		// or the state of one of its blockers.
		degraded := failed[issue.Number] || (includeParentRows && parentBodyFailed)
		for _, dep := range deps[issue.Number] {
			if failedStates[dep.Num] {
				degraded = true
			}
		}
		info[issue.Number] = WaveInfo{
			Wave:      waves[issue.Number],
			WaveLabel: issue.Wave,
			Blockers:  deps[issue.Number],
			Blocked:   blockers.HasOpen(deps[issue.Number]),
			Degraded:  degraded,
		}
	}
	return WaveGraph{Children: children, Info: info}, errors.Join(loadErr, stateErr)
}

// fetchWaveChildren resolves the child set and reports whether parent
// task-list rows participate in blocker parsing. Non-numeric parents have no
// parent issue to scan, so only recorded pane issues remain. hydrated marks
// children that already came from IssueDetail so hydration does not re-fetch
// issues whose body is genuinely empty.
func fetchWaveChildren(c IssueGraphClient, parent string, recordedNums []int) ([]ghissue.Issue, string, bool, map[int]bool, bool, error) {
	hydrated := map[int]bool{}
	parentNum, convErr := strconv.Atoi(parent)
	if convErr != nil {
		children, err := loadIssueDetails(c, recordedNums, map[int]bool{}, hydrated)
		return children, "", false, hydrated, false, err
	}

	var loadErr error
	parentBodyFailed := false
	parentBody, err := c.ParentBody(parentNum)
	if err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("parent body #%d: %w", parentNum, err))
		parentBody = ""
		parentBodyFailed = true
	}
	subIssues, err := c.SubIssueList(parentNum)
	if err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("sub-issues #%d: %w", parentNum, err))
		subIssues = nil
	}
	existing := map[int]bool{parentNum: true}
	for _, issue := range subIssues {
		existing[issue.Number] = true
	}
	taskExtra, taskErr := loadIssueDetails(c, ghissue.TaskListNumbers(parentBody), existing, hydrated)
	recordedExtra, recordedErr := loadIssueDetails(c, recordedNums, existing, hydrated)
	children := ghissue.MergeExtra(subIssues, append(taskExtra, recordedExtra...))
	assignWaveLabels(children, ghissue.TaskListWaves(parentBody))
	return children, parentBody, true, hydrated, parentBodyFailed, errors.Join(loadErr, taskErr, recordedErr)
}

// loadIssueDetails fetches IssueDetail for each num not already in existing,
// joining per-issue failures and keeping the rest. Loaded nums are marked in
// existing (failed ones are not, so a later source may retry them) and in
// hydrated, because IssueDetail returns the full issue — re-fetching it during
// hydration would double the gh calls for genuinely body-less issues.
func loadIssueDetails(c IssueGraphClient, nums []int, existing, hydrated map[int]bool) ([]ghissue.Issue, error) {
	extra := []ghissue.Issue{}
	var loadErr error
	for _, num := range nums {
		if num <= 0 || existing[num] {
			continue
		}
		detail, err := c.IssueDetail(num)
		if err != nil {
			loadErr = errors.Join(loadErr, fmt.Errorf("#%d: %w", num, err))
			continue
		}
		extra = append(extra, detail)
		existing[num] = true
		hydrated[num] = true
	}
	return extra, loadErr
}

// hydrateIssueBodies fills bodies (and missing titles/states) the Sub-issues
// API leaves blank. Issues in hydrated already came from IssueDetail and are
// skipped even when their body is genuinely empty. Per-issue failures are
// joined (with the failed issue numbers reported so rows can be flagged
// degraded) and the rest hydrated — a degraded row beats a blank dashboard
// (the TUI used to hard-fail here).
func hydrateIssueBodies(c IssueGraphClient, issues []ghissue.Issue, hydrated map[int]bool) (map[int]bool, error) {
	failed := map[int]bool{}
	var loadErr error
	for i := range issues {
		if issues[i].Body != "" || hydrated[issues[i].Number] {
			continue
		}
		detail, err := c.IssueDetail(issues[i].Number)
		if err != nil {
			loadErr = errors.Join(loadErr, fmt.Errorf("#%d: %w", issues[i].Number, err))
			failed[issues[i].Number] = true
			continue
		}
		issues[i].Body = detail.Body
		issues[i].Labels = detail.Labels
		if issues[i].Title == "" {
			issues[i].Title = detail.Title
		}
		if issues[i].State == "" {
			issues[i].State = detail.State
		}
	}
	return failed, loadErr
}

func assignWaveLabels(issues []ghissue.Issue, labels map[int]string) {
	for i := range issues {
		if label := labels[issues[i].Number]; label != "" {
			issues[i].Wave = label
		}
	}
}
