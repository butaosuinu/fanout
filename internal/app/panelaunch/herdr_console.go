package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	if err := worktree.EnsureLocalExclude(root); err != nil {
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
		result, reused, err := reuseHerdrConsole(locked, root, owned, pane)
		if err != nil || reused {
			return result, err
		}
	}
	realized, route, err := realizeHerdrConsole(
		ctx, root, shellPath, owned, callerEnvironment, locked, route,
	)
	if err != nil {
		return HerdrConsoleResult{}, err
	}
	launcher := &Launcher{Info: &fanoutruntime.Info{ProjectRoot: root}, Herdr: owned}
	live, err := launcher.startHerdrShell(ctx, locked, route, realized.Intent)
	if err != nil {
		return HerdrConsoleResult{}, err
	}
	pane, err = finalizeHerdrConsole(locked, root, realized.Intent, live)
	if err != nil {
		return HerdrConsoleResult{}, err
	}
	return herdrConsoleResult(owned, pane)
}

func realizeHerdrConsole(
	ctx context.Context,
	root, shellPath string,
	owned *herdrrun.OwnedSession,
	callerEnvironment []string,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
) (HerdrRealizeResult, herdrrun.OwnedLaunchRoute, error) {
	launch, prepared, err := prepareHerdrConsoleLaunch(
		locked, root, owned, route, shellPath, callerEnvironment,
	)
	if err != nil {
		return HerdrRealizeResult{}, route, err
	}
	realized, err := RealizeHerdrCoordinator(ctx, HerdrCoordinatorRequest{
		Parent: HerdrConsoleRuntimeParent, ProjectRoot: root, SourceRoot: root, CWD: root,
		HerdrSession: route.Session, SocketPath: route.SocketPath, Launch: launch,
	}, owned, locked, HerdrRealizeHooks{})
	if err != nil && !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		return HerdrRealizeResult{}, route, errors.Join(
			err,
			discardRejectedConsoleLaunch(locked, root, route, launch, prepared),
		)
	}
	return realized, route, nil
}

func reuseHerdrConsole(
	locked *state.LockedStore,
	projectRoot string,
	owned *herdrrun.OwnedSession,
	pane state.Pane,
) (HerdrConsoleResult, bool, error) {
	if err := verifySavedHerdrConsole(owned, pane); err != nil {
		if staleErr := removeAbsentHerdrConsole(locked, projectRoot, owned, pane); staleErr != nil {
			return HerdrConsoleResult{}, false, errors.Join(
				fmt.Errorf("saved Herdr console is not safely reusable: %w", err),
				staleErr,
			)
		}
		return HerdrConsoleResult{}, false, nil
	}
	if err := removeCompletedHerdrConsoleIntent(locked, projectRoot); err != nil {
		return HerdrConsoleResult{}, false, err
	}
	result, err := herdrConsoleResult(owned, pane)
	return result, true, err
}

func removeAbsentHerdrConsole(
	locked *state.LockedStore,
	projectRoot string,
	owned *herdrrun.OwnedSession,
	pane state.Pane,
) error {
	if err := validateSavedHerdrConsoleShape(pane); err != nil {
		return err
	}
	live, err := owned.Backend().ListLive()
	if err != nil {
		return fmt.Errorf("recheck saved Herdr console: %w", err)
	}
	for _, current := range live {
		if current.Ref.Workspace == pane.HerdrWorkspaceID {
			return fmt.Errorf("saved Herdr console workspace still exists with a different identity")
		}
	}
	if pane.SourceProjectRoot != projectRoot {
		return fmt.Errorf("stale Herdr console row belongs to linked worktree %s", pane.SourceProjectRoot)
	}
	if err := locked.RemovePane(pane.Parent, pane.IssueNum); err != nil {
		return fmt.Errorf("remove absent Herdr console row: %w", err)
	}
	return nil
}

func resolveHerdrConsoleInputs(projectRoot, shell string) (string, string, error) {
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize Herdr console root: %w", err)
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return "", "", fmt.Errorf("Herdr console root must be absolute")
	}
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = strings.TrimSpace(os.Getenv("SHELL"))
	}
	if !filepath.IsAbs(shell) {
		return "", "", fmt.Errorf("Herdr console shell must be an absolute path")
	}
	shell, err = filepath.EvalSymlinks(shell)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize Herdr console shell: %w", err)
	}
	info, err := os.Stat(shell)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", fmt.Errorf("Herdr console shell is not executable: %s", shell)
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
	return owned.LaunchRoute()
}

func prepareHerdrConsoleLaunch(
	locked *state.LockedStore,
	projectRoot string,
	owned HerdrLaunchRuntime,
	route herdrrun.OwnedLaunchRoute,
	shell string,
	callerEnvironment []string,
) (*state.HerdrLaunch, bool, error) {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return nil, false, err
	}
	intentID, err := state.HerdrCoordinatorIntentID(HerdrConsoleRuntimeParent, "", 0)
	if err != nil {
		return nil, false, err
	}
	if _, found := journal.FindIntent(intentID); found {
		return nil, false, nil
	}
	return newHerdrShellLaunch(owned, route, shell, callerEnvironment)
}

func newHerdrShellLaunch(
	owned HerdrLaunchRuntime,
	route herdrrun.OwnedLaunchRoute,
	shell string,
	callerEnvironment []string,
) (*state.HerdrLaunch, bool, error) {
	nonce, err := randomHerdrToken()
	if err != nil {
		return nil, false, err
	}
	environment, err := herdrrun.WorkloadEnvironment(callerEnvironment, route.LauncherPath)
	if err != nil {
		return nil, false, err
	}
	envPath, envCount, err := owned.PrepareWorkloadEnvironment(nonce, environment)
	if err != nil {
		return nil, false, err
	}
	return &state.HerdrLaunch{
		Nonce: nonce, Executable: shell, EnvFilePath: envPath, EnvNameCount: envCount,
	}, true, nil
}

func discardRejectedConsoleLaunch(
	locked *state.LockedStore,
	projectRoot string,
	route herdrrun.OwnedLaunchRoute,
	launch *state.HerdrLaunch,
	prepared bool,
) error {
	if !prepared {
		return nil
	}
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	intentID, err := state.HerdrCoordinatorIntentID(HerdrConsoleRuntimeParent, "", 0)
	if err != nil {
		return err
	}
	if _, found := journal.FindIntent(intentID); found {
		return nil
	}
	return herdrrun.DiscardWorkloadEnvironment(route.RuntimeDir, launch)
}

func (l *Launcher) startHerdrShell(
	ctx context.Context,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
) (live backend.LivePane, retErr error) {
	journal, intent, err := l.admitHerdrShellLaunch(ctx, locked, route, intent)
	if err != nil {
		return live, err
	}
	if intent.Launch.TokenIssued {
		return l.recoverIssuedHerdrShell(ctx, journal, intent)
	}
	return l.finishIssuedHerdrShell(ctx, intent)
}

func (l *Launcher) admitHerdrShellLaunch(
	ctx context.Context,
	locked *state.LockedStore,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
) (*state.LockedHerdrIntents, state.HerdrIntent, error) {
	if intent.Launch == nil || intent.Launch.Agent != "" || intent.Launch.AgentName != "" {
		return nil, intent, fmt.Errorf("Herdr shell intent has an invalid launch capsule")
	}
	journal, err := locked.HerdrIntents(l.Info.ProjectRoot)
	if err != nil {
		return nil, intent, err
	}
	if intent.Launch.TokenIssued {
		return journal, intent, nil
	}
	if err := admitHerdrAgentStartDeadline(locked, l.Info.ProjectRoot, intent); err != nil {
		return nil, intent, err
	}
	if err := l.admitHerdrLauncher(ctx, journal, route, &intent); err != nil {
		return nil, intent, err
	}
	if err := ensureHerdrLaunchActive(ctx, intent); err != nil {
		return nil, intent, err
	}
	intent.Launch.TokenIssued = true
	if err := saveHerdrLaunchPhase(journal, intent); err != nil {
		return nil, intent, err
	}
	return journal, intent, nil
}

func (l *Launcher) finishIssuedHerdrShell(
	ctx context.Context,
	intent state.HerdrIntent,
) (live backend.LivePane, retErr error) {
	if err := l.sendHerdrLaunchToken(ctx, intent); err != nil {
		return live, err
	}
	if err := l.waitForHerdrShellProcess(ctx, intent); err != nil {
		return live, err
	}
	if _, err := os.Lstat(intent.Launch.EnvFilePath); !errors.Is(err, os.ErrNotExist) {
		return live, fmt.Errorf("herdr workload environment capsule was not consumed")
	}
	return l.waitForHerdrShell(ctx, intent)
}

func (l *Launcher) recoverIssuedHerdrShell(
	ctx context.Context,
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	classifyCtx, cancel := context.WithTimeout(ctx, maxHerdrRecoveryClassificationTimeout)
	defer cancel()
	observationIntent := herdrCoordinatorObservationIntent(classifyCtx, intent)
	processErr := l.waitForHerdrShellProcess(classifyCtx, observationIntent)
	live, liveErr := l.waitForHerdrShell(classifyCtx, observationIntent)
	_, capsuleErr := os.Lstat(intent.Launch.EnvFilePath)
	if processErr == nil && liveErr == nil && errors.Is(capsuleErr, os.ErrNotExist) {
		return live, nil
	}
	cause := errors.Join(
		errHerdrLaunchResponseLost,
		processErr,
		liveErr,
		herdrCapsuleRecoveryError(capsuleErr),
	)
	return backend.LivePane{}, errors.Join(cause, l.failClosedIssuedHerdrLaunch(journal, intent))
}

func herdrCapsuleRecoveryError(statErr error) error {
	if statErr == nil {
		return fmt.Errorf("environment capsule was not consumed")
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("inspect environment capsule: %w", statErr)
}

func (l *Launcher) sendHerdrLaunchToken(ctx context.Context, intent state.HerdrIntent) error {
	stepCtx, cancel, err := herdrLaunchStepContext(ctx, intent)
	if err != nil {
		return err
	}
	return herdrLaunchStepResult(
		stepCtx, cancel,
		l.Herdr.SendLaunchToken(stepCtx, intent.Resource.PaneID, intent.Launch.Nonce),
	)
}

func (l *Launcher) waitForHerdrShellProcess(
	ctx context.Context,
	intent state.HerdrIntent,
) error {
	return retryHerdrObservation(ctx, intent, func(observeCtx context.Context) error {
		process, err := l.Herdr.ProcessInfo(observeCtx, intent.Resource.PaneID)
		if err != nil {
			return err
		}
		root, _, ok := herdrAgentProcessRoot(process, intent)
		if !ok || !directHerdrAgentProcess(root, intent) {
			return fmt.Errorf("Herdr shell process identity does not match launch intent")
		}
		return nil
	})
}

func (l *Launcher) waitForHerdrShell(
	ctx context.Context,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	deadline := time.UnixMilli(intent.ExpiresUnixMS)
	for time.Now().Before(deadline) {
		panes, err := l.Herdr.LivePanes(ctx)
		if err != nil {
			return backend.LivePane{}, err
		}
		for _, pane := range panes {
			if exactHerdrShellPane(intent, pane) {
				return pane, nil
			}
		}
		timer := time.NewTimer(min(herdrLaunchObservationInterval, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return backend.LivePane{}, ctx.Err()
		case <-timer.C:
		}
	}
	return backend.LivePane{}, fmt.Errorf("timed out waiting for exact Herdr shell identity")
}

func exactHerdrShellPane(intent state.HerdrIntent, pane backend.LivePane) bool {
	return pane.Ref.Backend == backend.Herdr &&
		pane.Ref.Workspace == intent.Resource.WorkspaceID &&
		pane.Ref.Pane == intent.Resource.PaneID &&
		pane.WorkspaceLabel == intent.Resource.Label &&
		pane.TerminalID == intent.Resource.TerminalID &&
		filepath.Clean(pane.CurrentPath) == filepath.Clean(intent.WorktreePath) &&
		pane.SessionID == intent.Session && pane.SocketPath == intent.SocketPath &&
		!pane.AgentPresent
}

func finalizeHerdrConsole(
	locked *state.LockedStore,
	projectRoot string,
	intent state.HerdrIntent,
	live backend.LivePane,
) (pane state.Pane, retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, markHerdrFinalizationFailure(locked, projectRoot, intent, retErr))
		}
	}()
	number := NextSyntheticPaneNumber(locked.Store, ManualParentRef)
	pane = state.Pane{
		Parent: ManualParentRef, RuntimeParent: HerdrConsoleRuntimeParent,
		IssueNum: number, Kind: state.PaneKindShell, Slug: "herdr-console",
		Backend: backend.Herdr, PaneID: live.Ref.Pane,
		HerdrWorkspaceID: live.Ref.Workspace, HerdrWorkspaceLabel: live.WorkspaceLabel,
		HerdrTerminalID: live.TerminalID, HerdrRepoKey: live.RepoKey,
		HerdrSession: live.SessionID, HerdrSocketPath: live.SocketPath,
		Agent: state.PaneKindShell, DisplayName: "Herdr console",
		WorktreePath: intent.WorktreePath, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := locked.RecordPane(pane); err != nil {
		return pane, err
	}
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return pane, err
	}
	journal.RemoveIntent(intent.ID)
	return pane, journal.Save()
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

func removeCompletedHerdrConsoleIntent(locked *state.LockedStore, projectRoot string) error {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	intentID, err := state.HerdrCoordinatorIntentID(HerdrConsoleRuntimeParent, "", 0)
	if err != nil {
		return err
	}
	if !journal.RemoveIntent(intentID) {
		return nil
	}
	return journal.Save()
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
