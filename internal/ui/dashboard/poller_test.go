package dashboard

import (
	"errors"
	"fmt"
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
	issueErrs   map[int]error
	batchCalls  [][]int
	branchPRs   []ghissue.PRRef // BranchPRs answer; nil keeps the default #700
	stackCalls  [][]int
	stacks      map[int]*ghissue.PRStack
	stacksErr   error
	stackFails  map[int]bool // numbers PRStacks fails to read, the rest succeeding
}

func (g *countingGH) IssuePRsBatch(nums []int) (map[int]ghissue.IssueSnapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.calls == nil {
		g.calls = map[int]int{}
	}
	g.batchCalls = append(g.batchCalls, slices.Clone(nums))
	snapshots := make(map[int]ghissue.IssueSnapshot, len(nums))
	var loadErr error
	for _, num := range nums {
		g.calls[num]++
		if err := g.issueErrs[num]; err != nil {
			loadErr = errors.Join(loadErr, fmt.Errorf("#%d: %w", num, err))
			continue
		}
		snapshots[num] = ghissue.IssueSnapshot{
			Number: num,
			State:  "CLOSED",
			PRs:    []ghissue.PRRef{{Number: 900 + num, State: "MERGED"}},
		}
	}
	return snapshots, loadErr
}

func (g *countingGH) BranchPRs(branch string) ([]ghissue.PRRef, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.branchCalls == nil {
		g.branchCalls = map[string]int{}
	}
	g.branchCalls[branch]++
	if g.branchPRs != nil {
		return slices.Clone(g.branchPRs), nil
	}
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

// PRStacks reads every number: g.stacks for the stacked ones, nil ("not in a
// stack") for the rest. With stacksErr set it fails outright, reading nothing.
func (g *countingGH) PRStacks(nums []int) (map[int]*ghissue.PRStack, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stackCalls = append(g.stackCalls, slices.Clone(nums))
	if g.stacksErr != nil {
		return nil, g.stacksErr
	}
	out := map[int]*ghissue.PRStack{}
	var loadErr error
	for _, num := range nums {
		if g.stackFails[num] {
			loadErr = errors.Join(loadErr, fmt.Errorf("pr_%d: graphql: could not resolve", num))
			continue
		}
		out[num] = g.stacks[num]
	}
	return out, loadErr
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
	  {"parent":"100","issueNum":101,"backend":"herdr","paneId":"w1:p1","herdrWorkspaceId":"w1","herdrWorkspaceLabel":"owned-label-a","herdrTerminalId":"terminal-a","herdrRepoKey":"/repo/.git","herdrAgentId":"agent-a","herdrAgentSession":{"source":"herdr:codex","agent":"codex","kind":"id","value":"conversation-a"},"herdrSession":"session-a","herdrSocketPath":"/tmp/herdr-a.sock","agent":"codex","worktreePath":"/repo/worktree-a"}
	]}`)

	p := newPoller("o/n", root, &countingGH{}, nil, newHub())
	called := 0
	p.listLive = func() ([]backend.LivePane, error) {
		called++
		agentSession := &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "conversation-a",
		}
		return []backend.LivePane{{
			Ref:              backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
			Title:            "herdr child",
			AgentState:       backend.AgentWorking,
			NativeAgentState: "working",
			AgentID:          "agent-a",
			AgentProvider:    "codex",
			AgentSession:     agentSession,
			AgentPresent:     true,
			WorkspaceLabel:   "owned-label-a",
			TerminalID:       "terminal-a",
			Focused:          true,
			RepoKey:          "/repo/.git",
			ProjectRoot:      "/repo",
			WorktreePath:     "/repo/worktree-a",
			SessionID:        "session-a",
			SocketPath:       "/tmp/herdr-a.sock",
		}}, nil
	}

	snap := p.build()
	if called == 0 {
		t.Fatal("runtime ListLive collector was not called")
	}
	if snap.Degraded.Legacy {
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
	if got.LegacyState != got.RuntimeState || got.LegacyTitle != got.RuntimeTitle {
		t.Fatalf("legacy tmux aliases = %q/%q, want runtime %q/%q", got.LegacyState, got.LegacyTitle, got.RuntimeState, got.RuntimeTitle)
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

// stackTick runs one GitHub tick the way runGHTick does, minus the subscriber
// gate: refresh, the stack read, then publish.
func stackTick(p *poller) {
	p.refreshGH()
	p.refreshStacks(p.build())
	p.publishGHRefresh(time.Now())
}

func newStackPoller(t *testing.T, gh *countingGH) *poller {
	t.Helper()
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"@manual","issueNum":-1,"branchName":"fanout/layer-1","slug":"layer-1","paneId":"%1"}
	]}`)
	return newPoller("o/n", root, gh, nil, newHub())
}

// TestRefreshStacksHydratesBuildWithoutTouchingCache pins that the stack rides
// on a copy: the cached PR stays stack-free, so nothing else that reads the
// cache ever sees one.
func TestRefreshStacksHydratesBuildWithoutTouchingCache(t *testing.T) {
	stack := &ghissue.PRStack{Number: 3, Size: 2, Position: 1, BaseRef: "main"}
	gh := &countingGH{
		branchPRs: []ghissue.PRRef{
			{Number: 700, State: "OPEN", BaseRepo: "o/n"},
			// Same number in another repository: never read, never hydrated.
			{Number: 5, State: "OPEN", BaseRepo: "other/repo"},
		},
		stacks: map[int]*ghissue.PRStack{700: stack, 5: stack},
	}
	p := newStackPoller(t, gh)
	stackTick(p)
	// The published frame itself, not a later rebuild: the tick reads stacks
	// before it publishes, so a new PR never flashes without its stack.
	snap := p.latest

	if want := [][]int{{700}}; !reflect.DeepEqual(gh.stackCalls, want) {
		t.Fatalf("PRStacks calls = %v, want %v", gh.stackCalls, want)
	}
	prs := snap.Sessions[0].Panes[0].PRs
	if prs[0].Stack != stack || prs[1].Stack != nil {
		t.Fatalf("build() PR stacks = %+v / %+v, want only #700 hydrated", prs[0].Stack, prs[1].Stack)
	}
	if cached := p.branchCache["fanout/layer-1"].prs; cached[0].Stack != nil {
		t.Fatalf("branchCache PR stack = %+v, want the cache left untouched", cached[0].Stack)
	}
}

func TestRefreshStacksThrottle(t *testing.T) {
	stack := &ghissue.PRStack{Number: 3, Size: 2, Position: 1, BaseRef: "main"}
	tests := []struct {
		name string
		// between runs after the first tick and before the second.
		between   func(p *poller, gh *countingGH)
		wantCalls int
		wantStack *ghissue.PRStack
	}{
		{
			name:      "same pull requests inside the interval reuse the cache",
			between:   func(*poller, *countingGH) {},
			wantCalls: 1,
			wantStack: stack,
		},
		{
			name:      "elapsed interval rereads",
			between:   func(p *poller, _ *countingGH) { p.lastStackRefresh = time.Now().Add(-p.waveInterval) },
			wantCalls: 2,
			wantStack: stack,
		},
		{
			name: "new pull request rereads at once",
			between: func(_ *poller, gh *countingGH) {
				gh.branchPRs = append(gh.branchPRs, ghissue.PRRef{Number: 701, State: "OPEN", BaseRepo: "o/n"})
			},
			wantCalls: 2,
			wantStack: stack,
		},
		{
			name: "failed reread keeps the last known stack",
			between: func(p *poller, gh *countingGH) {
				p.lastStackRefresh = time.Time{}
				gh.stacksErr = errors.New("rate limited")
			},
			wantCalls: 2,
			wantStack: stack,
		},
		{
			name: "reads failing past the staleness bound drop the stacks",
			between: func(p *poller, gh *countingGH) {
				p.lastStackRefresh = time.Time{}
				e := p.stackCache[700]
				e.readAt = time.Now().Add(-(staleStacksAfter + 1) * p.waveInterval)
				p.stackCache[700] = e
				gh.stacksErr = errors.New("field stack does not exist")
			},
			wantCalls: 2,
		},
		{
			name: "pull request no longer in a stack leaves the cache",
			between: func(p *poller, gh *countingGH) {
				p.lastStackRefresh = time.Time{}
				gh.stacks = nil
			},
			wantCalls: 2,
		},
		{
			name:      "pull request that leaves the snapshot leaves the cache",
			between:   func(_ *poller, gh *countingGH) { gh.branchPRs = []ghissue.PRRef{} },
			wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := &countingGH{
				branchPRs: []ghissue.PRRef{{Number: 700, State: "OPEN", BaseRepo: "o/n"}},
				stacks:    map[int]*ghissue.PRStack{700: stack},
			}
			p := newStackPoller(t, gh)
			stackTick(p)
			tt.between(p, gh)
			stackTick(p)

			if len(gh.stackCalls) != tt.wantCalls {
				t.Fatalf("PRStacks calls = %v, want %d", gh.stackCalls, tt.wantCalls)
			}
			if got := p.stackCache[700].stack; got != tt.wantStack {
				t.Fatalf("stackCache[700] = %+v, want %+v", got, tt.wantStack)
			}
			// No placeholder for "read, not in a stack": an empty cache is what
			// lets withStacks skip copying in a repository without stacks.
			if tt.wantStack == nil && len(p.stackCache) != 0 {
				t.Fatalf("stackCache = %v, want empty", p.stackCache)
			}
		})
	}
}

// TestRefreshStacksPartialReadKeepsWhatItRead pins per-PR freshness: one alias
// failing must not age out the stacks the same read returned.
func TestRefreshStacksPartialReadKeepsWhatItRead(t *testing.T) {
	stack := &ghissue.PRStack{Number: 3, Size: 2, Position: 1, BaseRef: "main"}
	gh := &countingGH{
		branchPRs: []ghissue.PRRef{
			{Number: 700, State: "OPEN", BaseRepo: "o/n"},
			{Number: 701, State: "OPEN", BaseRepo: "o/n"},
		},
		stacks:     map[int]*ghissue.PRStack{700: stack},
		stackFails: map[int]bool{701: true},
	}
	p := newStackPoller(t, gh)
	stackTick(p)
	stackTick(p) // same pull requests inside the interval: no read

	if len(gh.stackCalls) != 1 {
		t.Fatalf("PRStacks calls = %v, want 1", gh.stackCalls)
	}
	if got := p.stackCache[700].stack; got != stack {
		t.Fatalf("stackCache[700] = %+v, want the stack the partial read returned", got)
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
	if !reflect.DeepEqual(gh.batchCalls, [][]int{{101, 102}}) {
		t.Fatalf("IssuePRsBatch calls = %v, want one [101 102] batch", gh.batchCalls)
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

func TestRefreshIssuePRsKeepsSuccessfulSiblingOnBatchFailure(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1"},
	  {"parent":"100","issueNum":102,"slug":"b","paneId":"%2"}
	]}`)

	gh := &countingGH{issueErrs: map[int]error{102: errors.New("missing")}}
	p := newPoller("o/n", root, gh, nil, newHub())
	p.refreshGH()
	snap := p.build()

	if !reflect.DeepEqual(gh.batchCalls, [][]int{{101, 102}}) {
		t.Fatalf("IssuePRsBatch calls = %v, want one [101 102] batch", gh.batchCalls)
	}
	if !snap.Sessions[0].Panes[0].HasMergedPR {
		t.Fatal("successful #101 PR state was lost after sibling batch failure")
	}
	if snap.Sessions[0].Panes[1].IssueState != sessionview.IssueStateUnknown {
		t.Fatalf("failed #102 state = %q, want UNKNOWN", snap.Sessions[0].Panes[1].IssueState)
	}
	if !snap.Degraded.GitHub {
		t.Fatal("failed #102 must degrade GitHub")
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
		{Parent: "@manual", IssueNum: -1, BranchName: " fanout/manual ", PlanMode: true},
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

func TestPollerGHTickRequiresSubscriber(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, `{"schemaVersion":1,"panes":[
	  {"parent":"100","issueNum":101,"slug":"a","paneId":"%1","agent":"claude"}
	]}`)

	h := newHub()
	gh := &countingGH{}
	resolveCalls := 0
	p := newLazyPoller(root, func() (string, GHProvider, error) {
		resolveCalls++
		return "o/n", gh, nil
	}, h)

	// With no dashboard open, even a GitHub tick must not resolve the repository
	// or call the provider.
	p.runGHTick()
	if resolveCalls != 0 {
		t.Fatalf("resolve calls without subscribers = %d, want 0", resolveCalls)
	}
	if len(gh.calls) != 0 || len(gh.branchCalls) != 0 || len(gh.waveCalls) != 0 {
		t.Fatalf("provider calls without subscribers = issues %v, branches %v, waves %v; want none",
			gh.calls, gh.branchCalls, gh.waveCalls)
	}

	// The next GitHub tick after subscribing resolves, refreshes, and broadcasts
	// the resulting Snapshot.
	ch := h.subscribe()
	p.runGHTick()
	if resolveCalls != 1 {
		t.Fatalf("resolve calls after subscribing = %d, want 1", resolveCalls)
	}
	if gh.calls[101] != 1 || gh.waveCalls["100"] != 1 {
		t.Fatalf("provider calls after subscribing = issues %v, waves %v; want issue 101 and parent 100 once",
			gh.calls, gh.waveCalls)
	}
	select {
	case frame := <-ch:
		if !strings.Contains(string(frame), `"repo":"o/n"`) ||
			!strings.Contains(string(frame), `"hasMergedPr":true`) {
			t.Fatalf("broadcast Snapshot did not contain refreshed GitHub data: %s", frame)
		}
	default:
		t.Fatal("GitHub tick after subscribing did not broadcast a Snapshot")
	}

	// Closing the last stream stops later provider calls without discarding the
	// last-known cache used by the state-only snapshot.
	h.unsubscribe(ch)
	p.runGHTick()
	if gh.calls[101] != 1 || gh.waveCalls["100"] != 1 {
		t.Fatalf("provider calls after unsubscribing = issues %v, waves %v; want cached counts unchanged",
			gh.calls, gh.waveCalls)
	}
	snap := p.build()
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Panes) != 1 ||
		!snap.Sessions[0].Panes[0].HasMergedPR {
		t.Fatalf("snapshot after unsubscribing did not retain cached GitHub data: %+v", snap)
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
