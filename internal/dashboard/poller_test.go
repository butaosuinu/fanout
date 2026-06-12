package dashboard

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
)

type countingGH struct {
	mu        sync.Mutex
	calls     map[int]int
	waveCalls map[string]int
	waveNums  map[string][]int // recordedNums passed per parent (last call)
	waves     map[string]sessionview.WaveGraph
	wavesErr  error
}

func (g *countingGH) IssuePRs(num int) (string, []ghissue.PRRef, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.calls == nil {
		g.calls = map[int]int{}
	}
	g.calls[num]++
	return "CLOSED", []ghissue.PRRef{{Number: 900 + num, State: "MERGED"}}, nil
}

func (g *countingGH) Waves(parent string, recordedNums []int) (sessionview.WaveGraph, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waveCalls == nil {
		g.waveCalls = map[string]int{}
		g.waveNums = map[string][]int{}
	}
	g.waveCalls[parent]++
	g.waveNums[parent] = slices.Clone(recordedNums)
	if g.wavesErr != nil {
		return sessionview.WaveGraph{}, g.wavesErr
	}
	return g.waves[parent], nil
}

func writeState(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".fanout"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".fanout", "state.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPollerRefreshGHPopulatesCacheAndBuildReadsIt(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1","agent":"claude"},
	  {"parent":"100","issueNum":102,"slug":"b","paneId":"%2","agent":"codex"}
	]}`)

	gh := &countingGH{waves: map[string]sessionview.WaveGraph{
		"100": {Info: map[int]sessionview.WaveInfo{
			101: {Wave: 1, Blockers: []blockers.Status{}},
			102: {Wave: 2, WaveLabel: "wave 2", Blocked: true, Blockers: []blockers.Status{{Num: 101, State: "OPEN"}}},
		}},
	}}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.refreshGH()
	snap := p.build()

	if gh.calls[101] != 1 || gh.calls[102] != 1 {
		t.Fatalf("expected one gh call per issue, got %v", gh.calls)
	}
	if len(gh.waveCalls) != 1 || gh.waveCalls["100"] != 1 {
		t.Fatalf("expected one Waves call for parent 100, got %v", gh.waveCalls)
	}
	if !slices.Equal(gh.waveNums["100"], []int{101, 102}) {
		t.Fatalf("recorded nums for parent 100 = %v, want [101 102]", gh.waveNums["100"])
	}
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Panes) != 2 {
		t.Fatalf("unexpected snapshot shape: %+v", snap.Sessions)
	}
	if !snap.Sessions[0].Panes[0].HasMergedPR {
		t.Fatal("PR state from cache should reach the built snapshot")
	}
	second := snap.Sessions[0].Panes[1]
	if second.Wave != 2 || second.WaveLabel != "wave 2" || !second.Blocked {
		t.Fatalf("wave data from cache should reach the built snapshot, got %+v", second)
	}
	if len(second.Blockers) != 1 || second.Blockers[0].Num != 101 {
		t.Fatalf("blocker rows should reach the built snapshot, got %+v", second.Blockers)
	}
	if snap.Degraded.GitHub {
		t.Fatal("GitHub should not be degraded on success")
	}
}

func TestRefreshGHCallsWavesOncePerNormalizedParent(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"0100","issueNum":101,"slug":"a","paneId":"%1"},
	  {"parent":"100","issueNum":102,"slug":"b","paneId":"%2"},
	  {"parent":"@manual","issueNum":-1,"slug":"m","paneId":"%3"}
	]}`)

	gh := &countingGH{}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.refreshGH()

	if len(gh.waveCalls) != 2 || gh.waveCalls["100"] != 1 || gh.waveCalls["@manual"] != 1 {
		t.Fatalf("expected one Waves call per normalized parent, got %v", gh.waveCalls)
	}
	if !slices.Equal(gh.waveNums["100"], []int{101, 102}) {
		t.Fatalf(`"0100" and "100" must pool recorded nums, got %v`, gh.waveNums["100"])
	}
	if len(gh.waveNums["@manual"]) != 0 {
		t.Fatalf("synthetic nums must stay out of recordedNums, got %v", gh.waveNums["@manual"])
	}
}

func TestRefreshGHThrottlesWavePhaseToWaveInterval(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)

	gh := &countingGH{}
	p := newPoller("o/n", root, gh, nil, newHub())

	// First refresh always runs the wave phase so the UI populates; a second
	// refresh within waveInterval skips it while the PR phase still runs.
	p.refreshGH()
	p.refreshGH()

	if gh.waveCalls["100"] != 1 {
		t.Fatalf("Waves calls within waveInterval = %d, want 1 (first refresh only)", gh.waveCalls["100"])
	}
	if gh.calls[101] != 2 {
		t.Fatalf("IssuePRs calls = %d, want 2 (PR phase runs every tick)", gh.calls[101])
	}

	// Once waveInterval has elapsed the wave phase runs again.
	p.lastWaveRefresh = time.Now().Add(-p.waveInterval)
	p.refreshGH()
	if gh.waveCalls["100"] != 2 {
		t.Fatalf("Waves calls after waveInterval elapsed = %d, want 2", gh.waveCalls["100"])
	}
}

func TestWavesErrorDegradesGitHub(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)

	gh := &countingGH{wavesErr: errRepoUnresolvedForTest}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.refreshGH()
	snap := p.build()

	if !snap.Degraded.GitHub {
		t.Fatal("a Waves failure must mark GitHub degraded")
	}
	// IssuePRs succeeded, so PR state still reaches the snapshot (partial data).
	if !snap.Sessions[0].Panes[0].HasMergedPR {
		t.Fatal("PR state should survive a wave-graph failure")
	}
}

func TestWaveCacheMissDoesNotDegrade(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)

	// gh resolved but refreshGH never ran (pre first gh tick): every wave lookup
	// is a cache miss, which must read as "not fetched yet", not a failure.
	p := newPoller("o/n", root, &countingGH{}, nil, newHub())
	snap := p.build()

	if snap.Degraded.GitHub {
		t.Fatal("a wave cache miss before the first refresh must not degrade GitHub")
	}
	pane := snap.Sessions[0].Panes[0]
	if pane.Wave != 0 || pane.Blocked || len(pane.Blockers) != 0 {
		t.Fatalf("cache miss should leave zero-valued wave fields, got %+v", pane)
	}
}

func TestRecordedNumsByParentNormalizesAndSkipsSynthetic(t *testing.T) {
	got := recordedNumsByParent(state.Store{Panes: []state.Pane{
		{Parent: "0300", IssueNum: 302},
		{Parent: "300", IssueNum: 301},
		{Parent: "300", IssueNum: 301}, // duplicate
		{Parent: "@manual", IssueNum: -1},
		{Parent: "400", IssueNum: 0},
	}})

	if len(got) != 3 {
		t.Fatalf("recordedNumsByParent() = %#v, want 3 parents", got)
	}
	if !slices.Equal(got["300"], []int{301, 302}) {
		t.Fatalf("parent 300 nums = %v, want [301 302]", got["300"])
	}
	if len(got["@manual"]) != 0 || len(got["400"]) != 0 {
		t.Fatalf("non-positive nums must be dropped, got %#v", got)
	}
}

func TestDistinctIssueNumsSkipsSyntheticManualPanes(t *testing.T) {
	got := distinctIssueNums(state.Store{Panes: []state.Pane{
		{Parent: "@manual", IssueNum: -1},
		{Parent: "@manual", IssueNum: -2},
		{IssueNum: 0},
		{IssueNum: 102},
		{IssueNum: 101},
		{IssueNum: 102},
		{IssueNum: 101},
	}})

	want := []int{101, 102}
	if len(got) != len(want) {
		t.Fatalf("distinctIssueNums() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("distinctIssueNums() = %#v, want %#v", got, want)
		}
	}
}

func TestPollerUnresolvedRepoDegradesGitHub(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[{"parent":"1","issueNum":2,"paneId":"%1"}]}`)

	p := newPoller("", root, nil, errRepoUnresolvedForTest, newHub())
	snap := p.build()
	if !snap.Degraded.GitHub {
		t.Fatal("unresolved repo should mark GitHub degraded")
	}
	if snap.Sessions[0].Panes[0].IssueState != sessionview.IssueStateUnknown {
		t.Fatalf("issue state = %q want UNKNOWN", snap.Sessions[0].Panes[0].IssueState)
	}
}

func TestLazyPollerResolvesOnGHGoroutine(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1","agent":"claude"}
	]}`)

	gh := &countingGH{}
	resolverRan := false
	p := newLazyPoller(root, func() (string, GHProvider, error) {
		resolverRan = true
		return "o/n", gh, nil
	}, newHub())

	// Before resolution the snapshot is state-only: the server can paint without
	// waiting on `gh repo view`. The resolver must not have run yet.
	pre := p.build()
	if pre.Repo != "" {
		t.Fatalf("repo before resolve = %q, want empty (state-only paint)", pre.Repo)
	}
	if resolverRan {
		t.Fatal("resolver must not run until ensureResolved (gh goroutine)")
	}

	// The gh goroutine resolves once, then PR state and the repo label fill in.
	p.ensureResolved()
	p.refreshGH()
	post := p.build()
	if !resolverRan {
		t.Fatal("ensureResolved must invoke the resolver")
	}
	if post.Repo != "o/n" {
		t.Fatalf("repo after resolve = %q, want o/n", post.Repo)
	}
	if !post.Sessions[0].Panes[0].HasMergedPR {
		t.Fatal("PR state should populate after lazy resolve")
	}

	// ensureResolved is idempotent: a second call must not re-run the resolver.
	resolverRan = false
	p.ensureResolved()
	if resolverRan {
		t.Fatal("ensureResolved must resolve at most once")
	}
}

func TestLazyPollerNilResolverIsStateOnly(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[{"parent":"1","issueNum":2,"paneId":"%1"}]}`)
	p := newLazyPoller(root, nil, newHub())
	p.ensureResolved() // no-op, must not panic
	snap := p.build()
	if snap.Degraded.GitHub {
		t.Fatal("nil resolver -> GitHub tier disabled (state-only), not degraded")
	}
}

func TestContentKeyIgnoresTimestamp(t *testing.T) {
	a := sessionview.Snapshot{Repo: "x", GeneratedAt: "2026-06-06T00:00:00Z"}
	b := sessionview.Snapshot{Repo: "x", GeneratedAt: "2026-06-06T00:00:02Z"}
	if string(contentKey(a)) != string(contentKey(b)) {
		t.Fatal("contentKey must ignore GeneratedAt")
	}
	c := sessionview.Snapshot{Repo: "y", GeneratedAt: "2026-06-06T00:00:00Z"}
	if string(contentKey(a)) == string(contentKey(c)) {
		t.Fatal("contentKey must reflect real content changes")
	}
}

type errRepoUnresolved struct{}

func (errRepoUnresolved) Error() string { return "repo unresolved" }

var errRepoUnresolvedForTest error = errRepoUnresolved{}

func TestMergeDegradedWaveInfos(t *testing.T) {
	t.Parallel()

	blocked := sessionview.WaveInfo{Wave: 2, WaveLabel: "wave2", Blockers: []blockers.Status{{Num: 99, State: "OPEN"}}, Blocked: true}
	degraded := sessionview.WaveInfo{Wave: 1, Degraded: true}
	fresh := sessionview.WaveInfo{Wave: 2, WaveLabel: "wave2", Blockers: []blockers.Status{{Num: 99, State: "CLOSED"}}}

	tests := []struct {
		name     string
		previous map[int]sessionview.WaveInfo
		current  map[int]sessionview.WaveInfo
		want     map[int]sessionview.WaveInfo
	}{
		{
			name:    "no previous passes current through",
			current: map[int]sessionview.WaveInfo{101: degraded},
			want:    map[int]sessionview.WaveInfo{101: degraded},
		},
		{
			name:     "degraded row keeps previous data",
			previous: map[int]sessionview.WaveInfo{101: blocked},
			current:  map[int]sessionview.WaveInfo{101: degraded},
			want:     map[int]sessionview.WaveInfo{101: blocked},
		},
		{
			name:     "fresh row replaces previous",
			previous: map[int]sessionview.WaveInfo{101: blocked},
			current:  map[int]sessionview.WaveInfo{101: fresh},
			want:     map[int]sessionview.WaveInfo{101: fresh},
		},
		{
			name:     "row dropped by partial fetch is restored",
			previous: map[int]sessionview.WaveInfo{101: blocked, 102: fresh},
			current:  map[int]sessionview.WaveInfo{101: degraded},
			want:     map[int]sessionview.WaveInfo{101: blocked, 102: fresh},
		},
		{
			name:     "degraded previous does not mask newer degraded row",
			previous: map[int]sessionview.WaveInfo{101: degraded},
			current:  map[int]sessionview.WaveInfo{101: {Wave: 3, Degraded: true}},
			want:     map[int]sessionview.WaveInfo{101: {Wave: 3, Degraded: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mergeDegradedWaveInfos(tt.previous, tt.current)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("mergeDegradedWaveInfos = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMergeDegradedWaveGraphUnionsChildren(t *testing.T) {
	t.Parallel()

	previous := sessionview.WaveGraph{
		Children: []ghissue.Issue{{Number: 103, Title: "old three"}, {Number: 101, Title: "one"}},
		Info:     map[int]sessionview.WaveInfo{101: {Wave: 1}},
	}
	current := sessionview.WaveGraph{
		Children: []ghissue.Issue{{Number: 103, Title: "new three"}},
		Info:     map[int]sessionview.WaveInfo{103: {Wave: 2}},
	}

	got := mergeDegradedWaveGraph(previous, current)
	// Children: dropped #101 is restored, #103 keeps the fresh title, sorted.
	wantChildren := []ghissue.Issue{{Number: 101, Title: "one"}, {Number: 103, Title: "new three"}}
	if !reflect.DeepEqual(got.Children, wantChildren) {
		t.Fatalf("merged children = %#v, want %#v", got.Children, wantChildren)
	}
	// Info: same last-known-data semantics as mergeDegradedWaveInfos.
	if got.Info[101].Wave != 1 || got.Info[103].Wave != 2 {
		t.Fatalf("merged info = %#v", got.Info)
	}
}

func TestRefreshGHFetchesPRsForWaveChildren(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)

	// The wave graph knows a child (#103) with no recorded pane. Its PR state
	// must be fetched in the same refresh the child is discovered in — not a
	// full gh tick later — so the synthetic row paints with real data.
	gh := &countingGH{waves: map[string]sessionview.WaveGraph{
		"100": {
			Children: []ghissue.Issue{
				{Number: 101, Title: "recorded", State: "OPEN"},
				{Number: 103, Title: "queued child", State: "OPEN"},
			},
			Info: map[int]sessionview.WaveInfo{101: {Wave: 1}, 103: {Wave: 2}},
		},
	}}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.waveInterval = time.Hour
	p.refreshGH()
	if gh.calls[103] != 1 {
		t.Fatalf("IssuePRs(103) calls after first refresh = %d, want 1 (post-wave fetch)", gh.calls[103])
	}

	// Once cached, the child joins the phase-1 fetch set on every tick even
	// while the wave phase stays throttled.
	p.refreshGH()
	if gh.calls[103] != 2 {
		t.Fatalf("IssuePRs(103) calls after second refresh = %d, want 2 (phase-1 union)", gh.calls[103])
	}
	if gh.waveCalls["100"] != 1 {
		t.Fatalf("Waves(100) calls = %d, want 1 (throttled)", gh.waveCalls["100"])
	}

	// The built snapshot renders the child as a synthetic not-started row with
	// the cached PR state (countingGH reports a merged PR for every issue).
	snap := p.build()
	panes := snap.Sessions[0].Panes
	if len(panes) != 2 {
		t.Fatalf("want recorded+synthetic panes, got %+v", panes)
	}
	queued := panes[1]
	if queued.IssueNum != 103 || !queued.NotStarted || !queued.HasMergedPR {
		t.Fatalf("synthetic pane = %+v", queued)
	}
	if snap.Rollup.NotStarted != 1 || snap.Rollup.Total != 2 {
		t.Fatalf("rollup = %+v, want notStarted=1 total=2", snap.Rollup)
	}
}

func TestRefreshGHFetchesUncachedParentDespiteWaveThrottle(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)

	gh := &countingGH{}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.waveInterval = time.Hour // throttle would block everything but the first pass
	p.refreshGH()
	if gh.waveCalls["100"] != 1 {
		t.Fatalf("Waves(100) calls = %d, want 1 (first pass)", gh.waveCalls["100"])
	}

	// A new parent recorded mid-interval must be fetched immediately; the
	// already-cached parent stays throttled.
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"},
	  {"parent":"200","issueNum":201,"slug":"b","paneId":"%2"}
	]}`)
	p.refreshGH()
	if gh.waveCalls["100"] != 1 {
		t.Fatalf("Waves(100) calls = %d, want 1 (still throttled)", gh.waveCalls["100"])
	}
	if gh.waveCalls["200"] != 1 {
		t.Fatalf("Waves(200) calls = %d, want 1 (uncached parent bypasses throttle)", gh.waveCalls["200"])
	}
}

func TestRefreshGHFetchesNewIssueUnderCachedParentDespiteThrottle(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)

	gh := &countingGH{}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.waveInterval = time.Hour
	p.refreshGH()
	if gh.waveCalls["100"] != 1 {
		t.Fatalf("Waves(100) calls = %d, want 1 (first pass)", gh.waveCalls["100"])
	}

	// A NEW issue under the already-cached parent must trigger a refetch;
	// once attempted, further ticks with the same nums stay throttled even if
	// the lookup keeps failing (no per-tick retry loop).
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"},
	  {"parent":"100","issueNum":102,"slug":"b","paneId":"%2"}
	]}`)
	p.refreshGH()
	if gh.waveCalls["100"] != 2 {
		t.Fatalf("Waves(100) calls = %d, want 2 (new issue bypasses throttle)", gh.waveCalls["100"])
	}
	if !slices.Equal(gh.waveNums["100"], []int{101, 102}) {
		t.Fatalf("recordedNums = %v, want [101 102]", gh.waveNums["100"])
	}
	p.refreshGH()
	if gh.waveCalls["100"] != 2 {
		t.Fatalf("Waves(100) calls = %d, want 2 (same nums stay throttled)", gh.waveCalls["100"])
	}
}
