package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const helpPopupOpeningNotice = "opening help popup..."

type helpEntry struct {
	Key            string
	Description    string
	DisabledReason string
}

// HelpPopupFunc opens the keyboard shortcut help in an external surface, such as
// a tmux display-popup.
type HelpPopupFunc func() error

// HelpPopupOptions configures the standalone help popup program.
type HelpPopupOptions struct {
	Width  int
	Height int
}

// RunHelpPopup opens only the keyboard shortcut help and exits when the user
// presses Esc, q, ?, or Ctrl+C.
func RunHelpPopup(opts HelpPopupOptions) error {
	width := opts.Width
	if width <= 0 {
		width = 76
	}
	height := opts.Height
	if height <= 0 {
		height = 21
	}
	m := newModel(Options{})
	m.helpOnly = true
	m.mode = modeHelp
	m.width = width
	m.height = height
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m *model) openHelpPopupCmd() tea.Cmd {
	if m.helpHasDisabledRuntimeActions() {
		// The external popup is a fresh model and cannot carry the selected row's
		// backend. Keep backend-specific disabled state visible in the inline view.
		m.mode = modeHelp
		return nil
	}
	popup := m.opts.HelpPopup
	if popup == nil {
		m.mode = modeHelp
		return nil
	}
	m.notice = helpPopupOpeningNotice
	m.helpPopupOpen = true
	return func() tea.Msg {
		return helpPopupDoneMsg{err: popup()}
	}
}

func (m model) helpHasDisabledRuntimeActions() bool {
	if m.runtimeActionDisabledReason(nil, "launch") != "" {
		return true
	}
	pane, ok := m.selectedPane()
	if !ok {
		return false
	}
	return m.runtimeActionDisabledReason(&pane, "runtime action") != "" || m.peekDisabledReason(pane) != ""
}

func (m model) helpView() string {
	launchDisabled := m.runtimeActionDisabledReason(nil, "launch")
	var paneDisabled, peekDisabled string
	if pane, ok := m.selectedPane(); ok {
		paneDisabled = m.runtimeActionDisabledReason(&pane, "runtime action")
		peekDisabled = m.peekDisabledReason(pane)
	}
	monitor := []helpEntry{
		{"n", "New agent pane", launchDisabled},
		{"s", "Settings", ""},
		{"a", "Attach agent to worktree", paneDisabled},
		{"A", "Worktree terminal", firstNonEmpty(paneDisabled, launchDisabled)},
		{"t", "Project root terminal", launchDisabled},
		{"j/k", "Move selection", ""},
		{"[ / ]", "Prev / next Session", ""},
		{"1-9", "Jump to Nth pane", ""},
		{"/", "Filter rows", ""},
		{"Enter/o", "Focus pane", paneDisabled},
		{"Z", "Focus + zoom pane", paneDisabled},
		{"p", "Peek output", peekDisabled},
		{"v", "Auto/compact/full view", ""},
		{"c/x", "Close pane", paneDisabled},
		{"m", "Merge branch", ""},
		{"X", "Cleanup parent", paneDisabled},
	}
	newPane := []helpEntry{
		{"Ctrl+J", "Prompt newline", launchDisabled},
		{"Tab", "Move fields", launchDisabled},
		{"Up/Down", "Pick agent / row", launchDisabled},
		{"Space", "Toggle agent", launchDisabled},
		{"Left/Right", "Mode / agent", launchDisabled},
		{"@", "File completion", launchDisabled},
		{"Ctrl+O", "Open issue", launchDisabled},
		{"Enter", "Create / next", launchDisabled},
		{"Esc", "Cancel / back", launchDisabled},
	}
	columnWidth := m.helpColumnWidth()
	// No blank lines around the columns: the in-TUI modal fallback must stay
	// within a standard 24-row terminal (content 20 + border/padding 4).
	lines := make([]string, 0, 4)
	if !m.helpOnly {
		lines = append(lines, titleStyle.Render("Keyboard shortcuts"))
	}
	footer := "Esc / q / ? close"
	if reason := firstNonEmpty(launchDisabled, paneDisabled, peekDisabled); reason != "" {
		if summary, _, ok := strings.Cut(reason, ";"); ok {
			reason = summary
		}
		footer = "disabled: " + reason + " · " + footer
	}
	lines = append(lines,
		lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.helpColumn("Monitor", monitor, columnWidth),
			"  ",
			m.helpColumn("New pane popup", newPane, columnWidth),
		),
		dimStyle.Render(footer),
	)
	content := strings.Join(lines, "\n")
	if m.helpOnly {
		return popupContentStyle.Width(m.helpModalWidth()).Render(content)
	}
	return modalStyle.Width(m.helpModalWidth()).Render(content)
}

func (m model) helpColumn(title string, entries []helpEntry, width int) string {
	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, titleStyle.Render(title))
	for _, entry := range entries {
		lines = append(lines, m.helpRow(entry, width))
	}
	return strings.Join(lines, "\n")
}

func (m model) helpRow(entry helpEntry, width int) string {
	keyText := entry.Key
	if !strings.HasPrefix(keyText, "[") {
		keyText = "[" + keyText + "]"
	}
	keyText = titleStyle.Width(13).Render(keyText)
	description := entry.Description
	if entry.DisabledReason != "" {
		description = dimStyle.Render(description + " [disabled]")
	}
	return lipgloss.NewStyle().Width(width).Render(keyText + description)
}

func (m model) helpModalWidth() int {
	if m.width <= 0 {
		return 76
	}
	if m.helpOnly {
		return clampInt(m.width, 48, 88)
	}
	return clampInt(m.width-4, 48, 76)
}

func (m model) helpColumnWidth() int {
	return max(22, (m.helpModalWidth()-6)/2)
}
