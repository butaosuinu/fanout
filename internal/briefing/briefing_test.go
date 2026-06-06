package briefing

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/settings"
)

func TestPRVisualizationSectionHonorsSettings(t *testing.T) {
	defaults := settings.Defaults()
	for _, agent := range []string{"claude", "codex"} {
		got := Render(123, "structured PR briefing", "Issue body", agent, "release/v1", defaults)
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
	got := Render(123, "structured PR briefing", "Issue body", "claude", "release/v1", noVisualization)
	if strings.Contains(got, "structure the PR body") || strings.Contains(got, "Diagram gate") {
		t.Fatal("PR visualization section present when PRVisualization=false")
	}

	noAutoPR := defaults
	noAutoPR.AutoPullRequest = false
	got = Render(123, "structured PR briefing", "Issue body", "codex", "release/v1", noAutoPR)
	if strings.Contains(got, "structure the PR body") || strings.Contains(got, "Diagram gate") {
		t.Fatal("PR visualization section present when AutoPullRequest=false")
	}
}

func TestPRVisualizationSectionQuotesBaseBranch(t *testing.T) {
	got := Render(123, "structured PR briefing", "Issue body", "codex", "foo;bar", settings.Defaults())
	want := "git diff --name-only 'foo;bar'...HEAD"
	if !strings.Contains(got, want) {
		t.Fatalf("Render(...) missing shell-quoted base branch command %q", want)
	}
}
