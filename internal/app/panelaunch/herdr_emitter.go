package panelaunch

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type herdrEmitterLaunch struct {
	backendArgs []string
	environment []string
	nonce       string
}

type claudeHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type claudeHookMatcher struct {
	Hooks []claudeHookCommand `json:"hooks"`
}

type claudeHookSettings struct {
	Hooks map[string][]claudeHookMatcher `json:"hooks"`
}

func newHerdrEmitterLaunch(
	req Request,
	route herdrrun.OwnedLaunchRoute,
	intent state.HerdrIntent,
	launchNonce string,
	agentID string,
	statePath string,
) (herdrEmitterLaunch, error) {
	if req.Agent != "claude" {
		return herdrEmitterLaunch{}, nil
	}
	emitterNonce, err := randomHerdrToken()
	if err != nil {
		return herdrEmitterLaunch{}, err
	}
	settings, err := herdrClaudeHookSettings(route.LauncherPath)
	if err != nil {
		return herdrEmitterLaunch{}, err
	}
	return herdrEmitterLaunch{
		backendArgs: []string{"--settings", settings},
		environment: herdrEmitterEnvironment(
			statePath, intent, route, launchNonce, emitterNonce, req.Agent, agentID,
		),
		nonce: emitterNonce,
	}, nil
}

func herdrClaudeHookSettings(fanoutPath string) (string, error) {
	if !filepath.IsAbs(fanoutPath) || filepath.Clean(fanoutPath) != fanoutPath {
		return "", fmt.Errorf("telemetry emitter executable must be canonical and absolute")
	}
	emit := func(reportedState string) claudeHookMatcher {
		command := fmt.Sprintf(
			`FANOUT_STATE_PATH="$FANOUT_EMITTER_STATE_PATH" %s %s %s >/dev/null 2>&1 || true`,
			agent.ShellQuote(fanoutPath), telemetry.Command, reportedState,
		)
		return claudeHookMatcher{Hooks: []claudeHookCommand{{Type: "command", Command: command}}}
	}
	blocked := emit(string(backend.AgentBlocked))
	blocked.Hooks[0].Command = `grep -Eq '"notification_type"[[:space:]]*:[[:space:]]*"(permission_prompt|agent_needs_input|elicitation_dialog)"' - && ` + blocked.Hooks[0].Command
	settings := claudeHookSettings{Hooks: map[string][]claudeHookMatcher{
		"UserPromptSubmit": {emit(string(backend.AgentWorking))},
		"PreToolUse":       {emit(string(backend.AgentWorking))},
		"PostToolUse":      {emit(string(backend.AgentWorking))},
		"Notification":     {blocked},
		"Stop":             {emit(string(backend.AgentIdle))},
		"SessionEnd":       {emit(string(backend.AgentDone))},
	}}
	encoded, err := json.Marshal(settings)
	return string(encoded), err
}

func herdrEmitterEnvironment(
	statePath string,
	intent state.HerdrIntent,
	route herdrrun.OwnedLaunchRoute,
	launchNonce string,
	emitterNonce string,
	agentName string,
	agentID string,
) []string {
	values := [][2]string{
		{telemetry.EmitterPathEnv, statePath},
		{telemetry.RowKeyEnv, intent.ID},
		{telemetry.LaunchNonceEnv, launchNonce},
		{telemetry.EmitterNonceEnv, emitterNonce},
		{telemetry.BackendEnv, string(backend.Herdr)},
		{telemetry.SessionEnv, route.Session},
		{telemetry.SocketPathEnv, route.SocketPath},
		{telemetry.WorkspaceIDEnv, intent.Resource.WorkspaceID},
		{telemetry.PaneIDEnv, intent.Resource.PaneID},
		{telemetry.TerminalIDEnv, intent.Resource.TerminalID},
		{telemetry.AgentEnv, agentName},
		{telemetry.AgentIDEnv, agentID},
	}
	environment := make([]string, 0, len(values))
	for _, value := range values {
		environment = append(environment, value[0]+"="+value[1])
	}
	return environment
}
