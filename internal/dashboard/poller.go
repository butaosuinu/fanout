package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
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
// per-issue PR state plus the per-parent wave/blocker graph.
// sessionview.GH satisfies it; tests inject a fake.
type GHProvider interface {
	IssuePRs(num int) (string, []ghissue.PRRef, error)
	Waves(parent string, recordedNums []int) (map[int]sessionview.WaveInfo, error)
}

type ghCacheEntry struct {
	state string
	prs   []ghissue.PRRef
	err   error
}

// waveCacheEntry is one parent's cached wave graph. FetchWaveGraph keeps
// partial results next to a joined error, so both fields can be non-zero.
type waveCacheEntry struct {
	info map[int]sessionview.WaveInfo
	err  error
}

// poller owns the authoritative latest Snapshot. A cheap tick reloads
// state.json + tmux liveness (~2s); a throttled GitHub tick refreshes per-issue
// PR state (~20s), with the costlier per-parent wave-graph phase further
// throttled to waveInterval (~60s) to respect the gh API budget.
// sessionview.Build reads gh only through the cache, so the
// cheap tick never blocks on the network. Snapshots broadcast over SSE only when
// their content (ignoring the timestamp) actually changes.
type poller struct {
	projectRoot string
	hub         *hub

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

	cacheMu   sync.Mutex
	cache     map[int]ghCacheEntry
	waveCache map[string]waveCacheEntry // keyed by normalized parent
}

func newPollerBase(projectRoot string, h *hub) *poller {
	return &poller{
		projectRoot:   projectRoot,
		hub:           h,
		cheapInterval: defaultCheapInterval,
		ghInterval:    defaultGHInterval,
		waveInterval:  defaultWaveInterval,
		cache:         map[int]ghCacheEntry{},
		waveCache:     map[string]waveCacheEntry{},
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
	// panes, network latency) never starves the cheap state/tmux ticker — pane
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
	// never delays startup, then populate PR state. ctx-guarded so Ctrl-C during a
	// slow first refresh still exits promptly.
	if ctx.Err() == nil {
		p.ensureResolved()
		p.refreshGH()
		p.rebuildAndBroadcast()
	}
	ghT := time.NewTicker(p.ghInterval)
	defer ghT.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ghT.C:
			p.refreshGH()
			p.rebuildAndBroadcast()
		}
	}
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

// wavesFromCache mirrors issuePRsFromCache for the per-parent wave graph: a
// sticky GitHub failure short-circuits to (nil, err), and a cache miss is
// (nil, nil) — "not fetched yet", which Build renders as zero-valued wave
// fields without marking GitHub degraded.
func (p *poller) wavesFromCache(parent string) (map[int]sessionview.WaveInfo, error) {
	if _, _, ghErr := p.ghIdentity(); ghErr != nil {
		return nil, ghErr
	}
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	e, ok := p.waveCache[normalizeParent(parent)]
	if !ok {
		return nil, nil // cache miss: unknown, not degraded
	}
	return e.info, e.err
}

func (p *poller) refreshGH() {
	_, gh, _ := p.ghIdentity()
	if gh == nil {
		return
	}
	store, err := state.LoadProject(p.projectRoot)
	if err != nil {
		return
	}
	for _, num := range distinctIssueNums(store) {
		st, prs, err := gh.IssuePRs(num)
		p.cacheMu.Lock()
		p.cache[num] = ghCacheEntry{state: st, prs: prs, err: err}
		p.cacheMu.Unlock()
	}
	// Phase 2: one wave-graph fetch per distinct normalized parent. The recorded
	// issue numbers seed FetchWaveGraph's child set for @manual/Project parents
	// and backfill unlinked children for numeric ones.
	// This phase runs on its own, slower cadence (waveInterval) because each
	// pass is many gh calls per parent — coupling it to every PR tick would
	// exhaust GitHub's hourly API budget. The first refresh always runs it so
	// the UI populates immediately. Child bodies are deliberately refetched on
	// each pass (no cross-cycle body cache) so edits to "## Blocked by"
	// sections still show up; the slower cadence bounds that cost.
	if !p.lastWaveRefresh.IsZero() && time.Since(p.lastWaveRefresh) < p.waveInterval {
		return
	}
	p.lastWaveRefresh = time.Now()
	numsByParent := recordedNumsByParent(store)
	for _, parent := range slices.Sorted(maps.Keys(numsByParent)) {
		info, err := gh.Waves(parent, numsByParent[parent])
		p.cacheMu.Lock()
		if err != nil {
			// Partial failure: keep last-known rows instead of dropping
			// previously confirmed blockers until the next clean pass.
			info = mergeDegradedWaveInfos(p.waveCache[parent].info, info)
		}
		p.waveCache[parent] = waveCacheEntry{info: info, err: err}
		p.cacheMu.Unlock()
	}
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
		LoadState:    sessionview.StateLoader(p.projectRoot),
		LivePanes:    sessionview.LivePanes(),
		IssuePRs:     p.issuePRsFromCache,
		Waves:        p.wavesFromCache,
		WorktreeStat: sessionview.GitWorktreeStat(p.projectRoot),
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

// normalizeParent canonicalizes numeric parents via an Atoi round-trip so
// "0100" and "100" share one wave-cache entry, mirroring how state's
// parentMatches treats them as the same parent. Non-numeric parents
// (@manual, Project URLs) pass through unchanged.
func normalizeParent(parent string) string {
	if n, err := strconv.Atoi(parent); err == nil {
		return strconv.Itoa(n)
	}
	return parent
}

// recordedNumsByParent groups each distinct normalized parent in state to the
// sorted positive issue numbers recorded under it. Synthetic pane numbers
// (IssueNum <= 0, e.g. @manual rows) stay out — FetchWaveGraph skips them
// anyway — but their parent still gets an (empty) entry so its wave cache
// resolves to a hit instead of a perpetual miss.
func recordedNumsByParent(store state.Store) map[string][]int {
	grouped := map[string]map[int]bool{}
	for _, p := range store.Panes {
		key := normalizeParent(p.Parent)
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
