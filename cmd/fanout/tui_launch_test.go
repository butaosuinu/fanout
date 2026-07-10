package main

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func TestPlanSkillPromptPerAgent(t *testing.T) {
	path := planPromptPath("/repo", 1)
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
	wantPrefix := briefing.Dir("/repo") + "/fanout-repo-plan-prompt-"
	if !strings.HasPrefix(req.BriefingPath, wantPrefix) {
		t.Fatalf("req.BriefingPath = %q, want %q prefix", req.BriefingPath, wantPrefix)
	}
	if req.CodexPlanMode {
		t.Fatal("req.CodexPlanMode = true, want false for a plan coordinator")
	}
	if req.ParentRef != panelaunch.ManualParentRef {
		t.Fatalf("req.ParentRef = %q, want %q", req.ParentRef, panelaunch.ManualParentRef)
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

func TestLaunchPlanPromptFromTUIReturnsCoordinatorPaneID(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	installFakeExecutable(t, "claude")
	installTUITmuxShim(t, "%88")
	req := fanouttui.LaunchRequest{
		Prompt:     "Ship search",
		PlanFanout: true,
		Agents:     []string{"claude"},
	}

	result, err := launchPlanPromptFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CreatedPaneIDs) != 1 || result.CreatedPaneIDs[0] != "%88" {
		t.Fatalf("created pane ids = %#v, want [%%88]", result.CreatedPaneIDs)
	}
	if !strings.Contains(result.Notice, "started plan coordinator (claude)") {
		t.Fatalf("notice = %q, want coordinator success", result.Notice)
	}
}
