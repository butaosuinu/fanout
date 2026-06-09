package sessionview

import (
	"errors"
	"testing"
	"time"

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
func livePanesAt(paneIDs ...string) func() (map[string]string, error) {
	return func() (map[string]string, error) {
		m := map[string]string{}
		for _, id := range paneIDs {
			m[id] = "/wt/" + id
		}
		return m, nil
	}
}

func mergedPR(n int) []ghissue.PRRef {
	at := "2026-06-05T00:00:00Z"
	return []ghissue.PRRef{{Number: n, State: "MERGED", MergedAt: &at}}
}

func TestBuildGroupsByParentSortedAndComputesRollups(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("100", 102, "%2"), pane("100", 101, "%1"), pane("90", 91, "%9")),
		LivePanes: livePanesAt("%1", "%9"),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			if num == 101 {
				return "CLOSED", mergedPR(501), nil
			}
			return "OPEN", []ghissue.PRRef{}, nil
		},
	}
	snap := Build("owner/name", "/root", c)

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
		LivePanes: func() (map[string]string, error) { return nil, errors.New("tmux not found") },
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
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
	}
	snap := Build("o/n", "/root", c)
	if snap.Degraded.GitHub {
		t.Fatal("cache miss must NOT mark GitHub degraded")
	}
	if snap.Sessions[0].Panes[0].IssueState != IssueStateUnknown {
		t.Fatalf("issue state = %q want UNKNOWN", snap.Sessions[0].Panes[0].IssueState)
	}
}

func TestBuildPaneIDReusedAtOtherPathIsDead(t *testing.T) {
	// %1 is reported live but sitting at an unrelated path (a reused id after a
	// tmux server restart). The recorded worktree is /wt/%1, so it must be dead.
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		LivePanes: func() (map[string]string, error) { return map[string]string{"%1": "/some/other/dir"}, nil },
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
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
		LivePanes: func() (map[string]string, error) { return map[string]string{"%1": "/wt/%1/sub/dir"}, nil },
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
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
