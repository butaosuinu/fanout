package panelaunch

import (
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type herdrEmitterLaunch struct {
	backendArgs []string
	environment []string
	nonce       string
}

const herdrClaudeExitReasons = "logout|prompt_input_exit|bypass_permissions_disabled|other"

func newHerdrEmitterLaunch(
	req Request,
	route backend.OwnedLaunchRoute,
	intent state.HerdrIntent,
	launchNonce string,
	agentID string,
	statePath string,
) (herdrEmitterLaunch, error) {
	if req.Agent != "claude" && !req.CodexPlanMode() {
		return herdrEmitterLaunch{}, nil
	}
	emitterNonce, err := randomHerdrToken()
	if err != nil {
		return herdrEmitterLaunch{}, err
	}
	emitterPath := route.EmitterPath
	if emitterPath == "" {
		emitterPath = route.LauncherPath
	}
	launch := herdrEmitterLaunch{
		environment: herdrEmitterEnvironment(
			statePath, intent, route, launchNonce, emitterNonce, req.Agent, agentID,
		),
		nonce: emitterNonce,
	}
	if req.Agent == "claude" {
		settings, err := herdrClaudeHookSettings(emitterPath)
		if err != nil {
			return herdrEmitterLaunch{}, err
		}
		launch.backendArgs = []string{"--settings", settings}
	}
	return launch, nil
}

func herdrClaudeHookSettings(fanoutPath string) (string, error) {
	if !filepath.IsAbs(fanoutPath) || filepath.Clean(fanoutPath) != fanoutPath {
		return "", fmt.Errorf("telemetry emitter executable must be canonical and absolute")
	}
	emit := func(reportedState string) string {
		return fmt.Sprintf(
			`FANOUT_STATE_PATH="$FANOUT_EMITTER_STATE_PATH" %s %s %s >/dev/null 2>&1`,
			agent.ShellQuote(fanoutPath), telemetry.Command, reportedState,
		)
	}
	return agent.BuildClaudeHookSettingsJSON(agent.ClaudeHookCommands{
		Working: emit(string(backend.AgentWorking)),
		Blocked: emit(string(backend.AgentBlocked)),
		Idle:    emit(string(backend.AgentIdle)), Done: emit(string(backend.AgentDone)),
		DoneMatcher: herdrClaudeExitReasons, DoneTimeoutSeconds: telemetry.EmitterTimeoutSeconds,
		Background: true,
	}), nil
}

func herdrEmitterEnvironment(
	statePath string,
	intent state.HerdrIntent,
	route backend.OwnedLaunchRoute,
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
		{telemetry.WorkspaceLabelEnv, intent.Resource.Label},
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
