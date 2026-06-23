package tui

import (
	"github.com/charmbracelet/lipgloss"
)

type monitorLayout struct {
	Sidebar        bool
	MainWidth      int
	TableRows      int
	PanelWidth     int
	TopStripHeight int
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

	footer := dimStyle.Render(m.footerText())
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
	if m.notifyErr != "" {
		footer += "\n" + warnStyle.Render("notify: "+m.notifyErr)
	}

	layout := m.monitorLayout()
	body := m.monitorBody(layout)
	if layout.Sidebar {
		sessions := m.sessionSidebar(layout.PanelWidth, lipgloss.Height(body))
		body = lipgloss.JoinHorizontal(lipgloss.Top, sessions, " ", body)
	} else if layout.TopStripHeight > 0 {
		body = lipgloss.JoinVertical(lipgloss.Left, m.sessionTopStrip(layout.PanelWidth, layout.TopStripHeight), body)
	}
	base := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.mode == modeNewPane {
		return overlayCentered(base, m.newPaneView(), m.width, m.height)
	}
	return base
}

func (m *model) resize() {
	if m.width <= 0 {
		return
	}
	if m.mode == modeNewPane {
		inputWidth := m.inputContentWidth()
		m.newPane.prompt.SetWidth(inputWidth)
		m.newPane.slug.Width = textinputWidth(inputWidth, m.newPane.slug.Prompt)
	}
	layout := m.monitorLayout()
	m.table.SetWidth(layout.MainWidth)
	m.table.SetHeight(layout.TableRows)
	m.table.SetColumns(columnsForWidth(layout.MainWidth))
	m.detail.Width = layout.MainWidth
	m.detail.Height = detailHeight
	m.refreshRows()
}

func (m model) monitorLayout() monitorLayout {
	width := max(1, m.width)
	rowBudget := m.height - detailHeight - 5
	layout := monitorLayout{
		MainWidth:  width,
		TableRows:  max(rowBudget, 4),
		PanelWidth: width,
	}
	if width >= sessionSidebarAt {
		layout.Sidebar = true
		layout.PanelWidth = sessionSidebarWidth
		layout.MainWidth = max(40, width-sessionSidebarWidth-1)
		return layout
	}
	if rowBudget >= sessionTopHeight+4 {
		layout.TopStripHeight = sessionTopHeight
		layout.TableRows = max(rowBudget-sessionTopHeight, 4)
	}
	return layout
}

func (m model) monitorBody(layout monitorLayout) string {
	tableView := m.table
	tableView.SetWidth(layout.MainWidth)
	tableView.SetHeight(layout.TableRows)
	tableView.SetColumns(columnsForWidth(layout.MainWidth))

	detail := m.detail
	detail.Width = layout.MainWidth
	detail.Height = detailHeight
	detail.SetContent(m.detailContent())

	return lipgloss.JoinVertical(
		lipgloss.Left,
		tableView.View(),
		panelStyle.Width(max(0, layout.MainWidth)).Render(detail.View()),
	)
}
