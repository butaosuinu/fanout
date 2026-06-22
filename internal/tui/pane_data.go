package tui

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

func loadPaneViews(projectRoot string, issues map[issueKey]issueStatus) ([]paneView, error) {
	var stateErr error
	mergedState := sessionview.MergedStateLoader(projectRoot)
	loadState := func() (state.Store, error) {
		store, err := mergedState()
		stateErr = err
		return store, err
	}
	var tmuxErr error
	livePanes := sessionview.LivePanes()
	live := func() (map[string]sessionview.LivePaneInfo, error) {
		panes, err := livePanes()
		tmuxErr = err
		return panes, err
	}
	snap := sessionview.Build("", projectRoot, sessionview.Collectors{
		LoadState:    loadState,
		LivePanes:    live,
		IssuePRs:     issuePRCollector(issues),
		Waves:        waveCollector(issues),
		WorktreeStat: sessionview.GitWorktreeStat(projectRoot),
		Now:          time.Now,
	})
	return paneViewsFromSnapshot(projectRoot, snap), errors.Join(stateErr, tmuxErr)
}

func loadIssueStatuses(projectRoot string) (map[issueKey]issueStatus, error) {
	// Merge sibling worktrees so issue/PR/wave status is fetched for Sessions
	// fanned out from another worktree too, matching loadPaneViews.
	store, err := sessionview.MergedStateLoader(projectRoot)()
	if err != nil {
		return nil, err
	}
	parents := recordedParents(store.Panes)
	taskRows := recordedTaskRows(store.Panes)
	statuses := map[issueKey]issueStatus{}
	if len(parents) == 0 && len(taskRows) == 0 {
		return statuses, nil
	}

	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		return nil, fmt.Errorf("resolve repo: %w", err)
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("unexpected repo nameWithOwner: %s", nwo)
	}

	prCache := map[int]issueStatus{}
	branchCache := map[string]branchStatus{}
	var loadErr error
	for _, parent := range parents {
		graph, err := sessionview.FetchWaveGraph(gh, parent, recordedIssueNums(store.PanesForParent(parent)))
		loadErr = errors.Join(loadErr, err)
		for _, issue := range graph.Children {
			key := keyForIssue(parent, issue.Number)
			cached, ok := prCache[issue.Number]
			if !ok {
				stateName, prs, err := gh.IssueWithPRs(owner, repo, issue.Number)
				if err != nil {
					// Accumulate and skip, like the branch/wave paths: a single
					// sibling-worktree issue that was deleted/made private or hit a
					// transient gh error must not blank every other row's status.
					loadErr = errors.Join(loadErr, fmt.Errorf("#%d: %w", issue.Number, err))
					continue
				}
				cached = issueStatus{State: stateName, PRs: prs}
				prCache[issue.Number] = cached
			}
			if cached.State == "" {
				cached.State = issue.State
			}
			info := graph.Info[issue.Number]
			cached.Title = issue.Title
			cached.Wave = info.Wave
			cached.WaveLabel = info.WaveLabel
			cached.Blockers = blockers.FormatStatuses(info.Blockers)
			cached.BlockerRows = info.Blockers
			cached.HasOpenBlockers = info.Blocked
			cached.WaveDegraded = info.Degraded
			statuses[key] = cached
		}
	}
	for _, task := range taskRows {
		cached, ok := branchCache[task.BranchName]
		if !ok {
			prs, err := gh.PRsForBranch(task.BranchName)
			if err != nil {
				loadErr = errors.Join(loadErr, fmt.Errorf("branch %s: %w", task.BranchName, err))
				continue
			}
			cached = branchStatus{PRs: prs}
			branchCache[task.BranchName] = cached
		}
		statuses[task.key] = issueStatus{
			State: sessionview.IssueStateUnknown,
			PRs:   cached.PRs,
		}
	}
	return statuses, loadErr
}

type taskStatusRow struct {
	key        issueKey
	BranchName string
}

type branchStatus struct {
	PRs []ghissue.PRRef
}

func recordedIssueNums(panes []state.Pane) []int {
	nums := make([]int, 0, len(panes))
	for _, pane := range panes {
		if pane.IssueNum > 0 {
			nums = append(nums, pane.IssueNum)
		}
	}
	return nums
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
		seen[normalizedParent(pane.Parent)] = true
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
	return issueKey{Parent: normalizedParent(parent), Num: num}
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
		return issueKey{Parent: normalizedParent(pane.Parent), Num: pane.IssueNum, Source: pane.sourceProjectRoot}
	}
}

func keyForTask(parent, taskID, source string) issueKey {
	return issueKey{Parent: normalizedParent(parent), Num: 0, TaskID: strings.TrimSpace(taskID), Source: source}
}

func normalizedParent(parent string) string {
	parentNum, err := strconv.Atoi(parent)
	if err != nil {
		return parent
	}
	return strconv.Itoa(parentNum)
}

func buildPaneViews(panes []state.Pane, tmuxPanes []tmuxrun.PaneInfo, tmuxKnown bool, issues map[issueKey]issueStatus, worktrees map[string]worktreeStatView) []paneView {
	const projectRoot = "/repo"
	live := map[string]sessionview.LivePaneInfo{}
	for _, pane := range tmuxPanes {
		live[pane.ID] = sessionview.LivePaneInfo{Path: matchingWorktreePath(pane.ID, panes), Title: pane.Title}
	}
	liveCollector := func() (map[string]sessionview.LivePaneInfo, error) {
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
		LivePanes:    liveCollector,
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
				Name:               derived.Name,
				PaneID:             pv.PaneID,
				ShellKey:           pv.ShellKey,
				TmuxState:          pv.TmuxState,
				TmuxTitle:          pv.TmuxTitle,
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

func (p paneView) tableRow() table.Row {
	tmuxState := p.TmuxState
	if tmuxState == "stale" {
		tmuxState = "stale!"
	}
	return table.Row{
		compactParent(p.Parent),
		p.itemLabel(),
		truncate(dash(p.waveCell()), 12),
		truncate(dash(p.Blockers), 22),
		truncate(p.Name, 28),
		dash(p.Agent),
		tmuxState,
		dash(p.IssueState),
		truncate(dash(p.PRSummary), 12),
		truncate(dash(p.CIStatus), 7),
		dash(p.DiffSummary),
		dash(p.DirtyState),
		truncate(dash(p.BranchName), 18),
		dash(p.PaneID),
	}
}

func (p paneView) canFocus() bool {
	return strings.TrimSpace(p.PaneID) != "" && p.TmuxState != "stale" && p.TmuxState != "-"
}

func (p paneView) canPeek() bool {
	return p.canFocus()
}

func (p paneView) isShell() bool {
	return p.Kind == state.PaneKindShell
}

func (p paneView) absoluteWorktreePath(projectRoot string) string {
	path := strings.TrimSpace(firstNonEmpty(p.worktreeAbs, p.WorktreePath))
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return path
	}
	return filepath.Join(projectRoot, path)
}
