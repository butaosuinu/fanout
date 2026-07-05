// Package panelayout computes and applies a dmux-style auto layout for the
// tmux window that holds fanout's panes: a fixed-width sidebar (the TUI console
// pane) plus a grid of the remaining panes sized for comfortable widths, with
// an optional spacer pane absorbing excess width. The pure calculation
// (DecidePlan/Render/Checksum) lives here and imports only the standard
// library; the tmux IO orchestration (Apply) lives in apply.go behind an
// injected ops interface.
//
// The algorithm mirrors dmux (github.com/standardagents/dmux): the column
// count is chosen by scoring candidates on balance, vertical space, and
// comfortable width, and the layout is emitted as a tmux custom layout string
// with a 16-bit checksum.
package panelayout

import (
	"errors"
	"fmt"
	"strings"
)

// Config holds the comfort thresholds. The defaults match dmux.
type Config struct {
	SidebarWidth         int // reserved console/sidebar width; 0 = no sidebar
	MinComfortableWidth  int // feasibility floor and comfort lower bound
	MaxComfortableWidth  int // panes wider than this are penalized / capped
	MinComfortableHeight int // minimum row height for a feasible layout
	MinSpacerWidth       int // a spacer is only worth adding above this width
}

// DefaultConfig returns dmux's constants. SidebarWidth defaults to 0 (no
// sidebar); callers set it to the console width when a console pane exists.
func DefaultConfig() Config {
	return Config{
		SidebarWidth:         0,
		MinComfortableWidth:  60,
		MaxComfortableWidth:  100,
		MinComfortableHeight: 15,
		MinSpacerWidth:       20,
	}
}

// SidebarWidthDefault is the console sidebar width used when a console pane is
// present, matching dmux's SIDEBAR_WIDTH.
const SidebarWidthDefault = 40

// Window is a tmux window's interior geometry (status bar already excluded).
type Window struct {
	Width, Height int
}

// Plan is the pure layout decision: how many columns/rows, how content panes
// are distributed per row, whether the result is comfortable, and whether a
// spacer pane is warranted. It carries no pane ids so it can be computed in
// dry-run from a count alone.
type Plan struct {
	Cols             int
	Rows             int
	PaneDistribution []int // content panes per row; len == Rows
	Comfortable      bool  // false: caller should consider a coarser fallback
	Spacer           SpacerPlan
}

// SpacerPlan describes the single optional spacer cell at the end of the last
// row. dmux only ever adds one spacer.
type SpacerPlan struct {
	Needed bool
	Width  int
}

// DecidePlan chooses a column count for contentPaneCount grid panes (the
// sidebar and any spacer are not counted) and decides whether a spacer is
// warranted. It returns a zero Plan for non-positive inputs.
func DecidePlan(win Window, contentPaneCount int, cfg Config) Plan {
	if contentPaneCount <= 0 || win.Width <= 0 || win.Height <= 0 {
		return Plan{}
	}
	reserve := sidebarReserve(cfg)
	minFeasibleW := max(1, min(cfg.MinComfortableWidth, cfg.MaxComfortableWidth))

	var best, feasibleBest *Plan
	bestScore, feasibleScore := -1.0, -1.0

	// Descend so that, on a score tie, the smaller column count (wider panes)
	// is the one that ends up retained — matching dmux's tie-break.
	for cols := contentPaneCount; cols >= 1; cols-- {
		rows := ceilDiv(contentPaneCount, cols)
		paneWidth := float64(win.Width-reserve-(cols-1)) / float64(cols) // not floored: score input
		paneHeight := (win.Height - (rows - 1)) / rows                   // int division = floor

		cand := Plan{
			Cols:             cols,
			Rows:             rows,
			PaneDistribution: distribute(contentPaneCount, cols, rows),
		}
		score := scoreLayout(cand, paneWidth, paneHeight, win.Height, cfg)

		minReqW := reserve + cols*minFeasibleW + (cols - 1)
		minReqH := rows*cfg.MinComfortableHeight + (rows - 1)
		feasible := minReqW <= win.Width && minReqH <= win.Height
		cand.Comfortable = feasible

		if better(score, bestScore, cols, best) {
			bestScore = score
			c := cand
			best = &c
		}
		if feasible && better(score, feasibleScore, cols, feasibleBest) {
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

// scoreLayout reproduces dmux's balanceScore * heightScore * widthScore.
func scoreLayout(p Plan, paneWidth float64, paneHeight, winHeight int, cfg Config) float64 {
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
func better(score, bestScore float64, cols int, incumbent *Plan) bool {
	if score > bestScore {
		return true
	}
	return score == bestScore && (incumbent == nil || cols < incumbent.Cols)
}

// decideSpacer mirrors dmux's SpacerManager: add one spacer when the last row
// is short and its panes would otherwise stretch past MaxComfortableWidth, and
// the reclaimed width is itself worth a pane.
func decideSpacer(win Window, p Plan, cfg Config) SpacerPlan {
	if p.Cols <= 1 || p.Rows == 0 {
		return SpacerPlan{}
	}
	panesInLastRow := p.PaneDistribution[p.Rows-1]
	if panesInLastRow == p.Cols {
		return SpacerPlan{}
	}
	contentWidth := win.Width - sidebarReserve(cfg)
	available := contentWidth - (panesInLastRow - 1)
	widthPerPane := float64(available) / float64(panesInLastRow)
	if widthPerPane <= float64(cfg.MaxComfortableWidth) {
		return SpacerPlan{}
	}
	// With a spacer, the last row holds panesInLastRow capped content panes plus
	// one spacer cell, so there are panesInLastRow borders.
	spacerWidth := contentWidth - panesInLastRow*cfg.MaxComfortableWidth - panesInLastRow
	if spacerWidth < cfg.MinSpacerWidth {
		return SpacerPlan{}
	}
	return SpacerPlan{Needed: true, Width: spacerWidth}
}

// RenderInput is the input to Render. ContentPaneIDs are the numeric pane ids
// (the digits of "%N", without the leading '%') in row-major order; when a
// spacer is used its id is the final element and LastCellSpacer is true.
type RenderInput struct {
	Win            Window
	SidebarPaneID  string // numeric console pane id; "" for no sidebar
	ContentPaneIDs []string
	Cols           int
	LastCellSpacer bool
	Cfg            Config
}

// Render builds a tmux custom layout string of the form
// "checksum,WxH,0,0{sidebar,content}" (or just the content node when there is
// no sidebar). It returns an error when the geometry cannot hold the panes so
// the caller can fall back.
func Render(in RenderInput) (string, error) {
	n := len(in.ContentPaneIDs)
	switch {
	case n == 0:
		return "", errors.New("panelayout: no content panes")
	case in.Cols < 1:
		return "", errors.New("panelayout: cols must be >= 1")
	case in.Win.Width <= 0 || in.Win.Height <= 0:
		return "", errors.New("panelayout: window dimensions must be positive")
	}
	reserve := sidebarReserve(in.Cfg)
	contentWidth := in.Win.Width - reserve
	contentStartX := reserve
	if contentWidth <= 0 {
		return "", errors.New("panelayout: content width must be positive")
	}
	rows := ceilDiv(n, in.Cols)
	paneHeight := (in.Win.Height - (rows - 1)) / rows
	if paneHeight <= 0 {
		return "", errors.New("panelayout: pane height must be positive")
	}

	gridRows := make([]string, 0, rows)
	idx, currentY := 0, 0
	for r := range rows {
		panesInRow := min(in.Cols, n-idx)
		rowHeight := paneHeight
		if r == rows-1 {
			rowHeight = in.Win.Height - currentY // last row absorbs the remainder
		}
		lastRowSpacer := in.LastCellSpacer && r == rows-1
		widths := rowWidths(contentWidth, panesInRow, lastRowSpacer, in.Cfg)

		cells := make([]string, panesInRow)
		x := contentStartX
		for c := range panesInRow {
			cells[c] = fmt.Sprintf("%dx%d,%d,%d,%s", widths[c], rowHeight, x, currentY, in.ContentPaneIDs[idx+c])
			x += widths[c] + 1 // cell width plus one border column
		}
		idx += panesInRow

		if panesInRow == 1 {
			gridRows = append(gridRows, cells[0]) // a single pane needs no container
		} else {
			gridRows = append(gridRows, fmt.Sprintf("%dx%d,%d,%d{%s}", contentWidth, rowHeight, contentStartX, currentY, strings.Join(cells, ",")))
		}
		if r < rows-1 {
			currentY += paneHeight + 1
		}
	}

	content := gridRows[0]
	if len(gridRows) > 1 {
		content = fmt.Sprintf("%dx%d,%d,0[%s]", contentWidth, in.Win.Height, contentStartX, strings.Join(gridRows, ","))
	}

	body := content
	if in.SidebarPaneID != "" && in.Cfg.SidebarWidth > 0 {
		sidebar := fmt.Sprintf("%dx%d,0,0,%s", in.Cfg.SidebarWidth, in.Win.Height, in.SidebarPaneID)
		body = fmt.Sprintf("%dx%d,0,0{%s,%s}", in.Win.Width, in.Win.Height, sidebar, content)
	}
	return Checksum(body) + "," + body, nil
}

// rowWidths distributes contentWidth across panesInRow cells. Without a spacer
// the remainder lands on the first pane; with a spacer the content panes are
// capped at MaxComfortableWidth and the trailing spacer cell takes the rest.
func rowWidths(contentWidth, panesInRow int, spacer bool, cfg Config) []int {
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
func sidebarReserve(cfg Config) int {
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
