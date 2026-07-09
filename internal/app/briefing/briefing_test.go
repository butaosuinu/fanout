package briefing

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/settings"
)

func TestTaskPathUsesPlanTaskNamespace(t *testing.T) {
	root := "/repos/project_root"
	got := TaskPath(root, "plan-alpha", "task-001")
	want := filepath.Join(Dir(root), "fanout-project_root-plan%2Dalpha-task%2D001.md")
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
	if !strings.Contains(got, "POST_WORK_REVIEW_BASE=release/v1` to") {
		t.Fatalf("Render(..., codex, AutoPullRequest=false) missing post-work-review base branch:\n%s", got)
	}
}

func TestPRVisualizationSectionQuotesBaseBranch(t *testing.T) {
	got := Render(123, "structured PR briefing", "Issue body", "codex", "foo;bar", settings.Defaults(), false, nil)
	want := "git diff --name-only 'foo;bar'...HEAD"
	if !strings.Contains(got, want) {
		t.Fatalf("Render(...) missing shell-quoted base branch command %q", want)
	}
	want = "POST_WORK_REVIEW_BASE='foo;bar'` to"
	if !strings.Contains(got, want) {
		t.Fatalf("Render(...) missing shell-quoted post-work-review base branch command %q", want)
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
				"After focused checks pass, follow the final validation, commit, and push instructions below",
				"run the `/code-review` slash command",
				"`/post-work-review` once on the committed branch",
				"canonical full project validation for that exact HEAD",
				"`.git/post-work-review-passed`",
				"Optional: Agent Teams",
			},
		},
		{
			agent: "codex",
			wants: []string{
				"After focused checks pass, follow the review, commit, and push sequence below",
				"$post-work-review",
				"Commit the candidate changes before the final branch-scope review",
				"POST_WORK_REVIEW_BASE=release/v1` to every driver command",
				"canonical full project validation for that exact HEAD",
				"`.git/post-work-review-passed` for the",
				"exact HEAD you will push",
				"Push and open the PR only after the branch review is clean and marked",
			},
		},
	} {
		got := RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", tc.agent, "release/v1", defaults, nil)
		commonWants := []string{
			`You are assigned task "task-001" of plan "Launch Plan" (plan:plan-alpha) in this repository.`,
			"Title: Task title",
			"Task body",
			"Make focused, minimal changes scoped to this single task",
			"During implementation, run focused lint/test commands",
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
		for _, unwanted := range []string{"Fix actionable findings and rerun it", "post-work-reviewer", "post-work-verifier"} {
			if strings.Contains(got, unwanted) {
				t.Fatalf("RenderTask(..., %q) contains redundant review detail %q:\n%s", tc.agent, unwanted, got)
			}
		}
		assertIssueLessTaskBriefing(t, got)
	}
}

func TestRenderTaskSettingsToggleCombinations(t *testing.T) {
	defaults := settings.Defaults()

	noAutoPR := defaults
	noAutoPR.AutoPullRequest = false
	got := RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", "codex", "release/v1", noAutoPR, nil)
	for _, unwanted := range []string{
		"Open a pull request",
		"structure the PR body",
		"Diagram gate",
		"open the PR",
		"When implementation passes tests, commit and push the branch",
		"complete any review steps below before the final commit and push",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("RenderTask(..., AutoPullRequest=false) contains %q:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "Only after the committed branch review is clean and marked should you push the") {
		t.Fatalf("RenderTask(..., AutoPullRequest=false) missing no-PR post-work-review gate:\n%s", got)
	}
	if !strings.Contains(got, "clean=true`, `findings=0`, and an empty `stop_reason=") {
		t.Fatalf("RenderTask(..., AutoPullRequest=false) missing bounded clean condition:\n%s", got)
	}
	if !strings.Contains(got, "POST_WORK_REVIEW_BASE=release/v1` to every driver command") {
		t.Fatalf("RenderTask(..., AutoPullRequest=false) missing post-work-review base branch:\n%s", got)
	}
	if strings.Contains(got, "on your current diff") ||
		strings.Contains(got, "Run it on the current diff") ||
		strings.Contains(got, "Run `$post-work-review` again") ||
		strings.Contains(got, "post-work-reviewer") ||
		strings.Contains(got, "post-work-verifier") {
		t.Fatalf("RenderTask(..., AutoPullRequest=false) contains redundant pre-commit review guidance:\n%s", got)
	}
	assertIssueLessTaskBriefing(t, got)

	noVisualization := defaults
	noVisualization.PRVisualization = false
	got = RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", "claude", "release/v1", noVisualization, nil)
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
	got = RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", "claude", "release/v1", claudeToggles, nil)
	if !strings.Contains(got, "The PR review gate is disabled for this fanout run") {
		t.Fatalf("RenderTask(..., PRReviewGate=false) missing bypass notice:\n%s", got)
	}
	if !strings.Contains(got, "canonical full validation") ||
		!strings.Contains(got, "prefer `make check` when the") ||
		!strings.Contains(got, "once on the exact HEAD") {
		t.Fatalf("RenderTask(..., PRReviewGate=false) missing final full validation:\n%s", got)
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
	got := RenderTask("plan-alpha", "Launch Plan", "task-001", "Task title", "Task body", "codex", "foo;bar", settings.Defaults(), nil)
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

func TestRenderManualPlanUsesManualPlanBriefing(t *testing.T) {
	got := RenderManualPlan("Manual prompt", "Manual prompt\nMore context")
	for _, want := range []string{
		"manual fanout Codex Plan Mode session",
		"Title: Manual prompt",
		"Body:\nManual prompt\nMore context",
		"Before presenting a plan, follow normal Codex planning behavior",
		"use web/search when the task calls for current external information",
		"After that investigation, present the implementation plan",
		"<proposed_plan>...</proposed_plan>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderManualPlan missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"You are assigned GitHub issue",
		"commit and push",
		"Open a pull request",
		"$post-work-review",
		"codex review --uncommitted",
		"Your first response must",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("RenderManualPlan contains %q:\n%s", unwanted, got)
		}
	}
}

func testTaskTeamContext() *TeamContext {
	return &TeamContext{
		ParentLabel: "plan:launch-plan",
		DBPath:      "/tmp/fanout-project_root-plan-launch-plan.db",
		Siblings: []TeamSibling{
			{TaskID: "base-types", Title: "Define base types"},
			{TaskID: "api-client", Title: "Extract API client"},
		},
	}
}

func TestTaskTeamSectionAddressesSiblingsByTaskID(t *testing.T) {
	team := testTaskTeamContext()
	got := RenderTask("launch-plan", "Launch plan", "base-types", "Define base types", "Task body", "claude", "main", settings.Defaults(), team)
	for _, want := range []string{
		"## Coordinating with your sibling panes",
		"You are the pane for task base-types (parent plan:launch-plan)",
		"- base-types: Define base types (you)",
		"- api-client: Extract API client",
		"/tmp/fanout-project_root-plan-launch-plan.db",
		"fanout msg send --to <task-id>",
		"Peers are addressed by task id",
		"Agent Teams, which coordinates teammates inside your own single session",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderTask(..., team) missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- api-client: Extract API client (you)") {
		t.Fatal("RenderTask(..., team) marks a non-self sibling as (you)")
	}
	// The plan variant must not leak the issue-numbered cheatsheet.
	if strings.Contains(got, "fanout msg send --to <N>") {
		t.Fatalf("RenderTask(..., team) used the issue cheatsheet:\n%s", got)
	}
	// Issue-closing references stay absent in the task briefing.
	assertIssueLessTaskBriefing(t, got)
}

func TestTaskTeamSectionAbsentWithoutTeamContext(t *testing.T) {
	got := RenderTask("launch-plan", "Launch plan", "base-types", "Define base types", "Task body", "claude", "main", settings.Defaults(), nil)
	if strings.Contains(got, "Coordinating with your sibling panes") {
		t.Fatalf("RenderTask(..., team=nil) contains the team section:\n%s", got)
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
		"$post-work-review",
		"codex review --uncommitted",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("plan briefing contains implementation guidance %q:\n%s", unwanted, got)
		}
	}
}
