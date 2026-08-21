package backend

// ConsoleHost is the optional capability for runtimes whose sessions fanout's
// own console has to live inside: bring a session up, put the console pane in
// it, connect the operator's terminal to it, and mark that pane so the rest of
// fanout can find it again.
//
// Absence is the contract, not an omission. A runtime that keeps its sessions
// across fanout runs has nothing here for fanout to create or attach — a
// terminal enters by exec'ing the runtime's own client (AttachExec), and a
// caller without a terminal is handed the equivalent attach command to print —
// so it offers no ConsoleHost, and the console enters through that runtime's
// managed path rather than probing a session it does not own.
type ConsoleHost interface {
	// InsideSession reports whether this process is already running in one of
	// the runtime's panes. It decides whether the console can take over the
	// terminal it was invoked from or has to bring a session up first.
	InsideSession() bool
	// CurrentSession names the session the calling pane belongs to. It is
	// meaningful only while InsideSession reports true.
	CurrentSession() (string, error)
	// HasSession reports whether a session with this name already exists.
	HasSession(session string) bool
	// NewSession creates a detached session rooted at startDir.
	NewSession(session, startDir string) error
	// NewWindow adds a detached, titled container to session and returns its
	// initial pane.
	NewWindow(session, title, startDir string) (PaneInfo, error)
	// ListPanes reports one runtime-native target's panes with the display
	// metadata the console matches its own pane title on. RestoreOps names the
	// same observation for its own reasons; a runtime implements it once.
	ListPanes(target string) ([]PaneInfo, error)
	// RunCommandInPane types one shell command into a pane and submits it. The
	// console relaunches itself this way so the new console runs under that
	// pane's own shell instead of as a child of the process that asked for it.
	RunCommandInPane(paneID, command string) error
	// FocusPaneInSession brings a pane into view inside its own session. It is
	// wider than Backend.Focus, which only selects the pane: this also selects
	// the pane's container, so a console sitting in a background window is
	// actually on screen afterwards.
	FocusPaneInSession(pane PaneInfo) error
	// AttachOrSwitch connects the operator's terminal to session — attaching
	// from a plain shell, switching one that is already attached — and blocks
	// until that terminal detaches.
	AttachOrSwitch(session string) error
	// EnableInputExtensions asks the runtime to forward modified keys
	// (Shift+Enter above all) to the console distinctly instead of collapsing
	// them, for the terminal already attached. It reports nothing because it is
	// best-effort: a runtime or terminal without the protocol leaves the console
	// on its documented fallback key.
	EnableInputExtensions()
	// EnableInputExtensionsForTerm is the same request for a terminal that has
	// not attached yet, where the runtime cannot resolve the name on its own.
	EnableInputExtensionsForTerm(term string)
	// ActivePaneInWindow reports which pane in paneID's container currently
	// holds focus, so the console can follow the operator's selection.
	ActivePaneInWindow(paneID string) (string, error)
	// PaneTitle reads back the title a pane carries, so the console can put the
	// one it replaced back when it exits.
	PaneTitle(paneID string) (string, error)
	// SetPaneRole stamps fanout's layout role on a pane; an empty role clears
	// it. The console marks itself so auto-layout reserves it as a sidebar, and
	// clears the mark on the way out so the leftover shell is not treated as
	// one.
	SetPaneRole(paneID, role string) error
}

// AsConsoleHost resolves b's console-session capability. ok=false means the
// runtime owns its own sessions, so callers enter the console through that
// runtime's managed path instead of creating and attaching one themselves.
func AsConsoleHost(b Backend) (ConsoleHost, bool) {
	host, ok := b.(ConsoleHost)
	return host, ok
}
