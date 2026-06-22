package watch

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
)

const (
	triggerLabel = "fanout:ready"
	runningLabel = "fanout:running"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

type fakeWatchIO struct {
	issues        []ghissue.Issue
	runningIssues []ghissue.Issue
	store         state.Store
	openChildren  map[int]int
	childCounts   map[int]ChildCounts
	alive         map[string]bool

	swapErr        map[swapKey]error
	standaloneErr  map[int]error
	parentErr      map[int]error
	parentDeferred map[int]bool

	countCalls []int
	swaps      []swapCall
	standalone []int
	parents    []parentLaunch
}

type swapKey struct {
	num    int
	remove string
	add    string
}

type swapCall struct {
	num    int
	remove string
	add    string
}

type parentLaunch struct {
	num   int
	limit int
}

func (f *fakeWatchIO) IO() IO {
	return IO{
		ListLabeled: func(label string) ([]ghissue.Issue, error) {
			switch label {
			case triggerLabel:
				return slices.Clone(f.issues), nil
			case runningLabel:
				return slices.Clone(f.runningIssues), nil
			default:
				return nil, errors.New("unexpected label")
			}
		},
		CountOpenChildren: func(issue ghissue.Issue) (int, error) {
			f.countCalls = append(f.countCalls, issue.Number)
			return f.openChildren[issue.Number], nil
		},
		CountChildren: func(issue ghissue.Issue) (ChildCounts, error) {
			if f.childCounts == nil {
				f.countCalls = append(f.countCalls, issue.Number)
				open := f.openChildren[issue.Number]
				return ChildCounts{Open: open, Launchable: open, Unfanned: open}, nil
			}
			f.countCalls = append(f.countCalls, issue.Number)
			return f.childCounts[issue.Number], nil
		},
		SwapLabels: func(issue ghissue.Issue, removeLabel, addLabel string) error {
			f.swaps = append(f.swaps, swapCall{num: issue.Number, remove: removeLabel, add: addLabel})
			return f.swapErr[swapKey{num: issue.Number, remove: removeLabel, add: addLabel}]
		},
		LoadState: func() (state.Store, error) {
			return f.store, nil
		},
		PaneAlive: func(pane state.Pane) (bool, error) {
			return f.alive[pane.PaneID], nil
		},
		LaunchStandalone: func(issue ghissue.Issue) error {
			f.standalone = append(f.standalone, issue.Number)
			return f.standaloneErr[issue.Number]
		},
		LaunchParent: func(issue ghissue.Issue, limit int) (ParentLaunchResult, error) {
			f.parents = append(f.parents, parentLaunch{num: issue.Number, limit: limit})
			return ParentLaunchResult{Deferred: f.parentDeferred[issue.Number]}, f.parentErr[issue.Number]
		},
	}
}

func TestRunCycleTableDriven(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	launchErr := errors.New("launch failed")

	tests := []struct {
		name      string
		cfg       Config
		fake      *fakeWatchIO
		check     func(t *testing.T, report Report, fake *fakeWatchIO)
		wantError bool
	}{
		{
			name: "swap failure stops launch fail closed",
			fake: &fakeWatchIO{
				issues:       []ghissue.Issue{issue(101)},
				openChildren: map[int]int{101: 0},
				swapErr: map[swapKey]error{
					{num: 101, remove: triggerLabel, add: runningLabel}: errors.New("swap failed"),
				},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "standalone launches", fake.standalone, nil)
				assertFailure(t, report, 101, FailureSwapLabels, 0, false)
			},
		},
		{
			name: "launch failure reverts label and backs off",
			fake: &fakeWatchIO{
				issues:        []ghissue.Issue{issue(101)},
				openChildren:  map[int]int{101: 0},
				standaloneErr: map[int]error{101: launchErr},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "standalone launches", fake.standalone, []int{101})
				assertSwaps(t, fake.swaps, []swapCall{
					{num: 101, remove: triggerLabel, add: runningLabel},
					{num: 101, remove: runningLabel, add: triggerLabel},
				})
				failure := assertFailure(t, report, 101, FailureLaunch, 1, false)
				if got, want := failure.NextRetryAt, baseNow.Add(60*time.Second); !got.Equal(want) {
					t.Fatalf("next retry = %s, want %s", got, want)
				}
			},
		},
		{
			name: "idempotency skips standalone rows across all parents",
			fake: &fakeWatchIO{
				issues:       []ghissue.Issue{issue(101), issue(102), issue(103), issue(104)},
				openChildren: map[int]int{101: 0, 102: 0, 103: 0, 104: 0},
				store: state.Store{Panes: []state.Pane{
					{Parent: "900", IssueNum: 101, PaneID: "%1"},
					{Parent: "901", IssueNum: 999, Slug: "api-client-102", WorktreePath: "/repo/.fanout/worktrees/api-client-102"},
					{Parent: "plan:tasks", IssueNum: 0, TaskID: "migration-103", Slug: "migration-103", WorktreePath: "/repo/.fanout/worktrees/migration-103"},
				}},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "count calls", fake.countCalls, []int{101, 102, 103, 104})
				assertInts(t, "standalone launches", fake.standalone, []int{103, 104})
				assertSkipReasons(t, report, []SkipReason{SkipAlreadyFanned, SkipAlreadyFanned})
			},
		},
		{
			name: "standalone already fanned during launch requeues trigger label",
			fake: &fakeWatchIO{
				issues:        []ghissue.Issue{issue(101)},
				openChildren:  map[int]int{101: 0},
				standaloneErr: map[int]error{101: ErrAlreadyFanned},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "standalone launches", fake.standalone, []int{101})
				assertSwaps(t, fake.swaps, []swapCall{
					{num: 101, remove: triggerLabel, add: runningLabel},
					{num: 101, remove: runningLabel, add: triggerLabel},
				})
				assertSkipReasons(t, report, []SkipReason{SkipAlreadyFanned})
				if len(report.Launched) != 0 || len(report.Failures) != 0 {
					t.Fatalf("report = %#v, want requeued skip without launched/failure", report)
				}
			},
		},
		{
			name: "parent fanout ignores child issue false positive",
			fake: &fakeWatchIO{
				issues:       []ghissue.Issue{issue(201), issue(202)},
				openChildren: map[int]int{201: 2, 202: 2},
				store: state.Store{Panes: []state.Pane{
					{Parent: "0201", IssueNum: 301, PaneID: "%1"},
					{Parent: "201", IssueNum: 302, PaneID: "%2"},
					{Parent: "900", IssueNum: 202, PaneID: "%3"},
				}},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "count calls", fake.countCalls, []int{201, 202})
				if got, want := fake.parents, []parentLaunch{{num: 201, limit: 0}, {num: 202, limit: 0}}; !slices.Equal(got, want) {
					t.Fatalf("parent launches = %#v, want %#v", got, want)
				}
				assertSkipReasons(t, report, nil)
			},
		},
		{
			name: "max sessions reached defers without counting children",
			cfg:  Config{MaxSessions: 1},
			fake: &fakeWatchIO{
				issues: []ghissue.Issue{issue(101)},
				store:  state.Store{Panes: []state.Pane{{IssueNum: 900, PaneID: "%live"}}},
				alive:  map[string]bool{"%live": true},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "count calls", fake.countCalls, nil)
				if len(report.Deferred) != 1 || report.Deferred[0].Reason != DeferMaxSessions {
					t.Fatalf("deferred = %#v, want max_sessions", report.Deferred)
				}
			},
		},
		{
			name: "max sessions ignores shell panes",
			cfg:  Config{MaxSessions: 1},
			fake: &fakeWatchIO{
				issues:       []ghissue.Issue{issue(101)},
				openChildren: map[int]int{101: 0},
				store: state.Store{Panes: []state.Pane{
					{IssueNum: -1, Kind: state.PaneKindShell, PaneID: "%shell"},
				}},
				alive: map[string]bool{"%shell": true},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				if report.RunningSessions != 0 || report.RemainingSessions != 0 {
					t.Fatalf("budget = running %d remaining %d, want shell excluded",
						report.RunningSessions, report.RemainingSessions)
				}
				assertInts(t, "standalone launches", fake.standalone, []int{101})
			},
		},
		{
			name: "running label skips launch",
			fake: &fakeWatchIO{
				issues: []ghissue.Issue{issue(101, runningLabel)},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "count calls", fake.countCalls, nil)
				assertSkipReasons(t, report, []SkipReason{SkipAlreadyRunning})
				assertInts(t, "standalone launches", fake.standalone, nil)
			},
		},
		{
			name: "parent fanout gets reserved budget as limit",
			cfg:  Config{MaxSessions: 5},
			fake: &fakeWatchIO{
				issues:       []ghissue.Issue{issue(201)},
				openChildren: map[int]int{201: 3},
				store:        state.Store{Panes: []state.Pane{{IssueNum: 900, PaneID: "%live"}}},
				alive:        map[string]bool{"%live": true},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				if len(report.Actions) != 1 || report.Actions[0].Kind != LaunchParent || report.Actions[0].Limit != 3 {
					t.Fatalf("actions = %#v, want parent with limit 3", report.Actions)
				}
				if got, want := fake.parents, []parentLaunch{{num: 201, limit: 3}}; !slices.Equal(got, want) {
					t.Fatalf("parent launches = %#v, want %#v", got, want)
				}
			},
		},
		{
			name: "blocked-only parent does not consume capacity",
			cfg:  Config{MaxSessions: 1},
			fake: &fakeWatchIO{
				issues: []ghissue.Issue{issue(301), issue(101)},
				childCounts: map[int]ChildCounts{
					301: {Open: 2, Launchable: 0, Unfanned: 2},
					101: {Open: 0, Launchable: 0, Unfanned: 0},
				},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "count calls", fake.countCalls, []int{301, 101})
				if got, want := fake.parents, []parentLaunch(nil); !slices.Equal(got, want) {
					t.Fatalf("parent launches = %#v, want none", got)
				}
				assertInts(t, "standalone launches", fake.standalone, []int{101})
				if len(report.Deferred) != 1 || report.Deferred[0].Issue.Number != 301 || report.Deferred[0].Reason != DeferBlocked {
					t.Fatalf("deferred = %#v, want #301 blocked", report.Deferred)
				}
			},
		},
		{
			name: "parent exceeding remaining budget launches limited batch and keeps trigger label",
			cfg:  Config{MaxSessions: 3},
			fake: &fakeWatchIO{
				issues:         []ghissue.Issue{issue(301)},
				openChildren:   map[int]int{301: 3},
				parentDeferred: map[int]bool{301: true},
				store: state.Store{Panes: []state.Pane{
					{Parent: "301", IssueNum: 401, PaneID: "%old"},
					{Parent: "900", IssueNum: 900, PaneID: "%live"},
				}},
				alive: map[string]bool{"%live": true},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "count calls", fake.countCalls, []int{301})
				assertSwaps(t, fake.swaps, []swapCall{
					{num: 301, remove: triggerLabel, add: runningLabel},
					{num: 301, remove: runningLabel, add: triggerLabel},
				})
				if len(report.Deferred) != 1 || report.Deferred[0].Reason != DeferMaxSessions {
					t.Fatalf("deferred = %#v, want max_sessions", report.Deferred)
				}
				if len(report.Actions) != 1 || report.Actions[0].Limit != 2 {
					t.Fatalf("actions = %#v, want one parent launch with limit 2", report.Actions)
				}
				if got, want := fake.parents, []parentLaunch{{num: 301, limit: 2}}; !slices.Equal(got, want) {
					t.Fatalf("parent launches = %#v, want %#v", got, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{now: baseNow}
			cfg := tt.cfg
			cfg.TriggerLabel = triggerLabel
			cfg.RunningLabel = runningLabel
			cfg.Now = clock.Now
			engine := NewEngine(cfg, tt.fake.IO())
			report, err := engine.RunCycle()
			if tt.wantError && err == nil {
				t.Fatal("RunCycle error = nil, want error")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("RunCycle error = %v", err)
			}
			tt.check(t, report, tt.fake)
		})
	}
}

func TestRunCycleRecoversStandaloneRunningRetryAfterRestart(t *testing.T) {
	fake := &fakeWatchIO{
		runningIssues: []ghissue.Issue{issue(101, runningLabel)},
		openChildren:  map[int]int{101: 0},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}).Now,
	}, fake.IO())

	report, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("RunCycle error = %v", err)
	}
	assertInts(t, "count calls", fake.countCalls, []int{101})
	assertSwaps(t, fake.swaps, nil)
	assertInts(t, "standalone launches", fake.standalone, []int{101})
	if len(report.Actions) != 1 || !report.Actions[0].RetryRunning {
		t.Fatalf("actions = %#v, want recovered running-label standalone", report.Actions)
	}
}

func TestRunCycleRecoversParentRunningRetryAfterRestart(t *testing.T) {
	fake := &fakeWatchIO{
		runningIssues: []ghissue.Issue{issue(301, runningLabel)},
		openChildren:  map[int]int{301: 1},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}).Now,
	}, fake.IO())

	report, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("RunCycle error = %v", err)
	}
	assertInts(t, "count calls", fake.countCalls, []int{301})
	assertSwaps(t, fake.swaps, nil)
	if got, want := fake.parents, []parentLaunch{{num: 301, limit: 0}}; !slices.Equal(got, want) {
		t.Fatalf("parent launches = %#v, want %#v", got, want)
	}
	if len(report.Actions) != 1 || !report.Actions[0].RetryRunning || report.Actions[0].Kind != LaunchParent {
		t.Fatalf("actions = %#v, want recovered running-label parent", report.Actions)
	}
}

func TestRunCycleSkipsRecoveredRunningParentWithLiveChildren(t *testing.T) {
	fake := &fakeWatchIO{
		runningIssues: []ghissue.Issue{issue(301, runningLabel)},
		openChildren:  map[int]int{301: 2},
		store: state.Store{Panes: []state.Pane{
			{Parent: "301", IssueNum: 401, PaneID: "%child"},
		}},
		alive: map[string]bool{"%child": true},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}).Now,
	}, fake.IO())

	report, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("RunCycle error = %v", err)
	}
	assertInts(t, "count calls", fake.countCalls, []int{301})
	if len(fake.parents) != 0 || len(report.Actions) != 0 {
		t.Fatalf("report=%#v parents=%#v, want live parent skipped", report, fake.parents)
	}
	assertSkipReasons(t, report, []SkipReason{SkipAlreadyRunning})
}

func TestRunCycleRecoversBothLabeledRunningRetryAfterRestart(t *testing.T) {
	fake := &fakeWatchIO{
		issues:        []ghissue.Issue{issue(101, runningLabel)},
		runningIssues: []ghissue.Issue{issue(101, runningLabel)},
		openChildren:  map[int]int{101: 0},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}).Now,
	}, fake.IO())

	report, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("RunCycle error = %v", err)
	}
	assertInts(t, "count calls", fake.countCalls, []int{101})
	assertSwaps(t, fake.swaps, []swapCall{{num: 101, remove: triggerLabel, add: runningLabel}})
	assertInts(t, "standalone launches", fake.standalone, []int{101})
	if len(report.Actions) != 1 || report.Actions[0].RetryRunning {
		t.Fatalf("actions = %#v, want recovered trigger acquisition", report.Actions)
	}
}

func TestRunCycleSkipsRecoveredRunningParentWithoutOpenChildren(t *testing.T) {
	fake := &fakeWatchIO{
		runningIssues: []ghissue.Issue{issue(301, runningLabel)},
		openChildren:  map[int]int{301: 0},
		store: state.Store{Panes: []state.Pane{
			{Parent: "301", IssueNum: 401, PaneID: "%child"},
		}},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}).Now,
	}, fake.IO())

	report, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("RunCycle error = %v", err)
	}
	assertInts(t, "count calls", fake.countCalls, []int{301})
	assertInts(t, "standalone launches", fake.standalone, nil)
	if len(fake.parents) != 0 || len(report.Actions) != 0 {
		t.Fatalf("report=%#v parents=%#v, want recovered parent skipped", report, fake.parents)
	}
	assertSkipReasons(t, report, []SkipReason{SkipAlreadyRunning})
}

func TestRunCycleRetriesStandaloneWhenRequeueSwapFails(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: baseNow}
	fake := &fakeWatchIO{
		issues:        []ghissue.Issue{issue(101)},
		openChildren:  map[int]int{101: 0},
		standaloneErr: map[int]error{101: errors.New("launch failed")},
		swapErr: map[swapKey]error{
			{num: 101, remove: runningLabel, add: triggerLabel}: errors.New("requeue failed"),
		},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          clock.Now,
	}, fake.IO())

	first, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("first RunCycle error = %v", err)
	}
	assertInts(t, "first standalone launches", fake.standalone, []int{101})
	assertSwaps(t, fake.swaps, []swapCall{
		{num: 101, remove: triggerLabel, add: runningLabel},
		{num: 101, remove: runningLabel, add: triggerLabel},
	})
	assertFailure(t, first, 101, FailureLaunch, 1, false)

	clock.Advance(60 * time.Second)
	fake.issues = nil
	fake.runningIssues = []ghissue.Issue{issue(101, runningLabel)}
	fake.standaloneErr = nil
	fake.swapErr = nil
	fake.countCalls = nil
	fake.swaps = nil
	fake.standalone = nil

	second, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("second RunCycle error = %v", err)
	}
	assertInts(t, "count calls on retry", fake.countCalls, []int{101})
	assertSwaps(t, fake.swaps, nil)
	assertInts(t, "standalone retry launches", fake.standalone, []int{101})
	if len(second.Actions) != 1 || !second.Actions[0].RetryRunning {
		t.Fatalf("second actions = %#v, want running-label retry", second.Actions)
	}
	if len(second.Failures) != 0 || len(second.Deferred) != 0 || len(second.Launched) != 1 {
		t.Fatalf("second report = %#v, want one clean launch", second)
	}

	fake.countCalls = nil
	fake.standalone = nil
	fake.store = state.Store{Panes: []state.Pane{{IssueNum: 101, PaneID: "%new"}}}

	third, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("third RunCycle error = %v", err)
	}
	if len(third.Actions) != 0 || len(fake.standalone) != 0 {
		t.Fatalf("third retry persisted: report=%#v standalone=%#v", third, fake.standalone)
	}
	assertInts(t, "count calls after state record", fake.countCalls, []int{101})
	assertSkipReasons(t, third, []SkipReason{SkipAlreadyFanned})
}

func TestRunCycleRetriesBlockedOnlyParentWhenRequeueSwapFails(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	fake := &fakeWatchIO{
		issues:         []ghissue.Issue{issue(301)},
		openChildren:   map[int]int{301: 3},
		parentDeferred: map[int]bool{301: true},
		swapErr: map[swapKey]error{
			{num: 301, remove: runningLabel, add: triggerLabel}: errors.New("requeue failed"),
		},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: baseNow}).Now,
	}, fake.IO())

	first, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("first RunCycle error = %v", err)
	}
	assertFailure(t, first, 301, FailureSwapLabels, 0, false)

	fake.issues = nil
	fake.runningIssues = []ghissue.Issue{issue(301, runningLabel)}
	fake.childCounts = map[int]ChildCounts{
		301: {Open: 2, Launchable: 0, Unfanned: 2},
	}
	fake.swapErr = nil
	fake.countCalls = nil
	fake.swaps = nil
	fake.parents = nil

	second, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("second RunCycle error = %v", err)
	}
	assertInts(t, "count calls on retry", fake.countCalls, []int{301})
	assertSwaps(t, fake.swaps, []swapCall{{num: 301, remove: runningLabel, add: triggerLabel}})
	if got, want := fake.parents, []parentLaunch{{num: 301, limit: 0}}; !slices.Equal(got, want) {
		t.Fatalf("parent retry launches = %#v, want %#v", got, want)
	}
	if len(second.Deferred) != 1 || second.Deferred[0].Reason != DeferMaxSessions {
		t.Fatalf("second deferred = %#v, want requeued parent", second.Deferred)
	}
	if len(second.Failures) != 0 || len(second.Launched) != 1 {
		t.Fatalf("second report = %#v, want clean requeue after retry", second)
	}
}

func TestRunCycleRetriesDeferredParentWhenRequeueSwapFails(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	fake := &fakeWatchIO{
		issues:         []ghissue.Issue{issue(301)},
		openChildren:   map[int]int{301: 3},
		parentDeferred: map[int]bool{301: true},
		swapErr: map[swapKey]error{
			{num: 301, remove: runningLabel, add: triggerLabel}: errors.New("requeue failed"),
		},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: baseNow}).Now,
	}, fake.IO())

	first, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("first RunCycle error = %v", err)
	}
	assertSwaps(t, fake.swaps, []swapCall{
		{num: 301, remove: triggerLabel, add: runningLabel},
		{num: 301, remove: runningLabel, add: triggerLabel},
	})
	if len(first.Deferred) != 1 || first.Deferred[0].Reason != DeferMaxSessions {
		t.Fatalf("first deferred = %#v, want max_sessions", first.Deferred)
	}
	assertFailure(t, first, 301, FailureSwapLabels, 0, false)

	fake.issues = nil
	fake.runningIssues = []ghissue.Issue{issue(301, runningLabel)}
	fake.openChildren[301] = 1
	fake.parentDeferred[301] = false
	fake.swapErr = nil
	fake.countCalls = nil
	fake.swaps = nil
	fake.parents = nil

	second, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("second RunCycle error = %v", err)
	}
	assertInts(t, "count calls on retry", fake.countCalls, []int{301})
	assertSwaps(t, fake.swaps, nil)
	if got, want := fake.parents, []parentLaunch{{num: 301, limit: 0}}; !slices.Equal(got, want) {
		t.Fatalf("parent retry launches = %#v, want %#v", got, want)
	}
	if len(second.Actions) != 1 || !second.Actions[0].RetryRunning {
		t.Fatalf("second actions = %#v, want running-label retry", second.Actions)
	}
	if len(second.Failures) != 0 || len(second.Deferred) != 0 || len(second.Launched) != 1 {
		t.Fatalf("second report = %#v, want one clean launch", second)
	}

	fake.countCalls = nil
	fake.parents = nil
	fake.openChildren[301] = 0
	fake.store = state.Store{Panes: []state.Pane{{Parent: "301", IssueNum: 401, PaneID: "%new"}}}

	third, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("third RunCycle error = %v", err)
	}
	if len(third.Actions) != 0 || len(fake.parents) != 0 {
		t.Fatalf("third retry persisted: report=%#v parents=%#v", third, fake.parents)
	}
}

func TestRunCycleAcquiresTriggerLabeledParentRetry(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	fake := &fakeWatchIO{
		issues:         []ghissue.Issue{issue(301)},
		openChildren:   map[int]int{301: 3},
		parentDeferred: map[int]bool{301: true},
		swapErr: map[swapKey]error{
			{num: 301, remove: runningLabel, add: triggerLabel}: errors.New("requeue failed"),
		},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: baseNow}).Now,
	}, fake.IO())

	if _, err := engine.RunCycle(); err != nil {
		t.Fatalf("first RunCycle error = %v", err)
	}

	fake.issues = []ghissue.Issue{issue(301)}
	fake.openChildren[301] = 1
	fake.parentDeferred[301] = false
	fake.swapErr = nil
	fake.swaps = nil
	fake.parents = nil

	second, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("second RunCycle error = %v", err)
	}
	assertSwaps(t, fake.swaps, []swapCall{{num: 301, remove: triggerLabel, add: runningLabel}})
	if got, want := fake.parents, []parentLaunch{{num: 301, limit: 0}}; !slices.Equal(got, want) {
		t.Fatalf("parent retry launches = %#v, want %#v", got, want)
	}
	if len(second.Actions) != 1 || second.Actions[0].RetryRunning {
		t.Fatalf("second actions = %#v, want normal trigger acquisition", second.Actions)
	}
}

func TestRunCycleAcquiresBothLabeledParentRetry(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	fake := &fakeWatchIO{
		issues:         []ghissue.Issue{issue(301)},
		openChildren:   map[int]int{301: 3},
		parentDeferred: map[int]bool{301: true},
		swapErr: map[swapKey]error{
			{num: 301, remove: runningLabel, add: triggerLabel}: errors.New("requeue failed"),
		},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: baseNow}).Now,
	}, fake.IO())

	if _, err := engine.RunCycle(); err != nil {
		t.Fatalf("first RunCycle error = %v", err)
	}

	fake.issues = []ghissue.Issue{issue(301, runningLabel)}
	fake.openChildren[301] = 1
	fake.parentDeferred[301] = false
	fake.swapErr = nil
	fake.swaps = nil
	fake.parents = nil

	second, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("second RunCycle error = %v", err)
	}
	assertSwaps(t, fake.swaps, []swapCall{{num: 301, remove: triggerLabel, add: runningLabel}})
	if got, want := fake.parents, []parentLaunch{{num: 301, limit: 0}}; !slices.Equal(got, want) {
		t.Fatalf("parent retry launches = %#v, want %#v", got, want)
	}
	if len(second.Skipped) != 0 || len(second.Actions) != 1 || second.Actions[0].RetryRunning {
		t.Fatalf("second report = %#v, want normal acquisition retry", second)
	}
}

func TestRunCycleIgnoresStaleHiddenParentRetry(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	fake := &fakeWatchIO{
		issues:         []ghissue.Issue{issue(301)},
		openChildren:   map[int]int{301: 3},
		parentDeferred: map[int]bool{301: true},
		swapErr: map[swapKey]error{
			{num: 301, remove: runningLabel, add: triggerLabel}: errors.New("requeue failed"),
		},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          (&fakeClock{now: baseNow}).Now,
	}, fake.IO())

	if _, err := engine.RunCycle(); err != nil {
		t.Fatalf("first RunCycle error = %v", err)
	}

	fake.issues = nil
	fake.runningIssues = nil
	fake.openChildren[301] = 1
	fake.parentDeferred[301] = false
	fake.swapErr = nil
	fake.countCalls = nil
	fake.swaps = nil
	fake.parents = nil

	second, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("second RunCycle error = %v", err)
	}
	if len(second.Actions) != 0 || len(fake.parents) != 0 || len(fake.countCalls) != 0 || len(fake.swaps) != 0 {
		t.Fatalf("second report = %#v calls=%#v swaps=%#v parents=%#v, want stale retry ignored",
			second, fake.countCalls, fake.swaps, fake.parents)
	}
}

func TestRunCycleDisablesAfterThreeConsecutiveLaunchFailures(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: baseNow}
	fake := &fakeWatchIO{
		issues:        []ghissue.Issue{issue(101)},
		openChildren:  map[int]int{101: 0},
		standaloneErr: map[int]error{101: errors.New("launch failed")},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          clock.Now,
	}, fake.IO())

	first, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("first RunCycle error = %v", err)
	}
	assertFailure(t, first, 101, FailureLaunch, 1, false)

	clock.Advance(60 * time.Second)
	second, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("second RunCycle error = %v", err)
	}
	assertFailure(t, second, 101, FailureLaunch, 2, false)

	clock.Advance(120 * time.Second)
	third, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("third RunCycle error = %v", err)
	}
	assertFailure(t, third, 101, FailureLaunch, 3, true)

	clock.Advance(time.Hour)
	fake.standalone = nil
	fake.swaps = nil
	fourth, err := engine.RunCycle()
	if err != nil {
		t.Fatalf("fourth RunCycle error = %v", err)
	}
	assertSkipReasons(t, fourth, []SkipReason{SkipDisabled})
	assertInts(t, "standalone launches after disable", fake.standalone, nil)
	assertSwaps(t, fake.swaps, nil)
}

func TestPlanCycleDefersDuringBackoff(t *testing.T) {
	baseNow := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: baseNow}
	fake := &fakeWatchIO{
		issues:        []ghissue.Issue{issue(101)},
		openChildren:  map[int]int{101: 0},
		standaloneErr: map[int]error{101: errors.New("launch failed")},
	}
	engine := NewEngine(Config{
		TriggerLabel: triggerLabel,
		RunningLabel: runningLabel,
		Now:          clock.Now,
	}, fake.IO())

	if _, err := engine.RunCycle(); err != nil {
		t.Fatalf("RunCycle error = %v", err)
	}
	fake.standalone = nil
	fake.swaps = nil

	report, err := engine.PlanCycle()
	if err != nil {
		t.Fatalf("PlanCycle error = %v", err)
	}
	if len(report.Deferred) != 1 || report.Deferred[0].Reason != DeferBackoff {
		t.Fatalf("deferred = %#v, want one backoff", report.Deferred)
	}
	if got, want := report.Deferred[0].RetryAfter, 60*time.Second; got != want {
		t.Fatalf("retry after = %s, want %s", got, want)
	}
	assertInts(t, "standalone launches during backoff", fake.standalone, nil)
	assertSwaps(t, fake.swaps, nil)
}

func issue(num int, labels ...string) ghissue.Issue {
	issue := ghissue.Issue{Number: num, Title: "issue", State: "OPEN"}
	for _, label := range labels {
		issue.Labels = append(issue.Labels, ghissue.Label{Name: label})
	}
	return issue
}

func assertFailure(t *testing.T, report Report, num int, stage FailureStage, attempts int, disabled bool) Failure {
	t.Helper()
	for _, failure := range report.Failures {
		if failure.Issue.Number == num && failure.Stage == stage {
			if failure.Attempts != attempts {
				t.Fatalf("failure attempts = %d, want %d in %#v", failure.Attempts, attempts, failure)
			}
			if failure.Disabled != disabled {
				t.Fatalf("failure disabled = %v, want %v in %#v", failure.Disabled, disabled, failure)
			}
			return failure
		}
	}
	t.Fatalf("missing failure issue #%d stage %s in %#v", num, stage, report.Failures)
	return Failure{}
}

func assertInts(t *testing.T, name string, got, want []int) {
	t.Helper()
	if got == nil {
		got = []int{}
	}
	if want == nil {
		want = []int{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}

func assertSwaps(t *testing.T, got, want []swapCall) {
	t.Helper()
	if got == nil {
		got = []swapCall{}
	}
	if want == nil {
		want = []swapCall{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("swaps = %#v, want %#v", got, want)
	}
}

func assertSkipReasons(t *testing.T, report Report, want []SkipReason) {
	t.Helper()
	got := make([]SkipReason, len(report.Skipped))
	for i, skip := range report.Skipped {
		got[i] = skip.Reason
	}
	if !slices.Equal(got, want) {
		t.Fatalf("skip reasons = %#v, want %#v", got, want)
	}
}
