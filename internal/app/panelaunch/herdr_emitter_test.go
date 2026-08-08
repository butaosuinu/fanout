package panelaunch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestHerdrEmitterLaunchInjectsClaudeSettingsAndExactIdentity(t *testing.T) {
	intent := state.HerdrIntent{
		ID: "issue:3:524:529",
		Resource: state.HerdrResource{
			WorkspaceID: "workspace-1", PaneID: "workspace-1:pane-1", TerminalID: "terminal-1",
		},
	}
	route := herdrrun.OwnedLaunchRoute{
		Session: "fanout-owned", SocketPath: "/tmp/fanout-owned/herdr.sock",
		LauncherPath: "/opt/fanout build/fanout",
		EmitterPath:  "/opt/current fanout/fanout",
	}
	launch, err := newHerdrEmitterLaunch(
		Request{Agent: "claude"}, route, intent, strings.Repeat("a", 32),
		"fanout-agent", "/repo/.fanout/state.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(launch.backendArgs) != 2 || launch.backendArgs[0] != "--settings" || !json.Valid([]byte(launch.backendArgs[1])) {
		t.Fatalf("backend args = %#v", launch.backendArgs)
	}
	if !telemetry.ValidNonce(launch.nonce) {
		t.Fatalf("emitter nonce = %q", launch.nonce)
	}
	environment := environmentMap(t, launch.environment)
	want := map[string]string{
		telemetry.EmitterPathEnv:  "/repo/.fanout/state.json",
		telemetry.RowKeyEnv:       intent.ID,
		telemetry.LaunchNonceEnv:  strings.Repeat("a", 32),
		telemetry.EmitterNonceEnv: launch.nonce,
		telemetry.BackendEnv:      "herdr",
		telemetry.SessionEnv:      route.Session,
		telemetry.SocketPathEnv:   route.SocketPath,
		telemetry.WorkspaceIDEnv:  intent.Resource.WorkspaceID,
		telemetry.PaneIDEnv:       intent.Resource.PaneID,
		telemetry.TerminalIDEnv:   intent.Resource.TerminalID,
		telemetry.AgentEnv:        "claude",
		telemetry.AgentIDEnv:      "fanout-agent",
	}
	for key, value := range want {
		if environment[key] != value {
			t.Fatalf("environment[%s] = %q, want %q", key, environment[key], value)
		}
	}
	if _, leaked := environment[telemetry.StatePathEnv]; leaked {
		t.Fatal("owner FANOUT_STATE_PATH leaked into the agent environment")
	}
}

func TestHerdrEmitterLaunchUsesCurrentPinnedEmitterInsteadOfSessionLauncher(t *testing.T) {
	launch, err := newHerdrEmitterLaunch(
		Request{Agent: "claude"},
		herdrrun.OwnedLaunchRoute{
			LauncherPath: "/owned/old-fanout", EmitterPath: "/owned/current-fanout",
		},
		state.HerdrIntent{}, strings.Repeat("a", 32), "agent", "/repo/.fanout/state.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	settings := launch.backendArgs[1]
	if !strings.Contains(settings, "/owned/current-fanout") || strings.Contains(settings, "old-fanout") {
		t.Fatalf("settings use stale session launcher: %s", settings)
	}
}

func TestHerdrEmitterLaunchLeavesCodexBare(t *testing.T) {
	launch, err := newHerdrEmitterLaunch(Request{Agent: "codex"}, herdrrun.OwnedLaunchRoute{}, state.HerdrIntent{}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if launch.nonce != "" || len(launch.backendArgs) != 0 || len(launch.environment) != 0 {
		t.Fatalf("codex emitter launch = %+v", launch)
	}
}

func TestApplyHerdrLaunchTelemetryStartsSyntheticRunningUnrefined(t *testing.T) {
	pane := state.Pane{Backend: "herdr"}
	intent := state.HerdrIntent{
		ID: "issue:3:524:529",
		Launch: &state.HerdrLaunch{
			Nonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
			Executable: "/opt/bin/claude", Args: []string{"prompt"},
		},
	}
	applyHerdrLaunchTelemetry(&pane, intent)
	if pane.ReportedState != "running" || pane.StateRefinement {
		t.Fatalf("initial telemetry = (%q, %t), want synthetic running without refinement", pane.ReportedState, pane.StateRefinement)
	}
	if pane.EmitterRowKey != intent.ID || pane.LaunchNonce != intent.Launch.Nonce || pane.EmitterNonce != intent.Launch.EmitterNonce {
		t.Fatalf("launch binding = %+v", pane)
	}
}

func environmentMap(t *testing.T, entries []string) map[string]string {
	t.Helper()
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || values[name] != "" {
			t.Fatalf("invalid environment entry %q", entry)
		}
		values[name] = value
	}
	return values
}
