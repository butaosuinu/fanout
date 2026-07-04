package lifecycle

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/panelayout"
	"github.com/butaosuinu/fanout/internal/state"
)

type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Ok(string, ...any)   {}
func (nopLogger) Warn(string, ...any) {}
func (nopLogger) Err(string, ...any)  {}
func (nopLogger) Stderr() io.Writer   { return io.Discard }

// installFakeTmux puts a tmux shim on PATH. display-message prints windowID (or
// exits 1 when fail is set); every other subcommand is a silent no-op.
func installFakeTmux(t *testing.T, windowID string, fail bool) {
	t.Helper()
	dir := t.TempDir()
	dm := "printf '%s\\n' '" + windowID + "'"
	if fail {
		dm = "exit 1"
	}
	script := "#!/bin/sh\ncase \"$1\" in\n  display-message) " + dm + " ;;\n  *) : ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type relayoutCall struct {
	id   string
	trig panelayout.Trigger
}

func stubRelayout(t *testing.T) *[]relayoutCall {
	t.Helper()
	var calls []relayoutCall
	orig := relayoutWindow
	relayoutWindow = func(id string, trig panelayout.Trigger) error {
		calls = append(calls, relayoutCall{id, trig})
		return nil
	}
	t.Cleanup(func() { relayoutWindow = orig })
	return &calls
}

// closeAndRelayout mirrors the public entrypoints: capture windows during the
// close, then relayout the accumulated set once.
func closeAndRelayout(panes []state.Pane) {
	windows := map[string]struct{}{}
	closePaneRecords(Options{Hooks: hooks.EmptyConfig()}, panes, ClosePaneOnly, nopLogger{}, windows)
	relayoutClosedWindows(windows, nopLogger{})
}

func TestClosePaneRecordsRelayoutsAffectedWindowOnce(t *testing.T) {
	installFakeTmux(t, "@1", false)
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: 1}, {PaneID: "%2", IssueNum: 2}})
	// Both panes share window @1, so it is re-laid-out exactly once.
	if len(*calls) != 1 {
		t.Fatalf("relayout calls = %+v, want one", *calls)
	}
	if (*calls)[0].id != "@1" || (*calls)[0].trig != panelayout.Close {
		t.Fatalf("relayout = %+v, want Close on @1", (*calls)[0])
	}
}

func TestClosePaneRecordsSkipsRelayoutWhenWindowUnknown(t *testing.T) {
	installFakeTmux(t, "", true) // display-message fails: window can't be resolved
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: 1}})
	if len(*calls) != 0 {
		t.Fatalf("relayout calls = %+v, want none", *calls)
	}
}

func TestClosePaneRecordsCapturesWindowBeforeKill(t *testing.T) {
	// A pane with no recorded id can't be resolved to a window, so no relayout.
	installFakeTmux(t, "@1", false)
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "", IssueNum: 1}})
	if len(*calls) != 0 {
		t.Fatalf("relayout calls = %+v, want none for id-less pane", *calls)
	}
}

func TestCleanupAccumulatesWindowsAcrossPanes(t *testing.T) {
	// Two panes cleaned in separate cleanupPaneRecords calls but sharing one
	// window must relayout that window exactly once (the Cleanup-loop pattern).
	installFakeTmux(t, "@7", false)
	calls := stubRelayout(t)

	windows := map[string]struct{}{}
	cleanupPaneRecords(Options{Hooks: hooks.EmptyConfig()}, []state.Pane{{PaneID: "%1", IssueNum: 1}}, nopLogger{}, windows)
	cleanupPaneRecords(Options{Hooks: hooks.EmptyConfig()}, []state.Pane{{PaneID: "%2", IssueNum: 2}}, nopLogger{}, windows)
	relayoutClosedWindows(windows, nopLogger{})

	if len(*calls) != 1 || (*calls)[0].id != "@7" {
		t.Fatalf("relayout calls = %+v, want one on @7", *calls)
	}
}

// A keyed attached agent (the plan fan-out coordinator) must take the
// identity-checked kill: when no live pane carries its liveness key, the close
// skips both the kill and the relayout instead of killing by pane id.
func TestClosePaneRecordsKeyVerifiesKeyedAttachedAgent(t *testing.T) {
	installFakeTmux(t, "@1", false)
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: -1, Kind: state.PaneKindAttachedAgent, ShellKey: "shell-coordinator"}})
	if len(*calls) != 0 {
		t.Fatalf("relayout calls = %+v, want none when the liveness key cannot be confirmed", *calls)
	}
}

// An attached agent without a liveness key keeps the direct pane-id kill.
func TestClosePaneRecordsKillsUnkeyedAttachedAgentByPaneID(t *testing.T) {
	installFakeTmux(t, "@1", false)
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: -1, Kind: state.PaneKindAttachedAgent}})
	if len(*calls) != 1 {
		t.Fatalf("relayout calls = %+v, want one for an unkeyed attached agent", *calls)
	}
}
