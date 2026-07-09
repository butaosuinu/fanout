package codexapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	codexRemoteAppConnectTimeout = 10 * time.Second
	codexRemoteTUIStartupGrace   = 3 * time.Second
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
	// SetAgentState reports best-effort pane state while the Plan Mode turn is
	// being started. The cmd entrypoint wires it to tmuxrun.SetPaneAgentState;
	// nil (tests, direct calls) means no reporting.
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

type codexPlanTurnStartResult struct {
	TurnID    string
	Completed bool
}

type codexTurnCompletion struct {
	Matched bool
	Status  string
}

// RunPlanTUI runs the fanout Codex Plan Mode controller: it starts an
// app-server, creates a native Plan Mode thread/turn through app-server (or
// resumes an existing one), attaches the interactive Codex TUI, and reports
// readiness through the status file.
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
	if err = initializeCodexPlanClient(client, cfg.Version); err != nil {
		return err
	}

	if thread.ID == "" {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return fmt.Errorf("resolve current directory: %w", cwdErr)
		}
		thread, err = setupCodexPlanThread(client, cwd)
		if err != nil {
			return err
		}
		var turnStart codexPlanTurnStartResult
		turnStart, err = startCodexPlanTurn(client, thread, cwd, cfg.Prompt)
		if err != nil {
			return err
		}
		if turnStart.Completed {
			reportCodexPlanAgentState(setState, "plan")
			drainDone = completedAppServerDrain()
		} else {
			reportCodexPlanAgentState(setState, "working")
			drainDone = drainCodexAppServerDuringStartupCmd(client, setState, thread.ID, turnStart.TurnID)
		}
	}

	tui, tuiDone, err := startCodexRemoteTUI(cfg.CodexPath, server.Addr, codexRemoteTUIResumeID(thread), stdout, stderr)
	if err != nil {
		return err
	}
	tuiStopped := false
	defer func() {
		if !tuiStopped {
			stopProcess(tui, tuiDone)
		}
	}()

	if drainDone == nil {
		drainDone = completedAppServerDrain()
	}
	if drainDone, err = waitForCodexRemoteTUIStartup(tuiDone, drainDone, server); err != nil {
		return err
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
	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, client, setState, false)
	drainDone = nil // consumed or awaited inside waitForCodexTUIAfterReady
	tuiStopped = tuiExited
	return err
}

func waitForCodexRemoteTUIStartup(tuiDone <-chan error, drainDone chan error, server *appServer) (chan error, error) {
	startupTimer := time.NewTimer(codexRemoteTUIStartupGrace)
	defer startupTimer.Stop()
	for waitingStartup := true; waitingStartup; {
		select {
		case tuiErr := <-tuiDone:
			if tuiErr != nil {
				return nil, fmt.Errorf("codex TUI exited during startup: %w", tuiErr)
			}
			return nil, fmt.Errorf("codex TUI exited during startup")
		case drainErr := <-drainDone:
			if drainErr != nil {
				return nil, fmt.Errorf("codex app-server request handling failed during TUI startup: %w", drainErr)
			}
			drainDone = nil
		case <-server.Done():
			if _, serverErr := server.Exited(); serverErr != nil {
				return nil, fmt.Errorf("codex app-server exited during TUI startup: %w%s", serverErr, serverLogSuffix(server))
			}
			return nil, fmt.Errorf("codex app-server exited during TUI startup%s", serverLogSuffix(server))
		case <-startupTimer.C:
			waitingStartup = false
		}
	}
	if drainDone == nil {
		drainDone = completedAppServerDrain()
	}
	return drainDone, nil
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

func setupCodexPlanThread(client requester, cwd string) (codexThreadInfo, error) {
	modeResult, err := client.Request("fanout-modes", "collaborationMode/list", map[string]any{})
	if err != nil {
		return codexThreadInfo{}, err
	}
	planEffort, err := codexPlanEffort(modeResult)
	if err != nil {
		return codexThreadInfo{}, err
	}

	settings, err := resolveCodexSettings(client, cwd, planEffort)
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
	params := codexTurnStartParams(thread.ID, cwd, thread.Model, prompt, nil)
	if thread.UseTurnCollaborationMode {
		params["collaborationMode"] = codexPlanCollaborationMode(thread.Model, thread.PlanEffort)
	}
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

func resolveCodexSettings(client requester, cwd, defaultEffort string) (codexResolvedSettings, error) {
	defaultEffort = strings.TrimSpace(defaultEffort)
	if defaultEffort == "" {
		defaultEffort = "medium"
	}
	settings := codexResolvedSettings{ReasoningEffort: defaultEffort}
	configResult, configErr := client.Request("fanout-config", "config/read", map[string]any{
		"includeLayers": false,
		"cwd":           cwd,
	})
	if configErr == nil {
		config := configSettings(configResult)
		if config.Model != "" {
			settings.Model = config.Model
		}
		if config.ReasoningEffort != "" {
			settings.ReasoningEffort = config.ReasoningEffort
		}
		if settings.Model != "" {
			return settings, nil
		}
	}

	modelResult, modelErr := client.Request("fanout-models", "model/list", map[string]any{
		"includeHidden": false,
	})
	if modelErr != nil {
		if configErr != nil {
			return codexResolvedSettings{}, fmt.Errorf("resolve codex model: config/read failed: %w; model/list failed: %w", configErr, modelErr)
		}
		return codexResolvedSettings{}, fmt.Errorf("resolve codex model from model/list: %w", modelErr)
	}
	model, err := modelListDefault(modelResult)
	if err != nil {
		if configErr != nil {
			return codexResolvedSettings{}, fmt.Errorf("resolve codex model: config/read failed: %w; model/list failed: %w", configErr, err)
		}
		return codexResolvedSettings{}, err
	}
	settings.Model = model
	return settings, nil
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

func modelListDefault(raw json.RawMessage) (string, error) {
	var res struct {
		Data []struct {
			ID        string `json:"id"`
			Model     string `json:"model"`
			Hidden    bool   `json:"hidden"`
			IsDefault bool   `json:"isDefault"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("parse model/list response: %w", err)
	}
	for _, model := range res.Data {
		if model.Hidden || !model.IsDefault {
			continue
		}
		if name := modelName(model.Model, model.ID); name != "" {
			return name, nil
		}
	}
	for _, model := range res.Data {
		if model.Hidden {
			continue
		}
		if name := modelName(model.Model, model.ID); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("model/list response did not include an available model")
}

func modelName(model, id string) string {
	if strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	return strings.TrimSpace(id)
}

func codexPlanEffort(raw json.RawMessage) (string, error) {
	var res struct {
		Data []struct {
			Name            string  `json:"name"`
			Mode            string  `json:"mode"`
			ReasoningEffort *string `json:"reasoning_effort"`
			Settings        *struct {
				ReasoningEffort *string `json:"reasoning_effort"`
			} `json:"settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("parse collaborationMode/list response: %w", err)
	}
	for _, mode := range res.Data {
		if mode.Mode != "plan" {
			continue
		}
		if mode.ReasoningEffort != nil && strings.TrimSpace(*mode.ReasoningEffort) != "" {
			return strings.TrimSpace(*mode.ReasoningEffort), nil
		}
		if mode.Settings != nil && mode.Settings.ReasoningEffort != nil && strings.TrimSpace(*mode.Settings.ReasoningEffort) != "" {
			return strings.TrimSpace(*mode.Settings.ReasoningEffort), nil
		}
		return "medium", nil
	}
	return "", fmt.Errorf("codex app-server does not advertise collaborationMode.mode=plan")
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
	threadID := strings.TrimSpace(res.Thread.ID)
	if threadID == "" {
		return codexThreadInfo{}, fmt.Errorf("thread/start response did not include thread.id")
	}
	sessionID := strings.TrimSpace(res.Thread.SessionID)
	if sessionID == "" {
		sessionID = threadID
	}
	return codexThreadInfo{ID: threadID, SessionID: sessionID}, nil
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

func startCodexRemoteTUI(codexPath, remoteAddr, resumeID string, stdout, stderr io.Writer) (*exec.Cmd, chan error, error) {
	cmd := exec.Command(codexPath, codexRemoteTUIArgs(remoteAddr, resumeID)...)
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

func codexRemoteTUIArgs(remoteAddr, resumeID string) []string {
	args := []string{"--remote", remoteAddr}
	if strings.TrimSpace(resumeID) != "" {
		args = append(args, "resume", resumeID)
	}
	return args
}

func codexRemoteTUIResumeID(thread codexThreadInfo) string {
	if strings.TrimSpace(thread.SessionID) != "" {
		return thread.SessionID
	}
	return thread.ID
}

func waitForCodexTUIAfterReady(tuiDone <-chan error, drainDone <-chan error, client *client, setState func(string), watchingAppServer bool) (bool, error) {
	for drainDone != nil {
		select {
		case tuiErr := <-tuiDone:
			awaitDrainAfterTUIExit(client, drainDone)
			return true, tuiErr
		case drainErr := <-drainDone:
			if drainErr != nil {
				if watchingAppServer {
					drainDone = nil
					continue
				}
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

func drainCodexAppServerDuringStartupCmd(client *client, setState func(string), threadID, turnID string) chan error {
	done := make(chan error, 1)
	go func() { done <- drainCodexAppServerDuringStartup(client, setState, threadID, turnID) }()
	return done
}

func drainCodexAppServerUntilClosed(client *client, setState func(string)) error {
	return drainCodexAppServerNotificationsUntilClosed(client, setState)
}

func drainCodexAppServerDuringStartup(client appServerStartupClient, setState func(string), threadID, turnID string) error {
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
			continue
		}
		if msg.Method == "turn/started" {
			reportCodexPlanAgentState(setState, "working")
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

type appServerReceiver interface {
	receive() (appServerMessage, error)
}

type appServerStartupClient interface {
	appServerReceiver
	sender
}

func drainCodexAppServerNotificationsUntilClosed(receiver appServerReceiver, setState func(string)) error {
	return drainCodexAppServerNotificationsUntilClosedWithThread(receiver, setState, nil)
}

func drainCodexAppServerNotificationsUntilClosedWithThread(receiver appServerReceiver, setState func(string), threadReady chan<- codexThreadInfo) error {
	threadReported := threadReady == nil
	for {
		msg, err := receiver.receive()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if !threadReported {
			if thread, ok := codexThreadStartedNotification(msg); ok {
				threadReady <- thread
				threadReported = true
			}
		}
		if state := codexTurnNotificationAgentState(msg); state != "" {
			reportCodexPlanAgentState(setState, state)
		}
	}
}

// awaitDrainAfterTUIExit closes the app client and briefly waits for the
// notification watcher to settle so its final display-state write cannot
// outlive this process by much. The bounded wait keeps shutdown responsive;
// stale agent state is display-only telemetry.
func awaitDrainAfterTUIExit(client *client, drainDone <-chan error) {
	closeCodexAppServerObserver(client, drainDone)
}

func closeCodexAppServerObserver(client *client, drainDone <-chan error) {
	if drainDone == nil {
		return
	}
	client.Close()
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
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
	// In this startup drain fanout has exactly one outstanding initial turn.
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

func codexThreadStartedNotification(msg appServerMessage) (codexThreadInfo, bool) {
	if msg.Method != "thread/started" {
		return codexThreadInfo{}, false
	}
	var params struct {
		Thread struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionId"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return codexThreadInfo{}, false
	}
	threadID := strings.TrimSpace(params.Thread.ID)
	if threadID == "" {
		return codexThreadInfo{}, false
	}
	sessionID := strings.TrimSpace(params.Thread.SessionID)
	if sessionID == "" {
		sessionID = threadID
	}
	return codexThreadInfo{ID: threadID, SessionID: sessionID}, true
}
