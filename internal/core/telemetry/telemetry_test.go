package telemetry

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

func TestParseSignalAcceptsExactLaunchIdentity(t *testing.T) {
	env := validSignalEnvironment()
	got, err := ParseSignal([]string{"working"}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got.State != backend.AgentWorking || got.Backend != backend.Herdr || got.RowKey != "issue:3:524:529" {
		t.Fatalf("signal = %+v", got)
	}
}

func TestParseSignalRejectsSyntheticOrIncompleteInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		mutate func(map[string]string)
	}{
		{name: "synthetic running", args: []string{"running"}},
		{name: "unknown state", args: []string{"waiting"}},
		{name: "missing argument"},
		{name: "path mismatch", args: []string{"idle"}, mutate: func(env map[string]string) {
			env[EmitterPathEnv] = "/other/.fanout/state.json"
		}},
		{name: "wrong backend", args: []string{"done"}, mutate: func(env map[string]string) {
			env[BackendEnv] = "tmux"
		}},
		{name: "unsupported agent", args: []string{"done"}, mutate: func(env map[string]string) {
			env[AgentEnv] = "codex"
		}},
		{name: "bad generation", args: []string{"plan"}, mutate: func(env map[string]string) {
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
