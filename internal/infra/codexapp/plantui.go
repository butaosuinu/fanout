package codexapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const (
	codexPlanDefaultEffort       = "xhigh"
	codexRemoteAppConnectTimeout = 10 * time.Second
	codexRemoteTUIStartupGrace   = 3 * time.Second
	codexPlanSeedAssistantText   = "Ready."
)

// TUIConfig configures one RunPlanTUI invocation. Version is the fanout
// version string injected into the app-server initialize clientInfo; the cmd
// entrypoint must pass its ldflags version.
type TUIConfig struct {
	CodexPath       string
	Prompt          string
	ResumeThreadID  string
	ResumeSessionID string
	StatusFile      string
	Version         string
	// SetAgentState reports the pane's agent state (working/plan) around the
	// fanout-driven initial plan turn. Best-effort display telemetry: the cmd
	// entrypoint wires it to tmuxrun.SetPaneAgentState; nil (tests, direct
	// calls) means no reporting.
	SetAgentState func(state string)
}

type codexThreadInfo struct {
	ID                       string
	SessionID                string
	Model                    string
	PlanEffort               string
	UseTurnCollaborationMode bool
}

type codexResolvedSettings struct {
	Model           string
	ReasoningEffort string
}

type codexModelSelection struct {
	Model                     string
	SupportedReasoningEfforts []string
}

type supportedReasoningEfforts []string

type codexPlanTurnStartResult struct {
	TurnID    string
	Completed bool
}

type codexTurnCompletion struct {
	Matched bool
	Status  string
}

// RunPlanTUI runs the fanout Codex Plan Mode controller: it starts an
// app-server, prepares (or resumes) a Plan Mode thread, attaches the
// interactive Codex TUI, and reports readiness through the status file.
func RunPlanTUI(cfg TUIConfig, stdout, stderr io.Writer) (err error) {
	ready := false
	defer func() {
		if err != nil && !ready {
			_ = writeStatus(cfg.StatusFile, Status{
				Status: statusFailed,
				Error:  err.Error(),
			})
		}
	}()

	server, err := startAppServer(cfg.CodexPath)
	if err != nil {
		return err
	}
	defer server.Close()
	stopSignalCleanup := installCodexAppServerSignalCleanup(server)
	defer stopSignalCleanup()

	client, err := connectAppServer(server, codexRemoteAppConnectTimeout)
	if err != nil {
		return err
	}
	defer client.Close()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}
	thread := codexThreadInfo{ID: strings.TrimSpace(cfg.ResumeThreadID), SessionID: strings.TrimSpace(cfg.ResumeSessionID)}
	if thread.ID != "" && thread.SessionID == "" {
		thread.SessionID = thread.ID
	}
	setState := cfg.SetAgentState
	if setState == nil {
		setState = func(string) {}
	}
	var drainDone chan error
	// Every return below must settle a still-live drain goroutine (finish or
	// bounded-wait) so its in-flight "plan" write cannot outlive the process;
	// paths that already consumed or awaited the drain nil drainDone first.
	defer func() { awaitDrainAfterTUIExit(client, drainDone) }()
	tuiPrompt := ""
	if thread.ID == "" {
		thread, err = setupCodexPlanThread(client, cwd, cfg.Version)
		if err != nil {
			return err
		}
		if thread.UseTurnCollaborationMode {
			drainDone, err = beginCodexPlanTurn(client, thread, cwd, cfg.Prompt, setState)
			if err != nil {
				return err
			}
		} else {
			// The initial plan turn runs inside the interactive TUI here. Keep
			// the initialized controller connection open so the post-ready
			// notification watcher can mirror turn/started and turn/completed
			// into @fanout_agent_state without answering TUI-owned requests.
			err = seedCodexPlanThreadForResume(client, thread.ID)
			if err != nil {
				return err
			}
			tuiPrompt = cfg.Prompt
		}
	} else {
		err = initializeCodexPlanClient(client, cfg.Version)
		if err != nil {
			return err
		}
	}

	tui, tuiDone, err := startCodexRemoteTUI(cfg.CodexPath, server.Addr, codexRemoteTUIResumeID(thread), tuiPrompt, stdout, stderr)
	if err != nil {
		return err
	}
	tuiStopped := false
	defer func() {
		if !tuiStopped {
			stopProcess(tui, tuiDone)
		}
	}()

	startupTimer := time.NewTimer(codexRemoteTUIStartupGrace)
	defer startupTimer.Stop()
	for waitingStartup := true; waitingStartup; {
		select {
		case tuiErr := <-tuiDone:
			tuiStopped = true
			if tuiErr != nil {
				return fmt.Errorf("codex TUI resume exited during startup: %w", tuiErr)
			}
			return fmt.Errorf("codex TUI resume exited during startup")
		case drainErr := <-drainDone:
			if drainErr != nil {
				return fmt.Errorf("codex app-server request handling failed during TUI startup: %w", drainErr)
			}
			drainDone = nil
		case <-server.Done():
			if _, serverErr := server.Exited(); serverErr != nil {
				return fmt.Errorf("codex app-server exited during TUI startup: %w%s", serverErr, serverLogSuffix(server))
			}
			return fmt.Errorf("codex app-server exited during TUI startup%s", serverLogSuffix(server))
		case <-startupTimer.C:
			waitingStartup = false
		}
	}
	if drainDone == nil && canWatchAppServer(client) {
		drainDone = completedAppServerDrain()
	}

	if err = writeStatus(cfg.StatusFile, Status{
		Status:    statusReady,
		ThreadID:  thread.ID,
		SessionID: thread.SessionID,
		Remote:    server.Addr,
	}); err != nil {
		return fmt.Errorf("write Codex Plan TUI status: %w", err)
	}
	ready = true
	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, client, setState)
	drainDone = nil // consumed or awaited inside waitForCodexTUIAfterReady
	tuiStopped = tuiExited
	return err
}

func setupCodexPlanThread(client sessionClient, cwd, version string) (codexThreadInfo, error) {
	if err := initializeCodexPlanClient(client, version); err != nil {
		return codexThreadInfo{}, err
	}
	modeResult, err := client.Request("fanout-modes", "collaborationMode/list", map[string]any{})
	if err != nil {
		return codexThreadInfo{}, err
	}
	if planModeErr := ensureCodexPlanMode(modeResult); planModeErr != nil {
		return codexThreadInfo{}, planModeErr
	}

	settings, err := resolveCodexSettings(client, cwd)
	if err != nil {
		return codexThreadInfo{}, err
	}

	threadResult, err := client.Request("fanout-thread", "thread/start", codexThreadStartParams(cwd, settings.Model))
	if err != nil {
		return codexThreadInfo{}, err
	}
	thread, err := parseThreadStart(threadResult)
	if err != nil {
		return codexThreadInfo{}, err
	}
	thread.Model = settings.Model
	thread.PlanEffort = settings.ReasoningEffort

	if _, err := client.Request("fanout-plan-mode", "thread/settings/update", codexPlanSettingsUpdateParams(thread.ID, settings.Model, settings.ReasoningEffort)); err != nil {
		if !isUnsupportedCodexAppServerMethod(err) {
			return codexThreadInfo{}, err
		}
		thread.UseTurnCollaborationMode = true
	}
	return thread, nil
}

func initializeCodexPlanClient(client sessionClient, version string) error {
	if _, err := client.Request("fanout-init", "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "fanout-codex-plan-tui",
			"title":   nil,
			"version": version,
		},
		"capabilities": map[string]any{
			"experimentalApi":           true,
			"requestAttestation":        false,
			"optOutNotificationMethods": nil,
		},
	}); err != nil {
		return err
	}
	if err := client.Notify("initialized"); err != nil {
		return err
	}
	return nil
}

func codexThreadStartParams(cwd, model string) map[string]any {
	return map[string]any{
		"cwd":                cwd,
		"model":              model,
		"sessionStartSource": "startup",
		"threadSource":       "user",
		"ephemeral":          false,
	}
}

func startCodexPlanTurn(client requester, thread codexThreadInfo, cwd, prompt string) (codexPlanTurnStartResult, error) {
	var collaborationMode map[string]any
	if thread.UseTurnCollaborationMode {
		collaborationMode = codexPlanCollaborationMode(thread.Model, thread.PlanEffort)
	}
	params := codexTurnStartParams(thread.ID, cwd, thread.Model, prompt, collaborationMode)
	result, err := client.Request("fanout-turn", "turn/start", params)
	if err != nil {
		return codexPlanTurnStartResult{}, err
	}
	status, turnID := codexTurnStartStatus(result)
	switch status {
	case "completed":
		return codexPlanTurnStartResult{TurnID: turnID, Completed: true}, nil
	case "failed", "interrupted":
		return codexPlanTurnStartResult{}, fmt.Errorf("codex initial plan turn ended with status %q", status)
	default:
		return codexPlanTurnStartResult{TurnID: turnID}, nil
	}
}

func codexTurnStartParams(threadID, cwd, model, prompt string, collaborationMode map[string]any) map[string]any {
	params := map[string]any{
		"threadId": threadID,
		"cwd":      cwd,
		"model":    model,
		"input": []map[string]any{
			{
				"type": "text",
				"text": prompt,
			},
		},
	}
	if collaborationMode != nil {
		params["collaborationMode"] = collaborationMode
	}
	return params
}

func seedCodexPlanThreadForResume(client requester, threadID string) error {
	_, err := client.Request("fanout-seed", "thread/inject_items", map[string]any{
		"threadId": threadID,
		"items": []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{
						"type": "output_text",
						"text": codexPlanSeedAssistantText,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("seed Codex Plan TUI thread for resume: %w", err)
	}
	return nil
}

func codexPlanSettingsUpdateParams(threadID, model, effort string) map[string]any {
	return map[string]any{
		"threadId":          threadID,
		"collaborationMode": codexPlanCollaborationMode(model, effort),
	}
}

func codexPlanCollaborationMode(model, effort string) map[string]any {
	return map[string]any{
		"mode": "plan",
		"settings": map[string]any{
			"model":                  model,
			"reasoning_effort":       effort,
			"developer_instructions": nil,
		},
	}
}

func startCodexRemoteTUI(codexPath, remoteAddr, resumeID, prompt string, stdout, stderr io.Writer) (*exec.Cmd, chan error, error) {
	cmd := exec.Command(codexPath, codexRemoteTUIArgs(remoteAddr, resumeID, prompt)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start codex TUI remote attach: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, done, nil
}

func codexRemoteTUIArgs(remoteAddr, resumeID, prompt string) []string {
	args := []string{"--remote", remoteAddr, "resume", resumeID}
	if strings.TrimSpace(prompt) != "" {
		args = append(args, "--", prompt)
	}
	return args
}

func codexRemoteTUIResumeID(thread codexThreadInfo) string {
	if strings.TrimSpace(thread.SessionID) != "" {
		return thread.SessionID
	}
	return thread.ID
}

// beginCodexPlanTurn starts the fanout-driven initial plan turn and reports
// its progress as agent state: "working" while the turn runs and "plan" once
// it completes successfully (written inside the drain goroutine, so waiting on
// the returned channel also waits for the state write). A synchronously
// completed turn skips "working" and reports "plan" directly. The returned
// channel is nil when there is nothing to drain.
func beginCodexPlanTurn(client planTurnClient, thread codexThreadInfo, cwd, prompt string, setState func(string)) (chan error, error) {
	turnStart, err := startCodexPlanTurn(client, thread, cwd, prompt)
	if err != nil {
		return nil, err
	}
	if turnStart.Completed {
		setState("plan")
		return nil, nil
	}
	setState("working")
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- drainCodexAppServerUntilTurnComplete(client, thread.ID, turnStart.TurnID, setState)
	}()
	return drainDone, nil
}

func waitForCodexTUIAfterReady(tuiDone <-chan error, drainDone <-chan error, client *client, setState func(string)) (bool, error) {
	watchingAppServer := false
	for drainDone != nil {
		select {
		case tuiErr := <-tuiDone:
			awaitDrainAfterTUIExit(client, drainDone)
			return true, tuiErr
		case drainErr := <-drainDone:
			if drainErr != nil {
				return false, fmt.Errorf("codex app-server request handling failed while Codex TUI was attached: %w", drainErr)
			}
			if !watchingAppServer && canWatchAppServer(client) {
				watchingAppServer = true
				drainDone = drainCodexAppServerUntilClosedCmd(client, setState)
				continue
			}
			drainDone = nil
		}
	}
	return true, <-tuiDone
}

func canWatchAppServer(client *client) bool {
	return client != nil && client.canWatch()
}

func completedAppServerDrain() chan error {
	done := make(chan error, 1)
	done <- nil
	return done
}

func drainCodexAppServerUntilClosedCmd(client *client, setState func(string)) chan error {
	done := make(chan error, 1)
	go func() { done <- drainCodexAppServerUntilClosed(client, setState) }()
	return done
}

func drainCodexAppServerUntilClosed(client *client, setState func(string)) error {
	return drainCodexAppServerNotificationsUntilClosed(client, setState)
}

type appServerReceiver interface {
	receive() (appServerMessage, error)
}

func drainCodexAppServerNotificationsUntilClosed(receiver appServerReceiver, setState func(string)) error {
	for {
		msg, err := receiver.receive()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if state := codexTurnNotificationAgentState(msg); state != "" {
			reportCodexPlanAgentState(setState, state)
		}
	}
}

// awaitDrainAfterTUIExit closes the app client and briefly waits for the
// initial-turn drain goroutine before the process exits. The drain writes the
// "plan" agent state synchronously before signaling drainDone; skipping this
// wait could leave an orphaned tmux set-option that stamps "plan" on the pane
// after the launch wrapper already recorded "done". The bounded wait narrows
// that window rather than closing it: a tmux server stalled past the timeout
// at the exact completion instant can still land "plan" late — accepted, the
// state is display-only telemetry.
func awaitDrainAfterTUIExit(client *client, drainDone <-chan error) {
	if drainDone == nil {
		return
	}
	client.Close()
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
	}
}

func drainCodexAppServerUntilTurnComplete(client streamClient, threadID, turnID string, setState func(string)) error {
	for {
		msg, err := client.receive()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("codex app-server disconnected before initial turn completed: %w", err)
			}
			return err
		}
		if isServerRequest(msg) {
			if err := handleServerRequestWithState(client, msg, setState); err != nil {
				return err
			}
		}
		completion := codexTurnCompletedNotification(msg, threadID, turnID)
		if completion.Matched {
			if state := codexTurnCompletionAgentState(completion); state != "" {
				reportCodexPlanAgentState(setState, state)
			}
			if completion.Status != "completed" {
				return fmt.Errorf("codex initial plan turn ended with status %q", completion.Status)
			}
			return nil
		}
	}
}

func reportCodexPlanAgentState(setState func(string), state string) {
	if setState != nil && state != "" {
		setState(state)
	}
}

func codexTurnStartStatus(raw json.RawMessage) (string, string) {
	var res struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", ""
	}
	return strings.TrimSpace(res.Turn.Status), strings.TrimSpace(res.Turn.ID)
}

func codexTurnCompletedNotification(msg appServerMessage, threadID, turnID string) codexTurnCompletion {
	if msg.Method != "turn/completed" {
		return codexTurnCompletion{}
	}
	var params struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
			Status   string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return codexTurnCompletion{}
	}
	status := strings.TrimSpace(params.Turn.Status)
	if !isTerminalCodexTurnStatus(status) {
		return codexTurnCompletion{}
	}
	if !codexTurnCompletionMatches(params.ThreadID, params.Turn.ThreadID, params.Turn.ID, threadID, turnID) {
		return codexTurnCompletion{}
	}
	return codexTurnCompletion{Matched: true, Status: status}
}

func codexTurnNotificationAgentState(msg appServerMessage) string {
	if msg.Method == "turn/started" {
		return "working"
	}
	completion := anyCodexTurnCompletedNotification(msg)
	return codexTurnCompletionAgentState(completion)
}

func anyCodexTurnCompletedNotification(msg appServerMessage) codexTurnCompletion {
	if msg.Method != "turn/completed" {
		return codexTurnCompletion{}
	}
	var params struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return codexTurnCompletion{}
	}
	status := strings.TrimSpace(params.Turn.Status)
	if !isTerminalCodexTurnStatus(status) {
		return codexTurnCompletion{}
	}
	return codexTurnCompletion{Matched: true, Status: status}
}

func codexTurnCompletionMatches(topLevelThreadID, turnThreadID, actualTurnID, expectedThreadID, expectedTurnID string) bool {
	topLevelThreadID = strings.TrimSpace(topLevelThreadID)
	turnThreadID = strings.TrimSpace(turnThreadID)
	actualTurnID = strings.TrimSpace(actualTurnID)
	expectedThreadID = strings.TrimSpace(expectedThreadID)
	expectedTurnID = strings.TrimSpace(expectedTurnID)

	if expectedTurnID != "" && actualTurnID != "" {
		return actualTurnID == expectedTurnID
	}
	if expectedThreadID != "" && (topLevelThreadID != "" || turnThreadID != "") {
		return topLevelThreadID == expectedThreadID || turnThreadID == expectedThreadID
	}
	// Older app-server payloads may only include {"turn":{"status":...}}.
	// In fallback mode this controller starts exactly one initial turn.
	return topLevelThreadID == "" && turnThreadID == "" && actualTurnID == ""
}

func isTerminalCodexTurnStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "interrupted", "failed":
		return true
	default:
		return false
	}
}

func codexTurnCompletionAgentState(completion codexTurnCompletion) string {
	if !completion.Matched {
		return ""
	}
	switch completion.Status {
	case "completed":
		return "plan"
	case "failed", "interrupted":
		return "idle"
	default:
		return ""
	}
}

func resolveCodexSettings(client requester, cwd string) (codexResolvedSettings, error) {
	configResult, configErr := client.Request("fanout-config", "config/read", map[string]any{
		"includeLayers": false,
		"cwd":           cwd,
	})
	model := ""
	effort := codexPlanDefaultEffort
	if configErr == nil {
		config := configSettings(configResult)
		model = config.Model
		if config.ReasoningEffort != "" {
			effort = config.ReasoningEffort
		}
	}

	modelResult, modelErr := client.Request("fanout-models", "model/list", map[string]any{
		"includeHidden": true,
	})
	if modelErr != nil {
		if model != "" {
			return codexResolvedSettings{Model: model, ReasoningEffort: effort}, nil
		}
		if configErr != nil {
			return codexResolvedSettings{}, fmt.Errorf("resolve codex model: config/read failed: %w; model/list failed: %w", configErr, modelErr)
		}
		return codexResolvedSettings{}, fmt.Errorf("resolve codex model from model/list: %w", modelErr)
	}

	selection, err := modelListSelection(modelResult, model)
	if err != nil {
		if model != "" {
			return codexResolvedSettings{Model: model, ReasoningEffort: effort}, nil
		}
		if configErr != nil {
			return codexResolvedSettings{}, fmt.Errorf("resolve codex model: config/read failed: %w; model/list failed: %w", configErr, err)
		}
		return codexResolvedSettings{}, err
	}
	if model == "" {
		model = selection.Model
	}
	if len(selection.SupportedReasoningEfforts) > 0 {
		effort = supportedReasoningEffort(effort, selection.SupportedReasoningEfforts)
	}
	return codexResolvedSettings{Model: model, ReasoningEffort: effort}, nil
}

func configSettings(raw json.RawMessage) codexResolvedSettings {
	var res struct {
		Config struct {
			Model                   string `json:"model"`
			ReasoningEffort         string `json:"reasoning_effort"`
			ModelReasoningEffort    string `json:"model_reasoning_effort"`
			PlanModeReasoningEffort string `json:"plan_mode_reasoning_effort"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return codexResolvedSettings{}
	}
	effort := strings.TrimSpace(res.Config.PlanModeReasoningEffort)
	if effort == "" {
		effort = strings.TrimSpace(res.Config.ReasoningEffort)
	}
	if effort == "" {
		effort = strings.TrimSpace(res.Config.ModelReasoningEffort)
	}
	return codexResolvedSettings{
		Model:           strings.TrimSpace(res.Config.Model),
		ReasoningEffort: effort,
	}
}

func modelListSelection(raw json.RawMessage, preferred string) (codexModelSelection, error) {
	var res struct {
		Data []struct {
			ID                                string                    `json:"id"`
			Model                             string                    `json:"model"`
			Hidden                            bool                      `json:"hidden"`
			IsDefault                         bool                      `json:"isDefault"`
			SupportedReasoningEfforts         supportedReasoningEfforts `json:"supportedReasoningEfforts"`
			SupportedReasoningEffortsFallback supportedReasoningEfforts `json:"supported_reasoning_efforts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return codexModelSelection{}, fmt.Errorf("parse model/list response: %w", err)
	}
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		for _, model := range res.Data {
			if modelName(model.Model, model.ID) != preferred && strings.TrimSpace(model.ID) != preferred {
				continue
			}
			if name := modelName(model.Model, model.ID); name != "" {
				return codexModelSelection{Model: name, SupportedReasoningEfforts: modelSupportedReasoningEfforts(model.SupportedReasoningEfforts, model.SupportedReasoningEffortsFallback)}, nil
			}
		}
	}
	for _, model := range res.Data {
		if model.Hidden || !model.IsDefault {
			continue
		}
		if name := modelName(model.Model, model.ID); name != "" {
			return codexModelSelection{Model: name, SupportedReasoningEfforts: modelSupportedReasoningEfforts(model.SupportedReasoningEfforts, model.SupportedReasoningEffortsFallback)}, nil
		}
	}
	for _, model := range res.Data {
		if model.Hidden {
			continue
		}
		if name := modelName(model.Model, model.ID); name != "" {
			return codexModelSelection{Model: name, SupportedReasoningEfforts: modelSupportedReasoningEfforts(model.SupportedReasoningEfforts, model.SupportedReasoningEffortsFallback)}, nil
		}
	}
	return codexModelSelection{}, fmt.Errorf("model/list response did not include an available model")
}

func modelSupportedReasoningEfforts(primary, fallback []string) []string {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

func (e *supportedReasoningEfforts) UnmarshalJSON(raw []byte) error {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		var effort string
		if err := json.Unmarshal(item, &effort); err == nil {
			effort = strings.TrimSpace(effort)
			if effort != "" {
				out = append(out, effort)
			}
			continue
		}
		var shaped struct {
			ReasoningEffort string `json:"reasoningEffort"`
			Value           string `json:"value"`
			ID              string `json:"id"`
			Name            string `json:"name"`
		}
		if err := json.Unmarshal(item, &shaped); err != nil {
			return err
		}
		for _, candidate := range []string{shaped.ReasoningEffort, shaped.Value, shaped.ID, shaped.Name} {
			if effort = strings.TrimSpace(candidate); effort != "" {
				out = append(out, effort)
				break
			}
		}
	}
	*e = out
	return nil
}

func supportedReasoningEffort(requested string, supported []string) string {
	requested = strings.TrimSpace(requested)
	available := map[string]bool{}
	for _, effort := range supported {
		effort = strings.TrimSpace(effort)
		if effort != "" {
			available[effort] = true
		}
	}
	if len(available) == 0 || available[requested] {
		return requested
	}
	for _, effort := range slices.Backward(supported) {
		if effort = strings.TrimSpace(effort); effort != "" {
			return effort
		}
	}
	return requested
}

func modelName(model, id string) string {
	if strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	return strings.TrimSpace(id)
}

func ensureCodexPlanMode(raw json.RawMessage) error {
	var res struct {
		Data []struct {
			Name string `json:"name"`
			Mode string `json:"mode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("parse collaborationMode/list response: %w", err)
	}
	for _, mode := range res.Data {
		if mode.Mode != "plan" {
			continue
		}
		return nil
	}
	return fmt.Errorf("codex app-server does not advertise collaborationMode.mode=plan")
}

func parseThreadStart(raw json.RawMessage) (codexThreadInfo, error) {
	var res struct {
		Thread struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionId"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return codexThreadInfo{}, fmt.Errorf("parse thread/start response: %w", err)
	}
	if res.Thread.ID == "" {
		return codexThreadInfo{}, fmt.Errorf("thread/start response did not include thread.id")
	}
	if res.Thread.SessionID == "" {
		res.Thread.SessionID = res.Thread.ID
	}
	return codexThreadInfo{ID: res.Thread.ID, SessionID: res.Thread.SessionID}, nil
}

func isUnsupportedCodexAppServerMethod(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown variant") ||
		strings.Contains(msg, "unknown method") ||
		strings.Contains(msg, "method not found") ||
		strings.Contains(msg, "unsupported method")
}
