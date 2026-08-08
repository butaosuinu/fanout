// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const (
	defaultStateInterval  = 2 * time.Second
	defaultGHInterval     = 20 * time.Second
	defaultActiveInterval = 500 * time.Millisecond
	defaultWatchInterval  = 60 * time.Second
	minWatchInterval      = 20 * time.Second
	defaultWatchLabel     = "fanout:auto"
	detailHeight          = 13
	peekLines             = 80
	defaultLaunchAgent    = "claude"
	sessionSidebarAt      = 120
	sessionSidebarWidth   = 26
	sessionTopHeight      = 3
	compactWidthAt        = 80
	// relayoutDebounce coalesces the burst of resize events tmux emits while a
	// terminal window is being dragged into a single relayout.
	relayoutDebounce = 150 * time.Millisecond
)

// Options configures the TUI monitor.
type Options struct {
	ProjectRoot         string
	Session             string
	BackendSelection    backend.Selection
	StateInterval       time.Duration
	GHInterval          time.Duration
	ActivePaneInterval  time.Duration
	Watcher             WatcherRunner
	WatchInterval       time.Duration
	WatchLabel          string
	DefaultAgent        string
	WatcherRunningLabel string
	Hooks               hooks.Config
	LaunchPane          LaunchFunc
	NewPanePrompt       NewPanePromptFunc
	HelpPopup           HelpPopupFunc
	CloseChoicePopup    CloseChoicePopupFunc
	SettingsPopup       SettingsPopupFunc
	ReloadSettings      SettingsReloadFunc
	LaunchAttach        AttachLaunchFunc
	LaunchShell         ShellLaunchFunc
	RestorePanes        func() (string, error)
	// New-session issue mode wiring. A nil list provider hides the mode from
	// the form; a nil launcher turns its submissions into a notice.
	ListOpenIssues    func() ([]IssueListItem, error)
	ListIssueChildren func(parent int) ([]ChildTarget, error)
	LaunchIssue       IssueLaunchFunc
	LaunchIssuePlan   IssuePlanLaunchFunc
	OpenIssue         IssueOpenFunc
	// Relayout re-tiles the TUI's tmux window into the fanout grid. It is wired
	// to panelayout.Apply(target, Resize) in production and left nil in tests
	// (then resize handling is a no-op).
	Relayout          func() error
	FocusPane         func(string) error
	ZoomPane          func(string) error
	ActivePane        func() (string, error)
	PaneAlive         func(string) bool
	ShellPaneAlive    func(paneID, shellKey string) bool
	CapturePaneOutput func(string, int) (string, error)
	// Herdr capabilities receive the exact persisted identity. The composition
	// root must bind it to this repository's owned session before each action.
	HerdrActionDisabled func(state.Pane) string
	FocusHerdrPane      func(state.Pane) error
	CaptureHerdrPane    func(state.Pane, int) (string, error)
	ListLive            func() ([]backend.LivePane, error)
	ClosePane           func(backend.PaneRef) error
	// LifecycleCloseOwned is the runtime-specific destructive capability used
	// for state-backed closes. It keeps identity verification isolated from the
	// mixed-backend display collector.
	LifecycleCloseOwned func(backend.CloseRequest) (backend.CloseResult, error)
	ListRepoFiles       func(root string) ([]string, error)
	Notifier            transitionNotifier
	lifecycle           lifecycleRunner
	keyboard            keyboardProtocols
}

type viewMode int

const (
	modeMonitor viewMode = iota
	modeNewPane
	modeHelp
	modeCloseChoice
	modeSettings
)

type model struct {
	opts          Options
	mode          viewMode
	table         table.Model
	detail        viewport.Model
	width         int
	height        int
	allPanes      []paneView
	panes         []paneView
	filterQuery   string
	filterEditing bool
	viewOverride  viewOverride
	issues        map[issueKey]issueStatus
	issueLoader   *issueStatusLoader
	// worktreeStat is built once: it owns the untracked-file cache, and
	// rebuilding it per tick would throw that cache away every refresh.
	worktreeStat      func(path, baseRef string) (sessionview.WorktreeStat, error)
	lastState         time.Time
	lastGH            time.Time
	stateErr          string
	ghErr             string
	notifyErr         string
	watchRunning      bool
	lastWatch         time.Time
	watchLaunched     int
	watchErr          string
	watchDisabled     bool
	watchTickGen      int
	notice            string
	newPane           newPaneForm
	settings          settingsForm
	newPanePopupOpen  bool
	helpPopupOpen     bool
	closePopupOpen    bool
	settingsPopupOpen bool
	repoFiles         []string
	repoFileIndex     []fileEntry
	repoFilesLoaded   bool
	repoFilesLoading  bool
	repoFilesErr      string
	peek              panePeek
	pendingAction     *pendingLifecycleAction
	actionRunning     bool
	quitAfterAction   bool
	actionMessage     string
	notifications     map[issueKey]issueTransitionSnapshot
	notifyPrimed      bool
	keyboardPaused    bool
	quitAfterLaunch   bool
	relayoutGen       int
	promptOnly        bool
	helpOnly          bool
	closeOnly         bool
	promptDone        bool
	promptCanceled    bool
	promptResult      LaunchRequest
	agentStates       map[string]agentTransitionSnapshot
	agentPrimed       bool
	closeDone         bool
	closeCanceled     bool
	closeResult       lifecycle.CloseMode
	settingsOnly      bool
	settingsDone      bool
	settingsCanceled  bool
	settingsResult    SettingsPopupResult
}

type keyboardProtocolsEnabledMsg struct{}

type (
	stateTickMsg  time.Time
	ghTickMsg     time.Time
	activeTickMsg time.Time
	watchTickMsg  struct {
		at  time.Time
		gen int
	}
	launchPaneMsg struct {
		notice         string
		count          int
		createdPaneIDs []string
		attached       bool
		err            error
	}
	activePaneMsg struct {
		paneID       string
		err          error
		scheduleNext bool
	}
	newPanePromptMsg struct {
		req      LaunchRequest
		canceled bool
		err      error
	}
	newPaneIssueOpenedMsg struct {
		issue int
		err   error
	}
	helpPopupDoneMsg struct {
		err error
	}
	closeChoicePopupDoneMsg struct {
		mode     lifecycle.CloseMode
		canceled bool
		err      error
	}
	settingsPopupDoneMsg struct {
		result   SettingsPopupResult
		canceled bool
		err      error
	}
	settingsReloadedMsg struct {
		result  SettingsPopupResult
		runtime SettingsRuntime
		err     error
	}
	launchShellMsg struct {
		req ShellLaunchRequest
		err error
	}
	// relayoutTickMsg fires after the debounce window; gen lets a newer resize
	// supersede a pending tick so only the last one in a burst relayouts.
	relayoutTickMsg struct{ gen int }
	relayoutDoneMsg struct{ err error }
)

// Run starts the Bubble Tea TUI.
func Run(opts Options) error {
	opts = normalizeOptions(opts)
	keyboard := newShiftEnterProtocols(os.Stdout, enhancedKeyboardKeysEnabled())
	opts.keyboard = keyboard
	m := newModel(opts)
	input, closeInput, err := newShiftEnterProgramInput(os.Stdin)
	if err != nil {
		return err
	}
	defer closeInput()
	defer keyboard.Disable()
	_, err = tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithInput(input),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.QuitMsg); ok {
				keyboard.Disable()
			}
			return msg
		}),
	).Run()
	return err
}

// enhancedKeyboardKeysEnabled reports whether the TUI requests the enhanced
// keyboard protocol (so Shift+Enter inserts a newline in the new-pane prompt
// instead of submitting). It is on by default; FANOUT_TUI_ENHANCED_KEYS set to
// a falsey value opts out for terminals that mishandle the protocol.
func enhancedKeyboardKeysEnabled() bool {
	return !EnhancedKeysDisabled(os.Getenv(EnhancedKeysEnv))
}

// EnhancedKeysDisabled reports whether the FANOUT_TUI_ENHANCED_KEYS value is an
// explicit opt-out. Any other value (including empty) keeps enhanced keys on.
func EnhancedKeysDisabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "off", "no":
		return true
	default:
		return false
	}
}

func normalizeOptions(opts Options) Options {
	opts.BackendSelection = normalizeBackendSelection(opts.BackendSelection)
	if opts.StateInterval <= 0 {
		opts.StateInterval = defaultStateInterval
	}
	if opts.GHInterval <= 0 {
		opts.GHInterval = defaultGHInterval
	}
	if opts.ActivePaneInterval <= 0 {
		opts.ActivePaneInterval = defaultActiveInterval
	}
	if opts.Watcher != nil {
		if opts.WatchInterval <= 0 {
			opts.WatchInterval = defaultWatchInterval
		}
		if opts.WatchInterval < minWatchInterval {
			opts.WatchInterval = minWatchInterval
		}
		if strings.TrimSpace(opts.WatchLabel) == "" {
			opts.WatchLabel = defaultWatchLabel
		}
	}
	if opts.lifecycle == nil {
		opts.lifecycle = defaultLifecycleRunner{}
	}
	if agent.ValidateKnown(opts.DefaultAgent) != nil {
		opts.DefaultAgent = defaultLaunchAgent
	}
	if opts.FocusPane == nil {
		opts.FocusPane = func(string) error { return fmt.Errorf("runtime backend focus is not configured") }
	}
	if opts.ZoomPane == nil {
		opts.ZoomPane = tmuxrun.ZoomPane
	}
	if opts.PaneAlive == nil {
		opts.PaneAlive = func(string) bool { return false }
	}
	if opts.ShellPaneAlive == nil {
		opts.ShellPaneAlive = shellPaneAliveByKey(opts.ListLive)
	}
	if opts.CapturePaneOutput == nil {
		opts.CapturePaneOutput = func(string, int) (string, error) {
			return "", fmt.Errorf("runtime backend read is not configured")
		}
	}
	if opts.ListRepoFiles == nil {
		opts.ListRepoFiles = worktree.ListFiles
	}
	if opts.keyboard == nil {
		opts.keyboard = noopKeyboardProtocols{}
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
	styles.Header = styles.Header.Bold(true).Foreground(colorAi)
	styles.Selected = styles.Selected.Bold(true).Foreground(colorAsagi)
	t.SetStyles(styles)

	return model{
		opts:          opts,
		mode:          modeMonitor,
		table:         t,
		detail:        viewport.New(120, detailHeight),
		issues:        map[issueKey]issueStatus{},
		issueLoader:   newIssueStatusLoader(3 * opts.GHInterval),
		worktreeStat:  sessionview.GitWorktreeStat(opts.ProjectRoot),
		notifications: map[issueKey]issueTransitionSnapshot{},
		agentStates:   map[string]agentTransitionSnapshot{},
	}
}

func (m model) Init() tea.Cmd {
	if m.closeOnly {
		return nil
	}
	if m.settingsOnly {
		return nil
	}
	if m.helpOnly {
		return nil
	}
	if m.promptOnly {
		loadFiles := m.loadRepoFilesCmd()
		if loadFiles == nil {
			return m.enableKeyboardProtocolsCmd()
		}
		return tea.Sequence(m.enableKeyboardProtocolsCmd(), loadFiles)
	}
	loads := tea.Batch(m.loadStateCmd(true), m.loadGHCmd(true), m.activePaneTickCmd())
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
