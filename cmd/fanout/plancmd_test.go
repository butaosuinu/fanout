package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/planspec"
	"github.com/butaosuinu/fanout/internal/state"
)

func TestPlanTaskSlugQualifiesDefaultAndHonorsExplicit(t *testing.T) {
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "## Goal\nExtract it"}

	if got := planTaskSlug("launch-plan", task); got != "launch-plan-extract-api-client-api-client" {
		t.Fatalf("default slug = %q", got)
	}
	task.Slug = "shared-api-client"
	if got := planTaskSlug("launch-plan", task); got != "shared-api-client" {
		t.Fatalf("explicit slug = %q", got)
	}
}

func TestValidatePlanExecutionNamesRejectsFinalDuplicates(t *testing.T) {
	tests := []struct {
		name    string
		spec    planspec.Spec
		wantErr string
	}{
		{
			name: "final slug duplicate",
			spec: planspec.Spec{
				Plan: planspec.Plan{Slug: "launch-plan"},
				Tasks: []planspec.Task{
					{ID: "api-client", Title: "Extract API client"},
					{ID: "worker", Title: "Worker", Slug: "launch-plan-extract-api-client-api-client"},
				},
			},
			wantErr: "final slug",
		},
		{
			name: "final branch duplicate",
			spec: planspec.Spec{
				Plan: planspec.Plan{Slug: "launch-plan"},
				Tasks: []planspec.Task{
					{ID: "api-client", Title: "Extract API client"},
					{ID: "worker", Title: "Worker", Branch: "fanout/launch-plan-extract-api-client-api-client"},
				},
			},
			wantErr: "final branch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePlanExecutionNames(tc.spec, planCommandConfig{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validatePlanExecutionNames() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestResolvePlanBaseBranchValidatesSpecBranchOnlyWhenUsed(t *testing.T) {
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", BaseBranch: "release candidate"}}

	got, err := resolvePlanBaseBranch(planCommandConfig{BaseBranch: "main"}, spec, t.TempDir())
	if err != nil || got != "main" {
		t.Fatalf("resolvePlanBaseBranch() = %q, %v; want main, nil", got, err)
	}

	_, err = resolvePlanBaseBranch(planCommandConfig{}, spec, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "plan.base_branch must not contain whitespace") {
		t.Fatalf("resolvePlanBaseBranch() error = %v, want plan.base_branch whitespace error", err)
	}
}

func TestPlanRerunSpecArgUsesCopiedPlanSlugForLiveRuns(t *testing.T) {
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan"}}

	if got := planRerunSpecArg(planCommandConfig{DryRun: true, SpecArg: "/tmp/plan.json"}, spec); got != "/tmp/plan.json" {
		t.Fatalf("dry-run rerun spec arg = %q, want original path", got)
	}
	if got := planRerunSpecArg(planCommandConfig{SpecArg: "/tmp/plan.json"}, spec); got != "launch-plan" {
		t.Fatalf("live rerun spec arg = %q, want copied plan slug", got)
	}
}

func TestPlanStatusAllowsBranchPrefixForFallbackBranches(t *testing.T) {
	cfg := planCommandConfig{StatusMode: true, Format: "json", BranchPrefix: "custom/"}

	if code := validatePlanActionFlags(cfg, "", "", log.New(false)); code != exitcode.OK {
		t.Fatalf("validatePlanActionFlags() = %d, want %d", code, exitcode.OK)
	}

	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan"}}
	task := planspec.Task{ID: "ui-shell", Title: "Build UI shell"}
	if got := planTaskBranch(cfg, spec, task); got != "custom/launch-plan-build-ui-shell-ui-shell" {
		t.Fatalf("planTaskBranch() = %q, want custom prefix fallback branch", got)
	}
}

func TestSplitPlanBlockedKeepsSameRunDependenciesOpen(t *testing.T) {
	tasks := []planspec.Task{
		{ID: "base-types", Title: "Define base types"},
		{ID: "api-client", Title: "Extract API client", BlockedBy: []string{"base-types"}},
	}

	kept, blocked := splitPlanBlocked(tasks, tasks, func(planspec.Task) bool {
		return false
	})

	if len(kept) != 1 || kept[0].ID != "base-types" {
		t.Fatalf("kept = %+v, want only base-types", kept)
	}
	if len(blocked) != 1 || blocked[0].Task.ID != "api-client" || blocked[0].Refs != "base-types" {
		t.Fatalf("blocked = %+v, want api-client blocked by base-types", blocked)
	}
}

func TestSplitPlanBlockedAllowsCompletedTargetDependencies(t *testing.T) {
	tasks := []planspec.Task{
		{ID: "base-types", Title: "Define base types"},
		{ID: "api-client", Title: "Extract API client", BlockedBy: []string{"base-types"}},
	}

	kept, blocked := splitPlanBlocked(tasks[1:], tasks, func(task planspec.Task) bool {
		return task.ID == "base-types"
	})

	if len(blocked) != 0 {
		t.Fatalf("blocked = %+v, want no blocked rows", blocked)
	}
	if len(kept) != 1 || kept[0].ID != "api-client" {
		t.Fatalf("kept = %+v, want api-client", kept)
	}
}

func TestBuildTaskPlanSkipsCompletedTargetsBeforeBlockerCheck(t *testing.T) {
	spec := planspec.Spec{
		Plan: planspec.Plan{Slug: "launch-plan"},
		Tasks: []planspec.Task{
			{ID: "base-types", Title: "Define base types"},
			{ID: "api-client", Title: "Extract API client", BlockedBy: []string{"base-types"}},
		},
	}

	plan := buildTaskPlan(planCommandConfig{UnblockedOnly: true}, spec, nil, func(task planspec.Task) bool {
		return task.ID == "base-types"
	})

	if len(plan.AlreadyComplete) != 1 || plan.AlreadyComplete[0] != "base-types" {
		t.Fatalf("AlreadyComplete = %+v, want base-types", plan.AlreadyComplete)
	}
	if len(plan.BlockedRows) != 0 {
		t.Fatalf("BlockedRows = %+v, want none", plan.BlockedRows)
	}
	if len(plan.Targets) != 1 || plan.Targets[0].ID != "api-client" {
		t.Fatalf("Targets = %+v, want only api-client", plan.Targets)
	}
}

func TestBuildTaskPlanSkipsCompletedLeafTargets(t *testing.T) {
	spec := planspec.Spec{
		Plan: planspec.Plan{Slug: "launch-plan"},
		Tasks: []planspec.Task{
			{ID: "ui-shell", Title: "Build UI shell"},
		},
	}

	plan := buildTaskPlan(planCommandConfig{UnblockedOnly: true}, spec, nil, func(task planspec.Task) bool {
		return task.ID == "ui-shell"
	})

	if len(plan.AlreadyComplete) != 1 || plan.AlreadyComplete[0] != "ui-shell" {
		t.Fatalf("AlreadyComplete = %+v, want ui-shell", plan.AlreadyComplete)
	}
	if plan.UnfannedCount != 0 || len(plan.Targets) != 0 {
		t.Fatalf("targets = %+v (unfanned=%d), want none", plan.Targets, plan.UnfannedCount)
	}
}

func TestPlanTaskCompleteTreatsMissingEvidenceAsIncomplete(t *testing.T) {
	binDir := t.TempDir()
	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	projectRoot := t.TempDir()
	spec := planspec.Spec{
		Plan:  planspec.Plan{Slug: "launch-plan"},
		Tasks: []planspec.Task{{ID: "base-types", Title: "Define base types"}},
	}

	complete := planTaskComplete(
		ghissue.Runner{Cwd: projectRoot},
		&cliflags.Config{},
		projectRoot,
		state.Store{},
		spec,
		spec.Tasks[0],
		log.New(false),
	)

	if complete {
		t.Fatal("planTaskComplete() = true, want false without merged PR, state row, or worktree")
	}
}

func TestPlanTaskCompleteTreatsNonMergedPRAsIncomplete(t *testing.T) {
	binDir := t.TempDir()
	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte(`#!/bin/sh
cat <<'JSON'
[{"number":42,"state":"OPEN","mergedAt":null,"isDraft":false,"reviewDecision":"","statusCheckRollup":null}]
JSON
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	projectRoot := t.TempDir()
	spec := planspec.Spec{
		Plan:  planspec.Plan{Slug: "launch-plan"},
		Tasks: []planspec.Task{{ID: "base-types", Title: "Define base types"}},
	}

	complete := planTaskComplete(
		ghissue.Runner{Cwd: projectRoot},
		&cliflags.Config{},
		projectRoot,
		state.Store{},
		spec,
		spec.Tasks[0],
		log.New(false),
	)

	if complete {
		t.Fatal("planTaskComplete() = true, want false when branch has a non-merged PR")
	}
}
