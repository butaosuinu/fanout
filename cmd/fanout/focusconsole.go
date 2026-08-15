package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
)

// defaultConsoleKey / defaultConsoleDirectKey are the tmux keys bindConsoleKey
// registers for returning to the fanout TUI console, the counterpart of the
// dashboard's F12 / prefix + D. Capital T leaves tmux's default lowercase
// `prefix + t` (clock-mode) untouched.
const (
	defaultConsoleKey       = "T"
	defaultConsoleDirectKey = "F11"
)

// focusConsoleRuntime is the host runtime the console-return keys live in. Both
// the shortcut registration and the command it spawns go through it, so a test
// can stand in for the whole pair. It stays a func rather than a value so no
// runtime is constructed just by loading this package.
var focusConsoleRuntime = func() backend.Backend { return paneruntime.NewTmux() }

func isFocusConsoleRequest(args []string) bool {
	return len(args) > 0 && args[0] == "focus-console"
}

const focusConsoleUsage = `Usage: fanout focus-console [--from <pane-id>] [--client <client-name>]

Switch to the live fanout TUI console pane. This is what the tmux keys
F11 / prefix + T run; it can also be invoked manually from any pane.

Options:
  --from <pane-id>        Pane whose recorded repo picks among multiple
                          consoles. Defaults to $TMUX_PANE.
  --client <client-name>  tmux client to switch. Defaults to the current
                          client.
`

type focusConsoleFlags struct {
	fromID string
	client string
	help   bool
}

// cmdFocusConsole implements `fanout focus-console`: find the live fanout TUI
// console pane and switch the pressing client to it. It is the target of the
// BindConsoleKeys tmux bindings, which pass the pressing pane as --from (so
// the console recorded for that pane's repo wins in multi-repo setups) and the
// pressing client as --client (so a multi-client server switches the terminal
// the key was pressed on, not the most recently active one). When no console
// is live it notifies via the status line and exits zero — a non-zero exit
// would make run-shell pop an error view over the pressing pane.
func cmdFocusConsole(args []string, lg *log.Logger) exitcode.Code {
	flags, code := parseFocusConsoleFlags(args, lg)
	if code != exitcode.OK {
		return code
	}
	if flags.help {
		fmt.Fprint(os.Stdout, focusConsoleUsage)
		return exitcode.OK
	}
	// The console-return keys exist only because a runtime registered them, so
	// the process they spawn resolves that same host runtime rather than the
	// ambient selection: the key would not be on screen otherwise.
	return switchViewerToConsole(focusConsoleRuntime(), flags, lg)
}

// switchViewerToConsole picks the console the pressing pane's repo owns and
// moves the pressing viewer to it.
func switchViewerToConsole(runtimeBackend backend.Backend, flags focusConsoleFlags, lg *log.Logger) exitcode.Code {
	panes, err := runtimeBackend.ListLive()
	if err != nil {
		lg.Err("focus-console: %v", err)
		return exitcode.Env
	}
	console, ok := pickConsolePane(livePaneByID(panes, flags.fromID), panes)
	if !ok {
		notifyFocusConsoleViewer(runtimeBackend, flags.client, lg)
		return exitcode.OK
	}
	focus, ok := backend.AsConsoleFocus(runtimeBackend)
	if !ok {
		lg.Err("focus-console: %s", backend.ObservationOnlyReason)
		return exitcode.Env
	}
	if err := focus.FocusPaneForViewer(flags.client, console.Ref); err != nil {
		lg.Err("focus-console: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

// livePaneByID returns the observation of one pane, or the zero value when it
// is not live. The pressing pane only breaks ties between consoles, so a
// missing one is a weaker choice, not an error.
func livePaneByID(panes []backend.LivePane, paneID string) backend.LivePane {
	for _, pane := range panes {
		if pane.Ref.Pane == paneID {
			return pane
		}
	}
	return backend.LivePane{}
}

// notifyFocusConsoleViewer reports "no console" on the status line of the
// terminal that pressed the key. It is best-effort on purpose: a non-zero exit
// here would pop the runtime's error view over the pane the user was working
// in, which is worse than a missing notice.
func notifyFocusConsoleViewer(runtimeBackend backend.Backend, viewerID string, lg *log.Logger) {
	host, ok := backend.AsPopupHost(runtimeBackend)
	if !ok {
		return
	}
	if err := host.NotifyViewer(viewerID, "fanout: no live console; run 'fanout' to start one"); err != nil {
		lg.Debug("focus-console: %v", err)
	}
}

func parseFocusConsoleFlags(args []string, lg *log.Logger) (focusConsoleFlags, exitcode.Code) {
	var flags focusConsoleFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h", "help":
			flags.help = true
			return flags, exitcode.OK
		case "--from":
			if i+1 >= len(args) {
				lg.Err("focus-console: --from requires an argument")
				return flags, exitcode.Invocation
			}
			i++
			flags.fromID = strings.TrimSpace(args[i])
		case "--client":
			if i+1 >= len(args) {
				lg.Err("focus-console: --client requires an argument")
				return flags, exitcode.Invocation
			}
			i++
			flags.client = strings.TrimSpace(args[i])
		default:
			lg.Err("focus-console: unknown option %s", args[i])
			return flags, exitcode.Invocation
		}
	}
	// The keybinding always passes --from (run-shell's environment has no
	// TMUX_PANE); the fallback covers manual invocations from a pane shell.
	if flags.fromID == "" {
		flags.fromID = strings.TrimSpace(os.Getenv("TMUX_PANE"))
	}
	return flags, exitcode.OK
}

// pickConsolePane selects the console pane the console-return keys land on.
// Candidates must carry BOTH @fanout_role=console and the fixed TUI pane
// title (the findTUIPane match). The double check is not forgery protection —
// a pane's own process can stamp both, and focus-console is a UX primitive
// that only moves display focus, not a security boundary — it screens out
// misconfiguration and half-cleaned panes. A crashed TUI still leaves a stale
// console-looking pane that the keys will land on; restarting fanout there
// recovers (same known limit as findTUIPane's title reuse check).
//
// Priority: ① candidates whose @fanout_project_root matches the pressing
// pane's recorded root, with same-session candidates winning ties — root
// outranks session so one global key stays multi-repo safe when consoles for
// several repos share a tmux session; ② same session as the pressing pane
// (session ids are tmux-generated, so this comparison does not trust pane
// contents); ③ the first candidate. A pressing pane with no recorded root
// (e.g. a pane fanout never touched) starts at ②.
func pickConsolePane(from backend.LivePane, panes []backend.LivePane) (backend.LivePane, bool) {
	var candidates []backend.LivePane
	for _, pane := range panes {
		if pane.Role == backend.RoleConsole && pane.Title == tuiPaneTitle {
			candidates = append(candidates, pane)
		}
	}
	if len(candidates) == 0 {
		return backend.LivePane{}, false
	}
	if fromRoot := strings.TrimSpace(from.ProjectRoot); fromRoot != "" {
		var rootMatches []backend.LivePane
		for _, pane := range candidates {
			if samePath(pane.ProjectRoot, fromRoot) {
				rootMatches = append(rootMatches, pane)
			}
		}
		if len(rootMatches) > 0 {
			if same, ok := firstPaneInSession(rootMatches, from.SessionID); ok {
				return same, true
			}
			return rootMatches[0], true
		}
	}
	if same, ok := firstPaneInSession(candidates, from.SessionID); ok {
		return same, true
	}
	return candidates[0], true
}

func firstPaneInSession(panes []backend.LivePane, sessionID string) (backend.LivePane, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return backend.LivePane{}, false
	}
	for _, pane := range panes {
		if pane.SessionID == sessionID {
			return pane, true
		}
	}
	return backend.LivePane{}, false
}

// bindConsoleKey registers the console-return tmux keys. Unlike
// bindDashboardKey it runs only at live TUI start: the binding's target is the
// console itself, so a run that does not put a console on screen has nothing
// for the keys to return to.
func bindConsoleKey(lg *log.Logger, enabled bool) {
	if !enabled {
		return
	}
	syncConsoleKey(lg, enabled, false)
}

func syncConsoleKey(lg *log.Logger, enabled bool, cleanupDisabled bool) {
	binder, ok := backend.AsShortcutBinder(focusConsoleRuntime())
	if !ok {
		return
	}
	if !enabled {
		if cleanupDisabled {
			if err := binder.UnbindConsoleShortcuts(defaultConsoleKey, defaultConsoleDirectKey); err != nil {
				lg.Debug("console keybind cleanup: %v (not in tmux?)", err)
			}
		}
		return
	}
	bindConsoleShortcuts(binder, lg)
}

func bindConsoleShortcuts(binder backend.ShortcutBinder, lg *log.Logger) {
	bin, err := os.Executable()
	if err != nil {
		lg.Debug("console keybind: cannot resolve fanout binary path: %v", err)
		return
	}
	if err := binder.BindConsoleShortcuts(defaultConsoleKey, defaultConsoleDirectKey, bin); err != nil {
		lg.Debug("console keybind: %v (not in tmux?)", err)
		return
	}
	lg.Info("tmux keybind: press %s or prefix + %s to return to the fanout console", defaultConsoleDirectKey, defaultConsoleKey)
}
