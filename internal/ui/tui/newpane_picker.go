package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pickerMaxRows caps how many list rows the issue picker shows at once.
const pickerMaxRows = 8

// IssueListItem is one OPEN repository issue offered by the issue picker.
type IssueListItem struct {
	Number int
	Title  string
	Labels []string
	// HasSession marks issues that already have a recorded fanout pane. The
	// row stays selectable — re-selecting a parent launches its remaining
	// children — but renders dimmed with a note.
	HasSession bool
	// HasParent and HasOpenChildren classify the row from the GitHub Sub-issues
	// graph so the picker can mark parents, children, and standalone issues.
	// The classification is Sub-issues-link based: a parent whose children are
	// only listed as body task-list rows (- [ ] #N) shows standalone here, yet
	// launch still fans it out (countOpenChildTargets is the source of truth).
	HasParent       bool
	HasOpenChildren bool
}

// pickerState is the incremental-filter list one non-prompt mode owns.
type pickerState struct {
	loading bool
	loaded  bool
	err     string
	items   []pickerItem
	query   string
	results []int // items indices, ranked; the view scrolls a window over them
	index   int   // selection position within results
}

type pickerItem struct {
	key    string // display and match key, e.g. "#123"
	number int    // issue number
	title  string
	labels []string
	note   string
	marker string // "▸" fan-out parent, "└" child, "·" standalone; "" for non-issue pickers
}

type newPaneIssuesLoadedMsg struct {
	items []IssueListItem
	err   error
}

// availableNewPaneModes lists the form modes the wiring supports. Prompt is
// always first; issue appears only when its list provider is wired. Attach
// binds the launch to a specific worktree from a free prompt, so issue mode
// never applies there — offer prompt only.
func (m model) availableNewPaneModes() []newPaneMode {
	if m.newPane.attach != nil {
		return []newPaneMode{newPaneModePrompt}
	}
	modes := []newPaneMode{newPaneModePrompt}
	if m.opts.ListOpenIssues != nil {
		modes = append(modes, newPaneModeIssue)
	}
	return modes
}

// cycleNewPaneMode advances the mode row and kicks off the target mode's list
// load when it has not completed yet (a prior failure retries here).
func (m *model) cycleNewPaneMode(key string) tea.Cmd {
	modes := m.availableNewPaneModes()
	if len(modes) <= 1 {
		return nil
	}
	idx := 0
	for i, mode := range modes {
		if mode == m.newPane.mode {
			idx = i
			break
		}
	}
	if key == "left" {
		idx = (idx + len(modes) - 1) % len(modes)
	} else {
		idx = (idx + 1) % len(modes)
	}
	m.newPane.mode = modes[idx]
	m.newPane.err = ""
	return m.ensureModeListLoaded()
}

func (m *model) ensureModeListLoaded() tea.Cmd {
	switch m.newPane.mode {
	case newPaneModeIssue:
		p := &m.newPane.issuePicker
		if p.loading || p.loaded || m.opts.ListOpenIssues == nil {
			return nil
		}
		p.loading = true
		p.err = ""
		list := m.opts.ListOpenIssues
		return func() tea.Msg {
			items, err := list()
			return newPaneIssuesLoadedMsg{items: items, err: err}
		}
	default:
		return nil
	}
}

func (m *model) activePicker() *pickerState {
	switch m.newPane.mode {
	case newPaneModeIssue:
		return &m.newPane.issuePicker
	default:
		return nil
	}
}

func (m *model) recomputePicker(p *pickerState) {
	p.results = rankPickerItems(p.items, p.query)
	p.index = 0
}

// pickerFormOverhead is the non-list height of the picker form: title, mode
// row, field labels, the list box frame, filter line, the marker legend line,
// both ↑/↓ scroll marker lines, the two count-selector agent rows, the hint
// line, and the modal frame.
const pickerFormOverhead = 18

// pickerVisibleRows adapts the result cap to the available height so the
// form never renders taller than the popup pty — bubbletea keeps only the
// last lines, which would clip the modal top.
func (m model) pickerVisibleRows() int {
	if m.height <= 0 {
		return pickerMaxRows
	}
	return clampInt(m.height-pickerFormOverhead, 3, pickerMaxRows)
}

func (m *model) moveActivePicker(delta int) {
	p := m.activePicker()
	if p == nil {
		return
	}
	n := len(p.results)
	if n == 0 {
		return
	}
	p.index = (p.index + delta + n) % n
}

func pickerMoveDelta(key string) int {
	if key == "up" || key == "ctrl+p" {
		return -1
	}
	return 1
}

// updateActivePickerFilter feeds printable keys into the active picker's
// incremental filter; backspace shrinks it and ctrl+u clears it.
func (m *model) updateActivePickerFilter(msg tea.KeyMsg) {
	p := m.activePicker()
	if p == nil {
		return
	}
	switch msg.String() {
	case "backspace", "ctrl+h":
		if p.query != "" {
			p.query = trimLastRune(p.query)
			m.recomputePicker(p)
		}
	case "ctrl+u":
		if p.query != "" {
			p.query = ""
			m.recomputePicker(p)
		}
	default:
		// A lone space arrives as tea.KeySpace, not KeyRunes; without it
		// multi-word title queries ("fix ui") are impossible.
		if msg.Type == tea.KeySpace {
			p.query += " "
			m.recomputePicker(p)
			return
		}
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			p.query += string(msg.Runes)
			m.recomputePicker(p)
		}
	}
}

func (p pickerState) selectedItem() (pickerItem, bool) {
	if p.index < 0 || p.index >= len(p.results) {
		return pickerItem{}, false
	}
	return p.items[p.results[p.index]], true
}

// rankPickerItems returns the indices of every matching item, ranked. Rank 0:
// the key (issue number) prefix-matches; 1: the title contains the query; 2:
// a label contains it. Source order is preserved within a rank: gh returns
// issues newest-first. The result is uncapped; the view scrolls a window over
// it so every match stays reachable.
func rankPickerItems(items []pickerItem, query string) []int {
	q := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(query)), "#")
	type scored struct {
		idx  int
		rank int
	}
	matches := make([]scored, 0, len(items))
	for i, item := range items {
		rank := pickerMatchRank(item, q)
		if rank < 0 {
			continue
		}
		matches = append(matches, scored{idx: i, rank: rank})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].rank < matches[j].rank })
	top := make([]int, len(matches))
	for i, s := range matches {
		top[i] = s.idx
	}
	return top
}

// pickerRowWindow returns the visible result range, sliding so the selected
// row stays in view; a taller-than-pty list would be top-clipped by the
// renderer. It mirrors assignRowWindow.
func (m model) pickerRowWindow(p pickerState) (start, end int) {
	rows := len(p.results)
	visible := m.pickerVisibleRows()
	if rows <= visible {
		return 0, rows
	}
	start = clampInt(p.index-visible+1, 0, rows-visible)
	start = min(start, p.index)
	return start, start + visible
}

func pickerMatchRank(item pickerItem, q string) int {
	if q == "" {
		return 0
	}
	key := strings.ToLower(strings.TrimPrefix(item.key, "#"))
	if strings.HasPrefix(key, q) {
		return 0
	}
	if item.title != "" && strings.Contains(strings.ToLower(item.title), q) {
		return 1
	}
	// Key substring keeps items filterable by any key fragment, not just a
	// prefix.
	if strings.Contains(key, q) {
		return 1
	}
	for _, label := range item.labels {
		if strings.Contains(strings.ToLower(label), q) {
			return 2
		}
	}
	return -1
}

func issuePickerItems(items []IssueListItem) []pickerItem {
	out := make([]pickerItem, 0, len(items))
	for _, item := range items {
		note := ""
		if item.HasSession {
			note = "has session"
		}
		out = append(out, pickerItem{
			key:    "#" + strconv.Itoa(item.Number),
			number: item.Number,
			title:  item.Title,
			labels: item.Labels,
			note:   note,
			marker: issueMarker(item),
		})
	}
	return out
}

// issueMarker classifies a picker row: a fan-out parent (open children) wins
// over a child (has a parent), and an issue with neither link is standalone.
// A child that itself has open children still reads as a parent here.
func issueMarker(item IssueListItem) string {
	switch {
	case item.HasOpenChildren:
		return "▸"
	case item.HasParent:
		return "└"
	default:
		return "·"
	}
}

// pickerHasMarkers reports whether any row carries a classification marker, so
// only the issue picker (not future marker-less pickers) renders the legend.
func pickerHasMarkers(items []pickerItem) bool {
	for _, item := range items {
		if item.marker != "" {
			return true
		}
	}
	return false
}

func (m model) newPaneModeTabsView() string {
	names := map[newPaneMode]string{
		newPaneModePrompt: "Prompt",
		newPaneModeIssue:  "Issue",
	}
	parts := make([]string, 0, 2)
	for _, mode := range m.availableNewPaneModes() {
		if mode == m.newPane.mode {
			parts = append(parts, titleStyle.Render("["+names[mode]+"]"))
		} else {
			parts = append(parts, dimStyle.Render(" "+names[mode]+" "))
		}
	}
	return strings.Join(parts, " ")
}

func (m model) pickerView(p pickerState, emptyText string) string {
	if p.loading {
		return dimStyle.Render("loading…")
	}
	if p.err != "" {
		return errStyle.Render("list failed: " + p.err)
	}
	if len(p.items) == 0 {
		return dimStyle.Render(emptyText)
	}
	width := m.inputContentWidth()
	filter := "filter: " + p.query
	if p.query == "" {
		filter = dimStyle.Render("filter: (type to narrow)")
	}
	lines := []string{filter}
	if pickerHasMarkers(p.items) {
		lines = append(lines, dimStyle.Render("▸ fan-out (open children)  └ child  · standalone"))
	}
	if len(p.results) == 0 {
		lines = append(lines, dimStyle.Render("  no match"))
	}
	start, end := m.pickerRowWindow(p)
	if start > 0 {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
	}
	for i := start; i < end; i++ {
		item := p.items[p.results[i]]
		text := item.key
		if item.marker != "" {
			// The marker leads the row so the line-level selected/dim style
			// carries it; it is not colored per-marker.
			text = item.marker + " " + item.key
		}
		if item.title != "" {
			text += " " + item.title
		}
		if len(item.labels) > 0 {
			text += " [" + strings.Join(item.labels, ",") + "]"
		}
		if item.note != "" {
			text += " (" + item.note + ")"
		}
		text = truncateToWidth(text, width-2)
		switch {
		case i == p.index:
			lines = append(lines, "> "+titleStyle.Render(text))
		case item.note != "":
			lines = append(lines, "  "+dimStyle.Render(text))
		default:
			lines = append(lines, "  "+text)
		}
	}
	if end < len(p.results) {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  ↓ %d more (type to narrow)", len(p.results)-end)))
	}
	return strings.Join(lines, "\n")
}

// truncateToWidth keeps the head of s, eliding the tail with an ellipsis when
// it exceeds limit display columns.
func truncateToWidth(s string, limit int) string {
	if limit <= 1 || lipgloss.Width(s) <= limit {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > limit-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
