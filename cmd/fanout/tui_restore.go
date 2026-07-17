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

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/panelayout"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type tuiRestoreReport struct {
	Rebound       int
	Restored      int
	Tracked       int
	RemovedShells int
	Skipped       int
}

type recreatedPane struct {
	Original     state.Pane
	Pane         state.Pane
	Index        int
	LiveShellKey string
	Track        bool
}

var (
	setRestoredPaneLivenessKey = tmuxrun.SetPaneShellKey
	closeFreshRestoredPane     = tmuxrun.CloseFreshPane
	waitRestoredPaneReady      = codexapp.WaitReady
	closeRestoredPaneIfOwned   = tmuxrun.ClosePaneIfOwned
)

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
			if identity != "" && (restorePaneAlive(snapshot.Live, pane) || restorePaneIdentityUnknown(snapshot.Live, pane)) {
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
	var createdPanes []recreatedPane
	for idx := 0; idx < len(locked.Panes); idx++ {
		pane := locked.Panes[idx]
		// Restore is deliberately tmux-only session management. Herdr v1 is
		// read-only, so a non-tmux row must never be rebound to a tmux pane,
		// recreated, or rewritten as a side effect of restoring tmux rows.
		if backend.NormalizeName(pane.Backend) != backend.Tmux {
			report.Skipped++
			continue
		}
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
		if restorePaneIdentityUnknown(snapshot.Live, pane) {
			if identity != "" {
				claimed[identity] = true
			}
			report.Skipped++
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
				ShellKey:    rebound.ShellKey,
			}
			report.Rebound++
			if identity != "" {
				claimed[identity] = true
			}
			changed = true
			continue
		}
		recreated, err := recreateRecordedPane(pane, root, session, commandName)
		if recreated.Track {
			recreated.Index = idx
			locked.Panes[idx] = recreated.Pane
			createdPanes = append(createdPanes, recreated)
			snapshot.Live[recreated.Pane.PaneID] = tmuxrun.LivePane{
				ID:          recreated.Pane.PaneID,
				CurrentPath: recreated.Pane.WorktreePath,
				Title:       restorePaneTitle(recreated.Pane),
				AgentState:  "running",
				ShellKey:    recreated.LiveShellKey,
			}
			if identity != "" {
				claimed[identity] = true
			}
			changed = true
		}
		if err != nil {
			if recreated.Track {
				report.Tracked++
			}
			report.Skipped++
			restoreErr = errors.Join(restoreErr, err)
			continue
		}
		report.Restored++
	}
	if changed {
		if err := locked.Save(); err != nil {
			remaining, cleanupErr := stopRestoredPanes(createdPanes)
			for _, created := range createdPanes {
				if !remaining[created.Pane.PaneID] {
					locked.Panes[created.Index] = created.Original
				}
			}
			var recoverySaveErr error
			if len(remaining) > 0 {
				recoverySaveErr = locked.Save()
				if recoverySaveErr != nil {
					recoverySaveErr = fmt.Errorf("save recovery rows for live restored panes: %w", recoverySaveErr)
				}
			}
			return report, errors.Join(err, cleanupErr, recoverySaveErr)
		}
	}
	return report, restoreErr
}

func stopRestoredPanes(panes []recreatedPane) (map[string]bool, error) {
	remaining := map[string]bool{}
	var stopErr error
	for _, pane := range panes {
		if strings.TrimSpace(pane.LiveShellKey) == "" {
			remaining[pane.Pane.PaneID] = true
			stopErr = errors.Join(stopErr, fmt.Errorf("restored pane %s has no verified liveness key", pane.Pane.PaneID))
			continue
		}
		if err := stopRestoredPane(pane.Pane.PaneID, pane.LiveShellKey); err != nil {
			remaining[pane.Pane.PaneID] = true
			stopErr = errors.Join(stopErr, err)
		}
	}
	return remaining, stopErr
}

func loadTUIRestoreSnapshot(session string) (tuiRestoreSnapshot, error) {
	livePanes, err := tmuxrun.ListLivePanesForIdentity()
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

func recreateRecordedPane(pane state.Pane, root, session, commandName string) (recreatedPane, error) {
	result := recreatedPane{Original: pane, Pane: pane}
	command, statusPath, err := restoreAgentCommand(pane, root, commandName)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(pane.ShellKey) == "" {
		pane.ShellKey, err = panelaunch.NewShellPaneKey()
		if err != nil {
			return result, err
		}
	}
	result.Pane = pane
	paneID, err := tmuxrun.SplitPaneWithAgentCommand(tuiLaunchTarget(session), pane.WorktreePath, command)
	if err != nil {
		return result, err
	}
	pane.PaneID = paneID
	pane.AgentStatus = "running"
	result.Pane = pane
	// Not best-effort: every recreated pane must bind its state row to this
	// exact tmux pane before the new pane id is persisted.
	if err := setRestoredPaneLivenessKey(paneID, pane.ShellKey); err != nil {
		stampErr := fmt.Errorf("restore pane liveness key on %s: %w", paneID, err)
		if cleanupErr := closeFreshRestoredPane(paneID); cleanupErr != nil {
			result.Track = true
			return result, errors.Join(stampErr, fmt.Errorf("stop unstamped restored pane %s: %w", paneID, cleanupErr))
		}
		return result, stampErr
	}
	result.LiveShellKey = pane.ShellKey
	_ = tmuxrun.SetPaneTitle(paneID, restorePaneTitle(pane))                                      // cosmetic; pane is still usable if tmux rejects title updates
	_ = tmuxrun.SetPaneLabel(paneID, panelaunch.BorderLabel(pane.Parent, restorePaneTitle(pane))) // cosmetic pane-border label
	_ = tmuxrun.EnablePaneBorderTitles(paneID)                                                    // cosmetic pane-border label
	_ = tmuxrun.SetPaneProjectRoot(paneID, root)                                                  // best-effort dashboard keybinding hint
	_ = tmuxrun.SetPaneWorktreePath(paneID, pane.WorktreePath)                                    // best-effort same-worktree action target
	if statusPath != "" {
		status, err := waitRestoredPaneReady(statusPath, panelaunch.CodexPlanTUIStartupTimeout)
		_ = os.Remove(statusPath)
		if err != nil {
			statusErr := fmt.Errorf("resume Codex Plan Mode TUI in pane %s: %w", paneID, err)
			if cleanupErr := stopRestoredPane(paneID, pane.ShellKey); cleanupErr != nil {
				result.Track = true
				return result, errors.Join(statusErr, cleanupErr)
			}
			return result, statusErr
		}
		pane.CodexThreadID = status.ThreadID
		pane.CodexSessionID = status.SessionID
	}
	result.Pane = pane
	result.Track = true
	return result, nil
}

func stopRestoredPane(paneID, shellKey string) error {
	result, err := closeRestoredPaneIfOwned(paneID, "", shellKey)
	if err != nil {
		return fmt.Errorf("stop restored pane %s: %w", paneID, err)
	}
	if result.Status == tmuxrun.ClosePaneFailed {
		return fmt.Errorf("restored pane %s remained live after close", paneID)
	}
	return nil
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
		statusPath := codexapp.StatusPath(root, pane.IssueNum, false)
		command := "PATH=" + agent.ShellQuote(os.Getenv("PATH")) + " " +
			codexapp.ResumeLaunchCommand(fanoutPath, codexPath, pane.CodexThreadID, pane.CodexSessionID, statusPath)
		return agent.WithFanoutBin(command, fanoutPath), statusPath, nil
	}
	command, err := agent.BuildResolvedResumeCommand(pane.Agent)
	if err != nil {
		return "", "", err
	}
	fanoutPath, executableErr := os.Executable()
	if executableErr != nil || strings.TrimSpace(fanoutPath) == "" {
		fanoutPath = commandName
	}
	return agent.WithFanoutBin(command, fanoutPath), "", nil
}

func restorePaneAlive(live map[string]tmuxrun.LivePane, pane state.Pane) bool {
	if strings.TrimSpace(pane.PaneID) == "" {
		return false
	}
	cur, ok := live[pane.PaneID]
	if !ok {
		return false
	}
	// Rows recorded with a ShellKey match on @fanout_shell_key. Legacy rows fall
	// back to their worktree path for read-only restore discovery.
	if pane.IsShell() || strings.TrimSpace(pane.ShellKey) != "" {
		return strings.TrimSpace(pane.ShellKey) != "" && cur.ShellKey == pane.ShellKey
	}
	return pathWithin(cur.CurrentPath, pane.WorktreePath)
}

// restorePaneIdentityUnknown reports the fail-closed middle state for a keyed
// record whose pane id is still live but whose tmux liveness option is empty.
// The pane may still own the running agent process tree, so restore must not
// launch a replacement until the identity can be resolved.
func restorePaneIdentityUnknown(live map[string]tmuxrun.LivePane, pane state.Pane) bool {
	key := strings.TrimSpace(pane.ShellKey)
	if key == "" {
		return false
	}
	cur, ok := live[pane.PaneID]
	return ok && strings.TrimSpace(cur.ShellKey) == ""
}

func restorePaneIDStillBelongsToRecord(livePane tmuxrun.LivePane, pane state.Pane) bool {
	// A keyed row is identified by its liveness key, not its title: a reused
	// pane id whose title happens to match must not keep the record bound.
	if key := strings.TrimSpace(pane.ShellKey); key != "" && livePane.ShellKey != key {
		return false
	}
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
	r.Tracked += other.Tracked
	r.RemovedShells += other.RemovedShells
	r.Skipped += other.Skipped
}

func (r tuiRestoreReport) Changed() bool {
	return r.Rebound > 0 || r.Restored > 0 || r.Tracked > 0 || r.RemovedShells > 0
}

func (r tuiRestoreReport) Notice() string {
	var parts []string
	if r.Restored > 0 {
		parts = append(parts, fmt.Sprintf("restored %d pane(s)", r.Restored))
	}
	if r.Tracked > 0 {
		parts = append(parts, fmt.Sprintf("tracked %d pane(s) after failed restore", r.Tracked))
	}
	if r.Rebound > 0 {
		parts = append(parts, fmt.Sprintf("rebound %d pane(s)", r.Rebound))
	}
	if r.RemovedShells > 0 {
		parts = append(parts, fmt.Sprintf("removed %d stale terminal(s)", r.RemovedShells))
	}
	return strings.Join(parts, ", ")
}
