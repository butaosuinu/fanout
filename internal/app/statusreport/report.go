// Package statusreport builds and renders the --status and plan --status
// reports: child/task aggregation, JSON output, the ASCII status table,
// and the --post-dashboard issue comment.
package statusreport

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/blockers"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

// Report is the --status report for one parent issue.
type Report struct {
	Parent   int     `json:"parent"`
	Children []Child `json:"children"`
	Summary  Summary `json:"summary"`
}

// Child is one fanned child issue with its PR evidence.
type Child struct {
	Num         int             `json:"num"`
	State       string          `json:"state"`
	Backend     backend.Name    `json:"backend,omitempty"`
	PaneID      string          `json:"pane_id,omitempty"`
	PRs         []ghissue.PRRef `json:"prs"`
	HasMergedPR bool            `json:"has_merged_pr"`
	Body        string          `json:"-"`
	Blocked     bool            `json:"-"`
}

// Summary is the shared rollup of both the issue and the plan report.
type Summary struct {
	Total     int  `json:"total"`
	Merged    int  `json:"merged"`
	Pending   int  `json:"pending"`
	Blocked   int  `json:"blocked"`
	AllMerged bool `json:"all_merged"`
}

// Summarize rolls items up into a Summary using the merged/blocked
// predicates. Pending is the non-merged remainder; AllMerged requires at
// least one item.
func Summarize[T any](items []T, merged, blocked func(T) bool) Summary {
	mergedCount := 0
	blockedCount := 0
	for _, item := range items {
		if merged(item) {
			mergedCount++
		}
		if blocked(item) {
			blockedCount++
		}
	}
	return Summary{
		Total:     len(items),
		Merged:    mergedCount,
		Pending:   len(items) - mergedCount,
		Blocked:   blockedCount,
		AllMerged: len(items) > 0 && mergedCount == len(items),
	}
}

// FetchChildren loads each child issue's state, body, and PRs via gh. mode
// is the flag name used in error messages ("--status").
func FetchChildren(projectRoot string, nums []int, mode string, lg *log.Logger) ([]Child, exitcode.Code) {
	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("%s: failed to resolve repo (gh repo view) in %s", mode, projectRoot)
		return nil, exitcode.GitHub
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		lg.Err("%s: unexpected nameWithOwner from gh: %s", mode, nwo)
		return nil, exitcode.GitHub
	}

	children := make([]Child, 0, len(nums))
	for _, num := range nums {
		snapshot, err := gh.IssueSnapshotWithPRs(owner, repo, num)
		if err != nil {
			lg.Err("%s: gh api graphql for #%d failed or returned no issue (auth / network / not found)", mode, num)
			return nil, exitcode.GitHub
		}
		child := Child{Num: num, State: snapshot.State, Body: snapshot.Body, PRs: snapshot.PRs}
		for _, pr := range snapshot.PRs {
			if pr.State == "MERGED" {
				child.HasMergedPR = true
				break
			}
		}
		children = append(children, child)
	}
	return children, exitcode.OK
}

// NewReport assembles the issue-mode report and its summary.
func NewReport(parent int, children []Child) Report {
	return Report{
		Parent:   parent,
		Children: children,
		Summary: Summarize(children,
			func(c Child) bool { return c.HasMergedPR },
			func(c Child) bool { return c.Blocked }),
	}
}

// MarkBlockers sets Child.Blocked from the child body's "Blocked by"
// section and the parent task-list row trailer, resolving blocker issue
// states via gh with a per-run cache.
func MarkBlockers(projectRoot string, parent int, children []Child) {
	gh := ghissue.Runner{Cwd: projectRoot}
	parentBody, err := gh.ParentBody(parent)
	if err != nil {
		parentBody = ""
	}
	childStates := make(map[int]string, len(children))
	for _, child := range children {
		childStates[child.Num] = child.State
	}
	stateCache := map[int]string{}
	issueState := func(num int) string {
		if state, ok := childStates[num]; ok && strings.TrimSpace(state) != "" {
			return state
		}
		if state, ok := stateCache[num]; ok {
			return state
		}
		state, err := gh.IssueState(num)
		if err != nil {
			state = "UNKNOWN"
		}
		stateCache[num] = state
		return state
	}

	for i := range children {
		refs := blockers.Dedupe(
			blockers.FromChildBody(children[i].Body),
			blockers.FromParentRow(parentBody, children[i].Num),
		)
		children[i].Blocked = hasOpenBlocker(refs, issueState)
	}
}

func hasOpenBlocker(refs []int, issueState func(int) string) bool {
	for _, num := range refs {
		if issueState(num) == "OPEN" {
			return true
		}
	}
	return false
}

// WriteReport prints the issue-mode report as indented JSON.
func WriteReport(report Report, lg *log.Logger) exitcode.Code {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		lg.Err("--status: failed to encode report: %v", err)
		return exitcode.GitHub
	}
	fmt.Fprintln(lg.Stdout(), string(out))
	return exitcode.OK
}
