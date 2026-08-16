package backend

import (
	"errors"
	"fmt"
	"testing"
)

func TestNormalizeNameTreatsLegacyEmptyAsTmux(t *testing.T) {
	if got := NormalizeName(""); got != Tmux {
		t.Fatalf("NormalizeName(empty) = %q, want %q", got, Tmux)
	}
	if got := NormalizeName(Herdr); got != Herdr {
		t.Fatalf("NormalizeName(herdr) = %q, want %q", got, Herdr)
	}
}

func TestAgentStateVocabulary(t *testing.T) {
	for _, value := range []AgentState{AgentRunning, AgentWorking, AgentPlan, AgentBlocked, AgentIdle, AgentDone} {
		got, ok := ParseAgentState(string(value))
		if !ok || got != value {
			t.Fatalf("ParseAgentState(%q) = %q,%v", value, got, ok)
		}
	}
	if got, ok := ParseAgentState("unknown"); ok || got != "" {
		t.Fatalf("ParseAgentState(unknown) = %q,%v, want empty,false", got, ok)
	}
}

func TestAgentSessionRefValidationPreservesExactTuple(t *testing.T) {
	valid := AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-1"}
	if !valid.Valid() {
		t.Fatalf("valid agent session ref rejected: %+v", valid)
	}
	path := valid
	path.Kind = "path"
	if !path.Valid() {
		t.Fatalf("path agent session ref rejected: %+v", path)
	}
	for _, invalid := range []AgentSessionRef{
		{},
		{Source: "", Agent: "codex", Kind: "id", Value: "session-1"},
		{Source: "herdr:codex", Agent: "", Kind: "id", Value: "session-1"},
		{Source: "herdr:codex", Agent: "codex", Kind: "", Value: "session-1"},
		{Source: "herdr:codex", Agent: "codex", Kind: "token", Value: "session-1"},
		{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: ""},
	} {
		if invalid.Valid() {
			t.Errorf("invalid agent session ref accepted: %+v", invalid)
		}
	}
}

func TestMapHerdrAgentState(t *testing.T) {
	tests := []struct {
		name         string
		agentPresent bool
		native       string
		reported     string
		want         AgentState
	}{
		{name: "reported running", agentPresent: true, native: "unknown", reported: "running", want: AgentRunning},
		{name: "reported plan", agentPresent: true, native: "working", reported: "plan", want: AgentPlan},
		{name: "working", agentPresent: true, native: "working", want: AgentWorking},
		{name: "blocked", agentPresent: true, native: "blocked", want: AgentBlocked},
		{name: "focused idle", agentPresent: true, native: "idle", want: AgentIdle},
		{name: "unfocused public done", agentPresent: true, native: "done", want: AgentDone},
		{name: "unknown is not running", agentPresent: true, native: "unknown"},
		{name: "missing agent record", agentPresent: false, native: "working", reported: "blocked"},
		{name: "invalid reported falls back", agentPresent: true, native: "idle", reported: "forged", want: AgentIdle},
		{name: "running is not a herdr public status", agentPresent: true, native: "running"},
		{name: "plan is unsupported", agentPresent: true, native: "plan"},
		{name: "invalid whitespace", agentPresent: true, native: " working "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapHerdrAgentState(tt.agentPresent, tt.native, tt.reported); got != tt.want {
				t.Fatalf("MapHerdrAgentState(%t, %q, %q) = %q, want %q", tt.agentPresent, tt.native, tt.reported, got, tt.want)
			}
		})
	}

	// Herdr exposes an unfocused idle agent as done, then idle after focus.
	// These are display states only; neither mapping invents running/plan.
	sequence := []LivePane{
		{AgentState: MapHerdrAgentState(true, "done", ""), Focused: false},
		{AgentState: MapHerdrAgentState(true, "idle", ""), Focused: true},
	}
	if sequence[0].AgentState != AgentDone || sequence[0].Focused ||
		sequence[1].AgentState != AgentIdle || !sequence[1].Focused {
		t.Fatalf("public focus sequence = %v, want [done idle]", sequence)
	}
}

func TestClassifyObservationError(t *testing.T) {
	tmux := ObservationRoute{Backend: Tmux}
	herdrA := ObservationRoute{Backend: Herdr, SessionID: "a", SocketPath: "/tmp/a.sock"}
	herdrB := ObservationRoute{Backend: Herdr, SessionID: "b", SocketPath: "/tmp/b.sock"}
	causeA := errors.New("a offline")
	causeB := errors.New("b offline")

	tests := []struct {
		name       string
		err        error
		wantRoutes map[ObservationRoute]bool
		wantAll    bool
	}{
		{name: "nil", wantRoutes: map[ObservationRoute]bool{}},
		{
			name:       "single scoped cause",
			err:        ObservationRouteUnavailable(herdrA, causeA),
			wantRoutes: map[ObservationRoute]bool{herdrA: true},
		},
		{
			name:       "outer wrapper remains scoped",
			err:        fmt.Errorf("poll runtime: %w", ObservationRouteUnavailable(herdrA, causeA)),
			wantRoutes: map[ObservationRoute]bool{herdrA: true},
		},
		{
			name: "joined scoped routes are deduplicated",
			err: errors.Join(
				ObservationRouteUnavailable(herdrA, causeA),
				fmt.Errorf("again: %w", ObservationRouteUnavailable(herdrA, causeA)),
				ObservationRouteUnavailable(herdrB, causeB),
			),
			wantRoutes: map[ObservationRoute]bool{herdrA: true, herdrB: true},
		},
		{
			name:       "untyped leaf degrades all",
			err:        errors.New("route discovery failed"),
			wantRoutes: map[ObservationRoute]bool{},
			wantAll:    true,
		},
		{
			name: "joined scoped and unscoped",
			err: errors.Join(
				ObservationRouteUnavailable(tmux, errors.New("tmux down")),
				fmt.Errorf("discover: %w", errors.New("state unreadable")),
			),
			wantRoutes: map[ObservationRoute]bool{tmux: true},
			wantAll:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRoutes, gotAll := ClassifyObservationError(tt.err)
			if !equalObservationRoutes(gotRoutes, tt.wantRoutes) || gotAll != tt.wantAll {
				t.Fatalf("ClassifyObservationError() = (%v, %t), want (%v, %t)", gotRoutes, gotAll, tt.wantRoutes, tt.wantAll)
			}
		})
	}

	wrapped := ObservationRouteUnavailable(herdrA, causeA)
	if wrapped.Error() != causeA.Error() || !errors.Is(wrapped, causeA) {
		t.Fatalf("route wrapper = %v, want unchanged text and errors.Is cause", wrapped)
	}
	if got := ObservationRouteUnavailable(herdrA, nil); got != nil {
		t.Fatalf("ObservationRouteUnavailable(route, nil) = %v, want nil", got)
	}
}

func equalObservationRoutes(a, b map[ObservationRoute]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for route, unavailable := range b {
		if a[route] != unavailable {
			return false
		}
	}
	return true
}

func TestUnsupportedMatchesSentinel(t *testing.T) {
	err := Unsupported(Herdr, "launch")
	if !errors.Is(err, ErrUnsupported) || !IsUnsupported(err) {
		t.Fatalf("Unsupported() = %v, want sentinel and typed match", err)
	}
}

// paneCapableBackend offers both optional pane capabilities. The embedded nil
// Backend supplies the required surface without a runtime: the accessors only
// type-assert, so no embedded method is ever called.
type paneCapableBackend struct{ Backend }

func (paneCapableBackend) SetPaneTitle(string, string) error        { return nil }
func (paneCapableBackend) SetPaneLabel(string, string) error        { return nil }
func (paneCapableBackend) EnablePaneBorderTitles(string) error      { return nil }
func (paneCapableBackend) SetPaneProjectRoot(string, string) error  { return nil }
func (paneCapableBackend) SetPaneWorktreePath(string, string) error { return nil }
func (paneCapableBackend) StampPaneShellKey(string, string) error   { return nil }

// bareBackend offers the required Backend surface and no optional capability.
type bareBackend struct{ Backend }

func TestPaneCapabilityAccessors(t *testing.T) {
	tests := []struct {
		name    string
		backend Backend
		want    bool
	}{
		{name: "backend implementing every pane capability", backend: paneCapableBackend{}, want: true},
		{name: "backend offering the required surface only", backend: bareBackend{}, want: false},
		{name: "unconfigured backend", backend: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := AsPaneDecorator(tt.backend); ok != tt.want {
				t.Fatalf("AsPaneDecorator(%s) = %t, want %t", tt.name, ok, tt.want)
			}
			if _, ok := AsLivenessStamper(tt.backend); ok != tt.want {
				t.Fatalf("AsLivenessStamper(%s) = %t, want %t", tt.name, ok, tt.want)
			}
		})
	}
}

func TestCloseResultZeroValueFailsClosed(t *testing.T) {
	var result CloseResult
	if result.Status != CloseFailed {
		t.Fatalf("zero-value CloseResult status = %d, want CloseFailed", result.Status)
	}
}
