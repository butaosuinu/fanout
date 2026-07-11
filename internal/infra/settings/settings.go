// Package settings resolves fanout settings from layered defaults, config
// files, environment variables, and CLI overrides.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
)

const (
	repoConfigRelPath = ".fanout/config.json"
	userConfigRelPath = "fanout/config.json"
)

// Settings contains the fully resolved settings.
type Settings struct {
	AutoPullRequest        bool
	PRReviewGate           bool
	BriefingCodeReview     bool
	AgentTeamsHint         bool
	CodexPlanMode          bool
	PRVisualization        bool
	DashboardKeybind       bool
	ConsoleKeybind         bool
	Watcher                bool
	WatcherTriggerLabel    string
	WatcherRunningLabel    string
	WatcherIntervalSeconds int
	WatcherAgent           string
	WatcherMaxSessions     int
	Notifications          string
	NtfyURL                string
	SlackWebhookURL        string
}

// ConfigScope selects which JSON config file is edited.
type ConfigScope string

const (
	ConfigScopeUser ConfigScope = "user"
	ConfigScopeRepo ConfigScope = "repo"
)

// ValueKind is the JSON scalar kind for one settings key.
type ValueKind string

const (
	ValueBool   ValueKind = "bool"
	ValueString ValueKind = "string"
	ValueInt    ValueKind = "int"
)

// ConfigKey describes one key accepted by fanout config.json files.
type ConfigKey struct {
	Key          string
	Group        string
	Label        string
	Kind         ValueKind
	Env          string
	Default      string
	RepoEditable bool
	Sensitive    bool
}

// ConfigValue is one nullable config.json value. A zero ConfigValue means the
// key is inherited from lower-priority settings.
type ConfigValue struct {
	Bool   *bool
	String *string
	Int    *int
}

// EditableConfig is a parsed config file ready for a UI editor.
type EditableConfig struct {
	Path   string
	Values map[string]ConfigValue
}

// CLIOverrides holds tri-state command-line overrides. nil means the flag was
// not specified, so lower-priority layers remain in effect.
type CLIOverrides struct {
	AutoPullRequest    *bool
	PRReviewGate       *bool
	BriefingCodeReview *bool
	AgentTeamsHint     *bool
	CodexPlanMode      *bool
	PRVisualization    *bool
	DashboardKeybind   *bool
}

type overrides struct {
	AutoPullRequest        *bool
	PRReviewGate           *bool
	BriefingCodeReview     *bool
	AgentTeamsHint         *bool
	CodexPlanMode          *bool
	PRVisualization        *bool
	DashboardKeybind       *bool
	ConsoleKeybind         *bool
	Watcher                *bool
	WatcherTriggerLabel    *string
	WatcherRunningLabel    *string
	WatcherIntervalSeconds *int
	WatcherAgent           *string
	WatcherMaxSessions     *int
	Notifications          *string
	NtfyURL                *string
	SlackWebhookURL        *string
}

var configKeys = []ConfigKey{
	{Key: "autoPullRequest", Group: "Briefing", Label: "PR auto-create", Kind: ValueBool, Env: "FANOUT_AUTO_PR", Default: "true", RepoEditable: true},
	{Key: "prReviewGate", Group: "Briefing", Label: "PR review gate", Kind: ValueBool, Env: "FANOUT_PR_REVIEW_GATE", Default: "true", RepoEditable: true},
	{Key: "briefingCodeReview", Group: "Briefing", Label: "Claude code review", Kind: ValueBool, Env: "FANOUT_BRIEFING_CODE_REVIEW", Default: "true", RepoEditable: true},
	{Key: "agentTeamsHint", Group: "Briefing", Label: "Agent Teams hint", Kind: ValueBool, Env: "FANOUT_AGENT_TEAMS_HINT", Default: "true", RepoEditable: true},
	{Key: "prVisualization", Group: "Briefing", Label: "PR visualization", Kind: ValueBool, Env: "FANOUT_PR_VISUALIZATION", Default: "true", RepoEditable: true},
	{Key: "codexPlanMode", Group: "Launch", Label: "Codex child Plan Mode", Kind: ValueBool, Env: "FANOUT_CODEX_PLAN_MODE", Default: "false", RepoEditable: true},
	{Key: "dashboardKeybind", Group: "TUI", Label: "Dashboard keybind", Kind: ValueBool, Env: "FANOUT_DASHBOARD_KEYBIND", Default: "true", RepoEditable: true},
	{Key: "consoleKeybind", Group: "TUI", Label: "Console keybind", Kind: ValueBool, Env: "FANOUT_CONSOLE_KEYBIND", Default: "true", RepoEditable: true},
	{Key: "watcher", Group: "Watcher", Label: "Watcher", Kind: ValueBool, Env: "FANOUT_WATCHER", Default: "false", RepoEditable: false},
	{Key: "watcherTriggerLabel", Group: "Watcher", Label: "Trigger label", Kind: ValueString, Env: "FANOUT_WATCHER_TRIGGER_LABEL", Default: "fanout:auto", RepoEditable: true},
	{Key: "watcherRunningLabel", Group: "Watcher", Label: "Running label", Kind: ValueString, Env: "FANOUT_WATCHER_RUNNING_LABEL", Default: "fanout:running", RepoEditable: true},
	{Key: "watcherIntervalSeconds", Group: "Watcher", Label: "Interval seconds", Kind: ValueInt, Env: "FANOUT_WATCHER_INTERVAL_SECONDS", Default: "60", RepoEditable: true},
	{Key: "watcherAgent", Group: "Watcher", Label: "Child agent", Kind: ValueString, Env: "FANOUT_WATCHER_AGENT", Default: "", RepoEditable: true},
	{Key: "watcherMaxSessions", Group: "Watcher", Label: "Max sessions", Kind: ValueInt, Env: "FANOUT_WATCHER_MAX_SESSIONS", Default: "4", RepoEditable: true},
	{Key: "notifications", Group: "Notifications", Label: "Channels", Kind: ValueString, Env: "FANOUT_NOTIFICATIONS", Default: "bell", RepoEditable: true},
	{Key: "ntfyURL", Group: "Notifications", Label: "ntfy URL", Kind: ValueString, Env: "FANOUT_NTFY_URL", Default: "", RepoEditable: false, Sensitive: true},
	{Key: "slackWebhookURL", Group: "Notifications", Label: "Slack webhook", Kind: ValueString, Env: "FANOUT_SLACK_WEBHOOK_URL", Default: "", RepoEditable: false, Sensitive: true},
}

// WarnFunc receives tolerant-parse diagnostics. Nil suppresses warnings.
type WarnFunc func(format string, a ...any)

// Defaults returns the built-in settings.
func Defaults() Settings {
	return Settings{
		AutoPullRequest:        true,
		PRReviewGate:           true,
		BriefingCodeReview:     true,
		AgentTeamsHint:         true,
		PRVisualization:        true,
		DashboardKeybind:       true,
		ConsoleKeybind:         true,
		WatcherTriggerLabel:    "fanout:auto",
		WatcherRunningLabel:    "fanout:running",
		WatcherIntervalSeconds: 60,
		WatcherMaxSessions:     4,
		Notifications:          "bell",
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

// ConfigKeys returns the config.json keys in UI display order.
func ConfigKeys() []ConfigKey {
	out := make([]ConfigKey, len(configKeys))
	copy(out, configKeys)
	return out
}

// Path returns the config file path for the scope.
func (s ConfigScope) Path(projectRoot string) string {
	if s == ConfigScopeRepo {
		return RepoConfigPath(projectRoot)
	}
	return UserConfigPath()
}

// LoadEditable loads the selected config file. Missing files are treated as an
// empty config.
func LoadEditable(projectRoot string, scope ConfigScope) (EditableConfig, error) {
	path := scope.Path(projectRoot)
	raw, err := readConfigRaw(path)
	if err != nil {
		return EditableConfig{}, err
	}
	values := make(map[string]ConfigValue, len(configKeys))
	for _, spec := range configKeys {
		value, ok := parseConfigValue(spec, raw[spec.Key])
		if ok {
			values[spec.Key] = value
		}
	}
	return EditableConfig{Path: path, Values: values}, nil
}

// SaveEditable writes the selected config file, preserving unknown keys.
// Values absent from the map are left untouched; present zero values delete the
// corresponding known key so it inherits from lower-priority layers.
func SaveEditable(projectRoot string, scope ConfigScope, values map[string]ConfigValue) (string, error) {
	path := scope.Path(projectRoot)
	raw, err := readConfigRaw(path)
	if err != nil {
		return path, err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	specs := configKeyMap()
	for key, value := range values {
		spec, ok := specs[key]
		if !ok {
			return path, fmt.Errorf("unknown settings key %q", key)
		}
		if err := validateEditableValue(scope, spec, value); err != nil {
			return path, err
		}
		if value.Empty() {
			delete(raw, key)
			continue
		}
		encoded, err := encodeConfigValue(spec, value)
		if err != nil {
			return path, err
		}
		raw[key] = encoded
	}
	return path, atomicfs.WriteJSON(path, raw, configFileMode(scope))
}

// Empty reports whether the key should be inherited.
func (v ConfigValue) Empty() bool {
	return v.Bool == nil && v.String == nil && v.Int == nil
}

// BoolValue returns a bool config value.
func BoolValue(v bool) ConfigValue {
	return ConfigValue{Bool: &v}
}

// StringValue returns a string config value.
func StringValue(v string) ConfigValue {
	return ConfigValue{String: &v}
}

// IntValue returns an int config value.
func IntValue(v int) ConfigValue {
	return ConfigValue{Int: &v}
}

func configFileMode(scope ConfigScope) os.FileMode {
	if scope == ConfigScopeRepo {
		return 0o644
	}
	return 0o600
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
	if o.CodexPlanMode != nil {
		s.CodexPlanMode = *o.CodexPlanMode
	}
	if o.PRVisualization != nil {
		s.PRVisualization = *o.PRVisualization
	}
	if o.DashboardKeybind != nil {
		s.DashboardKeybind = *o.DashboardKeybind
	}
	if o.ConsoleKeybind != nil {
		s.ConsoleKeybind = *o.ConsoleKeybind
	}
	if o.Watcher != nil {
		s.Watcher = *o.Watcher
	}
	if o.WatcherTriggerLabel != nil {
		s.WatcherTriggerLabel = *o.WatcherTriggerLabel
	}
	if o.WatcherRunningLabel != nil {
		s.WatcherRunningLabel = *o.WatcherRunningLabel
	}
	if o.WatcherIntervalSeconds != nil {
		s.WatcherIntervalSeconds = clampWatcherIntervalSeconds(*o.WatcherIntervalSeconds)
	}
	if o.WatcherAgent != nil {
		s.WatcherAgent = *o.WatcherAgent
	}
	if o.WatcherMaxSessions != nil {
		s.WatcherMaxSessions = *o.WatcherMaxSessions
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
		CodexPlanMode:      cli.CodexPlanMode,
		PRVisualization:    cli.PRVisualization,
		DashboardKeybind:   cli.DashboardKeybind,
	}
}

func repoOverrides(path string, warnf WarnFunc) overrides {
	out := loadFile(path, warnf)
	if out.Watcher != nil {
		warn(warnf, "settings %s: watcher is ignored in repo config; use user config or FANOUT_WATCHER", path)
		out.Watcher = nil
	}
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
	parts := notificationChannelParts(raw)
	if len(parts) == 0 {
		return nil
	}
	safe := make([]string, 0, len(parts))
	for _, ch := range parts {
		switch ch {
		case "none":
			none := "none"
			return &none
		case "bell", "tmux":
			safe = append(safe, ch)
		case "ntfy", "slack":
			warn(warnf, "settings %s: notification channel %q is ignored in repo config; use user config or FANOUT_NOTIFICATIONS", path, ch)
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

func notificationChannelParts(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		ch := strings.ToLower(strings.TrimSpace(part))
		if ch == "" || seen[ch] {
			continue
		}
		seen[ch] = true
		out = append(out, ch)
	}
	return out
}

func normalizeNotificationChannels(raw string, repo bool) (string, error) {
	parts := notificationChannelParts(raw)
	if len(parts) == 0 {
		return "", nil
	}
	out := make([]string, 0, len(parts))
	for _, ch := range parts {
		switch ch {
		case "none":
			return "none", nil
		case "bell", "tmux":
			out = append(out, ch)
		case "ntfy", "slack":
			if repo {
				return "", fmt.Errorf("notification channel %q is not allowed in repo config", ch)
			}
			out = append(out, ch)
		default:
			return "", fmt.Errorf("unknown notification channel %q", ch)
		}
	}
	return strings.Join(out, ","), nil
}

func readConfigRaw(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		return nil, fmt.Errorf("parse %s: top-level JSON must be an object", path)
	}
	return root, nil
}

func parseConfigValue(spec ConfigKey, raw json.RawMessage) (ConfigValue, bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return ConfigValue{}, false
	}
	switch spec.Kind {
	case ValueBool:
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return ConfigValue{}, false
		}
		return BoolValue(v), true
	case ValueString:
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return ConfigValue{}, false
		}
		return StringValue(v), true
	case ValueInt:
		var v int
		if err := json.Unmarshal(raw, &v); err != nil {
			return ConfigValue{}, false
		}
		return IntValue(v), true
	default:
		return ConfigValue{}, false
	}
}

func configKeyMap() map[string]ConfigKey {
	out := make(map[string]ConfigKey, len(configKeys))
	for _, spec := range configKeys {
		out[spec.Key] = spec
	}
	return out
}

func validateEditableValue(scope ConfigScope, spec ConfigKey, value ConfigValue) error {
	if value.Empty() {
		return nil
	}
	if scope == ConfigScopeRepo && !spec.RepoEditable {
		return fmt.Errorf("%s cannot be set in repo config", spec.Key)
	}
	switch spec.Kind {
	case ValueBool:
		if value.Bool == nil || value.String != nil || value.Int != nil {
			return fmt.Errorf("%s must be a boolean", spec.Key)
		}
	case ValueString:
		if value.String == nil || value.Bool != nil || value.Int != nil {
			return fmt.Errorf("%s must be a string", spec.Key)
		}
	case ValueInt:
		if value.Int == nil || value.Bool != nil || value.String != nil {
			return fmt.Errorf("%s must be an integer", spec.Key)
		}
	default:
		return fmt.Errorf("%s has unknown value kind %q", spec.Key, spec.Kind)
	}
	if spec.Key == "notifications" && value.String != nil {
		normalized, err := normalizeNotificationChannels(*value.String, scope == ConfigScopeRepo)
		if err != nil {
			return err
		}
		*value.String = normalized
	}
	if spec.Key == "watcherIntervalSeconds" && value.Int != nil && *value.Int < 20 {
		return fmt.Errorf("watcherIntervalSeconds must be at least 20")
	}
	if spec.Key == "watcherMaxSessions" && value.Int != nil && *value.Int < 0 {
		return fmt.Errorf("watcherMaxSessions must be 0 or greater")
	}
	return nil
}

func encodeConfigValue(spec ConfigKey, value ConfigValue) (json.RawMessage, error) {
	var (
		data []byte
		err  error
	)
	switch spec.Kind {
	case ValueBool:
		data, err = json.Marshal(*value.Bool)
	case ValueString:
		data, err = json.Marshal(*value.String)
	case ValueInt:
		data, err = json.Marshal(*value.Int)
	default:
		err = fmt.Errorf("unknown value kind %q", spec.Kind)
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
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
		"codexPlanMode":      func(v *bool) { out.CodexPlanMode = v },
		"prVisualization":    func(v *bool) { out.PRVisualization = v },
		"dashboardKeybind":   func(v *bool) { out.DashboardKeybind = v },
		"consoleKeybind":     func(v *bool) { out.ConsoleKeybind = v },
		"watcher":            func(v *bool) { out.Watcher = v },
	}
	stringKeys := map[string]func(*string){
		"watcherTriggerLabel": func(v *string) { out.WatcherTriggerLabel = v },
		"watcherRunningLabel": func(v *string) { out.WatcherRunningLabel = v },
		"watcherAgent":        func(v *string) { out.WatcherAgent = v },
		"notifications":       func(v *string) { out.Notifications = v },
		"ntfyURL":             func(v *string) { out.NtfyURL = v },
		"slackWebhookURL":     func(v *string) { out.SlackWebhookURL = v },
	}
	intKeys := map[string]func(*int){
		"watcherIntervalSeconds": func(v *int) { out.WatcherIntervalSeconds = v },
		"watcherMaxSessions":     func(v *int) { out.WatcherMaxSessions = v },
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
		if set, ok := intKeys[key]; ok {
			if strings.TrimSpace(string(raw)) == "null" {
				warn(warnf, "settings %s: %s must be an integer (ignored)", path, key)
				continue
			}
			var v int
			if err := json.Unmarshal(raw, &v); err != nil {
				warn(warnf, "settings %s: %s must be an integer (ignored)", path, key)
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
	read("FANOUT_CODEX_PLAN_MODE", func(v *bool) { out.CodexPlanMode = v })
	read("FANOUT_PR_VISUALIZATION", func(v *bool) { out.PRVisualization = v })
	read("FANOUT_DASHBOARD_KEYBIND", func(v *bool) { out.DashboardKeybind = v })
	read("FANOUT_CONSOLE_KEYBIND", func(v *bool) { out.ConsoleKeybind = v })
	read("FANOUT_WATCHER", func(v *bool) { out.Watcher = v })
	readString := func(name string, set func(*string)) {
		raw, ok := os.LookupEnv(name)
		if !ok {
			return
		}
		set(&raw)
	}
	readString("FANOUT_WATCHER_TRIGGER_LABEL", func(v *string) { out.WatcherTriggerLabel = v })
	readString("FANOUT_WATCHER_RUNNING_LABEL", func(v *string) { out.WatcherRunningLabel = v })
	readString("FANOUT_WATCHER_AGENT", func(v *string) { out.WatcherAgent = v })
	readString("FANOUT_NOTIFICATIONS", func(v *string) { out.Notifications = v })
	readString("FANOUT_NTFY_URL", func(v *string) { out.NtfyURL = v })
	readString("FANOUT_SLACK_WEBHOOK_URL", func(v *string) { out.SlackWebhookURL = v })
	readInt := func(name string, set func(*int)) {
		raw, ok := os.LookupEnv(name)
		if !ok {
			return
		}
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			warn(warnf, "settings env %s: invalid integer %q (ignored)", name, raw)
			return
		}
		set(&v)
	}
	readInt("FANOUT_WATCHER_INTERVAL_SECONDS", func(v *int) { out.WatcherIntervalSeconds = v })
	readInt("FANOUT_WATCHER_MAX_SESSIONS", func(v *int) { out.WatcherMaxSessions = v })
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

func clampWatcherIntervalSeconds(v int) int {
	if v < 20 {
		return 20
	}
	return v
}

func warn(warnf WarnFunc, format string, a ...any) {
	if warnf != nil {
		warnf(format, a...)
	}
}
