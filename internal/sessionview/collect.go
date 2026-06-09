package sessionview

import (
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/ghissue"
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
// its current path, so Build can require both an id match and a path under the
// recorded worktree (robust against server-restart pane-id reuse).
func LivePanes() func() (map[string]string, error) {
	return func() (map[string]string, error) {
		panes, err := tmuxrun.ListLivePanes()
		if err != nil {
			return nil, err
		}
		m := make(map[string]string, len(panes))
		for _, p := range panes {
			m[p.ID] = p.CurrentPath
		}
		return m, nil
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
