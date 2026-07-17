package agent

import (
	"bytes"
	"encoding/json"
	"strings"
)

// claudeHookSettingsJSON is the inline JSON injected into every tmux-backed
// Claude launch via `--settings`. It wires Claude Code lifecycle hooks that
// report the agent's state on the pane's @fanout_agent_state user option,
// refining the coarse running/done bracket the tmuxrun launch wrapper records:
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
// agentStateSetCommand one-liner (`|| true` keeps a hookless environment, e.g.
// tmux gone, from surfacing errors into the session).
var claudeHookSettingsJSON = buildClaudeHookSettingsJSON()

// agentStateOption must stay identical to internal/infra/tmuxrun's
// @fanout_agent_state pane user option, and agentStateSetCommand must produce
// the same one-liner tmuxrun.AgentStateSetCommand does. agent is a core-layer
// package and must not import infra (tmuxrun), so the shared shape is kept in
// sync by two byte-exact tests: the JSON pin in agent_test.go and the
// AgentStateSetCommand test in tmuxrun.
const agentStateOption = "@fanout_agent_state"

// agentStateSetCommand mirrors tmuxrun.AgentStateSetCommand: the state is
// shell-quoted defensively (ShellQuote and tmuxrun.shellQuote share the same
// algorithm, so contract values like "working" stay bare and identical).
func agentStateSetCommand(state string) string {
	return `tmux set-option -p -t "$TMUX_PANE" ` + agentStateOption + " " + ShellQuote(state) + " 2>/dev/null"
}

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
		return hook(agentStateSetCommand(state) + " || true")
	}
	blockedHook := hook(`grep -Eq '"notification_type"[[:space:]]*:[[:space:]]*"(` + blockedNotificationTypes + `)"' - && ` +
		agentStateSetCommand("blocked") + " || true")
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
