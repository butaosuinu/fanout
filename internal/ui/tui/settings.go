package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	fanoutsettings "github.com/butaosuinu/fanout/internal/infra/settings"
)

const settingsPopupOpeningNotice = "opening settings popup..."

const SettingsEnvOverridesEnv = "FANOUT_TUI_SETTINGS_ENV_OVERRIDES"

// SettingsPopupRequest describes a TUI settings popup launch.
type SettingsPopupRequest struct {
	ProjectRoot string
}

// SettingsPopupResult reports what the popup saved.
type SettingsPopupResult struct {
	Saved bool   `json:"saved,omitempty"`
	Scope string `json:"scope,omitempty"`
	Path  string `json:"path,omitempty"`
}

// SettingsPopupFunc opens settings in an external surface such as tmux popup.
type SettingsPopupFunc func(SettingsPopupRequest) (result SettingsPopupResult, canceled bool, err error)

// SettingsRuntime is the live TUI runtime derived from resolved settings.
type SettingsRuntime struct {
	Watcher             WatcherRunner
	WatchInterval       time.Duration
	WatchLabel          string
	WatcherRunningLabel string
	Notifier            transitionNotifier
	LaunchIssue         IssueLaunchFunc
}

// SettingsReloadFunc refreshes TUI runtime pieces after config changes.
type SettingsReloadFunc func() (SettingsRuntime, error)

// SettingsPopupOptions configures the standalone settings popup program.
type SettingsPopupOptions struct {
	ProjectRoot string
	Width       int
	Height      int
}

type settingsForm struct {
	scope      fanoutsettings.ConfigScope
	path       string
	rows       []settingsRow
	cursor     int // 0 is target; rows start at 1.
	top        int
	editing    bool
	editText   string
	err        string
	loadErr    bool
	loadErrMsg string
}

type settingsRow struct {
	spec        fanoutsettings.ConfigKey
	value       fanoutsettings.ConfigValue
	disabled    bool
	envOverride bool
}

// RunSettingsPopup opens only the settings UI and returns after save/cancel.
func RunSettingsPopup(opts SettingsPopupOptions) (SettingsPopupResult, bool, error) {
	width := opts.Width
	if width <= 0 {
		width = 90
	}
	height := opts.Height
	if height <= 0 {
		height = 24
	}
	m := newModel(Options{ProjectRoot: opts.ProjectRoot})
	m.settingsOnly = true
	m.mode = modeSettings
	m.width = width
	m.height = height
	m.openSettingsForm(fanoutsettings.ConfigScopeUser)
	finalModel, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return SettingsPopupResult{}, false, err
	}
	final, ok := finalModel.(model)
	if !ok {
		return SettingsPopupResult{}, false, fmt.Errorf("unexpected settings popup model %T", finalModel)
	}
	if !final.settingsDone || final.settingsCanceled {
		return SettingsPopupResult{}, true, nil
	}
	return final.settingsResult, false, nil
}

func (m *model) openSettingsForm(scope fanoutsettings.ConfigScope) {
	m.mode = modeSettings
	m.notice = ""
	m.settings = newSettingsForm(m.opts.ProjectRoot, scope)
}

func newSettingsForm(projectRoot string, scope fanoutsettings.ConfigScope) settingsForm {
	if scope != fanoutsettings.ConfigScopeRepo {
		scope = fanoutsettings.ConfigScopeUser
	}
	form := settingsForm{scope: scope}
	cfg, err := fanoutsettings.LoadEditable(projectRoot, scope)
	form.path = cfg.Path
	if form.path == "" {
		form.path = scope.Path(projectRoot)
	}
	specs := fanoutsettings.ConfigKeys()
	form.rows = make([]settingsRow, 0, len(specs))
	for _, spec := range specs {
		value := cfg.Values[spec.Key]
		row := settingsRow{
			spec:        spec,
			value:       value,
			disabled:    scope == fanoutsettings.ConfigScopeRepo && !spec.RepoEditable,
			envOverride: settingsEnvOverridePresent(spec),
		}
		if row.disabled {
			row.value = fanoutsettings.ConfigValue{}
		}
		form.rows = append(form.rows, row)
	}
	if err != nil {
		form.err = err.Error()
		form.loadErr = true
		form.loadErrMsg = err.Error()
	}
	return form
}

func (m *model) openSettingsPopupCmd() tea.Cmd {
	popup := m.opts.SettingsPopup
	if popup == nil {
		m.openSettingsForm(fanoutsettings.ConfigScopeUser)
		return nil
	}
	m.notice = settingsPopupOpeningNotice
	m.settingsPopupOpen = true
	req := SettingsPopupRequest{ProjectRoot: m.opts.ProjectRoot}
	return func() tea.Msg {
		result, canceled, err := popup(req)
		return settingsPopupDoneMsg{result: result, canceled: canceled, err: err}
	}
}

func (m model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.settings.editing {
		return m.updateSettingsEdit(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		if m.settingsOnly {
			m.settingsCanceled = true
			return m.quit()
		}
		return m.quit()
	case "esc", "q":
		if m.settingsOnly {
			m.settingsCanceled = true
			return m.quit()
		}
		m.mode = modeMonitor
		m.settings = settingsForm{}
		return m, nil
	case "ctrl+s":
		return m.saveSettings()
	case "up", "k":
		m.moveSettingsCursor(-1)
		return m, nil
	case "down", "j", "tab":
		m.moveSettingsCursor(1)
		return m, nil
	case "shift+tab":
		m.moveSettingsCursor(-1)
		return m, nil
	case "left", "right", " ":
		if m.settings.cursor == 0 {
			m.switchSettingsScope()
			return m, nil
		}
		m.adjustSettingsRow(msg.String())
		return m, nil
	case "enter":
		if m.settings.cursor == 0 {
			m.switchSettingsScope()
			return m, nil
		}
		m.beginSettingsEdit()
		return m, nil
	}
	return m, nil
}

func (m model) updateSettingsEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.settingsOnly {
			m.settingsCanceled = true
			return m.quit()
		}
		return m.quit()
	case "esc":
		m.settings.editing = false
		m.settings.editText = ""
		return m, nil
	case "enter":
		m.commitSettingsEdit()
		return m, nil
	case "backspace", "ctrl+h":
		if len(m.settings.editText) > 0 {
			runes := []rune(m.settings.editText)
			m.settings.editText = string(runes[:len(runes)-1])
		}
		return m, nil
	case "ctrl+u":
		m.settings.editText = ""
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		m.settings.editText += string(msg.Runes)
	}
	return m, nil
}

func (m *model) moveSettingsCursor(delta int) {
	maxCursor := len(m.settings.rows)
	m.settings.cursor = clampInt(m.settings.cursor+delta, 0, maxCursor)
	visible := m.settingsVisibleRows()
	if visible <= 0 || m.settings.cursor == 0 {
		if m.settings.cursor == 0 {
			m.settings.top = 0
		}
		return
	}
	row := m.settings.cursor - 1
	if row < m.settings.top {
		m.settings.top = row
	}
	if row >= m.settings.top+visible {
		m.settings.top = row - visible + 1
	}
}

func (m *model) switchSettingsScope() {
	next := fanoutsettings.ConfigScopeRepo
	if m.settings.scope == fanoutsettings.ConfigScopeRepo {
		next = fanoutsettings.ConfigScopeUser
	}
	oldCursor := m.settings.cursor
	oldTop := m.settings.top
	m.settings = newSettingsForm(m.opts.ProjectRoot, next)
	m.settings.cursor = clampInt(oldCursor, 0, len(m.settings.rows))
	m.settings.top = clampInt(oldTop, 0, max(len(m.settings.rows)-1, 0))
}

func (m *model) adjustSettingsRow(key string) {
	row := m.currentSettingsRow()
	if row == nil || row.disabled {
		return
	}
	switch row.spec.Kind {
	case fanoutsettings.ValueBool:
		row.value = nextSettingsBoolValue(row.value, key)
	case fanoutsettings.ValueString, fanoutsettings.ValueInt:
		if row.value.Empty() {
			row.value = defaultSettingsValue(row.spec)
		} else if key == " " {
			row.value = fanoutsettings.ConfigValue{}
		}
	}
	m.settings.err = ""
}

func nextSettingsBoolValue(value fanoutsettings.ConfigValue, key string) fanoutsettings.ConfigValue {
	state := 0 // inherit
	if value.Bool != nil {
		if *value.Bool {
			state = 1
		} else {
			state = 2
		}
	}
	switch key {
	case "left":
		state = (state + 2) % 3
	default:
		state = (state + 1) % 3
	}
	switch state {
	case 1:
		return fanoutsettings.BoolValue(true)
	case 2:
		return fanoutsettings.BoolValue(false)
	default:
		return fanoutsettings.ConfigValue{}
	}
}

func (m *model) beginSettingsEdit() {
	row := m.currentSettingsRow()
	if row == nil || row.disabled {
		return
	}
	switch row.spec.Kind {
	case fanoutsettings.ValueBool:
		return
	case fanoutsettings.ValueString, fanoutsettings.ValueInt:
		if row.value.Empty() {
			row.value = defaultSettingsValue(row.spec)
		}
		m.settings.editing = true
		m.settings.editText = settingsValueText(row.value)
		m.settings.err = ""
	}
}

func (m *model) commitSettingsEdit() {
	row := m.currentSettingsRow()
	if row == nil {
		m.settings.editing = false
		return
	}
	text := strings.TrimSpace(m.settings.editText)
	switch row.spec.Kind {
	case fanoutsettings.ValueBool:
		// Boolean rows cycle directly and never enter text editing.
	case fanoutsettings.ValueString:
		row.value = fanoutsettings.StringValue(text)
	case fanoutsettings.ValueInt:
		v, err := strconv.Atoi(text)
		if err != nil {
			m.settings.err = fmt.Sprintf("%s must be an integer", row.spec.Key)
			return
		}
		row.value = fanoutsettings.IntValue(v)
	}
	m.settings.editing = false
	m.settings.editText = ""
	m.settings.err = ""
}

func (m model) saveSettings() (tea.Model, tea.Cmd) {
	if m.settings.loadErr {
		m.settings.err = "fix or remove the invalid config before saving: " + m.settings.loadErrMsg
		return m, nil
	}
	values := map[string]fanoutsettings.ConfigValue{}
	for _, row := range m.settings.rows {
		if row.disabled {
			if m.settings.scope == fanoutsettings.ConfigScopeRepo {
				values[row.spec.Key] = fanoutsettings.ConfigValue{}
			}
			continue
		}
		if m.settings.scope == fanoutsettings.ConfigScopeRepo && row.spec.Key == "notifications" {
			row.value = safeRepoNotificationsValue(row.value)
		}
		values[row.spec.Key] = row.value
	}
	path, err := fanoutsettings.SaveEditable(m.opts.ProjectRoot, m.settings.scope, values)
	if err != nil {
		m.settings.err = err.Error()
		return m, nil
	}
	result := SettingsPopupResult{Saved: true, Scope: string(m.settings.scope), Path: path}
	if m.settingsOnly {
		m.settingsDone = true
		m.settingsResult = result
		return m.quit()
	}
	m.mode = modeMonitor
	m.settings = settingsForm{}
	m.notice = "settings saved: " + displayConfigPath(path)
	return m, m.reloadSettingsCmd(result)
}

func safeRepoNotificationsValue(value fanoutsettings.ConfigValue) fanoutsettings.ConfigValue {
	if value.String == nil {
		return value
	}
	allowed := []string{}
	seen := map[string]bool{}
	for token := range strings.FieldsFuncSeq(*value.String, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		switch token {
		case "none":
			return fanoutsettings.StringValue("none")
		case "bell", "tmux":
			allowed = append(allowed, token)
		}
	}
	if len(allowed) == 0 {
		return fanoutsettings.ConfigValue{}
	}
	return fanoutsettings.StringValue(strings.Join(allowed, " "))
}

func (m model) reloadSettingsCmd(result SettingsPopupResult) tea.Cmd {
	reload := m.opts.ReloadSettings
	if reload == nil {
		return nil
	}
	return func() tea.Msg {
		runtime, err := reload()
		return settingsReloadedMsg{result: result, runtime: runtime, err: err}
	}
}

func (m *model) currentSettingsRow() *settingsRow {
	if m.settings.cursor <= 0 || m.settings.cursor > len(m.settings.rows) {
		return nil
	}
	return &m.settings.rows[m.settings.cursor-1]
}

func defaultSettingsValue(spec fanoutsettings.ConfigKey) fanoutsettings.ConfigValue {
	switch spec.Kind {
	case fanoutsettings.ValueBool:
		return fanoutsettings.BoolValue(spec.Default == "true")
	case fanoutsettings.ValueInt:
		v, _ := strconv.Atoi(spec.Default)
		return fanoutsettings.IntValue(v)
	default:
		return fanoutsettings.StringValue(spec.Default)
	}
}

func settingsValueText(value fanoutsettings.ConfigValue) string {
	switch {
	case value.Bool != nil:
		if *value.Bool {
			return "on"
		}
		return "off"
	case value.String != nil:
		return *value.String
	case value.Int != nil:
		return strconv.Itoa(*value.Int)
	default:
		return "inherit"
	}
}

func (m model) settingsView() string {
	lines := make([]string, 0, 24)
	if !m.settingsOnly {
		lines = append(lines, titleStyle.Render("Settings"))
	}
	lines = append(lines, m.settingsTargetView())
	rows := m.settingsVisibleRows()
	start := clampInt(m.settings.top, 0, max(len(m.settings.rows)-1, 0))
	end := min(start+rows, len(m.settings.rows))
	lastGroup := ""
	for i := start; i < end; i++ {
		row := m.settings.rows[i]
		if row.spec.Group != lastGroup {
			lines = append(lines, dimStyle.Render(row.spec.Group))
			lastGroup = row.spec.Group
		}
		lines = append(lines, m.settingsRowView(i+1, row))
	}
	if end < len(m.settings.rows) {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("... %d more", len(m.settings.rows)-end)))
	}
	if m.settings.err != "" {
		lines = append(lines, errStyle.Render("error: "+m.settings.err))
	}
	lines = append(lines, dimStyle.Render(m.settingsHint()))
	content := strings.Join(lines, "\n")
	if m.settingsOnly {
		return popupContentStyle.Width(m.settingsModalWidth()).Render(content)
	}
	return modalStyle.Width(m.settingsModalWidth()).Render(content)
}

func (m model) settingsTargetView() string {
	marker := "  "
	if m.settings.cursor == 0 {
		marker = "> "
	}
	scope := "User config"
	if m.settings.scope == fanoutsettings.ConfigScopeRepo {
		scope = "Repo config"
	}
	return marker + titleStyle.Render(scope) + "  " + dimStyle.Render(displayConfigPath(m.settings.path))
}

func (m model) settingsRowView(cursor int, row settingsRow) string {
	marker := "  "
	if m.settings.cursor == cursor {
		marker = "> "
	}
	value := settingsDisplayValue(row)
	if m.settings.editing && m.settings.cursor == cursor {
		value = "[" + settingsEditDisplayValue(row, m.settings.editText) + "]"
	}
	if row.disabled {
		value = "repo-disabled"
	}
	env := ""
	if row.envOverride {
		env = " env"
	}
	text := fmt.Sprintf("%-24s %-16s %s", row.spec.Key, value, env)
	if row.disabled {
		return marker + dimStyle.Render(text)
	}
	if m.settings.cursor == cursor {
		return marker + titleStyle.Render(text)
	}
	return marker + text
}

func settingsEnvOverridePresent(spec fanoutsettings.ConfigKey) bool {
	if spec.Env == "" {
		return false
	}
	if _, ok := os.LookupEnv(spec.Env); ok {
		return true
	}
	for env := range strings.SplitSeq(os.Getenv(SettingsEnvOverridesEnv), ",") {
		if strings.TrimSpace(env) == spec.Env {
			return true
		}
	}
	return false
}

func settingsDisplayValue(row settingsRow) string {
	if row.spec.Sensitive && !row.value.Empty() {
		if row.value.String != nil && *row.value.String == "" {
			return "empty"
		}
		return "set"
	}
	return settingsValueText(row.value)
}

func settingsEditDisplayValue(row settingsRow, text string) string {
	if !row.spec.Sensitive {
		return text
	}
	if text == "" {
		return ""
	}
	return strings.Repeat("*", min(len([]rune(text)), 16))
}

func (m model) settingsHint() string {
	if m.settings.editing {
		return "enter apply  esc cancel edit  ctrl+u clear"
	}
	return "up/down row  left/right/space change  enter edit  ctrl+s save  esc cancel"
}

func (m model) settingsModalWidth() int {
	if m.width <= 0 {
		return 90
	}
	if m.settingsOnly {
		return clampInt(m.width, 54, 106)
	}
	return clampInt(m.width-12, 54, 104)
}

func (m model) settingsVisibleRows() int {
	if m.height <= 0 {
		return len(m.settings.rows)
	}
	overhead := 7
	if !m.settingsOnly {
		overhead++
	}
	return clampInt(m.height-overhead, 4, len(m.settings.rows))
}

func displayConfigPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if rel, relErr := filepath.Rel(home, path); relErr == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return path
}

func applySettingsRuntime(m *model, runtime SettingsRuntime) tea.Cmd {
	m.watchTickGen++
	m.opts.Watcher = runtime.Watcher
	m.opts.WatchInterval = runtime.WatchInterval
	m.opts.WatchLabel = runtime.WatchLabel
	m.opts.WatcherRunningLabel = runtime.WatcherRunningLabel
	m.opts.Notifier = runtime.Notifier
	if runtime.LaunchIssue != nil {
		m.opts.LaunchIssue = runtime.LaunchIssue
	}
	m.watchErr = ""
	m.notifyErr = ""
	m.watchDisabled = false
	if m.opts.Watcher != nil {
		return m.watchTickCmd()
	}
	return nil
}
