package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
)

func (m model) loadStateCmd(scheduleNext bool) tea.Cmd {
	projectRoot := m.opts.ProjectRoot
	issues := cloneIssueStatuses(m.issues)
	return func() tea.Msg {
		panes, err := loadPaneViews(projectRoot, issues)
		return stateLoadedMsg{panes: panes, at: time.Now(), err: err, scheduleNext: scheduleNext}
	}
}

func (m model) loadGHCmd(scheduleNext bool) tea.Cmd {
	projectRoot := m.opts.ProjectRoot
	return func() tea.Msg {
		issues, err := loadIssueStatuses(projectRoot)
		return ghLoadedMsg{issues: issues, at: time.Now(), err: err, scheduleNext: scheduleNext}
	}
}

func (m model) watchTickCmd() tea.Cmd {
	if m.opts.Watcher == nil {
		return nil
	}
	interval := m.opts.WatchInterval
	return tea.Tick(interval, func(t time.Time) tea.Msg { return watchTickMsg(t) })
}

func (m model) runWatchCmd() tea.Cmd {
	runner := m.opts.Watcher
	if runner == nil {
		return nil
	}
	return func() tea.Msg {
		report, err := runner.RunCycle()
		return watchDoneMsg{report: report, at: time.Now(), err: err}
	}
}

func (m model) notifyEventsCmd(events []fanoutnotify.Event) tea.Cmd {
	if len(events) == 0 || m.opts.Notifier == nil {
		return nil
	}
	notifier := m.opts.Notifier
	events = append([]fanoutnotify.Event(nil), events...)
	return func() tea.Msg {
		return transitionNotifiedMsg{count: len(events), err: notifier.Notify(events)}
	}
}
