package tmuxbackend

// The tmux console capabilities: the session management fanout's own console
// runs inside, and the two things a process running in a tmux pane does about
// that pane. Every method is a direct delegation to the tmuxrun call that
// already owned it, so the argv tmux sees is unchanged; what moves is only who
// names the command vocabulary.

import (
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

var (
	_ backend.ConsoleHost        = (*Backend)(nil)
	_ backend.AgentStateReporter = (*Backend)(nil)
	_ backend.PlanCapture        = (*Backend)(nil)
)

// InsideSession reports whether this process runs under a tmux server.
func (*Backend) InsideSession() bool {
	return tmuxrun.InsideTmux()
}

// CurrentSession returns the session name of the pane this process runs in.
func (*Backend) CurrentSession() (string, error) {
	return tmuxrun.CurrentSession()
}

// HasSession reports whether a tmux session with this name exists.
func (*Backend) HasSession(session string) bool {
	return tmuxrun.HasSession(session)
}

// NewSession creates a detached tmux session rooted at startDir.
func (*Backend) NewSession(session, startDir string) error {
	return tmuxrun.NewSession(session, startDir)
}

// NewWindow creates a detached window in session and returns its first pane.
func (*Backend) NewWindow(session, title, startDir string) (backend.PaneInfo, error) {
	return tmuxrun.NewWindow(session, title, startDir)
}

// RunCommandInPane sends the command to a pane's shell and submits it.
func (*Backend) RunCommandInPane(paneID, command string) error {
	return tmuxrun.SendKeys(paneID, command, "Enter")
}

// FocusPaneInSession selects the pane's window, then the pane itself.
func (*Backend) FocusPaneInSession(pane backend.PaneInfo) error {
	return tmuxrun.FocusPane(pane)
}

// AttachOrSwitch attaches to a session from a plain shell, or switches the
// current client when this process already runs inside tmux.
func (*Backend) AttachOrSwitch(session string) error {
	return tmuxrun.AttachOrSwitch(session)
}

// EnableInputExtensions turns on tmux's extended-keys forwarding for the
// attached client's terminal.
func (*Backend) EnableInputExtensions() {
	tmuxrun.EnableExtendedKeys()
}

// EnableInputExtensionsForTerm turns it on for a terminal that has not attached
// yet, whose name tmux cannot resolve on its own.
func (*Backend) EnableInputExtensionsForTerm(term string) {
	tmuxrun.EnableExtendedKeysForTerm(term)
}

// ActivePaneInWindow returns the active pane in paneID's tmux window.
func (*Backend) ActivePaneInWindow(paneID string) (string, error) {
	return tmuxrun.ActivePaneInWindow(paneID)
}

// PaneTitle returns tmux's current title for a pane.
func (*Backend) PaneTitle(paneID string) (string, error) {
	return tmuxrun.PaneTitle(paneID)
}

// SetPaneRole stamps the pane's auto-layout role, clearing it when role is
// empty.
func (*Backend) SetPaneRole(paneID, role string) error {
	return tmuxrun.SetPaneRole(paneID, role)
}

// SetPaneAgentState records an agent state written by the process running in
// the pane. Missing arguments are a silent no-op in tmuxrun: the value is
// display-only telemetry and must never fail the controller reporting it.
func (*Backend) SetPaneAgentState(paneID, state string) error {
	return tmuxrun.SetPaneAgentState(paneID, state)
}

// CapturePlanSource returns the join-wrapped, alternate-screen-aware transcript
// a plan-block parser reads.
func (*Backend) CapturePlanSource(paneID string, lines int) (string, error) {
	return tmuxrun.CapturePlanSource(paneID, lines)
}
