// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/gitstat"
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

type issueStatus struct {
	State string
	PRs   []ghissue.PRRef
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
	DiffSummary  string
	DirtyState   string
	WorktreeErr  string
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
	issues    map[int]issueStatus
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
	issues map[int]issueStatus
	at     time.Time
	err    error
}

type worktreeStatView struct {
	Diff  string
	Dirty string
	Err   string
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
		issues: map[int]issueStatus{},
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
		fmt.Sprintf("worktree=%s diff=%s dirty=%s", dash(pane.WorktreePath), dash(pane.DiffSummary), dash(pane.DirtyState)),
		fmt.Sprintf("created=%s", dash(pane.CreatedAt)),
	}
	if pane.WorktreeErr != "" {
		lines = append(lines, "worktree_error="+pane.WorktreeErr)
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

func loadPaneViews(projectRoot string, issues map[int]issueStatus) ([]paneView, error) {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil, err
	}
	tmuxPanes, err := tmuxrun.ListAllPanes()
	tmuxKnown := err == nil
	worktrees := loadWorktreeStats(store.Panes)
	return buildPaneViews(projectRoot, store.Panes, tmuxPanes, tmuxKnown, issues, worktrees), err
}

func loadIssueStatuses(projectRoot string) (map[int]issueStatus, error) {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil, err
	}
	nums := issueNumbers(store.Panes)
	statuses := make(map[int]issueStatus, len(nums))
	if len(nums) == 0 {
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
	for _, num := range nums {
		stateName, prs, err := gh.IssueWithPRs(owner, repo, num)
		if err != nil {
			return nil, fmt.Errorf("#%d: %w", num, err)
		}
		statuses[num] = issueStatus{State: stateName, PRs: prs}
	}
	return statuses, nil
}

func buildPaneViews(projectRoot string, panes []state.Pane, tmuxPanes []tmuxrun.PaneInfo, tmuxKnown bool, issues map[int]issueStatus, worktrees map[string]worktreeStatView) []paneView {
	tmuxByID := map[string]tmuxrun.PaneInfo{}
	for _, pane := range tmuxPanes {
		tmuxByID[pane.ID] = pane
	}
	out := make([]paneView, 0, len(panes))
	for _, pane := range panes {
		view := paneView{
			Parent:       pane.Parent,
			IssueNum:     pane.IssueNum,
			Name:         paneName(pane),
			PaneID:       pane.PaneID,
			TmuxState:    tmuxState(pane.PaneID, tmuxByID, tmuxKnown),
			IssueState:   "-",
			PRSummary:    "-",
			DiffSummary:  "-",
			DirtyState:   "-",
			BranchName:   pane.BranchName,
			WorktreePath: relativePath(projectRoot, pane.WorktreePath),
			Agent:        pane.Agent,
			CreatedAt:    pane.CreatedAt,
			Prompt:       pane.Prompt,
		}
		if tmuxPane, ok := tmuxByID[pane.PaneID]; ok {
			view.TmuxTitle = tmuxPane.Title
		}
		if status, ok := issues[pane.IssueNum]; ok {
			view.IssueState = dash(status.State)
			view.PRSummary = summarizePRs(status.PRs)
		}
		if stat, ok := worktrees[pane.WorktreePath]; ok {
			view.DiffSummary = stat.Diff
			view.DirtyState = stat.Dirty
			view.WorktreeErr = stat.Err
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Parent != out[j].Parent {
			return out[i].Parent < out[j].Parent
		}
		return out[i].IssueNum < out[j].IssueNum
	})
	return out
}

func applyIssueStatuses(panes []paneView, issues map[int]issueStatus) []paneView {
	out := make([]paneView, len(panes))
	copy(out, panes)
	for i := range out {
		if status, ok := issues[out[i].IssueNum]; ok {
			out[i].IssueState = dash(status.State)
			out[i].PRSummary = summarizePRs(status.PRs)
		}
	}
	return out
}

func (p paneView) tableRow() table.Row {
	return table.Row{
		compactParent(p.Parent),
		"#" + strconv.Itoa(p.IssueNum),
		truncate(p.Name, 28),
		p.TmuxState,
		dash(p.IssueState),
		truncate(dash(p.PRSummary), 14),
		dash(p.DiffSummary),
		dash(p.DirtyState),
		truncate(dash(p.BranchName), 22),
		dash(p.PaneID),
	}
}

func columnsForWidth(width int) []table.Column {
	nameWidth := clampInt(width/6, 12, 20)
	branchWidth := clampInt(width/8, 8, 14)
	return []table.Column{
		{Title: "PARENT", Width: 7},
		{Title: "ISSUE", Width: 6},
		{Title: "NAME", Width: nameWidth},
		{Title: "TMUX", Width: 6},
		{Title: "STATE", Width: 7},
		{Title: "PR", Width: 12},
		{Title: "DIFF", Width: 8},
		{Title: "DIRTY", Width: 7},
		{Title: "BRANCH", Width: branchWidth},
		{Title: "PANE", Width: 7},
	}
}

func loadWorktreeStats(panes []state.Pane) map[string]worktreeStatView {
	runner := gitstat.Runner{}
	stats := map[string]worktreeStatView{}
	for _, pane := range panes {
		path := strings.TrimSpace(pane.WorktreePath)
		if path == "" {
			continue
		}
		if _, ok := stats[path]; ok {
			continue
		}
		stats[path] = worktreeStatForPath(runner, path)
	}
	return stats
}

func worktreeStatForPath(runner gitstat.Runner, path string) worktreeStatView {
	stat, err := runner.Worktree(path)
	if err != nil {
		return worktreeStatView{Diff: "-", Dirty: "unknown", Err: err.Error()}
	}
	dirty := "clean"
	if stat.Dirty {
		dirty = "dirty"
	}
	return worktreeStatView{
		Diff:  fmt.Sprintf("+%d/-%d", stat.Additions, stat.Deletions),
		Dirty: dirty,
	}
}

func issueNumbers(panes []state.Pane) []int {
	seen := map[int]bool{}
	for _, pane := range panes {
		if pane.IssueNum > 0 {
			seen[pane.IssueNum] = true
		}
	}
	nums := make([]int, 0, len(seen))
	for num := range seen {
		nums = append(nums, num)
	}
	sort.Ints(nums)
	return nums
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

func cloneIssueStatuses(in map[int]issueStatus) map[int]issueStatus {
	out := make(map[int]issueStatus, len(in))
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
