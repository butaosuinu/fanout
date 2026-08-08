package panelaunch

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	herdrEmitterArrivalTimeout = 500 * time.Millisecond
	herdrEmitterHandoffTimeout = 5 * time.Second
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
	emitterPath := route.EmitterPath
	if emitterPath == "" {
		emitterPath = route.LauncherPath
	}
	settings, err := herdrClaudeHookSettings(emitterPath)
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
			`{ FANOUT_STATE_PATH="$FANOUT_EMITTER_STATE_PATH" %s %s %s >/dev/null 2>&1 || true; } &`,
			agent.ShellQuote(fanoutPath), telemetry.Command, reportedState,
		)
		return claudeHookMatcher{Hooks: []claudeHookCommand{{Type: "command", Command: command}}}
	}
	blocked := emit(string(backend.AgentBlocked))
	blocked.Hooks[0].Command = `if grep -Eq '"notification_type"[[:space:]]*:[[:space:]]*"(permission_prompt|agent_needs_input|elicitation_dialog)"' -; then ` + blocked.Hooks[0].Command + ` fi`
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

func (l *Launcher) handoffHerdrEmitter(
	ctx context.Context,
	locked *state.LockedStore,
	intent state.HerdrIntent,
	live backend.LivePane,
) (backend.LivePane, error) {
	if intent.Launch == nil || intent.Launch.EmitterNonce == "" {
		return live, nil
	}
	statePath := state.Path(l.Info.ProjectRoot)
	waiting, err := awaitHerdrEmitter(ctx, statePath, intent.Launch.EmitterNonce)
	if err != nil {
		l.Log.Warn("%s: telemetry emitter handoff unavailable: %v", paneLogLabel(Request{Number: intent.IssueNum, TaskID: intent.TaskID}), err)
		return live, nil
	}
	if !waiting {
		return live, nil
	}
	handoffCtx, cancel := context.WithTimeout(ctx, herdrEmitterHandoffTimeout)
	defer cancel()
	completed, err := locked.YieldForEmitter(
		handoffCtx, l.Info.ProjectRoot, statePath, intent.Launch.EmitterNonce,
	)
	if err != nil {
		return live, err
	}
	if !completed {
		l.Log.Warn("Herdr telemetry emitter did not finish during launch lock handoff")
	}
	return l.reverifyHerdrAgent(ctx, intent)
}

func awaitHerdrEmitter(ctx context.Context, statePath, launchNonce string) (bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, herdrEmitterArrivalTimeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		handoffs, err := state.EmitterHandoffs(statePath, launchNonce)
		if err != nil || len(handoffs) > 0 {
			return len(handoffs) > 0, err
		}
		select {
		case <-waitCtx.Done():
			return false, nil
		case <-ticker.C:
		}
	}
}

func (l *Launcher) reverifyHerdrAgent(
	ctx context.Context,
	intent state.HerdrIntent,
) (backend.LivePane, error) {
	live, err := l.waitForHerdrAgent(ctx, intent, intent.Launch.AgentName)
	if err != nil {
		return live, err
	}
	var process herdrrun.PaneProcessInfo
	err = retryHerdrObservation(ctx, intent, func(observeCtx context.Context) error {
		var processErr error
		process, processErr = l.Herdr.ProcessInfo(observeCtx, intent.Resource.PaneID)
		return processErr
	})
	if err != nil {
		return live, err
	}
	return live, verifyHerdrAgentProcess(process, intent)
}
