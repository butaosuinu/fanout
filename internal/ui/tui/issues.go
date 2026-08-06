package tui

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/blockers"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

type issueKey struct {
	Parent string
	Num    int
	TaskID string
	// Source is the owning worktree root for locally-scoped rows (plan tasks and
	// @manual panes), so the same (Parent, TaskID)/(Parent, Num) recorded in two
	// worktrees keys to distinct status — their branches and PR/CI state differ.
	// Empty for globally-stable GitHub issue rows, which aggregate across
	// worktrees (one status per issue number regardless of where it ran).
	Source string
}

type issueStatus struct {
	Title           string
	State           string
	PRs             []ghissue.PRRef
	Wave            int
	WaveLabel       string
	Blockers        string
	BlockerRows     []blockers.Status
	HasOpenBlockers bool
	// WaveDegraded marks rows whose body hydration failed in this refresh:
	// the wave/blocker fields were computed without the child's body, so an
	// empty Blockers value means "could not read", not "confirmed clear".
	WaveDegraded bool
}

type stateLoadedMsg struct {
	panes         []paneView
	at            time.Time
	err           error
	snapshotErr   error
	restoreErr    error
	restoreNotice string
	scheduleNext  bool
}

func (msg stateLoadedMsg) agentSnapshotOK() bool {
	if msg.snapshotErr != nil {
		return false
	}
	if msg.restoreErr != nil {
		return true
	}
	return msg.err == nil
}

type ghLoadedMsg struct {
	issues       map[issueKey]issueStatus
	at           time.Time
	err          error
	scheduleNext bool
}

type issueStatusProvider interface {
	IssuePRsBatch(nums []int) (map[int]ghissue.IssueSnapshot, error)
	BranchPRs(branch string) ([]ghissue.PRRef, error)
	Waves(parent string, recordedNums []int) (sessionview.WaveGraph, error)
}

type issueStatusResolver func(projectRoot string) (issueStatusProvider, error)

type issueWaveCacheEntry struct {
	graph     sessionview.WaveGraph
	err       error
	attempted map[int]bool
}

// issueStatusLoader owns GitHub identity and caches across TUI ticks. The
// mutex serializes event-driven refreshes with the scheduled GH loop, so two
// overlapping commands cannot both resolve the repo or race cache updates.
type issueStatusLoader struct {
	mu sync.Mutex

	waveInterval    time.Duration
	lastWaveRefresh time.Time
	resolve         issueStatusResolver
	gh              issueStatusProvider

	prCache     map[int]issueStatus
	branchCache map[string]branchStatus
	waveCache   map[string]issueWaveCacheEntry
}

func newIssueStatusLoader(waveInterval time.Duration) *issueStatusLoader {
	return &issueStatusLoader{
		waveInterval: waveInterval,
		resolve: func(projectRoot string) (issueStatusProvider, error) {
			gh, err := sessionview.ResolveGH(projectRoot)
			if err != nil {
				return nil, fmt.Errorf("resolve repo: %w", err)
			}
			return gh, nil
		},
		prCache:     map[int]issueStatus{},
		branchCache: map[string]branchStatus{},
		waveCache:   map[string]issueWaveCacheEntry{},
	}
}

type worktreeStatView struct {
	Diff  string
	Dirty string
	Err   string
}

func (m model) loadStateCmd(scheduleNext bool) tea.Cmd {
	projectRoot := m.opts.ProjectRoot
	issues := cloneIssueStatuses(m.issues)
	restorePanes := m.opts.RestorePanes
	listLive := m.opts.ListLive
	worktreeStat := m.worktreeStat
	return func() tea.Msg {
		var restoreNotice string
		var restoreErr error
		if restorePanes != nil {
			restoreNotice, restoreErr = restorePanes()
		}
		panes, err := loadPaneViews(projectRoot, issues, listLive, worktreeStat)
		return stateLoadedMsg{
			panes:         panes,
			at:            time.Now(),
			err:           errors.Join(restoreErr, err),
			snapshotErr:   err,
			restoreErr:    restoreErr,
			restoreNotice: restoreNotice,
			scheduleNext:  scheduleNext,
		}
	}
}

func (m model) loadGHCmd(scheduleNext bool) tea.Cmd {
	return m.loadGHCmdAt(scheduleNext, time.Time{})
}

func (m model) loadGHCmdAt(scheduleNext bool, tickAt time.Time) tea.Cmd {
	projectRoot := m.opts.ProjectRoot
	listLive := m.opts.ListLive
	loader := m.issueLoader
	return func() tea.Msg {
		if tickAt.IsZero() {
			tickAt = time.Now()
		}
		issues, err := loader.loadIssueStatuses(projectRoot, listLive, tickAt)
		return ghLoadedMsg{issues: issues, at: time.Now(), err: err, scheduleNext: scheduleNext}
	}
}

func (m model) activePaneTickCmd() tea.Cmd {
	if m.opts.ActivePane == nil {
		return nil
	}
	return tea.Tick(m.opts.ActivePaneInterval, func(t time.Time) tea.Msg { return activeTickMsg(t) })
}

func (m model) loadActivePaneCmd(scheduleNext bool) tea.Cmd {
	activePane := m.opts.ActivePane
	if activePane == nil {
		return nil
	}
	return func() tea.Msg {
		paneID, err := activePane()
		return activePaneMsg{paneID: paneID, err: err, scheduleNext: scheduleNext}
	}
}

func loadPaneViews(
	projectRoot string,
	issues map[issueKey]issueStatus,
	listLive func() ([]backend.LivePane, error),
	worktreeStat func(path, baseRef string) (sessionview.WorktreeStat, error),
) ([]paneView, error) {
	var stateErr error
	mergedState := sessionview.MergedStateLoader(projectRoot, listLive)
	loadState := func() (state.Store, error) {
		store, err := mergedState()
		stateErr = err
		return store, err
	}
	var backendErr error
	live := func() ([]backend.LivePane, error) {
		panes, err := listLive()
		backendErr = err
		return panes, err
	}
	if listLive == nil {
		live = nil
	}
	snap := sessionview.Build("", projectRoot, sessionview.Collectors{
		LoadState:    loadState,
		ListLive:     live,
		IssuePRs:     issuePRCollector(issues),
		Waves:        waveCollector(issues),
		WorktreeStat: worktreeStat,
		Now:          time.Now,
	})
	return paneViewsFromSnapshot(projectRoot, snap), errors.Join(stateErr, backendErr)
}

func (l *issueStatusLoader) loadIssueStatuses(
	projectRoot string,
	listLive func() ([]backend.LivePane, error),
	now time.Time,
) (map[issueKey]issueStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Merge sibling worktrees so issue/PR/wave status is fetched for Sessions
	// fanned out from another worktree too, matching loadPaneViews.
	store, err := sessionview.MergedStateLoader(projectRoot, listLive)()
	if err != nil {
		return nil, err
	}
	parents := recordedParents(store.Panes)
	taskRows := recordedTaskRows(store.Panes)
	statuses := map[issueKey]issueStatus{}
	if len(parents) == 0 && len(taskRows) == 0 {
		l.pruneWaveCache(nil)
		return statuses, nil
	}

	if l.gh == nil {
		gh, err := l.resolve(projectRoot)
		if err != nil {
			return nil, err
		}
		l.gh = gh
	}

	var loadErr error
	refreshedIssues := map[int]bool{}
	recordedNums := recordedIssueNums(store.Panes)
	for _, num := range recordedNums {
		refreshedIssues[num] = true
	}
	loadErr = errors.Join(loadErr, l.refreshIssuePRs(recordedNums))

	refreshedBranches := map[string]bool{}
	for _, task := range taskRows {
		if refreshedBranches[task.BranchName] {
			continue
		}
		refreshedBranches[task.BranchName] = true
		prs, err := l.gh.BranchPRs(task.BranchName)
		if err != nil {
			loadErr = errors.Join(loadErr, fmt.Errorf("branch %s: %w", task.BranchName, err))
			continue
		}
		l.branchCache[task.BranchName] = branchStatus{PRs: prs}
	}

	numsByParent := recordedIssueNumsByParent(store.Panes)
	l.pruneWaveCache(numsByParent)
	due := l.lastWaveRefresh.IsZero() || now.Sub(l.lastWaveRefresh) >= l.waveInterval
	if due && len(numsByParent) > 0 {
		l.lastWaveRefresh = now
	}
	for _, parent := range parents {
		nums := numsByParent[parent]
		entry, cached := l.waveCache[parent]
		if !due && waveAttemptCovered(entry, cached, nums) {
			continue
		}
		graph, err := l.gh.Waves(parent, nums)
		if err != nil {
			graph = mergeIssueWaveGraphs(entry.graph, graph)
		}
		prErr := l.refreshUnrecordedIssuePRs(graph, nums, refreshedIssues)
		attempted := make(map[int]bool, len(nums))
		for _, num := range nums {
			attempted[num] = true
		}
		l.waveCache[parent] = issueWaveCacheEntry{
			graph:     graph,
			err:       errors.Join(err, prErr),
			attempted: attempted,
		}
	}

	for _, parent := range parents {
		entry, ok := l.waveCache[parent]
		if !ok {
			continue
		}
		loadErr = errors.Join(loadErr, entry.err)
		for _, issue := range entry.graph.Children {
			cached, ok := l.prCache[issue.Number]
			if !ok {
				continue
			}
			if cached.State == "" {
				cached.State = issue.State
			}
			info := entry.graph.Info[issue.Number]
			cached.Title = issue.Title
			cached.Wave = info.Wave
			cached.WaveLabel = info.WaveLabel
			cached.Blockers = blockers.FormatStatuses(info.Blockers)
			cached.BlockerRows = info.Blockers
			cached.HasOpenBlockers = info.Blocked
			cached.WaveDegraded = info.Degraded
			statuses[keyForIssue(parent, issue.Number)] = cached
		}
	}
	for _, task := range taskRows {
		cached, ok := l.branchCache[task.BranchName]
		if !ok {
			continue
		}
		statuses[task.key] = issueStatus{
			State: sessionview.IssueStateUnknown,
			PRs:   cached.PRs,
		}
	}
	return statuses, loadErr
}

func (l *issueStatusLoader) refreshUnrecordedIssuePRs(
	graph sessionview.WaveGraph,
	recorded []int,
	refreshed map[int]bool,
) error {
	recordedSet := make(map[int]bool, len(recorded))
	for _, num := range recorded {
		recordedSet[num] = true
	}
	var nums []int
	for _, issue := range graph.Children {
		if issue.Number <= 0 || recordedSet[issue.Number] || refreshed[issue.Number] {
			continue
		}
		refreshed[issue.Number] = true
		nums = append(nums, issue.Number)
	}
	slices.Sort(nums)
	return l.refreshIssuePRs(nums)
}

func (l *issueStatusLoader) refreshIssuePRs(nums []int) error {
	if len(nums) == 0 {
		return nil
	}
	snapshots, loadErr := l.gh.IssuePRsBatch(nums)
	for _, num := range nums {
		snapshot, ok := snapshots[num]
		if !ok {
			if loadErr == nil {
				loadErr = errors.Join(loadErr, fmt.Errorf("#%d: missing from issue PR batch", num))
			}
			continue
		}
		l.prCache[num] = issueStatus{State: snapshot.State, PRs: snapshot.PRs}
	}
	return loadErr
}

func (l *issueStatusLoader) pruneWaveCache(numsByParent map[string][]int) {
	for parent := range l.waveCache {
		if _, ok := numsByParent[parent]; !ok {
			delete(l.waveCache, parent)
		}
	}
}

func waveAttemptCovered(entry issueWaveCacheEntry, cached bool, nums []int) bool {
	if !cached {
		return false
	}
	if len(entry.attempted) != len(nums) {
		return false
	}
	for _, num := range nums {
		if !entry.attempted[num] {
			return false
		}
	}
	return true
}

func mergeIssueWaveGraphs(previous, current sessionview.WaveGraph) sessionview.WaveGraph {
	if len(previous.Children) == 0 && len(previous.Info) == 0 {
		return current
	}
	children := make(map[int]ghissue.Issue, len(previous.Children)+len(current.Children))
	for _, issue := range previous.Children {
		children[issue.Number] = issue
	}
	for _, issue := range current.Children {
		children[issue.Number] = issue
	}
	current.Children = slices.SortedFunc(maps.Values(children), func(a, b ghissue.Issue) int {
		return cmp.Compare(a.Number, b.Number)
	})
	info := maps.Clone(previous.Info)
	if info == nil {
		info = map[int]sessionview.WaveInfo{}
	}
	maps.Copy(info, current.Info)
	current.Info = info
	return current
}

type taskStatusRow struct {
	key        issueKey
	BranchName string
}

type branchStatus struct {
	PRs []ghissue.PRRef
}

func recordedIssueNums(panes []state.Pane) []int {
	seen := map[int]bool{}
	for _, pane := range panes {
		if pane.IssueNum > 0 {
			seen[pane.IssueNum] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func recordedIssueNumsByParent(panes []state.Pane) map[string][]int {
	grouped := map[string]map[int]bool{}
	for _, pane := range panes {
		if pane.Parent == "" || pane.IssueNum <= 0 {
			continue
		}
		parent := parentref.Canon(pane.Parent)
		if grouped[parent] == nil {
			grouped[parent] = map[int]bool{}
		}
		grouped[parent][pane.IssueNum] = true
	}
	out := make(map[string][]int, len(grouped))
	for parent, nums := range grouped {
		out[parent] = slices.Sorted(maps.Keys(nums))
	}
	return out
}

func issuePRCollector(issues map[issueKey]issueStatus) func(int) (string, []ghissue.PRRef, error) {
	byNum := map[int]issueStatus{}
	for key, status := range issues {
		if key.Num > 0 {
			byNum[key.Num] = status
		}
	}
	return func(num int) (string, []ghissue.PRRef, error) {
		status, ok := byNum[num]
		if !ok {
			return "", nil, nil
		}
		return status.State, status.PRs, nil
	}
}

func waveCollector(issues map[issueKey]issueStatus) func(string) (sessionview.WaveGraph, error) {
	return func(parent string) (sessionview.WaveGraph, error) {
		return waveGraphFromStatuses(parent, issues), nil
	}
}

func waveGraphFromStatuses(parent string, issues map[issueKey]issueStatus) sessionview.WaveGraph {
	parent = sessionview.NormalizeParent(parent)
	graph := sessionview.WaveGraph{Info: map[int]sessionview.WaveInfo{}}
	for key, status := range issues {
		if key.Parent != parent || key.Num <= 0 {
			continue
		}
		graph.Children = append(graph.Children, ghissue.Issue{
			Number: key.Num,
			Title:  status.Title,
			State:  status.State,
		})
		rows := status.BlockerRows
		if rows == nil {
			rows = parseFormattedBlockers(status.Blockers)
		}
		graph.Info[key.Num] = sessionview.WaveInfo{
			Wave:      status.Wave,
			WaveLabel: status.WaveLabel,
			Blockers:  rows,
			Blocked:   status.HasOpenBlockers,
			Degraded:  status.WaveDegraded,
		}
	}
	slices.SortFunc(graph.Children, func(a, b ghissue.Issue) int { return cmp.Compare(a.Number, b.Number) })
	return graph
}

func parseFormattedBlockers(text string) []blockers.Status {
	text = strings.TrimSpace(text)
	if text == "" || text == "-" {
		return nil
	}
	rows := []blockers.Status{}
	for part := range strings.SplitSeq(text, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) < 2 {
			continue
		}
		numText := strings.TrimPrefix(fields[len(fields)-1], "#")
		num, err := strconv.Atoi(numText)
		if err != nil {
			continue
		}
		stateName := fields[0]
		if stateName == "resolved" {
			stateName = "CLOSED"
		}
		rows = append(rows, blockers.Status{Num: num, State: strings.ToUpper(stateName)})
	}
	return rows
}

func recordedParents(panes []state.Pane) []string {
	seen := map[string]bool{}
	for _, pane := range panes {
		if pane.Parent == "" || pane.IssueNum <= 0 {
			continue
		}
		seen[parentref.Canon(pane.Parent)] = true
	}
	parents := make([]string, 0, len(seen))
	for parent := range seen {
		parents = append(parents, parent)
	}
	slices.SortFunc(parents, func(a, b string) int {
		left, leftErr := strconv.Atoi(a)
		right, rightErr := strconv.Atoi(b)
		if leftErr == nil && rightErr == nil {
			return cmp.Compare(left, right)
		}
		return strings.Compare(a, b)
	})
	return parents
}

func recordedTaskRows(panes []state.Pane) []taskStatusRow {
	seen := map[issueKey]bool{}
	var rows []taskStatusRow
	for _, pane := range panes {
		branch := strings.TrimSpace(pane.BranchName)
		taskID := strings.TrimSpace(pane.TaskID)
		if pane.Parent == "" || pane.IssueNum > 0 || taskID == "" || branch == "" {
			continue
		}
		key := keyForTask(pane.Parent, taskID, pane.SourceProjectRoot)
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, taskStatusRow{key: key, BranchName: branch})
	}
	slices.SortFunc(rows, func(a, b taskStatusRow) int {
		if c := cmp.Compare(a.key.Parent, b.key.Parent); c != 0 {
			return c
		}
		if c := cmp.Compare(a.key.TaskID, b.key.TaskID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.key.Source, b.key.Source); c != 0 {
			return c
		}
		return cmp.Compare(a.BranchName, b.BranchName)
	})
	return rows
}

func keyForIssue(parent string, num int) issueKey {
	return issueKey{Parent: parentref.Canon(parent), Num: num}
}

func keyForPaneView(pane paneView) issueKey {
	taskID := strings.TrimSpace(pane.TaskID)
	switch {
	case pane.IssueNum > 0:
		// GitHub issue: globally stable, aggregates across worktrees.
		return keyForIssue(pane.Parent, pane.IssueNum)
	case taskID != "":
		return keyForTask(pane.Parent, taskID, pane.sourceProjectRoot)
	default:
		// Manual/synthetic (@manual, non-positive issueNum): worktree-local, so
		// scope by source to keep two worktrees' rows distinct.
		return issueKey{Parent: parentref.Canon(pane.Parent), Num: pane.IssueNum, Source: pane.sourceProjectRoot}
	}
}

func keyForTask(parent, taskID, source string) issueKey {
	return issueKey{Parent: parentref.Canon(parent), Num: 0, TaskID: strings.TrimSpace(taskID), Source: source}
}

func buildPaneViews(panes []state.Pane, tmuxPanes []tmuxrun.LivePane, tmuxKnown bool, issues map[issueKey]issueStatus, worktrees map[string]worktreeStatView) []paneView {
	const projectRoot = "/repo"
	live := make([]backend.LivePane, 0, len(tmuxPanes))
	for _, pane := range tmuxPanes {
		state, _ := backend.ParseAgentState(pane.AgentState)
		live = append(live, backend.LivePane{
			Ref:              backend.PaneRef{Backend: backend.Tmux, Pane: pane.ID},
			CurrentPath:      matchingWorktreePath(pane.ID, panes),
			Title:            pane.Title,
			AgentState:       state,
			NativeAgentState: pane.AgentState,
		})
	}
	liveCollector := func() ([]backend.LivePane, error) {
		if !tmuxKnown {
			return nil, errors.New("tmux unavailable")
		}
		return live, nil
	}
	worktreeCollector := func(path, baseRef string) (sessionview.WorktreeStat, error) {
		if stat, ok := worktrees[path]; ok {
			return sessionview.WorktreeStat{DiffSummary: stat.Diff, DirtyState: stat.Dirty}, errFromString(stat.Err)
		}
		return sessionview.WorktreeStat{DiffSummary: "-", DirtyState: "unknown"}, nil
	}
	snap := sessionview.Build("", projectRoot, sessionview.Collectors{
		LoadState:    func() (state.Store, error) { return state.Store{SchemaVersion: 1, Panes: panes}, nil },
		ListLive:     liveCollector,
		IssuePRs:     issuePRCollector(issues),
		Waves:        waveCollector(issues),
		WorktreeStat: worktreeCollector,
		Now:          time.Now,
	})
	return applyIssueStatuses(projectRoot, paneViewsFromSnapshot(projectRoot, snap), issues)
}

func matchingWorktreePath(paneID string, panes []state.Pane) string {
	for _, pane := range panes {
		if pane.PaneID == paneID {
			return pane.WorktreePath
		}
	}
	return ""
}

func errFromString(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return errors.New(s)
}

func paneViewsFromSnapshot(projectRoot string, snap sessionview.Snapshot) []paneView {
	out := []paneView{}
	for _, session := range snap.Sessions {
		parent := session.Parent
		for _, pv := range session.Panes {
			derived := pv.Derived
			if derived.Name == "" && derived.PRSummary == "" {
				derived = sessionview.DerivePane(projectRoot, parent, pv)
			}
			out = append(out, paneView{
				Parent:             parent,
				IssueNum:           pv.IssueNum,
				TaskID:             pv.TaskID,
				Kind:               pv.Kind,
				Slug:               pv.Slug,
				Name:               derived.Name,
				PaneID:             pv.PaneID,
				Backend:            pv.Backend,
				ShellKey:           pv.ShellKey,
				SourceParent:       pv.SourceParent,
				SourceIssueNum:     pv.SourceIssueNum,
				SourceTaskID:       pv.SourceTaskID,
				SourceKey:          pv.SourceKey,
				TmuxState:          firstNonEmpty(pv.RuntimeState, pv.TmuxState),
				TmuxTitle:          firstNonEmpty(pv.RuntimeTitle, pv.TmuxTitle),
				AgentState:         pv.AgentState,
				IssueState:         dash(pv.IssueState),
				PRSummary:          dash(derived.PRSummary),
				HasMergedPR:        pv.HasMergedPR,
				Wave:               pv.Wave,
				WaveLabel:          pv.WaveLabel,
				WaveBadge:          derived.WaveBadge,
				Blockers:           dash(derived.BlockersText),
				Blocked:            pv.Blocked,
				CIStatus:           dash(pv.CIStatus),
				DiffSummary:        pv.DiffSummary,
				DirtyState:         pv.DirtyState,
				WorktreeErr:        pv.WorktreeErr,
				BranchName:         pv.BranchName,
				WorktreePath:       firstNonEmpty(derived.WorktreeRelative, sessionview.RelativePath(projectRoot, pv.WorktreePath)),
				worktreeAbs:        pv.WorktreePath,
				sourceProjectRoot:  pv.SourceProjectRoot,
				sourceProjectRoots: pv.SourceProjectRoots,
				Agent:              pv.Agent,
				CreatedAt:          pv.CreatedAt,
				Prompt:             pv.Prompt,
				NotStarted:         pv.NotStarted,
				Derived:            derived,
			})
		}
	}
	return out
}

func applyIssueStatuses(projectRoot string, panes []paneView, issues map[issueKey]issueStatus) []paneView {
	out := slices.Clone(panes)
	seen := map[issueKey]bool{}
	for i := range out {
		key := keyForPaneView(out[i])
		seen[key] = true
		if status, ok := issues[key]; ok {
			out[i].IssueState = dash(status.State)
			out[i].PRSummary = summarizePRs(status.PRs)
			out[i].HasMergedPR = hasMergedPR(status.PRs)
			out[i].Wave = status.Wave
			if status.WaveLabel != "" {
				out[i].WaveLabel = status.WaveLabel
			}
			out[i].WaveBadge = waveBadge(status.Wave, status.HasOpenBlockers)
			out[i].Blockers = dash(status.Blockers)
			out[i].Blocked = status.HasOpenBlockers
			out[i].CIStatus = ghissue.SummarizeCI(status.PRs)
			out[i].Derived = derivePaneView(projectRoot, out[i], status.PRs, status.BlockerRows)
		}
	}
	for key, status := range issues {
		if seen[key] || key.TaskID != "" || key.Num <= 0 {
			continue
		}
		view := paneView{
			Parent:      key.Parent,
			IssueNum:    key.Num,
			Name:        issueTitle(status, key.Num),
			TmuxState:   syntheticTmuxState(status),
			IssueState:  dash(status.State),
			PRSummary:   summarizePRs(status.PRs),
			HasMergedPR: hasMergedPR(status.PRs),
			CIStatus:    ghissue.SummarizeCI(status.PRs),
			Wave:        status.Wave,
			WaveLabel:   status.WaveLabel,
			WaveBadge:   waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:    dash(status.Blockers),
			Blocked:     status.HasOpenBlockers,
			NotStarted:  true,
		}
		view.Derived = derivePaneView(projectRoot, view, status.PRs, status.BlockerRows)
		out = append(out, view)
	}
	sortPaneViews(out)
	return out
}

func derivePaneView(projectRoot string, view paneView, prs []ghissue.PRRef, blockerRows []blockers.Status) sessionview.PaneDerived {
	if blockerRows == nil {
		blockerRows = parseFormattedBlockers(view.Blockers)
	}
	return sessionview.DerivePane(projectRoot, view.Parent, sessionview.PaneView{
		IssueNum:     view.IssueNum,
		TaskID:       view.TaskID,
		Kind:         view.Kind,
		DisplayName:  view.Name,
		Agent:        view.Agent,
		BranchName:   view.BranchName,
		PaneID:       view.PaneID,
		Backend:      view.Backend,
		ShellKey:     view.ShellKey,
		WorktreePath: view.WorktreePath,
		CreatedAt:    view.CreatedAt,
		Alive:        view.TmuxState == "live",
		IssueState:   view.IssueState,
		PRs:          prs,
		HasMergedPR:  view.HasMergedPR,
		DiffSummary:  view.DiffSummary,
		DirtyState:   view.DirtyState,
		WorktreeErr:  view.WorktreeErr,
		TmuxState:    view.TmuxState,
		TmuxTitle:    view.TmuxTitle,
		AgentState:   view.AgentState,
		Prompt:       view.Prompt,
		CIStatus:     strings.ToLower(strings.TrimSpace(view.CIStatus)),
		Wave:         view.Wave,
		WaveLabel:    view.WaveLabel,
		Blockers:     blockerRows,
		Blocked:      view.Blocked,
		NotStarted:   view.NotStarted,
	})
}

func sortPaneViews(panes []paneView) {
	slices.SortFunc(panes, func(a, b paneView) int {
		if c := cmp.Compare(a.Parent, b.Parent); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Wave, b.Wave); c != 0 {
			return c
		}
		if c := cmp.Compare(a.IssueNum, b.IssueNum); c != 0 {
			return c
		}
		return strings.Compare(a.itemLabel(), b.itemLabel())
	})
}

func syntheticTmuxState(status issueStatus) string {
	// web ダッシュボードの synthetic 行と同一文字列を保証する単一実装に委譲
	// (`state:queued` 等のフィルタ語彙が TUI / web で割れないように)。
	return sessionview.SyntheticTmuxState(status.State, status.HasOpenBlockers)
}

func cloneIssueStatuses(in map[issueKey]issueStatus) map[issueKey]issueStatus {
	out := make(map[issueKey]issueStatus, len(in))
	maps.Copy(out, in)
	return out
}
