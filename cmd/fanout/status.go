package main

import (
	"slices"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/stateemitter"
	"github.com/butaosuinu/fanout/internal/app/statusreport"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

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
		report := statusreport.Report{
			Parent:   cfg.Parent,
			Children: []statusreport.Child{},
			Summary:  statusreport.Summary{AllMerged: false},
		}
		if cfg.Format == "table" {
			if code = statusreport.WriteIssueTable(report, rt.projectRoot, lg); code != exitcode.OK {
				return code
			}
		} else if code = statusreport.WriteReport(report, lg); code != exitcode.OK {
			return code
		}
		if cfg.PostDashboard {
			return statusreport.PostDashboard(report, rt.projectRoot, lg)
		}
		return exitcode.OK
	}

	children, code := statusreport.FetchChildren(rt.projectRoot, nums, "--status", lg)
	if code != exitcode.OK {
		return code
	}
	attachChildRuntimeMetadata(children, store, cfg.ParentRef)
	statusreport.MarkBlockers(rt.projectRoot, cfg.Parent, children)
	report := statusreport.NewReport(cfg.Parent, children)
	if cfg.Format == "table" {
		if code := statusreport.WriteIssueTable(report, rt.projectRoot, lg); code != exitcode.OK {
			return code
		}
	} else if code := statusreport.WriteReport(report, lg); code != exitcode.OK {
		return code
	}
	if cfg.PostDashboard {
		return statusreport.PostDashboard(report, rt.projectRoot, lg)
	}
	return exitcode.OK
}

func attachChildRuntimeMetadata(children []statusreport.Child, store state.Store, parent string) {
	for i := range children {
		pane, ok := store.Find(parent, children[i].Num)
		if !ok {
			continue
		}
		children[i].Backend = backend.NormalizeName(pane.Backend)
		children[i].PaneID = pane.PaneID
		if children[i].Backend == backend.Herdr {
			children[i].ReportedState = stateemitter.ReportedState(pane.ReportedState)
		}
	}
}

func sortedKeys(set map[int]bool) []int {
	nums := make([]int, 0, len(set))
	for n := range set {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	return nums
}
