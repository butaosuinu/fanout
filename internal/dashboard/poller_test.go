package dashboard

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
)

type countingGH struct {
	mu    sync.Mutex
	calls map[int]int
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

	gh := &countingGH{}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.refreshGH()
	snap := p.build()

	if gh.calls[101] != 1 || gh.calls[102] != 1 {
		t.Fatalf("expected one gh call per issue, got %v", gh.calls)
	}
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Panes) != 2 {
		t.Fatalf("unexpected snapshot shape: %+v", snap.Sessions)
	}
	if !snap.Sessions[0].Panes[0].HasMergedPR {
		t.Fatal("PR state from cache should reach the built snapshot")
	}
	if snap.Degraded.GitHub {
		t.Fatal("GitHub should not be degraded on success")
	}
}

func TestDistinctIssueNumsSkipsSyntheticManualPanes(t *testing.T) {
	got := distinctIssueNums(state.Store{Panes: []state.Pane{
		{IssueNum: -1},
		{IssueNum: 0},
		{IssueNum: 102},
		{IssueNum: 101},
		{IssueNum: 102},
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
