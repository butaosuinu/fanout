package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LaunchRequest describes one manual pane launch requested from the TUI.
type LaunchRequest struct {
	Prompt string
	Agents []string
	Slug   string
}

// LaunchFunc creates a manual fanout pane for a TUI request. It returns an
// optional notice (e.g. a tolerated base-refresh skip) to surface on success.
type LaunchFunc func(LaunchRequest) (notice string, err error)

type newPaneField int

const (
	newPaneFieldPrompt newPaneField = iota
	newPaneFieldAgent
	newPaneFieldSlug
	newPaneFieldCount
)

type newPaneForm struct {
	prompt     textarea.Model
	slug       textinput.Model
	agentCount map[string]int
	agentIndex int
	focus      newPaneField
	launching  bool
	err        string
}

func (m *model) openNewPaneForm() {
	m.mode = modeNewPane
	m.notice = ""
	m.newPane = newNewPaneForm(m.opts.DefaultAgent, m.inputContentWidth())
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
	slug.Width = textinputWidth(width, slug.Prompt)
	slug.Blur()

	if defaultAgent != "codex" {
		defaultAgent = defaultLaunchAgent
	}
	return newPaneForm{
		prompt:     prompt,
		slug:       slug,
		agentCount: defaultAgentCounts(defaultAgent),
		agentIndex: defaultAgentIndex(defaultAgent),
		focus:      newPaneFieldPrompt,
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
			m.adjustNewPaneAgent(msg.String())
			return m, nil
		}
	case "up", "down":
		if m.newPane.focus != newPaneFieldPrompt {
			if m.newPane.focus == newPaneFieldAgent {
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
	if m.opts.LaunchPane == nil {
		m.newPane.err = "new pane launcher is not configured"
		return nil
	}
	m.newPane.err = ""
	m.newPane.launching = true
	req := LaunchRequest{
		Prompt: prompt,
		Agents: agents,
		Slug:   strings.TrimSpace(m.newPane.slug.Value()),
	}
	launch := m.opts.LaunchPane
	return func() tea.Msg {
		notice, err := launch(req)
		return launchPaneMsg{notice: notice, count: len(agents), err: err}
	}
}

func (m model) newPaneView() string {
	lines := []string{
		titleStyle.Render("New agent pane"),
		m.newPaneFieldView(newPaneFieldPrompt, "Prompt", m.newPane.prompt.View(), true),
		m.newPaneFieldView(newPaneFieldAgent, "Agent", m.agentSelectorView(), false),
		m.newPaneFieldView(newPaneFieldSlug, "Slug", m.newPane.slug.View(), true),
	}
	if m.newPane.launching {
		lines = append(lines, dimStyle.Render("creating pane..."))
	}
	if m.newPane.err != "" {
		lines = append(lines, errStyle.Render("error: "+m.newPane.err))
	}
	lines = append(lines, dimStyle.Render("enter create  shift+enter newline  ctrl+j newline  tab field  up/down agent  left/right count  space toggle  esc cancel"))
	return modalStyle.Width(m.modalWidth()).Render(strings.Join(lines, "\n"))
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

// textinputWidth converts a desired total rendered width into the bubbles
// textinput Width setting. Unlike textarea (whose SetWidth is the full rendered
// width, prompt included), a textinput renders its prompt plus one trailing
// cursor cell outside of Width, so the two must be offset to frame to the same
// width.
func textinputWidth(rendered int, prompt string) int {
	return rendered - lipgloss.Width(prompt) - 1
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
