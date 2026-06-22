// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/lifecycle"
	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
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

type issueKey struct {
	Parent string
	Num    int
	TaskID string
	// Source is the owning worktree root for locally-scoped rows (plan tasks and
	// @manual panes), so the same (Parent, TaskID)/(Parent, Num) recorded in two
	// worktrees keys to distinct status — their branches and PR/CI state differ.
	// Empty for globally-stable GitHub issue rows, which aggregate across
	// worktrees (one status per issue number regardless of where it ran).
	Source string
}

// LaunchRequest describes one manual pane launch requested from the TUI.
type LaunchRequest struct {
	Prompt string
	Agent  string
	Slug   string
}

// LaunchFunc creates a manual fanout pane for a TUI request.
type LaunchFunc func(LaunchRequest) error

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

type issueStatus struct {
	Title           string
	State           string
	PRs             []ghissue.PRRef
	Wave            int
	WaveLabel       string
	Blockers        string
	BlockerRows     []blockers.Status
	HasOpenBlockers bool
	// WaveDegraded marks rows whose body hydration failed in this refresh:
	// the wave/blocker fields were computed without the child's body, so an
	// empty Blockers value means "could not read", not "confirmed clear".
	WaveDegraded bool
}

type transitionNotifier interface {
	Notify([]fanoutnotify.Event) error
}

type issueTransitionSnapshot struct {
	State     string
	HasMerged bool
	CIStatus  string
	Waiting   bool
	PRNumber  int
	Title     string
	Blockers  string
}

type paneView struct {
	Parent       string
	IssueNum     int
	TaskID       string
	Kind         string
	Name         string
	PaneID       string
	ShellKey     string
	TmuxState    string
	TmuxTitle    string
	AgentState   string
	IssueState   string
	PRSummary    string
	HasMergedPR  bool
	Wave         int
	WaveLabel    string
	WaveBadge    string
	Blockers     string
	Blocked      bool
	CIStatus     string
	DiffSummary  string
	DirtyState   string
	WorktreeErr  string
	BranchName   string
	WorktreePath string
	worktreeAbs  string
	// sourceProjectRoot は複数 worktree をまたいで集約した場合に、この pane を
	// 記録した worktree の root。close/merge/cleanup をその所有元 state.json へ
	// 向けるために使う。単一 worktree のときは空(= m.opts.ProjectRoot を使う)。
	sourceProjectRoot string
	// sourceProjectRoots は同一 identity が複数 worktree に記録されていた場合の
	// 全所有 root(通常は [sourceProjectRoot])。close/cleanup が de-duplicate
	// された sibling ストアも漏れなく対象にするために使う。
	sourceProjectRoots []string
	Agent              string
	CreatedAt          string
	Prompt             string
	Derived            sessionview.PaneDerived
}

type hudSummary struct {
	Total   int
	Merged  int
	Pending int
	Blocked int
}

type sessionSummary struct {
	Parent  string
	Start   int
	Total   int
	Merged  int
	Pending int
	Blocked int
	Live    int
	Active  bool
}

type monitorLayout struct {
	Sidebar        bool
	MainWidth      int
	TableRows      int
	PanelWidth     int
	TopStripHeight int
}

type viewMode int

const (
	modeMonitor viewMode = iota
	modeNewPane
)

type newPaneField int

const (
	newPaneFieldPrompt newPaneField = iota
	newPaneFieldAgent
	newPaneFieldSlug
	newPaneFieldCount
)

type newPaneForm struct {
	prompt    textarea.Model
	slug      textinput.Model
	agent     string
	focus     newPaneField
	launching bool
	err       string
}

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

type paneFilter struct {
	terms  []string
	states []string
	agents []string
	waves  []string
	runs   []string
	cis    []string
	dirty  []string
	live   []string
	issues []string
	prs    []string
	tasks  []string
}

type stateLoadedMsg struct {
	panes        []paneView
	at           time.Time
	err          error
	scheduleNext bool
}

type ghLoadedMsg struct {
	issues       map[issueKey]issueStatus
	at           time.Time
	err          error
	scheduleNext bool
}

type lifecycleAction string

const (
	actionClose   lifecycleAction = "close"
	actionMerge   lifecycleAction = "merge"
	actionCleanup lifecycleAction = "cleanup"
)

type pendingLifecycleAction struct {
	action lifecycleAction
	pane   paneView
}

type lifecycleDoneMsg struct {
	action lifecycleAction
	pane   paneView
	code   exitcode.Code
	output string
}

type worktreeStatView struct {
	Diff  string
	Dirty string
	Err   string
}

type paneFocusedMsg struct {
	paneID         string
	err            error
	keyboardPaused bool
}

type keyboardProtocolsEnabledMsg struct{}

type panePeekLoadedMsg struct {
	paneID string
	output string
	at     time.Time
	err    error
}

type panePeek struct {
	PaneID  string
	Output  string
	At      time.Time
	Err     string
	Loading bool
}

type (
	stateTickMsg  time.Time
	ghTickMsg     time.Time
	watchTickMsg  time.Time
	launchPaneMsg struct {
		err error
	}
	launchShellMsg struct {
		req ShellLaunchRequest
		err error
	}
)

type watchDoneMsg struct {
	report watch.Report
	at     time.Time
	err    error
}

type transitionNotifiedMsg struct {
	count int
	err   error
}

var errPaneNotAlive = errors.New("pane is no longer live")

type lifecycleRunner interface {
	Close(lifecycle.Options, string, int, lifecycle.Logger) exitcode.Code
	CloseTask(lifecycle.Options, string, string, lifecycle.Logger) exitcode.Code
	Merge(lifecycle.Options, string, int, lifecycle.Logger) exitcode.Code
	MergeTask(lifecycle.Options, string, string, lifecycle.Logger) exitcode.Code
	Cleanup(lifecycle.Options, string, lifecycle.Logger) exitcode.Code
	CleanupPlan(lifecycle.Options, string, lifecycle.Logger) exitcode.Code
}

type defaultLifecycleRunner struct{}

func (defaultLifecycleRunner) Close(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Close(opts, parent, issueNum, lg)
}

func (defaultLifecycleRunner) CloseTask(opts lifecycle.Options, parent, taskID string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.CloseTask(opts, parent, taskID, lg)
}

func (defaultLifecycleRunner) Merge(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Merge(opts, parent, issueNum, lg)
}

func (defaultLifecycleRunner) MergeTask(opts lifecycle.Options, parent, taskID string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.MergeTask(opts, parent, taskID, lg)
}

func (defaultLifecycleRunner) Cleanup(opts lifecycle.Options, parent string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Cleanup(opts, parent, lg)
}

func (defaultLifecycleRunner) CleanupPlan(opts lifecycle.Options, parent string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.CleanupPlan(opts, parent, lg)
}

type actionLogger struct {
	w io.Writer
}

func (l actionLogger) Info(format string, a ...any) {
	fmt.Fprintf(l.w, "[info] %s\n", fmt.Sprintf(format, a...))
}

func (l actionLogger) Ok(format string, a ...any) {
	fmt.Fprintf(l.w, "[ ok ] %s\n", fmt.Sprintf(format, a...))
}

func (l actionLogger) Warn(format string, a ...any) {
	fmt.Fprintf(l.w, "[warn] %s\n", fmt.Sprintf(format, a...))
}

func (l actionLogger) Err(format string, a ...any) {
	fmt.Fprintf(l.w, "[err ] %s\n", fmt.Sprintf(format, a...))
}

func (l actionLogger) Stderr() io.Writer {
	return l.w
}

// PAPER BREEZE palette (site/assets/css/main.css; keep the internal/log
// 256-color approximations in sync). Light = site values (紅 is an addition;
// the site defines no red), dark = same hue lifted for dark backgrounds.
var (
	colorAi     = lipgloss.AdaptiveColor{Light: "#165E83", Dark: "#6FAECE"} // 藍
	colorAsagi  = lipgloss.AdaptiveColor{Light: "#00A3AF", Dark: "#2BC4CF"} // 浅葱
	colorInk    = lipgloss.AdaptiveColor{Light: "#797D80", Dark: "#8A9096"} // 墨60%
	colorSuna   = lipgloss.AdaptiveColor{Light: "#E2D9C8", Dark: "#5C564C"} // 砂
	colorTsuchi = lipgloss.AdaptiveColor{Light: "#9A6B2F", Dark: "#C9974F"} // 土
	colorBeni   = lipgloss.AdaptiveColor{Light: "#B5495B", Dark: "#E07A8B"} // 紅
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAi)
	dimStyle   = lipgloss.NewStyle().Foreground(colorInk)
	warnStyle  = lipgloss.NewStyle().Foreground(colorTsuchi)
	errStyle   = lipgloss.NewStyle().Foreground(colorBeni)
	panelStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), true, false, false, false).BorderForeground(colorSuna)
	modalStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(1, 2).BorderForeground(colorAsagi)
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
		m.notice = "created new agent pane"
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

func (m model) jumpSession(delta int) (tea.Model, tea.Cmd) {
	sessions := buildSessionSummaries(m.panes, m.table.Cursor())
	if len(sessions) == 0 {
		m.notice = "no sessions to jump to"
		return m, nil
	}
	active := max(activeSessionIndex(sessions), 0)
	next := (active + delta + len(sessions)) % len(sessions)
	m.moveTableCursorTo(sessions[next].Start)
	m.refreshDetail()
	return m, m.peekSelectedCmd(false)
}

func (m *model) moveTableCursorTo(target int) {
	current := m.table.Cursor()
	switch {
	case target > current:
		m.table.MoveDown(target - current)
	case target < current:
		m.table.MoveUp(current - target)
	default:
		m.table.SetCursor(target)
	}
}

func (m model) updateFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "enter", "esc":
		m.filterEditing = false
		return m, nil
	case "backspace", "ctrl+h":
		m.filterQuery = trimLastRune(m.filterQuery)
	case "ctrl+u":
		m.filterQuery = ""
	default:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		m.filterQuery += string(msg.Runes)
	}
	m.refreshRows()
	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "fanout TUI"
	}
	header := titleStyle.Render("fanout")
	if m.opts.Session != "" {
		header += " " + dimStyle.Render("session="+m.opts.Session)
	}
	if m.opts.ProjectRoot != "" {
		header += " " + dimStyle.Render(m.opts.ProjectRoot)
	}
	header += " " + dimStyle.Render(formatHUD(summarizeHUD(m.panes)))

	footer := dimStyle.Render(m.footerText())
	if m.notice != "" {
		footer += "\n" + warnStyle.Render(m.notice)
	}
	if m.actionMessage != "" {
		footer += "\n" + m.renderActionMessage()
	}
	if m.stateErr != "" {
		footer += "\n" + errStyle.Render("state/tmux: "+m.stateErr)
	}
	if m.ghErr != "" {
		footer += "\n" + warnStyle.Render("gh: "+m.ghErr)
	}
	if m.notifyErr != "" {
		footer += "\n" + warnStyle.Render("notify: "+m.notifyErr)
	}

	layout := m.monitorLayout()
	body := m.monitorBody(layout)
	if layout.Sidebar {
		sessions := m.sessionSidebar(layout.PanelWidth, lipgloss.Height(body))
		body = lipgloss.JoinHorizontal(lipgloss.Top, sessions, " ", body)
	} else if layout.TopStripHeight > 0 {
		body = lipgloss.JoinVertical(lipgloss.Left, m.sessionTopStrip(layout.PanelWidth, layout.TopStripHeight), body)
	}
	base := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.mode == modeNewPane {
		return overlayCentered(base, m.newPaneView(), m.width, m.height)
	}
	return base
}

func (m *model) resize() {
	if m.width <= 0 {
		return
	}
	if m.mode == modeNewPane {
		inputWidth := m.formInputWidth()
		m.newPane.prompt.SetWidth(inputWidth)
		m.newPane.slug.Width = inputWidth
	}
	layout := m.monitorLayout()
	m.table.SetWidth(layout.MainWidth)
	m.table.SetHeight(layout.TableRows)
	m.table.SetColumns(columnsForWidth(layout.MainWidth))
	m.detail.Width = layout.MainWidth
	m.detail.Height = detailHeight
	m.refreshRows()
}

func (m model) monitorLayout() monitorLayout {
	width := max(1, m.width)
	rowBudget := m.height - detailHeight - 5
	layout := monitorLayout{
		MainWidth:  width,
		TableRows:  max(rowBudget, 4),
		PanelWidth: width,
	}
	if width >= sessionSidebarAt {
		layout.Sidebar = true
		layout.PanelWidth = sessionSidebarWidth
		layout.MainWidth = max(40, width-sessionSidebarWidth-1)
		return layout
	}
	if rowBudget >= sessionTopHeight+4 {
		layout.TopStripHeight = sessionTopHeight
		layout.TableRows = max(rowBudget-sessionTopHeight, 4)
	}
	return layout
}

func (m model) monitorBody(layout monitorLayout) string {
	tableView := m.table
	tableView.SetWidth(layout.MainWidth)
	tableView.SetHeight(layout.TableRows)
	tableView.SetColumns(columnsForWidth(layout.MainWidth))

	detail := m.detail
	detail.Width = layout.MainWidth
	detail.Height = detailHeight
	detail.SetContent(m.detailContent())

	return lipgloss.JoinVertical(
		lipgloss.Left,
		tableView.View(),
		panelStyle.Width(max(0, layout.MainWidth)).Render(detail.View()),
	)
}

type sessionRenderLine struct {
	Text   string
	Active bool
	Header bool
}

func (m model) sessionSidebar(width, height int) string {
	sessions := buildSessionSummaries(m.panes, m.table.Cursor())
	lines := []sessionRenderLine{{Text: fmt.Sprintf("Sessions %d", len(sessions)), Header: true}}
	rowBudget := max(height-2, 0)
	if len(sessions) == 0 {
		lines = append(lines, sessionRenderLine{Text: "(none)"})
	} else {
		lines = append(lines, sessionRows(sessions, rowBudget)...)
	}
	if len(lines) < height {
		lines = append(lines, sessionRenderLine{Text: "[/] session"})
	}
	return renderSessionBlock(lines, width, height, true)
}

func (m model) sessionTopStrip(width, height int) string {
	sessions := buildSessionSummaries(m.panes, m.table.Cursor())
	lines := []sessionRenderLine{
		{Text: fmt.Sprintf("Sessions %d  [/] session", len(sessions)), Header: true},
		{Text: topSessionText(sessions, width)},
		{Text: strings.Repeat("-", max(width, 0))},
	}
	return renderSessionBlock(lines, width, height, false)
}

func renderSessionBlock(lines []sessionRenderLine, width, height int, divider bool) string {
	width = max(width, 1)
	height = max(height, 1)
	contentWidth := width
	if divider {
		contentWidth = max(width-1, 1)
	}
	rendered := make([]string, 0, height)
	for i := range height {
		line := sessionRenderLine{}
		if i < len(lines) {
			line = lines[i]
		}
		text := fixedLine(line.Text, contentWidth)
		if divider {
			text += "|"
		}
		switch {
		case line.Active:
			text = titleStyle.Render(text)
		case line.Header:
			text = dimStyle.Render(text)
		}
		rendered = append(rendered, text)
	}
	return strings.Join(rendered, "\n")
}

func topSessionText(sessions []sessionSummary, width int) string {
	if len(sessions) == 0 {
		return "(none)"
	}
	for limit := len(sessions); limit >= 1; limit-- {
		text := joinSessionRows(sessionRows(sessions, limit))
		if len([]rune(text)) <= width {
			return text
		}
	}
	return joinSessionRows(sessionRows(sessions, 1))
}

func joinSessionRows(rows []sessionRenderLine) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.Text)
	}
	return strings.Join(parts, "  ")
}

func sessionRows(sessions []sessionSummary, limit int) []sessionRenderLine {
	if len(sessions) == 0 || limit <= 0 {
		return nil
	}
	active := max(activeSessionIndex(sessions), 0)
	if len(sessions) <= limit {
		return sessionSummaryLines(sessions)
	}
	if limit < 3 {
		return sessionSummaryLines(sessions[active : active+1])
	}

	slots := limit
	start := clampInt(active-slots/2, 0, len(sessions)-slots)
	end := start + slots
	if start > 0 {
		slots--
	}
	if end < len(sessions) {
		slots--
	}
	slots = max(slots, 1)
	start = clampInt(active-slots/2, 0, len(sessions)-slots)
	end = start + slots

	rows := []sessionRenderLine{}
	if start > 0 {
		rows = append(rows, sessionRenderLine{Text: "..."})
	}
	rows = append(rows, sessionSummaryLines(sessions[start:end])...)
	if end < len(sessions) {
		rows = append(rows, sessionRenderLine{Text: "..."})
	}
	return rows
}

func sessionSummaryLines(sessions []sessionSummary) []sessionRenderLine {
	lines := make([]sessionRenderLine, 0, len(sessions))
	for _, session := range sessions {
		lines = append(lines, sessionRenderLine{
			Text:   sessionSummaryText(session),
			Active: session.Active,
		})
	}
	return lines
}

func sessionSummaryText(session sessionSummary) string {
	marker := " "
	if session.Active {
		marker = ">"
	}
	return fmt.Sprintf(
		"%s %s t%d m%d p%d b%d l%d",
		marker,
		compactParent(session.Parent),
		session.Total,
		session.Merged,
		session.Pending,
		session.Blocked,
		session.Live,
	)
}

func buildSessionSummaries(panes []paneView, cursor int) []sessionSummary {
	if len(panes) == 0 {
		return nil
	}
	if cursor < 0 || cursor >= len(panes) {
		cursor = 0
	}
	activeParent := strings.TrimSpace(panes[cursor].Parent)
	if activeParent == "" {
		activeParent = "-"
	}
	indexByParent := map[string]int{}
	sessions := []sessionSummary{}
	for i, pane := range panes {
		parent := strings.TrimSpace(pane.Parent)
		if parent == "" {
			parent = "-"
		}
		idx, ok := indexByParent[parent]
		if !ok {
			idx = len(sessions)
			indexByParent[parent] = idx
			sessions = append(sessions, sessionSummary{
				Parent: parent,
				Start:  i,
				Active: parent == activeParent,
			})
		}
		sessions[idx].Total++
		if pane.HasMergedPR {
			sessions[idx].Merged++
		}
		if pane.Blocked {
			sessions[idx].Blocked++
		}
		if pane.TmuxState == "live" {
			sessions[idx].Live++
		}
	}
	for i := range sessions {
		sessions[i].Pending = sessions[i].Total - sessions[i].Merged
	}
	return sessions
}

func activeSessionIndex(sessions []sessionSummary) int {
	for i, session := range sessions {
		if session.Active {
			return i
		}
	}
	return -1
}

func (m model) updatePendingAction(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		pending := *m.pendingAction
		m.pendingAction = nil
		m.actionRunning = true
		m.actionMessage = lifecycleRunningMessage(pending)
		return m, m.lifecycleCmd(pending)
	case "n", "esc", "q", "ctrl+c":
		m.actionMessage = fmt.Sprintf("%s canceled", m.pendingAction.action)
		m.pendingAction = nil
		return m, nil
	}
	return m, nil
}

func (m model) startPendingAction(action lifecycleAction) (tea.Model, tea.Cmd) {
	pane, ok := m.selectedPane()
	if !ok {
		m.actionMessage = "no pane selected"
		return m, nil
	}
	if pane.isShell() && action != actionClose {
		m.actionMessage = fmt.Sprintf("%s unavailable for shell terminal", action)
		return m, nil
	}
	m.pendingAction = &pendingLifecycleAction{action: action, pane: pane}
	m.actionMessage = confirmMessage(action, pane)
	return m, nil
}

func (m model) lifecycleCmd(pending pendingLifecycleAction) tea.Cmd {
	// Close/merge act on one recorded pane, so route to the worktree that
	// recorded it. With cross-worktree aggregation that row may live in a sibling
	// worktree's state.json; using m.opts.ProjectRoot unconditionally would
	// target the wrong (often empty) store and fail with "not recorded".
	paneRoot := strings.TrimSpace(pending.pane.sourceProjectRoot)
	if paneRoot == "" {
		paneRoot = m.opts.ProjectRoot
	}
	// The watcher "running" label is removed from the GitHub issue on
	// close/merge/cleanup; it is repo-scoped, so the gh runner stays rooted at the
	// home checkout while state ops route to each owning worktree.
	watcherLabel := m.opts.WatcherRunningLabel
	removeLabel := ghissue.Runner{Cwd: m.opts.ProjectRoot}.RemoveIssueLabel
	lifecycleOpts := func(root string) lifecycle.Options {
		return lifecycle.Options{
			ProjectRoot:         root,
			StatePath:           state.Path(root),
			Hooks:               m.opts.Hooks,
			WatcherRunningLabel: watcherLabel,
			RemoveIssueLabel:    removeLabel,
		}
	}
	paneOpts := lifecycleOpts(paneRoot)
	// Close removes the row from its owning store(s). When the same logical child
	// was recorded in several worktrees the loader collapses it to one displayed
	// row but keeps every owning root here, so close each — otherwise the
	// de-duplicated sibling row survives and reappears on the next refresh.
	closeRoots := pending.pane.sourceProjectRoots
	if len(closeRoots) == 0 {
		closeRoots = []string{paneRoot}
	}
	// Cleanup is parent-scoped. For a globally-stable parent (a GitHub issue or
	// Project) the same parent in two worktrees is the same Session, so clean
	// every source root it spans — otherwise sibling rows survive and reappear on
	// the next refresh. But a locally-scoped parent (plan:<slug>, @manual) is only
	// meaningful within its worktree: two worktrees can hold unrelated plans under
	// the same slug, so cleanup must stay within the selected pane's own root(s).
	var cleanupRoots []string
	if isLocalParent(pending.pane.Parent) {
		cleanupRoots = pending.pane.sourceProjectRoots
		if len(cleanupRoots) == 0 {
			cleanupRoots = []string{paneRoot}
		}
	} else {
		cleanupRoots = m.sourceRootsForParent(pending.pane.Parent)
	}
	runner := m.opts.lifecycle
	return func() tea.Msg {
		var buf bytes.Buffer
		lg := actionLogger{w: &buf}
		var code exitcode.Code
		switch pending.action {
		case actionClose:
			code = exitcode.OK
			for _, r := range closeRoots {
				opts := lifecycleOpts(r)
				var c exitcode.Code
				if pending.pane.isTask() {
					c = runner.CloseTask(opts, pending.pane.Parent, pending.pane.TaskID, lg)
				} else {
					c = runner.Close(opts, pending.pane.Parent, pending.pane.IssueNum, lg)
				}
				if c != exitcode.OK {
					code = c
				}
			}
		case actionMerge:
			if pending.pane.isTask() {
				code = runner.MergeTask(paneOpts, pending.pane.Parent, pending.pane.TaskID, lg)
			} else {
				code = runner.Merge(paneOpts, pending.pane.Parent, pending.pane.IssueNum, lg)
			}
		case actionCleanup:
			code = exitcode.OK
			for _, r := range cleanupRoots {
				opts := lifecycleOpts(r)
				var c exitcode.Code
				if pending.pane.isTask() {
					c = runner.CleanupPlan(opts, pending.pane.Parent, lg)
				} else {
					c = runner.Cleanup(opts, pending.pane.Parent, lg)
				}
				if c != exitcode.OK {
					code = c
				}
			}
		default:
			code = exitcode.Invocation
			fmt.Fprintf(&buf, "[err ] unknown lifecycle action: %s\n", pending.action)
		}
		return lifecycleDoneMsg{
			action: pending.action,
			pane:   pending.pane,
			code:   code,
			output: strings.TrimSpace(buf.String()),
		}
	}
}

// isLocalParent reports whether a parent ref is only meaningful within one
// worktree — a plan slug (plan:<slug>) or the synthetic manual ref (@manual) — as
// opposed to a globally-stable parent: a GitHub issue number, a Project URL, or
// @watch (repo-wide watcher panes keyed by real GitHub issue numbers). Locally
// scoped parents can collide across worktrees with unrelated work, so
// parent-scoped lifecycle actions must not fan across worktrees for them. Note
// not every @-prefixed ref is local — @watch is repo-wide — so this matches
// @manual exactly rather than the @ prefix.
func isLocalParent(parent string) bool {
	parent = strings.TrimSpace(parent)
	return strings.HasPrefix(parent, "plan:") || parent == "@manual"
}

// sourceRootsForParent returns the distinct worktree roots whose state.json
// recorded a pane under parent, so a parent-scoped cleanup reaches every store
// the aggregated Session spans. Synthetic not-started rows (no sourceProjectRoot)
// are skipped; if none of the parent's panes carry a source root the home root
// is used.
func (m model) sourceRootsForParent(parent string) []string {
	seen := map[string]bool{}
	var roots []string
	// Normalize so numeric parent aliases ("100" vs "0100") recorded in
	// different worktrees match, mirroring lifecycle.Cleanup's parentMatches —
	// otherwise an exact compare would skip an eligible sibling root.
	want := sessionview.NormalizeParent(parent)
	for _, p := range m.allPanes {
		if sessionview.NormalizeParent(p.Parent) != want {
			continue
		}
		// Union every owning root, including those of identities the loader
		// collapsed into this row (sourceProjectRoots), so cleanup reaches the
		// de-duplicated sibling stores too. Synthetic not-started rows carry none.
		for _, root := range p.sourceProjectRoots {
			if root = strings.TrimSpace(root); root == "" {
				continue
			}
			if !seen[root] {
				seen[root] = true
				roots = append(roots, root)
			}
		}
	}
	if len(roots) == 0 {
		roots = []string{m.opts.ProjectRoot}
	}
	return roots
}

func confirmMessage(action lifecycleAction, pane paneView) string {
	switch action {
	case actionCleanup:
		return fmt.Sprintf("confirm cleanup for parent %s? y/n", dash(pane.Parent))
	default:
		return fmt.Sprintf("confirm %s %s? y/n", action, pane.identityLabel())
	}
}

func lifecycleResultMessage(msg lifecycleDoneMsg) string {
	prefix := fmt.Sprintf("%s %s", msg.action, msg.pane.identityLabel())
	if msg.action == actionCleanup {
		prefix = fmt.Sprintf("%s parent %s", msg.action, dash(msg.pane.Parent))
	}
	result := "ok"
	if msg.code != exitcode.OK {
		result = fmt.Sprintf("failed code=%d", msg.code)
	}
	if msg.output == "" {
		return prefix + ": " + result
	}
	return prefix + ": " + result + ": " + compactMessage(msg.output)
}

func lifecycleRunningMessage(pending pendingLifecycleAction) string {
	if pending.action == actionCleanup {
		return fmt.Sprintf("%s parent %s...", pending.action, dash(pending.pane.Parent))
	}
	return fmt.Sprintf("%s %s...", pending.action, pending.pane.identityLabel())
}

func (m model) renderActionMessage() string {
	if m.pendingAction != nil || m.actionRunning {
		return warnStyle.Render(m.actionMessage)
	}
	return dimStyle.Render(m.actionMessage)
}

func (m *model) openNewPaneForm() {
	m.mode = modeNewPane
	m.notice = ""
	m.newPane = newNewPaneForm(m.opts.DefaultAgent, m.formInputWidth())
}

func newNewPaneForm(defaultAgent string, width int) newPaneForm {
	prompt := textarea.New()
	prompt.Placeholder = "Prompt"
	prompt.Prompt = "> "
	prompt.ShowLineNumbers = false
	prompt.CharLimit = 1000
	prompt.SetWidth(width)
	prompt.SetHeight(6)
	prompt.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j"),
		key.WithHelp("shift+enter", "newline"),
	)
	prompt.Focus()

	slug := textinput.New()
	slug.Placeholder = "optional-slug"
	slug.Prompt = "> "
	slug.CharLimit = 80
	slug.Width = width
	slug.Blur()

	if defaultAgent != "codex" {
		defaultAgent = defaultLaunchAgent
	}
	return newPaneForm{
		prompt: prompt,
		slug:   slug,
		agent:  defaultAgent,
		focus:  newPaneFieldPrompt,
	}
}

func (m model) updateNewPane(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.newPane.launching {
		if msg.String() == "ctrl+c" {
			return m.quit()
		}
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		m.mode = modeMonitor
		m.newPane = newPaneForm{}
		return m, nil
	case "tab", "shift+tab":
		m.moveNewPaneFocus(msg.String())
		return m, nil
	case "left", "right", " ":
		if m.newPane.focus == newPaneFieldAgent {
			m.toggleNewPaneAgent()
			return m, nil
		}
	case "up", "down":
		if m.newPane.focus != newPaneFieldPrompt {
			m.moveNewPaneFocus(msg.String())
			return m, nil
		}
	case "enter":
		return m, m.submitNewPane()
	}

	var cmd tea.Cmd
	switch m.newPane.focus {
	case newPaneFieldPrompt:
		m.newPane.prompt, cmd = m.newPane.prompt.Update(msg)
	case newPaneFieldSlug:
		m.newPane.slug, cmd = m.newPane.slug.Update(msg)
	default:
		// The agent field is a toggle, not a text input; no message routing.
	}
	return m, cmd
}

func (m *model) moveNewPaneFocus(key string) {
	switch key {
	case "shift+tab", "up":
		m.newPane.focus = (m.newPane.focus + newPaneFieldCount - 1) % newPaneFieldCount
	default:
		m.newPane.focus = (m.newPane.focus + 1) % newPaneFieldCount
	}
	if m.newPane.focus == newPaneFieldPrompt {
		m.newPane.prompt.Focus()
	} else {
		m.newPane.prompt.Blur()
	}
	if m.newPane.focus == newPaneFieldSlug {
		m.newPane.slug.Focus()
	} else {
		m.newPane.slug.Blur()
	}
}

func (m *model) toggleNewPaneAgent() {
	if m.newPane.agent == "claude" {
		m.newPane.agent = "codex"
		return
	}
	m.newPane.agent = "claude"
}

func (m *model) submitNewPane() tea.Cmd {
	prompt := strings.TrimSpace(m.newPane.prompt.Value())
	if prompt == "" {
		m.newPane.err = "prompt is required"
		return nil
	}
	if m.opts.LaunchPane == nil {
		m.newPane.err = "new pane launcher is not configured"
		return nil
	}
	m.newPane.err = ""
	m.newPane.launching = true
	req := LaunchRequest{
		Prompt: prompt,
		Agent:  m.newPane.agent,
		Slug:   strings.TrimSpace(m.newPane.slug.Value()),
	}
	launch := m.opts.LaunchPane
	return func() tea.Msg {
		return launchPaneMsg{err: launch(req)}
	}
}

func (m model) newPaneView() string {
	lines := []string{
		titleStyle.Render("New agent pane"),
		m.newPaneFieldView(newPaneFieldPrompt, "Prompt", m.newPane.prompt.View()),
		m.newPaneFieldView(newPaneFieldAgent, "Agent", m.agentSelectorView()),
		m.newPaneFieldView(newPaneFieldSlug, "Slug", m.newPane.slug.View()),
	}
	if m.newPane.launching {
		lines = append(lines, dimStyle.Render("creating pane..."))
	}
	if m.newPane.err != "" {
		lines = append(lines, errStyle.Render("error: "+m.newPane.err))
	}
	lines = append(lines, dimStyle.Render("enter create  shift+enter newline  ctrl+j newline  tab field  arrows/space agent  esc cancel"))
	return modalStyle.Width(m.modalWidth()).Render(strings.Join(lines, "\n"))
}

func (m model) newPaneFieldView(field newPaneField, label, value string) string {
	marker := "  "
	if m.newPane.focus == field {
		marker = "> "
	}
	return marker + label + "\n" + value
}

func (m model) agentSelectorView() string {
	var claude, codex string
	if m.newPane.agent == "claude" {
		claude = titleStyle.Render("[claude]")
		codex = dimStyle.Render(" codex ")
	} else {
		claude = dimStyle.Render(" claude ")
		codex = titleStyle.Render("[codex]")
	}
	return claude + "  " + codex
}

func (m model) formInputWidth() int {
	if m.width <= 0 {
		return 72
	}
	return clampInt(m.modalWidth()-8, 24, 92)
}

func (m model) modalWidth() int {
	if m.width <= 0 {
		return 80
	}
	return clampInt(m.width-12, 40, 104)
}

func overlayCentered(base, modal string, width, height int) string {
	if width <= 0 {
		return modal
	}
	baseLines := strings.Split(base, "\n")
	if height <= 0 {
		height = len(baseLines)
	}
	for len(baseLines) < height {
		baseLines = append(baseLines, strings.Repeat(" ", width))
	}

	modalLines := strings.Split(modal, "\n")
	top := max((height-len(modalLines))/2, 0)
	for i, line := range modalLines {
		idx := top + i
		if idx >= len(baseLines) {
			break
		}
		baseLines[idx] = lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return strings.Join(baseLines, "\n")
}

func (m *model) refreshRows() {
	m.allPanes = applyIssueStatuses(m.opts.ProjectRoot, m.allPanes, m.issues)
	m.panes = filterPaneViews(m.allPanes, m.filterQuery)
	rows := make([]table.Row, 0, len(m.panes))
	for _, pane := range m.panes {
		rows = append(rows, pane.tableRow())
	}
	m.table.SetRows(rows)
	m.table.SetCursor(m.table.Cursor())
	m.refreshDetail()
}

func (m *model) refreshDetail() {
	m.detail.SetContent(m.detailContent())
}

func (m model) detailContent() string {
	if len(m.allPanes) == 0 {
		return "No recorded fanout panes in .fanout/state.json."
	}
	if len(m.panes) == 0 {
		return "No panes match the current filter."
	}
	pane, ok := m.selectedPane()
	if !ok {
		return "No panes match the current filter."
	}
	lines := []string{
		fmt.Sprintf("%s %s  %s", pane.Parent, pane.itemLabel(), pane.Name),
		fmt.Sprintf("pane=%s tmux=%s title=%s kind=%s agent=%s", dash(pane.PaneID), pane.TmuxState, dash(pane.TmuxTitle), dash(pane.Kind), dash(pane.Agent)),
		fmt.Sprintf("issue=%s pr=%s ci=%s branch=%s", dash(pane.IssueState), dash(pane.PRSummary), dash(pane.CIStatus), dash(pane.BranchName)),
		fmt.Sprintf("wave=%s blockers=%s", dash(pane.waveText()), dash(pane.Blockers)),
		fmt.Sprintf("worktree=%s diff=%s dirty=%s", dash(pane.WorktreePath), dash(pane.DiffSummary), dash(pane.DirtyState)),
		fmt.Sprintf("created=%s", dash(pane.CreatedAt)),
	}
	if pane.WorktreeErr != "" {
		lines = append(lines, "worktree_error="+pane.WorktreeErr)
	}
	peekBudget := max(2, m.detail.Height-len(lines)-1)
	lines = append(lines, m.peekContent(pane, peekBudget)...)
	if pane.Prompt != "" && len(lines) < m.detail.Height {
		lines = append(lines, "prompt="+pane.Prompt)
	}
	return strings.Join(lines, "\n")
}

func (m model) selectedPane() (paneView, bool) {
	if len(m.panes) == 0 {
		return paneView{}, false
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.panes) {
		idx = 0
	}
	return m.panes[idx], true
}

func (m *model) openSelectedWorktreeShellCmd() tea.Cmd {
	pane, ok := m.selectedPane()
	if !ok {
		m.notice = "no pane selected"
		return nil
	}
	targetPath := pane.absoluteWorktreePath(m.opts.ProjectRoot)
	if targetPath == "" {
		m.notice = fmt.Sprintf("terminal skipped for %s: no worktree path", pane.identityLabel())
		return nil
	}
	return m.launchShellCmd(ShellLaunchRequest{
		TargetPath: targetPath,
		Source:     pane.identityLabel(),
	})
}

func (m *model) openProjectRootShellCmd() tea.Cmd {
	projectRoot := strings.TrimSpace(m.opts.ProjectRoot)
	if projectRoot == "" {
		m.notice = "terminal skipped: project root is unknown"
		return nil
	}
	return m.launchShellCmd(ShellLaunchRequest{
		TargetPath: projectRoot,
		Root:       true,
		Source:     "project root",
	})
}

func (m *model) launchShellCmd(req ShellLaunchRequest) tea.Cmd {
	req.TargetPath = strings.TrimSpace(req.TargetPath)
	if req.TargetPath == "" {
		m.notice = "terminal skipped: target path is empty"
		return nil
	}
	if m.opts.LaunchShell == nil {
		m.notice = "terminal launcher is not configured"
		return nil
	}
	switch {
	case req.Root:
		m.notice = "opening project root terminal..."
	case req.Source != "":
		m.notice = fmt.Sprintf("opening terminal for %s...", req.Source)
	default:
		m.notice = "opening worktree terminal..."
	}
	launch := m.opts.LaunchShell
	return func() tea.Msg {
		return launchShellMsg{req: req, err: launch(req)}
	}
}

func (m *model) focusSelectedCmd() tea.Cmd {
	pane, ok := m.selectedPane()
	if !ok {
		m.notice = "no pane selected"
		return nil
	}
	if !pane.canFocus() {
		m.notice = fmt.Sprintf("focus skipped for %s: tmux state is %s", dash(pane.PaneID), pane.TmuxState)
		return nil
	}

	paneID := pane.PaneID
	alive := m.opts.PaneAlive
	shellAlive := m.opts.ShellPaneAlive
	focus := m.opts.FocusPane
	keyboard := m.opts.keyboard
	m.notice = fmt.Sprintf("focusing %s...", paneID)
	return func() tea.Msg {
		if !paneAliveForAction(pane, alive, shellAlive) {
			return paneFocusedMsg{paneID: paneID, err: errPaneNotAlive}
		}
		keyboard.Disable()
		if err := focus(paneID); err != nil {
			keyboard.Enable()
			return paneFocusedMsg{paneID: paneID, err: err}
		}
		return paneFocusedMsg{paneID: paneID, keyboardPaused: true}
	}
}

func (m *model) resumeKeyboardProtocols() {
	if !m.keyboardPaused {
		return
	}
	m.opts.keyboard.Enable()
	m.keyboardPaused = false
}

func (m *model) peekSelectedCmd(force bool) tea.Cmd {
	pane, ok := m.selectedPane()
	if !ok {
		m.peek = panePeek{}
		return nil
	}
	if !pane.canPeek() {
		m.peek = panePeek{PaneID: pane.PaneID, At: time.Now(), Err: fmt.Sprintf("tmux state is %s", pane.TmuxState)}
		return nil
	}
	if !force && m.peek.PaneID == pane.PaneID && (m.peek.Loading || m.peek.Output != "" || m.peek.Err != "") {
		return nil
	}

	paneID := pane.PaneID
	shellAlive := m.opts.ShellPaneAlive
	capture := m.opts.CapturePaneOutput
	m.peek = panePeek{PaneID: paneID, Loading: true}
	return func() tea.Msg {
		if pane.isShell() && !shellAlive(pane.PaneID, pane.ShellKey) {
			return panePeekLoadedMsg{paneID: paneID, at: time.Now(), err: errPaneNotAlive}
		}
		out, err := capture(paneID, peekLines)
		return panePeekLoadedMsg{paneID: paneID, output: out, at: time.Now(), err: err}
	}
}

func (m model) peekContent(pane paneView, maxLines int) []string {
	header := "peek"
	if !m.peek.At.IsZero() {
		header += " " + formatClock(m.peek.At)
	}
	if pane.PaneID == "" {
		return []string{header + ": no pane id recorded"}
	}
	if !pane.canPeek() {
		return []string{header + ": unavailable (" + pane.TmuxState + ")"}
	}
	if m.peek.PaneID != pane.PaneID {
		return []string{header + ": waiting for capture"}
	}
	if m.peek.Loading {
		return []string{header + ": loading..."}
	}
	if m.peek.Err != "" {
		return []string{header + ": " + m.peek.Err}
	}

	out := strings.TrimRight(m.peek.Output, "\r\n")
	if out == "" {
		return []string{header + ": no output"}
	}
	lines := []string{header + ":"}
	for _, line := range tailLines(out, maxLines) {
		lines = append(lines, truncatePreserveSpace(line, max(20, m.detail.Width-2)))
	}
	return lines
}

func (m *model) markPaneStale(paneID string) {
	for i := range m.allPanes {
		if m.allPanes[i].PaneID == paneID {
			m.allPanes[i].TmuxState = "stale"
			break
		}
	}
	for i := range m.panes {
		if m.panes[i].PaneID == paneID {
			m.panes[i].TmuxState = "stale"
			return
		}
	}
}

func (m model) loadStateCmd(scheduleNext bool) tea.Cmd {
	projectRoot := m.opts.ProjectRoot
	issues := cloneIssueStatuses(m.issues)
	return func() tea.Msg {
		panes, err := loadPaneViews(projectRoot, issues)
		return stateLoadedMsg{panes: panes, at: time.Now(), err: err, scheduleNext: scheduleNext}
	}
}

func (m model) loadGHCmd(scheduleNext bool) tea.Cmd {
	projectRoot := m.opts.ProjectRoot
	return func() tea.Msg {
		issues, err := loadIssueStatuses(projectRoot)
		return ghLoadedMsg{issues: issues, at: time.Now(), err: err, scheduleNext: scheduleNext}
	}
}

func (m model) watchTickCmd() tea.Cmd {
	if m.opts.Watcher == nil {
		return nil
	}
	interval := m.opts.WatchInterval
	return tea.Tick(interval, func(t time.Time) tea.Msg { return watchTickMsg(t) })
}

func (m model) runWatchCmd() tea.Cmd {
	runner := m.opts.Watcher
	if runner == nil {
		return nil
	}
	return func() tea.Msg {
		report, err := runner.RunCycle()
		return watchDoneMsg{report: report, at: time.Now(), err: err}
	}
}

func (m model) notifyEventsCmd(events []fanoutnotify.Event) tea.Cmd {
	if len(events) == 0 || m.opts.Notifier == nil {
		return nil
	}
	notifier := m.opts.Notifier
	events = append([]fanoutnotify.Event(nil), events...)
	return func() tea.Msg {
		return transitionNotifiedMsg{count: len(events), err: notifier.Notify(events)}
	}
}

func loadPaneViews(projectRoot string, issues map[issueKey]issueStatus) ([]paneView, error) {
	var stateErr error
	mergedState := sessionview.MergedStateLoader(projectRoot)
	loadState := func() (state.Store, error) {
		store, err := mergedState()
		stateErr = err
		return store, err
	}
	var tmuxErr error
	livePanes := sessionview.LivePanes()
	live := func() (map[string]sessionview.LivePaneInfo, error) {
		panes, err := livePanes()
		tmuxErr = err
		return panes, err
	}
	snap := sessionview.Build("", projectRoot, sessionview.Collectors{
		LoadState:    loadState,
		LivePanes:    live,
		IssuePRs:     issuePRCollector(issues),
		Waves:        waveCollector(issues),
		WorktreeStat: sessionview.GitWorktreeStat(projectRoot),
		Now:          time.Now,
	})
	return paneViewsFromSnapshot(projectRoot, snap), errors.Join(stateErr, tmuxErr)
}

func loadIssueStatuses(projectRoot string) (map[issueKey]issueStatus, error) {
	// Merge sibling worktrees so issue/PR/wave status is fetched for Sessions
	// fanned out from another worktree too, matching loadPaneViews.
	store, err := sessionview.MergedStateLoader(projectRoot)()
	if err != nil {
		return nil, err
	}
	parents := recordedParents(store.Panes)
	taskRows := recordedTaskRows(store.Panes)
	statuses := map[issueKey]issueStatus{}
	if len(parents) == 0 && len(taskRows) == 0 {
		return statuses, nil
	}

	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		return nil, fmt.Errorf("resolve repo: %w", err)
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("unexpected repo nameWithOwner: %s", nwo)
	}

	prCache := map[int]issueStatus{}
	branchCache := map[string]branchStatus{}
	var loadErr error
	for _, parent := range parents {
		graph, err := sessionview.FetchWaveGraph(gh, parent, recordedIssueNums(store.PanesForParent(parent)))
		loadErr = errors.Join(loadErr, err)
		for _, issue := range graph.Children {
			key := keyForIssue(parent, issue.Number)
			cached, ok := prCache[issue.Number]
			if !ok {
				stateName, prs, err := gh.IssueWithPRs(owner, repo, issue.Number)
				if err != nil {
					// Accumulate and skip, like the branch/wave paths: a single
					// sibling-worktree issue that was deleted/made private or hit a
					// transient gh error must not blank every other row's status.
					loadErr = errors.Join(loadErr, fmt.Errorf("#%d: %w", issue.Number, err))
					continue
				}
				cached = issueStatus{State: stateName, PRs: prs}
				prCache[issue.Number] = cached
			}
			if cached.State == "" {
				cached.State = issue.State
			}
			info := graph.Info[issue.Number]
			cached.Title = issue.Title
			cached.Wave = info.Wave
			cached.WaveLabel = info.WaveLabel
			cached.Blockers = blockers.FormatStatuses(info.Blockers)
			cached.BlockerRows = info.Blockers
			cached.HasOpenBlockers = info.Blocked
			cached.WaveDegraded = info.Degraded
			statuses[key] = cached
		}
	}
	for _, task := range taskRows {
		cached, ok := branchCache[task.BranchName]
		if !ok {
			prs, err := gh.PRsForBranch(task.BranchName)
			if err != nil {
				loadErr = errors.Join(loadErr, fmt.Errorf("branch %s: %w", task.BranchName, err))
				continue
			}
			cached = branchStatus{PRs: prs}
			branchCache[task.BranchName] = cached
		}
		statuses[task.key] = issueStatus{
			State: sessionview.IssueStateUnknown,
			PRs:   cached.PRs,
		}
	}
	return statuses, loadErr
}

type taskStatusRow struct {
	key        issueKey
	BranchName string
}

type branchStatus struct {
	PRs []ghissue.PRRef
}

func recordedIssueNums(panes []state.Pane) []int {
	nums := make([]int, 0, len(panes))
	for _, pane := range panes {
		if pane.IssueNum > 0 {
			nums = append(nums, pane.IssueNum)
		}
	}
	return nums
}

func issuePRCollector(issues map[issueKey]issueStatus) func(int) (string, []ghissue.PRRef, error) {
	byNum := map[int]issueStatus{}
	for key, status := range issues {
		if key.Num > 0 {
			byNum[key.Num] = status
		}
	}
	return func(num int) (string, []ghissue.PRRef, error) {
		status, ok := byNum[num]
		if !ok {
			return "", nil, nil
		}
		return status.State, status.PRs, nil
	}
}

func waveCollector(issues map[issueKey]issueStatus) func(string) (sessionview.WaveGraph, error) {
	return func(parent string) (sessionview.WaveGraph, error) {
		return waveGraphFromStatuses(parent, issues), nil
	}
}

func waveGraphFromStatuses(parent string, issues map[issueKey]issueStatus) sessionview.WaveGraph {
	parent = sessionview.NormalizeParent(parent)
	graph := sessionview.WaveGraph{Info: map[int]sessionview.WaveInfo{}}
	for key, status := range issues {
		if key.Parent != parent || key.Num <= 0 {
			continue
		}
		graph.Children = append(graph.Children, ghissue.Issue{
			Number: key.Num,
			Title:  status.Title,
			State:  status.State,
		})
		rows := status.BlockerRows
		if rows == nil {
			rows = parseFormattedBlockers(status.Blockers)
		}
		graph.Info[key.Num] = sessionview.WaveInfo{
			Wave:      status.Wave,
			WaveLabel: status.WaveLabel,
			Blockers:  rows,
			Blocked:   status.HasOpenBlockers,
			Degraded:  status.WaveDegraded,
		}
	}
	slices.SortFunc(graph.Children, func(a, b ghissue.Issue) int { return cmp.Compare(a.Number, b.Number) })
	return graph
}

func parseFormattedBlockers(text string) []blockers.Status {
	text = strings.TrimSpace(text)
	if text == "" || text == "-" {
		return nil
	}
	rows := []blockers.Status{}
	for part := range strings.SplitSeq(text, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) < 2 {
			continue
		}
		numText := strings.TrimPrefix(fields[len(fields)-1], "#")
		num, err := strconv.Atoi(numText)
		if err != nil {
			continue
		}
		stateName := fields[0]
		if stateName == "resolved" {
			stateName = "CLOSED"
		}
		rows = append(rows, blockers.Status{Num: num, State: strings.ToUpper(stateName)})
	}
	return rows
}

func detectIssueTransitions(previous map[issueKey]issueTransitionSnapshot, current map[issueKey]issueStatus) []fanoutnotify.Event {
	if len(previous) == 0 || len(current) == 0 {
		return nil
	}
	keys := sortedIssueKeys(current)
	events := []fanoutnotify.Event{}
	for _, key := range keys {
		if key.TaskID != "" {
			continue
		}
		prev, ok := previous[key]
		if !ok {
			continue
		}
		next := transitionSnapshot(current[key])
		if !prev.HasMerged && next.HasMerged {
			events = append(events, transitionEvent(fanoutnotify.EventMerged, key, next))
		}
		if prev.CIStatus != "fail" && next.CIStatus == "fail" {
			events = append(events, transitionEvent(fanoutnotify.EventCIFailed, key, next))
		}
		if !prev.Waiting && next.Waiting {
			events = append(events, transitionEvent(fanoutnotify.EventWaiting, key, next))
		}
	}
	return events
}

func issueTransitionSnapshots(statuses map[issueKey]issueStatus) map[issueKey]issueTransitionSnapshot {
	out := make(map[issueKey]issueTransitionSnapshot, len(statuses))
	for key, status := range statuses {
		out[key] = transitionSnapshot(status)
	}
	return out
}

func mergePartialIssueTransitionSnapshots(previous map[issueKey]issueTransitionSnapshot, statuses map[issueKey]issueStatus) map[issueKey]issueTransitionSnapshot {
	out := make(map[issueKey]issueTransitionSnapshot, len(previous))
	maps.Copy(out, previous)
	for key, status := range statuses {
		current := transitionSnapshot(status)
		if prev, ok := previous[key]; ok {
			current = mergePartialIssueTransitionSnapshot(prev, current)
		}
		out[key] = current
	}
	return out
}

// mergeDegradedIssueStatuses keeps last-known display data when an errored
// refresh returns a degraded partial snapshot: keys dropped from the partial
// result are restored wholesale from the previous snapshot, and keys present
// in both keep their previous wave/blocker fields when the partial entry lost
// them. New keys pass through unchanged.
func mergeDegradedIssueStatuses(previous, current map[issueKey]issueStatus) map[issueKey]issueStatus {
	out := make(map[issueKey]issueStatus, max(len(previous), len(current)))
	maps.Copy(out, previous)
	for key, status := range current {
		if prev, ok := previous[key]; ok {
			status = mergeDegradedIssueStatus(prev, status)
		}
		out[key] = status
	}
	return out
}

// mergeDegradedIssueStatus restores the previous wave/blocker display fields
// when a degraded refresh dropped them (failed child-body hydration renders a
// still-blocked child with "-" blockers and no blocked badge).
func mergeDegradedIssueStatus(previous, current issueStatus) issueStatus {
	// Restore only rows whose wave/blocker inputs actually failed this
	// refresh. A non-degraded row is fresh data — a confirmed "-" (blocker
	// list legitimately removed) must not be masked by stale data just
	// because an unrelated issue errored in the same refresh. A degraded row
	// is restored even when parent-row blockers produced partial text, and
	// even when the previous display was a confirmed unblocked "-": its
	// Wave/WaveLabel are still valid last-known data the degraded refresh
	// would otherwise blank.
	if !current.WaveDegraded {
		return current
	}
	if degradedBlockers(previous.Blockers) && previous.Wave == 0 && previous.WaveLabel == "" {
		return current // previous carries nothing better to preserve
	}
	current.Blockers = previous.Blockers
	current.BlockerRows = previous.BlockerRows
	current.HasOpenBlockers = previous.HasOpenBlockers
	current.Wave = previous.Wave
	current.WaveLabel = previous.WaveLabel
	return current
}

// degradedBlockers reports whether a formatted blockers string carries no
// blocker information ("-" or empty), the signature of a degraded refresh.
func degradedBlockers(s string) bool {
	trimmed := strings.TrimSpace(s)
	return trimmed == "" || trimmed == "-"
}

func mergePartialIssueTransitionSnapshot(previous, current issueTransitionSnapshot) issueTransitionSnapshot {
	if previous.HasMerged && !current.HasMerged {
		current.HasMerged = true
		if current.PRNumber == 0 {
			current.PRNumber = previous.PRNumber
		}
	}
	if previous.Waiting && !current.Waiting && !current.HasMerged && current.State != "CLOSED" && current.Blockers == "-" {
		current.Waiting = true
		current.Blockers = previous.Blockers
	}
	return current
}

func transitionSnapshot(status issueStatus) issueTransitionSnapshot {
	pr, _ := ghissue.PrimaryPR(status.PRs)
	return issueTransitionSnapshot{
		State:     strings.ToUpper(strings.TrimSpace(status.State)),
		HasMerged: hasMergedPR(status.PRs),
		CIStatus:  strings.ToLower(strings.TrimSpace(ghissue.SummarizeCI(status.PRs))),
		Waiting:   status.HasOpenBlockers || strings.EqualFold(status.State, "WAITING"),
		PRNumber:  pr.Number,
		Title:     status.Title,
		Blockers:  status.Blockers,
	}
}

func transitionEvent(kind fanoutnotify.EventKind, key issueKey, snapshot issueTransitionSnapshot) fanoutnotify.Event {
	return fanoutnotify.Event{
		Kind:     kind,
		Parent:   key.Parent,
		IssueNum: key.Num,
		Title:    snapshot.Title,
		PRNumber: snapshot.PRNumber,
		CIStatus: snapshot.CIStatus,
		Blockers: snapshot.Blockers,
	}
}

func sortedIssueKeys(statuses map[issueKey]issueStatus) []issueKey {
	keys := make([]issueKey, 0, len(statuses))
	for key := range statuses {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b issueKey) int {
		if c := cmp.Compare(a.Parent, b.Parent); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Num, b.Num); c != 0 {
			return c
		}
		return cmp.Compare(a.TaskID, b.TaskID)
	})
	return keys
}

func transitionNotice(events []fanoutnotify.Event) string {
	if len(events) == 0 {
		return ""
	}
	if len(events) == 1 {
		return events[0].Message()
	}
	return fmt.Sprintf("%d state changes: %s", len(events), events[0].Message())
}

func recordedParents(panes []state.Pane) []string {
	seen := map[string]bool{}
	for _, pane := range panes {
		if pane.Parent == "" || pane.IssueNum <= 0 {
			continue
		}
		seen[normalizedParent(pane.Parent)] = true
	}
	parents := make([]string, 0, len(seen))
	for parent := range seen {
		parents = append(parents, parent)
	}
	slices.SortFunc(parents, func(a, b string) int {
		left, leftErr := strconv.Atoi(a)
		right, rightErr := strconv.Atoi(b)
		if leftErr == nil && rightErr == nil {
			return cmp.Compare(left, right)
		}
		return strings.Compare(a, b)
	})
	return parents
}

func recordedTaskRows(panes []state.Pane) []taskStatusRow {
	seen := map[issueKey]bool{}
	var rows []taskStatusRow
	for _, pane := range panes {
		branch := strings.TrimSpace(pane.BranchName)
		taskID := strings.TrimSpace(pane.TaskID)
		if pane.Parent == "" || pane.IssueNum > 0 || taskID == "" || branch == "" {
			continue
		}
		key := keyForTask(pane.Parent, taskID, pane.SourceProjectRoot)
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, taskStatusRow{key: key, BranchName: branch})
	}
	slices.SortFunc(rows, func(a, b taskStatusRow) int {
		if c := cmp.Compare(a.key.Parent, b.key.Parent); c != 0 {
			return c
		}
		if c := cmp.Compare(a.key.TaskID, b.key.TaskID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.key.Source, b.key.Source); c != 0 {
			return c
		}
		return cmp.Compare(a.BranchName, b.BranchName)
	})
	return rows
}

func keyForIssue(parent string, num int) issueKey {
	return issueKey{Parent: normalizedParent(parent), Num: num}
}

func keyForPaneView(pane paneView) issueKey {
	taskID := strings.TrimSpace(pane.TaskID)
	switch {
	case pane.IssueNum > 0:
		// GitHub issue: globally stable, aggregates across worktrees.
		return keyForIssue(pane.Parent, pane.IssueNum)
	case taskID != "":
		return keyForTask(pane.Parent, taskID, pane.sourceProjectRoot)
	default:
		// Manual/synthetic (@manual, non-positive issueNum): worktree-local, so
		// scope by source to keep two worktrees' rows distinct.
		return issueKey{Parent: normalizedParent(pane.Parent), Num: pane.IssueNum, Source: pane.sourceProjectRoot}
	}
}

func keyForTask(parent, taskID, source string) issueKey {
	return issueKey{Parent: normalizedParent(parent), Num: 0, TaskID: strings.TrimSpace(taskID), Source: source}
}

func normalizedParent(parent string) string {
	parentNum, err := strconv.Atoi(parent)
	if err != nil {
		return parent
	}
	return strconv.Itoa(parentNum)
}

func buildPaneViews(panes []state.Pane, tmuxPanes []tmuxrun.PaneInfo, tmuxKnown bool, issues map[issueKey]issueStatus, worktrees map[string]worktreeStatView) []paneView {
	const projectRoot = "/repo"
	live := map[string]sessionview.LivePaneInfo{}
	for _, pane := range tmuxPanes {
		live[pane.ID] = sessionview.LivePaneInfo{Path: matchingWorktreePath(pane.ID, panes), Title: pane.Title}
	}
	liveCollector := func() (map[string]sessionview.LivePaneInfo, error) {
		if !tmuxKnown {
			return nil, errors.New("tmux unavailable")
		}
		return live, nil
	}
	worktreeCollector := func(path, baseRef string) (sessionview.WorktreeStat, error) {
		if stat, ok := worktrees[path]; ok {
			return sessionview.WorktreeStat{DiffSummary: stat.Diff, DirtyState: stat.Dirty}, errFromString(stat.Err)
		}
		return sessionview.WorktreeStat{DiffSummary: "-", DirtyState: "unknown"}, nil
	}
	snap := sessionview.Build("", projectRoot, sessionview.Collectors{
		LoadState:    func() (state.Store, error) { return state.Store{SchemaVersion: 1, Panes: panes}, nil },
		LivePanes:    liveCollector,
		IssuePRs:     issuePRCollector(issues),
		Waves:        waveCollector(issues),
		WorktreeStat: worktreeCollector,
		Now:          time.Now,
	})
	return applyIssueStatuses(projectRoot, paneViewsFromSnapshot(projectRoot, snap), issues)
}

func matchingWorktreePath(paneID string, panes []state.Pane) string {
	for _, pane := range panes {
		if pane.PaneID == paneID {
			return pane.WorktreePath
		}
	}
	return ""
}

func errFromString(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return errors.New(s)
}

func paneAliveForAction(pane paneView, paneAlive func(string) bool, shellPaneAlive func(string, string) bool) bool {
	if pane.isShell() {
		return shellPaneAlive(pane.PaneID, pane.ShellKey)
	}
	return paneAlive(pane.PaneID)
}

func shellPaneAliveByKey(paneID, shellKey string) bool {
	paneID = strings.TrimSpace(paneID)
	shellKey = strings.TrimSpace(shellKey)
	if paneID == "" || shellKey == "" {
		return false
	}
	panes, err := tmuxrun.ListLivePanes()
	if err != nil {
		return false
	}
	for _, pane := range panes {
		if pane.ID == paneID && pane.ShellKey == shellKey {
			return true
		}
	}
	return false
}

func paneViewsFromSnapshot(projectRoot string, snap sessionview.Snapshot) []paneView {
	out := []paneView{}
	for _, session := range snap.Sessions {
		parent := session.Parent
		for _, pv := range session.Panes {
			derived := pv.Derived
			if derived.Name == "" && derived.PRSummary == "" {
				derived = sessionview.DerivePane(projectRoot, parent, pv)
			}
			out = append(out, paneView{
				Parent:             parent,
				IssueNum:           pv.IssueNum,
				TaskID:             pv.TaskID,
				Kind:               pv.Kind,
				Name:               derived.Name,
				PaneID:             pv.PaneID,
				ShellKey:           pv.ShellKey,
				TmuxState:          pv.TmuxState,
				TmuxTitle:          pv.TmuxTitle,
				AgentState:         pv.AgentState,
				IssueState:         dash(pv.IssueState),
				PRSummary:          dash(derived.PRSummary),
				HasMergedPR:        pv.HasMergedPR,
				Wave:               pv.Wave,
				WaveLabel:          pv.WaveLabel,
				WaveBadge:          derived.WaveBadge,
				Blockers:           dash(derived.BlockersText),
				Blocked:            pv.Blocked,
				CIStatus:           dash(pv.CIStatus),
				DiffSummary:        pv.DiffSummary,
				DirtyState:         pv.DirtyState,
				WorktreeErr:        pv.WorktreeErr,
				BranchName:         pv.BranchName,
				WorktreePath:       firstNonEmpty(derived.WorktreeRelative, sessionview.RelativePath(projectRoot, pv.WorktreePath)),
				worktreeAbs:        pv.WorktreePath,
				sourceProjectRoot:  pv.SourceProjectRoot,
				sourceProjectRoots: pv.SourceProjectRoots,
				Agent:              pv.Agent,
				CreatedAt:          pv.CreatedAt,
				Prompt:             pv.Prompt,
				Derived:            derived,
			})
		}
	}
	return out
}

func applyIssueStatuses(projectRoot string, panes []paneView, issues map[issueKey]issueStatus) []paneView {
	out := slices.Clone(panes)
	seen := map[issueKey]bool{}
	for i := range out {
		key := keyForPaneView(out[i])
		seen[key] = true
		if status, ok := issues[key]; ok {
			out[i].IssueState = dash(status.State)
			out[i].PRSummary = summarizePRs(status.PRs)
			out[i].HasMergedPR = hasMergedPR(status.PRs)
			out[i].Wave = status.Wave
			if status.WaveLabel != "" {
				out[i].WaveLabel = status.WaveLabel
			}
			out[i].WaveBadge = waveBadge(status.Wave, status.HasOpenBlockers)
			out[i].Blockers = dash(status.Blockers)
			out[i].Blocked = status.HasOpenBlockers
			out[i].CIStatus = ghissue.SummarizeCI(status.PRs)
			out[i].Derived = derivePaneView(projectRoot, out[i], status.PRs, status.BlockerRows)
		}
	}
	for key, status := range issues {
		if seen[key] || key.TaskID != "" || key.Num <= 0 {
			continue
		}
		view := paneView{
			Parent:      key.Parent,
			IssueNum:    key.Num,
			Name:        issueTitle(status, key.Num),
			TmuxState:   syntheticTmuxState(status),
			IssueState:  dash(status.State),
			PRSummary:   summarizePRs(status.PRs),
			HasMergedPR: hasMergedPR(status.PRs),
			CIStatus:    ghissue.SummarizeCI(status.PRs),
			Wave:        status.Wave,
			WaveLabel:   status.WaveLabel,
			WaveBadge:   waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:    dash(status.Blockers),
			Blocked:     status.HasOpenBlockers,
		}
		view.Derived = derivePaneView(projectRoot, view, status.PRs, status.BlockerRows)
		out = append(out, view)
	}
	sortPaneViews(out)
	return out
}

func derivePaneView(projectRoot string, view paneView, prs []ghissue.PRRef, blockerRows []blockers.Status) sessionview.PaneDerived {
	if blockerRows == nil {
		blockerRows = parseFormattedBlockers(view.Blockers)
	}
	return sessionview.DerivePane(projectRoot, view.Parent, sessionview.PaneView{
		IssueNum:     view.IssueNum,
		TaskID:       view.TaskID,
		Kind:         view.Kind,
		DisplayName:  view.Name,
		Agent:        view.Agent,
		BranchName:   view.BranchName,
		PaneID:       view.PaneID,
		ShellKey:     view.ShellKey,
		WorktreePath: view.WorktreePath,
		CreatedAt:    view.CreatedAt,
		Alive:        view.TmuxState == "live",
		IssueState:   view.IssueState,
		PRs:          prs,
		HasMergedPR:  view.HasMergedPR,
		DiffSummary:  view.DiffSummary,
		DirtyState:   view.DirtyState,
		WorktreeErr:  view.WorktreeErr,
		TmuxState:    view.TmuxState,
		TmuxTitle:    view.TmuxTitle,
		AgentState:   view.AgentState,
		Prompt:       view.Prompt,
		CIStatus:     strings.ToLower(strings.TrimSpace(view.CIStatus)),
		Wave:         view.Wave,
		WaveLabel:    view.WaveLabel,
		Blockers:     blockerRows,
		Blocked:      view.Blocked,
	})
}

func sortPaneViews(panes []paneView) {
	slices.SortFunc(panes, func(a, b paneView) int {
		if c := cmp.Compare(a.Parent, b.Parent); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Wave, b.Wave); c != 0 {
			return c
		}
		if c := cmp.Compare(a.IssueNum, b.IssueNum); c != 0 {
			return c
		}
		return strings.Compare(a.itemLabel(), b.itemLabel())
	})
}

func syntheticTmuxState(status issueStatus) string {
	// web ダッシュボードの synthetic 行と同一文字列を保証する単一実装に委譲
	// (`state:queued` 等のフィルタ語彙が TUI / web で割れないように)。
	return sessionview.SyntheticTmuxState(status.State, status.HasOpenBlockers)
}

func (p paneView) tableRow() table.Row {
	tmuxState := p.TmuxState
	if tmuxState == "stale" {
		tmuxState = "stale!"
	}
	return table.Row{
		compactParent(p.Parent),
		p.itemLabel(),
		truncate(dash(p.waveCell()), 12),
		truncate(dash(p.Blockers), 22),
		truncate(p.Name, 28),
		dash(p.Agent),
		tmuxState,
		dash(p.IssueState),
		truncate(dash(p.PRSummary), 12),
		truncate(dash(p.CIStatus), 7),
		dash(p.DiffSummary),
		dash(p.DirtyState),
		truncate(dash(p.BranchName), 18),
		dash(p.PaneID),
	}
}

func (p paneView) canFocus() bool {
	return strings.TrimSpace(p.PaneID) != "" && p.TmuxState != "stale" && p.TmuxState != "-"
}

func (p paneView) canPeek() bool {
	return p.canFocus()
}

func (p paneView) isShell() bool {
	return p.Kind == state.PaneKindShell
}

func (p paneView) absoluteWorktreePath(projectRoot string) string {
	path := strings.TrimSpace(firstNonEmpty(p.worktreeAbs, p.WorktreePath))
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return path
	}
	return filepath.Join(projectRoot, path)
}

func columnsForWidth(width int) []table.Column {
	nameWidth := clampInt(width/7, 12, 20)
	blockerWidth := clampInt(width/8, 10, 16)
	branchWidth := clampInt(width/8, 10, 16)
	return []table.Column{
		{Title: "PARENT", Width: 10},
		{Title: "ISSUE", Width: 7},
		{Title: "WAVE", Width: 12},
		{Title: "BLOCKERS", Width: blockerWidth},
		{Title: "NAME", Width: nameWidth},
		{Title: "AGENT", Width: 7},
		{Title: "TMUX", Width: 7},
		{Title: "STATE", Width: 8},
		{Title: "PR", Width: 12},
		{Title: "CI", Width: 7},
		{Title: "DIFF", Width: 8},
		{Title: "DIRTY", Width: 7},
		{Title: "BRANCH", Width: branchWidth},
		{Title: "PANE", Width: 7},
	}
}

func (m model) footerText() string {
	parts := []string{"q quit", "n new", "A terminal", "t root", "j/k move", "[/] session", "/ filter", "enter/o focus", "p peek", "c close", "m merge", "x cleanup"}
	if m.filterEditing {
		parts = append(parts, "typing")
	}
	if m.filterEditing || strings.TrimSpace(m.filterQuery) != "" {
		parts = append(parts, fmt.Sprintf("filter=%q %d/%d", m.filterQuery, len(m.panes), len(m.allPanes)))
	}
	if watchText := m.watchFooterText(); watchText != "" {
		parts = append(parts, watchText)
	}
	parts = append(parts, "state "+formatClock(m.lastState), "gh "+formatClock(m.lastGH))
	return strings.Join(parts, "  ")
}

func (m model) watchFooterText() string {
	if m.opts.Watcher == nil {
		return ""
	}
	status := "on"
	if m.watchDisabled {
		status = "disabled"
	}
	parts := []string{
		"watch: " + status,
		"label=" + m.opts.WatchLabel,
	}
	if m.watchRunning {
		parts = append(parts, "running")
	}
	parts = append(parts,
		"last="+formatClock(m.lastWatch),
		fmt.Sprintf("launched=%d", m.watchLaunched),
		"err="+dash(truncate(m.watchErr, 120)),
	)
	return strings.Join(parts, " ")
}

func watchReportDisabled(report watch.Report) bool {
	for _, failure := range report.Failures {
		if failure.Disabled {
			return true
		}
	}
	for _, skip := range report.Skipped {
		if skip.Reason == watch.SkipDisabled {
			return true
		}
	}
	return false
}

func summarizeWatchError(report watch.Report, err error) string {
	if err != nil {
		return err.Error()
	}
	for _, failure := range slices.Backward(report.Failures) {
		if failure.Err == nil && failure.RevertErr == nil {
			continue
		}
		stage := strings.TrimSpace(string(failure.Stage))
		if stage == "" {
			stage = "watch"
		}
		prefix := stage
		if failure.Issue.Number > 0 {
			prefix = fmt.Sprintf("#%d %s", failure.Issue.Number, stage)
		}
		parts := []string{}
		if failure.Err != nil {
			parts = append(parts, failure.Err.Error())
		}
		if failure.RevertErr != nil {
			parts = append(parts, "revert: "+failure.RevertErr.Error())
		}
		if failure.Disabled {
			parts = append(parts, "disabled")
		}
		return prefix + ": " + strings.Join(parts, "; ")
	}
	return ""
}

func filterPaneViews(panes []paneView, query string) []paneView {
	filter := parsePaneFilter(query)
	if filter.empty() {
		return panes
	}
	out := make([]paneView, 0, len(panes))
	for _, pane := range panes {
		if pane.matchesFilter(filter) {
			out = append(out, pane)
		}
	}
	return out
}

func parsePaneFilter(query string) paneFilter {
	var filter paneFilter
	for token := range strings.FieldsSeq(strings.ToLower(strings.TrimSpace(query))) {
		key, value, ok := splitFilterToken(token)
		if !ok {
			filter.terms = append(filter.terms, token)
			continue
		}
		switch key {
		case "state", "status", "s":
			filter.states = append(filter.states, value)
		case "agent", "a":
			filter.agents = append(filter.agents, value)
		case "wave", "w":
			filter.waves = append(filter.waves, value)
		case "run":
			filter.runs = append(filter.runs, value)
		case "ci":
			filter.cis = append(filter.cis, value)
		case "dirty":
			filter.dirty = append(filter.dirty, value)
		case "live":
			filter.live = append(filter.live, value)
		case "issue":
			filter.issues = append(filter.issues, value)
		case "pr":
			filter.prs = append(filter.prs, value)
		case "task", "t":
			filter.tasks = append(filter.tasks, value)
		default:
			filter.terms = append(filter.terms, token)
		}
	}
	return filter
}

func splitFilterToken(token string) (string, string, bool) {
	idx := strings.IndexAny(token, ":=")
	if idx <= 0 || idx == len(token)-1 {
		return "", "", false
	}
	key := strings.TrimSpace(token[:idx])
	value := strings.TrimSpace(token[idx+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func (f paneFilter) empty() bool {
	return len(f.terms) == 0 && len(f.states) == 0 && len(f.agents) == 0 && len(f.waves) == 0 &&
		len(f.runs) == 0 && len(f.cis) == 0 && len(f.dirty) == 0 && len(f.live) == 0 &&
		len(f.issues) == 0 && len(f.prs) == 0 && len(f.tasks) == 0
}

func (p paneView) matchesFilter(filter paneFilter) bool {
	for _, state := range filter.states {
		if !containsFold(state, p.IssueState, p.TmuxState, p.PRSummary) {
			return false
		}
	}
	for _, agent := range filter.agents {
		if !containsFold(agent, p.Agent) {
			return false
		}
	}
	for _, wave := range filter.waves {
		if !containsFold(wave, p.WaveLabel, p.WaveBadge, p.waveCell(), p.dependencyWaveText()) {
			return false
		}
	}
	for _, run := range filter.runs {
		if !equalFold(run, p.AgentState) {
			return false
		}
	}
	for _, ci := range filter.cis {
		if !equalFold(ci, p.paneCI()) {
			return false
		}
	}
	for _, dirty := range filter.dirty {
		if !equalFold(dirty, yesNo(p.DirtyState == "dirty")) {
			return false
		}
	}
	for _, live := range filter.live {
		if !equalFold(live, yesNo(p.TmuxState == "live")) {
			return false
		}
	}
	for _, issue := range filter.issues {
		value := strings.TrimSpace(p.Derived.FilterValues["issue"])
		if value == "" {
			value = strconv.Itoa(p.IssueNum)
			if p.IssueNum <= 0 && strings.TrimSpace(p.TaskID) != "" {
				value = strings.ToLower(strings.TrimSpace(p.TaskID))
			}
		}
		if !equalFold(issue, value) {
			return false
		}
	}
	for _, pr := range filter.prs {
		if !equalFold(pr, p.primaryPRState()) {
			return false
		}
	}
	for _, task := range filter.tasks {
		if !containsFold(task, p.TaskID) {
			return false
		}
	}
	searchText := strings.ToLower(strings.Join([]string{
		p.Parent,
		p.TaskID,
		p.Kind,
		p.itemLabel(),
		"#" + strconv.Itoa(p.IssueNum),
		strconv.Itoa(p.IssueNum),
		p.TaskID,
		p.Name,
		p.PaneID,
		p.TmuxState,
		p.TmuxTitle,
		p.AgentState,
		p.IssueState,
		p.PRSummary,
		p.CIStatus,
		p.BranchName,
		p.WorktreePath,
		p.DiffSummary,
		p.DirtyState,
		p.WorktreeErr,
		p.Agent,
		p.WaveLabel,
		p.WaveBadge,
		p.Blockers,
		p.dependencyWaveText(),
		p.CreatedAt,
		p.Prompt,
	}, "\n"))
	if strings.TrimSpace(p.Derived.FilterText) != "" {
		searchText += "\n" + p.Derived.FilterText
	}
	for _, term := range filter.terms {
		if !strings.Contains(searchText, term) {
			return false
		}
	}
	return true
}

func (p paneView) paneCI() string {
	if strings.TrimSpace(p.Derived.CI) != "" {
		return p.Derived.CI
	}
	if p.CIStatus == "-" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(p.CIStatus))
}

func (p paneView) primaryPRState() string {
	if strings.TrimSpace(p.Derived.PrimaryPRState) != "" {
		return p.Derived.PrimaryPRState
	}
	fields := strings.Fields(p.PRSummary)
	if len(fields) >= 2 {
		return strings.ToLower(fields[1])
	}
	return "none"
}

func equalFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func containsFold(needle string, values ...string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return true
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func waveBadge(wave int, blocked bool) string {
	if wave <= 0 {
		return "-"
	}
	state := "ready"
	if blocked {
		state = "blocked"
	}
	return fmt.Sprintf("W%d %s", wave, state)
}

func (p paneView) waveCell() string {
	if strings.TrimSpace(p.WaveLabel) != "" {
		return p.WaveLabel
	}
	return p.WaveBadge
}

func (p paneView) waveText() string {
	parts := nonDashStrings(p.WaveLabel, p.WaveBadge)
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func (p paneView) dependencyWaveText() string {
	if p.Wave <= 0 {
		return ""
	}
	return fmt.Sprintf("wave%d", p.Wave)
}

func (p paneView) isTask() bool {
	return strings.TrimSpace(p.TaskID) != ""
}

func (p paneView) identityLabel() string {
	if p.isTask() {
		return p.TaskID
	}
	if p.isShell() {
		if label := strings.TrimSpace(firstNonEmpty(p.Name, p.Derived.Name, p.TmuxTitle)); label != "" && label != "-" {
			return label
		}
		if slug := strings.TrimSpace(p.Derived.Name); slug != "" && slug != "-" {
			return slug
		}
		return "shell"
	}
	return "#" + strconv.Itoa(p.IssueNum)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nonDashStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "-" || seen[value] {
			continue
		}
		out = append(out, value)
		seen[value] = true
	}
	return out
}

func issueTitle(status issueStatus, num int) string {
	if strings.TrimSpace(status.Title) != "" {
		return status.Title
	}
	return "#" + strconv.Itoa(num)
}

func summarizePRs(prs []ghissue.PRRef) string {
	pr, ok := ghissue.PrimaryPR(prs)
	if !ok {
		return "-"
	}
	return "#" + strconv.Itoa(pr.Number) + " " + dash(pr.DisplayState())
}

func hasMergedPR(prs []ghissue.PRRef) bool {
	for _, pr := range prs {
		if pr.State == "MERGED" {
			return true
		}
	}
	return false
}

func (p paneView) itemLabel() string {
	if strings.TrimSpace(p.TaskID) != "" {
		return p.TaskID
	}
	if p.isShell() {
		return "shell"
	}
	return "#" + strconv.Itoa(p.IssueNum)
}

func compactParent(parent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return "-"
	}
	if n, ok := strings.CutPrefix(parent, "https://github.com/"); ok {
		parts := strings.Split(strings.Trim(n, "/"), "/")
		if len(parts) >= 4 && parts[2] == "projects" {
			return "proj/" + parts[3]
		}
	}
	return truncate(parent, 10)
}

func cloneIssueStatuses(in map[issueKey]issueStatus) map[issueKey]issueStatus {
	out := make(map[issueKey]issueStatus, len(in))
	maps.Copy(out, in)
	return out
}

func summarizeHUD(panes []paneView) hudSummary {
	summary := hudSummary{}
	for _, pane := range panes {
		if pane.isShell() {
			continue
		}
		summary.Total++
		if pane.HasMergedPR {
			summary.Merged++
		}
		if pane.Blocked {
			summary.Blocked++
		}
	}
	summary.Pending = summary.Total - summary.Merged
	return summary
}

func formatHUD(summary hudSummary) string {
	return fmt.Sprintf("total=%d merged=%d pending=%d blocked=%d", summary.Total, summary.Merged, summary.Pending, summary.Blocked)
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.Format("15:04:05")
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	return truncateRunes(s, maxLen)
}

func truncatePreserveSpace(s string, maxLen int) string {
	return truncateRunes(s, maxLen)
}

func fixedLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncateRunes(s, width)
	if pad := width - len([]rune(s)); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if maxLen <= 0 || len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[:maxLen])
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func trimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}

func compactMessage(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return truncate(strings.Join(fields, " "), 160)
}

func tailLines(s string, maxLen int) []string {
	if maxLen <= 0 {
		return nil
	}
	raw := strings.Split(s, "\n")
	if len(raw) > maxLen {
		raw = raw[len(raw)-maxLen:]
	}
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		out = append(out, strings.TrimRight(line, "\r"))
	}
	return out
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}
