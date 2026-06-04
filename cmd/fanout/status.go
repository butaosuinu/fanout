package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/state"
)

type statusReport struct {
	Parent   int           `json:"parent"`
	Children []statusChild `json:"children"`
	Summary  statusSummary `json:"summary"`
}

type statusChild struct {
	Num         int             `json:"num"`
	State       string          `json:"state"`
	PRs         []ghissue.PRRef `json:"prs"`
	HasMergedPR bool            `json:"has_merged_pr"`
}

type statusSummary struct {
	Total     int  `json:"total"`
	Merged    int  `json:"merged"`
	Pending   int  `json:"pending"`
	AllMerged bool `json:"all_merged"`
}

func cmdStatus(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	rt, code := resolveStateRuntimeForMode("--status", lg)
	if code != exitcode.OK {
		return code
	}
	if rt.projectRoot == "" || !dirExists(rt.projectRoot) {
		lg.Err("--status: project_root is not a directory: %s (state=%s)", emptyLabel(rt.projectRoot), rt.statePath)
		return exitcode.Invocation
	}

	store, err := state.Load(rt.statePath)
	if err != nil {
		lg.Err("--status: fanout state at %s is not valid JSON or has an invalid schema: %v", rt.statePath, err)
		return exitcode.Invocation
	}
	nums := sortedKeys(store.FannedNumbersForParent(cfg.ParentRef))
	if len(nums) == 0 {
		return writeStatusReport(statusReport{
			Parent:   cfg.Parent,
			Children: []statusChild{},
			Summary:  statusSummary{AllMerged: false},
		}, lg)
	}

	children, code := statusChildren(rt.projectRoot, nums, "--status", lg)
	if code != exitcode.OK {
		return code
	}
	return writeStatusReport(newStatusReport(cfg.Parent, children), lg)
}

func statusChildren(projectRoot string, nums []int, mode string, lg *log.Logger) ([]statusChild, exitcode.Code) {
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

	children := make([]statusChild, 0, len(nums))
	for _, num := range nums {
		state, prs, err := gh.IssueWithPRs(owner, repo, num)
		if err != nil {
			lg.Err("%s: gh api graphql for #%d failed or returned no issue (auth / network / not found)", mode, num)
			return nil, exitcode.GitHub
		}
		child := statusChild{Num: num, State: state, PRs: prs}
		for _, pr := range prs {
			if pr.State == "MERGED" {
				child.HasMergedPR = true
				break
			}
		}
		children = append(children, child)
	}
	return children, exitcode.OK
}

func newStatusReport(parent int, children []statusChild) statusReport {
	merged := 0
	for _, child := range children {
		if child.HasMergedPR {
			merged++
		}
	}
	return statusReport{
		Parent:   parent,
		Children: children,
		Summary: statusSummary{
			Total:     len(children),
			Merged:    merged,
			Pending:   len(children) - merged,
			AllMerged: len(children) > 0 && merged == len(children),
		},
	}
}

func writeStatusReport(report statusReport, lg *log.Logger) exitcode.Code {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		lg.Err("--status: failed to encode report: %v", err)
		return exitcode.GitHub
	}
	fmt.Fprintln(lg.Stdout(), string(out))
	return exitcode.OK
}

func sortedKeys(set map[int]bool) []int {
	nums := make([]int, 0, len(set))
	for n := range set {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums
}
