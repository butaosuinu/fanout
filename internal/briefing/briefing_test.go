package briefing

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/settings"
)

func TestPRVisualizationSectionHonorsSettings(t *testing.T) {
	defaults := settings.Defaults()
	for _, agent := range []string{"claude", "codex"} {
		got := Render(123, "structured PR briefing", "Issue body", agent, defaults)
		if !strings.Contains(got, "structure the PR body") {
			t.Fatalf("Render(..., %q) is missing structured PR body guidance", agent)
		}
		if !strings.Contains(got, "Diagram gate") {
			t.Fatalf("Render(..., %q) is missing Diagram gate guidance", agent)
		}
	}

	noVisualization := defaults
	noVisualization.PRVisualization = false
	got := Render(123, "structured PR briefing", "Issue body", "claude", noVisualization)
	if strings.Contains(got, "structure the PR body") || strings.Contains(got, "Diagram gate") {
		t.Fatal("PR visualization section present when PRVisualization=false")
	}

	noAutoPR := defaults
	noAutoPR.AutoPullRequest = false
	got = Render(123, "structured PR briefing", "Issue body", "codex", noAutoPR)
	if strings.Contains(got, "structure the PR body") || strings.Contains(got, "Diagram gate") {
		t.Fatal("PR visualization section present when AutoPullRequest=false")
	}
}
