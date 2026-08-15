package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type HerdrRestartRuntime interface {
	LaunchRoute() (backend.OwnedLaunchRoute, error)
	WorkloadEnvironment([]string, string) ([]string, error)
	PrepareWorkloadEnvironment(string, []string) (string, int, error)
	DiscardWorkloadEnvironment(string, *state.LaunchCapsule) error
	WaitRestoredPanes(context.Context, time.Duration, func([]backend.LivePane) bool) backend.WaitResult
	IssueRestartResume(
		context.Context,
		string,
		string,
		time.Time,
		func(backend.PaneProcessInfo, []backend.LivePane) error,
		func() error,
	) error
	ObserveRestartResume(context.Context, string) (backend.PaneProcessInfo, []backend.LivePane, error)
}

type herdrRestartRow struct {
	root    string
	current bool
	saved   state.Pane
}

type herdrRestartCandidate struct {
	row  herdrRestartRow
	live backend.LivePane
}

func resumeRestartedHerdrRows(
	ctx context.Context,
	projectRoot string,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	totalTimeout time.Duration,
) error {
	rows, err := loadHerdrRestartRows(projectRoot, locked)
	if err != nil {
		return err
	}
	totalTimeout, err = herdrRestartWaitTimeout(totalTimeout)
	if err != nil {
		return err
	}
	deadline := herdrRestartResumeDeadline(ctx, totalTimeout)
	route, err := restarted.LaunchRoute()
	if err != nil {
		return err
	}
	return resumePendingHerdrRestartRows(
		ctx, locked, journal, restarted, route, rows, totalTimeout, deadline,
	)
}

func resumePendingHerdrRestartRows(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	route backend.OwnedLaunchRoute,
	rows []herdrRestartRow,
	totalTimeout time.Duration,
	deadline time.Time,
) error {
	firstWait := true
	for len(rows) != 0 {
		wait, observed, err := observePendingHerdrRestartRows(ctx, restarted, rows, totalTimeout, deadline, firstWait)
		if err != nil {
			return err
		}
		if !observed {
			return retireHerdrRestartRows(ctx, locked, journal, restarted, route, rows, deadline)
		}
		rows, err = retireUnsupportedHerdrRestartRows(ctx, locked, journal, restarted, route, rows, deadline)
		if err != nil {
			return err
		}
		if wait.Status == backend.WaitTimedOut {
			return retireHerdrRestartRows(ctx, locked, journal, restarted, route, rows, deadline)
		}
		rows, err = processObservedHerdrRestartRows(ctx, locked, journal, restarted, route, rows, wait.Panes, deadline)
		if err != nil {
			return err
		}
		firstWait = false
	}
	return nil
}

func observePendingHerdrRestartRows(
	ctx context.Context,
	restarted HerdrRestartRuntime,
	rows []herdrRestartRow,
	totalTimeout time.Duration,
	deadline time.Time,
	first bool,
) (backend.WaitResult, bool, error) {
	waitTimeout, ok := nextHerdrRestartWaitTimeout(totalTimeout, deadline, first)
	if !ok {
		return backend.WaitResult{}, false, nil
	}
	wait, err := waitForHerdrRestartRows(ctx, restarted, rows, waitTimeout)
	if err != nil {
		return wait, false, err
	}
	if err := validateRestartedTerminals(rows, wait.Panes); err != nil {
		return wait, false, err
	}
	return wait, true, nil
}

func nextHerdrRestartWaitTimeout(totalTimeout time.Duration, deadline time.Time, first bool) (time.Duration, bool) {
	if first {
		return totalTimeout, true
	}
	remaining := time.Until(deadline).Truncate(time.Second)
	return remaining, remaining >= 3*time.Second
}

func waitForHerdrRestartRows(
	ctx context.Context,
	restarted HerdrRestartRuntime,
	rows []herdrRestartRow,
	totalTimeout time.Duration,
) (backend.WaitResult, error) {
	waitForCandidate := slices.ContainsFunc(rows, func(row herdrRestartRow) bool {
		return resumableSavedCodex(row.saved)
	})
	wait := restarted.WaitRestoredPanes(ctx, totalTimeout, func(live []backend.LivePane) bool {
		return !waitForCandidate || anyHerdrRestartRouteObserved(rows, live)
	})
	if wait.Status == backend.WaitFailed || wait.Status == backend.WaitCancelled {
		return wait, fmt.Errorf("wait for restarted Herdr panes: %w", wait.Err)
	}
	return wait, nil
}

func retireUnsupportedHerdrRestartRows(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	route backend.OwnedLaunchRoute,
	rows []herdrRestartRow,
	deadline time.Time,
) ([]herdrRestartRow, error) {
	remaining := make([]herdrRestartRow, 0, len(rows))
	for _, row := range rows {
		if resumableSavedCodex(row.saved) {
			remaining = append(remaining, row)
			continue
		}
		if err := processRestartedHerdrRow(
			ctx, locked, journal, restarted, route, row, herdrRestartCandidate{}, false, deadline,
		); err != nil {
			return nil, err
		}
	}
	return remaining, nil
}

func processObservedHerdrRestartRows(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	route backend.OwnedLaunchRoute,
	rows []herdrRestartRow,
	live []backend.LivePane,
	deadline time.Time,
) ([]herdrRestartRow, error) {
	candidates := restartedCodexCandidates(rows, live)
	remaining := make([]herdrRestartRow, 0, len(rows))
	for _, row := range rows {
		if countHerdrRestartRoute(row.saved, live) == 0 {
			remaining = append(remaining, row)
			continue
		}
		candidate, eligible := candidates[herdrRestartRouteKey(row.saved)]
		if err := processRestartedHerdrRow(
			ctx, locked, journal, restarted, route, row, candidate, eligible, deadline,
		); err != nil {
			return nil, err
		}
	}
	return remaining, nil
}

func retireHerdrRestartRows(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	route backend.OwnedLaunchRoute,
	rows []herdrRestartRow,
	deadline time.Time,
) error {
	for _, row := range rows {
		if err := processRestartedHerdrRow(
			ctx, locked, journal, restarted, route, row, herdrRestartCandidate{}, false, deadline,
		); err != nil {
			return err
		}
	}
	return nil
}

func herdrRestartResumeDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if outer, ok := ctx.Deadline(); ok && outer.Before(deadline) {
		return outer
	}
	return deadline
}

func herdrRestartWaitTimeout(totalTimeout time.Duration) (time.Duration, error) {
	if totalTimeout == 0 {
		return backend.DefaultWaitTimeout, nil
	}
	if totalTimeout < 3*time.Second || totalTimeout%time.Second != 0 {
		return 0, fmt.Errorf("herdr restart wait timeout must be whole seconds and at least 3s")
	}
	return totalTimeout, nil
}

func loadHerdrRestartRows(projectRoot string, locked *state.LockedStore) ([]herdrRestartRow, error) {
	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("list linked worktrees after Herdr restart: %w", err)
	}
	currentRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize Herdr restart root: %w", err)
	}
	seen := map[string]bool{}
	var rows []herdrRestartRow
	for _, root := range roots {
		canonicalRoot, canonicalErr := filepath.EvalSymlinks(root)
		if canonicalErr != nil {
			return nil, fmt.Errorf("canonicalize linked Herdr state root: %w", canonicalErr)
		}
		if seen[canonicalRoot] {
			continue
		}
		seen[canonicalRoot] = true
		current := canonicalRoot == currentRoot
		store, loadErr := loadHerdrRestartStore(canonicalRoot, current, locked)
		if loadErr != nil {
			return nil, loadErr
		}
		rows = append(rows, herdrRestartRowsInStore(canonicalRoot, current, store)...)
	}
	return rows, nil
}

func loadHerdrRestartStore(root string, current bool, locked *state.LockedStore) (state.Store, error) {
	if current {
		return locked.Store, nil
	}
	return state.LoadProject(root)
}

func herdrRestartRowsInStore(root string, current bool, store state.Store) []herdrRestartRow {
	rows := make([]herdrRestartRow, 0, len(store.Panes))
	for _, pane := range store.Panes {
		if backend.NormalizeName(pane.Backend) == backend.Herdr && pane.TerminalID != "" {
			rows = append(rows, herdrRestartRow{root: root, current: current, saved: pane})
		}
	}
	return rows
}

func anyHerdrRestartRouteObserved(rows []herdrRestartRow, live []backend.LivePane) bool {
	for _, row := range rows {
		if countHerdrRestartRoute(row.saved, live) != 0 {
			return true
		}
	}
	return false
}

func completeHerdrRestartRoute(saved state.Pane) bool {
	return saved.SessionID != "" && saved.SocketPath != "" &&
		saved.WorkspaceID != "" && saved.PaneID != ""
}

func countHerdrRestartRoute(saved state.Pane, live []backend.LivePane) int {
	matches := 0
	for _, current := range live {
		if sameHerdrRestartRoute(saved, current) {
			matches++
		}
	}
	return matches
}

func validateRestartedTerminals(rows []herdrRestartRow, live []backend.LivePane) error {
	for _, row := range rows {
		for _, current := range live {
			if sameHerdrRestartRoute(row.saved, current) &&
				(current.TerminalID == "" || current.TerminalID == row.saved.TerminalID) {
				return fmt.Errorf("saved Herdr row terminal identity is not stale after restart")
			}
		}
	}
	return nil
}

func restartedCodexCandidates(
	rows []herdrRestartRow,
	live []backend.LivePane,
) map[string]herdrRestartCandidate {
	candidates := map[string]herdrRestartCandidate{}
	claimed := map[string]bool{}
	for _, row := range rows {
		current, ok := exactRestartedCodexPlaceholder(row.saved, live)
		if !ok || !allHerdrRestartSessionRoutesObserved(row.saved, rows, live) {
			continue
		}
		key := herdrRestartRouteKey(row.saved)
		if claimed[key] {
			delete(candidates, key)
			continue
		}
		claimed[key] = true
		candidates[key] = herdrRestartCandidate{row: row, live: current}
	}
	return candidates
}

func allHerdrRestartSessionRoutesObserved(
	saved state.Pane,
	rows []herdrRestartRow,
	live []backend.LivePane,
) bool {
	for _, row := range rows {
		sameRef := row.saved.AgentSession != nil && saved.AgentSession != nil &&
			*row.saved.AgentSession == *saved.AgentSession
		if sameRef && countHerdrRestartRoute(row.saved, live) == 0 {
			return false
		}
	}
	return true
}

func exactRestartedCodexPlaceholder(saved state.Pane, live []backend.LivePane) (backend.LivePane, bool) {
	if !resumableSavedCodex(saved) || countExactAgentSession(live, saved.AgentSession) != 1 {
		return backend.LivePane{}, false
	}
	var matched backend.LivePane
	matches := 0
	for _, current := range live {
		if restartedCodexPlaceholderMatches(saved, current) {
			matched, matches = current, matches+1
		}
	}
	return matched, matches == 1
}

func resumableSavedCodex(saved state.Pane) bool {
	requirements := []bool{
		backend.NormalizeName(saved.Backend) == backend.Herdr,
		saved.Kind != state.PaneKindAttachedAgent,
		saved.Agent == "codex", saved.DirectAgentLaunch, !saved.PlanMode,
		exactCodexSessionRef(saved.AgentSession), saved.AgentID != "",
		cleanAbsolutePath(saved.LaunchExecutable), cleanAbsolutePath(saved.WorktreePath),
		saved.RepoKey != "", cleanAbsolutePath(saved.RepoRoot),
		saved.ProcessIdentity != nil && saved.ProcessIdentity.Valid(),
		completeHerdrRestartRoute(saved), saved.WorkspaceLabel != "",
	}
	return !slices.Contains(requirements, false)
}

func exactCodexSessionRef(ref *backend.AgentSessionRef) bool {
	return ref != nil && ref.Source == "herdr:codex" && ref.Agent == "codex" &&
		ref.Kind == "id" && strings.TrimSpace(ref.Value) != "" && ref.Value == strings.TrimSpace(ref.Value)
}

func cleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

func countExactAgentSession(live []backend.LivePane, ref *backend.AgentSessionRef) int {
	matches := 0
	for _, current := range live {
		if current.AgentSession != nil && ref != nil && *current.AgentSession == *ref {
			matches++
		}
	}
	return matches
}

func restartedCodexPlaceholderMatches(saved state.Pane, current backend.LivePane) bool {
	sameSession := current.AgentSession != nil && saved.AgentSession != nil &&
		*current.AgentSession == *saved.AgentSession
	requirements := []bool{
		sameHerdrRestartRoute(saved, current), current.TerminalID != "",
		current.TerminalID != saved.TerminalID,
		current.WorkspaceLabel == saved.WorkspaceLabel, sameSession,
		!current.AgentPresent, current.AgentID == "", current.AgentProvider == "",
		current.RepoKey == saved.RepoKey, current.ProjectRoot == saved.RepoRoot,
		current.WorktreePath == saved.WorktreePath, current.CurrentPath == saved.WorktreePath,
	}
	return !slices.Contains(requirements, false)
}

func herdrRestartRouteKey(saved state.Pane) string {
	return saved.SessionID + "\x00" + saved.SocketPath + "\x00" +
		saved.WorkspaceID + "\x00" + saved.PaneID
}

func processRestartedHerdrRow(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	route backend.OwnedLaunchRoute,
	row herdrRestartRow,
	candidate herdrRestartCandidate,
	eligible bool,
	deadline time.Time,
) error {
	intent, found, err := existingHerdrResumeIntent(journal, row.saved)
	if err != nil {
		return err
	}
	if found {
		return finishHerdrResumeJournal(ctx, locked, journal, restarted, route.RuntimeDir, row, intent)
	}
	if !eligible || candidate.row.root != row.root {
		return persistHerdrRestartRow(ctx, locked, row, nil, nil, nil)
	}
	intent, err = prepareHerdrResumeIntent(journal, restarted, route, candidate, deadline)
	if err != nil {
		return errors.Join(err, persistHerdrRestartRow(ctx, locked, row, nil, nil, nil))
	}
	launchErr := startRestartedCodex(ctx, restarted, journal, route, &intent)
	if launchErr != nil {
		return finishHerdrResumeJournal(ctx, locked, journal, restarted, route.RuntimeDir, row, intent)
	}
	return finishSuccessfulHerdrResume(
		ctx, locked, journal, restarted, route.RuntimeDir, row, intent,
	)
}

func finishSuccessfulHerdrResume(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	runtimeDir string,
	row herdrRestartRow,
	intent state.LaunchIntent,
) error {
	if err := finishHerdrResumeIntent(journal, restarted, runtimeDir, intent); err != nil {
		return err
	}
	live, process, err := observeRestartedCodexOnce(ctx, restarted, intent)
	if err != nil {
		return persistHerdrRestartRow(ctx, locked, row, nil, nil, nil)
	}
	return persistHerdrRestartRow(ctx, locked, row, &live, &process, intent.Launch)
}

func existingHerdrResumeIntent(
	journal *state.LockedLaunchJournal,
	pane state.Pane,
) (state.LaunchIntent, bool, error) {
	if !completeHerdrRestartRoute(pane) {
		return state.LaunchIntent{}, false, nil
	}
	id, err := state.ResumeIntentID(
		pane.SessionID, pane.SocketPath, pane.WorkspaceID, pane.PaneID,
	)
	if err != nil {
		return state.LaunchIntent{}, false, err
	}
	intent, found := journal.FindIntent(id)
	if !found {
		return state.LaunchIntent{}, false, nil
	}
	if intent.Kind != state.IntentResume || !herdrResumeIntentTargetsRow(intent, pane) {
		return state.LaunchIntent{}, false, fmt.Errorf("saved Herdr resume intent does not match its state row")
	}
	return intent, true, nil
}

func herdrResumeIntentTargetsRow(intent state.LaunchIntent, pane state.Pane) bool {
	return intent.Parent == pane.Parent && intent.RuntimeParent == pane.RuntimeParent &&
		intent.IssueNum == pane.IssueNum && intent.TaskID == pane.TaskID &&
		intent.WorktreePath == pane.WorktreePath && intent.Session == pane.SessionID &&
		intent.SocketPath == pane.SocketPath &&
		intent.Resource.WorkspaceID == pane.WorkspaceID && intent.Resource.PaneID == pane.PaneID
}

func prepareHerdrResumeIntent(
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	route backend.OwnedLaunchRoute,
	candidate herdrRestartCandidate,
	deadline time.Time,
) (state.LaunchIntent, error) {
	saved := candidate.row.saved
	id, err := state.ResumeIntentID(saved.SessionID, saved.SocketPath, saved.WorkspaceID, saved.PaneID)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	nonce, err := randomHerdrToken()
	if err != nil {
		return state.LaunchIntent{}, err
	}
	environment, err := restarted.WorkloadEnvironment(os.Environ(), route.LauncherPath)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	envPath, envCount, err := restarted.PrepareWorkloadEnvironment(nonce, environment)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	intent := newHerdrResumeIntent(id, nonce, envPath, envCount, candidate, deadline)
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		return state.LaunchIntent{}, errors.Join(
			err, restarted.DiscardWorkloadEnvironment(route.RuntimeDir, intent.Launch),
		)
	}
	return intent, nil
}

func newHerdrResumeIntent(
	id, nonce, envPath string,
	envCount int,
	candidate herdrRestartCandidate,
	deadline time.Time,
) state.LaunchIntent {
	saved, current := candidate.row.saved, candidate.live
	ownerRoot := ""
	if saved.Parent == ManualParentRef || strings.HasPrefix(saved.Parent, "plan:") {
		ownerRoot = candidate.row.root
	}
	ref := *saved.AgentSession
	return state.LaunchIntent{
		ID: id, Kind: state.IntentResume, Status: state.IntentRealized,
		Parent: saved.Parent, RuntimeParent: saved.RuntimeParent, OwnerProjectRoot: ownerRoot,
		IssueNum: saved.IssueNum, TaskID: saved.TaskID, WorktreePath: saved.WorktreePath,
		WorkspaceLabel: saved.WorkspaceLabel,
		Resource: state.RuntimeResource{
			WorkspaceID: current.Ref.Workspace, Label: current.WorkspaceLabel,
			PaneID: current.Ref.Pane, TerminalID: current.TerminalID, CurrentPath: current.CurrentPath,
			RepoKey: current.RepoKey, RepoRoot: current.ProjectRoot,
		},
		Session: saved.SessionID, SocketPath: saved.SocketPath,
		ExpiresUnixMS: deadline.UnixMilli(), ResumeAgentSession: &ref,
		Launch: &state.LaunchCapsule{
			Nonce: nonce, Agent: "codex", Executable: saved.LaunchExecutable,
			Args: []string{"resume", ref.Value}, EnvFilePath: envPath, EnvNameCount: envCount,
		},
	}
}

func startRestartedCodex(
	ctx context.Context,
	restarted HerdrRestartRuntime,
	journal *state.LockedLaunchJournal,
	route backend.OwnedLaunchRoute,
	intent *state.LaunchIntent,
) error {
	preflight := func(info backend.PaneProcessInfo, panes []backend.LivePane) error {
		return preflightRestartedCodex(info, panes, *intent, route)
	}
	markIssued := func() error {
		intent.Launch.LauncherReady = true
		intent.Launch.TokenIssued = true
		return saveHerdrLaunchPhase(journal, *intent)
	}
	if err := restarted.IssueRestartResume(
		ctx, intent.Resource.PaneID, intent.Launch.Nonce, time.UnixMilli(intent.ExpiresUnixMS),
		preflight, markIssued,
	); err != nil {
		return err
	}
	_, _, err := waitForRestartedCodexProcess(ctx, restarted, *intent)
	return err
}

func preflightRestartedCodex(
	info backend.PaneProcessInfo,
	panes []backend.LivePane,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
) error {
	if err := verifyHerdrLauncherProcess(info, intent, route); err != nil {
		return err
	}
	if countExactAgentSession(panes, intent.ResumeAgentSession) != 1 {
		return fmt.Errorf("herdr Codex resume session is no longer unique")
	}
	for _, pane := range panes {
		if exactHerdrResumePlaceholder(intent, pane) {
			return nil
		}
	}
	return fmt.Errorf("exact Herdr Codex resume placeholder is not live")
}

func waitForRestartedCodexProcess(
	ctx context.Context,
	restarted HerdrRestartRuntime,
	intent state.LaunchIntent,
) (backend.LivePane, backend.ProcessIdentity, error) {
	var live backend.LivePane
	var identity backend.ProcessIdentity
	err := retryHerdrObservation(ctx, intent, func(observeCtx context.Context) error {
		info, panes, err := restarted.ObserveRestartResume(observeCtx, intent.Resource.PaneID)
		if err != nil {
			return err
		}
		if countExactAgentSession(panes, intent.ResumeAgentSession) != 1 {
			return herdrLaunchTransitionPending{}
		}
		var found bool
		live, found = restartedCodexPane(intent, panes)
		if !found {
			return herdrLaunchTransitionPending{}
		}
		identity, err = matchHerdrAgentProcess(info, intent)
		if err != nil {
			return herdrLaunchTransitionPending{}
		}
		return nil
	})
	return live, identity, err
}

func exactHerdrResumePlaceholder(intent state.LaunchIntent, pane backend.LivePane) bool {
	return exactHerdrResumeRoute(intent, pane) && !pane.AgentPresent &&
		pane.AgentID == "" && pane.AgentProvider == ""
}

func restartedCodexPane(intent state.LaunchIntent, panes []backend.LivePane) (backend.LivePane, bool) {
	for _, pane := range panes {
		if exactHerdrResumeRoute(intent, pane) && pane.AgentPresent &&
			pane.AgentProvider == "codex" && pane.AgentID != "" {
			return pane, true
		}
	}
	return backend.LivePane{}, false
}

func exactHerdrResumeRoute(intent state.LaunchIntent, pane backend.LivePane) bool {
	sameAgentSession := pane.AgentSession != nil && intent.ResumeAgentSession != nil &&
		*pane.AgentSession == *intent.ResumeAgentSession
	requirements := []bool{
		pane.Ref.Backend == backend.Herdr, pane.Ref.Workspace == intent.Resource.WorkspaceID,
		pane.Ref.Pane == intent.Resource.PaneID, pane.TerminalID == intent.Resource.TerminalID,
		pane.WorkspaceLabel == intent.Resource.Label, pane.CurrentPath == intent.Resource.CurrentPath,
		pane.RepoKey == intent.Resource.RepoKey, pane.ProjectRoot == intent.Resource.RepoRoot,
		pane.WorktreePath == intent.WorktreePath,
		pane.SessionID == intent.Session, pane.SocketPath == intent.SocketPath,
		sameAgentSession,
	}
	return !slices.Contains(requirements, false)
}

func observeRestartedCodexOnce(
	ctx context.Context,
	restarted HerdrRestartRuntime,
	intent state.LaunchIntent,
) (backend.LivePane, backend.ProcessIdentity, error) {
	info, panes, err := restarted.ObserveRestartResume(ctx, intent.Resource.PaneID)
	if err != nil {
		return backend.LivePane{}, backend.ProcessIdentity{}, err
	}
	if countExactAgentSession(panes, intent.ResumeAgentSession) != 1 {
		return backend.LivePane{}, backend.ProcessIdentity{}, fmt.Errorf("resumed Herdr Codex session is not unique")
	}
	live, found := restartedCodexPane(intent, panes)
	if !found {
		return backend.LivePane{}, backend.ProcessIdentity{}, fmt.Errorf("resumed Herdr Codex pane is not exact")
	}
	identity, err := matchHerdrAgentProcess(info, intent)
	return live, identity, err
}

func finishHerdrResumeJournal(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	runtimeDir string,
	row herdrRestartRow,
	intent state.LaunchIntent,
) error {
	if err := finishHerdrResumeIntent(journal, restarted, runtimeDir, intent); err != nil {
		return err
	}
	return persistHerdrRestartRow(ctx, locked, row, nil, nil, nil)
}

func finishHerdrResumeIntent(
	journal *state.LockedLaunchJournal,
	restarted HerdrRestartRuntime,
	runtimeDir string,
	intent state.LaunchIntent,
) error {
	if err := restarted.DiscardWorkloadEnvironment(runtimeDir, intent.Launch); err != nil {
		return err
	}
	if !journal.RemoveIntent(intent.ID) {
		return fmt.Errorf("herdr resume intent %s disappeared before completion", intent.ID)
	}
	if err := journal.Save(); err != nil {
		return err
	}
	return nil
}

func persistHerdrRestartRow(
	ctx context.Context,
	locked *state.LockedStore,
	row herdrRestartRow,
	live *backend.LivePane,
	process *backend.ProcessIdentity,
	launch *state.LaunchCapsule,
) (err error) {
	if row.current {
		if applyErr := applyHerdrRestartRow(&locked.Store, row.saved, live, process, launch); applyErr != nil {
			return applyErr
		}
		return locked.Save()
	}
	sibling, err := state.LockContext(ctx, state.Path(row.root))
	if err != nil {
		return fmt.Errorf("lock linked Herdr state in %s: %w", row.root, err)
	}
	defer func() { err = errors.Join(err, sibling.Unlock()) }()
	if err = applyHerdrRestartRow(&sibling.Store, row.saved, live, process, launch); err != nil {
		return err
	}
	return sibling.Save()
}

func applyHerdrRestartRow(
	store *state.Store,
	saved state.Pane,
	live *backend.LivePane,
	process *backend.ProcessIdentity,
	launch *state.LaunchCapsule,
) error {
	pane, err := findHerdrRestartRow(store, saved)
	if err != nil {
		return err
	}
	if !sameHerdrRestartBaseline(*pane, saved) {
		return fmt.Errorf("saved Herdr restart row changed before finalization")
	}
	nonce, err := randomHerdrToken()
	if err != nil {
		return err
	}
	pane.ReportedState, pane.EmitterRowKey, pane.LaunchNonce = "", "", ""
	pane.StateRefinement, pane.EmitterNonce = false, nonce
	if live == nil || process == nil || launch == nil {
		pane.DirectAgentLaunch = false
		return nil
	}
	pane.TerminalID = live.TerminalID
	pane.AgentID = live.AgentID
	ref := *live.AgentSession
	pane.AgentSession = &ref
	identity := *process
	pane.ProcessIdentity = &identity
	pane.LaunchExecutable = launch.Executable
	pane.LaunchArgs = slices.Clone(launch.Args)
	return nil
}

func findHerdrRestartRow(store *state.Store, saved state.Pane) (*state.Pane, error) {
	for i := range store.Panes {
		pane := &store.Panes[i]
		if pane.Parent == saved.Parent && pane.IssueNum == saved.IssueNum && pane.TaskID == saved.TaskID {
			return pane, nil
		}
	}
	return nil, fmt.Errorf("saved Herdr restart row disappeared before finalization")
}

func sameHerdrRestartBaseline(current, saved state.Pane) bool {
	requirements := []bool{
		current.Parent == saved.Parent, current.RuntimeParent == saved.RuntimeParent,
		current.IssueNum == saved.IssueNum, current.TaskID == saved.TaskID,
		current.Backend == saved.Backend, current.PaneID == saved.PaneID,
		current.WorkspaceID == saved.WorkspaceID,
		current.WorkspaceLabel == saved.WorkspaceLabel,
		current.TerminalID == saved.TerminalID, current.SessionID == saved.SessionID,
		current.SocketPath == saved.SocketPath, current.AgentID == saved.AgentID,
		current.RepoKey == saved.RepoKey, current.RepoRoot == saved.RepoRoot,
		reflect.DeepEqual(current.AgentSession, saved.AgentSession),
		reflect.DeepEqual(current.ProcessIdentity, saved.ProcessIdentity),
		current.LaunchExecutable == saved.LaunchExecutable,
		slices.Equal(current.LaunchArgs, saved.LaunchArgs),
		current.DirectAgentLaunch == saved.DirectAgentLaunch,
		current.Agent == saved.Agent, current.PlanMode == saved.PlanMode,
		current.WorktreePath == saved.WorktreePath,
	}
	return !slices.Contains(requirements, false)
}
