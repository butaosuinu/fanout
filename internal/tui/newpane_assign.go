package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ChildTarget is one OPEN child issue offered by the issue-mode agent
// assignment step.
type ChildTarget struct {
	Number int
	Title  string
	Wave   string
}

// PlanTaskItem is one plan-spec task offered by the plan-mode agent
// assignment step.
type PlanTaskItem struct {
	ID    string
	Title string
	Wave  string
}

// assignState is the step-2 per-target agent assignment. target identifies
// the pending selection ("#123" or a slug) so a stale load cannot populate a
// newer selection's rows.
type assignState struct {
	loading bool
	err     string
	target  string
	title   string
	rows    []assignRow
	index   int
}

type assignRow struct {
	target   string // child issue number ("123") or plan task id
	label    string
	wave     string
	agentIdx int // launchAgents index
}

type newPaneAssignLoadedMsg struct {
	mode     newPaneMode
	target   string
	children []ChildTarget
	tasks    []PlanTaskItem
	err      error
}

// submitNewPanePicker handles enter on the issue/plan picker: it validates
// the list state, then either opens the agent assignment step or (when no
// target lister is wired) submits with the default agent alone.
func (m *model) submitNewPanePicker() tea.Cmd {
	p := m.activePicker()
	if p == nil {
		return nil
	}
	if p.loading {
		m.newPane.err = "list is still loading"
		return nil
	}
	if p.err != "" {
		m.newPane.err = "list failed: " + p.err
		return nil
	}
	item, ok := p.selectedItem()
	if !ok {
		m.newPane.err = "nothing selected"
		return nil
	}
	m.newPane.err = ""
	switch m.newPane.mode {
	case newPaneModeIssue:
		m.newPane.selIssue = item.number
		if m.opts.ListIssueChildren == nil {
			return m.finalizeNewPaneModeSubmit()
		}
		return m.beginAssignStep(item.key, item.key+" "+item.title)
	case newPaneModePlan:
		m.newPane.selPlan = item.key
		if m.opts.ListPlanTasks == nil {
			return m.finalizeNewPaneModeSubmit()
		}
		return m.beginAssignStep(item.key, "plan "+item.key)
	default:
		return nil
	}
}

func (m *model) beginAssignStep(target, title string) tea.Cmd {
	m.newPane.step = newPaneStepAssign
	m.newPane.assign = assignState{loading: true, target: target, title: title}
	mode := m.newPane.mode
	switch mode {
	case newPaneModeIssue:
		list := m.opts.ListIssueChildren
		num := m.newPane.selIssue
		return func() tea.Msg {
			children, err := list(num)
			return newPaneAssignLoadedMsg{mode: mode, target: target, children: children, err: err}
		}
	case newPaneModePlan:
		list := m.opts.ListPlanTasks
		slug := m.newPane.selPlan
		return func() tea.Msg {
			tasks, err := list(slug)
			return newPaneAssignLoadedMsg{mode: mode, target: target, tasks: tasks, err: err}
		}
	default:
		return nil
	}
}

func buildAssignRows(msg newPaneAssignLoadedMsg, defaultAgentIdx int) []assignRow {
	switch msg.mode {
	case newPaneModeIssue:
		rows := make([]assignRow, 0, len(msg.children))
		for _, child := range msg.children {
			rows = append(rows, assignRow{
				target:   strconv.Itoa(child.Number),
				label:    fmt.Sprintf("#%d %s", child.Number, child.Title),
				wave:     child.Wave,
				agentIdx: defaultAgentIdx,
			})
		}
		return rows
	case newPaneModePlan:
		rows := make([]assignRow, 0, len(msg.tasks))
		for _, task := range msg.tasks {
			label := task.ID
			if task.Title != "" {
				label += "  " + task.Title
			}
			rows = append(rows, assignRow{
				target:   task.ID,
				label:    label,
				wave:     task.Wave,
				agentIdx: defaultAgentIdx,
			})
		}
		return rows
	default:
		return nil
	}
}

func (m model) updateNewPaneAssign(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a := &m.newPane.assign
	switch msg.String() {
	case "esc":
		m.newPane.step = newPaneStepForm
		m.newPane.assign = assignState{}
		m.newPane.err = ""
		return m, nil
	case "up", "ctrl+p":
		m.moveAssignRow(-1)
		return m, nil
	case "down", "ctrl+n":
		m.moveAssignRow(1)
		return m, nil
	case "left":
		m.cycleAssignRowAgent(-1)
		return m, nil
	case "right", " ":
		m.cycleAssignRowAgent(1)
		return m, nil
	case "enter":
		if a.loading {
			m.newPane.err = "targets are still loading"
			return m, nil
		}
		if a.err != "" {
			return m, m.beginAssignStep(a.target, a.title)
		}
		m.newPane.err = ""
		return m, m.finalizeNewPaneModeSubmit()
	}
	return m, nil
}

func (m *model) moveAssignRow(delta int) {
	n := len(m.newPane.assign.rows)
	if n == 0 {
		return
	}
	m.newPane.assign.index = (m.newPane.assign.index + delta + n) % n
}

func (m *model) cycleAssignRowAgent(delta int) {
	a := &m.newPane.assign
	if a.index < 0 || a.index >= len(a.rows) {
		return
	}
	n := len(launchAgents)
	a.rows[a.index].agentIdx = (a.rows[a.index].agentIdx + delta + n) % n
}

// assignOverrides returns only rows whose agent differs from the default, so
// the launch mirrors what repeatable --agent target=name flags would pass.
func (m model) assignOverrides() map[string]string {
	var overrides map[string]string
	for _, row := range m.newPane.assign.rows {
		if row.agentIdx == m.newPane.agentChoice {
			continue
		}
		if overrides == nil {
			overrides = map[string]string{}
		}
		overrides[row.target] = launchAgents[clampInt(row.agentIdx, 0, len(launchAgents)-1)]
	}
	return overrides
}

// finalizeNewPaneModeSubmit builds the issue/plan LaunchRequest and hands it
// to the prompt result (popup) or the launch dispatch (in-process form).
func (m *model) finalizeNewPaneModeSubmit() tea.Cmd {
	req := LaunchRequest{
		DefaultAgent:   launchAgents[clampInt(m.newPane.agentChoice, 0, len(launchAgents)-1)],
		AgentOverrides: m.assignOverrides(),
	}
	switch m.newPane.mode {
	case newPaneModeIssue:
		req.Mode = LaunchModeIssue
		req.Issue = m.newPane.selIssue
	case newPaneModePlan:
		req.Mode = LaunchModePlan
		req.Plan = m.newPane.selPlan
	default:
		return nil
	}
	if !m.promptOnly {
		if req.Mode == LaunchModeIssue && m.opts.LaunchIssue == nil {
			m.newPane.err = "issue launcher is not configured"
			return nil
		}
		if req.Mode == LaunchModePlan && m.opts.LaunchPlan == nil {
			m.newPane.err = "plan launcher is not configured"
			return nil
		}
	}
	m.newPane.err = ""
	m.newPane.launching = true
	if m.promptOnly {
		m.promptResult = req
		m.promptDone = true
		return tea.Quit
	}
	return m.launchNewPaneRequest(req)
}

func (m *model) launchIssueSessionRequest(req LaunchRequest) tea.Cmd {
	agentName := strings.TrimSpace(req.DefaultAgent)
	switch {
	case req.Issue <= 0:
		m.notice = "new session: issue number is required"
	case agentName == "":
		m.notice = "new session: select an agent"
	case m.opts.LaunchIssue == nil:
		m.notice = "new session: issue launcher is not configured"
	default:
		m.newPane.launching = true
		m.notice = fmt.Sprintf("starting session for #%d...", req.Issue)
		launch := m.opts.LaunchIssue
		num, overrides := req.Issue, req.AgentOverrides
		return func() tea.Msg {
			notice, err := launch(num, agentName, overrides)
			return launchPaneMsg{notice: notice, count: 1, err: err}
		}
	}
	return nil
}

func (m *model) launchPlanSessionRequest(req LaunchRequest) tea.Cmd {
	agentName := strings.TrimSpace(req.DefaultAgent)
	slug := strings.TrimSpace(req.Plan)
	switch {
	case slug == "":
		m.notice = "new session: plan slug is required"
	case agentName == "":
		m.notice = "new session: select an agent"
	case m.opts.LaunchPlan == nil:
		m.notice = "new session: plan launcher is not configured"
	default:
		m.newPane.launching = true
		m.notice = fmt.Sprintf("starting plan %s...", slug)
		launch := m.opts.LaunchPlan
		overrides := req.AgentOverrides
		return func() tea.Msg {
			notice, err := launch(slug, agentName, overrides)
			return launchPaneMsg{notice: notice, count: 1, err: err}
		}
	}
	return nil
}

func (m model) newPaneAssignView() string {
	a := m.newPane.assign
	lines := []string{
		titleStyle.Render("Assign agents"),
		dimStyle.Render(a.title),
	}
	switch {
	case a.loading:
		lines = append(lines, dimStyle.Render("loading targets…"))
	case a.err != "":
		lines = append(lines, errStyle.Render("error: "+a.err))
	default:
		width := m.inputContentWidth()
		for i, row := range a.rows {
			marker := "  "
			if i == a.index {
				marker = "> "
			}
			agentName := launchAgents[clampInt(row.agentIdx, 0, len(launchAgents)-1)]
			token := "[" + agentName + "]"
			if row.agentIdx != m.newPane.agentChoice {
				token = titleStyle.Render(token)
			} else {
				token = dimStyle.Render(token)
			}
			label := row.label
			if row.wave != "" {
				label += " (wave " + row.wave + ")"
			}
			lines = append(lines, marker+token+" "+truncateToWidth(label, width-10))
		}
	}
	if m.newPane.launching {
		lines = append(lines, dimStyle.Render("creating pane..."))
	}
	if m.newPane.err != "" {
		lines = append(lines, errStyle.Render("error: "+m.newPane.err))
	}
	switch {
	case a.err != "":
		lines = append(lines, dimStyle.Render("enter retry  esc back"))
	case !a.loading:
		lines = append(lines, dimStyle.Render("enter launch  ←/→ agent  ↑/↓ row  esc back"))
	}
	return modalStyle.Width(m.modalWidth()).Render(strings.Join(lines, "\n"))
}
