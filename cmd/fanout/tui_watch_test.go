package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestNewWatchLaunchConfigUsesResolvedChildPlanMode(t *testing.T) {
	resolved := settings.Defaults()
	resolved.ChildPlanMode = true

	cfg := newWatchLaunchConfig(resolved, 123, 2)

	if cfg.PlanMode == nil || !*cfg.PlanMode {
		t.Fatalf("PlanMode = %v, want child Plan Mode watcher override", cfg.PlanMode)
	}
}

func TestNewWatcherPreflightConfigCarriesAgentAndPlanMode(t *testing.T) {
	resolved := settings.Defaults()
	resolved.WatcherAgent = "codex"
	resolved.ChildPlanMode = true

	cfg := newWatcherPreflightConfig(resolved)

	if cfg.ParentRef != tuiWatcherPreflightRef || cfg.Agent != "codex" ||
		cfg.PlanMode == nil || !*cfg.PlanMode {
		t.Fatalf("watcher preflight config = %+v, want parent, agent, and Plan Mode", cfg)
	}
}

func TestAdmitStandaloneIssueRuntimeDefersBackendForRecordedIssue(t *testing.T) {
	prepareCalls := 0
	rt := &run.Runtime{PrepareBackend: func() error {
		prepareCalls++
		return nil
	}}
	err := admitStandaloneIssueRuntime(t.TempDir(), &cliflags.Config{ParentRef: "425"}, rt, state.Store{
		Panes: []state.Pane{{Parent: panelaunch.WatchParentRef, IssueNum: 425}},
	}, 425)
	if !errors.Is(err, watch.ErrAlreadyFanned) || prepareCalls != 0 {
		t.Fatalf("admit recorded issue = %v, prepare calls %d; want already-fanned without ownership", err, prepareCalls)
	}
	err = admitStandaloneIssueRuntime(t.TempDir(), &cliflags.Config{
		ParentRef: "425", Agent: "unknown", DryRun: true,
	}, rt, state.Store{}, 425)
	if err == nil || !strings.Contains(err.Error(), "unknown agent") || prepareCalls != 0 {
		t.Fatalf("admit invalid agent = %v, prepare calls %d; want validation before ownership", err, prepareCalls)
	}
}

func TestLaunchStandaloneIssuePaneReportsClaudeModeFallback(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	installTUITmuxShim(t, "%91")
	binDir := t.TempDir()
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nprintf '2.1.206 (Claude Code)\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	resolved := settings.Defaults()
	resolved.ChildPlanMode = true
	cfg := newWatchLaunchConfig(resolved, 425, 0)
	result, err := launchStandaloneIssuePaneWithResult(repo, "fanout-test", "fanout", cfg, resolved, hooks.EmptyConfig(), ghissue.Issue{
		Number: 425,
		Title:  "standalone",
		Body:   "body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Notice, "Claude Code 2.1.207+ is required for explicit plan mode") {
		t.Fatalf("notice = %q, want Claude Plan Mode fallback warning", result.Notice)
	}
	store, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(store.Panes) != 1 || store.Panes[0].PlanMode {
		t.Fatalf("state panes = %+v, want one effective non-plan standalone pane", store.Panes)
	}
}

func TestWatcherParentCandidateFetchesGitHubDataOnce(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	installTUISequentialTmuxShim(t, repo)
	argsPath := installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[{"number":501,"title":"ready","state":"open"},{"number":502,"title":"blocked","state":"open"}]]'
  ;;
"issue view 500 --json body -q .body")
  printf '%s\n' '- [ ] #501 ready' '- [ ] #502 blocked'
  ;;
"issue view 501 --json body,labels")
  printf '{"body":"","labels":[]}'
  ;;
"issue view 502 --json body,labels")
  printf '%s' '{"body":"## Blocked by\n- #600","labels":[]}'
  ;;
"issue view 600 --json state -q .state")
  printf 'OPEN\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	issue := ghissue.Issue{Number: 500, Title: "parent", State: "OPEN"}
	runner := ghissue.Runner{Cwd: repo}
	var listedLabels []string
	engine := watch.NewEngine(watch.Config{
		TriggerLabel: "fanout:auto",
		RunningLabel: "fanout:running",
	}, watch.IO{
		ListLabeled: func(label string) ([]ghissue.Issue, error) {
			listedLabels = append(listedLabels, label)
			if label == "fanout:auto" {
				return []ghissue.Issue{issue}, nil
			}
			return nil, nil
		},
		PlanChildren: func(issue ghissue.Issue) (watch.ChildPlan, error) {
			return newWatchParentChildPlan(repo, "fanout-test", "fanout", settings.Defaults(), &tuiWatcher{}, runner, issue)
		},
		SwapLabels: func(ghissue.Issue, string, string) error {
			return nil
		},
		LoadState: func() (state.Store, error) {
			return state.LoadProject(repo)
		},
		PaneAlive: func(state.Pane) (bool, error) {
			return false, nil
		},
		LaunchStandalone: func(ghissue.Issue) error {
			return nil
		},
	})
	report, err := engine.RunCycle()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Launched) != 1 || len(report.Deferred) != 1 || report.Deferred[0].Issue.Number != 500 {
		t.Fatalf("report = %+v, want one launched and deferred parent", report)
	}
	if got := strings.Join(listedLabels, ","); got != "fanout:auto,fanout:running" {
		t.Fatalf("listed labels = %q, want one trigger and one running query", got)
	}

	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(body)
	for _, want := range []string{
		"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100",
		"issue view 500 --json body -q .body",
		"issue view 501 --json body,labels",
		"issue view 502 --json body,labels",
		"issue view 600 --json state -q .state",
	} {
		if got := strings.Count(calls, want+"\n"); got != 1 {
			t.Fatalf("gh call %q count = %d, want 1\nall calls:\n%s", want, got, calls)
		}
	}
}

func TestTUIIssueAndProjectHerdrLaunchFailuresDoNotPersistPanes(t *testing.T) {
	tests := []struct {
		name   string
		launch func(string) error
	}{
		{
			name: "standalone issue",
			launch: func(repo string) error {
				cfg := tuiIssueLaunchConfig(425, "claude", nil)
				_, err := launchStandaloneIssuePaneWithResult(repo, "fanout-test", "fanout", cfg, settings.Defaults(), hooks.EmptyConfig(), ghissue.Issue{Number: 425, Title: "standalone"})
				return err
			},
		},
		{
			name: "parent issue",
			launch: func(repo string) error {
				cfg := tuiIssueLaunchConfig(425, "claude", nil)
				_, err := launchParentIssueFanoutWithResult(repo, "fanout-test", "fanout", cfg, nil)
				return err
			},
		},
		{
			name: "Project",
			launch: func(repo string) error {
				cfg := &cliflags.Config{
					ParentRef:      "https://github.com/orgs/example/projects/7",
					ParentMode:     cliflags.ModeProject,
					Agent:          "claude",
					TUIInteractive: true,
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
			isolateBackendEnv(t)
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("TMUX", "nested-tmux")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			err := tt.launch(repo)
			if err == nil {
				t.Fatal("launch succeeded without a usable Herdr or GitHub test environment")
			}
			if store, loadErr := state.Load(state.Path(repo)); loadErr != nil || len(store.Panes) != 0 {
				t.Fatalf("missing-session rejection persisted panes: store=%+v err=%v", store, loadErr)
			}
		})
	}
}

func TestWatcherLaunchConfigAllowsHerdrSelection(t *testing.T) {
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
			isolateBackendEnv(t)
			t.Setenv("HERDR_ENV", tt.herdrEnv)
			t.Setenv("TMUX", tt.tmuxEnv)
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
			resolved := settings.Defaults()
			resolved.Watcher = true
			cfg := newWatchLaunchConfig(resolved, 425, 0)
			cfg.DryRun = true
			selection, err := resolveLaunchBackend(cfg, repo, state.Store{}, nil)
			if err != nil || selection.Selection.Name != backend.Herdr {
				t.Fatalf("watcher Herdr selection = (%+v, %v)", selection.Selection, err)
			}
		})
	}
}
