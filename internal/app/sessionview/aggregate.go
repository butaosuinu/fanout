package sessionview

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/blockers"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
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
	// ListLive observes panes through the runtime backend routes selected by the
	// composition root. A pane counts as alive only when its backend-native
	// reference is present and its identity metadata still agrees with the
	// persisted row. A partial result may accompany an error when another route
	// is unavailable; Build retains those observations and marks runtime degraded.
	ListLive func() ([]backend.LivePane, error)
	IssuePRs func(num int) (issueState string, prs []ghissue.PRRef, err error)
	// BranchPRs mirrors IssuePRs for branch-owning issue-less pane rows (for
	// example plan tasks and @manual Prompt Sessions), keyed by normalized head
	// branch. A nil PR slice with nil error is a cache miss, while a non-nil
	// error marks GitHub degraded.
	BranchPRs func(branch string) ([]ghissue.PRRef, error)
	// Waves follows the same three-outcome cache contract as IssuePRs, keyed by
	// parent instead of issue number (the caller closes over the recorded issue
	// numbers it passes to FetchWaveGraph):
	//   - (graph, nil) with non-nil Info → known wave/blocker graph; the child
	//     set (graph.Children) also seeds synthetic not-started rows for
	//     children without a recorded pane.
	//   - (zero WaveGraph, nil) → not fetched yet (cache miss); panes show
	//     zero-valued wave fields and no synthetic rows appear, but this does
	//     NOT mark GitHub as degraded.
	//   - (graph, err) → a real failure; Degraded.GitHub is set and any partial
	//     graph is still applied (FetchWaveGraph keeps partial results).
	// A nil collector means the GitHub tier is disabled, like a nil IssuePRs.
	Waves        func(parent string) (WaveGraph, error)
	WorktreeStat func(path, baseRef string) (WorktreeStat, error)
	Now          func() time.Time
}

// BranchPRLookupKey returns the normalized head branch for an issue-less pane
// that owns its worktree branch. Shell and attached-agent rows reuse another
// pane's worktree and must not duplicate its PR lookup.
func BranchPRLookupKey(p state.Pane) (string, bool) {
	if p.IssueNum > 0 || p.IsShell() || p.IsAttachedAgent() {
		return "", false
	}
	branch := strings.TrimSpace(p.BranchName)
	return branch, branch != ""
}

// Build assembles a Snapshot. It never returns an error: a read-only dashboard
// must keep rendering state.json even when the runtime or gh is unavailable, so every
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

	live := map[livePaneKey]backend.LivePane{}
	failedRuntimeRoutes := map[backend.ObservationRoute]bool{}
	allRuntimeRoutesDegraded := false
	if c.ListLive == nil {
		snap.Degraded.Tmux = true
		snap.Degraded.Runtime = true
		allRuntimeRoutesDegraded = true
	} else {
		set, err := c.ListLive()
		live = indexLivePanes(set)
		if err != nil {
			snap.Degraded.Tmux = true
			snap.Degraded.Runtime = true
			failedRuntimeRoutes, allRuntimeRoutesDegraded = backend.ClassifyObservationError(err)
			snap.Degraded.Reason = appendReason(snap.Degraded.Reason, "runtime: "+err.Error())
		}
	}

	// One gh read per distinct issue number or branch, cached across sessions.
	prCache := map[int]struct {
		state string
		prs   []ghissue.PRRef
	}{}
	branchPRCache := map[string][]ghissue.PRRef{}
	fetchIssue := func(num int) (string, []ghissue.PRRef) {
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
	fetchBranch := func(branch string) []ghissue.PRRef {
		branch = strings.TrimSpace(branch)
		if got, ok := branchPRCache[branch]; ok {
			return got
		}
		prs := []ghissue.PRRef{}
		if branch == "" {
			branchPRCache[branch] = prs
			return prs
		}
		if c.BranchPRs == nil {
			snap.Degraded.GitHub = true
		} else if gotPRs, err := c.BranchPRs(branch); err != nil {
			snap.Degraded.GitHub = true
			snap.Degraded.Reason = appendReason(snap.Degraded.Reason, "github: "+err.Error())
		} else if gotPRs != nil {
			prs = gotPRs
		}
		branchPRCache[branch] = prs
		return prs
	}
	fetchPanePRs := func(p state.Pane) (string, []ghissue.PRRef) {
		if branch, ok := BranchPRLookupKey(p); ok {
			return IssueStateUnknown, fetchBranch(branch)
		}
		if p.IssueNum <= 0 {
			return IssueStateUnknown, []ghissue.PRRef{}
		}
		return fetchIssue(p.IssueNum)
	}
	// One waves read per distinct parent, cached across the Build call.
	waveCache := map[string]WaveGraph{}
	fetchWaves := func(parent string) WaveGraph {
		if got, ok := waveCache[parent]; ok {
			return got
		}
		var graph WaveGraph
		if c.Waves == nil {
			snap.Degraded.GitHub = true
		} else {
			got, err := c.Waves(parent)
			if err != nil {
				snap.Degraded.GitHub = true
				snap.Degraded.Reason = appendReason(snap.Degraded.Reason, "github: "+err.Error())
			}
			graph = got // a zero-valued cache miss leaves zero wave fields and no synthetic rows
		}
		waveCache[parent] = graph
		return graph
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
	recordedByParent := recordedNumsByNormalizedParent(store.Panes)
	syntheticEmitted := map[string]bool{}
	for _, parent := range sortedParents(grouped) {
		panes := grouped[parent]
		slices.SortFunc(panes, func(a, b state.Pane) int {
			if c := cmp.Compare(a.IssueNum, b.IssueNum); c != 0 {
				return c
			}
			return strings.Compare(taskSortKey(a), taskSortKey(b))
		})

		session := Session{Parent: parent, Panes: make([]PaneView, 0, len(panes))}
		graph := WaveGraph{}
		if hasPositiveIssuePane(panes) {
			graph = fetchWaves(parent)
		}
		for _, p := range panes {
			issueState, prs := fetchPanePRs(p)
			worktreeStat, worktreeErr := fetchWorktree(p.WorktreePath, p.BaseBranch)
			current, alive := livePaneForState(live, p)
			runtimeDegraded := allRuntimeRoutesDegraded || failedRuntimeRoutes[observationRouteForState(p)]
			runtimeUnsupported := herdrRowUnsupported(p)
			if runtimeUnsupported {
				alive = false
			}
			runtimeState := runtimeStateOf(p.PaneID, runtimeDegraded, runtimeUnsupported, alive)
			wi := graph.Info[p.IssueNum]
			pv := PaneView{
				IssueNum:           p.IssueNum,
				TaskID:             p.TaskID,
				Kind:               p.Kind,
				Slug:               p.Slug,
				DisplayName:        p.DisplayName,
				Agent:              p.Agent,
				BranchName:         p.BranchName,
				PaneID:             p.PaneID,
				Backend:            p.Backend,
				ShellKey:           p.ShellKey,
				SourceParent:       p.SourceParent,
				SourceIssueNum:     p.SourceIssueNum,
				SourceTaskID:       p.SourceTaskID,
				WorktreePath:       p.WorktreePath,
				SourceProjectRoot:  p.SourceProjectRoot,
				SourceProjectRoots: p.SourceProjectRoots,
				SourceKey:          localSourceKey(p),
				CreatedAt:          p.CreatedAt,
				Alive:              alive,
				IssueState:         issueState,
				PRs:                prs,
				HasMergedPR:        hasMergedPR(prs),
				DiffSummary:        worktreeStat.DiffSummary,
				DirtyState:         worktreeStat.DirtyState,
				WorktreeErr:        worktreeErr,
				RuntimeState:       runtimeState,
				TmuxState:          compatibilityTmuxState(runtimeState),
				PlanMode:           p.CodexPlanMode,
				Prompt:             p.Prompt,
				CIStatus:           strings.ToLower(strings.TrimSpace(ghissue.SummarizeCI(prs))),
				Wave:               wi.Wave,
				WaveLabel:          firstNonEmpty(p.Wave, wi.WaveLabel),
				Blockers:           normalizeBlockers(wi.Blockers),
				Blocked:            wi.Blocked,
			}
			if alive {
				pv.RuntimeTitle = current.Title
				pv.TmuxTitle = current.Title
				if backend.NormalizeName(p.Backend) != backend.Herdr || current.AgentPresent {
					pv.AgentState = normalizeAgentState(string(current.AgentState))
				}
			} else if runtimeDegraded && backend.NormalizeName(p.Backend) == backend.Tmux {
				// tmux 不通時は動的判定ができないので、起動時に state.json へ
				// 記録した値に fallback する(記録+動的の両方式を持つ利点)。
				// state.json は手編集されうる入力なので option 値と同じく
				// 許可 6 値以外は捨てる。pane 死亡かつ tmux 正常のときは
				// tmux 列が stale を伝えるので空のままにする。
				pv.AgentState = normalizeAgentState(p.AgentStatus)
			}
			pv.Derived = DerivePane(projectRoot, parent, pv)
			session.Panes = append(session.Panes, pv)
			accumulate(&session.Rollup, pv)
		}
		// 記録 pane の無い wave graph 上の子 issue を「未開始」の synthetic 行
		// として追加する(TUI の synthetic 行の web 移植)。rowKey は
		// (parent, issueNum) なので、pane が起動した次の snapshot ではこの行が
		// そのまま実 row に置き換わる。"0100"/"100" のようなエイリアス親は同じ
		// wave graph を共有するため、normalize したキーごとに一度だけ emit し、
		// 未記録判定もエイリアス session 横断で行う。
		if normKey := NormalizeParent(parent); !syntheticEmitted[normKey] {
			syntheticEmitted[normKey] = true
			recorded := recordedByParent[normKey]
			for _, child := range childrenByNumber(graph.Children) {
				if child.Number <= 0 || recorded[child.Number] {
					continue
				}
				issueState, prs := fetchIssue(child.Number)
				closeUnconfirmed := false
				if issueState == IssueStateUnknown && child.State != "" {
					// PR キャッシュ未取得でも wave graph は issue 状態を知って
					// いる(Sub-issues API / IssueDetail が state を返す)ので
					// fallback する。ただし PR 状態は未確認なので、CLOSED に
					// fallback した行は rollup から落とさない(下記 countsInRollup)。
					issueState = child.State
					closeUnconfirmed = strings.EqualFold(issueState, "CLOSED")
				}
				wi := graph.Info[child.Number]
				runtimeState := SyntheticTmuxState(issueState, wi.Blocked)
				pv := PaneView{
					IssueNum:         child.Number,
					DisplayName:      issueDisplayName(child),
					IssueState:       issueState,
					PRs:              prs,
					HasMergedPR:      hasMergedPR(prs),
					DiffSummary:      "-",
					DirtyState:       "-",
					RuntimeState:     runtimeState,
					TmuxState:        runtimeState,
					CIStatus:         strings.ToLower(strings.TrimSpace(ghissue.SummarizeCI(prs))),
					Wave:             wi.Wave,
					WaveLabel:        wi.WaveLabel,
					Blockers:         normalizeBlockers(wi.Blockers),
					Blocked:          wi.Blocked,
					NotStarted:       true,
					closeUnconfirmed: closeUnconfirmed,
				}
				pv.Derived = DerivePane(projectRoot, parent, pv)
				session.Panes = append(session.Panes, pv)
				if countsInRollup(pv) {
					accumulate(&session.Rollup, pv)
				}
			}
		}
		finalize(&session.Rollup)
		snap.Sessions = append(snap.Sessions, session)

		for _, pv := range session.Panes {
			if countsInRollup(pv) || isPaneOnly(pv) {
				accumulate(&snap.Rollup, pv)
			}
		}
	}
	finalize(&snap.Rollup)
	return snap
}

func hasPositiveIssuePane(panes []state.Pane) bool {
	for _, pane := range panes {
		if pane.IssueNum > 0 {
			return true
		}
	}
	return false
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

func taskSortKey(p state.Pane) string {
	if strings.TrimSpace(p.TaskID) != "" {
		return p.TaskID
	}
	if strings.TrimSpace(p.BranchName) != "" {
		return p.BranchName
	}
	return p.Slug
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
			if c := cmp.Compare(na, nb); c != 0 {
				return c
			}
			// エイリアス親("0100"/"100")は数値比較で等しい。raw 文字列で
			// タイブレークしないと map 順で session 順序と synthetic 行の
			// 帰属先が build ごとに揺れ、SSE が毎 tick 発火する。
			return strings.Compare(a, b)
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

// NormalizeParent は数値親を Atoi 往復で正規化する("0100" と "100" を同一
// 親として扱う)。state の parentMatches と同じ規則で、dashboard poller の
// wave cache キーもこれを使う(規則が割れるとエイリアス親の synthetic 行が
// 二重 emit / 消失する)。実体は core の parentref.Canon。
func NormalizeParent(parent string) string {
	return parentref.Canon(parent)
}

// localSourceKey returns a stable public token distinguishing a worktree-local
// row (a plan task or @manual pane, IssueNum <= 0) across worktree stores, so
// two siblings sharing the same (parent, issueNum)/(parent, taskId) don't collide
// on the SPA's row key. It hashes the source root rather than exposing the
// absolute path. Globally-stable GitHub issue rows (IssueNum > 0) need no
// disambiguator and return "" (a stable parent#issueNum key is preferred over one
// that shifts with whichever worktree happens to win aggregation).
func localSourceKey(p state.Pane) string {
	if p.IssueNum > 0 {
		return ""
	}
	root := strings.TrimSpace(p.SourceProjectRoot)
	if root == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(root))
	return strconv.FormatUint(uint64(h.Sum32()), 16)
}

// recordedNumsByNormalizedParent は normalize した親キーごとの記録済み issue
// 番号集合。synthetic 行の「未記録」判定はエイリアス session 横断で行う —
// "0100" 配下に記録済みの子を "100" session が synthetic として二重 emit して
// はならない。
func recordedNumsByNormalizedParent(panes []state.Pane) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	for _, p := range panes {
		key := NormalizeParent(p.Parent)
		if out[key] == nil {
			out[key] = map[int]bool{}
		}
		if p.IssueNum > 0 {
			out[key][p.IssueNum] = true
		}
	}
	return out
}

// childrenByNumber は wave graph の子集合を issue 番号昇順のクローンで返し、
// synthetic 行の出力順を決定的にする(FetchWaveGraph の子順はソース由来)。
func childrenByNumber(children []ghissue.Issue) []ghissue.Issue {
	out := slices.Clone(children)
	slices.SortFunc(out, func(a, b ghissue.Issue) int { return cmp.Compare(a.Number, b.Number) })
	return out
}

// issueDisplayName は synthetic 行の表示名: issue タイトル、無ければ "#<num>"
// (TUI の issueTitle と同じ規則)。
func issueDisplayName(child ghissue.Issue) string {
	if strings.TrimSpace(child.Title) != "" {
		return child.Title
	}
	return "#" + strconv.Itoa(child.Number)
}

// SyntheticTmuxState は未開始子 issue の tmux 列値。internal/ui/tui の synthetic
// 行もこれを呼ぶ単一実装(`state:queued` 等のフィルタが TUI と
// web で同じ意味を持つ): closed → deferred → queued の優先順で判定し、issue
// 状態が取れないものは unknown。
func SyntheticTmuxState(issueState string, blocked bool) string {
	switch {
	case strings.EqualFold(issueState, "CLOSED"):
		return "closed"
	case blocked:
		return "deferred"
	case strings.EqualFold(issueState, "OPEN"):
		return "queued"
	default:
		return "unknown"
	}
}

type livePaneKey struct {
	backend   backend.Name
	workspace string
	pane      string
	session   string
	socket    string
}

func indexLivePanes(panes []backend.LivePane) map[livePaneKey]backend.LivePane {
	live := make(map[livePaneKey]backend.LivePane, len(panes))
	for _, pane := range panes {
		live[livePaneKeyForObservation(pane)] = pane
	}
	return live
}

func livePaneKeyForObservation(pane backend.LivePane) livePaneKey {
	key := livePaneKey{
		backend: backend.NormalizeName(pane.Ref.Backend),
		pane:    pane.Ref.Pane,
	}
	if key.backend == backend.Herdr {
		key.workspace = pane.Ref.Workspace
		key.session = strings.TrimSpace(pane.SessionID)
		key.socket = strings.TrimSpace(pane.SocketPath)
	}
	return key
}

func livePaneKeyForState(pane state.Pane) livePaneKey {
	key := livePaneKey{
		backend: backend.NormalizeName(pane.Backend),
		pane:    pane.PaneID,
	}
	if key.backend == backend.Herdr {
		key.workspace = pane.HerdrWorkspaceID
		key.session = strings.TrimSpace(pane.HerdrSession)
		key.socket = strings.TrimSpace(pane.HerdrSocketPath)
	}
	return key
}

func observationRouteForState(pane state.Pane) backend.ObservationRoute {
	name := backend.NormalizeName(pane.Backend)
	route := backend.ObservationRoute{Backend: name}
	if name == backend.Herdr {
		route.SessionID = strings.TrimSpace(pane.HerdrSession)
		route.SocketPath = strings.TrimSpace(pane.HerdrSocketPath)
	}
	return route
}

// livePaneForState reports whether a recorded pane is live and returns the
// matching backend-neutral observation. Runtime and pane identity must match.
// Tmux agent panes accept their recorded worktree or a descendant, while herdr
// requires exact worktree provenance (or exact saved cwd for generic
// workspaces). Tmux shell panes use ShellKey because their repo-root path is too
// broad to protect against pane-id reuse.
func livePaneForState(live map[livePaneKey]backend.LivePane, pane state.Pane) (backend.LivePane, bool) {
	if pane.PaneID == "" {
		return backend.LivePane{}, false
	}
	name := backend.NormalizeName(pane.Backend)
	cur, ok := live[livePaneKeyForState(pane)]
	if !ok {
		return backend.LivePane{}, false
	}
	switch name {
	case backend.Herdr:
		return cur, herdrIdentityMatches(pane, cur)
	case backend.Tmux:
		if pane.IsShell() || pane.ShellKey != "" {
			return cur, pane.ShellKey != "" && cur.ShellKey == pane.ShellKey
		}
		worktree := pane.WorktreePath
		if strings.TrimSpace(worktree) == "" {
			return cur, true
		}
		wt := filepath.Clean(worktree)
		if optionPath := strings.TrimSpace(cur.WorktreePath); optionPath != "" {
			opt := filepath.Clean(optionPath)
			return cur, opt == wt || strings.HasPrefix(opt, wt+string(filepath.Separator))
		}
		cp := filepath.Clean(cur.CurrentPath)
		return cur, cp == wt || strings.HasPrefix(cp, wt+string(filepath.Separator))
	default:
		return backend.LivePane{}, false
	}
}

// herdrIdentityMatches keeps runtime routing, terminal identity, logical agent
// identity, and worktree ownership as separate checks. A generic row and
// observation may both omit an agent, leaving AgentState unknown. Once the row
// records a logical conversation, a missing or different agent identity fails
// closed. foreground_cwd is deliberately absent from LivePane and therefore
// cannot become liveness evidence.
func herdrIdentityMatches(pane state.Pane, current backend.LivePane) bool {
	if strings.TrimSpace(pane.HerdrTerminalID) == "" ||
		strings.TrimSpace(current.TerminalID) == "" ||
		pane.HerdrTerminalID != current.TerminalID {
		return false
	}
	storedAgent := strings.TrimSpace(pane.Agent) != "" || pane.HerdrAgentID != "" || pane.HerdrAgentSession != nil
	observedAgent := current.AgentPresent || current.AgentID != "" || current.AgentSession != nil
	if storedAgent != observedAgent {
		return false
	}
	if storedAgent {
		if pane.HerdrAgentID == "" || pane.HerdrAgentSession == nil || !current.AgentPresent ||
			pane.HerdrAgentID != current.AgentID || !agentSessionRefsEqual(pane.HerdrAgentSession, current.AgentSession) {
			return false
		}
	}

	storedPath := strings.TrimSpace(pane.WorktreePath)
	if storedPath == "" {
		return false
	}
	storedPath = filepath.Clean(storedPath)
	storedRepoKey := pane.HerdrRepoKey
	repoKey := current.RepoKey
	worktreePath := strings.TrimSpace(current.WorktreePath)
	projectRoot := strings.TrimSpace(current.ProjectRoot)
	if strings.TrimSpace(repoKey) != "" || worktreePath != "" || projectRoot != "" {
		// herdrrun rejects partial worktree provenance. Keep this boundary
		// fail-closed for tests and alternative collectors too.
		return strings.TrimSpace(storedRepoKey) != "" && repoKey == storedRepoKey && worktreePath != "" && projectRoot != "" && filepath.Clean(worktreePath) == storedPath
	}

	// Generic herdr workspaces have no worktree provenance. Only the saved cwd
	// may support the match; subdirectories and foreground cwd are not accepted.
	currentPath := strings.TrimSpace(current.CurrentPath)
	return strings.TrimSpace(storedRepoKey) == "" && currentPath != "" && filepath.Clean(currentPath) == storedPath
}

// herdrRowUnsupported identifies persisted rows that predate the authoritative
// identity baseline required by the observation-only v1 matcher. These rows are
// not stale: without the saved baseline there is no prior terminal or logical
// conversation to compare. They remain explicitly unsupported and are never
// filled from the current snapshot, which would adopt a potentially reused
// public pane ID.
func herdrRowUnsupported(pane state.Pane) bool {
	if backend.NormalizeName(pane.Backend) != backend.Herdr || strings.TrimSpace(pane.PaneID) == "" {
		return false
	}
	if strings.TrimSpace(pane.HerdrWorkspaceID) == "" ||
		strings.TrimSpace(pane.HerdrTerminalID) == "" ||
		strings.TrimSpace(pane.HerdrSession) == "" ||
		strings.TrimSpace(pane.HerdrSocketPath) == "" ||
		strings.TrimSpace(pane.WorktreePath) == "" {
		return true
	}
	storedAgentID := strings.TrimSpace(pane.HerdrAgentID) != ""
	storedAgentSession := pane.HerdrAgentSession != nil
	if storedAgentID != storedAgentSession || (storedAgentSession && !pane.HerdrAgentSession.Valid()) {
		return true
	}
	if !storedAgentID && strings.TrimSpace(pane.Agent) != "" {
		return true
	}
	return false
}

func agentSessionRefsEqual(stored, current *backend.AgentSessionRef) bool {
	if stored == nil || current == nil {
		return stored == nil && current == nil
	}
	return stored.Valid() && current.Valid() && *stored == *current
}

func paneAlive(live map[livePaneKey]backend.LivePane, pane state.Pane) bool {
	_, alive := livePaneForState(live, pane)
	return alive
}

// runtimeStateOf preserves the established TUI state strings across backends
// and adds "unsupported" for a herdr row that has no authoritative persisted
// identity baseline. Such a row is distinct from a stale row whose saved
// identity was actually compared and rejected.
func runtimeStateOf(paneID string, runtimeDegraded, runtimeUnsupported, alive bool) string {
	switch {
	case strings.TrimSpace(paneID) == "":
		return "-"
	case alive:
		return "live"
	case runtimeUnsupported:
		return "unsupported"
	case runtimeDegraded:
		return "unknown"
	default:
		return "stale"
	}
}

// compatibilityTmuxState keeps the pre-runtime-alias value set stable for
// existing snapshot consumers. A legacy consumer cannot distinguish an
// unsupported backend row from an unobservable one, so project it as unknown;
// RuntimeState carries the precise backend-neutral value.
func compatibilityTmuxState(runtimeState string) string {
	if runtimeState == "unsupported" {
		return "unknown"
	}
	return runtimeState
}

// knownAgentStates は @fanout_agent_state の許可語彙(6 値契約:
// docs/competitive-herdr.ja.md 提案 A + Codex Plan Mode の plan)。TUI の
// agentStateGlyphs・web の AGENT_STATE_CLASSES はこの集合と同じキーを持つ。
var knownAgentStates = map[string]bool{
	"running": true,
	"working": true,
	"plan":    true,
	"blocked": true,
	"idle":    true,
	"done":    true,
}

// normalizeAgentState は tmux pane user option @fanout_agent_state の値を
// PaneView.AgentState に正規化する。running と done は fanout の起動ラッパー
// (tmuxrun.BuildPaneLaunchCommand)が agent の起動前後に設定し、working /
// plan / blocked / idle は agent hooks が明示信号として設定する。それ以外の値
// (未設定 = 旧版 fanout やラッパー外で起動した pane、あるいは pane 内プロセスが
// 偽装した文字列)は ""(不明)に落とす。
// #{pane_current_command} ヒューリスティックは使えない: 非対話 sh -lc
// ラッパー経由の agent はラッパーと同一プロセスグループで動き、tmux は agent
// 実行中もラッパーシェル名を報告するため、fanout 起動 pane では常に「done」
// 誤判定になる。capture-pane からの状態推定もしない(ペイン内容は攻撃可能面)。
func normalizeAgentState(raw string) string {
	if state := strings.TrimSpace(raw); knownAgentStates[state] {
		return state
	}
	return ""
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

// countsInRollup は rollup に算入する行かどうか。PR が merge されないまま
// CLOSED になった synthetic 子(not-planned 等)は除外する(行としては表示
// する): 算入すると Pending に永久に残り、セッション進捗が 100% / AllMerged
// に到達できなくなるため。merged PR を持つ CLOSED 子は「pane なしで完了した
// 作業」として Total / Merged に算入する。記録 pane は従来どおり常に算入。
func countsInRollup(pv PaneView) bool {
	if isPaneOnly(pv) {
		return false
	}
	if !pv.NotStarted {
		return true
	}
	if !strings.EqualFold(pv.IssueState, "CLOSED") || pv.HasMergedPR {
		return true
	}
	// CLOSED かつ merged PR なし: PR lookup で「merged なし」を確認できたとき
	// だけ rollup から落とす(not-planned 等の確定 close)。PR 状態が未確認
	// (キャッシュ miss/失敗で wave graph 状態へ fallback)なら算入し、一時的な
	// gh 失敗で session が 100%/AllMerged に見えるのを防ぐ。
	return pv.closeUnconfirmed
}

func accumulate(r *Rollup, pv PaneView) {
	if isPaneOnly(pv) {
		if pv.Alive {
			r.Live++
		}
		return
	}
	r.Total++
	if pv.HasMergedPR {
		r.Merged++
	}
	if pv.Alive {
		r.Live++
	}
	switch pv.AgentState {
	case "running", "working", "plan":
		// active 集合: agent が手を動かしている状態。blocked / idle / done は
		// 「進行中」ではないので数えない。
		r.Running++
	}
	if pv.Blocked {
		r.Blocked++
	}
	if pv.NotStarted && pv.TmuxState != "closed" {
		// NotStarted は「まだ起動しうる作業」のカウンタ: merged 済みで閉じた
		// synthetic 子は Total/Merged には入るがここには数えない。
		r.NotStarted++
	}
}

func isPaneOnly(pv PaneView) bool {
	return pv.Kind == state.PaneKindShell || pv.Kind == state.PaneKindAttachedAgent
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

// DerivePane computes display/filter/sort helper values from the canonical
// PaneView fields. parent is passed separately because PaneView deliberately
// does not repeat its containing Session parent on the public wire shape.
func DerivePane(projectRoot, parent string, pv PaneView) PaneDerived {
	runtimeState := firstNonEmpty(pv.RuntimeState, pv.TmuxState)
	runtimeTitle := firstNonEmpty(pv.RuntimeTitle, pv.TmuxTitle)
	name := paneName(pv)
	prSummary, prNum, prState := summarizePR(pv.PRs)
	ci := paneCI(pv)
	waveBadge := WaveBadge(pv.Wave, pv.Blocked)
	waveText := WaveText(pv.WaveLabel, waveBadge)
	dependencyWave := DependencyWaveText(pv.Wave)
	blockersText := BlockersText(pv.Blockers)
	openBlockers := OpenBlockerCount(pv.Blockers)
	diffTotal, diffParsed := ParseDiffTotal(pv.DiffSummary)
	relWorktree := RelativePath(projectRoot, pv.WorktreePath)
	backendName := ""
	if !pv.NotStarted {
		backendName = strings.ToLower(strings.TrimSpace(string(backend.NormalizeName(pv.Backend))))
	}

	filterValues := map[string]string{
		"state":   strings.ToLower(strings.TrimSpace(firstMatchingState(runtimeState, pv.IssueState))),
		"backend": backendName,
		"run":     strings.ToLower(strings.TrimSpace(pv.AgentState)),
		"agent":   strings.ToLower(strings.TrimSpace(pv.Agent)),
		"wave":    strings.ToLower(strings.TrimSpace(firstNonEmpty(strconv.Itoa(pv.Wave), pv.WaveLabel, dependencyWave))),
		"ci":      strings.ToLower(strings.TrimSpace(ci)),
		"dirty":   yesNo(pv.DirtyState == "dirty"),
		"live":    yesNo(pv.Alive),
		"issue":   issueFilterValue(pv),
		"pr":      strings.ToLower(firstNonEmpty(prState, "none")),
	}
	if pv.Wave <= 0 {
		filterValues["wave"] = strings.ToLower(strings.TrimSpace(firstNonEmpty(pv.WaveLabel, dependencyWave)))
	}

	filterText := strings.ToLower(strings.Join([]string{
		parent,
		pv.TaskID,
		pv.Kind,
		"#" + strconv.Itoa(pv.IssueNum),
		strconv.Itoa(pv.IssueNum),
		name,
		pv.Slug,
		pv.PaneID,
		backendName,
		runtimeState,
		runtimeTitle,
		pv.TmuxState,
		pv.TmuxTitle,
		pv.AgentState,
		pv.IssueState,
		prSummary,
		ci,
		pv.BranchName,
		pv.WorktreePath,
		relWorktree,
		pv.DiffSummary,
		pv.DirtyState,
		pv.WorktreeErr,
		pv.Agent,
		pv.WaveLabel,
		waveBadge,
		waveText,
		blockersText,
		dependencyWave,
		pv.CreatedAt,
		pv.Prompt,
	}, "\n"))

	canFocus := backend.NormalizeName(pv.Backend) == backend.Tmux && canFocusPane(pv.PaneID, runtimeState)
	return PaneDerived{
		Name:             name,
		PRSummary:        prSummary,
		PrimaryPRNumber:  prNum,
		PrimaryPRState:   prState,
		CI:               ci,
		WaveBadge:        waveBadge,
		WaveText:         waveText,
		DependencyWave:   dependencyWave,
		BlockersText:     blockersText,
		OpenBlockers:     openBlockers,
		DiffTotal:        diffTotal,
		DiffParsed:       diffParsed,
		FilterText:       filterText,
		FilterValues:     filterValues,
		CanFocus:         canFocus,
		CanPeek:          canFocus,
		WorktreeRelative: relWorktree,
		Sort: PaneSortKeys{
			IssueNum: pv.IssueNum,
			Name:     strings.ToLower(name),
			Agent:    strings.ToLower(pv.Agent),
			Wave:     waveSortKey(pv.Wave),
			Blockers: openBlockers,
			Branch:   strings.ToLower(pv.BranchName),
			Diff:     diffSortKey(diffTotal, diffParsed),
			Dirty:    dirtyRank(pv.DirtyState),
			CI:       ciRank(ci),
			Tmux:     tmuxRank(pv),
			State:    strings.ToLower(pv.IssueState),
			PR:       prRank(prState),
		},
	}
}

func paneName(pv PaneView) string {
	return firstNonEmpty(pv.DisplayName, pv.Slug, pv.TaskID, "#"+strconv.Itoa(pv.IssueNum))
}

func issueFilterValue(pv PaneView) string {
	if pv.IssueNum <= 0 && strings.TrimSpace(pv.TaskID) != "" {
		return strings.ToLower(strings.TrimSpace(pv.TaskID))
	}
	return strconv.Itoa(pv.IssueNum)
}

func summarizePR(prs []ghissue.PRRef) (summary string, number int, state string) {
	pr, ok := ghissue.PrimaryPR(prs)
	if !ok {
		return "-", 0, "none"
	}
	return "#" + strconv.Itoa(pr.Number) + " " + dash(pr.DisplayState()), pr.Number, strings.ToLower(pr.State)
}

func paneCI(pv PaneView) string {
	if strings.TrimSpace(pv.CIStatus) == "" || pv.CIStatus == "-" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(pv.CIStatus))
}

// WaveBadge returns the compact TUI wave badge: WN ready / WN blocked.
func WaveBadge(wave int, blocked bool) string {
	if wave <= 0 {
		return "-"
	}
	state := "ready"
	if blocked {
		state = "blocked"
	}
	return fmt.Sprintf("W%d %s", wave, state)
}

// WaveText joins the human label and computed badge without duplicate dashes.
func WaveText(label, badge string) string {
	parts := nonDashStrings(label, badge)
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func DependencyWaveText(wave int) string {
	if wave <= 0 {
		return ""
	}
	return fmt.Sprintf("wave%d", wave)
}

func BlockersText(rows []blockers.Status) string {
	if len(rows) == 0 {
		return "-"
	}
	return blockers.FormatStatuses(rows)
}

func OpenBlockerCount(rows []blockers.Status) int {
	n := 0
	for _, row := range rows {
		if row.State == "OPEN" {
			n++
		}
	}
	return n
}

func ParseDiffTotal(summary string) (int, bool) {
	s := strings.TrimSpace(summary)
	if !strings.HasPrefix(s, "+") {
		return 0, false
	}
	addPart, delPart, ok := strings.Cut(s[1:], "/-")
	if !ok {
		return 0, false
	}
	add, errA := strconv.Atoi(addPart)
	del, errD := strconv.Atoi(delPart)
	if errA != nil || errD != nil {
		return 0, false
	}
	return add + del, true
}

func RelativePath(root, path string) string {
	if root == "" || path == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return path
	}
	return rel
}

func firstMatchingState(tmuxState, issueState string) string {
	if strings.TrimSpace(tmuxState) != "" && tmuxState != "-" {
		return tmuxState
	}
	return issueState
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func waveSortKey(wave int) int {
	if wave <= 0 {
		return 99
	}
	return wave
}

func diffSortKey(total int, parsed bool) int {
	if !parsed {
		return -1
	}
	return total
}

func dirtyRank(state string) int {
	switch state {
	case "clean":
		return 0
	case "dirty":
		return 1
	case "unknown":
		return 2
	default:
		return 3
	}
}

func ciRank(ci string) int {
	switch ci {
	case "fail":
		return 0
	case "pending":
		return 1
	case "pass":
		return 2
	default:
		return 3
	}
}

func tmuxRank(pv PaneView) int {
	switch {
	case pv.Alive:
		return 0
	case pv.NotStarted:
		return 2
	default:
		return 1
	}
}

func prRank(state string) int {
	switch strings.ToUpper(state) {
	case "MERGED":
		return 0
	case "OPEN":
		return 1
	case "CLOSED":
		return 2
	default:
		return 3
	}
}

func canFocusPane(paneID, tmuxState string) bool {
	return strings.TrimSpace(paneID) != "" && tmuxState != "stale" && tmuxState != "-"
}

func nonDashStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "-" || seen[value] {
			continue
		}
		out = append(out, value)
		seen[value] = true
	}
	return out
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
