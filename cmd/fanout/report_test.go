package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
)

func TestPrintSummaryUsesInvokedCommandNameInLimitRerunHint(t *testing.T) {
	var out, err bytes.Buffer
	lg := log.NewWith(&out, &err, false)
	plan := Plan{
		LimitDeferred: []ghissue.Issue{{Number: 702}},
	}
	cfg := &cliflags.Config{
		ParentRef: "700",
		Agent:     "claude",
	}

	printSummary(plan, executionResult{}, cfg, lg, log.Palette{}, "fanout-go")

	got := out.String()
	if !strings.Contains(got, "  fanout-go 700 --include 702 --only 702 --agent claude\n") {
		t.Fatalf("summary output did not include fanout-go rerun hint:\n%s", got)
	}
	if strings.Contains(got, "  fanout 700 --limit") {
		t.Fatalf("summary output fell back to fanout:\n%s", got)
	}
}

func TestPrintSummaryPreservesSettingsFlagsInLimitRerunHint(t *testing.T) {
	var out, err bytes.Buffer
	lg := log.NewWith(&out, &err, false)
	plan := Plan{
		LimitDeferred: []ghissue.Issue{{Number: 702}, {Number: 703}},
	}
	cfg := &cliflags.Config{
		ParentRef:          "700",
		Agent:              "claude",
		AutoPullRequest:    boolPtr(false),
		PRReviewGate:       boolPtr(true),
		BriefingCodeReview: boolPtr(false),
		AgentTeamsHint:     boolPtr(false),
	}

	printSummary(plan, executionResult{}, cfg, lg, log.Palette{}, "fanout-go")

	got := out.String()
	want := "  fanout-go 700 --include '702,703' --only '702,703' --no-auto-pr --pr-review-gate --no-briefing-code-review --no-agent-teams-hint --agent claude\n"
	if !strings.Contains(got, want) {
		t.Fatalf("summary output did not preserve settings flags:\nwant %q\noutput:\n%s", want, got)
	}
}

func TestPrintSummaryPreservesDeferredNameFlagsInLimitRerunHint(t *testing.T) {
	var out, err bytes.Buffer
	lg := log.NewWith(&out, &err, false)
	plan := Plan{
		LimitDeferred: []ghissue.Issue{{Number: 703}},
	}
	cfg := &cliflags.Config{
		ParentRef: "700",
		Agent:     "claude",
		Names: []cliflags.NameOverride{
			{Num: 701, SlugHint: "already-created"},
			{Num: 703, SlugHint: "custom-703", DisplayName: "Custom 703", BranchName: "feat/703"},
		},
	}

	printSummary(plan, executionResult{}, cfg, lg, log.Palette{}, "fanout-go")

	got := out.String()
	want := "  fanout-go 700 --include 703 --only 703 --name '703=custom-703|Custom 703|feat/703' --agent claude\n"
	if !strings.Contains(got, want) {
		t.Fatalf("summary output did not preserve deferred --name flag:\nwant %q\noutput:\n%s", want, got)
	}
	if strings.Contains(got, "already-created") {
		t.Fatalf("summary output included non-deferred name override:\n%s", got)
	}
}

func TestPrintSummarySuppressesLimitRerunHintAfterFailure(t *testing.T) {
	var out, err bytes.Buffer
	lg := log.NewWith(&out, &err, false)
	plan := Plan{
		LimitDeferred: []ghissue.Issue{{Number: 703}},
	}
	cfg := &cliflags.Config{
		ParentRef: "700",
		Agent:     "claude",
	}

	printSummary(plan, executionResult{Failed: 1}, cfg, lg, log.Palette{}, "fanout-go")

	got := out.String()
	if strings.Contains(got, "Rerun with:") || strings.Contains(got, "--only 703") {
		t.Fatalf("summary output printed unsafe deferred-only rerun hint after failure:\n%s", got)
	}
	if !strings.Contains(got, "deferred (--limit): 1") {
		t.Fatalf("summary output did not report deferred limit count after failure:\n%s", got)
	}
}

// TestShellQuoteIsCopyPasteSafe pins shellQuote and, more importantly, proves
// the property that actually matters for the --limit rerun hint: the quoted
// token must evaluate back to the original argument under a POSIX shell, so the
// printed command is copy-paste-safe. This is the path reached by metacharacter
// flag values such as `--only 1,2,3` (comma) and `--project-status "In Progress"`
// (space), which previously had no test.
//
// We deliberately do NOT assert byte-equality with bash's `printf %q`: that
// output is bash-version-dependent (bash 3.2 vs 5.x disagree on leading `=`/`~`,
// `!`, and control-char wrapping), so there is no single correct target. POSIX
// single-quoting is portable and round-trips identically everywhere, which is
// the real requirement here.
func TestShellQuoteIsCopyPasteSafe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "''"},
		{"plain", "claude", "claude"},
		{"command", "fanout-go", "fanout-go"},
		{"issue-number", "700", "700"},
		{"project-url-is-bare", "https://github.com/orgs/acme/projects/3", "https://github.com/orgs/acme/projects/3"},
		{"only-csv-comma", "1,2,3", "'1,2,3'"},
		{"project-status-space", "In Progress", "'In Progress'"},
		{"ampersand", "a&b", "'a&b'"},
		{"embedded-single-quote", "it's", `'it'\''s'`},
		{"dollar-no-expand", "$HOME", "'$HOME'"},
	}

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available; skipping round-trip check")
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shellQuote(c.in)
			if got != c.want {
				t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
			}
			// Round-trip: a POSIX shell must parse the quoted token back into the
			// exact original string (a single argument). If shellQuote under-quoted,
			// `sh` would split or expand it and the output would not match.
			out, err := exec.Command("sh", "-c", "printf %s "+got).Output()
			if err != nil {
				t.Fatalf("sh failed on shellQuote(%q)=%q: %v", c.in, got, err)
			}
			if string(out) != c.in {
				t.Errorf("round-trip: sh parsed shellQuote(%q)=%q as %q, want %q", c.in, got, string(out), c.in)
			}
		})
	}
}

func boolPtr(v bool) *bool {
	return &v
}
