package panelaunch

import (
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type managedEmitterLaunch struct {
	backendArgs []string
	environment []string
	nonce       string
}

const managedClaudeExitReasons = "logout|prompt_input_exit|bypass_permissions_disabled|other"

func newManagedEmitterLaunch(
	req Request,
	route backend.OwnedLaunchRoute,
	intent state.LaunchIntent,
	launchNonce string,
	agentID string,
	statePath string,
) (managedEmitterLaunch, error) {
	if req.Agent != "claude" && !req.CodexPlanMode() {
		return managedEmitterLaunch{}, nil
	}
	emitterNonce, err := randomManagedToken()
	if err != nil {
		return managedEmitterLaunch{}, err
	}
	launch := managedEmitterLaunch{
		environment: managedEmitterEnvironment(
			statePath, intent, route, launchNonce, emitterNonce, req.Agent, agentID,
		),
		nonce: emitterNonce,
	}
	backendArgs, err := managedEmitterBackendArgs(req, route)
	if err != nil {
		return managedEmitterLaunch{}, err
	}
	launch.backendArgs = backendArgs
	return launch, nil
}

// managedEmitterBackendArgs returns the launch arguments the telemetry emitter
// contributes to the agent command. They derive from the route alone — never
// from a nonce — so recording a launch and verifying it later rebuild the same
// argv. Both paths must call this; building the command without it made every
// emitter-bearing launch fail its own binding check.
func managedEmitterBackendArgs(req Request, route backend.OwnedLaunchRoute) ([]string, error) {
	if req.Agent != "claude" {
		return nil, nil
	}
	emitterPath := route.EmitterPath
	if emitterPath == "" {
		emitterPath = route.LauncherPath
	}
	settings, err := managedClaudeHookSettings(emitterPath)
	if err != nil {
		return nil, err
	}
	return []string{"--settings", settings}, nil
}

func managedClaudeHookSettings(fanoutPath string) (string, error) {
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
		DoneMatcher: managedClaudeExitReasons, DoneTimeoutSeconds: telemetry.EmitterTimeoutSeconds,
		Background: true,
	}), nil
}

func managedEmitterEnvironment(
	statePath string,
	intent state.LaunchIntent,
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
