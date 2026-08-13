package tui

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type lifecycleAction string

const (
	actionClose   lifecycleAction = "close"
	actionMerge   lifecycleAction = "merge"
	actionCleanup lifecycleAction = "cleanup"
)

type pendingLifecycleAction struct {
	action           lifecycleAction
	pane             paneView
	closeMode        lifecycle.CloseMode
	closeOptionIndex int
	requireWorktree  bool
}

type lifecycleDoneMsg struct {
	action lifecycleAction
	pane   paneView
	code   exitcode.Code
	output string
}

type lifecycleRunner interface {
	Close(lifecycle.Options, string, int, lifecycle.Logger) exitcode.Code
	CloseWithMode(lifecycle.Options, string, int, lifecycle.CloseMode, lifecycle.Logger) exitcode.Code
	CloseTask(lifecycle.Options, string, string, lifecycle.Logger) exitcode.Code
	CloseTaskWithMode(lifecycle.Options, string, string, lifecycle.CloseMode, lifecycle.Logger) exitcode.Code
	Merge(lifecycle.Options, string, int, lifecycle.Logger) exitcode.Code
	MergeTask(lifecycle.Options, string, string, lifecycle.Logger) exitcode.Code
	Cleanup(lifecycle.Options, string, lifecycle.Logger) exitcode.Code
	CleanupPlan(lifecycle.Options, string, lifecycle.Logger) exitcode.Code
}

type defaultLifecycleRunner struct{}

func (defaultLifecycleRunner) Close(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Close(opts, parent, issueNum, lg)
}

func (defaultLifecycleRunner) CloseWithMode(opts lifecycle.Options, parent string, issueNum int, mode lifecycle.CloseMode, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.CloseWithMode(opts, parent, issueNum, mode, lg)
}

func (defaultLifecycleRunner) CloseTask(opts lifecycle.Options, parent, taskID string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.CloseTask(opts, parent, taskID, lg)
}

func (defaultLifecycleRunner) CloseTaskWithMode(opts lifecycle.Options, parent, taskID string, mode lifecycle.CloseMode, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.CloseTaskWithMode(opts, parent, taskID, mode, lg)
}

func (defaultLifecycleRunner) Merge(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Merge(opts, parent, issueNum, lg)
}

func (defaultLifecycleRunner) MergeTask(opts lifecycle.Options, parent, taskID string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.MergeTask(opts, parent, taskID, lg)
}

func (defaultLifecycleRunner) Cleanup(opts lifecycle.Options, parent string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Cleanup(opts, parent, lg)
}

func (defaultLifecycleRunner) CleanupPlan(opts lifecycle.Options, parent string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.CleanupPlan(opts, parent, lg)
}

type actionLogger struct {
	w io.Writer
}

func (l actionLogger) Info(format string, a ...any) {
	fmt.Fprintf(l.w, "[info] %s\n", fmt.Sprintf(format, a...))
}

func (l actionLogger) Ok(format string, a ...any) {
	fmt.Fprintf(l.w, "[ ok ] %s\n", fmt.Sprintf(format, a...))
}

func (l actionLogger) Warn(format string, a ...any) {
	fmt.Fprintf(l.w, "[warn] %s\n", fmt.Sprintf(format, a...))
}

func (l actionLogger) Err(format string, a ...any) {
	fmt.Fprintf(l.w, "[err ] %s\n", fmt.Sprintf(format, a...))
}

func (l actionLogger) Stderr() io.Writer {
	return l.w
}

func (m model) updatePendingAction(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingAction.action == actionClose && !m.pendingAction.pane.isPaneOnly() {
		return m.updatePendingCloseChoice(msg)
	}
	switch msg.String() {
	case "y", "enter":
		pending := *m.pendingAction
		m.pendingAction = nil
		m.actionRunning = true
		m.actionMessage = lifecycleRunningMessage(pending)
		return m, m.lifecycleCmd(pending)
	case "n", "esc", "q", "ctrl+c":
		m.actionMessage = fmt.Sprintf("%s canceled", m.pendingAction.action)
		m.pendingAction = nil
		return m, nil
	}
	return m, nil
}

func (m model) updatePendingCloseChoice(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.pendingAction.closeOptionIndex = clampPendingCloseOptionIndex(m.pendingAction, m.pendingAction.closeOptionIndex-1)
		m.pendingAction.closeMode = closeOptions()[m.pendingAction.closeOptionIndex].mode
		return m, nil
	case "down", "j":
		m.pendingAction.closeOptionIndex = clampPendingCloseOptionIndex(m.pendingAction, m.pendingAction.closeOptionIndex+1)
		m.pendingAction.closeMode = closeOptions()[m.pendingAction.closeOptionIndex].mode
		return m, nil
	case "1", "2", "3":
		idx := int(msg.String()[0] - '1')
		m.pendingAction.closeOptionIndex = clampPendingCloseOptionIndex(m.pendingAction, idx)
		m.pendingAction.closeMode = closeOptions()[m.pendingAction.closeOptionIndex].mode
		return m, nil
	case "y", "enter":
		pending := *m.pendingAction
		if m.closeOnly {
			m.closeDone = true
			m.closeResult = pending.closeMode
			m.pendingAction = nil
			return m.quit()
		}
		m.pendingAction = nil
		m.mode = modeMonitor
		m.actionRunning = true
		m.actionMessage = lifecycleRunningMessage(pending)
		return m, m.lifecycleCmd(pending)
	case "n", "esc", "q", "ctrl+c":
		if m.closeOnly {
			m.closeDone = true
			m.closeCanceled = true
			m.pendingAction = nil
			return m.quit()
		}
		m.actionMessage = "close canceled"
		m.pendingAction = nil
		m.mode = modeMonitor
		return m, nil
	}
	return m, nil
}

func (m model) startPendingAction(action lifecycleAction) (tea.Model, tea.Cmd) {
	pane, ok := m.selectedPane()
	if !ok {
		m.actionMessage = "no pane selected"
		return m, nil
	}
	if reason := m.lifecycleActionDisabledReason(&pane, string(action)); reason != "" {
		m.actionMessage = reason
		return m, nil
	}
	m.pendingAction = &pendingLifecycleAction{action: action, pane: pane}
	if action == actionClose {
		m.pendingAction.closeMode = lifecycle.ClosePaneOnly
		if !pane.isPaneOnly() {
			m.pendingAction.requireWorktree = backend.NormalizeName(pane.Backend) == backend.Herdr
			m.pendingAction.closeOptionIndex = clampPendingCloseOptionIndex(m.pendingAction, 0)
			m.pendingAction.closeMode = closeOptions()[m.pendingAction.closeOptionIndex].mode
			return m.closeChoicePopupCmd()
		}
	}
	m.actionMessage = confirmMessage(action, pane)
	return m, nil
}

func (m model) lifecycleCmd(pending pendingLifecycleAction) tea.Cmd {
	routes := m.lifecycleActionRoutes(pending.pane)
	paneOpts := m.lifecycleOptions(routes.paneRoot)
	closeOpts := m.lifecycleOptionsForRoots(routes.closeRoots)
	cleanupOpts := m.lifecycleOptionsForRoots(routes.cleanupRoots)
	runner := m.opts.lifecycle
	return func() tea.Msg {
		var buf bytes.Buffer
		lg := actionLogger{w: &buf}
		code := runLifecycleAction(runner, pending, paneOpts, closeOpts, cleanupOpts, lg)
		return lifecycleDoneMsg{
			action: pending.action,
			pane:   pending.pane,
			code:   code,
			output: strings.TrimSpace(buf.String()),
		}
	}
}

type lifecycleActionRoutes struct {
	paneRoot     string
	closeRoots   []string
	cleanupRoots []string
}

func (m model) lifecycleActionRoutes(pane paneView) lifecycleActionRoutes {
	paneRoot := strings.TrimSpace(pane.sourceProjectRoot)
	if paneRoot == "" {
		paneRoot = m.opts.ProjectRoot
	}
	closeRoots := pane.sourceProjectRoots
	if len(closeRoots) == 0 {
		closeRoots = []string{paneRoot}
	}
	cleanupRoots := pane.sourceProjectRoots
	if !isLocalParent(pane.Parent) {
		cleanupRoots = m.sourceRootsForParent(pane.Parent)
	}
	if len(cleanupRoots) == 0 {
		cleanupRoots = []string{paneRoot}
	}
	return lifecycleActionRoutes{paneRoot: paneRoot, closeRoots: closeRoots, cleanupRoots: cleanupRoots}
}

func (m model) lifecycleOptions(root string) lifecycle.Options {
	var herdrRuntime lifecycle.HerdrRuntimeFactory
	if m.opts.LifecycleHerdrRuntimeForRoot != nil {
		herdrRuntime = m.opts.LifecycleHerdrRuntimeForRoot(root)
	}
	return lifecycle.Options{
		ProjectRoot:         root,
		StatePath:           state.Path(root),
		Hooks:               m.opts.Hooks,
		WatcherRunningLabel: m.opts.WatcherRunningLabel,
		RemoveIssueLabel:    ghissue.Runner{Cwd: m.opts.ProjectRoot}.RemoveIssueLabel,
		CloseOwned:          m.opts.LifecycleCloseOwned,
		HerdrRuntime:        herdrRuntime,
	}
}

func (m model) lifecycleOptionsForRoots(roots []string) []lifecycle.Options {
	opts := make([]lifecycle.Options, 0, len(roots))
	for _, root := range roots {
		opts = append(opts, m.lifecycleOptions(root))
	}
	return opts
}

func runLifecycleAction(runner lifecycleRunner, pending pendingLifecycleAction, paneOpts lifecycle.Options, closeOpts, cleanupOpts []lifecycle.Options, lg lifecycle.Logger) exitcode.Code {
	switch pending.action {
	case actionClose:
		return runCloseLifecycle(runner, pending, closeOpts, lg)
	case actionMerge:
		return runMergeLifecycle(runner, pending.pane, paneOpts, lg)
	case actionCleanup:
		return runCleanupLifecycle(runner, pending.pane, cleanupOpts, lg)
	default:
		lg.Err("unknown lifecycle action: %s", pending.action)
		return exitcode.Invocation
	}
}

func runCloseLifecycle(runner lifecycleRunner, pending pendingLifecycleAction, opts []lifecycle.Options, lg lifecycle.Logger) exitcode.Code {
	code := exitcode.OK
	for _, opt := range opts {
		var current exitcode.Code
		if pending.pane.isTask() {
			current = runner.CloseTaskWithMode(opt, pending.pane.Parent, pending.pane.TaskID, pending.closeMode, lg)
		} else {
			current = runner.CloseWithMode(opt, pending.pane.Parent, pending.pane.IssueNum, pending.closeMode, lg)
		}
		if current != exitcode.OK {
			code = current
		}
	}
	return code
}

func runMergeLifecycle(runner lifecycleRunner, pane paneView, opts lifecycle.Options, lg lifecycle.Logger) exitcode.Code {
	if pane.isTask() {
		return runner.MergeTask(opts, pane.Parent, pane.TaskID, lg)
	}
	return runner.Merge(opts, pane.Parent, pane.IssueNum, lg)
}

func runCleanupLifecycle(runner lifecycleRunner, pane paneView, opts []lifecycle.Options, lg lifecycle.Logger) exitcode.Code {
	code := exitcode.OK
	for _, opt := range opts {
		var current exitcode.Code
		if pane.isTask() {
			current = runner.CleanupPlan(opt, pane.Parent, lg)
		} else {
			current = runner.Cleanup(opt, pane.Parent, lg)
		}
		if current != exitcode.OK {
			code = current
		}
	}
	return code
}

// isLocalParent reports whether a parent ref is only meaningful within one
// worktree — a plan slug (plan:<slug>) or the synthetic manual ref (@manual) — as
// opposed to a globally-stable parent: a GitHub issue number, a Project URL, or
// @watch (repo-wide watcher panes keyed by real GitHub issue numbers). Locally
// scoped parents can collide across worktrees with unrelated work, so
// parent-scoped lifecycle actions must not fan across worktrees for them. Note
// not every @-prefixed ref is local — @watch is repo-wide — so this matches
// @manual exactly rather than the @ prefix.
func isLocalParent(parent string) bool {
	parent = strings.TrimSpace(parent)
	return strings.HasPrefix(parent, "plan:") || parent == "@manual"
}

// sourceRootsForParent returns the distinct worktree roots whose state.json
// recorded a pane under parent, so a parent-scoped cleanup reaches every store
// the aggregated Session spans. Synthetic not-started rows (no sourceProjectRoot)
// are skipped; if none of the parent's panes carry a source root the home root
// is used.
func (m model) sourceRootsForParent(parent string) []string {
	seen := map[string]bool{}
	var roots []string
	// Normalize so numeric parent aliases ("100" vs "0100") recorded in
	// different worktrees match, mirroring lifecycle.Cleanup's parentMatches —
	// otherwise an exact compare would skip an eligible sibling root.
	want := sessionview.NormalizeParent(parent)
	for _, p := range m.allPanes {
		if sessionview.NormalizeParent(p.Parent) != want {
			continue
		}
		// Union every owning root, including those of identities the loader
		// collapsed into this row (sourceProjectRoots), so cleanup reaches the
		// de-duplicated sibling stores too. Synthetic not-started rows carry none.
		for _, root := range p.sourceProjectRoots {
			if root = strings.TrimSpace(root); root == "" {
				continue
			}
			if !seen[root] {
				seen[root] = true
				roots = append(roots, root)
			}
		}
	}
	if len(roots) == 0 {
		roots = []string{m.opts.ProjectRoot}
	}
	return roots
}

func confirmMessage(action lifecycleAction, pane paneView) string {
	switch action {
	case actionClose:
		return fmt.Sprintf("confirm close %s? y/n", pane.identityLabel())
	case actionCleanup:
		return fmt.Sprintf("confirm cleanup for parent %s? y/n", dash(pane.Parent))
	default:
		return fmt.Sprintf("confirm %s %s? y/n", action, pane.identityLabel())
	}
}

func lifecycleResultMessage(msg lifecycleDoneMsg) string {
	prefix := fmt.Sprintf("%s %s", msg.action, msg.pane.identityLabel())
	if msg.action == actionCleanup {
		prefix = fmt.Sprintf("%s parent %s", msg.action, dash(msg.pane.Parent))
	}
	result := "ok"
	if msg.code != exitcode.OK {
		result = fmt.Sprintf("failed code=%d", msg.code)
	}
	if msg.output == "" {
		return prefix + ": " + result
	}
	return prefix + ": " + result + ": " + compactMessage(msg.output)
}

func lifecycleRunningMessage(pending pendingLifecycleAction) string {
	if pending.action == actionCleanup {
		return fmt.Sprintf("%s parent %s...", pending.action, dash(pending.pane.Parent))
	}
	if pending.action == actionClose {
		return fmt.Sprintf("%s %s...", closeModeVerb(pending.closeMode), pending.pane.identityLabel())
	}
	return fmt.Sprintf("%s %s...", pending.action, pending.pane.identityLabel())
}

func (m model) renderActionMessage() string {
	if m.pendingAction != nil || m.actionRunning {
		return warnStyle.Render(m.actionMessage)
	}
	return dimStyle.Render(m.actionMessage)
}

type closeOption struct {
	mode        lifecycle.CloseMode
	label       string
	description string
}

func closeOptions() []closeOption {
	return []closeOption{
		{mode: lifecycle.ClosePaneOnly, label: "Just close pane", description: "keep worktree and branch"},
		{mode: lifecycle.CloseWorktree, label: "Close and remove worktree", description: "keep branch"},
		{mode: lifecycle.CloseEverything, label: "Close and delete everything", description: "remove worktree and local branch"},
	}
}

func clampCloseOptionIndex(idx int) int {
	opts := closeOptions()
	if idx < 0 {
		return 0
	}
	if idx >= len(opts) {
		return len(opts) - 1
	}
	return idx
}

func clampPendingCloseOptionIndex(pending *pendingLifecycleAction, idx int) int {
	idx = clampCloseOptionIndex(idx)
	if pending != nil && pending.requireWorktree && idx == 0 {
		return 1
	}
	return idx
}

func closeModeVerb(mode lifecycle.CloseMode) string {
	switch mode {
	case lifecycle.ClosePaneOnly:
		return "close pane"
	case lifecycle.CloseEverything:
		return "delete"
	default:
		return "close"
	}
}

func paneOnlyKindLabel(pane paneView) string {
	if pane.isShell() {
		return "shell terminal"
	}
	if pane.isAttachedAgent() {
		return "attached agent"
	}
	return "pane"
}
