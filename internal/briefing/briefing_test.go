package briefing

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/settings"
)

func TestTaskPathUsesPlanTaskNamespace(t *testing.T) {
	root := "/repos/project_root"
	got := TaskPath(root, "plan-alpha", "task-001")
	want := "/tmp/fanout-project_root-plan%2Dalpha-task%2D001.md"
	if got != want {
		t.Fatalf("TaskPath() = %q, want %q", got, want)
	}
	if got == TaskPath(root, "plan", "alpha-task-001") {
		t.Fatalf("TaskPath() collides across plan/task boundary: %q", got)
	}
	if got == Path(root, 214) {
		t.Fatalf("TaskPath() collides with issue Path(): %q", got)
	}
}

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

func TestRenderTaskDefaultsUsePlanTaskFooterAndSharedAgentSections(t *testing.T) {
	defaults := settings.Defaults()
	for _, tc := range []struct {
		agent string
		wants []string
	}{
		{
			agent: "claude",
			wants: []string{
				"run the `/code-review` slash command",
				"Optional: Agent Teams",
			},
		},
		{
			agent: "codex",
			wants: []string{
				"codex review --uncommitted",
				"Only after the review loop is clean should you commit, push, and open the PR",
			},
		},
	} {
		got := RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", tc.agent, "release/v1", defaults)
		commonWants := []string{
			`You are assigned task "task-001" of plan "Launch Plan" (plan:plan-alpha) in this repository.`,
			"Title: Task title",
			"Task body",
			"Make focused, minimal changes scoped to this single task",
			`Open a pull request and end the PR body with "Plan: plan-alpha / Task: task-001"`,
			"do not add an issue-closing footer",
			"stop and report the ambiguity in this pane",
			"structure the PR body",
			"`Plan: plan-alpha / Task: task-001`",
			"git diff --name-only release/v1...HEAD",
			"Diagram gate",
		}
		for _, want := range append(commonWants, tc.wants...) {
			if !strings.Contains(got, want) {
				t.Fatalf("RenderTask(..., %q) missing %q:\n%s", tc.agent, want, got)
			}
		}
		assertIssueLessTaskBriefing(t, got)
	}
}

func TestRenderTaskSettingsToggleCombinations(t *testing.T) {
	defaults := settings.Defaults()

	noAutoPR := defaults
	noAutoPR.AutoPullRequest = false
	got := RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", "codex", "release/v1", noAutoPR)
	for _, unwanted := range []string{
		"Open a pull request",
		"structure the PR body",
		"Diagram gate",
		"open the PR",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("RenderTask(..., AutoPullRequest=false) contains %q:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "Only after the review loop is clean should you commit and push the branch") {
		t.Fatalf("RenderTask(..., AutoPullRequest=false) missing no-PR codex review gate:\n%s", got)
	}
	assertIssueLessTaskBriefing(t, got)

	noVisualization := defaults
	noVisualization.PRVisualization = false
	got = RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", "claude", "release/v1", noVisualization)
	if !strings.Contains(got, `Open a pull request and end the PR body with "Plan: plan-alpha / Task: task-001"`) {
		t.Fatalf("RenderTask(..., PRVisualization=false) missing auto-PR task requirement:\n%s", got)
	}
	for _, unwanted := range []string{"structure the PR body", "Diagram gate"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("RenderTask(..., PRVisualization=false) contains %q:\n%s", unwanted, got)
		}
	}
	assertIssueLessTaskBriefing(t, got)

	claudeToggles := defaults
	claudeToggles.PRReviewGate = false
	claudeToggles.BriefingCodeReview = false
	claudeToggles.AgentTeamsHint = false
	got = RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", "claude", "release/v1", claudeToggles)
	if !strings.Contains(got, "The PR review gate is disabled for this fanout run") {
		t.Fatalf("RenderTask(..., PRReviewGate=false) missing bypass notice:\n%s", got)
	}
	for _, unwanted := range []string{
		"run the `/code-review` slash command",
		"Optional: Agent Teams",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("RenderTask(..., disabled claude toggles) contains %q:\n%s", unwanted, got)
		}
	}
	assertIssueLessTaskBriefing(t, got)
}

func TestRenderTaskPRVisualizationQuotesBaseBranch(t *testing.T) {
	got := RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", "codex", "foo;bar", settings.Defaults())
	want := "git diff --name-only 'foo;bar'...HEAD"
	if !strings.Contains(got, want) {
		t.Fatalf("RenderTask(...) missing shell-quoted base branch command %q:\n%s", want, got)
	}
	assertIssueLessTaskBriefing(t, got)
}

func assertIssueLessTaskBriefing(t *testing.T, got string) {
	t.Helper()
	for _, unwanted := range []string{"Closes #", "GitHub issue #"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("issue-less task briefing contains %q:\n%s", unwanted, got)
		}
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
