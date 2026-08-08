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
	"time"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/panelayout"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutnotify "github.com/butaosuinu/fanout/internal/infra/notify"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

const tuiPaneTitle = "fanout tui"

var runTUI = fanouttui.Run

var (
	ensureOwnedHerdrForTUI   = ensureOwnedHerdrSession
	ensureHerdrConsoleForTUI = panelaunch.EnsureHerdrConsole
)

func isTUIRequest(args []string) bool {
	return len(args) == 0
}

func cmdTUI(commandName string, lg *log.Logger) exitcode.Code {
	stateRuntime, err := resolveStateRuntime()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	// Resolve an owning .fanout root from the cwd without consulting tmux. A
	// herdr pane may start inside <owner>/.fanout/worktrees/<child>, and the
	// backend decision must not strand the display on that child's empty state.
	projectRoot := resolveDisplayProjectRootFrom(stateRuntime.projectRoot, nil, projectHasState)
	selection, err := resolveDisplayBackendSelection(projectRoot)
	if err != nil {
		lg.Err("runtime backend: %v", err)
		return exitcode.Env
	}
	if selection.Name == backend.Herdr {
		return cmdHerdrTUI(projectRoot, commandName, selection, lg)
	}
	if selection.Name != backend.Tmux {
		lg.Err("tui: unsupported runtime backend %q", selection.Name)
		return exitcode.Env
	}
	// Tmux-hosted consoles preserve the existing pane-path fallback used by
	// keybind/wrapper launches. Herdr returns above so it never probes tmux just
	// to resolve the display root.
	projectRoot, err = tuiProjectRoot()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if exitOnMissingDeps(missingDeps(depNeeds{tmux: true}), lg) {
		return exitcode.Env
	}

	if !tmuxrun.InsideTmux() {
		return enterTUISession(projectRoot, commandName, lg)
	}

	session, err := tmuxrun.CurrentSession()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	// Forward Shift+Enter (and other modified keys) to the console so the
	// new-pane prompt can insert newlines instead of submitting on the first one.
	// Honor the same opt-out the TUI uses, so a user who disabled enhanced keys
	// does not have their tmux server reconfigured.
	if !fanouttui.EnhancedKeysDisabled(os.Getenv(fanouttui.EnhancedKeysEnv)) {
		tmuxrun.EnableExtendedKeys()
	}
	return runTUIConsole(projectRoot, session, commandName, selection, nil, lg)
}

func cmdHerdrTUI(
	projectRoot, commandName string,
	selection backend.Selection,
	lg *log.Logger,
) exitcode.Code {
	if os.Getenv("HERDR_ENV") != "1" {
		return enterHerdrTUISession(projectRoot, selection, lg)
	}
	owned, openErr := openOwnedHerdrSession(projectRoot)
	if openErr == nil && !ambientHerdrRouteMatches(owned) {
		openErr = fmt.Errorf("%s", ownedHerdrUnavailable)
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
		owned,
		lg,
	)
}

func enterHerdrTUISession(
	projectRoot string,
	selection backend.Selection,
	lg *log.Logger,
) exitcode.Code {
	owned, err := ensureOwnedHerdrForTUI(projectRoot)
	if err != nil {
		lg.Err("tui: ensure owned Herdr session: %v", err)
		return exitcode.Env
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	console, err := ensureHerdrConsoleForTUI(ctx, projectRoot, owned, os.Environ(), "")
	if err != nil {
		lg.Err("tui: ensure owned Herdr console: %v", err)
		return exitcode.Env
	}
	lg.Ok("Herdr console %s is ready (selected by %s)", console.Pane.PaneID, selection.Reason)
	fmt.Fprintln(lg.Stdout(), console.AttachCommand)
	return exitcode.OK
}

//nolint:funlen // The console composition root keeps its complete option wiring visible in one place.
func runTUIConsole(
	projectRoot, session, commandName string,
	selection backend.Selection,
	owned *herdrrun.OwnedSession,
	lg *log.Logger,
) exitcode.Code {
	tmuxHost := selection.Name == backend.Tmux
	resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
	listLive := runtimeListLiveForProject(projectRoot, tmuxHost)
	hookConfig := hooks.LoadUserConfig(lg)
	var watcher fanouttui.WatcherRunner
	var watchInterval time.Duration
	var watchLabel string
	var watchErr error
	interactiveLaunch := tmuxHost || owned != nil
	watcher, watchInterval, watchLabel, watchErr = newTUIWatcher(projectRoot, session, commandName, resolvedSettings, hookConfig, tmuxHost, interactiveLaunch)
	if watchErr != nil {
		lg.Err("watcher: %v", watchErr)
		return exitcode.Env
	}
	notifier, err := fanoutnotify.New(fanoutnotify.Config{
		Channels:        resolvedSettings.Notifications,
		RuntimeBackend:  selection.Name,
		TmuxTarget:      session,
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
			interactiveLaunch,
			lg,
		),
		ListLive: listLive,
		Notifier: notifier,
	}

	if tmuxHost {
		restoreTitle := wireTmuxTUI(
			&opts, projectRoot, session, commandName, resolvedSettings, hookConfig, lg,
		)
		defer restoreTitle()
	}
	if selection.Name == backend.Herdr {
		wireOwnedHerdrTUI(&opts, projectRoot, session, commandName, resolvedSettings, hookConfig, owned)
	}

	if err := runTUI(opts); err != nil {
		lg.Err("tui: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func wireTmuxTUI(
	opts *fanouttui.Options,
	projectRoot, session, commandName string,
	resolvedSettings settings.Settings,
	hookConfig hooks.Config,
	lg *log.Logger,
) func() {
	runtimeBackend := tmuxbackend.New()
	opts.LaunchPane = newTUILaunchPaneFunc(projectRoot, session, commandName, hookConfig)
	opts.NewPanePrompt = newTUINewPanePromptFunc(projectRoot, commandName)
	opts.HelpPopup = newTUIHelpPopupFunc(projectRoot, commandName)
	opts.CloseChoicePopup = newTUICloseChoicePopupFunc(projectRoot, commandName)
	opts.SettingsPopup = newTUISettingsPopupFunc(projectRoot, commandName)
	opts.LaunchAttach = newTUIAttachAgentFunc(projectRoot, session, commandName, hookConfig)
	opts.ListOpenIssues = newTUIListOpenIssuesFunc(projectRoot)
	opts.ListIssueChildren = newTUIListIssueChildrenFunc(projectRoot)
	opts.LaunchIssue = newTUIIssueLaunchFunc(projectRoot, session, commandName, resolvedSettings, hookConfig)
	opts.LaunchIssuePlan = newTUIIssuePlanLaunchFunc(projectRoot, session, commandName, hookConfig)
	opts.OpenIssue = newTUIOpenIssueFunc(projectRoot)
	opts.LaunchShell = newTUILaunchShellFunc(projectRoot, session)
	opts.RestorePanes = newTUIRestoreFunc(projectRoot, session, commandName)
	opts.Relayout = func() error { return panelayout.Apply(tuiLaunchTarget(session), panelayout.Resize) }
	wireTmuxPaneActions(opts, runtimeBackend)
	bindDashboardKey(lg, resolvedSettings.DashboardKeybind)
	bindConsoleKey(lg, resolvedSettings.ConsoleKeybind)
	return markTUIRunning(projectRoot)
}

func wireTmuxPaneActions(opts *fanouttui.Options, runtimeBackend *tmuxbackend.Backend) {
	opts.LifecycleCloseOwned = runtimeBackend.CloseOwned
	opts.ShellPaneAlive = runtimeShellPaneAlive(runtimeBackend.ListLive)
	opts.FocusPane = func(paneID string) error {
		return runtimeBackend.Focus(backend.PaneRef{Backend: backend.Tmux, Pane: paneID})
	}
	opts.PaneAlive = runtimeTmuxPaneAlive(runtimeBackend.ListLive)
	opts.CapturePaneOutput = func(paneID string, lines int) (string, error) {
		return runtimeBackend.Read(backend.PaneRef{Backend: backend.Tmux, Pane: paneID}, lines)
	}
	opts.ClosePane = runtimeBackend.Close
	opts.ActivePane = newTUIActivePaneFunc(os.Getenv("TMUX_PANE"))
}

func runtimeTmuxPaneAlive(listLive func() ([]backend.LivePane, error)) func(string) bool {
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

func ambientHerdrRouteMatches(owned *herdrrun.OwnedSession) bool {
	return owned != nil &&
		strings.TrimSpace(os.Getenv("HERDR_SESSION")) == owned.Session &&
		filepath.Clean(os.Getenv("HERDR_SOCKET_PATH")) == filepath.Clean(owned.SocketPath)
}

func wireOwnedHerdrTUI(
	opts *fanouttui.Options,
	projectRoot, session, commandName string,
	resolvedSettings settings.Settings,
	hookConfig hooks.Config,
	owned *herdrrun.OwnedSession,
) {
	opts.HerdrActionDisabled = func(pane state.Pane) string {
		return ownedHerdrActionDisabled(owned, pane)
	}
	if owned == nil {
		return
	}
	wireOwnedHerdrLaunchTUI(
		opts, projectRoot, session, commandName, resolvedSettings, hookConfig, owned,
	)
	wireOwnedHerdrPaneTUI(opts, owned)
}

func wireOwnedHerdrLaunchTUI(
	opts *fanouttui.Options,
	projectRoot, session, commandName string,
	resolvedSettings settings.Settings,
	hookConfig hooks.Config,
	owned *herdrrun.OwnedSession,
) {
	opts.LaunchPane = newTUILaunchPaneFunc(projectRoot, session, commandName, hookConfig)
	opts.LaunchAttach = newTUIAttachAgentFunc(projectRoot, session, commandName, hookConfig)
	opts.ListOpenIssues = newTUIListOpenIssuesFunc(projectRoot)
	opts.ListIssueChildren = newTUIListIssueChildrenFunc(projectRoot)
	opts.LaunchIssue = newTUIIssueLaunchFunc(projectRoot, session, commandName, resolvedSettings, hookConfig)
	opts.LaunchIssuePlan = newTUIIssuePlanLaunchFunc(projectRoot, session, commandName, hookConfig)
	opts.OpenIssue = newTUIOpenIssueFunc(projectRoot)
	opts.LaunchShell = newOwnedHerdrLaunchShellFunc(projectRoot, owned)
}

func wireOwnedHerdrPaneTUI(opts *fanouttui.Options, owned *herdrrun.OwnedSession) {
	opts.FocusHerdrPane = func(pane state.Pane) error {
		bound, ref, err := bindOwnedHerdrPane(owned, pane)
		if err != nil {
			return err
		}
		return bound.Focus(ref)
	}
	opts.CaptureHerdrPane = func(pane state.Pane, lines int) (string, error) {
		bound, ref, err := bindOwnedHerdrPane(owned, pane)
		if err != nil {
			return "", err
		}
		return bound.Read(ref, lines)
	}
}

func runtimeShellPaneAlive(listLive func() ([]backend.LivePane, error)) func(paneID, shellKey string) bool {
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
			if backend.NormalizeName(pane.Ref.Backend) == backend.Tmux && pane.Ref.Pane == paneID && pane.ShellKey == shellKey {
				return true
			}
		}
		return false
	}
}

func newTUISettingsReloadFunc(projectRoot, session, commandName string, hookConfig hooks.Config, selection backend.Selection, interactiveLaunch bool, lg *log.Logger) fanouttui.SettingsReloadFunc {
	return func() (fanouttui.SettingsRuntime, error) {
		resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
		var watcher fanouttui.WatcherRunner
		var watchInterval time.Duration
		var watchLabel string
		var watchErr error
		watcher, watchInterval, watchLabel, watchErr = newTUIWatcher(projectRoot, session, commandName, resolvedSettings, hookConfig, selection.Name == backend.Tmux, interactiveLaunch)
		if watchErr != nil {
			return fanouttui.SettingsRuntime{}, fmt.Errorf("watcher: %w", watchErr)
		}
		notifier, err := fanoutnotify.New(fanoutnotify.Config{
			Channels:        resolvedSettings.Notifications,
			RuntimeBackend:  selection.Name,
			TmuxTarget:      session,
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
		syncTUIReloadKeys(selection.Name, resolvedSettings, lg)
		runtime.LaunchIssue = reloadedTUIIssueLauncher(interactiveLaunch, projectRoot, session, commandName, resolvedSettings, hookConfig)
		return runtime, nil
	}
}

func syncTUIReloadKeys(runtimeBackend backend.Name, resolved settings.Settings, lg *log.Logger) {
	if runtimeBackend != backend.Tmux {
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
	if pane := strings.TrimSpace(os.Getenv("TMUX_PANE")); pane != "" {
		return pane
	}
	return session
}

func newTUIActivePaneFunc(consolePaneID string) func() (string, error) {
	consolePaneID = strings.TrimSpace(consolePaneID)
	return func() (string, error) {
		paneID, err := tmuxrun.ActivePaneInWindow(consolePaneID)
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

func enterTUISession(projectRoot, commandName string, lg *log.Logger) exitcode.Code {
	session := fanoutTUISessionName(projectRoot)
	created := false
	if !tmuxrun.HasSession(session) {
		if err := tmuxrun.NewSession(session, projectRoot); err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
		created = true
	}

	pane, running, err := findTUIPane(session)
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if !running {
		if created {
			pane, err = firstSessionPane(session)
		} else {
			pane, err = tmuxrun.NewWindow(session, tuiPaneTitle, projectRoot)
		}
		if err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
		if err := tmuxrun.SendKeys(pane.ID, tuiLaunchCommand(commandName, projectRoot), "Enter"); err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
	}
	if err := tmuxrun.FocusPane(pane); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	// Advertise extkeys for the outer terminal before the client attaches, so the
	// fresh attach forwards Shift+Enter without needing a re-attach. The inner
	// console (running in tmux) cannot see the outer TERM, so resolve it here in
	// the plain shell. Honor the same opt-out the TUI uses.
	if !fanouttui.EnhancedKeysDisabled(os.Getenv(fanouttui.EnhancedKeysEnv)) {
		tmuxrun.EnableExtendedKeysForTerm(os.Getenv("TERM"))
	}
	if err := tmuxrun.AttachOrSwitch(session); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	return exitcode.OK
}

func markTUIRunning(projectRoot string) func() {
	paneID := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if paneID == "" {
		return func() {}
	}
	_ = tmuxrun.SetPaneProjectRoot(paneID, projectRoot) // Best-effort dashboard keybinding hint.
	// Mark this pane as the console so the auto-layout reserves it as a sidebar.
	_ = tmuxrun.SetPaneRole(paneID, tmuxrun.RoleConsole)
	originalTitle, err := tmuxrun.PaneTitle(paneID)
	if err != nil {
		originalTitle = "fanout"
	}
	_ = tmuxrun.SetPaneTitle(paneID, tuiPaneTitle)
	return func() {
		_ = tmuxrun.SetPaneTitle(paneID, originalTitle)
		_ = tmuxrun.SetPaneRole(paneID, "") // a post-TUI shell must not look like a sidebar
		// Re-tile so the ex-console pane is not left stuck at the 40-col sidebar
		// width beside full-size agent panes.
		_ = panelayout.Apply(paneID, panelayout.Close)
	}
}

func findTUIPane(session string) (tmuxrun.PaneInfo, bool, error) {
	panes, err := tmuxrun.ListPanes(session)
	if err != nil {
		return tmuxrun.PaneInfo{}, false, err
	}
	for _, pane := range panes {
		if pane.Title == tuiPaneTitle {
			return pane, true, nil
		}
	}
	return tmuxrun.PaneInfo{}, false, nil
}

func firstSessionPane(session string) (tmuxrun.PaneInfo, error) {
	panes, err := tmuxrun.ListPanes(session)
	if err != nil {
		return tmuxrun.PaneInfo{}, err
	}
	if len(panes) == 0 {
		return tmuxrun.PaneInfo{}, fmt.Errorf("tmux session %s has no panes", session)
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
