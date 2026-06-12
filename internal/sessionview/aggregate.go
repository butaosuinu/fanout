package sessionview

import (
	"cmp"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
)

// LivePaneInfo is what Build needs to know about one live tmux pane: the cwd
// of its foreground process (for the worktree-path liveness check) and its
// pane title (surfaced as PaneView.TmuxTitle when the pane is alive).
type LivePaneInfo struct {
	// Path is the pane's current working directory.
	Path string
	// Title is the tmux pane title; "" when tmux reports none.
	Title string
	// Command は pane のフォアグラウンドコマンド(#{pane_current_command})。
	// tmux から取得できなかったときは ""。
	Command string
}

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
	// LivePanes maps each live tmux pane id to its current working path and
	// title. A pane counts as alive only when its id is present AND the live
	// path is at/under its recorded worktree — pane ids are reused across tmux
	// server restarts, so id-only matching would falsely revive stale rows.
	LivePanes func() (map[string]LivePaneInfo, error)
	IssuePRs  func(num int) (issueState string, prs []ghissue.PRRef, err error)
	// Waves follows the same three-outcome cache contract as IssuePRs, keyed by
	// parent instead of issue number (the caller closes over the recorded issue
	// numbers it passes to FetchWaveGraph):
	//   - (info, nil)  → known wave/blocker graph for the parent's children.
	//   - (nil, nil)   → not fetched yet (cache miss); panes show zero-valued
	//     wave fields but this does NOT mark GitHub as degraded.
	//   - (info, err)  → a real failure; Degraded.GitHub is set and any partial
	//     info is still applied (FetchWaveGraph keeps partial results).
	// A nil collector means the GitHub tier is disabled, like a nil IssuePRs.
	Waves        func(parent string) (map[int]WaveInfo, error)
	WorktreeStat func(path, baseRef string) (WorktreeStat, error)
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

	live := map[string]LivePaneInfo{}
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
	// One waves read per distinct parent, cached across the Build call.
	waveCache := map[string]map[int]WaveInfo{}
	fetchWaves := func(parent string) map[int]WaveInfo {
		if got, ok := waveCache[parent]; ok {
			return got
		}
		var info map[int]WaveInfo
		if c.Waves == nil {
			snap.Degraded.GitHub = true
		} else {
			got, err := c.Waves(parent)
			if err != nil {
				snap.Degraded.GitHub = true
				snap.Degraded.Reason = appendReason(snap.Degraded.Reason, "github: "+err.Error())
			}
			info = got // a (nil, nil) cache miss leaves zero-valued wave fields
		}
		waveCache[parent] = info
		return info
	}
	worktreeCache := map[string]struct {
		stat WorktreeStat
		err  error
	}{}
	fetchWorktree := func(path, baseRef string) (WorktreeStat, string) {
		path = strings.TrimSpace(path)
		if path == "" || c.WorktreeStat == nil {
			return unknownWorktreeStat(), ""
		}
		key := path + "\x00" + baseRef
		if got, ok := worktreeCache[key]; ok {
			return got.stat, errString(got.err)
		}
		stat, err := c.WorktreeStat(path, baseRef)
		stat = normalizeWorktreeStat(stat)
		worktreeCache[key] = struct {
			stat WorktreeStat
			err  error
		}{stat, err}
		return stat, errString(err)
	}

	grouped := groupByParent(store.Panes)
	for _, parent := range sortedParents(grouped) {
		panes := grouped[parent]
		slices.SortFunc(panes, func(a, b state.Pane) int { return cmp.Compare(a.IssueNum, b.IssueNum) })

		session := Session{Parent: parent, Panes: make([]PaneView, 0, len(panes))}
		waveInfo := fetchWaves(parent)
		for _, p := range panes {
			issueState, prs := fetch(p.IssueNum)
			worktreeStat, worktreeErr := fetchWorktree(p.WorktreePath, p.BaseBranch)
			alive := paneAlive(live, p.PaneID, p.WorktreePath)
			wi := waveInfo[p.IssueNum]
			pv := PaneView{
				IssueNum:     p.IssueNum,
				Slug:         p.Slug,
				DisplayName:  p.DisplayName,
				Agent:        p.Agent,
				BranchName:   p.BranchName,
				PaneID:       p.PaneID,
				WorktreePath: p.WorktreePath,
				CreatedAt:    p.CreatedAt,
				Alive:        alive,
				IssueState:   issueState,
				PRs:          prs,
				HasMergedPR:  hasMergedPR(prs),
				DiffSummary:  worktreeStat.DiffSummary,
				DirtyState:   worktreeStat.DirtyState,
				WorktreeErr:  worktreeErr,
				TmuxState:    tmuxStateOf(p.PaneID, snap.Degraded.Tmux, alive),
				Prompt:       p.Prompt,
				CIStatus:     strings.ToLower(strings.TrimSpace(ghissue.SummarizeCI(prs))),
				Wave:         wi.Wave,
				WaveLabel:    firstNonEmpty(p.Wave, wi.WaveLabel),
				Blockers:     normalizeBlockers(wi.Blockers),
				Blocked:      wi.Blocked,
			}
			if alive {
				pv.TmuxTitle = live[p.PaneID].Title
				pv.AgentState = deriveAgentState(live[p.PaneID].Command)
			} else if snap.Degraded.Tmux {
				// tmux 不通時は動的判定ができないので、起動時に state.json へ
				// 記録した値に fallback する(記録+動的の両方式を持つ利点)。
				// pane 死亡かつ tmux 正常のときは tmux 列が stale を伝えるので
				// 空のままにする。
				pv.AgentState = p.AgentStatus
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
	slices.SortFunc(keys, func(a, b string) int {
		na, ea := strconv.Atoi(a)
		nb, eb := strconv.Atoi(b)
		switch {
		case ea == nil && eb == nil:
			return cmp.Compare(na, nb)
		case ea == nil:
			return -1 // numeric before non-numeric
		case eb == nil:
			return 1
		default:
			return strings.Compare(a, b)
		}
	})
	return keys
}

// paneAlive reports whether a recorded pane is live: a tmux pane with its id
// must exist AND be sitting at/under the recorded worktree. Requiring the path
// match defends against tmux reusing a %N id for an unrelated pane after a
// server restart. A row without a recorded worktree (legacy) falls back to an
// id-only match.
func paneAlive(live map[string]LivePaneInfo, paneID, worktree string) bool {
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
	cp := filepath.Clean(cur.Path)
	return cp == wt || strings.HasPrefix(cp, wt+string(filepath.Separator))
}

// tmuxStateOf mirrors the TUI's tmux-state strings so `state:` filters behave
// identically across surfaces: "-" for a row that never had a pane, "unknown"
// when tmux itself could not be read, "live"/"stale" otherwise.
func tmuxStateOf(paneID string, tmuxDegraded, alive bool) string {
	switch {
	case strings.TrimSpace(paneID) == "":
		return "-"
	case tmuxDegraded:
		return "unknown"
	case alive:
		return "live"
	default:
		return "stale"
	}
}

// shellCommands は deriveAgentState が「agent は終了してシェルに戻った」と
// みなすフォアグラウンドコマンド名の集合。
var shellCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true,
	"ksh": true, "tcsh": true, "csh": true, "ash": true, "nu": true, "pwsh": true,
}

// deriveAgentState は tmux の #{pane_current_command} から agent の実行状態を
// 導く。正規化として先頭の "-" を剥がし basename を小文字化する(ログイン
// シェルは "-zsh" のような形で、tmux はフルパスを返すことがある)。シェル集合
// に含まれれば "done"(agent は終了してシェルに戻った)、空文字なら ""(不明)、
// それ以外(claude / codex / node / python 等)は "running"。
func deriveAgentState(command string) string {
	if command == "" {
		return ""
	}
	name := strings.ToLower(filepath.Base(strings.TrimPrefix(command, "-")))
	if shellCommands[name] {
		return "done"
	}
	return "running"
}

// normalizeBlockers keeps PaneView.Blockers non-nil so it serializes as []
// rather than null (the SPA iterates it unconditionally).
func normalizeBlockers(rows []blockers.Status) []blockers.Status {
	if rows == nil {
		return []blockers.Status{}
	}
	return rows
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
	if pv.AgentState == "running" {
		r.Running++
	}
	if pv.Blocked {
		r.Blocked++
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
