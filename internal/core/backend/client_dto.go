package backend

// Terminal-client geometry and the popup invocation orchestration builds from
// it. The code that measures a client and displays the popup lives in infra;
// this file is data only.

// ClientSize is the current tmux client's drawable terminal size.
type ClientSize struct {
	Width  int
	Height int
}

// PopupPosition describes an absolute tmux display-popup origin.
type PopupPosition struct {
	X int
	Y int
}

// PopupOptions describes a tmux display-popup invocation.
type PopupOptions struct {
	Width    int
	Height   int
	StartDir string
	Title    string
	Command  string
	Position *PopupPosition
}

// PaneGeometry is the absolute position and size of a tmux pane plus the
// current client size used to clamp adjacent popups into view.
type PaneGeometry struct {
	Left         int
	Top          int
	Width        int
	Height       int
	ClientWidth  int
	ClientHeight int
}
