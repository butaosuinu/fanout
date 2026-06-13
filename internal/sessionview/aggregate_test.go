package sessionview

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
)

func fixedNow() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }

func storeOf(panes ...state.Pane) func() (state.Store, error) {
	return func() (state.Store, error) {
		return state.Store{SchemaVersion: 1, Panes: panes}, nil
	}
}

func pane(parent string, num int, paneID string) state.Pane {
	return state.Pane{
		Parent: parent, IssueNum: num, Slug: "slug", PaneID: paneID,
		Agent: "claude", DisplayName: "disp", BranchName: "fanout/x", CreatedAt: "2026-06-04T00:00:00Z",
		WorktreePath: "/wt/" + paneID,
	}
}

// livePanesAt builds a LivePanes collector marking the given panes live, mapping
// each pane id to its recorded worktree path (so paneAlive's path check passes).
func livePanesAt(paneIDs ...string) func() (map[string]LivePaneInfo, error) {
	return func() (map[string]LivePaneInfo, error) {
		m := map[string]LivePaneInfo{}
		for _, id := range paneIDs {
			m[id] = LivePaneInfo{Path: "/wt/" + id}
		}
		return m, nil
	}
}

// livePanesWith builds a LivePanes collector serving the given map verbatim,
// for tests that need per-pane titles/agent states beyond livePanesAt's defaults.
func livePanesWith(m map[string]LivePaneInfo) func() (map[string]LivePaneInfo, error) {
	return func() (map[string]LivePaneInfo, error) { return m, nil }
}

// wavesNone is a Waves collector that always reports a cache miss (not fetched
// yet) — the non-degraded GitHub baseline for tests not exercising waves.
func wavesNone(parent string) (WaveGraph, error) {
	return WaveGraph{}, nil
}

// wavesOf builds a Waves collector serving the same WaveInfo map (no child
// set, so no synthetic rows) for any parent and counting calls through calls
// (when non-nil).
func wavesOf(info map[int]WaveInfo, calls *int) func(string) (WaveGraph, error) {
	return wavesGraphOf(WaveGraph{Info: info}, calls)
}

// wavesGraphOf builds a Waves collector serving the same full WaveGraph for
// any parent.
func wavesGraphOf(graph WaveGraph, calls *int) func(string) (WaveGraph, error) {
	return func(parent string) (WaveGraph, error) {
		if calls != nil {
			*calls++
		}
		return graph, nil
	}
}

func mergedPR(n int) []ghissue.PRRef {
	at := "2026-06-05T00:00:00Z"
	return []ghissue.PRRef{{Number: n, State: "MERGED", MergedAt: &at}}
}

func TestBuildGroupsByParentSortedAndComputesRollups(t *testing.T) {
	p101 := pane("100", 101, "%1")
	p101.BaseBranch = "main"
	gotBaseRefs := map[string]string{}
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("100", 102, "%2"), p101, pane("90", 91, "%9")),
		LivePanes: livePanesAt("%1", "%9"),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			if num == 101 {
				return "CLOSED", mergedPR(501), nil
			}
			return "OPEN", []ghissue.PRRef{}, nil
		},
		Waves: wavesNone,
		WorktreeStat: func(path, baseRef string) (WorktreeStat, error) {
			gotBaseRefs[path] = baseRef
			if path == "/wt/%1" {
				return WorktreeStat{DiffSummary: "+12/-3", DirtyState: "dirty"}, nil
			}
			return WorktreeStat{DiffSummary: "+0/-0", DirtyState: "clean"}, nil
		},
	}
	snap := Build("owner/name", "/root", c)

	// the state row's BaseBranch must reach the worktree-stat collector;
	// rows without one pass "" (legacy rows).
	if gotBaseRefs["/wt/%1"] != "main" {
		t.Fatalf("baseRef for /wt/%%1 = %q, want main", gotBaseRefs["/wt/%1"])
	}
	if gotBaseRefs["/wt/%2"] != "" {
		t.Fatalf("baseRef for /wt/%%2 = %q, want empty (legacy row)", gotBaseRefs["/wt/%2"])
	}

	if snap.GeneratedAt != "2026-06-06T12:00:00Z" {
		t.Fatalf("GeneratedAt = %q", snap.GeneratedAt)
	}
	if len(snap.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(snap.Sessions))
	}
	// numeric parents ascending: 90 before 100
	if snap.Sessions[0].Parent != "90" || snap.Sessions[1].Parent != "100" {
		t.Fatalf("parent order = %q,%q want 90,100", snap.Sessions[0].Parent, snap.Sessions[1].Parent)
	}
	// panes within parent 100 sorted by IssueNum: 101 then 102
	p100 := snap.Sessions[1]
	if p100.Panes[0].IssueNum != 101 || p100.Panes[1].IssueNum != 102 {
		t.Fatalf("pane order = %d,%d want 101,102", p100.Panes[0].IssueNum, p100.Panes[1].IssueNum)
	}
	if !p100.Panes[0].HasMergedPR || !p100.Panes[0].Alive || p100.Panes[0].IssueState != "CLOSED" {
		t.Fatalf("#101 view = %+v", p100.Panes[0])
	}
	if p100.Panes[0].DiffSummary != "+12/-3" || p100.Panes[0].DirtyState != "dirty" {
		t.Fatalf("#101 worktree stat = %q/%q, want +12/-3/dirty", p100.Panes[0].DiffSummary, p100.Panes[0].DirtyState)
	}
	if p100.Panes[1].Alive {
		t.Fatalf("#102 should be dead (paneID %%2 not in live set)")
	}
	// repo rollup: total 3, merged 1, pending 2, live 2
	if snap.Rollup.Total != 3 || snap.Rollup.Merged != 1 || snap.Rollup.Pending != 2 || snap.Rollup.Live != 2 {
		t.Fatalf("repo rollup = %+v", snap.Rollup)
	}
	if snap.Rollup.AllMerged {
		t.Fatal("repo AllMerged should be false")
	}
	if snap.Degraded.Tmux || snap.Degraded.GitHub {
		t.Fatalf("degraded = %+v want clean", snap.Degraded)
	}
}

func TestBuildPerSessionAllMerged(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("7", 8, "%1")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "CLOSED", mergedPR(9), nil },
	}
	snap := Build("o/n", "/root", c)
	if !snap.Sessions[0].Rollup.AllMerged || !snap.Rollup.AllMerged {
		t.Fatalf("AllMerged should be true: session=%+v repo=%+v", snap.Sessions[0].Rollup, snap.Rollup)
	}
}

func TestBuildDegradesWhenTmuxFails(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		LivePanes: func() (map[string]LivePaneInfo, error) { return nil, errors.New("tmux not found") },
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	snap := Build("o/n", "/root", c)
	if !snap.Degraded.Tmux {
		t.Fatal("Degraded.Tmux should be set")
	}
	if snap.Sessions[0].Panes[0].Alive {
		t.Fatal("pane should be dead when tmux is down")
	}
	if snap.Degraded.Reason == "" {
		t.Fatal("Degraded.Reason should mention tmux")
	}
}

func TestBuildDegradesWhenGitHubFails(t *testing.T) {
	calls := 0
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1"), pane("1", 2, "%1")), // dup issue num -> single gh call
		LivePanes: livePanesAt("%1"),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			calls++
			return "", nil, errors.New("gh auth required")
		},
	}
	snap := Build("o/n", "/root", c)
	if !snap.Degraded.GitHub {
		t.Fatal("Degraded.GitHub should be set")
	}
	if calls != 1 {
		t.Fatalf("gh should be called once per distinct issue number, got %d", calls)
	}
	if snap.Sessions[0].Panes[0].IssueState != IssueStateUnknown {
		t.Fatalf("issue state = %q want UNKNOWN", snap.Sessions[0].Panes[0].IssueState)
	}
	if snap.Sessions[0].Panes[0].PRs == nil {
		t.Fatal("PRs should be non-nil (empty slice) for JSON stability")
	}
}

func TestBuildCacheMissIsUnknownNotDegraded(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		LivePanes: livePanesAt(),
		// cheap-tier collector: cache miss returns ("", nil, nil)
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) { return "", nil, nil },
		Waves:    wavesNone,
	}
	snap := Build("o/n", "/root", c)
	if snap.Degraded.GitHub {
		t.Fatal("cache miss must NOT mark GitHub degraded")
	}
	if snap.Sessions[0].Panes[0].IssueState != IssueStateUnknown {
		t.Fatalf("issue state = %q want UNKNOWN", snap.Sessions[0].Panes[0].IssueState)
	}
}

func TestBuildWorktreeStatErrorIsPerPane(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
		WorktreeStat: func(path, baseRef string) (WorktreeStat, error) {
			return WorktreeStat{}, errors.New("git unavailable")
		},
	}
	snap := Build("o/n", "/root", c)
	got := snap.Sessions[0].Panes[0]
	if got.DiffSummary != "-" || got.DirtyState != "unknown" {
		t.Fatalf("worktree stat = %q/%q, want -/unknown", got.DiffSummary, got.DirtyState)
	}
	if got.WorktreeErr == "" {
		t.Fatal("WorktreeErr should carry the per-pane gitstat failure")
	}
	if snap.Degraded.GitHub || snap.Degraded.Tmux {
		t.Fatalf("worktree error should not mark gh/tmux degraded: %+v", snap.Degraded)
	}
}

func TestBuildPaneIDReusedAtOtherPathIsDead(t *testing.T) {
	// %1 is reported live but sitting at an unrelated path (a reused id after a
	// tmux server restart). The recorded worktree is /wt/%1, so it must be dead.
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		LivePanes: func() (map[string]LivePaneInfo, error) {
			return map[string]LivePaneInfo{"%1": {Path: "/some/other/dir"}}, nil
		},
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
	}
	snap := Build("o/n", "/root", c)
	if snap.Sessions[0].Panes[0].Alive {
		t.Fatal("a reused pane id at an unrelated path must be dead, not alive")
	}
}

func TestBuildAliveWhenPaneInWorktreeSubdir(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")), // worktree /wt/%1
		LivePanes: func() (map[string]LivePaneInfo, error) {
			return map[string]LivePaneInfo{"%1": {Path: "/wt/%1/sub/dir"}}, nil
		},
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
	}
	snap := Build("o/n", "/root", c)
	if !snap.Sessions[0].Panes[0].Alive {
		t.Fatal("a pane cd'd into a worktree subdir should still be alive")
	}
}

func TestBuildEmptyStateYieldsEmptySnapshot(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: func() (state.Store, error) { return state.Store{SchemaVersion: 1, Panes: []state.Pane{}}, nil },
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
	}
	snap := Build("o/n", "/root", c)
	if len(snap.Sessions) != 0 {
		t.Fatalf("want 0 sessions, got %d", len(snap.Sessions))
	}
	if snap.Rollup.Total != 0 || snap.Rollup.AllMerged {
		t.Fatalf("rollup = %+v want zero", snap.Rollup)
	}
	if snap.Sessions == nil {
		t.Fatal("Sessions must be non-nil empty slice for JSON stability")
	}
}

func TestBuildLoadStateErrorReturnsReason(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: func() (state.Store, error) { return state.Store{}, errors.New("bad json") },
	}
	snap := Build("o/n", "/root", c)
	if snap.Degraded.Reason == "" {
		t.Fatal("load-state error should set Degraded.Reason")
	}
	if len(snap.Sessions) != 0 {
		t.Fatal("no sessions on load error")
	}
}

func TestBuildTmuxStateMatrix(t *testing.T) {
	noPane := pane("1", 2, "")
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(noPane, pane("1", 3, "%1"), pane("1", 4, "%2")),
		LivePanes: livePanesAt("%1"), // %2 is recorded but not live
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	panes := Build("o/n", "/root", c).Sessions[0].Panes
	if panes[0].TmuxState != "-" {
		t.Fatalf("no-pane TmuxState = %q want -", panes[0].TmuxState)
	}
	if panes[1].TmuxState != "live" {
		t.Fatalf("live TmuxState = %q want live", panes[1].TmuxState)
	}
	if panes[2].TmuxState != "stale" {
		t.Fatalf("dead TmuxState = %q want stale", panes[2].TmuxState)
	}

	c.LivePanes = func() (map[string]LivePaneInfo, error) { return nil, errors.New("tmux not found") }
	panes = Build("o/n", "/root", c).Sessions[0].Panes
	if panes[0].TmuxState != "-" {
		t.Fatalf("no-pane TmuxState under degraded tmux = %q want -", panes[0].TmuxState)
	}
	if panes[1].TmuxState != "unknown" || panes[2].TmuxState != "unknown" {
		t.Fatalf("degraded-tmux TmuxState = %q,%q want unknown,unknown", panes[1].TmuxState, panes[2].TmuxState)
	}
}

func TestBuildTmuxTitleOnlyWhenAlive(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1"), pane("1", 3, "%2")),
		LivePanes: func() (map[string]LivePaneInfo, error) {
			return map[string]LivePaneInfo{
				"%1": {Path: "/wt/%1", Title: "two: child"},
				"%2": {Path: "/somewhere/else", Title: "reused id"}, // path mismatch -> dead
			}, nil
		},
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:    wavesNone,
	}
	panes := Build("o/n", "/root", c).Sessions[0].Panes
	if panes[0].TmuxTitle != "two: child" {
		t.Fatalf("alive TmuxTitle = %q want %q", panes[0].TmuxTitle, "two: child")
	}
	if panes[1].Alive || panes[1].TmuxTitle != "" {
		t.Fatalf("dead pane must not carry a title: alive=%v title=%q", panes[1].Alive, panes[1].TmuxTitle)
	}
}

func TestNormalizeAgentState(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"running":    "running",
		"done":       "done",
		" running ":  "running", // 余分な空白は剥がす
		"claude":     "",        // ラッパー外の値は不明扱い
		"bash":       "",
		"x\ty":       "", // pane 内プロセスが偽装した文字列も不明扱い
		"RUNNING":    "", // 値はラッパーが設定する小文字リテラルのみ
		"done extra": "",
	}
	for raw, want := range cases {
		if got := normalizeAgentState(raw); got != want {
			t.Errorf("normalizeAgentState(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestBuildAgentStateFromLiveOption(t *testing.T) {
	dead := pane("1", 6, "%5")
	dead.AgentStatus = "running" // pane 死亡 + tmux 正常なら記録値は使われない
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1"), pane("1", 3, "%2"), pane("1", 4, "%3"), pane("1", 5, "%4"), dead),
		LivePanes: livePanesWith(map[string]LivePaneInfo{
			"%1": {Path: "/wt/%1", AgentState: "running"},
			"%2": {Path: "/wt/%2", AgentState: "done"},
			"%3": {Path: "/wt/%3", AgentState: "forged junk"}, // 偽装/未知の値は不明へ
			"%4": {Path: "/wt/%4"},                            // option 未設定の alive pane(旧版 fanout 起動など)
			// %5 は live set に居ない(pane 死亡)
		}),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:    wavesNone,
	}
	snap := Build("o/n", "/root", c)
	panes := snap.Sessions[0].Panes
	wants := []string{"running", "done", "", "", ""}
	for i, want := range wants {
		if panes[i].AgentState != want {
			t.Fatalf("#%d AgentState = %q, want %q", panes[i].IssueNum, panes[i].AgentState, want)
		}
	}
	if snap.Sessions[0].Rollup.Running != 1 {
		t.Fatalf("session Rollup.Running = %d, want 1", snap.Sessions[0].Rollup.Running)
	}
	if snap.Rollup.Running != 1 {
		t.Fatalf("snapshot Rollup.Running = %d, want 1", snap.Rollup.Running)
	}
}

func TestBuildAgentStateFallsBackToRecordedStatusWhenTmuxDegraded(t *testing.T) {
	recorded := pane("1", 2, "%1")
	recorded.AgentStatus = "running"
	unrecorded := pane("1", 3, "%2") // 旧 state 行: agentStatus 無し
	tampered := pane("1", 4, "%3")   // 手編集された state 行: 規定外の値は捨てる
	tampered.AgentStatus = "<b>maybe</b>"
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(recorded, unrecorded, tampered),
		LivePanes: func() (map[string]LivePaneInfo, error) { return nil, errors.New("tmux not found") },
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	snap := Build("o/n", "/root", c)
	panes := snap.Sessions[0].Panes
	if panes[0].AgentState != "running" {
		t.Fatalf("degraded-tmux AgentState = %q, want fallback to recorded \"running\"", panes[0].AgentState)
	}
	if panes[1].AgentState != "" {
		t.Fatalf("degraded-tmux AgentState without recorded status = %q, want empty", panes[1].AgentState)
	}
	if panes[2].AgentState != "" {
		t.Fatalf("degraded-tmux AgentState with tampered status = %q, want empty", panes[2].AgentState)
	}
	if snap.Rollup.Running != 1 {
		t.Fatalf("Rollup.Running = %d, want 1 (fallback row counts)", snap.Rollup.Running)
	}
}

func TestBuildPromptPassthrough(t *testing.T) {
	p := pane("1", 2, "%1")
	p.Prompt = "Implement #2 as designed"
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(p),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	got := Build("o/n", "/root", c).Sessions[0].Panes[0].Prompt
	if got != "Implement #2 as designed" {
		t.Fatalf("Prompt = %q want passthrough of the state row prompt", got)
	}
}

func TestBuildPlanModePassthrough(t *testing.T) {
	planPane := pane("1", 2, "%1")
	planPane.CodexPlanMode = true
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(planPane, pane("1", 3, "%2")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	panes := Build("o/n", "/root", c).Sessions[0].Panes
	if !panes[0].PlanMode {
		t.Fatal("PlanMode = false want passthrough of the state row CodexPlanMode")
	}
	if panes[1].PlanMode {
		t.Fatal("PlanMode = true for a non-plan row, want false")
	}
}

func TestBuildTaskIDPaneUsesBranchPRs(t *testing.T) {
	first := pane("plan:alpha", 0, "%1")
	first.TaskID = "task-b"
	first.BranchName = "fanout/task-branch"
	second := pane("plan:alpha", 0, "%2")
	second.TaskID = "task-a"
	second.BranchName = "fanout/task-branch"

	branchCalls := 0
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(first, second),
		LivePanes: livePanesAt(),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			t.Fatalf("IssuePRs(%d) called for task row", num)
			return "", nil, nil
		},
		BranchPRs: func(branch string) ([]ghissue.PRRef, error) {
			branchCalls++
			if branch != "fanout/task-branch" {
				t.Fatalf("BranchPRs branch = %q, want fanout/task-branch", branch)
			}
			return mergedPR(701), nil
		},
		Waves: wavesNone,
	}
	snap := Build("o/n", "/root", c)

	if branchCalls != 1 {
		t.Fatalf("BranchPRs calls = %d, want one cached call for the shared branch", branchCalls)
	}
	panes := snap.Sessions[0].Panes
	if len(panes) != 2 {
		t.Fatalf("panes = %+v, want two task panes", panes)
	}
	if panes[0].TaskID != "task-a" || panes[1].TaskID != "task-b" {
		t.Fatalf("task pane order = %q,%q want task-a,task-b", panes[0].TaskID, panes[1].TaskID)
	}
	for _, pv := range panes {
		if pv.IssueState != IssueStateUnknown || !pv.HasMergedPR || len(pv.PRs) != 1 {
			t.Fatalf("task pane should carry branch PR state and unknown issue state: %+v", pv)
		}
	}
	if snap.Sessions[0].Rollup.Total != 2 || snap.Sessions[0].Rollup.Merged != 2 || !snap.Sessions[0].Rollup.AllMerged {
		t.Fatalf("task rollup = %+v, want all merged", snap.Sessions[0].Rollup)
	}
	raw, err := json.Marshal(panes[0])
	if err != nil {
		t.Fatalf("marshal task pane: %v", err)
	}
	if !strings.Contains(string(raw), `"taskId":"task-a"`) {
		t.Fatalf("task pane JSON should include taskId: %s", raw)
	}
}

func TestBuildTaskIDBranchPRFailureDegradesGitHub(t *testing.T) {
	task := pane("plan:alpha", 0, "%1")
	task.TaskID = "task-a"
	task.BranchName = "fanout/task-a"
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(task),
		LivePanes: livePanesAt(),
		BranchPRs: func(branch string) ([]ghissue.PRRef, error) {
			return nil, errors.New("gh pr list failed")
		},
		Waves: wavesNone,
	}
	snap := Build("o/n", "/root", c)
	if !snap.Degraded.GitHub || !strings.Contains(snap.Degraded.Reason, "gh pr list failed") {
		t.Fatalf("branch PR failure should degrade GitHub, got %+v", snap.Degraded)
	}
	pv := snap.Sessions[0].Panes[0]
	if pv.IssueState != IssueStateUnknown || pv.PRs == nil || len(pv.PRs) != 0 {
		t.Fatalf("failed task pane = %+v, want UNKNOWN and non-nil empty PRs", pv)
	}
}

func TestBuildCIStatusFromPrimaryPR(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1"), pane("1", 3, "%2")),
		LivePanes: livePanesAt(),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			if num == 2 {
				return "OPEN", []ghissue.PRRef{{Number: 9, State: "OPEN", CIStatus: " PASS "}}, nil
			}
			return "OPEN", nil, nil // no PR
		},
		Waves: wavesNone,
	}
	panes := Build("o/n", "/root", c).Sessions[0].Panes
	if panes[0].CIStatus != "pass" {
		t.Fatalf("CIStatus = %q want lowercased-trimmed %q", panes[0].CIStatus, "pass")
	}
	if panes[1].CIStatus != "-" {
		t.Fatalf("CIStatus without PR = %q want -", panes[1].CIStatus)
	}
}

func TestBuildWavesMissIsNotDegraded(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	snap := Build("o/n", "/root", c)
	if snap.Degraded.GitHub {
		t.Fatal("waves cache miss must NOT mark GitHub degraded")
	}
	pv := snap.Sessions[0].Panes[0]
	if pv.Wave != 0 || pv.WaveLabel != "" || pv.Blocked {
		t.Fatalf("waves miss must leave zero-valued fields, got %+v", pv)
	}
	if pv.Blockers == nil || len(pv.Blockers) != 0 {
		t.Fatalf("Blockers = %#v want non-nil empty slice", pv.Blockers)
	}
}

func TestBuildWavesErrorDegradesGitHub(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     func(parent string) (WaveGraph, error) { return WaveGraph{}, errors.New("gh graph down") },
	}
	snap := Build("o/n", "/root", c)
	if !snap.Degraded.GitHub {
		t.Fatal("waves error should set Degraded.GitHub")
	}
	if !strings.Contains(snap.Degraded.Reason, "gh graph down") {
		t.Fatalf("Degraded.Reason = %q should carry the waves error", snap.Degraded.Reason)
	}
	if snap.Sessions[0].Panes[0].Blockers == nil {
		t.Fatal("Blockers must stay non-nil on waves error")
	}
}

func TestBuildWavesDataPopulatesPanesAndRollups(t *testing.T) {
	calls := 0
	info := map[int]WaveInfo{
		101: {Wave: 1, WaveLabel: "wave1", Blockers: []blockers.Status{}, Blocked: false},
		102: {
			Wave:      2,
			WaveLabel: "wave2",
			Blockers:  []blockers.Status{{Num: 101, State: "OPEN"}},
			Blocked:   true,
		},
	}
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("100", 101, "%1"), pane("100", 102, "%2")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesOf(info, &calls),
	}
	snap := Build("o/n", "/root", c)
	if calls != 1 {
		t.Fatalf("waves should be fetched once per distinct parent, got %d calls", calls)
	}
	panes := snap.Sessions[0].Panes
	if panes[0].Wave != 1 || panes[0].WaveLabel != "wave1" || panes[0].Blocked {
		t.Fatalf("#101 wave view = %+v", panes[0])
	}
	if panes[1].Wave != 2 || panes[1].WaveLabel != "wave2" || !panes[1].Blocked {
		t.Fatalf("#102 wave view = %+v", panes[1])
	}
	if len(panes[1].Blockers) != 1 || panes[1].Blockers[0] != (blockers.Status{Num: 101, State: "OPEN"}) {
		t.Fatalf("#102 blockers = %#v", panes[1].Blockers)
	}
	if snap.Sessions[0].Rollup.Blocked != 1 {
		t.Fatalf("session Rollup.Blocked = %d want 1", snap.Sessions[0].Rollup.Blocked)
	}
	if snap.Rollup.Blocked != 1 {
		t.Fatalf("snapshot Rollup.Blocked = %d want 1", snap.Rollup.Blocked)
	}
}

func TestBuildWaveLabelStateRowWinsOverGraph(t *testing.T) {
	withRow := pane("100", 101, "%1")
	withRow.Wave = "row-label"
	noRow := pane("100", 102, "%2")
	info := map[int]WaveInfo{
		101: {Wave: 1, WaveLabel: "graph-label"},
		102: {Wave: 1, WaveLabel: "graph-label"},
	}
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(withRow, noRow),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesOf(info, nil),
	}
	panes := Build("o/n", "/root", c).Sessions[0].Panes
	if panes[0].WaveLabel != "row-label" {
		t.Fatalf("WaveLabel = %q; the state row label must win over the graph label", panes[0].WaveLabel)
	}
	if panes[1].WaveLabel != "graph-label" {
		t.Fatalf("WaveLabel = %q; the graph label must fill in when the row has none", panes[1].WaveLabel)
	}
}

func TestBuildAppendsSyntheticPanesForUnrecordedChildren(t *testing.T) {
	graph := WaveGraph{
		Children: []ghissue.Issue{
			{Number: 104, State: "OPEN"}, // タイトル無し → "#104" fallback
			{Number: 101, Title: "Recorded", State: "OPEN"},
			{Number: 103, Title: "Queued child", State: "OPEN"},
			{Number: 105, Title: "Blocked child", State: "OPEN"},
		},
		Info: map[int]WaveInfo{
			101: {Wave: 1},
			103: {Wave: 2, WaveLabel: "wave2", Blockers: []blockers.Status{}},
			104: {Wave: 2},
			105: {Wave: 3, Blockers: []blockers.Status{{Num: 103, State: "OPEN"}}, Blocked: true},
		},
	}
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("100", 101, "%1")),
		LivePanes: livePanesAt("%1"),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			switch num {
			case 103:
				return "CLOSED", mergedPR(503), nil // PR キャッシュが状態と PR を知っている
			case 104:
				return "", nil, nil // キャッシュ未取得 → graph の State へ fallback
			default:
				return "OPEN", nil, nil
			}
		},
		Waves: wavesGraphOf(graph, nil),
	}
	snap := Build("o/n", "/root", c)
	panes := snap.Sessions[0].Panes
	if len(panes) != 4 {
		t.Fatalf("want 1 recorded + 3 synthetic panes, got %+v", panes)
	}
	if panes[0].IssueNum != 101 || panes[0].NotStarted {
		t.Fatalf("recorded pane must come first and not be synthetic: %+v", panes[0])
	}
	// synthetic 行は記録行の後に issue 番号昇順で並ぶ
	if panes[1].IssueNum != 103 || panes[2].IssueNum != 104 || panes[3].IssueNum != 105 {
		t.Fatalf("synthetic order = %d,%d,%d want 103,104,105",
			panes[1].IssueNum, panes[2].IssueNum, panes[3].IssueNum)
	}
	queued := panes[1]
	if !queued.NotStarted || queued.Alive || queued.PaneID != "" || queued.Agent != "" || queued.BranchName != "" {
		t.Fatalf("synthetic pane must carry zero pane fields: %+v", queued)
	}
	if queued.DisplayName != "Queued child" {
		t.Fatalf("DisplayName = %q want the issue title", queued.DisplayName)
	}
	if queued.IssueState != "CLOSED" || !queued.HasMergedPR || queued.TmuxState != "closed" {
		t.Fatalf("PR-cache-backed synthetic pane = %+v", queued)
	}
	if queued.DiffSummary != "-" || queued.DirtyState != "-" {
		t.Fatalf("synthetic diff/dirty = %q/%q want -/-", queued.DiffSummary, queued.DirtyState)
	}
	if queued.Wave != 2 || queued.WaveLabel != "wave2" {
		t.Fatalf("synthetic wave fields = %+v", queued)
	}
	noTitle := panes[2]
	if noTitle.DisplayName != "#104" {
		t.Fatalf("DisplayName = %q want #104 fallback", noTitle.DisplayName)
	}
	if noTitle.IssueState != "OPEN" || noTitle.TmuxState != "queued" {
		t.Fatalf("PR cache miss must fall back to the graph issue state: %+v", noTitle)
	}
	blocked := panes[3]
	if blocked.TmuxState != "deferred" || !blocked.Blocked || len(blocked.Blockers) != 1 {
		t.Fatalf("blocked synthetic pane = %+v", blocked)
	}
	// rollup: OPEN/不明の synthetic 行は Total/Pending/Blocked と NotStarted に
	// 入る。merged PR 持ちで CLOSED の #103 は Total/Merged に入る(pane なしで
	// 完了した作業)が NotStarted には数えない。
	r := snap.Sessions[0].Rollup
	if r.Total != 4 || r.NotStarted != 2 || r.Merged != 1 || r.Pending != 3 || r.Blocked != 1 {
		t.Fatalf("session rollup = %+v", r)
	}
	if snap.Rollup.NotStarted != 2 || snap.Rollup.Total != 4 {
		t.Fatalf("snapshot rollup = %+v", snap.Rollup)
	}
}

// CLOSED のまま起動されなかった synthetic 子が rollup から除外され、全 merge
// 済みセッションの AllMerged を妨げないことを単体で固定する。
func TestBuildClosedSyntheticChildDoesNotBlockAllMerged(t *testing.T) {
	graph := WaveGraph{
		Children: []ghissue.Issue{
			{Number: 101, Title: "merged", State: "CLOSED"},
			{Number: 103, Title: "not planned", State: "CLOSED"},
		},
		Info: map[int]WaveInfo{},
	}
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("100", 101, "%1")),
		LivePanes: livePanesAt(),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			if num == 101 {
				return "CLOSED", mergedPR(501), nil
			}
			return "CLOSED", nil, nil // PR なしで閉じられた子
		},
		Waves: wavesGraphOf(graph, nil),
	}
	snap := Build("o/n", "/root", c)
	panes := snap.Sessions[0].Panes
	if len(panes) != 2 || panes[1].TmuxState != "closed" {
		t.Fatalf("closed synthetic row must still render: %+v", panes)
	}
	r := snap.Sessions[0].Rollup
	if r.Total != 1 || !r.AllMerged || r.Pending != 0 || r.NotStarted != 0 {
		t.Fatalf("closed synthetic child must not block AllMerged: %+v", r)
	}
}

// PR lookup が失敗(IssuePRs エラー)した CLOSED 子は PR 状態が未確認なので
// rollup に算入し、一時的な gh 失敗で session が誤って AllMerged に見えるのを
// 防ぐ。
func TestBuildUnconfirmedClosedSyntheticChildStaysInRollup(t *testing.T) {
	graph := WaveGraph{
		Children: []ghissue.Issue{
			{Number: 101, Title: "merged", State: "CLOSED"},
			{Number: 103, Title: "closed but PR unknown", State: "CLOSED"},
		},
		Info: map[int]WaveInfo{},
	}
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("100", 101, "%1")),
		LivePanes: livePanesAt(),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			if num == 101 {
				return "CLOSED", mergedPR(501), nil
			}
			// #103 は PR lookup 失敗(レート制限等)。状態は wave graph の
			// CLOSED に fallback するが、merged PR の有無は未確認。
			return "", nil, errors.New("rate limited")
		},
		Waves: wavesGraphOf(graph, nil),
	}
	snap := Build("o/n", "/root", c)
	panes := snap.Sessions[0].Panes
	if len(panes) != 2 || panes[1].IssueState != "CLOSED" {
		t.Fatalf("unconfirmed closed row must still render as CLOSED: %+v", panes)
	}
	r := snap.Sessions[0].Rollup
	// #103 は算入される → Total 2 / Pending 1 / AllMerged false(完了扱いしない)
	if r.Total != 2 || r.AllMerged || r.Pending != 1 {
		t.Fatalf("unconfirmed closed child must stay in rollup: %+v", r)
	}
}

func TestBuildWaveCacheMissEmitsNoSyntheticRows(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("100", 101, "%1")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	snap := Build("o/n", "/root", c)
	if len(snap.Sessions[0].Panes) != 1 {
		t.Fatalf("a wave cache miss must not invent synthetic rows: %+v", snap.Sessions[0].Panes)
	}
	if snap.Rollup.NotStarted != 0 {
		t.Fatalf("Rollup.NotStarted = %d want 0", snap.Rollup.NotStarted)
	}
}

func TestBuildSyntheticEmittedOncePerAliasedParent(t *testing.T) {
	// "0100" と "100" は normalize すると同一親で、同じ wave graph を共有する。
	// 未記録の #103 はどちらか一方の session にだけ現れ、記録済みの #101/#102
	// はエイリアス横断で「記録済み」と判定されて synthetic にならない。
	graph := WaveGraph{
		Children: []ghissue.Issue{
			{Number: 101, State: "OPEN"},
			{Number: 102, State: "OPEN"},
			{Number: 103, Title: "Queued", State: "OPEN"},
		},
		Info: map[int]WaveInfo{101: {Wave: 1}, 102: {Wave: 1}, 103: {Wave: 2}},
	}
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("0100", 101, "%1"), pane("100", 102, "%2")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesGraphOf(graph, nil),
	}
	snap := Build("o/n", "/root", c)
	if len(snap.Sessions) != 2 {
		t.Fatalf("want 2 alias sessions, got %d", len(snap.Sessions))
	}
	total := 0
	var synthetic []int
	for _, sess := range snap.Sessions {
		total += len(sess.Panes)
		for _, pv := range sess.Panes {
			if pv.NotStarted {
				synthetic = append(synthetic, pv.IssueNum)
			}
		}
	}
	if total != 3 {
		t.Fatalf("total panes = %d want 3 (2 recorded + 1 synthetic)", total)
	}
	if len(synthetic) != 1 || synthetic[0] != 103 {
		t.Fatalf("synthetic nums = %v want [103] emitted exactly once", synthetic)
	}
	if snap.Rollup.NotStarted != 1 {
		t.Fatalf("Rollup.NotStarted = %d want 1", snap.Rollup.NotStarted)
	}
}

func TestSyntheticTmuxStateMatchesTUIStrings(t *testing.T) {
	cases := []struct {
		issueState string
		blocked    bool
		want       string
	}{
		{"CLOSED", false, "closed"},
		{"closed", true, "closed"}, // closed は blocked より優先(TUI と同順)
		{"OPEN", true, "deferred"},
		{"OPEN", false, "queued"},
		{"open", false, "queued"},
		{IssueStateUnknown, false, "unknown"},
		{"", false, "unknown"},
	}
	for _, tc := range cases {
		if got := SyntheticTmuxState(tc.issueState, tc.blocked); got != tc.want {
			t.Errorf("SyntheticTmuxState(%q, %v) = %q, want %q", tc.issueState, tc.blocked, got, tc.want)
		}
	}
}

func TestBuildBlockersMarshalAsEmptyArray(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		LivePanes: livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	pv := Build("o/n", "/root", c).Sessions[0].Panes[0]
	if pv.Blockers == nil {
		t.Fatal("Blockers must be non-nil for JSON stability")
	}
	raw, err := json.Marshal(pv)
	if err != nil {
		t.Fatalf("marshal pane view: %v", err)
	}
	if !strings.Contains(string(raw), `"blockers":[]`) {
		t.Fatalf("pane view JSON should contain \"blockers\":[] — got %s", raw)
	}
	if strings.Contains(string(raw), `"taskId"`) {
		t.Fatalf("issue pane JSON should omit empty taskId for wire compatibility — got %s", raw)
	}
}
