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
	"time"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type tuiRestoreReport struct {
	Rebound       int
	Restored      int
	Tracked       int
	RemovedShells int
	Skipped       int
	Adopted       int
}

type recreatedPane struct {
	Original     state.Pane
	Pane         state.Pane
	Index        int
	LiveShellKey string
	Track        bool
}

var waitRestoredPaneReady = codexapp.WaitReady

// restoreRuntime is every capability console restore calls. RestoreOps is the
// discriminating one: a runtime that persists and rearranges its own sessions
// offers no restore observations, and the rest of the set rides along on the
// same value, so one assertion resolves the whole lane.
type restoreRuntime interface {
	backend.Backend
	backend.RestoreOps
	backend.LivenessStamper
	backend.FreshCloser
	backend.OwnedCloser
	backend.PaneDecorator
	backend.LayoutManager
}

// asRestoreRuntime resolves rt's restore capability set. ok=false means the
// runtime cannot prove that a recorded pane is still the pane its row launched,
// so the console leaves restore unwired rather than rebinding rows on a guess.
func asRestoreRuntime(rt backend.Backend) (restoreRuntime, bool) {
	if _, ok := backend.AsRestoreOps(rt); !ok {
		return nil, false
	}
	runtime, ok := rt.(restoreRuntime)
	return runtime, ok
}

// Adoption provenance window: the live pane's root process must have started
// shortly before the row was recorded. Pane creation precedes RecordPane, and
// Codex Plan Mode startup (panelaunch.CodexPlanTUIStartupTimeout, 90s) plus
// state-lock contention can hold that gap open, so the early bound is
// generous; the late bound only absorbs second-precision rounding.
const (
	adoptPaneStartEarly = 15 * time.Minute
	adoptPaneStartLate  = 2 * time.Minute
)

type tuiRestoreSnapshot struct {
	Live         map[string]backend.LivePane
	PanesByTitle map[string][]backend.LivePane
	// ServerStart is the start time of the runtime's current process
	// generation. Pane ids are never reused within one generation, so a row
	// created at or after this instant still owns its recorded pane id. Zero
	// (query failed) disables adoption.
	ServerStart time.Time
}

func newTUIRestoreFunc(runtimeBackend backend.Backend, projectRoot, session, commandName string) func() (string, error) {
	restorer, ok := asRestoreRuntime(runtimeBackend)
	if !ok {
		// Nothing this runtime exposes can prove a recorded pane's identity
		// across a restart, so there is no safe repair to offer.
		return nil
	}
	var mu sync.Mutex
	return func() (string, error) {
		mu.Lock()
		defer mu.Unlock()

		report, err := restoreRecordedPanes(restorer, projectRoot, session, commandName)
		return report.Notice(), err
	}
}

func restoreRecordedPanes(rt restoreRuntime, projectRoot, session, commandName string) (tuiRestoreReport, error) {
	roots, rootsErr := worktree.ListRoots(projectRoot) // Degrade to the current root, matching the TUI state loader.
	initialSnapshot, err := loadTUIRestoreSnapshot(rt, session)
	if err != nil {
		return tuiRestoreReport{}, err
	}
	var report tuiRestoreReport
	var restoreErr error
	claimed := preclaimLiveRestoreIdentities(roots, initialSnapshot)
	claims := collectRestoreClaimants(roots)
	if rootsErr != nil {
		// An incomplete root listing may hide claimants in unlisted sibling
		// stores; restore proceeds, adoption fails closed for this cycle.
		claims.complete = false
	}
	for _, root := range uniqueRestoreRoots(roots) {
		rootReport, err := restoreRecordedPanesForRoot(rt, root, session, commandName, claimed, claims)
		report.Add(rootReport)
		restoreErr = errors.Join(restoreErr, err)
	}
	if report.Changed() {
		_ = rt.Relayout(tuiLaunchTarget(session), backend.LayoutCreate)
	}
	return report, restoreErr
}

func preclaimLiveRestoreIdentities(roots []string, snapshot tuiRestoreSnapshot) map[string]bool {
	claimed := map[string]bool{}
	if snapshot.Live == nil {
		snapshot.Live = map[string]backend.LivePane{}
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

func restoreRecordedPanesForRoot(rt restoreRuntime, root, session, commandName string, claimed map[string]bool, claims *restoreClaimants) (tuiRestoreReport, error) {
	loadSnapshot := func(session string) (tuiRestoreSnapshot, error) { return loadTUIRestoreSnapshot(rt, session) }
	return restoreRecordedPanesForRootWithSnapshot(rt, root, session, commandName, loadSnapshot, claimed, claims)
}

func restoreRecordedPanesForRootWithSnapshot(rt restoreRuntime, root, session, commandName string, loadSnapshot func(string) (tuiRestoreSnapshot, error), claimed map[string]bool, claims *restoreClaimants) (tuiRestoreReport, error) {
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
		snapshot.Live = map[string]backend.LivePane{}
	}
	// Direct single-root callers (tests) may pass nil claims: fall back to this
	// store's own rows, the pre-cross-root behavior.
	if claims == nil {
		claims = newRestoreClaimants()
		claims.addRows(locked.Panes)
	}

	var report tuiRestoreReport
	changed := false
	var restoreErr error
	var createdPanes []recreatedPane
	for idx := 0; idx < len(locked.Panes); idx++ {
		pane := locked.Panes[idx]
		// Restore only ever repairs rows recorded on the runtime it is
		// restoring. A row from another runtime must never be rebound to this
		// runtime's pane, recreated, or rewritten as a side effect: the herdr
		// lane is read-only here, and only tmux carries backend.RestoreOps, so
		// this stays the deliberately tmux-only session management it was.
		if backend.NormalizeName(pane.Backend) != rt.Name() {
			report.Skipped++
			continue
		}
		// Adoption must run before the dedupe skip: preclaimed live issue rows
		// never reach the alive branch below.
		if key, adoptErr := adoptLegacyLivePaneKey(rt, pane, snapshot, claims); adoptErr != nil {
			restoreErr = errors.Join(restoreErr, adoptErr)
		} else if key != "" {
			pane.ShellKey = key
			locked.Panes[idx].ShellKey = key
			cur := snapshot.Live[pane.PaneID]
			cur.ShellKey = key
			snapshot.Live[pane.PaneID] = cur
			claims.keys[key]++
			report.Adopted++
			changed = true
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
			snapshot.Live[rebound.PaneID] = observedRestoredPane(rt, rebound, pane.WorktreePath, rebound.ShellKey)
			report.Rebound++
			if identity != "" {
				claimed[identity] = true
			}
			changed = true
			continue
		}
		recreated, err := recreateRecordedPane(rt, pane, root, session, commandName)
		if recreated.Track {
			recreated.Index = idx
			locked.Panes[idx] = recreated.Pane
			createdPanes = append(createdPanes, recreated)
			snapshot.Live[recreated.Pane.PaneID] = observedRestoredPane(
				rt, recreated.Pane, recreated.Pane.WorktreePath, recreated.LiveShellKey,
			)
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
			remaining, cleanupErr := stopRestoredPanes(rt, createdPanes)
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

// observedRestoredPane is the in-memory observation a just-restored pane would
// produce on the next sweep. Writing it into the snapshot keeps later rows in
// the same cycle from treating the pane as free.
func observedRestoredPane(rt restoreRuntime, pane state.Pane, currentPath, shellKey string) backend.LivePane {
	return backend.LivePane{
		Ref:         backend.PaneRef{Backend: rt.Name(), Pane: pane.PaneID},
		CurrentPath: currentPath,
		Title:       restorePaneTitle(pane),
		AgentState:  backend.AgentRunning,
		ShellKey:    shellKey,
	}
}

func stopRestoredPanes(rt restoreRuntime, panes []recreatedPane) (map[string]bool, error) {
	remaining := map[string]bool{}
	var stopErr error
	for _, pane := range panes {
		if strings.TrimSpace(pane.LiveShellKey) == "" {
			remaining[pane.Pane.PaneID] = true
			stopErr = errors.Join(stopErr, fmt.Errorf("restored pane %s has no verified liveness key", pane.Pane.PaneID))
			continue
		}
		if err := stopRestoredPane(rt, pane.Pane.PaneID, pane.LiveShellKey); err != nil {
			remaining[pane.Pane.PaneID] = true
			stopErr = errors.Join(stopErr, err)
		}
	}
	return remaining, stopErr
}

func loadTUIRestoreSnapshot(rt restoreRuntime, session string) (tuiRestoreSnapshot, error) {
	livePanes, err := rt.ListLiveForIdentity()
	if err != nil {
		return tuiRestoreSnapshot{}, err
	}
	live := make(map[string]backend.LivePane, len(livePanes))
	for _, pane := range livePanes {
		live[pane.Ref.Pane] = pane
	}
	sessionPanes, err := rt.ListPanes(session)
	if err != nil {
		return tuiRestoreSnapshot{}, err
	}
	// Best-effort: a failed query leaves ServerStart zero, which only disables
	// legacy-pane adoption for this cycle; restore itself proceeds.
	serverStart, _ := rt.ServerStartTime()
	return tuiRestoreSnapshot{Live: live, PanesByTitle: liveSessionPanesByTitle(sessionPanes, live), ServerStart: serverStart}, nil
}

func liveSessionPanesByTitle(sessionPanes []backend.PaneInfo, live map[string]backend.LivePane) map[string][]backend.LivePane {
	out := map[string][]backend.LivePane{}
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

func rebindRecordedPane(pane state.Pane, panesByTitle map[string][]backend.LivePane) (state.Pane, bool) {
	for _, title := range restorePaneTitleCandidates(pane) {
		for _, livePane := range panesByTitle[title] {
			candidate := pane
			candidate.PaneID = livePane.Ref.Pane
			if restorePaneAlive(map[string]backend.LivePane{livePane.Ref.Pane: livePane}, candidate) {
				candidate.AgentStatus = "running"
				return candidate, true
			}
		}
	}
	return state.Pane{}, false
}

func recreateRecordedPane(rt restoreRuntime, pane state.Pane, root, session, commandName string) (recreatedPane, error) {
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
	ref, err := rt.Launch(backend.LaunchRequest{
		Target:       tuiLaunchTarget(session),
		WorktreePath: pane.WorktreePath,
		Command:      command,
	})
	if err != nil {
		return result, err
	}
	paneID := ref.Pane
	pane.PaneID = paneID
	pane.AgentStatus = "running"
	result.Pane = pane
	// Not best-effort: every recreated pane must bind its state row to this
	// exact runtime pane before the new pane id is persisted.
	if err := rt.StampPaneShellKey(paneID, pane.ShellKey); err != nil {
		stampErr := fmt.Errorf("restore pane liveness key on %s: %w", paneID, err)
		if cleanupErr := rt.CloseFresh(restoredPaneRef(rt, paneID)); cleanupErr != nil {
			result.Track = true
			return result, errors.Join(stampErr, fmt.Errorf("stop unstamped restored pane %s: %w", paneID, cleanupErr))
		}
		return result, stampErr
	}
	result.LiveShellKey = pane.ShellKey
	decorateRestoredPane(rt, paneID, root, pane)
	if statusPath != "" {
		status, err := waitRestoredPaneReady(statusPath, panelaunch.CodexPlanTUIStartupTimeout)
		_ = os.Remove(statusPath)
		if err != nil {
			statusErr := fmt.Errorf("resume Codex Plan Mode TUI in pane %s: %w", paneID, err)
			if cleanupErr := stopRestoredPane(rt, paneID, pane.ShellKey); cleanupErr != nil {
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

// decorateRestoredPane reapplies the display metadata the pane carried before
// the restart. Every call is cosmetic: the pane is usable even when the runtime
// rejects a title, a border label, or a keybinding hint.
func decorateRestoredPane(rt restoreRuntime, paneID, root string, pane state.Pane) {
	_ = rt.SetPaneTitle(paneID, restorePaneTitle(pane))
	_ = rt.SetPaneLabel(paneID, panelaunch.BorderLabel(pane.Parent, restorePaneTitle(pane)))
	_ = rt.EnablePaneBorderTitles(paneID)
	_ = rt.SetPaneProjectRoot(paneID, root)               // dashboard keybinding hint
	_ = rt.SetPaneWorktreePath(paneID, pane.WorktreePath) // same-worktree action target
}

func restoredPaneRef(rt restoreRuntime, paneID string) backend.PaneRef {
	return backend.PaneRef{Backend: rt.Name(), Pane: paneID}
}

func stopRestoredPane(rt restoreRuntime, paneID, shellKey string) error {
	result, err := rt.CloseOwned(backend.CloseRequest{Ref: restoredPaneRef(rt, paneID), ShellKey: shellKey})
	if err != nil {
		return fmt.Errorf("stop restored pane %s: %w", paneID, err)
	}
	if result.Status == backend.CloseFailed {
		return fmt.Errorf("restored pane %s remained live after close", paneID)
	}
	return nil
}

func restoreAgentCommand(pane state.Pane, root, commandName string) (string, string, error) {
	if pane.PlanMode && pane.Agent == "codex" {
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

// restoreClaimants indexes, across every restore root's store, how many rows
// record each pane id and each liveness key. Adoption consults it so a pane
// or key that any other row — in any root — also claims fails closed.
type restoreClaimants struct {
	paneIDs map[string]int
	keys    map[string]int
	// complete is false when a store could not be read; an incomplete sweep
	// may hide a claimant, so it disables adoption for the whole cycle.
	complete bool
}

func newRestoreClaimants() *restoreClaimants {
	return &restoreClaimants{paneIDs: map[string]int{}, keys: map[string]int{}, complete: true}
}

func collectRestoreClaimants(roots []string) *restoreClaimants {
	claims := newRestoreClaimants()
	for _, root := range uniqueRestoreRoots(roots) {
		store, err := state.LoadProject(root)
		if err != nil {
			claims.complete = false
			continue
		}
		claims.addRows(store.Panes)
	}
	return claims
}

func (c *restoreClaimants) addRows(rows []state.Pane) {
	for _, pane := range rows {
		if id := strings.TrimSpace(pane.PaneID); id != "" {
			c.paneIDs[id]++
		}
		if key := strings.TrimSpace(pane.ShellKey); key != "" {
			c.keys[key]++
		}
	}
}

// adoptLegacyLivePaneKey migrates a keyless legacy agent row whose recorded
// pane is still alive: it stamps a fresh liveness key on the live pane so the
// row graduates to the keyed identity that pane close requires, and returns the
// key to persist.
//
// Ownership proof: the runtime never reuses a pane id within one process
// generation, so a row created at or after the generation's start time still
// owns its recorded pane id — the live pane at that id is the exact pane the
// row launched. Rows older than that generation (or with an unparsable
// CreatedAt, or when the start time is unknown) fail closed: their recorded id
// may have been reused by an unrelated pane. The incarnation proof is
// generation-scoped, so a second, per-pane proof binds the exact pane: the live
// pane's root-process start time must fall in the window in which the row
// recorded its pane.
// On top of those, adoption keeps defense-in-depth requirements: the live
// pane must carry the launch-time fanout markers (recorded worktree path equal
// to the row's worktree and border label equal to the row's), the row must be
// the only claimant of the pane id across every restore root's store, and the
// row must be alive by path containment. Rows that are keyed, shells, or
// missing a worktree path return ("", nil).
//
// A live pane already holding a key is an interrupted earlier adoption
// (stamped, then the state save failed or the process died) — within the
// same generation nothing else stamps a key on this row's own pane without
// also recording a row, and a recorded key is rejected via the claimant index.
// Such an orphan key is re-associated without restamping. The runtime stamp
// happens before the state row is persisted; the reverse order would create
// the fail-closed keyed-row-without-live-key state on stamp failure.
func adoptLegacyLivePaneKey(rt restoreRuntime, pane state.Pane, snapshot tuiRestoreSnapshot, claims *restoreClaimants) (string, error) {
	live := snapshot.Live
	if claims == nil || !claims.complete {
		return "", nil
	}
	if pane.IsShell() || strings.TrimSpace(pane.ShellKey) != "" || strings.TrimSpace(pane.WorktreePath) == "" {
		return "", nil
	}
	created, createdErr := time.Parse(time.RFC3339, strings.TrimSpace(pane.CreatedAt))
	if snapshot.ServerStart.IsZero() || createdErr != nil || created.Before(snapshot.ServerStart) {
		//nolint:nilerr // Unprovable incarnation declines adoption; the row stays legacy, not an error.
		return "", nil
	}
	cur, ok := live[pane.PaneID]
	if !ok {
		return "", nil
	}
	liveWorktree := strings.TrimSpace(cur.WorktreePath)
	if liveWorktree == "" || filepath.Clean(liveWorktree) != filepath.Clean(strings.TrimSpace(pane.WorktreePath)) {
		return "", nil
	}
	rowLabel := rt.CanonicalPaneLabel(panelaunch.BorderLabel(pane.Parent, restorePaneTitle(pane)))
	if liveLabel := strings.TrimSpace(cur.PaneLabel); liveLabel == "" || liveLabel != strings.TrimSpace(rowLabel) {
		return "", nil
	}
	if claims.paneIDs[strings.TrimSpace(pane.PaneID)] != 1 {
		return "", nil
	}
	if !restorePaneAlive(live, pane) {
		return "", nil
	}
	// Per-pane provenance: the live pane's root process must have started in
	// the window in which this row recorded its pane. A pane id coincidence —
	// another generation, another runtime instance — cannot fake the process
	// start time matching the row's CreatedAt.
	paneStart, startErr := rt.PaneStartTime(pane.PaneID)
	if startErr != nil {
		//nolint:nilerr // Unverifiable provenance declines adoption; the next poll retries.
		return "", nil
	}
	if paneStart.Before(created.Add(-adoptPaneStartEarly)) || paneStart.After(created.Add(adoptPaneStartLate)) {
		return "", nil
	}
	if liveKey := strings.TrimSpace(cur.ShellKey); liveKey != "" {
		if claims.keys[liveKey] > 0 {
			return "", nil
		}
		return liveKey, nil
	}
	key, err := panelaunch.NewShellPaneKey()
	if err != nil {
		return "", fmt.Errorf("adopt legacy pane %s: %w", pane.PaneID, err)
	}
	if err := rt.StampPaneShellKey(pane.PaneID, key); err != nil {
		return "", fmt.Errorf("adopt legacy pane %s liveness key: %w", pane.PaneID, err)
	}
	return key, nil
}

func restorePaneAlive(live map[string]backend.LivePane, pane state.Pane) bool {
	if strings.TrimSpace(pane.PaneID) == "" {
		return false
	}
	cur, ok := live[pane.PaneID]
	if !ok {
		return false
	}
	// Rows recorded with a ShellKey match on the pane's liveness key. Legacy
	// rows fall back to their worktree path for read-only restore discovery.
	if pane.IsShell() || strings.TrimSpace(pane.ShellKey) != "" {
		return strings.TrimSpace(pane.ShellKey) != "" && cur.ShellKey == pane.ShellKey
	}
	return pathWithin(cur.CurrentPath, pane.WorktreePath)
}

// restorePaneIdentityUnknown reports the fail-closed middle state for a keyed
// record whose pane id is still live but whose runtime liveness token is empty.
// The pane may still own the running agent process tree, so restore must not
// launch a replacement until the identity can be resolved.
func restorePaneIdentityUnknown(live map[string]backend.LivePane, pane state.Pane) bool {
	key := strings.TrimSpace(pane.ShellKey)
	if key == "" {
		return false
	}
	cur, ok := live[pane.PaneID]
	return ok && strings.TrimSpace(cur.ShellKey) == ""
}

func restorePaneIDStillBelongsToRecord(livePane backend.LivePane, pane state.Pane) bool {
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
	r.Adopted += other.Adopted
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
	if r.Adopted > 0 {
		parts = append(parts, fmt.Sprintf("adopted %d legacy pane(s)", r.Adopted))
	}
	if r.RemovedShells > 0 {
		parts = append(parts, fmt.Sprintf("removed %d stale terminal(s)", r.RemovedShells))
	}
	return strings.Join(parts, ", ")
}
