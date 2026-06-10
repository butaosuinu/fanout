package sessionview

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
)

// Collectors are the injectable IO boundary. The web dashboard's poller and the
// future TUI supply real implementations; tests supply fakes. Build owns no IO
// of its own — every external read goes through one of these function fields.
//
// IssuePRs is intentionally cache-shaped, not "call gh now": the caller decides
// when to refresh GitHub (an expensive per-issue GraphQL call) and Build simply
// reads whatever the caller has. The three return shapes:
//   - (state, prs, nil) with state != ""  → known issue/PR state.
//   - ("", nil, nil)                       → not fetched yet (cache miss); the
//     pane shows UNKNOWN but this does NOT mark GitHub as degraded.
//   - ("", nil, err)                       → a real failure (gh down, repo
//     unresolved); the pane shows UNKNOWN and Degraded.GitHub is set.
type Collectors struct {
	LoadState func() (state.Store, error)
	// LivePanes maps each live tmux pane id to its current working path. A pane
	// counts as alive only when its id is present AND the live path is at/under
	// its recorded worktree — pane ids are reused across tmux server restarts,
	// so id-only matching would falsely revive stale rows.
	LivePanes    func() (map[string]string, error)
	IssuePRs     func(num int) (issueState string, prs []ghissue.PRRef, err error)
	WorktreeStat func(path string) (WorktreeStat, error)
	Now          func() time.Time
}

// Build assembles a Snapshot. It never returns an error: a read-only dashboard
// must keep rendering state.json even when tmux or gh is unavailable, so every
// collector failure degrades into a Degraded flag plus best-effort partial data.
func Build(repo, projectRoot string, c Collectors) Snapshot {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	snap := Snapshot{
		Repo:        repo,
		ProjectRoot: projectRoot,
		GeneratedAt: now().UTC().Format(time.RFC3339),
		Sessions:    []Session{},
	}

	store, err := c.LoadState()
	if err != nil {
		snap.Degraded.Reason = "load state: " + err.Error()
		return snap
	}

	live := map[string]string{}
	if c.LivePanes == nil {
		snap.Degraded.Tmux = true
	} else if set, err := c.LivePanes(); err != nil {
		snap.Degraded.Tmux = true
		snap.Degraded.Reason = appendReason(snap.Degraded.Reason, "tmux: "+err.Error())
	} else {
		live = set
	}

	// One gh read per distinct issue number, cached across sessions.
	prCache := map[int]struct {
		state string
		prs   []ghissue.PRRef
	}{}
	fetch := func(num int) (string, []ghissue.PRRef) {
		if got, ok := prCache[num]; ok {
			return got.state, got.prs
		}
		st, prs := IssueStateUnknown, []ghissue.PRRef{}
		if c.IssuePRs == nil {
			snap.Degraded.GitHub = true
		} else if gotState, gotPRs, err := c.IssuePRs(num); err != nil {
			snap.Degraded.GitHub = true
			snap.Degraded.Reason = appendReason(snap.Degraded.Reason, "github: "+err.Error())
		} else {
			if gotState != "" {
				st = gotState
			}
			if gotPRs != nil {
				prs = gotPRs
			}
		}
		prCache[num] = struct {
			state string
			prs   []ghissue.PRRef
		}{st, prs}
		return st, prs
	}
	worktreeCache := map[string]struct {
		stat WorktreeStat
		err  error
	}{}
	fetchWorktree := func(path string) (WorktreeStat, string) {
		path = strings.TrimSpace(path)
		if path == "" || c.WorktreeStat == nil {
			return unknownWorktreeStat(), ""
		}
		if got, ok := worktreeCache[path]; ok {
			return got.stat, errString(got.err)
		}
		stat, err := c.WorktreeStat(path)
		stat = normalizeWorktreeStat(stat)
		worktreeCache[path] = struct {
			stat WorktreeStat
			err  error
		}{stat, err}
		return stat, errString(err)
	}

	grouped := groupByParent(store.Panes)
	for _, parent := range sortedParents(grouped) {
		panes := grouped[parent]
		sort.Slice(panes, func(i, j int) bool { return panes[i].IssueNum < panes[j].IssueNum })

		session := Session{Parent: parent, Panes: make([]PaneView, 0, len(panes))}
		for _, p := range panes {
			issueState, prs := fetch(p.IssueNum)
			worktreeStat, worktreeErr := fetchWorktree(p.WorktreePath)
			pv := PaneView{
				IssueNum:     p.IssueNum,
				Slug:         p.Slug,
				DisplayName:  p.DisplayName,
				Agent:        p.Agent,
				BranchName:   p.BranchName,
				PaneID:       p.PaneID,
				WorktreePath: p.WorktreePath,
				CreatedAt:    p.CreatedAt,
				Alive:        paneAlive(live, p.PaneID, p.WorktreePath),
				IssueState:   issueState,
				PRs:          prs,
				HasMergedPR:  hasMergedPR(prs),
				DiffSummary:  worktreeStat.DiffSummary,
				DirtyState:   worktreeStat.DirtyState,
				WorktreeErr:  worktreeErr,
			}
			session.Panes = append(session.Panes, pv)
			accumulate(&session.Rollup, pv)
		}
		finalize(&session.Rollup)
		snap.Sessions = append(snap.Sessions, session)

		for _, pv := range session.Panes {
			accumulate(&snap.Rollup, pv)
		}
	}
	finalize(&snap.Rollup)
	return snap
}

func unknownWorktreeStat() WorktreeStat {
	return WorktreeStat{DiffSummary: "-", DirtyState: "unknown"}
}

func normalizeWorktreeStat(stat WorktreeStat) WorktreeStat {
	if stat.DiffSummary == "" {
		stat.DiffSummary = "-"
	}
	if stat.DirtyState == "" {
		stat.DirtyState = "unknown"
	}
	return stat
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func groupByParent(panes []state.Pane) map[string][]state.Pane {
	out := map[string][]state.Pane{}
	for _, p := range panes {
		out[p.Parent] = append(out[p.Parent], p)
	}
	return out
}

// sortedParents orders numeric parents (issue numbers) ascending and before any
// non-numeric parent (Project URLs), which sort lexicographically after.
func sortedParents(grouped map[string][]state.Pane) []string {
	keys := make([]string, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ni, ei := strconv.Atoi(keys[i])
		nj, ej := strconv.Atoi(keys[j])
		switch {
		case ei == nil && ej == nil:
			return ni < nj
		case ei == nil:
			return true // numeric before non-numeric
		case ej == nil:
			return false
		default:
			return keys[i] < keys[j]
		}
	})
	return keys
}

// paneAlive reports whether a recorded pane is live: a tmux pane with its id
// must exist AND be sitting at/under the recorded worktree. Requiring the path
// match defends against tmux reusing a %N id for an unrelated pane after a
// server restart. A row without a recorded worktree (legacy) falls back to an
// id-only match.
func paneAlive(live map[string]string, paneID, worktree string) bool {
	if paneID == "" {
		return false
	}
	cur, ok := live[paneID]
	if !ok {
		return false
	}
	if strings.TrimSpace(worktree) == "" {
		return true
	}
	wt := filepath.Clean(worktree)
	cp := filepath.Clean(cur)
	return cp == wt || strings.HasPrefix(cp, wt+string(filepath.Separator))
}

func hasMergedPR(prs []ghissue.PRRef) bool {
	for _, pr := range prs {
		if pr.State == "MERGED" {
			return true
		}
	}
	return false
}

func accumulate(r *Rollup, pv PaneView) {
	r.Total++
	if pv.HasMergedPR {
		r.Merged++
	}
	if pv.Alive {
		r.Live++
	}
}

func finalize(r *Rollup) {
	r.Pending = r.Total - r.Merged
	r.AllMerged = r.Total > 0 && r.Merged == r.Total
}

func appendReason(existing, add string) string {
	if existing == "" {
		return add
	}
	if strings.Contains(existing, add) {
		return existing // collapse repeated identical reasons (e.g. one per issue)
	}
	return existing + "; " + add
}
