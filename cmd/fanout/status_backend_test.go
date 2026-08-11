package main

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/app/statusreport"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestAttachChildRuntimeMetadata(t *testing.T) {
	children := []statusreport.Child{{Num: 101}, {Num: 102}, {Num: 103}}
	store := state.Store{Panes: []state.Pane{
		{Parent: "100", IssueNum: 101, PaneID: "%11", ReportedState: "blocked"},
		{Parent: "100", IssueNum: 102, Backend: backend.Herdr, PaneID: "workspace-1:terminal-2", ReportedState: "working"},
		{Parent: "other", IssueNum: 103, Backend: backend.Herdr, PaneID: "other:terminal"},
	}}

	attachChildRuntimeMetadata(children, store, "100")

	if children[0].Backend != backend.Tmux || children[0].PaneID != "%11" || children[0].ReportedState != "" {
		t.Fatalf("legacy child metadata = (%q, %q, %q), want (%q, %%11, empty)", children[0].Backend, children[0].PaneID, children[0].ReportedState, backend.Tmux)
	}
	if children[1].Backend != backend.Herdr || children[1].PaneID != "workspace-1:terminal-2" || children[1].ReportedState != "working" {
		t.Fatalf("herdr child metadata = (%q, %q, %q), want (%q, workspace-1:terminal-2, working)", children[1].Backend, children[1].PaneID, children[1].ReportedState, backend.Herdr)
	}
	if children[2].Backend != "" || children[2].PaneID != "" {
		t.Fatalf("unrecorded child metadata = (%q, %q), want empty", children[2].Backend, children[2].PaneID)
	}
}

func TestAttachPlanRuntimeMetadata(t *testing.T) {
	tasks := []statusreport.PlanTask{{ID: "legacy"}, {ID: "herdr"}, {ID: "unstarted"}}
	store := state.Store{Panes: []state.Pane{
		{Parent: "plan:demo", TaskID: "legacy", PaneID: "%21", ReportedState: "blocked"},
		{Parent: "plan:demo", TaskID: "herdr", Backend: backend.Herdr, PaneID: "workspace-2:terminal-3", ReportedState: "plan"},
	}}

	attachPlanRuntimeMetadata(tasks, store, "plan:demo")

	if tasks[0].Backend != backend.Tmux || tasks[0].PaneID != "%21" || tasks[0].ReportedState != "" {
		t.Fatalf("legacy task metadata = (%q, %q, %q), want (%q, %%21, empty)", tasks[0].Backend, tasks[0].PaneID, tasks[0].ReportedState, backend.Tmux)
	}
	if tasks[1].Backend != backend.Herdr || tasks[1].PaneID != "workspace-2:terminal-3" || tasks[1].ReportedState != "plan" {
		t.Fatalf("herdr task metadata = (%q, %q, %q), want (%q, workspace-2:terminal-3, plan)", tasks[1].Backend, tasks[1].PaneID, tasks[1].ReportedState, backend.Herdr)
	}
	if tasks[2].Backend != "" || tasks[2].PaneID != "" {
		t.Fatalf("unstarted task metadata = (%q, %q), want empty", tasks[2].Backend, tasks[2].PaneID)
	}
}
