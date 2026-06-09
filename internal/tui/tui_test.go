package tui

import (
	"reflect"
	"strings"
	"testing"

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
				{Number: 12, State: "MERGED", CIStatus: "pass"},
			},
		},
	}
	worktrees := map[string]worktreeStatView{
		"/repo/.fanout/worktrees/second": {Diff: "+12/-3", Dirty: "dirty"},
	}

	got := buildPaneViews(projectRoot, panes, tmuxPanes, true, issues, worktrees)

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
	if second.IssueState != "CLOSED" || second.PRSummary != "#12 merged" || second.CIStatus != "pass" {
		t.Fatalf("issue/pr/ci = %q/%q/%q, want CLOSED/#12 merged/pass", second.IssueState, second.PRSummary, second.CIStatus)
	}
	if second.DiffSummary != "+12/-3" || second.DirtyState != "dirty" {
		t.Fatalf("worktree stat = %q/%q, want +12/-3/dirty", second.DiffSummary, second.DirtyState)
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

func TestFocusSelectedPaneUsesInjectedFocus(t *testing.T) {
	var focused string
	m := newModel(Options{
		FocusPane: func(paneID string) error {
			focused = paneID
			return nil
		},
		PaneAlive: func(string) bool { return true },
	})
	m.panes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
	m.refreshRows()

	cmd := m.focusSelectedCmd()
	if cmd == nil {
		t.Fatalf("focusSelectedCmd() returned nil, want focus command")
	}
	msg, ok := cmd().(paneFocusedMsg)
	if !ok {
		t.Fatalf("focusSelectedCmd() msg = %T, want paneFocusedMsg", msg)
	}
	next, _ := m.Update(msg)
	m = next.(model)

	if focused != "%1" {
		t.Fatalf("focused pane = %q, want %%1", focused)
	}
	if !strings.Contains(m.notice, "focused %1") {
		t.Fatalf("notice = %q, want focused message", m.notice)
	}
}

func TestFocusSelectedPaneSkipsStaleRows(t *testing.T) {
	called := false
	m := newModel(Options{
		FocusPane: func(string) error {
			called = true
			return nil
		},
		PaneAlive: func(string) bool { return true },
	})
	m.panes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "stale"}}
	m.refreshRows()

	if cmd := m.focusSelectedCmd(); cmd != nil {
		t.Fatalf("focusSelectedCmd() returned a command for stale pane")
	}
	if called {
		t.Fatalf("FocusPane was called for stale pane")
	}
	if !strings.Contains(m.notice, "focus skipped") {
		t.Fatalf("notice = %q, want skipped message", m.notice)
	}
}

func TestFocusSelectedPaneMarksDeadPaneStale(t *testing.T) {
	called := false
	m := newModel(Options{
		FocusPane: func(string) error {
			called = true
			return nil
		},
		PaneAlive: func(string) bool { return false },
	})
	m.panes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
	m.refreshRows()

	cmd := m.focusSelectedCmd()
	if cmd == nil {
		t.Fatalf("focusSelectedCmd() returned nil, want alive-check command")
	}
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(model)

	if called {
		t.Fatalf("FocusPane was called after PaneAlive returned false")
	}
	if m.panes[0].TmuxState != "stale" {
		t.Fatalf("TmuxState = %q, want stale", m.panes[0].TmuxState)
	}
	if got := m.table.Rows()[0][3]; got != "stale!" {
		t.Fatalf("table tmux cell = %q, want stale!", got)
	}
}

func TestPeekSelectedPaneLoadsOutputIntoDetail(t *testing.T) {
	var capturedPane string
	var capturedLines int
	m := newModel(Options{
		CapturePaneOutput: func(paneID string, lines int) (string, error) {
			capturedPane = paneID
			capturedLines = lines
			return "line 1\n  line 2\n\tline 3\n", nil
		},
	})
	m.detail.Width = 80
	m.detail.Height = 9
	m.panes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
	m.refreshRows()

	cmd := m.peekSelectedCmd(true)
	if cmd == nil {
		t.Fatalf("peekSelectedCmd() returned nil, want capture command")
	}
	if !m.peek.Loading {
		t.Fatalf("peek.Loading = false, want true before command returns")
	}
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(model)

	if capturedPane != "%1" {
		t.Fatalf("captured pane = %q, want %%1", capturedPane)
	}
	if capturedLines != peekLines {
		t.Fatalf("captured lines = %d, want %d", capturedLines, peekLines)
	}
	got := m.detailContent()
	if !strings.Contains(got, "peek") || !strings.Contains(got, "\tline 3") || !strings.Contains(got, "  line 2") {
		t.Fatalf("detailContent() = %q, want peek output", got)
	}
}

func TestKeySelectionChangeStartsPeekCapture(t *testing.T) {
	var capturedPane string
	m := newModel(Options{
		CapturePaneOutput: func(paneID string, lines int) (string, error) {
			capturedPane = paneID
			return "selected\n", nil
		},
	})
	m.panes = []paneView{
		{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"},
		{IssueNum: 2, Name: "two", PaneID: "%2", TmuxState: "live"},
	}
	m.refreshRows()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if cmd == nil {
		t.Fatalf("Update(down) returned nil command, want peek command")
	}
	msg := cmd()
	if _, ok := msg.(panePeekLoadedMsg); !ok {
		t.Fatalf("Update(down) cmd msg = %T, want panePeekLoadedMsg", msg)
	}
	if capturedPane != "%2" {
		t.Fatalf("captured pane = %q, want %%2", capturedPane)
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

func TestNewPaneKeyOpensForm(t *testing.T) {
	m := newModel(Options{DefaultAgent: "codex"})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	got := updated.(model)

	if got.mode != modeNewPane {
		t.Fatalf("mode = %v, want new pane form", got.mode)
	}
	if got.newPane.agent != "codex" {
		t.Fatalf("default agent = %q, want codex", got.newPane.agent)
	}
}

func TestNewPaneFormRequiresPrompt(t *testing.T) {
	m := newModel(Options{LaunchPane: func(LaunchRequest) error {
		t.Fatal("LaunchPane should not be called without a prompt")
		return nil
	}})
	m.openNewPaneForm()

	if cmd := m.submitNewPane(); cmd != nil {
		t.Fatal("submitNewPane returned a command without a prompt")
	}
	if m.newPane.err != "prompt is required" {
		t.Fatalf("form error = %q, want prompt is required", m.newPane.err)
	}
}

func TestNewPaneFormSubmitsLaunchRequest(t *testing.T) {
	var got LaunchRequest
	called := false
	m := newModel(Options{
		DefaultAgent: "codex",
		LaunchPane: func(req LaunchRequest) error {
			called = true
			got = req
			return nil
		},
	})
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("Inspect the HTTP API")
	m.newPane.slug.SetValue("inspect-http-api")

	cmd := m.submitNewPane()
	if cmd == nil {
		t.Fatal("submitNewPane returned nil command")
	}
	if !m.newPane.launching {
		t.Fatal("launching = false, want true")
	}
	msg := cmd()
	if launch, ok := msg.(launchPaneMsg); !ok || launch.err != nil {
		t.Fatalf("launch message = %#v, want nil-error launchPaneMsg", msg)
	}
	if !called {
		t.Fatal("LaunchPane was not called")
	}
	want := LaunchRequest{Prompt: "Inspect the HTTP API", Agent: "codex", Slug: "inspect-http-api"}
	if got != want {
		t.Fatalf("launch request = %#v, want %#v", got, want)
	}
}

func TestNewPaneLaunchSuccessReturnsToMonitorAndReloadsState(t *testing.T) {
	m := newModel(Options{})
	m.openNewPaneForm()

	updated, cmd := m.Update(launchPaneMsg{})
	got := updated.(model)

	if got.mode != modeMonitor {
		t.Fatalf("mode = %v, want monitor", got.mode)
	}
	if !strings.Contains(got.notice, "created") {
		t.Fatalf("notice = %q, want created message", got.notice)
	}
	if cmd == nil {
		t.Fatal("launch success did not request a state reload")
	}
}
