package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

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

func (m *model) openSelectedWorktreeShellCmd() tea.Cmd {
	pane, ok := m.selectedPane()
	if !ok {
		m.notice = "no pane selected"
		return nil
	}
	targetPath := pane.absoluteWorktreePath(m.opts.ProjectRoot)
	if targetPath == "" {
		m.notice = fmt.Sprintf("terminal skipped for %s: no worktree path", pane.identityLabel())
		return nil
	}
	return m.launchShellCmd(ShellLaunchRequest{
		TargetPath: targetPath,
		Source:     pane.identityLabel(),
	})
}

func (m *model) openProjectRootShellCmd() tea.Cmd {
	projectRoot := strings.TrimSpace(m.opts.ProjectRoot)
	if projectRoot == "" {
		m.notice = "terminal skipped: project root is unknown"
		return nil
	}
	return m.launchShellCmd(ShellLaunchRequest{
		TargetPath: projectRoot,
		Root:       true,
		Source:     "project root",
	})
}

func (m *model) launchShellCmd(req ShellLaunchRequest) tea.Cmd {
	req.TargetPath = strings.TrimSpace(req.TargetPath)
	if req.TargetPath == "" {
		m.notice = "terminal skipped: target path is empty"
		return nil
	}
	if m.opts.LaunchShell == nil {
		m.notice = "terminal launcher is not configured"
		return nil
	}
	switch {
	case req.Root:
		m.notice = "opening project root terminal..."
	case req.Source != "":
		m.notice = fmt.Sprintf("opening terminal for %s...", req.Source)
	default:
		m.notice = "opening worktree terminal..."
	}
	launch := m.opts.LaunchShell
	return func() tea.Msg {
		return launchShellMsg{req: req, err: launch(req)}
	}
}

func (m *model) focusSelectedCmd() tea.Cmd {
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
		return paneFocusedMsg{paneID: paneID, keyboardPaused: true}
	}
}

func (m *model) resumeKeyboardProtocols() {
	if !m.keyboardPaused {
		return
	}
	m.opts.keyboard.Enable()
	m.keyboardPaused = false
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
			break
		}
	}
	for i := range m.panes {
		if m.panes[i].PaneID == paneID {
			m.panes[i].TmuxState = "stale"
			return
		}
	}
}

func paneAliveForAction(pane paneView, paneAlive func(string) bool, shellPaneAlive func(string, string) bool) bool {
	if pane.isShell() {
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
