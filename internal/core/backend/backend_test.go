package backend

import (
	"errors"
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

func TestUnsupportedMatchesSentinel(t *testing.T) {
	err := Unsupported(Herdr, "launch")
	if !errors.Is(err, ErrUnsupported) || !IsUnsupported(err) {
		t.Fatalf("Unsupported() = %v, want sentinel and typed match", err)
	}
}

func TestCloseResultZeroValueFailsClosed(t *testing.T) {
	var result CloseResult
	if result.Status != CloseFailed {
		t.Fatalf("zero-value CloseResult status = %d, want CloseFailed", result.Status)
	}
}
