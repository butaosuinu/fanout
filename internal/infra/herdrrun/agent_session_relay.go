package herdrrun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	agentSessionRelayModeEnv             = "FANOUT_HERDR_AGENT_SESSION_RELAY"
	agentSessionRelayControlEnv          = "FANOUT_HERDR_RELAY_CONTROL_PATH"
	agentSessionRelayExecutableEnv       = "FANOUT_HERDR_RELAY_EXECUTABLE"
	agentSessionRelayIntentEnv           = "FANOUT_HERDR_RELAY_INTENT_ID"
	agentSessionRelayNonceEnv            = "FANOUT_HERDR_RELAY_NONCE"
	agentSessionRelaySocketEnv           = "FANOUT_HERDR_RELAY_SOCKET_PATH"
	agentSessionRelayBootstrap           = "bootstrap"
	agentSessionRelayServe               = "serve"
	agentSessionRelayBootstrapLifetimeFD = 3
	agentSessionRelayListenerFD          = 3
	agentSessionRelayServeLifetimeFD     = 4
	maxAgentSessionReportBytes           = 16 << 10
)

type agentSessionRelayRequest struct {
	mode        string
	controlPath string
	executable  string
	intentID    string
	nonce       string
	socketPath  string
}

type agentSessionReport struct {
	ID     string                   `json:"id"`
	Method string                   `json:"method"`
	Params agentSessionReportParams `json:"params"`
}

type agentSessionReportParams struct {
	PaneID             string `json:"pane_id"`
	Source             string `json:"source"`
	Agent              string `json:"agent"`
	Seq                int64  `json:"seq"`
	AgentSessionID     string `json:"agent_session_id"`
	SessionStartSource string `json:"session_start_source,omitempty"`
}

type agentSessionReportResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// IsAgentSessionRelayRequest reports whether this process is an internal,
// launch-scoped relay for the official Codex SessionStart integration.
func IsAgentSessionRelayRequest() bool {
	mode := os.Getenv(agentSessionRelayModeEnv)
	return mode == agentSessionRelayBootstrap || mode == agentSessionRelayServe
}

// RunAgentSessionRelay starts or serves one restricted agent-session relay.
func RunAgentSessionRelay(errOut io.Writer) int {
	request, err := agentSessionRelayRequestFromEnvironment()
	if err == nil && request.mode == agentSessionRelayBootstrap {
		err = bootstrapAgentSessionRelay(request)
	} else if err == nil {
		err = serveAgentSessionRelay(request)
	}
	if err != nil {
		fmt.Fprintf(errOut, "fanout Herdr agent-session relay: %v\n", err)
		return 1
	}
	return 0
}

func startCodexAgentSessionRelay(
	request paneLauncherRequest,
	intent state.LaunchIntent,
) (string, *os.File, error) {
	if !codexAgentSessionRelayLaunch(intent) {
		return "", nil, nil
	}
	if !workloadLaunchNonce.MatchString(intent.Launch.Nonce) {
		return "", nil, fmt.Errorf("invalid Codex agent-session relay nonce")
	}
	lifetimeRead, lifetimeWrite, err := os.Pipe()
	if err != nil {
		return "", nil, fmt.Errorf("create Codex agent-session relay lifetime: %w", err)
	}
	defer func() {
		_ = lifetimeRead.Close() // The bootstrap child owns its duplicated descriptor after Start.
	}()
	socketPath := filepath.Join(
		launcherRuntimeDir(request.launcherPath), ".asr-"+intent.Launch.Nonce[:16]+".sock",
	)
	relay := agentSessionRelayRequest{
		mode: agentSessionRelayBootstrap, controlPath: request.controlPath,
		executable: request.launcherPath, intentID: intent.ID,
		nonce: intent.Launch.Nonce, socketPath: socketPath,
	}
	if err := runAgentSessionRelayBootstrap(relay, lifetimeRead); err != nil {
		_ = lifetimeWrite.Close() // End any detached relay started before the bootstrap error.
		return "", nil, err
	}
	return socketPath, lifetimeWrite, nil
}

func runAgentSessionRelayBootstrap(request agentSessionRelayRequest, lifetime *os.File) error {
	cmd := exec.Command(request.executable)
	cmd.Env, cmd.ExtraFiles = request.environment(), []*os.File{lifetime}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"start Codex agent-session relay: %w: %s", err, strings.TrimSpace(string(out)),
		)
	}
	return nil
}

func codexAgentSessionRelayLaunch(intent state.LaunchIntent) bool {
	return intent.Launch != nil && intent.Launch.Agent == "codex" &&
		directAgentIntegrationLaunch(intent.Launch)
}

func agentSessionRelayRequestFromEnvironment() (agentSessionRelayRequest, error) {
	request := agentSessionRelayRequest{
		mode:        os.Getenv(agentSessionRelayModeEnv),
		controlPath: filepath.Clean(os.Getenv(agentSessionRelayControlEnv)),
		executable:  filepath.Clean(os.Getenv(agentSessionRelayExecutableEnv)),
		intentID:    os.Getenv(agentSessionRelayIntentEnv), nonce: os.Getenv(agentSessionRelayNonceEnv),
		socketPath: filepath.Clean(os.Getenv(agentSessionRelaySocketEnv)),
	}
	valid := request.mode == agentSessionRelayBootstrap || request.mode == agentSessionRelayServe
	valid = valid && filepath.IsAbs(request.controlPath) && filepath.IsAbs(request.executable) &&
		filepath.IsAbs(request.socketPath) && request.intentID != "" && workloadLaunchNonce.MatchString(request.nonce) &&
		len(request.socketPath) <= maxUnixSocketPathBytes
	if !valid {
		return agentSessionRelayRequest{}, fmt.Errorf("invalid agent-session relay environment")
	}
	return request, nil
}

func (request agentSessionRelayRequest) environment() []string {
	return []string{
		agentSessionRelayModeEnv + "=" + request.mode,
		agentSessionRelayControlEnv + "=" + request.controlPath,
		agentSessionRelayExecutableEnv + "=" + request.executable,
		agentSessionRelayIntentEnv + "=" + request.intentID,
		agentSessionRelayNonceEnv + "=" + request.nonce,
		agentSessionRelaySocketEnv + "=" + request.socketPath,
	}
}

func bootstrapAgentSessionRelay(request agentSessionRelayRequest) error {
	if _, err := currentAgentSessionRelayIntent(request); err != nil {
		return err
	}
	lifetime := os.NewFile(agentSessionRelayBootstrapLifetimeFD, "herdr-agent-session-relay-lifetime")
	if lifetime == nil {
		return fmt.Errorf("relay lifetime is unavailable")
	}
	defer func() {
		_ = lifetime.Close() // The detached relay owns its duplicated descriptor after Start.
	}()
	listener, err := listenAgentSessionRelay(request.socketPath)
	if err != nil {
		return err
	}
	listenerFile, err := listener.File()
	if err != nil {
		_ = listener.Close()              // Best effort while returning the duplication error.
		_ = os.Remove(request.socketPath) // Best effort for a socket that may already be absent.
		return fmt.Errorf("duplicate relay listener: %w", err)
	}
	if err := startDetachedAgentSessionRelay(request, listenerFile, lifetime); err != nil {
		_ = listenerFile.Close()          // Best effort while returning the relay start error.
		_ = listener.Close()              // Best effort while returning the relay start error.
		_ = os.Remove(request.socketPath) // Best effort for a socket that may already be absent.
		return err
	}
	_ = listenerFile.Close() // The detached child owns its duplicated descriptor.
	_ = listener.Close()     // The detached child keeps the inherited listener open.
	return nil
}

func listenAgentSessionRelay(socketPath string) (*net.UnixListener, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on restricted relay socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	if chmodErr := os.Chmod(socketPath, 0o600); chmodErr != nil {
		_ = listener.Close()      // Best effort while returning the permission error.
		_ = os.Remove(socketPath) // Best effort for a socket that may already be absent.
		return nil, fmt.Errorf("protect relay socket: %w", chmodErr)
	}
	return listener, nil
}

func startDetachedAgentSessionRelay(
	request agentSessionRelayRequest,
	listener, lifetime *os.File,
) error {
	serve := request
	serve.mode = agentSessionRelayServe
	cmd := exec.Command(request.executable)
	cmd.Env, cmd.ExtraFiles = serve.environment(), []*os.File{listener, lifetime}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start relay server: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		killErr := cmd.Process.Kill()
		_ = cmd.Wait() // Reap the failed detached child; its killed status is expected.
		return fmt.Errorf("release relay server process: %w", errors.Join(err, killErr))
	}
	return nil
}

func serveAgentSessionRelay(request agentSessionRelayRequest) error {
	intent, err := currentAgentSessionRelayIntent(request)
	if err != nil {
		return err
	}
	file := os.NewFile(agentSessionRelayListenerFD, "herdr-agent-session-relay")
	if file == nil {
		return fmt.Errorf("relay listener is unavailable")
	}
	listener, err := net.FileListener(file)
	_ = file.Close() // FileListener owns a duplicate, so the inherited descriptor can close.
	if err != nil {
		return fmt.Errorf("adopt relay listener: %w", err)
	}
	defer func() {
		_ = listener.Close()              // Best effort after the relay has produced its final result.
		_ = os.Remove(request.socketPath) // Best effort because close may already unlink it.
	}()
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		return fmt.Errorf("relay listener is not a Unix socket")
	}
	unixListener.SetUnlinkOnClose(true)
	lifetime := os.NewFile(agentSessionRelayServeLifetimeFD, "herdr-agent-session-relay-lifetime")
	if lifetime == nil {
		return fmt.Errorf("relay lifetime is unavailable")
	}
	defer func() {
		_ = lifetime.Close() // Closing the relay also releases its launch-lifetime descriptor.
	}()
	return serveAgentSessionReports(intent, unixListener, lifetime)
}

func serveAgentSessionReports(
	intent state.LaunchIntent,
	listener *net.UnixListener,
	lifetime io.Reader,
) error {
	lifetimeEnded := make(chan struct{})
	lifetimeResult := make(chan error, 1)
	go func() {
		_, lifetimeErr := io.Copy(io.Discard, lifetime)
		lifetimeResult <- lifetimeErr
		close(lifetimeEnded)
		_ = listener.Close() // Wake AcceptUnix when the launched workload exits.
	}()
	if err := acceptAgentSessionReports(intent, listener, lifetimeEnded); err != nil {
		return err
	}
	if lifetimeErr := <-lifetimeResult; lifetimeErr != nil {
		return fmt.Errorf("watch relay lifetime: %w", lifetimeErr)
	}
	return nil
}

func acceptAgentSessionReports(
	intent state.LaunchIntent,
	listener *net.UnixListener,
	lifetimeEnded <-chan struct{},
) error {
	resumePending := intent.Kind == state.IntentResume
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if channelClosed(lifetimeEnded) {
				return nil
			}
			return fmt.Errorf("accept agent-session report: %w", err)
		}
		connectionDone := closeConnectionWhenLifetimeEnds(connection, lifetimeEnded)
		forwarded, relayErr := relayAgentSessionReport(intent, connection, resumePending)
		close(connectionDone)
		_ = connection.Close() // The relay result is authoritative; the peer may close first.
		if channelClosed(lifetimeEnded) {
			return nil
		}
		if forwarded {
			resumePending = false
		}
		_ = relayErr // Invalid or failed reports do not terminate the launch-scoped relay.
	}
}

func closeConnectionWhenLifetimeEnds(
	connection *net.UnixConn,
	lifetimeEnded <-chan struct{},
) chan struct{} {
	connectionDone := make(chan struct{})
	go func() {
		select {
		case <-lifetimeEnded:
			_ = connection.Close() // The workload lifetime is authoritative over this request.
		case <-connectionDone:
		}
	}()
	return connectionDone
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func relayAgentSessionReport(
	intent state.LaunchIntent,
	connection *net.UnixConn,
	requireResumeMatch bool,
) (bool, error) {
	if err := connection.SetDeadline(time.Now().Add(commandTimeout)); err != nil {
		return false, fmt.Errorf("bound agent-session report: %w", err)
	}
	report, err := readAgentSessionReport(connection)
	if err != nil {
		return false, err
	}
	if validateErr := validateAgentSessionReport(report, intent, requireResumeMatch); validateErr != nil {
		return false, validateErr
	}
	response, err := forwardAgentSessionReport(intent.SocketPath, report)
	if err != nil {
		return false, err
	}
	_, err = connection.Write(response)
	return acceptedAgentSessionReportResponse(response, report.ID), err
}

func acceptedAgentSessionReportResponse(response []byte, requestID string) bool {
	var envelope agentSessionReportResponse
	if err := json.Unmarshal(response, &envelope); err != nil {
		return false
	}
	result := strings.TrimSpace(string(envelope.Result))
	reportError := strings.TrimSpace(string(envelope.Error))
	return envelope.ID == requestID && result != "" && result != "null" &&
		(reportError == "" || reportError == "null")
}

func readAgentSessionReport(reader io.Reader) (agentSessionReport, error) {
	buffered := bufio.NewReaderSize(reader, maxAgentSessionReportBytes)
	line, err := buffered.ReadSlice('\n')
	if err != nil {
		return agentSessionReport{}, fmt.Errorf("read agent-session report: %w", err)
	}
	var report agentSessionReport
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return agentSessionReport{}, fmt.Errorf("decode agent-session report: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return agentSessionReport{}, err
	}
	return report, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("agent-session report has trailing JSON")
}

func validateAgentSessionReport(
	report agentSessionReport,
	intent state.LaunchIntent,
	requireResumeMatch bool,
) error {
	requirements := []bool{
		report.ID != "", len(report.ID) <= 256,
		report.Method == "pane.report_agent_session",
		report.Params.PaneID == intent.Resource.PaneID,
		report.Params.Source == "herdr:codex", report.Params.Agent == "codex",
		report.Params.Seq > 0, report.Params.AgentSessionID != "",
		strings.TrimSpace(report.Params.AgentSessionID) == report.Params.AgentSessionID,
	}
	valid := !slices.Contains(requirements, false)
	if intent.Kind == state.IntentResume && requireResumeMatch {
		valid = valid && intent.ResumeAgentSession != nil &&
			report.Params.AgentSessionID == intent.ResumeAgentSession.Value
	}
	if !valid {
		return fmt.Errorf("agent-session report does not match launch intent")
	}
	return nil
}

func currentAgentSessionRelayIntent(
	request agentSessionRelayRequest,
) (state.LaunchIntent, error) {
	journal, err := state.LoadLaunchJournalPath(request.controlPath)
	if err != nil {
		return state.LaunchIntent{}, fmt.Errorf("read relay launch intent: %w", err)
	}
	intent, found := journal.FindIntent(request.intentID)
	valid := found && intent.Status == state.IntentRealized && intent.Launch != nil &&
		intent.Launch.Nonce == request.nonce && intent.Launch.LauncherReady && intent.Launch.TokenIssued &&
		codexAgentSessionRelayLaunch(intent)
	if !valid {
		return state.LaunchIntent{}, fmt.Errorf("agent-session relay launch intent is not active")
	}
	return intent, nil
}

func forwardAgentSessionReport(socketPath string, report agentSessionReport) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect restricted report to owned Herdr socket: %w", err)
	}
	defer func() {
		_ = connection.Close() // The request result is more useful than a close error.
	}()
	if deadlineErr := connection.SetDeadline(time.Now().Add(commandTimeout)); deadlineErr != nil {
		return nil, fmt.Errorf("bound restricted agent-session report: %w", deadlineErr)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	if _, writeErr := connection.Write(append(payload, '\n')); writeErr != nil {
		return nil, fmt.Errorf("write restricted agent-session report: %w", writeErr)
	}
	response, err := bufio.NewReaderSize(connection, maxAgentSessionReportBytes).ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("read restricted agent-session response: %w", err)
	}
	return response, nil
}
