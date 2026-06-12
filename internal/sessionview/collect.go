package sessionview

import (
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/gitstat"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

// StateLoader returns a LoadState collector reading .fanout/state.json under
// projectRoot. The read is lockless — the dashboard only ever observes state,
// never mutates it.
func StateLoader(projectRoot string) func() (state.Store, error) {
	return func() (state.Store, error) {
		return state.LoadProject(projectRoot)
	}
}

// LivePanes returns a LivePanes collector that maps each live tmux pane id to
// its current path and title, so Build can require both an id match and a path
// under the recorded worktree (robust against server-restart pane-id reuse)
// and surface the live pane title.
func LivePanes() func() (map[string]LivePaneInfo, error) {
	return func() (map[string]LivePaneInfo, error) {
		panes, err := tmuxrun.ListLivePanes()
		if err != nil {
			return nil, err
		}
		m := make(map[string]LivePaneInfo, len(panes))
		for _, p := range panes {
			m[p.ID] = LivePaneInfo{Path: p.CurrentPath, Title: p.Title, AgentState: p.AgentState}
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

// Waves fetches one parent's wave/blocker graph keyed by child issue number.
// The Collectors.Waves field takes only the parent — the poller closes over
// the recorded issue numbers it observed in state and wraps this method.
// Partial results are returned alongside a non-nil error (FetchWaveGraph joins
// per-issue failures), so callers can cache the partial graph and degrade.
func (g GH) Waves(parent string, recordedNums []int) (map[int]WaveInfo, error) {
	graph, err := FetchWaveGraph(g.runner, parent, recordedNums)
	return graph.Info, err
}
