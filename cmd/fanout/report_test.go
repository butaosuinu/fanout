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
	if !strings.Contains(got, "  fanout-go 700 --limit 1 --agent claude\n") {
		t.Fatalf("summary output did not include fanout-go rerun hint:\n%s", got)
	}
	if strings.Contains(got, "  fanout 700 --limit") {
		t.Fatalf("summary output fell back to fanout:\n%s", got)
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
