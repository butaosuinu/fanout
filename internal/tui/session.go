package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionSummary struct {
	Parent  string
	Start   int
	Total   int
	Merged  int
	Pending int
	Blocked int
	Live    int
	Active  bool
}

func (m model) jumpSession(delta int) (tea.Model, tea.Cmd) {
	sessions := buildSessionSummaries(m.panes, m.table.Cursor())
	if len(sessions) == 0 {
		m.notice = "no sessions to jump to"
		return m, nil
	}
	active := max(activeSessionIndex(sessions), 0)
	next := (active + delta + len(sessions)) % len(sessions)
	m.moveTableCursorTo(sessions[next].Start)
	m.refreshDetail()
	return m, m.peekSelectedCmd(false)
}

func (m model) jumpToOrdinal(n int) (tea.Model, tea.Cmd) {
	if n > len(m.panes) {
		m.notice = fmt.Sprintf("no pane %d in the current list", n)
		return m, nil
	}
	m.moveTableCursorTo(n - 1)
	m.refreshDetail()
	// Schedule a peek alongside the focus, like every other cursor move, so
	// the detail panel is fresh when the focus is skipped or fails.
	focusCmd := m.focusSelectedCmd()
	return m, tea.Batch(focusCmd, m.peekSelectedCmd(false))
}

func (m *model) moveTableCursorTo(target int) {
	current := m.table.Cursor()
	switch {
	case target > current:
		m.table.MoveDown(target - current)
	case target < current:
		m.table.MoveUp(current - target)
	default:
		m.table.SetCursor(target)
	}
}

type sessionRenderLine struct {
	Text   string
	Active bool
	Header bool
}

func (m model) sessionSidebar(width, height int) string {
	sessions := buildSessionSummaries(m.panes, m.table.Cursor())
	lines := []sessionRenderLine{{Text: fmt.Sprintf("Sessions %d", len(sessions)), Header: true}}
	rowBudget := max(height-2, 0)
	if len(sessions) == 0 {
		lines = append(lines, sessionRenderLine{Text: "(none)"})
	} else {
		lines = append(lines, sessionRows(sessions, rowBudget)...)
	}
	if len(lines) < height {
		lines = append(lines, sessionRenderLine{Text: "[/] session"})
	}
	return renderSessionBlock(lines, width, height, true)
}

func (m model) sessionTopStrip(width, height int) string {
	sessions := buildSessionSummaries(m.panes, m.table.Cursor())
	lines := []sessionRenderLine{
		{Text: fmt.Sprintf("Sessions %d  [/] session", len(sessions)), Header: true},
		{Text: topSessionText(sessions, width)},
		{Text: strings.Repeat("-", max(width, 0))},
	}
	return renderSessionBlock(lines, width, height, false)
}

func renderSessionBlock(lines []sessionRenderLine, width, height int, divider bool) string {
	width = max(width, 1)
	height = max(height, 1)
	contentWidth := width
	if divider {
		contentWidth = max(width-1, 1)
	}
	rendered := make([]string, 0, height)
	for i := range height {
		line := sessionRenderLine{}
		if i < len(lines) {
			line = lines[i]
		}
		text := fixedLine(line.Text, contentWidth)
		if divider {
			text += "|"
		}
		switch {
		case line.Active:
			text = titleStyle.Render(text)
		case line.Header:
			text = dimStyle.Render(text)
		}
		rendered = append(rendered, text)
	}
	return strings.Join(rendered, "\n")
}

func topSessionText(sessions []sessionSummary, width int) string {
	if len(sessions) == 0 {
		return "(none)"
	}
	for limit := len(sessions); limit >= 1; limit-- {
		text := joinSessionRows(sessionRows(sessions, limit))
		if len([]rune(text)) <= width {
			return text
		}
	}
	return joinSessionRows(sessionRows(sessions, 1))
}

func joinSessionRows(rows []sessionRenderLine) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.Text)
	}
	return strings.Join(parts, "  ")
}

func sessionRows(sessions []sessionSummary, limit int) []sessionRenderLine {
	if len(sessions) == 0 || limit <= 0 {
		return nil
	}
	active := max(activeSessionIndex(sessions), 0)
	if len(sessions) <= limit {
		return sessionSummaryLines(sessions)
	}
	if limit < 3 {
		return sessionSummaryLines(sessions[active : active+1])
	}

	slots := limit
	start := clampInt(active-slots/2, 0, len(sessions)-slots)
	end := start + slots
	if start > 0 {
		slots--
	}
	if end < len(sessions) {
		slots--
	}
	slots = max(slots, 1)
	start = clampInt(active-slots/2, 0, len(sessions)-slots)
	end = start + slots

	rows := []sessionRenderLine{}
	if start > 0 {
		rows = append(rows, sessionRenderLine{Text: "..."})
	}
	rows = append(rows, sessionSummaryLines(sessions[start:end])...)
	if end < len(sessions) {
		rows = append(rows, sessionRenderLine{Text: "..."})
	}
	return rows
}

func sessionSummaryLines(sessions []sessionSummary) []sessionRenderLine {
	lines := make([]sessionRenderLine, 0, len(sessions))
	for _, session := range sessions {
		lines = append(lines, sessionRenderLine{
			Text:   sessionSummaryText(session),
			Active: session.Active,
		})
	}
	return lines
}

func sessionSummaryText(session sessionSummary) string {
	marker := " "
	if session.Active {
		marker = ">"
	}
	return fmt.Sprintf(
		"%s %s t%d m%d p%d b%d l%d",
		marker,
		compactParent(session.Parent),
		session.Total,
		session.Merged,
		session.Pending,
		session.Blocked,
		session.Live,
	)
}

func buildSessionSummaries(panes []paneView, cursor int) []sessionSummary {
	if len(panes) == 0 {
		return nil
	}
	if cursor < 0 || cursor >= len(panes) {
		cursor = 0
	}
	activeParent := strings.TrimSpace(panes[cursor].Parent)
	if activeParent == "" {
		activeParent = "-"
	}
	indexByParent := map[string]int{}
	sessions := []sessionSummary{}
	for i, pane := range panes {
		parent := strings.TrimSpace(pane.Parent)
		if parent == "" {
			parent = "-"
		}
		idx, ok := indexByParent[parent]
		if !ok {
			idx = len(sessions)
			indexByParent[parent] = idx
			sessions = append(sessions, sessionSummary{
				Parent: parent,
				Start:  i,
				Active: parent == activeParent,
			})
		}
		sessions[idx].Total++
		if pane.HasMergedPR {
			sessions[idx].Merged++
		}
		if pane.Blocked {
			sessions[idx].Blocked++
		}
		if pane.TmuxState == "live" {
			sessions[idx].Live++
		}
	}
	for i := range sessions {
		sessions[i].Pending = sessions[i].Total - sessions[i].Merged
	}
	return sessions
}

func activeSessionIndex(sessions []sessionSummary) int {
	for i, session := range sessions {
		if session.Active {
			return i
		}
	}
	return -1
}
