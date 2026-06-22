package tui

import (
	"errors"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/watch"
)

type issueKey struct {
	Parent string
	Num    int
	TaskID string
	// Source is the owning worktree root for locally-scoped rows (plan tasks and
	// @manual panes), so the same (Parent, TaskID)/(Parent, Num) recorded in two
	// worktrees keys to distinct status — their branches and PR/CI state differ.
	// Empty for globally-stable GitHub issue rows, which aggregate across
	// worktrees (one status per issue number regardless of where it ran).
	Source string
}

type issueStatus struct {
	Title           string
	State           string
	PRs             []ghissue.PRRef
	Wave            int
	WaveLabel       string
	Blockers        string
	BlockerRows     []blockers.Status
	HasOpenBlockers bool
	// WaveDegraded marks rows whose body hydration failed in this refresh:
	// the wave/blocker fields were computed without the child's body, so an
	// empty Blockers value means "could not read", not "confirmed clear".
	WaveDegraded bool
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

type paneView struct {
	Parent       string
	IssueNum     int
	TaskID       string
	Kind         string
	Name         string
	PaneID       string
	ShellKey     string
	TmuxState    string
	TmuxTitle    string
	AgentState   string
	IssueState   string
	PRSummary    string
	HasMergedPR  bool
	Wave         int
	WaveLabel    string
	WaveBadge    string
	Blockers     string
	Blocked      bool
	CIStatus     string
	DiffSummary  string
	DirtyState   string
	WorktreeErr  string
	BranchName   string
	WorktreePath string
	worktreeAbs  string
	// sourceProjectRoot は複数 worktree をまたいで集約した場合に、この pane を
	// 記録した worktree の root。close/merge/cleanup をその所有元 state.json へ
	// 向けるために使う。単一 worktree のときは空(= m.opts.ProjectRoot を使う)。
	sourceProjectRoot string
	// sourceProjectRoots は同一 identity が複数 worktree に記録されていた場合の
	// 全所有 root(通常は [sourceProjectRoot])。close/cleanup が de-duplicate
	// された sibling ストアも漏れなく対象にするために使う。
	sourceProjectRoots []string
	Agent              string
	CreatedAt          string
	Prompt             string
	Derived            sessionview.PaneDerived
}

type hudSummary struct {
	Total   int
	Merged  int
	Pending int
	Blocked int
}

type sessionSummary struct {
	Parent  string
	Start   int
	Total   int
	Merged  int
	Pending int
	Blocked int
	Live    int
	Active  bool
}

type monitorLayout struct {
	Sidebar        bool
	MainWidth      int
	TableRows      int
	PanelWidth     int
	TopStripHeight int
}

type viewMode int

const (
	modeMonitor viewMode = iota
	modeNewPane
)

type newPaneField int

const (
	newPaneFieldPrompt newPaneField = iota
	newPaneFieldAgent
	newPaneFieldSlug
	newPaneFieldCount
)

type newPaneForm struct {
	prompt     textarea.Model
	slug       textinput.Model
	agentCount map[string]int
	agentIndex int
	focus      newPaneField
	launching  bool
	err        string
}

type model struct {
	opts            Options
	mode            viewMode
	table           table.Model
	detail          viewport.Model
	width           int
	height          int
	allPanes        []paneView
	panes           []paneView
	filterQuery     string
	filterEditing   bool
	issues          map[issueKey]issueStatus
	lastState       time.Time
	lastGH          time.Time
	stateErr        string
	ghErr           string
	notifyErr       string
	watchRunning    bool
	lastWatch       time.Time
	watchLaunched   int
	watchErr        string
	watchDisabled   bool
	notice          string
	newPane         newPaneForm
	peek            panePeek
	pendingAction   *pendingLifecycleAction
	actionRunning   bool
	quitAfterAction bool
	actionMessage   string
	notifications   map[issueKey]issueTransitionSnapshot
	notifyPrimed    bool
	keyboardPaused  bool
}

type stateLoadedMsg struct {
	panes        []paneView
	at           time.Time
	err          error
	scheduleNext bool
}

type ghLoadedMsg struct {
	issues       map[issueKey]issueStatus
	at           time.Time
	err          error
	scheduleNext bool
}

type lifecycleAction string

const (
	actionClose   lifecycleAction = "close"
	actionMerge   lifecycleAction = "merge"
	actionCleanup lifecycleAction = "cleanup"
)

type pendingLifecycleAction struct {
	action lifecycleAction
	pane   paneView
}

type lifecycleDoneMsg struct {
	action lifecycleAction
	pane   paneView
	code   exitcode.Code
	output string
}

type worktreeStatView struct {
	Diff  string
	Dirty string
	Err   string
}

type paneFocusedMsg struct {
	paneID         string
	err            error
	keyboardPaused bool
}

type keyboardProtocolsEnabledMsg struct{}

type panePeekLoadedMsg struct {
	paneID string
	output string
	at     time.Time
	err    error
}

type panePeek struct {
	PaneID  string
	Output  string
	At      time.Time
	Err     string
	Loading bool
}

type (
	stateTickMsg  time.Time
	ghTickMsg     time.Time
	watchTickMsg  time.Time
	launchPaneMsg struct {
		notice string
		count  int
		err    error
	}
	launchShellMsg struct {
		req ShellLaunchRequest
		err error
	}
)

type watchDoneMsg struct {
	report watch.Report
	at     time.Time
	err    error
}

type transitionNotifiedMsg struct {
	count int
	err   error
}

var errPaneNotAlive = errors.New("pane is no longer live")

func newModel(opts Options) model {
	opts = normalizeOptions(opts)
	t := table.New(
		table.WithColumns(columnsForWidth(120)),
		table.WithRows([]table.Row{}),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(colorAi)
	styles.Selected = styles.Selected.Bold(true).Foreground(colorAsagi)
	t.SetStyles(styles)

	return model{
		opts:          opts,
		mode:          modeMonitor,
		table:         t,
		detail:        viewport.New(120, detailHeight),
		issues:        map[issueKey]issueStatus{},
		notifications: map[issueKey]issueTransitionSnapshot{},
	}
}

func (m model) Init() tea.Cmd {
	loads := tea.Batch(m.loadStateCmd(true), m.loadGHCmd(true))
	if m.opts.Watcher == nil {
		return tea.Sequence(m.enableKeyboardProtocolsCmd(), loads)
	}
	return tea.Sequence(
		m.enableKeyboardProtocolsCmd(),
		tea.Batch(loads, m.watchTickCmd()),
	)
}

func (m model) enableKeyboardProtocolsCmd() tea.Cmd {
	keyboard := m.opts.keyboard
	return func() tea.Msg {
		keyboard.Enable()
		return keyboardProtocolsEnabledMsg{}
	}
}

func (m model) quit() (tea.Model, tea.Cmd) {
	m.opts.keyboard.Disable()
	m.keyboardPaused = false
	return m, tea.Quit
}
