package statusreport

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

// PlanReport is the plan --status report for one plan spec.
type PlanReport struct {
	Plan    planspec.Plan `json:"plan"`
	Tasks   []PlanTask    `json:"tasks"`
	Summary Summary       `json:"summary"`
}

// PlanTask is one plan task with the PRs found on its branch.
type PlanTask struct {
	ID            string          `json:"id"`
	Branch        string          `json:"branch"`
	Backend       backend.Name    `json:"backend,omitempty"`
	PaneID        string          `json:"pane_id,omitempty"`
	ReportedState string          `json:"reported_state,omitempty"`
	PRs           []ghissue.PRRef `json:"prs"`
	HasMergedPR   bool            `json:"has_merged_pr"`
	Blocked       bool            `json:"blocked"`
}

// BuildPlanReport queries the PRs on every task branch and assembles the
// plan report. branchFor resolves a task to its branch (recorded state row
// first, then the naming fallback).
func BuildPlanReport(spec planspec.Spec, projectRoot string, branchFor func(planspec.Task) string, lg *log.Logger) (PlanReport, exitcode.Code) {
	gh := ghissue.Runner{Cwd: projectRoot}
	tasks := make([]PlanTask, 0, len(spec.Tasks))
	mergedByID := map[string]bool{}
	for _, task := range spec.Tasks {
		branch := branchFor(task)
		prs, err := gh.PRsForBranch(branch)
		if err != nil {
			lg.Err("--status: gh pr list --head %s failed for task %s: %v", branch, task.ID, err)
			return PlanReport{}, exitcode.GitHub
		}
		row := PlanTask{
			ID:          task.ID,
			Branch:      branch,
			PRs:         prs,
			HasMergedPR: planHasMergedPR(prs),
		}
		mergedByID[task.ID] = row.HasMergedPR
		tasks = append(tasks, row)
	}
	for i, task := range spec.Tasks {
		tasks[i].Blocked = planTaskStatusBlocked(task, mergedByID)
	}
	return PlanReport{
		Plan:  spec.Plan,
		Tasks: tasks,
		Summary: Summarize(tasks,
			func(t PlanTask) bool { return t.HasMergedPR },
			func(t PlanTask) bool { return t.Blocked }),
	}, exitcode.OK
}

func planTaskStatusBlocked(task planspec.Task, mergedByID map[string]bool) bool {
	if mergedByID[task.ID] {
		return false
	}
	for _, depID := range task.BlockedBy {
		if !mergedByID[depID] {
			return true
		}
	}
	return false
}

func planHasMergedPR(prs []ghissue.PRRef) bool {
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "MERGED") || pr.MergedAt != nil {
			return true
		}
	}
	return false
}

func planStatusState(task PlanTask) string {
	if task.HasMergedPR {
		return "merged"
	}
	if task.Blocked {
		return "blocked"
	}
	return "pending"
}

// WritePlanReport prints the plan report as indented JSON.
func WritePlanReport(report PlanReport, lg *log.Logger) exitcode.Code {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		lg.Err("--status: failed to encode plan report: %v", err)
		return exitcode.GitHub
	}
	fmt.Fprintln(lg.Stdout(), string(out))
	return exitcode.OK
}

// WritePlanTable renders the plan report as the status table.
func WritePlanTable(report PlanReport, projectRoot string, lg *log.Logger) exitcode.Code {
	sources := make([]RowSource, 0, len(report.Tasks))
	for _, task := range report.Tasks {
		sources = append(sources, RowSource{
			Label:         task.ID,
			Backend:       task.Backend,
			PaneID:        task.PaneID,
			ReportedState: task.ReportedState,
			State:         planStatusState(task),
			PRs:           task.PRs,
		})
	}
	rows, maxLines, addWidth, delWidth, code := BuildTableRows(ghissue.Runner{Cwd: projectRoot}, projectRoot, sources, lg)
	if code != exitcode.OK {
		return code
	}
	spec := TableSpec{
		Heading: fmt.Sprintf("fanout plan status %s: total=%d merged=%d pending=%d blocked=%d all_merged=%t",
			report.Plan.Slug, report.Summary.Total, report.Summary.Merged, report.Summary.Pending, report.Summary.Blocked, report.Summary.AllMerged),
		EmptyText:      "(no plan tasks)",
		FirstColHeader: "TASK",
	}
	WriteTable(lg.Stdout(), lg.Colors(), spec, rows, maxLines, addWidth, delWidth)
	return exitcode.OK
}
