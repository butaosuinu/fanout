package telemetry

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

func TestParseSignalAcceptsExactLaunchIdentity(t *testing.T) {
	env := validSignalEnvironment()
	got, err := ParseSignal([]string{"working", "7"}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got.State != backend.AgentWorking || got.Sequence != 7 ||
		got.Backend != backend.Herdr || got.RowKey != "issue:3:524:529" {
		t.Fatalf("signal = %+v", got)
	}
}

func TestParseSignalAcceptsCodexPlanController(t *testing.T) {
	env := validSignalEnvironment()
	env[AgentEnv] = "codex"
	if _, err := ParseSignal([]string{"plan"}, func(key string) string { return env[key] }); err != nil {
		t.Fatal(err)
	}
}

func TestSequencedClaudeLaunchRequiresClaudeSequenceMarker(t *testing.T) {
	sequenced := []string{"--settings", `{"command":"$FANOUT_EMITTER_STATE_PATH.sequence"}`}
	if !SequencedClaudeLaunch("claude", sequenced) {
		t.Fatal("SequencedClaudeLaunch() rejected the managed Claude marker")
	}
	if SequencedClaudeLaunch("claude", []string{"--settings", `{}`}) ||
		SequencedClaudeLaunch("codex", sequenced) ||
		SequencedClaudeLaunch("claude", []string{"prompt $FANOUT_EMITTER_STATE_PATH.sequence"}) {
		t.Fatal("SequencedClaudeLaunch() accepted an unsequenced launch")
	}
}

func TestParseSignalRejectsSyntheticOrIncompleteInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		mutate func(map[string]string)
	}{
		{name: "synthetic running", args: []string{"running", "1"}},
		{name: "unknown state", args: []string{"waiting", "1"}},
		{name: "missing argument"},
		{name: "missing sequence", args: []string{"idle"}},
		{name: "invalid sequence", args: []string{"idle", "old"}},
		{name: "zero sequence", args: []string{"idle", "0"}},
		{name: "path mismatch", args: []string{"idle", "1"}, mutate: func(env map[string]string) {
			env[EmitterPathEnv] = "/other/.fanout/state.json"
		}},
		{name: "wrong backend", args: []string{"done", "1"}, mutate: func(env map[string]string) {
			env[BackendEnv] = "tmux"
		}},
		{name: "unsupported agent", args: []string{"done"}, mutate: func(env map[string]string) {
			env[AgentEnv] = "opencode"
		}},
		{name: "bad generation", args: []string{"plan", "1"}, mutate: func(env map[string]string) {
			env[LaunchNonceEnv] = "old"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := validSignalEnvironment()
			if test.mutate != nil {
				test.mutate(env)
			}
			if _, err := ParseSignal(test.args, func(key string) string { return env[key] }); err == nil {
				t.Fatal("ParseSignal() succeeded")
			}
		})
	}
}

func validSignalEnvironment() map[string]string {
	statePath := "/repo/.fanout/state.json"
	return map[string]string{
		StatePathEnv: statePath, EmitterPathEnv: statePath,
		RowKeyEnv: "issue:3:524:529", LaunchNonceEnv: strings.Repeat("a", 32),
		EmitterNonceEnv: strings.Repeat("b", 32), BackendEnv: "herdr",
		SessionEnv: "fanout-owned", SocketPathEnv: "/tmp/herdr.sock",
		WorkspaceIDEnv: "w1", PaneIDEnv: "w1:p1", TerminalIDEnv: "term-1",
		AgentEnv: "claude", AgentIDEnv: "fanout-agent",
	}
}
