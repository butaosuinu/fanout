package dashboard

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/butaosuinu/fanout/internal/app/sessionbinding"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	defaultCheapInterval = 2 * time.Second
	defaultGHInterval    = 20 * time.Second
	// defaultWaveInterval throttles the wave-graph phase to every third gh
	// tick. One wave pass costs many gh calls per parent (parent body +
	// sub-issue list + per-child body hydration + out-of-set blocker states),
	// so running it at PR cadence would burn through GitHub's hourly API
	// budget on busy boards and leave the dashboard permanently rate-limited.
	defaultWaveInterval = 3 * defaultGHInterval
)

// GHProvider is the GitHub fetch surface the poller throttles and caches:
// per-issue PR state, per-branch PR refs for task rows, plus the per-parent
// wave/blocker graph (child set included, so not-started children can render
// as synthetic rows).
// sessionview.GH satisfies it; tests inject a fake.
type GHProvider interface {
	IssuePRsBatch(nums []int) (map[int]ghissue.IssueSnapshot, error)
	BranchPRs(branch string) ([]ghissue.PRRef, error)
	Waves(parent string, recordedNums []int) (sessionview.WaveGraph, error)
}

type ghCacheEntry struct {
	state string
	prs   []ghissue.PRRef
	err   error
}

type branchPRCacheEntry struct {
	prs []ghissue.PRRef
	err error
}

// waveCacheEntry is one parent's cached wave graph. FetchWaveGraph keeps
// partial results next to a joined error, so both fields can be non-zero.
// attempted records the recorded issue numbers the fetch was asked about, so
// the throttle bypass fires once per newly recorded issue without retrying
// permanently failing lookups every tick.
type waveCacheEntry struct {
	graph     sessionview.WaveGraph
	err       error
	attempted map[int]bool
}

// poller owns the authoritative latest Snapshot. A cheap tick reloads
// state.json + runtime liveness (~2s); a throttled GitHub tick refreshes per-issue
// PR state (~20s), with the costlier per-parent wave-graph phase further
// throttled to waveInterval (~60s) to respect the gh API budget.
// sessionview.Build reads gh only through the cache, so the
// cheap tick never blocks on the network. Snapshots broadcast over SSE only when
// their content (ignoring the timestamp) actually changes.
type poller struct {
	projectRoot string
	hub         *hub
	listLive    func() ([]backend.LivePane, error)

	cheapInterval time.Duration
	ghInterval    time.Duration
	waveInterval  time.Duration

	// lastWaveRefresh tracks when the wave-graph phase last ran. It is only
	// touched by refreshGH, which runs solely on the single gh goroutine
	// (tests call it directly, also single-threaded), so it needs no lock.
	lastWaveRefresh time.Time

	// ghMu guards the GitHub identity (repo/gh/ghErr) so the deferred resolution
	// running on the gh goroutine can publish it while the cheap ticker reads it.
	ghMu     sync.Mutex
	repo     string
	gh       GHProvider // nil when gh is unavailable
	ghErr    error      // sticky failure (e.g. repo unresolved) -> Degraded.GitHub
	resolve  func() (string, GHProvider, error)
	resolved bool

	mu         sync.RWMutex
	latest     sessionview.Snapshot
	latestJSON []byte
	lastKey    []byte

	// worktreeStat is built once: it owns the untracked-file cache that keeps
	// the 2-second tick off a per-file git process for every un-ignored file.
	worktreeStat func(path, baseRef string) (sessionview.WorktreeStat, error)

	cacheMu     sync.Mutex
	cache       map[int]ghCacheEntry
	branchCache map[string]branchPRCacheEntry
	waveCache   map[string]waveCacheEntry // keyed by normalized parent

	// refreshNow lets a completed merge pull the next GitHub tick forward
	// instead of leaving the row rendering its pre-merge state for a full
	// ghInterval. Buffered depth 1: a kick already queued covers any later one.
	refreshNow chan struct{}
}

func newPollerBase(projectRoot string, h *hub) *poller {
	return &poller{
		projectRoot:   projectRoot,
		hub:           h,
		worktreeStat:  sessionview.GitWorktreeStat(projectRoot),
		cheapInterval: defaultCheapInterval,
		ghInterval:    defaultGHInterval,
		waveInterval:  defaultWaveInterval,
		cache:         map[int]ghCacheEntry{},
		branchCache:   map[string]branchPRCacheEntry{},
		waveCache:     map[string]waveCacheEntry{},
		refreshNow:    make(chan struct{}, 1),
	}
}

// newPoller builds a poller whose GitHub identity is already known (repo/gh
// resolved up front). Used by tests that inject a fake provider directly.
func newPoller(repo, projectRoot string, gh GHProvider, ghErr error, h *hub) *poller {
	p := newPollerBase(projectRoot, h)
	p.repo, p.gh, p.ghErr, p.resolved = repo, gh, ghErr, true
	return p
}

// newLazyPoller defers GitHub resolution to the background gh goroutine. The
// HTTP server can bind and paint the state-only view immediately even when
// `gh repo view` is slow; PR data and the repo label fill in once resolve runs.
// A nil resolve disables the GitHub tier (state-only, no degradation).
func newLazyPoller(projectRoot string, resolve func() (string, GHProvider, error), h *hub) *poller {
	p := newPollerBase(projectRoot, h)
	p.resolve = resolve
	return p
}

// Start publishes an immediate state-only snapshot (so the server can serve and
// the browser can paint without waiting on the network) and launches the polling
// loop. The first GitHub refresh runs inside the loop goroutine, not here, so a
// slow gh on a large repo never blocks the HTTP server from coming up or Ctrl-C
// from being handled. The loop stops and closes all SSE subscribers when ctx is
// canceled.
func (p *poller) Start(ctx context.Context) {
	p.rebuildAndBroadcast()
	go p.loop(ctx)
}

func (p *poller) loop(ctx context.Context) {
	// GitHub refresh runs on its own goroutine so a slow `gh api graphql` (many
	// panes, network latency) never starves the cheap state/runtime ticker — pane
	// liveness and state changes keep updating every cheapInterval regardless.
	go p.ghLoop(ctx)

	cheap := time.NewTicker(p.cheapInterval)
	defer cheap.Stop()
	for {
		select {
		case <-ctx.Done():
			p.hub.closeAll()
			return
		case <-cheap.C:
			p.rebuildAndBroadcast()
		}
	}
}

func (p *poller) ghLoop(ctx context.Context) {
	// Resolve the repo here (not before the server binds) so a slow `gh repo view`
	// never delays startup, then populate PR state. Skip the whole GitHub tier
	// while nobody is subscribed; the cheap state/runtime ticker keeps running.
	// ctx-guarded so Ctrl-C during a slow first refresh still exits promptly.
	if ctx.Err() == nil {
		p.runGHTick()
	}
	ghT := time.NewTicker(p.ghInterval)
	defer ghT.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ghT.C:
			p.runGHTick()
		case <-p.refreshNow:
			p.runGHTick()
		}
	}
}

// requestGHRefresh asks the gh goroutine to run a tick now. It never blocks, and
// reports whether a kick is now queued: a full buffer means one is already
// pending, which is the same outcome for the caller but not the same claim, so
// the two are not conflated on the wire.
//
// It deliberately does not drop the row's cached PR state. refreshGH refetches
// every recorded row on each tick, so invalidation buys no freshness — and it
// would open a window where the 2-second cheap ticker broadcasts the row with no
// PRs at all, which downstream reads as "this row lost its pull request".
// prSettled reports whether the latest snapshot shows this pull request merged
// or closed. It is how an unconfirmed merge stops blocking: once GitHub's answer
// arrives through the normal poll, the claim on that PR can go.
//
// repo is required: `Fixes owner/repo#N` puts pull requests from other
// repositories on a row, and numbers repeat across repositories. A merged #7
// somewhere else must not release the hold on this repository's #7.
func (p *poller) prSettled(repo string, number int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, session := range p.latest.Sessions {
		for i := range session.Panes {
			if settledPRRef(session.Panes[i].PRs, repo, number) {
				return true
			}
		}
	}
	return false
}

// prMergePending reports whether the latest snapshot shows GitHub still holding
// a merge for this pull request — an auto-merge armed, or an entry in the merge
// queue — and whether the pull request was found at all. The two are separate
// answers: "nothing pending" and "not in the snapshot" must not release the same
// hold.
func (p *poller) prMergePending(repo string, number int) (pending, found bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, session := range p.latest.Sessions {
		for i := range session.Panes {
			for _, pr := range session.Panes[i].PRs {
				if pr.Number == number && strings.EqualFold(pr.BaseRepo, repo) {
					return pr.AutoMerge || pr.Queued, true
				}
			}
		}
	}
	return false, false
}

func settledPRRef(prs []ghissue.PRRef, repo string, number int) bool {
	for _, pr := range prs {
		if pr.Number != number || !strings.EqualFold(pr.BaseRepo, repo) {
			continue
		}
		if strings.EqualFold(pr.State, "MERGED") || strings.EqualFold(pr.State, "CLOSED") ||
			pr.MergedAt != nil {
			return true
		}
	}
	return false
}

func (p *poller) requestGHRefresh() bool {
	select {
	case p.refreshNow <- struct{}{}:
		return true
	default:
		return false
	}
}

func (p *poller) runGHTick() {
	if p.hub.subscriberCount() == 0 && !p.hub.snapshotRecentlyRequested(time.Now()) {
		return
	}
	p.ensureResolved()
	p.refreshGH()
	p.rebuildAndBroadcast()
}

// ensureResolved runs the deferred GitHub resolution exactly once. It is only
// ever called from the single gh goroutine, so no double-resolution can occur.
// The (slow) resolve call runs WITHOUT ghMu held, so the cheap ticker's reads of
// repo/ghErr never block behind a stalled `gh repo view`.
func (p *poller) ensureResolved() {
	p.ghMu.Lock()
	if p.resolved || p.resolve == nil {
		p.ghMu.Unlock()
		return
	}
	resolve := p.resolve
	p.ghMu.Unlock()

	repo, gh, err := resolve()

	p.ghMu.Lock()
	p.repo, p.gh, p.ghErr, p.resolved = repo, gh, err, true
	p.ghMu.Unlock()
}

func (p *poller) ghIdentity() (repo string, gh GHProvider, ghErr error) {
	p.ghMu.Lock()
	defer p.ghMu.Unlock()
	return p.repo, p.gh, p.ghErr
}

func (p *poller) issuePRsFromCache(num int) (string, []ghissue.PRRef, error) {
	if _, _, ghErr := p.ghIdentity(); ghErr != nil {
		return "", nil, ghErr
	}
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	e, ok := p.cache[num]
	if !ok {
		return "", nil, nil // cache miss: unknown, not degraded
	}
	return e.state, e.prs, e.err
}

func (p *poller) branchPRsFromCache(branch string) ([]ghissue.PRRef, error) {
	if _, _, ghErr := p.ghIdentity(); ghErr != nil {
		return nil, ghErr
	}
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	e, ok := p.branchCache[branch]
	if !ok {
		return nil, nil // cache miss: unknown, not degraded
	}
	return e.prs, e.err
}

// wavesFromCache mirrors issuePRsFromCache for the per-parent wave graph: a
// sticky GitHub failure short-circuits to (zero, err), and a cache miss is
// (zero, nil) — "not fetched yet", which Build renders as zero-valued wave
// fields (and no synthetic rows) without marking GitHub degraded.
func (p *poller) wavesFromCache(parent string) (sessionview.WaveGraph, error) {
	if _, _, ghErr := p.ghIdentity(); ghErr != nil {
		return sessionview.WaveGraph{}, ghErr
	}
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	e, ok := p.waveCache[sessionview.NormalizeParent(parent)]
	if !ok {
		return sessionview.WaveGraph{}, nil // cache miss: unknown, not degraded
	}
	return e.graph, e.err
}

func (p *poller) refreshGH() {
	_, gh, _ := p.ghIdentity()
	if gh == nil {
		return
	}
	// Load the merged store so PR/wave refresh covers issues recorded in sibling
	// worktrees too; otherwise build() surfaces their panes but refreshGH never
	// fetches their issue/PR/wave state, leaving them permanently UNKNOWN.
	store, err := sessionview.MergedStateLoader(p.projectRoot, p.listLive)()
	if err != nil {
		return
	}
	// Phase 1: per-issue PR state for RECORDED issues only. Not-started
	// children get their PR state inside the wave pass below, at the slower
	// wave cadence — unioning them here would scale the 20s tick with the
	// total child count instead of the launched pane count and exhaust the
	// hourly GitHub API budget (the exact failure the wave throttle exists
	// to prevent).
	p.refreshIssuePRs(gh, distinctIssueNums(store))
	p.refreshBranchPRs(gh, distinctIssueLessBranches(store))
	// Phase 2: one wave-graph fetch per distinct normalized parent. The recorded
	// issue numbers seed FetchWaveGraph's child set for @manual/Project parents
	// and backfill unlinked children for numeric ones.
	// This phase runs on its own, slower cadence (waveInterval) because each
	// pass is many gh calls per parent — coupling it to every PR tick would
	// exhaust GitHub's hourly API budget. The first refresh always runs it so
	// the UI populates immediately. Child bodies are deliberately refetched on
	// each pass (no cross-cycle body cache) so edits to "## Blocked by"
	// sections still show up; the slower cadence bounds that cost.
	// Parents whose cache does not yet cover every recorded issue bypass the
	// throttle: a row recorded just after a wave pass would otherwise show no
	// wave/blocker data for a full waveInterval. Coverage is judged against
	// the nums the last fetch was ASKED about (attempted), not what it
	// returned, so a permanently failing lookup does not retry every tick.
	// Fully covered parents stay on the slow cadence.
	due := p.lastWaveRefresh.IsZero() || time.Since(p.lastWaveRefresh) >= p.waveInterval
	if due {
		p.lastWaveRefresh = time.Now()
	}
	numsByParent := recordedNumsByParent(store)
	// state から消えた親(--cleanup 済み等)の wave エントリは捨てる: その
	// session はもう build されないのに、子の PR fetch だけを永遠に駆動して
	// しまう(stale エントリは wave パスの対象外なので自然回復もしない)。
	p.pruneWaveCache(numsByParent)
	for _, parent := range slices.Sorted(maps.Keys(numsByParent)) {
		nums := numsByParent[parent]
		if len(nums) == 0 {
			continue
		}
		if !due {
			p.cacheMu.Lock()
			e, cached := p.waveCache[parent]
			covered := cached
			for _, num := range nums {
				if cached && !e.attempted[num] {
					covered = false
					break
				}
			}
			p.cacheMu.Unlock()
			if covered {
				continue
			}
		}
		graph, err := gh.Waves(parent, nums)
		attempted := make(map[int]bool, len(nums))
		for _, num := range nums {
			attempted[num] = true
		}
		p.cacheMu.Lock()
		if err != nil {
			// Partial failure: keep last-known rows instead of dropping
			// previously confirmed blockers (or already-discovered children)
			// until the next clean pass.
			graph = mergeDegradedWaveGraph(p.waveCache[parent].graph, graph)
		}
		p.cacheMu.Unlock()
		// Fetch the children's PR state BEFORE publishing the graph: the 2s
		// cheap ticker rebuilds concurrently, and publishing first would
		// broadcast torn frames (synthetic rows without PR/CI data) for every
		// child until the fetch loop catches up.
		p.refreshChildPRs(gh, graph, nums)
		p.cacheMu.Lock()
		p.waveCache[parent] = waveCacheEntry{graph: graph, err: err, attempted: attempted}
		p.cacheMu.Unlock()
	}
}

// refreshChildPRs fetches PR state for a parent's not-started children (those
// outside the recorded set), as part of the wave pass — i.e. at the wave
// cadence, not the 20s PR tick. A child is skipped only when BOTH the graph
// and the PR cache agree it is CLOSED: that state is terminal (the PR set can
// no longer change), and Build prefers the cached state, so skipping on the
// graph state alone would freeze a stale OPEN cache entry forever after an
// OPEN→CLOSED transition. A reopen flips child.State and re-enters here.
func (p *poller) refreshChildPRs(gh GHProvider, graph sessionview.WaveGraph, recorded []int) {
	rec := make(map[int]bool, len(recorded))
	for _, n := range recorded {
		rec[n] = true
	}
	var nums []int
	for _, child := range graph.Children {
		if child.Number <= 0 || rec[child.Number] {
			continue
		}
		if strings.EqualFold(child.State, "CLOSED") && p.cachedPRStateClosed(child.Number) {
			continue
		}
		nums = append(nums, child.Number)
	}
	slices.Sort(nums)
	p.refreshIssuePRs(gh, nums)
}

// cachedPRStateClosed reports whether num has a successful PR cache entry that
// already recorded the issue as CLOSED.
func (p *poller) cachedPRStateClosed(num int) bool {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	e, ok := p.cache[num]
	return ok && e.err == nil && strings.EqualFold(e.state, "CLOSED")
}

// pruneWaveCache drops wave entries whose parent no longer has state rows.
func (p *poller) pruneWaveCache(numsByParent map[string][]int) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	for parent := range p.waveCache {
		if _, ok := numsByParent[parent]; !ok {
			delete(p.waveCache, parent)
		}
	}
}

// refreshIssuePRs fetches issue/PR state in one provider call and stores
// success or failure independently for each requested issue.
func (p *poller) refreshIssuePRs(gh GHProvider, nums []int) {
	if len(nums) == 0 {
		return
	}
	snapshots, batchErr := gh.IssuePRsBatch(nums)
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	for _, num := range nums {
		snapshot, ok := snapshots[num]
		if ok {
			p.cache[num] = ghCacheEntry{state: snapshot.State, prs: snapshot.PRs}
			continue
		}
		err := batchErr
		if err == nil {
			err = fmt.Errorf("#%d: missing from issue PR batch", num)
		}
		p.cache[num] = ghCacheEntry{err: err}
	}
}

func (p *poller) refreshBranchPRs(gh GHProvider, branches []string) {
	for _, branch := range branches {
		prs, err := gh.BranchPRs(branch)
		p.cacheMu.Lock()
		p.branchCache[branch] = branchPRCacheEntry{prs: prs, err: err}
		p.cacheMu.Unlock()
	}
}

// mergeDegradedWaveGraph overlays a degraded wave fetch onto the previous
// cache entry: the per-child info merges via mergeDegradedWaveInfos and the
// child sets union by issue number, so previously discovered not-started
// children do not flicker out of the dashboard during a partial failure.
// Fresh data always wins, so the merge self-heals on the next clean refresh.
func mergeDegradedWaveGraph(previous, current sessionview.WaveGraph) sessionview.WaveGraph {
	return sessionview.WaveGraph{
		Children: mergeWaveChildren(previous.Children, current.Children),
		Info:     mergeDegradedWaveInfos(previous.Info, current.Info),
	}
}

// mergeWaveChildren unions two child sets by issue number: children dropped by
// the partial fetch are restored from the previous set, same-number children
// keep the fresh data. The result is sorted ascending for determinism.
func mergeWaveChildren(previous, current []ghissue.Issue) []ghissue.Issue {
	if len(previous) == 0 {
		return current
	}
	seen := make(map[int]bool, len(current))
	for _, child := range current {
		seen[child.Number] = true
	}
	out := slices.Clone(current)
	for _, child := range previous {
		if !seen[child.Number] {
			out = append(out, child)
		}
	}
	slices.SortFunc(out, func(a, b ghissue.Issue) int { return cmp.Compare(a.Number, b.Number) })
	return out
}

// mergeDegradedWaveInfos overlays a degraded wave fetch onto the previous
// cache entry: rows missing from the new graph are restored wholesale, and
// rows whose body hydration failed keep the previous (non-degraded) data.
// Fresh rows always win, so the merge self-heals on the next clean refresh.
func mergeDegradedWaveInfos(previous, current map[int]sessionview.WaveInfo) map[int]sessionview.WaveInfo {
	if len(previous) == 0 {
		return current
	}
	out := make(map[int]sessionview.WaveInfo, max(len(previous), len(current)))
	maps.Copy(out, previous)
	for num, info := range current {
		if prev, ok := previous[num]; ok && info.Degraded && !prev.Degraded {
			continue // keep prev (already copied)
		}
		out[num] = info
	}
	return out
}

func (p *poller) build() sessionview.Snapshot {
	repo, _, _ := p.ghIdentity()
	return sessionview.Build(repo, p.projectRoot, sessionview.Collectors{
		LoadState:    sessionbinding.StateLoader(p.projectRoot, p.listLive),
		ListLive:     p.listLive,
		IssuePRs:     p.issuePRsFromCache,
		BranchPRs:    p.branchPRsFromCache,
		Waves:        p.wavesFromCache,
		WorktreeStat: p.worktreeStat,
		Now:          time.Now,
	})
}

func (p *poller) rebuildAndBroadcast() {
	snap := p.build()
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	key := contentKey(snap)

	p.mu.Lock()
	changed := !bytes.Equal(key, p.lastKey)
	p.latest = snap
	p.latestJSON = data
	p.lastKey = key
	p.mu.Unlock()

	if changed {
		p.hub.broadcast(sseFrame(data))
	}
}

func (p *poller) snapshotJSON() []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.latestJSON == nil {
		return []byte("{}")
	}
	out := make([]byte, len(p.latestJSON))
	copy(out, p.latestJSON)
	return out
}

// contentKey is the change-detection key: the snapshot JSON with the timestamp
// blanked, so a tick that only advances GeneratedAt does not trigger a redundant
// broadcast/redraw.
// PaneView now carries the live tmux pane title, so a title change alone
// produces an SSE frame (intended — the dashboard surfaces titles live).
func contentKey(snap sessionview.Snapshot) []byte {
	snap.GeneratedAt = ""
	b, _ := json.Marshal(snap)
	return b
}

func distinctIssueNums(store state.Store) []int {
	seen := map[int]bool{}
	var nums []int
	for _, p := range store.Panes {
		if p.IssueNum <= 0 {
			continue
		}
		if !seen[p.IssueNum] {
			seen[p.IssueNum] = true
			nums = append(nums, p.IssueNum)
		}
	}
	slices.Sort(nums)
	return nums
}

func distinctIssueLessBranches(store state.Store) []string {
	seen := map[string]bool{}
	var branches []string
	for _, p := range store.Panes {
		branch, ok := sessionview.BranchPRLookupKey(p)
		if !ok {
			continue
		}
		if !seen[branch] {
			seen[branch] = true
			branches = append(branches, branch)
		}
	}
	slices.Sort(branches)
	return branches
}

// recordedNumsByParent groups each distinct normalized parent in state to the
// sorted positive issue numbers recorded under it. Synthetic pane numbers
// (IssueNum <= 0, e.g. @manual or plan task rows) stay out because they have
// no GitHub issue wave graph to fetch; refreshGH skips parents whose recorded
// set is empty.
func recordedNumsByParent(store state.Store) map[string][]int {
	grouped := map[string]map[int]bool{}
	for _, p := range store.Panes {
		key := sessionview.NormalizeParent(p.Parent)
		if grouped[key] == nil {
			grouped[key] = map[int]bool{}
		}
		if p.IssueNum > 0 {
			grouped[key][p.IssueNum] = true
		}
	}
	out := make(map[string][]int, len(grouped))
	for parent, set := range grouped {
		out[parent] = slices.Sorted(maps.Keys(set))
	}
	return out
}
