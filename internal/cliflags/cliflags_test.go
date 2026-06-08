package cliflags

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
)

func TestParseNumCSVAllowsSingleTrailingComma(t *testing.T) {
	got, err := parseNumCSV("--only", "501,")
	if err != nil {
		t.Fatalf("parseNumCSV returned error: %v", err)
	}
	if want := []int{501}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNumCSV = %#v, want %#v", got, want)
	}
}

func TestParseNumCSVRejectsInternalAndRepeatedTrailingEmptyEntries(t *testing.T) {
	for _, raw := range []string{"501,,502", "501,,", ","} {
		if _, err := parseNumCSV("--only", raw); err == nil {
			t.Fatalf("parseNumCSV(%q) returned nil error", raw)
		}
	}
}

func TestParseSettingsBoolFlagsLastWins(t *testing.T) {
	cfg := parseOK(t,
		"100",
		"--no-auto-pr", "--auto-pr",
		"--pr-review-gate", "--no-pr-review-gate",
		"--no-briefing-code-review", "--briefing-code-review",
		"--agent-teams-hint", "--no-agent-teams-hint",
		"--codex-plan-mode", "--no-codex-plan-mode",
		"--no-pr-visualization", "--pr-visualization",
	)

	assertBoolPtr(t, "AutoPullRequest", cfg.AutoPullRequest, true)
	assertBoolPtr(t, "PRReviewGate", cfg.PRReviewGate, false)
	assertBoolPtr(t, "BriefingCodeReview", cfg.BriefingCodeReview, true)
	assertBoolPtr(t, "AgentTeamsHint", cfg.AgentTeamsHint, false)
	assertBoolPtr(t, "CodexPlanMode", cfg.CodexPlanMode, false)
	assertBoolPtr(t, "PRVisualization", cfg.PRVisualization, true)
}

func TestParseCodexPlanModeFlag(t *testing.T) {
	cfg := parseOK(t, "100", "--agent", "codex", "--codex-plan-mode")

	if !cfg.CodexPlanModeEnabled() {
		t.Fatal("CodexPlanModeEnabled() = false, want true")
	}
}

func TestParseWorktreeFlags(t *testing.T) {
	cfg := parseOK(t,
		"100",
		"--base-branch", "release/v2",
		"--branch-prefix", "fanout/custom/",
		"--no-refresh",
	)

	if cfg.BaseBranch != "release/v2" {
		t.Fatalf("BaseBranch = %q, want release/v2", cfg.BaseBranch)
	}
	if cfg.BranchPrefix != "fanout/custom/" {
		t.Fatalf("BranchPrefix = %q, want fanout/custom/", cfg.BranchPrefix)
	}
	if !cfg.NoRefresh {
		t.Fatal("NoRefresh = false, want true")
	}
}

func TestParseStatusFormat(t *testing.T) {
	cfg := parseOK(t, "--status", "100", "--format", "table")
	if cfg.Format != "table" {
		t.Fatalf("Format = %q, want table", cfg.Format)
	}

	cfg = parseOK(t, "--status", "100")
	if cfg.Format != DefaultFormat {
		t.Fatalf("default Format = %q, want %q", cfg.Format, DefaultFormat)
	}
}

func TestParseStatusPostDashboard(t *testing.T) {
	cfg := parseOK(t, "--status", "100", "--post-dashboard")
	if !cfg.PostDashboard {
		t.Fatal("PostDashboard = false, want true")
	}
}

func TestParseFormatRequiresStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res := Parse([]string{"100", "--format", "table"}, log.NewWith(&stdout, &stderr, false), io.Discard)
	if res.Code != exitcode.Invocation {
		t.Fatalf("Parse() code = %d, want %d", res.Code, exitcode.Invocation)
	}
	if got := stderr.String(); !strings.Contains(got, "--format can only be used with --status") {
		t.Fatalf("stderr = %q, want --format requires --status message", got)
	}
}

func TestParsePostDashboardRequiresStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res := Parse([]string{"100", "--post-dashboard"}, log.NewWith(&stdout, &stderr, false), io.Discard)
	if res.Code != exitcode.Invocation {
		t.Fatalf("Parse() code = %d, want %d", res.Code, exitcode.Invocation)
	}
	if got := stderr.String(); !strings.Contains(got, "--post-dashboard can only be used with --status") {
		t.Fatalf("stderr = %q, want --post-dashboard requires --status message", got)
	}
}

func TestParseFormatRejectsUnknownValue(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res := Parse([]string{"--status", "100", "--format", "yaml"}, log.NewWith(&stdout, &stderr, false), io.Discard)
	if res.Code != exitcode.Env {
		t.Fatalf("Parse() code = %d, want %d", res.Code, exitcode.Env)
	}
	if got := stderr.String(); !strings.Contains(got, "--format must be one of json,table") {
		t.Fatalf("stderr = %q, want invalid --format message", got)
	}
}

func TestParseStatusRejectsSettingsBoolFlags(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want string
	}{
		{"--auto-pr", "--status cannot be combined with --auto-pr"},
		{"--no-auto-pr", "--status cannot be combined with --no-auto-pr"},
		{"--pr-review-gate", "--status cannot be combined with --pr-review-gate"},
		{"--no-pr-review-gate", "--status cannot be combined with --no-pr-review-gate"},
		{"--briefing-code-review", "--status cannot be combined with --briefing-code-review"},
		{"--no-briefing-code-review", "--status cannot be combined with --no-briefing-code-review"},
		{"--agent-teams-hint", "--status cannot be combined with --agent-teams-hint"},
		{"--no-agent-teams-hint", "--status cannot be combined with --no-agent-teams-hint"},
		{"--codex-plan-mode", "--status cannot be combined with --codex-plan-mode"},
		{"--no-codex-plan-mode", "--status cannot be combined with --no-codex-plan-mode"},
		{"--pr-visualization", "--status cannot be combined with --pr-visualization"},
		{"--no-pr-visualization", "--status cannot be combined with --no-pr-visualization"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			res := Parse([]string{"--status", "100", tc.flag}, log.NewWith(&stdout, &stderr, false), io.Discard)
			if res.Code != exitcode.Invocation {
				t.Fatalf("Parse() code = %d, want %d", res.Code, exitcode.Invocation)
			}
			if got := stderr.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("stderr = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

func parseOK(t *testing.T, args ...string) *Config {
	t.Helper()
	var stdout, stderr bytes.Buffer
	res := Parse(args, log.NewWith(&stdout, &stderr, false), io.Discard)
	if res.Code != exitcode.OK || res.Config == nil {
		t.Fatalf("Parse(%q) failed with code=%d stdout=%q stderr=%q", args, res.Code, stdout.String(), stderr.String())
	}
	return res.Config
}

func assertBoolPtr(t *testing.T, name string, got *bool, want bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %v, want %v", name, *got, want)
	}
}
