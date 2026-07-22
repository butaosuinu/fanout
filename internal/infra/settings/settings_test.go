package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

func TestResolveDefaultsWhenFilesAndEnvAreAbsent(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)

	got := Resolve(repo, CLIOverrides{}, t.Fatalf)

	if got != Defaults() {
		t.Fatalf("Resolve() = %#v, want defaults %#v", got, Defaults())
	}
}

func TestResolvePriorityCLIEnvRepoUserBuiltin(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, filepath.Join(xdg, "fanout", "config.json"), `{
  "autoPullRequest": false,
  "briefingCodeReview": false,
  "agentTeamsHint": false,
  "prVisualization": true,
  "notifications": "tmux",
  "ntfyURL": "https://ntfy-user.example/topic"
}`)
	writeConfig(t, RepoConfigPath(repo), `{
  "autoPullRequest": true,
  "prReviewGate": true,
  "agentTeamsHint": true,
  "prVisualization": false,
  "notifications": "bell"
}`)
	t.Setenv("FANOUT_AUTO_PR", "off")
	t.Setenv("FANOUT_PR_REVIEW_GATE", "0")
	t.Setenv("FANOUT_PR_VISUALIZATION", "yes")
	t.Setenv("FANOUT_NOTIFICATIONS", "bell,ntfy")
	t.Setenv("FANOUT_SLACK_WEBHOOK_URL", "https://hooks.example/slack")

	got := Resolve(repo, CLIOverrides{
		AutoPullRequest: new(true),
		PRVisualization: new(false),
	}, t.Fatalf)

	want := Settings{
		AutoPullRequest:        true,
		PRReviewGate:           false,
		BriefingCodeReview:     false,
		AgentTeamsHint:         true,
		NewSessionPlanMode:     true,
		OrchestratorPlanMode:   true,
		PRVisualization:        false,
		DashboardKeybind:       true,
		ConsoleKeybind:         true,
		WatcherTriggerLabel:    "fanout:auto",
		WatcherRunningLabel:    "fanout:running",
		WatcherIntervalSeconds: 60,
		WatcherMaxSessions:     4,
		Notifications:          "bell,ntfy",
		NtfyURL:                "https://ntfy-user.example/topic",
		SlackWebhookURL:        "https://hooks.example/slack",
		RuntimeBackend:         backend.Tmux,
		RuntimeBackendSource:   RuntimeBackendSourceDefault,
	}
	if got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}

	got = Resolve(repo, CLIOverrides{}, t.Fatalf)
	if !got.PRVisualization {
		t.Fatalf("PRVisualization = false, want true from CLI-free env override")
	}

	if err := os.Unsetenv("FANOUT_PR_VISUALIZATION"); err != nil {
		t.Fatalf("Unsetenv(FANOUT_PR_VISUALIZATION): %v", err)
	}
	if err := os.Unsetenv("FANOUT_NOTIFICATIONS"); err != nil {
		t.Fatalf("Unsetenv(FANOUT_NOTIFICATIONS): %v", err)
	}
	got = Resolve(repo, CLIOverrides{}, t.Fatalf)
	if got.PRVisualization {
		t.Fatalf("PRVisualization = true, want false from repo override")
	}
	if got.Notifications != "bell" {
		t.Fatalf("Notifications = %q, want repo override", got.Notifications)
	}
	if got.NtfyURL != "https://ntfy-user.example/topic" {
		t.Fatalf("NtfyURL = %q, want user override", got.NtfyURL)
	}
}

func TestResolveCodexPlanModePriority(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	userPath := filepath.Join(xdg, "fanout", "config.json")
	writeConfig(t, userPath, `{"codexPlanMode": true}`)
	t.Setenv("FANOUT_CODEX_PLAN_MODE", "on")

	got := Resolve(repo, CLIOverrides{CodexPlanMode: new(false)}, t.Fatalf)
	if got.CodexPlanMode {
		t.Fatal("CodexPlanMode = true, want CLI override false")
	}

	got = Resolve(repo, CLIOverrides{}, t.Fatalf)
	if !got.CodexPlanMode {
		t.Fatal("CodexPlanMode = false, want environment override true")
	}

	if err := os.Unsetenv("FANOUT_CODEX_PLAN_MODE"); err != nil {
		t.Fatalf("Unsetenv(FANOUT_CODEX_PLAN_MODE): %v", err)
	}
	got = Resolve(repo, CLIOverrides{CodexPlanMode: new(true)}, t.Fatalf)
	if !got.CodexPlanMode {
		t.Fatal("CodexPlanMode = false, want CLI override true over repo false")
	}

	got = Resolve(repo, CLIOverrides{}, t.Fatalf)
	if !got.CodexPlanMode {
		t.Fatal("CodexPlanMode = false, want user override true")
	}

	if err := os.Remove(userPath); err != nil {
		t.Fatalf("Remove(%q): %v", userPath, err)
	}
	got = Resolve(repo, CLIOverrides{}, t.Fatalf)
	if got.CodexPlanMode {
		t.Fatal("CodexPlanMode = true, want built-in default false")
	}
}

func TestResolvePlanModeSettingsPriority(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	userPath := filepath.Join(xdg, "fanout", "config.json")
	writeConfig(t, userPath, `{
  "newSessionPlanMode": false,
  "orchestratorPlanMode": false,
  "childPlanMode": true
}`)
	writeConfig(t, RepoConfigPath(repo), `{
  "newSessionPlanMode": true,
  "orchestratorPlanMode": true,
  "childPlanMode": false
}`)
	t.Setenv("FANOUT_NEW_SESSION_PLAN_MODE", "on")
	t.Setenv("FANOUT_CHILD_PLAN_MODE", "off")

	got := Resolve(repo, CLIOverrides{}, nil)
	if !got.NewSessionPlanMode || got.OrchestratorPlanMode || got.ChildPlanMode {
		t.Fatalf("plan mode settings = new:%t orchestrator:%t child:%t, want env/user/env true/false/false",
			got.NewSessionPlanMode, got.OrchestratorPlanMode, got.ChildPlanMode)
	}

	if err := os.Unsetenv("FANOUT_NEW_SESSION_PLAN_MODE"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("FANOUT_CHILD_PLAN_MODE"); err != nil {
		t.Fatal(err)
	}
	got = Resolve(repo, CLIOverrides{}, nil)
	if got.NewSessionPlanMode || got.OrchestratorPlanMode || !got.ChildPlanMode {
		t.Fatalf("plan mode settings = new:%t orchestrator:%t child:%t, want user false/false/true",
			got.NewSessionPlanMode, got.OrchestratorPlanMode, got.ChildPlanMode)
	}

	if err := os.Remove(userPath); err != nil {
		t.Fatal(err)
	}
	got = Resolve(repo, CLIOverrides{}, nil)
	if !got.NewSessionPlanMode || !got.OrchestratorPlanMode || got.ChildPlanMode {
		t.Fatalf("plan mode settings = new:%t orchestrator:%t child:%t, want defaults true/true/false",
			got.NewSessionPlanMode, got.OrchestratorPlanMode, got.ChildPlanMode)
	}
}

func TestRepoConfigCannotOverridePlanModeSettings(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, filepath.Join(xdg, "fanout", "config.json"), `{
  "codexPlanMode": true,
  "newSessionPlanMode": false,
  "orchestratorPlanMode": false,
  "childPlanMode": true
}`)
	path := RepoConfigPath(repo)
	writeConfig(t, path, `{
  "codexPlanMode": false,
  "newSessionPlanMode": true,
  "orchestratorPlanMode": true,
  "childPlanMode": false
}`)

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})
	if !got.CodexPlanMode || got.NewSessionPlanMode || got.OrchestratorPlanMode || !got.ChildPlanMode {
		t.Fatalf("plan mode settings = codex:%t new:%t orchestrator:%t child:%t, want user values true/false/false/true",
			got.CodexPlanMode, got.NewSessionPlanMode, got.OrchestratorPlanMode, got.ChildPlanMode)
	}
	wantWarnings := []string{
		fmt.Sprintf("settings %s: codexPlanMode is ignored in repo config; use user config, FANOUT_CODEX_PLAN_MODE, or --codex-plan-mode", path),
		fmt.Sprintf("settings %s: newSessionPlanMode is ignored in repo config; use user config or FANOUT_NEW_SESSION_PLAN_MODE", path),
		fmt.Sprintf("settings %s: orchestratorPlanMode is ignored in repo config; use user config or FANOUT_ORCHESTRATOR_PLAN_MODE", path),
		fmt.Sprintf("settings %s: childPlanMode is ignored in repo config; use user config or FANOUT_CHILD_PLAN_MODE", path),
	}
	if len(warnings) != len(wantWarnings) {
		t.Fatalf("warnings = %#v, want %#v", warnings, wantWarnings)
	}
	for i, want := range wantWarnings {
		if warnings[i] != want {
			t.Fatalf("warnings[%d] = %q, want %q", i, warnings[i], want)
		}
	}
}

func TestResolveRuntimeBackendPriorityAndSource(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	userPath := filepath.Join(xdg, "fanout", "config.json")
	repoPath := RepoConfigPath(repo)
	writeConfig(t, userPath, `{"runtimeBackend": "herdr"}`)
	writeConfig(t, repoPath, `{"runtimeBackend": "tmux"}`)
	t.Setenv("FANOUT_BACKEND", "tmux")
	cliBackend := backend.Herdr

	got := Resolve(repo, CLIOverrides{RuntimeBackend: &cliBackend}, nil)
	if got.RuntimeBackend != backend.Herdr || got.RuntimeBackendSource != RuntimeBackendSourceCLI {
		t.Fatalf("CLI RuntimeBackend = %q from %q, want herdr from CLI", got.RuntimeBackend, got.RuntimeBackendSource)
	}

	got = Resolve(repo, CLIOverrides{}, nil)
	if got.RuntimeBackend != backend.Tmux || got.RuntimeBackendSource != RuntimeBackendSourceEnvironment {
		t.Fatalf("env RuntimeBackend = %q from %q, want tmux from environment", got.RuntimeBackend, got.RuntimeBackendSource)
	}

	if err := os.Unsetenv("FANOUT_BACKEND"); err != nil {
		t.Fatal(err)
	}
	got = Resolve(repo, CLIOverrides{}, nil)
	if got.RuntimeBackend != backend.Herdr || got.RuntimeBackendSource != RuntimeBackendSourceUserConfig {
		t.Fatalf("user RuntimeBackend = %q from %q, want herdr from user config", got.RuntimeBackend, got.RuntimeBackendSource)
	}

	if err := os.Remove(userPath); err != nil {
		t.Fatal(err)
	}
	got = Resolve(repo, CLIOverrides{}, nil)
	if got.RuntimeBackend != backend.Tmux || got.RuntimeBackendSource != RuntimeBackendSourceDefault {
		t.Fatalf("default RuntimeBackend = %q from %q, want tmux from default", got.RuntimeBackend, got.RuntimeBackendSource)
	}
}

func TestResolveInvalidRuntimeBackendValuesWarnsAndIgnores(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, filepath.Join(xdg, "fanout", "config.json"), `{"runtimeBackend": "screen"}`)
	t.Setenv("FANOUT_BACKEND", "wezterm")
	invalidCLI := backend.Name("zellij")

	var warnings []string
	got := Resolve(repo, CLIOverrides{RuntimeBackend: &invalidCLI}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})
	if got.RuntimeBackend != backend.Tmux || got.RuntimeBackendSource != RuntimeBackendSourceDefault {
		t.Fatalf("RuntimeBackend = %q from %q, want default tmux", got.RuntimeBackend, got.RuntimeBackendSource)
	}
	for _, want := range []string{
		"runtimeBackend: unknown runtime backend \"screen\"",
		"FANOUT_BACKEND: unknown runtime backend \"wezterm\"",
		"CLI runtimeBackend: unknown runtime backend \"zellij\"",
	} {
		assertWarningContains(t, warnings, want)
	}
}

func TestResolveEmptyRuntimeBackendEnvironmentIsUnset(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	t.Setenv("FANOUT_BACKEND", "  ")

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})
	if got.RuntimeBackend != backend.Tmux || got.RuntimeBackendSource != RuntimeBackendSourceDefault {
		t.Fatalf("RuntimeBackend = %q from %q, want default tmux", got.RuntimeBackend, got.RuntimeBackendSource)
	}
	if len(warnings) != 0 {
		t.Fatalf("empty FANOUT_BACKEND warnings = %v, want none", warnings)
	}
}

func TestRepoConfigCannotSelectRuntimeBackend(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, filepath.Join(xdg, "fanout", "config.json"), `{"runtimeBackend": "herdr"}`)
	writeConfig(t, RepoConfigPath(repo), `{"runtimeBackend": "tmux"}`)

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})
	if got.RuntimeBackend != backend.Herdr || got.RuntimeBackendSource != RuntimeBackendSourceUserConfig {
		t.Fatalf("RuntimeBackend = %q from %q, want user herdr", got.RuntimeBackend, got.RuntimeBackendSource)
	}
	assertWarningContains(t, warnings, "runtimeBackend is ignored in repo config")
}

func TestResolveWatcherSettingsPriority(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, filepath.Join(xdg, "fanout", "config.json"), `{
  "watcher": true,
  "watcherTriggerLabel": "fanout:user",
  "watcherRunningLabel": "fanout:user-running",
  "watcherIntervalSeconds": 45,
  "watcherAgent": "claude",
  "watcherMaxSessions": 2
}`)
	writeConfig(t, RepoConfigPath(repo), `{
  "watcher": false,
  "watcherTriggerLabel": "fanout:repo",
  "watcherRunningLabel": "fanout:repo-running",
  "watcherIntervalSeconds": 30,
  "watcherAgent": "codex",
  "watcherMaxSessions": 3
}`)
	t.Setenv("FANOUT_WATCHER", "off")
	t.Setenv("FANOUT_WATCHER_TRIGGER_LABEL", "fanout:env")
	t.Setenv("FANOUT_WATCHER_INTERVAL_SECONDS", "10")
	t.Setenv("FANOUT_WATCHER_MAX_SESSIONS", "0")

	got := Resolve(repo, CLIOverrides{}, nil)

	if got.Watcher {
		t.Fatalf("Watcher = true, want env override false")
	}
	if got.WatcherTriggerLabel != "fanout:env" {
		t.Fatalf("WatcherTriggerLabel = %q, want env override", got.WatcherTriggerLabel)
	}
	if got.WatcherRunningLabel != "fanout:repo-running" {
		t.Fatalf("WatcherRunningLabel = %q, want repo override", got.WatcherRunningLabel)
	}
	if got.WatcherIntervalSeconds != 20 {
		t.Fatalf("WatcherIntervalSeconds = %d, want env override clamped to 20", got.WatcherIntervalSeconds)
	}
	if got.WatcherAgent != "codex" {
		t.Fatalf("WatcherAgent = %q, want repo override", got.WatcherAgent)
	}
	if got.WatcherMaxSessions != 0 {
		t.Fatalf("WatcherMaxSessions = %d, want env override", got.WatcherMaxSessions)
	}

	for _, name := range []string{
		"FANOUT_WATCHER",
		"FANOUT_WATCHER_TRIGGER_LABEL",
		"FANOUT_WATCHER_INTERVAL_SECONDS",
		"FANOUT_WATCHER_MAX_SESSIONS",
	} {
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("Unsetenv(%s): %v", name, err)
		}
	}
	got = Resolve(repo, CLIOverrides{}, nil)
	if !got.Watcher {
		t.Fatalf("Watcher = false, want user config because repo watcher is ignored")
	}
	if got.WatcherTriggerLabel != "fanout:repo" {
		t.Fatalf("WatcherTriggerLabel = %q, want repo override", got.WatcherTriggerLabel)
	}
	if got.WatcherIntervalSeconds != 30 {
		t.Fatalf("WatcherIntervalSeconds = %d, want repo override", got.WatcherIntervalSeconds)
	}
	if got.WatcherMaxSessions != 3 {
		t.Fatalf("WatcherMaxSessions = %d, want repo override", got.WatcherMaxSessions)
	}
}

func TestRepoConfigCannotEnableWatcher(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, RepoConfigPath(repo), `{
  "watcher": true
}`)

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})

	if got.Watcher {
		t.Fatal("Watcher = true, want repo config watcher ignored")
	}
	assertWarningContains(t, warnings, "watcher is ignored in repo config")
}

func TestRepoConfigCannotEnableHTTPNotifications(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, RepoConfigPath(repo), `{
  "notifications": "bell,slack,ntfy",
  "ntfyURL": "https://ntfy-repo.example/topic",
  "slackWebhookURL": "https://hooks.example/repo"
}`)

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})

	if got.Notifications != "bell" {
		t.Fatalf("Notifications = %q, want only repo-safe bell", got.Notifications)
	}
	if got.NtfyURL != "" || got.SlackWebhookURL != "" {
		t.Fatalf("HTTP URLs = %q/%q, want ignored from repo config", got.NtfyURL, got.SlackWebhookURL)
	}
	assertWarningContains(t, warnings, "notification channel \"slack\" is ignored in repo config")
	assertWarningContains(t, warnings, "notification channel \"ntfy\" is ignored in repo config")
	assertWarningContains(t, warnings, "ntfyURL is ignored in repo config")
	assertWarningContains(t, warnings, "slackWebhookURL is ignored in repo config")
}

func TestRepoConfigIgnoresUnknownNotificationSelectors(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, filepath.Join(xdg, "fanout", "config.json"), `{
  "notifications": "slack",
  "slackWebhookURL": "https://hooks.example/user"
}`)
	writeConfig(t, RepoConfigPath(repo), `{
  "notifications": "email"
}`)

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})

	if got.Notifications != "slack" {
		t.Fatalf("Notifications = %q, want user setting preserved", got.Notifications)
	}
	if got.SlackWebhookURL != "https://hooks.example/user" {
		t.Fatalf("SlackWebhookURL = %q, want user setting preserved", got.SlackWebhookURL)
	}
	assertWarningContains(t, warnings, "notification channel \"email\" is ignored in repo config")
}

func TestResolveWarnsAndIgnoresInvalidInputs(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, RepoConfigPath(repo), `{
  "autoPullRequest": "nope",
  "prReviewGate": null,
  "prVisualization": 42,
  "notifications": false,
  "ntfyURL": null,
  "unknownKey": true
}`)
	t.Setenv("FANOUT_AGENT_TEAMS_HINT", "sometimes")

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})

	if got != Defaults() {
		t.Fatalf("Resolve() = %#v, want defaults %#v", got, Defaults())
	}
	assertWarningContains(t, warnings, "autoPullRequest must be a boolean")
	assertWarningContains(t, warnings, "prReviewGate must be a boolean")
	assertWarningContains(t, warnings, "prVisualization must be a boolean")
	assertWarningContains(t, warnings, "notifications must be a string")
	assertWarningContains(t, warnings, "ntfyURL must be a string")
	assertWarningContains(t, warnings, "unknown key \"unknownKey\"")
	assertWarningContains(t, warnings, "settings env FANOUT_AGENT_TEAMS_HINT: invalid boolean \"sometimes\"")
}

func TestResolveWarnsAndIgnoresInvalidCodexPlanMode(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, RepoConfigPath(repo), `{"codexPlanMode": "yes"}`)
	t.Setenv("FANOUT_CODEX_PLAN_MODE", "sometimes")

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})

	if got.CodexPlanMode {
		t.Fatal("CodexPlanMode = true, want invalid inputs ignored")
	}
	assertWarningContains(t, warnings, "codexPlanMode must be a boolean")
	assertWarningContains(t, warnings, "settings env FANOUT_CODEX_PLAN_MODE: invalid boolean \"sometimes\"")
}

func TestResolveWarnsAndIgnoresInvalidWatcherInts(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, filepath.Join(xdg, "fanout", "config.json"), `{
  "watcherIntervalSeconds": "fast",
  "watcherMaxSessions": null
}`)
	t.Setenv("FANOUT_WATCHER_INTERVAL_SECONDS", "soon")
	t.Setenv("FANOUT_WATCHER_MAX_SESSIONS", "many")

	var warnings []string
	got := Resolve(repo, CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	})

	if got.WatcherIntervalSeconds != 60 {
		t.Fatalf("WatcherIntervalSeconds = %d, want default", got.WatcherIntervalSeconds)
	}
	if got.WatcherMaxSessions != 4 {
		t.Fatalf("WatcherMaxSessions = %d, want default", got.WatcherMaxSessions)
	}
	assertWarningContains(t, warnings, "watcherIntervalSeconds must be an integer")
	assertWarningContains(t, warnings, "watcherMaxSessions must be an integer")
	assertWarningContains(t, warnings, "settings env FANOUT_WATCHER_INTERVAL_SECONDS: invalid integer \"soon\"")
	assertWarningContains(t, warnings, "settings env FANOUT_WATCHER_MAX_SESSIONS: invalid integer \"many\"")
}

func TestResolveConsoleKeybindPriority(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, filepath.Join(xdg, "fanout", "config.json"), `{
  "consoleKeybind": true
}`)
	writeConfig(t, RepoConfigPath(repo), `{
  "consoleKeybind": false
}`)

	got := Resolve(repo, CLIOverrides{}, t.Fatalf)
	if got.ConsoleKeybind {
		t.Fatal("ConsoleKeybind = true, want repo config override false")
	}

	t.Setenv("FANOUT_CONSOLE_KEYBIND", "on")
	got = Resolve(repo, CLIOverrides{}, t.Fatalf)
	if !got.ConsoleKeybind {
		t.Fatal("ConsoleKeybind = false, want env override true")
	}
}

func TestResolveClampsWatcherIntervalSeconds(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	t.Setenv("FANOUT_WATCHER_INTERVAL_SECONDS", "5")

	got := Resolve(repo, CLIOverrides{}, t.Fatalf)

	if got.WatcherIntervalSeconds != 20 {
		t.Fatalf("WatcherIntervalSeconds = %d, want 20", got.WatcherIntervalSeconds)
	}
}

func TestUserConfigPathHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/fanout-xdg")

	got := UserConfigPath()
	want := "/tmp/fanout-xdg/fanout/config.json"
	if got != want {
		t.Fatalf("UserConfigPath() = %q, want %q", got, want)
	}
}

func TestSaveEditablePreservesUnknownKeysAndDeletesInheritedValues(t *testing.T) {
	repo := t.TempDir()
	xdg := setEmptyUserConfig(t)
	clearEnv(t)
	path := filepath.Join(xdg, "fanout", "config.json")
	writeConfig(t, path, `{
  "custom": "keep",
  "autoPullRequest": false,
  "notifications": "ntfy",
  "ntfyURL": "https://ntfy.example/topic"
}`)

	gotPath, err := SaveEditable(repo, ConfigScopeUser, map[string]ConfigValue{
		"autoPullRequest":        {},
		"prReviewGate":           BoolValue(false),
		"watcherIntervalSeconds": IntValue(20),
		"notifications":          StringValue("tmux bell"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Fatalf("SaveEditable path = %q, want %q", gotPath, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if unmarshalErr := json.Unmarshal(body, &root); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if root["custom"] != "keep" {
		t.Fatalf("custom key = %#v, want preserved", root["custom"])
	}
	if _, ok := root["autoPullRequest"]; ok {
		t.Fatalf("autoPullRequest should have been deleted for inherit:\n%s", body)
	}
	if root["prReviewGate"] != false {
		t.Fatalf("prReviewGate = %#v, want false", root["prReviewGate"])
	}
	if root["notifications"] != "tmux,bell" {
		t.Fatalf("notifications = %#v, want normalized tmux,bell", root["notifications"])
	}
	if root["ntfyURL"] != "https://ntfy.example/topic" {
		t.Fatalf("ntfyURL = %#v, want preserved untouched", root["ntfyURL"])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("user config mode = %o, want 600", got)
	}
}

func TestSaveEditableWritesRepoConfigWorldReadable(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	path := RepoConfigPath(repo)

	gotPath, err := SaveEditable(repo, ConfigScopeRepo, map[string]ConfigValue{
		"autoPullRequest": BoolValue(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Fatalf("SaveEditable path = %q, want %q", gotPath, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("repo config mode = %o, want 644", got)
	}
}

func TestRuntimeBackendConfigMetadataAndUserValidation(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)

	var (
		gotSpec ConfigKey
		found   bool
	)
	for _, spec := range ConfigKeys() {
		if spec.Key == "runtimeBackend" {
			gotSpec = spec
			found = true
			break
		}
	}
	if !found {
		t.Fatal("ConfigKeys() does not contain runtimeBackend")
	}
	wantSpec := ConfigKey{
		Key:          "runtimeBackend",
		Group:        "Launch",
		Label:        "Runtime backend",
		Kind:         ValueString,
		Env:          "FANOUT_BACKEND",
		Default:      "tmux",
		RepoEditable: false,
	}
	if gotSpec != wantSpec {
		t.Fatalf("runtimeBackend ConfigKey = %#v, want %#v", gotSpec, wantSpec)
	}

	path, err := SaveEditable(repo, ConfigScopeUser, map[string]ConfigValue{
		"runtimeBackend": StringValue("herdr"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := Resolve(repo, CLIOverrides{}, t.Fatalf)
	if got.RuntimeBackend != backend.Herdr || got.RuntimeBackendSource != RuntimeBackendSourceUserConfig {
		t.Fatalf("saved RuntimeBackend = %q from %q, want user herdr", got.RuntimeBackend, got.RuntimeBackendSource)
	}

	if _, err = SaveEditable(repo, ConfigScopeUser, map[string]ConfigValue{
		"runtimeBackend": StringValue("screen"),
	}); err == nil {
		t.Fatal("SaveEditable(user invalid runtimeBackend) = nil, want error")
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(body), `"runtimeBackend": "herdr"`) {
		t.Fatalf("valid runtimeBackend changed after rejected save:\n%s", body)
	}
}

func TestCodexPlanModeConfigMetadataAndUserRoundTrip(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)

	var (
		gotSpec ConfigKey
		found   bool
	)
	for _, spec := range ConfigKeys() {
		if spec.Key == "codexPlanMode" {
			gotSpec = spec
			found = true
			break
		}
	}
	if !found {
		t.Fatal("ConfigKeys() does not contain codexPlanMode")
	}
	wantSpec := ConfigKey{
		Key:          "codexPlanMode",
		Group:        "Launch",
		Label:        "Codex child Plan Mode",
		Kind:         ValueBool,
		Env:          "FANOUT_CODEX_PLAN_MODE",
		Default:      "false",
		RepoEditable: false,
	}
	if gotSpec != wantSpec {
		t.Fatalf("codexPlanMode ConfigKey = %#v, want %#v", gotSpec, wantSpec)
	}
	if _, err := SaveEditable(repo, ConfigScopeRepo, map[string]ConfigValue{
		"codexPlanMode": BoolValue(true),
	}); err == nil {
		t.Fatal("SaveEditable(repo codexPlanMode) = nil, want unsafe-setting rejection")
	}

	path, err := SaveEditable(repo, ConfigScopeUser, map[string]ConfigValue{
		"codexPlanMode": BoolValue(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != UserConfigPath() {
		t.Fatalf("SaveEditable path = %q, want %q", path, UserConfigPath())
	}
	loaded, err := LoadEditable(repo, ConfigScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := loaded.Values["codexPlanMode"]
	if !ok || value.Bool == nil || !*value.Bool {
		t.Fatalf("loaded codexPlanMode = %#v (found=%v), want true", value, ok)
	}
	if got := Resolve(repo, CLIOverrides{}, t.Fatalf); !got.CodexPlanMode {
		t.Fatal("resolved CodexPlanMode = false, want saved repo value true")
	}

	if _, err = SaveEditable(repo, ConfigScopeUser, map[string]ConfigValue{
		"codexPlanMode": {},
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadEditable(repo, ConfigScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok = loaded.Values["codexPlanMode"]; ok {
		t.Fatal("codexPlanMode remains after saving inherit")
	}
}

func TestPlanModeConfigMetadataAndUserRoundTrip(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)

	keys := ConfigKeys()
	runtimeBackendIndex := -1
	for i, spec := range keys {
		if spec.Key == "runtimeBackend" {
			runtimeBackendIndex = i
			break
		}
	}
	if runtimeBackendIndex < 3 {
		t.Fatalf("runtimeBackend index = %d, want room for three preceding Plan Mode keys", runtimeBackendIndex)
	}
	wantSpecs := []ConfigKey{
		{Key: "newSessionPlanMode", Group: "Launch", Label: "New session Plan Mode", Kind: ValueBool, Env: "FANOUT_NEW_SESSION_PLAN_MODE", Default: "true", RepoEditable: false},
		{Key: "orchestratorPlanMode", Group: "Launch", Label: "Orchestrator Plan Mode", Kind: ValueBool, Env: "FANOUT_ORCHESTRATOR_PLAN_MODE", Default: "true", RepoEditable: false},
		{Key: "childPlanMode", Group: "Launch", Label: "Child Plan Mode", Kind: ValueBool, Env: "FANOUT_CHILD_PLAN_MODE", Default: "false", RepoEditable: false},
	}
	for i, want := range wantSpecs {
		got := keys[runtimeBackendIndex-len(wantSpecs)+i]
		if got != want {
			t.Fatalf("Plan Mode ConfigKeys()[%d] = %#v, want %#v", runtimeBackendIndex-len(wantSpecs)+i, got, want)
		}
	}

	path, err := SaveEditable(repo, ConfigScopeUser, map[string]ConfigValue{
		"newSessionPlanMode":   BoolValue(false),
		"orchestratorPlanMode": BoolValue(false),
		"childPlanMode":        BoolValue(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != UserConfigPath() {
		t.Fatalf("SaveEditable path = %q, want %q", path, UserConfigPath())
	}
	got := Resolve(repo, CLIOverrides{}, t.Fatalf)
	if got.NewSessionPlanMode || got.OrchestratorPlanMode || !got.ChildPlanMode {
		t.Fatalf("saved plan mode settings = new:%t orchestrator:%t child:%t, want false/false/true",
			got.NewSessionPlanMode, got.OrchestratorPlanMode, got.ChildPlanMode)
	}
}

func TestSaveEditableRejectsUnsafeRepoSettingsWithoutChangingFile(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	path := RepoConfigPath(repo)
	original := "{\n  \"custom\": \"keep\"\n}\n"
	writeConfig(t, path, original)

	_, err := SaveEditable(repo, ConfigScopeRepo, map[string]ConfigValue{
		"watcher": BoolValue(true),
	})
	if err == nil {
		t.Fatal("SaveEditable(repo watcher) = nil, want error")
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != original {
		t.Fatalf("repo config changed after rejected save:\nwant %q\ngot  %q", original, body)
	}

	_, err = SaveEditable(repo, ConfigScopeRepo, map[string]ConfigValue{
		"notifications": StringValue("bell,slack"),
	})
	if err == nil {
		t.Fatal("SaveEditable(repo slack notifications) = nil, want error")
	}

	_, err = SaveEditable(repo, ConfigScopeRepo, map[string]ConfigValue{
		"runtimeBackend": StringValue("herdr"),
	})
	if err == nil {
		t.Fatal("SaveEditable(repo runtimeBackend) = nil, want error")
	}
}

func setEmptyUserConfig(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	return xdg
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"FANOUT_AUTO_PR",
		"FANOUT_PR_REVIEW_GATE",
		"FANOUT_BRIEFING_CODE_REVIEW",
		"FANOUT_AGENT_TEAMS_HINT",
		"FANOUT_CODEX_PLAN_MODE",
		"FANOUT_NEW_SESSION_PLAN_MODE",
		"FANOUT_ORCHESTRATOR_PLAN_MODE",
		"FANOUT_CHILD_PLAN_MODE",
		"FANOUT_PR_VISUALIZATION",
		"FANOUT_DASHBOARD_KEYBIND",
		"FANOUT_CONSOLE_KEYBIND",
		"FANOUT_WATCHER",
		"FANOUT_WATCHER_TRIGGER_LABEL",
		"FANOUT_WATCHER_RUNNING_LABEL",
		"FANOUT_WATCHER_INTERVAL_SECONDS",
		"FANOUT_WATCHER_AGENT",
		"FANOUT_WATCHER_MAX_SESSIONS",
		"FANOUT_NOTIFICATIONS",
		"FANOUT_NTFY_URL",
		"FANOUT_SLACK_WEBHOOK_URL",
		"FANOUT_BACKEND",
	} {
		// t.Setenv registers restoration of the original value (or unset state);
		// the follow-up Unsetenv leaves the variable unset for the test body.
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func assertWarningContains(t *testing.T, warnings []string, want string) {
	t.Helper()
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return
		}
	}
	t.Fatalf("warnings %#v did not contain %q", warnings, want)
}
