package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// compactEntry is one switcher unit: a session header line or a pane row.
// The selected pane carries its expansion lines in the same entry so the
// sliding window never splits them from the row.
type compactEntry struct {
	lines  []string
	active bool
}

// cellWidth / truncateCells measure terminal display cells (CJK-aware),
// unlike the package's rune-count helpers: a compact row that exceeds the
// pane width wraps and breaks the fixed-height frame, while the bubbles
// table clips cells display-width-aware.
func cellWidth(s string) int {
	return ansi.StringWidth(s)
}

func truncateCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width <= 3 {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, width, "...")
}

// compactBody renders the vertical switcher list that replaces the table +
// detail panel below compactWidthAt. It reads the same m.panes /
// m.table.Cursor() state as the table, so selection and key handling are
// unchanged. The output is padded to exactly layout.TableRows lines so the
// footer stays anchored while the selection moves.
func (m model) compactBody(layout monitorLayout) string {
	if len(m.allPanes) == 0 {
		return padCompactLines([]string{truncateCells(emptyStateNoPanes, layout.MainWidth)}, layout.TableRows)
	}
	if len(m.panes) == 0 {
		return padCompactLines([]string{truncateCells(emptyStateNoFilter, layout.MainWidth)}, layout.TableRows)
	}
	entries, activeIdx := m.compactEntries(layout.MainWidth)
	return padCompactLines(compactWindowLines(entries, activeIdx, layout.TableRows), layout.TableRows)
}

func padCompactLines(lines []string, height int) string {
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// compactEntries walks m.panes in row order, inserting a session header
// whenever the parent changes, and returns the entry index of the selected
// pane. Ordinals are the 1-based visible-row index, matching jumpToOrdinal.
func (m model) compactEntries(width int) ([]compactEntry, int) {
	sessions := buildSessionSummaries(m.panes, m.table.Cursor())
	summaryByParent := make(map[string]sessionSummary, len(sessions))
	for _, session := range sessions {
		summaryByParent[session.Parent] = session
	}
	cursor := m.table.Cursor()
	if cursor < 0 || cursor >= len(m.panes) {
		cursor = 0
	}
	entries := make([]compactEntry, 0, len(m.panes)+len(sessions))
	activeIdx := 0
	prevParent := ""
	for i, pane := range m.panes {
		parent := normalizeParent(pane.Parent)
		if i == 0 || parent != prevParent {
			header := compactSessionHeaderLine(summaryByParent[parent], width)
			entries = append(entries, compactEntry{lines: []string{dimStyle.Render(header)}})
			prevParent = parent
		}
		selected := i == cursor
		line := compactPaneLine(pane, i+1, selected, width)
		if selected {
			activeIdx = len(entries)
			lines := []string{compactSelectedStyle.Render(line)}
			lines = append(lines, m.compactExpansionLines(pane, width)...)
			entries = append(entries, compactEntry{lines: lines, active: true})
			continue
		}
		entries = append(entries, compactEntry{lines: []string{line}})
	}
	return entries, activeIdx
}

// compactSessionHeaderLine has no ">" marker: that column marks the selected
// pane row, and headers reuse the sidebar's counter text instead.
func compactSessionHeaderLine(session sessionSummary, width int) string {
	return truncateCells("▏"+sessionCountersText(session), width)
}

// compactPaneLine renders one switcher row: selection marker, ordinal
// (digit-jump correspondence, blank past 9), agent-state glyph, item label,
// name, and the pane ID right-aligned. Only Name shrinks when the width is
// short; below that the trailing pane ID gets clipped.
func compactPaneLine(p paneView, ordinal int, selected bool, width int) string {
	marker := " "
	if selected {
		marker = ">"
	}
	ord := " "
	if ordinal >= 1 && ordinal <= 9 {
		ord = strconv.Itoa(ordinal)
	}
	prefix := marker + ord + agentStateGlyph(p) + " " + dash(p.backendLabel()) + " " + p.itemLabel() + " "
	paneID := dash(p.PaneID)
	name := truncateCells(strings.TrimSpace(p.Name), width-cellWidth(prefix)-cellWidth(paneID)-1)
	line := prefix + name
	pad := max(width-cellWidth(line)-cellWidth(paneID), 1)
	line += strings.Repeat(" ", pad) + paneID
	return truncateCells(line, width)
}

// compactExpansionLines are the detail lines under the selected row: branch +
// PR, ci/wave/blockers/dirty, the recorded worktree error, and the last line
// of the peek capture (or its loading/error state, which the detail panel
// would otherwise be the only surface for). A line with no content is
// dropped, so healthy panes expand by 2-3 lines.
func (m model) compactExpansionLines(p paneView, width int) []string {
	lines := make([]string, 0, 4)
	branch := make([]string, 0, 2)
	if b := strings.TrimSpace(p.BranchName); b != "" && b != "-" {
		branch = append(branch, "⎇ "+b)
	}
	if pr := strings.TrimSpace(p.PRSummary); pr != "" && pr != "-" {
		branch = append(branch, "PR"+pr)
	}
	if len(branch) > 0 {
		lines = append(lines, truncateCells("   "+strings.Join(branch, " "), width))
	}
	status := make([]string, 0, 4)
	if ci := p.paneCI(); ci != "" {
		status = append(status, "ci:"+ci)
	}
	wave := strings.TrimSpace(p.WaveLabel)
	if wave == "" && p.Wave > 0 {
		wave = fmt.Sprintf("W%d", p.Wave)
	}
	if wave != "" && wave != "-" {
		status = append(status, wave)
	}
	if blk := strings.TrimSpace(p.Blockers); blk != "" && blk != "-" {
		status = append(status, "blk:"+blk)
	}
	if p.DirtyState == "dirty" {
		status = append(status, "dirty")
	}
	if len(status) > 0 {
		lines = append(lines, truncateCells("   "+strings.Join(status, " "), width))
	}
	// WorktreeErr carries raw git stderr, which can embed newlines; flatten
	// it first so the entry stays one display line (the window and padding
	// count slice elements as lines).
	if err := strings.Join(strings.Fields(p.WorktreeErr), " "); err != "" {
		lines = append(lines, truncateCells("   worktree_error="+err, width))
	}
	if p.PaneID != "" && m.peek.PaneID == p.PaneID {
		switch {
		case m.peek.Loading:
			lines = append(lines, truncateCells("   peek: loading...", width))
		case m.peek.Err != "":
			lines = append(lines, truncateCells("   peek: "+m.peek.Err, width))
		default:
			if out := strings.TrimRight(m.peek.Output, "\r\n"); out != "" {
				if tail := tailLines(out, 1); len(tail) == 1 && strings.TrimSpace(tail[0]) != "" {
					lines = append(lines, truncateCells("   "+strings.ReplaceAll(tail[0], "\t", " "), width))
				}
			}
		}
	}
	return lines
}

// compactWindowLines flattens entries into at most budget lines, keeping the
// active entry fully visible and roughly centered, with a "..." line marking
// each truncated side — the sessionRows pattern, made height-aware because
// the active entry spans multiple lines.
func compactWindowLines(entries []compactEntry, activeIdx, budget int) []string {
	if len(entries) == 0 || budget <= 0 {
		return nil
	}
	total := 0
	for _, entry := range entries {
		total += len(entry.lines)
	}
	if total <= budget {
		return flattenCompactEntries(entries)
	}
	activeIdx = clampInt(activeIdx, 0, len(entries)-1)
	// Grow outward from the active entry, alternating sides. Each acceptance
	// keeps used + current "..." markers within budget; markers only shrink
	// afterwards (a side disappears once fully included), so the invariant
	// holds through the loop.
	start, end := activeIdx, activeIdx+1
	used := len(entries[activeIdx].lines)
	for start > 0 || end < len(entries) {
		grew := false
		if start > 0 {
			cand := len(entries[start-1].lines)
			if used+cand+markLines(start-1 > 0)+markLines(end < len(entries)) <= budget {
				start--
				used += cand
				grew = true
			}
		}
		if end < len(entries) {
			cand := len(entries[end].lines)
			if used+cand+markLines(start > 0)+markLines(end+1 < len(entries)) <= budget {
				end++
				used += cand
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	head := markLines(start > 0)
	tail := markLines(end < len(entries))
	if used+head+tail > budget {
		// Degenerate case: the active entry alone plus markers exceeds the
		// budget. Trim the active entry's tail (its first line is the pane
		// row) rather than the markers, so truncation stays visible; with no
		// room for markers at all, spend the whole budget on the active rows.
		avail := budget - head - tail
		if avail < 1 {
			return entries[activeIdx].lines[:min(budget, len(entries[activeIdx].lines))]
		}
		avail = min(avail, len(entries[activeIdx].lines))
		lines := make([]string, 0, budget)
		if head > 0 {
			lines = append(lines, "...")
		}
		lines = append(lines, entries[activeIdx].lines[:avail]...)
		if tail > 0 && len(lines) < budget {
			lines = append(lines, "...")
		}
		return lines
	}
	lines := make([]string, 0, budget)
	if head > 0 {
		lines = append(lines, "...")
	}
	lines = append(lines, flattenCompactEntries(entries[start:end])...)
	if tail > 0 {
		lines = append(lines, "...")
	}
	return lines
}

func markLines(present bool) int {
	if present {
		return 1
	}
	return 0
}

func flattenCompactEntries(entries []compactEntry) []string {
	lines := []string{}
	for _, entry := range entries {
		lines = append(lines, entry.lines...)
	}
	return lines
}
