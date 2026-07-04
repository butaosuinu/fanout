package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const helpPopupOpeningNotice = "opening help popup..."

type helpEntry struct {
	Key         string
	Description string
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
		height = 18
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

func (m model) helpView() string {
	monitor := []helpEntry{
		{"n", "New agent pane"},
		{"a", "Attach agent to worktree"},
		{"A", "Worktree terminal"},
		{"t", "Project root terminal"},
		{"j/k", "Move selection"},
		{"[ / ]", "Prev / next Session"},
		{"1-9", "Jump to Nth pane"},
		{"/", "Filter rows"},
		{"Enter/o", "Focus pane"},
		{"Z", "Focus + zoom pane"},
		{"p", "Peek output"},
		{"c/x", "Close pane"},
		{"m", "Merge branch"},
		{"X", "Cleanup parent"},
		{"q", "Quit TUI"},
	}
	newPane := []helpEntry{
		{"Ctrl+J", "Prompt newline"},
		{"Tab", "Move fields"},
		{"Up/Down", "Pick agent / row"},
		{"Space", "Toggle agent"},
		{"Left/Right", "Mode / agent"},
		{"@", "File completion"},
		{"Enter", "Create / next"},
		{"Esc", "Cancel / back"},
	}
	columnWidth := m.helpColumnWidth()
	lines := make([]string, 0, 5)
	if !m.helpOnly {
		lines = append(lines, titleStyle.Render("Keyboard shortcuts"), "")
	}
	lines = append(lines,
		lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.helpColumn("Monitor", monitor, columnWidth),
			"  ",
			m.helpColumn("New pane popup", newPane, columnWidth),
		),
		"",
		dimStyle.Render("Esc / q / ? close"),
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
		lines = append(lines, m.helpRow(entry.Key, entry.Description, width))
	}
	return strings.Join(lines, "\n")
}

func (m model) helpRow(key, description string, width int) string {
	keyText := key
	if !strings.HasPrefix(keyText, "[") {
		keyText = "[" + keyText + "]"
	}
	keyText = titleStyle.Width(13).Render(keyText)
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
