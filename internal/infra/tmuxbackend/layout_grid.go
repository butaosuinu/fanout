package tmuxbackend

import (
	"errors"
	"fmt"
	"strings"
)

// This file computes the dmux-style auto layout for the tmux window that holds
// fanout's panes: a fixed-width sidebar (the TUI console pane) plus a grid of
// the remaining panes sized for comfortable widths, with an optional spacer
// pane absorbing excess width. The calculation (decideLayoutPlan/renderLayout)
// is pure and imports only the standard library; layout.go drives it against a
// live tmux window.
//
// The algorithm mirrors dmux (github.com/standardagents/dmux): the column
// count is chosen by scoring candidates on balance, vertical space, and
// comfortable width, and the layout is emitted as a tmux custom layout string
// with a 16-bit checksum (layout_checksum.go).

// layoutConfig holds the comfort thresholds. The defaults match dmux.
type layoutConfig struct {
	SidebarWidth         int // reserved console/sidebar width; 0 = no sidebar
	MinComfortableWidth  int // feasibility floor and comfort lower bound
	MaxComfortableWidth  int // panes wider than this are penalized / capped
	MinComfortableHeight int // minimum row height for a feasible layout
	MinSpacerWidth       int // a spacer is only worth adding above this width
}

// defaultLayoutConfig returns dmux's constants. SidebarWidth defaults to 0 (no
// sidebar); callers set it to the console width when a console pane exists.
func defaultLayoutConfig() layoutConfig {
	return layoutConfig{
		SidebarWidth:         0,
		MinComfortableWidth:  60,
		MaxComfortableWidth:  100,
		MinComfortableHeight: 15,
		MinSpacerWidth:       20,
	}
}

// sidebarWidthDefault is the console sidebar width used when a console pane is
// present, matching dmux's SIDEBAR_WIDTH.
const sidebarWidthDefault = 40

// layoutWindow is a tmux window's interior geometry (status bar excluded).
type layoutWindow struct {
	Width, Height int
}

// layoutPlan is the pure layout decision: how many columns/rows, how content
// panes are distributed per row, whether the result is comfortable, and whether
// a spacer pane is warranted. It carries no pane ids so it can be computed in
// dry-run from a count alone.
type layoutPlan struct {
	Cols             int
	Rows             int
	PaneDistribution []int // content panes per row; len == Rows
	Comfortable      bool  // false: caller should consider a coarser fallback
	Spacer           spacerPlan
}

// spacerPlan describes the single optional spacer cell at the end of the last
// row. dmux only ever adds one spacer.
type spacerPlan struct {
	Needed bool
	Width  int
}

// decideLayoutPlan chooses a column count for contentPaneCount grid panes (the
// sidebar and any spacer are not counted) and decides whether a spacer is
// warranted. It returns a zero layoutPlan for non-positive inputs.
func decideLayoutPlan(win layoutWindow, contentPaneCount int, cfg layoutConfig) layoutPlan {
	if contentPaneCount <= 0 || win.Width <= 0 || win.Height <= 0 {
		return layoutPlan{}
	}
	var best, feasibleBest *layoutPlan
	bestScore, feasibleScore := -1.0, -1.0

	// Descend so that, on a score tie, the smaller column count (wider panes)
	// is the one that ends up retained — matching dmux's tie-break.
	for cols := contentPaneCount; cols >= 1; cols-- {
		cand, score := scoreColumnCount(win, contentPaneCount, cols, cfg)
		if better(score, bestScore, cols, best) {
			bestScore = score
			c := cand
			best = &c
		}
		if cand.Comfortable && better(score, feasibleScore, cols, feasibleBest) {
			feasibleScore = score
			c := cand
			feasibleBest = &c
		}
	}

	chosen := best
	if feasibleBest != nil { // prefer a feasible layout; fall back to best-effort
		chosen = feasibleBest
	}
	chosen.Spacer = decideSpacer(win, *chosen, cfg)
	return *chosen
}

// scoreColumnCount builds the plan that arranges contentPaneCount panes in cols
// columns and scores it. The returned plan carries whether the window can hold
// that grid comfortably; the score ranks it against the other column counts.
func scoreColumnCount(win layoutWindow, contentPaneCount, cols int, cfg layoutConfig) (layoutPlan, float64) {
	rows := ceilDiv(contentPaneCount, cols)
	paneWidth := float64(win.Width-sidebarReserve(cfg)-(cols-1)) / float64(cols) // not floored: score input
	paneHeight := (win.Height - (rows - 1)) / rows                               // int division = floor

	plan := layoutPlan{
		Cols:             cols,
		Rows:             rows,
		PaneDistribution: distribute(contentPaneCount, cols, rows),
	}
	score := scoreLayout(plan, paneWidth, paneHeight, win.Height, cfg)
	plan.Comfortable = fitsComfortably(win, plan, cfg)
	return plan, score
}

// fitsComfortably reports whether the window can hold the plan's grid at the
// minimum comfortable pane size, sidebar and borders included.
func fitsComfortably(win layoutWindow, p layoutPlan, cfg layoutConfig) bool {
	minFeasibleW := max(1, min(cfg.MinComfortableWidth, cfg.MaxComfortableWidth))
	minReqW := sidebarReserve(cfg) + p.Cols*minFeasibleW + (p.Cols - 1)
	minReqH := p.Rows*cfg.MinComfortableHeight + (p.Rows - 1)
	return minReqW <= win.Width && minReqH <= win.Height
}

// scoreLayout reproduces dmux's balanceScore * heightScore * widthScore.
func scoreLayout(p layoutPlan, paneWidth float64, paneHeight, winHeight int, cfg layoutConfig) float64 {
	balance := 1.0
	if p.PaneDistribution[p.Rows-1] == 1 {
		balance = 0.5 // a lone pane in the last row reads as unbalanced
	}
	height := float64(paneHeight) / float64(winHeight)
	width := 1.0
	if paneWidth > float64(cfg.MaxComfortableWidth) {
		width = 0.8
	}
	return balance * height * width
}

// better reports whether score beats the incumbent, treating an equal score as
// an improvement only when the candidate uses fewer columns.
func better(score, bestScore float64, cols int, incumbent *layoutPlan) bool {
	if score > bestScore {
		return true
	}
	return score == bestScore && (incumbent == nil || cols < incumbent.Cols)
}

// decideSpacer mirrors dmux's SpacerManager: add one spacer when the last row
// is short and its panes would otherwise stretch past MaxComfortableWidth, and
// the reclaimed width is itself worth a pane.
func decideSpacer(win layoutWindow, p layoutPlan, cfg layoutConfig) spacerPlan {
	if p.Cols <= 1 || p.Rows == 0 {
		return spacerPlan{}
	}
	panesInLastRow := p.PaneDistribution[p.Rows-1]
	if panesInLastRow == p.Cols {
		return spacerPlan{}
	}
	contentWidth := win.Width - sidebarReserve(cfg)
	available := contentWidth - (panesInLastRow - 1)
	widthPerPane := float64(available) / float64(panesInLastRow)
	if widthPerPane <= float64(cfg.MaxComfortableWidth) {
		return spacerPlan{}
	}
	// With a spacer, the last row holds panesInLastRow capped content panes plus
	// one spacer cell, so there are panesInLastRow borders.
	spacerWidth := contentWidth - panesInLastRow*cfg.MaxComfortableWidth - panesInLastRow
	if spacerWidth < cfg.MinSpacerWidth {
		return spacerPlan{}
	}
	return spacerPlan{Needed: true, Width: spacerWidth}
}

// renderLayoutInput is the input to renderLayout. ContentPaneIDs are the
// numeric pane ids (the digits of "%N", without the leading '%') in row-major
// order; when a spacer is used its id is the final element and LastCellSpacer
// is true.
type renderLayoutInput struct {
	Win            layoutWindow
	SidebarPaneID  string // numeric console pane id; "" for no sidebar
	ContentPaneIDs []string
	Cols           int
	LastCellSpacer bool
	Cfg            layoutConfig
}

// renderLayout builds a tmux custom layout string of the form
// "checksum,WxH,0,0{sidebar,content}" (or just the content node when there is
// no sidebar). It returns an error when the geometry cannot hold the panes so
// the caller can fall back.
func renderLayout(in renderLayoutInput) (string, error) {
	geom, err := contentGeometry(in)
	if err != nil {
		return "", err
	}
	gridRows := make([]string, 0, geom.Rows)
	for _, row := range splitRows(in, geom) {
		gridRows = append(gridRows, renderRow(row, geom, in.Cfg))
	}

	content := gridRows[0]
	if len(gridRows) > 1 {
		content = fmt.Sprintf("%dx%d,%d,0[%s]", geom.ContentWidth, in.Win.Height, geom.ContentStartX, strings.Join(gridRows, ","))
	}

	body := content
	if in.SidebarPaneID != "" && in.Cfg.SidebarWidth > 0 {
		sidebar := fmt.Sprintf("%dx%d,0,0,%s", in.Cfg.SidebarWidth, in.Win.Height, in.SidebarPaneID)
		body = fmt.Sprintf("%dx%d,0,0{%s,%s}", in.Win.Width, in.Win.Height, sidebar, content)
	}
	return layoutChecksum(body) + "," + body, nil
}

// contentGeom is the validated area the grid is drawn into: the content width
// and its left edge (after the sidebar reserve), the row count, and the height
// every row but the last one gets.
type contentGeom struct {
	ContentWidth  int
	ContentStartX int
	Rows          int
	PaneHeight    int
}

// contentGeometry validates the render input and derives the content area from
// it. It errors when the window cannot hold the panes, so renderLayout's caller
// can fall back to a coarser tmux layout.
func contentGeometry(in renderLayoutInput) (contentGeom, error) {
	n := len(in.ContentPaneIDs)
	switch {
	case n == 0:
		return contentGeom{}, errors.New("tmux layout: no content panes")
	case in.Cols < 1:
		return contentGeom{}, errors.New("tmux layout: cols must be >= 1")
	case in.Win.Width <= 0 || in.Win.Height <= 0:
		return contentGeom{}, errors.New("tmux layout: window dimensions must be positive")
	}
	reserve := sidebarReserve(in.Cfg)
	contentWidth := in.Win.Width - reserve
	if contentWidth <= 0 {
		return contentGeom{}, errors.New("tmux layout: content width must be positive")
	}
	rows := ceilDiv(n, in.Cols)
	paneHeight := (in.Win.Height - (rows - 1)) / rows
	if paneHeight <= 0 {
		return contentGeom{}, errors.New("tmux layout: pane height must be positive")
	}
	return contentGeom{
		ContentWidth:  contentWidth,
		ContentStartX: reserve,
		Rows:          rows,
		PaneHeight:    paneHeight,
	}, nil
}

// layoutRow is one row of the content grid: the panes it holds in left-to-right
// order, where it sits vertically, and whether its trailing cell is the spacer.
type layoutRow struct {
	PaneIDs []string
	Y       int
	Height  int
	Spacer  bool
}

// splitRows slices the content panes into rows of at most Cols and places each
// row vertically. The last row absorbs the leftover height and carries the
// spacer cell when one is used.
func splitRows(in renderLayoutInput, geom contentGeom) []layoutRow {
	rows := make([]layoutRow, 0, geom.Rows)
	idx, y := 0, 0
	for r := range geom.Rows {
		panesInRow := min(in.Cols, len(in.ContentPaneIDs)-idx)
		row := layoutRow{
			PaneIDs: in.ContentPaneIDs[idx : idx+panesInRow],
			Y:       y,
			Height:  geom.PaneHeight,
		}
		if r == geom.Rows-1 {
			row.Height = in.Win.Height - y // last row absorbs the remainder
			row.Spacer = in.LastCellSpacer
		}
		rows = append(rows, row)
		idx += panesInRow
		y += geom.PaneHeight + 1
	}
	return rows
}

// renderRow emits one row's layout node: a cell per pane laid out left to right,
// wrapped in a container unless the row holds a single pane.
func renderRow(row layoutRow, geom contentGeom, cfg layoutConfig) string {
	widths := rowWidths(geom.ContentWidth, len(row.PaneIDs), row.Spacer, cfg)
	cells := make([]string, len(row.PaneIDs))
	x := geom.ContentStartX
	for c, id := range row.PaneIDs {
		cells[c] = fmt.Sprintf("%dx%d,%d,%d,%s", widths[c], row.Height, x, row.Y, id)
		x += widths[c] + 1 // cell width plus one border column
	}
	if len(cells) == 1 {
		return cells[0] // a single pane needs no container
	}
	return fmt.Sprintf("%dx%d,%d,%d{%s}", geom.ContentWidth, row.Height, geom.ContentStartX, row.Y, strings.Join(cells, ","))
}

// rowWidths distributes contentWidth across panesInRow cells. Without a spacer
// the remainder lands on the first pane; with a spacer the content panes are
// capped at MaxComfortableWidth and the trailing spacer cell takes the rest.
func rowWidths(contentWidth, panesInRow int, spacer bool, cfg layoutConfig) []int {
	w := make([]int, panesInRow)
	if spacer {
		nContent := panesInRow - 1
		for i := range nContent {
			w[i] = cfg.MaxComfortableWidth
		}
		borders := panesInRow - 1
		w[panesInRow-1] = contentWidth - nContent*cfg.MaxComfortableWidth - borders
		return w
	}
	available := contentWidth - (panesInRow - 1)
	even := available / panesInRow
	for i := range w {
		w[i] = even
	}
	w[0] += available - even*panesInRow // first pane absorbs the remainder
	return w
}

// sidebarReserve is the width consumed by the sidebar and its border column.
func sidebarReserve(cfg layoutConfig) int {
	if cfg.SidebarWidth > 0 {
		return cfg.SidebarWidth + 1
	}
	return 0
}

// distribute returns the content-pane count for each row, row-major: full rows
// of cols, then the remainder in the last row.
func distribute(n, cols, rows int) []int {
	out := make([]int, rows)
	for i := range rows - 1 {
		out[i] = cols
	}
	last := n % cols
	if last == 0 {
		last = cols
	}
	out[rows-1] = last
	return out
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }
