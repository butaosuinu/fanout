package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/settings"
)

func TestNewWatchLaunchConfigDisablesResolvedCodexPlanMode(t *testing.T) {
	resolved := settings.Defaults()
	resolved.CodexPlanMode = true

	cfg := newWatchLaunchConfig(resolved, 123, 2)

	if cfg.CodexPlanMode == nil || *cfg.CodexPlanMode {
		t.Fatalf("CodexPlanMode = %v, want explicit false watcher override", cfg.CodexPlanMode)
	}
}

func TestTUIIssueProjectAndWatcherLaunchesRejectHerdrBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		launch func(string) error
	}{
		{
			name: "standalone issue",
			launch: func(repo string) error {
				cfg := newWatchLaunchConfig(settings.Defaults(), 425, 0)
				_, err := launchStandaloneIssuePaneWithResult(repo, "fanout-test", "fanout", cfg, settings.Defaults(), hooks.EmptyConfig(), ghissue.Issue{Number: 425, Title: "standalone"})
				return err
			},
		},
		{
			name: "parent issue",
			launch: func(repo string) error {
				cfg := newWatchLaunchConfig(settings.Defaults(), 425, 0)
				_, err := launchParentIssueFanoutWithResult(repo, "fanout-test", "fanout", cfg, nil)
				return err
			},
		},
		{
			name: "Project",
			launch: func(repo string) error {
				cfg := &cliflags.Config{
					ParentRef:  "https://github.com/orgs/example/projects/7",
					ParentMode: cliflags.ModeProject,
					Agent:      "claude",
				}
				_, err := launchParentIssueFanoutWithResult(repo, "fanout-test", "fanout", cfg, nil)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			initTUITestGitRepo(t, repo)
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("TMUX", "nested-tmux")
			t.Setenv("FANOUT_BACKEND", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			excludePath := filepath.Join(repo, ".git", "info", "exclude")
			excludeBefore, err := os.ReadFile(excludePath)
			if err != nil {
				t.Fatal(err)
			}

			err = tt.launch(repo)
			if err == nil || !strings.Contains(err.Error(), "runtime backend herdr does not support") {
				t.Fatalf("launch error = %v, want herdr unsupported", err)
			}
			if _, statErr := os.Stat(filepath.Join(repo, ".fanout")); !os.IsNotExist(statErr) {
				t.Fatalf("herdr rejection mutated .fanout: %v", statErr)
			}
			excludeAfter, err := os.ReadFile(excludePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(excludeAfter) != string(excludeBefore) {
				t.Fatalf("git exclude changed before herdr rejection:\n%s", excludeAfter)
			}
		})
	}
}

func TestNewTUIWatcherRejectsHerdrBeforeGitHubLabelMutation(t *testing.T) {
	tests := []struct {
		name       string
		herdrEnv   string
		tmuxEnv    string
		userConfig bool
	}{
		{name: "nested herdr context", herdrEnv: "1", tmuxEnv: "nested-tmux"},
		{name: "user config", userConfig: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			gitCmdTest(t, repo, "init", "-b", "main")
			t.Setenv("HERDR_ENV", tt.herdrEnv)
			t.Setenv("TMUX", tt.tmuxEnv)
			t.Setenv("FANOUT_BACKEND", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if tt.userConfig {
				path := settings.UserConfigPath()
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{"runtimeBackend":"herdr"}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ghCalls := installTUIWatcherGHScript(t, `
printf 'unexpected gh call: %s\n' "$args" >&2
exit 64
`)
			resolved := settings.Defaults()
			resolved.Watcher = true

			watcher, _, _, err := newTUIWatcher(repo, "fanout-test", "fanout", resolved, hooks.EmptyConfig())
			if err == nil || !strings.Contains(err.Error(), "runtime backend herdr does not support") {
				t.Fatalf("newTUIWatcher() = (%T, %v), want herdr unsupported", watcher, err)
			}
			if watcher != nil {
				t.Fatalf("watcher = %T, want nil", watcher)
			}
			if body, readErr := os.ReadFile(ghCalls); readErr == nil && len(body) > 0 {
				t.Fatalf("GitHub calls before herdr rejection:\n%s", body)
			} else if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if _, statErr := os.Stat(filepath.Join(repo, ".fanout")); !os.IsNotExist(statErr) {
				t.Fatalf("watcher herdr preflight mutated .fanout: %v", statErr)
			}
		})
	}
}
