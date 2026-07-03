package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/panelayout"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/worktree"
)

type tuiRestoreReport struct {
	Rebound       int
	Restored      int
	RemovedShells int
	Skipped       int
}

type tuiRestoreSnapshot struct {
	Live         map[string]tmuxrun.LivePane
	PanesByTitle map[string][]tmuxrun.LivePane
}

func newTUIRestoreFunc(projectRoot, session, commandName string) func() (string, error) {
	var mu sync.Mutex
	return func() (string, error) {
		mu.Lock()
		defer mu.Unlock()

		report, err := restoreRecordedPanes(projectRoot, session, commandName)
		return report.Notice(), err
	}
}

func restoreRecordedPanes(projectRoot, session, commandName string) (tuiRestoreReport, error) {
	roots, _ := worktree.ListRoots(projectRoot) // Degrade to the current root, matching the TUI state loader.
	initialSnapshot, err := loadTUIRestoreSnapshot(session)
	if err != nil {
		return tuiRestoreReport{}, err
	}
	var report tuiRestoreReport
	var restoreErr error
	claimed := preclaimLiveRestoreIdentities(roots, initialSnapshot)
	for _, root := range uniqueRestoreRoots(roots) {
		rootReport, err := restoreRecordedPanesForRoot(root, session, commandName, claimed)
		report.Add(rootReport)
		restoreErr = errors.Join(restoreErr, err)
	}
	if report.Changed() {
		_ = panelayout.Apply(tuiLaunchTarget(session), panelayout.Create)
	}
	return report, restoreErr
}

func preclaimLiveRestoreIdentities(roots []string, snapshot tuiRestoreSnapshot) map[string]bool {
	claimed := map[string]bool{}
	if snapshot.Live == nil {
		snapshot.Live = map[string]tmuxrun.LivePane{}
	}
	for _, root := range uniqueRestoreRoots(roots) {
		store, err := state.LoadProject(root)
		if err != nil {
			continue
		}
		for _, pane := range store.Panes {
			identity := restoreDedupeKey(pane)
			if identity != "" && restorePaneAlive(snapshot.Live, pane) {
				claimed[identity] = true
			}
		}
	}
	return claimed
}

func restoreRecordedPanesForRoot(root, session, commandName string, claimed map[string]bool) (tuiRestoreReport, error) {
	return restoreRecordedPanesForRootWithSnapshot(root, session, commandName, loadTUIRestoreSnapshot, claimed)
}

func restoreRecordedPanesForRootWithSnapshot(root, session, commandName string, loadSnapshot func(string) (tuiRestoreSnapshot, error), claimed map[string]bool) (tuiRestoreReport, error) {
	hasState, err := restoreStateFileExists(root)
	if err != nil || !hasState {
		return tuiRestoreReport{}, err
	}
	if claimed == nil {
		claimed = map[string]bool{}
	}
	locked, err := state.LockProject(root)
	if err != nil {
		return tuiRestoreReport{}, err
	}
	defer func() { _ = locked.Unlock() }()

	snapshot, err := loadSnapshot(session)
	if err != nil {
		return tuiRestoreReport{}, err
	}
	if snapshot.Live == nil {
		snapshot.Live = map[string]tmuxrun.LivePane{}
	}

	var report tuiRestoreReport
	changed := false
	var restoreErr error
	var createdPaneIDs []string
	for idx := 0; idx < len(locked.Panes); idx++ {
		pane := locked.Panes[idx]
		identity := restoreDedupeKey(pane)
		if identity != "" && claimed[identity] {
			report.Skipped++
			continue
		}
		if restorePaneAlive(snapshot.Live, pane) {
			if identity != "" {
				claimed[identity] = true
			}
			continue
		}
		if pane.IsShell() {
			if livePane, ok := snapshot.Live[pane.PaneID]; ok && strings.TrimSpace(livePane.ShellKey) == "" {
				report.Skipped++
				continue
			}
			locked.Panes = append(locked.Panes[:idx], locked.Panes[idx+1:]...)
			idx--
			report.RemovedShells++
			changed = true
			continue
		}
		if livePane, ok := snapshot.Live[pane.PaneID]; ok && restorePaneIDStillBelongsToRecord(livePane, pane) {
			if identity != "" {
				claimed[identity] = true
			}
			report.Skipped++
			continue
		}
		if strings.TrimSpace(pane.WorktreePath) == "" {
			report.Skipped++
			continue
		}
		if !dirExistsForRestore(pane.WorktreePath) {
			report.Skipped++
			continue
		}
		if rebound, ok := rebindRecordedPane(pane, snapshot.PanesByTitle); ok {
			locked.Panes[idx] = rebound
			snapshot.Live[rebound.PaneID] = tmuxrun.LivePane{
				ID:          rebound.PaneID,
				CurrentPath: pane.WorktreePath,
				Title:       restorePaneTitle(rebound),
				AgentState:  "running",
			}
			report.Rebound++
			if identity != "" {
				claimed[identity] = true
			}
			changed = true
			continue
		}
		restored, err := recreateRecordedPane(pane, root, session, commandName)
		if err != nil {
			report.Skipped++
			restoreErr = errors.Join(restoreErr, err)
			continue
		}
		locked.Panes[idx] = restored
		createdPaneIDs = append(createdPaneIDs, restored.PaneID)
		snapshot.Live[restored.PaneID] = tmuxrun.LivePane{
			ID:          restored.PaneID,
			CurrentPath: restored.WorktreePath,
			Title:       restorePaneTitle(restored),
			AgentState:  "running",
		}
		report.Restored++
		if identity != "" {
			claimed[identity] = true
		}
		changed = true
	}
	if changed {
		if err := locked.Save(); err != nil {
			killRestoredPanes(createdPaneIDs)
			return report, err
		}
	}
	return report, restoreErr
}

func killRestoredPanes(paneIDs []string) {
	for _, paneID := range paneIDs {
		_ = tmuxrun.KillPane(paneID)
	}
}

func loadTUIRestoreSnapshot(session string) (tuiRestoreSnapshot, error) {
	livePanes, err := tmuxrun.ListLivePanes()
	if err != nil {
		return tuiRestoreSnapshot{}, err
	}
	live := make(map[string]tmuxrun.LivePane, len(livePanes))
	for _, pane := range livePanes {
		live[pane.ID] = pane
	}
	sessionPanes, err := tmuxrun.ListPanes(session)
	if err != nil {
		return tuiRestoreSnapshot{}, err
	}
	return tuiRestoreSnapshot{Live: live, PanesByTitle: liveSessionPanesByTitle(sessionPanes, live)}, nil
}

func liveSessionPanesByTitle(sessionPanes []tmuxrun.PaneInfo, live map[string]tmuxrun.LivePane) map[string][]tmuxrun.LivePane {
	out := map[string][]tmuxrun.LivePane{}
	for _, pane := range sessionPanes {
		title := strings.TrimSpace(pane.Title)
		if title == "" {
			continue
		}
		livePane, ok := live[pane.ID]
		if !ok {
			continue
		}
		out[title] = append(out[title], livePane)
	}
	return out
}

func rebindRecordedPane(pane state.Pane, panesByTitle map[string][]tmuxrun.LivePane) (state.Pane, bool) {
	for _, title := range restorePaneTitleCandidates(pane) {
		for _, livePane := range panesByTitle[title] {
			candidate := pane
			candidate.PaneID = livePane.ID
			if restorePaneAlive(map[string]tmuxrun.LivePane{livePane.ID: livePane}, candidate) {
				candidate.AgentStatus = "running"
				return candidate, true
			}
		}
	}
	return state.Pane{}, false
}

func recreateRecordedPane(pane state.Pane, root, session, commandName string) (state.Pane, error) {
	command, statusPath, err := restoreAgentCommand(pane, root, commandName)
	if err != nil {
		return pane, err
	}
	paneID, err := tmuxrun.SplitPaneWithAgentCommand(tuiLaunchTarget(session), pane.WorktreePath, command)
	if err != nil {
		return pane, err
	}
	_ = tmuxrun.SetPaneTitle(paneID, restorePaneTitle(pane))                           // cosmetic; pane is still usable if tmux rejects title updates
	_ = tmuxrun.SetPaneLabel(paneID, borderLabel(pane.Parent, restorePaneTitle(pane))) // cosmetic pane-border label
	_ = tmuxrun.EnablePaneBorderTitles(paneID)                                         // cosmetic pane-border label
	_ = tmuxrun.SetPaneProjectRoot(paneID, root)                                       // best-effort dashboard keybinding hint
	if statusPath != "" {
		status, err := waitForCodexPlanTUIReadyStatus(statusPath, codexPlanTUIStartupTimeout)
		_ = os.Remove(statusPath)
		if err != nil {
			_ = tmuxrun.KillPane(paneID)
			return pane, fmt.Errorf("resume Codex Plan Mode TUI in pane %s: %w", paneID, err)
		}
		pane.CodexThreadID = status.ThreadID
		pane.CodexSessionID = status.SessionID
	}
	pane.PaneID = paneID
	pane.AgentStatus = "running"
	return pane, nil
}

func restoreAgentCommand(pane state.Pane, root, commandName string) (string, string, error) {
	if pane.CodexPlanMode {
		if strings.TrimSpace(pane.CodexThreadID) == "" {
			return "", "", fmt.Errorf("cannot restore Codex Plan Mode pane %q: missing codex thread id", restorePaneTitle(pane))
		}
		codexPath, err := agent.ResolveExecutable("codex")
		if err != nil {
			return "", "", err
		}
		fanoutPath, err := os.Executable()
		if err != nil || strings.TrimSpace(fanoutPath) == "" {
			fanoutPath = commandName
		}
		statusPath := codexPlanStatusPath(root, pane.IssueNum, false)
		command := "PATH=" + agent.ShellQuote(os.Getenv("PATH")) + " " +
			buildCodexPlanTUIResumeLaunchCommand(fanoutPath, codexPath, pane.CodexThreadID, pane.CodexSessionID, statusPath)
		return command, statusPath, nil
	}
	command, err := agent.BuildResolvedResumeCommand(pane.Agent)
	return command, "", err
}

func restorePaneAlive(live map[string]tmuxrun.LivePane, pane state.Pane) bool {
	if strings.TrimSpace(pane.PaneID) == "" {
		return false
	}
	cur, ok := live[pane.PaneID]
	if !ok {
		return false
	}
	if pane.IsShell() {
		return strings.TrimSpace(pane.ShellKey) != "" && cur.ShellKey == pane.ShellKey
	}
	return pathWithin(cur.CurrentPath, pane.WorktreePath)
}

func restorePaneIDStillBelongsToRecord(livePane tmuxrun.LivePane, pane state.Pane) bool {
	return slices.Contains(restorePaneTitleCandidates(pane), livePane.Title)
}

func pathWithin(path, base string) bool {
	if strings.TrimSpace(base) == "" {
		return true
	}
	path = filepath.Clean(path)
	base = filepath.Clean(base)
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func restoreDedupeKey(pane state.Pane) string {
	if pane.IsShell() || pane.IssueNum <= 0 {
		return ""
	}
	return "issue\x00" + normalizeRestoreParent(pane.Parent) + "\x00" + strconv.Itoa(pane.IssueNum)
}

func normalizeRestoreParent(parent string) string {
	num, err := strconv.Atoi(parent)
	if err != nil {
		return parent
	}
	return strconv.Itoa(num)
}

func restorePaneTitleCandidates(pane state.Pane) []string {
	seen := map[string]bool{}
	var out []string
	for _, title := range []string{pane.DisplayName, pane.Slug} {
		title = strings.TrimSpace(title)
		if title == "" || seen[title] {
			continue
		}
		seen[title] = true
		out = append(out, title)
	}
	return out
}

func restorePaneTitle(pane state.Pane) string {
	if title := strings.TrimSpace(pane.DisplayName); title != "" {
		return title
	}
	if title := strings.TrimSpace(pane.Slug); title != "" {
		return title
	}
	if pane.TaskID != "" {
		return pane.TaskID
	}
	if pane.IssueNum != 0 {
		return strconv.Itoa(pane.IssueNum)
	}
	return "fanout pane"
}

func dirExistsForRestore(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func restoreStateFileExists(root string) (bool, error) {
	_, err := os.Stat(state.Path(root))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func uniqueRestoreRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		key := filepath.Clean(root)
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			key = resolved
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, root)
	}
	return out
}

func (r *tuiRestoreReport) Add(other tuiRestoreReport) {
	r.Rebound += other.Rebound
	r.Restored += other.Restored
	r.RemovedShells += other.RemovedShells
	r.Skipped += other.Skipped
}

func (r tuiRestoreReport) Changed() bool {
	return r.Rebound > 0 || r.Restored > 0 || r.RemovedShells > 0
}

func (r tuiRestoreReport) Notice() string {
	var parts []string
	if r.Restored > 0 {
		parts = append(parts, fmt.Sprintf("restored %d pane(s)", r.Restored))
	}
	if r.Rebound > 0 {
		parts = append(parts, fmt.Sprintf("rebound %d pane(s)", r.Rebound))
	}
	if r.RemovedShells > 0 {
		parts = append(parts, fmt.Sprintf("removed %d stale terminal(s)", r.RemovedShells))
	}
	return strings.Join(parts, ", ")
}
