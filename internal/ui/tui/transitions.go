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

func issueTransitionSnapshots(statuses map[issueKey]issueStatus) map[issueKey]issueTransitionSnapshot {
	out := make(map[issueKey]issueTransitionSnapshot, len(statuses))
	for key, status := range statuses {
		out[key] = transitionSnapshot(status)
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
