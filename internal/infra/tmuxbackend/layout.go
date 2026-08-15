package tmuxbackend

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

// layoutOps is the tmux IO the relayout needs, injected so the orchestration is
// unit testable without a real tmux.
type layoutOps interface {
	WindowGeometry(target string) (backend.Geometry, error)
	WindowPanes(windowID string) ([]backend.WindowPane, error)
	SplitSpacer(windowID string) (string, error)
	KillPane(paneID string) error
	ApplyLayout(windowID, layout string) error
	SelectMainVertical(windowID string, mainPaneWidth int) error
	SelectTiled(windowID string) error
}

type tmuxLayoutOps struct{}

func (tmuxLayoutOps) WindowGeometry(t string) (backend.Geometry, error) {
	return tmuxrun.WindowGeometry(t)
}

func (tmuxLayoutOps) WindowPanes(w string) ([]backend.WindowPane, error) {
	return tmuxrun.WindowPanes(w)
}
func (tmuxLayoutOps) SplitSpacer(w string) (string, error) { return tmuxrun.SplitSpacerPane(w) }
func (tmuxLayoutOps) KillPane(p string) error              { return tmuxrun.KillPane(p) }
func (tmuxLayoutOps) ApplyLayout(w, l string) error        { return tmuxrun.ApplyLayout(w, l) }
func (tmuxLayoutOps) SelectMainVertical(w string, mw int) error {
	return tmuxrun.SelectMainVertical(w, mw)
}
func (tmuxLayoutOps) SelectTiled(w string) error { return tmuxrun.SelectTiled(w) }

// layoutApplier holds the tmux ops, the comfort config, and the per-window
// resize memo that breaks the relayout-triggers-resize feedback loop.
type layoutApplier struct {
	ops  layoutOps
	cfg  layoutConfig
	mu   sync.Mutex
	memo map[string]string
}

var defaultLayoutApplier = &layoutApplier{
	ops:  tmuxLayoutOps{},
	cfg:  defaultLayoutConfig(),
	memo: map[string]string{},
}

// Relayout re-lays out the tmux window that holds target (a pane id, window id,
// or session name) into the fanout grid: a console sidebar (when present) plus
// a comfortable-width grid of the remaining panes, with an optional spacer. It
// is best-effort — a missing window or a rejected custom layout degrades
// quietly (main-vertical, then tiled) and never returns an error that should
// block pane creation or teardown.
func (*Backend) Relayout(target string, trigger backend.LayoutTrigger) error {
	return defaultLayoutApplier.apply(target, trigger)
}

func (a *layoutApplier) apply(target string, trigger backend.LayoutTrigger) error {
	// Serialize the whole apply: it is invoked concurrently from bubbletea
	// command goroutines (resize, create, close), and its tmux ops (list, split,
	// kill, select-layout) would otherwise interleave on the same window. The
	// memo helpers below assume this lock is held.
	a.mu.Lock()
	defer a.mu.Unlock()

	geom, err := a.ops.WindowGeometry(target)
	if err != nil {
		// Window gone or tmux unavailable: best-effort no-op.
		return nil
	}
	windowID := geom.WindowID
	win := layoutWindow{Width: geom.Width, Height: geom.Height}

	panes, err := a.ops.WindowPanes(windowID)
	if err != nil {
		return err
	}
	console, grid, spacers := partition(panes)

	// Resize dedup: our own relayout resizes the console pane, which makes the
	// TUI emit a fresh resize. The window geometry and pane set are unchanged by
	// that, so a matching signature means there is nothing to do. A create or a
	// close always changed the pane set, so they bypass the check.
	if trigger == backend.LayoutResize && a.memoMatch(windowID, win, console != nil, numericIDs(grid), numericIDs(spacers)) {
		return nil
	}

	cfg := a.cfg
	cfg.SidebarWidth = 0
	if console != nil {
		cfg.SidebarWidth = sidebarWidthDefault
	}

	// No grid panes: nothing to arrange. Drop any leftover spacers and let the
	// console (or lone pane) fill the window.
	if len(grid) == 0 {
		for _, s := range spacers {
			_ = a.ops.KillPane(s.ID)
		}
		a.store(windowID, win, console != nil, nil, nil)
		return nil
	}

	plan := decideLayoutPlan(win, len(grid), cfg)
	// Spacers only earn their keep with a resident TUI (console present) that
	// reconciles them on later relayouts; a one-shot batch run would leave the
	// blank pane orphaned, so skip it there. Always drop pre-existing spacers.
	desired := 0
	if plan.Spacer.Needed && console != nil {
		desired = 1
	}
	spacerIDs := a.reconcileSpacers(windowID, spacers, desired)

	gridIDs := numericIDs(grid)
	contentIDs := gridIDs
	lastCellSpacer := false
	if len(spacerIDs) > 0 {
		contentIDs = append(append([]string(nil), gridIDs...), spacerIDs...)
		lastCellSpacer = true
	}
	sidebarID := ""
	if console != nil {
		sidebarID = console.NumericID
	}

	applied := a.applyLayout(windowID, win, cfg, plan, sidebarID, contentIDs, lastCellSpacer)
	if !applied && len(spacerIDs) > 0 {
		// The coarse fallbacks do not place the spacer cell, so the blank pane
		// would dangle; remove it and record no spacer.
		for _, id := range spacerIDs {
			_ = a.ops.KillPane("%" + id)
		}
		spacerIDs = nil
	}
	// Only memoize when the custom grid actually applied. After a fallback we must
	// not let an identical resize short-circuit and leave the window stuck in the
	// coarse layout — the next relayout should retry the grid.
	if applied {
		a.store(windowID, win, console != nil, gridIDs, spacerIDs)
	}
	return nil
}

// applyLayout renders and applies the custom grid and reports whether that grid
// was applied. A comfortable plan whose custom layout tmux rejects cascades to
// main-vertical then tiled; an un-comfortable (too cramped) plan goes straight
// to tiled, the layout that handles dense windows best. Both fallbacks return
// false.
func (a *layoutApplier) applyLayout(windowID string, win layoutWindow, cfg layoutConfig, plan layoutPlan, sidebarID string, contentIDs []string, lastCellSpacer bool) bool {
	if plan.Comfortable {
		layout, err := renderLayout(renderLayoutInput{
			Win:            win,
			SidebarPaneID:  sidebarID,
			ContentPaneIDs: contentIDs,
			Cols:           plan.Cols,
			LastCellSpacer: lastCellSpacer,
			Cfg:            cfg,
		})
		if err == nil && a.ops.ApplyLayout(windowID, layout) == nil {
			return true
		}
		if a.ops.SelectMainVertical(windowID, cfg.SidebarWidth) != nil {
			_ = a.ops.SelectTiled(windowID)
		}
		return false
	}
	_ = a.ops.SelectTiled(windowID)
	return false
}

// reconcileSpacers brings the window's spacer panes to the desired count
// (0 or 1), killing surplus and splitting deficit, and returns the numeric ids
// of the spacers that remain. It is self-healing: stale spacers from a crashed
// run are reconciled away.
func (a *layoutApplier) reconcileSpacers(windowID string, existing []backend.WindowPane, desired int) []string {
	for i := desired; i < len(existing); i++ {
		_ = a.ops.KillPane(existing[i].ID)
	}
	var kept []string
	for i := 0; i < desired && i < len(existing); i++ {
		kept = append(kept, existing[i].NumericID)
	}
	for i := len(existing); i < desired; i++ {
		id, err := a.ops.SplitSpacer(windowID)
		if err != nil {
			break // best-effort: lay out with whatever spacers we have
		}
		kept = append(kept, strings.TrimPrefix(strings.TrimSpace(id), "%"))
	}
	return kept
}

// partition splits a window's panes into the console (if any), the grid panes in
// stable pane-index order, and existing spacers.
func partition(panes []backend.WindowPane) (console *backend.WindowPane, grid, spacers []backend.WindowPane) {
	for i := range panes {
		p := panes[i]
		switch {
		case p.Spacer:
			spacers = append(spacers, p)
		case p.Role == backend.RoleConsole && console == nil:
			c := p
			console = &c
		default:
			grid = append(grid, p)
		}
	}
	sort.Slice(grid, func(i, j int) bool { return grid[i].Index < grid[j].Index })
	return console, grid, spacers
}

// memoCap bounds the per-window resize-dedup memo. tmux window ids are
// monotonic and never reused, so without a cap a long-lived TUI would accumulate
// one stale entry per window forever. The memo is only a dedup hint, so dropping
// it wholesale at the cap is harmless (the next resize just recomputes).
const memoCap = 256

// memoMatch reports whether the window's last applied signature is unchanged.
// The caller must hold a.mu.
func (a *layoutApplier) memoMatch(windowID string, win layoutWindow, hasConsole bool, gridIDs, spacerIDs []string) bool {
	prev, ok := a.memo[windowID]
	return ok && prev == layoutSignature(win, hasConsole, gridIDs, spacerIDs)
}

// store records the window's signature. The caller must hold a.mu.
func (a *layoutApplier) store(windowID string, win layoutWindow, hasConsole bool, gridIDs, spacerIDs []string) {
	if len(a.memo) >= memoCap {
		a.memo = make(map[string]string)
	}
	a.memo[windowID] = layoutSignature(win, hasConsole, gridIDs, spacerIDs)
}

// layoutSignature is the resize-dedup key: window size, console presence, and
// the sorted grid/spacer id sets. It deliberately ignores ordering so an
// identical arrangement maps to one signature.
func layoutSignature(win layoutWindow, hasConsole bool, gridIDs, spacerIDs []string) string {
	g := append([]string(nil), gridIDs...)
	sort.Strings(g)
	s := append([]string(nil), spacerIDs...)
	sort.Strings(s)
	return fmt.Sprintf("%dx%d|c=%t|g=%s|sp=%s", win.Width, win.Height, hasConsole, strings.Join(g, ","), strings.Join(s, ","))
}

func numericIDs(panes []backend.WindowPane) []string {
	out := make([]string, len(panes))
	for i, p := range panes {
		out[i] = p.NumericID
	}
	return out
}
