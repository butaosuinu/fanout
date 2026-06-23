// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
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

type viewMode int

const (
	modeMonitor viewMode = iota
	modeNewPane
)

type model struct {
	opts            Options
	mode            viewMode
	table           table.Model
	detail          viewport.Model
	width           int
	height          int
	allPanes        []paneView
	panes           []paneView
	filterQuery     string
	filterEditing   bool
	issues          map[issueKey]issueStatus
	lastState       time.Time
	lastGH          time.Time
	stateErr        string
	ghErr           string
	notifyErr       string
	watchRunning    bool
	lastWatch       time.Time
	watchLaunched   int
	watchErr        string
	watchDisabled   bool
	notice          string
	newPane         newPaneForm
	peek            panePeek
	pendingAction   *pendingLifecycleAction
	actionRunning   bool
	quitAfterAction bool
	actionMessage   string
	notifications   map[issueKey]issueTransitionSnapshot
	notifyPrimed    bool
	keyboardPaused  bool
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
	keyboard := newShiftEnterProtocols(os.Stdout)
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

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case keyboardProtocolsEnabledMsg:
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case tea.KeyMsg:
		m.resumeKeyboardProtocols()
		if m.pendingAction != nil {
			return m.updatePendingAction(msg)
		}
		if m.actionRunning {
			switch msg.String() {
			case "q", "ctrl+c":
				m.quitAfterAction = true
				m.actionMessage = "will quit after lifecycle action finishes"
			}
			return m, nil
		}
		if m.mode == modeNewPane {
			return m.updateNewPane(msg)
		}
		if m.filterEditing {
			next, cmd := m.updateFilterInput(msg)
			return next, cmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m.quit()
		case "/":
			m.filterEditing = true
			m.refreshRows()
			return m, nil
		case "esc":
			if m.filterQuery != "" {
				m.filterQuery = ""
				m.refreshRows()
			}
			return m, nil
		case "[":
			return m.jumpSession(-1)
		case "]":
			return m.jumpSession(1)
		case "n":
			m.openNewPaneForm()
			return m, nil
		case "A":
			return m, m.openSelectedWorktreeShellCmd()
		case "t":
			return m, m.openProjectRootShellCmd()
		case "enter", "o":
			cmd := m.focusSelectedCmd()
			return m, cmd
		case "p":
			cmd := m.peekSelectedCmd(true)
			return m, cmd
		case "c":
			return m.startPendingAction(actionClose)
		case "m":
			return m.startPendingAction(actionMerge)
		case "x":
			return m.startPendingAction(actionCleanup)
		}
		oldCursor := m.table.Cursor()
		next, cmd := m.table.Update(msg)
		m.table = next
		m.refreshDetail()
		if m.table.Cursor() != oldCursor {
			peekCmd := m.peekSelectedCmd(false)
			return m, tea.Batch(cmd, peekCmd)
		}
		return m, cmd
	case stateLoadedMsg:
		if msg.err != nil {
			m.stateErr = msg.err.Error()
		} else {
			m.stateErr = ""
		}
		m.allPanes = msg.panes
		m.lastState = msg.at
		m.refreshRows()
		peekCmd := m.peekSelectedCmd(false)
		if msg.scheduleNext {
			return m, tea.Batch(
				tea.Tick(m.opts.StateInterval, func(t time.Time) tea.Msg { return stateTickMsg(t) }),
				peekCmd,
			)
		}
		return m, peekCmd
	case ghLoadedMsg:
		if msg.err != nil {
			m.ghErr = msg.err.Error()
		} else {
			m.ghErr = ""
		}
		var notifyCmd tea.Cmd
		if msg.issues != nil {
			// Merge BEFORE transition detection: a degraded refresh can carry
			// partial blocker data (e.g. parent rows without the child body)
			// that the display immediately discards — notifications must not
			// fire on data the user never sees.
			issues := msg.issues
			if msg.err != nil && m.issues != nil {
				issues = mergeDegradedIssueStatuses(m.issues, msg.issues)
			}
			wasPrimed := m.notifyPrimed
			events := detectIssueTransitions(m.notifications, issues)
			if msg.err == nil {
				m.notifications = issueTransitionSnapshots(issues)
				if !m.notifyPrimed {
					m.notifyPrimed = true
				}
			} else if wasPrimed {
				m.notifications = mergePartialIssueTransitionSnapshots(m.notifications, issues)
			}
			if wasPrimed {
				notifyCmd = m.notifyEventsCmd(events)
				if len(events) > 0 {
					m.notice = transitionNotice(events)
				}
			}
			m.issues = issues
		}
		m.lastGH = msg.at
		m.refreshRows()
		if msg.scheduleNext {
			return m, tea.Batch(
				tea.Tick(m.opts.GHInterval, func(t time.Time) tea.Msg { return ghTickMsg(t) }),
				notifyCmd,
			)
		}
		return m, notifyCmd
	case lifecycleDoneMsg:
		m.actionRunning = false
		m.actionMessage = lifecycleResultMessage(msg)
		if m.quitAfterAction {
			m.quitAfterAction = false
			return m.quit()
		}
		return m, tea.Batch(m.loadStateCmd(false), m.loadGHCmd(false))
	case stateTickMsg:
		return m, m.loadStateCmd(true)
	case ghTickMsg:
		return m, m.loadGHCmd(true)
	case watchTickMsg:
		if m.opts.Watcher == nil {
			return m, nil
		}
		if m.watchRunning {
			return m, m.watchTickCmd()
		}
		m.watchRunning = true
		return m, tea.Batch(m.runWatchCmd(), m.watchTickCmd())
	case watchDoneMsg:
		m.watchRunning = false
		m.lastWatch = msg.at
		m.watchLaunched = len(msg.report.Launched)
		m.watchDisabled = watchReportDisabled(msg.report)
		m.watchErr = summarizeWatchError(msg.report, msg.err)
		return m, tea.Batch(m.loadStateCmd(false), m.loadGHCmd(false))
	case launchPaneMsg:
		m.newPane.launching = false
		if msg.err != nil {
			m.newPane.err = msg.err.Error()
			return m, nil
		}
		m.mode = modeMonitor
		switch {
		case msg.notice != "":
			m.notice = msg.notice
		case msg.count > 1:
			m.notice = fmt.Sprintf("created %d new agent panes", msg.count)
		default:
			m.notice = "created new agent pane"
		}
		return m, m.loadStateCmd(false)
	case launchShellMsg:
		if msg.err != nil {
			m.notice = fmt.Sprintf("terminal: %v", msg.err)
			return m, nil
		}
		switch {
		case msg.req.Root:
			m.notice = "opened project root terminal"
		case msg.req.Source != "":
			m.notice = fmt.Sprintf("opened terminal for %s", msg.req.Source)
		default:
			m.notice = "opened worktree terminal"
		}
		return m, m.loadStateCmd(false)
	case paneFocusedMsg:
		if msg.keyboardPaused {
			m.keyboardPaused = true
		}
		if msg.err != nil {
			m.notice = fmt.Sprintf("focus skipped for %s: %v", dash(msg.paneID), msg.err)
			if errors.Is(msg.err, errPaneNotAlive) {
				m.markPaneStale(msg.paneID)
				m.refreshRows()
			}
		} else {
			m.notice = fmt.Sprintf("focused %s; return to the fanout tui pane to continue", msg.paneID)
		}
		return m, nil
	case panePeekLoadedMsg:
		if pane, ok := m.selectedPane(); ok && pane.PaneID != msg.paneID {
			return m, nil
		}
		if msg.err != nil {
			m.peek = panePeek{PaneID: msg.paneID, At: msg.at, Err: msg.err.Error()}
			if errors.Is(msg.err, errPaneNotAlive) {
				m.markPaneStale(msg.paneID)
				m.refreshRows()
			}
		} else {
			m.peek = panePeek{PaneID: msg.paneID, Output: msg.output, At: msg.at}
		}
		m.refreshDetail()
		return m, nil
	case transitionNotifiedMsg:
		if msg.err != nil {
			m.notifyErr = msg.err.Error()
		} else {
			m.notifyErr = ""
		}
		return m, nil
	}
	return m, nil
}

func (m *model) resumeKeyboardProtocols() {
	if !m.keyboardPaused {
		return
	}
	m.opts.keyboard.Enable()
	m.keyboardPaused = false
}
