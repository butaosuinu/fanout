package main

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/codexapp"
)

// TestSelfExecSubcommandNames pins the hidden self-exec subcommand tokens so a
// rename can never silently desync the tmux command line that spawns a popup /
// Plan Mode pane from the dispatch that recognizes it back. These strings are
// baked into tmux `display-popup`/`send-keys` invocations, so drift is a
// runtime break the type system cannot catch.
func TestSelfExecSubcommandNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: "new pane popup", got: tuiNewPanePopupCommand, want: "__tui-new-pane-popup"},
		{name: "help popup", got: tuiHelpPopupCommand, want: "__tui-help-popup"},
		{name: "close popup", got: tuiClosePopupCommand, want: "__tui-close-popup"},
		{name: "codex plan tui", got: codexapp.PlanTUICommand, want: "__codex-plan-tui"},
		{name: "codex team tui", got: codexapp.TeamTUICommand, want: "__codex-team-tui"},
		{name: "post-work-review JSON", got: postWorkReviewJSONCommand, want: "__post-work-review-json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("subcommand token = %q, want %q", tc.got, tc.want)
			}
		})
	}
}
