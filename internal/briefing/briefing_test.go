package briefing

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/settings"
)

func TestPRVisualizationSectionHonorsSettings(t *testing.T) {
	defaults := settings.Defaults()
	for _, agent := range []string{"claude", "codex"} {
		got := Render(123, "structured PR briefing", "Issue body", agent, "release/v1", defaults, false)
		if !strings.Contains(got, "structure the PR body") {
			t.Fatalf("Render(..., %q) is missing structured PR body guidance", agent)
		}
		if !strings.Contains(got, "Diagram gate") {
			t.Fatalf("Render(..., %q) is missing Diagram gate guidance", agent)
		}
		for _, want := range []string{
			"Closes #123",
			"git diff --name-only release/v1...HEAD",
			"git diff --cached --name-only",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("Render(..., %q) missing %q", agent, want)
			}
		}
	}

	noVisualization := defaults
	noVisualization.PRVisualization = false
	got := Render(123, "structured PR briefing", "Issue body", "claude", "release/v1", noVisualization, false)
	if strings.Contains(got, "structure the PR body") || strings.Contains(got, "Diagram gate") {
		t.Fatal("PR visualization section present when PRVisualization=false")
	}

	noAutoPR := defaults
	noAutoPR.AutoPullRequest = false
	got = Render(123, "structured PR briefing", "Issue body", "codex", "release/v1", noAutoPR, false)
	if strings.Contains(got, "structure the PR body") || strings.Contains(got, "Diagram gate") {
		t.Fatal("PR visualization section present when AutoPullRequest=false")
	}
}

func TestPRVisualizationSectionQuotesBaseBranch(t *testing.T) {
	got := Render(123, "structured PR briefing", "Issue body", "codex", "foo;bar", settings.Defaults(), false)
	want := "git diff --name-only 'foo;bar'...HEAD"
	if !strings.Contains(got, want) {
		t.Fatalf("Render(...) missing shell-quoted base branch command %q", want)
	}
}

func TestCodexPlanModeUsesPlanningBriefing(t *testing.T) {
	got := Render(122, "Plan mode", "Issue body", "codex", "release/v1", settings.Defaults(), true)
	if !strings.Contains(got, "<proposed_plan>...</proposed_plan>") {
		t.Fatalf("plan briefing missing proposed_plan requirement:\n%s", got)
	}
	for _, unwanted := range []string{
		"commit and push",
		"Open a pull request",
		"structure the PR body",
		"Diagram gate",
		"codex review --uncommitted",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("plan briefing contains implementation guidance %q:\n%s", unwanted, got)
		}
	}
}
