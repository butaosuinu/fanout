package tui

import (
	"reflect"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	tea "github.com/charmbracelet/bubbletea"
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
			Wave:         "wave5",
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

	got := buildPaneViews(projectRoot, panes, tmuxPanes, true, issues)

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
	if second.Wave != "wave5" {
		t.Fatalf("Wave = %q, want wave5", second.Wave)
	}
	if got[0].TmuxState != "stale" {
		t.Fatalf("first tmux state = %q, want stale", got[0].TmuxState)
	}
}

func TestBuildPaneViewsMarksTmuxUnknownWhenListFails(t *testing.T) {
	got := buildPaneViews("/repo", []state.Pane{{IssueNum: 3, PaneID: "%3"}}, nil, false, nil)
	if len(got) != 1 {
		t.Fatalf("buildPaneViews len = %d, want 1", len(got))
	}
	if got[0].TmuxState != "unknown" {
		t.Fatalf("TmuxState = %q, want unknown", got[0].TmuxState)
	}
}

func TestRefreshRowsClampsCursorWhenRowsShrink(t *testing.T) {
	m := newModel(Options{})
	m.allPanes = []paneView{{IssueNum: 1, Name: "one"}, {IssueNum: 2, Name: "two"}, {IssueNum: 3, Name: "three"}}
	m.refreshRows()
	m.table.SetCursor(2)

	m.allPanes = []paneView{{IssueNum: 1, Name: "one"}}
	m.refreshRows()

	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor after shrink = %d, want 0", got)
	}
	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("rows after shrink = %d, want 1", got)
	}
}

func TestFilterPaneViewsSearchesTextAndPredicates(t *testing.T) {
	panes := []paneView{
		{
			Parent:     "142",
			IssueNum:   115,
			Name:       "state agent wave filters",
			TmuxState:  "live",
			IssueState: "OPEN",
			PRSummary:  "#201 OPEN",
			BranchName: "feat/dashboard-filter",
			Agent:      "codex",
			Wave:       "wave5",
		},
		{
			Parent:     "142",
			IssueNum:   109,
			Name:       "pr ci merge columns",
			TmuxState:  "stale",
			IssueState: "CLOSED",
			PRSummary:  "#199 MERGED",
			Agent:      "claude",
			Wave:       "wave4",
		},
	}

	got := filterPaneViews(panes, "state:open agent:codex wave:wave5 dashboard")
	if len(got) != 1 || got[0].IssueNum != 115 {
		t.Fatalf("filterPaneViews predicate match = %#v, want only #115", got)
	}
	got = filterPaneViews(panes, "status:merged")
	if len(got) != 1 || got[0].IssueNum != 109 {
		t.Fatalf("filterPaneViews state alias over PR state = %#v, want only #109", got)
	}
	got = filterPaneViews(panes, "#115")
	if len(got) != 1 || got[0].IssueNum != 115 {
		t.Fatalf("filterPaneViews issue search = %#v, want only #115", got)
	}
}

func TestRefreshRowsKeepsFilterDuringStateAndGHUpdates(t *testing.T) {
	m := newModel(Options{})
	m.filterQuery = "agent:codex state:open"
	m.allPanes = []paneView{
		{IssueNum: 1, Name: "one", Agent: "codex", IssueState: "OPEN"},
		{IssueNum: 2, Name: "two", Agent: "claude", IssueState: "OPEN"},
	}
	m.refreshRows()

	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("initial filtered rows = %d, want 1", got)
	}

	updated, _ := m.Update(stateLoadedMsg{
		panes: []paneView{
			{IssueNum: 1, Name: "one", Agent: "codex", IssueState: "-"},
			{IssueNum: 2, Name: "two", Agent: "claude", IssueState: "-"},
			{IssueNum: 3, Name: "three", Agent: "codex", IssueState: "-"},
		},
		at: time.Unix(10, 0),
	})
	m = updated.(model)

	updated, _ = m.Update(ghLoadedMsg{
		issues: map[int]issueStatus{
			1: {State: "CLOSED"},
			2: {State: "OPEN"},
			3: {State: "OPEN"},
		},
		at: time.Unix(20, 0),
	})
	m = updated.(model)

	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("filtered rows after refresh = %d, want 1", got)
	}
	if got := m.panes[0].IssueNum; got != 3 {
		t.Fatalf("filtered issue after refresh = #%d, want #3", got)
	}
	if m.lastState.IsZero() || m.lastGH.IsZero() {
		t.Fatalf("refresh timestamps were not updated: state=%v gh=%v", m.lastState, m.lastGH)
	}
}

func TestFilterInputEditsQueryAndEscapeClearsWhenInactive(t *testing.T) {
	m := newModel(Options{})
	m.allPanes = []paneView{
		{IssueNum: 1, Name: "one", Agent: "codex"},
		{IssueNum: 2, Name: "two", Agent: "claude"},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(model)
	if !m.filterEditing {
		t.Fatal("filterEditing = false, want true")
	}

	for _, r := range "agent:codex" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("rows while editing = %d, want 1", got)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.filterEditing {
		t.Fatal("filterEditing = true after enter, want false")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.filterQuery != "" {
		t.Fatalf("filterQuery after inactive esc = %q, want empty", m.filterQuery)
	}
	if got := len(m.table.Rows()); got != 2 {
		t.Fatalf("rows after clearing filter = %d, want 2", got)
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
