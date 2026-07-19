package tui

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
)

func TestPaneViewsFromSnapshotRendersHerdrRuntimeAliases(t *testing.T) {
	snap := sessionview.Snapshot{Sessions: []sessionview.Session{{
		Parent: "100",
		Panes: []sessionview.PaneView{
			{
				IssueNum: 101, Backend: backend.Herdr, PaneID: "w1:p1", Alive: true,
				RuntimeState: "live", RuntimeTitle: "live child",
				TmuxState: "live", TmuxTitle: "live child", AgentState: "working",
			},
			{
				IssueNum: 102, Backend: backend.Herdr, PaneID: "w1:p2",
				RuntimeState: "stale", TmuxState: "stale",
			},
			{
				IssueNum: 103, Backend: backend.Herdr, PaneID: "w1:p3",
				RuntimeState: "unknown", TmuxState: "unknown",
			},
			{
				IssueNum: 104, Backend: backend.Herdr, PaneID: "w1:p4",
				RuntimeState: "unsupported", TmuxState: "unknown",
			},
		},
	}}}

	got := paneViewsFromSnapshot("/repo", snap)
	if len(got) != 4 {
		t.Fatalf("paneViewsFromSnapshot len = %d, want 4", len(got))
	}

	wantStates := []string{"live", "stale!", "unknown", "unsup!"}
	wantRuns := []string{"◐", "✗", "-", "!"}
	runtimeColumn := columnIndex(t, "TMUX")
	runColumn := columnIndex(t, "RUN")
	for i := range got {
		row := got[i].tableRow()
		if row[runtimeColumn] != wantStates[i] {
			t.Errorf("row %d TMUX = %q, want %q", i, row[runtimeColumn], wantStates[i])
		}
		if row[runColumn] != wantRuns[i] {
			t.Errorf("row %d RUN = %q, want %q", i, row[runColumn], wantRuns[i])
		}
		if got[i].Backend != backend.Herdr {
			t.Errorf("row %d backend = %q, want herdr", i, got[i].Backend)
		}
	}
	if compact := compactPaneLine(got[3], 4, false, 60); !strings.Contains(compact, "4! #104") {
		t.Fatalf("compact unsupported row = %q, want distinct ! runtime glyph", compact)
	}

	filtered := filterPaneViews(got, "run:working")
	if len(filtered) != 1 || filtered[0].IssueNum != 101 {
		t.Fatalf("run:working rows = %+v, want only #101", filtered)
	}
}
