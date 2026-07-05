package tui

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	fanoutnotify "github.com/butaosuinu/fanout/internal/infra/notify"
)

type transitionNotifier interface {
	Notify([]fanoutnotify.Event) error
}

type issueTransitionSnapshot struct {
	State     string
	HasMerged bool
	CIStatus  string
	Waiting   bool
	PRNumber  int
	Title     string
	Blockers  string
}

type agentTransitionSnapshot struct {
	State string
}

type transitionNotifiedMsg struct {
	count int
	err   error
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

func detectIssueTransitions(previous map[issueKey]issueTransitionSnapshot, current map[issueKey]issueStatus) []fanoutnotify.Event {
	if len(previous) == 0 || len(current) == 0 {
		return nil
	}
	keys := sortedIssueKeys(current)
	events := []fanoutnotify.Event{}
	for _, key := range keys {
		if key.TaskID != "" {
			continue
		}
		prev, ok := previous[key]
		if !ok {
			continue
		}
		next := transitionSnapshot(current[key])
		if !prev.HasMerged && next.HasMerged {
			events = append(events, transitionEvent(fanoutnotify.EventMerged, key, next))
		}
		if prev.CIStatus != "fail" && next.CIStatus == "fail" {
			events = append(events, transitionEvent(fanoutnotify.EventCIFailed, key, next))
		}
		if !prev.Waiting && next.Waiting {
			events = append(events, transitionEvent(fanoutnotify.EventWaiting, key, next))
		}
	}
	return events
}

func detectAgentTransitions(previous map[string]agentTransitionSnapshot, current []paneView) []fanoutnotify.Event {
	if len(previous) == 0 || len(current) == 0 {
		return nil
	}
	events := []fanoutnotify.Event{}
	for _, pane := range sortedAgentTransitionPanes(current) {
		key, next, ok := agentTransitionSnapshotForPane(pane)
		if !ok {
			continue
		}
		prev, ok := previous[key]
		if !ok {
			continue
		}
		kind, ok := agentTransitionKind(prev.State, next.State)
		if !ok {
			continue
		}
		events = append(events, agentTransitionEvent(kind, pane, next.State))
	}
	return events
}

func issueTransitionSnapshots(statuses map[issueKey]issueStatus) map[issueKey]issueTransitionSnapshot {
	out := make(map[issueKey]issueTransitionSnapshot, len(statuses))
	for key, status := range statuses {
		out[key] = transitionSnapshot(status)
	}
	return out
}

func mergeAgentTransitionSnapshots(previous map[string]agentTransitionSnapshot, panes []paneView) map[string]agentTransitionSnapshot {
	out := make(map[string]agentTransitionSnapshot, len(panes))
	for _, pane := range panes {
		key := agentTransitionKey(pane)
		if key == "" {
			continue
		}
		if _, snapshot, ok := agentTransitionSnapshotForPane(pane); ok {
			out[key] = snapshot
			continue
		}
		if prev, ok := previous[key]; ok {
			out[key] = prev
		}
	}
	return out
}

func mergePartialIssueTransitionSnapshots(previous map[issueKey]issueTransitionSnapshot, statuses map[issueKey]issueStatus) map[issueKey]issueTransitionSnapshot {
	out := make(map[issueKey]issueTransitionSnapshot, len(previous))
	maps.Copy(out, previous)
	for key, status := range statuses {
		current := transitionSnapshot(status)
		if prev, ok := previous[key]; ok {
			current = mergePartialIssueTransitionSnapshot(prev, current)
		}
		out[key] = current
	}
	return out
}

// mergeDegradedIssueStatuses keeps last-known display data when an errored
// refresh returns a degraded partial snapshot: keys dropped from the partial
// result are restored wholesale from the previous snapshot, and keys present
// in both keep their previous wave/blocker fields when the partial entry lost
// them. New keys pass through unchanged.
func mergeDegradedIssueStatuses(previous, current map[issueKey]issueStatus) map[issueKey]issueStatus {
	out := make(map[issueKey]issueStatus, max(len(previous), len(current)))
	maps.Copy(out, previous)
	for key, status := range current {
		if prev, ok := previous[key]; ok {
			status = mergeDegradedIssueStatus(prev, status)
		}
		out[key] = status
	}
	return out
}

// mergeDegradedIssueStatus restores the previous wave/blocker display fields
// when a degraded refresh dropped them (failed child-body hydration renders a
// still-blocked child with "-" blockers and no blocked badge).
func mergeDegradedIssueStatus(previous, current issueStatus) issueStatus {
	// Restore only rows whose wave/blocker inputs actually failed this
	// refresh. A non-degraded row is fresh data — a confirmed "-" (blocker
	// list legitimately removed) must not be masked by stale data just
	// because an unrelated issue errored in the same refresh. A degraded row
	// is restored even when parent-row blockers produced partial text, and
	// even when the previous display was a confirmed unblocked "-": its
	// Wave/WaveLabel are still valid last-known data the degraded refresh
	// would otherwise blank.
	if !current.WaveDegraded {
		return current
	}
	if degradedBlockers(previous.Blockers) && previous.Wave == 0 && previous.WaveLabel == "" {
		return current // previous carries nothing better to preserve
	}
	current.Blockers = previous.Blockers
	current.BlockerRows = previous.BlockerRows
	current.HasOpenBlockers = previous.HasOpenBlockers
	current.Wave = previous.Wave
	current.WaveLabel = previous.WaveLabel
	return current
}

// degradedBlockers reports whether a formatted blockers string carries no
// blocker information ("-" or empty), the signature of a degraded refresh.
func degradedBlockers(s string) bool {
	trimmed := strings.TrimSpace(s)
	return trimmed == "" || trimmed == "-"
}

func mergePartialIssueTransitionSnapshot(previous, current issueTransitionSnapshot) issueTransitionSnapshot {
	if previous.HasMerged && !current.HasMerged {
		current.HasMerged = true
		if current.PRNumber == 0 {
			current.PRNumber = previous.PRNumber
		}
	}
	if previous.Waiting && !current.Waiting && !current.HasMerged && current.State != "CLOSED" && current.Blockers == "-" {
		current.Waiting = true
		current.Blockers = previous.Blockers
	}
	return current
}

func transitionSnapshot(status issueStatus) issueTransitionSnapshot {
	pr, _ := ghissue.PrimaryPR(status.PRs)
	return issueTransitionSnapshot{
		State:     strings.ToUpper(strings.TrimSpace(status.State)),
		HasMerged: hasMergedPR(status.PRs),
		CIStatus:  strings.ToLower(strings.TrimSpace(ghissue.SummarizeCI(status.PRs))),
		Waiting:   status.HasOpenBlockers || strings.EqualFold(status.State, "WAITING"),
		PRNumber:  pr.Number,
		Title:     status.Title,
		Blockers:  status.Blockers,
	}
}

func transitionEvent(kind fanoutnotify.EventKind, key issueKey, snapshot issueTransitionSnapshot) fanoutnotify.Event {
	return fanoutnotify.Event{
		Kind:     kind,
		Parent:   key.Parent,
		IssueNum: key.Num,
		Title:    snapshot.Title,
		PRNumber: snapshot.PRNumber,
		CIStatus: snapshot.CIStatus,
		Blockers: snapshot.Blockers,
	}
}

func agentTransitionSnapshotForPane(pane paneView) (string, agentTransitionSnapshot, bool) {
	if pane.TmuxState != "live" {
		return "", agentTransitionSnapshot{}, false
	}
	state := strings.ToLower(strings.TrimSpace(pane.AgentState))
	if !trackedAgentState(state) {
		return "", agentTransitionSnapshot{}, false
	}
	key := agentTransitionKey(pane)
	if key == "" {
		return "", agentTransitionSnapshot{}, false
	}
	return key, agentTransitionSnapshot{State: state}, true
}

func trackedAgentState(state string) bool {
	switch state {
	case "running", "working", "idle", "plan", "blocked", "done":
		return true
	default:
		return false
	}
}

func agentTransitionKind(previous, current string) (fanoutnotify.EventKind, bool) {
	if previous == current {
		return "", false
	}
	switch current {
	case "plan":
		return fanoutnotify.EventAgentPlan, activeAgentState(previous)
	case "blocked":
		return fanoutnotify.EventAgentBlocked, trackedAgentState(previous)
	case "done":
		return fanoutnotify.EventAgentDone, trackedAgentState(previous) && previous != "done"
	default:
		return "", false
	}
}

func activeAgentState(state string) bool {
	return state == "running" || state == "working"
}

func agentTransitionEvent(kind fanoutnotify.EventKind, pane paneView, state string) fanoutnotify.Event {
	return fanoutnotify.Event{
		Kind:       kind,
		Parent:     normalizedParent(pane.Parent),
		IssueNum:   max(pane.IssueNum, 0),
		TaskID:     firstNonEmpty(pane.TaskID, pane.SourceTaskID),
		Title:      firstNonEmpty(pane.Name, pane.Derived.Name, pane.TmuxTitle),
		PaneID:     pane.PaneID,
		SourceKey:  pane.SourceKey,
		AgentState: state,
	}
}

func sortedAgentTransitionPanes(panes []paneView) []paneView {
	out := slices.Clone(panes)
	slices.SortFunc(out, func(a, b paneView) int {
		return cmp.Compare(agentTransitionKey(a), agentTransitionKey(b))
	})
	return out
}

func agentTransitionKey(pane paneView) string {
	key := keyForPaneView(pane)
	switch {
	case key.Num > 0:
		return fmt.Sprintf("issue:%s:%d", key.Parent, key.Num)
	case key.TaskID != "":
		return fmt.Sprintf("task:%s:%s:%s", key.Parent, key.Source, key.TaskID)
	case strings.TrimSpace(pane.PaneID) != "":
		return "pane:" + strings.TrimSpace(pane.PaneID)
	case strings.TrimSpace(pane.ShellKey) != "":
		return fmt.Sprintf("shell:%s:%s:%s", key.Parent, key.Source, strings.TrimSpace(pane.ShellKey))
	case strings.TrimSpace(pane.SourceKey) != "":
		return fmt.Sprintf("source:%s:%d:%s", key.Parent, key.Num, strings.TrimSpace(pane.SourceKey))
	case key.Parent != "" || key.Num != 0:
		return fmt.Sprintf("row:%s:%d:%s", key.Parent, key.Num, strings.TrimSpace(pane.Name))
	default:
		return ""
	}
}

func sortedIssueKeys(statuses map[issueKey]issueStatus) []issueKey {
	keys := make([]issueKey, 0, len(statuses))
	for key := range statuses {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b issueKey) int {
		if c := cmp.Compare(a.Parent, b.Parent); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Num, b.Num); c != 0 {
			return c
		}
		return cmp.Compare(a.TaskID, b.TaskID)
	})
	return keys
}

func transitionNotice(events []fanoutnotify.Event) string {
	if len(events) == 0 {
		return ""
	}
	if len(events) == 1 {
		return events[0].Message()
	}
	return fmt.Sprintf("%d state changes: %s", len(events), events[0].Message())
}

func hasMergedPR(prs []ghissue.PRRef) bool {
	for _, pr := range prs {
		if pr.State == "MERGED" {
			return true
		}
	}
	return false
}
