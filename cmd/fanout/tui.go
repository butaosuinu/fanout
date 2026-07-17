package main

import (
	"bytes"
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
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutnotify "github.com/butaosuinu/fanout/internal/infra/notify"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

const tuiPaneTitle = "fanout tui"

var runTUI = fanouttui.Run

func isTUIRequest(args []string) bool {
	return len(args) == 0
}

func cmdTUI(commandName string, lg *log.Logger) exitcode.Code {
	projectRoot, err := tuiProjectRoot()
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
	resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
	runtimeBackend := tmuxbackend.New()
	listLive := runtimeListLiveForProject(projectRoot, true)
	hookConfig := hooks.LoadUserConfig(lg)
	watcher, watchInterval, watchLabel, err := newTUIWatcher(projectRoot, session, commandName, resolvedSettings, hookConfig)
	if err != nil {
		lg.Err("watcher: %v", err)
		return exitcode.Env
	}
	notifier, err := fanoutnotify.New(fanoutnotify.Config{
		Channels:        resolvedSettings.Notifications,
		TmuxTarget:      session,
		NtfyURL:         resolvedSettings.NtfyURL,
		SlackWebhookURL: resolvedSettings.SlackWebhookURL,
		BellWriter:      os.Stdout,
	})
	if err != nil {
		lg.Err("notifications: %v", err)
		return exitcode.Env
	}
	restoreTitle := markTUIRunning(projectRoot)
	defer restoreTitle()
	bindDashboardKey(lg, resolvedSettings.DashboardKeybind)
	bindConsoleKey(lg, resolvedSettings.ConsoleKeybind)
	if err := runTUI(fanouttui.Options{
		ProjectRoot:         projectRoot,
		Session:             session,
		StateInterval:       2 * time.Second,
		GHInterval:          20 * time.Second,
		Watcher:             watcher,
		WatchInterval:       watchInterval,
		WatchLabel:          watchLabel,
		DefaultAgent:        defaultTUIAgent(),
		WatcherRunningLabel: resolvedSettings.WatcherRunningLabel,
		Hooks:               hookConfig,
		LaunchPane:          newTUILaunchPaneFunc(projectRoot, session, commandName, hookConfig),
		NewPanePrompt:       newTUINewPanePromptFunc(projectRoot, commandName),
		HelpPopup:           newTUIHelpPopupFunc(projectRoot, commandName),
		CloseChoicePopup:    newTUICloseChoicePopupFunc(projectRoot, commandName),
		SettingsPopup:       newTUISettingsPopupFunc(projectRoot, commandName),
		ReloadSettings:      newTUISettingsReloadFunc(projectRoot, session, commandName, hookConfig, lg),
		LaunchAttach:        newTUIAttachAgentFunc(projectRoot, session, commandName, hookConfig),
		// List providers also feed the in-process fallback form (NewPanePrompt
		// unavailable); the popup process wires its own copies.
		ListOpenIssues:    newTUIListOpenIssuesFunc(projectRoot),
		ListIssueChildren: newTUIListIssueChildrenFunc(projectRoot),
		LaunchIssue:       newTUIIssueLaunchFunc(projectRoot, session, commandName, resolvedSettings, hookConfig),
		LaunchIssuePlan:   newTUIIssuePlanLaunchFunc(projectRoot, session, commandName, hookConfig),
		OpenIssue:         newTUIOpenIssueFunc(projectRoot),
		LaunchShell:       newTUILaunchShellFunc(projectRoot, session),
		RestorePanes:      newTUIRestoreFunc(projectRoot, session, commandName),
		Relayout:          func() error { return panelayout.Apply(tuiLaunchTarget(session), panelayout.Resize) },
		ListLive:          listLive,
		LifecycleListLive: runtimeBackend.ListLive,
		ShellPaneAlive:    runtimeShellPaneAlive(runtimeBackend.ListLive),
		FocusPane: func(paneID string) error {
			return runtimeBackend.Focus(backend.PaneRef{Backend: backend.Tmux, Pane: paneID})
		},
		PaneAlive: func(paneID string) bool {
			panes, listErr := runtimeBackend.ListLive()
			if listErr != nil {
				return false
			}
			for _, pane := range panes {
				if pane.Ref.Pane == paneID {
					return true
				}
			}
			return false
		},
		CapturePaneOutput: func(paneID string, lines int) (string, error) {
			return runtimeBackend.Read(backend.PaneRef{Backend: backend.Tmux, Pane: paneID}, lines)
		},
		ClosePane:  runtimeBackend.Close,
		ActivePane: newTUIActivePaneFunc(os.Getenv("TMUX_PANE")),
		Notifier:   notifier,
	}); err != nil {
		lg.Err("tui: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
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

func newTUISettingsReloadFunc(projectRoot, session, commandName string, hookConfig hooks.Config, lg *log.Logger) fanouttui.SettingsReloadFunc {
	return func() (fanouttui.SettingsRuntime, error) {
		resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
		watcher, watchInterval, watchLabel, err := newTUIWatcher(projectRoot, session, commandName, resolvedSettings, hookConfig)
		if err != nil {
			return fanouttui.SettingsRuntime{}, fmt.Errorf("watcher: %w", err)
		}
		notifier, err := fanoutnotify.New(fanoutnotify.Config{
			Channels:        resolvedSettings.Notifications,
			TmuxTarget:      session,
			NtfyURL:         resolvedSettings.NtfyURL,
			SlackWebhookURL: resolvedSettings.SlackWebhookURL,
			BellWriter:      os.Stdout,
		})
		if err != nil {
			return fanouttui.SettingsRuntime{}, fmt.Errorf("notifications: %w", err)
		}
		syncDashboardKey(lg, resolvedSettings.DashboardKeybind, true)
		syncConsoleKey(lg, resolvedSettings.ConsoleKeybind, true)
		return fanouttui.SettingsRuntime{
			Watcher:             watcher,
			WatchInterval:       watchInterval,
			WatchLabel:          watchLabel,
			WatcherRunningLabel: resolvedSettings.WatcherRunningLabel,
			Notifier:            notifier,
			LaunchIssue:         newTUIIssueLaunchFunc(projectRoot, session, commandName, resolvedSettings, hookConfig),
		}, nil
	}
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
	if strings.TrimSpace(agentName) == "codex" {
		return "codex"
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
