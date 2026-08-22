package panelaunch

import (
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// managedEmitterLaunch carries what the telemetry emitter contributes to a
// launch beyond its argv. The argv side lives in managedEmitterBackendArgs so
// that recording a launch and verifying it later share one source.
type managedEmitterLaunch struct {
	environment []string
	nonce       string
}

const managedClaudeExitReasons = "logout|prompt_input_exit|bypass_permissions_disabled|other"

// Assign the event sequence before the emitter is backgrounded. The lock keeps
// attached Claude panes that share one owning state path on one counter.
const managedClaudeNextSequence = `__fanout_sequence_file="$FANOUT_EMITTER_STATE_PATH.sequence"; ` +
	`__fanout_sequence_lock="$__fanout_sequence_file.lock"; ` +
	`mkdir "$__fanout_sequence_lock" 2>/dev/null || { sleep 1; mkdir "$__fanout_sequence_lock" 2>/dev/null || exit 1; }; ` +
	`trap 'rmdir "$__fanout_sequence_lock" 2>/dev/null || true' 0; ` +
	`__fanout_previous_sequence=0; [ ! -f "$__fanout_sequence_file" ] || ` +
	`IFS= read -r __fanout_previous_sequence < "$__fanout_sequence_file" || exit 1; ` +
	`case "$__fanout_previous_sequence" in ''|*[!0-9]*) exit 1;; esac; ` +
	`__fanout_event_sequence=$((__fanout_previous_sequence + 1)); ` +
	`[ "$__fanout_event_sequence" -gt "$__fanout_previous_sequence" ] || exit 1; ` +
	`(umask 077; printf '%s\n' "$__fanout_event_sequence" > "$__fanout_sequence_file") || exit 1; ` +
	`rmdir "$__fanout_sequence_lock" || exit 1; trap - 0; printf '%s\n' "$__fanout_event_sequence"`

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
	return managedEmitterLaunch{
		environment: managedEmitterEnvironment(
			statePath, intent, route, launchNonce, emitterNonce, req.Agent, agentID,
		),
		nonce: emitterNonce,
	}, nil
}

// managedEmitterBackendArgs returns the launch arguments the telemetry emitter
// contributes to the agent command. Only claude has any: the others carry the
// emitter through the environment alone. They derive from the route — never
// from a nonce — so recording a launch and verifying it later rebuild the same
// argv. Both paths must call this; building the command without it made every
// claude launch fail its own binding check.
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
		NextSequence: managedClaudeNextSequence,
		DoneMatcher:  managedClaudeExitReasons, DoneTimeoutSeconds: telemetry.EmitterTimeoutSeconds,
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
