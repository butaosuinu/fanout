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

// wavesNone is a Waves collector that always reports a cache miss (not fetched
// yet) — the non-degraded GitHub baseline for tests not exercising waves.
func wavesNone(parent string) (map[int]WaveInfo, error) {
	return nil, nil
}

// wavesOf builds a Waves collector serving the same WaveInfo map for any
// parent and counting calls through calls (when non-nil).
func wavesOf(info map[int]WaveInfo, calls *int) func(string) (map[int]WaveInfo, error) {
	return func(parent string) (map[int]WaveInfo, error) {
		if calls != nil {
			*calls++
		}
		return info, nil
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
		Waves:     func(parent string) (map[int]WaveInfo, error) { return nil, errors.New("gh graph down") },
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
}
