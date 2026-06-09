package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/lifecycle"
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
				{Number: 12, State: "MERGED", CIStatus: "pass"},
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
	if second.IssueState != "CLOSED" || second.PRSummary != "#12 merged" || second.CIStatus != "pass" {
		t.Fatalf("issue/pr/ci = %q/%q/%q, want CLOSED/#12 merged/pass", second.IssueState, second.PRSummary, second.CIStatus)
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

func TestFocusSelectedPaneUsesInjectedFocus(t *testing.T) {
	var focused string
	m := newModel(Options{
		FocusPane: func(paneID string) error {
			focused = paneID
			return nil
		},
		PaneAlive: func(string) bool { return true },
	})
	m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
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
	m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "stale"}}
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
	m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
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
	if got := m.table.Rows()[0][4]; got != "stale!" {
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
	m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
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
	m.allPanes = []paneView{
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

func TestFilterPaneViewsSearchesTextAndPredicates(t *testing.T) {
	panes := []paneView{
		{
			Parent:     "142",
			IssueNum:   115,
			Name:       "state agent wave filters",
			TmuxState:  "live",
			IssueState: "OPEN",
			PRSummary:  "#201 open",
			CIStatus:   "pass",
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
			PRSummary:  "#199 merged",
			CIStatus:   "fail",
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

func TestLifecycleCloseKeyConfirmsRunsAndRefreshes(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
	m.allPanes = []paneView{{Parent: "84", IssueNum: 101, Name: "child"}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("c"))
	if cmd != nil {
		t.Fatal("close prompt returned command, want nil")
	}
	m = updated.(model)
	if m.pendingAction == nil || m.pendingAction.action != actionClose {
		t.Fatalf("pendingAction = %#v, want close", m.pendingAction)
	}
	if !strings.Contains(m.actionMessage, "confirm close #101") {
		t.Fatalf("actionMessage = %q, want close confirmation", m.actionMessage)
	}

	updated, cmd = m.Update(keyRunes("y"))
	if cmd == nil {
		t.Fatal("confirm returned nil command, want lifecycle command")
	}
	m = updated.(model)
	if !m.actionRunning {
		t.Fatal("actionRunning = false, want true while command runs")
	}

	rawMsg := cmd()
	msg, ok := rawMsg.(lifecycleDoneMsg)
	if !ok {
		t.Fatalf("lifecycle command returned %T, want lifecycleDoneMsg", rawMsg)
	}
	if runner.closeParent != "84" || runner.closeIssue != 101 {
		t.Fatalf("Close called with parent=%q issue=%d, want 84/101", runner.closeParent, runner.closeIssue)
	}
	if runner.projectRoot != "/repo" || runner.statePath != state.Path("/repo") {
		t.Fatalf("Close opts = %q/%q, want project root and state path", runner.projectRoot, runner.statePath)
	}

	updated, cmd = m.Update(msg)
	m = updated.(model)
	if m.actionRunning {
		t.Fatal("actionRunning = true after done, want false")
	}
	if !strings.Contains(m.actionMessage, "close #101: ok") {
		t.Fatalf("actionMessage = %q, want close success", m.actionMessage)
	}
	if cmd == nil {
		t.Fatal("done returned nil command, want state/GH reload")
	}
}

func TestLifecycleCleanupUsesSelectedParent(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 1, Name: "one"},
		{Parent: "200", IssueNum: 2, Name: "two"},
	}
	m.refreshRows()
	m.table.SetCursor(1)

	updated, _ := m.Update(keyRunes("x"))
	m = updated.(model)
	if m.pendingAction == nil || m.pendingAction.action != actionCleanup {
		t.Fatalf("pendingAction = %#v, want cleanup", m.pendingAction)
	}

	updated, cmd := m.Update(keyRunes("y"))
	if cmd == nil {
		t.Fatal("confirm returned nil command, want lifecycle command")
	}
	m = updated.(model)
	rawMsg := cmd()
	if _, ok := rawMsg.(lifecycleDoneMsg); !ok {
		t.Fatalf("lifecycle command returned %T, want lifecycleDoneMsg", rawMsg)
	}
	if runner.cleanupParent != "200" {
		t.Fatalf("Cleanup parent = %q, want selected parent 200", runner.cleanupParent)
	}
}

func TestManualLifecycleRefreshDoesNotScheduleExtraTicks(t *testing.T) {
	m := newModel(Options{})

	updated, cmd := m.Update(stateLoadedMsg{
		panes:        []paneView{{IssueNum: 1, Name: "one"}},
		at:           time.Unix(1, 0),
		scheduleNext: false,
	})
	if cmd != nil {
		t.Fatal("manual state refresh returned command, want no extra tick")
	}
	m = updated.(model)

	updated, cmd = m.Update(ghLoadedMsg{
		issues:       map[int]issueStatus{},
		at:           time.Unix(2, 0),
		scheduleNext: false,
	})
	if cmd != nil {
		t.Fatal("manual GH refresh returned command, want no extra tick")
	}
	m = updated.(model)

	_, cmd = m.Update(stateLoadedMsg{at: time.Unix(3, 0), scheduleNext: true})
	if cmd == nil {
		t.Fatal("scheduled state refresh returned nil command, want next tick")
	}
}

func TestLifecycleRunningDefersQuitKeysUntilDone(t *testing.T) {
	m := newModel(Options{})
	m.actionRunning = true

	updated, cmd := m.Update(keyRunes("q"))
	if cmd != nil {
		t.Fatal("q while lifecycle action is running returned command, want deferred quit")
	}
	m = updated.(model)
	if !m.quitAfterAction {
		t.Fatal("quitAfterAction = false, want true after q while action is running")
	}

	_, cmd = m.Update(keyRunes("j"))
	if cmd != nil {
		t.Fatal("non-quit key while lifecycle action is running returned command, want nil")
	}

	_, cmd = m.Update(lifecycleDoneMsg{action: actionClose, pane: paneView{IssueNum: 1}, code: exitcode.OK})
	if cmd == nil {
		t.Fatal("lifecycleDone with deferred quit returned nil command, want quit command")
	}
	rawMsg := cmd()
	if _, ok := rawMsg.(tea.QuitMsg); !ok {
		t.Fatalf("deferred quit command returned %T, want tea.QuitMsg", rawMsg)
	}
}

type fakeLifecycleRunner struct {
	code          exitcode.Code
	projectRoot   string
	statePath     string
	closeParent   string
	closeIssue    int
	cleanupParent string
}

func (f *fakeLifecycleRunner) Close(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	f.projectRoot = opts.ProjectRoot
	f.statePath = opts.StatePath
	f.closeParent = parent
	f.closeIssue = issueNum
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake close\n")
	return f.code
}

func (f *fakeLifecycleRunner) Merge(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake merge\n")
	return f.code
}

func (f *fakeLifecycleRunner) Cleanup(opts lifecycle.Options, parent string, lg lifecycle.Logger) exitcode.Code {
	f.cleanupParent = parent
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake cleanup\n")
	return f.code
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
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
