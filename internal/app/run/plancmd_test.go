package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

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
			err := ValidatePlanExecutionNames(tc.spec, PlanCommandConfig{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidatePlanExecutionNames() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestResolvePlanBaseBranchValidatesSpecBranchOnlyWhenUsed(t *testing.T) {
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", BaseBranch: "release candidate"}}

	got, err := resolvePlanBaseBranch(PlanCommandConfig{BaseBranch: "main"}, spec, t.TempDir())
	if err != nil || got != "main" {
		t.Fatalf("resolvePlanBaseBranch() = %q, %v; want main, nil", got, err)
	}

	_, err = resolvePlanBaseBranch(PlanCommandConfig{}, spec, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "plan.base_branch must not contain whitespace") {
		t.Fatalf("resolvePlanBaseBranch() error = %v, want plan.base_branch whitespace error", err)
	}
}

func TestResolvePlanBaseBranchUsesCurrentBranchWithoutOrigin(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "trunk")
	gitCmdTest(t, repo, "config", "user.email", "fanout@example.test")
	gitCmdTest(t, repo, "config", "user.name", "Fanout Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, repo, "add", "README.md")
	gitCmdTest(t, repo, "commit", "-m", "base")

	got, err := resolvePlanBaseBranch(PlanCommandConfig{}, planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan"}}, repo)
	if err != nil {
		t.Fatalf("resolvePlanBaseBranch() error = %v", err)
	}
	if got != "trunk" {
		t.Fatalf("resolvePlanBaseBranch() = %q, want current branch trunk", got)
	}
}

func TestPlanRerunSpecArgUsesCopiedPlanSlugForLiveRuns(t *testing.T) {
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan"}}

	if got := planRerunSpecArg(PlanCommandConfig{DryRun: true, SpecArg: "/tmp/plan.json"}, spec); got != "/tmp/plan.json" {
		t.Fatalf("dry-run rerun spec arg = %q, want original path", got)
	}
	if got := planRerunSpecArg(PlanCommandConfig{SpecArg: "/tmp/plan.json"}, spec); got != "launch-plan" {
		t.Fatalf("live rerun spec arg = %q, want copied plan slug", got)
	}
}

func TestPreparedPlanSpecSnapshotOwnsExecutionAndCopiedBytes(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "incoming.json")
	original := []byte(`{"version":1,"plan":{"slug":"launch-plan","title":"Original"},"tasks":[{"id":"base","title":"Base","briefing":"Build it"}]}`)
	if err := os.WriteFile(sourcePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := planspec.LoadWithoutResolvedNameChecksSnapshot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := PlanCommandConfig{SpecArg: sourcePath, SpecSnapshot: &snapshot}

	replacement := []byte(`{"version":1,"plan":{"slug":"changed-plan","title":"Changed"},"tasks":[{"id":"other","title":"Other","briefing":"Replace it"}]}`)
	err = os.WriteFile(sourcePath, replacement, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := resolvePlanSpecSnapshot(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Spec().Plan.Slug; got != "launch-plan" {
		t.Fatalf("resolved plan slug = %q, want launch-plan", got)
	}
	cfg.SpecSnapshot = &resolved
	if got := cfg.CLIConfig().PlanSpecIdentity; got != snapshot.Identity() {
		t.Fatalf("CLI plan identity = %q, want %q", got, snapshot.Identity())
	}
	target, err := preparePlanSpecCopy(resolved, repo, resolved.Spec().Plan.Slug)
	if err != nil {
		t.Fatal(err)
	}
	err = copyPlanSpec(resolved.Bytes(), target)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(repo, ".fanout", "plans", "launch-plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != string(original) {
		t.Fatalf("copied plan bytes = %q, want captured bytes %q", copied, original)
	}
}

func TestCopyPlanSpecPreservesMatchingDestinationMode(t *testing.T) {
	repo := t.TempDir()
	data := []byte(`{"version":1,"plan":{"slug":"launch-plan","title":"Plan"},"tasks":[{"id":"base","title":"Base","briefing":"Build it"}]}`)
	dst := filepath.Join(repo, ".fanout", "plans", "launch-plan.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}

	snapshotPath := filepath.Join(repo, "incoming.json")
	if err := os.WriteFile(snapshotPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := planspec.LoadWithoutResolvedNameChecksSnapshot(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := preparePlanSpecCopy(snapshot, repo, "launch-plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyPlanSpec(data, target); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("matching destination mode = %o, want preserved 600", info.Mode().Perm())
	}
}

func TestCopyPlanSpecUpdatesCapturedDestinationAndPreservesMode(t *testing.T) {
	repo := t.TempDir()
	dst := filepath.Join(repo, ".fanout", "plans", "launch-plan.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("previous saved plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := loadPlanCopySnapshot(t, repo, []byte(
		`{"version":1,"plan":{"slug":"launch-plan","title":"Plan"},"tasks":[{"id":"base","title":"Base","briefing":"Build it"}]}`,
	))
	target, err := preparePlanSpecCopy(snapshot, repo, "launch-plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyPlanSpec(snapshot.Bytes(), target); err != nil {
		t.Fatal(err)
	}
	data, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(snapshot.Bytes()) {
		t.Fatalf("updated destination bytes = %q, want snapshot bytes", data)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("updated destination mode = %o, want preserved 600", info.Mode().Perm())
	}
}

func TestCopyPlanSpecRejectsDestinationChangedAfterPreflight(t *testing.T) {
	repo := t.TempDir()
	dst := filepath.Join(repo, ".fanout", "plans", "launch-plan.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("captured preimage"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := loadPlanCopySnapshot(t, repo, []byte(
		`{"version":1,"plan":{"slug":"launch-plan","title":"Plan"},"tasks":[{"id":"base","title":"Base","briefing":"Build it"}]}`,
	))
	target, err := preparePlanSpecCopy(snapshot, repo, "launch-plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("concurrent user bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = copyPlanSpec(snapshot.Bytes(), target)
	if err == nil || !strings.Contains(err.Error(), "changed after preflight") {
		t.Fatalf("copyPlanSpec() error = %v, want concurrent-change rejection", err)
	}
	data, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "concurrent user bytes" {
		t.Fatalf("changed destination bytes = %q, want preserved concurrent bytes", data)
	}
}

func TestPreparePlanSpecCopyRejectsSavedSourceChangedAfterSnapshot(t *testing.T) {
	repo := t.TempDir()
	dst := filepath.Join(repo, ".fanout", "plans", "launch-plan.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(
		`{"version":1,"plan":{"slug":"launch-plan","title":"Original"},"tasks":[{"id":"base","title":"Base","briefing":"Build it"}]}`,
	)
	if err := os.WriteFile(dst, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := planspec.LoadWithoutResolvedNameChecksSnapshot(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(
		`{"version":1,"plan":{"slug":"launch-plan","title":"Changed"},"tasks":[{"id":"base","title":"Base","briefing":"Build it"}]}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = preparePlanSpecCopy(snapshot, repo, "launch-plan")
	if err == nil || !strings.Contains(err.Error(), "changed after snapshot") {
		t.Fatalf("preparePlanSpecCopy() error = %v, want stale-source rejection", err)
	}
}

func loadPlanCopySnapshot(t *testing.T, repo string, data []byte) planspec.Snapshot {
	t.Helper()
	path := filepath.Join(repo, "incoming.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := planspec.LoadWithoutResolvedNameChecksSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPreparedPlanSpecSnapshotRejectsDifferentResolvedPath(t *testing.T) {
	repo := t.TempDir()
	firstPath := filepath.Join(repo, "first.json")
	if err := os.WriteFile(firstPath, []byte(`{"version":1,"plan":{"slug":"launch-plan","title":"Plan"},"tasks":[{"id":"base","title":"Base","briefing":"Build it"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := planspec.LoadWithoutResolvedNameChecksSnapshot(firstPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = resolvePlanSpecSnapshot(PlanCommandConfig{
		SpecArg:      filepath.Join(repo, "second.json"),
		SpecSnapshot: &snapshot,
	}, repo)
	if err == nil || !strings.Contains(err.Error(), "does not match resolved path") {
		t.Fatalf("resolvePlanSpecSnapshot() error = %v, want path mismatch", err)
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

	plan := buildTaskPlan(PlanCommandConfig{UnblockedOnly: true}, spec, nil, func(task planspec.Task) bool {
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

	plan := buildTaskPlan(PlanCommandConfig{UnblockedOnly: true}, spec, nil, func(task planspec.Task) bool {
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
