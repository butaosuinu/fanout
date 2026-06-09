// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultStateInterval = 2 * time.Second
	defaultGHInterval    = 20 * time.Second
	detailHeight         = 7
)

// Options configures the TUI monitor.
type Options struct {
	ProjectRoot   string
	Session       string
	StateInterval time.Duration
	GHInterval    time.Duration
}

type issueKey struct {
	Parent string
	Num    int
}

type issueStatus struct {
	Title           string
	State           string
	PRs             []ghissue.PRRef
	Wave            int
	Blockers        string
	HasOpenBlockers bool
}

type paneView struct {
	Parent       string
	IssueNum     int
	Name         string
	PaneID       string
	TmuxState    string
	TmuxTitle    string
	IssueState   string
	PRSummary    string
	Wave         int
	WaveBadge    string
	Blockers     string
	Blocked      bool
	BranchName   string
	WorktreePath string
	Agent        string
	CreatedAt    string
	Prompt       string
}

type model struct {
	opts      Options
	table     table.Model
	detail    viewport.Model
	width     int
	height    int
	panes     []paneView
	issues    map[issueKey]issueStatus
	lastState time.Time
	lastGH    time.Time
	stateErr  string
	ghErr     string
}

type stateLoadedMsg struct {
	panes []paneView
	at    time.Time
	err   error
}

type ghLoadedMsg struct {
	issues map[issueKey]issueStatus
	at     time.Time
	err    error
}

type stateTickMsg time.Time
type ghTickMsg time.Time

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("34"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("31"))
	panelStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), true, false, false, false).BorderForeground(lipgloss.Color("240"))
)

// Run starts the Bubble Tea TUI.
func Run(opts Options) error {
	opts = normalizeOptions(opts)
	m := newModel(opts)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func normalizeOptions(opts Options) Options {
	if opts.StateInterval <= 0 {
		opts.StateInterval = defaultStateInterval
	}
	if opts.GHInterval <= 0 {
		opts.GHInterval = defaultGHInterval
	}
	return opts
}

func newModel(opts Options) model {
	opts = normalizeOptions(opts)
	t := table.New(
		table.WithColumns(columnsForWidth(120)),
		table.WithRows([]table.Row{}),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("34"))
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("32"))
	t.SetStyles(styles)

	return model{
		opts:   opts,
		table:  t,
		detail: viewport.New(120, detailHeight),
		issues: map[issueKey]issueStatus{},
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.loadStateCmd(), m.loadGHCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
		next, cmd := m.table.Update(msg)
		m.table = next
		m.refreshDetail()
		return m, cmd
	case stateLoadedMsg:
		if msg.err != nil {
			m.stateErr = msg.err.Error()
		} else {
			m.stateErr = ""
		}
		m.panes = msg.panes
		m.lastState = msg.at
		m.refreshRows()
		return m, tea.Tick(m.opts.StateInterval, func(t time.Time) tea.Msg { return stateTickMsg(t) })
	case ghLoadedMsg:
		if msg.err != nil {
			m.ghErr = msg.err.Error()
		} else {
			m.ghErr = ""
		}
		if msg.issues != nil {
			m.issues = msg.issues
		}
		m.lastGH = msg.at
		m.refreshRows()
		return m, tea.Tick(m.opts.GHInterval, func(t time.Time) tea.Msg { return ghTickMsg(t) })
	case stateTickMsg:
		return m, m.loadStateCmd()
	case ghTickMsg:
		return m, m.loadGHCmd()
	}
	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "fanout TUI"
	}
	header := titleStyle.Render("fanout")
	if m.opts.Session != "" {
		header += " " + dimStyle.Render("session="+m.opts.Session)
	}
	if m.opts.ProjectRoot != "" {
		header += " " + dimStyle.Render(m.opts.ProjectRoot)
	}

	footer := dimStyle.Render("q quit  j/k move  state " + formatClock(m.lastState) + "  gh " + formatClock(m.lastGH))
	if m.stateErr != "" {
		footer += "\n" + errStyle.Render("state/tmux: "+m.stateErr)
	}
	if m.ghErr != "" {
		footer += "\n" + warnStyle.Render("gh: "+m.ghErr)
	}

	detail := m.detail
	detail.SetContent(m.detailContent())
	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		m.table.View(),
		panelStyle.Width(maxInt(0, m.width)).Render(detail.View()),
		footer,
	)
}

func (m *model) resize() {
	if m.width <= 0 {
		return
	}
	tableHeight := m.height - detailHeight - 5
	if tableHeight < 4 {
		tableHeight = 4
	}
	m.table.SetWidth(m.width)
	m.table.SetHeight(tableHeight)
	m.table.SetColumns(columnsForWidth(m.width))
	m.detail.Width = m.width
	m.detail.Height = detailHeight
	m.refreshRows()
}

func (m *model) refreshRows() {
	m.panes = applyIssueStatuses(m.panes, m.issues)
	rows := make([]table.Row, 0, len(m.panes))
	for _, pane := range m.panes {
		rows = append(rows, pane.tableRow())
	}
	m.table.SetRows(rows)
	m.table.SetCursor(m.table.Cursor())
	m.refreshDetail()
}

func (m *model) refreshDetail() {
	m.detail.SetContent(m.detailContent())
}

func (m model) detailContent() string {
	if len(m.panes) == 0 {
		return "No recorded fanout panes in .fanout/state.json."
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.panes) {
		idx = 0
	}
	pane := m.panes[idx]
	lines := []string{
		fmt.Sprintf("%s #%d  %s", pane.Parent, pane.IssueNum, pane.Name),
		fmt.Sprintf("pane=%s tmux=%s title=%s agent=%s", dash(pane.PaneID), pane.TmuxState, dash(pane.TmuxTitle), dash(pane.Agent)),
		fmt.Sprintf("issue=%s pr=%s branch=%s", dash(pane.IssueState), dash(pane.PRSummary), dash(pane.BranchName)),
		fmt.Sprintf("wave=%s blockers=%s", dash(pane.WaveBadge), dash(pane.Blockers)),
		fmt.Sprintf("worktree=%s", dash(pane.WorktreePath)),
		fmt.Sprintf("created=%s", dash(pane.CreatedAt)),
	}
	if pane.Prompt != "" {
		lines = append(lines, "prompt="+pane.Prompt)
	}
	return strings.Join(lines, "\n")
}

func (m model) loadStateCmd() tea.Cmd {
	projectRoot := m.opts.ProjectRoot
	issues := cloneIssueStatuses(m.issues)
	return func() tea.Msg {
		panes, err := loadPaneViews(projectRoot, issues)
		return stateLoadedMsg{panes: panes, at: time.Now(), err: err}
	}
}

func (m model) loadGHCmd() tea.Cmd {
	projectRoot := m.opts.ProjectRoot
	return func() tea.Msg {
		issues, err := loadIssueStatuses(projectRoot)
		return ghLoadedMsg{issues: issues, at: time.Now(), err: err}
	}
}

func loadPaneViews(projectRoot string, issues map[issueKey]issueStatus) ([]paneView, error) {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil, err
	}
	tmuxPanes, err := tmuxrun.ListAllPanes()
	tmuxKnown := err == nil
	return buildPaneViews(projectRoot, store.Panes, tmuxPanes, tmuxKnown, issues), err
}

func loadIssueStatuses(projectRoot string) (map[issueKey]issueStatus, error) {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil, err
	}
	parents := recordedParents(store.Panes)
	statuses := map[issueKey]issueStatus{}
	if len(parents) == 0 {
		return statuses, nil
	}

	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		return nil, fmt.Errorf("resolve repo: %w", err)
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("unexpected repo nameWithOwner: %s", nwo)
	}

	prCache := map[int]issueStatus{}
	stateCache := map[int]string{}
	var loadErr error
	for _, parent := range parents {
		children, parentBody, err := loadParentIssues(gh, parent, store.PanesForParent(parent))
		if err != nil {
			loadErr = errors.Join(loadErr, err)
			if len(children) == 0 {
				fallbackChildren, fallbackBody, fallbackErr := loadRecordedPaneIssues(gh, store.PanesForParent(parent))
				if fallbackErr != nil {
					return nil, errors.Join(loadErr, fallbackErr)
				}
				children = fallbackChildren
				if parentBody == "" {
					parentBody = fallbackBody
				}
			}
		}
		if err := hydrateIssues(gh, children); err != nil {
			return nil, err
		}
		deps := parentDependencies(parent, children, parentBody, stateCache, func(num int) string {
			stateName, _ := gh.IssueState(num)
			return stateName
		})
		waves := dependencyWaves(children, deps)
		for _, issue := range children {
			key := keyForIssue(parent, issue.Number)
			cached, ok := prCache[issue.Number]
			if !ok {
				stateName, prs, err := gh.IssueWithPRs(owner, repo, issue.Number)
				if err != nil {
					return nil, fmt.Errorf("#%d: %w", issue.Number, err)
				}
				cached = issueStatus{State: stateName, PRs: prs}
				prCache[issue.Number] = cached
			}
			if cached.State == "" {
				cached.State = issue.State
			}
			cached.Title = issue.Title
			cached.Wave = waves[issue.Number]
			cached.Blockers = formatBlockers(deps[issue.Number])
			cached.HasOpenBlockers = hasOpenBlocker(deps[issue.Number])
			statuses[key] = cached
		}
	}
	return statuses, loadErr
}

func loadParentIssues(gh ghissue.Runner, parent string, panes []state.Pane) ([]ghissue.Issue, string, error) {
	parentNum, err := strconv.Atoi(parent)
	if err != nil {
		return loadRecordedPaneIssues(gh, panes)
	}

	parentBody, err := gh.ParentBody(parentNum)
	var loadErr error
	if err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("parent body #%d: %w", parentNum, err))
		parentBody = ""
	}
	subIssues, err := gh.SubIssueList(parentNum)
	if err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("sub-issues #%d: %w", parentNum, err))
		subIssues = nil
	}
	children, err := mergeParentIssueChildren(parentNum, subIssues, parentBody, panes, gh.IssueDetail)
	if err != nil {
		return nil, parentBody, errors.Join(loadErr, err)
	}
	return children, parentBody, loadErr
}

func mergeParentIssueChildren(parentNum int, subIssues []ghissue.Issue, parentBody string, panes []state.Pane, issueDetail func(int) (ghissue.Issue, error)) ([]ghissue.Issue, error) {
	existing := parentChildNumbers(parentNum, subIssues)
	extra := []ghissue.Issue{}
	extra = append(extra, loadMissingIssueDetails(ghissue.TaskListNumbers(parentBody), existing, issueDetail)...)
	for _, pane := range panes {
		if pane.IssueNum <= 0 || existing[pane.IssueNum] {
			continue
		}
		detail, err := issueDetail(pane.IssueNum)
		if err != nil {
			return nil, fmt.Errorf("#%d: %w", pane.IssueNum, err)
		}
		extra = append(extra, detail)
		existing[pane.IssueNum] = true
	}
	return ghissue.MergeExtra(subIssues, extra), nil
}

func parentChildNumbers(parentNum int, subIssues []ghissue.Issue) map[int]bool {
	existing := map[int]bool{parentNum: true}
	for _, issue := range subIssues {
		existing[issue.Number] = true
	}
	return existing
}

func loadMissingIssueDetails(nums []int, existing map[int]bool, issueDetail func(int) (ghissue.Issue, error)) []ghissue.Issue {
	extra := []ghissue.Issue{}
	for _, num := range nums {
		if existing[num] {
			continue
		}
		detail, err := issueDetail(num)
		if err != nil {
			continue
		}
		extra = append(extra, detail)
		existing[num] = true
	}
	return extra
}

func loadRecordedPaneIssues(gh ghissue.Runner, panes []state.Pane) ([]ghissue.Issue, string, error) {
	children := make([]ghissue.Issue, 0, len(panes))
	seen := map[int]bool{}
	for _, pane := range panes {
		if pane.IssueNum <= 0 || seen[pane.IssueNum] {
			continue
		}
		detail, err := gh.IssueDetail(pane.IssueNum)
		if err != nil {
			return nil, "", fmt.Errorf("#%d: %w", pane.IssueNum, err)
		}
		children = append(children, detail)
		seen[pane.IssueNum] = true
	}
	return children, "", nil
}

func hydrateIssues(gh ghissue.Runner, issues []ghissue.Issue) error {
	for i := range issues {
		if issues[i].Body != "" {
			continue
		}
		detail, err := gh.IssueDetail(issues[i].Number)
		if err != nil {
			return fmt.Errorf("#%d: %w", issues[i].Number, err)
		}
		issues[i].Body = detail.Body
		issues[i].Labels = detail.Labels
		if issues[i].Title == "" {
			issues[i].Title = detail.Title
		}
		if issues[i].State == "" {
			issues[i].State = detail.State
		}
	}
	return nil
}

func parentDependencies(parent string, issues []ghissue.Issue, parentBody string, stateCache map[int]string, issueState func(int) string) map[int][]blockerStatus {
	deps := make(map[int][]blockerStatus, len(issues))
	for _, issue := range issues {
		childBlockers := blockers.FromChildBody(issue.Body)
		parentBlockers := []int{}
		if _, err := strconv.Atoi(parent); err == nil {
			parentBlockers = blockers.FromParentRow(parentBody, issue.Number)
		}
		nums := blockers.Dedupe(childBlockers, parentBlockers)
		rows := make([]blockerStatus, 0, len(nums))
		for _, num := range nums {
			stateName, ok := stateCache[num]
			if !ok {
				stateName = issueState(num)
				if stateName == "" {
					stateName = "UNKNOWN"
				}
				stateCache[num] = stateName
			}
			rows = append(rows, blockerStatus{Num: num, State: strings.ToUpper(stateName)})
		}
		deps[issue.Number] = rows
	}
	return deps
}

type blockerStatus struct {
	Num   int
	State string
}

func dependencyWaves(issues []ghissue.Issue, deps map[int][]blockerStatus) map[int]int {
	childSet := map[int]bool{}
	for _, issue := range issues {
		childSet[issue.Number] = true
	}
	waves := map[int]int{}
	visiting := map[int]bool{}
	var waveFor func(int) int
	waveFor = func(num int) int {
		if wave := waves[num]; wave > 0 {
			return wave
		}
		if visiting[num] {
			return 1
		}
		visiting[num] = true
		maxBlockerWave := 0
		for _, blocker := range deps[num] {
			blockerWave := 1
			if childSet[blocker.Num] {
				blockerWave = waveFor(blocker.Num)
			}
			if blockerWave > maxBlockerWave {
				maxBlockerWave = blockerWave
			}
		}
		delete(visiting, num)
		waves[num] = maxBlockerWave + 1
		return waves[num]
	}
	for _, issue := range issues {
		waveFor(issue.Number)
	}
	return waves
}

func formatBlockers(rows []blockerStatus) string {
	if len(rows) == 0 {
		return "-"
	}
	parts := make([]string, len(rows))
	for i, row := range rows {
		switch row.State {
		case "OPEN":
			parts[i] = fmt.Sprintf("OPEN #%d", row.Num)
		case "CLOSED":
			parts[i] = fmt.Sprintf("resolved #%d", row.Num)
		default:
			parts[i] = fmt.Sprintf("%s #%d", dash(row.State), row.Num)
		}
	}
	return strings.Join(parts, ", ")
}

func hasOpenBlocker(rows []blockerStatus) bool {
	for _, row := range rows {
		if row.State == "OPEN" {
			return true
		}
	}
	return false
}

func recordedParents(panes []state.Pane) []string {
	seen := map[string]bool{}
	for _, pane := range panes {
		if pane.Parent == "" || pane.IssueNum <= 0 {
			continue
		}
		seen[normalizedParent(pane.Parent)] = true
	}
	parents := make([]string, 0, len(seen))
	for parent := range seen {
		parents = append(parents, parent)
	}
	sort.Slice(parents, func(i, j int) bool {
		left, leftErr := strconv.Atoi(parents[i])
		right, rightErr := strconv.Atoi(parents[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return parents[i] < parents[j]
	})
	return parents
}

func keyForIssue(parent string, num int) issueKey {
	return issueKey{Parent: normalizedParent(parent), Num: num}
}

func normalizedParent(parent string) string {
	parentNum, err := strconv.Atoi(parent)
	if err != nil {
		return parent
	}
	return strconv.Itoa(parentNum)
}

func buildPaneViews(projectRoot string, panes []state.Pane, tmuxPanes []tmuxrun.PaneInfo, tmuxKnown bool, issues map[issueKey]issueStatus) []paneView {
	tmuxByID := map[string]tmuxrun.PaneInfo{}
	for _, pane := range tmuxPanes {
		tmuxByID[pane.ID] = pane
	}
	out := make([]paneView, 0, len(panes))
	seen := map[issueKey]bool{}
	for _, pane := range panes {
		key := keyForIssue(pane.Parent, pane.IssueNum)
		status, hasStatus := issues[key]
		seen[key] = true
		view := paneView{
			Parent:       pane.Parent,
			IssueNum:     pane.IssueNum,
			Name:         paneName(pane),
			PaneID:       pane.PaneID,
			TmuxState:    tmuxState(pane.PaneID, tmuxByID, tmuxKnown),
			IssueState:   "-",
			PRSummary:    "-",
			Wave:         status.Wave,
			WaveBadge:    waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:     dash(status.Blockers),
			Blocked:      status.HasOpenBlockers,
			BranchName:   pane.BranchName,
			WorktreePath: relativePath(projectRoot, pane.WorktreePath),
			Agent:        pane.Agent,
			CreatedAt:    pane.CreatedAt,
			Prompt:       pane.Prompt,
		}
		if tmuxPane, ok := tmuxByID[pane.PaneID]; ok {
			view.TmuxTitle = tmuxPane.Title
		}
		if hasStatus {
			view.IssueState = dash(status.State)
			view.PRSummary = summarizePRs(status.PRs)
		}
		out = append(out, view)
	}
	for key, status := range issues {
		if seen[key] {
			continue
		}
		out = append(out, paneView{
			Parent:     key.Parent,
			IssueNum:   key.Num,
			Name:       issueTitle(status, key.Num),
			TmuxState:  syntheticTmuxState(status),
			IssueState: dash(status.State),
			PRSummary:  summarizePRs(status.PRs),
			Wave:       status.Wave,
			WaveBadge:  waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:   dash(status.Blockers),
			Blocked:    status.HasOpenBlockers,
		})
	}
	sortPaneViews(out)
	return out
}

func applyIssueStatuses(panes []paneView, issues map[issueKey]issueStatus) []paneView {
	out := make([]paneView, len(panes))
	copy(out, panes)
	seen := map[issueKey]bool{}
	for i := range out {
		key := keyForIssue(out[i].Parent, out[i].IssueNum)
		seen[key] = true
		if status, ok := issues[key]; ok {
			out[i].IssueState = dash(status.State)
			out[i].PRSummary = summarizePRs(status.PRs)
			out[i].Wave = status.Wave
			out[i].WaveBadge = waveBadge(status.Wave, status.HasOpenBlockers)
			out[i].Blockers = dash(status.Blockers)
			out[i].Blocked = status.HasOpenBlockers
		}
	}
	for key, status := range issues {
		if seen[key] {
			continue
		}
		out = append(out, paneView{
			Parent:     key.Parent,
			IssueNum:   key.Num,
			Name:       issueTitle(status, key.Num),
			TmuxState:  syntheticTmuxState(status),
			IssueState: dash(status.State),
			PRSummary:  summarizePRs(status.PRs),
			Wave:       status.Wave,
			WaveBadge:  waveBadge(status.Wave, status.HasOpenBlockers),
			Blockers:   dash(status.Blockers),
			Blocked:    status.HasOpenBlockers,
		})
	}
	sortPaneViews(out)
	return out
}

func sortPaneViews(panes []paneView) {
	sort.Slice(panes, func(i, j int) bool {
		if panes[i].Parent != panes[j].Parent {
			return panes[i].Parent < panes[j].Parent
		}
		if panes[i].Wave != panes[j].Wave {
			return panes[i].Wave < panes[j].Wave
		}
		return panes[i].IssueNum < panes[j].IssueNum
	})
}

func syntheticTmuxState(status issueStatus) string {
	switch {
	case strings.EqualFold(status.State, "CLOSED"):
		return "closed"
	case status.HasOpenBlockers:
		return "deferred"
	case strings.EqualFold(status.State, "OPEN"):
		return "queued"
	default:
		return "unknown"
	}
}

func (p paneView) tableRow() table.Row {
	return table.Row{
		compactParent(p.Parent),
		"#" + strconv.Itoa(p.IssueNum),
		dash(p.WaveBadge),
		truncate(dash(p.Blockers), 22),
		truncate(p.Name, 28),
		p.TmuxState,
		dash(p.IssueState),
		truncate(dash(p.PRSummary), 14),
		dash(p.PaneID),
	}
}

func columnsForWidth(width int) []table.Column {
	nameWidth := clampInt(width/5, 14, 28)
	blockerWidth := clampInt(width/5, 12, 22)
	return []table.Column{
		{Title: "PARENT", Width: 10},
		{Title: "ISSUE", Width: 7},
		{Title: "WAVE", Width: 10},
		{Title: "BLOCKERS", Width: blockerWidth},
		{Title: "NAME", Width: nameWidth},
		{Title: "TMUX", Width: 8},
		{Title: "STATE", Width: 8},
		{Title: "PR", Width: 14},
		{Title: "PANE", Width: 8},
	}
}

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

func issueTitle(status issueStatus, num int) string {
	if strings.TrimSpace(status.Title) != "" {
		return status.Title
	}
	return "#" + strconv.Itoa(num)
}

func summarizePRs(prs []ghissue.PRRef) string {
	if len(prs) == 0 {
		return "-"
	}
	for _, pr := range prs {
		if pr.State == "MERGED" {
			return "#" + strconv.Itoa(pr.Number) + " MERGED"
		}
	}
	pr := prs[0]
	return "#" + strconv.Itoa(pr.Number) + " " + dash(pr.State)
}

func paneName(pane state.Pane) string {
	if strings.TrimSpace(pane.DisplayName) != "" {
		return pane.DisplayName
	}
	if strings.TrimSpace(pane.Slug) != "" {
		return pane.Slug
	}
	return "#" + strconv.Itoa(pane.IssueNum)
}

func tmuxState(paneID string, panes map[string]tmuxrun.PaneInfo, known bool) string {
	if strings.TrimSpace(paneID) == "" {
		return "-"
	}
	if !known {
		return "unknown"
	}
	if _, ok := panes[paneID]; ok {
		return "live"
	}
	return "stale"
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

func relativePath(root, path string) string {
	if root == "" || path == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return path
	}
	return rel
}

func cloneIssueStatuses(in map[issueKey]issueStatus) map[issueKey]issueStatus {
	out := make(map[issueKey]issueStatus, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.Format("15:04:05")
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
