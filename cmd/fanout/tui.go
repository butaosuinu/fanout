package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutnotify "github.com/butaosuinu/fanout/internal/infra/notify"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tty"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

const tuiPaneTitle = "fanout tui"

var runTUI = fanouttui.Run

var (
	ensureManagedSessionForTUI = paneruntime.EnsureProject
	ensureManagedConsoleForTUI = panelaunch.EnsureManagedConsole
)

// The console entry's terminal probe and process replacement are seams so
// tests can drive both attach forms without a real terminal.
var (
	stdioIsTerminal = func() bool {
		return tty.IsTerminal(os.Stdin) && tty.IsTerminal(os.Stdout)
	}
	execSessionAttach = func(spec backend.AttachExec) error {
		return syscall.Exec(spec.Path, spec.Argv, spec.Env)
	}
	execConsoleShell = syscall.Exec
)

// consoleRuntime is every capability the resident console's own session entry
// and pane bookkeeping call. ConsoleHost is the discriminating one: a runtime
// that keeps its sessions across fanout runs has no session for fanout to
// create or attach, and the rest of the set rides along on the same value, so
// one assertion resolves the whole lane.
type consoleRuntime interface {
	backend.Backend
	backend.ConsoleHost
	backend.PaneDecorator
	backend.OwnedCloser
	backend.LayoutManager
}

// asConsoleRuntime resolves rt's console capability set. ok=false means the
// runtime owns its own sessions, so the console enters through that runtime's
// managed path instead of bringing one up and attaching to it here.
func asConsoleRuntime(rt backend.Backend) (consoleRuntime, bool) {
	if _, ok := backend.AsConsoleHost(rt); !ok {
		return nil, false
	}
	runtime, ok := rt.(consoleRuntime)
	return runtime, ok
}

func isTUIRequest(args []string) bool {
	return len(args) == 0
}

// isManagedConsoleWorkloadRequest recognizes the reserved token the owned
// console workload is exec'd with; it runs the same resident console as the
// no-argument form. The token exists so the pane process is distinguishable
// from the idle pane launcher, which is the same binary with no arguments.
func isManagedConsoleWorkloadRequest(args []string) bool {
	return len(args) > 0 && args[0] == panelaunch.ManagedConsoleWorkloadArg
}

// cmdTUI wraps the whole resident-console run in the console-shell hand-off:
// inside the owned console pane every exit — an early environment failure as
// much as a TUI quit — must land in the operator shell, or the runtime folds
// the pane and the console row goes stale. Outside that pane the hand-off
// name is unset and the wrap is a no-op.
func cmdTUI(commandName string, lg *log.Logger) exitcode.Code {
	return handoffConsoleShell(runResidentTUI(commandName, lg), lg)
}

func runResidentTUI(commandName string, lg *log.Logger) exitcode.Code {
	stateRuntime, err := resolveStateRuntime()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	// Resolve an owning .fanout root from the cwd without consulting the host
	// runtime. A managed pane may start inside <owner>/.fanout/worktrees/<child>,
	// and the backend decision must not strand the display on that child's empty
	// state.
	projectRoot := resolveDisplayProjectRootFrom(stateRuntime.projectRoot, nil, projectHasState)
	selection, err := resolveDisplayBackendSelection(projectRoot)
	if err != nil {
		lg.Err("runtime backend: %v", err)
		return exitcode.Env
	}
	// The ambient route is empty here on purpose: this construction only resolves
	// which console lane the runtime supports, and neither lane reads a pane
	// through the value.
	runtimeBackend, err := paneruntime.NewBackend(selection.Name, "", "")
	if err != nil {
		lg.Err("tui: unsupported runtime backend %q", selection.Name)
		return exitcode.Env
	}
	console, hosted := asConsoleRuntime(runtimeBackend)
	if !hosted {
		return cmdManagedConsoleTUI(projectRoot, commandName, selection, lg)
	}
	return cmdHostedConsoleTUI(console, commandName, selection, lg)
}

// cmdHostedConsoleTUI runs the console inside a session the host runtime owns,
// bringing that session up first when fanout was started from a plain shell.
func cmdHostedConsoleTUI(
	console consoleRuntime,
	commandName string,
	selection backend.Selection,
	lg *log.Logger,
) exitcode.Code {
	// A hosted console keeps the pane-path fallback used by keybind and wrapper
	// launches; the managed lane never probes a pane for its display root.
	projectRoot, err := tuiProjectRoot()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if exitOnMissingDeps(missingDeps(depNeeds{tmux: true}), lg) {
		return exitcode.Env
	}
	if !console.InsideSession() {
		return enterTUISession(console, projectRoot, commandName, lg)
	}
	session, err := console.CurrentSession()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	// Forward Shift+Enter (and other modified keys) to the console so the
	// new-pane prompt can insert newlines instead of submitting on the first one.
	// Honor the same opt-out the TUI uses, so a user who disabled enhanced keys
	// does not have their runtime server reconfigured.
	if !fanouttui.EnhancedKeysDisabled(os.Getenv(fanouttui.EnhancedKeysEnv)) {
		console.EnableInputExtensions()
	}
	return runTUIConsole(projectRoot, session, commandName, selection, console, nil, lg)
}

// cmdManagedConsoleTUI runs the console for a runtime that owns its sessions.
// It either adopts the repository-owned session this process was already
// started inside, or bootstraps one and enters it in place — printing the
// attach command instead when no terminal is present.
func cmdManagedConsoleTUI(
	projectRoot, commandName string,
	selection backend.Selection,
	lg *log.Logger,
) exitcode.Code {
	if os.Getenv("HERDR_ENV") != "1" {
		return enterManagedConsole(projectRoot, selection, lg)
	}
	owned, openErr := paneruntime.OpenProject(projectRoot)
	if openErr == nil && !ambientRouteMatches(owned) {
		openErr = fmt.Errorf("%s", ownedPaneUnavailable)
		owned = nil
	}
	if openErr != nil {
		lg.Warn("tui: owned Herdr actions disabled: %v", openErr)
	}
	return runTUIConsole(
		projectRoot,
		strings.TrimSpace(os.Getenv("HERDR_SESSION")),
		commandName,
		selection,
		nil,
		owned,
		lg,
	)
}

// handoffConsoleShell execs the operator shell the console bootstrap recorded
// in ConsoleShellEnv, so quitting the TUI — an error exit included — leaves a
// live shell pane instead of ending the pane process, which the runtime would
// fold and turn the console row stale. The name is stripped from the shell's
// environment so a fanout started by hand in that shell exits normally. When
// no hand-off applies or the exec fails, code is returned unchanged.
func handoffConsoleShell(code exitcode.Code, lg *log.Logger) exitcode.Code {
	shell := os.Getenv(backend.ConsoleShellEnv)
	if shell == "" {
		return code
	}
	if err := panelaunch.RunnableConsoleShell(shell); err != nil {
		lg.Warn("tui: console shell hand-off skipped: %v", err)
		return code
	}
	// The line lands in the pane right above the new shell prompt, so a later
	// attach that finds a shell here also finds the way back in. FANOUT_BIN
	// names the pinned binary and survives the hand-off, so the hint works
	// even when no `fanout` is on the pane's PATH.
	fmt.Fprintln(lg.Stdout(), `fanout console closed; run "$FANOUT_BIN" here to reopen it`)
	if err := execConsoleShell(shell, []string{shell}, environmentWithout(backend.ConsoleShellEnv)); err != nil {
		lg.Warn("tui: console shell hand-off failed: %v", err)
		return code
	}
	panic("unreachable")
}

func environmentWithout(name string) []string {
	environment := os.Environ()
	kept := environment[:0]
	for _, entry := range environment {
		if entryName, _, ok := strings.Cut(entry, "="); ok && entryName == name {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func enterManagedConsole(
	projectRoot string,
	selection backend.Selection,
	lg *log.Logger,
) exitcode.Code {
	owned, err := ensureManagedSessionForTUI(projectRoot)
	if err != nil {
		lg.Err("tui: ensure owned Herdr session: %v", err)
		return exitcode.Env
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	console, err := ensureManagedConsoleForTUI(ctx, projectRoot, owned, os.Environ(), "")
	if err != nil {
		lg.Err("tui: ensure owned Herdr console: %v", err)
		return exitcode.Env
	}
	lg.Ok("Herdr console %s is ready (selected by %s)", console.Pane.PaneID, selection.Reason)
	return attachOwnedConsole(console, lg)
}

// attachOwnedConsole enters the owned session in place when a terminal is
// present; a successful exec never returns. Pipes and scripts get the attach
// command printed instead. A failed exec falls back to the same print so the
// operator can still enter by hand, but exits non-zero so wrappers see that
// the entry itself did not happen.
func attachOwnedConsole(console panelaunch.ManagedConsoleResult, lg *log.Logger) exitcode.Code {
	if !stdioIsTerminal() {
		fmt.Fprintln(lg.Stdout(), console.AttachCommand)
		return exitcode.OK
	}
	err := execSessionAttach(console.Attach)
	lg.Warn("tui: enter owned session: %v", err)
	fmt.Fprintln(lg.Stdout(), console.AttachCommand)
	return exitcode.Env
}

//nolint:funlen // The console composition root keeps its complete option wiring visible in one place.
func runTUIConsole(
	projectRoot, session, commandName string,
	selection backend.Selection,
	console consoleRuntime,
	owned paneruntime.ManagedSession,
	lg *log.Logger,
) exitcode.Code {
	hosted := console != nil
	resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
	listLive := runtimeListLiveForProject(projectRoot, hosted)
	hookConfig := hooks.LoadUserConfig(lg)
	var watcher fanouttui.WatcherRunner
	var watchInterval time.Duration
	var watchLabel string
	var watchErr error
	interactiveLaunch := hosted || owned != nil
	watcher, watchInterval, watchLabel, watchErr = newTUIWatcher(projectRoot, session, commandName, resolvedSettings, hookConfig, hosted, interactiveLaunch)
	if watchErr != nil {
		lg.Err("watcher: %v", watchErr)
		return exitcode.Env
	}
	notifier, err := fanoutnotify.New(fanoutnotify.Config{
		Channels:        resolvedSettings.Notifications,
		RuntimeBackend:  selection.Name,
		RuntimeTarget:   session,
		NtfyURL:         resolvedSettings.NtfyURL,
		SlackWebhookURL: resolvedSettings.SlackWebhookURL,
		BellWriter:      os.Stdout,
	})
	if err != nil {
		lg.Err("notifications: %v", err)
		return exitcode.Env
	}
	opts := fanouttui.Options{
		ProjectRoot:         projectRoot,
		Session:             session,
		BackendSelection:    selection,
		StateInterval:       2 * time.Second,
		GHInterval:          20 * time.Second,
		Watcher:             watcher,
		WatchInterval:       watchInterval,
		WatchLabel:          watchLabel,
		DefaultAgent:        defaultTUIAgent(),
		WatcherRunningLabel: resolvedSettings.WatcherRunningLabel,
		Hooks:               hookConfig,
		ReloadSettings: newTUISettingsReloadFunc(
			projectRoot,
			session,
			commandName,
			hookConfig,
			selection,
			hosted,
			interactiveLaunch,
			lg,
		),
		ListLive:                         listLive,
		Notifier:                         notifier,
		LifecycleWorkspaceRuntimeForRoot: newWorkspaceLifecycleFactory,
	}

	if hosted {
		restoreTitle := wireHostedConsoleTUI(
			&opts, console, projectRoot, session, commandName, resolvedSettings, hookConfig, lg,
		)
		defer restoreTitle()
	} else {
		wireManagedConsoleTUI(&opts, projectRoot, session, commandName, resolvedSettings, hookConfig, owned)
	}

	if err := runTUI(opts); err != nil {
		lg.Err("tui: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func wireHostedConsoleTUI(
	opts *fanouttui.Options,
	runtimeBackend consoleRuntime,
	projectRoot, session, commandName string,
	resolvedSettings settings.Settings,
	hookConfig hooks.Config,
	lg *log.Logger,
) func() {
	popupHost := newTUIPopupHost(runtimeBackend, os.Getenv(invokingPaneEnv))
	opts.LaunchPane = newTUILaunchPaneFunc(projectRoot, session, commandName, hookConfig)
	opts.NewPanePrompt = newTUINewPanePromptFunc(popupHost, projectRoot, commandName)
	opts.HelpPopup = newTUIHelpPopupFunc(popupHost, projectRoot, commandName)
	opts.CloseChoicePopup = newTUICloseChoicePopupFunc(popupHost, projectRoot, commandName)
	opts.SettingsPopup = newTUISettingsPopupFunc(popupHost, projectRoot, commandName)
	opts.LaunchAttach = newTUIAttachAgentFunc(projectRoot, session, commandName, hookConfig)
	opts.ListOpenIssues = newTUIListOpenIssuesFunc(projectRoot)
	opts.ListIssueChildren = newTUIListIssueChildrenFunc(projectRoot)
	opts.LaunchIssue = newTUIIssueLaunchFunc(projectRoot, session, commandName, resolvedSettings, hookConfig)
	opts.LaunchIssuePlan = newTUIIssuePlanLaunchFunc(projectRoot, session, commandName, hookConfig)
	opts.OpenIssue = newTUIOpenIssueFunc(projectRoot)
	opts.LaunchShell = newTUILaunchShellFunc(projectRoot, session)
	opts.RestorePanes = newTUIRestoreFunc(runtimeBackend, projectRoot, session, commandName)
	opts.Relayout = func() error {
		return runtimeBackend.Relayout(tuiLaunchTarget(session), backend.LayoutResize)
	}
	wireHostedPaneActions(opts, runtimeBackend)
	bindDashboardKey(lg, resolvedSettings.DashboardKeybind)
	bindConsoleKey(lg, resolvedSettings.ConsoleKeybind)
	return markTUIRunning(runtimeBackend, projectRoot)
}

func wireHostedPaneActions(opts *fanouttui.Options, runtimeBackend consoleRuntime) {
	hostName := runtimeBackend.Name()
	opts.LifecycleCloseOwned = runtimeBackend.CloseOwned
	opts.LifecycleRelayout = runtimeBackend.Relayout
	opts.ShellPaneAlive = runtimeShellPaneAlive(hostName, runtimeBackend.ListLive)
	opts.FocusPane = func(paneID string) error {
		return runtimeBackend.Focus(backend.PaneRef{Backend: hostName, Pane: paneID})
	}
	opts.PaneAlive = runtimeHostPaneAlive(runtimeBackend.ListLive)
	opts.CapturePaneOutput = func(paneID string, lines int) (string, error) {
		return runtimeBackend.Read(backend.PaneRef{Backend: hostName, Pane: paneID}, lines)
	}
	opts.ClosePane = runtimeBackend.Close
	opts.ActivePane = newTUIActivePaneFunc(runtimeBackend, os.Getenv(invokingPaneEnv))
}

func runtimeHostPaneAlive(listLive func() ([]backend.LivePane, error)) func(string) bool {
	return func(paneID string) bool {
		panes, err := listLive()
		if err != nil {
			return false
		}
		for _, pane := range panes {
			if pane.Ref.Pane == paneID {
				return true
			}
		}
		return false
	}
}

// ambientRouteMatches reports whether the session this process was started
// inside is the one the repository currently owns. The environment is a wire
// contract with the runtime that spawned the pane, so a stale value has to be
// rejected rather than trusted.
func ambientRouteMatches(owned paneruntime.ManagedSession) bool {
	return owned != nil &&
		strings.TrimSpace(os.Getenv("HERDR_SESSION")) == owned.Session &&
		filepath.Clean(os.Getenv("HERDR_SOCKET_PATH")) == filepath.Clean(owned.SocketPath)
}

func wireManagedConsoleTUI(
	opts *fanouttui.Options,
	projectRoot, session, commandName string,
	resolvedSettings settings.Settings,
	hookConfig hooks.Config,
	owned paneruntime.ManagedSession,
) {
	opts.ManagedActionDisabled = func(pane state.Pane) string {
		return managedActionDisabled(owned, pane)
	}
	if owned == nil {
		return
	}
	wireManagedLaunchTUI(
		opts, projectRoot, session, commandName, resolvedSettings, hookConfig, owned,
	)
	wireManagedPaneTUI(opts, owned)
}

func wireManagedLaunchTUI(
	opts *fanouttui.Options,
	projectRoot, session, commandName string,
	resolvedSettings settings.Settings,
	hookConfig hooks.Config,
	owned paneruntime.ManagedSession,
) {
	opts.LaunchPane = newTUILaunchPaneFunc(projectRoot, session, commandName, hookConfig)
	opts.LaunchAttach = newTUIAttachAgentFunc(projectRoot, session, commandName, hookConfig)
	opts.ListOpenIssues = newTUIListOpenIssuesFunc(projectRoot)
	opts.ListIssueChildren = newTUIListIssueChildrenFunc(projectRoot)
	opts.LaunchIssue = newTUIIssueLaunchFunc(projectRoot, session, commandName, resolvedSettings, hookConfig)
	opts.LaunchIssuePlan = newTUIIssuePlanLaunchFunc(projectRoot, session, commandName, hookConfig)
	opts.OpenIssue = newTUIOpenIssueFunc(projectRoot)
	opts.LaunchShell = newManagedLaunchShellFunc(projectRoot, owned)
}

func wireManagedPaneTUI(opts *fanouttui.Options, owned paneruntime.ManagedSession) {
	opts.FocusManagedPane = func(pane state.Pane) error {
		bound, ref, err := bindManagedPane(owned, pane)
		if err != nil {
			return err
		}
		return bound.Focus(ref)
	}
	opts.CaptureManagedPane = func(pane state.Pane, lines int) (string, error) {
		bound, ref, err := bindManagedPane(owned, pane)
		if err != nil {
			return "", err
		}
		return bound.Read(ref, lines)
	}
}

// runtimeShellPaneAlive answers the console's shell-row liveness question
// against hostName's rows only: a shell row's pane id is scoped to the runtime
// that issued it, so a mixed-backend listing must be filtered before the id is
// compared.
func runtimeShellPaneAlive(hostName backend.Name, listLive func() ([]backend.LivePane, error)) func(paneID, shellKey string) bool {
	return func(paneID, shellKey string) bool {
		paneID = strings.TrimSpace(paneID)
		shellKey = strings.TrimSpace(shellKey)
		if paneID == "" || shellKey == "" || listLive == nil {
			return false
		}
		panes, err := listLive()
		if err != nil {
			return false
		}
		for _, pane := range panes {
			if backend.NormalizeName(pane.Ref.Backend) == hostName && pane.Ref.Pane == paneID && pane.ShellKey == shellKey {
				return true
			}
		}
		return false
	}
}

func newTUISettingsReloadFunc(projectRoot, session, commandName string, hookConfig hooks.Config, selection backend.Selection, hosted, interactiveLaunch bool, lg *log.Logger) fanouttui.SettingsReloadFunc {
	return func() (fanouttui.SettingsRuntime, error) {
		resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
		var watcher fanouttui.WatcherRunner
		var watchInterval time.Duration
		var watchLabel string
		var watchErr error
		watcher, watchInterval, watchLabel, watchErr = newTUIWatcher(projectRoot, session, commandName, resolvedSettings, hookConfig, hosted, interactiveLaunch)
		if watchErr != nil {
			return fanouttui.SettingsRuntime{}, fmt.Errorf("watcher: %w", watchErr)
		}
		notifier, err := fanoutnotify.New(fanoutnotify.Config{
			Channels:        resolvedSettings.Notifications,
			RuntimeBackend:  selection.Name,
			RuntimeTarget:   session,
			NtfyURL:         resolvedSettings.NtfyURL,
			SlackWebhookURL: resolvedSettings.SlackWebhookURL,
			BellWriter:      os.Stdout,
		})
		if err != nil {
			return fanouttui.SettingsRuntime{}, fmt.Errorf("notifications: %w", err)
		}
		runtime := fanouttui.SettingsRuntime{
			Watcher:             watcher,
			WatchInterval:       watchInterval,
			WatchLabel:          watchLabel,
			WatcherRunningLabel: resolvedSettings.WatcherRunningLabel,
			Notifier:            notifier,
		}
		syncTUIReloadKeys(hosted, resolvedSettings, lg)
		runtime.LaunchIssue = reloadedTUIIssueLauncher(interactiveLaunch, projectRoot, session, commandName, resolvedSettings, hookConfig)
		return runtime, nil
	}
}

// syncTUIReloadKeys re-applies the global shortcuts a settings reload changed.
// It runs only for a hosted console: the shortcut registration resolves the
// host runtime itself, so a managed console would otherwise rewrite keys on a
// server it never put a pane on.
func syncTUIReloadKeys(hosted bool, resolved settings.Settings, lg *log.Logger) {
	if !hosted {
		return
	}
	syncDashboardKey(lg, resolved.DashboardKeybind, true)
	syncConsoleKey(lg, resolved.ConsoleKeybind, true)
}

func reloadedTUIIssueLauncher(enabled bool, projectRoot, session, commandName string, resolved settings.Settings, hookConfig hooks.Config) fanouttui.IssueLaunchFunc {
	if !enabled {
		return nil
	}
	return newTUIIssueLaunchFunc(projectRoot, session, commandName, resolved, hookConfig)
}

func tuiLaunchTarget(session string) string {
	if pane := strings.TrimSpace(os.Getenv(invokingPaneEnv)); pane != "" {
		return pane
	}
	return session
}

func newTUIActivePaneFunc(console backend.ConsoleHost, consolePaneID string) func() (string, error) {
	consolePaneID = strings.TrimSpace(consolePaneID)
	return func() (string, error) {
		paneID, err := console.ActivePaneInWindow(consolePaneID)
		if err != nil {
			return "", err
		}
		if paneID == consolePaneID {
			return "", nil
		}
		return paneID, nil
	}
}

func defaultTUIAgent() string {
	return tuiAgentOrDefault(os.Getenv("FANOUT_AGENT"))
}

func tuiAgentOrDefault(agentName string) string {
	name := strings.TrimSpace(agentName)
	if agent.ValidateKnown(name) == nil {
		return name
	}
	return "claude"
}

func bufferedLaunchError(stdout, stderr bytes.Buffer, fallback string) error {
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = strings.TrimSpace(stdout.String())
	}
	if msg == "" {
		msg = fallback
	}
	return fmt.Errorf("%s", msg)
}

func enterTUISession(console consoleRuntime, projectRoot, commandName string, lg *log.Logger) exitcode.Code {
	session := fanoutTUISessionName(projectRoot)
	created := false
	if !console.HasSession(session) {
		if err := console.NewSession(session, projectRoot); err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
		created = true
	}

	pane, running, err := findTUIPane(console, session)
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if !running {
		if created {
			pane, err = firstSessionPane(console, session)
		} else {
			pane, err = console.NewWindow(session, tuiPaneTitle, projectRoot)
		}
		if err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
		if err := console.RunCommandInPane(pane.ID, tuiLaunchCommand(commandName, projectRoot)); err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
	}
	if err := console.FocusPaneInSession(pane); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	// Advertise extkeys for the outer terminal before the client attaches, so the
	// fresh attach forwards Shift+Enter without needing a re-attach. The inner
	// console (running in the session) cannot see the outer TERM, so resolve it
	// here in the plain shell. Honor the same opt-out the TUI uses.
	if !fanouttui.EnhancedKeysDisabled(os.Getenv(fanouttui.EnhancedKeysEnv)) {
		console.EnableInputExtensionsForTerm(os.Getenv("TERM"))
	}
	if err := console.AttachOrSwitch(session); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	return exitcode.OK
}

func markTUIRunning(console consoleRuntime, projectRoot string) func() {
	paneID := strings.TrimSpace(os.Getenv(invokingPaneEnv))
	if paneID == "" {
		return func() {}
	}
	_ = console.SetPaneProjectRoot(paneID, projectRoot) // Best-effort dashboard keybinding hint.
	// Mark this pane as the console so the auto-layout reserves it as a sidebar.
	_ = console.SetPaneRole(paneID, backend.RoleConsole)
	originalTitle, err := console.PaneTitle(paneID)
	if err != nil {
		originalTitle = "fanout"
	}
	_ = console.SetPaneTitle(paneID, tuiPaneTitle)
	return func() {
		_ = console.SetPaneTitle(paneID, originalTitle)
		_ = console.SetPaneRole(paneID, "") // a post-TUI shell must not look like a sidebar
		// Re-tile so the ex-console pane is not left stuck at the 40-col sidebar
		// width beside full-size agent panes.
		_ = console.Relayout(paneID, backend.LayoutClose)
	}
}

func findTUIPane(console backend.ConsoleHost, session string) (backend.PaneInfo, bool, error) {
	panes, err := console.ListPanes(session)
	if err != nil {
		return backend.PaneInfo{}, false, err
	}
	for _, pane := range panes {
		if pane.Title == tuiPaneTitle {
			return pane, true, nil
		}
	}
	return backend.PaneInfo{}, false, nil
}

func firstSessionPane(console backend.ConsoleHost, session string) (backend.PaneInfo, error) {
	panes, err := console.ListPanes(session)
	if err != nil {
		return backend.PaneInfo{}, err
	}
	if len(panes) == 0 {
		return backend.PaneInfo{}, fmt.Errorf("tmux session %s has no panes", session)
	}
	return panes[0], nil
}

func tuiProjectRoot() (string, error) {
	return resolveDisplayProjectRoot()
}

func fanoutTUISessionName(projectRoot string) string {
	sum := sha1.Sum([]byte(projectRoot))
	base := panelaunch.SanitizeSessionPart(filepath.Base(projectRoot))
	return "fanout-" + base + "-" + hex.EncodeToString(sum[:])[:8]
}

func tuiLaunchCommand(commandName, projectRoot string) string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = commandName
	}
	// Enhanced keyboard input is on by default. Always forward the current value
	// (even empty) so the relaunched console matches this process exactly and
	// overrides any stale FANOUT_TUI_ENHANCED_KEYS that an earlier run captured in
	// the tmux session environment — otherwise an old opt-out would persist even
	// after relaunching from a plain shell with the variable unset.
	prefix := fanouttui.EnhancedKeysEnv + "=" + run.ShellQuote(os.Getenv(fanouttui.EnhancedKeysEnv)) + " "
	return "cd " + run.ShellQuote(projectRoot) + " && " + prefix + run.ShellQuote(exe)
}
