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
