package backend

// The optional host capabilities: the terminal surfaces a runtime lends to
// fanout's own console rather than to the panes it launches — a modal popup
// drawn over the viewer's terminal, the global shortcuts that reach fanout from
// any pane, and moving one viewer's focus. They stay outside Backend because a
// runtime that arranges its own windows launches perfectly usable panes without
// any of them; a caller that finds a capability absent hides the feature, it
// does not fail the run.
//
// "Viewer" is the attached terminal a runtime is currently drawing to (a tmux
// client). It is named separately from a pane because both the shortcuts and
// the status-line notice have to address the terminal a key was pressed on,
// which on a multi-terminal server is not the most recently active one.

// NotifyViewerEnv names the environment variable a ShortcutBinder-installed
// shortcut sets on the fanout process it spawns, telling it which viewer
// pressed the key. It is a wire contract between the shortcut a runtime
// registered — possibly during an earlier fanout run — and the process that
// reads it, so the name is frozen.
const NotifyViewerEnv = "FANOUT_DASHBOARD_NOTIFY_CLIENT"

// PopupHost is an optional capability for runtimes that draw a modal popup over
// the viewer's terminal and report the geometry needed to size and place one.
// The caller owns the popup's layout arithmetic and its content; this capability
// only measures and draws.
type PopupHost interface {
	// CurrentClientSize reports the drawable size of the viewer a popup would be
	// drawn on. It is the viewer's size, not the calling pane's: a popup is
	// scoped to the whole terminal.
	CurrentClientSize() (ClientSize, error)
	// PaneGeometryForPane reports paneID's position and size in viewer
	// coordinates, plus that viewer's size, so a caller can place a popup beside
	// the pane and clamp it into view.
	PaneGeometryForPane(paneID string) (PaneGeometry, error)
	// ShowPopup draws the popup and blocks until its command exits.
	ShowPopup(PopupOptions) error
	// NotifyViewer writes one transient line to viewerID's status area. An empty
	// viewerID lets the runtime pick its own current viewer.
	NotifyViewer(viewerID, message string) error
}

// ShortcutBinder is an optional capability for runtimes that register global
// key shortcuts against a running server, reaching fanout from any pane without
// editing the user's configuration file.
//
// The three shortcut sets stay separate named pairs rather than one keymap
// registry: each carries its own launch command, its own ownership markers for
// a safe unbind, and its own lifetime — the console pair is registered only
// while a console is on screen, while the dashboard pair outlives it.
type ShortcutBinder interface {
	BindDashboardShortcuts(prefixKey, directKey, fanoutBin string) error
	UnbindDashboardShortcuts(prefixKey, directKey string) error
	BindConsoleShortcuts(prefixKey, directKey, fanoutBin string) error
	UnbindConsoleShortcuts(prefixKey, directKey string) error
	BindWorktreeActionShortcut(key, fanoutBin string) error
	UnbindWorktreeActionShortcut(key string) error
}

// DashboardShortcutOptions describes the one global shortcut a runtime may
// install without offering fanout's broader host shortcut surface. Environment
// is the caller environment before the runtime narrows it for its own server;
// the implementation must retain only the values it explicitly trusts.
type DashboardShortcutOptions struct {
	Enabled     bool
	FanoutBin   string
	Environment []string
}

// DashboardShortcutBinder is the narrow dashboard-only shortcut capability.
// It lets a runtime offer F12 without claiming the console, worktree-action,
// popup, or viewer-scoped behavior bundled into ShortcutBinder.
type DashboardShortcutBinder interface {
	SyncDashboardShortcut(DashboardShortcutOptions) error
}

// ConsoleFocus is an optional capability for runtimes that can move a *named*
// viewer's focus. It is deliberately narrower than Backend.Focus, which acts on
// whichever viewer the runtime considers current: a shortcut-driven command
// runs with no viewer context, so the console-return keys must name the viewer
// that pressed them or a second attached terminal gets yanked instead.
//
// Listing the panes to choose from needs no capability of its own —
// Backend.ListLive already reports every field the choice reads.
type ConsoleFocus interface {
	FocusPaneForViewer(viewerID string, ref PaneRef) error
}

// PaneLocator is an optional capability for runtimes that report where one of
// their panes currently sits on disk. fanout's console and dashboard read it
// for the pane they were invoked from: a wrapper can leave their own process
// cwd pointing somewhere other than the directory the operator is looking at,
// and the pane's own path is the only evidence of which repository that is.
type PaneLocator interface {
	PaneCurrentPath(paneID string) (string, error)
}

// AsPopupHost resolves b's popup capability. ok=false means the runtime draws
// no popups, so callers hide the popup-driven action instead of failing.
func AsPopupHost(b Backend) (PopupHost, bool) {
	host, ok := b.(PopupHost)
	return host, ok
}

// AsShortcutBinder resolves b's broad host-shortcut capability. ok=false still
// permits the narrower dashboard-only capability below.
func AsShortcutBinder(b Backend) (ShortcutBinder, bool) {
	binder, ok := b.(ShortcutBinder)
	return binder, ok
}

// AsDashboardShortcutBinder resolves b's dashboard-only shortcut capability.
// ok=false means the runtime has no narrow dashboard binding of its own.
func AsDashboardShortcutBinder(b Backend) (DashboardShortcutBinder, bool) {
	binder, ok := b.(DashboardShortcutBinder)
	return binder, ok
}

// AsConsoleFocus resolves b's viewer-scoped focus capability. ok=false means the
// runtime cannot address one viewer, so callers fall back to Backend.Focus or
// skip the switch.
func AsConsoleFocus(b Backend) (ConsoleFocus, bool) {
	focus, ok := b.(ConsoleFocus)
	return focus, ok
}

// AsPaneLocator resolves b's pane-location capability. ok=false means the
// runtime does not report where a pane sits, so callers keep the project root
// they resolved from their own process.
func AsPaneLocator(b Backend) (PaneLocator, bool) {
	locator, ok := b.(PaneLocator)
	return locator, ok
}
