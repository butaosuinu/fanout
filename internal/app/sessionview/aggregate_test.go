package sessionview

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/blockers"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
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

func herdrPane(parent string, num int, paneID string) state.Pane {
	p := pane(parent, num, paneID)
	p.Agent = "codex"
	p.Backend = backend.Herdr
	p.HerdrWorkspaceID = "workspace-a"
	p.HerdrWorkspaceLabel = "owned-label-a"
	p.HerdrTerminalID = "terminal-a"
	p.HerdrRepoKey = "/repo/.git"
	p.HerdrAgentID = "agent-a"
	p.HerdrAgentSession = &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-value-" + paneID,
	}
	p.HerdrSession = "session-a"
	p.HerdrSocketPath = "/tmp/herdr-a.sock"
	p.WorktreePath = "/repo/.fanout/worktrees/child"
	return p
}

func liveHerdrPane(p state.Pane) backend.LivePane {
	var agentSession *backend.AgentSessionRef
	if p.HerdrAgentSession != nil {
		cloned := *p.HerdrAgentSession
		agentSession = &cloned
	}
	return backend.LivePane{
		Ref: backend.PaneRef{
			Backend:   backend.Herdr,
			Workspace: p.HerdrWorkspaceID,
			Pane:      p.PaneID,
		},
		CurrentPath:      "/unrelated/saved-cwd",
		Title:            "herdr child",
		AgentState:       backend.AgentWorking,
		NativeAgentState: "working",
		WorkspaceLabel:   p.HerdrWorkspaceLabel,
		TerminalID:       p.HerdrTerminalID,
		AgentID:          p.HerdrAgentID,
		AgentProvider:    p.Agent,
		AgentSession:     agentSession,
		AgentPresent:     true,
		Focused:          true,
		RepoKey:          p.HerdrRepoKey,
		ProjectRoot:      "/repo",
		WorktreePath:     p.WorktreePath,
		SessionID:        p.HerdrSession,
		SocketPath:       p.HerdrSocketPath,
	}
}

func buildWithLivePanes(rows []state.Pane, live []backend.LivePane, liveErr error) Snapshot {
	return Build("o/n", "/repo", Collectors{
		Now:       fixedNow,
		LoadState: storeOf(rows...),
		ListLive:  func() ([]backend.LivePane, error) { return live, liveErr },
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	})
}

type LivePaneInfo struct {
	Path         string
	WorktreePath string
	Title        string
	AgentState   string
	ShellKey     string
}

// livePanesAt builds a ListLive collector marking the given panes live and
// mapping each pane id to its recorded worktree path.
func livePanesAt(paneIDs ...string) func() ([]backend.LivePane, error) {
	panes := make(map[string]LivePaneInfo, len(paneIDs))
	for _, id := range paneIDs {
		panes[id] = LivePaneInfo{Path: "/wt/" + id}
	}
	return livePanesWith(panes)
}

// livePanesWith maps compact test fixtures to backend-neutral observations.
func livePanesWith(m map[string]LivePaneInfo) func() ([]backend.LivePane, error) {
	return func() ([]backend.LivePane, error) {
		panes := make([]backend.LivePane, 0, len(m))
		for id, pane := range m {
			state, _ := backend.ParseAgentState(pane.AgentState)
			panes = append(panes, backend.LivePane{
				Ref:          backend.PaneRef{Backend: backend.Tmux, Pane: id},
				CurrentPath:  pane.Path,
				WorktreePath: pane.WorktreePath,
				Title:        pane.Title,
				AgentState:   state,
				ShellKey:     pane.ShellKey,
			})
		}
		return panes, nil
	}
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
		ListLive:  livePanesAt("%1", "%9"),
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
	if p100.Panes[0].BaseBranch != "main" || p100.Panes[1].BaseBranch != "" {
		t.Fatalf("pane base branches = %q,%q want main and legacy empty", p100.Panes[0].BaseBranch, p100.Panes[1].BaseBranch)
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

func TestBuildShellPaneCountsLiveButNotProgressRollup(t *testing.T) {
	agentPane := pane("100", 101, "%1")
	shellPane := state.Pane{
		Parent:       "@manual",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		Slug:         "terminal-root-1",
		PaneID:       "%2",
		ShellKey:     "shell-root",
		Agent:        "shell",
		DisplayName:  "root terminal",
		WorktreePath: "/repo",
		CreatedAt:    "2026-06-04T00:00:00Z",
	}
	snap := Build("owner/name", "/repo", Collectors{
		Now:       fixedNow,
		LoadState: storeOf(agentPane, shellPane),
		ListLive: livePanesWith(map[string]LivePaneInfo{
			"%1": {Path: "/wt/%1"},
			"%2": {Path: "/repo", Title: "root terminal", ShellKey: "shell-root"},
		}),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			return "OPEN", nil, nil
		},
		Waves: wavesNone,
		WorktreeStat: func(path, baseRef string) (WorktreeStat, error) {
			return WorktreeStat{DiffSummary: "+0/-0", DirtyState: "clean"}, nil
		},
	})

	if snap.Rollup.Total != 1 || snap.Rollup.Pending != 1 || snap.Rollup.Live != 2 {
		t.Fatalf("repo rollup = %+v, want progress total 1 and live 2", snap.Rollup)
	}
	var shell PaneView
	for _, session := range snap.Sessions {
		for _, pane := range session.Panes {
			if pane.Kind == state.PaneKindShell {
				shell = pane
			}
		}
	}
	if shell.Kind != state.PaneKindShell || shell.ShellKey != "shell-root" || !shell.Alive || shell.Derived.Name != "root terminal" {
		t.Fatalf("shell pane = %+v, want live shell row", shell)
	}
}

func TestBuildAttachedAgentCountsLiveButNotProgressRollup(t *testing.T) {
	agentPane := pane("100", 101, "%1")
	attachedPane := state.Pane{
		Parent:         "100",
		IssueNum:       -1,
		Kind:           state.PaneKindAttachedAgent,
		Slug:           "child-codex-a1",
		BranchName:     "fanout/child-101",
		PaneID:         "%2",
		SourceParent:   "100",
		SourceIssueNum: 101,
		Agent:          "codex",
		DisplayName:    "codex for #101",
		WorktreePath:   "/wt/%1",
		CreatedAt:      "2026-06-04T00:00:00Z",
	}
	snap := Build("owner/name", "/repo", Collectors{
		Now:       fixedNow,
		LoadState: storeOf(agentPane, attachedPane),
		ListLive: livePanesWith(map[string]LivePaneInfo{
			"%1": {Path: "/wt/%1"},
			"%2": {Path: "/wt/%1/subdir", Title: "codex"},
		}),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			return "OPEN", nil, nil
		},
		Waves: wavesNone,
		WorktreeStat: func(path, baseRef string) (WorktreeStat, error) {
			return WorktreeStat{DiffSummary: "+0/-0", DirtyState: "clean"}, nil
		},
	})

	if snap.Rollup.Total != 1 || snap.Rollup.Pending != 1 || snap.Rollup.Live != 2 {
		t.Fatalf("repo rollup = %+v, want progress total 1 and live 2", snap.Rollup)
	}
	var attached PaneView
	for _, session := range snap.Sessions {
		for _, pane := range session.Panes {
			if pane.Kind == state.PaneKindAttachedAgent {
				attached = pane
			}
		}
	}
	if attached.Kind != state.PaneKindAttachedAgent || attached.SourceIssueNum != 101 || !attached.Alive || attached.Derived.Name != "codex for #101" {
		t.Fatalf("attached pane = %+v, want live attached-agent row", attached)
	}
}

func TestBuildShellPaneRequiresShellKeyForLiveness(t *testing.T) {
	shellPane := state.Pane{
		Parent:       "@manual",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		Slug:         "terminal-root-1",
		PaneID:       "%2",
		ShellKey:     "shell-root",
		Agent:        "shell",
		DisplayName:  "root terminal",
		WorktreePath: "/repo",
		CreatedAt:    "2026-06-04T00:00:00Z",
	}
	snap := Build("owner/name", "/repo", Collectors{
		Now:       fixedNow,
		LoadState: storeOf(shellPane),
		ListLive: livePanesWith(map[string]LivePaneInfo{
			"%2": {Path: "/repo/subdir", Title: "reused id", ShellKey: "other-shell"},
		}),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			return "OPEN", nil, nil
		},
		Waves: wavesNone,
		WorktreeStat: func(path, baseRef string) (WorktreeStat, error) {
			return WorktreeStat{DiffSummary: "+0/-0", DirtyState: "clean"}, nil
		},
	})

	got := snap.Sessions[0].Panes[0]
	if got.Alive || got.TmuxState != "stale" {
		t.Fatalf("shell pane alive=%v tmux=%q, want stale when shell key differs", got.Alive, got.TmuxState)
	}
}

// TestBuildCoordinatorPaneRequiresShellKeyForLiveness pins the plan fan-out
// coordinator's liveness: an attached-agent row recorded with the repo root as
// WorktreePath matches on @fanout_shell_key, so a reused pane id — which
// always runs somewhere under the repo root — reads as stale, not live.
func TestBuildCoordinatorPaneRequiresShellKeyForLiveness(t *testing.T) {
	coordinator := state.Pane{
		Parent:       "@manual",
		IssueNum:     -1,
		Kind:         state.PaneKindAttachedAgent,
		Slug:         "plan-prompt-1",
		PaneID:       "%2",
		ShellKey:     "shell-coordinator",
		Agent:        "claude",
		DisplayName:  "plan: build search",
		WorktreePath: "/repo",
		CreatedAt:    "2026-06-04T00:00:00Z",
	}
	tests := []struct {
		name      string
		live      map[string]LivePaneInfo
		wantAlive bool
		wantTmux  string
	}{
		{
			name:      "matching key is live",
			live:      map[string]LivePaneInfo{"%2": {Path: "/repo", Title: "plan: build search", ShellKey: "shell-coordinator"}},
			wantAlive: true,
			wantTmux:  "live",
		},
		{
			name:      "reused pane id under the repo root is stale",
			live:      map[string]LivePaneInfo{"%2": {Path: "/repo/.fanout/worktrees/other", Title: "reused id"}},
			wantAlive: false,
			wantTmux:  "stale",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := Build("owner/name", "/repo", Collectors{
				Now:       fixedNow,
				LoadState: storeOf(coordinator),
				ListLive:  livePanesWith(tt.live),
				IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
					return "OPEN", nil, nil
				},
				Waves: wavesNone,
				WorktreeStat: func(path, baseRef string) (WorktreeStat, error) {
					return WorktreeStat{DiffSummary: "+0/-0", DirtyState: "clean"}, nil
				},
			})
			got := snap.Sessions[0].Panes[0]
			if got.Alive != tt.wantAlive || got.TmuxState != tt.wantTmux {
				t.Fatalf("Build() coordinator alive=%v tmux=%q, want alive=%v tmux=%q", got.Alive, got.TmuxState, tt.wantAlive, tt.wantTmux)
			}
		})
	}
}

func TestBuildAddsDerivedDisplayFilterAndSortFields(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(state.Pane{Parent: "100", IssueNum: 101, Slug: "child", DisplayName: "Child work", PaneID: "%1", Agent: "codex", BranchName: "feat/child", WorktreePath: "/repo/.fanout/worktrees/child"}),
		ListLive:  livePanesWith(map[string]LivePaneInfo{"%1": {Path: "/repo/.fanout/worktrees/child", Title: "child title", AgentState: "running"}}),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			return "OPEN", []ghissue.PRRef{{Number: 501, State: "OPEN", CIStatus: "fail", ReviewDecision: "CHANGES_REQUESTED"}}, nil
		},
		Waves: wavesOf(map[int]WaveInfo{
			101: {Wave: 2, WaveLabel: "wave2", Blocked: true, Blockers: []blockers.Status{{Num: 99, State: "OPEN"}}},
		}, nil),
		WorktreeStat: func(path, baseRef string) (WorktreeStat, error) {
			return WorktreeStat{DiffSummary: "+12/-3", DirtyState: "dirty"}, nil
		},
	}

	pv := Build("o/n", "/repo", c).Sessions[0].Panes[0]
	d := pv.Derived
	if d.Name != "Child work" || d.PRSummary != "#501 changes-requested" || d.CI != "fail" {
		t.Fatalf("derived display = %+v", d)
	}
	if d.WaveBadge != "W2 blocked" || d.WaveText != "wave2 W2 blocked" || d.BlockersText != "OPEN #99" {
		t.Fatalf("derived wave/blockers = %+v", d)
	}
	if d.DiffTotal != 15 || !d.DiffParsed || d.Sort.Diff != 15 || d.Sort.CI != 0 {
		t.Fatalf("derived sort/diff = %+v", d)
	}
	if d.FilterValues["backend"] != "tmux" || d.FilterValues["run"] != "running" || d.FilterValues["dirty"] != "yes" || d.FilterValues["pr"] != "open" {
		t.Fatalf("derived filter values = %+v", d.FilterValues)
	}
	// pr: はライフサイクル状態、review: はレビュー状態 — 直交する 2 軸
	if d.FilterValues["review"] != "changes-requested" {
		t.Fatalf("derived review filter value = %q, want %q", d.FilterValues["review"], "changes-requested")
	}
	if !strings.Contains(d.FilterText, "tmux") || !strings.Contains(d.FilterText, "child title") || !strings.Contains(d.FilterText, "+12/-3") {
		t.Fatalf("derived filter text = %q", d.FilterText)
	}
	if d.WorktreeRelative != ".fanout/worktrees/child" || !d.CanFocus || !d.CanPeek {
		t.Fatalf("derived path/focus = %+v", d)
	}
}

// TestConflictFilterText pins that the rendered `conflict` badge is reachable
// by free-text search, like every other visible tag.
func TestConflictFilterText(t *testing.T) {
	for _, tt := range []struct {
		name string
		prs  []ghissue.PRRef
		want string
	}{
		{name: "conflicting primary", prs: []ghissue.PRRef{{Number: 1, State: "OPEN", Mergeable: "CONFLICTING"}}, want: "conflict"},
		{name: "mergeable primary", prs: []ghissue.PRRef{{Number: 1, State: "OPEN", Mergeable: "MERGEABLE"}}, want: ""},
		// merged PRs always report UNKNOWN, which normalizes to ""
		{name: "merged primary", prs: []ghissue.PRRef{{Number: 1, State: "MERGED"}}, want: ""},
		{name: "no prs", prs: nil, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := conflictFilterText(tt.prs); got != tt.want {
				t.Fatalf("conflictFilterText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReviewFilterValue pins that `review:` stays orthogonal to `pr:`: it
// reports the decision even after the PR merges, where DisplayState would have
// collapsed to "merged".
func TestReviewFilterValue(t *testing.T) {
	for _, tt := range []struct {
		name string
		prs  []ghissue.PRRef
		want string
	}{
		{name: "no prs", prs: nil, want: "none"},
		{name: "approved", prs: []ghissue.PRRef{{Number: 1, State: "OPEN", ReviewDecision: "APPROVED"}}, want: "approved"},
		{name: "hyphenates changes requested", prs: []ghissue.PRRef{{Number: 1, State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}}, want: "changes-requested"},
		{name: "empty decision is none", prs: []ghissue.PRRef{{Number: 1, State: "OPEN"}}, want: "none"},
		{name: "merged pr keeps its decision", prs: []ghissue.PRRef{{Number: 1, State: "MERGED", ReviewDecision: "APPROVED"}}, want: "approved"},
		{
			name: "reads the primary pr, not the first",
			prs: []ghissue.PRRef{
				{Number: 1, State: "OPEN", ReviewDecision: "REVIEW_REQUIRED"},
				{Number: 2, State: "MERGED", ReviewDecision: "APPROVED"},
			},
			want: "approved",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := reviewFilterValue(tt.prs); got != tt.want {
				t.Fatalf("reviewFilterValue() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildPerSessionAllMerged(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("7", 8, "%1")),
		ListLive:  livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "CLOSED", mergedPR(9), nil },
	}
	snap := Build("o/n", "/root", c)
	if !snap.Sessions[0].Rollup.AllMerged || !snap.Rollup.AllMerged {
		t.Fatalf("AllMerged should be true: session=%+v repo=%+v", snap.Sessions[0].Rollup, snap.Rollup)
	}
}

func TestBuildCopiesSourceProjectRootToPaneView(t *testing.T) {
	tagged := pane("7", 8, "%1")
	tagged.SourceProjectRoot = "/other/worktree"
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(tagged),
		ListLive:  livePanesAt("%1"),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
	}
	snap := Build("o/n", "/root", c)
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Panes) != 1 {
		t.Fatalf("sessions = %+v", snap.Sessions)
	}
	if got := snap.Sessions[0].Panes[0].SourceProjectRoot; got != "/other/worktree" {
		t.Fatalf("SourceProjectRoot = %q, want /other/worktree", got)
	}
}

func TestBuildCarriesTaskIDRowsWithoutIssueFetch(t *testing.T) {
	issueCalls := 0
	waveCalls := 0
	c := Collectors{
		Now: fixedNow,
		LoadState: storeOf(
			state.Pane{Parent: "plan:launch-plan", IssueNum: 0, TaskID: "api-client", Slug: "launch-plan-api-client", PaneID: "%2", Agent: "claude", DisplayName: "API client", WorktreePath: "/wt/%2"},
			state.Pane{Parent: "plan:launch-plan", IssueNum: 0, TaskID: "base-types", Slug: "launch-plan-base-types", PaneID: "%1", Agent: "claude", DisplayName: "Base types", WorktreePath: "/wt/%1"},
		),
		ListLive: livePanesAt("%1", "%2"),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			issueCalls++
			return "OPEN", nil, nil
		},
		Waves: func(parent string) (WaveGraph, error) {
			waveCalls++
			return WaveGraph{}, nil
		},
	}

	snap := Build("owner/name", "/root", c)

	if issueCalls != 0 || waveCalls != 0 {
		t.Fatalf("task rows should not fetch issue/wave data, got issue=%d wave=%d", issueCalls, waveCalls)
	}
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Panes) != 2 {
		t.Fatalf("sessions = %+v", snap.Sessions)
	}
	if got := []string{snap.Sessions[0].Panes[0].TaskID, snap.Sessions[0].Panes[1].TaskID}; got[0] != "api-client" || got[1] != "base-types" {
		t.Fatalf("task ids = %v", got)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"taskId":"api-client"`) {
		t.Fatalf("snapshot JSON missing taskId: %s", data)
	}
}

func TestBuildDegradesWhenTmuxFails(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		ListLive:  func() ([]backend.LivePane, error) { return nil, errors.New("tmux not found") },
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

func TestBuildUsesPartialRuntimeSnapshotWhenCollectorDegrades(t *testing.T) {
	recordedLive := pane("1", 2, "%1")
	recordedUnknown := pane("1", 3, "%2")
	live := livePanesAt("%1")
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(recordedLive, recordedUnknown),
		ListLive: func() ([]backend.LivePane, error) {
			panes, err := live()
			return panes, errors.Join(err, errors.New("herdr session offline"))
		},
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:    wavesNone,
	}
	snap := Build("o/n", "/root", c)
	if !snap.Degraded.Tmux || !snap.Degraded.Runtime || !strings.Contains(snap.Degraded.Reason, "herdr session offline") {
		t.Fatalf("degraded = %+v, want partial runtime error", snap.Degraded)
	}
	if !snap.Sessions[0].Panes[0].Alive {
		t.Fatal("successful tmux observation should remain live in a degraded mixed snapshot")
	}
	if got := snap.Sessions[0].Panes[0].TmuxState; got != "live" {
		t.Fatalf("tmux state = %q, want live for successful observation in degraded mixed snapshot", got)
	}
	if snap.Sessions[0].Panes[1].Alive {
		t.Fatal("unobserved pane should not be live in a degraded mixed snapshot")
	}
	if got := snap.Sessions[0].Panes[1].TmuxState; got != "unknown" {
		t.Fatalf("unobserved tmux state = %q, want unknown in degraded mixed snapshot", got)
	}
	if got := snap.Sessions[0].Panes[1].RuntimeState; got != "unknown" {
		t.Fatalf("unobserved runtime state = %q, want unknown in globally degraded snapshot", got)
	}
}

func TestBuildScopesMixedBackendDegradationToFailedRoute(t *testing.T) {
	tmuxLive := pane("1", 2, "%1")
	tmuxMissing := pane("1", 3, "%2")
	herdrLive := herdrPane("1", 4, "workspace-a:p1")
	herdrMissing := herdrPane("1", 5, "workspace-a:p2")
	herdrMissing.AgentStatus = "running"
	herdrFailed := herdrPane("1", 6, "workspace-b:p1")
	herdrFailed.HerdrWorkspaceID = "workspace-b"
	herdrFailed.HerdrSession = "session-b"
	herdrFailed.HerdrSocketPath = "/tmp/herdr-b.sock"
	herdrFailed.HerdrTerminalID = "terminal-b"
	herdrFailed.HerdrAgentID = "agent-b"
	herdrFailed.WorktreePath = "/repo/.fanout/worktrees/failed"
	herdrFailed.AgentStatus = "running"

	tmuxObservation := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Tmux, Pane: tmuxLive.PaneID},
		CurrentPath: tmuxLive.WorktreePath,
		Title:       "tmux child",
	}
	herdrObservation := liveHerdrPane(herdrLive)
	routeErr := backend.ObservationRouteUnavailable(backend.ObservationRoute{
		Backend:    backend.Herdr,
		SessionID:  herdrFailed.HerdrSession,
		SocketPath: herdrFailed.HerdrSocketPath,
	}, errors.New("herdr session-b offline"))

	snap := buildWithLivePanes(
		[]state.Pane{tmuxLive, tmuxMissing, herdrLive, herdrMissing, herdrFailed},
		[]backend.LivePane{tmuxObservation, herdrObservation},
		routeErr,
	)
	if !snap.Degraded.Tmux || !snap.Degraded.Runtime || !strings.Contains(snap.Degraded.Reason, "session-b offline") {
		t.Fatalf("degraded = %+v, want compatible tmux flag plus runtime route failure", snap.Degraded)
	}

	panes := snap.Sessions[0].Panes
	wants := []struct {
		issue int
		alive bool
		state string
	}{
		{issue: 2, alive: true, state: "live"},
		{issue: 3, alive: false, state: "stale"},
		{issue: 4, alive: true, state: "live"},
		{issue: 5, alive: false, state: "stale"},
		{issue: 6, alive: false, state: "unknown"},
	}
	if len(panes) != len(wants) {
		t.Fatalf("panes = %d, want %d", len(panes), len(wants))
	}
	for i, want := range wants {
		got := panes[i]
		if got.IssueNum != want.issue || got.Alive != want.alive || got.RuntimeState != want.state || got.TmuxState != want.state {
			t.Errorf("pane[%d] = issue:%d alive:%t runtime:%q tmux:%q, want issue:%d alive:%t state:%q", i, got.IssueNum, got.Alive, got.RuntimeState, got.TmuxState, want.issue, want.alive, want.state)
		}
	}
	if panes[3].AgentState != "" || panes[4].AgentState != "" {
		t.Fatalf("unobserved herdr AgentState = %q,%q, want empty without runtime agent evidence", panes[3].AgentState, panes[4].AgentState)
	}
}

func TestBuildDegradesWhenGitHubFails(t *testing.T) {
	calls := 0
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1"), pane("1", 2, "%1")), // dup issue num -> single gh call
		ListLive:  livePanesAt("%1"),
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesWith(map[string]LivePaneInfo{"%1": {Path: "/some/other/dir"}}),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
	}
	snap := Build("o/n", "/root", c)
	if snap.Sessions[0].Panes[0].Alive {
		t.Fatal("a reused pane id at an unrelated path must be dead, not alive")
	}
}

func TestBuildHerdrLivenessRequiresFullIdentityAndProvenance(t *testing.T) {
	tests := []struct {
		name       string
		mutateRow  func(*state.Pane)
		mutateLive func(*backend.LivePane)
		include    bool
		wantAlive  bool
		wantState  string
	}{
		{name: "matching identity is live", include: true, wantAlive: true},
		{
			name: "workspace label changed",
			mutateLive: func(p *backend.LivePane) {
				p.WorkspaceLabel = "foreign-label"
			},
			include: true,
		},
		{
			name: "recorded workspace label missing",
			mutateRow: func(p *state.Pane) {
				p.HerdrWorkspaceLabel = ""
			},
			include:   true,
			wantState: "unsupported",
		},
		{
			name: "terminal id changed",
			mutateLive: func(p *backend.LivePane) {
				p.TerminalID = "terminal-reused"
			},
			include: true,
		},
		{
			name: "recorded terminal id missing",
			mutateRow: func(p *state.Pane) {
				p.HerdrTerminalID = ""
			},
			include:   true,
			wantState: "unsupported",
		},
		{
			name: "recorded workspace id missing",
			mutateRow: func(p *state.Pane) {
				p.HerdrWorkspaceID = ""
			},
			include:   true,
			wantState: "unsupported",
		},
		{
			name: "recorded route session missing",
			mutateRow: func(p *state.Pane) {
				p.HerdrSession = ""
			},
			include:   true,
			wantState: "unsupported",
		},
		{
			name: "recorded route socket missing",
			mutateRow: func(p *state.Pane) {
				p.HerdrSocketPath = ""
			},
			include:   true,
			wantState: "unsupported",
		},
		{
			name: "observed terminal id missing",
			mutateLive: func(p *backend.LivePane) {
				p.TerminalID = ""
			},
			include: true,
		},
		{
			name: "workspace changed",
			mutateLive: func(p *backend.LivePane) {
				p.Ref.Workspace = "workspace-reused"
			},
			include: true,
		},
		{
			name: "session changed",
			mutateLive: func(p *backend.LivePane) {
				p.SessionID = "session-reused"
			},
			include: true,
		},
		{
			name: "socket changed",
			mutateLive: func(p *backend.LivePane) {
				p.SocketPath = "/tmp/herdr-reused.sock"
			},
			include: true,
		},
		{
			name: "agent identity changed",
			mutateLive: func(p *backend.LivePane) {
				p.AgentID = "agent-reused"
			},
			include: true,
		},
		{
			name: "agent provider changed",
			mutateLive: func(p *backend.LivePane) {
				p.AgentProvider = "claude"
			},
			include: true,
		},
		{
			name: "agent provider missing",
			mutateLive: func(p *backend.LivePane) {
				p.AgentProvider = ""
			},
			include: true,
		},
		{
			name: "recorded agent disappeared",
			mutateLive: func(p *backend.LivePane) {
				p.AgentID = ""
				p.AgentSession = nil
				p.AgentPresent = false
				p.AgentState = ""
			},
			include: true,
		},
		{
			name: "logical conversation changed",
			mutateLive: func(p *backend.LivePane) {
				changed := *p.AgentSession
				changed.Value = "session-value-reused"
				p.AgentSession = &changed
			},
			include: true,
		},
		{
			name: "recorded agent identity missing",
			mutateRow: func(p *state.Pane) {
				p.HerdrAgentID = ""
				p.HerdrAgentSession = nil
			},
			include:   true,
			wantState: "unsupported",
		},
		{
			name: "recorded agent contract lacks runtime identity after process exit",
			mutateRow: func(p *state.Pane) {
				p.HerdrAgentID = ""
				p.HerdrAgentSession = nil
			},
			mutateLive: func(p *backend.LivePane) {
				p.AgentID = ""
				p.AgentSession = nil
				p.AgentPresent = false
				p.AgentState = ""
			},
			include:   true,
			wantState: "unsupported",
		},
		{
			name: "agent appeared in recorded generic pane",
			mutateRow: func(p *state.Pane) {
				p.Agent = ""
				p.HerdrAgentID = ""
				p.HerdrAgentSession = nil
			},
			include: true,
		},
		{
			name: "unbound logical conversation requires state binding",
			mutateRow: func(p *state.Pane) {
				p.HerdrAgentSession = nil
			},
			include: true,
		},
		{
			name: "provider omitted logical conversation",
			mutateRow: func(p *state.Pane) {
				p.HerdrAgentSession = nil
			},
			mutateLive: func(p *backend.LivePane) {
				p.AgentSession = nil
			},
			include:   true,
			wantAlive: true,
		},
		{
			name: "recorded logical conversation invalid",
			mutateRow: func(p *state.Pane) {
				p.HerdrAgentSession.Kind = "name"
			},
			include:   true,
			wantState: "unsupported",
		},
		{
			name: "observed logical conversation missing",
			mutateLive: func(p *backend.LivePane) {
				p.AgentSession = nil
			},
			include: true,
		},
		{
			name: "repository identity changed",
			mutateLive: func(p *backend.LivePane) {
				p.RepoKey = "/other/.git"
			},
			include: true,
		},
		{
			name: "recorded repository identity missing",
			mutateRow: func(p *state.Pane) {
				p.HerdrRepoKey = ""
			},
			include: true,
		},
		{
			name: "observed repository identity missing",
			mutateLive: func(p *backend.LivePane) {
				p.RepoKey = ""
			},
			include: true,
		},
		{
			name: "worktree provenance changed despite matching saved cwd",
			mutateLive: func(p *backend.LivePane) {
				p.CurrentPath = "/repo/.fanout/worktrees/child"
				p.WorktreePath = "/repo/.fanout/worktrees/reused"
			},
			include: true,
		},
		{
			name: "recorded worktree path missing",
			mutateRow: func(p *state.Pane) {
				p.WorktreePath = ""
			},
			include:   true,
			wantState: "unsupported",
		},
		{name: "pane absent", include: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := herdrPane("1", 2, "workspace-a:p1")
			observed := liveHerdrPane(row)
			if tt.mutateRow != nil {
				tt.mutateRow(&row)
			}
			if tt.mutateLive != nil {
				tt.mutateLive(&observed)
			}
			if tt.wantAlive && herdrRowUnsupported(row) {
				t.Fatalf("matching fixture is unexpectedly unsupported: %+v", row)
			}
			var live []backend.LivePane
			if tt.include {
				live = []backend.LivePane{observed}
			}
			got := buildWithLivePanes([]state.Pane{row}, live, nil).Sessions[0].Panes[0]
			wantState := tt.wantState
			if wantState == "" {
				wantState = "stale"
			}
			wantTitle := ""
			wantAgentState := ""
			if tt.wantAlive {
				wantState = "live"
				wantTitle = "herdr child"
				wantAgentState = "working"
			}
			wantTmuxState := compatibilityTmuxState(wantState)
			if got.Alive != tt.wantAlive || got.RuntimeState != wantState || got.TmuxState != wantTmuxState {
				t.Fatalf("Build() alive=%t runtime=%q tmux=%q, want alive=%t runtime=%q tmux=%q", got.Alive, got.RuntimeState, got.TmuxState, tt.wantAlive, wantState, wantTmuxState)
			}
			if got.RuntimeTitle != wantTitle || got.TmuxTitle != wantTitle || got.AgentState != wantAgentState {
				t.Fatalf("Build() runtimeTitle=%q tmuxTitle=%q agentState=%q, want titles=%q agentState=%q", got.RuntimeTitle, got.TmuxTitle, got.AgentState, wantTitle, wantAgentState)
			}
		})
	}
}

func TestHerdrPaneMatchesOwnedShellWithoutAgentIdentity(t *testing.T) {
	row := herdrPane("@manual", -1, "workspace-a:p1")
	row.Kind = state.PaneKindShell
	row.Agent = state.PaneKindShell
	row.HerdrAgentID = ""
	row.HerdrAgentSession = nil
	live := liveHerdrPane(row)
	live.AgentState = ""
	live.NativeAgentState = ""
	live.AgentID = ""
	live.AgentProvider = ""
	live.AgentSession = nil
	live.AgentPresent = false
	if !HerdrPaneMatches(row, live) || herdrRowUnsupported(row) {
		t.Fatalf("owned shell identity rejected: row=%+v live=%+v", row, live)
	}
}

func TestBuildHerdrWithoutWorktreeProvenanceUsesSavedCWDExactly(t *testing.T) {
	row := herdrPane("1", 2, "workspace-a:p1")
	row.WorktreePath = "/repo/saved-cwd"
	row.HerdrRepoKey = ""
	base := liveHerdrPane(row)
	base.RepoKey = ""
	base.ProjectRoot = ""
	base.WorktreePath = ""

	tests := []struct {
		name              string
		currentCWD        string
		currentProvenance bool
		wantAlive         bool
	}{
		{name: "matching saved cwd", currentCWD: row.WorktreePath, wantAlive: true},
		{name: "saved cwd subdirectory is not exact", currentCWD: row.WorktreePath + "/subdir"},
		{name: "different saved cwd", currentCWD: "/repo/other"},
		{name: "worktree provenance appeared", currentCWD: row.WorktreePath, currentProvenance: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observed := base
			// CurrentPath is the terminal record's saved cwd. The adapter does not
			// project foreground_cwd into LivePane, so it cannot authorize liveness.
			observed.CurrentPath = tt.currentCWD
			if tt.currentProvenance {
				observed.RepoKey = "/repo/.git"
				observed.ProjectRoot = "/repo"
				observed.WorktreePath = row.WorktreePath
			}
			got := buildWithLivePanes([]state.Pane{row}, []backend.LivePane{observed}, nil).Sessions[0].Panes[0]
			if got.Alive != tt.wantAlive {
				t.Fatalf("Build() alive=%t for saved cwd %q, want %t", got.Alive, tt.currentCWD, tt.wantAlive)
			}
			wantState := "stale"
			if tt.wantAlive {
				wantState = "live"
			}
			if got.RuntimeState != wantState {
				t.Fatalf("Build() runtime state=%q, want %q", got.RuntimeState, wantState)
			}
		})
	}
}

func TestBuildHerdrAgentRecordIsSeparateFromPaneLiveness(t *testing.T) {
	tests := []struct {
		name       string
		mutateRow  func(*state.Pane)
		mutateLive func(*backend.LivePane)
	}{
		{
			name: "generic pane without agent record",
			mutateRow: func(p *state.Pane) {
				p.Agent = ""
				p.HerdrAgentID = ""
				p.HerdrAgentSession = nil
			},
			mutateLive: func(p *backend.LivePane) {
				p.AgentID = ""
				p.AgentSession = nil
				p.AgentPresent = false
				// Even a stale projected value must not survive once the agent
				// record itself is absent from the same snapshot.
				p.AgentState = backend.AgentWorking
				p.NativeAgentState = "working"
			},
		},
		{
			name: "unknown native status",
			mutateLive: func(p *backend.LivePane) {
				p.AgentState = ""
				p.NativeAgentState = "unknown"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := herdrPane("1", 2, "workspace-a:p1")
			row.AgentStatus = "running"
			if tt.mutateRow != nil {
				tt.mutateRow(&row)
			}
			observed := liveHerdrPane(row)
			tt.mutateLive(&observed)
			got := buildWithLivePanes([]state.Pane{row}, []backend.LivePane{observed}, nil).Sessions[0].Panes[0]
			if !got.Alive || got.RuntimeState != "live" || got.TmuxState != "live" {
				t.Fatalf("pane liveness = alive:%t runtime:%q tmux:%q, want live", got.Alive, got.RuntimeState, got.TmuxState)
			}
			if got.AgentState != "" {
				t.Fatalf("AgentState = %q, want unknown; recorded running must not leak into live herdr observation", got.AgentState)
			}
		})
	}
}

func TestBuildHerdrReportedStateRefinesOnlyLiveAgentDisplay(t *testing.T) {
	row := herdrPane("1", 2, "workspace-a:p1")
	row.ReportedState = "plan"
	observed := liveHerdrPane(row)
	observed.AgentState = ""
	observed.NativeAgentState = "unknown"

	got := buildWithLivePanes([]state.Pane{row}, []backend.LivePane{observed}, nil).Sessions[0].Panes[0]
	if !got.Alive || got.AgentState != "plan" {
		t.Fatalf("live telemetry display = alive:%t state:%q, want live plan", got.Alive, got.AgentState)
	}
	stale := buildWithLivePanes([]state.Pane{row}, nil, nil).Sessions[0].Panes[0]
	if stale.Alive || stale.AgentState != "" {
		t.Fatalf("stale telemetry display = alive:%t state:%q, want stale without state", stale.Alive, stale.AgentState)
	}
	row.ReportedState = "done"
	done := buildWithLivePanes([]state.Pane{row}, []backend.LivePane{observed}, nil)
	if done.Rollup.Merged != 0 || done.Rollup.Pending != 1 || done.Rollup.AllMerged {
		t.Fatalf("done telemetry changed completion rollup: %+v", done.Rollup)
	}
}

func TestBuildHerdrDoneThenFocusedIdleRemainPublicDisplayStates(t *testing.T) {
	row := herdrPane("1", 2, "workspace-a:p1")
	sequence := []struct {
		state   backend.AgentState
		focused bool
	}{
		{state: backend.AgentDone, focused: false},
		{state: backend.AgentIdle, focused: true},
	}
	for step, want := range sequence {
		observed := liveHerdrPane(row)
		observed.AgentState = want.state
		observed.NativeAgentState = string(want.state)
		observed.Focused = want.focused
		snap := buildWithLivePanes([]state.Pane{row}, []backend.LivePane{observed}, nil)
		got := snap.Sessions[0].Panes[0]
		if !got.Alive || got.AgentState != string(want.state) {
			t.Fatalf("step %d = alive:%t agentState:%q, want alive with %q", step, got.Alive, got.AgentState, want.state)
		}
		if snap.Rollup.Running != 0 {
			t.Fatalf("step %d Rollup.Running = %d, want 0 for display-only %q", step, snap.Rollup.Running, want.state)
		}
	}
}

func TestBuildHerdrRuntimeFieldsAreAdditiveTmuxAliases(t *testing.T) {
	row := herdrPane("1", 2, "workspace-a:p1")
	got := buildWithLivePanes([]state.Pane{row}, []backend.LivePane{liveHerdrPane(row)}, nil).Sessions[0].Panes[0]
	if got.RuntimeState != "live" || got.TmuxState != got.RuntimeState || got.RuntimeTitle != "herdr child" || got.TmuxTitle != got.RuntimeTitle {
		t.Fatalf("runtime/tmux aliases = runtime:%q tmux:%q runtimeTitle:%q tmuxTitle:%q", got.RuntimeState, got.TmuxState, got.RuntimeTitle, got.TmuxTitle)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal pane view: %v", err)
	}
	for _, want := range []string{`"runtimeState":"live"`, `"tmuxState":"live"`, `"runtimeTitle":"herdr child"`, `"tmuxTitle":"herdr child"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("pane view JSON missing %s: %s", want, raw)
		}
	}

	row.HerdrTerminalID = ""
	unsupported := buildWithLivePanes([]state.Pane{row}, nil, nil).Sessions[0].Panes[0]
	if unsupported.RuntimeState != "unsupported" || unsupported.TmuxState != "unknown" {
		t.Fatalf("unsupported compatibility projection = runtime:%q tmux:%q, want unsupported/unknown", unsupported.RuntimeState, unsupported.TmuxState)
	}
}

func TestBuildHerdrUsesPersistedIdentityWithoutRebinding(t *testing.T) {
	root := t.TempDir()
	row := herdrPane("423", 427, "workspace-a:p1")
	locked, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Errorf("unlock state: %v", unlockErr)
		}
	})
	if err = locked.RecordPane(row); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(state.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	build := func(observed backend.LivePane) Snapshot {
		return Build("o/n", root, Collectors{
			Now:       fixedNow,
			LoadState: func() (state.Store, error) { return state.LoadProject(root) },
			ListLive:  func() ([]backend.LivePane, error) { return []backend.LivePane{observed}, nil },
			IssuePRs:  func(int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
			Waves:     wavesNone,
		})
	}

	matching := liveHerdrPane(row)
	got := build(matching).Sessions[0].Panes[0]
	if !got.Alive || got.RuntimeState != "live" {
		t.Fatalf("matching persisted identity = alive:%t state:%q, want live", got.Alive, got.RuntimeState)
	}

	restarted := matching
	restarted.TerminalID = "terminal-after-restart"
	got = build(restarted).Sessions[0].Panes[0]
	if got.Alive || got.RuntimeState != "stale" {
		t.Fatalf("changed terminal identity = alive:%t state:%q, want stale", got.Alive, got.RuntimeState)
	}

	after, err := os.ReadFile(state.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("snapshot observation rebound persisted herdr identity:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	loaded, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := loaded.Find("423", 427)
	if !ok || persisted.HerdrTerminalID != row.HerdrTerminalID ||
		persisted.HerdrAgentSession == nil || *persisted.HerdrAgentSession != *row.HerdrAgentSession {
		t.Fatalf("persisted herdr identity after stale observation = %+v (found=%t)", persisted, ok)
	}

	// Rows written before the v1 identity baseline existed are a persistent
	// unsupported capability, not a stale match and not a candidate for
	// snapshot-driven adoption.
	unbound := row
	unbound.HerdrTerminalID = ""
	unbound.HerdrRepoKey = ""
	unbound.HerdrAgentSession = nil
	unboundStore, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if unlockErr := unboundStore.Unlock(); unlockErr != nil {
			t.Errorf("unlock unbound state: %v", unlockErr)
		}
	})
	if err = unboundStore.RecordPane(unbound); err != nil {
		t.Fatal(err)
	}
	if err = unboundStore.Unlock(); err != nil {
		t.Fatal(err)
	}
	unboundBefore, err := os.ReadFile(state.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	got = build(matching).Sessions[0].Panes[0]
	if got.Alive || got.RuntimeState != "unsupported" || got.TmuxState != "unknown" {
		t.Fatalf("unbound persisted identity = alive:%t runtime:%q tmux:%q, want false/unsupported/unknown", got.Alive, got.RuntimeState, got.TmuxState)
	}
	unboundAfter, err := os.ReadFile(state.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(unboundAfter) != string(unboundBefore) {
		t.Fatalf("snapshot observation filled unsupported herdr identity:\n--- before ---\n%s\n--- after ---\n%s", unboundBefore, unboundAfter)
	}
}

func TestDerivePaneEnablesHerdrReadButNotDashboardMutation(t *testing.T) {
	derived := DerivePane("/repo", "425", PaneView{
		Backend:   backend.Herdr,
		PaneID:    "w1:p1",
		Alive:     true,
		TmuxState: "live",
	})
	if derived.CanFocus || !derived.CanPeek {
		t.Fatalf("herdr derived actions = focus:%t peek:%t, want false/true", derived.CanFocus, derived.CanPeek)
	}
	if derived.FilterValues["backend"] != "herdr" || !strings.Contains(derived.FilterText, "herdr") {
		t.Fatalf("herdr derived filter metadata = values:%+v text:%q", derived.FilterValues, derived.FilterText)
	}
}

func TestBuildAliveWhenPaneInWorktreeSubdir(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")), // worktree /wt/%1
		ListLive:  livePanesWith(map[string]LivePaneInfo{"%1": {Path: "/wt/%1/sub/dir"}}),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
	}
	snap := Build("o/n", "/root", c)
	if !snap.Sessions[0].Panes[0].Alive {
		t.Fatal("a pane cd'd into a worktree subdir should still be alive")
	}
}

func TestBuildAliveWhenPaneWorktreeOptionMatches(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")), // worktree /wt/%1
		ListLive: livePanesWith(map[string]LivePaneInfo{"%1": {
			Path:         "/repo",
			WorktreePath: "/wt/%1",
		}}),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
	}
	snap := Build("o/n", "/root", c)
	if !snap.Sessions[0].Panes[0].Alive {
		t.Fatal("a pane with matching @fanout_worktree_path should be alive even when cwd is stale")
	}
}

func TestBuildEmptyStateYieldsEmptySnapshot(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: func() (state.Store, error) { return state.Store{SchemaVersion: 1, Panes: []state.Pane{}}, nil },
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt("%1"), // %2 is recorded but not live
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

	c.ListLive = func() ([]backend.LivePane, error) { return nil, errors.New("tmux not found") }
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
		ListLive: livePanesWith(map[string]LivePaneInfo{
			"%1": {Path: "/wt/%1", Title: "two: child"},
			"%2": {Path: "/somewhere/else", Title: "reused id"}, // path mismatch -> dead
		}),
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

// TestNormalizeAgentState pins normalizeAgentState to the lowercase literals of
// the 6-value contract (running / working / plan / blocked / idle / done: the
// wrapper writes running/done, agent hooks write the rest); every other input
// collapses to "".
func TestNormalizeAgentState(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty stays empty", in: "", want: ""},
		{name: "running passes through", in: "running", want: "running"},
		{name: "working passes through", in: "working", want: "working"},
		{name: "idle passes through", in: "idle", want: "idle"},
		{name: "plan passes through", in: "plan", want: "plan"},
		{name: "blocked passes through", in: "blocked", want: "blocked"},
		{name: "done passes through", in: "done", want: "done"},
		{name: "surrounding whitespace is trimmed", in: " running ", want: "running"},
		{name: "hook value with whitespace is trimmed", in: " idle ", want: "idle"},
		{name: "value from outside the wrapper is unknown", in: "claude", want: ""},
		{name: "process name is unknown", in: "bash", want: ""},
		{name: "string spoofed by an in-pane process is unknown", in: "x\ty", want: ""},
		{name: "only the lowercase literal is accepted", in: "RUNNING", want: ""},
		{name: "uppercase hook value is rejected", in: "WORKING", want: ""},
		{name: "mixed-case hook value is rejected", in: "Idle", want: ""},
		{name: "trailing garbage is rejected", in: "done extra", want: ""},
		{name: "trailing garbage on a hook value is rejected", in: "plan extra", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAgentState(tt.in); got != tt.want {
				t.Errorf("normalizeAgentState(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildAgentStateFromLiveOption(t *testing.T) {
	dead := pane("1", 10, "%9")
	dead.AgentStatus = "running" // pane 死亡 + tmux 正常なら記録値は使われない
	c := Collectors{
		Now: fixedNow,
		LoadState: storeOf(
			pane("1", 2, "%1"), pane("1", 3, "%2"), pane("1", 4, "%3"), pane("1", 5, "%4"),
			pane("1", 6, "%5"), pane("1", 7, "%6"), pane("1", 8, "%7"), pane("1", 9, "%8"), dead,
		),
		ListLive: livePanesWith(map[string]LivePaneInfo{
			"%1": {Path: "/wt/%1", AgentState: "running"},
			"%2": {Path: "/wt/%2", AgentState: "done"},
			"%3": {Path: "/wt/%3", AgentState: "forged junk"}, // 偽装/未知の値は不明へ
			"%4": {Path: "/wt/%4"},                            // option 未設定の alive pane(旧版 fanout 起動など)
			"%5": {Path: "/wt/%5", AgentState: "working"},     // 以下 4 値は hooks が書く明示信号
			"%6": {Path: "/wt/%6", AgentState: "plan"},
			"%7": {Path: "/wt/%7", AgentState: "blocked"},
			"%8": {Path: "/wt/%8", AgentState: "idle"},
			// %9 は live set に居ない(pane 死亡)
		}),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:    wavesNone,
	}
	snap := Build("o/n", "/root", c)
	panes := snap.Sessions[0].Panes
	wants := []string{"running", "done", "", "", "working", "plan", "blocked", "idle", ""}
	for i, want := range wants {
		if panes[i].AgentState != want {
			t.Fatalf("#%d AgentState = %q, want %q", panes[i].IssueNum, panes[i].AgentState, want)
		}
	}
	// active 集合(running / working / plan)だけを数える: blocked と idle は
	// live でも進行中ではない。
	if snap.Sessions[0].Rollup.Running != 3 {
		t.Fatalf("session Rollup.Running = %d, want 3", snap.Sessions[0].Rollup.Running)
	}
	if snap.Rollup.Running != 3 {
		t.Fatalf("snapshot Rollup.Running = %d, want 3", snap.Rollup.Running)
	}
}

func TestBuildAgentStateFallsBackToRecordedStatusWhenTmuxDegraded(t *testing.T) {
	recorded := pane("1", 2, "%1")
	recorded.AgentStatus = "running"
	unrecorded := pane("1", 3, "%2") // 旧 state 行: agentStatus 無し
	tampered := pane("1", 4, "%3")   // 手編集された state 行: 規定外の値は捨てる
	tampered.AgentStatus = "<b>maybe</b>"
	hooked := pane("1", 5, "%4") // 6 値契約の hook 値も fallback で通る
	hooked.AgentStatus = "working"
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(recorded, unrecorded, tampered, hooked),
		ListLive:  func() ([]backend.LivePane, error) { return nil, errors.New("tmux not found") },
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
	if panes[3].AgentState != "working" {
		t.Fatalf("degraded-tmux AgentState = %q, want fallback to recorded \"working\"", panes[3].AgentState)
	}
	if snap.Rollup.Running != 2 {
		t.Fatalf("Rollup.Running = %d, want 2 (running + working fallback rows count)", snap.Rollup.Running)
	}
}

func TestBuildPromptPassthrough(t *testing.T) {
	p := pane("1", 2, "%1")
	p.Prompt = "Implement #2 as designed"
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(p),
		ListLive:  livePanesAt(),
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
	planPane.PlanMode = true
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(planPane, pane("1", 3, "%2")),
		ListLive:  livePanesAt(),
		IssuePRs:  func(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil },
		Waves:     wavesNone,
	}
	panes := Build("o/n", "/root", c).Sessions[0].Panes
	if !panes[0].PlanMode {
		t.Fatal("PlanMode = false want passthrough of the state row PlanMode")
	}
	if panes[1].PlanMode {
		t.Fatal("PlanMode = true for a non-plan row, want false")
	}
}

func TestBranchPRLookupKey(t *testing.T) {
	tests := []struct {
		name       string
		pane       state.Pane
		wantBranch string
		wantOK     bool
	}{
		{
			name:       "manual prompt session",
			pane:       state.Pane{Parent: "@manual", IssueNum: -1, BranchName: "  fanout/manual  "},
			wantBranch: "fanout/manual",
			wantOK:     true,
		},
		{
			name:       "plan task",
			pane:       state.Pane{Parent: "plan:alpha", IssueNum: 0, TaskID: "task-a", BranchName: "fanout/task-a"},
			wantBranch: "fanout/task-a",
			wantOK:     true,
		},
		{
			name: "positive issue",
			pane: state.Pane{IssueNum: 12, BranchName: "fanout/issue-12"},
		},
		{
			name: "empty branch",
			pane: state.Pane{Parent: "@manual", IssueNum: -1, BranchName: "  "},
		},
		{
			name: "shell",
			pane: state.Pane{IssueNum: -1, Kind: state.PaneKindShell, BranchName: "fanout/shell"},
		},
		{
			name: "attached agent",
			pane: state.Pane{IssueNum: -1, Kind: state.PaneKindAttachedAgent, BranchName: "fanout/source"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBranch, gotOK := BranchPRLookupKey(tt.pane)
			if gotBranch != tt.wantBranch || gotOK != tt.wantOK {
				t.Fatalf("BranchPRLookupKey() = %q, %v, want %q, %v", gotBranch, gotOK, tt.wantBranch, tt.wantOK)
			}
		})
	}
}

func TestBuildManualPromptSessionsUseBranchPRs(t *testing.T) {
	planMode := pane("@manual", -2, "%1")
	planMode.TaskID = ""
	planMode.BranchName = "  fanout/manual-shared  "
	planMode.PlanMode = true
	normalMode := pane("@manual", -1, "%2")
	normalMode.TaskID = ""
	normalMode.BranchName = "fanout/manual-shared"
	normalMode.PlanMode = false

	branchCalls := 0
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(normalMode, planMode),
		ListLive:  livePanesAt(),
		IssuePRs: func(num int) (string, []ghissue.PRRef, error) {
			t.Fatalf("IssuePRs(%d) called for @manual row", num)
			return "", nil, nil
		},
		BranchPRs: func(branch string) ([]ghissue.PRRef, error) {
			branchCalls++
			if branch != "fanout/manual-shared" {
				t.Fatalf("BranchPRs branch = %q, want fanout/manual-shared", branch)
			}
			return []ghissue.PRRef{{Number: 702, State: "OPEN", CIStatus: " PASS "}}, nil
		},
		Waves: wavesNone,
	}

	snap := Build("o/n", "/root", c)
	if branchCalls != 1 {
		t.Fatalf("BranchPRs calls = %d, want one cached call for normalized shared branch", branchCalls)
	}
	panes := snap.Sessions[0].Panes
	if len(panes) != 2 {
		t.Fatalf("panes = %+v, want two @manual panes", panes)
	}
	for _, pv := range panes {
		if pv.TaskID != "" || pv.IssueState != IssueStateUnknown || len(pv.PRs) != 1 || pv.PRs[0].Number != 702 {
			t.Fatalf("manual pane should carry branch PR state: %+v", pv)
		}
		if pv.CIStatus != "pass" {
			t.Fatalf("manual pane CIStatus = %q, want pass", pv.CIStatus)
		}
		if pv.Derived.PRSummary != "#702 open" || pv.Derived.PrimaryPRNumber != 702 || pv.Derived.PrimaryPRState != "open" || pv.Derived.CI != "pass" {
			t.Fatalf("manual pane derived PR/CI = %+v", pv.Derived)
		}
	}
	if !panes[0].PlanMode || panes[1].PlanMode {
		t.Fatalf("PlanMode passthrough = %v,%v, want true,false", panes[0].PlanMode, panes[1].PlanMode)
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
		ListLive:  livePanesAt(),
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

func TestBuildIssueLessBranchPRFailureDegradesGitHub(t *testing.T) {
	planTask := pane("plan:alpha", 0, "%1")
	planTask.TaskID = "task-a"
	planTask.BranchName = "fanout/task-a"
	manual := pane("@manual", -1, "%1")
	manual.TaskID = ""
	manual.BranchName = "fanout/manual"

	for _, tt := range []struct {
		name string
		pane state.Pane
	}{
		{name: "plan task", pane: planTask},
		{name: "manual prompt session", pane: manual},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Collectors{
				Now:       fixedNow,
				LoadState: storeOf(tt.pane),
				ListLive:  livePanesAt(),
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
				t.Fatalf("failed issue-less pane = %+v, want UNKNOWN and non-nil empty PRs", pv)
			}
		})
	}
}

func TestBuildCIStatusFromPrimaryPR(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1"), pane("1", 3, "%2")),
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt("%1"),
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
	if !queued.NotStarted || queued.Alive || queued.Backend != "" || queued.PaneID != "" || queued.Agent != "" || queued.BranchName != "" {
		t.Fatalf("synthetic pane must carry zero pane fields: %+v", queued)
	}
	if queued.Derived.FilterValues["backend"] != "" || strings.Contains(queued.Derived.FilterText, "tmux") {
		t.Fatalf("synthetic pane must not invent backend metadata: %+v", queued.Derived)
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt(),
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
		ListLive:  livePanesAt(),
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
	tests := []struct {
		name       string
		issueState string
		blocked    bool
		want       string
	}{
		{name: "closed issue is closed", issueState: "CLOSED", blocked: false, want: "closed"},
		{name: "closed wins over blocked", issueState: "closed", blocked: true, want: "closed"}, // closed は blocked より優先(TUI と同順)
		{name: "open and blocked is deferred", issueState: "OPEN", blocked: true, want: "deferred"},
		{name: "open and unblocked is queued", issueState: "OPEN", blocked: false, want: "queued"},
		{name: "lowercase open is queued", issueState: "open", blocked: false, want: "queued"},
		{name: "unknown issue state is unknown", issueState: IssueStateUnknown, blocked: false, want: "unknown"},
		{name: "empty issue state is unknown", issueState: "", blocked: false, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SyntheticTmuxState(tt.issueState, tt.blocked); got != tt.want {
				t.Errorf("SyntheticTmuxState(%q, %v) = %q, want %q", tt.issueState, tt.blocked, got, tt.want)
			}
		})
	}
}

func TestBuildBlockersMarshalAsEmptyArray(t *testing.T) {
	c := Collectors{
		Now:       fixedNow,
		LoadState: storeOf(pane("1", 2, "%1")),
		ListLive:  livePanesAt(),
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
