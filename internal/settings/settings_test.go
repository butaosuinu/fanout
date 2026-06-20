package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		PRVisualization:        false,
		DashboardKeybind:       true,
		WatcherTriggerLabel:    "fanout:auto",
		WatcherRunningLabel:    "fanout:running",
		WatcherIntervalSeconds: 60,
		WatcherMaxSessions:     4,
		Notifications:          "bell,ntfy",
		NtfyURL:                "https://ntfy-user.example/topic",
		SlackWebhookURL:        "https://hooks.example/slack",
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
		"FANOUT_PR_VISUALIZATION",
		"FANOUT_DASHBOARD_KEYBIND",
		"FANOUT_WATCHER",
		"FANOUT_WATCHER_TRIGGER_LABEL",
		"FANOUT_WATCHER_RUNNING_LABEL",
		"FANOUT_WATCHER_INTERVAL_SECONDS",
		"FANOUT_WATCHER_AGENT",
		"FANOUT_WATCHER_MAX_SESSIONS",
		"FANOUT_NOTIFICATIONS",
		"FANOUT_NTFY_URL",
		"FANOUT_SLACK_WEBHOOK_URL",
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
