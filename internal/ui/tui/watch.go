package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/app/watch"
)

// WatcherRunner runs one watcher cycle.
type WatcherRunner interface {
	RunCycle() (watch.Report, error)
}

type watchDoneMsg struct {
	report watch.Report
	at     time.Time
	err    error
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
