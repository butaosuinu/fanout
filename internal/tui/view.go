package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	"github.com/butaosuinu/fanout/internal/watch"
)

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

func (m *model) refreshRows() {
	m.allPanes = applyIssueStatuses(m.opts.ProjectRoot, m.allPanes, m.issues)
	m.panes = filterPaneViews(m.allPanes, m.filterQuery)
	rows := make([]table.Row, 0, len(m.panes))
	for _, pane := range m.panes {
		rows = append(rows, pane.tableRow())
	}
	m.table.SetRows(rows)
	m.table.SetCursor(m.table.Cursor())
	m.refreshDetail()
}

func (m *model) refreshDetail() {
	m.detail.SetContent(m.detailContent())
}

func (m model) detailContent() string {
	if len(m.allPanes) == 0 {
		return "No recorded fanout panes in .fanout/state.json."
	}
	if len(m.panes) == 0 {
		return "No panes match the current filter."
	}
	pane, ok := m.selectedPane()
	if !ok {
		return "No panes match the current filter."
	}
	lines := []string{
		fmt.Sprintf("%s %s  %s", pane.Parent, pane.itemLabel(), pane.Name),
		fmt.Sprintf("pane=%s tmux=%s title=%s kind=%s agent=%s", dash(pane.PaneID), pane.TmuxState, dash(pane.TmuxTitle), dash(pane.Kind), dash(pane.Agent)),
		fmt.Sprintf("issue=%s pr=%s ci=%s branch=%s", dash(pane.IssueState), dash(pane.PRSummary), dash(pane.CIStatus), dash(pane.BranchName)),
		fmt.Sprintf("wave=%s blockers=%s", dash(pane.waveText()), dash(pane.Blockers)),
		fmt.Sprintf("worktree=%s diff=%s dirty=%s", dash(pane.WorktreePath), dash(pane.DiffSummary), dash(pane.DirtyState)),
		fmt.Sprintf("created=%s", dash(pane.CreatedAt)),
	}
	if pane.WorktreeErr != "" {
		lines = append(lines, "worktree_error="+pane.WorktreeErr)
	}
	peekBudget := max(2, m.detail.Height-len(lines)-1)
	lines = append(lines, m.peekContent(pane, peekBudget)...)
	if pane.Prompt != "" && len(lines) < m.detail.Height {
		lines = append(lines, "prompt="+pane.Prompt)
	}
	return strings.Join(lines, "\n")
}

func columnsForWidth(width int) []table.Column {
	nameWidth := clampInt(width/7, 12, 20)
	blockerWidth := clampInt(width/8, 10, 16)
	branchWidth := clampInt(width/8, 10, 16)
	return []table.Column{
		{Title: "PARENT", Width: 10},
		{Title: "ISSUE", Width: 7},
		{Title: "WAVE", Width: 12},
		{Title: "BLOCKERS", Width: blockerWidth},
		{Title: "NAME", Width: nameWidth},
		{Title: "AGENT", Width: 7},
		{Title: "TMUX", Width: 7},
		{Title: "STATE", Width: 8},
		{Title: "PR", Width: 12},
		{Title: "CI", Width: 7},
		{Title: "DIFF", Width: 8},
		{Title: "DIRTY", Width: 7},
		{Title: "BRANCH", Width: branchWidth},
		{Title: "PANE", Width: 7},
	}
}

func (m model) footerText() string {
	parts := []string{"q quit", "n new", "A terminal", "t root", "j/k move", "[/] session", "/ filter", "enter/o focus", "p peek", "c close", "m merge", "x cleanup"}
	if m.filterEditing {
		parts = append(parts, "typing")
	}
	if m.filterEditing || strings.TrimSpace(m.filterQuery) != "" {
		parts = append(parts, fmt.Sprintf("filter=%q %d/%d", m.filterQuery, len(m.panes), len(m.allPanes)))
	}
	if watchText := m.watchFooterText(); watchText != "" {
		parts = append(parts, watchText)
	}
	parts = append(parts, "state "+formatClock(m.lastState), "gh "+formatClock(m.lastGH))
	return strings.Join(parts, "  ")
}

func (m model) watchFooterText() string {
	if m.opts.Watcher == nil {
		return ""
	}
	status := "on"
	if m.watchDisabled {
		status = "disabled"
	}
	parts := []string{
		"watch: " + status,
		"label=" + m.opts.WatchLabel,
	}
	if m.watchRunning {
		parts = append(parts, "running")
	}
	parts = append(parts,
		"last="+formatClock(m.lastWatch),
		fmt.Sprintf("launched=%d", m.watchLaunched),
		"err="+dash(truncate(m.watchErr, 120)),
	)
	return strings.Join(parts, " ")
}

func watchReportDisabled(report watch.Report) bool {
	for _, failure := range report.Failures {
		if failure.Disabled {
			return true
		}
	}
	for _, skip := range report.Skipped {
		if skip.Reason == watch.SkipDisabled {
			return true
		}
	}
	return false
}

func summarizeWatchError(report watch.Report, err error) string {
	if err != nil {
		return err.Error()
	}
	for _, failure := range slices.Backward(report.Failures) {
		if failure.Err == nil && failure.RevertErr == nil {
			continue
		}
		stage := strings.TrimSpace(string(failure.Stage))
		if stage == "" {
			stage = "watch"
		}
		prefix := stage
		if failure.Issue.Number > 0 {
			prefix = fmt.Sprintf("#%d %s", failure.Issue.Number, stage)
		}
		parts := []string{}
		if failure.Err != nil {
			parts = append(parts, failure.Err.Error())
		}
		if failure.RevertErr != nil {
			parts = append(parts, "revert: "+failure.RevertErr.Error())
		}
		if failure.Disabled {
			parts = append(parts, "disabled")
		}
		return prefix + ": " + strings.Join(parts, "; ")
	}
	return ""
}
