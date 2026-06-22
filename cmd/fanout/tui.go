package main

import (
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/log"
	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
)

const tuiPaneTitle = "fanout tui"

func isTUIRequest(args []string) bool {
	return len(args) == 0
}

func cmdTUI(commandName string, lg *log.Logger) exitcode.Code {
	projectRoot, err := tuiProjectRoot()
	if err != nil {
		lg.Err("%s", err.Error())
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
	resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
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
	if err := fanouttui.Run(fanouttui.Options{
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
		LaunchShell:         newTUILaunchShellFunc(projectRoot, session),
		Notifier:            notifier,
	}); err != nil {
		lg.Err("tui: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}
