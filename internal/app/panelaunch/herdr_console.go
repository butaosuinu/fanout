package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const HerdrConsoleRuntimeParent = "@console"

// HerdrConsoleResult is the durable console pane plus the shell command a
// plain terminal can run to attach the isolated owned Herdr client.
type HerdrConsoleResult struct {
	Pane          state.Pane
	AttachCommand string
}

// EnsureHerdrConsole creates or adopts the one repo-root console workspace for
// an owned session. It owns the combined state/intent lock through finalization.
func EnsureHerdrConsole(
	ctx context.Context,
	projectRoot string,
	owned *herdrrun.OwnedSession,
	callerEnvironment []string,
	shell string,
) (result HerdrConsoleResult, retErr error) {
	if ctx == nil || owned == nil {
		return result, fmt.Errorf("ensure Herdr console requires context and owned session")
	}
	root, shellPath, err := resolveHerdrConsoleInputs(projectRoot, shell)
	if err != nil {
		return result, err
	}
	err = worktree.EnsureLocalExclude(root)
	if err != nil {
		return result, fmt.Errorf("prepare local git exclude: %w", err)
	}
	locked, err := state.LockProjectForLaunch(root)
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, locked.Unlock()) }()
	return ensureHerdrConsoleLocked(ctx, root, shellPath, owned, callerEnvironment, locked)
}

//nolint:funlen // Keep the lock-held reuse-or-realize console transaction visible as one ordered state machine.
func ensureHerdrConsoleLocked(
	ctx context.Context,
	root, shellPath string,
	owned *herdrrun.OwnedSession,
	callerEnvironment []string,
	locked *state.LockedStore,
) (HerdrConsoleResult, error) {
	route, err := verifyHerdrConsoleRoute(ctx, owned)
	if err != nil {
		return HerdrConsoleResult{}, err
	}
	pane, found, err := findHerdrConsolePane(root, locked.Store)
	if err != nil {
		return HerdrConsoleResult{}, err
	}
	if found {
		result, reused, reuseErr := reuseHerdrConsole(ctx, locked, root, owned, pane)
		if reuseErr != nil || reused {
			return result, reuseErr
		}
	}
	intent, err := realizeHerdrInteractive(
		ctx, owned, locked, route,
		HerdrCoordinatorRequest{
			Parent:      HerdrConsoleRuntimeParent,
			ProjectRoot: root, SourceRoot: root, CWD: root,
			HerdrSession: route.Session, SocketPath: route.SocketPath,
		},
		func(state.HerdrIntent) (*state.HerdrLaunch, error) {
			return newHerdrShellLaunch(owned, route, shellPath, callerEnvironment)
		},
	)
	if err != nil {
		return HerdrConsoleResult{}, err
	}
	if validationErr := validateHerdrConsoleIntentRoot(intent, root); validationErr != nil {
		return HerdrConsoleResult{}, validationErr
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: root}, Herdr: owned}
	live, err := launcher.startHerdrAgent(ctx, locked, route, intent, validateHerdrShellLaunch, nil, exactHerdrShellPane, nil)
	if err != nil {
		return HerdrConsoleResult{}, err
	}
	pane = herdrShellStatePane(
		intent, live, NextSyntheticPaneNumber(locked.Store, ManualParentRef),
		"herdr-console", "Herdr console", HerdrConsoleRuntimeParent,
	)
	if err := finalizeHerdrPane(locked, root, intent, staticHerdrPane(pane)); err != nil {
		return HerdrConsoleResult{}, err
	}
	return herdrConsoleResult(owned, pane)
}

func validateHerdrConsoleIntentRoot(intent state.HerdrIntent, projectRoot string) error {
	if filepath.Clean(intent.WorktreePath) != filepath.Clean(projectRoot) {
		return fmt.Errorf("saved Herdr console intent belongs to another worktree")
	}
	return nil
}

func reuseHerdrConsole(
	ctx context.Context,
	locked *state.LockedStore,
	projectRoot string,
	owned *herdrrun.OwnedSession,
	pane state.Pane,
) (HerdrConsoleResult, bool, error) {
	if err := verifySavedHerdrConsole(owned, pane); err != nil {
		if !staleHerdrConsoleRecoverable(err) {
			return HerdrConsoleResult{}, false, err
		}
		if staleErr := removeStaleHerdrConsole(ctx, locked, projectRoot, owned, pane); staleErr != nil {
			return HerdrConsoleResult{}, false, errors.Join(
				fmt.Errorf("saved Herdr console is not safely reusable: %w", err),
				staleErr,
			)
		}
		return HerdrConsoleResult{}, false, nil
	}
	if err := removeCompletedHerdrConsoleIntent(locked, projectRoot, pane); err != nil {
		return HerdrConsoleResult{}, false, err
	}
	result, err := herdrConsoleResult(owned, pane)
	return result, true, err
}

func staleHerdrConsoleRecoverable(err error) bool {
	return errors.Is(err, herdrrun.ErrOwnedIdentityMismatch)
}

func removeStaleHerdrConsole(
	ctx context.Context,
	locked *state.LockedStore,
	projectRoot string,
	owned *herdrrun.OwnedSession,
	pane state.Pane,
) error {
	if err := validateSavedHerdrConsoleShape(pane); err != nil {
		return err
	}
	workspaces, err := owned.ObserveWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("observe saved Herdr console workspace: %w", err)
	}
	present, err := savedHerdrConsoleWorkspacePresent(pane, workspaces)
	if err != nil {
		return err
	}
	if !present {
		return removeStaleHerdrConsoleState(locked, projectRoot, pane)
	}
	live, err := owned.LivePanes(ctx)
	if err != nil {
		return fmt.Errorf("recheck saved Herdr console panes: %w", err)
	}
	current, err := staleHerdrConsoleTarget(pane, live)
	if err != nil {
		return err
	}
	if err := closeStaleHerdrConsole(owned, current); err != nil {
		return err
	}
	return removeStaleHerdrConsoleState(locked, projectRoot, pane)
}

func removeStaleHerdrConsoleState(
	locked *state.LockedStore,
	projectRoot string,
	pane state.Pane,
) error {
	if err := removeCompletedHerdrConsoleIntent(locked, projectRoot, pane); err != nil {
		return err
	}
	return removeSavedHerdrConsoleRow(locked, projectRoot, pane)
}

func savedHerdrConsoleWorkspacePresent(
	saved state.Pane,
	workspaces []herdrrun.WorkspaceObservation,
) (bool, error) {
	for _, workspace := range workspaces {
		if workspace.WorkspaceID != saved.HerdrWorkspaceID {
			continue
		}
		if workspace.Label != saved.HerdrWorkspaceLabel || workspace.Path != "" ||
			workspace.RepoKey != "" || workspace.RepoRoot != "" {
			return false, fmt.Errorf("saved Herdr console workspace identity changed")
		}
		return true, nil
	}
	return false, nil
}

func staleHerdrConsoleTarget(
	saved state.Pane,
	live []backend.LivePane,
) (backend.LivePane, error) {
	var matches []backend.LivePane
	for _, current := range live {
		if current.Ref.Workspace != saved.HerdrWorkspaceID {
			continue
		}
		identity := []bool{
			current.Ref.Backend == backend.Herdr,
			current.WorkspaceLabel == saved.HerdrWorkspaceLabel,
			current.SessionID == saved.HerdrSession,
			filepath.Clean(current.SocketPath) == filepath.Clean(saved.HerdrSocketPath),
			filepath.Clean(current.CurrentPath) == filepath.Clean(saved.WorktreePath),
			current.Ref.Pane != "", current.TerminalID != "", !current.AgentPresent,
		}
		if !slices.Contains(identity, false) {
			matches = append(matches, current)
		}
	}
	if len(matches) != 1 {
		return backend.LivePane{}, fmt.Errorf(
			"%w: saved Herdr console workspace has %d closeable pane matches",
			ErrHerdrManualCleanupRequired, len(matches),
		)
	}
	return matches[0], nil
}

func closeStaleHerdrConsole(owned *herdrrun.OwnedSession, current backend.LivePane) error {
	identity := herdrrun.OwnedPaneIdentity{
		Ref: current.Ref, SessionID: current.SessionID, SocketPath: current.SocketPath,
		WorkspaceLabel: current.WorkspaceLabel, TerminalID: current.TerminalID,
		CurrentPath: current.CurrentPath,
	}
	bound, err := owned.Backend().BindOwnedWorkspaceClose(identity)
	if err != nil {
		return fmt.Errorf("bind stale Herdr console for close: %w", err)
	}
	_, err = bound.CloseOwned(backend.CloseRequest{Ref: backend.PaneRef{
		Backend: backend.Herdr, Pane: current.Ref.Pane,
	}})
	if err != nil {
		return fmt.Errorf("close stale Herdr console workspace: %w", err)
	}
	return nil
}

func removeSavedHerdrConsoleRow(
	locked *state.LockedStore,
	projectRoot string,
	pane state.Pane,
) (retErr error) {
	ownerRoot := filepath.Clean(pane.SourceProjectRoot)
	if ownerRoot == filepath.Clean(projectRoot) {
		return locked.RemovePane(pane.Parent, pane.IssueNum)
	}
	owner, err := state.LockProject(ownerRoot)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, owner.Unlock()) }()
	saved, found := owner.Find(pane.Parent, pane.IssueNum)
	if !found || !sameSavedHerdrConsole(pane, saved) {
		return fmt.Errorf("saved Herdr console row changed before cleanup")
	}
	return owner.RemovePane(pane.Parent, pane.IssueNum)
}

func sameSavedHerdrConsole(expected, actual state.Pane) bool {
	requirements := []bool{
		actual.RuntimeParent == expected.RuntimeParent,
		actual.Kind == expected.Kind,
		backend.NormalizeName(actual.Backend) == backend.Herdr,
		actual.PaneID == expected.PaneID,
		actual.HerdrWorkspaceID == expected.HerdrWorkspaceID,
		actual.HerdrWorkspaceLabel == expected.HerdrWorkspaceLabel,
		actual.HerdrTerminalID == expected.HerdrTerminalID,
		actual.HerdrSession == expected.HerdrSession,
		filepath.Clean(actual.HerdrSocketPath) == filepath.Clean(expected.HerdrSocketPath),
		filepath.Clean(actual.WorktreePath) == filepath.Clean(expected.WorktreePath),
	}
	return !slices.Contains(requirements, false)
}

func resolveHerdrConsoleInputs(projectRoot, shell string) (string, string, error) {
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize Herdr console root: %w", err)
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return "", "", fmt.Errorf("herdr console root must be absolute")
	}
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = strings.TrimSpace(os.Getenv("SHELL"))
	}
	if !filepath.IsAbs(shell) {
		return "", "", fmt.Errorf("herdr console shell must be an absolute path")
	}
	shell, err = filepath.EvalSymlinks(shell)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize Herdr console shell: %w", err)
	}
	info, err := os.Stat(shell)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", fmt.Errorf("herdr console shell is not executable: %s", shell)
	}
	return root, filepath.Clean(shell), nil
}

func verifyHerdrConsoleRoute(
	ctx context.Context,
	owned HerdrLaunchRuntime,
) (herdrrun.OwnedLaunchRoute, error) {
	if err := owned.VerifyOwned(ctx); err != nil {
		return herdrrun.OwnedLaunchRoute{}, err
	}
	route, err := owned.LaunchRoute()
	if err != nil {
		return herdrrun.OwnedLaunchRoute{}, err
	}
	if err := validateHerdrLaunchRoute(route); err != nil {
		return herdrrun.OwnedLaunchRoute{}, err
	}
	return route, nil
}

func validateHerdrLaunchRoute(route herdrrun.OwnedLaunchRoute) error {
	if route.LauncherPath == "" || route.EmitterPath == "" {
		return fmt.Errorf("owned Herdr launch route is incomplete")
	}
	if route.LauncherPath != route.EmitterPath {
		return fmt.Errorf(
			"owned Herdr launcher predates the current fanout; restart is required before launching panes",
		)
	}
	return nil
}

func newHerdrShellLaunch(
	owned HerdrLaunchRuntime,
	route herdrrun.OwnedLaunchRoute,
	shell string,
	callerEnvironment []string,
) (*state.HerdrLaunch, error) {
	nonce, err := randomHerdrToken()
	if err != nil {
		return nil, err
	}
	environment, err := herdrrun.WorkloadEnvironment(callerEnvironment, route.LauncherPath)
	if err != nil {
		return nil, err
	}
	envPath, envCount, err := owned.PrepareWorkloadEnvironment(nonce, environment)
	if err != nil {
		return nil, err
	}
	return &state.HerdrLaunch{
		Nonce: nonce, Executable: shell, EnvFilePath: envPath, EnvNameCount: envCount,
	}, nil
}

func findHerdrConsolePane(projectRoot string, current state.Store) (state.Pane, bool, error) {
	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return state.Pane{}, false, fmt.Errorf("list worktrees for Herdr console: %w", err)
	}
	var found state.Pane
	hasFound := false
	for _, root := range roots {
		store, err := loadHerdrConsoleStore(root, projectRoot, current)
		if err != nil {
			return state.Pane{}, false, err
		}
		pane, ok, err := herdrConsolePaneInStore(root, store)
		if err != nil || !ok {
			if err != nil {
				return state.Pane{}, false, err
			}
			continue
		}
		if hasFound {
			return state.Pane{}, false, fmt.Errorf("multiple saved Herdr console panes")
		}
		found = pane
		hasFound = true
	}
	return found, hasFound, nil
}

func loadHerdrConsoleStore(root, currentRoot string, current state.Store) (state.Store, error) {
	if root == currentRoot {
		return current, nil
	}
	store, err := state.LoadProject(root)
	if err != nil {
		return state.Store{}, fmt.Errorf("load Herdr console state from %s: %w", root, err)
	}
	return store, nil
}

func herdrConsolePaneInStore(root string, store state.Store) (state.Pane, bool, error) {
	var found state.Pane
	hasFound := false
	for _, pane := range store.Panes {
		if pane.RuntimeParent != HerdrConsoleRuntimeParent {
			continue
		}
		if hasFound {
			return state.Pane{}, false, fmt.Errorf("multiple saved Herdr console panes")
		}
		pane.SourceProjectRoot = root
		found = pane
		hasFound = true
	}
	return found, hasFound, nil
}

func verifySavedHerdrConsole(
	owned *herdrrun.OwnedSession,
	pane state.Pane,
) error {
	if err := validateSavedHerdrConsoleShape(pane); err != nil {
		return err
	}
	identity := herdrrun.OwnedPaneIdentity{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: pane.HerdrWorkspaceID, Pane: pane.PaneID,
		},
		SessionID: pane.HerdrSession, SocketPath: pane.HerdrSocketPath,
		WorkspaceLabel: pane.HerdrWorkspaceLabel, TerminalID: pane.HerdrTerminalID,
		CurrentPath: pane.WorktreePath,
	}
	_, err := owned.Backend().BindOwnedTarget(identity)
	return err
}

func validateSavedHerdrConsoleShape(pane state.Pane) error {
	projectRoot := strings.TrimSpace(pane.SourceProjectRoot)
	requirements := []bool{
		pane.Parent == ManualParentRef,
		pane.Kind == state.PaneKindShell,
		pane.RuntimeParent == HerdrConsoleRuntimeParent,
		backend.NormalizeName(pane.Backend) == backend.Herdr,
		pane.Agent == state.PaneKindShell,
		projectRoot != "",
		filepath.Clean(pane.WorktreePath) == filepath.Clean(projectRoot),
		pane.PaneID != "",
		pane.HerdrWorkspaceID != "",
		pane.HerdrWorkspaceLabel != "",
		pane.HerdrTerminalID != "",
		pane.HerdrSession != "",
		pane.HerdrSocketPath != "",
		pane.HerdrRepoKey == "",
		pane.HerdrAgentID == "",
		pane.HerdrAgentSession == nil,
	}
	if slices.Contains(requirements, false) {
		return fmt.Errorf("saved Herdr console role is invalid")
	}
	return nil
}

func removeCompletedHerdrConsoleIntent(
	locked *state.LockedStore,
	projectRoot string,
	pane state.Pane,
) error {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	intentID, err := state.HerdrCoordinatorIntentID(HerdrConsoleRuntimeParent, "", 0)
	if err != nil {
		return err
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		return nil
	}
	if !completedHerdrConsoleIntentMatchesPane(intent, pane) {
		return fmt.Errorf("completed Herdr console intent does not match saved pane")
	}
	journal.RemoveIntent(intentID)
	return journal.Save()
}

func completedHerdrConsoleIntentMatchesPane(intent state.HerdrIntent, pane state.Pane) bool {
	if intent.Kind != state.HerdrIntentCoordinator || intent.Status != state.HerdrIntentRealized ||
		intent.Parent != HerdrConsoleRuntimeParent || intent.RuntimeParent != HerdrConsoleRuntimeParent ||
		filepath.Clean(intent.WorktreePath) != filepath.Clean(pane.WorktreePath) ||
		intent.WorkspaceLabel != pane.HerdrWorkspaceLabel {
		return false
	}
	route := herdrrun.OwnedLaunchRoute{Session: intent.Session, SocketPath: intent.SocketPath}
	return validateHerdrCoordinatorPane(pane, intent, route) == nil
}

func herdrConsoleResult(
	owned *herdrrun.OwnedSession,
	pane state.Pane,
) (HerdrConsoleResult, error) {
	command, err := owned.AttachCommand()
	if err != nil {
		return HerdrConsoleResult{}, err
	}
	return HerdrConsoleResult{Pane: pane, AttachCommand: command}, nil
}
