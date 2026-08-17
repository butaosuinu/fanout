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

// apply serializes the whole relayout: it is invoked concurrently from
// bubbletea command goroutines (resize, create, close), and its tmux ops (list,
// split, kill, select-layout) would otherwise interleave on the same window.
// Every helper it calls assumes a.mu is held.
func (a *layoutApplier) apply(target string, trigger backend.LayoutTrigger) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	geom, ok := a.windowGeometry(target)
	if !ok {
		return nil
	}
	w, err := a.readWindow(geom)
	if err != nil {
		return err
	}

	// Resize dedup: our own relayout resizes the console pane, which makes the
	// TUI emit a fresh resize. The window geometry and pane set are unchanged by
	// that, so a matching signature means there is nothing to do. A create or a
	// close always changed the pane set, so they bypass the check.
	if trigger == backend.LayoutResize && a.memoMatch(w.ID, w.Win, w.hasConsole(), numericIDs(w.Grid), numericIDs(w.Spacers)) {
		return nil
	}

	cfg := a.cfg
	cfg.SidebarWidth = w.sidebarWidth()

	if len(w.Grid) == 0 {
		a.clearGridlessWindow(w)
		return nil
	}
	a.arrangeGrid(w, cfg)
	return nil
}

// windowState is the live window a single relayout pass works on: its id, its
// interior geometry, and its panes split by role.
type windowState struct {
	ID      string
	Win     layoutWindow
	Console *backend.WindowPane
	Grid    []backend.WindowPane
	Spacers []backend.WindowPane
}

func (w windowState) hasConsole() bool { return w.Console != nil }

// sidebarWidth is the width the layout reserves for the console sidebar: the
// default console width when the window has one, otherwise no reservation.
func (w windowState) sidebarWidth() int {
	if !w.hasConsole() {
		return 0
	}
	return sidebarWidthDefault
}

// windowGeometry resolves the window that holds target. ok is false when there
// is nothing to lay out — the window is gone or tmux is unavailable — which is
// not an error: the relayout is best-effort and must never fail pane creation
// or teardown.
func (a *layoutApplier) windowGeometry(target string) (backend.Geometry, bool) {
	geom, err := a.ops.WindowGeometry(target)
	return geom, err == nil
}

// readWindow lists the window's panes and splits them by role.
func (a *layoutApplier) readWindow(geom backend.Geometry) (windowState, error) {
	panes, err := a.ops.WindowPanes(geom.WindowID)
	if err != nil {
		return windowState{}, err
	}
	console, grid, spacers := partition(panes)
	return windowState{
		ID:      geom.WindowID,
		Win:     layoutWindow{Width: geom.Width, Height: geom.Height},
		Console: console,
		Grid:    grid,
		Spacers: spacers,
	}, nil
}

// clearGridlessWindow handles a window with no grid panes: there is nothing to
// arrange, so drop any leftover spacer and let the console (or the lone pane)
// fill the window.
func (a *layoutApplier) clearGridlessWindow(w windowState) {
	for _, s := range w.Spacers {
		_ = a.ops.KillPane(s.ID)
	}
	a.store(w.ID, w.Win, w.hasConsole(), nil, nil)
}

// arrangeGrid lays the grid panes out: it reconciles the spacer pane against the
// plan, applies the grid, and memoizes the window signature only when the custom
// grid actually took effect. After a fallback we must not let an identical resize
// short-circuit and leave the window stuck in the coarse layout — the next
// relayout should retry the grid.
func (a *layoutApplier) arrangeGrid(w windowState, cfg layoutConfig) {
	plan := decideLayoutPlan(w.Win, len(w.Grid), cfg)
	spacerIDs := a.reconcileSpacers(w.ID, w.Spacers, desiredSpacers(plan, w))

	gridIDs := numericIDs(w.Grid)
	contentIDs, lastCellSpacer := contentCells(gridIDs, spacerIDs)
	sidebarID := ""
	if w.hasConsole() {
		sidebarID = w.Console.NumericID
	}

	if !a.applyLayout(w.ID, w.Win, cfg, plan, sidebarID, contentIDs, lastCellSpacer) {
		// The coarse fallbacks do not place the spacer cell, so the blank pane
		// would dangle; remove it and record nothing.
		for _, id := range spacerIDs {
			_ = a.ops.KillPane("%" + id)
		}
		return
	}
	a.store(w.ID, w.Win, w.hasConsole(), gridIDs, spacerIDs)
}

// desiredSpacers is how many spacer panes the window should end up with. Spacers
// only earn their keep with a resident TUI (console present) that reconciles them
// on later relayouts; a one-shot batch run would leave the blank pane orphaned,
// so skip it there. Pre-existing spacers are always dropped.
func desiredSpacers(plan layoutPlan, w windowState) int {
	if plan.Spacer.Needed && w.hasConsole() {
		return 1
	}
	return 0
}

// contentCells returns the grid's cell ids in row-major order — the grid panes,
// then the spacer when one is used — and whether that trailing cell is a spacer.
func contentCells(gridIDs, spacerIDs []string) (contentIDs []string, lastCellSpacer bool) {
	if len(spacerIDs) == 0 {
		return gridIDs, false
	}
	return append(append([]string(nil), gridIDs...), spacerIDs...), true
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
