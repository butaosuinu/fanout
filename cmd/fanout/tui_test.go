package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/state"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
)

func TestTUIAgentOrDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "codex", raw: "codex", want: "codex"},
		{name: "claude", raw: "claude", want: "claude"},
		{name: "unknown", raw: "other", want: "claude"},
		{name: "empty", raw: "", want: "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tuiAgentOrDefault(tc.raw); got != tc.want {
				t.Fatalf("tuiAgentOrDefault(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeTUISlug(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty", raw: "  ", want: ""},
		{name: "kebab", raw: "  manual-1-pane  ", want: "manual-1-pane"},
		{name: "trailing hyphen", raw: "manual-", want: "manual-"},
		{name: "issue-like numeric suffix", raw: "debug-12", wantErr: true},
		{name: "uppercase", raw: "Manual", wantErr: true},
		{name: "space", raw: "manual pane", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTUISlug(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeTUISlug(%q) error = nil, want error", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeTUISlug(%q) error = %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeTUISlug(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestManualPaneOptionsForTUIKeepsSingleLinePromptInline(t *testing.T) {
	opts := manualPaneOptionsForTUI("inspect workspace", "inspect-workspace", "codex")

	if opts.Title != "inspect workspace" || opts.Prompt != "inspect workspace" {
		t.Fatalf("single-line title/prompt = %q/%q, want original", opts.Title, opts.Prompt)
	}
	if opts.Body != "" {
		t.Fatalf("single-line body = %q, want empty", opts.Body)
	}
	if opts.Slug != "inspect-workspace" || opts.Agent != "codex" {
		t.Fatalf("slug/agent = %q/%q, want inspect-workspace/codex", opts.Slug, opts.Agent)
	}
}

func TestManualPaneOptionsForTUIMultilinePromptUsesBriefingBody(t *testing.T) {
	prompt := normalizeTUIPrompt("\n  inspect workspace\r\n\ncheck handlers\r")
	opts := manualPaneOptionsForTUI(prompt, "", "claude")

	if opts.Title != "inspect workspace" || opts.Prompt != "inspect workspace" {
		t.Fatalf("multiline title/prompt = %q/%q, want first non-empty line", opts.Title, opts.Prompt)
	}
	if opts.Body != "inspect workspace\n\ncheck handlers" {
		t.Fatalf("multiline body = %q, want normalized full prompt", opts.Body)
	}
}

func TestLaunchManualPaneFromTUIChecksAgentBeforeState(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	err := launchManualPaneFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.LaunchRequest{
		Prompt: "inspect workspace",
		Agent:  "claude",
	})

	if err == nil || !strings.Contains(err.Error(), `agent "claude" is not installed`) {
		t.Fatalf("launchManualPaneFromTUI() error = %v, want missing claude", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".fanout")); !os.IsNotExist(statErr) {
		t.Fatalf(".fanout state was touched before agent validation: %v", statErr)
	}
}

func TestLaunchShellPaneFromTUIRecordsShellState(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	installTUITmuxShim(t, "%77")

	err := launchShellPaneFromTUI(repo, "fanout-test", fanouttui.ShellLaunchRequest{
		TargetPath: repo,
		Root:       true,
	})
	if err != nil {
		t.Fatal(err)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 1 {
		t.Fatalf("state panes = %+v, want one shell pane", store.Panes)
	}
	got := store.Panes[0]
	if got.Kind != state.PaneKindShell || got.Agent != "shell" || got.PaneID != "%77" {
		t.Fatalf("shell state = %+v, want shell kind/agent/pane", got)
	}
	if got.Parent != manualPaneParentRef || got.IssueNum != -1 {
		t.Fatalf("shell identity = %s/%d, want @manual/-1", got.Parent, got.IssueNum)
	}
	if got.WorktreePath != repo || got.DisplayName != "root terminal" || got.Slug != "terminal-root-1" {
		t.Fatalf("shell path/name/slug = %q/%q/%q", got.WorktreePath, got.DisplayName, got.Slug)
	}

	body, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{".fanout/state.json", ".fanout/state.json.lock"} {
		if !strings.Contains(string(body), pattern) {
			t.Fatalf("git exclude missing %q:\n%s", pattern, body)
		}
	}
}

func initTUITestGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}

func installTUITmuxShim(t *testing.T, paneID string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  split-window)
    printf '%s\n' "$TMUX_SHIM_PANE_ID"
    ;;
  select-pane|set-option|select-layout|kill-pane)
    ;;
  *)
    ;;
esac
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_SHIM_PANE_ID", paneID)
}
