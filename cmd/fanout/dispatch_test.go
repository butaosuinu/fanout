package main

import (
	"os"
	"slices"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
)

// upgradeContract explains, in the failures below, why these literals cannot be
// renamed even in lockstep: the process spelling them out is an already running
// herdr server started by an older fanout, and the process reading them is the
// newly installed binary that server execs.
const upgradeContract = "an already running herdr server re-execs the installed fanout binary with this literal; " +
	"renaming it strands every owned session started before the upgrade"

// herdrPaneLauncherEnv duplicates the unexported marker in
// internal/infra/herdrrun/launcher.go so a rename there fails here.
const herdrPaneLauncherEnv = "FANOUT_HERDR_PANE_LAUNCHER"

// herdrSupervisorCommand duplicates the unexported token in
// internal/infra/herdrrun/owned.go so a rename there fails here.
const herdrSupervisorCommand = "__herdr-supervisor"

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
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("subcommand token = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestHerdrSupervisorSelfExecToken pins the supervisor self-exec token across
// binary versions: the argv comes from a server the old binary started, so the
// new binary has to keep recognizing the old spelling in argv[0] of its args.
func TestHerdrSupervisorSelfExecToken(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "token an older server re-execs with",
			args: []string{herdrSupervisorCommand, "/run/fhr-501/fhr-abc/owner.json", "nonce", "start", "3"},
			want: true,
		},
		{name: "token must lead the argument list", args: []string{"plan", herdrSupervisorCommand}, want: false},
		{name: "no arguments is a normal invocation", args: nil, want: false},
		{name: "a near-miss spelling is not accepted", args: []string{"__herdr_supervisor"}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := herdrrun.IsSupervisorRequest(tt.args); got != tt.want {
				t.Fatalf("IsSupervisorRequest(%q) = %v, want %v; %s", tt.args, got, tt.want, upgradeContract)
			}
		})
	}
}

// TestHerdrPaneLauncherEnvMarker pins the pane-launcher env marker across
// binary versions: herdr starts panes with the shell environment recorded when
// the session was created, so the new binary has to keep keying on the old name
// and the exact value "1" the old one wrote.
func TestHerdrPaneLauncherEnvMarker(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{name: "marker an older server sets on the pane shell", value: "1", set: true, want: true},
		{name: "absent marker falls through to the normal dispatch", set: false, want: false},
		{name: "empty marker falls through to the normal dispatch", value: "", set: true, want: false},
		{name: "only the exact value selects the launcher", value: "true", set: true, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv registers the restore; Unsetenv then models an absent marker.
			t.Setenv(herdrPaneLauncherEnv, tt.value)
			if !tt.set {
				if err := os.Unsetenv(herdrPaneLauncherEnv); err != nil {
					t.Fatal(err)
				}
			}
			if got := herdrrun.IsPaneLauncherRequest(); got != tt.want {
				t.Fatalf("IsPaneLauncherRequest() with %s=%q (set=%v) = %v, want %v; %s",
					herdrPaneLauncherEnv, tt.value, tt.set, got, tt.want, upgradeContract)
			}
		})
	}
}

// TestSelfExecArgs covers the argv shapes the two self-exec entries arrive
// with. The env-recognized launcher carries no token at all, so the slice that
// forwards a token's trailing arguments has to tolerate a one-element argv.
func TestSelfExecArgs(t *testing.T) {
	for _, tt := range []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "env-recognized entry runs as a shell with no token",
			argv: []string{"/run/fhr-501/fhr-abc/launcher/fanout"},
			want: nil,
		},
		{name: "token with nothing trailing it", argv: []string{"fanout", herdrSupervisorCommand}, want: nil},
		{
			// The supervisor's real argv: marker path, nonce, start token, ready fd.
			name: "token forwards everything after it",
			argv: []string{
				"fanout", herdrSupervisorCommand,
				"/run/fhr-501/fhr-abc/owner.json", "nonce", "start", "3",
			},
			want: []string{"/run/fhr-501/fhr-abc/owner.json", "nonce", "start", "3"},
		},
		{name: "empty argv", argv: nil, want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := selfExecArgs(tt.argv); !slices.Equal(got, tt.want) {
				t.Fatalf("selfExecArgs(%q) = %q, want %q", tt.argv, got, tt.want)
			}
		})
	}
}

// TestSelfExecDispatchRunsTokenlessEntry runs the dispatch path itself, not just
// the recognition predicate the tests above pin. Herdr starts the pane launcher
// as the configured shell, so argv holds only the executable path; a dispatch
// that slices past an absent token panics before the entry runs, and every
// launched pane dies with the shell. The launcher rejects the incomplete
// environment here — reaching that rejection at all is the guarantee.
func TestSelfExecDispatchRunsTokenlessEntry(t *testing.T) {
	original := os.Args
	t.Cleanup(func() { os.Args = original })
	os.Args = []string{"/run/fhr-501/fhr-abc/launcher/fanout"}
	t.Setenv(herdrPaneLauncherEnv, "1")
	// An inherited launcher environment would make the entry wait for an intent
	// instead of returning, so blank the rest of the contract.
	t.Setenv("FANOUT_HERDR_LAUNCHER_PATH", "")
	t.Setenv("FANOUT_HERDR_CONTROL_PATH", "")

	var launcher func() exitcode.Code
	for _, entry := range selfExecDispatch() {
		if entry.match(nil) {
			launcher = entry.handle
			break
		}
	}
	if launcher == nil {
		t.Fatal("selfExecDispatch() has no entry matching a tokenless launcher invocation")
	}
	if got := launcher(); got == exitcode.OK {
		t.Fatalf("launcher handle() = %v, want non-zero for an incomplete launcher environment", got)
	}
}
