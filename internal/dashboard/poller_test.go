package dashboard

import (
	"os"
	"path/filepath"
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
	waves     map[string]map[int]sessionview.WaveInfo
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

func (g *countingGH) Waves(parent string, recordedNums []int) (map[int]sessionview.WaveInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waveCalls == nil {
		g.waveCalls = map[string]int{}
		g.waveNums = map[string][]int{}
	}
	g.waveCalls[parent]++
	g.waveNums[parent] = slices.Clone(recordedNums)
	if g.wavesErr != nil {
		return nil, g.wavesErr
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

	gh := &countingGH{waves: map[string]map[int]sessionview.WaveInfo{
		"100": {
			101: {Wave: 1, Blockers: []blockers.Status{}},
			102: {Wave: 2, WaveLabel: "wave 2", Blocked: true, Blockers: []blockers.Status{{Num: 101, State: "OPEN"}}},
		},
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
