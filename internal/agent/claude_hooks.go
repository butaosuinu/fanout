package agent

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

// claudeHookSettingsJSON is the inline JSON injected into every claude launch
// via `--settings`. It wires Claude Code lifecycle hooks that report the
// agent's state on the pane's @fanout_agent_state user option, refining the
// coarse running/done bracket the tmuxrun launch wrapper records:
// UserPromptSubmit / PreToolUse / PostToolUse -> working, Notification ->
// blocked (a permission or input wait became user-visible), Stop -> idle.
// PreToolUse fires before the permission check, so recovery from blocked is
// carried by the next PostToolUse or Stop instead.
//
// The Notification hook filters its stdin JSON by notification_type so only
// real user-visible waits (permission_prompt, agent_needs_input,
// elicitation_dialog) report blocked; the ~60s idle_prompt reminder must not
// flip an idle pane to blocked. The filter greps stdin instead of relying on
// a hook matcher because matcher semantics for Notification events are not
// pinned across Claude Code versions, while stdin is documented as one JSON
// object.
//
// Launch-time injection keeps the hooks scoped to fanout-launched panes:
// Claude Code merges --settings hooks with the user's own settings files, so
// no file under the user's home is touched. Each hook is the best-effort
// tmuxrun.AgentStateSetCommand one-liner (`|| true` keeps a hookless
// environment, e.g. tmux gone, from surfacing errors into the session).
var claudeHookSettingsJSON = buildClaudeHookSettingsJSON()

// blockedNotificationTypes are the Notification notification_type values that
// mean a user-visible wait. idle_prompt is deliberately absent.
const blockedNotificationTypes = "permission_prompt|agent_needs_input|elicitation_dialog"

type claudeHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type claudeHookMatcher struct {
	Hooks []claudeHookCommand `json:"hooks"`
}

// claudeHookEvents fixes the JSON key order so the generated settings string
// is deterministic (dry-run goldens pin it byte-for-byte).
type claudeHookEvents struct {
	UserPromptSubmit []claudeHookMatcher `json:"UserPromptSubmit"`
	PreToolUse       []claudeHookMatcher `json:"PreToolUse"`
	PostToolUse      []claudeHookMatcher `json:"PostToolUse"`
	Notification     []claudeHookMatcher `json:"Notification"`
	Stop             []claudeHookMatcher `json:"Stop"`
}

type claudeHookSettings struct {
	Hooks claudeHookEvents `json:"hooks"`
}

func buildClaudeHookSettingsJSON() string {
	hook := func(command string) []claudeHookMatcher {
		return []claudeHookMatcher{{Hooks: []claudeHookCommand{{
			Type:    "command",
			Command: command,
		}}}}
	}
	stateHook := func(state string) []claudeHookMatcher {
		return hook(tmuxrun.AgentStateSetCommand(state) + " || true")
	}
	blockedHook := hook(`grep -Eq '"notification_type"[[:space:]]*:[[:space:]]*"(` + blockedNotificationTypes + `)"' - && ` +
		tmuxrun.AgentStateSetCommand("blocked") + " || true")
	settings := claudeHookSettings{Hooks: claudeHookEvents{
		UserPromptSubmit: stateHook("working"),
		PreToolUse:       stateHook("working"),
		PostToolUse:      stateHook("working"),
		Notification:     blockedHook,
		Stop:             stateHook("idle"),
	}}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// The hook command contains `2>/dev/null`; keep `>` literal instead of the
	// default > HTML escape so dry-run output stays readable.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(settings); err != nil {
		panic(err) // fixed struct of strings; cannot fail
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
