package main

import (
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// largeDiffLines / largeDiffFiles are the S11 thresholds: a diff whose non-NONE
// files together change more than largeDiffLines lines, or number more than
// largeDiffFiles, is large enough that a low/medium base gets one extra level.
const (
	largeDiffLines = 800
	largeDiffFiles = 30
)

// Signal identifiers recorded in Reason.Signal. S1-S8 push straight to critical,
// S9/S10 bump to high, S11 adds one level to a low/medium base.
const (
	sigTestDeleted       = "S1-test-deleted"
	sigMeasureDeleted    = "S2-measure-deleted"
	sigSkipAdded         = "S3-skip-added"
	sigGuardModified     = "S4-guard-modified"
	sigReviewGateChanged = "S5-review-gate-modified"
	sigRiskToolModified  = "S6-risk-tool-modified"
	sigInstallerModified = "S7-installer-modified"
	sigCIWorkflowDeleted = "S8-ci-workflow-deleted"
	sigUnclassifiedPath  = "S9-unclassified-path"
	sigInvariantHit      = "S10-invariant-hit"
	sigLargeDiff         = "S11-large-diff"
)

// Skip-marker greps for S3, one per test flavor. goSkipRe requires the call
// paren so t.Skip/Skipf/SkipNow match but t.Skipped() (a status read) does not.
// tsSkipRe covers the vitest skip spellings: .skip(...), .skipIf(...), chained
// modifiers like test.skip.concurrent(...), the { skip: true } option, and the
// x-prefixed aliases. batsSkipRe matches skip in command position — at line
// start or after &&, ||, ;, {, (, then/else/do — so conditional forms like
// `[[ $CI == true ]] && skip "flaky"` count, while prose in a comment does not.
var (
	goSkipRe   = regexp.MustCompile(`\bt\.(Skip|Skipf|SkipNow)\(`)
	tsSkipRe   = regexp.MustCompile(`\.skip(If)?\s*\(|\.skip\.|\bskip:\s*true|\bxit\(|\bxdescribe\(|\bxtest\(`)
	batsSkipRe = regexp.MustCompile(`(^|&&|\|\||;|\{|\(|\bthen\b|\belse\b|\bdo\b)\s*skip\b`)
)

// evaluate classifies every changed file and aggregates the risk level. Order:
// base = max(levelForClass over files); S9 (unclassified) and S10 (invariant
// hit) bump to high; S11 (large diff) adds one level to a low/medium base; then
// any S1-S8 signal forces critical. Every bump appends a Reason. Files are
// sorted by path and Reasons by (level desc, signal, file) so the output is
// deterministic.
func evaluate(d Diff) Report {
	files := make([]FileReport, 0, len(d.Files))
	classByPath := make(map[string]Class, len(d.Files))
	level := LevelNone
	for _, fc := range d.Files {
		fr := FileReport{Path: fc.Path, Status: statusString(fc)}
		rule, ok := classifyPath(fc.Path)
		if !ok {
			rule = Rule{Class: ClassUnknown, ID: "unclassified", Note: "未分類(rules.go に要追記)"}
		}
		// A rename can carry a file out of a heavier class into a lighter one
		// (git mv from an H package into M/A). Judge by the heavier endpoint so
		// the move is not under-reviewed. Only when the new path classified: an
		// unclassified destination must stay ClassUnknown so S9 fails closed
		// (e.g. git mv README.md some-new-top-level-file).
		if ok && fc.Status == 'R' && fc.OldPath != "" {
			if oldRule, ook := classifyPath(fc.OldPath); ook && oldRule.Class > rule.Class {
				rule = oldRule
				rule.Note = oldRule.Note + "(rename 元 " + fc.OldPath + " 由来)"
			}
		}
		fr.Class, fr.RuleID, fr.Note = rule.Class, rule.ID, rule.Note
		fr.Level = levelForClass(fr.Class)
		if fr.Level > level {
			level = fr.Level
		}
		classByPath[fr.Path] = fr.Class
		files = append(files, fr)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	var reasons []Reason

	// S9 unclassified-path -> high.
	for _, fr := range files {
		if fr.Class == ClassUnknown {
			level = maxLevel(level, LevelHigh)
			reasons = append(reasons, Reason{
				Signal: sigUnclassifiedPath, Level: LevelHigh, File: fr.Path,
				Detail: "未分類パス(fail-closed で high)",
			})
		}
	}

	// S10 invariant-hit -> high.
	for _, r := range invariantReasons(d) {
		level = maxLevel(level, LevelHigh)
		reasons = append(reasons, r)
	}

	// S11 large-diff -> +1, only on a low/medium base.
	if r, ok := largeDiffReason(d, classByPath); ok && (level == LevelLow || level == LevelMedium) {
		level++
		r.Level = level
		reasons = append(reasons, r)
	}

	// S1-S8 -> critical.
	if crit := criticalReasons(d); len(crit) > 0 {
		level = LevelCritical
		reasons = append(reasons, crit...)
	}

	sortReasons(reasons)
	return Report{Level: level, Files: files, Reasons: reasons, Stats: computeStats(d)}
}

// criticalReasons collects every S1-S8 signal that fired. Any non-empty result
// forces the report to critical.
func criticalReasons(d Diff) []Reason {
	var reasons []Reason
	for _, fc := range d.Files {
		// S1 test-deleted: a deleted test file, or a rename that drops test shape.
		if fc.Status == 'D' && isTestShape(fc.Path) {
			reasons = append(reasons, Reason{Signal: sigTestDeleted, Level: LevelCritical, File: fc.Path, Detail: "テストファイル削除"})
		}
		if fc.Status == 'R' && isTestShape(fc.OldPath) && !isTestShape(fc.Path) {
			reasons = append(reasons, Reason{Signal: sigTestDeleted, Level: LevelCritical, File: fc.Path, Detail: "rename でテスト形状を喪失(" + fc.OldPath + ")"})
		}
		// S2 measure-deleted: a deleted golden/fixture/bin file, or a rename
		// that carries one out of the measured tree.
		if fc.Status == 'D' && isMeasurePath(fc.Path) {
			reasons = append(reasons, Reason{Signal: sigMeasureDeleted, Level: LevelCritical, File: fc.Path, Detail: "測定基準(golden/fixture/bin)削除"})
		}
		if fc.Status == 'R' && isMeasurePath(fc.OldPath) && !isMeasurePath(fc.Path) {
			reasons = append(reasons, Reason{Signal: sigMeasureDeleted, Level: LevelCritical, File: fc.Path, Detail: "rename で測定基準を移動(" + fc.OldPath + ")"})
		}
		// S4 guard-modified: any touch of the layer guard.
		if touches(fc, "internal/arch/") {
			reasons = append(reasons, Reason{Signal: sigGuardModified, Level: LevelCritical, File: fc.Path, Detail: "層ガード(internal/arch)変更"})
		}
		// S5 review-gate-modified: any touch of .claude/ or the post-work-review
		// gate pieces installed from codex/ and claude/ (they produce the review
		// marker the PR gate checks; weakening them weakens the gate itself).
		// codex/agents/post-work-* are the reviewer/verifier agent definitions
		// the gate script drives — same gate, same weight.
		if touches(fc, ".claude/") ||
			touches(fc, "codex/tools/post-work-review") ||
			touches(fc, "codex/agents/post-work-") ||
			touches(fc, "codex/skills/post-work-review/") ||
			touches(fc, "claude/skills/post-work-review/") {
			reasons = append(reasons, Reason{Signal: sigReviewGateChanged, Level: LevelCritical, File: fc.Path, Detail: "PR review gate(.claude / post-work-review)変更"})
		}
		// S6 risk-tool-modified: this tool or its workflow.
		if touches(fc, "tools/reviewrisk/") || fc.Path == riskWorkflow || fc.OldPath == riskWorkflow {
			reasons = append(reasons, Reason{Signal: sigRiskToolModified, Level: LevelCritical, File: fc.Path, Detail: "risk 判定ツール自身の変更"})
		}
		// S7 installer-modified: install.sh.
		if fc.Path == "install.sh" || fc.OldPath == "install.sh" {
			reasons = append(reasons, Reason{Signal: sigInstallerModified, Level: LevelCritical, File: fc.Path, Detail: "install.sh 変更"})
		}
		// S8 ci-workflow-deleted: a deleted workflow file, or a rename that stops
		// it being one — moving it out of .github/workflows/ OR dropping the
		// .yml/.yaml extension in place (test.yml -> test.yml.disabled), which
		// GitHub Actions treats as removal.
		if fc.Status == 'D' && isWorkflowFile(fc.Path) {
			reasons = append(reasons, Reason{Signal: sigCIWorkflowDeleted, Level: LevelCritical, File: fc.Path, Detail: "CI workflow 削除"})
		}
		if fc.Status == 'R' && isWorkflowFile(fc.OldPath) && !isWorkflowFile(fc.Path) {
			reasons = append(reasons, Reason{Signal: sigCIWorkflowDeleted, Level: LevelCritical, File: fc.Path, Detail: "CI workflow を rename で無効化(" + fc.OldPath + ")"})
		}
	}
	// S3 skip-added: an added skip/xit/xdescribe line in a test file.
	for _, p := range slices.Sorted(maps.Keys(d.AddedLines)) {
		if line, ok := skipAddedMatch(p, d.AddedLines[p]); ok {
			reasons = append(reasons, Reason{Signal: sigSkipAdded, Level: LevelCritical, File: p, Detail: "スキップ追加: " + line})
		}
	}
	return reasons
}

const riskWorkflow = ".github/workflows/review-risk.yml"

// invariantReasons collects S10 signals: a diff line (added or removed) that hits
// a human-must-read invariant pattern, plus FANOUT_* env var names dropped by the
// diff. Markdown files are exempt: prose that quotes an invariant literal (the
// architecture doc's own catalog, install docs pinning FANOUT_VERSION) is not a
// code change to that invariant.
func invariantReasons(d Diff) []Reason {
	var reasons []Reason
	for _, p := range unionPaths(d.AddedLines, d.RemovedLines) {
		if strings.HasSuffix(p, ".md") {
			continue
		}
		for _, ip := range invariantPatterns {
			if anyMatch(ip.Re, d.AddedLines[p]) || anyMatch(ip.Re, d.RemovedLines[p]) {
				reasons = append(reasons, Reason{Signal: sigInvariantHit, Level: LevelHigh, File: p, Detail: "不変条件 " + ip.Name + " に接触"})
			}
		}
	}
	return append(reasons, envRemovalReasons(d)...)
}

// envRemovalReasons fires S10 for each FANOUT_* env var name that appears on a
// removed line of a file without reappearing on an added line of the SAME file.
// The suppression is per-file on purpose: it forgives line moves and re-indents
// inside one file, but a mere string occurrence added elsewhere (a comment, a
// test fixture) must not mask a genuine reference removal. Markdown files are
// exempt on both sides.
func envRemovalReasons(d Diff) []Reason {
	var reasons []Reason
	for _, p := range slices.Sorted(maps.Keys(d.RemovedLines)) {
		if strings.HasSuffix(p, ".md") {
			continue
		}
		addedHere := make(map[string]bool)
		for _, l := range d.AddedLines[p] {
			for _, name := range envPatternRe.FindAllString(l, -1) {
				addedHere[name] = true
			}
		}
		removedHere := make(map[string]bool)
		for _, l := range d.RemovedLines[p] {
			for _, name := range envPatternRe.FindAllString(l, -1) {
				removedHere[name] = true
			}
		}
		for _, name := range slices.Sorted(maps.Keys(removedHere)) {
			if addedHere[name] {
				continue
			}
			reasons = append(reasons, Reason{
				Signal: sigInvariantHit, Level: LevelHigh, File: p,
				Detail: "FANOUT_* env 参照を削除: " + name,
			})
		}
	}
	return reasons
}

// largeDiffReason returns the S11 reason when the non-NONE files together exceed
// the line or file thresholds. Binary files (-1 counts) contribute 0 lines. It
// reuses the classByPath map evaluate already built (keyed by new path) instead
// of re-classifying. The caller only applies the bump on a low/medium base; the
// returned Reason's Level is filled in there.
func largeDiffReason(d Diff, classByPath map[string]Class) (Reason, bool) {
	var lines, count int
	for _, fc := range d.Files {
		if classByPath[fc.Path] == ClassNone {
			continue
		}
		count++
		lines += nonNeg(fc.Added) + nonNeg(fc.Deleted)
	}
	if lines > largeDiffLines || count > largeDiffFiles {
		return Reason{
			Signal: sigLargeDiff, File: "",
			Detail: "大規模 diff: 非 NONE " + strconv.Itoa(count) + " ファイル / 変更 " + strconv.Itoa(lines) + " 行",
		}, true
	}
	return Reason{}, false
}

// computeStats totals the whole diff for the report summary. Binary files (-1)
// contribute 0.
func computeStats(d Diff) Stats {
	s := Stats{Files: len(d.Files)}
	for _, fc := range d.Files {
		s.Added += nonNeg(fc.Added)
		s.Deleted += nonNeg(fc.Deleted)
	}
	return s
}

// skipAddedMatch reports the first added line in a test file that introduces a
// skip marker, choosing the grep by the file's shape. bats sourced helpers
// (tests/bats/*.bash) call skip the same way, so they get the bats grep too,
// and the web harness under web/src/test/ gets the vitest grep alongside
// *.test.ts(x) (isWebTestFile covers both).
func skipAddedMatch(p string, lines []string) (string, bool) {
	var re *regexp.Regexp
	switch {
	case strings.HasSuffix(p, "_test.go"):
		re = goSkipRe
	case isWebTestFile(p):
		re = tsSkipRe
	case strings.HasSuffix(p, ".bats"):
		re = batsSkipRe
	case strings.HasPrefix(p, "tests/bats/") && strings.HasSuffix(p, ".bash"):
		re = batsSkipRe
	default:
		return "", false
	}
	for _, l := range lines {
		if re.MatchString(l) {
			return strings.TrimSpace(l), true
		}
	}
	return "", false
}

// isTestShape reports whether a path is a test file: a Go *_test.go, a bats file
// under tests/bats/, a web *.test.* under web/src/, or anything under
// web/src/test/.
func isTestShape(p string) bool {
	if strings.HasSuffix(p, "_test.go") {
		return true
	}
	if strings.HasPrefix(p, "tests/bats/") && strings.HasSuffix(p, ".bats") {
		return true
	}
	if strings.HasPrefix(p, "web/src/") {
		if strings.HasPrefix(p, "web/src/test/") {
			return true
		}
		if strings.Contains(path.Base(p), ".test.") {
			return true
		}
	}
	return false
}

// isMeasurePath reports whether a path is a measurement yardstick: a golden,
// fixture, or test-bin file whose deletion removes what a test checks against.
// isWorkflowFile reports whether p is a file GitHub Actions actually runs:
// DIRECTLY under .github/workflows/ (Actions ignores subdirectories) AND
// carrying the required .yml/.yaml extension. A rename to another extension or
// into a subdirectory stops being a workflow.
func isWorkflowFile(p string) bool {
	rest, ok := strings.CutPrefix(p, ".github/workflows/")
	if !ok || strings.Contains(rest, "/") {
		return false
	}
	return strings.HasSuffix(rest, ".yml") || strings.HasSuffix(rest, ".yaml")
}

func isMeasurePath(p string) bool {
	return strings.HasPrefix(p, "tests/golden/") ||
		strings.HasPrefix(p, "tests/fixtures/") ||
		strings.HasPrefix(p, "tests/bin/")
}

// touches reports whether a change's new or old path is under prefix (so a
// rename out of a guarded tree still fires).
func touches(fc FileChange, prefix string) bool {
	return strings.HasPrefix(fc.Path, prefix) ||
		(fc.OldPath != "" && strings.HasPrefix(fc.OldPath, prefix))
}

// statusString renders a FileChange status byte for the report's St column.
func statusString(fc FileChange) string {
	if fc.Status == 0 {
		return "?"
	}
	return string(fc.Status)
}

func maxLevel(a, b Level) Level {
	if a > b {
		return a
	}
	return b
}

func nonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func anyMatch(re *regexp.Regexp, lines []string) bool {
	return slices.ContainsFunc(lines, re.MatchString)
}

// unionPaths returns the sorted union of two content maps' keys.
func unionPaths(a, b map[string][]string) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	return slices.Sorted(maps.Keys(set))
}

// sortReasons orders reasons deterministically: highest level first, then signal
// id, then file.
func sortReasons(rs []Reason) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Level != rs[j].Level {
			return rs[i].Level > rs[j].Level
		}
		if rs[i].Signal != rs[j].Signal {
			return rs[i].Signal < rs[j].Signal
		}
		return rs[i].File < rs[j].File
	})
}
