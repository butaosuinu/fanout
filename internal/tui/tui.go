// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/gitstat"
	"github.com/butaosuinu/fanout/internal/lifecycle"
	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

const (
	defaultStateInterval = 2 * time.Second
	defaultGHInterval    = 20 * time.Second
	detailHeight         = 13
	peekLines            = 80
	defaultLaunchAgent   = "claude"
)

// Options configures the TUI monitor.
type Options struct {
	ProjectRoot       string
	Session           string
	StateInterval     time.Duration
	GHInterval        time.Duration
	DefaultAgent      string
	LaunchPane        LaunchFunc
	FocusPane         func(string) error
	PaneAlive         func(string) bool
	CapturePaneOutput func(string, int) (string, error)
	Notifier          transitionNotifier
	lifecycle         lifecycleRunner
}

type issueKey struct {
	Parent string
	Num    int
}

// LaunchRequest describes one manual pane launch requested from the TUI.
type LaunchRequest struct {
	Prompt string
	Agent  string
	Slug   string
}

// LaunchFunc creates a manual fanout pane for a TUI request.
type LaunchFunc func(LaunchRequest) error

type issueStatus struct {
	Title           string
	State           string
	PRs             []ghissue.PRRef
	Wave            int
	WaveLabel       string
	Blockers        string
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
	Name         string
	PaneID       string
	TmuxState    string
	TmuxTitle    string
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
	Agent        string
	CreatedAt    string
	Prompt       string
}

type hudSummary struct {
	Total   int
	Merged  int
	Pending int
	Blocked int
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
	prompt    textinput.Model
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
	notice          string
	newPane         newPaneForm
	peek            panePeek
	pendingAction   *pendingLifecycleAction
	actionRunning   bool
	quitAfterAction bool
	actionMessage   string
	notifications   map[issueKey]issueTransitionSnapshot
	notifyPrimed    bool
}

type paneFilter struct {
	terms  []string
	states []string
	agents []string
	waves  []string
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
	paneID string
	err    error
}

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
	launchPaneMsg struct {
		err error
	}
)

type transitionNotifiedMsg struct {
	count int
	err   error
}

var errPaneNotAlive = errors.New("pane is no longer live")

type lifecycleRunner interface {
	Close(lifecycle.Options, string, int, lifecycle.Logger) exitcode.Code
	Merge(lifecycle.Options, string, int, lifecycle.Logger) exitcode.Code
	Cleanup(lifecycle.Options, string, lifecycle.Logger) exitcode.Code
}

type defaultLifecycleRunner struct{}

func (defaultLifecycleRunner) Close(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Close(opts, parent, issueNum, lg)
}

func (defaultLifecycleRunner) Merge(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Merge(opts, parent, issueNum, lg)
}

func (defaultLifecycleRunner) Cleanup(opts lifecycle.Options, parent string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Cleanup(opts, parent, lg)
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

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("34"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("31"))
	panelStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), true, false, false, false).BorderForeground(lipgloss.Color("240"))
	formStyle  = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(1, 2).BorderForeground(lipgloss.Color("240"))
)

// Run starts the Bubble Tea TUI.
func Run(opts Options) error {
	opts = normalizeOptions(opts)
	m := newModel(opts)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func normalizeOptions(opts Options) Options {
	if opts.StateInterval <= 0 {
		opts.StateInterval = defaultStateInterval
	}
	if opts.GHInterval <= 0 {
		opts.GHInterval = defaultGHInterval
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
	if opts.CapturePaneOutput == nil {
		opts.CapturePaneOutput = tmuxrun.CapturePaneOutput
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
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("34"))
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("32"))
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
	return tea.Batch(m.loadStateCmd(true), m.loadGHCmd(true))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case tea.KeyMsg:
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
			return m, tea.Quit
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
		case "n":
			m.openNewPaneForm()
			return m, nil
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
			return m, tea.Quit
		}
		return m, tea.Batch(m.loadStateCmd(false), m.loadGHCmd(false))
	case stateTickMsg:
		return m, m.loadStateCmd(true)
	case ghTickMsg:
		return m, m.loadGHCmd(true)
	case launchPaneMsg:
		m.newPane.launching = false
		if msg.err != nil {
			m.newPane.err = msg.err.Error()
			return m, nil
		}
		m.mode = modeMonitor
		m.notice = "created new agent pane"
		return m, m.loadStateCmd(false)
	case paneFocusedMsg:
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

func (m model) updateFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
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

	if m.mode == modeNewPane {
		return lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			m.newPaneView(),
			dimStyle.Render("enter create  tab field  arrows/space agent  esc cancel"),
			footer,
		)
	}

	detail := m.detail
	detail.SetContent(m.detailContent())
	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		m.table.View(),
		panelStyle.Width(max(0, m.width)).Render(detail.View()),
		footer,
	)
}

func (m *model) resize() {
	if m.width <= 0 {
		return
	}
	if m.mode == modeNewPane {
		inputWidth := m.formInputWidth()
		m.newPane.prompt.Width = inputWidth
		m.newPane.slug.Width = inputWidth
	}
	tableHeight := max(m.height-detailHeight-5, 4)
	m.table.SetWidth(m.width)
	m.table.SetHeight(tableHeight)
	m.table.SetColumns(columnsForWidth(m.width))
	m.detail.Width = m.width
	m.detail.Height = detailHeight
	m.refreshRows()
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
	m.pendingAction = &pendingLifecycleAction{action: action, pane: pane}
	m.actionMessage = confirmMessage(action, pane)
	return m, nil
}

func (m model) lifecycleCmd(pending pendingLifecycleAction) tea.Cmd {
	opts := lifecycle.Options{
		ProjectRoot: m.opts.ProjectRoot,
		StatePath:   state.Path(m.opts.ProjectRoot),
	}
	runner := m.opts.lifecycle
	return func() tea.Msg {
		var buf bytes.Buffer
		lg := actionLogger{w: &buf}
		var code exitcode.Code
		switch pending.action {
		case actionClose:
			code = runner.Close(opts, pending.pane.Parent, pending.pane.IssueNum, lg)
		case actionMerge:
			code = runner.Merge(opts, pending.pane.Parent, pending.pane.IssueNum, lg)
		case actionCleanup:
			code = runner.Cleanup(opts, pending.pane.Parent, lg)
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

func confirmMessage(action lifecycleAction, pane paneView) string {
	switch action {
	case actionCleanup:
		return fmt.Sprintf("confirm cleanup for parent %s? y/n", dash(pane.Parent))
	default:
		return fmt.Sprintf("confirm %s #%d? y/n", action, pane.IssueNum)
	}
}

func lifecycleResultMessage(msg lifecycleDoneMsg) string {
	prefix := fmt.Sprintf("%s #%d", msg.action, msg.pane.IssueNum)
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
	return fmt.Sprintf("%s #%d...", pending.action, pending.pane.IssueNum)
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
	prompt := textinput.New()
	prompt.Placeholder = "Prompt"
	prompt.Prompt = "> "
	prompt.CharLimit = 1000
	prompt.Width = width
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
			return m, tea.Quit
		}
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeMonitor
		m.newPane = newPaneForm{}
		return m, nil
	case "tab", "shift+tab", "up", "down":
		m.moveNewPaneFocus(msg.String())
		return m, nil
	case "left", "right", " ":
		if m.newPane.focus == newPaneFieldAgent {
			m.toggleNewPaneAgent()
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
	return formStyle.Width(max(0, m.width-4)).Render(strings.Join(lines, "\n"))
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
	return clampInt(m.width-12, 24, 100)
}

func (m *model) refreshRows() {
	m.allPanes = applyIssueStatuses(m.allPanes, m.issues)
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
		fmt.Sprintf("%s #%d  %s", pane.Parent, pane.IssueNum, pane.Name),
		fmt.Sprintf("pane=%s tmux=%s title=%s agent=%s", dash(pane.PaneID), pane.TmuxState, dash(pane.TmuxTitle), dash(pane.Agent)),
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
	focus := m.opts.FocusPane
	m.notice = fmt.Sprintf("focusing %s...", paneID)
	return func() tea.Msg {
		if !alive(paneID) {
			return paneFocusedMsg{paneID: paneID, err: errPaneNotAlive}
		}
		return paneFocusedMsg{paneID: paneID, err: focus(paneID)}
	}
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
	capture := m.opts.CapturePaneOutput
	m.peek = panePeek{PaneID: paneID, Loading: true}
	return func() tea.Msg {
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
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil, err
	}
	tmuxPanes, err := tmuxrun.ListAllPanes()
	tmuxKnown := err == nil
	worktrees := loadWorktreeStats(store.Panes)
	return buildPaneViews(projectRoot, store.Panes, tmuxPanes, tmuxKnown, issues, worktrees), err
}

func loadIssueStatuses(projectRoot string) (map[issueKey]issueStatus, error) {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil, err
	}
	parents := recordedParents(store.Panes)
	statuses := map[issueKey]issueStatus{}
	if len(parents) == 0 {
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
					return nil, fmt.Errorf("#%d: %w", issue.Number, err)
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
			cached.HasOpenBlockers = info.Blocked
			cached.WaveDegraded = info.Degraded
			statuses[key] = cached
		}
	}
	return statuses, loadErr
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

func detectIssueTransitions(previous map[issueKey]issueTransitionSnapshot, current map[issueKey]issueStatus) []fanoutnotify.Event {
	if len(previous) == 0 || len(current) == 0 {
		return nil
	}
	keys := sortedIssueKeys(current)
	events := []fanoutnotify.Event{}
	for _, key := range keys {
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
		return cmp.Compare(a.Num, b.Num)
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

func keyForIssue(parent string, num int) issueKey {
	return issueKey{Parent: normalizedParent(parent), Num: num}
}

func normalizedParent(parent string) string {
	parentNum, err := strconv.Atoi(parent)
	if err != nil {
		return parent
	}
	return strconv.Itoa(parentNum)
}

func buildPaneViews(projectRoot string, panes []state.Pane, tmuxPanes []tmuxrun.PaneInfo, tmuxKnown bool, issues map[issueKey]issueStatus, worktrees map[string]worktreeStatView) []paneView {
	tmuxByID := map[string]tmuxrun.PaneInfo{}
	for _, pane := range tmuxPanes {
		tmuxByID[pane.ID] = pane
	}
	out := make([]paneView, 0, len(panes))
	seen := map[issueKey]bool{}
	for _, pane := range panes {
		key := keyForIssue(pane.Parent, pane.IssueNum)
		status, hasStatus := issues[key]
		seen[key] = true
		view := paneView{
			Parent:       pane.Parent,
			IssueNum:     pane.IssueNum,
			Name:         paneName(pane),
			PaneID:       pane.PaneID,
			TmuxState:    tmuxState(pane.PaneID, tmuxByID, tmuxKnown),
			IssueState:   "-",
			PRSummary:    "-",
			Wave:         status.Wave,
			WaveLabel:    firstNonEmpty(pane.Wave, status.WaveLabel),
			WaveBadge:    waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:     dash(status.Blockers),
			Blocked:      status.HasOpenBlockers,
			CIStatus:     "-",
			DiffSummary:  "-",
			DirtyState:   "-",
			BranchName:   pane.BranchName,
			WorktreePath: relativePath(projectRoot, pane.WorktreePath),
			Agent:        pane.Agent,
			CreatedAt:    pane.CreatedAt,
			Prompt:       pane.Prompt,
		}
		if tmuxPane, ok := tmuxByID[pane.PaneID]; ok {
			view.TmuxTitle = tmuxPane.Title
		}
		if hasStatus {
			view.IssueState = dash(status.State)
			view.PRSummary = summarizePRs(status.PRs)
			view.HasMergedPR = hasMergedPR(status.PRs)
			view.CIStatus = ghissue.SummarizeCI(status.PRs)
		}
		if stat, ok := worktrees[pane.WorktreePath]; ok {
			view.DiffSummary = stat.Diff
			view.DirtyState = stat.Dirty
			view.WorktreeErr = stat.Err
		}
		out = append(out, view)
	}
	for key, status := range issues {
		if seen[key] {
			continue
		}
		out = append(out, paneView{
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
		})
	}
	sortPaneViews(out)
	return out
}

func applyIssueStatuses(panes []paneView, issues map[issueKey]issueStatus) []paneView {
	out := slices.Clone(panes)
	seen := map[issueKey]bool{}
	for i := range out {
		key := keyForIssue(out[i].Parent, out[i].IssueNum)
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
		}
	}
	for key, status := range issues {
		if seen[key] {
			continue
		}
		out = append(out, paneView{
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
		})
	}
	sortPaneViews(out)
	return out
}

func sortPaneViews(panes []paneView) {
	slices.SortFunc(panes, func(a, b paneView) int {
		if c := cmp.Compare(a.Parent, b.Parent); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Wave, b.Wave); c != 0 {
			return c
		}
		return cmp.Compare(a.IssueNum, b.IssueNum)
	})
}

func syntheticTmuxState(status issueStatus) string {
	switch {
	case strings.EqualFold(status.State, "CLOSED"):
		return "closed"
	case status.HasOpenBlockers:
		return "deferred"
	case strings.EqualFold(status.State, "OPEN"):
		return "queued"
	default:
		return "unknown"
	}
}

func (p paneView) tableRow() table.Row {
	tmuxState := p.TmuxState
	if tmuxState == "stale" {
		tmuxState = "stale!"
	}
	return table.Row{
		compactParent(p.Parent),
		"#" + strconv.Itoa(p.IssueNum),
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

func loadWorktreeStats(panes []state.Pane) map[string]worktreeStatView {
	runner := gitstat.Runner{}
	stats := map[string]worktreeStatView{}
	for _, pane := range panes {
		path := strings.TrimSpace(pane.WorktreePath)
		if path == "" {
			continue
		}
		if _, ok := stats[path]; ok {
			continue
		}
		stats[path] = worktreeStatForPath(runner, path)
	}
	return stats
}

func worktreeStatForPath(runner gitstat.Runner, path string) worktreeStatView {
	stat, err := runner.Worktree(path)
	if err != nil {
		return worktreeStatView{Diff: "-", Dirty: "unknown", Err: err.Error()}
	}
	dirty := "clean"
	if stat.Dirty {
		dirty = "dirty"
	}
	return worktreeStatView{
		Diff:  fmt.Sprintf("+%d/-%d", stat.Additions, stat.Deletions),
		Dirty: dirty,
	}
}

func (m model) footerText() string {
	parts := []string{"q quit", "n new", "j/k move", "/ filter", "enter/o focus", "p peek", "c close", "m merge", "x cleanup"}
	if m.filterEditing {
		parts = append(parts, "typing")
	}
	if m.filterEditing || strings.TrimSpace(m.filterQuery) != "" {
		parts = append(parts, fmt.Sprintf("filter=%q %d/%d", m.filterQuery, len(m.panes), len(m.allPanes)))
	}
	parts = append(parts, "state "+formatClock(m.lastState), "gh "+formatClock(m.lastGH))
	return strings.Join(parts, "  ")
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
	return len(f.terms) == 0 && len(f.states) == 0 && len(f.agents) == 0 && len(f.waves) == 0
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
	searchText := strings.ToLower(strings.Join([]string{
		p.Parent,
		"#" + strconv.Itoa(p.IssueNum),
		strconv.Itoa(p.IssueNum),
		p.Name,
		p.PaneID,
		p.TmuxState,
		p.TmuxTitle,
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
	for _, term := range filter.terms {
		if !strings.Contains(searchText, term) {
			return false
		}
	}
	return true
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

func paneName(pane state.Pane) string {
	if strings.TrimSpace(pane.DisplayName) != "" {
		return pane.DisplayName
	}
	if strings.TrimSpace(pane.Slug) != "" {
		return pane.Slug
	}
	return "#" + strconv.Itoa(pane.IssueNum)
}

func tmuxState(paneID string, panes map[string]tmuxrun.PaneInfo, known bool) string {
	if strings.TrimSpace(paneID) == "" {
		return "-"
	}
	if !known {
		return "unknown"
	}
	if _, ok := panes[paneID]; ok {
		return "live"
	}
	return "stale"
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

func relativePath(root, path string) string {
	if root == "" || path == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return path
	}
	return rel
}

func cloneIssueStatuses(in map[issueKey]issueStatus) map[issueKey]issueStatus {
	out := make(map[issueKey]issueStatus, len(in))
	maps.Copy(out, in)
	return out
}

func summarizeHUD(panes []paneView) hudSummary {
	summary := hudSummary{Total: len(panes)}
	for _, pane := range panes {
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
