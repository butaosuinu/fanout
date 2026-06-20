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
	issues       []ghissue.Issue
	store        state.Store
	openChildren map[int]int
	alive        map[string]bool

	swapErr       map[swapKey]error
	standaloneErr map[int]error
	parentErr     map[int]error

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
			if label != triggerLabel {
				return nil, errors.New("unexpected label")
			}
			return slices.Clone(f.issues), nil
		},
		CountOpenChildren: func(issue ghissue.Issue) (int, error) {
			f.countCalls = append(f.countCalls, issue.Number)
			return f.openChildren[issue.Number], nil
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
		LaunchParent: func(issue ghissue.Issue, limit int) error {
			f.parents = append(f.parents, parentLaunch{num: issue.Number, limit: limit})
			return f.parentErr[issue.Number]
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
			name: "idempotency skips issue number and issue-backed worktree fallback",
			fake: &fakeWatchIO{
				issues:       []ghissue.Issue{issue(101), issue(102), issue(103)},
				openChildren: map[int]int{103: 0},
				store: state.Store{Panes: []state.Pane{
					{Parent: "900", IssueNum: 101, PaneID: "%1"},
					{Parent: "901", IssueNum: 999, Slug: "api-client-102", WorktreePath: "/repo/.fanout/worktrees/api-client-102"},
					{Parent: "plan:tasks", IssueNum: 0, TaskID: "migration-103", Slug: "migration-103", WorktreePath: "/repo/.fanout/worktrees/migration-103"},
				}},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				assertInts(t, "count calls", fake.countCalls, []int{103})
				assertInts(t, "standalone launches", fake.standalone, []int{103})
				assertSkipReasons(t, report, []SkipReason{SkipAlreadyFanned, SkipAlreadyFanned})
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
			name: "parent fanout gets remaining budget as limit",
			cfg:  Config{MaxSessions: 5},
			fake: &fakeWatchIO{
				issues:       []ghissue.Issue{issue(201)},
				openChildren: map[int]int{201: 3},
				store:        state.Store{Panes: []state.Pane{{IssueNum: 900, PaneID: "%live"}}},
				alive:        map[string]bool{"%live": true},
			},
			check: func(t *testing.T, report Report, fake *fakeWatchIO) {
				t.Helper()
				if len(report.Actions) != 1 || report.Actions[0].Kind != LaunchParent || report.Actions[0].Limit != 4 {
					t.Fatalf("actions = %#v, want parent with limit 4", report.Actions)
				}
				if got, want := fake.parents, []parentLaunch{{num: 201, limit: 4}}; !slices.Equal(got, want) {
					t.Fatalf("parent launches = %#v, want %#v", got, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{now: baseNow}
			cfg := Config{
				TriggerLabel: triggerLabel,
				RunningLabel: runningLabel,
				Now:          clock.Now,
				MaxSessions:  tt.cfg.MaxSessions,
			}
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
