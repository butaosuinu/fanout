package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		{name: "kebab", raw: "  manual-pane-1  ", want: "manual-pane-1"},
		{name: "trailing hyphen", raw: "manual-", want: "manual-"},
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

func TestLaunchManualPaneFromTUIChecksAgentBeforeState(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	err := launchManualPaneFromTUI(repo, "fanout-test", "fanout", fanouttui.LaunchRequest{
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
