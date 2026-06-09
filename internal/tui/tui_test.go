package tui

import (
	"errors"
	"reflect"
	"testing"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
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
	got := buildPaneViews("/repo", []state.Pane{{IssueNum: 3, PaneID: "%3"}}, nil, false, nil)
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

	got := buildPaneViews("/repo", []state.Pane{{Parent: "100", IssueNum: 1, Slug: "ready-child-1"}}, nil, true, issues)

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

	got := buildPaneViews("/repo", []state.Pane{{Parent: "0300", IssueNum: 501, Slug: "child"}}, nil, true, issues)

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

	got := loadMissingIssueDetails([]int{100, 101, 102}, existing, func(num int) (ghissue.Issue, error) {
		if num == 101 {
			return ghissue.Issue{}, errBoom
		}
		return ghissue.Issue{Number: num, Title: "loaded"}, nil
	})

	want := []ghissue.Issue{{Number: 102, Title: "loaded"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadMissingIssueDetails() = %#v, want %#v", got, want)
	}
	if existing[101] {
		t.Fatal("failed lookup should not mark #101 as existing")
	}
	if !existing[102] {
		t.Fatal("loaded lookup should mark #102 as existing")
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
