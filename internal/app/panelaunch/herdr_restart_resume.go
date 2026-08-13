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
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type HerdrRestartRuntime interface {
	HerdrLaunchRuntime
	WaitRestoredPanes(context.Context, time.Duration, func([]backend.LivePane) bool) herdrrun.WaitResult
	SendRestartResumeToken(context.Context, string, string) error
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
	journal *state.LockedHerdrIntents,
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
	deadline := time.Now().Add(totalTimeout)
	wait := restarted.WaitRestoredPanes(ctx, totalTimeout, func(live []backend.LivePane) bool {
		return allHerdrRestartRoutesObserved(rows, live)
	})
	if wait.Status == herdrrun.WaitFailed || wait.Status == herdrrun.WaitCancelled {
		return fmt.Errorf("wait for restarted Herdr panes: %w", wait.Err)
	}
	rows, err = recoverIssuedHerdrResumes(ctx, locked, journal, restarted, rows, wait.Panes)
	if err != nil {
		return err
	}
	if err := validateRestartedTerminals(rows, wait.Panes); err != nil {
		return err
	}
	candidates := restartedCodexCandidates(rows, wait.Panes)
	return processRestartedHerdrRows(ctx, locked, journal, restarted, rows, candidates, deadline)
}

func herdrRestartWaitTimeout(totalTimeout time.Duration) (time.Duration, error) {
	if totalTimeout == 0 {
		return herdrrun.DefaultWaitTimeout, nil
	}
	if totalTimeout < 3*time.Second || totalTimeout%time.Second != 0 {
		return 0, fmt.Errorf("Herdr restart wait timeout must be whole seconds and at least 3s")
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

func herdrRestartRowsInStore(root string, current bool, store state.Store) []herdrRestartRow {
	rows := make([]herdrRestartRow, 0, len(store.Panes))
	for _, pane := range store.Panes {
		if backend.NormalizeName(pane.Backend) == backend.Herdr && pane.HerdrTerminalID != "" {
			rows = append(rows, herdrRestartRow{root: root, current: current, saved: pane})
		}
	}
	return rows
}

func loadHerdrRestartStore(root string, current bool, locked *state.LockedStore) (state.Store, error) {
	if current {
		return locked.Store, nil
	}
	return state.LoadProject(root)
}

func allHerdrRestartRoutesObserved(rows []herdrRestartRow, live []backend.LivePane) bool {
	for _, row := range rows {
		if completeHerdrRestartRoute(row.saved) && countHerdrRestartRoute(row.saved, live) == 0 {
			return false
		}
	}
	return true
}

func completeHerdrRestartRoute(saved state.Pane) bool {
	return saved.HerdrSession != "" && saved.HerdrSocketPath != "" &&
		saved.HerdrWorkspaceID != "" && saved.PaneID != ""
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
			if !sameHerdrRestartRoute(row.saved, current) {
				continue
			}
			if current.TerminalID == "" || current.TerminalID == row.saved.HerdrTerminalID {
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
		if !ok {
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

func recoverIssuedHerdrResumes(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	restarted HerdrRestartRuntime,
	rows []herdrRestartRow,
	live []backend.LivePane,
) ([]herdrRestartRow, error) {
	handled := map[string]bool{}
	for _, intent := range slices.Clone(journal.Intents) {
		if intent.Kind != state.HerdrIntentResume || intent.Launch == nil || !intent.Launch.TokenIssued {
			continue
		}
		row, err := exactHerdrResumeIntentRow(rows, intent)
		if err != nil {
			return nil, err
		}
		if err := recoverIssuedHerdrResume(ctx, locked, journal, restarted, row, intent, live); err != nil {
			return nil, err
		}
		handled[herdrRestartRowKey(row)] = true
	}
	return slices.DeleteFunc(slices.Clone(rows), func(row herdrRestartRow) bool {
		return handled[herdrRestartRowKey(row)]
	}), nil
}

func exactHerdrResumeIntentRow(rows []herdrRestartRow, intent state.HerdrIntent) (herdrRestartRow, error) {
	var matched herdrRestartRow
	matches := 0
	for _, row := range rows {
		if herdrResumeIntentTargetsRow(intent, row.saved) {
			matched, matches = row, matches+1
		}
	}
	if matches != 1 {
		return herdrRestartRow{}, fmt.Errorf("issued Herdr resume intent has %d exact state rows", matches)
	}
	return matched, nil
}

func herdrResumeIntentTargetsRow(intent state.HerdrIntent, pane state.Pane) bool {
	return intent.Parent == pane.Parent && intent.RuntimeParent == pane.RuntimeParent &&
		intent.IssueNum == pane.IssueNum && intent.TaskID == pane.TaskID &&
		intent.WorktreePath == pane.WorktreePath && intent.Session == pane.HerdrSession &&
		intent.SocketPath == pane.HerdrSocketPath &&
		intent.Resource.WorkspaceID == pane.HerdrWorkspaceID && intent.Resource.PaneID == pane.PaneID
}

func herdrRestartRowKey(row herdrRestartRow) string {
	return row.root + "\x00" + row.saved.Parent + "\x00" + row.saved.TaskID + "\x00" +
		fmt.Sprint(row.saved.IssueNum)
}

func recoverIssuedHerdrResume(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	restarted HerdrRestartRuntime,
	row herdrRestartRow,
	intent state.HerdrIntent,
	live []backend.LivePane,
) error {
	if intent.Status == state.HerdrIntentManualCleanupRequired {
		return finalizeFailedHerdrResume(ctx, locked, journal, "", row, intent)
	}
	current, ok := restartedCodexPane(intent, live)
	if !ok {
		return finalizeFailedHerdrResume(ctx, locked, journal, "", row, intent)
	}
	process, err := restarted.ProcessInfo(ctx, intent.Resource.PaneID)
	if err == nil {
		var identity backend.ProcessIdentity
		identity, err = matchHerdrAgentProcess(process, intent)
		if err == nil {
			return finishRecoveredHerdrResume(ctx, locked, journal, row, intent, current, identity)
		}
	}
	return finalizeFailedHerdrResume(ctx, locked, journal, "", row, intent)
}

func finishRecoveredHerdrResume(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	row herdrRestartRow,
	intent state.HerdrIntent,
	live backend.LivePane,
	process backend.ProcessIdentity,
) error {
	if err := requireConsumedHerdrResumeEnvironment(intent); err != nil {
		return finalizeFailedHerdrResume(ctx, locked, journal, "", row, intent)
	}
	if completedHerdrResumeBinding(row.saved, intent, live, process) {
		return removeHerdrResumeIntent(journal, intent.ID)
	}
	return completeHerdrResumeRow(ctx, locked, journal, row, intent, live, process)
}

func completedHerdrResumeBinding(
	pane state.Pane,
	intent state.HerdrIntent,
	live backend.LivePane,
	process backend.ProcessIdentity,
) bool {
	requirements := []bool{
		pane.HerdrTerminalID == live.TerminalID, pane.HerdrAgentID == live.AgentID,
		reflect.DeepEqual(pane.HerdrAgentSession, live.AgentSession),
		reflect.DeepEqual(pane.HerdrProcessIdentity, &process),
		pane.HerdrLaunchExecutable == intent.Launch.Executable,
		slices.Equal(pane.HerdrLaunchArgs, intent.Launch.Args), pane.ReportedState == "",
		!pane.StateRefinement, pane.EmitterRowKey == "", pane.LaunchNonce == "",
		pane.EmitterNonce != "",
	}
	return !slices.Contains(requirements, false)
}

func exactRestartedCodexPlaceholder(saved state.Pane, live []backend.LivePane) (backend.LivePane, bool) {
	if !resumableSavedCodex(saved) || countExactAgentSession(live, saved.HerdrAgentSession) != 1 {
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
	ref := saved.HerdrAgentSession
	requirements := []bool{
		backend.NormalizeName(saved.Backend) == backend.Herdr,
		saved.Agent == "codex", saved.HerdrDirectAgentLaunch, !saved.PlanMode,
		exactCodexSessionRef(ref), saved.HerdrAgentID != "",
		cleanAbsolutePath(saved.HerdrLaunchExecutable), cleanAbsolutePath(saved.WorktreePath),
		cleanAbsolutePath(saved.HerdrRepoRoot), saved.HerdrRepoKey != "",
		saved.HerdrProcessIdentity != nil && saved.HerdrProcessIdentity.Valid(),
		completeHerdrRestartRoute(saved), saved.HerdrWorkspaceLabel != "",
	}
	return !slices.Contains(requirements, false)
}

func exactCodexSessionRef(ref *backend.AgentSessionRef) bool {
	return ref != nil && ref.Valid() && ref.Source == "herdr:codex" &&
		ref.Agent == "codex" && ref.Kind == "id"
}

func cleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func countExactAgentSession(live []backend.LivePane, ref *backend.AgentSessionRef) int {
	matches := 0
	for _, current := range live {
		if ref != nil && current.AgentSession != nil && *current.AgentSession == *ref {
			matches++
		}
	}
	return matches
}

func restartedCodexPlaceholderMatches(saved state.Pane, current backend.LivePane) bool {
	requirements := []bool{
		sameHerdrRestartRoute(saved, current), current.TerminalID != "",
		current.TerminalID != saved.HerdrTerminalID,
		current.WorkspaceLabel == saved.HerdrWorkspaceLabel,
		!current.AgentPresent, current.AgentProvider == "", current.AgentID == "",
		current.AgentSession != nil && *current.AgentSession == *saved.HerdrAgentSession,
		current.RepoKey == saved.HerdrRepoKey, current.ProjectRoot == saved.HerdrRepoRoot,
		current.WorktreePath == saved.WorktreePath, current.CurrentPath == saved.WorktreePath,
	}
	return !slices.Contains(requirements, false)
}

func herdrRestartRouteKey(saved state.Pane) string {
	return saved.HerdrSession + "\x00" + saved.HerdrSocketPath + "\x00" +
		saved.HerdrWorkspaceID + "\x00" + saved.PaneID
}

func processRestartedHerdrRows(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	restarted HerdrRestartRuntime,
	rows []herdrRestartRow,
	candidates map[string]herdrRestartCandidate,
	deadline time.Time,
) error {
	if len(rows) == 0 {
		return nil
	}
	route, err := restarted.LaunchRoute()
	if err != nil {
		return err
	}
	launcher := &Launcher{Herdr: restarted}
	for _, row := range rows {
		candidate, eligible := candidates[herdrRestartRouteKey(row.saved)]
		if err := processOneRestartedHerdrRow(
			ctx, locked, journal, restarted, launcher, route, row, candidate, eligible, deadline,
		); err != nil {
			return err
		}
	}
	return nil
}

func processOneRestartedHerdrRow(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	restarted HerdrRestartRuntime,
	launcher *Launcher,
	route herdrrun.OwnedLaunchRoute,
	row herdrRestartRow,
	candidate herdrRestartCandidate,
	eligible bool,
	deadline time.Time,
) error {
	if !eligible || candidate.row.root != row.root {
		return persistHerdrRestartRow(ctx, locked, row, nil, nil, nil)
	}
	intent, err := prepareHerdrResumeIntent(journal, restarted, route, candidate, deadline)
	if err != nil {
		return err
	}
	if intent.Status == state.HerdrIntentManualCleanupRequired || intent.Launch.TokenIssued {
		return finalizeFailedHerdrResume(ctx, locked, journal, route.RuntimeDir, row, intent)
	}
	live, process, err := startRestartedCodex(ctx, restarted, launcher, journal, route, &intent)
	if err != nil {
		return failHerdrResumeRow(ctx, locked, journal, route.RuntimeDir, row, intent, err)
	}
	return completeHerdrResumeRow(ctx, locked, journal, row, intent, live, process)
}

func failHerdrResumeRow(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	runtimeDir string,
	row herdrRestartRow,
	intent state.HerdrIntent,
	cause error,
) error {
	if err := preserveFailedHerdrResume(journal, intent, cause); err != nil {
		return err
	}
	return finalizeFailedHerdrResume(ctx, locked, journal, runtimeDir, row, intent)
}

func finalizeFailedHerdrResume(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	runtimeDir string,
	row herdrRestartRow,
	intent state.HerdrIntent,
) error {
	if !intent.Launch.TokenIssued {
		if err := herdrrun.DiscardWorkloadEnvironment(runtimeDir, intent.Launch); err != nil {
			return err
		}
	}
	if err := persistHerdrRestartRow(ctx, locked, row, nil, nil, nil); err != nil {
		return err
	}
	if !journal.RemoveIntent(intent.ID) {
		return fmt.Errorf("Herdr failed resume intent %s disappeared before completion", intent.ID)
	}
	return journal.Save()
}

func completeHerdrResumeRow(
	ctx context.Context,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	row herdrRestartRow,
	intent state.HerdrIntent,
	live backend.LivePane,
	process backend.ProcessIdentity,
) error {
	if err := persistHerdrRestartRow(ctx, locked, row, &live, &process, intent.Launch); err != nil {
		return err
	}
	return removeHerdrResumeIntent(journal, intent.ID)
}

func removeHerdrResumeIntent(journal *state.LockedHerdrIntents, intentID string) error {
	if !journal.RemoveIntent(intentID) {
		return fmt.Errorf("Herdr resume intent %s disappeared before completion", intentID)
	}
	return journal.Save()
}

func prepareHerdrResumeIntent(
	journal *state.LockedHerdrIntents,
	restarted HerdrRestartRuntime,
	route herdrrun.OwnedLaunchRoute,
	candidate herdrRestartCandidate,
	deadline time.Time,
) (state.HerdrIntent, error) {
	saved := candidate.row.saved
	id, err := state.HerdrResumeIntentID(saved.HerdrSession, saved.HerdrSocketPath, saved.HerdrWorkspaceID, saved.PaneID)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	if existing, found := journal.FindIntent(id); found {
		return existing, nil
	}
	nonce, err := randomHerdrToken()
	if err != nil {
		return state.HerdrIntent{}, err
	}
	environment, err := herdrrun.WorkloadEnvironment(os.Environ(), route.LauncherPath)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	envPath, envCount, err := restarted.PrepareWorkloadEnvironment(nonce, environment)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	intent := newHerdrResumeIntent(id, nonce, envPath, envCount, candidate, deadline)
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		return state.HerdrIntent{}, errors.Join(err, herdrrun.DiscardWorkloadEnvironment(route.RuntimeDir, intent.Launch))
	}
	return intent, nil
}

func newHerdrResumeIntent(
	id, nonce, envPath string,
	envCount int,
	candidate herdrRestartCandidate,
	deadline time.Time,
) state.HerdrIntent {
	saved, current := candidate.row.saved, candidate.live
	ownerRoot := ""
	if saved.Parent == ManualParentRef || strings.HasPrefix(saved.Parent, "plan:") {
		ownerRoot = candidate.row.root
	}
	ref := *saved.HerdrAgentSession
	return state.HerdrIntent{
		ID: id, Kind: state.HerdrIntentResume, Status: state.HerdrIntentRealized,
		Parent: saved.Parent, RuntimeParent: saved.RuntimeParent, OwnerProjectRoot: ownerRoot,
		IssueNum: saved.IssueNum, TaskID: saved.TaskID, WorktreePath: saved.WorktreePath,
		WorkspaceLabel: saved.HerdrWorkspaceLabel,
		Resource: state.HerdrResource{
			WorkspaceID: current.Ref.Workspace, Label: current.WorkspaceLabel,
			PaneID: current.Ref.Pane, TerminalID: current.TerminalID, CurrentPath: current.CurrentPath,
			RepoKey: current.RepoKey, RepoRoot: current.ProjectRoot,
		},
		Session: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
		ExpiresUnixMS: deadline.UnixMilli(), ResumeAgentSession: &ref,
		Launch: &state.HerdrLaunch{
			Nonce: nonce, Agent: "codex",
			Executable: saved.HerdrLaunchExecutable, Args: []string{"resume", ref.Value},
			EnvFilePath: envPath, EnvNameCount: envCount,
		},
	}
}

func startRestartedCodex(
	ctx context.Context,
	restarted HerdrRestartRuntime,
	launcher *Launcher,
	journal *state.LockedHerdrIntents,
	route herdrrun.OwnedLaunchRoute,
	intent *state.HerdrIntent,
) (backend.LivePane, backend.ProcessIdentity, error) {
	if err := issueRestartedCodex(ctx, restarted, launcher, journal, route, intent); err != nil {
		return backend.LivePane{}, backend.ProcessIdentity{}, err
	}
	process, err := waitForRestartedCodexProcess(ctx, launcher, *intent, route)
	if err != nil {
		return backend.LivePane{}, backend.ProcessIdentity{}, err
	}
	live, err := observeRestartedCodex(ctx, launcher, *intent)
	if err != nil {
		return backend.LivePane{}, backend.ProcessIdentity{}, err
	}
	if err := requireConsumedHerdrResumeEnvironment(*intent); err != nil {
		return backend.LivePane{}, backend.ProcessIdentity{}, err
	}
	return live, process, nil
}

func requireConsumedHerdrResumeEnvironment(intent state.HerdrIntent) error {
	if _, err := os.Lstat(intent.Launch.EnvFilePath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("Herdr resume environment capsule was not consumed")
}

func issueRestartedCodex(
	ctx context.Context,
	restarted HerdrRestartRuntime,
	launcher *Launcher,
	journal *state.LockedHerdrIntents,
	route herdrrun.OwnedLaunchRoute,
	intent *state.HerdrIntent,
) error {
	if err := admitHerdrResumeLauncher(ctx, launcher, journal, route, intent); err != nil {
		return err
	}
	intent.Launch.TokenIssued = true
	if err := saveHerdrLaunchPhase(journal, *intent); err != nil {
		return err
	}
	stepCtx, cancel, err := herdrLaunchStepContext(ctx, *intent)
	if err != nil {
		return err
	}
	return herdrLaunchStepResult(stepCtx, cancel, restarted.SendRestartResumeToken(
		stepCtx, intent.Resource.PaneID, intent.Launch.Nonce,
	))
}

func admitHerdrResumeLauncher(
	ctx context.Context,
	launcher *Launcher,
	journal *state.LockedHerdrIntents,
	route herdrrun.OwnedLaunchRoute,
	intent *state.HerdrIntent,
) error {
	if err := launcher.Herdr.WaitForLauncher(
		ctx, intent.Resource.PaneID, intent.Launch.Nonce, remainingHerdrLaunchTime(*intent),
	); err != nil {
		return err
	}
	if err := retryHerdrObservation(ctx, *intent, func(observeCtx context.Context) error {
		return verifyHerdrResumeLauncher(observeCtx, launcher, *intent, route)
	}); err != nil {
		return err
	}
	intent.Launch.LauncherReady = true
	return saveHerdrLaunchPhase(journal, *intent)
}

func verifyHerdrResumeLauncher(
	ctx context.Context,
	launcher *Launcher,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) error {
	process, err := launcher.Herdr.ProcessInfo(ctx, intent.Resource.PaneID)
	if err != nil {
		return err
	}
	if err := verifyHerdrLauncherProcess(process, intent, route); err != nil {
		return err
	}
	panes, err := launcher.Herdr.LivePanes(ctx)
	if err != nil {
		return err
	}
	for _, pane := range panes {
		if exactHerdrResumePlaceholder(intent, pane) {
			return nil
		}
	}
	return fmt.Errorf("exact Herdr Codex resume placeholder is not live")
}

func observeRestartedCodex(
	ctx context.Context,
	launcher *Launcher,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	return launcher.waitForHerdrPane(ctx, intent, restartedCodexPane, "")
}

func waitForRestartedCodexProcess(
	ctx context.Context,
	launcher *Launcher,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
) (backend.ProcessIdentity, error) {
	var matched backend.ProcessIdentity
	err := retryHerdrObservation(ctx, intent, func(observeCtx context.Context) error {
		process, err := launcher.Herdr.ProcessInfo(observeCtx, intent.Resource.PaneID)
		if err != nil {
			return err
		}
		matched, err = matchHerdrAgentProcess(process, intent)
		if err == nil || verifyHerdrLauncherProcess(process, intent, route) != nil {
			return err
		}
		return herdrLaunchTransitionPending{}
	})
	return matched, err
}

func restartedCodexPane(intent state.HerdrIntent, panes []backend.LivePane) (backend.LivePane, bool) {
	for _, pane := range panes {
		if exactResumedCodexPane(intent, pane) {
			return pane, true
		}
	}
	return backend.LivePane{}, false
}

func exactHerdrResumePlaceholder(intent state.HerdrIntent, pane backend.LivePane) bool {
	return exactHerdrResumeRoute(intent, pane) && !pane.AgentPresent &&
		pane.AgentID == "" && pane.AgentProvider == ""
}

func exactResumedCodexPane(intent state.HerdrIntent, pane backend.LivePane) bool {
	return exactHerdrResumeRoute(intent, pane) && pane.AgentPresent &&
		pane.AgentProvider == "codex" && pane.AgentID != ""
}

func exactHerdrResumeRoute(intent state.HerdrIntent, pane backend.LivePane) bool {
	sameAgentSession := pane.AgentSession != nil && intent.ResumeAgentSession != nil &&
		*pane.AgentSession == *intent.ResumeAgentSession
	requirements := []bool{
		pane.Ref.Backend == backend.Herdr, pane.Ref.Workspace == intent.Resource.WorkspaceID,
		pane.Ref.Pane == intent.Resource.PaneID, pane.TerminalID == intent.Resource.TerminalID,
		pane.WorkspaceLabel == intent.Resource.Label, pane.CurrentPath == intent.Resource.CurrentPath,
		pane.RepoKey == intent.Resource.RepoKey, pane.ProjectRoot == intent.Resource.RepoRoot,
		pane.SessionID == intent.Session, pane.SocketPath == intent.SocketPath,
		sameAgentSession,
	}
	return !slices.Contains(requirements, false)
}

func preserveFailedHerdrResume(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	cause error,
) error {
	intent.Status = state.HerdrIntentManualCleanupRequired
	intent.Failure = cause.Error()
	journal.UpsertIntent(intent)
	return journal.Save()
}

func persistHerdrRestartRow(
	ctx context.Context,
	locked *state.LockedStore,
	row herdrRestartRow,
	live *backend.LivePane,
	process *backend.ProcessIdentity,
	launch *state.HerdrLaunch,
) (err error) {
	if row.current {
		if err := applyHerdrRestartRow(&locked.Store, row.saved, live, process, launch); err != nil {
			return err
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
	launch *state.HerdrLaunch,
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
		return nil
	}
	pane.HerdrTerminalID = live.TerminalID
	pane.HerdrAgentID = live.AgentID
	pane.HerdrAgentSession = cloneAgentSession(live.AgentSession)
	identity := *process
	pane.HerdrProcessIdentity = &identity
	pane.HerdrLaunchExecutable = launch.Executable
	pane.HerdrLaunchArgs = slices.Clone(launch.Args)
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
		current.HerdrWorkspaceID == saved.HerdrWorkspaceID,
		current.HerdrWorkspaceLabel == saved.HerdrWorkspaceLabel,
		current.HerdrTerminalID == saved.HerdrTerminalID, current.HerdrSession == saved.HerdrSession,
		current.HerdrSocketPath == saved.HerdrSocketPath, current.HerdrAgentID == saved.HerdrAgentID,
		current.HerdrRepoKey == saved.HerdrRepoKey, current.HerdrRepoRoot == saved.HerdrRepoRoot,
		reflect.DeepEqual(current.HerdrAgentSession, saved.HerdrAgentSession),
		reflect.DeepEqual(current.HerdrProcessIdentity, saved.HerdrProcessIdentity),
		current.HerdrLaunchExecutable == saved.HerdrLaunchExecutable,
		slices.Equal(current.HerdrLaunchArgs, saved.HerdrLaunchArgs),
		current.HerdrDirectAgentLaunch == saved.HerdrDirectAgentLaunch,
		current.Agent == saved.Agent, current.PlanMode == saved.PlanMode,
		current.WorktreePath == saved.WorktreePath,
	}
	return !slices.Contains(requirements, false)
}

func cloneAgentSession(ref *backend.AgentSessionRef) *backend.AgentSessionRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}
