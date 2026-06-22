package tui

import (
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/hooks"
	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/watch"
)

const (
	defaultStateInterval = 2 * time.Second
	defaultGHInterval    = 20 * time.Second
	defaultWatchInterval = 60 * time.Second
	minWatchInterval     = 20 * time.Second
	defaultWatchLabel    = "fanout:auto"
	detailHeight         = 13
	peekLines            = 80
	defaultLaunchAgent   = "claude"
	sessionSidebarAt     = 120
	sessionSidebarWidth  = 26
	sessionTopHeight     = 3
)

// Options configures the TUI monitor.
type Options struct {
	ProjectRoot         string
	Session             string
	StateInterval       time.Duration
	GHInterval          time.Duration
	Watcher             WatcherRunner
	WatchInterval       time.Duration
	WatchLabel          string
	DefaultAgent        string
	WatcherRunningLabel string
	Hooks               hooks.Config
	LaunchPane          LaunchFunc
	LaunchShell         ShellLaunchFunc
	FocusPane           func(string) error
	PaneAlive           func(string) bool
	ShellPaneAlive      func(paneID, shellKey string) bool
	CapturePaneOutput   func(string, int) (string, error)
	Notifier            transitionNotifier
	lifecycle           lifecycleRunner
	keyboard            keyboardProtocols
}

// LaunchRequest describes one manual pane launch requested from the TUI.
type LaunchRequest struct {
	Prompt string
	Agents []string
	Slug   string
}

// LaunchFunc creates a manual fanout pane for a TUI request. It returns an
// optional notice (e.g. a tolerated base-refresh skip) to surface on success.
type LaunchFunc func(LaunchRequest) (notice string, err error)

// WatcherRunner runs one watcher cycle.
type WatcherRunner interface {
	RunCycle() (watch.Report, error)
}

// ShellLaunchRequest describes one shell terminal launch requested from the TUI.
type ShellLaunchRequest struct {
	TargetPath string
	Root       bool
	Source     string
}

// ShellLaunchFunc creates a shell terminal pane for a TUI request.
type ShellLaunchFunc func(ShellLaunchRequest) error

type transitionNotifier interface {
	Notify([]fanoutnotify.Event) error
}

func normalizeOptions(opts Options) Options {
	if opts.StateInterval <= 0 {
		opts.StateInterval = defaultStateInterval
	}
	if opts.GHInterval <= 0 {
		opts.GHInterval = defaultGHInterval
	}
	if opts.Watcher != nil {
		if opts.WatchInterval <= 0 {
			opts.WatchInterval = defaultWatchInterval
		}
		if opts.WatchInterval < minWatchInterval {
			opts.WatchInterval = minWatchInterval
		}
		if strings.TrimSpace(opts.WatchLabel) == "" {
			opts.WatchLabel = defaultWatchLabel
		}
	}
	if opts.lifecycle == nil {
		opts.lifecycle = defaultLifecycleRunner{}
	}
	if opts.DefaultAgent != "codex" {
		opts.DefaultAgent = defaultLaunchAgent
	}
	if opts.FocusPane == nil {
		opts.FocusPane = tmuxrun.SelectPane
	}
	if opts.PaneAlive == nil {
		opts.PaneAlive = tmuxrun.IsPaneAlive
	}
	if opts.ShellPaneAlive == nil {
		opts.ShellPaneAlive = shellPaneAliveByKey
	}
	if opts.CapturePaneOutput == nil {
		opts.CapturePaneOutput = tmuxrun.CapturePaneOutput
	}
	if opts.keyboard == nil {
		opts.keyboard = noopKeyboardProtocols{}
	}
	return opts
}
