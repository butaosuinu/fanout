package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

type paneFocusedMsg struct {
	paneID         string
	err            error
	zoomErr        error
	keyboardPaused bool
}

type panePeekLoadedMsg struct {
	paneID string
	output string
	at     time.Time
	err    error
}

type panePeek struct {
	PaneID  string
	Output  string
	At      time.Time
	Err     string
	Loading bool
}

var errPaneNotAlive = errors.New("pane is no longer live")

// Shared by the detail panel and the compact switcher so both views report
// the same empty states.
const (
	emptyStateNoPanes  = "No recorded fanout panes in .fanout/state.json."
	emptyStateNoFilter = "No panes match the current filter."
)

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

func (m *model) syncCursorToActivePane(paneID string) bool {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return false
	}
	for i, pane := range m.panes {
		if strings.TrimSpace(pane.PaneID) != paneID || !pane.canFocus() {
			continue
		}
		if i == m.table.Cursor() {
			return false
		}
		m.moveTableCursorTo(i)
		m.refreshDetail()
		return true
	}
	return false
}

func (m model) detailContent() string {
	if len(m.allPanes) == 0 {
		return emptyStateNoPanes
	}
	if len(m.panes) == 0 {
		return emptyStateNoFilter
	}
	pane, ok := m.selectedPane()
	if !ok {
		return emptyStateNoFilter
	}
	lines := []string{
		fmt.Sprintf("%s %s  %s", pane.Parent, pane.itemLabel(), pane.Name),
		fmt.Sprintf("pane=%s tmux=%s title=%s kind=%s agent=%s run=%s", dash(pane.PaneID), pane.TmuxState, dash(pane.TmuxTitle), dash(pane.Kind), dash(pane.Agent), dash(pane.AgentState)),
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

func (m model) selectedPane() (paneView, bool) {
	if len(m.panes) == 0 {
		return paneView{}, false
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.panes) {
		idx = 0
	}
	return m.panes[idx], true
}

func (m *model) focusSelectedCmd() tea.Cmd {
	return m.focusSelected(false)
}

func (m *model) zoomSelectedCmd() tea.Cmd {
	return m.focusSelected(true)
}

func (m *model) focusSelected(zoom bool) tea.Cmd {
	pane, ok := m.selectedPane()
	if !ok {
		m.notice = "no pane selected"
		return nil
	}
	if !pane.canFocus() {
		m.notice = fmt.Sprintf("focus skipped for %s: tmux state is %s", dash(pane.PaneID), pane.TmuxState)
		return nil
	}

	paneID := pane.PaneID
	alive := m.opts.PaneAlive
	shellAlive := m.opts.ShellPaneAlive
	focus := m.opts.FocusPane
	zoomPane := m.opts.ZoomPane
	keyboard := m.opts.keyboard
	m.notice = fmt.Sprintf("focusing %s...", paneID)
	return func() tea.Msg {
		if !paneAliveForAction(pane, alive, shellAlive) {
			return paneFocusedMsg{paneID: paneID, err: errPaneNotAlive}
		}
		keyboard.Disable()
		if err := focus(paneID); err != nil {
			keyboard.Enable()
			return paneFocusedMsg{paneID: paneID, err: err}
		}
		msg := paneFocusedMsg{paneID: paneID, keyboardPaused: true}
		if zoom {
			msg.zoomErr = zoomPane(paneID)
		}
		return msg
	}
}

func (m *model) peekSelectedCmd(force bool) tea.Cmd {
	pane, ok := m.selectedPane()
	if !ok {
		m.peek = panePeek{}
		return nil
	}
	if !pane.canPeek() {
		m.peek = panePeek{PaneID: pane.PaneID, At: time.Now(), Err: fmt.Sprintf("tmux state is %s", pane.TmuxState)}
		return nil
	}
	if !force && m.peek.PaneID == pane.PaneID && (m.peek.Loading || m.peek.Output != "" || m.peek.Err != "") {
		return nil
	}

	paneID := pane.PaneID
	shellAlive := m.opts.ShellPaneAlive
	capture := m.opts.CapturePaneOutput
	m.peek = panePeek{PaneID: paneID, Loading: true}
	return func() tea.Msg {
		if pane.isShell() && !shellAlive(pane.PaneID, pane.ShellKey) {
			return panePeekLoadedMsg{paneID: paneID, at: time.Now(), err: errPaneNotAlive}
		}
		out, err := capture(paneID, peekLines)
		return panePeekLoadedMsg{paneID: paneID, output: out, at: time.Now(), err: err}
	}
}

func (m model) peekContent(pane paneView, maxLines int) []string {
	header := "peek"
	if !m.peek.At.IsZero() {
		header += " " + formatClock(m.peek.At)
	}
	if pane.PaneID == "" {
		return []string{header + ": no pane id recorded"}
	}
	if !pane.canPeek() {
		return []string{header + ": unavailable (" + pane.TmuxState + ")"}
	}
	if m.peek.PaneID != pane.PaneID {
		return []string{header + ": waiting for capture"}
	}
	if m.peek.Loading {
		return []string{header + ": loading..."}
	}
	if m.peek.Err != "" {
		return []string{header + ": " + m.peek.Err}
	}

	out := strings.TrimRight(m.peek.Output, "\r\n")
	if out == "" {
		return []string{header + ": no output"}
	}
	lines := []string{header + ":"}
	for _, line := range tailLines(out, maxLines) {
		lines = append(lines, truncatePreserveSpace(line, max(20, m.detail.Width-2)))
	}
	return lines
}

func (m *model) markPaneStale(paneID string) {
	for i := range m.allPanes {
		if m.allPanes[i].PaneID == paneID {
			m.allPanes[i].TmuxState = "stale"
			m.allPanes[i].AgentState = ""
			break
		}
	}
	for i := range m.panes {
		if m.panes[i].PaneID == paneID {
			m.panes[i].TmuxState = "stale"
			m.panes[i].AgentState = ""
			return
		}
	}
}

// paneAliveForAction gates focus/close style actions. Any pane recorded with a
// ShellKey (shell terminals, the plan fan-out coordinator at the repo root)
// must match the live pane's @fanout_shell_key: a bare pane id check would let
// the action target an unrelated pane after tmux reuses the id.
func paneAliveForAction(pane paneView, paneAlive func(string) bool, shellPaneAlive func(string, string) bool) bool {
	if pane.isShell() || pane.ShellKey != "" {
		return shellPaneAlive(pane.PaneID, pane.ShellKey)
	}
	return paneAlive(pane.PaneID)
}

func shellPaneAliveByKey(paneID, shellKey string) bool {
	paneID = strings.TrimSpace(paneID)
	shellKey = strings.TrimSpace(shellKey)
	if paneID == "" || shellKey == "" {
		return false
	}
	panes, err := tmuxrun.ListLivePanes()
	if err != nil {
		return false
	}
	for _, pane := range panes {
		if pane.ID == paneID && pane.ShellKey == shellKey {
			return true
		}
	}
	return false
}

func tailLines(s string, maxLen int) []string {
	if maxLen <= 0 {
		return nil
	}
	raw := strings.Split(s, "\n")
	if len(raw) > maxLen {
		raw = raw[len(raw)-maxLen:]
	}
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		out = append(out, strings.TrimRight(line, "\r"))
	}
	return out
}
