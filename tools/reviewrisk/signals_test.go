package main

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

// reasonSignals returns the signal ids present in a report, for presence checks.
func reasonSignals(r Report) []string {
	out := make([]string, 0, len(r.Reasons))
	for _, rs := range r.Reasons {
		out = append(out, rs.Signal)
	}
	return out
}

// TestEvaluate drives the whole aggregation: the base level from file classes,
// each escalation signal S1-S11, and the interactions the plan pins (FANOUT_
// removed-only, large-diff caps at the low/medium base, NONE lines excluded).
func TestEvaluate(t *testing.T) {
	tests := []struct {
		name      string
		diff      Diff
		wantLevel Level
		wantSig   []string // signals that must be present
		notSig    []string // signals that must be absent
	}{
		{
			name:      "empty diff is none",
			diff:      Diff{},
			wantLevel: LevelNone,
		},
		{
			name:      "class A file only is low",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "internal/infra/log/log.go"}}},
			wantLevel: LevelLow,
		},
		{
			name:      "class M file is medium",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "internal/app/run/run.go"}}},
			wantLevel: LevelMedium,
		},
		{
			name:      "class H file is high",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "internal/infra/state/store.go"}}},
			wantLevel: LevelHigh,
		},
		{
			name:      "S1 deleted test file is critical",
			diff:      Diff{Files: []FileChange{{Status: 'D', Path: "internal/core/naming/slug_test.go"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigTestDeleted},
		},
		{
			name:      "S1 rename dropping test shape is critical",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: "internal/core/naming/foo_test.go", Path: "internal/core/naming/foo.go"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigTestDeleted},
		},
		{
			// A web test renamed to a suffix vitest no longer collects
			// (foo.test.ts -> foo.test.disabled.ts) drops test shape: the
			// suffix check, not a substring, is what catches this.
			name:      "S1 web rename to an uncollected suffix drops test shape",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: "web/src/lib/foo.test.ts", Path: "web/src/lib/foo.test.disabled.ts"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigTestDeleted},
		},
		{
			name:      "rename keeping test shape does not fire S1",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: "internal/core/naming/a_test.go", Path: "internal/core/naming/b_test.go"}}},
			wantLevel: LevelMedium, // still classified under core/naming (M)
			notSig:    []string{sigTestDeleted},
		},
		{
			name:      "rename from an H package into docs is judged by the heavier old class",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: "internal/infra/settings/resolve.go", Path: "docs/resolve-notes.md"}}},
			wantLevel: LevelHigh, // old path is H (settings), new path is docs (NONE)
			notSig:    []string{sigTestDeleted, sigMeasureDeleted},
		},
		{
			name:      "rename onto an unclassified path stays fail-closed at S9",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: "README.md", Path: "some-new-top-level-file"}}},
			wantLevel: LevelHigh, // NONE old class must not mask the unclassified destination
			wantSig:   []string{sigUnclassifiedPath},
		},
		{
			name:      "S2 deleted golden is critical",
			diff:      Diff{Files: []FileChange{{Status: 'D', Path: "tests/golden/scenario-plan.txt"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigMeasureDeleted},
		},
		{
			name:      "S2 rename out of tests/golden is critical",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: "tests/golden/scenario-plan.txt", Path: "docs/scenario-plan.txt"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigMeasureDeleted},
		},
		{
			name: "S3 added t.Skip in a test file is critical",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "internal/core/naming/foo_test.go"}},
				AddedLines: map[string][]string{"internal/core/naming/foo_test.go": {"\tt.Skip(\"wip\")"}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigSkipAdded},
		},
		{
			// Skip receiver is not fixed to t: a testing.TB helper (tb) skips too.
			name: "S3 added tb.Skip under a non-t receiver is critical",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "internal/core/naming/foo_test.go"}},
				AddedLines: map[string][]string{"internal/core/naming/foo_test.go": {"\ttb.Skip(\"wip\")"}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigSkipAdded},
		},
		{
			// A *testing.B benchmark skips via b.Skip; the receiver name still varies.
			name: "S3 added b.Skip in a benchmark is critical",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "internal/core/naming/foo_test.go"}},
				AddedLines: map[string][]string{"internal/core/naming/foo_test.go": {"\tb.Skip(\"bench\")"}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigSkipAdded},
		},
		{
			name: "S3 does not fire on t.Skipped() (a status read, not a skip call)",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "internal/core/naming/foo_test.go"}},
				AddedLines: map[string][]string{"internal/core/naming/foo_test.go": {"\tif t.Skipped() {"}},
			},
			wantLevel: LevelMedium, // core/naming (M), no S3
			notSig:    []string{sigSkipAdded},
		},
		{
			name: "S3 added skip in bats helpers.bash is critical",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "tests/bats/helpers.bash"}},
				AddedLines: map[string][]string{"tests/bats/helpers.bash": {"  skip \"needs docker\""}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigSkipAdded},
		},
		{
			name: "S3 added describe.skip in the web test harness is critical",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "web/src/test/setup.ts"}},
				AddedLines: map[string][]string{"web/src/test/setup.ts": {"describe.skip('flaky suite', () => {"}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigSkipAdded},
		},
		{
			name: "S3 added vitest skipIf variant is critical",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "web/src/lib/sort.test.ts"}},
				AddedLines: map[string][]string{"web/src/lib/sort.test.ts": {"test.skipIf(isCI)('sorts', () => {})"}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigSkipAdded},
		},
		{
			// vitest collects .spec files too, so a skip added in one fires S3.
			name: "S3 added test.skip in a .spec.tsx file is critical",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "web/src/components/foo.spec.tsx"}},
				AddedLines: map[string][]string{"web/src/components/foo.spec.tsx": {"  test.skip('renders', () => {})"}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigSkipAdded},
		},
		{
			name: "S5 post-work-review gate script change is critical",
			diff: Diff{
				Files: []FileChange{{Status: 'M', Path: "codex/tools/post-work-review.sh"}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigReviewGateChanged},
		},
		{
			name:      "S4 arch change is critical",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "internal/arch/arch.go"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigGuardModified},
		},
		{
			name:      "S5 .claude change is critical",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: ".claude/settings.json"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigReviewGateChanged},
		},
		{
			name:      "S6 risk tool change is critical",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "tools/reviewrisk/rules.go"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigRiskToolModified},
		},
		{
			name:      "S6 risk workflow change is critical",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: ".github/workflows/review-risk.yml"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigRiskToolModified},
		},
		{
			name:      "S7 installer change is critical",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "install.sh"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigInstallerModified},
		},
		{
			name:      "S8 deleted workflow is critical",
			diff:      Diff{Files: []FileChange{{Status: 'D', Path: ".github/workflows/test.yml"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigCIWorkflowDeleted},
		},
		{
			name:      "S8 rename out of workflows is critical",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: ".github/workflows/test.yml", Path: "hack/test.yml"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigCIWorkflowDeleted},
		},
		{
			// GitHub Actions only runs .yml/.yaml under workflows/, so an in-place
			// rename to another extension disables the workflow.
			name:      "S8 in-place rename dropping the yml extension is critical",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: ".github/workflows/test.yml", Path: ".github/workflows/test.yml.disabled"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigCIWorkflowDeleted},
		},
		{
			// Actions ignores subdirectories of workflows/, so this move also
			// disables the workflow.
			name:      "S8 rename into a workflows subdirectory is critical",
			diff:      Diff{Files: []FileChange{{Status: 'R', OldPath: ".github/workflows/test.yml", Path: ".github/workflows/disabled/test.yml"}}},
			wantLevel: LevelCritical,
			wantSig:   []string{sigCIWorkflowDeleted},
		},
		{
			name: "S3 conditional bats skip after && is critical",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "tests/bats/tier1_flags.bats"}},
				AddedLines: map[string][]string{"tests/bats/tier1_flags.bats": {"  [[ $CI == true ]] && skip \"flaky\""}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigSkipAdded},
		},
		{
			name: "S3 does not fire on a bats comment mentioning skip",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "tests/bats/tier1_flags.bats"}},
				AddedLines: map[string][]string{"tests/bats/tier1_flags.bats": {"# do not skip this scenario"}},
			},
			wantLevel: LevelMedium, // tests/bats (M), no S3
			notSig:    []string{sigSkipAdded},
		},
		{
			name: "S5 post-work-review agent definition change is critical",
			diff: Diff{
				Files: []FileChange{{Status: 'M', Path: "codex/agents/post-work-reviewer.md"}},
			},
			wantLevel: LevelCritical,
			wantSig:   []string{sigReviewGateChanged},
		},
		{
			name:      "S9 unclassified dashboard file is high",
			diff:      Diff{Files: []FileChange{{Status: 'A', Path: "internal/ui/dashboard/newthing.go"}}},
			wantLevel: LevelHigh,
			wantSig:   []string{sigUnclassifiedPath},
		},
		{
			name: "S10 invariant added line bumps to high",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "internal/infra/log/log.go"}},
				AddedLines: map[string][]string{"internal/infra/log/log.go": {"if requireToken(r) {"}},
			},
			wantLevel: LevelHigh,
			wantSig:   []string{sigInvariantHit},
		},
		{
			name: "S10 FANOUT_ removed line bumps to high",
			diff: Diff{
				Files:        []FileChange{{Status: 'M', Path: "internal/infra/log/log.go"}},
				RemovedLines: map[string][]string{"internal/infra/log/log.go": {"os.Getenv(\"FANOUT_DB_PATH\")"}},
			},
			wantLevel: LevelHigh,
			wantSig:   []string{sigInvariantHit},
		},
		{
			name: "S10 FANOUT_ added line does not fire (removed-only)",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "internal/infra/log/log.go"}},
				AddedLines: map[string][]string{"internal/infra/log/log.go": {"os.Getenv(\"FANOUT_NEW_FLAG\")"}},
			},
			wantLevel: LevelLow,
			notSig:    []string{sigInvariantHit},
		},
		{
			name: "S10 FANOUT_ moved line (removed then re-added) does not fire",
			diff: Diff{
				Files:        []FileChange{{Status: 'M', Path: "internal/infra/log/log.go"}},
				RemovedLines: map[string][]string{"internal/infra/log/log.go": {"\tos.Getenv(\"FANOUT_DB_PATH\")"}},
				AddedLines:   map[string][]string{"internal/infra/log/log.go": {"    os.Getenv(\"FANOUT_DB_PATH\")"}}, // same name, re-indented
			},
			wantLevel: LevelLow,
			notSig:    []string{sigInvariantHit},
		},
		{
			// Suppression is per-file: a string occurrence added in another file
			// (a comment, a fixture) must not mask a real reference removal.
			name: "S10 FANOUT_ removal fires even when another file adds the same name",
			diff: Diff{
				Files: []FileChange{
					{Status: 'M', Path: "internal/app/sessionview/collect.go"},
					{Status: 'M', Path: "internal/infra/log/log.go"},
				},
				RemovedLines: map[string][]string{"internal/app/sessionview/collect.go": {"os.Getenv(\"FANOUT_STATE_PATH\")"}},
				AddedLines:   map[string][]string{"internal/infra/log/log.go": {"// see FANOUT_STATE_PATH"}},
			},
			wantLevel: LevelHigh,
			wantSig:   []string{sigInvariantHit},
		},
		{
			name: "S10 renamed FANOUT_ fires for the dropped old name only",
			diff: Diff{
				Files:        []FileChange{{Status: 'M', Path: "internal/infra/log/log.go"}},
				RemovedLines: map[string][]string{"internal/infra/log/log.go": {"os.Getenv(\"FANOUT_OLD_FLAG\")"}},
				AddedLines:   map[string][]string{"internal/infra/log/log.go": {"os.Getenv(\"FANOUT_NEW_FLAG\")"}},
			},
			wantLevel: LevelHigh,
			wantSig:   []string{sigInvariantHit},
		},
		{
			name: "S10 skips markdown quoting an invariant literal",
			diff: Diff{
				Files:      []FileChange{{Status: 'M', Path: "docs/architecture.ja.md"}},
				AddedLines: map[string][]string{"docs/architecture.ja.md": {"- **dashboard は read-only**: `requireToken` が守る"}},
			},
			wantLevel: LevelMedium, // architecture.ja.md's own class, no S10 bump
			notSig:    []string{sigInvariantHit},
		},
		{
			name: "S10 skips FANOUT_ removal in markdown",
			diff: Diff{
				Files:        []FileChange{{Status: 'M', Path: "site/content/docs/installation.md"}},
				RemovedLines: map[string][]string{"site/content/docs/installation.md": {"FANOUT_VERSION=v0.8.0 ./install.sh"}},
			},
			wantLevel: LevelNone,
			notSig:    []string{sigInvariantHit},
		},
		{
			name:      "S11 large diff bumps low to medium",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "internal/infra/log/log.go", Added: 500, Deleted: 400}}},
			wantLevel: LevelMedium,
			wantSig:   []string{sigLargeDiff},
		},
		{
			name:      "S11 large diff bumps medium to high",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "internal/app/run/run.go", Added: 900}}},
			wantLevel: LevelHigh,
			wantSig:   []string{sigLargeDiff},
		},
		{
			name:      "S11 does not bump a high base",
			diff:      Diff{Files: []FileChange{{Status: 'M', Path: "internal/infra/state/store.go", Added: 900}}},
			wantLevel: LevelHigh,
			notSig:    []string{sigLargeDiff},
		},
		{
			name: "S11 ignores NONE lines when totalling",
			diff: Diff{Files: []FileChange{
				{Status: 'M', Path: "README.md", Added: 900},                // NONE: excluded
				{Status: 'M', Path: "internal/infra/log/log.go", Added: 10}, // A: 10 lines only
			}},
			wantLevel: LevelLow,
			notSig:    []string{sigLargeDiff},
		},
		{
			name: "binary file counts as zero lines for S11",
			diff: Diff{Files: []FileChange{
				{Status: 'M', Path: "internal/infra/log/log.go", Added: -1, Deleted: -1}, // binary
			}},
			wantLevel: LevelLow,
			notSig:    []string{sigLargeDiff},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluate(tt.diff)
			if got.Level != tt.wantLevel {
				t.Errorf("evaluate(%s) level = %v, want %v", tt.name, got.Level, tt.wantLevel)
			}
			sigs := reasonSignals(got)
			for _, w := range tt.wantSig {
				if !slices.Contains(sigs, w) {
					t.Errorf("evaluate(%s) reasons = %v, want signal %q present", tt.name, sigs, w)
				}
			}
			for _, n := range tt.notSig {
				if slices.Contains(sigs, n) {
					t.Errorf("evaluate(%s) reasons = %v, want signal %q absent", tt.name, sigs, n)
				}
			}
		})
	}
}

// TestEvaluateLargeDiffFileCount pins the S11 file-count threshold (>30) on its
// own: 31 small non-NONE files trip it even though their line total is tiny.
func TestEvaluateLargeDiffFileCount(t *testing.T) {
	var d Diff
	for i := range largeDiffFiles + 1 {
		d.Files = append(d.Files, FileChange{Status: 'M', Path: fmt.Sprintf("internal/infra/log/f%d.go", i), Added: 1})
	}
	got := evaluate(d)
	if got.Level != LevelMedium {
		t.Errorf("evaluate(31 class-A files) level = %v, want medium (S11 file-count bump)", got.Level)
	}
	if !slices.Contains(reasonSignals(got), sigLargeDiff) {
		t.Errorf("evaluate(31 class-A files) reasons = %v, want S11 present", reasonSignals(got))
	}
}

// TestEvaluateDeterministic guards the sort promises: repeated evaluation of the
// same diff yields byte-identical Files and Reasons ordering.
func TestEvaluateDeterministic(t *testing.T) {
	d := Diff{Files: []FileChange{
		{Status: 'D', Path: "tests/golden/z.txt"},
		{Status: 'M', Path: "internal/arch/arch.go"},
		{Status: 'A', Path: "internal/ui/dashboard/newthing.go"},
	}}
	a := evaluate(d)
	b := evaluate(d)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("evaluate is not deterministic:\n a = %#v\n b = %#v", a, b)
	}
	// Files must be path-sorted.
	paths := make([]string, len(a.Files))
	for i, f := range a.Files {
		paths[i] = f.Path
	}
	if !slices.IsSorted(paths) {
		t.Errorf("evaluate files are not path-sorted: %v", paths)
	}
}
