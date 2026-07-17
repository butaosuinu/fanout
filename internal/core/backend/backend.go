// Package backend defines the runtime-neutral pane contract used by fanout.
// Implementations live in infra; this package contains no process or filesystem
// access.
package backend

import (
	"errors"
	"fmt"
	"strings"
)

// Name identifies a pane runtime implementation.
type Name string

const (
	Tmux  Name = "tmux"
	Herdr Name = "herdr"
)

// ParseName validates a configured backend name. An empty value means no
// selection and is rejected; persisted legacy rows should use NormalizeName.
func ParseName(raw string) (Name, error) {
	name := Name(strings.TrimSpace(raw))
	switch name {
	case Tmux, Herdr:
		return name, nil
	default:
		return "", fmt.Errorf("unknown runtime backend %q (supported: %s, %s)", raw, Tmux, Herdr)
	}
}

// NormalizeName maps the empty backend stored by legacy tmux rows to tmux.
// Non-empty values are returned unchanged so callers can validate them without
// silently treating corrupt state as tmux.
func NormalizeName(name Name) Name {
	if strings.TrimSpace(string(name)) == "" {
		return Tmux
	}
	return name
}

// PaneRef is the runtime-native address of one pane. Pane is deliberately an
// opaque string: tmux uses values such as %12 while herdr uses values such as
// w1:p1. Workspace is the backend-native containing workspace/session target.
type PaneRef struct {
	Backend   Name
	Workspace string
	Pane      string
}

// AgentState is fanout's six-value display vocabulary.
type AgentState string

const (
	AgentRunning AgentState = "running"
	AgentWorking AgentState = "working"
	AgentPlan    AgentState = "plan"
	AgentBlocked AgentState = "blocked"
	AgentIdle    AgentState = "idle"
	AgentDone    AgentState = "done"
)

// ParseAgentState accepts only the six values fanout exposes. Backend-native
// unknown values stay outside this vocabulary until their mapping is proven.
func ParseAgentState(raw string) (AgentState, bool) {
	state := AgentState(strings.TrimSpace(raw))
	switch state {
	case AgentRunning, AgentWorking, AgentPlan, AgentBlocked, AgentIdle, AgentDone:
		return state, true
	default:
		return "", false
	}
}

// LivePane is the backend-neutral observation of one live pane. Runtime-owned
// identity fields remain separate from display metadata so later liveness code
// can choose the evidence appropriate for each backend.
type LivePane struct {
	Ref              PaneRef
	CurrentPath      string
	Title            string
	AgentState       AgentState
	NativeAgentState string
	TerminalID       string
	AgentID          string
	ShellKey         string
	ProjectRoot      string
	WorktreePath     string
	Role             string
	SessionID        string
	SocketPath       string
}

// LaunchRequest contains only runtime-neutral launch inputs. Popup, keybind,
// layout, pane decoration, and session management stay implementation details.
type LaunchRequest struct {
	Workspace    string
	Target       string
	WorktreePath string
	Command      string
	StartGate    string
}

// Backend is the minimum runtime surface. Implementations may expose
// backend-specific helpers, but orchestration depends only on these methods.
type Backend interface {
	Name() Name
	CheckAvailable() error
	Launch(LaunchRequest) (PaneRef, error)
	ReleaseStartGate(string) error
	ListLive() ([]LivePane, error)
	Read(PaneRef, int) (string, error)
	SendLine(PaneRef, string) error
	Focus(PaneRef) error
	Close(PaneRef) error
}

// CloseStatus classifies a destructive close whose runtime identity was
// checked before and after the close attempt.
type CloseStatus int

const (
	CloseConfirmed CloseStatus = iota
	CloseStale
	CloseFailed
)

// CloseRequest carries the durable identity needed to avoid closing a pane
// whose runtime-native id was reused.
type CloseRequest struct {
	Ref          PaneRef
	WorktreePath string
	ShellKey     string
}

// CloseResult reports the identity-checked result. ContainerID is an optional
// runtime-native layout hint (a tmux window id for the tmux adapter).
type CloseResult struct {
	Status      CloseStatus
	ContainerID string
}

// OwnedCloser is an optional destructive capability. It stays separate from
// Backend.Close because only runtimes with a proven durable identity contract
// may let lifecycle cleanup remove state after a close attempt.
type OwnedCloser interface {
	CloseOwned(CloseRequest) (CloseResult, error)
}

// FreshCloser is an optional capability for a pane returned by the immediately
// preceding Launch call, before its durable identity has been stamped.
type FreshCloser interface {
	CloseFresh(PaneRef) error
}

// UnsupportedError reports an operation intentionally disabled by a backend.
// Herdr v1 uses this fail-closed result for every mutation and targeted read.
type UnsupportedError struct {
	Backend   Name
	Operation string
}

// ErrUnsupported is the sentinel matched by errors.Is for operations a
// backend deliberately disables.
var ErrUnsupported = errors.New("runtime backend operation unsupported")

func (e UnsupportedError) Error() string {
	return fmt.Sprintf("runtime backend %s does not support %s", e.Backend, e.Operation)
}

func (e UnsupportedError) Unwrap() error { return ErrUnsupported }

// Unsupported constructs a typed unsupported-operation error.
func Unsupported(name Name, operation string) error {
	return UnsupportedError{Backend: name, Operation: operation}
}

// IsUnsupported reports whether err is an UnsupportedError.
func IsUnsupported(err error) bool {
	var target UnsupportedError
	return errors.As(err, &target)
}
