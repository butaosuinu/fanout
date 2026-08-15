package backend

// Pane- and window-shaped data orchestration exchanges with a terminal
// multiplexer adapter: the pane listing, the auto-layout view of a window, and
// the outcome of an identity-checked close. The adapter that fills these values
// lives in infra; this file is data only.

// RoleConsole is the pane role value for the resident TUI console pane.
const RoleConsole = "console"

// PaneInfo describes a pane currently known to tmux.
type PaneInfo struct {
	ID       string
	WindowID string
	Index    int
	Active   bool
	Title    string
}

// Geometry is a tmux window's id and interior size (status bar excluded).
type Geometry struct {
	WindowID string
	Width    int
	Height   int
}

// WindowPane is one pane in a window as seen by the auto-layout orchestrator.
// NumericID is the pane id without its leading '%', the form tmux custom layout
// strings embed in each leaf cell.
type WindowPane struct {
	ID        string
	NumericID string
	Index     int
	Active    bool
	Role      string
	Spacer    bool
}

// ClosePaneStatus classifies an identity-checked pane close.
type ClosePaneStatus int

const (
	// ClosePaneClosed means the recorded pane identity was confirmed and the
	// pane disappeared after kill-pane.
	ClosePaneClosed ClosePaneStatus = iota
	// ClosePaneStale means the pane was already gone or its server-scoped id
	// had been reused by a pane that did not match the recorded identity.
	ClosePaneStale
	// ClosePaneFailed means tmux could not be inspected or the confirmed pane
	// remained live after kill-pane. Callers must preserve durable state so the
	// close can be retried.
	ClosePaneFailed
)

// ClosePaneResult reports whether ClosePaneIfOwned killed a confirmed pane or
// safely treated its state row as stale. WindowID is set only for a confirmed
// pane and lets lifecycle callers repair that window after the close.
type ClosePaneResult struct {
	Status   ClosePaneStatus
	WindowID string
}
