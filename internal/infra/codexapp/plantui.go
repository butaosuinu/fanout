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
	"sync"
	"time"
)

const (
	codexRemoteAppConnectTimeout       = 10 * time.Second
	codexRemoteTUIStartupGrace         = 3 * time.Second
	codexRemoteTUIThreadStartupTimeout = 10 * time.Second
	codexPlanTUIClientName             = "fanout-codex-plan-tui"
	codexPlanApprovalUIPollInterval    = 250 * time.Millisecond
	codexPlanApprovalUIPrompt          = "Implement this plan?"
	codexPlanTUIWorkingPrompt          = "esc to interrupt"
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
	// CapturePlanScreen snapshots the running TUI pane for best-effort state
	// telemetry. It is not a startup gate: a Plan turn may legitimately take
	// longer than any fixed timeout before the approval prompt appears.
	CapturePlanScreen func() (string, error)
	// SetAgentState reports best-effort pane state while the Plan Mode turn
	// runs and once the approval UI is visible. The cmd entrypoint wires it to
	// tmuxrun.SetPaneAgentState; nil (tests, direct calls) means no reporting.
	SetAgentState func(state string)
}

type codexThreadInfo struct {
	ID         string
	SessionID  string
	Model      string
	PlanEffort string
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

type codexRemoteTUIConfig struct {
	CodexPath       string
	ResumeThreadID  string
	ResumeSessionID string
	Version         string
	ClientName      string
	SetAgentState   func(string)
	Stdout          io.Writer
	Stderr          io.Writer
}

// codexRemoteTUISession owns the app-server observer and the remotely attached
// interactive TUI shared by the Plan and team controllers. Mode-specific turn
// handling stays with those controllers.
type codexRemoteTUISession struct {
	server            *appServer
	client            *client
	thread            codexThreadInfo
	freshThread       bool
	tui               *exec.Cmd
	tuiDone           chan error
	signals           <-chan os.Signal
	stopSignalCleanup func()

	mu             sync.Mutex
	tuiStopped     bool
	drainDone      chan error
	observerDone   <-chan struct{}
	shutdownSignal os.Signal
	closeOnce      sync.Once
}

func startCodexRemoteTUISession(cfg codexRemoteTUIConfig) (_ *codexRemoteTUISession, err error) {
	session := &codexRemoteTUISession{}
	defer func() {
		if err != nil {
			err = session.finish(err)
		}
	}()
	session.signals, session.stopSignalCleanup = installCodexControllerSignals()

	session.server, err = startAppServer(cfg.CodexPath)
	if err != nil {
		return nil, err
	}

	session.client, err = connectAppServerWithSignals(session.server, codexRemoteAppConnectTimeout, session.signals)
	if err != nil {
		return nil, err
	}
	if _, err = waitForCodexOperation(session.signals, func() (struct{}, error) {
		return struct{}{}, initializeCodexClient(session.client, cfg.Version, cfg.ClientName)
	}); err != nil {
		return nil, err
	}

	session.thread = codexThreadInfo{
		ID:        strings.TrimSpace(cfg.ResumeThreadID),
		SessionID: strings.TrimSpace(cfg.ResumeSessionID),
	}
	if session.thread.ID != "" && session.thread.SessionID == "" {
		session.thread.SessionID = session.thread.ID
	}
	session.freshThread = session.thread.ID == ""
	resumeID := codexRemoteTUIResumeID(session.thread)
	if session.freshThread {
		resumeID = ""
	}
	session.tui, session.tuiDone, err = startCodexRemoteTUI(cfg.CodexPath, session.server.Addr, resumeID, cfg.Stdout, cfg.Stderr)
	if err != nil {
		return nil, err
	}
	session.setDrainDone(completedAppServerDrain())
	drainDone := session.currentDrainDone()
	if drainDone, err = waitForCodexRemoteTUIStartup(session.tuiDone, drainDone, session.server, session.signals); err != nil {
		return nil, err
	}
	session.setDrainDone(drainDone)
	if session.freshThread {
		session.thread, err = waitForCodexRemoteTUIThread(session.tuiDone, session.client, session.server, cfg.SetAgentState, codexRemoteTUIThreadStartupTimeout, session.signals)
		if err != nil {
			return nil, err
		}
	}
	return session, nil
}

func (s *codexRemoteTUISession) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		tuiStopped := s.tuiStopped
		drainDone := s.drainDone
		observerDone := s.observerDone
		shutdownSignal := s.shutdownSignal
		s.mu.Unlock()

		if s.tui != nil && !tuiStopped {
			stopProcess(s.tui, s.tuiDone, isInterruptSignal(shutdownSignal))
			s.setTUIStopped(true)
		}
		if s.client != nil {
			awaitDrainAfterTUIExit(s.client, drainDone)
			s.client.Close()
		}
		observerExited := waitForObserverExit(observerDone, processShutdownTimeout)
		if s.server != nil {
			s.server.Close()
		}
		if !observerExited {
			_ = waitForObserverExit(observerDone, processShutdownTimeout)
		}
		if s.stopSignalCleanup != nil {
			s.stopSignalCleanup()
		}
	})
}

func (s *codexRemoteTUISession) finish(err error) error {
	if s == nil {
		return err
	}
	if sig := signalFromError(err); sig != nil {
		s.setShutdownSignal(sig)
	} else {
		select {
		case sig := <-s.signals:
			s.setShutdownSignal(sig)
			err = newCodexSignalError(sig)
		default:
		}
	}
	s.Close()
	// A signal can arrive while Close is waiting for the remote TUI or
	// app-server. Capture that race before deciding whether the pane group needs
	// the final fallback kill.
	select {
	case sig := <-s.signals:
		s.setShutdownSignal(sig)
	default:
	}
	if sig := s.currentShutdownSignal(); requiresPaneGroupFallback(sig) {
		forceCurrentPaneProcessGroup()
	}
	if sig := s.currentShutdownSignal(); sig != nil {
		return newCodexSignalError(sig)
	}
	return err
}

func (s *codexRemoteTUISession) setTUIStopped(stopped bool) {
	s.mu.Lock()
	s.tuiStopped = stopped
	s.mu.Unlock()
}

func (s *codexRemoteTUISession) setDrainDone(done chan error) {
	s.mu.Lock()
	s.drainDone = done
	s.mu.Unlock()
}

func (s *codexRemoteTUISession) currentDrainDone() chan error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drainDone
}

func (s *codexRemoteTUISession) setObserverDone(done <-chan struct{}) {
	s.mu.Lock()
	s.observerDone = done
	s.mu.Unlock()
}

func (s *codexRemoteTUISession) setShutdownSignal(sig os.Signal) {
	if sig == nil {
		return
	}
	s.mu.Lock()
	if s.shutdownSignal == nil {
		s.shutdownSignal = sig
	}
	s.mu.Unlock()
}

func (s *codexRemoteTUISession) currentShutdownSignal() os.Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownSignal
}

// RunPlanTUI runs the fanout Codex Plan Mode controller: it starts an
// app-server, creates a native Plan Mode thread through app-server (or resumes
// an existing one), attaches the interactive Codex TUI, starts the initial turn,
// and reports readiness once that turn has been accepted. Plan generation and
// approval remain owned by the TUI and are not bounded as startup work.
func RunPlanTUI(cfg TUIConfig, stdout, stderr io.Writer) (err error) {
	ready := false
	defer func() {
		_, signaled := SignalErrorExitCode(err)
		if err != nil && !ready && !signaled {
			_ = writeStatus(cfg.StatusFile, Status{
				Status: statusFailed,
				Error:  err.Error(),
			})
		}
	}()

	setState := cfg.SetAgentState
	if setState == nil {
		setState = func(string) {}
	}
	session, err := startCodexRemoteTUISession(codexRemoteTUIConfig{
		CodexPath:       cfg.CodexPath,
		ResumeThreadID:  cfg.ResumeThreadID,
		ResumeSessionID: cfg.ResumeSessionID,
		Version:         cfg.Version,
		ClientName:      codexPlanTUIClientName,
		SetAgentState:   setState,
		Stdout:          stdout,
		Stderr:          stderr,
	})
	if err != nil {
		return err
	}
	defer func() { err = session.finish(err) }()

	thread := session.thread
	freshThread := session.freshThread
	var cwd string
	if freshThread {
		var cwdErr error
		cwd, cwdErr = os.Getwd()
		if cwdErr != nil {
			return fmt.Errorf("resolve current directory: %w", cwdErr)
		}
	}

	if freshThread {
		thread, err = waitForCodexOperation(session.signals, func() (codexThreadInfo, error) {
			return configureCodexPlanThread(session.client, thread, cwd)
		})
		if err != nil {
			return err
		}
		reportCodexPlanAgentState(setState, "working")
		var turnStart codexPlanTurnStartResult
		turnStart, err = waitForCodexOperation(session.signals, func() (codexPlanTurnStartResult, error) {
			return startCodexPlanTurn(session.client, thread, cwd, cfg.Prompt)
		})
		if err != nil {
			return err
		}
		if turnStart.Completed {
			session.setDrainDone(completedAppServerDrain())
		} else {
			session.setDrainDone(drainCodexAppServerDuringStartupCmd(session.client, setState, thread.ID, turnStart.TurnID))
		}
	}

	if err = writeStatus(cfg.StatusFile, Status{
		Status:    statusReady,
		ThreadID:  thread.ID,
		SessionID: thread.SessionID,
		Remote:    session.server.Addr,
	}); err != nil {
		return fmt.Errorf("write Codex Plan TUI status: %w", err)
	}
	ready = true
	tuiExited, err := waitForCodexTUIAfterReady(session.tuiDone, session.currentDrainDone(), session.client, setState, cfg.CapturePlanScreen, freshThread, false, session.signals)
	session.setDrainDone(nil) // consumed or awaited inside waitForCodexTUIAfterReady
	session.setTUIStopped(tuiExited)
	return err
}

func waitForCodexRemoteTUIStartup(tuiDone <-chan error, drainDone chan error, server *appServer, signalChannels ...<-chan os.Signal) (chan error, error) {
	signals := firstSignalChannel(signalChannels)
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
		case sig := <-signals:
			return nil, newCodexSignalError(sig)
		}
	}
	if drainDone == nil {
		drainDone = completedAppServerDrain()
	}
	return drainDone, nil
}

func waitForCodexRemoteTUIThread(tuiDone <-chan error, client appServerStartupClient, server *appServer, setState func(string), timeout time.Duration, signalChannels ...<-chan os.Signal) (codexThreadInfo, error) {
	signals := firstSignalChannel(signalChannels)
	type threadWaitResult struct {
		thread codexThreadInfo
		err    error
	}
	waitDone := make(chan threadWaitResult, 1)
	go func() {
		thread, err := receiveCodexRemoteTUIThread(client, setState)
		waitDone <- threadWaitResult{thread: thread, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-waitDone:
		if result.err != nil {
			return codexThreadInfo{}, result.err
		}
		if strings.TrimSpace(result.thread.ID) == "" {
			return codexThreadInfo{}, fmt.Errorf("codex TUI thread observer exited before reporting a thread")
		}
		return result.thread, nil
	case tuiErr := <-tuiDone:
		if tuiErr != nil {
			return codexThreadInfo{}, fmt.Errorf("codex TUI exited before reporting its active thread: %w", tuiErr)
		}
		return codexThreadInfo{}, fmt.Errorf("codex TUI exited before reporting its active thread")
	case <-server.Done():
		if _, serverErr := server.Exited(); serverErr != nil {
			return codexThreadInfo{}, fmt.Errorf("codex app-server exited before TUI reported its active thread: %w%s", serverErr, serverLogSuffix(server))
		}
		return codexThreadInfo{}, fmt.Errorf("codex app-server exited before TUI reported its active thread%s", serverLogSuffix(server))
	case <-timer.C:
		return codexThreadInfo{}, fmt.Errorf("codex TUI did not report an active thread within %s", timeout)
	case sig := <-signals:
		return codexThreadInfo{}, newCodexSignalError(sig)
	}
}

func receiveCodexRemoteTUIThread(client appServerStartupClient, setState func(string)) (codexThreadInfo, error) {
	for {
		msg, err := client.receive()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
				return codexThreadInfo{}, fmt.Errorf("codex app-server observer closed before TUI reported its active thread: %w", err)
			}
			if errors.Is(err, io.EOF) {
				return codexThreadInfo{}, fmt.Errorf("codex app-server disconnected before TUI reported its active thread: %w", err)
			}
			return codexThreadInfo{}, err
		}
		if isServerRequest(msg) {
			if err := handleServerRequestWithState(client, msg, setState); err != nil {
				return codexThreadInfo{}, err
			}
			continue
		}
		if thread, ok := codexThreadStartedNotification(msg); ok {
			return thread, nil
		}
		if state := codexTurnNotificationAgentState(msg); state != "" {
			reportCodexPlanAgentState(setState, state)
		}
	}
}

func initializeCodexClient(client sessionClient, version, clientName string) error {
	if strings.TrimSpace(clientName) == "" {
		return fmt.Errorf("initialize codex app-server client: client name is required")
	}
	if _, err := client.Request("fanout-init", "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    clientName,
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

func configureCodexPlanThread(client requester, thread codexThreadInfo, cwd string) (codexThreadInfo, error) {
	settings, err := resolveCodexPlanThreadSettings(client, cwd)
	if err != nil {
		return codexThreadInfo{}, err
	}
	return applyCodexPlanThreadSettings(client, thread, settings)
}

func resolveCodexPlanThreadSettings(client requester, cwd string) (codexResolvedSettings, error) {
	modeResult, err := client.Request("fanout-modes", "collaborationMode/list", map[string]any{})
	if err != nil {
		return codexResolvedSettings{}, err
	}
	planEffort, err := codexPlanEffort(modeResult)
	if err != nil {
		return codexResolvedSettings{}, err
	}

	settings, err := resolveCodexSettings(client, cwd, planEffort)
	if err != nil {
		return codexResolvedSettings{}, err
	}
	return settings, nil
}

func applyCodexPlanThreadSettings(client requester, thread codexThreadInfo, settings codexResolvedSettings) (codexThreadInfo, error) {
	thread.Model = settings.Model
	thread.PlanEffort = settings.ReasoningEffort

	if _, err := client.Request("fanout-plan-mode", "thread/settings/update", codexPlanSettingsUpdateParams(thread.ID, settings.Model, settings.ReasoningEffort)); err != nil {
		if !isUnsupportedCodexAppServerMethod(err) {
			return codexThreadInfo{}, err
		}
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
	params := codexTurnStartParams(thread.ID, cwd, thread.Model, prompt, codexPlanCollaborationMode(thread.Model, planEffortOrDefault(thread.PlanEffort)))
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

func planEffortOrDefault(effort string) string {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return "medium"
	}
	return effort
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
	}

	modelResult, modelErr := client.Request("fanout-models", "model/list", map[string]any{
		"includeHidden": true,
	})
	if modelErr != nil {
		if settings.Model != "" {
			return settings, nil
		}
		if configErr != nil {
			return codexResolvedSettings{}, fmt.Errorf("resolve codex model: config/read failed: %w; model/list failed: %w", configErr, modelErr)
		}
		return codexResolvedSettings{}, fmt.Errorf("resolve codex model from model/list: %w", modelErr)
	}
	selection, err := modelListSelection(modelResult, settings.Model)
	if err != nil {
		if settings.Model != "" {
			return settings, nil
		}
		if configErr != nil {
			return codexResolvedSettings{}, fmt.Errorf("resolve codex model: config/read failed: %w; model/list failed: %w", configErr, err)
		}
		return codexResolvedSettings{}, err
	}
	settings.Model = selection.Model
	if len(selection.SupportedReasoningEfforts) > 0 {
		settings.ReasoningEffort = supportedReasoningEffort(settings.ReasoningEffort, selection.SupportedReasoningEfforts)
	}
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
	selection, err := modelListSelection(raw, "")
	if err != nil {
		return "", err
	}
	return selection.Model, nil
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
	// Use separate passes so server list order cannot override this priority:
	// explicit preference (including hidden), visible default, then first visible.
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		for _, model := range res.Data {
			if modelName(model.Model, model.ID) != preferred && strings.TrimSpace(model.ID) != preferred {
				continue
			}
			if name := modelName(model.Model, model.ID); name != "" {
				return codexModelSelection{
					Model:                     name,
					SupportedReasoningEfforts: modelSupportedReasoningEfforts(model.SupportedReasoningEfforts, model.SupportedReasoningEffortsFallback),
				}, nil
			}
		}
	}
	for _, model := range res.Data {
		if model.Hidden || !model.IsDefault {
			continue
		}
		if name := modelName(model.Model, model.ID); name != "" {
			return codexModelSelection{
				Model:                     name,
				SupportedReasoningEfforts: modelSupportedReasoningEfforts(model.SupportedReasoningEfforts, model.SupportedReasoningEffortsFallback),
			}, nil
		}
	}
	for _, model := range res.Data {
		if model.Hidden {
			continue
		}
		if name := modelName(model.Model, model.ID); name != "" {
			return codexModelSelection{
				Model:                     name,
				SupportedReasoningEfforts: modelSupportedReasoningEfforts(model.SupportedReasoningEfforts, model.SupportedReasoningEffortsFallback),
			}, nil
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
	for _, supportedEffort := range slices.Backward(supported) {
		if effort := strings.TrimSpace(supportedEffort); effort != "" {
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
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
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
	if strings.TrimSpace(thread.ID) != "" {
		return thread.ID
	}
	return thread.SessionID
}

func waitForCodexTUIAfterReady(tuiDone <-chan error, drainDone <-chan error, client *client, setState func(string), capturePlanScreen func() (string, error), initialPlanTurn, watchingAppServer bool, signalChannels ...<-chan os.Signal) (bool, error) {
	signals := firstSignalChannel(signalChannels)
	screenTracker := newCodexPlanScreenTracker(capturePlanScreen, setState, initialPlanTurn)
	screenTicks := screenTracker.ticks()
	defer screenTracker.stop()
	for {
		select {
		case tuiErr := <-tuiDone:
			awaitDrainAfterTUIExit(client, drainDone)
			return true, tuiErr
		case drainErr := <-drainDone:
			if !watchingAppServer {
				screenTracker.initialTurnCompleted()
			}
			if drainErr != nil {
				if !watchingAppServer && canWatchAppServer(client) {
					watchingAppServer = true
					drainDone = drainCodexAppServerUntilClosedCmd(client, setState)
					continue
				}
				drainDone = nil
				continue
			}
			if !watchingAppServer && canWatchAppServer(client) {
				watchingAppServer = true
				drainDone = drainCodexAppServerUntilClosedCmd(client, setState)
				continue
			}
			drainDone = nil
		case <-screenTicks:
			screenTracker.poll()
		case sig := <-signals:
			return false, newCodexSignalError(sig)
		}
	}
}

type codexPlanScreenPhase int

const (
	codexPlanScreenPlanning codexPlanScreenPhase = iota
	codexPlanScreenAwaitingApproval
	codexPlanScreenApprovalVisible
	codexPlanScreenRunning
	codexPlanScreenIdle
)

type codexPlanScreenTracker struct {
	capture  func() (string, error)
	setState func(string)
	ticker   *time.Ticker
	phase    codexPlanScreenPhase
	last     string
}

func newCodexPlanScreenTracker(capture func() (string, error), setState func(string), initialPlanTurn bool) *codexPlanScreenTracker {
	phase := codexPlanScreenIdle
	if initialPlanTurn {
		phase = codexPlanScreenPlanning
	}
	tracker := &codexPlanScreenTracker{capture: capture, setState: setState, phase: phase}
	if capture != nil {
		tracker.ticker = time.NewTicker(codexPlanApprovalUIPollInterval)
	}
	return tracker
}

func (t *codexPlanScreenTracker) initialTurnCompleted() {
	if t != nil && t.phase == codexPlanScreenPlanning {
		t.phase = codexPlanScreenAwaitingApproval
	}
}

func (t *codexPlanScreenTracker) ticks() <-chan time.Time {
	if t == nil || t.ticker == nil {
		return nil
	}
	return t.ticker.C
}

func (t *codexPlanScreenTracker) stop() {
	if t != nil && t.ticker != nil {
		t.ticker.Stop()
	}
}

func (t *codexPlanScreenTracker) poll() {
	if t == nil || t.capture == nil {
		return
	}
	screen, err := t.capture()
	if err != nil {
		return
	}
	state, phase := codexPlanScreenAgentState(screen, t.phase)
	t.phase = phase
	if state == "" || state == t.last {
		return
	}
	t.last = state
	reportCodexPlanAgentState(t.setState, state)
}

func codexPlanScreenAgentState(screen string, phase codexPlanScreenPhase) (string, codexPlanScreenPhase) {
	switch {
	case codexPlanApprovalUIReady(screen):
		return "plan", codexPlanScreenApprovalVisible
	case phase == codexPlanScreenPlanning && codexPlanScreenWorking(screen):
		return "working", codexPlanScreenPlanning
	case phase == codexPlanScreenPlanning:
		return "", phase
	case codexPlanScreenWorking(screen):
		return "working", codexPlanScreenRunning
	case phase == codexPlanScreenApprovalVisible:
		return "working", codexPlanScreenRunning
	case phase == codexPlanScreenRunning:
		return "idle", codexPlanScreenIdle
	default:
		return "", phase
	}
}

func codexPlanScreenWorking(screen string) bool {
	return strings.Contains(screen, codexPlanTUIWorkingPrompt) || strings.Contains(screen, "Working (")
}

func codexPlanApprovalUIReady(screen string) bool {
	return strings.Contains(screen, codexPlanApprovalUIPrompt)
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
			if completion.Status != "completed" {
				if state := codexTurnCompletionAgentState(completion); state != "" {
					reportCodexPlanAgentState(setState, state)
				}
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
		return "idle"
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
