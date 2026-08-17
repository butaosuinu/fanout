package tmuxbackend_test

import (
	"reflect"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
)

// tmux hosts fanout's console, so it must offer the session-entry capability
// and the two a controller uses about the pane it runs in. Their presence is
// what routes the console down the in-place lane instead of the managed one.
func TestConsoleCapabilitiesArePresent(t *testing.T) {
	b := tmuxbackend.New()
	if _, ok := backend.AsConsoleHost(b); !ok {
		t.Fatal("AsConsoleHost(tmux backend) reported no capability")
	}
	if _, ok := backend.AsAgentStateReporter(b); !ok {
		t.Fatal("AsAgentStateReporter(tmux backend) reported no capability")
	}
	if _, ok := backend.AsPlanCapture(b); !ok {
		t.Fatal("AsPlanCapture(tmux backend) reported no capability")
	}
}

// The console bootstrap runs these in order from a plain shell, so the argv is
// pinned: a changed flag here silently strands the console in a session the
// operator never sees.
func TestConsoleHostBringsUpAndEntersASession(t *testing.T) {
	logPath := installTmuxShim(t, `
case "$1" in
  new-window) printf '%%9:@3:0:1:fanout tui\n' ;;
esac
`)
	console, ok := backend.AsConsoleHost(tmuxbackend.New())
	if !ok {
		t.Fatal("AsConsoleHost(tmux backend) reported no capability")
	}

	if err := console.NewSession("fanout-repo-abcd1234", "/repo/project root"); err != nil {
		t.Fatalf("NewSession() failed: %v", err)
	}
	pane, err := console.NewWindow("fanout-repo-abcd1234", "fanout tui", "/repo/project root")
	if err != nil {
		t.Fatalf("NewWindow() failed: %v", err)
	}
	if want := (backend.PaneInfo{ID: "%9", WindowID: "@3", Index: 0, Active: true, Title: "fanout tui"}); pane != want {
		t.Fatalf("NewWindow() = %#v, want %#v", pane, want)
	}
	if err := console.RunCommandInPane(pane.ID, "cd /repo && fanout"); err != nil {
		t.Fatalf("RunCommandInPane() failed: %v", err)
	}
	if err := console.FocusPaneInSession(pane); err != nil {
		t.Fatalf("FocusPaneInSession() failed: %v", err)
	}

	wantCalls := [][]string{
		{"new-session", "-d", "-s", "fanout-repo-abcd1234", "-c", "/repo/project root"},
		{
			"new-window", "-d", "-P", "-F",
			"#{pane_id}:#{window_id}:#{pane_index}:#{pane_active}:#{pane_title}",
			"-t", "=fanout-repo-abcd1234", "-n", "fanout tui", "-c", "/repo/project root",
		},
		{"send-keys", "-t", "%9", "cd /repo && fanout", "Enter"},
		{"select-window", "-t", "@3"},
		{"select-pane", "-t", "%9"},
	}
	if got := readCalls(t, logPath); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("tmux calls = %#v, want %#v", got, wantCalls)
	}
}

// The console marks its own pane so auto-layout reserves it as a sidebar, and
// clears the mark on the way out. Clearing goes through -pu (unset), not an
// empty value: an empty option would still read as a stamped role.
func TestConsoleHostStampsAndClearsThePaneRole(t *testing.T) {
	logPath := installTmuxShim(t, "")
	console, _ := backend.AsConsoleHost(tmuxbackend.New())

	if err := console.SetPaneRole("%9", backend.RoleConsole); err != nil {
		t.Fatalf("SetPaneRole() failed: %v", err)
	}
	if err := console.SetPaneRole("%9", ""); err != nil {
		t.Fatalf("SetPaneRole(clear) failed: %v", err)
	}

	wantCalls := [][]string{
		{"set-option", "-p", "-t", "%9", "@fanout_role", "console"},
		{"set-option", "-pu", "-t", "%9", "@fanout_role"},
	}
	if got := readCalls(t, logPath); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("tmux calls = %#v, want %#v", got, wantCalls)
	}
}

// A controller running inside a pane publishes its own display state there.
// The empty-argument no-op is the contract that keeps a replaced or missing
// pane from failing the controller reporting on it.
func TestAgentStateReporterWritesThePaneOption(t *testing.T) {
	logPath := installTmuxShim(t, "")
	reporter, _ := backend.AsAgentStateReporter(tmuxbackend.New())

	if err := reporter.SetPaneAgentState("%9", "plan"); err != nil {
		t.Fatalf("SetPaneAgentState() failed: %v", err)
	}
	if err := reporter.SetPaneAgentState("", "plan"); err != nil {
		t.Fatalf("SetPaneAgentState(no pane) = %v, want a silent no-op", err)
	}

	wantCalls := [][]string{{"set-option", "-p", "-t", "%9", "@fanout_agent_state", "plan"}}
	if got := readCalls(t, logPath); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("tmux calls = %#v, want %#v", got, wantCalls)
	}
}
