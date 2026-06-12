package briefing

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/settings"
)

func TestPRVisualizationSectionHonorsSettings(t *testing.T) {
	defaults := settings.Defaults()
	for _, agent := range []string{"claude", "codex"} {
		got := Render(123, "structured PR briefing", "Issue body", agent, "release/v1", defaults, false, nil)
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
	got := Render(123, "structured PR briefing", "Issue body", "claude", "release/v1", noVisualization, false, nil)
	if strings.Contains(got, "structure the PR body") || strings.Contains(got, "Diagram gate") {
		t.Fatal("PR visualization section present when PRVisualization=false")
	}

	noAutoPR := defaults
	noAutoPR.AutoPullRequest = false
	got = Render(123, "structured PR briefing", "Issue body", "codex", "release/v1", noAutoPR, false, nil)
	if strings.Contains(got, "structure the PR body") || strings.Contains(got, "Diagram gate") {
		t.Fatal("PR visualization section present when AutoPullRequest=false")
	}
}

func TestPRVisualizationSectionQuotesBaseBranch(t *testing.T) {
	got := Render(123, "structured PR briefing", "Issue body", "codex", "foo;bar", settings.Defaults(), false, nil)
	want := "git diff --name-only 'foo;bar'...HEAD"
	if !strings.Contains(got, want) {
		t.Fatalf("Render(...) missing shell-quoted base branch command %q", want)
	}
}

func testTeamContext() *TeamContext {
	return &TeamContext{
		ParentLabel: "#100",
		DBPath:      "/tmp/fanout-project_root-100.db",
		Siblings: []TeamSibling{
			{Num: 101, Title: "First child"},
			{Num: 102, Title: "Second child"},
		},
	}
}

func TestTeamSectionAbsentWithoutTeamContext(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		got := Render(101, "First child", "Issue body", agent, "main", settings.Defaults(), false, nil)
		if strings.Contains(got, "Coordinating with your sibling panes") {
			t.Fatalf("Render(..., %q, team=nil) contains the team section", agent)
		}
	}
}

func TestTeamSectionRendersForBothAgents(t *testing.T) {
	team := testTeamContext()
	// The section sits in the agent-shared base string, so the full content
	// is asserted once (claude) and codex only needs the section to appear.
	got := Render(101, "First child", "Issue body", "claude", "main", settings.Defaults(), false, team)
	for _, want := range []string{
		"## Coordinating with your sibling panes",
		"You are the pane for issue #101 (parent #100)",
		"- #101: First child (you)",
		"- #102: Second child",
		"/tmp/fanout-project_root-100.db",
		"fanout msg peers",
		"fanout msg send --to <N>",
		"Agent Teams, which coordinates teammates inside your own single session",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Render(..., team) missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- #102: Second child (you)") {
		t.Fatal("Render(..., team) marks a non-self sibling as (you)")
	}

	codex := Render(101, "First child", "Issue body", "codex", "main", settings.Defaults(), false, team)
	if !strings.Contains(codex, "## Coordinating with your sibling panes") {
		t.Fatalf("Render(..., \"codex\", team) missing the team section:\n%s", codex)
	}
}

func TestTeamSectionAbsentInCodexPlanBriefing(t *testing.T) {
	got := Render(101, "First child", "Issue body", "codex", "main", settings.Defaults(), true, testTeamContext())
	if strings.Contains(got, "Coordinating with your sibling panes") {
		t.Fatalf("codex plan briefing contains the team section:\n%s", got)
	}
}

func TestCodexPlanModeUsesPlanningBriefing(t *testing.T) {
	got := Render(122, "Plan mode", "Issue body", "codex", "release/v1", settings.Defaults(), true, nil)
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
