package dashboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/blockers"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type countingGH struct {
	mu          sync.Mutex
	calls       map[int]int
	branchCalls map[string]int
	waveCalls   map[string]int
	waveNums    map[string][]int // recordedNums passed per parent (last call)
	waves       map[string]sessionview.WaveGraph
	wavesErr    error
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

func (g *countingGH) BranchPRs(branch string) ([]ghissue.PRRef, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.branchCalls == nil {
		g.branchCalls = map[string]int{}
	}
	g.branchCalls[branch]++
	return []ghissue.PRRef{{Number: 700, State: "MERGED", CIStatus: "pass"}}, nil
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

func gitDash(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitTopDash(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("rev-parse --show-toplevel in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

func newCommittedRepoDash(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitDash(t, "", "init", "-b", "main", repo)
	gitDash(t, repo, "config", "user.name", "Fanout Test")
	gitDash(t, repo, "config", "user.email", "fanout@example.test")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDash(t, repo, "add", "file.txt")
	gitDash(t, repo, "commit", "-m", "base")
	return repo
}

func TestPollerBuildProjectsMatchingHerdrRuntimeObservation(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"backend":"herdr","paneId":"w1:p1","herdrWorkspaceId":"w1","herdrTerminalId":"terminal-a","herdrRepoKey":"/repo/.git","herdrAgentId":"agent-a","herdrAgentSession":{"source":"herdr:codex","agent":"codex","kind":"id","value":"conversation-a"},"herdrSession":"session-a","herdrSocketPath":"/tmp/herdr-a.sock","worktreePath":"/repo/worktree-a"}
	]}`)

	p := newPoller("o/n", root, &countingGH{}, nil, newHub())
	called := 0
	p.listLive = func() ([]backend.LivePane, error) {
		called++
		agentSession := &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "conversation-a",
		}
		return []backend.LivePane{{
			Ref:          backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
			Title:        "herdr child",
			AgentState:   backend.AgentWorking,
			AgentID:      "agent-a",
			AgentSession: agentSession,
			AgentPresent: true,
			TerminalID:   "terminal-a",
			Focused:      true,
			RepoKey:      "/repo/.git",
			ProjectRoot:  "/repo",
			WorktreePath: "/repo/worktree-a",
			SessionID:    "session-a",
			SocketPath:   "/tmp/herdr-a.sock",
		}}, nil
	}

	snap := p.build()
	if called == 0 {
		t.Fatal("runtime ListLive collector was not called")
	}
	if snap.Degraded.Tmux {
		t.Fatalf("runtime collector unexpectedly degraded: %+v", snap.Degraded)
	}
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Panes) != 1 {
		t.Fatalf("unexpected snapshot shape: %+v", snap.Sessions)
	}
	got := snap.Sessions[0].Panes[0]
	if !got.Alive || got.Backend != backend.Herdr || got.AgentState != "working" {
		t.Fatalf("matching herdr row = %+v, want live working row", got)
	}
	if got.RuntimeState != "live" || got.RuntimeTitle != "herdr child" {
		t.Fatalf("runtime projection = %q/%q, want live/herdr child", got.RuntimeState, got.RuntimeTitle)
	}
	if got.TmuxState != got.RuntimeState || got.TmuxTitle != got.RuntimeTitle {
		t.Fatalf("legacy tmux aliases = %q/%q, want runtime %q/%q", got.TmuxState, got.TmuxTitle, got.RuntimeState, got.RuntimeTitle)
	}
}

// TestPollerMergesSiblingWorktreesForBuildAndRefreshGH guards the cross-worktree
// fix: a Session fanned out from a sibling worktree must appear in build() AND
// have its issue state fetched by refreshGH (not left permanently UNKNOWN).
func TestPollerMergesSiblingWorktreesForBuildAndRefreshGH(t *testing.T) {
	repo := newCommittedRepoDash(t)
	homeTop := gitTopDash(t, repo)
	sibling := filepath.Join(t.TempDir(), "sib")
	gitDash(t, repo, "worktree", "add", "-b", "feat-sib", sibling)
	sibTop := gitTopDash(t, sibling)

	writeState(t, homeTop, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1","agent":"claude"}
	]}`)
	writeState(t, sibTop, `{"schemaVersion":1,"panes":[
	  {"parent":"200","issueNum":202,"slug":"b","paneId":"%2","agent":"codex"}
	]}`)

	gh := &countingGH{}
	p := newPoller("o/n", homeTop, gh, nil, newHub())
	p.refreshGH()
	snap := p.build()

	// refreshGH fetched the sibling's issue, not just the home worktree's.
	if gh.calls[101] != 1 || gh.calls[202] != 1 {
		t.Fatalf("IssuePRs calls = %v, want both 101 and 202 fetched once", gh.calls)
	}
	// build() surfaces both sessions, with provenance tagged per pane.
	parents := map[string]string{} // parent -> source root of its pane
	for _, s := range snap.Sessions {
		if len(s.Panes) != 1 {
			t.Fatalf("session %s panes = %+v, want 1", s.Parent, s.Panes)
		}
		parents[s.Parent] = s.Panes[0].SourceProjectRoot
	}
	if parents["100"] != homeTop {
		t.Fatalf("session 100 source = %q, want home %q", parents["100"], homeTop)
	}
	if parents["200"] != sibTop {
		t.Fatalf("session 200 source = %q, want sibling %q", parents["200"], sibTop)
	}
}

func TestPollerRefreshGHPopulatesBranchCacheAndBuildReadsIt(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"plan:alpha","issueNum":0,"taskId":"task-a","branchName":"fanout/task-a","slug":"a","paneId":"%1"},
	  {"parent":"plan:alpha","issueNum":0,"taskId":"task-b","branchName":"fanout/task-a","slug":"b","paneId":"%2"}
	]}`)

	gh := &countingGH{}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.refreshGH()
	snap := p.build()

	if len(gh.calls) != 0 {
		t.Fatalf("IssuePRs calls = %v, want none for task-only rows", gh.calls)
	}
	if gh.branchCalls["fanout/task-a"] != 1 {
		t.Fatalf("BranchPRs(fanout/task-a) calls = %d, want 1", gh.branchCalls["fanout/task-a"])
	}
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Panes) != 2 {
		t.Fatalf("unexpected snapshot shape: %+v", snap.Sessions)
	}
	got := snap.Sessions[0].Panes[0]
	if got.TaskID != "task-a" || got.IssueState != sessionview.IssueStateUnknown || !got.HasMergedPR {
		t.Fatalf("task branch PR state should reach the snapshot, got %+v", got)
	}
	if snap.Degraded.GitHub {
		t.Fatal("GitHub should not be degraded on branch PR success")
	}
}

func TestPollerRefreshGHPopulatesManualPromptModePRAndCI(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"@manual","issueNum":-1,"branchName":"fanout/prompt-session","slug":"prompt-session","paneId":"%1","agent":"codex","codexPlanMode":true}
	]}`)

	gh := &countingGH{}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.refreshGH()
	snap := p.build()

	if len(gh.calls) != 0 {
		t.Fatalf("IssuePRs calls = %v, want none for the manual row", gh.calls)
	}
	if gh.branchCalls["fanout/prompt-session"] != 1 {
		t.Fatalf("BranchPRs(fanout/prompt-session) calls = %d, want 1", gh.branchCalls["fanout/prompt-session"])
	}
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Panes) != 1 {
		t.Fatalf("unexpected snapshot shape: %+v", snap.Sessions)
	}
	got := snap.Sessions[0].Panes[0]
	if got.IssueNum != -1 || got.TaskID != "" || !got.PlanMode {
		t.Fatalf("manual prompt-mode identity = issue:%d task:%q plan:%v", got.IssueNum, got.TaskID, got.PlanMode)
	}
	if len(got.PRs) != 1 || got.PRs[0].Number != 700 || !got.HasMergedPR {
		t.Fatalf("manual prompt-mode PR state = %+v", got)
	}
	if got.CIStatus != "pass" || got.Derived.CI != "pass" || got.Derived.PrimaryPRNumber != 700 {
		t.Fatalf("manual prompt-mode derived PR/CI = ci:%q derived:%+v", got.CIStatus, got.Derived)
	}
	if snap.Degraded.GitHub {
		t.Fatal("GitHub should not be degraded on manual branch PR success")
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

func TestRefreshGHCallsWavesOncePerNormalizedParentWithRecordedIssues(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"0100","issueNum":101,"slug":"a","paneId":"%1"},
	  {"parent":"100","issueNum":102,"slug":"b","paneId":"%2"},
	  {"parent":"@manual","issueNum":-1,"slug":"m","paneId":"%3"}
	]}`)

	gh := &countingGH{}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.refreshGH()

	if len(gh.waveCalls) != 1 || gh.waveCalls["100"] != 1 {
		t.Fatalf("expected one Waves call per normalized parent with recorded issues, got %v", gh.waveCalls)
	}
	if !slices.Equal(gh.waveNums["100"], []int{101, 102}) {
		t.Fatalf(`"0100" and "100" must pool recorded nums, got %v`, gh.waveNums["100"])
	}
	if _, ok := gh.waveNums["@manual"]; ok {
		t.Fatalf("parents without positive issue rows must not fetch waves, got %v", gh.waveNums["@manual"])
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

func TestDistinctIssueLessBranchesUsesBranchOwners(t *testing.T) {
	got := distinctIssueLessBranches(state.Store{Panes: []state.Pane{
		{Parent: "plan:a", IssueNum: 0, TaskID: "task-a", BranchName: " fanout/task-a "},
		{Parent: "plan:a", IssueNum: 0, TaskID: "task-b", BranchName: "fanout/task-a"},
		{Parent: "@manual", IssueNum: -1, BranchName: " fanout/manual ", CodexPlanMode: true},
		{Parent: "@manual", IssueNum: -2, BranchName: "fanout/manual"}, // duplicate
		{Parent: "100", IssueNum: 101, TaskID: "task-issue", BranchName: "fanout/issue"},
		{Parent: "@manual", IssueNum: -3, BranchName: ""},
		{Parent: "@manual", IssueNum: -4, Kind: state.PaneKindShell, BranchName: "fanout/shell"},
		{Parent: "@manual", IssueNum: -5, Kind: state.PaneKindAttachedAgent, BranchName: "fanout/attached"},
	}})

	want := []string{"fanout/manual", "fanout/task-a"}
	if !slices.Equal(got, want) {
		t.Fatalf("distinctIssueLessBranches() = %#v, want %#v", got, want)
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

// state から消えた親の wave エントリは prune され、その子の PR fetch も止まる。
func TestRefreshGHPrunesWaveCacheForRemovedParents(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)
	gh := &countingGH{waves: map[string]sessionview.WaveGraph{
		"100": {
			Children: []ghissue.Issue{{Number: 103, Title: "child", State: "OPEN"}},
			Info:     map[int]sessionview.WaveInfo{},
		},
	}}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.waveInterval = 0 // 常に wave パスを走らせる
	p.refreshGH()
	if gh.calls[103] != 1 {
		t.Fatalf("IssuePRs(103) calls = %d, want 1", gh.calls[103])
	}

	// 親 100 の state 行が消える(--cleanup 相当)
	writeState(t, root, `{"schemaVersion":1,"panes":[]}`)
	p.refreshGH()
	p.refreshGH()
	if gh.calls[103] != 1 {
		t.Fatalf("IssuePRs(103) calls after cleanup = %d, want 1 (pruned)", gh.calls[103])
	}
	p.cacheMu.Lock()
	_, cached := p.waveCache["100"]
	p.cacheMu.Unlock()
	if cached {
		t.Fatalf("waveCache entry for removed parent must be pruned")
	}
}

// OPEN 時代の古いキャッシュが残っていても、CLOSED へ遷移した子は再 fetch
// される(キャッシュ側も CLOSED のときだけスキップ)。
func TestRefreshChildPRsRefetchesOnOpenToClosedTransition(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)
	gh := &countingGH{waves: map[string]sessionview.WaveGraph{
		"100": {
			Children: []ghissue.Issue{{Number: 103, Title: "closing child", State: "CLOSED"}},
			Info:     map[int]sessionview.WaveInfo{},
		},
	}}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.waveInterval = 0
	// OPEN 時代のキャッシュを偽装(graph は既に CLOSED を報告している)
	p.cacheMu.Lock()
	p.cache[103] = ghCacheEntry{state: "OPEN"}
	p.cacheMu.Unlock()
	p.refreshGH()
	if gh.calls[103] != 1 {
		t.Fatalf("IssuePRs(103) calls = %d, want 1 (stale OPEN cache must refetch)", gh.calls[103])
	}
}

// CLOSED で PR キャッシュ済みの子は wave パスでも再 fetch しない(終端状態)。
func TestRefreshChildPRsSkipsClosedCachedChildren(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"}
	]}`)
	gh := &countingGH{waves: map[string]sessionview.WaveGraph{
		"100": {
			Children: []ghissue.Issue{{Number: 103, Title: "closed child", State: "CLOSED"}},
			Info:     map[int]sessionview.WaveInfo{},
		},
	}}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.waveInterval = 0
	p.refreshGH()
	if gh.calls[103] != 1 {
		t.Fatalf("IssuePRs(103) calls = %d, want 1 (first fetch)", gh.calls[103])
	}
	p.refreshGH()
	if gh.calls[103] != 1 {
		t.Fatalf("IssuePRs(103) calls = %d, want 1 (closed+cached skipped)", gh.calls[103])
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

	// 子の PR fetch は wave cadence に閉じる: wave パスが throttle されている
	// tick では子を再 fetch しない(20 秒 tick のコストは記録 pane 数に比例
	// させ、全子 issue 数に比例させない — gh API 予算の防衛線)。
	p.refreshGH()
	if gh.calls[103] != 1 {
		t.Fatalf("IssuePRs(103) calls after throttled refresh = %d, want 1 (wave cadence only)", gh.calls[103])
	}
	if gh.waveCalls["100"] != 1 {
		t.Fatalf("Waves(100) calls = %d, want 1 (throttled)", gh.waveCalls["100"])
	}
	// 記録 pane の方は毎 tick fetch される
	if gh.calls[101] != 2 {
		t.Fatalf("IssuePRs(101) calls = %d, want 2 (every tick)", gh.calls[101])
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
	// countingGH は全 issue に CLOSED+merged を返すので、synthetic 子は
	// 「pane なしで完了」として Total/Merged に入り NotStarted には数えない
	if snap.Rollup.NotStarted != 0 || snap.Rollup.Total != 2 || !snap.Rollup.AllMerged {
		t.Fatalf("rollup = %+v, want total=2 allMerged", snap.Rollup)
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
