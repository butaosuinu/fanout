package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
)

// minusSign is the U+2212 minus used in the "+A −D" diff-size summaries, shared
// by Text and Markdown so both render identically.
const minusSign = "−"

// reviewRiskMarker leads the Markdown sticky comment so CI can find and replace
// the same comment. Must match the jq startswith filter in
// .github/workflows/review-risk.yml.
const reviewRiskMarker = "<!-- review-risk -->"

// plusMinus formats an added/deleted pair as "+A −D".
func plusMinus(added, deleted int) string {
	return fmt.Sprintf("+%d %s%d", added, minusSign, deleted)
}

// Text renders the report for a terminal: the level and its guidance, the
// escalation reasons, and an aligned per-file table whose header carries the
// diff stats.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review risk: %s — %s\n", strings.ToUpper(r.Level.String()), levelGuidance[r.Level])

	if len(r.Reasons) > 0 {
		b.WriteString("\n理由:\n")
		for _, rs := range r.Reasons {
			file := rs.File
			if file == "" {
				file = "-"
			}
			fmt.Fprintf(&b, "  [%s] %s  %s  %s\n", rs.Level, rs.Signal, file, rs.Detail)
		}
	}

	if len(r.Files) > 0 {
		fmt.Fprintf(&b, "\nファイル (%d files, %s):\n", r.Stats.Files, plusMinus(r.Stats.Added, r.Stats.Deleted))
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  St\tClass\tLevel\tRule\tFile")
		for _, f := range r.Files {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", f.Status, f.Class, f.Level, f.RuleID, f.Path)
		}
		_ = tw.Flush()
	}

	return strings.TrimRight(b.String(), "\n")
}

// Markdown renders the sticky-comment body. It leads with the review-risk marker
// so CI can find and update the same comment, states the level and guidance,
// lists the reasons, and folds the per-file class table into a <details>.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString(reviewRiskMarker + "\n")
	fmt.Fprintf(&b, "## Review risk: **%s**\n\n", strings.ToUpper(r.Level.String()))
	b.WriteString(levelGuidance[r.Level] + "\n\n")

	b.WriteString("### 理由\n\n")
	if len(r.Reasons) == 0 {
		b.WriteString("- エスカレーションシグナルなし\n\n")
	} else {
		for _, rs := range r.Reasons {
			file := rs.File
			if file == "" {
				file = "(diff 全体)"
			}
			fmt.Fprintf(&b, "- **[%s]** `%s` — `%s`: %s\n", rs.Level, rs.Signal, file, rs.Detail)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "<details><summary>ファイル別クラス (%d files, %s)</summary>\n\n", r.Stats.Files, plusMinus(r.Stats.Added, r.Stats.Deleted))
	b.WriteString("| File | St | Class | Level | Rule |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, f := range r.Files {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", f.Path, f.Status, f.Class, f.Level, f.RuleID)
	}
	b.WriteString("\n</details>\n\n")

	b.WriteString("判定ルール: docs/review-risk.ja.md / docs/architecture.ja.md\n")
	return b.String()
}

// JSON renders the report as indented JSON. Level and Class marshal to their
// string labels via their MarshalJSON methods.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
