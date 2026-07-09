package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
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

// TestNewIssuePlanPaneRequestWritesIssueCoordinatorBrief pins the issue-sourced
// plan coordinator pane request: a plain (non-Codex-Plan-Mode) agent whose
// one-line prompt invokes the fanout-plan skill on the issue-derived coordinator
// brief, and whose briefing carries the issue title/body, the worker --agent
// override, and the "Refs #N" (never "Closes") requirement.
func TestNewIssuePlanPaneRequestWritesIssueCoordinatorBrief(t *testing.T) {
	issue := ghissue.Issue{
		Number: 123,
		Title:  "Add full-text search",
		Body:   "Users cannot search issues by keyword.",
	}
	tests := []struct {
		name        string
		coordinator string
		wantPrefix  string
	}{
		{name: "claude coordinator uses the slash command", coordinator: "claude", wantPrefix: "/fanout plan "},
		{name: "codex coordinator uses the dollar invocation", coordinator: "codex", wantPrefix: "$fanout-plan "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newIssuePlanPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), issue, tt.coordinator, "codex", "shell-issue-plan-key")

			if !strings.HasPrefix(req.Prompt, tt.wantPrefix) {
				t.Fatalf("req.Prompt = %q, want %q prefix", req.Prompt, tt.wantPrefix)
			}
			if !strings.Contains(req.Prompt, req.BriefingPath) {
				t.Fatalf("req.Prompt %q does not reference briefing path %q", req.Prompt, req.BriefingPath)
			}
			if base := filepath.Base(req.BriefingPath); base != "fanout-repo-plan-issue-123.md" {
				t.Fatalf("briefing basename = %q, want fanout-repo-plan-issue-123.md", base)
			}
			if !strings.Contains(req.BriefingBody, issue.Title) {
				t.Fatalf("req.BriefingBody = %q, want the issue title", req.BriefingBody)
			}
			if !strings.Contains(req.BriefingBody, issue.Body) {
				t.Fatalf("req.BriefingBody = %q, want the issue body", req.BriefingBody)
			}
			// The worker agent flows into the fan-out command; a codex worker pins the override.
			if !strings.Contains(req.BriefingBody, "--agent codex") {
				t.Fatalf("req.BriefingBody = %q, want a --agent codex worker override", req.BriefingBody)
			}
			if !strings.Contains(req.BriefingBody, "Refs #123") {
				t.Fatalf("req.BriefingBody = %q, want a Refs #123 requirement", req.BriefingBody)
			}
			if req.CodexPlanMode {
				t.Fatal("req.CodexPlanMode = true, want false for a plan coordinator")
			}
			if req.ParentRef != panelaunch.ManualParentRef {
				t.Fatalf("req.ParentRef = %q, want %q", req.ParentRef, panelaunch.ManualParentRef)
			}
			if req.ShellKey != "shell-issue-plan-key" {
				t.Fatalf("req.ShellKey = %q, want the liveness key passed through", req.ShellKey)
			}
			if req.Slug != "plan-issue-123" {
				t.Fatalf("req.Slug = %q, want plan-issue-123", req.Slug)
			}
			if want := "plan: #123 Add full-text search"; req.Title != want {
				t.Fatalf("req.Title = %q, want %q", req.Title, want)
			}
		})
	}
}

// TestLaunchIssuePlanFromTUIValidatesBeforeGH pins the fail-fast validation:
// a bad issue number or an unknown agent name must be rejected before any gh
// call, so no gh binary is needed on PATH.
func TestLaunchIssuePlanFromTUIValidatesBeforeGH(t *testing.T) {
	tests := []struct {
		name        string
		issueNum    int
		coordinator string
		worker      string
		wantErr     string
	}{
		{name: "rejects non-positive issue number", issueNum: 0, coordinator: "claude", worker: "codex", wantErr: "issue number is required"},
		{name: "rejects unknown coordinator agent", issueNum: 7, coordinator: "bogus", worker: "codex", wantErr: `unknown agent "bogus"`},
		{name: "rejects unknown worker agent", issueNum: 7, coordinator: "claude", worker: "bogus", wantErr: `unknown agent "bogus"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// An empty PATH: a validation that leaked to a gh call would fail with a
			// gh-not-found error instead of the expected message, exposing the bug.
			t.Setenv("PATH", t.TempDir())
			_, err := launchIssuePlanFromTUI(t.TempDir(), "fanout-test", "fanout", hooks.EmptyConfig(), tt.issueNum, tt.coordinator, tt.worker)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("launchIssuePlanFromTUI() error = %v, want %q", err, tt.wantErr)
			}
		})
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
