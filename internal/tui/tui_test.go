package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/lifecycle"
	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/watch"
)

var errBoom = errors.New("boom")

type fakeWatcherRunner struct {
	report watch.Report
	err    error
	calls  int
}

func (f *fakeWatcherRunner) RunCycle() (watch.Report, error) {
	f.calls++
	return f.report, f.err
}

func TestBuildPaneViewsMergesStateTmuxAndIssueStatuses(t *testing.T) {
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

	got := buildPaneViews(panes, tmuxPanes, true, issues, worktrees)

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
	if second.WaveLabel != "wave5" {
		t.Fatalf("WaveLabel = %q, want wave5", second.WaveLabel)
	}
	if got[0].TmuxState != "stale" {
		t.Fatalf("first tmux state = %q, want stale", got[0].TmuxState)
	}
}

func TestBuildPaneViewsMarksTmuxUnknownWhenListFails(t *testing.T) {
	got := buildPaneViews([]state.Pane{{IssueNum: 3, PaneID: "%3"}}, nil, false, nil, nil)
	if len(got) != 1 {
		t.Fatalf("buildPaneViews len = %d, want 1", len(got))
	}
	if got[0].TmuxState != "unknown" {
		t.Fatalf("TmuxState = %q, want unknown", got[0].TmuxState)
	}
}

func TestBuildPaneViewsTaskIDDisplayFallback(t *testing.T) {
	mergedAt := "2026-06-13T00:00:00Z"
	issues := map[issueKey]issueStatus{
		keyForTask("plan:alpha", "task-a"): {
			State: "UNKNOWN",
			PRs: []ghissue.PRRef{{
				Number:   42,
				State:    "MERGED",
				MergedAt: &mergedAt,
				CIStatus: "pass",
			}},
		},
	}
	got := buildPaneViews([]state.Pane{
		{Parent: "plan:alpha", IssueNum: 0, TaskID: "task-b", PaneID: "%2"},
		{Parent: "plan:alpha", IssueNum: 0, TaskID: "task-a", PaneID: "%1"},
	}, nil, true, issues, nil)

	if len(got) != 2 {
		t.Fatalf("buildPaneViews len = %d, want 2", len(got))
	}
	if got[0].TaskID != "task-a" || got[1].TaskID != "task-b" {
		t.Fatalf("task order = %q,%q want task-a,task-b", got[0].TaskID, got[1].TaskID)
	}
	if got[0].Name != "task-a" {
		t.Fatalf("Name = %q, want task id fallback", got[0].Name)
	}
	if row := got[0].tableRow(); row[1] != "task-a" {
		t.Fatalf("issue column = %q, want task id", row[1])
	}
	if got[0].PRSummary != "#42 merged" || got[0].CIStatus != "pass" || !got[0].HasMergedPR {
		t.Fatalf("task PR fields = pr:%q ci:%q merged:%v", got[0].PRSummary, got[0].CIStatus, got[0].HasMergedPR)
	}
	if filtered := filterPaneViews(got, "task-a"); len(filtered) != 1 || filtered[0].TaskID != "task-a" {
		t.Fatalf("task id filter = %+v, want only task-a", filtered)
	}
}

func TestLoadIssueStatusesFetchesTaskBranchPRs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".fanout"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".fanout", "state.json"), []byte(`{"schemaVersion":1,"panes":[
	  {"parent":"plan:alpha","issueNum":0,"taskId":"task-a","branchName":"fanout/task-a","paneId":"%1"}
	]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	argsPath := installTUIFakeGH(t, `[{
	  "number":42,
	  "state":"MERGED",
	  "mergedAt":"2026-06-13T00:00:00Z",
	  "isDraft":false,
	  "reviewDecision":"APPROVED",
	  "statusCheckRollup":{"state":"SUCCESS"}
	}]`)

	statuses, err := loadIssueStatuses(root)
	if err != nil {
		t.Fatal(err)
	}
	status, ok := statuses[keyForTask("plan:alpha", "task-a")]
	if !ok {
		t.Fatalf("missing task status in %#v", statuses)
	}
	if status.State != sessionview.IssueStateUnknown || len(status.PRs) != 1 || status.PRs[0].Number != 42 || status.PRs[0].CIStatus != "pass" {
		t.Fatalf("task branch status = %+v", status)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "pr list --head fanout/task-a --state all") {
		t.Fatalf("fake gh args did not include branch PR lookup:\n%s", args)
	}
}

func TestBuildPaneViewsCarriesTaskID(t *testing.T) {
	got := buildPaneViews([]state.Pane{
		{
			Parent:       "plan:launch-plan",
			IssueNum:     0,
			TaskID:       "api-client",
			Slug:         "launch-plan-api-client",
			WorktreePath: "/repo/.fanout/worktrees/launch-plan-api-client",
		},
	}, nil, true, nil, nil)

	if len(got) != 1 {
		t.Fatalf("buildPaneViews len = %d, want 1 task row", len(got))
	}
	if got[0].TaskID != "api-client" || got[0].itemLabel() != "api-client" {
		t.Fatalf("task identity = %q/%q, want api-client", got[0].TaskID, got[0].itemLabel())
	}
	if got[0].IssueState != sessionview.IssueStateUnknown {
		t.Fatalf("IssueState = %q, want task row issue state unknown", got[0].IssueState)
	}
	if got[0].tableRow()[1] != "api-client" {
		t.Fatalf("table identity cell = %q, want api-client", got[0].tableRow()[1])
	}
}

func TestBuildPaneViewsAddsDeferredIssueRows(t *testing.T) {
	issues := map[issueKey]issueStatus{
		{Parent: "100", Num: 1}: {Title: "ready child", State: "OPEN", Wave: 1, Blockers: "-"},
		{Parent: "100", Num: 2}: {Title: "blocked child", State: "OPEN", Wave: 2, Blockers: "OPEN #1", HasOpenBlockers: true},
		{Parent: "100", Num: 3}: {Title: "closed child", State: "CLOSED", Wave: 1, Blockers: "-"},
	}

	got := buildPaneViews([]state.Pane{{Parent: "100", IssueNum: 1, Slug: "ready-child-1"}}, nil, true, issues, nil)

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

	got := buildPaneViews([]state.Pane{{Parent: "0300", IssueNum: 501, Slug: "child"}}, nil, true, issues, nil)

	if len(got) != 1 {
		t.Fatalf("buildPaneViews len = %d, want one real pane without synthetic duplicate", len(got))
	}
	if got[0].IssueState != "OPEN" || got[0].WaveBadge != "W1 ready" {
		t.Fatalf("pane status = %q/%q, want OPEN/W1 ready", got[0].IssueState, got[0].WaveBadge)
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

func TestBuildSessionSummariesGroupsVisibleRows(t *testing.T) {
	panes := []paneView{
		{Parent: "100", IssueNum: 101, HasMergedPR: true, TmuxState: "live"},
		{Parent: "100", IssueNum: 102, Blocked: true, TmuxState: "stale"},
		{Parent: "200", IssueNum: 201, TmuxState: "live"},
	}

	got := buildSessionSummaries(panes, 2)
	want := []sessionSummary{
		{Parent: "100", Start: 0, Total: 2, Merged: 1, Pending: 1, Blocked: 1, Live: 1},
		{Parent: "200", Start: 2, Total: 1, Pending: 1, Live: 1, Active: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionSummaries = %#v, want %#v", got, want)
	}
}

func TestSessionJumpKeysMoveBetweenParentGroups(t *testing.T) {
	m := newModel(Options{})
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 101, Name: "first"},
		{Parent: "100", IssueNum: 102, Name: "second"},
		{Parent: "200", IssueNum: 201, Name: "third"},
		{Parent: "300", IssueNum: 301, Name: "fourth"},
	}
	m.refreshRows()

	updated, _ := m.Update(keyRunes("]"))
	m = updated.(model)
	if got := m.table.Cursor(); got != 2 {
		t.Fatalf("cursor after ] = %d, want first row of parent 200", got)
	}

	updated, _ = m.Update(keyRunes("]"))
	m = updated.(model)
	if got := m.table.Cursor(); got != 3 {
		t.Fatalf("cursor after second ] = %d, want first row of parent 300", got)
	}

	updated, _ = m.Update(keyRunes("]"))
	m = updated.(model)
	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor after wrap ] = %d, want first row of parent 100", got)
	}

	updated, _ = m.Update(keyRunes("["))
	m = updated.(model)
	if got := m.table.Cursor(); got != 3 {
		t.Fatalf("cursor after wrap [ = %d, want first row of parent 300", got)
	}
}

func TestSessionJumpScrollsTargetRowIntoView(t *testing.T) {
	m := newModel(Options{})
	m.width = 90
	m.height = 24
	m.resize()
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 101, Name: "one"},
		{Parent: "100", IssueNum: 102, Name: "two"},
		{Parent: "100", IssueNum: 103, Name: "three"},
		{Parent: "100", IssueNum: 104, Name: "four"},
		{Parent: "100", IssueNum: 105, Name: "five"},
		{Parent: "200", IssueNum: 201, Name: "target-session"},
	}
	m.refreshRows()

	updated, _ := m.Update(keyRunes("]"))
	m = updated.(model)

	if got := m.table.Cursor(); got != 5 {
		t.Fatalf("cursor after ] = %d, want target session row", got)
	}
	if view := m.table.View(); !strings.Contains(view, "#201") {
		t.Fatalf("table view after session jump did not include target row:\n%s", view)
	}
}

func TestSessionJumpUsesFilteredVisibleRows(t *testing.T) {
	m := newModel(Options{})
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 101, Name: "hidden"},
		{Parent: "200", IssueNum: 201, Name: "visible"},
		{Parent: "200", IssueNum: 202, Name: "also visible"},
	}
	m.filterQuery = "200"
	m.refreshRows()

	if got := len(m.panes); got != 2 {
		t.Fatalf("filtered panes = %d, want 2", got)
	}
	updated, _ := m.Update(keyRunes("]"))
	m = updated.(model)
	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor after single visible session jump = %d, want 0", got)
	}
	if got := buildSessionSummaries(m.panes, m.table.Cursor()); len(got) != 1 || got[0].Parent != "200" {
		t.Fatalf("visible sessions = %#v, want only parent 200", got)
	}
}

func TestNarrowShortLayoutCollapsesTopStrip(t *testing.T) {
	m := newModel(Options{})
	m.width = 90
	m.height = detailHeight + 5 + 4

	layout := m.monitorLayout()

	if layout.TopStripHeight != 0 {
		t.Fatalf("TopStripHeight = %d, want collapsed strip", layout.TopStripHeight)
	}
	if layout.TableRows != 4 {
		t.Fatalf("TableRows = %d, want minimum table height without extra strip", layout.TableRows)
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

func TestMergeDegradedIssueStatuses(t *testing.T) {
	key := issueKey{Parent: "100", Num: 101}
	blocked := issueStatus{Title: "child", State: "OPEN", Wave: 2, WaveLabel: "wave2", Blockers: "OPEN #99", HasOpenBlockers: true}
	blockedRows := blocked
	blockedRows.BlockerRows = parseFormattedBlockers("OPEN #99")
	degraded := issueStatus{Title: "child", State: "OPEN", Wave: 1, WaveLabel: "wave1", Blockers: "-", HasOpenBlockers: false, WaveDegraded: true}
	unblocked := issueStatus{Title: "child", State: "OPEN", Wave: 2, WaveLabel: "wave2", Blockers: "resolved #99", HasOpenBlockers: false}
	cleared := issueStatus{Title: "child", State: "OPEN", Wave: 1, WaveLabel: "wave1", Blockers: "-", HasOpenBlockers: false}
	restored := blocked
	restored.WaveDegraded = true
	restoredRows := blockedRows
	restoredRows.WaveDegraded = true

	tests := []struct {
		name     string
		previous map[issueKey]issueStatus
		current  map[issueKey]issueStatus
		want     map[issueKey]issueStatus
	}{
		{
			name:     "degraded entry keeps old wave and blocker fields",
			previous: map[issueKey]issueStatus{key: blocked},
			current:  map[issueKey]issueStatus{key: degraded},
			want:     map[issueKey]issueStatus{key: restored},
		},
		{
			// A non-degraded "-" is a confirmed clear: blockers legitimately
			// removed must not be masked by stale data when an unrelated row
			// errored in the same refresh.
			name:     "legitimately cleared blockers are not restored",
			previous: map[issueKey]issueStatus{key: blocked},
			current:  map[issueKey]issueStatus{key: cleared},
			want:     map[issueKey]issueStatus{key: cleared},
		},
		{
			// Hydration failed but parent task-list rows still produced some
			// blocker text — the previous entry (computed with the child body)
			// is strictly better last-known data.
			name:     "degraded entry with partial parent-row blockers restores previous",
			previous: map[issueKey]issueStatus{key: blocked},
			current:  map[issueKey]issueStatus{key: {Title: "child", State: "OPEN", Wave: 1, Blockers: "OPEN #7", HasOpenBlockers: true, WaveDegraded: true}},
			want:     map[issueKey]issueStatus{key: restored},
		},
		{
			name:     "degraded entry restores previous structured blocker rows",
			previous: map[issueKey]issueStatus{key: blockedRows},
			current:  map[issueKey]issueStatus{key: {Title: "child", State: "OPEN", Wave: 1, Blockers: "OPEN #7", BlockerRows: parseFormattedBlockers("OPEN #7"), HasOpenBlockers: true, WaveDegraded: true}},
			want:     map[issueKey]issueStatus{key: restoredRows},
		},
		{
			name:     "fresh blocker data replaces old entry",
			previous: map[issueKey]issueStatus{key: blocked},
			current:  map[issueKey]issueStatus{key: unblocked},
			want:     map[issueKey]issueStatus{key: unblocked},
		},
		{
			name:     "new key passes through",
			previous: map[issueKey]issueStatus{},
			current:  map[issueKey]issueStatus{key: degraded},
			want:     map[issueKey]issueStatus{key: degraded},
		},
		{
			name:     "dropped key restored wholesale",
			previous: map[issueKey]issueStatus{key: blocked, {Parent: "100", Num: 102}: unblocked},
			current:  map[issueKey]issueStatus{key: degraded},
			want:     map[issueKey]issueStatus{key: restored, {Parent: "100", Num: 102}: unblocked},
		},
		{
			name:     "previous without blocker data does not mask new entry",
			previous: map[issueKey]issueStatus{key: degraded},
			current:  map[issueKey]issueStatus{key: {Title: "child", State: "OPEN", Blockers: ""}},
			want:     map[issueKey]issueStatus{key: {Title: "child", State: "OPEN", Blockers: ""}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeDegradedIssueStatuses(tt.previous, tt.current)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("mergeDegradedIssueStatuses = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestUpdateKeepsLastKnownBlockersOnDegradedRefresh(t *testing.T) {
	m := newModel(Options{})
	key := issueKey{Parent: "100", Num: 101}
	dropped := issueKey{Parent: "100", Num: 102}
	initial := map[issueKey]issueStatus{
		key:     {Title: "blocked child", State: "OPEN", Wave: 2, WaveLabel: "wave2", Blockers: "OPEN #99", HasOpenBlockers: true},
		dropped: {Title: "recorded child", State: "OPEN", Wave: 1, WaveLabel: "wave1", Blockers: "resolved #98"},
	}

	updated, _ := m.Update(ghLoadedMsg{issues: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	degraded := map[issueKey]issueStatus{
		key: {Title: "blocked child", State: "OPEN", Blockers: "-", HasOpenBlockers: false, WaveDegraded: true},
	}
	updated, _ = m.Update(ghLoadedMsg{issues: degraded, at: time.Unix(2, 0), err: errBoom})
	m = updated.(model)

	wantMerged := map[issueKey]issueStatus{
		key:     {Title: "blocked child", State: "OPEN", Wave: 2, WaveLabel: "wave2", Blockers: "OPEN #99", HasOpenBlockers: true, WaveDegraded: true},
		dropped: initial[dropped],
	}
	if !reflect.DeepEqual(m.issues, wantMerged) {
		t.Fatalf("issues after degraded refresh = %#v, want last-known %#v", m.issues, wantMerged)
	}

	recovered := map[issueKey]issueStatus{
		key: {Title: "blocked child", State: "OPEN", Wave: 2, WaveLabel: "wave2", Blockers: "resolved #99", HasOpenBlockers: false},
	}
	updated, _ = m.Update(ghLoadedMsg{issues: recovered, at: time.Unix(3, 0)})
	m = updated.(model)

	if !reflect.DeepEqual(m.issues, recovered) {
		t.Fatalf("issues after successful refresh = %#v, want wholesale replacement %#v", m.issues, recovered)
	}
}

func TestGHUpdateNotifiesTransitionsOnceAfterInitialSnapshot(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})
	initial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "merge me", State: "OPEN", PRs: []ghissue.PRRef{{Number: 901, State: "OPEN", CIStatus: "pass"}}},
		{Parent: "100", Num: 102}: {Title: "break me", State: "OPEN", PRs: []ghissue.PRRef{{Number: 902, State: "OPEN", CIStatus: "pass"}}},
		{Parent: "100", Num: 103}: {Title: "wait me", State: "OPEN", Blockers: "-"},
	}

	updated, cmd := m.Update(ghLoadedMsg{issues: initial, at: time.Unix(1, 0)})
	if cmd != nil {
		t.Fatal("initial GH snapshot returned notification command, want nil")
	}
	m = updated.(model)

	nextIssues := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "merge me", State: "CLOSED", PRs: []ghissue.PRRef{{Number: 901, State: "MERGED", CIStatus: "pass"}}},
		{Parent: "100", Num: 102}: {Title: "break me", State: "OPEN", PRs: []ghissue.PRRef{{Number: 902, State: "OPEN", CIStatus: "fail"}}},
		{Parent: "100", Num: 103}: {Title: "wait me", State: "OPEN", Blockers: "OPEN #99", HasOpenBlockers: true},
	}
	updated, cmd = m.Update(ghLoadedMsg{issues: nextIssues, at: time.Unix(2, 0)})
	if cmd == nil {
		t.Fatal("transition GH snapshot returned nil command, want notification command")
	}
	m = updated.(model)
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}
	updated, _ = m.Update(msg)
	m = updated.(model)

	gotKinds := []fanoutnotify.EventKind{}
	gotNums := []int{}
	for _, event := range notifier.events {
		gotKinds = append(gotKinds, event.Kind)
		gotNums = append(gotNums, event.IssueNum)
	}
	wantKinds := []fanoutnotify.EventKind{fanoutnotify.EventMerged, fanoutnotify.EventCIFailed, fanoutnotify.EventWaiting}
	wantNums := []int{101, 102, 103}
	if !reflect.DeepEqual(gotKinds, wantKinds) || !reflect.DeepEqual(gotNums, wantNums) {
		t.Fatalf("events kinds=%#v nums=%#v, want %#v/%#v", gotKinds, gotNums, wantKinds, wantNums)
	}
	if m.notifyErr != "" {
		t.Fatalf("notifyErr = %q, want empty", m.notifyErr)
	}
	if !strings.Contains(m.notice, "3 state changes") {
		t.Fatalf("notice = %q, want transition summary", m.notice)
	}

	_, cmd = m.Update(ghLoadedMsg{issues: nextIssues, at: time.Unix(3, 0)})
	if cmd != nil {
		t.Fatal("unchanged GH snapshot returned notification command, want nil")
	}
	if len(notifier.events) != 3 {
		t.Fatalf("notifier events after unchanged snapshot = %d, want 3", len(notifier.events))
	}
}

func TestGHUpdatePreservesNotificationBaselineOnPartialError(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})
	initial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "visible", State: "OPEN", PRs: []ghissue.PRRef{{Number: 901, State: "OPEN", CIStatus: "pass"}}},
		{Parent: "100", Num: 102}: {Title: "omitted", State: "OPEN", PRs: []ghissue.PRRef{{Number: 902, State: "OPEN", CIStatus: "pass"}}},
	}

	updated, _ := m.Update(ghLoadedMsg{issues: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	partial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "visible", State: "OPEN", PRs: []ghissue.PRRef{{Number: 901, State: "OPEN", CIStatus: "pass"}}},
	}
	updated, cmd := m.Update(ghLoadedMsg{issues: partial, at: time.Unix(2, 0), err: errBoom})
	if cmd != nil {
		t.Fatal("partial unchanged GH snapshot returned notification command, want nil")
	}
	m = updated.(model)

	recovered := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "visible", State: "OPEN", PRs: []ghissue.PRRef{{Number: 901, State: "OPEN", CIStatus: "pass"}}},
		{Parent: "100", Num: 102}: {Title: "omitted", State: "CLOSED", PRs: []ghissue.PRRef{{Number: 902, State: "MERGED", CIStatus: "pass"}}},
	}
	updated, cmd = m.Update(ghLoadedMsg{issues: recovered, at: time.Unix(3, 0)})
	if cmd == nil {
		t.Fatal("recovered GH snapshot returned nil command, want notification command")
	}
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}
	updated, _ = updated.(model).Update(msg)
	m = updated.(model)

	want := []fanoutnotify.Event{{Kind: fanoutnotify.EventMerged, Parent: "100", IssueNum: 102, Title: "omitted", PRNumber: 902, CIStatus: "pass"}}
	if !reflect.DeepEqual(notifier.events, want) {
		t.Fatalf("notifier events = %#v, want %#v", notifier.events, want)
	}
	if m.notifyErr != "" {
		t.Fatalf("notifyErr = %q, want empty", m.notifyErr)
	}
}

func TestGHUpdateDoesNotOverwriteNotificationBaselineOnPartialError(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})
	initial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "waiting", State: "OPEN", Blockers: "OPEN #99", HasOpenBlockers: true},
	}

	updated, _ := m.Update(ghLoadedMsg{issues: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	partial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "waiting", State: "OPEN", Blockers: "-", HasOpenBlockers: false},
	}
	updated, cmd := m.Update(ghLoadedMsg{issues: partial, at: time.Unix(2, 0), err: errBoom})
	if cmd != nil {
		t.Fatal("partial GH snapshot returned notification command, want nil")
	}
	m = updated.(model)

	_, cmd = m.Update(ghLoadedMsg{issues: initial, at: time.Unix(3, 0)})
	if cmd != nil {
		t.Fatal("recovered unchanged waiting snapshot returned notification command, want nil")
	}
	if len(notifier.events) != 0 {
		t.Fatalf("notifier events = %#v, want none", notifier.events)
	}
}

func TestGHUpdateNotifiesUsableTransitionOnPartialError(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})
	initial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "visible", State: "OPEN", PRs: []ghissue.PRRef{{Number: 901, State: "OPEN", CIStatus: "pass"}}},
		{Parent: "100", Num: 102}: {Title: "omitted", State: "OPEN", PRs: []ghissue.PRRef{{Number: 902, State: "OPEN", CIStatus: "pass"}}},
	}

	updated, _ := m.Update(ghLoadedMsg{issues: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	partial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "visible", State: "CLOSED", PRs: []ghissue.PRRef{{Number: 901, State: "MERGED", CIStatus: "pass"}}},
	}
	updated, cmd := m.Update(ghLoadedMsg{issues: partial, at: time.Unix(2, 0), err: errBoom})
	if cmd == nil {
		t.Fatal("partial transition GH snapshot returned nil command, want notification command")
	}
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}
	updated, _ = updated.(model).Update(msg)
	m = updated.(model)

	recovered := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "visible", State: "CLOSED", PRs: []ghissue.PRRef{{Number: 901, State: "MERGED", CIStatus: "pass"}}},
		{Parent: "100", Num: 102}: {Title: "omitted", State: "OPEN", PRs: []ghissue.PRRef{{Number: 902, State: "OPEN", CIStatus: "pass"}}},
	}
	_, cmd = m.Update(ghLoadedMsg{issues: recovered, at: time.Unix(3, 0)})
	if cmd != nil {
		t.Fatal("recovered already-notified snapshot returned notification command, want nil")
	}

	want := []fanoutnotify.Event{{Kind: fanoutnotify.EventMerged, Parent: "100", IssueNum: 101, Title: "visible", PRNumber: 901, CIStatus: "pass"}}
	if !reflect.DeepEqual(notifier.events, want) {
		t.Fatalf("notifier events = %#v, want %#v", notifier.events, want)
	}
	if m.notifyErr != "" {
		t.Fatalf("notifyErr = %q, want empty", m.notifyErr)
	}
}

func TestGHUpdateRecordsPartialRecoveryBeforeLaterTransition(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})
	initial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "flaky", State: "OPEN", PRs: []ghissue.PRRef{{Number: 901, State: "OPEN", CIStatus: "fail"}}},
	}

	updated, _ := m.Update(ghLoadedMsg{issues: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	recoveredPartial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "flaky", State: "OPEN", PRs: []ghissue.PRRef{{Number: 901, State: "OPEN", CIStatus: "pass"}}},
	}
	updated, cmd := m.Update(ghLoadedMsg{issues: recoveredPartial, at: time.Unix(2, 0), err: errBoom})
	if cmd != nil {
		t.Fatal("partial CI recovery returned notification command, want nil")
	}
	m = updated.(model)

	failedAgainPartial := map[issueKey]issueStatus{
		{Parent: "100", Num: 101}: {Title: "flaky", State: "OPEN", PRs: []ghissue.PRRef{{Number: 901, State: "OPEN", CIStatus: "fail"}}},
	}
	updated, cmd = m.Update(ghLoadedMsg{issues: failedAgainPartial, at: time.Unix(3, 0), err: errBoom})
	if cmd == nil {
		t.Fatal("partial CI failure returned nil command, want notification command")
	}
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}
	updated, _ = updated.(model).Update(msg)
	m = updated.(model)

	want := []fanoutnotify.Event{{Kind: fanoutnotify.EventCIFailed, Parent: "100", IssueNum: 101, Title: "flaky", PRNumber: 901, CIStatus: "fail"}}
	if !reflect.DeepEqual(notifier.events, want) {
		t.Fatalf("notifier events = %#v, want %#v", notifier.events, want)
	}
	if m.notifyErr != "" {
		t.Fatalf("notifyErr = %q, want empty", m.notifyErr)
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
	m.panes = applyIssueStatuses("/repo", m.panes, m.issues)
	m.refreshRows()

	got := m.View()
	if !strings.Contains(got, "total=3 merged=1 pending=2 blocked=1") {
		t.Fatalf("View() = %q, want HUD counts", got)
	}
}

func TestWatchTickRunsCycleAndRefreshesStateAndGH(t *testing.T) {
	runner := &fakeWatcherRunner{
		report: watch.Report{
			Launched: []watch.Action{{Issue: ghissue.Issue{Number: 101}}},
		},
	}
	m := newModel(Options{
		ProjectRoot:    "/repo",
		Watcher:        runner,
		WatchInterval:  time.Minute,
		WatchLabel:     "fanout:auto",
		StateInterval:  time.Second,
		GHInterval:     time.Second,
		DefaultAgent:   "codex",
		FocusPane:      func(string) error { return nil },
		PaneAlive:      func(string) bool { return true },
		LaunchPane:     func(LaunchRequest) error { return nil },
		LaunchShell:    func(ShellLaunchRequest) error { return nil },
		Notifier:       nil,
		lifecycle:      &fakeLifecycleRunner{},
		keyboard:       noopKeyboardProtocols{},
		ShellPaneAlive: func(string, string) bool { return true },
	})

	updated, cmd := m.Update(watchTickMsg(time.Unix(1, 0)))
	m = updated.(model)
	if !m.watchRunning {
		t.Fatal("watchRunning = false, want true while RunCycle command is outstanding")
	}
	if cmd == nil {
		t.Fatal("watch tick returned nil command, want RunCycle + next tick batch")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("watch tick command = %T len=%d, want two-command BatchMsg", batch, len(batch))
	}

	msg, ok := batch[0]().(watchDoneMsg)
	if !ok {
		t.Fatalf("watch RunCycle command returned %T, want watchDoneMsg", msg)
	}
	if runner.calls != 1 {
		t.Fatalf("RunCycle calls = %d, want 1", runner.calls)
	}

	updated, cmd = m.Update(msg)
	m = updated.(model)
	if m.watchRunning {
		t.Fatal("watchRunning = true after watchDoneMsg, want false")
	}
	if m.watchLaunched != 1 || m.watchErr != "" {
		t.Fatalf("watch status launched=%d err=%q, want 1 and empty", m.watchLaunched, m.watchErr)
	}
	if cmd == nil {
		t.Fatal("watchDoneMsg returned nil command, want state+GH reload batch")
	}
	reloadBatch, ok := cmd().(tea.BatchMsg)
	if !ok || len(reloadBatch) != 2 {
		t.Fatalf("watch done command = %T len=%d, want state+GH reload BatchMsg", reloadBatch, len(reloadBatch))
	}
}

func TestWatchLaunchFailureRendersFooter(t *testing.T) {
	m := newModel(Options{
		ProjectRoot: "/repo",
		Watcher:     &fakeWatcherRunner{},
		WatchLabel:  "fanout:auto",
	})
	m.width = 100
	m.height = 30
	report := watch.Report{
		Failures: []watch.Failure{
			{
				Issue:    ghissue.Issue{Number: 101},
				Stage:    watch.FailureLaunch,
				Err:      errBoom,
				Attempts: 3,
				Disabled: true,
			},
		},
	}

	updated, _ := m.Update(watchDoneMsg{report: report, at: time.Date(2026, 6, 20, 12, 34, 56, 0, time.UTC)})
	m = updated.(model)
	view := m.View()

	for _, want := range []string{"watch: disabled label=fanout:auto", "last=12:34:56", "launched=0", "err=#101 launch: boom; disabled"} {
		if !strings.Contains(view, want) {
			t.Fatalf("watch footer missing %q:\n%s", want, view)
		}
	}
}

func TestWatchTickIgnoredWhenWatcherDisabled(t *testing.T) {
	m := newModel(Options{})

	updated, cmd := m.Update(watchTickMsg(time.Unix(1, 0)))
	if cmd != nil {
		t.Fatal("watch tick without watcher returned command, want nil")
	}
	if updated.(model).watchRunning {
		t.Fatal("watchRunning = true without watcher, want false")
	}
}

func TestTopSessionTextKeepsActiveSessionVisible(t *testing.T) {
	panes := []paneView{
		{Parent: "100", IssueNum: 101},
		{Parent: "200", IssueNum: 201},
		{Parent: "300", IssueNum: 301},
		{Parent: "400", IssueNum: 401},
		{Parent: "500", IssueNum: 501},
	}
	sessions := buildSessionSummaries(panes, 4)

	got := topSessionText(sessions, 100)

	if !strings.Contains(got, "> 500") {
		t.Fatalf("top session strip = %q, want active final session visible", got)
	}
	if len([]rune(got)) > 100 {
		t.Fatalf("top session strip length = %d, want <= 100: %q", len([]rune(got)), got)
	}
}

func TestViewRendersAdaptiveSessionList(t *testing.T) {
	m := newModel(Options{ProjectRoot: "/repo"})
	m.width = 130
	m.height = 30
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 101, Name: "first", HasMergedPR: true, TmuxState: "live"},
		{Parent: "200", IssueNum: 201, Name: "second", Blocked: true},
	}
	m.resize()

	wide := m.View()
	for _, want := range []string{"Sessions 2", "> 100 t1 m1 p0 b0 l1", "|", "PARENT"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide view missing %q:\n%s", want, wide)
		}
	}

	m.width = 90
	m.resize()
	narrow := m.View()
	for _, want := range []string{"Sessions 2  [/] session", "> 100 t1 m1 p0 b0 l1", "PARENT"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("narrow view missing %q:\n%s", want, narrow)
		}
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

func TestFocusSelectedPanePausesKeyboardProtocolsUntilNextKey(t *testing.T) {
	protocols := &fakeKeyboardProtocols{}
	m := newModel(Options{
		FocusPane: func(string) error {
			return nil
		},
		PaneAlive: func(string) bool { return true },
		keyboard:  protocols,
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
	if protocols.disableCount != 1 || protocols.enableCount != 0 {
		t.Fatalf("protocol calls after focus = enable %d disable %d, want enable 0 disable 1", protocols.enableCount, protocols.disableCount)
	}

	updated, _ := m.Update(msg)
	m = updated.(model)
	if !m.keyboardPaused {
		t.Fatal("keyboardPaused = false, want true")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(model)
	if protocols.enableCount != 1 || protocols.disableCount != 1 {
		t.Fatalf("protocol calls after return key = enable %d disable %d, want enable 1 disable 1", protocols.enableCount, protocols.disableCount)
	}
	if m.keyboardPaused {
		t.Fatal("keyboardPaused = true after return key, want false")
	}
}

func TestFocusSelectedPaneRestoresKeyboardProtocolsOnFocusError(t *testing.T) {
	protocols := &fakeKeyboardProtocols{}
	m := newModel(Options{
		FocusPane: func(string) error {
			return errors.New("focus failed")
		},
		PaneAlive: func(string) bool { return true },
		keyboard:  protocols,
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
	updated, _ := m.Update(msg)
	m = updated.(model)

	if protocols.disableCount != 1 || protocols.enableCount != 1 {
		t.Fatalf("protocol calls after failed focus = enable %d disable %d, want enable 1 disable 1", protocols.enableCount, protocols.disableCount)
	}
	if m.keyboardPaused {
		t.Fatal("keyboardPaused = true after failed focus, want false")
	}
}

func TestEnableKeyboardProtocolsCmd(t *testing.T) {
	protocols := &fakeKeyboardProtocols{}
	m := newModel(Options{keyboard: protocols})

	msg := m.enableKeyboardProtocolsCmd()()
	if _, ok := msg.(keyboardProtocolsEnabledMsg); !ok {
		t.Fatalf("enableKeyboardProtocolsCmd() msg = %T, want keyboardProtocolsEnabledMsg", msg)
	}
	if protocols.enableCount != 1 || protocols.disableCount != 0 {
		t.Fatalf("protocol calls = enable %d disable %d, want enable 1 disable 0", protocols.enableCount, protocols.disableCount)
	}
}

func TestQuitDisablesKeyboardProtocols(t *testing.T) {
	protocols := &fakeKeyboardProtocols{}
	m := newModel(Options{keyboard: protocols})
	m.keyboardPaused = true

	next, cmd := m.quit()
	if cmd == nil {
		t.Fatal("quit() returned nil command, want tea.Quit")
	}
	m = next.(model)
	if protocols.disableCount != 1 {
		t.Fatalf("disableCount = %d, want 1", protocols.disableCount)
	}
	if m.keyboardPaused {
		t.Fatal("keyboardPaused = true after quit, want false")
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
	if got := m.table.Rows()[0][6]; got != "stale!" {
		t.Fatalf("table tmux cell = %q, want stale!", got)
	}
}

func TestFocusSelectedShellPaneRevalidatesShellKey(t *testing.T) {
	focusCalled := false
	paneAliveCalled := false
	var gotPaneID, gotShellKey string
	m := newModel(Options{
		FocusPane: func(string) error {
			focusCalled = true
			return nil
		},
		PaneAlive: func(string) bool {
			paneAliveCalled = true
			return true
		},
		ShellPaneAlive: func(paneID, shellKey string) bool {
			gotPaneID = paneID
			gotShellKey = shellKey
			return false
		},
	})
	m.allPanes = []paneView{{
		IssueNum:  -1,
		Kind:      state.PaneKindShell,
		Name:      "root terminal",
		PaneID:    "%1",
		ShellKey:  "shell-root",
		TmuxState: "live",
	}}
	m.refreshRows()

	cmd := m.focusSelectedCmd()
	if cmd == nil {
		t.Fatalf("focusSelectedCmd() returned nil, want shell identity check command")
	}
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(model)

	if gotPaneID != "%1" || gotShellKey != "shell-root" {
		t.Fatalf("ShellPaneAlive saw (%q, %q), want (%%1, shell-root)", gotPaneID, gotShellKey)
	}
	if focusCalled {
		t.Fatal("FocusPane was called after shell key revalidation failed")
	}
	if paneAliveCalled {
		t.Fatal("PaneAlive was called for a shell row; want shell-key revalidation")
	}
	if m.panes[0].TmuxState != "stale" {
		t.Fatalf("TmuxState = %q, want stale", m.panes[0].TmuxState)
	}
}

type fakeKeyboardProtocols struct {
	enableCount  int
	disableCount int
}

func (p *fakeKeyboardProtocols) Enable() {
	p.enableCount++
}

func (p *fakeKeyboardProtocols) Disable() {
	p.disableCount++
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

func TestPeekSelectedShellPaneRevalidatesShellKey(t *testing.T) {
	captureCalled := false
	paneAliveCalled := false
	var gotPaneID, gotShellKey string
	m := newModel(Options{
		CapturePaneOutput: func(string, int) (string, error) {
			captureCalled = true
			return "wrong pane output", nil
		},
		PaneAlive: func(string) bool {
			paneAliveCalled = true
			return true
		},
		ShellPaneAlive: func(paneID, shellKey string) bool {
			gotPaneID = paneID
			gotShellKey = shellKey
			return false
		},
	})
	m.detail.Width = 80
	m.detail.Height = 9
	m.allPanes = []paneView{{
		IssueNum:  -1,
		Kind:      state.PaneKindShell,
		Name:      "root terminal",
		PaneID:    "%1",
		ShellKey:  "shell-root",
		TmuxState: "live",
	}}
	m.refreshRows()

	cmd := m.peekSelectedCmd(true)
	if cmd == nil {
		t.Fatalf("peekSelectedCmd() returned nil, want shell identity check command")
	}
	msg, ok := cmd().(panePeekLoadedMsg)
	if !ok {
		t.Fatalf("peekSelectedCmd() msg = %T, want panePeekLoadedMsg", msg)
	}
	if !errors.Is(msg.err, errPaneNotAlive) {
		t.Fatalf("peek err = %v, want errPaneNotAlive", msg.err)
	}
	next, _ := m.Update(msg)
	m = next.(model)

	if gotPaneID != "%1" || gotShellKey != "shell-root" {
		t.Fatalf("ShellPaneAlive saw (%q, %q), want (%%1, shell-root)", gotPaneID, gotShellKey)
	}
	if captureCalled {
		t.Fatal("CapturePaneOutput was called after shell key revalidation failed")
	}
	if paneAliveCalled {
		t.Fatal("PaneAlive was called for a shell row; want shell-key revalidation")
	}
	if m.panes[0].TmuxState != "stale" {
		t.Fatalf("TmuxState = %q, want stale", m.panes[0].TmuxState)
	}
	if !strings.Contains(m.peek.Err, errPaneNotAlive.Error()) {
		t.Fatalf("peek err = %q, want errPaneNotAlive", m.peek.Err)
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

func TestPaneViewsFromSnapshotCarriesShellKey(t *testing.T) {
	snap := sessionview.Snapshot{
		Sessions: []sessionview.Session{{
			Parent: "@manual",
			Panes: []sessionview.PaneView{{
				Kind:         state.PaneKindShell,
				DisplayName:  "root terminal",
				Agent:        "shell",
				PaneID:       "%9",
				ShellKey:     "shell-root",
				WorktreePath: "/repo",
				TmuxState:    "live",
				Derived:      sessionview.PaneDerived{Name: "root terminal"},
			}},
		}},
	}

	got := paneViewsFromSnapshot("/repo", snap)

	if len(got) != 1 {
		t.Fatalf("paneViewsFromSnapshot len = %d, want 1", len(got))
	}
	if got[0].ShellKey != "shell-root" {
		t.Fatalf("ShellKey = %q, want shell-root", got[0].ShellKey)
	}
}

func TestFilterPaneViewsSearchesTextAndPredicates(t *testing.T) {
	panes := []paneView{
		{
			Parent:      "142",
			IssueNum:    115,
			Name:        "state agent wave filters",
			TmuxState:   "live",
			IssueState:  "OPEN",
			PRSummary:   "#201 open",
			CIStatus:    "pass",
			BranchName:  "feat/dashboard-filter",
			DiffSummary: "+12/-3",
			DirtyState:  "dirty",
			Agent:       "codex",
			Wave:        2,
			WaveLabel:   "wave5",
			WaveBadge:   "W2 blocked",
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
			WaveLabel:  "wave4",
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
	got = filterPaneViews(panes, "wave:wave2")
	if len(got) != 1 || got[0].IssueNum != 115 {
		t.Fatalf("filterPaneViews dependency wave match = %#v, want only #115", got)
	}
	got = filterPaneViews(panes, "#115")
	if len(got) != 1 || got[0].IssueNum != 115 {
		t.Fatalf("filterPaneViews issue search = %#v, want only #115", got)
	}
	got = filterPaneViews(panes, "dirty")
	if len(got) != 1 || got[0].IssueNum != 115 {
		t.Fatalf("filterPaneViews dirty search = %#v, want only #115", got)
	}
	got = filterPaneViews(panes, "+12/-3")
	if len(got) != 1 || got[0].IssueNum != 115 {
		t.Fatalf("filterPaneViews diff search = %#v, want only #115", got)
	}
	got = filterPaneViews([]paneView{{TaskID: "api-client", Name: "plan task"}}, "task:api")
	if len(got) != 1 || got[0].TaskID != "api-client" {
		t.Fatalf("filterPaneViews task predicate = %#v, want api-client task", got)
	}
}

func TestRefreshRowsKeepsFilterDuringStateAndGHUpdates(t *testing.T) {
	m := newModel(Options{})
	m.filterQuery = "agent:codex state:open"
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 1, Name: "one", Agent: "codex", IssueState: "OPEN"},
		{Parent: "100", IssueNum: 2, Name: "two", Agent: "claude", IssueState: "OPEN"},
	}
	m.refreshRows()

	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("initial filtered rows = %d, want 1", got)
	}

	updated, _ := m.Update(stateLoadedMsg{
		panes: []paneView{
			{Parent: "100", IssueNum: 1, Name: "one", Agent: "codex", IssueState: "-"},
			{Parent: "100", IssueNum: 2, Name: "two", Agent: "claude", IssueState: "-"},
			{Parent: "100", IssueNum: 3, Name: "three", Agent: "codex", IssueState: "-"},
		},
		at: time.Unix(10, 0),
	})
	m = updated.(model)

	updated, _ = m.Update(ghLoadedMsg{
		issues: map[issueKey]issueStatus{
			{Parent: "100", Num: 1}: {State: "CLOSED"},
			{Parent: "100", Num: 2}: {State: "OPEN"},
			{Parent: "100", Num: 3}: {State: "OPEN"},
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

func TestRecordedIssueNumsFiltersNonPositive(t *testing.T) {
	got := recordedIssueNums([]state.Pane{
		{IssueNum: 5},
		{IssueNum: 0},
		{IssueNum: -1},
		{IssueNum: 6},
	})
	want := []int{5, 6}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recordedIssueNums() = %#v, want %#v", got, want)
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
	m := newModel(Options{ProjectRoot: "/repo", WatcherRunningLabel: "fanout:test-running", lifecycle: runner})
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
	if runner.watcherRunningLabel != "fanout:test-running" {
		t.Fatalf("Close watcherRunningLabel = %q, want fanout:test-running", runner.watcherRunningLabel)
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

func TestLifecycleCmdRoutesToPaneSourceProjectRoot(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})

	// A pane recorded in a sibling worktree must be closed against that
	// worktree's state.json, not m.opts.ProjectRoot's (which may be empty).
	pane := paneView{Parent: "84", IssueNum: 101, Name: "child", sourceProjectRoot: "/sibling"}
	cmd := m.lifecycleCmd(pendingLifecycleAction{action: actionClose, pane: pane})
	if _, ok := cmd().(lifecycleDoneMsg); !ok {
		t.Fatal("lifecycleCmd did not return lifecycleDoneMsg")
	}
	if runner.projectRoot != "/sibling" || runner.statePath != state.Path("/sibling") {
		t.Fatalf("Close opts = %q/%q, want sibling root /sibling", runner.projectRoot, runner.statePath)
	}
}

func TestLifecycleCloseRunsAcrossAllOwningRoots(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})

	// The same logical child collapsed from two worktrees: close must remove it
	// from both stores, not just the winning one.
	pane := paneView{
		Parent:             "84",
		IssueNum:           101,
		Name:               "child",
		sourceProjectRoot:  "/wt-a",
		sourceProjectRoots: []string{"/wt-a", "/wt-b"},
	}
	cmd := m.lifecycleCmd(pendingLifecycleAction{action: actionClose, pane: pane})
	if _, ok := cmd().(lifecycleDoneMsg); !ok {
		t.Fatal("lifecycleCmd did not return lifecycleDoneMsg")
	}
	if len(runner.closeRoots) != 2 {
		t.Fatalf("close ran on roots %v, want both /wt-a and /wt-b", runner.closeRoots)
	}
	got := map[string]bool{}
	for _, r := range runner.closeRoots {
		got[r] = true
	}
	if !got["/wt-a"] || !got["/wt-b"] {
		t.Fatalf("close roots = %v, want /wt-a and /wt-b", runner.closeRoots)
	}
}

func TestLifecycleCmdFallsBackToProjectRootWhenSourceEmpty(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})

	pane := paneView{Parent: "84", IssueNum: 101, Name: "child"} // no sourceProjectRoot
	cmd := m.lifecycleCmd(pendingLifecycleAction{action: actionClose, pane: pane})
	if _, ok := cmd().(lifecycleDoneMsg); !ok {
		t.Fatal("lifecycleCmd did not return lifecycleDoneMsg")
	}
	if runner.projectRoot != "/repo" || runner.statePath != state.Path("/repo") {
		t.Fatalf("Close opts = %q/%q, want /repo fallback", runner.projectRoot, runner.statePath)
	}
}

func TestLifecycleCleanupRunsAcrossAllSourceRootsForParent(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
	// Parent #84 spread across worktrees: pane "a" was recorded in two worktrees
	// and collapsed by the loader (sourceProjectRoots has both), "b" in one, "d"
	// under a numeric alias ("0084") that must still match, plus a synthetic
	// not-started row (no source root) that must add no spurious target.
	m.allPanes = []paneView{
		{Parent: "84", IssueNum: 101, Name: "a", sourceProjectRoot: "/wt-a", sourceProjectRoots: []string{"/wt-a", "/wt-x"}},
		{Parent: "84", IssueNum: 102, Name: "b", sourceProjectRoot: "/wt-b", sourceProjectRoots: []string{"/wt-b"}},
		{Parent: "0084", IssueNum: 104, Name: "d", sourceProjectRoot: "/wt-c", sourceProjectRoots: []string{"/wt-c"}},
		{Parent: "84", IssueNum: 103, Name: "c"},
	}

	cmd := m.lifecycleCmd(pendingLifecycleAction{action: actionCleanup, pane: m.allPanes[0]})
	if _, ok := cmd().(lifecycleDoneMsg); !ok {
		t.Fatal("lifecycleCmd did not return lifecycleDoneMsg")
	}
	got := map[string]bool{}
	for _, r := range runner.cleanupRoots {
		got[r] = true
	}
	want := []string{"/wt-a", "/wt-x", "/wt-b", "/wt-c"}
	if len(runner.cleanupRoots) != len(want) {
		t.Fatalf("cleanup ran on roots %v, want %v (collapsed + alias included)", runner.cleanupRoots, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("cleanup roots = %v, missing %s", runner.cleanupRoots, w)
		}
	}
}

func TestPaneViewsFromSnapshotCarriesSourceProjectRoot(t *testing.T) {
	snap := sessionview.Snapshot{Sessions: []sessionview.Session{{
		Parent: "100",
		Panes: []sessionview.PaneView{{
			IssueNum:           101,
			PaneID:             "%1",
			SourceProjectRoot:  "/sibling",
			SourceProjectRoots: []string{"/sibling", "/sibling2"},
			WorktreePath:       "/sibling/.fanout/worktrees/x",
		}},
	}}}
	out := paneViewsFromSnapshot("/repo", snap)
	if len(out) != 1 {
		t.Fatalf("want 1 pane, got %d", len(out))
	}
	if out[0].sourceProjectRoot != "/sibling" {
		t.Fatalf("sourceProjectRoot = %q, want /sibling", out[0].sourceProjectRoot)
	}
	if len(out[0].sourceProjectRoots) != 2 {
		t.Fatalf("sourceProjectRoots = %v, want both owning roots carried", out[0].sourceProjectRoots)
	}
}

func TestLifecycleKeysRoutePlanTaskRows(t *testing.T) {
	tests := []struct {
		key    string
		action lifecycleAction
		check  func(*testing.T, *fakeLifecycleRunner)
	}{
		{
			key:    "c",
			action: actionClose,
			check: func(t *testing.T, runner *fakeLifecycleRunner) {
				t.Helper()
				if runner.closeTaskParent != "plan:launch-plan" || runner.closeTaskID != "api-client" {
					t.Fatalf("CloseTask called with parent=%q task=%q, want plan/api-client", runner.closeTaskParent, runner.closeTaskID)
				}
			},
		},
		{
			key:    "m",
			action: actionMerge,
			check: func(t *testing.T, runner *fakeLifecycleRunner) {
				t.Helper()
				if runner.mergeTaskParent != "plan:launch-plan" || runner.mergeTaskID != "api-client" {
					t.Fatalf("MergeTask called with parent=%q task=%q, want plan/api-client", runner.mergeTaskParent, runner.mergeTaskID)
				}
			},
		},
		{
			key:    "x",
			action: actionCleanup,
			check: func(t *testing.T, runner *fakeLifecycleRunner) {
				t.Helper()
				if runner.cleanupPlanParent != "plan:launch-plan" {
					t.Fatalf("CleanupPlan parent = %q, want plan:launch-plan", runner.cleanupPlanParent)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			runner := &fakeLifecycleRunner{code: exitcode.OK}
			m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
			m.allPanes = []paneView{{Parent: "plan:launch-plan", IssueNum: 0, TaskID: "api-client", Name: "task"}}
			m.refreshRows()

			updated, cmd := m.Update(keyRunes(tc.key))
			m = updated.(model)
			if cmd != nil {
				t.Fatalf("Update(%q) returned command before confirmation", tc.key)
			}
			if m.pendingAction == nil || m.pendingAction.action != tc.action {
				t.Fatalf("pendingAction = %#v, want %s", m.pendingAction, tc.action)
			}
			wantMessage := "api-client"
			if tc.action == actionCleanup {
				wantMessage = "plan:launch-plan"
			}
			if !strings.Contains(m.actionMessage, wantMessage) {
				t.Fatalf("actionMessage = %q, want confirmation containing %q", m.actionMessage, wantMessage)
			}

			updated, cmd = m.Update(keyRunes("y"))
			if cmd == nil {
				t.Fatal("confirm returned nil command, want lifecycle command")
			}
			m = updated.(model)
			if !m.actionRunning {
				t.Fatal("actionRunning = false, want true while command runs")
			}
			if _, ok := cmd().(lifecycleDoneMsg); !ok {
				t.Fatalf("lifecycle command did not return lifecycleDoneMsg")
			}
			tc.check(t, runner)
		})
	}
}

func TestLifecycleMergeAndCleanupSkipShellRows(t *testing.T) {
	for _, key := range []string{"m", "x"} {
		t.Run(key, func(t *testing.T) {
			runner := &fakeLifecycleRunner{code: exitcode.OK}
			m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
			m.allPanes = []paneView{{Parent: "@manual", IssueNum: -1, Kind: state.PaneKindShell, Name: "root terminal"}}
			m.refreshRows()

			updated, cmd := m.Update(keyRunes(key))
			m = updated.(model)
			if cmd != nil {
				t.Fatalf("Update(%q) returned command for shell row, want nil", key)
			}
			if m.pendingAction != nil {
				t.Fatalf("pendingAction = %#v, want nil", m.pendingAction)
			}
			if !strings.Contains(m.actionMessage, "unavailable for shell terminal") {
				t.Fatalf("actionMessage = %q, want shell unavailable", m.actionMessage)
			}
			if runner.mergeTaskID != "" || runner.cleanupParent != "" || runner.cleanupPlanParent != "" {
				t.Fatalf("lifecycle runner was called: %+v", runner)
			}
		})
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
	code                exitcode.Code
	projectRoot         string
	statePath           string
	closeParent         string
	closeIssue          int
	closeTaskParent     string
	closeTaskID         string
	mergeTaskParent     string
	mergeTaskID         string
	cleanupParent       string
	cleanupPlanParent   string
	watcherRunningLabel string
	cleanupRoots        []string
	closeRoots          []string
}

type fakeTransitionNotifier struct {
	events []fanoutnotify.Event
	err    error
}

func (f *fakeTransitionNotifier) Notify(events []fanoutnotify.Event) error {
	f.events = append(f.events, events...)
	return f.err
}

func (f *fakeLifecycleRunner) Close(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	f.projectRoot = opts.ProjectRoot
	f.statePath = opts.StatePath
	f.watcherRunningLabel = opts.WatcherRunningLabel
	f.closeParent = parent
	f.closeIssue = issueNum
	f.closeRoots = append(f.closeRoots, opts.ProjectRoot)
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake close\n")
	return f.code
}

func (f *fakeLifecycleRunner) CloseTask(opts lifecycle.Options, parent, taskID string, lg lifecycle.Logger) exitcode.Code {
	f.projectRoot = opts.ProjectRoot
	f.statePath = opts.StatePath
	f.closeTaskParent = parent
	f.closeTaskID = taskID
	f.closeRoots = append(f.closeRoots, opts.ProjectRoot)
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake close task\n")
	return f.code
}

func (f *fakeLifecycleRunner) Merge(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake merge\n")
	return f.code
}

func (f *fakeLifecycleRunner) MergeTask(opts lifecycle.Options, parent, taskID string, lg lifecycle.Logger) exitcode.Code {
	f.mergeTaskParent = parent
	f.mergeTaskID = taskID
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake merge task\n")
	return f.code
}

func (f *fakeLifecycleRunner) Cleanup(opts lifecycle.Options, parent string, lg lifecycle.Logger) exitcode.Code {
	f.cleanupParent = parent
	f.cleanupRoots = append(f.cleanupRoots, opts.ProjectRoot)
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake cleanup\n")
	return f.code
}

func (f *fakeLifecycleRunner) CleanupPlan(opts lifecycle.Options, parent string, lg lifecycle.Logger) exitcode.Code {
	f.cleanupPlanParent = parent
	f.cleanupRoots = append(f.cleanupRoots, opts.ProjectRoot)
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake cleanup plan\n")
	return f.code
}

func installTUIFakeGH(t *testing.T, prListOutput string) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := filepath.Join(dir, "gh")
	body := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GH_FAKE_ARGS"
if [[ "$1 $2" == "repo view" ]]; then
  printf 'o/n'
  exit 0
fi
if [[ "$1 $2" == "pr list" ]]; then
  printf '%s' "$GH_FAKE_PR_LIST"
  exit 0
fi
printf 'unexpected gh command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_FAKE_ARGS", argsPath)
	t.Setenv("GH_FAKE_PR_LIST", prListOutput)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
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

func TestShellTerminalKeysLaunchRootAndSelectedWorktree(t *testing.T) {
	var got []ShellLaunchRequest
	m := newModel(Options{
		ProjectRoot: "/repo",
		LaunchShell: func(req ShellLaunchRequest) error {
			got = append(got, req)
			return nil
		},
	})
	m.allPanes = []paneView{{
		Parent:       "100",
		IssueNum:     101,
		Name:         "child",
		WorktreePath: ".fanout/worktrees/child",
		worktreeAbs:  "/repo/.fanout/worktrees/child",
	}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("A"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("A returned nil command, want shell launch")
	}
	msg, ok := cmd().(launchShellMsg)
	if !ok {
		t.Fatalf("A command returned %T, want launchShellMsg", msg)
	}
	if msg.err != nil {
		t.Fatalf("A launch error = %v", msg.err)
	}
	if len(got) != 1 || got[0].TargetPath != "/repo/.fanout/worktrees/child" || got[0].Root || got[0].Source != "#101" {
		t.Fatalf("worktree shell request = %#v", got)
	}

	updated, cmd = m.Update(msg)
	m = updated.(model)
	if !strings.Contains(m.notice, "opened terminal for #101") {
		t.Fatalf("notice after worktree shell = %q", m.notice)
	}
	if cmd == nil {
		t.Fatal("shell success returned nil command, want state reload")
	}

	updated, cmd = m.Update(keyRunes("t"))
	if cmd == nil {
		t.Fatal("t returned nil command, want shell launch")
	}
	m = updated.(model)
	msg, ok = cmd().(launchShellMsg)
	if !ok {
		t.Fatalf("t command returned %T, want launchShellMsg", msg)
	}
	if len(got) != 2 || got[1].TargetPath != "/repo" || !got[1].Root {
		t.Fatalf("root shell request = %#v", got)
	}
}

func TestShellTerminalKeyRequiresSelectedWorktree(t *testing.T) {
	called := false
	m := newModel(Options{
		ProjectRoot: "/repo",
		LaunchShell: func(ShellLaunchRequest) error {
			called = true
			return nil
		},
	})
	m.allPanes = []paneView{{Parent: "100", IssueNum: 101, Name: "queued"}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("A"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("A returned command for row without worktree, want nil")
	}
	if called {
		t.Fatal("LaunchShell was called for row without worktree")
	}
	if !strings.Contains(m.notice, "no worktree path") {
		t.Fatalf("notice = %q, want no worktree path", m.notice)
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

func TestNewPaneFormPromptNewlineKeysDoNotSubmit(t *testing.T) {
	called := false
	m := newModel(Options{
		LaunchPane: func(LaunchRequest) error {
			called = true
			return nil
		},
	})
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("first")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = updated.(model)

	if called {
		t.Fatal("LaunchPane was called for ctrl+j")
	}
	if got := m.newPane.prompt.Value(); got != "first\n" {
		t.Fatalf("prompt after ctrl+j = %q, want newline", got)
	}
}

func TestNewPaneFormSubmitsMultilinePrompt(t *testing.T) {
	var got LaunchRequest
	m := newModel(Options{
		DefaultAgent: "codex",
		LaunchPane: func(req LaunchRequest) error {
			got = req
			return nil
		},
	})
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("Inspect the API\nCheck handlers")
	m.newPane.slug.SetValue("inspect-api")

	cmd := m.submitNewPane()
	if cmd == nil {
		t.Fatal("submitNewPane returned nil command")
	}
	_ = cmd()

	want := LaunchRequest{Prompt: "Inspect the API\nCheck handlers", Agent: "codex", Slug: "inspect-api"}
	if got != want {
		t.Fatalf("launch request = %#v, want %#v", got, want)
	}
}

func TestNewPaneViewRendersModalOverMonitor(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 30
	m.allPanes = []paneView{{Parent: "100", IssueNum: 1, Name: "existing", TmuxState: "live"}}
	m.refreshRows()
	m.openNewPaneForm()

	view := m.View()
	for _, want := range []string{"PARENT", "existing", "New agent pane", "shift+enter newline"} {
		if !strings.Contains(view, want) {
			t.Fatalf("modal view missing %q:\n%s", want, view)
		}
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

func TestMergeDegradedIssueStatusKeepsWaveOfUnblockedPrevious(t *testing.T) {
	// Previously confirmed unblocked ("-") but with valid wave fields: a
	// degraded refresh must not blank Wave/WaveLabel.
	key := issueKey{Parent: "100", Num: 101}
	previous := map[issueKey]issueStatus{
		key: {Title: "child", State: "OPEN", Wave: 2, WaveLabel: "wave2", Blockers: "-"},
	}
	current := map[issueKey]issueStatus{
		key: {Title: "child", State: "OPEN", Blockers: "-", WaveDegraded: true},
	}

	got := mergeDegradedIssueStatuses(previous, current)
	want := issueStatus{Title: "child", State: "OPEN", Wave: 2, WaveLabel: "wave2", Blockers: "-", WaveDegraded: true}
	if !reflect.DeepEqual(got[key], want) {
		t.Fatalf("merged = %#v, want %#v", got[key], want)
	}
}
