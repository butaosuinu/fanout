package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type paneFilter struct {
	terms    []string
	states   []string
	agents   []string
	waves    []string
	runs     []string
	cis      []string
	dirty    []string
	live     []string
	issues   []string
	prs      []string
	tasks    []string
	backends []string
}

func (m model) updateFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "enter", "esc":
		m.filterEditing = false
		return m, nil
	case "backspace", "ctrl+h":
		m.filterQuery = trimLastRune(m.filterQuery)
	case "ctrl+u":
		m.filterQuery = ""
	default:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		m.filterQuery += string(msg.Runes)
	}
	m.refreshRows()
	return m, nil
}

func filterPaneViews(panes []paneView, query string) []paneView {
	filter := parsePaneFilter(query)
	if filter.empty() {
		return panes
	}
	out := make([]paneView, 0, len(panes))
	for _, pane := range panes {
		if pane.matchesFilter(filter) {
			out = append(out, pane)
		}
	}
	return out
}

func parsePaneFilter(query string) paneFilter {
	var filter paneFilter
	for token := range strings.FieldsSeq(strings.ToLower(strings.TrimSpace(query))) {
		key, value, ok := splitFilterToken(token)
		if !ok {
			filter.terms = append(filter.terms, token)
			continue
		}
		switch key {
		case "state", "status", "s":
			filter.states = append(filter.states, value)
		case "agent", "a":
			filter.agents = append(filter.agents, value)
		case "wave", "w":
			filter.waves = append(filter.waves, value)
		case "run":
			filter.runs = append(filter.runs, value)
		case "ci":
			filter.cis = append(filter.cis, value)
		case "dirty":
			filter.dirty = append(filter.dirty, value)
		case "live":
			filter.live = append(filter.live, value)
		case "issue":
			filter.issues = append(filter.issues, value)
		case "pr":
			filter.prs = append(filter.prs, value)
		case "task", "t":
			filter.tasks = append(filter.tasks, value)
		case "backend", "b":
			filter.backends = append(filter.backends, value)
		default:
			filter.terms = append(filter.terms, token)
		}
	}
	return filter
}

func splitFilterToken(token string) (string, string, bool) {
	idx := strings.IndexAny(token, ":=")
	if idx <= 0 || idx == len(token)-1 {
		return "", "", false
	}
	key := strings.TrimSpace(token[:idx])
	value := strings.TrimSpace(token[idx+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func (f paneFilter) empty() bool {
	return len(f.terms) == 0 && len(f.states) == 0 && len(f.agents) == 0 && len(f.waves) == 0 &&
		len(f.runs) == 0 && len(f.cis) == 0 && len(f.dirty) == 0 && len(f.live) == 0 &&
		len(f.issues) == 0 && len(f.prs) == 0 && len(f.tasks) == 0 && len(f.backends) == 0
}

func (p paneView) matchesFilter(filter paneFilter) bool {
	for _, state := range filter.states {
		if !containsFold(state, p.IssueState, p.TmuxState, p.PRSummary) {
			return false
		}
	}
	for _, agent := range filter.agents {
		if !containsFold(agent, p.Agent) {
			return false
		}
	}
	for _, wave := range filter.waves {
		if !containsFold(wave, p.WaveLabel, p.WaveBadge, p.waveCell(), p.dependencyWaveText()) {
			return false
		}
	}
	for _, run := range filter.runs {
		if !equalFold(run, p.AgentState) {
			return false
		}
	}
	for _, ci := range filter.cis {
		if !equalFold(ci, p.paneCI()) {
			return false
		}
	}
	for _, dirty := range filter.dirty {
		if !equalFold(dirty, yesNo(p.DirtyState == "dirty")) {
			return false
		}
	}
	for _, live := range filter.live {
		if !equalFold(live, yesNo(p.TmuxState == "live")) {
			return false
		}
	}
	for _, issue := range filter.issues {
		value := strings.TrimSpace(p.Derived.FilterValues["issue"])
		if value == "" {
			value = strconv.Itoa(p.IssueNum)
			if p.IssueNum <= 0 && strings.TrimSpace(p.TaskID) != "" {
				value = strings.ToLower(strings.TrimSpace(p.TaskID))
			}
		}
		if !equalFold(issue, value) {
			return false
		}
	}
	for _, pr := range filter.prs {
		if !equalFold(pr, p.primaryPRState()) {
			return false
		}
	}
	for _, task := range filter.tasks {
		if !containsFold(task, p.TaskID) {
			return false
		}
	}
	for _, runtimeBackend := range filter.backends {
		if !equalFold(runtimeBackend, p.backendLabel()) {
			return false
		}
	}
	searchText := strings.ToLower(strings.Join([]string{
		p.Parent,
		p.TaskID,
		p.Kind,
		p.itemLabel(),
		"#" + strconv.Itoa(p.IssueNum),
		strconv.Itoa(p.IssueNum),
		p.TaskID,
		p.Name,
		p.PaneID,
		p.backendLabel(),
		p.TmuxState,
		p.TmuxTitle,
		p.AgentState,
		p.IssueState,
		p.PRSummary,
		p.CIStatus,
		p.BranchName,
		p.WorktreePath,
		p.DiffSummary,
		p.DirtyState,
		p.WorktreeErr,
		p.Agent,
		p.WaveLabel,
		p.WaveBadge,
		p.Blockers,
		p.dependencyWaveText(),
		p.CreatedAt,
		p.Prompt,
	}, "\n"))
	if strings.TrimSpace(p.Derived.FilterText) != "" {
		searchText += "\n" + p.Derived.FilterText
	}
	for _, term := range filter.terms {
		if !strings.Contains(searchText, term) {
			return false
		}
	}
	return true
}

func equalFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func containsFold(needle string, values ...string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return true
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}
