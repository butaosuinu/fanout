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
  "agentTeamsHint": false
}`)
	writeConfig(t, RepoConfigPath(repo), `{
  "autoPullRequest": true,
  "prReviewGate": true,
  "agentTeamsHint": true
}`)
	t.Setenv("FANOUT_AUTO_PR", "off")
	t.Setenv("FANOUT_PR_REVIEW_GATE", "0")

	got := Resolve(repo, CLIOverrides{AutoPullRequest: boolp(true)}, t.Fatalf)

	want := Settings{
		AutoPullRequest:    true,
		PRReviewGate:       false,
		BriefingCodeReview: false,
		AgentTeamsHint:     true,
	}
	if got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
}

func TestResolveWarnsAndIgnoresInvalidInputs(t *testing.T) {
	repo := t.TempDir()
	setEmptyUserConfig(t)
	clearEnv(t)
	writeConfig(t, RepoConfigPath(repo), `{
  "autoPullRequest": "nope",
  "prReviewGate": null,
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
	assertWarningContains(t, warnings, "unknown key \"unknownKey\"")
	assertWarningContains(t, warnings, "settings env FANOUT_AGENT_TEAMS_HINT: invalid boolean \"sometimes\"")
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
	} {
		old, hadOld := os.LookupEnv(name)
		os.Unsetenv(name)
		t.Cleanup(func() {
			if hadOld {
				_ = os.Setenv(name, old)
			} else {
				_ = os.Unsetenv(name)
			}
		})
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

func boolp(v bool) *bool {
	return &v
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
