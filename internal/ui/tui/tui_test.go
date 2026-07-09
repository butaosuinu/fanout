package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	fanoutnotify "github.com/butaosuinu/fanout/internal/infra/notify"
	fanoutsettings "github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
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
	tmuxPanes := []tmuxrun.LivePane{
		{ID: "%2", Title: "running title", AgentState: "running"},
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
	if second.AgentState != "running" {
		t.Fatalf("AgentState = %q, want running forwarded from live pane", second.AgentState)
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
		// buildPaneViews injects state directly (no MergedStateLoader), so panes
		// carry no source root and the task key's Source is empty.
		keyForTask("plan:alpha", "task-a", ""): {
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

func TestBuildPaneViewsKeysTaskStatusBySourceRoot(t *testing.T) {
	// Same plan:<slug>/<taskId> in two worktrees with different branches: each row
	// must receive its own branch's status, not have the first applied to both.
	mergedAt := "2026-06-13T00:00:00Z"
	issues := map[issueKey]issueStatus{
		keyForTask("plan:launch", "api", "/wt-a"): {
			State: "UNKNOWN",
			PRs:   []ghissue.PRRef{{Number: 11, State: "MERGED", MergedAt: &mergedAt}},
		},
		keyForTask("plan:launch", "api", "/wt-b"): {
			State: "UNKNOWN",
			PRs:   []ghissue.PRRef{{Number: 22, State: "OPEN"}},
		},
	}
	panes := []paneView{
		{Parent: "plan:launch", IssueNum: 0, TaskID: "api", PaneID: "%1", sourceProjectRoot: "/wt-a"},
		{Parent: "plan:launch", IssueNum: 0, TaskID: "api", PaneID: "%2", sourceProjectRoot: "/wt-b"},
	}
	out := applyIssueStatuses("/repo", panes, issues)

	bySrc := map[string]paneView{}
	for _, p := range out {
		bySrc[p.sourceProjectRoot] = p
	}
	if got := bySrc["/wt-a"]; !got.HasMergedPR || got.PRSummary != "#11 merged" {
		t.Fatalf("/wt-a status = pr:%q merged:%v, want #11 merged", got.PRSummary, got.HasMergedPR)
	}
	if got := bySrc["/wt-b"]; got.HasMergedPR || got.PRSummary != "#22 open" {
		t.Fatalf("/wt-b status = pr:%q merged:%v, want #22 open (not the /wt-a status)", got.PRSummary, got.HasMergedPR)
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
	// loadIssueStatuses reads through MergedStateLoader; outside a git repo it
	// falls back to {root}, tagging the task row's source with root.
	status, ok := statuses[keyForTask("plan:alpha", "task-a", root)]
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

func TestActivePaneMessageSyncsCursorAndPeek(t *testing.T) {
	var captured string
	m := newModel(Options{
		CapturePaneOutput: func(paneID string, _ int) (string, error) {
			captured = paneID
			return "latest output", nil
		},
	})
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 101, Name: "one", PaneID: "%1", TmuxState: "live"},
		{Parent: "100", IssueNum: 102, Name: "two", PaneID: "%2", TmuxState: "live"},
	}
	m.refreshRows()

	updated, cmd := m.Update(activePaneMsg{paneID: "%2"})
	m = updated.(model)

	if got := m.table.Cursor(); got != 1 {
		t.Fatalf("cursor after active pane sync = %d, want 1", got)
	}
	if cmd == nil {
		t.Fatal("active pane sync returned nil command, want peek refresh")
	}
	msg, ok := cmd().(panePeekLoadedMsg)
	if !ok {
		t.Fatalf("active pane sync command = %T, want panePeekLoadedMsg", msg)
	}
	if msg.paneID != "%2" || captured != "%2" {
		t.Fatalf("peek pane = msg %q captured %q, want %%2", msg.paneID, captured)
	}
}

func TestActivePaneMessageIgnoresUnselectableRows(t *testing.T) {
	tests := []struct {
		name   string
		panes  []paneView
		filter string
		active string
	}{
		{
			name: "filtered out pane",
			panes: []paneView{
				{IssueNum: 1, Name: "visible", PaneID: "%1", TmuxState: "live"},
				{IssueNum: 2, Name: "hidden", PaneID: "%2", TmuxState: "live"},
			},
			filter: "visible",
			active: "%2",
		},
		{
			name: "stale pane",
			panes: []paneView{
				{IssueNum: 1, Name: "live", PaneID: "%1", TmuxState: "live"},
				{IssueNum: 2, Name: "stale", PaneID: "%2", TmuxState: "stale"},
			},
			active: "%2",
		},
		{
			name: "unrecorded pane",
			panes: []paneView{
				{IssueNum: 1, Name: "live", PaneID: "%1", TmuxState: "live"},
			},
			active: "%9",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(Options{CapturePaneOutput: func(string, int) (string, error) {
				t.Fatal("CapturePaneOutput should not run when cursor does not move")
				return "", nil
			}})
			m.allPanes = tt.panes
			m.filterQuery = tt.filter
			m.refreshRows()

			updated, cmd := m.Update(activePaneMsg{paneID: tt.active})
			m = updated.(model)

			if got := m.table.Cursor(); got != 0 {
				t.Fatalf("cursor after ignored active pane = %d, want 0", got)
			}
			if cmd != nil {
				t.Fatalf("active pane sync command = %T, want nil", cmd)
			}
		})
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

func TestStateUpdatePrimesAgentNotificationsOnInitialSnapshot(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})

	updated, cmd := m.Update(stateLoadedMsg{
		panes: []paneView{
			{Parent: "100", IssueNum: 101, Name: "existing plan", TmuxState: "live", AgentState: "plan"},
			{Parent: "100", IssueNum: 102, Name: "existing blocked", TmuxState: "live", AgentState: "blocked"},
			{Parent: "100", IssueNum: 103, Name: "existing done", TmuxState: "live", AgentState: "done"},
		},
		at: time.Unix(1, 0),
	})
	if cmd != nil {
		t.Fatal("initial state snapshot returned notification command, want nil")
	}
	m = updated.(model)
	if !m.agentPrimed {
		t.Fatal("agentPrimed = false, want true after initial state snapshot")
	}
	if len(notifier.events) != 0 {
		t.Fatalf("notifier events = %#v, want none", notifier.events)
	}
}

func TestAgentTransitionKindMatrix(t *testing.T) {
	tests := []struct {
		name string
		prev string
		next string
		want fanoutnotify.EventKind
		ok   bool
	}{
		{name: "running to plan", prev: "running", next: "plan", want: fanoutnotify.EventAgentPlan, ok: true},
		{name: "working to plan", prev: "working", next: "plan", want: fanoutnotify.EventAgentPlan, ok: true},
		{name: "blocked to plan", prev: "blocked", next: "plan", want: fanoutnotify.EventAgentPlan, ok: true},
		{name: "idle to plan", prev: "idle", next: "plan", want: fanoutnotify.EventAgentPlan, ok: true},
		{name: "done to plan", prev: "done", next: "plan", want: fanoutnotify.EventAgentPlan, ok: true},
		{name: "running to done", prev: "running", next: "done", want: fanoutnotify.EventAgentDone, ok: true},
		{name: "working to done", prev: "working", next: "done", want: fanoutnotify.EventAgentDone, ok: true},
		{name: "plan to done", prev: "plan", next: "done", want: fanoutnotify.EventAgentDone, ok: true},
		{name: "blocked to done", prev: "blocked", next: "done", want: fanoutnotify.EventAgentDone, ok: true},
		{name: "idle to done", prev: "idle", next: "done", want: fanoutnotify.EventAgentDone, ok: true},
		{name: "running to blocked", prev: "running", next: "blocked", want: fanoutnotify.EventAgentBlocked, ok: true},
		{name: "working to blocked", prev: "working", next: "blocked", want: fanoutnotify.EventAgentBlocked, ok: true},
		{name: "idle to blocked", prev: "idle", next: "blocked", want: fanoutnotify.EventAgentBlocked, ok: true},
		{name: "plan to blocked", prev: "plan", next: "blocked", want: fanoutnotify.EventAgentBlocked, ok: true},
		{name: "done to blocked", prev: "done", next: "blocked", want: fanoutnotify.EventAgentBlocked, ok: true},
		{name: "unchanged running", prev: "running", next: "running", ok: false},
		{name: "repeated done", prev: "done", next: "done", ok: false},
		{name: "unknown next", prev: "running", next: "surprised", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := agentTransitionKind(tt.prev, tt.next)
			if ok != tt.ok {
				t.Fatalf("agentTransitionKind(%q, %q) ok = %v, want %v", tt.prev, tt.next, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("agentTransitionKind(%q, %q) = %q, want %q", tt.prev, tt.next, got, tt.want)
			}
		})
	}
}

func TestStateUpdateNotifiesAgentTransitionsOnce(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})

	initial := []paneView{
		{Parent: "100", IssueNum: 101, Name: "issue work", PaneID: "%1", TmuxState: "live", AgentState: "running"},
		{Parent: "100", IssueNum: 102, Name: "issue needs approval", PaneID: "%2", TmuxState: "live", AgentState: "plan"},
		{Parent: "plan:alpha", TaskID: "api-client", Name: "API client", PaneID: "%3", SourceKey: "tasksrc", TmuxState: "live", AgentState: "working", sourceProjectRoot: "/wt/task"},
		{Parent: "@manual", IssueNum: -1, Kind: state.PaneKindAttachedAgent, Name: "manual session", PaneID: "%4", SourceKey: "manualsrc", TmuxState: "live", AgentState: "working", sourceProjectRoot: "/wt/manual"},
	}
	updated, _ := m.Update(stateLoadedMsg{panes: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	next := []paneView{
		{Parent: "100", IssueNum: 101, Name: "issue work", PaneID: "%1", TmuxState: "live", AgentState: "plan"},
		{Parent: "100", IssueNum: 102, Name: "issue needs approval", PaneID: "%2", TmuxState: "live", AgentState: "blocked"},
		{Parent: "plan:alpha", TaskID: "api-client", Name: "API client", PaneID: "%3", SourceKey: "tasksrc", TmuxState: "live", AgentState: "done", sourceProjectRoot: "/wt/task"},
		{Parent: "@manual", IssueNum: -1, Kind: state.PaneKindAttachedAgent, Name: "manual session", PaneID: "%4", SourceKey: "manualsrc", TmuxState: "live", AgentState: "blocked", sourceProjectRoot: "/wt/manual"},
	}
	updated, cmd := m.Update(stateLoadedMsg{panes: next, at: time.Unix(2, 0)})
	if cmd == nil {
		t.Fatal("agent transition snapshot returned nil command, want notification command")
	}
	m = updated.(model)
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}
	updated, _ = m.Update(msg)
	m = updated.(model)

	gotKinds := []fanoutnotify.EventKind{}
	gotSubjects := []string{}
	for _, event := range notifier.events {
		gotKinds = append(gotKinds, event.Kind)
		gotSubjects = append(gotSubjects, event.Message())
	}
	wantKinds := []fanoutnotify.EventKind{
		fanoutnotify.EventAgentPlan,
		fanoutnotify.EventAgentBlocked,
		fanoutnotify.EventAgentBlocked,
		fanoutnotify.EventAgentDone,
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("event kinds = %#v, want %#v", gotKinds, wantKinds)
	}
	for _, msg := range gotSubjects {
		if strings.Contains(msg, "#0") {
			t.Fatalf("agent notification %q contains #0 label", msg)
		}
	}
	gotMessageText := strings.Join(gotSubjects, "\n")
	for _, want := range []string{"#101 issue work plan ready", "#102 issue needs approval waiting for input", "task api-client API client agent exited", "manual session waiting for input"} {
		if !strings.Contains(gotMessageText, want) {
			t.Fatalf("messages = %#v, want substring %q", gotSubjects, want)
		}
	}
	if !strings.Contains(m.notice, "4 state changes") {
		t.Fatalf("notice = %q, want transition summary", m.notice)
	}
	if m.notifyErr != "" {
		t.Fatalf("notifyErr = %q, want empty", m.notifyErr)
	}

	_, cmd = m.Update(stateLoadedMsg{panes: next, at: time.Unix(3, 0)})
	if cmd != nil {
		t.Fatal("unchanged agent state snapshot returned notification command, want nil")
	}
	if len(notifier.events) != 4 {
		t.Fatalf("notifier events after unchanged snapshot = %d, want 4", len(notifier.events))
	}
}

func TestStateUpdateNotifiesAgentTransitionsFromBuiltPaneViews(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})
	statePanes := []state.Pane{
		{Parent: "100", IssueNum: 101, DisplayName: "issue work", PaneID: "%1", WorktreePath: "/repo/.fanout/worktrees/issue-work", Agent: "codex"},
		{Parent: "plan:alpha", IssueNum: 0, TaskID: "api-client", DisplayName: "API client", PaneID: "%2", WorktreePath: "/repo/.fanout/worktrees/api-client", Agent: "codex", BranchName: "fanout/api-client"},
	}

	initial := buildPaneViews(statePanes, []tmuxrun.LivePane{
		{ID: "%1", AgentState: "running"},
		{ID: "%2", AgentState: "working"},
	}, true, nil, nil)
	updated, _ := m.Update(stateLoadedMsg{panes: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	next := buildPaneViews(statePanes, []tmuxrun.LivePane{
		{ID: "%1", AgentState: "plan"},
		{ID: "%2", AgentState: "blocked"},
	}, true, nil, nil)
	_, cmd := m.Update(stateLoadedMsg{panes: next, at: time.Unix(2, 0)})
	if cmd == nil {
		t.Fatal("built paneView transition returned nil command, want notification command")
	}
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}

	gotKinds := []fanoutnotify.EventKind{}
	for _, event := range notifier.events {
		gotKinds = append(gotKinds, event.Kind)
	}
	wantKinds := []fanoutnotify.EventKind{fanoutnotify.EventAgentPlan, fanoutnotify.EventAgentBlocked}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("event kinds = %#v, want %#v", gotKinds, wantKinds)
	}
}

func TestStateUpdateNotifiesFirstObservedAgentStatesAfterPriming(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})

	updated, cmd := m.Update(stateLoadedMsg{panes: nil, at: time.Unix(1, 0)})
	if cmd != nil {
		t.Fatal("initial empty state snapshot returned notification command, want nil")
	}
	m = updated.(model)
	if !m.agentPrimed {
		t.Fatal("agentPrimed = false, want true after initial empty state snapshot")
	}

	firstObserved := []paneView{
		{Parent: "100", IssueNum: 101, Name: "fast plan", PaneID: "%1", TmuxState: "live", AgentState: "plan"},
		{Parent: "100", IssueNum: 102, Name: "fast blocked", PaneID: "%2", TmuxState: "live", AgentState: "blocked"},
		{Parent: "100", IssueNum: 103, Name: "fast done", PaneID: "%3", TmuxState: "live", AgentState: "done"},
		{Parent: "100", IssueNum: 104, Name: "still running", PaneID: "%4", TmuxState: "live", AgentState: "running"},
	}
	updated, cmd = m.Update(stateLoadedMsg{panes: firstObserved, at: time.Unix(2, 0)})
	if cmd == nil {
		t.Fatal("first observed agent states returned nil command, want notification command")
	}
	m = updated.(model)
	var msg transitionNotifiedMsg
	found := false
	for _, candidate := range runCmd(cmd) {
		if notified, ok := candidate.(transitionNotifiedMsg); ok {
			msg = notified
			found = true
			break
		}
	}
	if !found {
		t.Fatal("notify command returned no transitionNotifiedMsg")
	}
	updated, _ = m.Update(msg)
	m = updated.(model)

	gotKinds := []fanoutnotify.EventKind{}
	gotMessages := []string{}
	for _, event := range notifier.events {
		gotKinds = append(gotKinds, event.Kind)
		gotMessages = append(gotMessages, event.Message())
	}
	wantKinds := []fanoutnotify.EventKind{
		fanoutnotify.EventAgentPlan,
		fanoutnotify.EventAgentBlocked,
		fanoutnotify.EventAgentDone,
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("event kinds = %#v, want %#v", gotKinds, wantKinds)
	}
	gotMessageText := strings.Join(gotMessages, "\n")
	for _, want := range []string{"#101 fast plan plan ready", "#102 fast blocked waiting for input", "#103 fast done agent exited"} {
		if !strings.Contains(gotMessageText, want) {
			t.Fatalf("messages = %#v, want substring %q", gotMessages, want)
		}
	}
	if len(m.agentStates) != 4 {
		t.Fatalf("agentStates len = %d, want 4 including non-notifying running pane", len(m.agentStates))
	}

	_, cmd = m.Update(stateLoadedMsg{panes: firstObserved, at: time.Unix(3, 0)})
	if cmd != nil {
		t.Fatal("repeated first observed agent states returned notification command, want nil")
	}
	if len(notifier.events) != 3 {
		t.Fatalf("notifier events after repeated snapshot = %d, want 3", len(notifier.events))
	}
}

func TestAgentTransitionKeyPrefersStableRowIdentityOverPaneID(t *testing.T) {
	tests := []struct {
		name string
		pane paneView
		want string
	}{
		{
			name: "issue identity wins over pane id",
			pane: paneView{Parent: "100", IssueNum: 101, PaneID: "%1", SourceKey: "source-a"},
			want: "issue:100:101",
		},
		{
			name: "task identity wins over pane id",
			pane: paneView{Parent: "plan:alpha", TaskID: "api-client", PaneID: "%1", SourceKey: "source-a", sourceProjectRoot: "/wt/task"},
			want: "task:plan:alpha:/wt/task:api-client",
		},
		{
			name: "shell key wins over pane id",
			pane: paneView{Parent: "@manual", IssueNum: -1, PaneID: "%1", ShellKey: "shell-root", sourceProjectRoot: "/repo"},
			want: "shell:@manual:/repo:shell-root",
		},
		{
			name: "source key wins over pane id",
			pane: paneView{Parent: "@manual", IssueNum: -1, PaneID: "%1", SourceKey: "manual-source", sourceProjectRoot: "/repo"},
			want: "source:@manual:-1:manual-source",
		},
		{
			name: "pane id is fallback",
			pane: paneView{Parent: "@manual", IssueNum: -1, PaneID: "%1"},
			want: "pane:%1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentTransitionKey(tt.pane); got != tt.want {
				t.Fatalf("agentTransitionKey(%+v) = %q, want %q", tt.pane, got, tt.want)
			}
		})
	}
}

func TestStateUpdateSuppressesInvalidAndRepeatedAgentTransitions(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})

	initial := []paneView{
		{Parent: "100", IssueNum: 101, Name: "unchanged", PaneID: "%1", TmuxState: "live", AgentState: "running"},
		{Parent: "100", IssueNum: 102, Name: "stale", PaneID: "%2", TmuxState: "live", AgentState: "running"},
		{Parent: "100", IssueNum: 103, Name: "queued", PaneID: "%3", TmuxState: "live", AgentState: "running"},
		{Parent: "100", IssueNum: 104, Name: "unknown", PaneID: "%4", TmuxState: "live", AgentState: "running"},
		{Parent: "100", IssueNum: 105, Name: "empty", PaneID: "%5", TmuxState: "live", AgentState: "running"},
		{Parent: "100", IssueNum: 106, Name: "done already", PaneID: "%6", TmuxState: "live", AgentState: "done"},
	}
	updated, _ := m.Update(stateLoadedMsg{panes: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	next := []paneView{
		{Parent: "100", IssueNum: 101, Name: "unchanged", PaneID: "%1", TmuxState: "live", AgentState: "running"},
		{Parent: "100", IssueNum: 102, Name: "stale", PaneID: "%2", TmuxState: "stale", AgentState: "done"},
		{Parent: "100", IssueNum: 103, Name: "queued", PaneID: "%3", TmuxState: "queued", AgentState: "done"},
		{Parent: "100", IssueNum: 104, Name: "unknown", PaneID: "%4", TmuxState: "live", AgentState: "surprised"},
		{Parent: "100", IssueNum: 105, Name: "empty", PaneID: "%5", TmuxState: "live"},
		{Parent: "100", IssueNum: 106, Name: "done already", PaneID: "%6", TmuxState: "live", AgentState: "done"},
	}
	_, cmd := m.Update(stateLoadedMsg{panes: next, at: time.Unix(2, 0)})
	if cmd != nil {
		t.Fatal("invalid/repeated agent state snapshot returned notification command, want nil")
	}
	if len(notifier.events) != 0 {
		t.Fatalf("notifier events = %#v, want none", notifier.events)
	}
}

func TestStateUpdatePreservesAgentNotificationBaselineOnError(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})

	initial := []paneView{{Parent: "100", IssueNum: 101, Name: "work", TmuxState: "live", AgentState: "running"}}
	updated, _ := m.Update(stateLoadedMsg{panes: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	errored := []paneView{{Parent: "100", IssueNum: 101, Name: "work", TmuxState: "live", AgentState: "done"}}
	updated, cmd := m.Update(stateLoadedMsg{panes: errored, at: time.Unix(2, 0), err: errBoom})
	if cmd != nil {
		t.Fatal("errored state snapshot returned notification command, want nil")
	}
	m = updated.(model)
	if got := m.agentStates[agentTransitionKey(initial[0])].State; got != "running" {
		t.Fatalf("agent baseline after errored refresh = %q, want running", got)
	}

	updated, cmd = m.Update(stateLoadedMsg{panes: errored, at: time.Unix(3, 0)})
	if cmd == nil {
		t.Fatal("recovered done snapshot returned nil command, want notification command")
	}
	m = updated.(model)
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}
	updated, _ = m.Update(msg)
	m = updated.(model)

	want := []fanoutnotify.Event{{Kind: fanoutnotify.EventAgentDone, Parent: "100", IssueNum: 101, Title: "work", AgentState: "done"}}
	if !reflect.DeepEqual(notifier.events, want) {
		t.Fatalf("notifier events = %#v, want %#v", notifier.events, want)
	}
	if m.notifyErr != "" {
		t.Fatalf("notifyErr = %q, want empty", m.notifyErr)
	}
}

func TestStateUpdateDetectsAgentTransitionsWhenRestoreFails(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})

	initial := []paneView{{Parent: "100", IssueNum: 101, Name: "work", TmuxState: "live", AgentState: "running"}}
	updated, _ := m.Update(stateLoadedMsg{panes: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	done := []paneView{{Parent: "100", IssueNum: 101, Name: "work", TmuxState: "live", AgentState: "done"}}
	updated, cmd := m.Update(stateLoadedMsg{
		panes:      done,
		at:         time.Unix(2, 0),
		err:        errBoom,
		restoreErr: errBoom,
	})
	if cmd == nil {
		t.Fatal("restore-only error snapshot returned nil command, want notification command")
	}
	m = updated.(model)
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}
	updated, _ = m.Update(msg)
	m = updated.(model)

	want := []fanoutnotify.Event{{Kind: fanoutnotify.EventAgentDone, Parent: "100", IssueNum: 101, Title: "work", AgentState: "done"}}
	if !reflect.DeepEqual(notifier.events, want) {
		t.Fatalf("notifier events = %#v, want %#v", notifier.events, want)
	}
	if got := m.agentStates[agentTransitionKey(initial[0])].State; got != "done" {
		t.Fatalf("agent baseline after restore-only error = %q, want done", got)
	}
	if m.stateErr == "" {
		t.Fatal("stateErr = empty, want restore error still displayed")
	}
}

func TestStateUpdatePreservesAgentNotificationBaselineOnUntrackableSnapshot(t *testing.T) {
	notifier := &fakeTransitionNotifier{}
	m := newModel(Options{Notifier: notifier})

	initial := []paneView{{Parent: "100", IssueNum: 101, Name: "work", PaneID: "%1", TmuxState: "live", AgentState: "running"}}
	updated, _ := m.Update(stateLoadedMsg{panes: initial, at: time.Unix(1, 0)})
	m = updated.(model)

	untrackable := []paneView{{Parent: "100", IssueNum: 101, Name: "work", PaneID: "%1", TmuxState: "stale", AgentState: "done"}}
	updated, cmd := m.Update(stateLoadedMsg{panes: untrackable, at: time.Unix(2, 0)})
	if cmd != nil {
		t.Fatal("untrackable state snapshot returned notification command, want nil")
	}
	m = updated.(model)
	if got := m.agentStates[agentTransitionKey(initial[0])].State; got != "running" {
		t.Fatalf("agent baseline after untrackable refresh = %q, want running", got)
	}

	done := []paneView{{Parent: "100", IssueNum: 101, Name: "work", PaneID: "%1", TmuxState: "live", AgentState: "done"}}
	updated, cmd = m.Update(stateLoadedMsg{panes: done, at: time.Unix(3, 0)})
	if cmd == nil {
		t.Fatal("recovered done snapshot returned nil command, want notification command")
	}
	m = updated.(model)
	msg, ok := cmd().(transitionNotifiedMsg)
	if !ok {
		t.Fatalf("notify command returned %T, want transitionNotifiedMsg", msg)
	}
	updated, _ = m.Update(msg)
	m = updated.(model)

	want := []fanoutnotify.Event{{Kind: fanoutnotify.EventAgentDone, Parent: "100", IssueNum: 101, Title: "work", PaneID: "%1", AgentState: "done"}}
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
		{Parent: "200", IssueNum: -1, Kind: state.PaneKindAttachedAgent, Name: "helper", SourceIssueNum: 202},
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
		LaunchPane:     func(LaunchRequest) (string, error) { return "", nil },
		LaunchShell:    func(ShellLaunchRequest) error { return nil },
		Notifier:       nil,
		lifecycle:      &fakeLifecycleRunner{},
		keyboard:       noopKeyboardProtocols{},
		ShellPaneAlive: func(string, string) bool { return true },
	})

	updated, cmd := m.Update(watchTickMsg{at: time.Unix(1, 0)})
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

	updated, cmd := m.Update(watchTickMsg{at: time.Unix(1, 0)})
	if cmd != nil {
		t.Fatal("watch tick without watcher returned command, want nil")
	}
	if updated.(model).watchRunning {
		t.Fatal("watchRunning = true without watcher, want false")
	}
}

func TestWatchTickIgnoresStaleGeneration(t *testing.T) {
	runner := &fakeWatcherRunner{}
	m := newModel(Options{
		Watcher:       runner,
		WatchInterval: time.Minute,
	})
	m.watchTickGen = 2

	updated, cmd := m.Update(watchTickMsg{at: time.Unix(1, 0), gen: 1})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("stale watch tick returned command")
	}
	if m.watchRunning {
		t.Fatal("watchRunning = true after stale watch tick")
	}
	if runner.calls != 0 {
		t.Fatalf("RunCycle calls = %d, want 0", runner.calls)
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

// runCmd executes cmd, flattening one level of tea.Batch, and returns the
// produced messages.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	msgs := make([]tea.Msg, 0, len(batch))
	for _, sub := range batch {
		if sub != nil {
			msgs = append(msgs, sub())
		}
	}
	return msgs
}

func findPaneFocusedMsg(t *testing.T, msgs []tea.Msg) paneFocusedMsg {
	t.Helper()
	for _, msg := range msgs {
		if focused, ok := msg.(paneFocusedMsg); ok {
			return focused
		}
	}
	t.Fatalf("no paneFocusedMsg in %#v", msgs)
	return paneFocusedMsg{}
}

func TestNumericJumpSelectsAndFocusesNthPane(t *testing.T) {
	var focused string
	m := newModel(Options{
		FocusPane: func(paneID string) error {
			focused = paneID
			return nil
		},
		PaneAlive:         func(string) bool { return true },
		CapturePaneOutput: func(string, int) (string, error) { return "", nil },
	})
	m.allPanes = []paneView{
		{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"},
		{IssueNum: 2, Name: "two", PaneID: "%2", TmuxState: "live"},
		{IssueNum: 3, Name: "three", PaneID: "%3", TmuxState: "live"},
	}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("3"))
	m = updated.(model)
	if got := m.table.Cursor(); got != 2 {
		t.Fatalf("cursor after 3 = %d, want 2", got)
	}
	if cmd == nil {
		t.Fatal("numeric jump returned nil command, want focus command")
	}
	if msg := findPaneFocusedMsg(t, runCmd(cmd)); msg.err != nil {
		t.Fatalf("focus msg err = %v, want nil", msg.err)
	}
	if focused != "%3" {
		t.Fatalf("focused pane = %q, want %%3", focused)
	}
}

// Guards that the numeric jump indexes the filtered list, not all panes.
func TestNumericJumpIndexesFilteredList(t *testing.T) {
	var focused string
	m := newModel(Options{
		FocusPane: func(paneID string) error {
			focused = paneID
			return nil
		},
		PaneAlive:         func(string) bool { return true },
		CapturePaneOutput: func(string, int) (string, error) { return "", nil },
	})
	m.allPanes = []paneView{
		{IssueNum: 1, Name: "alpha", PaneID: "%1", TmuxState: "live"},
		{IssueNum: 2, Name: "bravo", PaneID: "%2", TmuxState: "live"},
		{IssueNum: 3, Name: "charlie", PaneID: "%3", TmuxState: "live"},
	}
	m.filterQuery = "bravo"
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("1"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("numeric jump returned nil command, want focus command")
	}
	if msg := findPaneFocusedMsg(t, runCmd(cmd)); msg.err != nil {
		t.Fatalf("focus msg err = %v, want nil", msg.err)
	}
	if focused != "%2" {
		t.Fatalf("focused pane = %q, want %%2 (first filtered row)", focused)
	}

	focused = ""
	updated, cmd = m.Update(keyRunes("2"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("jump past the filtered list returned command, want nil")
	}
	if !strings.Contains(m.notice, "no pane 2") {
		t.Fatalf("notice = %q, want out-of-range message", m.notice)
	}
	if focused != "" {
		t.Fatalf("focused pane = %q, want no focus", focused)
	}
}

// Guards that the numeric jump refreshes the detail-panel peek like every
// other cursor move, so a skipped or failed focus does not leave it stale.
func TestNumericJumpSchedulesPeekAlongsideFocus(t *testing.T) {
	m := newModel(Options{
		FocusPane:         func(string) error { return nil },
		PaneAlive:         func(string) bool { return true },
		CapturePaneOutput: func(string, int) (string, error) { return "captured", nil },
	})
	m.allPanes = []paneView{
		{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"},
		{IssueNum: 2, Name: "two", PaneID: "%2", TmuxState: "live"},
	}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("2"))
	m = updated.(model)
	if !m.peek.Loading || m.peek.PaneID != "%2" {
		t.Fatalf("peek after jump = %#v, want loading for %%2", m.peek)
	}
	peeked := false
	for _, msg := range runCmd(cmd) {
		if loaded, ok := msg.(panePeekLoadedMsg); ok && loaded.paneID == "%2" {
			peeked = true
		}
	}
	if !peeked {
		t.Fatal("numeric jump scheduled no peek for the target pane")
	}
}

func TestNumericJumpOutOfRangeShowsNotice(t *testing.T) {
	focusCalls := 0
	m := newModel(Options{
		FocusPane: func(string) error {
			focusCalls++
			return nil
		},
		PaneAlive: func(string) bool { return true },
	})
	m.allPanes = []paneView{
		{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"},
		{IssueNum: 2, Name: "two", PaneID: "%2", TmuxState: "live"},
	}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("9"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("out-of-range jump returned command, want nil")
	}
	if !strings.Contains(m.notice, "no pane 9") {
		t.Fatalf("notice = %q, want out-of-range message", m.notice)
	}
	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor after out-of-range jump = %d, want 0", got)
	}
	if focusCalls != 0 {
		t.Fatalf("FocusPane calls = %d, want 0", focusCalls)
	}
}

// Guards that the close menu's 1-3 option choices win over the numeric jump.
func TestNumericKeysDuringCloseMenuSelectOptionsNotJump(t *testing.T) {
	focusCalls := 0
	m := newModel(Options{
		ProjectRoot: "/repo",
		lifecycle:   &fakeLifecycleRunner{code: exitcode.OK},
		FocusPane: func(string) error {
			focusCalls++
			return nil
		},
		PaneAlive: func(string) bool { return true },
	})
	m.allPanes = []paneView{
		{Parent: "84", IssueNum: 101, Name: "one", PaneID: "%1", TmuxState: "live"},
		{Parent: "84", IssueNum: 102, Name: "two", PaneID: "%2", TmuxState: "live"},
	}
	m.refreshRows()

	updated, _ := m.Update(keyRunes("c"))
	m = updated.(model)
	updated, cmd := m.Update(keyRunes("2"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("close choice returned command, want nil")
	}
	if m.pendingAction == nil || m.pendingAction.closeMode != lifecycle.CloseWorktree {
		t.Fatalf("pendingAction = %#v, want close-worktree choice", m.pendingAction)
	}
	if got := m.table.Cursor(); got != 0 {
		t.Fatalf("cursor after close-menu 2 = %d, want 0", got)
	}
	if focusCalls != 0 {
		t.Fatalf("FocusPane calls = %d, want 0", focusCalls)
	}
}

// Guards that filter editing captures digits instead of the numeric jump.
func TestNumericKeysDuringFilterEditingTypeIntoQuery(t *testing.T) {
	focusCalls := 0
	m := newModel(Options{
		FocusPane: func(string) error {
			focusCalls++
			return nil
		},
		PaneAlive: func(string) bool { return true },
	})
	m.allPanes = []paneView{
		{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"},
		{IssueNum: 2, Name: "two", PaneID: "%2", TmuxState: "live"},
	}
	m.refreshRows()

	updated, _ := m.Update(keyRunes("/"))
	m = updated.(model)
	updated, cmd := m.Update(keyRunes("2"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("filter digit returned command, want nil")
	}
	if m.filterQuery != "2" {
		t.Fatalf("filterQuery = %q, want \"2\"", m.filterQuery)
	}
	if focusCalls != 0 {
		t.Fatalf("FocusPane calls = %d, want 0", focusCalls)
	}
}

func TestZoomKeyFocusesThenZoomsSelectedPane(t *testing.T) {
	var calls []string
	m := newModel(Options{
		FocusPane: func(paneID string) error {
			calls = append(calls, "focus:"+paneID)
			return nil
		},
		ZoomPane: func(paneID string) error {
			calls = append(calls, "zoom:"+paneID)
			return nil
		},
		PaneAlive: func(string) bool { return true },
	})
	m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("Z"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("Z returned nil command, want focus+zoom command")
	}
	raw := cmd()
	msg, ok := raw.(paneFocusedMsg)
	if !ok {
		t.Fatalf("Z msg = %T, want paneFocusedMsg", raw)
	}
	if want := []string{"focus:%1", "zoom:%1"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	next, _ := m.Update(msg)
	m = next.(model)
	if !strings.Contains(m.notice, "focused %1") {
		t.Fatalf("notice = %q, want focused message", m.notice)
	}
}

func TestZoomKeySkipsZoomWhenFocusFails(t *testing.T) {
	zoomCalls := 0
	m := newModel(Options{
		FocusPane: func(string) error { return errors.New("focus failed") },
		ZoomPane: func(string) error {
			zoomCalls++
			return nil
		},
		PaneAlive: func(string) bool { return true },
	})
	m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("Z"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("Z returned nil command, want focus+zoom command")
	}
	raw := cmd()
	msg, ok := raw.(paneFocusedMsg)
	if !ok {
		t.Fatalf("Z msg = %T, want paneFocusedMsg", raw)
	}
	if msg.err == nil {
		t.Fatal("msg.err = nil, want focus error")
	}
	if zoomCalls != 0 {
		t.Fatalf("ZoomPane calls after failed focus = %d, want 0", zoomCalls)
	}
}

func TestZoomFailureKeepsFocusAndShowsNotice(t *testing.T) {
	m := newModel(Options{
		FocusPane: func(string) error { return nil },
		ZoomPane:  func(string) error { return errors.New("zoom failed") },
		PaneAlive: func(string) bool { return true },
	})
	m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("Z"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("Z returned nil command, want focus+zoom command")
	}
	raw := cmd()
	msg, ok := raw.(paneFocusedMsg)
	if !ok {
		t.Fatalf("Z msg = %T, want paneFocusedMsg", raw)
	}
	if msg.err != nil {
		t.Fatalf("msg.err = %v, want nil when only zoom fails", msg.err)
	}
	next, _ := m.Update(msg)
	m = next.(model)
	if !m.keyboardPaused {
		t.Fatal("keyboardPaused = false, want true when focus succeeded")
	}
	if !strings.Contains(m.notice, "zoom failed") {
		t.Fatalf("notice = %q, want zoom failure message", m.notice)
	}
}

// Guards that enter / o keep plain focus behavior and never zoom.
func TestEnterAndOFocusKeysDoNotZoom(t *testing.T) {
	for _, key := range []string{"enter", "o"} {
		t.Run(key, func(t *testing.T) {
			zoomCalls := 0
			var focused string
			m := newModel(Options{
				FocusPane: func(paneID string) error {
					focused = paneID
					return nil
				},
				ZoomPane: func(string) error {
					zoomCalls++
					return nil
				},
				PaneAlive: func(string) bool { return true },
			})
			m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live"}}
			m.refreshRows()

			updated, cmd := m.Update(keyRunes(key))
			m = updated.(model)
			if cmd == nil {
				t.Fatalf("%s returned nil command, want focus command", key)
			}
			if raw := cmd(); raw == nil {
				t.Fatalf("%s msg = nil, want paneFocusedMsg", key)
			} else if _, ok := raw.(paneFocusedMsg); !ok {
				t.Fatalf("%s msg = %T, want paneFocusedMsg", key, raw)
			}
			if focused != "%1" {
				t.Fatalf("focused pane = %q, want %%1", focused)
			}
			if zoomCalls != 0 {
				t.Fatalf("ZoomPane calls = %d, want 0", zoomCalls)
			}
		})
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

func TestPromptOnlyInitEnablesKeyboardProtocols(t *testing.T) {
	protocols := &fakeKeyboardProtocols{}
	m := newModel(Options{keyboard: protocols})
	m.promptOnly = true
	m.opts.ListRepoFiles = nil

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("prompt-only Init() returned nil command, want keyboard protocol enable")
	}
	msg := cmd()
	if _, ok := msg.(keyboardProtocolsEnabledMsg); !ok {
		t.Fatalf("prompt-only Init() msg = %T, want keyboardProtocolsEnabledMsg", msg)
	}
	if protocols.enableCount != 1 || protocols.disableCount != 0 {
		t.Fatalf("protocol calls = enable %d disable %d, want enable 1 disable 0", protocols.enableCount, protocols.disableCount)
	}
}

func TestEnhancedKeyboardKeysEnabledByDefault(t *testing.T) {
	for _, value := range []string{"", "1", "true", "anything"} {
		t.Setenv(EnhancedKeysEnv, value)
		if !enhancedKeyboardKeysEnabled() {
			t.Fatalf("enhancedKeyboardKeysEnabled() = false with %s=%q, want on by default", EnhancedKeysEnv, value)
		}
	}

	for _, value := range []string{"0", "false", "off", "no", "OFF", " 0 "} {
		t.Setenv(EnhancedKeysEnv, value)
		if enhancedKeyboardKeysEnabled() {
			t.Fatalf("enhancedKeyboardKeysEnabled() = true with %s=%q, want opt-out", EnhancedKeysEnv, value)
		}
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

func TestNewPanePromptSignalCleanupDisablesKeyboardBeforeExit(t *testing.T) {
	protocols := &fakeKeyboardProtocols{}
	inputClosed := false
	exitCode := 0

	cleanupNewPanePromptSignal(syscall.SIGTERM, protocols, func() {
		inputClosed = true
	}, func(code int) {
		exitCode = code
	})

	if protocols.disableCount != 1 {
		t.Fatalf("disableCount = %d, want 1", protocols.disableCount)
	}
	if !inputClosed {
		t.Fatal("inputClosed = false, want true")
	}
	if exitCode != 143 {
		t.Fatalf("exitCode = %d, want 143", exitCode)
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
	m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live", AgentState: "running"}}
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
	if m.panes[0].AgentState != "" {
		t.Fatalf("AgentState = %q, want cleared on stale", m.panes[0].AgentState)
	}
	if got := m.table.Rows()[0][columnIndex(t, "TMUX")]; got != "stale!" {
		t.Fatalf("table tmux cell = %q, want stale!", got)
	}
	if got := m.table.Rows()[0][columnIndex(t, "RUN")]; got != "✗" {
		t.Fatalf("table run cell = %q, want ✗", got)
	}
}

func TestDetailContentShowsAgentState(t *testing.T) {
	m := newModel(Options{})
	// hook 由来の新値(6 値契約)も raw のまま run= に出る。
	for _, state := range []string{"running", "working", "plan", "blocked", "idle", "done"} {
		m.allPanes = []paneView{{IssueNum: 1, Name: "one", PaneID: "%1", TmuxState: "live", AgentState: state}}
		m.refreshRows()

		if got := m.detailContent(); !strings.Contains(got, "run="+state) {
			t.Fatalf("detailContent() = %q, want run=%s", got, state)
		}
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

// TestFilterPaneViewsRunPredicate pins run: against the 6-value agent-state
// contract: exact match (equalFold), one glyph-worth of state per pane, and no
// run:stale — stale lives in TmuxState (state:stale), and stale panes carry no
// AgentState.
func TestFilterPaneViewsRunPredicate(t *testing.T) {
	panes := []paneView{
		{IssueNum: 1, TmuxState: "live", AgentState: "running"},
		{IssueNum: 2, TmuxState: "live", AgentState: "working"},
		{IssueNum: 3, TmuxState: "live", AgentState: "plan"},
		{IssueNum: 4, TmuxState: "live", AgentState: "blocked"},
		{IssueNum: 5, TmuxState: "live", AgentState: "idle"},
		{IssueNum: 6, TmuxState: "live", AgentState: "done"},
		{IssueNum: 7, TmuxState: "live"},  // 状態不明の live pane
		{IssueNum: 8, TmuxState: "stale"}, // stale pane は AgentState を持たない
	}
	tests := []struct {
		name  string
		query string
		want  []int // matching issue numbers
	}{
		{name: "run:running matches only the running pane", query: "run:running", want: []int{1}},
		{name: "run:working matches only the working pane", query: "run:working", want: []int{2}},
		{name: "run:plan matches only the plan pane", query: "run:plan", want: []int{3}},
		{name: "run:blocked matches only the blocked pane", query: "run:blocked", want: []int{4}},
		{name: "run:idle matches only the idle pane", query: "run:idle", want: []int{5}},
		{name: "run:done matches only the done pane", query: "run:done", want: []int{6}},
		{name: "run matches case-insensitively", query: "run:WORKING", want: []int{2}},
		{name: "run is exact, not substring", query: "run:work", want: []int{}},
		{name: "run:stale matches nothing", query: "run:stale", want: []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nums []int
			for _, p := range filterPaneViews(panes, tt.query) {
				nums = append(nums, p.IssueNum)
			}
			if !slices.Equal(nums, tt.want) {
				t.Fatalf("filterPaneViews(%q) = %v, want %v", tt.query, nums, tt.want)
			}
		})
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
	m.width = 100
	m.height = 40
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
	if m.mode != modeCloseChoice {
		t.Fatalf("mode = %v, want close choice", m.mode)
	}
	view := m.View()
	if !strings.Contains(view, "Just close pane") || !strings.Contains(view, "Close and delete everything") {
		t.Fatalf("view = %q, want close option menu", view)
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
	if runner.closeMode != lifecycle.ClosePaneOnly {
		t.Fatalf("Close mode = %v, want pane-only", runner.closeMode)
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

func TestLifecycleCloseKeyUsesPopupWhenConfigured(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	calls := 0
	var prompted CloseChoiceRequest
	m := newModel(Options{
		ProjectRoot: "/repo",
		lifecycle:   runner,
		CloseChoicePopup: func(req CloseChoiceRequest) (lifecycle.CloseMode, bool, error) {
			calls++
			prompted = req
			return lifecycle.CloseEverything, false, nil
		},
	})
	m.allPanes = []paneView{{Parent: "84", IssueNum: 101, Name: "child"}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("c"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("close key returned nil command, want popup command")
	}
	if calls != 0 {
		t.Fatalf("popup calls before command execution = %d, want 0", calls)
	}
	if m.mode != modeMonitor {
		t.Fatalf("mode = %v, want monitor while popup owns input", m.mode)
	}
	if !m.closePopupOpen {
		t.Fatal("closePopupOpen = false, want true")
	}
	if m.notice != closePopupOpeningNotice {
		t.Fatalf("notice = %q, want %q", m.notice, closePopupOpeningNotice)
	}
	if m.actionMessage != "" {
		t.Fatalf("actionMessage = %q, want no footer close menu", m.actionMessage)
	}

	for _, key := range []tea.KeyMsg{keyRunes("q"), keyRunes("1"), keyRunes("n"), {Type: tea.KeyCtrlC}} {
		nextModel, blockedCmd := m.Update(key)
		m = nextModel.(model)
		if blockedCmd != nil {
			t.Fatalf("%q returned command while close popup is open", key.String())
		}
		if !m.closePopupOpen {
			t.Fatalf("%q cleared closePopupOpen", key.String())
		}
	}

	updated, next := m.Update(cmd())
	m = updated.(model)
	if next == nil {
		t.Fatal("close popup completion returned nil command, want lifecycle command")
	}
	if calls != 1 {
		t.Fatalf("popup calls = %d, want 1", calls)
	}
	if prompted.PaneLabel != "#101" || prompted.InitialMode != lifecycle.ClosePaneOnly {
		t.Fatalf("popup request = %#v, want #101 pane-only", prompted)
	}
	if m.closePopupOpen {
		t.Fatal("closePopupOpen = true after popup completion")
	}
	if m.notice != "" {
		t.Fatalf("notice = %q, want cleared", m.notice)
	}
	if !m.actionRunning {
		t.Fatal("actionRunning = false, want true while command runs")
	}
	if !strings.Contains(m.actionMessage, "delete #101") {
		t.Fatalf("actionMessage = %q, want delete running message", m.actionMessage)
	}

	rawMsg := next()
	if _, ok := rawMsg.(lifecycleDoneMsg); !ok {
		t.Fatalf("lifecycle command returned %T, want lifecycleDoneMsg", rawMsg)
	}
	if runner.closeMode != lifecycle.CloseEverything {
		t.Fatalf("close mode = %v, want delete everything", runner.closeMode)
	}
}

func TestLifecycleClosePopupCancelAndFailure(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		runner := &fakeLifecycleRunner{code: exitcode.OK}
		m := newModel(Options{
			ProjectRoot: "/repo",
			lifecycle:   runner,
			CloseChoicePopup: func(CloseChoiceRequest) (lifecycle.CloseMode, bool, error) {
				return lifecycle.ClosePaneOnly, true, nil
			},
		})
		m.allPanes = []paneView{{Parent: "84", IssueNum: 101, Name: "child"}}
		m.refreshRows()

		updated, cmd := m.Update(keyRunes("c"))
		m = updated.(model)
		updated, next := m.Update(cmd())
		m = updated.(model)
		if next != nil {
			t.Fatal("canceled close popup returned command")
		}
		if m.closePopupOpen {
			t.Fatal("closePopupOpen = true after cancel")
		}
		if m.pendingAction != nil {
			t.Fatalf("pendingAction = %#v, want nil after cancel", m.pendingAction)
		}
		if m.actionMessage != "close canceled" {
			t.Fatalf("actionMessage = %q, want close canceled", m.actionMessage)
		}
		if runner.closeParent != "" {
			t.Fatalf("runner closeParent = %q, want not called", runner.closeParent)
		}
	})

	t.Run("failure", func(t *testing.T) {
		runner := &fakeLifecycleRunner{code: exitcode.OK}
		m := newModel(Options{
			ProjectRoot: "/repo",
			lifecycle:   runner,
			CloseChoicePopup: func(CloseChoiceRequest) (lifecycle.CloseMode, bool, error) {
				return lifecycle.ClosePaneOnly, false, errBoom
			},
		})
		m.width = 100
		m.height = 40
		m.allPanes = []paneView{{Parent: "84", IssueNum: 101, Name: "child"}}
		m.refreshRows()

		updated, cmd := m.Update(keyRunes("c"))
		m = updated.(model)
		updated, next := m.Update(cmd())
		m = updated.(model)
		if next != nil {
			t.Fatal("failed close popup returned command")
		}
		if m.closePopupOpen {
			t.Fatal("closePopupOpen = true after failure")
		}
		if m.pendingAction == nil {
			t.Fatal("pendingAction = nil, want fallback close choice")
		}
		if m.mode != modeCloseChoice {
			t.Fatalf("mode = %v, want close choice fallback", m.mode)
		}
		if m.notice != "close popup: boom" {
			t.Fatalf("notice = %q, want close popup error", m.notice)
		}
		if view := m.View(); !strings.Contains(view, "Close #101?") {
			t.Fatalf("fallback view = %q, want close choice modal", view)
		}

		updated, next = m.Update(keyRunes("enter"))
		m = updated.(model)
		if next == nil {
			t.Fatal("fallback enter returned nil command, want lifecycle command")
		}
		if !m.actionRunning {
			t.Fatal("actionRunning = false, want true while fallback lifecycle runs")
		}
		if _, ok := next().(lifecycleDoneMsg); !ok {
			t.Fatal("fallback lifecycle command did not return lifecycleDoneMsg")
		}
		if runner.closeMode != lifecycle.ClosePaneOnly {
			t.Fatalf("close mode = %v, want pane only", runner.closeMode)
		}
	})
}

func TestCloseChoiceViewUsesTriangleSelectionMarker(t *testing.T) {
	m := newModel(Options{})
	m.mode = modeCloseChoice
	m.pendingAction = &pendingLifecycleAction{
		action:           actionClose,
		pane:             paneView{Parent: "84", IssueNum: 101},
		closeOptionIndex: 0,
		closeMode:        lifecycle.ClosePaneOnly,
	}

	view := m.closeChoiceView()
	if !strings.Contains(view, "▶ 1. Just close pane") {
		t.Fatalf("close choice view missing selected triangle marker:\n%s", view)
	}
	if strings.Contains(view, "> 1.") {
		t.Fatalf("close choice view should not render the old > marker:\n%s", view)
	}
}

func TestLifecycleCloseChoiceSelectsWorktreeAndBranchModes(t *testing.T) {
	tests := []struct {
		key  string
		want lifecycle.CloseMode
	}{
		{key: "2", want: lifecycle.CloseWorktree},
		{key: "3", want: lifecycle.CloseEverything},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			runner := &fakeLifecycleRunner{code: exitcode.OK}
			m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
			m.allPanes = []paneView{{Parent: "84", IssueNum: 101, Name: "child"}}
			m.refreshRows()

			updated, cmd := m.Update(keyRunes("c"))
			if cmd != nil {
				t.Fatal("close menu returned command, want nil")
			}
			m = updated.(model)

			updated, cmd = m.Update(keyRunes(tc.key))
			if cmd != nil {
				t.Fatal("close choice returned command before enter, want nil")
			}
			m = updated.(model)
			if m.pendingAction == nil || m.pendingAction.closeMode != tc.want {
				t.Fatalf("pendingAction = %#v, want selected mode %v", m.pendingAction, tc.want)
			}

			updated, cmd = m.Update(keyRunes("enter"))
			if cmd == nil {
				t.Fatal("enter returned nil command, want lifecycle command")
			}
			m = updated.(model)
			if !m.actionRunning {
				t.Fatal("actionRunning = false, want true while command runs")
			}
			if _, ok := cmd().(lifecycleDoneMsg); !ok {
				t.Fatalf("lifecycle command did not return lifecycleDoneMsg")
			}
			if runner.closeMode != tc.want {
				t.Fatalf("Close mode = %v, want %v", runner.closeMode, tc.want)
			}
		})
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

func TestLifecycleCleanupPlanStaysWithinSelectedWorktree(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
	// Two worktrees hold an unrelated plan:launch (slug is worktree-local); a
	// cleanup on the /wt-a pane must only touch /wt-a, never the sibling plan.
	m.allPanes = []paneView{
		{Parent: "plan:launch", IssueNum: 0, TaskID: "api", Name: "a", sourceProjectRoot: "/wt-a", sourceProjectRoots: []string{"/wt-a"}},
		{Parent: "plan:launch", IssueNum: 0, TaskID: "api", Name: "b", sourceProjectRoot: "/wt-b", sourceProjectRoots: []string{"/wt-b"}},
	}

	cmd := m.lifecycleCmd(pendingLifecycleAction{action: actionCleanup, pane: m.allPanes[0]})
	if _, ok := cmd().(lifecycleDoneMsg); !ok {
		t.Fatal("lifecycleCmd did not return lifecycleDoneMsg")
	}
	if len(runner.cleanupRoots) != 1 || runner.cleanupRoots[0] != "/wt-a" {
		t.Fatalf("plan cleanup roots = %v, want only /wt-a (selected worktree)", runner.cleanupRoots)
	}
}

func TestLifecycleCleanupWatchFansAcrossWorktrees(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
	// @watch is repo-wide (watcher panes keyed by real GitHub issue numbers), so
	// cleanup must fan across every worktree the parent spans — unlike @manual.
	m.allPanes = []paneView{
		{Parent: "@watch", IssueNum: 501, Name: "a", sourceProjectRoot: "/wt-a", sourceProjectRoots: []string{"/wt-a"}},
		{Parent: "@watch", IssueNum: 502, Name: "b", sourceProjectRoot: "/wt-b", sourceProjectRoots: []string{"/wt-b"}},
	}

	cmd := m.lifecycleCmd(pendingLifecycleAction{action: actionCleanup, pane: m.allPanes[0]})
	if _, ok := cmd().(lifecycleDoneMsg); !ok {
		t.Fatal("lifecycleCmd did not return lifecycleDoneMsg")
	}
	got := map[string]bool{}
	for _, r := range runner.cleanupRoots {
		got[r] = true
	}
	if len(runner.cleanupRoots) != 2 || !got["/wt-a"] || !got["/wt-b"] {
		t.Fatalf("@watch cleanup roots = %v, want both /wt-a and /wt-b (repo-wide)", runner.cleanupRoots)
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
			key:    "X",
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
			m.width = 100
			m.height = 40
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
			if tc.action == actionClose {
				if m.mode != modeCloseChoice {
					t.Fatalf("mode = %v, want close choice", m.mode)
				}
				if view := m.View(); !strings.Contains(view, "api-client") {
					t.Fatalf("close choice view = %q, want task id", view)
				}
			} else {
				wantMessage := "api-client"
				if tc.action == actionCleanup {
					wantMessage = "plan:launch-plan"
				}
				if !strings.Contains(m.actionMessage, wantMessage) {
					t.Fatalf("actionMessage = %q, want confirmation containing %q", m.actionMessage, wantMessage)
				}
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

func TestLifecycleCloseKeyRoutesPlanTaskRows(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
	m.allPanes = []paneView{{Parent: "plan:launch-plan", IssueNum: 0, TaskID: "api-client", Name: "task"}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("c"))
	if cmd != nil {
		t.Fatal("close menu returned command, want nil")
	}
	m = updated.(model)
	updated, cmd = m.Update(keyRunes("3"))
	if cmd != nil {
		t.Fatal("close choice returned command before enter, want nil")
	}
	m = updated.(model)
	updated, cmd = m.Update(keyRunes("enter"))
	if cmd == nil {
		t.Fatal("enter returned nil command, want lifecycle command")
	}
	m = updated.(model)
	if !m.actionRunning {
		t.Fatal("actionRunning = false, want true while command runs")
	}
	if _, ok := cmd().(lifecycleDoneMsg); !ok {
		t.Fatalf("lifecycle command did not return lifecycleDoneMsg")
	}
	if runner.closeTaskParent != "plan:launch-plan" || runner.closeTaskID != "api-client" {
		t.Fatalf("CloseTask called with parent=%q task=%q, want plan/api-client", runner.closeTaskParent, runner.closeTaskID)
	}
	if runner.closeMode != lifecycle.CloseEverything {
		t.Fatalf("CloseTask mode = %v, want delete everything", runner.closeMode)
	}
}

func TestLifecycleMergeAndCleanupSkipShellRows(t *testing.T) {
	for _, key := range []string{"m", "X"} {
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

func TestLifecycleCloseAliasesCloseShellRows(t *testing.T) {
	for _, key := range []string{"c", "x"} {
		t.Run(key, func(t *testing.T) {
			runner := &fakeLifecycleRunner{code: exitcode.OK}
			m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
			m.allPanes = []paneView{{Parent: "@manual", IssueNum: -1, Kind: state.PaneKindShell, Name: "root terminal"}}
			m.refreshRows()

			updated, cmd := m.Update(keyRunes(key))
			if cmd != nil {
				t.Fatalf("Update(%q) returned command before confirmation", key)
			}
			m = updated.(model)
			if m.pendingAction == nil || m.pendingAction.action != actionClose {
				t.Fatalf("pendingAction = %#v, want close", m.pendingAction)
			}
			if strings.Contains(m.actionMessage, "Just close pane") {
				t.Fatalf("shell close actionMessage = %q, want simple confirmation", m.actionMessage)
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
			if runner.closeParent != "@manual" || runner.closeIssue != -1 {
				t.Fatalf("Close called with parent=%q issue=%d, want @manual/-1", runner.closeParent, runner.closeIssue)
			}
			if runner.closeMode != lifecycle.ClosePaneOnly {
				t.Fatalf("shell Close mode = %v, want pane-only close", runner.closeMode)
			}
		})
	}
}

func TestLifecycleCloseAliasUsesSelectedPane(t *testing.T) {
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
	if m.pendingAction == nil || m.pendingAction.action != actionClose {
		t.Fatalf("pendingAction = %#v, want close", m.pendingAction)
	}

	updated, cmd := m.Update(keyRunes("1"))
	if cmd != nil {
		t.Fatal("close choice returned command before enter, want nil")
	}
	m = updated.(model)
	updated, cmd = m.Update(keyRunes("enter"))
	if cmd == nil {
		t.Fatal("enter returned nil command, want lifecycle command")
	}
	m = updated.(model)
	rawMsg := cmd()
	if _, ok := rawMsg.(lifecycleDoneMsg); !ok {
		t.Fatalf("lifecycle command returned %T, want lifecycleDoneMsg", rawMsg)
	}
	if runner.closeParent != "200" || runner.closeIssue != 2 {
		t.Fatalf("Close target = %s/%d, want selected pane 200/2", runner.closeParent, runner.closeIssue)
	}
	if runner.closeMode != lifecycle.ClosePaneOnly {
		t.Fatalf("Close mode = %v, want pane-only", runner.closeMode)
	}
}

func TestLifecycleCleanupKeyUsesSelectedParent(t *testing.T) {
	runner := &fakeLifecycleRunner{code: exitcode.OK}
	m := newModel(Options{ProjectRoot: "/repo", lifecycle: runner})
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 1, Name: "one"},
		{Parent: "200", IssueNum: 2, Name: "two"},
	}
	m.refreshRows()
	m.table.SetCursor(1)

	updated, _ := m.Update(keyRunes("X"))
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
	closeMode           lifecycle.CloseMode
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
	return f.CloseWithMode(opts, parent, issueNum, lifecycle.CloseWorktree, lg)
}

func (f *fakeLifecycleRunner) CloseWithMode(opts lifecycle.Options, parent string, issueNum int, mode lifecycle.CloseMode, lg lifecycle.Logger) exitcode.Code {
	f.projectRoot = opts.ProjectRoot
	f.statePath = opts.StatePath
	f.watcherRunningLabel = opts.WatcherRunningLabel
	f.closeParent = parent
	f.closeIssue = issueNum
	f.closeMode = mode
	f.closeRoots = append(f.closeRoots, opts.ProjectRoot)
	fmt.Fprintf(lg.Stderr(), "[ ok ] fake close\n")
	return f.code
}

func (f *fakeLifecycleRunner) CloseTask(opts lifecycle.Options, parent, taskID string, lg lifecycle.Logger) exitcode.Code {
	return f.CloseTaskWithMode(opts, parent, taskID, lifecycle.CloseWorktree, lg)
}

func (f *fakeLifecycleRunner) CloseTaskWithMode(opts lifecycle.Options, parent, taskID string, mode lifecycle.CloseMode, lg lifecycle.Logger) exitcode.Code {
	f.projectRoot = opts.ProjectRoot
	f.statePath = opts.StatePath
	f.closeTaskParent = parent
	f.closeTaskID = taskID
	f.closeMode = mode
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
	if agents := got.selectedNewPaneAgents(); len(agents) != 1 || agents[0] != "codex" {
		t.Fatalf("default agents = %#v, want [codex]", agents)
	}
}

func TestNewPaneKeyUsesPopupPromptWhenConfigured(t *testing.T) {
	var prompted NewPanePromptRequest
	var launched LaunchRequest
	m := newModel(Options{
		DefaultAgent: "codex",
		NewPanePrompt: func(req NewPanePromptRequest) (LaunchRequest, bool, error) {
			prompted = req
			return LaunchRequest{Prompt: "Inspect the API", Agents: []string{"codex"}}, false, nil
		},
		LaunchPane: func(req LaunchRequest) (string, error) {
			launched = req
			return "", nil
		},
	})

	updated, cmd := m.Update(keyRunes("n"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("n returned nil command, want popup prompt command")
	}
	if m.mode != modeMonitor {
		t.Fatalf("mode = %v, want monitor while popup owns input", m.mode)
	}
	if !m.newPanePopupOpen {
		t.Fatal("newPanePopupOpen = false, want true")
	}
	if prompted.DefaultAgent != "" {
		t.Fatalf("prompt was called before command execution: %#v", prompted)
	}

	updated, cmd = m.Update(cmd())
	m = updated.(model)
	if prompted.DefaultAgent != "codex" {
		t.Fatalf("prompt request = %#v, want default codex", prompted)
	}
	if cmd == nil {
		t.Fatal("popup result returned nil command, want launch command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(model)

	want := LaunchRequest{Prompt: "Inspect the API", Agents: []string{"codex"}}
	if !reflect.DeepEqual(launched, want) {
		t.Fatalf("launch request = %#v, want %#v", launched, want)
	}
	if m.notice != "created new agent pane" {
		t.Fatalf("notice = %q, want created new agent pane", m.notice)
	}
}

func TestNewPanePopupOpenBlocksMonitorKeys(t *testing.T) {
	m := newModel(Options{
		DefaultAgent: "codex",
		NewPanePrompt: func(NewPanePromptRequest) (LaunchRequest, bool, error) {
			return LaunchRequest{Prompt: "Inspect the API", Agents: []string{"codex"}}, false, nil
		},
		LaunchPane: func(LaunchRequest) (string, error) {
			return "", nil
		},
	})

	updated, promptCmd := m.Update(keyRunes("n"))
	m = updated.(model)
	if promptCmd == nil {
		t.Fatal("n returned nil command, want popup prompt command")
	}
	if !m.newPanePopupOpen {
		t.Fatal("newPanePopupOpen = false, want true")
	}

	for _, key := range []tea.KeyMsg{keyRunes("q"), keyRunes("n"), {Type: tea.KeyCtrlC}} {
		updated, cmd := m.Update(key)
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("%q returned command while popup is open", key.String())
		}
		if m.mode != modeMonitor {
			t.Fatalf("%q changed mode = %v, want monitor", key.String(), m.mode)
		}
		if !m.newPanePopupOpen {
			t.Fatalf("%q cleared popup-open flag", key.String())
		}
	}
}

func TestNewPanePopupLaunchBlocksMonitorKeysWhileRunning(t *testing.T) {
	launchCalls := 0
	m := newModel(Options{
		DefaultAgent: "codex",
		NewPanePrompt: func(NewPanePromptRequest) (LaunchRequest, bool, error) {
			return LaunchRequest{Prompt: "Inspect the API", Agents: []string{"codex"}}, false, nil
		},
		LaunchPane: func(LaunchRequest) (string, error) {
			launchCalls++
			return "", nil
		},
	})

	updated, promptCmd := m.Update(keyRunes("n"))
	m = updated.(model)
	if promptCmd == nil {
		t.Fatal("n returned nil command, want popup prompt command")
	}
	updated, launchCmd := m.Update(promptCmd())
	m = updated.(model)
	if launchCmd == nil {
		t.Fatal("popup submit returned nil command, want launch command")
	}
	if !m.newPane.launching {
		t.Fatal("launching = false, want true")
	}
	if m.mode != modeMonitor {
		t.Fatalf("mode = %v, want monitor during popup launch", m.mode)
	}

	for _, key := range []tea.KeyMsg{keyRunes("q"), keyRunes("n"), {Type: tea.KeyCtrlC}} {
		updated, cmd := m.Update(key)
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("%q returned command while popup launch is running", key.String())
		}
		if m.mode != modeMonitor {
			t.Fatalf("%q changed mode = %v, want monitor", key.String(), m.mode)
		}
		if !m.newPane.launching {
			t.Fatalf("%q cleared launching flag", key.String())
		}
		if m.newPanePopupOpen {
			t.Fatalf("%q opened another popup while launch is running", key.String())
		}
	}
	if launchCalls != 0 {
		t.Fatalf("LaunchPane calls before launch command execution = %d, want 0", launchCalls)
	}
}

func TestNewPanePopupCancelDoesNotLaunch(t *testing.T) {
	launched := false
	m := newModel(Options{
		NewPanePrompt: func(NewPanePromptRequest) (LaunchRequest, bool, error) {
			return LaunchRequest{}, true, nil
		},
		LaunchPane: func(LaunchRequest) (string, error) {
			launched = true
			return "", nil
		},
	})

	updated, cmd := m.Update(keyRunes("n"))
	m = updated.(model)
	updated, next := m.Update(cmd())
	m = updated.(model)

	if next != nil {
		t.Fatal("canceled popup returned launch command")
	}
	if launched {
		t.Fatal("LaunchPane called for canceled popup")
	}
	if m.newPanePopupOpen {
		t.Fatal("newPanePopupOpen = true after cancel")
	}
}

func TestNewPanePopupCancelPreservesNewerNotice(t *testing.T) {
	m := newModel(Options{
		NewPanePrompt: func(NewPanePromptRequest) (LaunchRequest, bool, error) {
			return LaunchRequest{}, true, nil
		},
		LaunchPane: func(LaunchRequest) (string, error) {
			return "", nil
		},
	})

	updated, cmd := m.Update(keyRunes("n"))
	m = updated.(model)
	m.notice = "child #123 merged"
	updated, next := m.Update(cmd())
	m = updated.(model)

	if next != nil {
		t.Fatal("canceled popup returned launch command")
	}
	if m.notice != "child #123 merged" {
		t.Fatalf("notice = %q, want newer notice preserved", m.notice)
	}
}

func TestHelpKeyUsesPopupWhenConfigured(t *testing.T) {
	calls := 0
	m := newModel(Options{
		HelpPopup: func() error {
			calls++
			return nil
		},
	})

	updated, cmd := m.Update(keyRunes("?"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("? returned nil command, want help popup command")
	}
	if calls != 0 {
		t.Fatalf("help popup calls before command execution = %d, want 0", calls)
	}
	if m.mode != modeMonitor {
		t.Fatalf("mode = %v, want monitor while popup owns input", m.mode)
	}
	if !m.helpPopupOpen {
		t.Fatal("helpPopupOpen = false, want true")
	}
	if m.notice != helpPopupOpeningNotice {
		t.Fatalf("notice = %q, want %q", m.notice, helpPopupOpeningNotice)
	}

	updated, next := m.Update(cmd())
	m = updated.(model)
	if next != nil {
		t.Fatal("help popup completion returned command")
	}
	if calls != 1 {
		t.Fatalf("help popup calls = %d, want 1", calls)
	}
	if m.helpPopupOpen {
		t.Fatal("helpPopupOpen = true after popup completion")
	}
	if m.notice != "" {
		t.Fatalf("notice = %q, want cleared", m.notice)
	}
}

func TestHelpPopupOpenBlocksMonitorKeys(t *testing.T) {
	m := newModel(Options{
		HelpPopup: func() error {
			return nil
		},
	})

	updated, popupCmd := m.Update(keyRunes("?"))
	m = updated.(model)
	if popupCmd == nil {
		t.Fatal("? returned nil command, want help popup command")
	}
	if !m.helpPopupOpen {
		t.Fatal("helpPopupOpen = false, want true")
	}

	for _, key := range []tea.KeyMsg{keyRunes("q"), keyRunes("n"), {Type: tea.KeyCtrlC}} {
		updated, cmd := m.Update(key)
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("%q returned command while help popup is open", key.String())
		}
		if m.mode != modeMonitor {
			t.Fatalf("%q changed mode = %v, want monitor", key.String(), m.mode)
		}
		if !m.helpPopupOpen {
			t.Fatalf("%q cleared helpPopupOpen", key.String())
		}
	}
}

func TestHelpPopupFailureSurfacesNotice(t *testing.T) {
	m := newModel(Options{
		HelpPopup: func() error {
			return errBoom
		},
	})

	updated, cmd := m.Update(keyRunes("?"))
	m = updated.(model)
	updated, next := m.Update(cmd())
	m = updated.(model)

	if next != nil {
		t.Fatal("help popup failure returned command")
	}
	if m.helpPopupOpen {
		t.Fatal("helpPopupOpen = true after popup failure")
	}
	if m.notice != "help popup: boom" {
		t.Fatalf("notice = %q, want help popup error", m.notice)
	}
}

func TestSettingsKeyUsesPopupAndReloadsRuntime(t *testing.T) {
	popupCalls := 0
	reloadCalls := 0
	issueLaunchCalls := 0
	m := newModel(Options{
		SettingsPopup: func(req SettingsPopupRequest) (SettingsPopupResult, bool, error) {
			popupCalls++
			if req.ProjectRoot != "/repo" {
				t.Fatalf("settings popup project root = %q, want /repo", req.ProjectRoot)
			}
			return SettingsPopupResult{Saved: true, Scope: "user", Path: "/tmp/fanout-config.json"}, false, nil
		},
		ReloadSettings: func() (SettingsRuntime, error) {
			reloadCalls++
			return SettingsRuntime{
				WatchLabel:    "fanout:test",
				WatchInterval: time.Minute,
				LaunchIssue: func(int, string, map[string]string) (string, error) {
					issueLaunchCalls++
					return "launched with reloaded settings", nil
				},
			}, nil
		},
		ProjectRoot: "/repo",
	})

	updated, cmd := m.Update(keyRunes("s"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("s returned nil command, want settings popup command")
	}
	if popupCalls != 0 {
		t.Fatalf("popup calls before command execution = %d, want 0", popupCalls)
	}
	if m.mode != modeMonitor {
		t.Fatalf("mode = %v, want monitor while popup owns input", m.mode)
	}
	if !m.settingsPopupOpen {
		t.Fatal("settingsPopupOpen = false, want true")
	}
	if m.notice != settingsPopupOpeningNotice {
		t.Fatalf("notice = %q, want %q", m.notice, settingsPopupOpeningNotice)
	}

	updated, reloadCmd := m.Update(cmd())
	m = updated.(model)
	if reloadCmd == nil {
		t.Fatal("settings popup save returned nil command, want reload command")
	}
	if popupCalls != 1 {
		t.Fatalf("popup calls = %d, want 1", popupCalls)
	}
	if m.settingsPopupOpen {
		t.Fatal("settingsPopupOpen = true after popup completion")
	}

	updated, next := m.Update(reloadCmd())
	m = updated.(model)
	if next != nil {
		t.Fatal("settings reload returned command with nil watcher")
	}
	if reloadCalls != 1 {
		t.Fatalf("reload calls = %d, want 1", reloadCalls)
	}
	if m.opts.WatchLabel != "fanout:test" || m.opts.WatchInterval != time.Minute {
		t.Fatalf("runtime = label %q interval %s, want reloaded", m.opts.WatchLabel, m.opts.WatchInterval)
	}
	if m.opts.LaunchIssue == nil {
		t.Fatal("LaunchIssue = nil, want reloaded launcher")
	}
	notice, err := m.opts.LaunchIssue(123, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if issueLaunchCalls != 1 || notice != "launched with reloaded settings" {
		t.Fatalf("LaunchIssue calls=%d notice=%q, want reloaded launcher", issueLaunchCalls, notice)
	}
	if m.notice != "settings saved: /tmp/fanout-config.json" {
		t.Fatalf("notice = %q, want settings saved", m.notice)
	}
}

func TestSettingsPopupOpenBlocksMonitorKeys(t *testing.T) {
	m := newModel(Options{
		SettingsPopup: func(SettingsPopupRequest) (SettingsPopupResult, bool, error) {
			return SettingsPopupResult{}, true, nil
		},
	})

	updated, popupCmd := m.Update(keyRunes("s"))
	m = updated.(model)
	if popupCmd == nil {
		t.Fatal("s returned nil command, want settings popup command")
	}
	if !m.settingsPopupOpen {
		t.Fatal("settingsPopupOpen = false, want true")
	}

	for _, key := range []tea.KeyMsg{keyRunes("q"), keyRunes("s"), {Type: tea.KeyCtrlC}} {
		updated, cmd := m.Update(key)
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("%q returned command while settings popup is open", key.String())
		}
		if m.mode != modeMonitor {
			t.Fatalf("%q changed mode = %v, want monitor", key.String(), m.mode)
		}
		if !m.settingsPopupOpen {
			t.Fatalf("%q cleared settingsPopupOpen", key.String())
		}
	}
}

func TestSettingsReloadPreservesRunningWatcher(t *testing.T) {
	m := newModel(Options{
		Watcher:       &fakeWatcherRunner{},
		WatchInterval: time.Minute,
	})
	m.watchRunning = true

	updated, cmd := m.Update(settingsReloadedMsg{
		result: SettingsPopupResult{Saved: true, Path: "/tmp/fanout-config.json"},
		runtime: SettingsRuntime{
			Watcher:       &fakeWatcherRunner{},
			WatchInterval: 2 * time.Minute,
			WatchLabel:    "fanout:test",
		},
	})
	m = updated.(model)
	if !m.watchRunning {
		t.Fatal("watchRunning = false after settings reload, want in-flight cycle preserved")
	}
	if cmd == nil {
		t.Fatal("settings reload with watcher returned nil command, want next watch tick")
	}
	if m.opts.WatchInterval != 2*time.Minute || m.opts.WatchLabel != "fanout:test" {
		t.Fatalf("runtime not applied: interval=%s label=%q", m.opts.WatchInterval, m.opts.WatchLabel)
	}
}

func TestSettingsReloadInvalidatesOlderWatchTicks(t *testing.T) {
	m := newModel(Options{
		Watcher:       &fakeWatcherRunner{},
		WatchInterval: time.Minute,
	})
	m.notifyErr = "old notifier failure"
	before := m.watchTickGen

	updated, cmd := m.Update(settingsReloadedMsg{
		result: SettingsPopupResult{Saved: true, Path: "/tmp/fanout-config.json"},
		runtime: SettingsRuntime{
			Watcher:       &fakeWatcherRunner{},
			WatchInterval: 2 * time.Minute,
		},
	})
	m = updated.(model)
	if m.watchTickGen != before+1 {
		t.Fatalf("watchTickGen = %d, want %d", m.watchTickGen, before+1)
	}
	if m.notifyErr != "" {
		t.Fatalf("notifyErr = %q, want cleared after settings reload", m.notifyErr)
	}
	if cmd == nil {
		t.Fatal("settings reload with watcher returned nil command, want replacement tick")
	}

	updated, staleCmd := m.Update(watchTickMsg{at: time.Unix(1, 0), gen: before})
	m = updated.(model)
	if staleCmd != nil {
		t.Fatal("stale pre-reload watch tick returned command")
	}
	if m.watchRunning {
		t.Fatal("watchRunning = true after stale pre-reload watch tick")
	}
}

func TestSettingsRowMasksSensitiveValues(t *testing.T) {
	row := settingsRow{
		spec:  fanoutsettings.ConfigKey{Key: "slackWebhookURL", Kind: fanoutsettings.ValueString, Sensitive: true},
		value: fanoutsettings.StringValue("https://hooks.slack.com/services/secret"),
	}
	m := newModel(Options{})
	m.settings = settingsForm{rows: []settingsRow{row}, cursor: 1}

	view := m.settingsRowView(1, row)
	if strings.Contains(view, "hooks.slack.com") || strings.Contains(view, "secret") {
		t.Fatalf("sensitive row leaked URL:\n%s", view)
	}
	if !strings.Contains(view, "set") {
		t.Fatalf("sensitive row = %q, want masked persisted value", view)
	}

	m.settings.editing = true
	m.settings.editText = "https://hooks.slack.com/services/typed-secret"
	editView := m.settingsRowView(1, row)
	if strings.Contains(editView, "hooks.slack.com") || strings.Contains(editView, "secret") {
		t.Fatalf("sensitive edit row leaked URL:\n%s", editView)
	}
	if !strings.Contains(editView, "****************") {
		t.Fatalf("sensitive edit row = %q, want masked edit value", editView)
	}
}

func TestSettingsRowMarksForwardedEnvOverride(t *testing.T) {
	spec := fanoutsettings.ConfigKey{
		Key:  "watcher",
		Kind: fanoutsettings.ValueBool,
		Env:  "FANOUT_TEST_ONLY_SETTINGS_ENV_OVERRIDE_9D0A4F0E",
	}
	t.Setenv(SettingsEnvOverridesEnv, spec.Env)
	row := settingsRow{
		spec:        spec,
		value:       fanoutsettings.BoolValue(false),
		envOverride: settingsEnvOverridePresent(spec),
	}
	m := newModel(Options{})
	m.settings = settingsForm{rows: []settingsRow{row}, cursor: 1}

	view := m.settingsRowView(1, row)
	if !strings.Contains(view, " env") {
		t.Fatalf("settings row = %q, want forwarded env marker", view)
	}
}

func TestSettingsRowUsesTriangleSelectionMarker(t *testing.T) {
	row := settingsRow{
		spec:  fanoutsettings.ConfigKey{Key: "watcher", Kind: fanoutsettings.ValueBool},
		value: fanoutsettings.BoolValue(true),
	}
	m := newModel(Options{})
	m.settings = settingsForm{rows: []settingsRow{row}, cursor: 1}

	view := m.settingsRowView(1, row)
	if !strings.HasPrefix(view, selectedItemMarker) || strings.HasPrefix(view, "> ") {
		t.Fatalf("settings selected row marker = %q, want %q", view, selectedItemMarker)
	}
}

func TestSettingsSaveBlocksInvalidConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	configPath := filepath.Join(xdg, "fanout", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "{broken"
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newModel(Options{ProjectRoot: t.TempDir()})
	m.openSettingsForm(fanoutsettings.ConfigScopeUser)
	if !m.settings.loadErr {
		t.Fatal("settings loadErr = false, want true for invalid JSON")
	}

	updated, cmd := m.saveSettings()
	m = updated.(model)
	if cmd != nil {
		t.Fatal("save invalid settings returned command")
	}
	if !strings.Contains(m.settings.err, "fix or remove the invalid config before saving") {
		t.Fatalf("settings err = %q, want save-blocking error", m.settings.err)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("invalid config changed after blocked save:\nwant %q\ngot  %q", original, body)
	}
}

func TestSettingsRepoSaveDeletesDisabledUnsafeKeys(t *testing.T) {
	repo := t.TempDir()
	configPath := fanoutsettings.RepoConfigPath(repo)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "watcher": true,
  "watcherLabel": "fanout:ready",
  "notifications": "slack ntfy bell",
  "ntfyURL": "https://ntfy.example/topic",
  "slackWebhookURL": "https://hooks.slack.com/services/secret"
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newModel(Options{ProjectRoot: repo})
	m.openSettingsForm(fanoutsettings.ConfigScopeRepo)
	updated, cmd := m.saveSettings()
	m = updated.(model)
	if cmd != nil {
		t.Fatal("repo settings save returned reload command")
	}
	if m.settings.err != "" {
		t.Fatalf("settings err = %q", m.settings.err)
	}

	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"watcher", "ntfyURL", "slackWebhookURL"} {
		if _, ok := root[key]; ok {
			t.Fatalf("%s should be deleted from repo config:\n%s", key, body)
		}
	}
	if root["notifications"] != "bell" {
		t.Fatalf("notifications = %#v, want safe selector preserved", root["notifications"])
	}
	if root["watcherLabel"] != "fanout:ready" {
		t.Fatalf("watcherLabel = %#v, want preserved", root["watcherLabel"])
	}
}

func TestSettingsRepoSavePreservesSafeNotificationValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "none", raw: "none", want: "none"},
		{name: "comma-safe", raw: "bell,tmux", want: "bell,tmux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			configPath := fanoutsettings.RepoConfigPath(repo)
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatal(err)
			}
			body := fmt.Appendf(nil, `{"notifications": %q, "watcherLabel": "fanout:ready"}`, tc.raw)
			if err := os.WriteFile(configPath, body, 0o644); err != nil {
				t.Fatal(err)
			}

			m := newModel(Options{ProjectRoot: repo})
			m.openSettingsForm(fanoutsettings.ConfigScopeRepo)
			updated, cmd := m.saveSettings()
			m = updated.(model)
			if cmd != nil {
				t.Fatal("repo settings save returned reload command")
			}
			if m.settings.err != "" {
				t.Fatalf("settings err = %q", m.settings.err)
			}

			body, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]any
			if err := json.Unmarshal(body, &root); err != nil {
				t.Fatal(err)
			}
			if root["notifications"] != tc.want {
				t.Fatalf("notifications = %#v, want %q\n%s", root["notifications"], tc.want, body)
			}
		})
	}
}

func TestHelpKeyOpensAndClosesModal(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 40

	updated, cmd := m.Update(keyRunes("?"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("? returned command, want nil")
	}
	if m.mode != modeHelp {
		t.Fatalf("mode = %v, want help", m.mode)
	}
	view := m.View()
	for _, want := range []string{"Keyboard shortcuts", "[n]", "New agent pane", "Esc / q / ? close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view missing %q:\n%s", want, view)
		}
	}

	closeKeys := []tea.KeyMsg{
		{Type: tea.KeyEsc},
		keyRunes("q"),
		keyRunes("?"),
	}
	for _, key := range closeKeys {
		m.mode = modeHelp
		updated, cmd = m.Update(key)
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("%q returned command, want nil", key.String())
		}
		if m.mode != modeMonitor {
			t.Fatalf("after %q mode = %v, want monitor", key.String(), m.mode)
		}
	}
}

func TestHelpModalFitsStandardTerminalHeight(t *testing.T) {
	m := newModel(Options{})
	m.width = 80
	m.height = 24
	m.mode = modeHelp

	view := m.View()
	for _, want := range []string{"[Enter]", "Create / next", "[Esc]", "Cancel / back", "Esc / q / ? close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("80x24 help view missing %q:\n%s", want, view)
		}
	}
	if got := lipgloss.Height(m.helpView()); got > 24 {
		t.Fatalf("help modal height = %d lines, want <= 24 (bottom border clips otherwise)", got)
	}
}

func TestHelpModalRendersCompactKeyLabels(t *testing.T) {
	m := newModel(Options{})
	m.width = 80
	view := m.helpView()
	for _, want := range []string{"[j/k]", "[ / ]", "[Left/Right]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view missing compact label %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"[[ / ]]", "j/k,\n", "Left /\n"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("help view contains malformed label %q:\n%s", unwanted, view)
		}
	}
}

func TestHelpModalBlocksBackgroundKeys(t *testing.T) {
	m := newModel(Options{})
	m.mode = modeHelp

	updated, cmd := m.Update(keyRunes("n"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("background key returned command while help is open")
	}
	if m.mode != modeHelp {
		t.Fatalf("mode = %v, want help", m.mode)
	}
	if m.newPane.prompt.Value() != "" {
		t.Fatalf("new pane form changed behind help: %#v", m.newPane)
	}
}

func TestQuestionMarkIsTextWhileEditing(t *testing.T) {
	m := newModel(Options{})

	updated, _ := m.Update(keyRunes("/"))
	m = updated.(model)
	updated, _ = m.Update(keyRunes("?"))
	m = updated.(model)
	if m.mode != modeMonitor {
		t.Fatalf("filter ? changed mode = %v, want monitor", m.mode)
	}
	if m.filterQuery != "?" {
		t.Fatalf("filterQuery = %q, want ?", m.filterQuery)
	}

	m = newModel(Options{})
	m.openNewPaneForm()
	updated, _ = m.Update(keyRunes("?"))
	m = updated.(model)
	if m.mode != modeNewPane {
		t.Fatalf("new pane ? changed mode = %v, want new pane", m.mode)
	}
	if got := m.newPane.prompt.Value(); got != "?" {
		t.Fatalf("prompt = %q, want ?", got)
	}
}

func TestFooterPointsToHelpWithoutLongKeyList(t *testing.T) {
	m := newModel(Options{})
	got := m.footerText()
	if !strings.Contains(got, "? help") {
		t.Fatalf("footer = %q, want ? help", got)
	}
	for _, unwanted := range []string{"q quit", "n new", "c close", "x cleanup"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("footer = %q, should not contain %q", got, unwanted)
		}
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

func TestShellTerminalKeyCarriesSourceProjectRoot(t *testing.T) {
	var got []ShellLaunchRequest
	m := newModel(Options{
		ProjectRoot: "/repo",
		LaunchShell: func(req ShellLaunchRequest) error {
			got = append(got, req)
			return nil
		},
	})
	m.allPanes = []paneView{{
		Parent:            "100",
		IssueNum:          101,
		Name:              "child",
		WorktreePath:      ".fanout/worktrees/child",
		worktreeAbs:       "/sibling/.fanout/worktrees/child",
		sourceProjectRoot: "/sibling",
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
	if len(got) != 1 || got[0].TargetPath != "/sibling/.fanout/worktrees/child" || got[0].SourceProjectRoot != "/sibling" {
		t.Fatalf("worktree shell request = %#v, want sibling target and source root", got)
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

func TestAttachAgentKeyOpensSameWorktreeForm(t *testing.T) {
	m := newModel(Options{ProjectRoot: "/repo", DefaultAgent: "codex"})
	m.allPanes = []paneView{{
		Parent:       "100",
		IssueNum:     101,
		Name:         "child",
		BranchName:   "fanout/child-101",
		WorktreePath: ".fanout/worktrees/child",
		worktreeAbs:  "/repo/.fanout/worktrees/child",
	}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("a"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("a returned nil command, want repo file reload")
	}
	if m.mode != modeNewPane || m.newPane.attach == nil {
		t.Fatalf("mode/attach = %v/%#v, want attach form", m.mode, m.newPane.attach)
	}
	want := AttachTarget{
		TargetPath:       "/repo/.fanout/worktrees/child",
		SourceParent:     "100",
		SourceIssueNum:   101,
		SourceBranchName: "fanout/child-101",
		SourceLabel:      "#101",
	}
	if !reflect.DeepEqual(*m.newPane.attach, want) {
		t.Fatalf("attach target = %#v, want %#v", *m.newPane.attach, want)
	}
	if agents := m.selectedNewPaneAgents(); len(agents) != 1 || agents[0] != "codex" {
		t.Fatalf("default agents = %#v, want [codex]", agents)
	}
}

func TestAttachAgentKeyCarriesSourceProjectRoot(t *testing.T) {
	m := newModel(Options{ProjectRoot: "/repo", DefaultAgent: "codex"})
	m.allPanes = []paneView{{
		Parent:            "100",
		IssueNum:          101,
		Name:              "child",
		BranchName:        "fanout/child-101",
		WorktreePath:      ".fanout/worktrees/child",
		worktreeAbs:       "/sibling/.fanout/worktrees/child",
		sourceProjectRoot: "/sibling",
	}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("a"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("a returned nil command, want repo file reload")
	}
	if m.mode != modeNewPane || m.newPane.attach == nil {
		t.Fatalf("mode/attach = %v/%#v, want attach form", m.mode, m.newPane.attach)
	}
	if got := m.newPane.attach.SourceProjectRoot; got != "/sibling" {
		t.Fatalf("SourceProjectRoot = %q, want /sibling", got)
	}
	if got := m.newPane.attach.TargetPath; got != "/sibling/.fanout/worktrees/child" {
		t.Fatalf("TargetPath = %q, want sibling worktree", got)
	}
}

func TestAttachAgentKeyPreservesAttachedAgentSourceIdentity(t *testing.T) {
	m := newModel(Options{ProjectRoot: "/repo", DefaultAgent: "codex"})
	m.allPanes = []paneView{{
		Parent:         "@manual",
		IssueNum:       -1,
		Kind:           state.PaneKindAttachedAgent,
		Name:           "codex for #101",
		BranchName:     "fanout/child-101",
		WorktreePath:   ".fanout/worktrees/child",
		worktreeAbs:    "/repo/.fanout/worktrees/child",
		SourceParent:   "100",
		SourceIssueNum: 101,
	}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("a"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("a returned nil command, want repo file reload")
	}
	if m.mode != modeNewPane || m.newPane.attach == nil {
		t.Fatalf("mode/attach = %v/%#v, want attach form", m.mode, m.newPane.attach)
	}
	if got := m.newPane.attach.SourceParent; got != "100" {
		t.Fatalf("SourceParent = %q, want 100", got)
	}
	if got := m.newPane.attach.SourceIssueNum; got != 101 {
		t.Fatalf("SourceIssueNum = %d, want 101", got)
	}
	if got := m.newPane.attach.SourceTaskID; got != "" {
		t.Fatalf("SourceTaskID = %q, want empty", got)
	}
	if got := m.newPane.attach.SourceLabel; got != "#101" {
		t.Fatalf("SourceLabel = %q, want #101", got)
	}
}

func TestAttachAgentKeyRequiresSelectedWorktree(t *testing.T) {
	called := false
	m := newModel(Options{
		ProjectRoot: "/repo",
		LaunchAttach: func(AttachLaunchRequest) (string, error) {
			called = true
			return "", nil
		},
	})
	m.allPanes = []paneView{{Parent: "100", IssueNum: 101, Name: "queued"}}
	m.refreshRows()

	updated, cmd := m.Update(keyRunes("a"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("a returned command for row without worktree, want nil")
	}
	if called {
		t.Fatal("LaunchAttach was called for row without worktree")
	}
	if !strings.Contains(m.notice, "no worktree path") {
		t.Fatalf("notice = %q, want no worktree path", m.notice)
	}
}

func TestNewPaneFormRequiresPrompt(t *testing.T) {
	m := newModel(Options{LaunchPane: func(LaunchRequest) (string, error) {
		t.Fatal("LaunchPane should not be called without a prompt")
		return "", nil
	}})
	m.openNewPaneForm()

	if cmd := m.submitNewPane(); cmd != nil {
		t.Fatal("submitNewPane returned a command without a prompt")
	}
	if m.newPane.err != "prompt is required" {
		t.Fatalf("form error = %q, want prompt is required", m.newPane.err)
	}
}

func TestNewPaneFormRequiresAgentSelection(t *testing.T) {
	m := newModel(Options{LaunchPane: func(LaunchRequest) (string, error) {
		t.Fatal("LaunchPane should not be called without a selected agent")
		return "", nil
	}})
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("Inspect the HTTP API")
	for _, agentName := range launchAgents {
		m.newPane.agentCount[agentName] = 0
	}

	if cmd := m.submitNewPane(); cmd != nil {
		t.Fatal("submitNewPane returned a command without a selected agent")
	}
	if m.newPane.err != "select at least one agent" {
		t.Fatalf("form error = %q, want select at least one agent", m.newPane.err)
	}
}

func TestNewPaneFormSubmitsLaunchRequest(t *testing.T) {
	var got LaunchRequest
	called := false
	m := newModel(Options{
		DefaultAgent: "codex",
		LaunchPane: func(req LaunchRequest) (string, error) {
			called = true
			got = req
			return "", nil
		},
	})
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("Inspect the HTTP API")

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
	want := LaunchRequest{Prompt: "Inspect the HTTP API", Agents: []string{"codex"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch request = %#v, want %#v", got, want)
	}
}

func TestNewPaneFormSubmitsMultipleAgents(t *testing.T) {
	var got LaunchRequest
	m := newModel(Options{
		DefaultAgent: "codex",
		LaunchPane: func(req LaunchRequest) (string, error) {
			got = req
			return "", nil
		},
	})
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("Compare implementations")
	m.newPane.agentCount["claude"] = 1
	m.newPane.agentCount["codex"] = 2

	cmd := m.submitNewPane()
	if cmd == nil {
		t.Fatal("submitNewPane returned nil command")
	}
	_ = cmd()

	want := LaunchRequest{Prompt: "Compare implementations", Agents: []string{"claude", "codex", "codex"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch request = %#v, want %#v", got, want)
	}
}

func TestAttachAgentFormSubmitsAttachRequest(t *testing.T) {
	target := AttachTarget{
		TargetPath:       "/repo/.fanout/worktrees/child",
		SourceParent:     "100",
		SourceIssueNum:   101,
		SourceBranchName: "fanout/child-101",
		SourceLabel:      "#101",
	}
	var got AttachLaunchRequest
	called := false
	m := newModel(Options{
		DefaultAgent: "codex",
		LaunchAttach: func(req AttachLaunchRequest) (string, error) {
			called = true
			got = req
			return "", nil
		},
	})
	m.openNewPaneForm()
	m.newPane.attach = &target
	m.newPane.prompt.SetValue("Inspect the HTTP API")

	cmd := m.submitNewPane()
	if cmd == nil {
		t.Fatal("submitNewPane returned nil command")
	}
	if !m.newPane.launching {
		t.Fatal("launching = false, want true")
	}
	msg := cmd()
	if launch, ok := msg.(launchPaneMsg); !ok || launch.err != nil || !launch.attached {
		t.Fatalf("launch message = %#v, want nil-error attached launchPaneMsg", msg)
	}
	if !called {
		t.Fatal("LaunchAttach was not called")
	}
	want := AttachLaunchRequest{Prompt: "Inspect the HTTP API", Agents: []string{"codex"}, Target: target}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attach request = %#v, want %#v", got, want)
	}
}

func TestNewPaneFormPromptNewlineKeysDoNotSubmit(t *testing.T) {
	called := false
	m := newModel(Options{
		LaunchPane: func(LaunchRequest) (string, error) {
			called = true
			return "", nil
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
		LaunchPane: func(req LaunchRequest) (string, error) {
			got = req
			return "", nil
		},
	})
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("Inspect the API\nCheck handlers")

	cmd := m.submitNewPane()
	if cmd == nil {
		t.Fatal("submitNewPane returned nil command")
	}
	_ = cmd()

	want := LaunchRequest{Prompt: "Inspect the API\nCheck handlers", Agents: []string{"codex"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch request = %#v, want %#v", got, want)
	}
}

func TestNewPaneFormAcceptsCJKPromptInput(t *testing.T) {
	var got LaunchRequest
	m := newModel(Options{
		DefaultAgent: "codex",
		LaunchPane: func(req LaunchRequest) (string, error) {
			got = req
			return "", nil
		},
	})
	m.openNewPaneForm()

	updated, _ := m.Update(keyRunes("日本語入力テスト"))
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = updated.(model)
	updated, _ = m.Update(keyRunes("中文한글"))
	m = updated.(model)

	cmd := m.submitNewPane()
	if cmd == nil {
		t.Fatal("submitNewPane returned nil command")
	}
	_ = cmd()

	if got.Prompt != "日本語入力テスト\n中文한글" {
		t.Fatalf("CJK prompt = %q", got.Prompt)
	}
}

func TestNewPaneViewRendersModalOverMonitor(t *testing.T) {
	t.Setenv(EnhancedKeysEnv, "") // default-on: Shift+Enter is advertised
	m := newModel(Options{})
	m.width = 100
	// Tall enough that the centered modal does not cover the monitor's header
	// and pane rows behind it (the framed inputs make the modal ~22 lines).
	m.height = 48
	m.allPanes = []paneView{{Parent: "100", IssueNum: 1, Name: "existing", TmuxState: "live"}}
	m.refreshRows()
	m.openNewPaneForm()

	view := m.View()
	for _, want := range []string{"PARENT", "New agent pane", "shift+enter/ctrl+j newline", "tab field"} {
		if !strings.Contains(view, want) {
			t.Fatalf("modal view missing %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"left/right count"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("modal view should not contain %q:\n%s", unwanted, view)
		}
	}
}

func TestNewPaneViewHidesShiftEnterHintWhenOptedOut(t *testing.T) {
	t.Setenv(EnhancedKeysEnv, "0") // opt-out: Shift+Enter submits, so don't advertise it
	m := newModel(Options{})
	m.width = 100
	m.height = 48
	m.openNewPaneForm()

	view := m.View()
	if !strings.Contains(view, "ctrl+j newline") {
		t.Fatalf("modal view missing ctrl+j hint:\n%s", view)
	}
	if strings.Contains(view, "shift+enter") {
		t.Fatalf("modal view should not advertise shift+enter when opted out:\n%s", view)
	}
}

func TestNewPaneViewRendersCJKPromptInsideFrame(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 30
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("日本語入力テスト")

	view := m.newPaneView()
	if !strings.Contains(view, "日本語入力テスト") {
		t.Fatalf("modal view missing CJK prompt:\n%s", view)
	}
	// Double modal outer frame + the single Prompt box; the Agent toggle is unframed.
	if got := strings.Count(view, "╔"); got != 1 {
		t.Fatalf("modal frame top-left corners: got %d, want 1:\n%s", got, view)
	}
	if got := strings.Count(view, "┌"); got != 1 {
		t.Fatalf("input box top-left corners: got %d, want 1:\n%s", got, view)
	}
}

func TestNewPaneViewFramesTextInputs(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 30
	m.openNewPaneForm()

	view := m.newPaneView()
	// One double border for the modal itself plus one single border around the
	// Prompt textarea; the Agent toggle stays unframed.
	if got := strings.Count(view, "╔"); got != 1 {
		t.Fatalf("modal frame top-left corners: got %d, want 1:\n%s", got, view)
	}
	if got := strings.Count(view, "┌"); got != 1 {
		t.Fatalf("input box top-left corners: got %d, want 1:\n%s", got, view)
	}

	// Collect each top border. Order: modal outer frame, then the Prompt box.
	var modalTops, inputTops []string
	for ln := range strings.SplitSeq(view, "\n") {
		if i := strings.Index(ln, "╔"); i >= 0 {
			j := strings.Index(ln, "╗")
			if j >= i {
				modalTops = append(modalTops, ln[i:j+len("╗")])
			}
		}
		if i := strings.Index(ln, "┌"); i >= 0 {
			j := strings.Index(ln, "┐")
			if j >= i {
				inputTops = append(inputTops, ln[i:j+len("┐")])
			}
		}
	}
	if len(modalTops) != 1 || len(inputTops) != 1 {
		t.Fatalf("top borders: modal=%d input=%d, want 1 each:\n%s", len(modalTops), len(inputTops), view)
	}
}

func TestNewPaneViewUsesOneCellModalPadding(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 30
	m.openNewPaneForm()

	view := m.newPaneView()
	lines := strings.Split(view, "\n")
	if len(lines) < 3 {
		t.Fatalf("modal view too short:\n%s", view)
	}
	if strings.Trim(strings.Trim(lines[1], "║"), " ") != "" {
		t.Fatalf("modal should have one blank top padding line:\n%s", view)
	}
	for _, line := range lines {
		if strings.Contains(line, selectedItemMarker+"Prompt") {
			if !strings.HasPrefix(line, "║ "+selectedItemMarker) || strings.HasPrefix(line, "║  "+selectedItemMarker) {
				t.Fatalf("modal content should start after one cell of padding:\n%s", view)
			}
			return
		}
	}
	t.Fatalf("modal view missing selected prompt line:\n%s", view)
}

func TestNewPanePromptRemovesTextareaPromptMarker(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 30
	m.openNewPaneForm()

	view := m.newPaneView()
	if strings.Contains(view, "> Prompt") || strings.Contains(view, "> ") {
		t.Fatalf("new pane prompt view should not render the old > marker:\n%s", view)
	}
	if !strings.Contains(view, selectedItemMarker+"Prompt") {
		t.Fatalf("new pane prompt view missing selected marker %q:\n%s", selectedItemMarker, view)
	}
}

func TestNewPaneViewSeparatesFormSections(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 30
	m.openNewPaneForm()

	view := m.newPaneView()
	lines := strings.Split(view, "\n")
	promptBoxEnd := -1
	planLine := -1
	agentLine := -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "┘"):
			promptBoxEnd = i
		case strings.Contains(line, "decompose via /fanout plan"):
			planLine = i
		case strings.Contains(line, "Agent"):
			agentLine = i
		}
	}
	if promptBoxEnd < 0 || planLine < 0 || agentLine < 0 {
		t.Fatalf("new pane view missing prompt/plan/agent sections:\n%s", view)
	}
	if promptBoxEnd+2 != planLine || strings.Trim(strings.Trim(lines[promptBoxEnd+1], "║"), " ") != "" {
		t.Fatalf("new pane view should leave one blank line between prompt and plan sections:\n%s", view)
	}
	if planLine+2 != agentLine || strings.Trim(strings.Trim(lines[planLine+1], "║"), " ") != "" {
		t.Fatalf("new pane view should leave one blank line between plan and agent sections:\n%s", view)
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

func TestNewPaneLaunchSuccessReportsMultiplePanes(t *testing.T) {
	m := newModel(Options{})
	m.openNewPaneForm()

	updated, _ := m.Update(launchPaneMsg{count: 3})
	got := updated.(model)

	if got.notice != "created 3 new agent panes" {
		t.Fatalf("notice = %q, want multiple pane count", got.notice)
	}
}

func TestNewPaneLaunchSuccessSurfacesNotice(t *testing.T) {
	m := newModel(Options{})
	m.openNewPaneForm()

	skip := "base branch refresh skipped: local branch main is checked out"
	updated, _ := m.Update(launchPaneMsg{notice: skip})
	got := updated.(model)

	if got.mode != modeMonitor {
		t.Fatalf("mode = %v, want monitor", got.mode)
	}
	if got.notice != skip {
		t.Fatalf("notice = %q, want the launch notice %q", got.notice, skip)
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

// The tmux display-popup already frames the prompt-only popup, so its content
// must drop the modal border and the duplicate "New agent pane" heading while
// keeping the popup pty width stable.
func TestNewPanePromptOnlyViewFillsPopupWithoutModalFrame(t *testing.T) {
	m := newModel(Options{})
	m.promptOnly = true
	m.width = 88
	m.height = 18
	m.openNewPaneForm()

	view := m.View()
	// Only the Prompt input box is framed; the modal outer border is gone.
	if got := strings.Count(view, "┌"); got != 1 {
		t.Fatalf("framed input boxes: got %d top-left corners, want 1:\n%s", got, view)
	}
	if strings.Contains(view, "New agent pane") {
		t.Fatalf("popup view should not repeat the tmux -T title:\n%s", view)
	}
	if got := lipgloss.Width(view); got != 88 {
		t.Fatalf("lipgloss.Width(view) = %d, want 88:\n%s", got, view)
	}
}

func TestNewPanePromptOnlyViewFitsStandardPopupWithModeRow(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return nil, nil
		},
	})
	m.promptOnly = true
	m.width = 74
	m.height = 20
	m.openNewPaneForm()

	view := m.View()
	if !strings.Contains(view, "Mode") {
		t.Fatalf("popup view should render the mode selector when issue mode is wired:\n%s", view)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("popup view height = %d, want <= %d:\n%s", got, m.height, view)
	}
}

func TestNewPanePromptOnlyErrorViewFitsStandardPopupWithModeRow(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return nil, nil
		},
	})
	m.promptOnly = true
	m.width = 74
	m.height = 20
	m.openNewPaneForm()

	if cmd := m.submitNewPane(); cmd != nil {
		t.Fatal("submitNewPane returned a command without a prompt")
	}
	view := m.View()
	if !strings.Contains(view, "error: prompt is required") {
		t.Fatalf("popup view missing prompt error:\n%s", view)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("popup error view height = %d, want <= %d:\n%s", got, m.height, view)
	}
}

func TestNewPaneFallbackPromptViewFitsStandardHeightWithModeRow(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return nil, nil
		},
	})
	m.width = 80
	m.height = 24
	m.openNewPaneForm()

	view := m.newPaneView()
	if !strings.Contains(view, "New agent pane") {
		t.Fatalf("fallback view should keep the in-modal title:\n%s", view)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("fallback prompt view height = %d, want <= %d:\n%s", got, m.height, view)
	}
}

func TestNewPaneFallbackIssueViewFitsStandardHeightWithModeRow(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return nil, nil
		},
	})
	m.width = 80
	m.height = 24
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue
	items := make([]IssueListItem, 12)
	for i := range items {
		items[i] = IssueListItem{Number: i + 1, Title: "issue row", HasOpenChildren: true}
	}
	p := &m.newPane.issuePicker
	p.loaded = true
	p.items = issuePickerItems(items)
	m.recomputePicker(p)

	view := m.newPaneView()
	if !strings.Contains(view, "more") {
		t.Fatalf("fallback issue view should window overflowing rows:\n%s", view)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("fallback issue view height = %d, want <= %d:\n%s", got, m.height, view)
	}
}

func TestNewPaneFallbackIssueLaunchingViewFitsStandardHeightWithModeRow(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return nil, nil
		},
	})
	m.width = 80
	// The issue plan checkbox adds a two-line block, so the launching issue form
	// (which also renders the "creating pane..." footer) needs one row more than
	// the childless-picker minimum before the floor-of-one picker window would
	// clip the modal top.
	m.height = 25
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue
	m.newPane.launching = true
	items := make([]IssueListItem, 12)
	for i := range items {
		items[i] = IssueListItem{Number: i + 1, Title: "issue row", HasOpenChildren: true}
	}
	p := &m.newPane.issuePicker
	p.loaded = true
	p.items = issuePickerItems(items)
	m.recomputePicker(p)

	view := m.newPaneView()
	if !strings.Contains(view, "creating pane...") {
		t.Fatalf("fallback issue launching view missing footer:\n%s", view)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("fallback issue launching view height = %d, want <= %d:\n%s", got, m.height, view)
	}
}

func TestNewPanePromptOnlyPromptNoticeViewFitsStandardPopup(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return nil, nil
		},
	})
	m.promptOnly = true
	m.width = 74
	m.height = 20
	m.openNewPaneForm()
	m.setNewPaneNotice("opened #42 in browser")

	view := m.View()
	if !strings.Contains(view, m.newPane.notice) {
		t.Fatalf("prompt popup view missing notice:\n%s", view)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("prompt popup notice view height = %d, want <= %d:\n%s", got, m.height, view)
	}
}

func TestNewPanePromptOnlyPromptEnhancedHintFitsMinimumPopup(t *testing.T) {
	t.Setenv(EnhancedKeysEnv, "")
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return nil, nil
		},
	})
	m.promptOnly = true
	m.width = 54
	m.height = 20
	m.openNewPaneForm()

	view := m.View()
	if !strings.Contains(view, "shift+enter/ctrl+j newline") {
		t.Fatalf("prompt popup view missing enhanced-key hint:\n%s", view)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("prompt popup enhanced hint view height = %d, want <= %d:\n%s", got, m.height, view)
	}
}

func TestNewPanePromptOnlyIssueNoticeViewFitsStandardPopup(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return nil, nil
		},
	})
	m.promptOnly = true
	m.width = 74
	m.height = 20
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue
	m.newPane.notice = "opened https://example.test/issues/1"
	items := make([]IssueListItem, 12)
	for i := range items {
		items[i] = IssueListItem{Number: i + 1, Title: "issue row", HasOpenChildren: true}
	}
	p := &m.newPane.issuePicker
	p.loaded = true
	p.items = issuePickerItems(items)
	m.recomputePicker(p)

	view := m.View()
	if !strings.Contains(view, m.newPane.notice) {
		t.Fatalf("issue popup view missing notice:\n%s", view)
	}
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("issue popup notice view height = %d, want <= %d:\n%s", got, m.height, view)
	}
}

func TestPopupContentStyleUsesOneCellPadding(t *testing.T) {
	view := popupContentStyle.Width(8).Render("x")
	lines := strings.Split(view, "\n")
	if len(lines) != 3 {
		t.Fatalf("popup content height = %d, want 3:\n%s", len(lines), view)
	}
	if strings.TrimSpace(lines[0]) != "" || strings.TrimSpace(lines[2]) != "" {
		t.Fatalf("popup content should have blank top/bottom padding:\n%s", view)
	}
	if !strings.HasPrefix(lines[1], " x") || strings.HasPrefix(lines[1], "  x") {
		t.Fatalf("popup content should have one leading cell of padding:\n%s", view)
	}
	if got := lipgloss.Width(view); got != 8 {
		t.Fatalf("popup content width = %d, want 8:\n%s", got, view)
	}
}

// The assign step of the prompt-only popup must also drop the modal border while
// keeping its "Assign agents" heading (that heading is step-2 information, not
// the popup title).
func TestNewPanePromptOnlyAssignViewDropsModalFrame(t *testing.T) {
	m := newModel(Options{})
	m.promptOnly = true
	m.width = 88
	m.height = 18
	m.openNewPaneForm()
	m.newPane.step = newPaneStepAssign
	m.newPane.assign = assignState{
		title: "#100 parent",
		rows:  []assignRow{{target: "101", label: "#101 child", agentIdx: 0}},
	}

	view := m.View()
	if got := strings.Count(view, "┌"); got != 0 {
		t.Fatalf("assign view should be borderless in the popup: got %d top-left corners:\n%s", got, view)
	}
	if !strings.Contains(view, "Assign agents") {
		t.Fatalf("assign view missing its heading:\n%s", view)
	}
}
