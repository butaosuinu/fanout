package tmuxbackend

// The tmux host capabilities: popups, global key shortcuts, and viewer-scoped
// focus. Each method is a direct delegation to the tmuxrun call that already
// owned it, so the argv tmux sees is unchanged; what moves is only who names
// the command vocabulary.

import (
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

var (
	_ backend.PopupHost      = (*Backend)(nil)
	_ backend.ShortcutBinder = (*Backend)(nil)
	_ backend.ConsoleFocus   = (*Backend)(nil)
)

// CurrentClientSize reports the attached tmux client's drawable size. A popup
// is client-scoped, so the calling pane's own size is irrelevant here.
func (*Backend) CurrentClientSize() (backend.ClientSize, error) {
	return tmuxrun.CurrentClientSize()
}

// PaneGeometryForPane reports a pane's bounds in client coordinates.
func (*Backend) PaneGeometryForPane(paneID string) (backend.PaneGeometry, error) {
	return tmuxrun.PaneGeometryForPane(paneID)
}

// ShowPopup opens a tmux display-popup and blocks until its command exits.
func (*Backend) ShowPopup(opts backend.PopupOptions) error {
	return tmuxrun.DisplayPopup(opts)
}

// NotifyViewer writes one line to a tmux client's status line. An empty viewer
// falls back to tmux's own current-client resolution.
func (*Backend) NotifyViewer(viewerID, message string) error {
	return tmuxrun.DisplayMessageToClient(viewerID, message)
}

// BindDashboardShortcuts registers the dashboard keys in the running tmux
// server's prefix and root tables.
func (*Backend) BindDashboardShortcuts(prefixKey, directKey, fanoutBin string) error {
	return tmuxrun.BindDashboardKeys(prefixKey, directKey, fanoutBin)
}

// UnbindDashboardShortcuts removes the dashboard keys, leaving a key the user
// rebound to something of their own alone.
func (*Backend) UnbindDashboardShortcuts(prefixKey, directKey string) error {
	return tmuxrun.UnbindDashboardKeys(prefixKey, directKey)
}

// BindConsoleShortcuts registers the console-return keys.
func (*Backend) BindConsoleShortcuts(prefixKey, directKey, fanoutBin string) error {
	return tmuxrun.BindConsoleKeys(prefixKey, directKey, fanoutBin)
}

// UnbindConsoleShortcuts removes the console-return keys fanout owns.
func (*Backend) UnbindConsoleShortcuts(prefixKey, directKey string) error {
	return tmuxrun.UnbindConsoleKeys(prefixKey, directKey)
}

// BindWorktreeActionShortcut registers the focused pane's worktree-action key.
func (*Backend) BindWorktreeActionShortcut(key, fanoutBin string) error {
	return tmuxrun.BindWorktreeActionKey(key, fanoutBin)
}

// UnbindWorktreeActionShortcut removes the worktree-action key fanout owns.
func (*Backend) UnbindWorktreeActionShortcut(key string) error {
	return tmuxrun.UnbindWorktreeActionKey(key)
}

// FocusPaneForViewer switches one named tmux client to a pane. An empty viewer
// degrades to tmux's current-client resolution.
func (*Backend) FocusPaneForViewer(viewerID string, ref backend.PaneRef) error {
	paneID, err := tmuxPaneID(ref)
	if err != nil {
		return err
	}
	return tmuxrun.SelectPaneForClient(viewerID, paneID)
}
