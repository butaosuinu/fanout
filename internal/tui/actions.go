package tui

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/lifecycle"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
)

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

type lifecycleRunner interface {
	Close(lifecycle.Options, string, int, lifecycle.Logger) exitcode.Code
	CloseTask(lifecycle.Options, string, string, lifecycle.Logger) exitcode.Code
	Merge(lifecycle.Options, string, int, lifecycle.Logger) exitcode.Code
	MergeTask(lifecycle.Options, string, string, lifecycle.Logger) exitcode.Code
	Cleanup(lifecycle.Options, string, lifecycle.Logger) exitcode.Code
	CleanupPlan(lifecycle.Options, string, lifecycle.Logger) exitcode.Code
}

type defaultLifecycleRunner struct{}

func (defaultLifecycleRunner) Close(opts lifecycle.Options, parent string, issueNum int, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.Close(opts, parent, issueNum, lg)
}

func (defaultLifecycleRunner) CloseTask(opts lifecycle.Options, parent, taskID string, lg lifecycle.Logger) exitcode.Code {
	return lifecycle.CloseTask(opts, parent, taskID, lg)
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

func (m model) startPendingAction(action lifecycleAction) (tea.Model, tea.Cmd) {
	pane, ok := m.selectedPane()
	if !ok {
		m.actionMessage = "no pane selected"
		return m, nil
	}
	if pane.isShell() && action != actionClose {
		m.actionMessage = fmt.Sprintf("%s unavailable for shell terminal", action)
		return m, nil
	}
	m.pendingAction = &pendingLifecycleAction{action: action, pane: pane}
	m.actionMessage = confirmMessage(action, pane)
	return m, nil
}

func (m model) lifecycleCmd(pending pendingLifecycleAction) tea.Cmd {
	// Close/merge act on one recorded pane, so route to the worktree that
	// recorded it. With cross-worktree aggregation that row may live in a sibling
	// worktree's state.json; using m.opts.ProjectRoot unconditionally would
	// target the wrong (often empty) store and fail with "not recorded".
	paneRoot := strings.TrimSpace(pending.pane.sourceProjectRoot)
	if paneRoot == "" {
		paneRoot = m.opts.ProjectRoot
	}
	// The watcher "running" label is removed from the GitHub issue on
	// close/merge/cleanup; it is repo-scoped, so the gh runner stays rooted at the
	// home checkout while state ops route to each owning worktree.
	watcherLabel := m.opts.WatcherRunningLabel
	removeLabel := ghissue.Runner{Cwd: m.opts.ProjectRoot}.RemoveIssueLabel
	lifecycleOpts := func(root string) lifecycle.Options {
		return lifecycle.Options{
			ProjectRoot:         root,
			StatePath:           state.Path(root),
			Hooks:               m.opts.Hooks,
			WatcherRunningLabel: watcherLabel,
			RemoveIssueLabel:    removeLabel,
		}
	}
	paneOpts := lifecycleOpts(paneRoot)
	// Close removes the row from its owning store(s). When the same logical child
	// was recorded in several worktrees the loader collapses it to one displayed
	// row but keeps every owning root here, so close each — otherwise the
	// de-duplicated sibling row survives and reappears on the next refresh.
	closeRoots := pending.pane.sourceProjectRoots
	if len(closeRoots) == 0 {
		closeRoots = []string{paneRoot}
	}
	// Cleanup is parent-scoped. For a globally-stable parent (a GitHub issue or
	// Project) the same parent in two worktrees is the same Session, so clean
	// every source root it spans — otherwise sibling rows survive and reappear on
	// the next refresh. But a locally-scoped parent (plan:<slug>, @manual) is only
	// meaningful within its worktree: two worktrees can hold unrelated plans under
	// the same slug, so cleanup must stay within the selected pane's own root(s).
	var cleanupRoots []string
	if isLocalParent(pending.pane.Parent) {
		cleanupRoots = pending.pane.sourceProjectRoots
		if len(cleanupRoots) == 0 {
			cleanupRoots = []string{paneRoot}
		}
	} else {
		cleanupRoots = m.sourceRootsForParent(pending.pane.Parent)
	}
	runner := m.opts.lifecycle
	return func() tea.Msg {
		var buf bytes.Buffer
		lg := actionLogger{w: &buf}
		var code exitcode.Code
		switch pending.action {
		case actionClose:
			code = exitcode.OK
			for _, r := range closeRoots {
				opts := lifecycleOpts(r)
				var c exitcode.Code
				if pending.pane.isTask() {
					c = runner.CloseTask(opts, pending.pane.Parent, pending.pane.TaskID, lg)
				} else {
					c = runner.Close(opts, pending.pane.Parent, pending.pane.IssueNum, lg)
				}
				if c != exitcode.OK {
					code = c
				}
			}
		case actionMerge:
			if pending.pane.isTask() {
				code = runner.MergeTask(paneOpts, pending.pane.Parent, pending.pane.TaskID, lg)
			} else {
				code = runner.Merge(paneOpts, pending.pane.Parent, pending.pane.IssueNum, lg)
			}
		case actionCleanup:
			code = exitcode.OK
			for _, r := range cleanupRoots {
				opts := lifecycleOpts(r)
				var c exitcode.Code
				if pending.pane.isTask() {
					c = runner.CleanupPlan(opts, pending.pane.Parent, lg)
				} else {
					c = runner.Cleanup(opts, pending.pane.Parent, lg)
				}
				if c != exitcode.OK {
					code = c
				}
			}
		default:
			code = exitcode.Invocation
			fmt.Fprintf(&buf, "[err ] unknown lifecycle action: %s\n", pending.action)
		}
		return lifecycleDoneMsg{
			action: pending.action,
			pane:   pending.pane,
			code:   code,
			output: strings.TrimSpace(buf.String()),
		}
	}
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
	return fmt.Sprintf("%s %s...", pending.action, pending.pane.identityLabel())
}

func (m model) renderActionMessage() string {
	if m.pendingAction != nil || m.actionRunning {
		return warnStyle.Render(m.actionMessage)
	}
	return dimStyle.Render(m.actionMessage)
}
