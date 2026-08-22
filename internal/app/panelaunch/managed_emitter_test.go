package panelaunch

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestManagedEmitterLaunchInjectsClaudeSettingsAndExactIdentity(t *testing.T) {
	intent := state.LaunchIntent{
		ID: "issue:3:524:529",
		Resource: state.RuntimeResource{
			WorkspaceID: "workspace-1", PaneID: "workspace-1:pane-1", TerminalID: "terminal-1",
		},
	}
	route := backend.OwnedLaunchRoute{
		Session: "fanout-owned", SocketPath: "/tmp/fanout-owned/herdr.sock",
		LauncherPath: "/opt/fanout build/fanout",
		EmitterPath:  "/opt/current fanout/fanout",
	}
	launch, err := newManagedEmitterLaunch(
		Request{Agent: "claude"}, route, intent, strings.Repeat("a", 32),
		"fanout-agent", "/repo/.fanout/state.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	backendArgs, err := managedEmitterBackendArgs(Request{Agent: "claude"}, route)
	if err != nil {
		t.Fatal(err)
	}
	if len(backendArgs) != 2 || backendArgs[0] != "--settings" || !json.Valid([]byte(backendArgs[1])) {
		t.Fatalf("backend args = %#v", backendArgs)
	}
	settings := backendArgs[1]
	if !strings.Contains(settings, `"matcher":"`+managedClaudeExitReasons+`"`) ||
		!strings.Contains(settings, `"timeout":15`) ||
		!strings.Contains(settings, "$FANOUT_EMITTER_STATE_PATH.sequence") ||
		strings.Contains(settings, "clear") || strings.Contains(settings, "resume") {
		t.Fatalf("SessionEnd settings = %s", settings)
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

func TestManagedClaudeSequenceCounterIncreases(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	for _, want := range []string{"1", "2"} {
		cmd := exec.Command("sh", "-c", managedClaudeNextSequence)
		cmd.Env = append(os.Environ(), telemetry.EmitterPathEnv+"="+statePath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sequence command: %v: %s", err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Fatalf("sequence = %q, want %q", got, want)
		}
	}
}

func TestManagedEmitterLaunchUsesCurrentPinnedEmitterInsteadOfSessionLauncher(t *testing.T) {
	backendArgs, err := managedEmitterBackendArgs(
		Request{Agent: "claude"},
		backend.OwnedLaunchRoute{
			LauncherPath: "/owned/old-fanout", EmitterPath: "/owned/current-fanout",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	settings := backendArgs[1]
	if !strings.Contains(settings, "/owned/current-fanout") || strings.Contains(settings, "old-fanout") {
		t.Fatalf("settings use stale session launcher: %s", settings)
	}
}

func TestManagedEmitterLaunchLeavesNonPlanCodexBare(t *testing.T) {
	launch, err := newManagedEmitterLaunch(Request{Agent: "codex"}, backend.OwnedLaunchRoute{}, state.LaunchIntent{}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if launch.nonce != "" || len(launch.environment) != 0 {
		t.Fatalf("codex emitter launch = %+v", launch)
	}
	backendArgs, err := managedEmitterBackendArgs(Request{Agent: "codex"}, backend.OwnedLaunchRoute{})
	if err != nil || len(backendArgs) != 0 {
		t.Fatalf("managedEmitterBackendArgs(codex) = %#v, %v; want no arguments", backendArgs, err)
	}
}

func TestManagedEmitterLaunchInjectsCodexPlanIdentityWithoutBackendArgs(t *testing.T) {
	intent := state.LaunchIntent{
		ID: "issue:3:524:554", WorkspaceLabel: "fanout-codex-plan",
		Resource: state.RuntimeResource{
			WorkspaceID: "workspace-1", Label: "fanout-codex-plan",
			PaneID: "workspace-1:pane-1", TerminalID: "terminal-1",
		},
	}
	route := backend.OwnedLaunchRoute{Session: "fanout-owned", SocketPath: "/tmp/herdr.sock"}
	launch, err := newManagedEmitterLaunch(
		Request{Agent: "codex", LaunchMode: agent.ModePlan}, route, intent,
		strings.Repeat("a", 32), "fanout-agent", "/repo/.fanout/state.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !telemetry.ValidNonce(launch.nonce) {
		t.Fatalf("Codex Plan emitter launch = %+v", launch)
	}
	backendArgs, err := managedEmitterBackendArgs(Request{Agent: "codex", LaunchMode: agent.ModePlan}, route)
	if err != nil || len(backendArgs) != 0 {
		t.Fatalf("managedEmitterBackendArgs(Codex Plan) = %#v, %v; want no arguments", backendArgs, err)
	}
	environment := environmentMap(t, launch.environment)
	if environment[telemetry.AgentEnv] != "codex" ||
		environment[telemetry.WorkspaceLabelEnv] != intent.WorkspaceLabel {
		t.Fatalf("Codex Plan emitter environment = %#v", environment)
	}
}

func TestApplyManagedLaunchTelemetryStartsSyntheticRunningUnrefined(t *testing.T) {
	pane := state.Pane{Backend: "herdr"}
	intent := state.LaunchIntent{
		ID: "issue:3:524:529",
		Launch: &state.LaunchCapsule{
			Nonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
			Executable: "/opt/bin/claude", Args: []string{"prompt"},
		},
	}
	applyManagedLaunchTelemetry(&pane, intent)
	if pane.ReportedState != "running" || pane.StateRefinement {
		t.Fatalf("initial telemetry = (%q, %t), want synthetic running without refinement", pane.ReportedState, pane.StateRefinement)
	}
	if pane.EmitterRowKey != intent.ID || pane.LaunchNonce != intent.Launch.Nonce || pane.EmitterNonce != intent.Launch.EmitterNonce {
		t.Fatalf("launch binding = %+v", pane)
	}
}

func TestApplyManagedLaunchTelemetryPersistsDirectLaunchWithoutEmitter(t *testing.T) {
	pane := state.Pane{Backend: "herdr"}
	intent := state.LaunchIntent{Launch: &state.LaunchCapsule{
		Executable: "/opt/bin/codex", Args: []string{"prompt"},
	}}

	applyManagedLaunchTelemetry(&pane, intent)

	if pane.LaunchExecutable != intent.Launch.Executable ||
		!slices.Equal(pane.LaunchArgs, intent.Launch.Args) || pane.ReportedState != "" {
		t.Fatalf("direct launch binding = %+v", pane)
	}
}

func TestApplyManagedLaunchTelemetryDropsPendingStateFromReplacedSession(t *testing.T) {
	pending := backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "old-session",
	}
	current := pending
	current.Value = "new-session"
	pane := state.Pane{Backend: "herdr", AgentSession: &current, StateRefinement: true}
	intent := state.LaunchIntent{
		ID: "issue:3:524:529",
		Launch: &state.LaunchCapsule{
			Nonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
			PendingReportedState: "idle", PendingReportedSeq: 4, PendingAgentSession: &pending,
		},
	}

	applyManagedLaunchTelemetry(&pane, intent)

	if pane.ReportedState != "running" || pane.ReportedStateSeq != 0 || pane.StateRefinement {
		t.Fatalf("replaced-session telemetry = (%q, %t), want synthetic running", pane.ReportedState, pane.StateRefinement)
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
