package main

import "regexp"

// RuleSource records where a Rule came from. SourceDocTable rules mirror the
// package table in docs/architecture.ja.md and the docsync test cross-checks
// them; SourceExtra rules cover files with no package-table row (go.mod,
// tests/, .github/, ...).
type RuleSource int

const (
	SourceDocTable RuleSource = iota
	SourceExtra
)

// Rule is the classification a path resolves to: its review Class, where the
// rule is sourced from, a stable ID for reporting, and a one-line note.
type Rule struct {
	ID     string
	Class  Class
	Source RuleSource
	Note   string
}

// FileChange is one entry from git diff --name-status / --numstat. Status is
// the porcelain letter (A/M/D/R/C); OldPath is set for renames and copies;
// Added and Deleted are -1 for binary files (numstat reports "-").
type FileChange struct {
	Path    string
	OldPath string
	Status  byte
	Added   int
	Deleted int
}

// Diff is the parsed base..HEAD diff. AddedLines and RemovedLines hold the +/-
// content lines per path (from -U0) that the escalation-signal greps scan.
type Diff struct {
	Files        []FileChange
	AddedLines   map[string][]string
	RemovedLines map[string][]string
}

// Reason is one escalation signal that fired, with the level it pushed to and
// the file that triggered it.
type Reason struct {
	Signal string `json:"signal"`
	Level  Level  `json:"level"`
	File   string `json:"file"`
	Detail string `json:"detail"`
}

// FileReport is the per-file row in the report: its resolved class/level and
// the rule that matched.
type FileReport struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Class  Class  `json:"class"`
	Level  Level  `json:"level"`
	RuleID string `json:"rule"`
	Note   string `json:"note"`
}

// Stats summarizes the diff size for the large-diff signal and the report.
type Stats struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
}

// Report is the whole judgment: the aggregate level, per-file rows, the
// reasons that escalated it, and the diff stats.
type Report struct {
	Level   Level        `json:"level"`
	Files   []FileReport `json:"files"`
	Reasons []Reason     `json:"reasons"`
	Stats   Stats        `json:"stats"`
}

// invariantPatterns are the S10 grep patterns: a diff that adds or removes a
// line matching one of these touches a human-must-read invariant and bumps to
// high. DocLiteral is the plain string the pattern corresponds to in
// docs/architecture.ja.md, which TestInvariantPatternsAppearInDoc pins so a
// renamed invariant cannot silently drop off the watch list.
var invariantPatterns = []struct {
	Name       string
	Re         *regexp.Regexp
	DocLiteral string
}{
	{Name: "requireToken", Re: regexp.MustCompile(`requireToken`), DocLiteral: "requireToken"},
	{Name: "127.0.0.1", Re: regexp.MustCompile(`127\.0\.0\.1`), DocLiteral: "127.0.0.1"},
	{Name: "__tui-new-pane-popup", Re: regexp.MustCompile(`__tui-new-pane-popup`), DocLiteral: "__tui-new-pane-popup"},
	{Name: "__tui-help-popup", Re: regexp.MustCompile(`__tui-help-popup`), DocLiteral: "__tui-help-popup"},
	{Name: "__codex-plan-tui", Re: regexp.MustCompile(`__codex-plan-tui`), DocLiteral: "__codex-plan-tui"},
	{Name: "main.version", Re: regexp.MustCompile(`main\.version`), DocLiteral: "main.version"},
}

// envPatternRe matches FANOUT_* env var names. Unlike invariantPatterns it is a
// removed-line-only signal: dropping a reference to a FANOUT_* variable can
// break shell/CI/doc callers, but adding one is routine.
var envPatternRe = regexp.MustCompile(`FANOUT_[A-Z_]{2,}`)
