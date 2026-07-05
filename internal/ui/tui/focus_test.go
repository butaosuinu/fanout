package tui

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/state"
)

// TestPaneAliveForActionMatchesLivenessKey pins the focus/close gate: rows
// recorded with a ShellKey (shell terminals, the plan fan-out coordinator at
// the repo root) must match the live pane's @fanout_shell_key, while plain
// agent rows keep the pane-id check.
func TestPaneAliveForActionMatchesLivenessKey(t *testing.T) {
	byID := func(alive bool) func(string) bool {
		return func(string) bool { return alive }
	}
	byKey := func(alive bool) func(string, string) bool {
		return func(string, string) bool { return alive }
	}

	tests := []struct {
		name string
		pane paneView
		id   bool
		key  bool
		want bool
	}{
		{name: "agent row uses the pane-id check", pane: paneView{PaneID: "%1"}, id: true, key: false, want: true},
		{name: "shell row uses the key check", pane: paneView{PaneID: "%1", Kind: state.PaneKindShell, ShellKey: "shell-a"}, id: true, key: false, want: false},
		{name: "coordinator row with a key ignores the pane-id check", pane: paneView{PaneID: "%1", Kind: state.PaneKindAttachedAgent, ShellKey: "shell-b"}, id: true, key: false, want: false},
		{name: "coordinator row with a matching key is actionable", pane: paneView{PaneID: "%1", Kind: state.PaneKindAttachedAgent, ShellKey: "shell-b"}, id: false, key: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paneAliveForAction(tt.pane, byID(tt.id), byKey(tt.key)); got != tt.want {
				t.Fatalf("paneAliveForAction(%+v) = %v, want %v", tt.pane, got, tt.want)
			}
		})
	}
}
