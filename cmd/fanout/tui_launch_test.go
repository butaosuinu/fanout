package main

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func TestPlanSkillPromptPerAgent(t *testing.T) {
	const path = "/tmp/fanout-repo-plan-prompt-1.md"
	tests := []struct {
		name  string
		agent string
		want  string
	}{
		{name: "claude uses the slash command", agent: "claude", want: "/fanout plan " + path},
		{name: "codex uses the dollar invocation", agent: "codex", want: "$fanout-plan " + path},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planSkillPrompt(tt.agent, path); got != tt.want {
				t.Fatalf("planSkillPrompt(%q, %q) = %q, want %q", tt.agent, path, got, tt.want)
			}
		})
	}
}

// TestNewPlanPromptPaneRequestWritesSkillInvocation pins the coordinator pane
// request: a plain (non-Codex-Plan-Mode) agent whose one-line prompt invokes
// the fanout-plan skill on the full prompt written to the briefing file, and
// whose liveness key survives into the request (the repo-root WorktreePath is
// too broad for path-based liveness, so the key is the row's identity).
func TestNewPlanPromptPaneRequestWritesSkillInvocation(t *testing.T) {
	const prompt = "Build a full-text search over issues.\nInclude ranking and filters."
	req := newPlanPromptPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), prompt, "claude", "shell-coordinator-key")

	if !strings.HasPrefix(req.Prompt, "/fanout plan ") {
		t.Fatalf("req.Prompt = %q, want a /fanout plan invocation", req.Prompt)
	}
	if !strings.Contains(req.Prompt, req.BriefingPath) {
		t.Fatalf("req.Prompt %q does not reference briefing path %q", req.Prompt, req.BriefingPath)
	}
	if req.BriefingBody != prompt {
		t.Fatalf("req.BriefingBody = %q, want the full prompt", req.BriefingBody)
	}
	if !strings.HasPrefix(req.BriefingPath, "/tmp/fanout-repo-plan-prompt-") {
		t.Fatalf("req.BriefingPath = %q, want /tmp/fanout-repo-plan-prompt- prefix", req.BriefingPath)
	}
	if req.CodexPlanMode {
		t.Fatal("req.CodexPlanMode = true, want false for a plan coordinator")
	}
	if req.ParentRef != manualPaneParentRef {
		t.Fatalf("req.ParentRef = %q, want %q", req.ParentRef, manualPaneParentRef)
	}
	if req.ShellKey != "shell-coordinator-key" {
		t.Fatalf("req.ShellKey = %q, want the liveness key passed through", req.ShellKey)
	}
}

func TestLaunchPlanPromptFromTUIRejectsMultipleAgents(t *testing.T) {
	req := fanouttui.LaunchRequest{
		Prompt:     "Ship it",
		PlanFanout: true,
		Agents:     []string{"claude", "codex"},
	}
	_, err := launchPlanPromptFromTUI(t.TempDir(), "fanout-test", "fanout", hooks.Config{}, req)
	if err == nil || !strings.Contains(err.Error(), "select exactly one") {
		t.Fatalf("launchPlanPromptFromTUI() error = %v, want single-agent rejection", err)
	}
}
