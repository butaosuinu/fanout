package tui

import (
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LaunchMode selects the new-session lane. The zero value is the classic
// free-prompt manual pane so pre-mode requests keep their meaning.
type LaunchMode string

const (
	LaunchModePrompt LaunchMode = ""
	LaunchModeIssue  LaunchMode = "issue"
	LaunchModePlan   LaunchMode = "plan"
)

// LaunchRequest describes one launch requested from the TUI new-session form.
type LaunchRequest struct {
	Mode   LaunchMode
	Prompt string // Mode == prompt
	Issue  int    // Mode == issue: the selected issue number
	Plan   string // Mode == plan: the selected plan slug
	// Agents holds the prompt-mode launch list (one pane per entry).
	Agents []string
	// DefaultAgent and AgentOverrides carry the issue/plan-mode agent
	// selection. Overrides are keyed by child issue number ("123") or plan
	// task id and hold only rows that differ from DefaultAgent, mirroring
	// repeatable --agent target=name flags.
	DefaultAgent   string
	AgentOverrides map[string]string
}

// LaunchFunc creates a manual fanout pane for a TUI request. It returns an
// optional notice (e.g. a tolerated base-refresh skip) to surface on success.
type LaunchFunc func(LaunchRequest) (notice string, err error)

// IssueLaunchFunc starts a session for one GitHub issue: a fan-out when the
// issue has OPEN children, a single pane otherwise.
type IssueLaunchFunc func(issueNum int, defaultAgent string, overrides map[string]string) (notice string, err error)

// PlanLaunchFunc runs `fanout plan <slug>` semantics for a stored plan spec.
type PlanLaunchFunc func(slug string, defaultAgent string, overrides map[string]string) (notice string, err error)

// NewPanePromptRequest describes a request to collect a manual pane prompt from
// an external prompt surface, such as a tmux display-popup.
type NewPanePromptRequest struct {
	DefaultAgent string
}

// NewPanePromptFunc collects one manual pane launch request. canceled is true
// when the user dismissed the prompt without submitting.
type NewPanePromptFunc func(NewPanePromptRequest) (req LaunchRequest, canceled bool, err error)

// NewPanePromptOptions configures the standalone new-pane prompt program.
type NewPanePromptOptions struct {
	ProjectRoot   string
	DefaultAgent  string
	Width         int
	Height        int
	ListRepoFiles func(root string) ([]string, error)
	// List providers for the issue/plan modes; a nil provider hides its mode.
	ListOpenIssues    func() ([]IssueListItem, error)
	ListPlanSlugs     func() ([]string, error)
	ListIssueChildren func(parent int) ([]ChildTarget, error)
	ListPlanTasks     func(slug string) ([]PlanTaskItem, error)
}

const newPanePopupOpeningNotice = "opening new pane popup..."

// AttachTarget describes the recorded pane/worktree a new agent should share.
type AttachTarget struct {
	TargetPath        string
	SourceProjectRoot string
	SourceParent      string
	SourceIssueNum    int
	SourceTaskID      string
	SourceBranchName  string
	SourceLabel       string
}

// AttachLaunchRequest describes one same-worktree agent launch requested from
// the TUI.
type AttachLaunchRequest struct {
	Prompt string
	Agents []string
	Target AttachTarget
}

// AttachLaunchFunc creates an agent pane in an existing worktree.
type AttachLaunchFunc func(AttachLaunchRequest) (notice string, err error)

type newPaneField int

const (
	newPaneFieldMode newPaneField = iota
	newPaneFieldMain
	newPaneFieldAgent
)

// newPaneMode is the form's input lane: the classic free prompt, the OPEN
// issue picker, or the stored plan picker.
type newPaneMode int

const (
	newPaneModePrompt newPaneMode = iota
	newPaneModeIssue
	newPaneModePlan
)

// newPaneStep is the wizard position: the mode form (step 1) or the
// per-target agent assignment that issue/plan submissions open (step 2).
type newPaneStep int

const (
	newPaneStepForm newPaneStep = iota
	newPaneStepAssign
)

type newPaneForm struct {
	prompt     textarea.Model
	agentCount map[string]int
	agentIndex int
	focus      newPaneField
	launching  bool
	err        string
	attach     *AttachTarget

	// Issue/plan mode state. agentChoice is the single default-agent
	// selection those modes use instead of the prompt-mode count selector.
	mode        newPaneMode
	step        newPaneStep
	issuePicker pickerState
	planPicker  pickerState
	agentChoice int
	assign      assignState
	assignGen   int // monotonic load generation; survives esc so stale loads drop
	selIssue    int
	selPlan     string

	// @-mention file completion state, active only while focus is on the
	// prompt field. compQuery is the text typed after '@' (the '@' itself is
	// left in the textarea); compResults holds the ranked, display-capped
	// matches and compTotal the full match count for the "+N more" hint.
	completing  bool
	compQuery   string
	compResults []string
	compIndex   int
	compTotal   int
}

func (m *model) openNewPaneForm() {
	m.mode = modeNewPane
	m.notice = ""
	m.newPane = newNewPaneForm(m.opts.DefaultAgent, m.inputContentWidth())
}

// RunNewPanePrompt opens only the new-pane prompt UI and returns the submitted
// launch request. It is used by the tmux display-popup helper process.
func RunNewPanePrompt(opts NewPanePromptOptions) (LaunchRequest, bool, error) {
	width := opts.Width
	if width <= 0 {
		width = 90
	}
	height := opts.Height
	if height <= 0 {
		height = 24
	}
	keyboard := newShiftEnterProtocols(os.Stdout, enhancedKeyboardKeysEnabled())
	m := newModel(Options{
		ProjectRoot:       opts.ProjectRoot,
		DefaultAgent:      opts.DefaultAgent,
		ListRepoFiles:     opts.ListRepoFiles,
		ListOpenIssues:    opts.ListOpenIssues,
		ListPlanSlugs:     opts.ListPlanSlugs,
		ListIssueChildren: opts.ListIssueChildren,
		ListPlanTasks:     opts.ListPlanTasks,
		LaunchPane:        func(LaunchRequest) (string, error) { return "", nil },
		keyboard:          keyboard,
	})
	m.promptOnly = true
	m.width = width
	m.height = height
	m.openNewPaneForm()
	if m.opts.ListRepoFiles != nil {
		m.repoFilesLoading = true
	}
	input, closeInput, err := newShiftEnterProgramInput(os.Stdin)
	if err != nil {
		return LaunchRequest{}, false, err
	}
	defer closeInput()
	defer keyboard.Disable()
	stopSignalCleanup := watchNewPanePromptSignals(keyboard, closeInput, os.Exit)
	defer stopSignalCleanup()
	finalModel, err := tea.NewProgram(
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
	if err != nil {
		return LaunchRequest{}, false, err
	}
	final, ok := finalModel.(model)
	if !ok {
		return LaunchRequest{}, false, fmt.Errorf("unexpected prompt model %T", finalModel)
	}
	if !final.promptDone || final.promptCanceled {
		return LaunchRequest{}, true, nil
	}
	return final.promptResult, false, nil
}

func watchNewPanePromptSignals(keyboard keyboardProtocols, closeInput func(), exit func(int)) func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	var stopOnce sync.Once
	signal.Notify(signals, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-signals:
			cleanupNewPanePromptSignal(sig, keyboard, closeInput, exit)
		case <-done:
		}
	}()
	return func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
}

func cleanupNewPanePromptSignal(sig os.Signal, keyboard keyboardProtocols, closeInput func(), exit func(int)) {
	keyboard.Disable()
	closeInput()
	exit(signalExitCode(sig))
}

func signalExitCode(sig os.Signal) int {
	if code, ok := sig.(syscall.Signal); ok {
		return 128 + int(code)
	}
	return 1
}

func (m *model) openAttachAgentForm() tea.Cmd {
	pane, ok := m.selectedPane()
	if !ok {
		m.notice = "no pane selected"
		return nil
	}
	targetPath := pane.absoluteWorktreePath(m.opts.ProjectRoot)
	if targetPath == "" {
		m.notice = fmt.Sprintf("attach skipped for %s: no worktree path", pane.identityLabel())
		return nil
	}
	sourceParent, sourceIssueNum, sourceTaskID, sourceLabel := attachSourceIdentity(pane)
	target := AttachTarget{
		TargetPath:        targetPath,
		SourceProjectRoot: pane.sourceProjectRoot,
		SourceParent:      sourceParent,
		SourceIssueNum:    sourceIssueNum,
		SourceTaskID:      sourceTaskID,
		SourceBranchName:  pane.BranchName,
		SourceLabel:       sourceLabel,
	}
	m.mode = modeNewPane
	m.notice = ""
	m.newPane = newNewPaneForm(m.opts.DefaultAgent, m.inputContentWidth())
	m.newPane.attach = &target
	return m.reloadRepoFilesCmd()
}

func attachSourceIdentity(pane paneView) (parent string, issueNum int, taskID, label string) {
	if !pane.isAttachedAgent() {
		return pane.Parent, pane.IssueNum, pane.TaskID, pane.identityLabel()
	}
	parent = strings.TrimSpace(pane.SourceParent)
	if parent == "" {
		parent = pane.Parent
	}
	if pane.SourceIssueNum > 0 {
		issueNum = pane.SourceIssueNum
	}
	taskID = strings.TrimSpace(pane.SourceTaskID)
	switch {
	case taskID != "":
		label = taskID
	case issueNum > 0:
		label = fmt.Sprintf("#%d", issueNum)
	default:
		label = pane.identityLabel()
	}
	return parent, issueNum, taskID, label
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
		key.WithHelp("ctrl+j", "newline"),
	)
	prompt.Focus()

	if defaultAgent != "codex" {
		defaultAgent = defaultLaunchAgent
	}
	return newPaneForm{
		prompt:      prompt,
		agentCount:  defaultAgentCounts(defaultAgent),
		agentIndex:  defaultAgentIndex(defaultAgent),
		agentChoice: defaultAgentIndex(defaultAgent),
		focus:       newPaneFieldMain,
	}
}

func (m model) updateNewPane(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.newPane.launching {
		if msg.String() == "ctrl+c" {
			return m.quit()
		}
		return m, nil
	}
	// Keep ctrl+c a global quit, above the completion router, so the popup
	// never traps it.
	if msg.String() == "ctrl+c" {
		return m.quit()
	}
	if m.newPane.step == newPaneStepAssign {
		return m.updateNewPaneAssign(msg)
	}
	// While the @-completion popup is open it owns navigation/confirm/cancel
	// keys; anything it does not handle (printable chars, backspace, cursor
	// moves) falls through to normal editing with the updated completion state
	// carried forward.
	if m.newPane.focus == newPaneFieldMain && m.newPane.mode == newPaneModePrompt && m.newPane.completing {
		next, handled := m.updatePromptCompletion(msg)
		if handled {
			return next, nil
		}
		m = next
	}
	switch msg.String() {
	case "esc":
		if m.promptOnly {
			m.promptCanceled = true
			return m, tea.Quit
		}
		m.mode = modeMonitor
		m.newPane = newPaneForm{}
		return m, nil
	case "tab", "shift+tab":
		m.moveNewPaneFocus(msg.String())
		return m, nil
	case "left", "right", " ":
		switch m.newPane.focus {
		case newPaneFieldMode:
			return m, m.cycleNewPaneMode(msg.String())
		case newPaneFieldAgent:
			if m.newPane.mode == newPaneModePrompt {
				m.adjustNewPaneAgent(msg.String())
			} else {
				m.cycleNewPaneAgentChoice(msg.String())
			}
			return m, nil
		default:
			// Main field: the key falls through to the text/filter routing below.
		}
	case "up", "down", "ctrl+p", "ctrl+n":
		if m.newPane.focus == newPaneFieldMain && m.newPane.mode != newPaneModePrompt {
			m.moveActivePicker(pickerMoveDelta(msg.String()))
			return m, nil
		}
		if (msg.String() == "up" || msg.String() == "down") && m.newPane.focus != newPaneFieldMain {
			if m.newPane.focus == newPaneFieldAgent && m.newPane.mode == newPaneModePrompt {
				m.moveNewPaneAgent(msg.String())
			} else {
				m.moveNewPaneFocus(msg.String())
			}
			return m, nil
		}
	case "enter":
		return m, m.submitNewPane()
	}

	var cmd tea.Cmd
	switch {
	case m.newPane.focus == newPaneFieldMain && m.newPane.mode == newPaneModePrompt:
		m.newPane.prompt, cmd = m.newPane.prompt.Update(msg)
		// Open completion when '@' is typed at a word boundary. Inspecting the
		// textarea after the insert keeps this robust to soft-wrapping.
		if !m.newPane.completing && msg.Type == tea.KeyRunes && string(msg.Runes) == "@" &&
			m.atWordBoundaryBeforeCursor() {
			m.beginCompletion()
		}
	case m.newPane.focus == newPaneFieldMain:
		m.updateActivePickerFilter(msg)
	default:
		// The mode and agent rows are toggles, not text inputs; no message routing.
	}
	return m, cmd
}

func (m *model) moveNewPaneFocus(key string) {
	order := m.newPaneFocusOrder()
	idx := 0
	for i, field := range order {
		if field == m.newPane.focus {
			idx = i
			break
		}
	}
	switch key {
	case "shift+tab", "up":
		idx = (idx + len(order) - 1) % len(order)
	default:
		idx = (idx + 1) % len(order)
	}
	m.newPane.focus = order[idx]
	if m.newPane.focus == newPaneFieldMain && m.newPane.mode == newPaneModePrompt {
		m.newPane.prompt.Focus()
	} else {
		m.newPane.prompt.Blur()
	}
}

// newPaneFocusOrder hides the Mode row when only the classic prompt mode is
// wired, so the original two-field form keeps its focus cycle.
func (m model) newPaneFocusOrder() []newPaneField {
	if len(m.availableNewPaneModes()) > 1 {
		return []newPaneField{newPaneFieldMode, newPaneFieldMain, newPaneFieldAgent}
	}
	return []newPaneField{newPaneFieldMain, newPaneFieldAgent}
}

func (m *model) moveNewPaneAgent(key string) {
	switch key {
	case "up":
		m.newPane.agentIndex = (m.newPane.agentIndex + len(launchAgents) - 1) % len(launchAgents)
	default:
		m.newPane.agentIndex = (m.newPane.agentIndex + 1) % len(launchAgents)
	}
}

func (m *model) adjustNewPaneAgent(key string) {
	agentName := launchAgents[m.newPane.agentIndex]
	current := m.newPane.agentCount[agentName]
	switch key {
	case "left":
		if current > 0 {
			m.newPane.agentCount[agentName] = current - 1
		}
	case "right":
		if current < maxAgentLaunchCount {
			m.newPane.agentCount[agentName] = current + 1
		}
	default:
		if current > 0 {
			m.newPane.agentCount[agentName] = 0
		} else {
			m.newPane.agentCount[agentName] = 1
		}
	}
}

func (m *model) submitNewPane() tea.Cmd {
	if m.newPane.mode != newPaneModePrompt {
		return m.submitNewPanePicker()
	}
	prompt := strings.TrimSpace(m.newPane.prompt.Value())
	if prompt == "" {
		m.newPane.err = "prompt is required"
		return nil
	}
	agents := m.selectedNewPaneAgents()
	if len(agents) == 0 {
		m.newPane.err = "select at least one agent"
		return nil
	}
	m.newPane.err = ""
	m.newPane.launching = true
	if m.newPane.attach != nil {
		if m.opts.LaunchAttach == nil {
			m.newPane.err = "attach launcher is not configured"
			m.newPane.launching = false
			return nil
		}
		req := AttachLaunchRequest{
			Prompt: prompt,
			Agents: agents,
			Target: *m.newPane.attach,
		}
		launch := m.opts.LaunchAttach
		return func() tea.Msg {
			notice, err := launch(req)
			return launchPaneMsg{notice: notice, count: len(agents), attached: true, err: err}
		}
	}
	req := LaunchRequest{
		Prompt: prompt,
		Agents: agents,
	}
	if m.promptOnly {
		m.promptResult = req
		m.promptDone = true
		return tea.Quit
	}
	if m.opts.LaunchPane == nil {
		m.newPane.err = "new pane launcher is not configured"
		m.newPane.launching = false
		return nil
	}
	launch := m.opts.LaunchPane
	return func() tea.Msg {
		notice, err := launch(req)
		return launchPaneMsg{notice: notice, count: len(agents), err: err}
	}
}

func (m *model) openNewPanePopupCmd() tea.Cmd {
	prompt := m.opts.NewPanePrompt
	if prompt == nil {
		m.openNewPaneForm()
		return m.reloadRepoFilesCmd()
	}
	m.notice = newPanePopupOpeningNotice
	m.newPanePopupOpen = true
	req := NewPanePromptRequest{DefaultAgent: m.opts.DefaultAgent}
	return func() tea.Msg {
		launchReq, canceled, err := prompt(req)
		return newPanePromptMsg{req: launchReq, canceled: canceled, err: err}
	}
}

func (m *model) launchNewPaneRequest(req LaunchRequest) tea.Cmd {
	switch req.Mode {
	case LaunchModeIssue:
		return m.launchIssueSessionRequest(req)
	case LaunchModePlan:
		return m.launchPlanSessionRequest(req)
	default:
		// Prompt mode continues below.
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	agents := make([]string, 0, len(req.Agents))
	for _, agentName := range req.Agents {
		agentName = strings.TrimSpace(agentName)
		if agentName != "" {
			agents = append(agents, agentName)
		}
	}
	req.Agents = agents
	if req.Prompt == "" {
		m.notice = "new pane: prompt is required"
		return nil
	}
	if len(req.Agents) == 0 {
		m.notice = "new pane: select at least one agent"
		return nil
	}
	if m.opts.LaunchPane == nil {
		m.notice = "new pane: launcher is not configured"
		return nil
	}
	m.newPane.launching = true
	m.notice = "creating new agent pane..."
	launch := m.opts.LaunchPane
	return func() tea.Msg {
		notice, err := launch(req)
		return launchPaneMsg{notice: notice, count: len(req.Agents), err: err}
	}
}

func (m model) newPaneView() string {
	if m.newPane.step == newPaneStepAssign {
		return m.newPaneAssignView()
	}
	title := "New agent pane"
	if m.newPane.attach != nil {
		title = "Attach agent to worktree"
	}
	lines := []string{titleStyle.Render(title)}
	if len(m.availableNewPaneModes()) > 1 {
		lines = append(lines, m.newPaneFieldView(newPaneFieldMode, "Mode", m.newPaneModeTabsView(), false))
	}
	switch m.newPane.mode {
	case newPaneModeIssue:
		lines = append(lines,
			m.newPaneFieldView(newPaneFieldMain, "Issue", m.pickerView(m.newPane.issuePicker, "no open issues"), true),
			m.newPaneFieldView(newPaneFieldAgent, "Agent", m.singleAgentSelectorView(), false),
		)
	case newPaneModePlan:
		lines = append(lines,
			m.newPaneFieldView(newPaneFieldMain, "Plan", m.pickerView(m.newPane.planPicker, "no plans found in .fanout/plans"), true),
			m.newPaneFieldView(newPaneFieldAgent, "Agent", m.singleAgentSelectorView(), false),
		)
	default:
		lines = append(lines, m.newPaneFieldView(newPaneFieldMain, "Prompt", m.newPane.prompt.View(), true))
		if m.newPane.focus == newPaneFieldMain && m.newPane.completing {
			lines = append(lines, m.completionPopupView())
		}
		lines = append(lines, m.newPaneFieldView(newPaneFieldAgent, "Agent", m.agentSelectorView(), false))
	}
	if m.newPane.launching {
		lines = append(lines, dimStyle.Render("creating pane..."))
	}
	if m.newPane.err != "" {
		lines = append(lines, errStyle.Render("error: "+m.newPane.err))
	}
	lines = append(lines, dimStyle.Render(m.newPaneHint()))
	return modalStyle.Width(m.modalWidth()).Render(strings.Join(lines, "\n"))
}

func (m model) newPaneHint() string {
	if m.newPane.mode != newPaneModePrompt {
		return "enter next  type to filter  tab field  esc cancel"
	}
	// Only advertise Shift+Enter when enhanced keyboard input is active; otherwise
	// it submits the form and the hint would mislead. Ctrl+J always inserts a newline.
	newlineHint := "ctrl+j newline"
	if enhancedKeyboardKeysEnabled() {
		newlineHint = "shift+enter/ctrl+j newline"
	}
	verb := "create"
	if m.newPane.attach != nil {
		verb = "attach"
	}
	return "enter " + verb + "  " + newlineHint + "  tab field  esc cancel"
}

func (m model) newPaneFieldView(field newPaneField, label, value string, boxed bool) string {
	focused := m.newPane.focus == field
	marker := "  "
	if focused {
		marker = "> "
	}
	if boxed {
		style := inputBoxStyle
		if focused {
			style = inputBoxFocusStyle
		}
		value = style.Render(value)
	}
	return marker + label + "\n" + value
}

func (m model) agentSelectorView() string {
	lines := make([]string, 0, len(launchAgents))
	for i, agentName := range launchAgents {
		count := m.newPane.agentCount[agentName]
		marker := "  "
		if m.newPane.focus == newPaneFieldAgent && m.newPane.agentIndex == i {
			marker = "> "
		}
		token := fmt.Sprintf("[%d] %s", count, agentName)
		if count > 0 {
			token = titleStyle.Render(token)
		} else {
			token = dimStyle.Render(token)
		}
		lines = append(lines, marker+token)
	}
	return strings.Join(lines, "\n")
}

// singleAgentSelectorView renders the issue/plan-mode default-agent choice: a
// one-of selector, unlike the prompt-mode per-agent counts (a fan-out shares
// one default agent across children; per-target overrides live in step 2).
func (m model) singleAgentSelectorView() string {
	parts := make([]string, 0, len(launchAgents))
	for i, agentName := range launchAgents {
		token := "( ) " + agentName
		if i == m.newPane.agentChoice {
			token = titleStyle.Render("(x) " + agentName)
		} else {
			token = dimStyle.Render(token)
		}
		parts = append(parts, token)
	}
	return strings.Join(parts, "  ")
}

func (m *model) cycleNewPaneAgentChoice(key string) {
	n := len(launchAgents)
	switch key {
	case "left":
		m.newPane.agentChoice = (m.newPane.agentChoice + n - 1) % n
	default:
		m.newPane.agentChoice = (m.newPane.agentChoice + 1) % n
	}
}

const maxAgentLaunchCount = 3

var launchAgents = []string{"claude", "codex"}

func defaultAgentCounts(defaultAgent string) map[string]int {
	counts := make(map[string]int, len(launchAgents))
	if !slices.Contains(launchAgents, defaultAgent) {
		defaultAgent = defaultLaunchAgent
	}
	for _, agentName := range launchAgents {
		counts[agentName] = 0
	}
	counts[defaultAgent] = 1
	return counts
}

func defaultAgentIndex(defaultAgent string) int {
	for i, agentName := range launchAgents {
		if agentName == defaultAgent {
			return i
		}
	}
	return 0
}

func (m model) selectedNewPaneAgents() []string {
	var agents []string
	for _, agentName := range launchAgents {
		count := clampInt(m.newPane.agentCount[agentName], 0, maxAgentLaunchCount)
		for range count {
			agents = append(agents, agentName)
		}
	}
	return agents
}

func (m model) formInputWidth() int {
	if m.width <= 0 {
		return 72
	}
	return clampInt(m.modalWidth()-8, 24, 92)
}

// inputContentWidth is the rendered width each framed text input should occupy.
// It leaves room for the input box's left/right border so the framed field
// keeps the same footprint as formInputWidth.
func (m model) inputContentWidth() int {
	return m.formInputWidth() - 2
}

func (m model) modalWidth() int {
	if m.width <= 0 {
		return 80
	}
	if m.promptOnly {
		return clampInt(m.width-2, 40, 104)
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
