package tui

import (
	"fmt"
	"maps"
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
)

// LaunchRequest describes one launch requested from the TUI new-session form.
type LaunchRequest struct {
	Mode   LaunchMode
	Prompt string // Mode == prompt
	Issue  int    // Mode == issue: the selected issue number
	// PlanFanout is prompt mode only: decompose the prompt via the fanout-plan
	// skill instead of launching a plain agent pane.
	PlanFanout bool
	// Agents holds the prompt-mode launch list (one pane per entry).
	Agents []string
	// DefaultAgent and AgentOverrides carry the issue-mode agent selection.
	// Overrides are keyed by child issue number ("123") and hold only rows that
	// differ from DefaultAgent, mirroring repeatable --agent target=name flags.
	DefaultAgent   string
	AgentOverrides map[string]string
}

// LaunchFunc creates a manual fanout pane for a TUI request. It returns an
// optional notice (e.g. a tolerated base-refresh skip) to surface on success.
type LaunchFunc func(LaunchRequest) (notice string, err error)

// IssueLaunchFunc starts a session for one GitHub issue: a fan-out when the
// issue has OPEN children, a single pane otherwise.
type IssueLaunchFunc func(issueNum int, defaultAgent string, overrides map[string]string) (notice string, err error)

// IssueOpenFunc opens a GitHub issue in an external browser surface.
type IssueOpenFunc func(issueNum int) error

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
	// List providers for the issue mode; a nil provider hides its mode.
	ListOpenIssues    func() ([]IssueListItem, error)
	ListIssueChildren func(parent int) ([]ChildTarget, error)
	OpenIssue         IssueOpenFunc
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
	newPaneFieldPlan
	newPaneFieldAgent
)

// newPaneMode is the form's input lane: the classic free prompt or the OPEN
// issue picker.
type newPaneMode int

const (
	newPaneModePrompt newPaneMode = iota
	newPaneModeIssue
)

// newPaneStep is the wizard position: the mode form (step 1) or the
// per-child agent assignment that issue submissions open (step 2).
type newPaneStep int

const (
	newPaneStepForm newPaneStep = iota
	newPaneStepAssign
)

type newPaneForm struct {
	prompt     textarea.Model
	agentCount map[string]int
	// promptAgentCount stashes the prompt-mode launch counts while a
	// single-agent context (issue mode, plan fan-out) collapses agentCount to
	// one selection; returning to plain prompt mode restores it. nil when
	// nothing is stashed.
	promptAgentCount map[string]int
	// singleAgent remembers the single-select choice (issue mode, plan
	// fan-out) so a mode round trip re-selects it instead of re-deriving the
	// default from the restored prompt counts. "" until a selection is made.
	singleAgent string
	agentIndex  int
	focus       newPaneField
	launching   bool
	notice      string
	err         string
	attach      *AttachTarget

	// planFanout is the prompt-mode checkbox: decompose the prompt via the
	// fanout-plan skill instead of launching a plain agent pane.
	planFanout bool

	// Issue mode state. The default-agent choice reuses the prompt-mode count
	// selector (agentCount/agentIndex), constrained to a single-agent selection.
	mode        newPaneMode
	step        newPaneStep
	issuePicker pickerState
	assign      assignState
	assignGen   int // monotonic load generation; survives esc so stale loads drop
	selIssue    int

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
		ListIssueChildren: opts.ListIssueChildren,
		OpenIssue:         opts.OpenIssue,
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
	prompt.Prompt = ""
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
		prompt:     prompt,
		agentCount: defaultAgentCounts(defaultAgent),
		agentIndex: defaultAgentIndex(defaultAgent),
		focus:      newPaneFieldMain,
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
			cmd := m.cycleNewPaneMode(msg.String())
			m.syncAgentSelectionForMode()
			return m, cmd
		case newPaneFieldPlan:
			m.newPane.planFanout = !m.newPane.planFanout
			m.syncAgentSelectionForMode()
			return m, nil
		case newPaneFieldAgent:
			m.adjustNewPaneAgent(msg.String())
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
			if m.newPane.focus == newPaneFieldAgent {
				m.moveNewPaneAgent(msg.String())
			} else {
				m.moveNewPaneFocus(msg.String())
			}
			return m, nil
		}
	case "enter":
		return m, m.submitNewPane()
	case "ctrl+o":
		if m.newPane.mode == newPaneModeIssue {
			return m, m.openSelectedIssueCmd()
		}
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
// wired, and offers the plan fan-out checkbox only in a non-attach prompt form.
func (m model) newPaneFocusOrder() []newPaneField {
	order := make([]newPaneField, 0, 4)
	if len(m.availableNewPaneModes()) > 1 {
		order = append(order, newPaneFieldMode)
	}
	order = append(order, newPaneFieldMain)
	if m.newPane.mode == newPaneModePrompt && m.newPane.attach == nil {
		order = append(order, newPaneFieldPlan)
	}
	order = append(order, newPaneFieldAgent)
	return order
}

func (m *model) moveNewPaneAgent(key string) {
	switch key {
	case "up":
		m.newPane.agentIndex = (m.newPane.agentIndex + len(launchAgents) - 1) % len(launchAgents)
	default:
		m.newPane.agentIndex = (m.newPane.agentIndex + 1) % len(launchAgents)
	}
}

// newPaneSingleAgentMode reports whether the agent selector enforces a
// single-agent (sum == 1) selection: issue mode always, prompt mode when the
// plan fan-out checkbox is on.
func (m model) newPaneSingleAgentMode() bool {
	if m.newPane.mode == newPaneModeIssue {
		return true
	}
	return m.newPane.planFanout
}

// selectSingleNewPaneAgent sets the focused agent's count to 1 and zeros the
// others. Never toggles to zero, so the selection always sums to exactly 1.
// The choice is remembered in singleAgent so it survives a mode round trip.
func (m *model) selectSingleNewPaneAgent() {
	focused := launchAgents[m.newPane.agentIndex]
	for _, agentName := range launchAgents {
		if agentName == focused {
			m.newPane.agentCount[agentName] = 1
		} else {
			m.newPane.agentCount[agentName] = 0
		}
	}
	m.newPane.singleAgent = focused
}

// syncAgentSelectionForMode reconciles the shared agent counts with the
// current selection context. Entering a single-agent context (issue mode, plan
// fan-out checkbox) stashes the prompt-mode launch counts before collapsing
// them, and returning to plain prompt mode restores the stash — so peeking at
// issue mode or toggling the checkbox never changes how many panes a prompt
// submit launches. The single-select choice survives the same round trip via
// singleAgent, so re-entering issue mode keeps the agent the user picked there.
func (m *model) syncAgentSelectionForMode() {
	if m.newPaneSingleAgentMode() {
		if m.newPane.promptAgentCount == nil {
			m.newPane.promptAgentCount = maps.Clone(m.newPane.agentCount)
		}
		if idx := slices.Index(launchAgents, m.newPane.singleAgent); idx >= 0 {
			m.newPane.agentIndex = idx
			m.selectSingleNewPaneAgent()
			return
		}
		m.normalizeSingleAgentSelection()
		return
	}
	if m.newPane.promptAgentCount != nil {
		m.newPane.agentCount = m.newPane.promptAgentCount
		m.newPane.promptAgentCount = nil
	}
}

// normalizeSingleAgentSelection collapses the agent counts to a single
// selection whenever the current mode enforces one. Prompt mode shares the
// count map, so a stale multi-count (or all-zero) selection could otherwise
// carry into issue mode and launch a default agent that differs from what the
// selector shows. It keeps an existing single selection; when zero or more than
// one agent is selected it falls back to the cursor, which starts on the form's
// configured default agent (so a codex default is not silently reset to claude).
func (m *model) normalizeSingleAgentSelection() {
	if !m.newPaneSingleAgentMode() {
		return
	}
	if sel := m.singleSelectedAgentIndex(); sel >= 0 {
		m.newPane.agentIndex = sel
	}
	m.selectSingleNewPaneAgent()
}

// singleSelectedAgentIndex returns the index of the sole agent at a non-zero
// count, or -1 when zero or more than one agent is selected.
func (m model) singleSelectedAgentIndex() int {
	idx := -1
	for i, agentName := range launchAgents {
		if m.newPane.agentCount[agentName] > 0 {
			if idx >= 0 {
				return -1 // more than one agent selected
			}
			idx = i
		}
	}
	return idx
}

func (m *model) adjustNewPaneAgent(key string) {
	if m.newPaneSingleAgentMode() {
		// left/right/space all collapse to selecting the focused agent; a
		// re-press on the already-selected row is a no-op (still sums to 1).
		m.selectSingleNewPaneAgent()
		return
	}
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
	if m.newPane.planFanout && len(agents) != 1 {
		m.newPane.err = "plan fan-out launches one coordinator agent; select exactly one"
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
		Prompt:     prompt,
		Agents:     agents,
		PlanFanout: m.newPane.planFanout,
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
	// In the tmux popup the -T frame already shows the title, so drop the
	// duplicate in-content heading; the in-process fallback keeps it.
	var sections []string
	if !m.promptOnly {
		sections = append(sections, titleStyle.Render(title))
	}
	if len(m.availableNewPaneModes()) > 1 {
		sections = append(sections, m.newPaneFieldView(newPaneFieldMode, "Mode", m.newPaneModeTabsView(), false))
	}
	switch m.newPane.mode {
	case newPaneModeIssue:
		sections = append(sections,
			m.newPaneFieldView(newPaneFieldMain, "Issue", m.pickerView(m.newPane.issuePicker, "no open issues"), true),
			m.newPaneFieldView(newPaneFieldAgent, "Agent", m.agentSelectorView(), false),
		)
	default:
		promptSection := m.newPaneFieldView(newPaneFieldMain, "Prompt", m.newPane.prompt.View(), true)
		if m.newPane.focus == newPaneFieldMain && m.newPane.completing {
			promptSection += "\n" + m.completionPopupView()
		}
		sections = append(sections, promptSection)
		if m.newPane.attach == nil {
			sections = append(sections, m.planFanoutCheckboxView())
		}
		sections = append(sections, m.newPaneFieldView(newPaneFieldAgent, "Agent", m.agentSelectorView(), false))
	}
	footers := make([]string, 0, 3)
	if m.newPane.launching {
		footers = append(footers, dimStyle.Render("creating pane..."))
	}
	if m.newPane.notice != "" {
		lines = append(lines, dimStyle.Render(m.newPane.notice))
	}
	if m.newPane.err != "" {
		footers = append(footers, errStyle.Render("error: "+m.newPane.err))
	}
	footers = append(footers, dimStyle.Render(m.newPaneHint()))
	content := strings.Join(sections, "\n\n")
	if len(footers) > 0 {
		if content != "" {
			content += "\n"
		}
		content += strings.Join(footers, "\n")
	}
	return m.renderNewPaneModal(content)
}

// renderNewPaneModal frames the new-pane content. The tmux popup already draws
// a border, so promptOnly renders borderless with a one-cell content gutter;
// the in-process overlay keeps the modal border.
func (m model) renderNewPaneModal(content string) string {
	if m.promptOnly {
		return popupContentStyle.Width(m.modalWidth()).Render(content)
	}
	return modalStyle.Width(m.modalWidth()).Render(content)
}

func (m model) newPaneHint() string {
	if m.newPane.mode != newPaneModePrompt {
		return "enter next  ctrl+o open issue  type to filter  tab field  esc cancel"
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
	marker := plainItemMarker
	if focused {
		marker = selectedItemMarker
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

// planFanoutCheckboxView renders the prompt-mode plan fan-out toggle. When on,
// the launch hands the prompt to the fanout-plan skill instead of starting a
// plain agent pane.
func (m model) planFanoutCheckboxView() string {
	marker := plainItemMarker
	if m.newPane.focus == newPaneFieldPlan {
		marker = selectedItemMarker
	}
	box := "[ ]"
	if m.newPane.planFanout {
		box = "[x]"
	}
	text := box + " decompose via /fanout plan"
	if m.newPane.planFanout {
		text = titleStyle.Render(text)
	} else {
		text = dimStyle.Render(text)
	}
	return marker + text
}

func (m model) agentSelectorView() string {
	lines := make([]string, 0, len(launchAgents))
	for i, agentName := range launchAgents {
		count := m.newPane.agentCount[agentName]
		marker := plainItemMarker
		if m.newPane.focus == newPaneFieldAgent && m.newPane.agentIndex == i {
			marker = selectedItemMarker
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

// selectedDefaultAgent returns the single-agent selection's agent name: the one
// agent left at a non-zero count. The single-agent selector keeps the counts
// summing to exactly one; the fallback to defaultLaunchAgent is defensive.
func (m model) selectedDefaultAgent() string {
	for _, agentName := range launchAgents {
		if m.newPane.agentCount[agentName] > 0 {
			return agentName
		}
	}
	return defaultLaunchAgent
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
		// No modal border in the popup; popupContentStyle owns the one-cell
		// gutter inside the pty that tmux display-popup provides.
		return clampInt(m.width, 40, 106)
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
