package tui

import (
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
)

func waveBadge(wave int, blocked bool) string {
	if wave <= 0 {
		return "-"
	}
	state := "ready"
	if blocked {
		state = "blocked"
	}
	return fmt.Sprintf("W%d %s", wave, state)
}

func (p paneView) waveCell() string {
	if strings.TrimSpace(p.WaveLabel) != "" {
		return p.WaveLabel
	}
	return p.WaveBadge
}

func (p paneView) waveText() string {
	parts := nonDashStrings(p.WaveLabel, p.WaveBadge)
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func (p paneView) dependencyWaveText() string {
	if p.Wave <= 0 {
		return ""
	}
	return fmt.Sprintf("wave%d", p.Wave)
}

func (p paneView) isTask() bool {
	return strings.TrimSpace(p.TaskID) != ""
}

func (p paneView) identityLabel() string {
	if p.isTask() {
		return p.TaskID
	}
	if p.isShell() {
		if label := strings.TrimSpace(firstNonEmpty(p.Name, p.Derived.Name, p.TmuxTitle)); label != "" && label != "-" {
			return label
		}
		if slug := strings.TrimSpace(p.Derived.Name); slug != "" && slug != "-" {
			return slug
		}
		return "shell"
	}
	return "#" + strconv.Itoa(p.IssueNum)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nonDashStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "-" || seen[value] {
			continue
		}
		out = append(out, value)
		seen[value] = true
	}
	return out
}

func issueTitle(status issueStatus, num int) string {
	if strings.TrimSpace(status.Title) != "" {
		return status.Title
	}
	return "#" + strconv.Itoa(num)
}

func summarizePRs(prs []ghissue.PRRef) string {
	pr, ok := ghissue.PrimaryPR(prs)
	if !ok {
		return "-"
	}
	return "#" + strconv.Itoa(pr.Number) + " " + dash(pr.DisplayState())
}

func hasMergedPR(prs []ghissue.PRRef) bool {
	for _, pr := range prs {
		if pr.State == "MERGED" {
			return true
		}
	}
	return false
}

func (p paneView) itemLabel() string {
	if strings.TrimSpace(p.TaskID) != "" {
		return p.TaskID
	}
	if p.isShell() {
		return "shell"
	}
	return "#" + strconv.Itoa(p.IssueNum)
}

func compactParent(parent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return "-"
	}
	if n, ok := strings.CutPrefix(parent, "https://github.com/"); ok {
		parts := strings.Split(strings.Trim(n, "/"), "/")
		if len(parts) >= 4 && parts[2] == "projects" {
			return "proj/" + parts[3]
		}
	}
	return truncate(parent, 10)
}

func cloneIssueStatuses(in map[issueKey]issueStatus) map[issueKey]issueStatus {
	out := make(map[issueKey]issueStatus, len(in))
	maps.Copy(out, in)
	return out
}

func summarizeHUD(panes []paneView) hudSummary {
	summary := hudSummary{}
	for _, pane := range panes {
		if pane.isShell() {
			continue
		}
		summary.Total++
		if pane.HasMergedPR {
			summary.Merged++
		}
		if pane.Blocked {
			summary.Blocked++
		}
	}
	summary.Pending = summary.Total - summary.Merged
	return summary
}

func formatHUD(summary hudSummary) string {
	return fmt.Sprintf("total=%d merged=%d pending=%d blocked=%d", summary.Total, summary.Merged, summary.Pending, summary.Blocked)
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.Format("15:04:05")
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	return truncateRunes(s, maxLen)
}

func truncatePreserveSpace(s string, maxLen int) string {
	return truncateRunes(s, maxLen)
}

func fixedLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncateRunes(s, width)
	if pad := width - len([]rune(s)); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if maxLen <= 0 || len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[:maxLen])
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func trimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}

func compactMessage(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return truncate(strings.Join(fields, " "), 160)
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

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}
