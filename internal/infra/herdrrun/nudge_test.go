package herdrrun

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/naming"
)

// The prompt response is checked after the prompt was delivered, so it admits
// the same record the preflight did. Refusing an anonymous record there turns a
// delivered nudge into a reported failure, and the retry sends it twice.
func TestAgentPromptResponseAdmitsUnnamedRecord(t *testing.T) {
	minted := naming.ManagedAgentName("/repo/.git", "row", strings.Repeat("a", 32))
	h := newOwnedHarness(t)
	setAgentName(h, "w2:p1", minted)
	target := h.target()

	if target.Agent != "codex" {
		t.Fatalf("fixture provider = %q, want codex so the other-provider case differs", target.Agent)
	}
	tests := []struct {
		name    string
		mutate  func(*agentJSON)
		wantErr bool
	}{
		{name: "record still answers to the recorded name"},
		{
			name:   "runtime dropped the name",
			mutate: func(a *agentJSON) { a.Name = nil },
		},
		{
			name: "record answers to another name",
			mutate: func(a *agentJSON) {
				other := "someone-else"
				a.Name = &other
			},
			wantErr: true,
		},
		{
			// An anonymous record is admitted on the recorded name's shape, so
			// the provider is what keeps a prompt delivered to a different
			// agent from being reported as a success.
			name: "anonymous record belongs to another provider",
			mutate: func(a *agentJSON) {
				other := "claude"
				a.Name, a.Agent = nil, &other
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAgentPromptResponse(agentPromptResponse(target, tt.mutate), target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateAgentPromptResponse() = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}
