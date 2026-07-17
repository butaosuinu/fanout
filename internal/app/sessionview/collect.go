package sessionview

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/gitstat"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// StateLoader returns a LoadState collector reading .fanout/state.json under
// projectRoot. The read is lockless — the dashboard only ever observes state,
// never mutates it.
func StateLoader(projectRoot string) func() (state.Store, error) {
	return func() (state.Store, error) {
		return state.LoadProject(projectRoot)
	}
}

// MergedStateLoader returns a LoadState collector that unions the
// .fanout/state.json of every git worktree sharing projectRoot's repository, so
// the dashboard and TUI surface Sessions regardless of which worktree fanout
// was launched from. Each worktree records its own state.json; without this
// merge a Session fanned out from a sibling worktree is invisible from the one
// the dashboard runs in.
//
// Provenance is preserved on each pane (SourceProjectRoot) so the TUI can route
// close/merge/cleanup back to the owning worktree's state.json. A
// `git worktree list` failure degrades to a single-root load. The read stays
// lockless and mutation-free, like StateLoader.
//
// Panes are de-duplicated by their stable store identity — the same key the
// per-worktree state lock enforces within one store: (parent, taskId) for task
// rows, the shell key for shell terminals, else (parent, issueNum). Pane id is
// deliberately NOT the dedup key: tmux reuses ids like %1 across server
// restarts, so a stale row in one store could otherwise shadow a live row with
// the reused id in another before Build runs its worktree-path liveness check,
// hiding a valid Session with no way to close it. projectRoot is read first, so
// the home worktree wins identity collisions. Rows with no stable identity (a
// shell pane lacking a shell key) are always kept.
//
// When one identity is recorded in several worktrees, only one row is surfaced,
// but every owning root is accumulated on its SourceProjectRoots so a
// close/cleanup reaches the de-duplicated sibling stores too instead of leaving
// rows that reappear on the next refresh. The surfaced row prefers a candidate
// whose pane is currently live over a stale one, so a dead home row never
// shadows a live sibling before Build's worktree-path liveness check (which
// would otherwise show the child stale and make its live pane un-peekable). When
// none or several candidates are live, the first-read (home) wins. Liveness is
// resolved lazily — only when a duplicate is actually found — so the common
// no-duplicate load issues no tmux call.
//
// Roots are de-duplicated by their symlink-resolved path so the home worktree is
// not read twice when projectRoot is a non-canonical path (a symlinked checkout,
// or a FANOUT_STATE_PATH-inferred root) that differs from git's canonical
// `worktree list` output but points at the same .fanout/state.json.
func MergedStateLoader(projectRoot string) func() (state.Store, error) {
	return mergedStateLoader(projectRoot, liveTmuxPanes)
}

// liveTmuxPanes returns the live tmux panes keyed by id (path/shell-key included),
// best effort (nil when tmux is unavailable). Used only to break
// duplicate-identity ties, with the same path/shell-key-aware check Build uses.
func liveTmuxPanes() map[string]LivePaneInfo {
	live, err := LivePanes()()
	if err != nil {
		return nil
	}
	return live
}

func mergedStateLoader(projectRoot string, livePanes func() map[string]LivePaneInfo) func() (state.Store, error) {
	return func() (state.Store, error) {
		roots, _ := worktree.ListRoots(projectRoot) // always returns at least {projectRoot}
		merged := state.Store{SchemaVersion: state.SchemaVersion, Panes: []state.Pane{}}
		seenIdx := map[string]int{}
		seenRoot := map[string]bool{}

		var live map[string]LivePaneInfo
		liveLoaded := false
		isLive := func(p state.Pane) bool {
			if p.PaneID == "" {
				return false
			}
			if !liveLoaded {
				liveLoaded = true
				if livePanes != nil {
					live = livePanes()
				}
			}
			// Same path/shell-key-aware check as Build: a bare pane-id match would
			// keep a stale home row whose %N was reused by an unrelated live pane,
			// blocking promotion of the genuinely-live sibling.
			return paneAlive(live, p)
		}

		for _, root := range roots {
			rk := resolvePath(root)
			if seenRoot[rk] {
				continue
			}
			seenRoot[rk] = true
			st, err := state.Load(state.Path(root))
			if err != nil {
				// The home root's failure is authoritative (a corrupt own
				// state.json should surface); a sibling's is skipped so one bad
				// store can't blank the whole view.
				if root == projectRoot {
					return state.Store{}, err
				}
				continue
			}
			for _, p := range st.Panes {
				p.SourceProjectRoot = root
				key := paneIdentityKey(p)
				if key == "" {
					p.SourceProjectRoots = []string{root}
					merged.Panes = append(merged.Panes, p)
					continue
				}
				if idx, ok := seenIdx[key]; ok {
					// Same identity in another store. Accumulate this root for
					// lifecycle routing first (PaneID is untouched, so the
					// liveness check below is unaffected), then promote this
					// candidate to the surfaced row when it is live and the kept
					// one is not.
					merged.Panes[idx].SourceProjectRoots = append(merged.Panes[idx].SourceProjectRoots, root)
					if isLive(p) && !isLive(merged.Panes[idx]) {
						p.SourceProjectRoots = merged.Panes[idx].SourceProjectRoots
						merged.Panes[idx] = p
					}
					continue
				}
				seenIdx[key] = len(merged.Panes)
				p.SourceProjectRoots = []string{root}
				merged.Panes = append(merged.Panes, p)
			}
		}
		return merged, nil
	}
}

// paneIdentityKey returns a pane's identity for cross-worktree de-duplication,
// or "" when the pane has no identity that is stable across separate
// .fanout/state.json stores (and so must never be collapsed). Only two kinds of
// identity are globally stable within a repo:
//   - A positive GitHub issue number: assigned by GitHub, unique repo-wide, so
//     the same (parent, issueNum) in two worktrees is the same child.
//   - A shell terminal's shell key: a random 16-byte token, unique per pane, so
//     it never collides across stores. Other pane keys deliberately do not
//     change the issue/manual/task de-duplication rules below.
//
// Everything else is assigned locally and can legitimately repeat across
// worktrees, so it is kept distinct (key ""):
//   - Plan task rows: plan:<slug>/<taskId> is scoped to a spec, so two worktrees
//     running different specs can reuse the same slug+taskId for unrelated work.
//   - Manual panes: nextSyntheticPaneNumber assigns negative numbers per store
//     (@manual/-1, @manual/-2, …), so the same number means different panes.
//
// De-duplicating a locally-assigned identity would hide one worktree's live pane
// before Build's liveness check and let a close/cleanup of the shown row remove
// the hidden sibling's row too (via SourceProjectRoots). Pane id is also not used
// as a key: tmux reuses ids like %1 across server restarts.
func paneIdentityKey(p state.Pane) string {
	switch {
	case p.IsShell():
		if k := strings.TrimSpace(p.ShellKey); k != "" {
			return "shell\x00" + k
		}
		return ""
	case p.IssueNum > 0:
		return "issue\x00" + NormalizeParent(p.Parent) + "\x00" + strconv.Itoa(p.IssueNum)
	default:
		return ""
	}
}

// resolvePath returns path with symlinks resolved, falling back to the cleaned
// path when the target does not exist or cannot be resolved. Used to detect when
// two worktree roots are the same physical directory.
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// LivePanes returns a LivePanes collector that maps each live tmux pane id to
// its current path, recorded worktree path option, and title, so Build can
// require both an id match and a path under the recorded worktree (robust
// against server-restart pane-id reuse) and surface the live pane title.
func LivePanes() func() (map[string]LivePaneInfo, error) {
	return func() (map[string]LivePaneInfo, error) {
		panes, err := tmuxrun.ListLivePanes()
		if err != nil {
			return nil, err
		}
		m := make(map[string]LivePaneInfo, len(panes))
		for _, p := range panes {
			m[p.ID] = LivePaneInfo{
				Path:         p.CurrentPath,
				WorktreePath: p.WorktreePath,
				Title:        p.Title,
				AgentState:   p.AgentState,
				ShellKey:     p.ShellKey,
			}
		}
		return m, nil
	}
}

// GitWorktreeStat returns a collector for per-pane worktree diff/dirty state.
// baseRef is the pane's recorded base branch; gitstat diffs against its
// merge-base ("" falls back to origin/HEAD, then HEAD).
func GitWorktreeStat(projectRoot string) func(path, baseRef string) (WorktreeStat, error) {
	runner := gitstat.Runner{Cwd: projectRoot}
	return func(path, baseRef string) (WorktreeStat, error) {
		stat, err := runner.Worktree(path, baseRef)
		if err != nil {
			return unknownWorktreeStat(), err
		}
		dirty := "clean"
		if stat.Dirty {
			dirty = "dirty"
		}
		return WorktreeStat{
			DiffSummary: fmt.Sprintf("+%d/-%d", stat.Additions, stat.Deletions),
			DirtyState:  dirty,
		}, nil
	}
}

// GH resolves a repo once and fetches per-issue PR state. The dashboard poller
// drives this on its throttled GitHub tick and caches the results; Build never
// calls gh directly.
type GH struct {
	runner ghissue.Runner
	owner  string
	repo   string
}

// ResolveGH resolves the repo (owner/name) via `gh repo view` in projectRoot,
// mirroring status.go's statusChildren. A failure means the GitHub tier is
// unavailable; the caller should degrade to a state-only view.
func ResolveGH(projectRoot string) (GH, error) {
	runner := ghissue.Runner{Cwd: projectRoot}
	nwo, err := runner.RepoNameWithOwner()
	if err != nil {
		return GH{}, err
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		return GH{}, fmt.Errorf("unexpected nameWithOwner from gh: %q", nwo)
	}
	return GH{runner: runner, owner: owner, repo: repo}, nil
}

// NameWithOwner returns the resolved "owner/name" for display.
func (g GH) NameWithOwner() string {
	if g.owner == "" || g.repo == "" {
		return ""
	}
	return g.owner + "/" + g.repo
}

// IssuePRs fetches one issue's state and closed-by PR refs from GitHub.
func (g GH) IssuePRs(num int) (string, []ghissue.PRRef, error) {
	return g.runner.IssueWithPRs(g.owner, g.repo, num)
}

// BranchPRs fetches PR refs by head branch for branch-owning issue-less rows.
func (g GH) BranchPRs(branch string) ([]ghissue.PRRef, error) {
	return g.runner.PRsForBranch(branch)
}

// Waves fetches one parent's wave/blocker graph: the resolved child set plus
// per-child wave/blocker info keyed by issue number. The Collectors.Waves
// field takes only the parent — the poller closes over the recorded issue
// numbers it observed in state and wraps this method. Partial results are
// returned alongside a non-nil error (FetchWaveGraph joins per-issue
// failures), so callers can cache the partial graph and degrade.
func (g GH) Waves(parent string, recordedNums []int) (WaveGraph, error) {
	return FetchWaveGraph(g.runner, parent, recordedNums)
}
