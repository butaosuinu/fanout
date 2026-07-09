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
	codexRemoteTUIThreadTimeout  = 10 * time.Second
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
	// SetAgentState reports best-effort pane state while the Plan Mode prompt is
	// being submitted. The cmd entrypoint wires it to tmuxrun.SetPaneAgentState;
	// nil (tests, direct calls) means no reporting.
	SetAgentState func(state string)
	// SendPlanPrompt types the initial /plan line into the already-running TUI.
	// The cmd entrypoint wires this to tmux send-keys so Codex sees the same
	// composer slash-command path a user would type by hand.
	SendPlanPrompt func(prompt string) error
}

type codexThreadInfo struct {
	ID        string
	SessionID string
}

type codexTurnCompletion struct {
	Matched bool
	Status  string
}

// RunPlanTUI runs the fanout Codex Plan Mode controller: it starts an
// app-server, lets the interactive Codex TUI own the Plan Mode turn (or
// resumes an existing one), and reports readiness through the status file.
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
	planPrompt := ""
	if thread.ID == "" {
		if err = initializeCodexPlanClient(client, cfg.Version); err != nil {
			return err
		}
		planPrompt = codexPlanStartupPrompt(cfg.Prompt)
	} else {
		err = initializeCodexPlanClient(client, cfg.Version)
		if err != nil {
			return err
		}
	}

	tui, tuiDone, err := startCodexRemoteTUI(cfg.CodexPath, server.Addr, codexRemoteTUIResumeID(thread), "", stdout, stderr)
	if err != nil {
		return err
	}
	tuiStopped := false
	defer func() {
		if !tuiStopped {
			stopProcess(tui, tuiDone)
		}
	}()

	if thread.ID == "" {
		threadReady, watchDone := drainCodexAppServerUntilThreadStartedCmd(client, setState)
		drainDone = watchDone
		if drainDone, err = waitForCodexRemoteTUIStartup(tuiDone, drainDone, server); err != nil {
			return err
		}
		if err = sendCodexPlanStartupPrompt(cfg.SendPlanPrompt, planPrompt); err != nil {
			return err
		}
		reportCodexPlanAgentState(setState, "working")
		thread, err = waitForCodexPlanThreadStarted(tuiDone, drainDone, threadReady, server)
		if err != nil {
			return err
		}
		closeCodexAppServerObserver(client, drainDone)
		drainDone = nil
	} else {
		if drainDone, err = waitForCodexRemoteTUIStartup(tuiDone, drainDone, server); err != nil {
			return err
		}
		closeCodexAppServerObserver(client, drainDone)
		drainDone = nil
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

func waitForCodexPlanThreadStarted(tuiDone <-chan error, drainDone <-chan error, threadReady <-chan codexThreadInfo, server *appServer) (codexThreadInfo, error) {
	startupTimer := time.NewTimer(codexRemoteTUIThreadTimeout)
	defer startupTimer.Stop()
	for {
		select {
		case thread := <-threadReady:
			if strings.TrimSpace(thread.ID) != "" {
				return thread, nil
			}
		case tuiErr := <-tuiDone:
			if tuiErr != nil {
				return codexThreadInfo{}, fmt.Errorf("codex TUI exited before thread startup: %w", tuiErr)
			}
			return codexThreadInfo{}, fmt.Errorf("codex TUI exited before thread startup")
		case drainErr := <-drainDone:
			if drainErr != nil {
				return codexThreadInfo{}, fmt.Errorf("codex app-server request handling failed during TUI startup: %w", drainErr)
			}
			return codexThreadInfo{}, fmt.Errorf("codex app-server disconnected before Codex TUI started a thread")
		case <-server.Done():
			if _, serverErr := server.Exited(); serverErr != nil {
				return codexThreadInfo{}, fmt.Errorf("codex app-server exited during TUI startup: %w%s", serverErr, serverLogSuffix(server))
			}
			return codexThreadInfo{}, fmt.Errorf("codex app-server exited during TUI startup%s", serverLogSuffix(server))
		case <-startupTimer.C:
			return codexThreadInfo{}, fmt.Errorf("timed out after %s waiting for Codex TUI to start a Plan Mode thread", codexRemoteTUIThreadTimeout)
		}
	}
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
	args := []string{"--remote", remoteAddr}
	if strings.TrimSpace(resumeID) != "" {
		args = append(args, "resume", resumeID)
	}
	if strings.TrimSpace(prompt) != "" {
		args = append(args, "--", prompt)
	}
	return args
}

func codexPlanStartupPrompt(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	if prompt == "" {
		return "/plan"
	}
	return "/plan " + prompt
}

func sendCodexPlanStartupPrompt(send func(string) error, prompt string) error {
	if send == nil {
		return fmt.Errorf("codex plan mode prompt injection is not configured")
	}
	if err := send(prompt); err != nil {
		return fmt.Errorf("send Codex Plan Mode prompt: %w", err)
	}
	return nil
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

func drainCodexAppServerUntilThreadStartedCmd(client *client, setState func(string)) (<-chan codexThreadInfo, chan error) {
	threadReady := make(chan codexThreadInfo, 1)
	done := make(chan error, 1)
	go func() { done <- drainCodexAppServerNotificationsUntilClosedWithThread(client, setState, threadReady) }()
	return threadReady, done
}

func drainCodexAppServerUntilClosed(client *client, setState func(string)) error {
	return drainCodexAppServerNotificationsUntilClosed(client, setState)
}

type appServerReceiver interface {
	receive() (appServerMessage, error)
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
