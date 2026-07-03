package tui

import (
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case keyboardProtocolsEnabledMsg:
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		// Bump the generation here (not in the return expression) so the mutated
		// model is the one returned.
		cmd := m.scheduleRelayout()
		return m, cmd
	case relayoutTickMsg:
		if msg.gen != m.relayoutGen {
			return m, nil // a newer resize superseded this debounce tick
		}
		return m, m.relayoutCmd()
	case relayoutDoneMsg:
		// Layout follow is best-effort; the orchestrator already fell back if the
		// custom layout was rejected, so there is nothing to surface here.
		return m, nil
	case tea.KeyMsg:
		m.resumeKeyboardProtocols()
		if (m.newPanePopupOpen || m.newPane.launching) && m.mode != modeNewPane {
			return m, nil
		}
		if m.pendingAction != nil {
			return m.updatePendingAction(msg)
		}
		if m.actionRunning {
			switch msg.String() {
			case "q", "ctrl+c":
				m.quitAfterAction = true
				m.actionMessage = "will quit after lifecycle action finishes"
			}
			return m, nil
		}
		if m.mode == modeHelp {
			switch msg.String() {
			case "esc", "q", "?":
				m.mode = modeMonitor
			case "ctrl+c":
				return m.quit()
			}
			return m, nil
		}
		if m.mode == modeNewPane {
			return m.updateNewPane(msg)
		}
		if m.filterEditing {
			next, cmd := m.updateFilterInput(msg)
			return next, cmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m.quit()
		case "?":
			m.mode = modeHelp
			return m, nil
		case "/":
			m.filterEditing = true
			m.refreshRows()
			return m, nil
		case "esc":
			if m.filterQuery != "" {
				m.filterQuery = ""
				m.refreshRows()
			}
			return m, nil
		case "[":
			return m.jumpSession(-1)
		case "]":
			return m.jumpSession(1)
		case "n":
			return m, m.openNewPanePopupCmd()
		case "a":
			return m, m.openAttachAgentForm()
		case "A":
			return m, m.openSelectedWorktreeShellCmd()
		case "t":
			return m, m.openProjectRootShellCmd()
		case "enter", "o":
			cmd := m.focusSelectedCmd()
			return m, cmd
		case "p":
			cmd := m.peekSelectedCmd(true)
			return m, cmd
		case "c":
			return m.startPendingAction(actionClose)
		case "m":
			return m.startPendingAction(actionMerge)
		case "x":
			return m.startPendingAction(actionClose)
		case "X":
			return m.startPendingAction(actionCleanup)
		}
		oldCursor := m.table.Cursor()
		next, cmd := m.table.Update(msg)
		m.table = next
		m.refreshDetail()
		if m.table.Cursor() != oldCursor {
			peekCmd := m.peekSelectedCmd(false)
			return m, tea.Batch(cmd, peekCmd)
		}
		return m, cmd
	case stateLoadedMsg:
		if msg.err != nil {
			m.stateErr = msg.err.Error()
		} else {
			m.stateErr = ""
		}
		m.allPanes = msg.panes
		m.lastState = msg.at
		if msg.restoreNotice != "" {
			m.notice = msg.restoreNotice
		}
		m.refreshRows()
		peekCmd := m.peekSelectedCmd(false)
		if msg.scheduleNext {
			return m, tea.Batch(
				tea.Tick(m.opts.StateInterval, func(t time.Time) tea.Msg { return stateTickMsg(t) }),
				peekCmd,
			)
		}
		return m, peekCmd
	case ghLoadedMsg:
		if msg.err != nil {
			m.ghErr = msg.err.Error()
		} else {
			m.ghErr = ""
		}
		var notifyCmd tea.Cmd
		if msg.issues != nil {
			// Merge BEFORE transition detection: a degraded refresh can carry
			// partial blocker data (e.g. parent rows without the child body)
			// that the display immediately discards — notifications must not
			// fire on data the user never sees.
			issues := msg.issues
			if msg.err != nil && m.issues != nil {
				issues = mergeDegradedIssueStatuses(m.issues, msg.issues)
			}
			wasPrimed := m.notifyPrimed
			events := detectIssueTransitions(m.notifications, issues)
			if msg.err == nil {
				m.notifications = issueTransitionSnapshots(issues)
				if !m.notifyPrimed {
					m.notifyPrimed = true
				}
			} else if wasPrimed {
				m.notifications = mergePartialIssueTransitionSnapshots(m.notifications, issues)
			}
			if wasPrimed {
				notifyCmd = m.notifyEventsCmd(events)
				if len(events) > 0 {
					m.notice = transitionNotice(events)
				}
			}
			m.issues = issues
		}
		m.lastGH = msg.at
		m.refreshRows()
		if msg.scheduleNext {
			return m, tea.Batch(
				tea.Tick(m.opts.GHInterval, func(t time.Time) tea.Msg { return ghTickMsg(t) }),
				notifyCmd,
			)
		}
		return m, notifyCmd
	case lifecycleDoneMsg:
		m.actionRunning = false
		m.actionMessage = lifecycleResultMessage(msg)
		if m.quitAfterAction {
			m.quitAfterAction = false
			return m.quit()
		}
		return m, tea.Batch(m.loadStateCmd(false), m.loadGHCmd(false))
	case stateTickMsg:
		return m, m.loadStateCmd(true)
	case ghTickMsg:
		return m, m.loadGHCmd(true)
	case watchTickMsg:
		if m.opts.Watcher == nil {
			return m, nil
		}
		if m.watchRunning {
			return m, m.watchTickCmd()
		}
		m.watchRunning = true
		return m, tea.Batch(m.runWatchCmd(), m.watchTickCmd())
	case watchDoneMsg:
		m.watchRunning = false
		m.lastWatch = msg.at
		m.watchLaunched = len(msg.report.Launched)
		m.watchDisabled = watchReportDisabled(msg.report)
		m.watchErr = summarizeWatchError(msg.report, msg.err)
		return m, tea.Batch(m.loadStateCmd(false), m.loadGHCmd(false))
	case launchPaneMsg:
		m.newPane.launching = false
		if msg.err != nil {
			if m.mode == modeNewPane {
				m.newPane.err = msg.err.Error()
			} else {
				m.notice = "new pane: " + msg.err.Error()
			}
			return m, nil
		}
		m.mode = modeMonitor
		switch {
		case msg.notice != "":
			m.notice = msg.notice
		case msg.attached && msg.count > 1:
			m.notice = fmt.Sprintf("attached %d new agent panes", msg.count)
		case msg.attached:
			m.notice = "attached new agent pane"
		case msg.count > 1:
			m.notice = fmt.Sprintf("created %d new agent panes", msg.count)
		default:
			m.notice = "created new agent pane"
		}
		return m, m.loadStateCmd(false)
	case newPanePromptMsg:
		m.newPanePopupOpen = false
		if msg.err != nil {
			m.notice = "new pane popup: " + msg.err.Error()
			return m, nil
		}
		if msg.canceled {
			if m.notice == newPanePopupOpeningNotice {
				m.notice = ""
			}
			return m, nil
		}
		return m, m.launchNewPaneRequest(msg.req)
	case newPaneIssuesLoadedMsg:
		p := &m.newPane.issuePicker
		p.loading = false
		if msg.err != nil {
			// Leave loaded false so re-entering the mode retries the fetch.
			p.err = msg.err.Error()
			return m, nil
		}
		p.err = ""
		p.loaded = true
		p.items = issuePickerItems(msg.items)
		m.recomputePicker(p)
		return m, nil
	case newPanePlansLoadedMsg:
		p := &m.newPane.planPicker
		p.loading = false
		if msg.err != nil {
			p.err = msg.err.Error()
			return m, nil
		}
		p.err = ""
		p.loaded = true
		p.items = planPickerItems(msg.slugs)
		m.recomputePicker(p)
		return m, nil
	case newPaneAssignLoadedMsg:
		if m.mode != modeNewPane || m.newPane.step != newPaneStepAssign || m.newPane.assign.target != msg.target {
			return m, nil // a stale load must not populate a newer selection
		}
		m.newPane.assign.loading = false
		if msg.err != nil {
			m.newPane.assign.err = msg.err.Error()
			return m, nil
		}
		m.newPane.assign.err = ""
		rows := buildAssignRows(msg, m.newPane.agentChoice)
		if len(rows) == 0 {
			// A childless issue launches as a single pane; an empty plan defers
			// its error to the launch lane. Either way there is nothing to assign.
			return m, m.finalizeNewPaneModeSubmit()
		}
		m.newPane.assign.rows = rows
		m.newPane.assign.index = 0
		return m, nil
	case filesLoadedMsg:
		m.repoFilesLoading = false
		if msg.err != nil {
			// Leave repoFilesLoaded false so the next form open retries; surface
			// the reason in the popup instead of a silent "no match".
			m.repoFilesErr = msg.err.Error()
		} else {
			m.repoFiles = msg.files
			m.repoFileIndex = buildFileIndex(msg.files)
			m.repoFilesLoaded = true
			m.repoFilesErr = ""
		}
		if m.mode == modeNewPane && m.newPane.completing {
			m.recomputeCompletion()
		}
		return m, nil
	case launchShellMsg:
		if msg.err != nil {
			m.notice = fmt.Sprintf("terminal: %v", msg.err)
			return m, nil
		}
		switch {
		case msg.req.Root:
			m.notice = "opened project root terminal"
		case msg.req.Source != "":
			m.notice = fmt.Sprintf("opened terminal for %s", msg.req.Source)
		default:
			m.notice = "opened worktree terminal"
		}
		return m, m.loadStateCmd(false)
	case paneFocusedMsg:
		if msg.keyboardPaused {
			m.keyboardPaused = true
		}
		if msg.err != nil {
			m.notice = fmt.Sprintf("focus skipped for %s: %v", dash(msg.paneID), msg.err)
			if errors.Is(msg.err, errPaneNotAlive) {
				m.markPaneStale(msg.paneID)
				m.refreshRows()
			}
		} else {
			m.notice = fmt.Sprintf("focused %s; return to the fanout tui pane to continue", msg.paneID)
		}
		return m, nil
	case panePeekLoadedMsg:
		if pane, ok := m.selectedPane(); ok && pane.PaneID != msg.paneID {
			return m, nil
		}
		if msg.err != nil {
			m.peek = panePeek{PaneID: msg.paneID, At: msg.at, Err: msg.err.Error()}
			if errors.Is(msg.err, errPaneNotAlive) {
				m.markPaneStale(msg.paneID)
				m.refreshRows()
			}
		} else {
			m.peek = panePeek{PaneID: msg.paneID, Output: msg.output, At: msg.at}
		}
		m.refreshDetail()
		return m, nil
	case transitionNotifiedMsg:
		if msg.err != nil {
			m.notifyErr = msg.err.Error()
		} else {
			m.notifyErr = ""
		}
		return m, nil
	}
	return m, nil
}

// scheduleRelayout bumps the resize generation and returns a debounced tick. A
// later resize bumps the generation again, so only the last tick in a burst
// actually relayouts. It is a no-op when no Relayout callback is wired (tests).
func (m *model) scheduleRelayout() tea.Cmd {
	if m.opts.Relayout == nil {
		return nil
	}
	m.relayoutGen++
	gen := m.relayoutGen
	return tea.Tick(relayoutDebounce, func(time.Time) tea.Msg { return relayoutTickMsg{gen: gen} })
}

func (m model) relayoutCmd() tea.Cmd {
	relayout := m.opts.Relayout
	if relayout == nil {
		return nil
	}
	return func() tea.Msg { return relayoutDoneMsg{err: relayout()} }
}

func (m *model) resumeKeyboardProtocols() {
	if !m.keyboardPaused {
		return
	}
	m.opts.keyboard.Enable()
	m.keyboardPaused = false
}
