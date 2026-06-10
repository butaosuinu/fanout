// Package settings resolves fanout's opinionated-behavior switches from
// layered defaults, config files, environment variables, and CLI overrides.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	repoConfigRelPath = ".fanout/config.json"
	userConfigRelPath = "fanout/config.json"
)

// Settings contains the fully resolved behavior switches.
type Settings struct {
	AutoPullRequest    bool
	PRReviewGate       bool
	BriefingCodeReview bool
	AgentTeamsHint     bool
	PRVisualization    bool
	DashboardKeybind   bool
	Notifications      string
	NtfyURL            string
	SlackWebhookURL    string
}

// CLIOverrides holds tri-state command-line overrides. nil means the flag was
// not specified, so lower-priority layers remain in effect.
type CLIOverrides struct {
	AutoPullRequest    *bool
	PRReviewGate       *bool
	BriefingCodeReview *bool
	AgentTeamsHint     *bool
	PRVisualization    *bool
	DashboardKeybind   *bool
}

type overrides struct {
	AutoPullRequest    *bool
	PRReviewGate       *bool
	BriefingCodeReview *bool
	AgentTeamsHint     *bool
	PRVisualization    *bool
	DashboardKeybind   *bool
	Notifications      *string
	NtfyURL            *string
	SlackWebhookURL    *string
}

// WarnFunc receives tolerant-parse diagnostics. Nil suppresses warnings.
type WarnFunc func(format string, a ...any)

// Defaults returns the built-in settings. All switches default to true for
// backwards compatibility.
func Defaults() Settings {
	return Settings{
		AutoPullRequest:    true,
		PRReviewGate:       true,
		BriefingCodeReview: true,
		AgentTeamsHint:     true,
		PRVisualization:    true,
		DashboardKeybind:   true,
		Notifications:      "bell",
	}
}

// Resolve overlays settings in priority order:
// builtin < user file < repo file < environment < CLI.
func Resolve(projectRoot string, cli CLIOverrides, warnf WarnFunc) Settings {
	out := Defaults()
	apply(&out, loadFile(UserConfigPath(), warnf))
	if projectRoot != "" {
		apply(&out, repoOverrides(RepoConfigPath(projectRoot), warnf))
	}
	apply(&out, envOverrides(warnf))
	apply(&out, cliOverrides(cli))
	return out
}

// UserConfigPath returns the global fanout config path. XDG_CONFIG_HOME wins;
// otherwise fanout uses ~/.config/fanout/config.json.
func UserConfigPath() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, userConfigRelPath)
	}
	home := os.Getenv("HOME")
	if home == "" {
		return userConfigRelPath
	}
	return filepath.Join(home, ".config", userConfigRelPath)
}

// RepoConfigPath returns the repository-scoped fanout config path.
func RepoConfigPath(projectRoot string) string {
	return filepath.Join(projectRoot, repoConfigRelPath)
}

func apply(s *Settings, o overrides) {
	if o.AutoPullRequest != nil {
		s.AutoPullRequest = *o.AutoPullRequest
	}
	if o.PRReviewGate != nil {
		s.PRReviewGate = *o.PRReviewGate
	}
	if o.BriefingCodeReview != nil {
		s.BriefingCodeReview = *o.BriefingCodeReview
	}
	if o.AgentTeamsHint != nil {
		s.AgentTeamsHint = *o.AgentTeamsHint
	}
	if o.PRVisualization != nil {
		s.PRVisualization = *o.PRVisualization
	}
	if o.DashboardKeybind != nil {
		s.DashboardKeybind = *o.DashboardKeybind
	}
	if o.Notifications != nil {
		s.Notifications = *o.Notifications
	}
	if o.NtfyURL != nil {
		s.NtfyURL = *o.NtfyURL
	}
	if o.SlackWebhookURL != nil {
		s.SlackWebhookURL = *o.SlackWebhookURL
	}
}

func cliOverrides(cli CLIOverrides) overrides {
	return overrides{
		AutoPullRequest:    cli.AutoPullRequest,
		PRReviewGate:       cli.PRReviewGate,
		BriefingCodeReview: cli.BriefingCodeReview,
		AgentTeamsHint:     cli.AgentTeamsHint,
		PRVisualization:    cli.PRVisualization,
		DashboardKeybind:   cli.DashboardKeybind,
	}
}

func repoOverrides(path string, warnf WarnFunc) overrides {
	out := loadFile(path, warnf)
	if out.NtfyURL != nil {
		warn(warnf, "settings %s: ntfyURL is ignored in repo config; use user config or FANOUT_NTFY_URL", path)
		out.NtfyURL = nil
	}
	if out.SlackWebhookURL != nil {
		warn(warnf, "settings %s: slackWebhookURL is ignored in repo config; use user config or FANOUT_SLACK_WEBHOOK_URL", path)
		out.SlackWebhookURL = nil
	}
	if out.Notifications != nil {
		out.Notifications = repoSafeNotifications(path, *out.Notifications, warnf)
	}
	return out
}

func repoSafeNotifications(path, raw string, warnf WarnFunc) *string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	if len(parts) == 0 {
		return nil
	}
	safe := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		ch := strings.ToLower(strings.TrimSpace(part))
		if ch == "" || seen[ch] {
			continue
		}
		switch ch {
		case "ntfy", "slack":
			warn(warnf, "settings %s: notification channel %q is ignored in repo config; use user config or FANOUT_NOTIFICATIONS", path, ch)
		case "none":
			none := "none"
			return &none
		case "bell", "tmux":
			seen[ch] = true
			safe = append(safe, ch)
		default:
			warn(warnf, "settings %s: notification channel %q is ignored in repo config; allowed repo channels are bell, tmux, none", path, ch)
		}
	}
	if len(safe) == 0 {
		return nil
	}
	value := strings.Join(safe, ",")
	return &value
}

func loadFile(path string, warnf WarnFunc) overrides {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			warn(warnf, "settings %s: read failed: %v (ignored)", path, err)
		}
		return overrides{}
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		warn(warnf, "settings %s: parse failed: %v (ignored)", path, err)
		return overrides{}
	}
	if root == nil {
		warn(warnf, "settings %s: top-level JSON must be an object (ignored)", path)
		return overrides{}
	}

	var out overrides
	boolKeys := map[string]func(*bool){
		"autoPullRequest":    func(v *bool) { out.AutoPullRequest = v },
		"prReviewGate":       func(v *bool) { out.PRReviewGate = v },
		"briefingCodeReview": func(v *bool) { out.BriefingCodeReview = v },
		"agentTeamsHint":     func(v *bool) { out.AgentTeamsHint = v },
		"prVisualization":    func(v *bool) { out.PRVisualization = v },
		"dashboardKeybind":   func(v *bool) { out.DashboardKeybind = v },
	}
	stringKeys := map[string]func(*string){
		"notifications":   func(v *string) { out.Notifications = v },
		"ntfyURL":         func(v *string) { out.NtfyURL = v },
		"slackWebhookURL": func(v *string) { out.SlackWebhookURL = v },
	}
	for key, raw := range root {
		if set, ok := boolKeys[key]; ok {
			if strings.TrimSpace(string(raw)) == "null" {
				warn(warnf, "settings %s: %s must be a boolean (ignored)", path, key)
				continue
			}
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				warn(warnf, "settings %s: %s must be a boolean (ignored)", path, key)
				continue
			}
			set(&v)
			continue
		}
		if set, ok := stringKeys[key]; ok {
			if strings.TrimSpace(string(raw)) == "null" {
				warn(warnf, "settings %s: %s must be a string (ignored)", path, key)
				continue
			}
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				warn(warnf, "settings %s: %s must be a string (ignored)", path, key)
				continue
			}
			set(&v)
			continue
		}
		warn(warnf, "settings %s: unknown key %q (ignored)", path, key)
	}
	return out
}

func envOverrides(warnf WarnFunc) overrides {
	var out overrides
	read := func(name string, set func(*bool)) {
		raw, ok := os.LookupEnv(name)
		if !ok {
			return
		}
		v, ok := parseBool(raw)
		if !ok {
			warn(warnf, "settings env %s: invalid boolean %q (expected 1/true/yes/on or 0/false/no/off; ignored)", name, raw)
			return
		}
		set(&v)
	}
	read("FANOUT_AUTO_PR", func(v *bool) { out.AutoPullRequest = v })
	read("FANOUT_PR_REVIEW_GATE", func(v *bool) { out.PRReviewGate = v })
	read("FANOUT_BRIEFING_CODE_REVIEW", func(v *bool) { out.BriefingCodeReview = v })
	read("FANOUT_AGENT_TEAMS_HINT", func(v *bool) { out.AgentTeamsHint = v })
	read("FANOUT_PR_VISUALIZATION", func(v *bool) { out.PRVisualization = v })
	read("FANOUT_DASHBOARD_KEYBIND", func(v *bool) { out.DashboardKeybind = v })
	readString := func(name string, set func(*string)) {
		raw, ok := os.LookupEnv(name)
		if !ok {
			return
		}
		set(&raw)
	}
	readString("FANOUT_NOTIFICATIONS", func(v *string) { out.Notifications = v })
	readString("FANOUT_NTFY_URL", func(v *string) { out.NtfyURL = v })
	readString("FANOUT_SLACK_WEBHOOK_URL", func(v *string) { out.SlackWebhookURL = v })
	return out
}

func parseBool(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func warn(warnf WarnFunc, format string, a ...any) {
	if warnf != nil {
		warnf(format, a...)
	}
}
