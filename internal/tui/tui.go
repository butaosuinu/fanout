// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/worktree"
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
	ListRepoFiles       func(root string) ([]string, error)
	Notifier            transitionNotifier
	lifecycle           lifecycleRunner
	keyboard            keyboardProtocols
}

type viewMode int

const (
	modeMonitor viewMode = iota
	modeNewPane
)

type model struct {
	opts             Options
	mode             viewMode
	table            table.Model
	detail           viewport.Model
	width            int
	height           int
	allPanes         []paneView
	panes            []paneView
	filterQuery      string
	filterEditing    bool
	issues           map[issueKey]issueStatus
	lastState        time.Time
	lastGH           time.Time
	stateErr         string
	ghErr            string
	notifyErr        string
	watchRunning     bool
	lastWatch        time.Time
	watchLaunched    int
	watchErr         string
	watchDisabled    bool
	notice           string
	newPane          newPaneForm
	repoFiles        []string
	repoFileIndex    []fileEntry
	repoFilesLoaded  bool
	repoFilesLoading bool
	repoFilesErr     string
	peek             panePeek
	pendingAction    *pendingLifecycleAction
	actionRunning    bool
	quitAfterAction  bool
	actionMessage    string
	notifications    map[issueKey]issueTransitionSnapshot
	notifyPrimed     bool
	keyboardPaused   bool
}

type keyboardProtocolsEnabledMsg struct{}

type (
	stateTickMsg  time.Time
	ghTickMsg     time.Time
	watchTickMsg  time.Time
	launchPaneMsg struct {
		notice string
		count  int
		err    error
	}
	launchShellMsg struct {
		req ShellLaunchRequest
		err error
	}
)

// Run starts the Bubble Tea TUI.
func Run(opts Options) error {
	opts = normalizeOptions(opts)
	keyboard := newShiftEnterProtocols(os.Stdout, enhancedKeyboardKeysEnabled())
	opts.keyboard = keyboard
	m := newModel(opts)
	input, closeInput, err := newShiftEnterProgramInput(os.Stdin)
	if err != nil {
		return err
	}
	defer closeInput()
	defer keyboard.Disable()
	_, err = tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithInput(input),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.QuitMsg); ok {
				keyboard.Disable()
			}
			return msg
		}),
	).Run()
	return err
}

func enhancedKeyboardKeysEnabled() bool {
	return os.Getenv(EnhancedKeysEnv) == "1"
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
	if opts.ListRepoFiles == nil {
		opts.ListRepoFiles = worktree.ListFiles
	}
	if opts.keyboard == nil {
		opts.keyboard = noopKeyboardProtocols{}
	}
	return opts
}

func newModel(opts Options) model {
	opts = normalizeOptions(opts)
	t := table.New(
		table.WithColumns(columnsForWidth(120)),
		table.WithRows([]table.Row{}),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(colorAi)
	styles.Selected = styles.Selected.Bold(true).Foreground(colorAsagi)
	t.SetStyles(styles)

	return model{
		opts:          opts,
		mode:          modeMonitor,
		table:         t,
		detail:        viewport.New(120, detailHeight),
		issues:        map[issueKey]issueStatus{},
		notifications: map[issueKey]issueTransitionSnapshot{},
	}
}

func (m model) Init() tea.Cmd {
	loads := tea.Batch(m.loadStateCmd(true), m.loadGHCmd(true))
	if m.opts.Watcher == nil {
		return tea.Sequence(m.enableKeyboardProtocolsCmd(), loads)
	}
	return tea.Sequence(
		m.enableKeyboardProtocolsCmd(),
		tea.Batch(loads, m.watchTickCmd()),
	)
}

func (m model) enableKeyboardProtocolsCmd() tea.Cmd {
	keyboard := m.opts.keyboard
	return func() tea.Msg {
		keyboard.Enable()
		return keyboardProtocolsEnabledMsg{}
	}
}

func (m model) quit() (tea.Model, tea.Cmd) {
	m.opts.keyboard.Disable()
	m.keyboardPaused = false
	return m, tea.Quit
}
