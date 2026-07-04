package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type helpEntry struct {
	Key         string
	Description string
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
	lines := []string{
		titleStyle.Render("Keyboard shortcuts"),
		"",
		lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.helpColumn("Monitor", monitor, columnWidth),
			"  ",
			m.helpColumn("New pane popup", newPane, columnWidth),
		),
		"",
		dimStyle.Render("Esc / q / ? close"),
	}
	return modalStyle.Width(m.helpModalWidth()).Render(strings.Join(lines, "\n"))
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
	return clampInt(m.width-4, 48, 76)
}

func (m model) helpColumnWidth() int {
	return max(22, (m.helpModalWidth()-6)/2)
}
