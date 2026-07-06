package tui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type monitorLayout struct {
	Sidebar        bool
	Compact        bool
	MainWidth      int
	TableRows      int
	PanelWidth     int
	TopStripHeight int
}

// viewOverride is the manual view toggle cycled by the v key. It is
// session-local and never persisted.
type viewOverride int

const (
	overrideAuto viewOverride = iota
	overrideCompact
	overrideFull
)

func (v viewOverride) next() viewOverride {
	switch v {
	case overrideAuto:
		return overrideCompact
	case overrideCompact:
		return overrideFull
	default:
		return overrideAuto
	}
}

func (v viewOverride) String() string {
	switch v {
	case overrideCompact:
		return "compact"
	case overrideFull:
		return "full"
	default:
		return "auto"
	}
}

func (m model) compactActive() bool {
	switch m.viewOverride {
	case overrideCompact:
		return true
	case overrideFull:
		return false
	default:
		return m.width < compactWidthAt
	}
}

func (m model) View() string {
	if m.closeOnly {
		if m.width == 0 {
			return "Close pane"
		}
		return m.closeChoiceView()
	}
	if m.helpOnly {
		if m.width == 0 {
			return "Keyboard shortcuts"
		}
		return m.helpView()
	}
	if m.width == 0 {
		return "fanout TUI"
	}
	if m.promptOnly {
		return m.newPaneView()
	}
	layout := m.monitorLayout()
	header := titleStyle.Render("fanout")
	if layout.Compact {
		// The full header (session= + project root + HUD) wraps at 40 columns
		// and eats list rows. Keep the title plus the repo basename so
		// multi-repo consoles stay tellable apart; counters live on the ▏
		// session header lines (a t/m/p/b HUD here would contradict them —
		// summarizeHUD skips shell/attached rows, session counters do not).
		if root := strings.TrimSpace(m.opts.ProjectRoot); root != "" {
			// Truncate by display cells so a long basename cannot wrap the
			// one-line header monitorLayout budgets for. 7 = "fanout" + space.
			if base := truncateCells(filepath.Base(root), max(m.width-7, 0)); base != "" {
				header += " " + dimStyle.Render(base)
			}
		}
	} else {
		if m.opts.Session != "" {
			header += " " + dimStyle.Render("session="+m.opts.Session)
		}
		if m.opts.ProjectRoot != "" {
			header += " " + dimStyle.Render(m.opts.ProjectRoot)
		}
		header += " " + dimStyle.Render(formatHUD(summarizeHUD(m.panes)))
	}

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

	var body string
	if layout.Compact {
		body = m.compactBody(layout)
	} else {
		body = m.monitorBody(layout)
		if layout.Sidebar {
			sessions := m.sessionSidebar(layout.PanelWidth, lipgloss.Height(body))
			body = lipgloss.JoinHorizontal(lipgloss.Top, sessions, " ", body)
		} else if layout.TopStripHeight > 0 {
			body = lipgloss.JoinVertical(lipgloss.Left, m.sessionTopStrip(layout.PanelWidth, layout.TopStripHeight), body)
		}
	}
	base := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.mode == modeNewPane {
		return overlayCentered(base, m.newPaneView(), m.width, m.height)
	}
	if m.mode == modeHelp {
		return overlayCentered(base, m.helpView(), m.width, m.height)
	}
	if m.mode == modeCloseChoice {
		return overlayCentered(base, m.closeChoiceView(), m.width, m.height)
	}
	return base
}

func (m *model) resize() {
	if m.width <= 0 {
		return
	}
	if m.mode == modeNewPane {
		m.newPane.prompt.SetWidth(m.inputContentWidth())
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
	if m.compactActive() {
		return monitorLayout{
			Compact:    true,
			MainWidth:  width,
			PanelWidth: width,
			// header (1) + footer base line (1) + 2 slack lines so a notice
			// or error footer line does not push the header off-screen
			// (table mode keeps the same slack via its rowBudget margin).
			TableRows: max(m.height-4, 4),
		}
	}
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
