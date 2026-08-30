package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/agentprocess"
	"github.com/butaosuinu/fanout/internal/core/backend"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const ManagedConsoleRuntimeParent = "@console"

// ManagedConsoleWorkloadArg is the reserved argv token the console workload is
// started with. The pane launcher and the console workload are the same pinned
// binary, so without it launch verification could not tell an exec'd console
// TUI apart from the launcher still waiting for its token — the launcher's
// identity is exactly that binary with an empty argv tail.
const ManagedConsoleWorkloadArg = "__managed-console"

// ManagedSessionRuntime is the whole owned-session surface the composition root
// holds: the launch port plus the session-wide operations no single launch
// needs — the two attach forms a plain terminal enters through, the saved-row
// identity recheck, and the bound backend a workspace is retired through.
type ManagedSessionRuntime interface {
	ManagedLaunchRuntime
	AttachForms(baseEnvironment []string) (string, backend.AttachExec, error)
	VerifyOwnedTarget(backend.OwnedPaneIdentity) error
	BindOwnedWorkspaceClose(backend.OwnedPaneIdentity) (backend.OwnedClosingBackend, error)
}

// ManagedConsoleResult is the durable console pane plus both attach forms: the
// exec image a terminal enters the isolated owned Herdr client through, and
// the equivalent shell command for a caller that can only print it.
type ManagedConsoleResult struct {
	Pane          state.Pane
	AttachCommand string
	Attach        backend.AttachExec
}

// EnsureManagedConsole creates or adopts the one repo-root console workspace for
// an owned session. It owns the combined state/intent lock through finalization.
func EnsureManagedConsole(
	ctx context.Context,
	projectRoot string,
	owned ManagedSessionRuntime,
	callerEnvironment []string,
	shell string,
) (result ManagedConsoleResult, retErr error) {
	if ctx == nil || owned == nil {
		return result, fmt.Errorf("ensure Herdr console requires context and owned session")
	}
	root, shellPath, err := resolveManagedConsoleInputs(projectRoot, shell)
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
	return ensureManagedConsoleLocked(ctx, root, shellPath, owned, callerEnvironment, locked)
}

//nolint:funlen // Keep the lock-held reuse-or-realize console transaction visible as one ordered state machine.
func ensureManagedConsoleLocked(
	ctx context.Context,
	root, shellPath string,
	owned ManagedSessionRuntime,
	callerEnvironment []string,
	locked *state.LockedStore,
) (ManagedConsoleResult, error) {
	route, err := verifyManagedConsoleRoute(ctx, owned)
	if err != nil {
		return ManagedConsoleResult{}, err
	}
	pane, found, err := findManagedConsolePane(root, locked.Store)
	if err != nil {
		return ManagedConsoleResult{}, err
	}
	if found {
		result, reused, reuseErr := reuseManagedConsole(ctx, locked, root, owned, pane, callerEnvironment)
		if reuseErr != nil || reused {
			return result, reuseErr
		}
	}
	intent, err := realizeManagedInteractive(
		ctx, owned, locked, route,
		ManagedCoordinatorRequest{
			Parent:      ManagedConsoleRuntimeParent,
			ProjectRoot: root, SourceRoot: root, CWD: root,
			ManagedSession: route.Session, SocketPath: route.SocketPath,
		},
		func(state.LaunchIntent) (*state.LaunchCapsule, error) {
			return newManagedConsoleLaunch(owned, route, shellPath, callerEnvironment)
		},
	)
	if err != nil {
		return ManagedConsoleResult{}, err
	}
	if validationErr := validateManagedConsoleIntentRoot(intent, root); validationErr != nil {
		return ManagedConsoleResult{}, validationErr
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: root}, Managed: owned}
	live, err := launcher.startManagedAgent(
		ctx, locked, route, intent, validateManagedConsoleLaunch(route), nil, exactManagedShellPane,
		func(adoptCtx context.Context, _ *state.LockedStore, issued state.LaunchIntent) (backend.LivePane, error) {
			return launcher.adoptManagedConsolePane(adoptCtx, issued, route, shellPath)
		},
	)
	if err != nil {
		return ManagedConsoleResult{}, err
	}
	pane = managedShellStatePane(
		intent, live, NextSyntheticPaneNumber(locked.Store, ManualParentRef),
		"herdr-console", "Herdr console", ManagedConsoleRuntimeParent,
	)
	if err := finalizeManagedPane(locked, root, intent, staticManagedPane(pane)); err != nil {
		return ManagedConsoleResult{}, err
	}
	return managedConsoleResult(owned, pane, callerEnvironment)
}

// validateManagedConsoleLaunch admits only a capsule that runs the pinned
// console TUI. prepareManagedLaunch re-validates the capsule a journal intent
// saved before issuing its token, and an intent left by an interrupted or
// pre-upgrade bootstrap can still name the operator shell as the workload —
// issuing that token would either confirm a bare shell as the console or
// strand the intent on an identity mismatch, so the stale capsule is refused
// while the token does not exist yet (shutdown recovery prunes the intent).
func validateManagedConsoleLaunch(route backend.OwnedLaunchRoute) managedLaunchValidator {
	return func(launch *state.LaunchCapsule) error {
		if err := validateManagedShellLaunch(launch); err != nil {
			return err
		}
		if launch.Executable != route.LauncherPath ||
			!slices.Equal(launch.Args, []string{ManagedConsoleWorkloadArg}) {
			return fmt.Errorf("saved Herdr console launch does not run the pinned console workload")
		}
		return nil
	}
}

func validateManagedConsoleIntentRoot(intent state.LaunchIntent, projectRoot string) error {
	if filepath.Clean(intent.WorktreePath) != filepath.Clean(projectRoot) {
		return fmt.Errorf("saved Herdr console intent belongs to another worktree")
	}
	return nil
}

func reuseManagedConsole(
	ctx context.Context,
	locked *state.LockedStore,
	projectRoot string,
	owned ManagedSessionRuntime,
	pane state.Pane,
	callerEnvironment []string,
) (ManagedConsoleResult, bool, error) {
	if err := verifySavedManagedConsole(owned, pane); err != nil {
		if !staleManagedConsoleRecoverable(err) {
			return ManagedConsoleResult{}, false, err
		}
		if staleErr := removeStaleManagedConsole(ctx, locked, projectRoot, owned, pane); staleErr != nil {
			return ManagedConsoleResult{}, false, errors.Join(
				fmt.Errorf("saved Herdr console is not safely reusable: %w", err),
				staleErr,
			)
		}
		return ManagedConsoleResult{}, false, nil
	}
	if err := removeCompletedManagedConsoleIntent(locked, projectRoot, pane); err != nil {
		return ManagedConsoleResult{}, false, err
	}
	result, err := managedConsoleResult(owned, pane, callerEnvironment)
	return result, true, err
}

func staleManagedConsoleRecoverable(err error) bool {
	return errors.Is(err, backend.ErrOwnedIdentityMismatch)
}

func removeStaleManagedConsole(
	ctx context.Context,
	locked *state.LockedStore,
	projectRoot string,
	owned ManagedSessionRuntime,
	pane state.Pane,
) error {
	if err := validateSavedManagedConsoleShape(pane); err != nil {
		return err
	}
	workspaces, err := owned.ObserveWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("observe saved Herdr console workspace: %w", err)
	}
	present, err := savedManagedConsoleWorkspacePresent(pane, workspaces)
	if err != nil {
		return err
	}
	if !present {
		return removeStaleManagedConsoleState(locked, projectRoot, pane)
	}
	live, err := owned.LivePanes(ctx)
	if err != nil {
		return fmt.Errorf("recheck saved Herdr console panes: %w", err)
	}
	current, err := staleManagedConsoleTarget(pane, live)
	if err != nil {
		return err
	}
	if err := closeStaleManagedConsole(owned, current); err != nil {
		return err
	}
	return removeStaleManagedConsoleState(locked, projectRoot, pane)
}

func removeStaleManagedConsoleState(
	locked *state.LockedStore,
	projectRoot string,
	pane state.Pane,
) error {
	if err := removeCompletedManagedConsoleIntent(locked, projectRoot, pane); err != nil {
		return err
	}
	return removeSavedManagedConsoleRow(locked, projectRoot, pane)
}

func savedManagedConsoleWorkspacePresent(
	saved state.Pane,
	workspaces []backend.WorkspaceObservation,
) (bool, error) {
	for _, workspace := range workspaces {
		if workspace.WorkspaceID != saved.WorkspaceID {
			continue
		}
		if workspace.Label != saved.WorkspaceLabel || workspace.Path != "" ||
			workspace.RepoKey != "" || workspace.RepoRoot != "" {
			return false, fmt.Errorf("saved Herdr console workspace identity changed")
		}
		return true, nil
	}
	return false, nil
}

func staleManagedConsoleTarget(
	saved state.Pane,
	live []backend.LivePane,
) (backend.LivePane, error) {
	var matches []backend.LivePane
	for _, current := range live {
		if current.Ref.Workspace != saved.WorkspaceID {
			continue
		}
		identity := []bool{
			current.Ref.Backend == backend.Herdr,
			current.WorkspaceLabel == saved.WorkspaceLabel,
			current.SessionID == saved.SessionID,
			filepath.Clean(current.SocketPath) == filepath.Clean(saved.SocketPath),
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
			ErrManualCleanupRequired, len(matches),
		)
	}
	return matches[0], nil
}

func closeStaleManagedConsole(owned ManagedSessionRuntime, current backend.LivePane) error {
	identity := backend.OwnedPaneIdentity{
		Ref: current.Ref, SessionID: current.SessionID, SocketPath: current.SocketPath,
		WorkspaceLabel: current.WorkspaceLabel, TerminalID: current.TerminalID,
		CurrentPath: current.CurrentPath,
	}
	bound, err := owned.BindOwnedWorkspaceClose(identity)
	if err != nil {
		return fmt.Errorf("bind stale Herdr console for close: %w", err)
	}
	_, err = bound.CloseOwned(backend.CloseRequest{Ref: backend.PaneRef{
		Backend: backend.Herdr, Pane: current.Ref.Pane,
	}})
	if err != nil {
		return fmt.Errorf("close stale Herdr console workspace: %w", managedWorkspaceCloseError(err))
	}
	return nil
}

func managedWorkspaceCloseError(err error) error {
	if errors.Is(err, backend.ErrOwnedWorkspaceHasUnadmittedPane) {
		return fmt.Errorf("%w: %w", ErrManualCleanupRequired, err)
	}
	return err
}

func removeSavedManagedConsoleRow(
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
	if !found || !sameSavedManagedConsole(pane, saved) {
		return fmt.Errorf("saved Herdr console row changed before cleanup")
	}
	return owner.RemovePane(pane.Parent, pane.IssueNum)
}

func sameSavedManagedConsole(expected, actual state.Pane) bool {
	requirements := []bool{
		actual.RuntimeParent == expected.RuntimeParent,
		actual.Kind == expected.Kind,
		backend.NormalizeName(actual.Backend) == backend.Herdr,
		actual.PaneID == expected.PaneID,
		actual.WorkspaceID == expected.WorkspaceID,
		actual.WorkspaceLabel == expected.WorkspaceLabel,
		actual.TerminalID == expected.TerminalID,
		actual.SessionID == expected.SessionID,
		filepath.Clean(actual.SocketPath) == filepath.Clean(expected.SocketPath),
		filepath.Clean(actual.WorktreePath) == filepath.Clean(expected.WorktreePath),
	}
	return !slices.Contains(requirements, false)
}

func resolveManagedConsoleInputs(projectRoot, shell string) (string, string, error) {
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
	if err := RunnableConsoleShell(shell); err != nil {
		return "", "", err
	}
	return root, filepath.Clean(shell), nil
}

// RunnableConsoleShell is the one check both ends of the ConsoleShellEnv
// contract apply: the bootstrap before recording the shell, and the console
// TUI before exec'ing it on exit — the value crosses a process boundary, so
// the consumer re-checks rather than trusting the record.
func RunnableConsoleShell(shell string) error {
	if !filepath.IsAbs(shell) {
		return fmt.Errorf("herdr console shell must be an absolute path")
	}
	info, err := os.Stat(shell)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("herdr console shell is not executable: %s", shell)
	}
	return nil
}

func verifyManagedConsoleRoute(
	ctx context.Context,
	owned ManagedLaunchRuntime,
) (backend.OwnedLaunchRoute, error) {
	if err := owned.VerifyOwned(ctx); err != nil {
		return backend.OwnedLaunchRoute{}, err
	}
	route, err := owned.LaunchRoute()
	if err != nil {
		return backend.OwnedLaunchRoute{}, err
	}
	if err := validateManagedLaunchRoute(route); err != nil {
		return backend.OwnedLaunchRoute{}, err
	}
	return route, nil
}

func validateManagedLaunchRoute(route backend.OwnedLaunchRoute) error {
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

func newManagedShellLaunch(
	owned ManagedLaunchRuntime,
	route backend.OwnedLaunchRoute,
	shell string,
	callerEnvironment []string,
) (*state.LaunchCapsule, error) {
	return newManagedWorkloadLaunch(owned, route, shell, callerEnvironment)
}

// newManagedWorkloadLaunch is the one capsule builder both interactive lanes
// share: nonce, filtered workload environment, one-shot capsule, executable.
// extraEnvironment entries are appended after the filtered environment, so a
// lane can record contract values (ConsoleShellEnv) the caller filter blocks.
func newManagedWorkloadLaunch(
	owned ManagedLaunchRuntime,
	route backend.OwnedLaunchRoute,
	executable string,
	callerEnvironment []string,
	extraEnvironment ...string,
) (*state.LaunchCapsule, error) {
	nonce, err := randomManagedToken()
	if err != nil {
		return nil, err
	}
	environment, err := owned.WorkloadEnvironment(callerEnvironment, route.LauncherPath)
	if err != nil {
		return nil, err
	}
	environment = append(environment, extraEnvironment...)
	envPath, envCount, err := owned.PrepareWorkloadEnvironment(nonce, environment)
	if err != nil {
		return nil, err
	}
	return &state.LaunchCapsule{
		Nonce: nonce, Executable: executable, EnvFilePath: envPath, EnvNameCount: envCount,
	}, nil
}

// newManagedConsoleLaunch records the pinned fanout binary as the console
// workload, so the attach lands on a running console TUI instead of a bare
// shell. The operator shell still resolves and rides along as ConsoleShellEnv:
// the TUI execs it on exit so the pane outlives a quit. The manual shell lane
// (newManualManagedShellLaunch) keeps launching the shell itself.
func newManagedConsoleLaunch(
	owned ManagedLaunchRuntime,
	route backend.OwnedLaunchRoute,
	shell string,
	callerEnvironment []string,
) (*state.LaunchCapsule, error) {
	capsule, err := newManagedWorkloadLaunch(
		owned, route, route.LauncherPath, callerEnvironment,
		backend.ConsoleShellEnv+"="+shell,
	)
	if err != nil {
		return nil, err
	}
	capsule.Args = []string{ManagedConsoleWorkloadArg}
	return capsule, nil
}

// adoptManagedConsolePane is the console lane's process gate. Unlike the
// generic workload wait it also adopts the operator shell the TUI execs on
// exit: a console TUI that fails during init still leaves a live console pane
// holding that shell, and treating it as a failed launch would pin the intent
// on manual cleanup even though the pane is healthy and the next `fanout`
// inside it reopens the console.
func (l *Launcher) adoptManagedConsolePane(
	ctx context.Context,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
	shell string,
) (backend.LivePane, error) {
	err := retryManagedObservation(ctx, intent, func(observeCtx context.Context) error {
		process, err := l.Managed.ProcessInfo(observeCtx, intent.Resource.PaneID)
		if err != nil {
			return err
		}
		return classifyManagedConsoleProcess(process, intent, route, shell)
	})
	if err != nil {
		return backend.LivePane{}, err
	}
	return l.waitForManagedPane(ctx, intent, exactManagedShellPane, "")
}

// classifyManagedConsoleProcess reports what the console pane runs: nil for a
// started state (the exec'd console TUI, or the operator shell it hands the
// pane to on exit), pending while the launcher still waits for its token, and
// the identity mismatch otherwise.
func classifyManagedConsoleProcess(
	process backend.PaneProcessInfo,
	intent state.LaunchIntent,
	route backend.OwnedLaunchRoute,
	shell string,
) error {
	processErr := verifyManagedAgentProcess(process, intent)
	if processErr == nil {
		return nil
	}
	if _, err := agentprocess.MatchAgent(process, agentprocess.Identity{
		WorktreePath: intent.WorktreePath, Executable: shell,
	}); err == nil {
		return nil
	}
	if verifyManagedLauncherProcess(process, intent, route) == nil {
		return managedLaunchTransitionPending{}
	}
	return processErr
}

func findManagedConsolePane(projectRoot string, current state.Store) (state.Pane, bool, error) {
	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return state.Pane{}, false, fmt.Errorf("list worktrees for Herdr console: %w", err)
	}
	var found state.Pane
	hasFound := false
	for _, root := range roots {
		store, err := loadManagedConsoleStore(root, projectRoot, current)
		if err != nil {
			return state.Pane{}, false, err
		}
		pane, ok, err := managedConsolePaneInStore(root, store)
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

func loadManagedConsoleStore(root, currentRoot string, current state.Store) (state.Store, error) {
	if root == currentRoot {
		return current, nil
	}
	store, err := state.LoadProject(root)
	if err != nil {
		return state.Store{}, fmt.Errorf("load Herdr console state from %s: %w", root, err)
	}
	return store, nil
}

func managedConsolePaneInStore(root string, store state.Store) (state.Pane, bool, error) {
	var found state.Pane
	hasFound := false
	for _, pane := range store.Panes {
		if pane.RuntimeParent != ManagedConsoleRuntimeParent {
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

func verifySavedManagedConsole(
	owned ManagedSessionRuntime,
	pane state.Pane,
) error {
	if err := validateSavedManagedConsoleShape(pane); err != nil {
		return err
	}
	identity := backend.OwnedPaneIdentity{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: pane.WorkspaceID, Pane: pane.PaneID,
		},
		SessionID: pane.SessionID, SocketPath: pane.SocketPath,
		WorkspaceLabel: pane.WorkspaceLabel, TerminalID: pane.TerminalID,
		CurrentPath: pane.WorktreePath,
	}
	return owned.VerifyOwnedTarget(identity)
}

func validateSavedManagedConsoleShape(pane state.Pane) error {
	projectRoot := strings.TrimSpace(pane.SourceProjectRoot)
	requirements := []bool{
		pane.Parent == ManualParentRef,
		pane.Kind == state.PaneKindShell,
		pane.RuntimeParent == ManagedConsoleRuntimeParent,
		backend.NormalizeName(pane.Backend) == backend.Herdr,
		pane.Agent == state.PaneKindShell,
		projectRoot != "",
		filepath.Clean(pane.WorktreePath) == filepath.Clean(projectRoot),
		pane.PaneID != "",
		pane.WorkspaceID != "",
		pane.WorkspaceLabel != "",
		pane.TerminalID != "",
		pane.SessionID != "",
		pane.SocketPath != "",
		pane.RepoKey == "",
		pane.AgentID == "",
		pane.AgentSession == nil,
	}
	if slices.Contains(requirements, false) {
		return fmt.Errorf("saved Herdr console role is invalid")
	}
	return nil
}

func removeCompletedManagedConsoleIntent(
	locked *state.LockedStore,
	projectRoot string,
	pane state.Pane,
) error {
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return err
	}
	intentID, err := state.CoordinatorIntentID(ManagedConsoleRuntimeParent, "", 0)
	if err != nil {
		return err
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		return nil
	}
	if !completedManagedConsoleIntentMatchesPane(intent, pane) {
		return fmt.Errorf("completed Herdr console intent does not match saved pane")
	}
	journal.RemoveIntent(intentID)
	return journal.Save()
}

func completedManagedConsoleIntentMatchesPane(intent state.LaunchIntent, pane state.Pane) bool {
	if intent.Kind != state.IntentCoordinator || intent.Status != state.IntentRealized ||
		intent.Parent != ManagedConsoleRuntimeParent || intent.RuntimeParent != ManagedConsoleRuntimeParent ||
		filepath.Clean(intent.WorktreePath) != filepath.Clean(pane.WorktreePath) ||
		intent.WorkspaceLabel != pane.WorkspaceLabel {
		return false
	}
	route := backend.OwnedLaunchRoute{Session: intent.Session, SocketPath: intent.SocketPath}
	return validateManagedCoordinatorPane(pane, intent, route) == nil
}

func managedConsoleResult(
	owned ManagedSessionRuntime,
	pane state.Pane,
	callerEnvironment []string,
) (ManagedConsoleResult, error) {
	command, attach, err := owned.AttachForms(callerEnvironment)
	if err != nil {
		return ManagedConsoleResult{}, err
	}
	return ManagedConsoleResult{Pane: pane, AttachCommand: command, Attach: attach}, nil
}
