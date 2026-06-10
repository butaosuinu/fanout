package tui

import (
	"errors"
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

var errBoom = errors.New("boom")

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
	issues := map[issueKey]issueStatus{
		{Parent: "200", Num: 2}: {
			State:           "CLOSED",
			Wave:            2,
			Blockers:        "resolved #1",
			HasOpenBlockers: false,
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
	if !second.HasMergedPR {
		t.Fatal("HasMergedPR = false, want true for merged PR")
	}
	if second.WaveBadge != "W2 ready" || second.Blockers != "resolved #1" {
		t.Fatalf("wave/blockers = %q/%q, want W2 ready/resolved #1", second.WaveBadge, second.Blockers)
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

func TestBuildPaneViewsAddsDeferredIssueRows(t *testing.T) {
	issues := map[issueKey]issueStatus{
		{Parent: "100", Num: 1}: {Title: "ready child", State: "OPEN", Wave: 1, Blockers: "-"},
		{Parent: "100", Num: 2}: {Title: "blocked child", State: "OPEN", Wave: 2, Blockers: "OPEN #1", HasOpenBlockers: true},
		{Parent: "100", Num: 3}: {Title: "closed child", State: "CLOSED", Wave: 1, Blockers: "-"},
	}

	got := buildPaneViews("/repo", []state.Pane{{Parent: "100", IssueNum: 1, Slug: "ready-child-1"}}, nil, true, issues, nil)

	if len(got) != 3 {
		t.Fatalf("buildPaneViews len = %d, want recorded pane plus deferred issue", len(got))
	}
	deferred := paneByIssue(t, got, 2)
	if deferred.IssueNum != 2 || deferred.TmuxState != "deferred" {
		t.Fatalf("deferred row = #%d/%s, want #2/deferred", deferred.IssueNum, deferred.TmuxState)
	}
	if deferred.Name != "blocked child" || deferred.WaveBadge != "W2 blocked" || deferred.Blockers != "OPEN #1" {
		t.Fatalf("deferred details = %q/%q/%q, want blocked child/W2 blocked/OPEN #1", deferred.Name, deferred.WaveBadge, deferred.Blockers)
	}
	closed := paneByIssue(t, got, 3)
	if closed.TmuxState != "closed" {
		t.Fatalf("closed row tmux = %q, want closed", closed.TmuxState)
	}
}

func TestBuildPaneViewsMatchesNormalizedNumericParents(t *testing.T) {
	issues := map[issueKey]issueStatus{
		{Parent: "300", Num: 501}: {Title: "child", State: "OPEN", Wave: 1},
	}

	got := buildPaneViews("/repo", []state.Pane{{Parent: "0300", IssueNum: 501, Slug: "child"}}, nil, true, issues, nil)

	if len(got) != 1 {
		t.Fatalf("buildPaneViews len = %d, want one real pane without synthetic duplicate", len(got))
	}
	if got[0].IssueState != "OPEN" || got[0].WaveBadge != "W1 ready" {
		t.Fatalf("pane status = %q/%q, want OPEN/W1 ready", got[0].IssueState, got[0].WaveBadge)
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

func TestUpdateKeepsPartialGHResultsOnError(t *testing.T) {
	m := newModel(Options{})
	issues := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "partial", State: "OPEN", Wave: 1},
	}

	next, _ := m.Update(ghLoadedMsg{issues: issues, err: errBoom})
	got := next.(model)

	if got.ghErr == "" {
		t.Fatal("ghErr is empty, want partial refresh error")
	}
	if !reflect.DeepEqual(got.issues, issues) {
		t.Fatalf("issues = %#v, want partial results %#v", got.issues, issues)
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
	m.issues = map[issueKey]issueStatus{
		{Parent: "200", Num: 201}: {State: "CLOSED", PRs: []ghissue.PRRef{{Number: 11, State: "MERGED"}}},
		{Parent: "200", Num: 202}: {State: "OPEN"},
		{Parent: "200", Num: 203}: {State: "OPEN", HasOpenBlockers: true},
	}
	m.panes = applyIssueStatuses(m.panes, m.issues)
	m.refreshRows()

	got := m.View()
	if !strings.Contains(got, "total=3 merged=1 pending=2 blocked=1") {
		t.Fatalf("View() = %q, want HUD counts", got)
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
	if got := m.table.Rows()[0][5]; got != "stale!" {
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

func TestRecordedParentsDedupesAndSorts(t *testing.T) {
	got := recordedParents([]state.Pane{
		{Parent: "20", IssueNum: 5},
		{Parent: "020", IssueNum: 6},
		{Parent: "10", IssueNum: 3},
		{Parent: "20", IssueNum: 5},
		{Parent: "", IssueNum: 4},
		{Parent: "30", IssueNum: 0},
	})
	want := []string{"10", "20"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recordedParents() = %#v, want %#v", got, want)
	}
}

func TestParentChildNumbersIncludesParent(t *testing.T) {
	got := parentChildNumbers(100, []ghissue.Issue{{Number: 101}})

	if !got[100] {
		t.Fatal("parent issue should be marked existing")
	}
	if !got[101] {
		t.Fatal("sub-issue should be marked existing")
	}
}

func TestMergeParentIssueChildrenUsesParentBodyWithoutSubIssues(t *testing.T) {
	parentBody := "- [ ] #101 first child (blocked by #201)\n- [ ] #102 second child\n"

	got, err := mergeParentIssueChildren(100, nil, parentBody, []state.Pane{{IssueNum: 101}}, func(num int) (ghissue.Issue, error) {
		return ghissue.Issue{Number: num, Title: "issue"}, nil
	})
	if err != nil {
		t.Fatalf("mergeParentIssueChildren() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("mergeParentIssueChildren() len = %d, want 2", len(got))
	}

	gotNums := []int{got[0].Number, got[1].Number}
	want := []int{101, 102}
	if !reflect.DeepEqual(gotNums, want) {
		t.Fatalf("mergeParentIssueChildren() nums = %#v, want %#v", gotNums, want)
	}
}

func TestParentDependenciesFormatsOpenAndClosedBlockers(t *testing.T) {
	issues := []ghissue.Issue{
		{Number: 101, Body: "## Blocked by\n- #201\n- #202\n"},
	}
	states := map[int]string{201: "OPEN", 202: "CLOSED", 203: "OPEN"}
	parentBody := "- [ ] #101 parent dependency (blocked by #202, #203)\n"

	deps := parentDependencies("100", issues, parentBody, map[int]string{}, func(num int) string {
		return states[num]
	})

	got := formatBlockers(deps[101])
	want := "OPEN #201, resolved #202, OPEN #203"
	if got != want {
		t.Fatalf("formatBlockers() = %q, want %q", got, want)
	}
	if !hasOpenBlocker(deps[101]) {
		t.Fatal("hasOpenBlocker() = false, want true")
	}
}

func TestLoadMissingIssueDetailsSkipsLookupFailures(t *testing.T) {
	existing := map[int]bool{100: true}

	got, err := loadMissingIssueDetails([]int{100, 101, 102}, existing, func(num int) (ghissue.Issue, error) {
		if num == 101 {
			return ghissue.Issue{}, errBoom
		}
		return ghissue.Issue{Number: num, Title: "loaded"}, nil
	})

	want := []ghissue.Issue{{Number: 102, Title: "loaded"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadMissingIssueDetails() = %#v, want %#v", got, want)
	}
	if err == nil || !strings.Contains(err.Error(), "#101") {
		t.Fatalf("loadMissingIssueDetails() error = %v, want #101 partial error", err)
	}
	if existing[101] {
		t.Fatal("failed lookup should not mark #101 as existing")
	}
	if !existing[102] {
		t.Fatal("loaded lookup should mark #102 as existing")
	}
}

func TestMergeParentIssueChildrenReturnsPartialRowsWithLookupError(t *testing.T) {
	parentBody := "- [ ] #101 missing child\n- [ ] #102 loaded child\n"

	got, err := mergeParentIssueChildren(100, []ghissue.Issue{{Number: 103, Title: "sub", State: "OPEN"}}, parentBody, nil, func(num int) (ghissue.Issue, error) {
		if num == 101 {
			return ghissue.Issue{}, errBoom
		}
		return ghissue.Issue{Number: num, Title: "loaded", State: "OPEN"}, nil
	})

	if err == nil || !strings.Contains(err.Error(), "#101") {
		t.Fatalf("mergeParentIssueChildren() error = %v, want #101 partial error", err)
	}
	gotNums := []int{}
	for _, issue := range got {
		gotNums = append(gotNums, issue.Number)
	}
	wantNums := []int{103, 102}
	if !reflect.DeepEqual(gotNums, wantNums) {
		t.Fatalf("mergeParentIssueChildren() nums = %#v, want %#v", gotNums, wantNums)
	}
}

func TestDependencyWavesUseParentBlockerDepth(t *testing.T) {
	issues := []ghissue.Issue{{Number: 1}, {Number: 2}, {Number: 3}, {Number: 4}}
	deps := map[int][]blockerStatus{
		1: nil,
		2: {{Num: 1, State: "CLOSED"}},
		3: {{Num: 2, State: "OPEN"}},
		4: {{Num: 99, State: "OPEN"}},
	}

	got := dependencyWaves(issues, deps)

	want := map[int]int{1: 1, 2: 2, 3: 3, 4: 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencyWaves() = %#v, want %#v", got, want)
	}
}

func paneByIssue(t *testing.T, panes []paneView, issueNum int) paneView {
	t.Helper()
	for _, pane := range panes {
		if pane.IssueNum == issueNum {
			return pane
		}
	}
	t.Fatalf("missing pane for #%d in %#v", issueNum, panes)
	return paneView{}
}

func TestLifecycleCloseKeyConfirmsRunsAndRefreshes(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
	m.panes = []paneView{{Parent: "84", IssueNum: 101, Name: "child"}}
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
	m.panes = []paneView{
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
		issues:       map[issueKey]issueStatus{},
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
