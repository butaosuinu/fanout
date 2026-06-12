package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/lifecycle"
	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
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
	if second.WaveLabel != "wave5" {
		t.Fatalf("WaveLabel = %q, want wave5", second.WaveLabel)
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
	degraded := issueStatus{Title: "child", State: "OPEN", Wave: 1, WaveLabel: "wave1", Blockers: "-", HasOpenBlockers: false, WaveDegraded: true}
	unblocked := issueStatus{Title: "child", State: "OPEN", Wave: 2, WaveLabel: "wave2", Blockers: "resolved #99", HasOpenBlockers: false}
	cleared := issueStatus{Title: "child", State: "OPEN", Wave: 1, WaveLabel: "wave1", Blockers: "-", HasOpenBlockers: false}
	restored := blocked
	restored.WaveDegraded = true

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
	if got := m.table.Rows()[0][6]; got != "stale!" {
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
