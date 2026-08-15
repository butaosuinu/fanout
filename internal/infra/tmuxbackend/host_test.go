package tmuxbackend_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
)

// The three host capabilities are what fanout's own console borrows from a
// runtime, so tmux must offer all three; a runtime that offers none makes the
// console hide those actions instead of running commands that do nothing.
func TestHostCapabilitiesArePresent(t *testing.T) {
	b := tmuxbackend.New()
	if _, ok := backend.AsPopupHost(b); !ok {
		t.Fatal("AsPopupHost(tmux backend) reported no capability")
	}
	if _, ok := backend.AsShortcutBinder(b); !ok {
		t.Fatal("AsShortcutBinder(tmux backend) reported no capability")
	}
	if _, ok := backend.AsConsoleFocus(b); !ok {
		t.Fatal("AsConsoleFocus(tmux backend) reported no capability")
	}
}

func TestPopupHostMeasuresViewerAndPane(t *testing.T) {
	logPath := installTmuxShim(t, `
case "$3" in
  "#{client_width} #{client_height}") printf '200 50\n' ;;
  *) printf '10\t4\t80\t20\t200\t50\n' ;;
esac
`)
	host, ok := backend.AsPopupHost(tmuxbackend.New())
	if !ok {
		t.Fatal("AsPopupHost(tmux backend) reported no capability")
	}

	size, err := host.CurrentClientSize()
	if err != nil {
		t.Fatalf("CurrentClientSize() failed: %v", err)
	}
	if want := (backend.ClientSize{Width: 200, Height: 50}); size != want {
		t.Fatalf("CurrentClientSize() = %#v, want %#v", size, want)
	}

	geom, err := host.PaneGeometryForPane("%12")
	if err != nil {
		t.Fatalf("PaneGeometryForPane() failed: %v", err)
	}
	want := backend.PaneGeometry{Left: 10, Top: 4, Width: 80, Height: 20, ClientWidth: 200, ClientHeight: 50}
	if geom != want {
		t.Fatalf("PaneGeometryForPane() = %#v, want %#v", geom, want)
	}

	wantCalls := [][]string{
		{"display-message", "-p", "#{client_width} #{client_height}"},
		{
			"display-message", "-p", "-t", "%12", "-F",
			"#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}\t#{client_width}\t#{client_height}",
		},
	}
	if got := readCalls(t, logPath); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("tmux calls = %#v, want %#v", got, wantCalls)
	}
}

// An absolute position suppresses tmux's centering flags, which is how a popup
// lands beside the console pane rather than over it.
func TestPopupHostDrawsPopupAtRequestedPosition(t *testing.T) {
	logPath := installTmuxShim(t, "")
	host, _ := backend.AsPopupHost(tmuxbackend.New())

	err := host.ShowPopup(backend.PopupOptions{
		Width: 90, Height: 30,
		StartDir: "/repo/project root",
		Title:    "New agent pane",
		Command:  "fanout __tui-new-pane-popup",
		Position: &backend.PopupPosition{X: 91, Y: 4},
	})
	if err != nil {
		t.Fatalf("ShowPopup() failed: %v", err)
	}

	want := [][]string{{
		"display-popup", "-E",
		"-b", "double",
		"-S", "fg=#00A3AF",
		"-w", "90",
		"-h", "30",
		"-d", "/repo/project root",
		"-x", "91", "-y", "4",
		"-T", "New agent pane",
		"fanout __tui-new-pane-popup",
	}}
	if got := readCalls(t, logPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("tmux calls = %#v, want %#v", got, want)
	}
}

func TestPopupHostCentersPopupWithoutPosition(t *testing.T) {
	logPath := installTmuxShim(t, "")
	host, _ := backend.AsPopupHost(tmuxbackend.New())

	if err := host.ShowPopup(backend.PopupOptions{Width: 76, Height: 21, Command: "fanout __tui-help-popup"}); err != nil {
		t.Fatalf("ShowPopup() failed: %v", err)
	}

	want := [][]string{{
		"display-popup", "-E",
		"-b", "double",
		"-S", "fg=#00A3AF",
		"-w", "76",
		"-h", "21",
		"-x", "C", "-y", "C",
		"fanout __tui-help-popup",
	}}
	if got := readCalls(t, logPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("tmux calls = %#v, want %#v", got, want)
	}
}

// The viewer id pins the message to the terminal that pressed the key; an empty
// one lets tmux fall back to its own current-client resolution.
func TestPopupHostNotifiesNamedViewer(t *testing.T) {
	tests := []struct {
		name     string
		viewerID string
		want     []string
	}{
		{
			name:     "named viewer is targeted with -c",
			viewerID: "/dev/ttys003",
			want:     []string{"display-message", "-c", "/dev/ttys003", "fanout dashboard: http://127.0.0.1:8765"},
		},
		{
			name: "empty viewer leaves the target to tmux",
			want: []string{"display-message", "fanout dashboard: http://127.0.0.1:8765"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := installTmuxShim(t, "")
			host, _ := backend.AsPopupHost(tmuxbackend.New())
			if err := host.NotifyViewer(tt.viewerID, "fanout dashboard: http://127.0.0.1:8765"); err != nil {
				t.Fatalf("NotifyViewer() failed: %v", err)
			}
			if got := readCalls(t, logPath); !reflect.DeepEqual(got, [][]string{tt.want}) {
				t.Fatalf("tmux calls = %#v, want %#v", got, [][]string{tt.want})
			}
		})
	}
}

// Each shortcut pair registers a prefix-table and a root-table binding, and the
// launch command each bakes in is what a later unbind matches on to prove
// fanout owns the key.
func TestShortcutBinderRegistersEachKeySet(t *testing.T) {
	tests := []struct {
		name    string
		bind    func(backend.ShortcutBinder) error
		wantLen int
		// wantMarker must appear in the bound command of every registration, and
		// is the same substring the matching unbind screens on.
		wantMarker string
	}{
		{
			name: "dashboard pair opens a detached dashboard window",
			bind: func(b backend.ShortcutBinder) error {
				return b.BindDashboardShortcuts("D", "F12", "/usr/local/bin/fanout")
			},
			wantLen:    2,
			wantMarker: "dashboard --web --open",
		},
		{
			name: "console pair returns focus without creating a window",
			bind: func(b backend.ShortcutBinder) error {
				return b.BindConsoleShortcuts("T", "F11", "/usr/local/bin/fanout")
			},
			wantLen:    2,
			wantMarker: "focus-console --from",
		},
		{
			name: "worktree action is prefix-only",
			bind: func(b backend.ShortcutBinder) error {
				return b.BindWorktreeActionShortcut("M", "/usr/local/bin/fanout")
			},
			wantLen:    1,
			wantMarker: "__worktree-action --pane",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := installTmuxShim(t, "")
			binder, ok := backend.AsShortcutBinder(tmuxbackend.New())
			if !ok {
				t.Fatal("AsShortcutBinder(tmux backend) reported no capability")
			}
			if err := tt.bind(binder); err != nil {
				t.Fatalf("bind failed: %v", err)
			}
			calls := readCalls(t, logPath)
			if len(calls) != tt.wantLen {
				t.Fatalf("tmux calls = %#v, want %d bind-key calls", calls, tt.wantLen)
			}
			for _, call := range calls {
				if call[0] != "bind-key" {
					t.Fatalf("tmux call = %#v, want a bind-key call", call)
				}
				if !strings.Contains(strings.Join(call, " "), tt.wantMarker) {
					t.Fatalf("bind-key call %#v does not carry marker %q", call, tt.wantMarker)
				}
			}
		})
	}
}

// Unbinding screens the current binding first: a key the user rebound to
// something of their own is left alone rather than silently removed.
func TestShortcutBinderUnbindsOnlyFanoutOwnedKeys(t *testing.T) {
	tests := []struct {
		name    string
		listOut string
		unbind  func(backend.ShortcutBinder) error
		want    [][]string
	}{
		{
			name:    "fanout-owned dashboard keys are removed from both tables",
			listOut: "bind-key -T prefix D run-shell -b 'tmux new-window -n fanout-dashboard ... dashboard --web --open'",
			unbind:  func(b backend.ShortcutBinder) error { return b.UnbindDashboardShortcuts("D", "F12") },
			want: [][]string{
				{"list-keys", "-T", "prefix", "D"},
				{"unbind-key", "-q", "D"},
				{"list-keys", "-T", "root", "F12"},
				{"unbind-key", "-q", "-n", "F12"},
			},
		},
		{
			name:    "a key rebound by the user is inspected and left in place",
			listOut: "bind-key -T prefix D split-window",
			unbind:  func(b backend.ShortcutBinder) error { return b.UnbindDashboardShortcuts("D", "F12") },
			want: [][]string{
				{"list-keys", "-T", "prefix", "D"},
				{"list-keys", "-T", "root", "F12"},
			},
		},
		{
			name:    "worktree action screens the prefix table only",
			listOut: "bind-key -T prefix M display-popup -E '/usr/local/bin/fanout __worktree-action --pane %1'",
			unbind:  func(b backend.ShortcutBinder) error { return b.UnbindWorktreeActionShortcut("M") },
			want: [][]string{
				{"list-keys", "-T", "prefix", "M"},
				{"unbind-key", "-q", "M"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := installTmuxShim(t, `
if [ "$1" = "list-keys" ]; then
  printf '%s\n' "$TMUXBACKEND_LIST_KEYS"
fi
`)
			t.Setenv("TMUXBACKEND_LIST_KEYS", tt.listOut)
			binder, _ := backend.AsShortcutBinder(tmuxbackend.New())
			if err := tt.unbind(binder); err != nil {
				t.Fatalf("unbind failed: %v", err)
			}
			if got := readCalls(t, logPath); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("tmux calls = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// A shortcut-driven command runs with no viewer context, so the console-return
// key must name the terminal that pressed it; only an unnamed viewer may fall
// back to tmux's most-recently-active resolution.
func TestConsoleFocusSwitchesNamedViewer(t *testing.T) {
	tests := []struct {
		name     string
		viewerID string
		want     [][]string
	}{
		{
			name:     "named viewer is switched with switch-client",
			viewerID: "client-1",
			want:     [][]string{{"switch-client", "-c", "client-1", "-t", "%9"}},
		},
		{
			name: "unnamed viewer degrades to tmux's own current-client switch",
			want: [][]string{{"switch-client", "-t", "%9"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := installTmuxShim(t, "")
			focus, ok := backend.AsConsoleFocus(tmuxbackend.New())
			if !ok {
				t.Fatal("AsConsoleFocus(tmux backend) reported no capability")
			}
			ref := backend.PaneRef{Backend: backend.Tmux, Pane: "%9"}
			if err := focus.FocusPaneForViewer(tt.viewerID, ref); err != nil {
				t.Fatalf("FocusPaneForViewer() failed: %v", err)
			}
			if got := readCalls(t, logPath); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("tmux calls = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// A pane recorded on another runtime must never be addressed as a tmux pane id.
func TestConsoleFocusRejectsForeignPaneReference(t *testing.T) {
	focus, _ := backend.AsConsoleFocus(tmuxbackend.New())
	err := focus.FocusPaneForViewer("client-1", backend.PaneRef{Backend: backend.Herdr, Pane: "w1:p1"})
	if err == nil {
		t.Fatal("FocusPaneForViewer(herdr ref) succeeded, want an error")
	}
}
