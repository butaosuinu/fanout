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
// plus dependency waves. Per-issue failures are joined and partial results
// kept so consumers degrade instead of going blank. Out-of-set blocker states
// resolve lazily through IssueState with a per-call cache.
func FetchWaveGraph(c IssueGraphClient, parent string, recordedNums []int) (WaveGraph, error) {
	children, parentBody, includeParentRows, loadErr := fetchWaveChildren(c, parent, recordedNums)
	loadErr = errors.Join(loadErr, hydrateIssueBodies(c, children))

	stateCache := map[int]string{}
	stateOf := func(num int) string {
		state, ok := stateCache[num]
		if !ok {
			state, _ = c.IssueState(num) // failures degrade to UNKNOWN downstream
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
		info[issue.Number] = WaveInfo{
			Wave:      waves[issue.Number],
			WaveLabel: issue.Wave,
			Blockers:  deps[issue.Number],
			Blocked:   blockers.HasOpen(deps[issue.Number]),
		}
	}
	return WaveGraph{Children: children, Info: info}, loadErr
}

// fetchWaveChildren resolves the child set and reports whether parent
// task-list rows participate in blocker parsing. Non-numeric parents have no
// parent issue to scan, so only recorded pane issues remain.
func fetchWaveChildren(c IssueGraphClient, parent string, recordedNums []int) ([]ghissue.Issue, string, bool, error) {
	parentNum, convErr := strconv.Atoi(parent)
	if convErr != nil {
		children, err := loadIssueDetails(c, recordedNums, map[int]bool{})
		return children, "", false, err
	}

	var loadErr error
	parentBody, err := c.ParentBody(parentNum)
	if err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("parent body #%d: %w", parentNum, err))
		parentBody = ""
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
	taskExtra, taskErr := loadIssueDetails(c, ghissue.TaskListNumbers(parentBody), existing)
	recordedExtra, recordedErr := loadIssueDetails(c, recordedNums, existing)
	children := ghissue.MergeExtra(subIssues, append(taskExtra, recordedExtra...))
	assignWaveLabels(children, ghissue.TaskListWaves(parentBody))
	return children, parentBody, true, errors.Join(loadErr, taskErr, recordedErr)
}

// loadIssueDetails fetches IssueDetail for each num not already in existing,
// joining per-issue failures and keeping the rest. Loaded nums are marked in
// existing; failed ones are not, so a later source may retry them.
func loadIssueDetails(c IssueGraphClient, nums []int, existing map[int]bool) ([]ghissue.Issue, error) {
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
	}
	return extra, loadErr
}

// hydrateIssueBodies fills bodies (and missing titles/states) the Sub-issues
// API leaves blank. Per-issue failures are joined and the rest hydrated — a
// degraded row beats a blank dashboard (the TUI used to hard-fail here).
func hydrateIssueBodies(c IssueGraphClient, issues []ghissue.Issue) error {
	var loadErr error
	for i := range issues {
		if issues[i].Body != "" {
			continue
		}
		detail, err := c.IssueDetail(issues[i].Number)
		if err != nil {
			loadErr = errors.Join(loadErr, fmt.Errorf("#%d: %w", issues[i].Number, err))
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
	return loadErr
}

func assignWaveLabels(issues []ghissue.Issue, labels map[int]string) {
	for i := range issues {
		if label := labels[issues[i].Number]; label != "" {
			issues[i].Wave = label
		}
	}
}
