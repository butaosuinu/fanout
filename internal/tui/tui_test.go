package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

func TestBuildPaneViewsMergesStateTmuxAndIssueStatuses(t *testing.T) {
	projectRoot := "/repo"
	panes := []state.Pane{
		{
			Parent:       "200",
			IssueNum:     2,
			Slug:         "second",
			PaneID:       "%2",
			BranchName:   "feat/second",
			DisplayName:  "Second pane",
			WorktreePath: "/repo/.fanout/worktrees/second",
			Agent:        "codex",
		},
		{
			Parent:       "100",
			IssueNum:     1,
			Slug:         "first",
			PaneID:       "%1",
			WorktreePath: "/repo/.fanout/worktrees/first",
		},
	}
	tmuxPanes := []tmuxrun.PaneInfo{
		{ID: "%2", Title: "running title"},
	}
	issues := map[int]issueStatus{
		2: {
			State: "CLOSED",
			PRs: []ghissue.PRRef{
				{Number: 11, State: "OPEN"},
				{Number: 12, State: "MERGED"},
			},
		},
	}

	got := buildPaneViews(projectRoot, panes, tmuxPanes, true, issues, nil)

	if len(got) != 2 {
		t.Fatalf("buildPaneViews len = %d, want 2", len(got))
	}
	if got[0].IssueNum != 1 || got[1].IssueNum != 2 {
		t.Fatalf("buildPaneViews sort = issues %#v, want 1 then 2", []int{got[0].IssueNum, got[1].IssueNum})
	}
	second := got[1]
	if second.Name != "Second pane" {
		t.Fatalf("Name = %q, want display name", second.Name)
	}
	if second.TmuxState != "live" || second.TmuxTitle != "running title" {
		t.Fatalf("tmux = %q/%q, want live/running title", second.TmuxState, second.TmuxTitle)
	}
	if second.IssueState != "CLOSED" || second.PRSummary != "#12 MERGED" {
		t.Fatalf("issue/pr = %q/%q, want CLOSED/#12 MERGED", second.IssueState, second.PRSummary)
	}
	if second.WorktreePath != ".fanout/worktrees/second" {
		t.Fatalf("WorktreePath = %q, want relative path", second.WorktreePath)
	}
	if got[0].TmuxState != "stale" {
		t.Fatalf("first tmux state = %q, want stale", got[0].TmuxState)
	}
}

func TestBuildPaneViewsMarksTmuxUnknownWhenListFails(t *testing.T) {
	got := buildPaneViews("/repo", []state.Pane{{IssueNum: 3, PaneID: "%3"}}, nil, false, nil, nil)
	if len(got) != 1 {
		t.Fatalf("buildPaneViews len = %d, want 1", len(got))
	}
	if got[0].TmuxState != "unknown" {
		t.Fatalf("TmuxState = %q, want unknown", got[0].TmuxState)
	}
}

func TestRefreshRowsClampsCursorWhenRowsShrink(t *testing.T) {
	m := newModel(Options{})
	m.panes = []paneView{{IssueNum: 1, Name: "one"}, {IssueNum: 2, Name: "two"}, {IssueNum: 3, Name: "three"}}
	m.refreshRows()
	m.table.SetCursor(2)

	m.panes = []paneView{{IssueNum: 1, Name: "one"}}
	m.refreshRows()

	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor after shrink = %d, want 0", got)
	}
	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("rows after shrink = %d, want 1", got)
	}
}

func TestBlockedPanesUsesOpenBlockers(t *testing.T) {
	panes := []state.Pane{
		{Parent: "200", IssueNum: 202},
		{Parent: "200", IssueNum: 203},
		{Parent: "200", IssueNum: 204},
	}
	issues := map[int]issueStatus{
		201: {State: "CLOSED"},
		202: {State: "OPEN", Body: "## Blocked by\n- #201\n"},
		203: {State: "OPEN", Body: "## Blocked by\n- #204\n"},
		204: {State: "OPEN"},
	}
	parentBodies := map[string]string{
		"200": "- [ ] #204 parent row blocker (blocked by #203)\n",
	}

	got := blockedPanes(ghissue.Runner{}, panes, issues, parentBodies)

	if got[paneKey{Parent: "200", IssueNum: 202}] {
		t.Fatal("issue 202 was marked blocked by a closed blocker")
	}
	if !got[paneKey{Parent: "200", IssueNum: 203}] {
		t.Fatal("issue 203 was not marked blocked by an open child-body blocker")
	}
	if !got[paneKey{Parent: "200", IssueNum: 204}] {
		t.Fatal("issue 204 was not marked blocked by an open parent-row blocker")
	}
}

func TestLoadParentBodiesTreatsLookupFailureAsEmpty(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	got := loadParentBodies(ghissue.Runner{}, []state.Pane{
		{Parent: "200", IssueNum: 201},
		{Parent: "https://github.com/users/butaosuinu/projects/1", IssueNum: 202},
	})

	if len(got) != 0 {
		t.Fatalf("loadParentBodies() = %#v, want empty map when parent lookup fails", got)
	}
}

func TestViewRendersHUDCounts(t *testing.T) {
	m := newModel(Options{ProjectRoot: "/repo"})
	m.width = 100
	m.height = 30
	m.panes = []paneView{
		{Parent: "200", IssueNum: 201, Name: "one"},
		{Parent: "200", IssueNum: 202, Name: "two"},
		{Parent: "200", IssueNum: 203, Name: "three"},
	}
	m.issues = map[int]issueStatus{
		201: {State: "CLOSED", PRs: []ghissue.PRRef{{Number: 11, State: "MERGED"}}},
		202: {State: "OPEN"},
		203: {State: "OPEN"},
	}
	m.blocked = map[paneKey]bool{{Parent: "200", IssueNum: 203}: true}
	m.refreshRows()

	got := m.View()
	if !strings.Contains(got, "total=3 merged=1 pending=2 blocked=1") {
		t.Fatalf("View() = %q, want HUD counts", got)
	}
}

func TestIssueNumbersDedupesAndSorts(t *testing.T) {
	got := issueNumbers([]state.Pane{
		{IssueNum: 5},
		{IssueNum: 3},
		{IssueNum: 5},
		{IssueNum: 0},
	})
	want := []int{3, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("issueNumbers() = %#v, want %#v", got, want)
	}
}
