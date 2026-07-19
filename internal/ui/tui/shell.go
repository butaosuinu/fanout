package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ShellLaunchRequest describes one shell terminal launch requested from the TUI.
type ShellLaunchRequest struct {
	TargetPath        string
	SourceProjectRoot string
	Root              bool
	Source            string
}

// ShellLaunchFunc creates a shell terminal pane for a TUI request.
type ShellLaunchFunc func(ShellLaunchRequest) error

func (m *model) openSelectedWorktreeShellCmd() tea.Cmd {
	pane, ok := m.selectedPane()
	if !ok {
		m.notice = "no pane selected"
		return nil
	}
	if reason := m.runtimeActionDisabledReason(&pane, "terminal launch"); reason != "" {
		m.notice = reason
		return nil
	}
	targetPath := pane.absoluteWorktreePath(m.opts.ProjectRoot)
	if targetPath == "" {
		m.notice = fmt.Sprintf("terminal skipped for %s: no worktree path", pane.identityLabel())
		return nil
	}
	return m.launchShellCmd(ShellLaunchRequest{
		TargetPath:        targetPath,
		SourceProjectRoot: pane.sourceProjectRoot,
		Source:            pane.identityLabel(),
	})
}

func (m *model) openProjectRootShellCmd() tea.Cmd {
	if reason := m.runtimeActionDisabledReason(nil, "terminal launch"); reason != "" {
		m.notice = reason
		return nil
	}
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
