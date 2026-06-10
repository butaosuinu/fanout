// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/lifecycle"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	Blockers        string
	HasOpenBlockers bool
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
	WaveBadge    string
	Blockers     string
	Blocked      bool
	CIStatus     string
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
	panes           []paneView
	issues          map[issueKey]issueStatus
	lastState       time.Time
	lastGH          time.Time
	stateErr        string
	ghErr           string
	notice          string
	newPane         newPaneForm
	peek            panePeek
	pendingAction   *pendingLifecycleAction
	actionRunning   bool
	quitAfterAction bool
	actionMessage   string
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

type stateTickMsg time.Time
type ghTickMsg time.Time
type launchPaneMsg struct {
	err error
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
		opts:   opts,
		mode:   modeMonitor,
		table:  t,
		detail: viewport.New(120, detailHeight),
		issues: map[issueKey]issueStatus{},
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
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
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
		m.panes = msg.panes
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
		if msg.issues != nil {
			m.issues = msg.issues
		}
		m.lastGH = msg.at
		m.refreshRows()
		if msg.scheduleNext {
			return m, tea.Tick(m.opts.GHInterval, func(t time.Time) tea.Msg { return ghTickMsg(t) })
		}
		return m, nil
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
	}
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

	footer := dimStyle.Render("q quit  n new  j/k move  enter/o focus  p peek  c close  m merge  x cleanup  state " + formatClock(m.lastState) + "  gh " + formatClock(m.lastGH))
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
		panelStyle.Width(maxInt(0, m.width)).Render(detail.View()),
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
	tableHeight := m.height - detailHeight - 5
	if tableHeight < 4 {
		tableHeight = 4
	}
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
		m.actionMessage = fmt.Sprintf("%s cancelled", m.pendingAction.action)
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
		switch msg.String() {
		case "ctrl+c":
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
	return formStyle.Width(maxInt(0, m.width-4)).Render(strings.Join(lines, "\n"))
}

func (m model) newPaneFieldView(field newPaneField, label, value string) string {
	marker := "  "
	if m.newPane.focus == field {
		marker = "> "
	}
	return marker + label + "\n" + value
}

func (m model) agentSelectorView() string {
	claude := "claude"
	codex := "codex"
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
	m.panes = applyIssueStatuses(m.panes, m.issues)
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
	if len(m.panes) == 0 {
		return "No recorded fanout panes in .fanout/state.json."
	}
	pane, ok := m.selectedPane()
	if !ok {
		return "No recorded fanout panes in .fanout/state.json."
	}
	lines := []string{
		fmt.Sprintf("%s #%d  %s", pane.Parent, pane.IssueNum, pane.Name),
		fmt.Sprintf("pane=%s tmux=%s title=%s agent=%s", dash(pane.PaneID), pane.TmuxState, dash(pane.TmuxTitle), dash(pane.Agent)),
		fmt.Sprintf("issue=%s pr=%s ci=%s branch=%s", dash(pane.IssueState), dash(pane.PRSummary), dash(pane.CIStatus), dash(pane.BranchName)),
		fmt.Sprintf("wave=%s blockers=%s", dash(pane.WaveBadge), dash(pane.Blockers)),
		fmt.Sprintf("worktree=%s", dash(pane.WorktreePath)),
		fmt.Sprintf("created=%s", dash(pane.CreatedAt)),
	}
	peekBudget := maxInt(2, m.detail.Height-len(lines)-1)
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
		lines = append(lines, truncatePreserveSpace(line, maxInt(20, m.detail.Width-2)))
	}
	return lines
}

func (m *model) markPaneStale(paneID string) {
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

func loadPaneViews(projectRoot string, issues map[issueKey]issueStatus) ([]paneView, error) {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil, err
	}
	tmuxPanes, err := tmuxrun.ListAllPanes()
	tmuxKnown := err == nil
	return buildPaneViews(projectRoot, store.Panes, tmuxPanes, tmuxKnown, issues), err
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
	stateCache := map[int]string{}
	var loadErr error
	for _, parent := range parents {
		children, parentBody, err := loadParentIssues(gh, parent, store.PanesForParent(parent))
		if err != nil {
			loadErr = errors.Join(loadErr, err)
			if len(children) == 0 {
				fallbackChildren, fallbackBody, fallbackErr := loadRecordedPaneIssues(gh, store.PanesForParent(parent))
				if fallbackErr != nil {
					return nil, errors.Join(loadErr, fallbackErr)
				}
				children = fallbackChildren
				if parentBody == "" {
					parentBody = fallbackBody
				}
			}
		}
		if err := hydrateIssues(gh, children); err != nil {
			return nil, err
		}
		deps := parentDependencies(parent, children, parentBody, stateCache, func(num int) string {
			stateName, _ := gh.IssueState(num)
			return stateName
		})
		waves := dependencyWaves(children, deps)
		for _, issue := range children {
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
			cached.Title = issue.Title
			cached.Wave = waves[issue.Number]
			cached.Blockers = formatBlockers(deps[issue.Number])
			cached.HasOpenBlockers = hasOpenBlocker(deps[issue.Number])
			statuses[key] = cached
		}
	}
	return statuses, loadErr
}

func loadParentIssues(gh ghissue.Runner, parent string, panes []state.Pane) ([]ghissue.Issue, string, error) {
	parentNum, err := strconv.Atoi(parent)
	if err != nil {
		return loadRecordedPaneIssues(gh, panes)
	}

	parentBody, err := gh.ParentBody(parentNum)
	var loadErr error
	if err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("parent body #%d: %w", parentNum, err))
		parentBody = ""
	}
	subIssues, err := gh.SubIssueList(parentNum)
	if err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("sub-issues #%d: %w", parentNum, err))
		subIssues = nil
	}
	children, err := mergeParentIssueChildren(parentNum, subIssues, parentBody, panes, gh.IssueDetail)
	if err != nil {
		return nil, parentBody, errors.Join(loadErr, err)
	}
	return children, parentBody, loadErr
}

func mergeParentIssueChildren(parentNum int, subIssues []ghissue.Issue, parentBody string, panes []state.Pane, issueDetail func(int) (ghissue.Issue, error)) ([]ghissue.Issue, error) {
	existing := parentChildNumbers(parentNum, subIssues)
	extra := []ghissue.Issue{}
	extra = append(extra, loadMissingIssueDetails(ghissue.TaskListNumbers(parentBody), existing, issueDetail)...)
	for _, pane := range panes {
		if pane.IssueNum <= 0 || existing[pane.IssueNum] {
			continue
		}
		detail, err := issueDetail(pane.IssueNum)
		if err != nil {
			return nil, fmt.Errorf("#%d: %w", pane.IssueNum, err)
		}
		extra = append(extra, detail)
		existing[pane.IssueNum] = true
	}
	return ghissue.MergeExtra(subIssues, extra), nil
}

func parentChildNumbers(parentNum int, subIssues []ghissue.Issue) map[int]bool {
	existing := map[int]bool{parentNum: true}
	for _, issue := range subIssues {
		existing[issue.Number] = true
	}
	return existing
}

func loadMissingIssueDetails(nums []int, existing map[int]bool, issueDetail func(int) (ghissue.Issue, error)) []ghissue.Issue {
	extra := []ghissue.Issue{}
	for _, num := range nums {
		if existing[num] {
			continue
		}
		detail, err := issueDetail(num)
		if err != nil {
			continue
		}
		extra = append(extra, detail)
		existing[num] = true
	}
	return extra
}

func loadRecordedPaneIssues(gh ghissue.Runner, panes []state.Pane) ([]ghissue.Issue, string, error) {
	children := make([]ghissue.Issue, 0, len(panes))
	seen := map[int]bool{}
	for _, pane := range panes {
		if pane.IssueNum <= 0 || seen[pane.IssueNum] {
			continue
		}
		detail, err := gh.IssueDetail(pane.IssueNum)
		if err != nil {
			return nil, "", fmt.Errorf("#%d: %w", pane.IssueNum, err)
		}
		children = append(children, detail)
		seen[pane.IssueNum] = true
	}
	return children, "", nil
}

func hydrateIssues(gh ghissue.Runner, issues []ghissue.Issue) error {
	for i := range issues {
		if issues[i].Body != "" {
			continue
		}
		detail, err := gh.IssueDetail(issues[i].Number)
		if err != nil {
			return fmt.Errorf("#%d: %w", issues[i].Number, err)
		}
		issues[i].Body = detail.Body
		issues[i].Labels = detail.Labels
		if issues[i].Title == "" {
			issues[i].Title = detail.Title
		}
		if issues[i].State == "" {
			issues[i].State = detail.State
		}
	}
	return nil
}

func parentDependencies(parent string, issues []ghissue.Issue, parentBody string, stateCache map[int]string, issueState func(int) string) map[int][]blockerStatus {
	deps := make(map[int][]blockerStatus, len(issues))
	for _, issue := range issues {
		childBlockers := blockers.FromChildBody(issue.Body)
		parentBlockers := []int{}
		if _, err := strconv.Atoi(parent); err == nil {
			parentBlockers = blockers.FromParentRow(parentBody, issue.Number)
		}
		nums := blockers.Dedupe(childBlockers, parentBlockers)
		rows := make([]blockerStatus, 0, len(nums))
		for _, num := range nums {
			stateName, ok := stateCache[num]
			if !ok {
				stateName = issueState(num)
				if stateName == "" {
					stateName = "UNKNOWN"
				}
				stateCache[num] = stateName
			}
			rows = append(rows, blockerStatus{Num: num, State: strings.ToUpper(stateName)})
		}
		deps[issue.Number] = rows
	}
	return deps
}

type blockerStatus struct {
	Num   int
	State string
}

func dependencyWaves(issues []ghissue.Issue, deps map[int][]blockerStatus) map[int]int {
	childSet := map[int]bool{}
	for _, issue := range issues {
		childSet[issue.Number] = true
	}
	waves := map[int]int{}
	visiting := map[int]bool{}
	var waveFor func(int) int
	waveFor = func(num int) int {
		if wave := waves[num]; wave > 0 {
			return wave
		}
		if visiting[num] {
			return 1
		}
		visiting[num] = true
		maxBlockerWave := 0
		for _, blocker := range deps[num] {
			blockerWave := 1
			if childSet[blocker.Num] {
				blockerWave = waveFor(blocker.Num)
			}
			if blockerWave > maxBlockerWave {
				maxBlockerWave = blockerWave
			}
		}
		delete(visiting, num)
		waves[num] = maxBlockerWave + 1
		return waves[num]
	}
	for _, issue := range issues {
		waveFor(issue.Number)
	}
	return waves
}

func formatBlockers(rows []blockerStatus) string {
	if len(rows) == 0 {
		return "-"
	}
	parts := make([]string, len(rows))
	for i, row := range rows {
		switch row.State {
		case "OPEN":
			parts[i] = fmt.Sprintf("OPEN #%d", row.Num)
		case "CLOSED":
			parts[i] = fmt.Sprintf("resolved #%d", row.Num)
		default:
			parts[i] = fmt.Sprintf("%s #%d", dash(row.State), row.Num)
		}
	}
	return strings.Join(parts, ", ")
}

func hasOpenBlocker(rows []blockerStatus) bool {
	for _, row := range rows {
		if row.State == "OPEN" {
			return true
		}
	}
	return false
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
	sort.Slice(parents, func(i, j int) bool {
		left, leftErr := strconv.Atoi(parents[i])
		right, rightErr := strconv.Atoi(parents[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return parents[i] < parents[j]
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

func buildPaneViews(projectRoot string, panes []state.Pane, tmuxPanes []tmuxrun.PaneInfo, tmuxKnown bool, issues map[issueKey]issueStatus) []paneView {
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
			WaveBadge:    waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:     dash(status.Blockers),
			Blocked:      status.HasOpenBlockers,
			CIStatus:     "-",
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
			view.CIStatus = summarizePRCI(status.PRs)
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
			CIStatus:    summarizePRCI(status.PRs),
			Wave:        status.Wave,
			WaveBadge:   waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:    dash(status.Blockers),
			Blocked:     status.HasOpenBlockers,
		})
	}
	sortPaneViews(out)
	return out
}

func applyIssueStatuses(panes []paneView, issues map[issueKey]issueStatus) []paneView {
	out := make([]paneView, len(panes))
	copy(out, panes)
	seen := map[issueKey]bool{}
	for i := range out {
		key := keyForIssue(out[i].Parent, out[i].IssueNum)
		seen[key] = true
		if status, ok := issues[key]; ok {
			out[i].IssueState = dash(status.State)
			out[i].PRSummary = summarizePRs(status.PRs)
			out[i].HasMergedPR = hasMergedPR(status.PRs)
			out[i].Wave = status.Wave
			out[i].WaveBadge = waveBadge(status.Wave, status.HasOpenBlockers)
			out[i].Blockers = dash(status.Blockers)
			out[i].Blocked = status.HasOpenBlockers
			out[i].CIStatus = summarizePRCI(status.PRs)
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
			CIStatus:    summarizePRCI(status.PRs),
			Wave:        status.Wave,
			WaveBadge:   waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:    dash(status.Blockers),
			Blocked:     status.HasOpenBlockers,
		})
	}
	sortPaneViews(out)
	return out
}

func sortPaneViews(panes []paneView) {
	sort.Slice(panes, func(i, j int) bool {
		if panes[i].Parent != panes[j].Parent {
			return panes[i].Parent < panes[j].Parent
		}
		if panes[i].Wave != panes[j].Wave {
			return panes[i].Wave < panes[j].Wave
		}
		return panes[i].IssueNum < panes[j].IssueNum
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
		dash(p.WaveBadge),
		truncate(dash(p.Blockers), 22),
		truncate(p.Name, 28),
		tmuxState,
		dash(p.IssueState),
		truncate(dash(p.PRSummary), 12),
		truncate(dash(p.CIStatus), 7),
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
	nameWidth := clampInt(width/5, 14, 28)
	blockerWidth := clampInt(width/5, 12, 22)
	branchWidth := clampInt(width/6, 10, 18)
	return []table.Column{
		{Title: "PARENT", Width: 10},
		{Title: "ISSUE", Width: 7},
		{Title: "WAVE", Width: 10},
		{Title: "BLOCKERS", Width: blockerWidth},
		{Title: "NAME", Width: nameWidth},
		{Title: "TMUX", Width: 8},
		{Title: "STATE", Width: 8},
		{Title: "PR", Width: 12},
		{Title: "CI", Width: 7},
		{Title: "BRANCH", Width: branchWidth},
		{Title: "PANE", Width: 8},
	}
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

func issueTitle(status issueStatus, num int) string {
	if strings.TrimSpace(status.Title) != "" {
		return status.Title
	}
	return "#" + strconv.Itoa(num)
}

func summarizePRs(prs []ghissue.PRRef) string {
	pr, ok := selectedPR(prs)
	if !ok {
		return "-"
	}
	return "#" + strconv.Itoa(pr.Number) + " " + dash(pr.DisplayState())
}

func summarizePRCI(prs []ghissue.PRRef) string {
	pr, ok := selectedPR(prs)
	if !ok {
		return "-"
	}
	return dash(pr.CIStatus)
}

func selectedPR(prs []ghissue.PRRef) (ghissue.PRRef, bool) {
	if len(prs) == 0 {
		return ghissue.PRRef{}, false
	}
	for _, pr := range prs {
		if pr.State == "MERGED" {
			return pr, true
		}
	}
	return prs[0], true
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
	for k, v := range in {
		out[k] = v
	}
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

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	return truncateRunes(s, max)
}

func truncatePreserveSpace(s string, max int) string {
	return truncateRunes(s, max)
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func compactMessage(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return truncate(strings.Join(fields, " "), 160)
}

func tailLines(s string, max int) []string {
	if max <= 0 {
		return nil
	}
	raw := strings.Split(s, "\n")
	if len(raw) > max {
		raw = raw[len(raw)-max:]
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
