// Package telemetry defines the runtime-independent agent telemetry wire contract.
package telemetry

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

const (
	Command               = "__fanout-emitter"
	SequenceCommand       = "__fanout-emitter-sequence"
	EmitterTimeoutSeconds = 15
	// SequencedSessionEndTimeoutSeconds covers the serial allocator and emitter
	// budgets, plus one second for their shell handoff.
	SequencedSessionEndTimeoutSeconds = 2*EmitterTimeoutSeconds + 1
)

const (
	StatePathEnv      = "FANOUT_STATE_PATH"
	EmitterPathEnv    = "FANOUT_EMITTER_STATE_PATH"
	RowKeyEnv         = "FANOUT_EMITTER_ROW_KEY"
	LaunchNonceEnv    = "FANOUT_EMITTER_LAUNCH_NONCE"
	EmitterNonceEnv   = "FANOUT_EMITTER_NONCE"
	BackendEnv        = "FANOUT_EMITTER_BACKEND"
	SessionEnv        = "FANOUT_EMITTER_SESSION"
	SocketPathEnv     = "FANOUT_EMITTER_SOCKET_PATH"
	WorkspaceIDEnv    = "FANOUT_EMITTER_WORKSPACE_ID"
	WorkspaceLabelEnv = "FANOUT_EMITTER_WORKSPACE_LABEL"
	WorktreePathEnv   = "FANOUT_EMITTER_WORKTREE_PATH"
	PaneIDEnv         = "FANOUT_EMITTER_PANE_ID"
	TerminalIDEnv     = "FANOUT_EMITTER_TERMINAL_ID"
	AgentEnv          = "FANOUT_EMITTER_AGENT"
	AgentIDEnv        = "FANOUT_EMITTER_AGENT_ID"
)

var noncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Signal is one launch-bound provider state report. Its identity values are
// comparison inputs, not secrets or capabilities.
type Signal struct {
	StatePath      string
	RowKey         string
	LaunchNonce    string
	EmitterNonce   string
	Backend        backend.Name
	Session        string
	SocketPath     string
	WorkspaceID    string
	WorkspaceLabel string
	WorktreePath   string
	PaneID         string
	TerminalID     string
	Agent          string
	AgentID        string
	State          backend.AgentState
	Sequence       uint64
}

// IsRequest reports whether args target the hidden telemetry emitter.
func IsRequest(args []string) bool {
	return len(args) > 0 && args[0] == Command
}

// IsSequenceRequest reports whether args target the synchronous sequence allocator.
func IsSequenceRequest(args []string) bool {
	return len(args) == 1 && args[0] == SequenceCommand
}

// SequencedClaudeLaunch reports whether persisted launch arguments carry the
// sequence hook. LaunchArgs survive saves by older state writers, so this also
// distinguishes bindings that must be fenced during a mixed-version upgrade.
func SequencedClaudeLaunch(agent string, args []string) bool {
	if agent != "claude" {
		return false
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--settings" && strings.Contains(args[i+1], SequenceCommand) {
			return true
		}
	}
	return false
}

// ClaudeSequenceWatermarkMissing reports a refined sequenced launch whose
// applied sequence was stripped by a writer that does not know the field.
func ClaudeSequenceWatermarkMissing(agent string, args []string, refined bool, applied uint64) bool {
	return refined && applied == 0 && SequencedClaudeLaunch(agent, args)
}

// ParseSignal validates one hidden-command invocation and its inherited wire
// identity. getenv keeps parsing pure and directly testable.
func ParseSignal(args []string, getenv func(string) string) (Signal, error) {
	agentName := getenv(AgentEnv)
	wantArgs := 1
	if agentName == "claude" {
		wantArgs = 2
	}
	if len(args) != wantArgs {
		return Signal{}, fmt.Errorf("expected one reported state and its required sequence")
	}
	state, ok := providerState(args[0])
	if !ok {
		return Signal{}, fmt.Errorf("unsupported reported state %q", args[0])
	}
	var sequence uint64
	if agentName == "claude" {
		var err error
		sequence, err = strconv.ParseUint(args[1], 10, 64)
		if err != nil || sequence == 0 {
			return Signal{}, fmt.Errorf("claude telemetry sequence is invalid")
		}
	}
	signal := signalFromEnvironment(getenv, agentName, state, sequence)
	if err := validateSignal(signal, getenv(EmitterPathEnv)); err != nil {
		return Signal{}, err
	}
	return signal, nil
}

func signalFromEnvironment(
	getenv func(string) string,
	agentName string,
	state backend.AgentState,
	sequence uint64,
) Signal {
	return Signal{
		StatePath: getenv(StatePathEnv), RowKey: getenv(RowKeyEnv),
		LaunchNonce: getenv(LaunchNonceEnv), EmitterNonce: getenv(EmitterNonceEnv),
		Backend: backend.Name(getenv(BackendEnv)), Session: getenv(SessionEnv),
		SocketPath: getenv(SocketPathEnv), WorkspaceID: getenv(WorkspaceIDEnv),
		WorkspaceLabel: getenv(WorkspaceLabelEnv), WorktreePath: getenv(WorktreePathEnv),
		PaneID: getenv(PaneIDEnv), TerminalID: getenv(TerminalIDEnv),
		Agent: agentName, AgentID: getenv(AgentIDEnv), State: state, Sequence: sequence,
	}
}

func providerState(raw string) (backend.AgentState, bool) {
	state, ok := backend.ParseAgentState(raw)
	if !ok || state == backend.AgentRunning {
		return "", false
	}
	return state, true
}

func validateSignal(signal Signal, emitterPath string) error {
	if !cleanAbsolute(signal.StatePath) || signal.StatePath != emitterPath {
		return fmt.Errorf("emitter state path is not an exact canonical absolute path")
	}
	if signal.RowKey == "" || strings.ContainsRune(signal.RowKey, '\x00') {
		return fmt.Errorf("emitter row key is invalid")
	}
	if !ValidNonce(signal.LaunchNonce) || !ValidNonce(signal.EmitterNonce) {
		return fmt.Errorf("emitter nonce is invalid")
	}
	if signal.Backend != backend.Herdr {
		return fmt.Errorf("emitter backend must be %s", backend.Herdr)
	}
	if !slices.Contains([]string{"claude", "codex"}, signal.Agent) {
		return fmt.Errorf("telemetry emitter supports Claude and Codex launches only")
	}
	return validateRuntimeIdentity(signal)
}

func validateRuntimeIdentity(signal Signal) error {
	values := []string{
		signal.Session, signal.SocketPath, signal.WorkspaceID, signal.PaneID,
		signal.WorkspaceLabel, signal.TerminalID, signal.Agent, signal.AgentID,
	}
	for _, value := range values {
		if value == "" || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("emitter runtime identity is incomplete")
		}
	}
	if !cleanAbsolute(signal.SocketPath) {
		return fmt.Errorf("emitter socket path is not canonical and absolute")
	}
	if !cleanAbsolute(signal.WorktreePath) {
		return fmt.Errorf("emitter worktree path is not canonical and absolute")
	}
	return nil
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

// ValidNonce validates launch-scoped opaque nonces without treating them as
// authorization material.
func ValidNonce(value string) bool {
	return noncePattern.MatchString(value)
}
