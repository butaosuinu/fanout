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

// assignState is the step-2 per-target agent assignment. target identifies
// the pending selection ("#123") and gen its load generation, so a stale load
// — even one for the same target after an esc + re-enter — cannot populate a
// newer selection's rows or finalize with outdated data.
type assignState struct {
	loading bool
	err     string
	target  string
	title   string
	gen     int
	rows    []assignRow
	index   int
}

type assignRow struct {
	target   string // child issue number ("123")
	label    string
	wave     string
	agentIdx int // launchAgents index
}

type newPaneAssignLoadedMsg struct {
	mode     newPaneMode
	target   string
	gen      int
	children []ChildTarget
	err      error
}

// submitNewPanePicker handles enter on the issue picker: it validates the list
// state, then either opens the agent assignment step or (when no target lister
// is wired) submits with the default agent alone.
func (m *model) submitNewPanePicker() tea.Cmd {
	p := m.activePicker()
	if p == nil {
		return nil
	}
	if p.loading {
		m.newPane.err = "list is still loading"
		m.newPane.notice = ""
		return nil
	}
	if p.err != "" {
		m.newPane.err = "list failed: " + p.err
		m.newPane.notice = ""
		return nil
	}
	item, ok := p.selectedItem()
	if !ok {
		m.newPane.err = "nothing selected"
		m.newPane.notice = ""
		return nil
	}
	m.newPane.err = ""
	m.newPane.notice = ""
	switch m.newPane.mode {
	case newPaneModeIssue:
		m.newPane.selIssue = item.number
		if m.opts.ListIssueChildren == nil {
			return m.finalizeNewPaneModeSubmit()
		}
		return m.beginAssignStep(item.key, item.key+" "+item.title)
	default:
		return nil
	}
}

func (m *model) beginAssignStep(target, title string) tea.Cmd {
	m.newPane.assignGen++
	gen := m.newPane.assignGen
	m.newPane.step = newPaneStepAssign
	m.newPane.assign = assignState{loading: true, target: target, title: title, gen: gen}
	mode := m.newPane.mode
	switch mode {
	case newPaneModeIssue:
		list := m.opts.ListIssueChildren
		num := m.newPane.selIssue
		return func() tea.Msg {
			children, err := list(num)
			return newPaneAssignLoadedMsg{mode: mode, target: target, gen: gen, children: children, err: err}
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
	defaultIdx := defaultAgentIndex(m.selectedDefaultAgent())
	var overrides map[string]string
	for _, row := range m.newPane.assign.rows {
		if row.agentIdx == defaultIdx {
			continue
		}
		if overrides == nil {
			overrides = map[string]string{}
		}
		overrides[row.target] = launchAgents[clampInt(row.agentIdx, 0, len(launchAgents)-1)]
	}
	return overrides
}

// finalizeNewPaneModeSubmit builds the issue LaunchRequest and hands it to the
// prompt result (popup) or the launch dispatch (in-process form).
func (m *model) finalizeNewPaneModeSubmit() tea.Cmd {
	req := LaunchRequest{
		DefaultAgent:   m.selectedDefaultAgent(),
		AgentOverrides: m.assignOverrides(),
	}
	switch m.newPane.mode {
	case newPaneModeIssue:
		req.Mode = LaunchModeIssue
		req.Issue = m.newPane.selIssue
	default:
		return nil
	}
	if !m.promptOnly {
		if req.Mode == LaunchModeIssue && m.opts.LaunchIssue == nil {
			m.newPane.err = "issue launcher is not configured"
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

// assignViewOverhead is the non-row height of the assign view: title,
// subtitle, hint, an error line, scroll markers, and the modal frame.
const assignViewOverhead = 8

// assignRowWindow returns the visible row range, sliding so the selected row
// stays in view; a taller-than-pty list would be top-clipped by the renderer.
func (m model) assignRowWindow() (start, end int) {
	rows := len(m.newPane.assign.rows)
	visible := pickerMaxRows
	if m.height > 0 {
		height := m.height
		if m.promptOnly {
			height = popupContentAvailableHeight(height)
		}
		visible = clampInt(height-assignViewOverhead, 3, pickerMaxRows)
	}
	if rows <= visible {
		return 0, rows
	}
	start = clampInt(m.newPane.assign.index-visible+1, 0, rows-visible)
	start = min(start, m.newPane.assign.index)
	return start, start + visible
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
		defaultIdx := defaultAgentIndex(m.selectedDefaultAgent())
		start, end := m.assignRowWindow()
		if start > 0 {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
		}
		for i := start; i < end; i++ {
			row := a.rows[i]
			marker := plainItemMarker
			if i == a.index {
				marker = selectedItemMarker
			}
			agentName := launchAgents[clampInt(row.agentIdx, 0, len(launchAgents)-1)]
			token := "[" + agentName + "]"
			if row.agentIdx != defaultIdx {
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
		if end < len(a.rows) {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(a.rows)-end)))
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
	return m.renderNewPaneModal(strings.Join(lines, "\n"))
}
