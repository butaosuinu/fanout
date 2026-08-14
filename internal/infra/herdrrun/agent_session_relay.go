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

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	agentSessionRelayModeEnv       = "FANOUT_HERDR_AGENT_SESSION_RELAY"
	agentSessionRelayControlEnv    = "FANOUT_HERDR_RELAY_CONTROL_PATH"
	agentSessionRelayExecutableEnv = "FANOUT_HERDR_RELAY_EXECUTABLE"
	agentSessionRelayIntentEnv     = "FANOUT_HERDR_RELAY_INTENT_ID"
	agentSessionRelayNonceEnv      = "FANOUT_HERDR_RELAY_NONCE"
	agentSessionRelayStateEnv      = "FANOUT_HERDR_RELAY_STATE_PATH"
	agentSessionRelaySocketEnv     = "FANOUT_HERDR_RELAY_SOCKET_PATH"
	agentSessionRelayBootstrap     = "bootstrap"
	agentSessionRelayServe         = "serve"
	agentSessionRelayListenerFD    = 3
	agentSessionRelayReadyFD       = 4
	maxAgentSessionReportBytes     = 16 << 10
)

type agentSessionRelayRequest struct {
	mode        string
	controlPath string
	executable  string
	intentID    string
	nonce       string
	statePath   string
	socketPath  string
}

type agentSessionReport struct {
	ID     string                   `json:"id"`
	Method string                   `json:"method"`
	Params agentSessionReportParams `json:"params"`
}

type agentSessionReportResponse struct {
	ID     string           `json:"id"`
	Result *paneRunResult   `json:"result"`
	Error  *json.RawMessage `json:"error"`
}

type agentSessionReportParams struct {
	PaneID             string `json:"pane_id"`
	Source             string `json:"source"`
	Agent              string `json:"agent"`
	Seq                int64  `json:"seq"`
	AgentSessionID     string `json:"agent_session_id"`
	SessionStartSource string `json:"session_start_source,omitempty"`
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
	intent state.HerdrIntent,
) (string, error) {
	if !directCodexIntegrationLaunch(intent) {
		return "", nil
	}
	if !workloadLaunchNonce.MatchString(intent.Launch.Nonce) {
		return "", fmt.Errorf("invalid Codex agent-session relay nonce")
	}
	socketPath := filepath.Join(
		launcherRuntimeDir(request.launcherPath), ".asr-"+intent.Launch.Nonce[:16]+".sock",
	)
	relay := agentSessionRelayRequest{
		mode: agentSessionRelayBootstrap, controlPath: request.controlPath,
		executable: request.launcherPath, intentID: intent.ID,
		nonce: intent.Launch.Nonce, statePath: intent.Launch.AgentSessionStatePath,
		socketPath: socketPath,
	}
	cmd := exec.Command(request.launcherPath)
	cmd.Env = relay.environment()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("start Codex agent-session relay: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return socketPath, nil
}

func directCodexIntegrationLaunch(intent state.HerdrIntent) bool {
	launch := intent.Launch
	directKind := intent.Kind == state.HerdrIntentWorktree || intent.Kind == state.HerdrIntentResume
	return directKind && launch != nil && launch.Agent == "codex" &&
		launch.CodexPlanStatusPath == "" && launch.CodexTeamStatusPath == ""
}

func agentSessionRelayRequestFromEnvironment() (agentSessionRelayRequest, error) {
	request := agentSessionRelayRequest{
		mode:        os.Getenv(agentSessionRelayModeEnv),
		controlPath: filepath.Clean(os.Getenv(agentSessionRelayControlEnv)),
		executable:  filepath.Clean(os.Getenv(agentSessionRelayExecutableEnv)),
		intentID:    os.Getenv(agentSessionRelayIntentEnv), nonce: os.Getenv(agentSessionRelayNonceEnv),
		statePath:  filepath.Clean(os.Getenv(agentSessionRelayStateEnv)),
		socketPath: filepath.Clean(os.Getenv(agentSessionRelaySocketEnv)),
	}
	valid := request.mode == agentSessionRelayBootstrap || request.mode == agentSessionRelayServe
	valid = valid && filepath.IsAbs(request.controlPath) && filepath.IsAbs(request.executable) &&
		filepath.IsAbs(request.statePath) &&
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
		agentSessionRelayStateEnv + "=" + request.statePath,
		agentSessionRelaySocketEnv + "=" + request.socketPath,
	}
}

func bootstrapAgentSessionRelay(request agentSessionRelayRequest) error {
	if _, err := currentAgentSessionRelayIntent(request); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: request.socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on restricted relay socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	cleanup := func() {
		_ = listener.Close()              // Best effort while returning the primary bootstrap error.
		_ = os.Remove(request.socketPath) // Best effort for a socket that may already be absent.
	}
	if chmodErr := os.Chmod(request.socketPath, 0o600); chmodErr != nil {
		cleanup()
		return fmt.Errorf("protect relay socket: %w", chmodErr)
	}
	listenerFile, err := listener.File()
	if err != nil {
		cleanup()
		return fmt.Errorf("duplicate relay listener: %w", err)
	}
	if err := startDetachedAgentSessionRelay(request, listenerFile); err != nil {
		_ = listenerFile.Close() // Best effort while returning the relay start error.
		cleanup()
		return err
	}
	_ = listenerFile.Close() // The detached child owns its duplicated descriptor.
	_ = listener.Close()     // The detached child keeps the inherited listener open.
	return nil
}

func startDetachedAgentSessionRelay(request agentSessionRelayRequest, listener *os.File) error {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create relay readiness pipe: %w", err)
	}
	defer func() {
		_ = readyReader.Close() // Best effort after readiness or process failure.
		_ = readyWriter.Close() // Best effort after handing the descriptor to the child.
	}()
	serve := request
	serve.mode = agentSessionRelayServe
	cmd := exec.Command(request.executable)
	cmd.Env, cmd.ExtraFiles = serve.environment(), []*os.File{listener, readyWriter}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start relay server: %w", err)
	}
	if err := readyWriter.Close(); err != nil {
		return stopUnreadyAgentSessionRelay(cmd, fmt.Errorf("close relay readiness writer: %w", err))
	}
	if err := waitForAgentSessionRelayReady(readyReader); err != nil {
		return stopUnreadyAgentSessionRelay(cmd, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return stopUnreadyAgentSessionRelay(cmd, fmt.Errorf("release relay server process: %w", err))
	}
	return nil
}

func waitForAgentSessionRelayReady(reader *os.File) error {
	if err := reader.SetReadDeadline(time.Now().Add(commandTimeout)); err != nil {
		return fmt.Errorf("bound relay readiness: %w", err)
	}
	var marker [1]byte
	if _, err := io.ReadFull(reader, marker[:]); err != nil {
		return fmt.Errorf("wait for relay readiness: %w", err)
	}
	if marker[0] != 1 {
		return fmt.Errorf("wait for relay readiness: unexpected marker")
	}
	return nil
}

func stopUnreadyAgentSessionRelay(cmd *exec.Cmd, cause error) error {
	killErr := cmd.Process.Kill()
	_ = cmd.Wait() // Reap the failed detached child; its killed status is expected.
	return errors.Join(cause, killErr)
}

func serveAgentSessionRelay(request agentSessionRelayRequest) error {
	intent, err := currentAgentSessionRelayIntent(request)
	if err != nil {
		return err
	}
	ready := os.NewFile(agentSessionRelayReadyFD, "herdr-agent-session-relay-ready")
	if ready == nil {
		return fmt.Errorf("relay readiness descriptor is unavailable")
	}
	defer func() {
		_ = ready.Close() // Best effort after readiness or an earlier setup failure.
	}()
	listener, err := adoptAgentSessionRelayListener(intent)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()              // Best effort after the relay has produced its final result.
		_ = os.Remove(request.socketPath) // Best effort because close may already unlink it.
	}()
	if _, err := ready.Write([]byte{1}); err != nil {
		return fmt.Errorf("signal relay readiness: %w", err)
	}
	return acceptAgentSessionReport(request, intent, listener)
}

func adoptAgentSessionRelayListener(intent state.HerdrIntent) (*net.UnixListener, error) {
	file := os.NewFile(agentSessionRelayListenerFD, "herdr-agent-session-relay")
	if file == nil {
		return nil, fmt.Errorf("relay listener is unavailable")
	}
	listener, err := net.FileListener(file)
	_ = file.Close() // FileListener owns a duplicate, so the inherited descriptor can close.
	if err != nil {
		return nil, fmt.Errorf("adopt relay listener: %w", err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close() // Best effort after rejecting the inherited descriptor type.
		return nil, fmt.Errorf("relay listener is not a Unix socket")
	}
	unixListener.SetUnlinkOnClose(true)
	if err := unixListener.SetDeadline(time.UnixMilli(intent.ExpiresUnixMS)); err != nil {
		_ = unixListener.Close() // Best effort after failing to bind the relay lifetime.
		return nil, fmt.Errorf("bound relay lifetime: %w", err)
	}
	return unixListener, nil
}

func acceptAgentSessionReport(
	request agentSessionRelayRequest,
	intent state.HerdrIntent,
	listener *net.UnixListener,
) error {
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			return fmt.Errorf("accept agent-session report: %w", err)
		}
		err = relayAgentSessionReport(request, intent, connection)
		_ = connection.Close() // The relay result is authoritative; the peer may close first.
		if err == nil {
			return nil
		}
	}
}

func relayAgentSessionReport(
	request agentSessionRelayRequest,
	intent state.HerdrIntent,
	connection net.Conn,
) error {
	deadline, err := agentSessionRelayConnectionDeadline(intent, time.Now())
	if err != nil {
		return err
	}
	if deadlineErr := connection.SetDeadline(deadline); deadlineErr != nil {
		return fmt.Errorf("bound agent-session report: %w", deadlineErr)
	}
	report, err := readAgentSessionReport(connection)
	if err != nil {
		return err
	}
	if validateErr := validateAgentSessionReport(report, intent); validateErr != nil {
		return validateErr
	}
	if authErr := authorizeAgentSessionRelayReport(request, intent, report); authErr != nil {
		return authErr
	}
	response, err := forwardAuthorizedAgentSessionReport(intent, report)
	if err != nil {
		return err
	}
	return completeAgentSessionRelayReport(connection, response, report.ID)
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

func validateAgentSessionReport(report agentSessionReport, intent state.HerdrIntent) error {
	requirements := []bool{
		report.ID != "", len(report.ID) <= 256,
		report.Method == "pane.report_agent_session",
		report.Params.PaneID == intent.Resource.PaneID,
		report.Params.Source == "herdr:codex", report.Params.Agent == "codex",
		report.Params.Seq > 0, report.Params.AgentSessionID != "",
		strings.TrimSpace(report.Params.AgentSessionID) == report.Params.AgentSessionID,
	}
	valid := !slices.Contains(requirements, false)
	if intent.Kind == state.HerdrIntentResume {
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
) (state.HerdrIntent, error) {
	journal, err := state.LoadHerdrIntentsPath(request.controlPath)
	if err != nil {
		return state.HerdrIntent{}, fmt.Errorf("read relay launch intent: %w", err)
	}
	intent, found := journal.FindIntent(request.intentID)
	if !found || !activeAgentSessionRelayIntent(request, intent) {
		return state.HerdrIntent{}, fmt.Errorf("agent-session relay launch intent is not active")
	}
	return intent, nil
}

func authorizeAgentSessionRelayReport(
	request agentSessionRelayRequest,
	original state.HerdrIntent,
	report agentSessionReport,
) error {
	journal, err := state.LoadHerdrIntentsPath(request.controlPath)
	if err != nil {
		return fmt.Errorf("read relay launch intent: %w", err)
	}
	if current, found := journal.FindIntent(request.intentID); found {
		if activeAgentSessionRelayIntent(request, current) && sameAgentSessionRelayLaunch(original, current) {
			return nil
		}
		return fmt.Errorf("agent-session relay launch intent is not active")
	}
	store, err := state.Load(request.statePath)
	if err != nil {
		return fmt.Errorf("read finalized relay state: %w", err)
	}
	if !uniqueFinalizedAgentSessionRelayRow(store, original, report) {
		return fmt.Errorf("agent-session relay launch identity is no longer current")
	}
	return nil
}

func activeAgentSessionRelayIntent(request agentSessionRelayRequest, intent state.HerdrIntent) bool {
	return time.Now().Before(time.UnixMilli(intent.ExpiresUnixMS)) &&
		intent.Status == state.HerdrIntentRealized && intent.Launch != nil &&
		intent.Launch.Nonce == request.nonce && intent.Launch.LauncherReady && intent.Launch.TokenIssued &&
		intent.Launch.AgentSessionStatePath == request.statePath && directCodexIntegrationLaunch(intent)
}

func agentSessionRelayConnectionDeadline(intent state.HerdrIntent, now time.Time) (time.Time, error) {
	expires := time.UnixMilli(intent.ExpiresUnixMS)
	if !now.Before(expires) {
		return time.Time{}, fmt.Errorf("agent-session relay launch intent expired")
	}
	deadline := now.Add(commandTimeout)
	if deadline.After(expires) {
		deadline = expires
	}
	return deadline, nil
}

func sameAgentSessionRelayLaunch(left, right state.HerdrIntent) bool {
	if left.Launch == nil || right.Launch == nil {
		return false
	}
	identity := []bool{
		left.ID == right.ID, left.Kind == right.Kind, left.Parent == right.Parent,
		left.RuntimeParent == right.RuntimeParent, left.OwnerProjectRoot == right.OwnerProjectRoot,
		left.IssueNum == right.IssueNum, left.TaskID == right.TaskID,
		left.WorktreePath == right.WorktreePath, left.WorkspaceLabel == right.WorkspaceLabel,
		left.Resource == right.Resource, left.Session == right.Session, left.SocketPath == right.SocketPath,
		left.ExpiresUnixMS == right.ExpiresUnixMS,
		sameAgentSessionRelayRef(left.ResumeAgentSession, right.ResumeAgentSession),
		left.Launch.Nonce == right.Launch.Nonce, left.Launch.Agent == right.Launch.Agent,
		left.Launch.AgentName == right.Launch.AgentName,
		left.Launch.Executable == right.Launch.Executable,
		slices.Equal(left.Launch.Args, right.Launch.Args),
		left.Launch.AgentSessionStatePath == right.Launch.AgentSessionStatePath,
	}
	return !slices.Contains(identity, false)
}

func sameAgentSessionRelayRef(left, right *backend.AgentSessionRef) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func uniqueFinalizedAgentSessionRelayRow(
	store state.Store,
	intent state.HerdrIntent,
	report agentSessionReport,
) bool {
	matches := 0
	for _, pane := range store.Panes {
		if finalizedAgentSessionRelayRowMatches(pane, intent, report) {
			matches++
		}
	}
	return matches == 1
}

func finalizedAgentSessionRelayRowMatches(
	pane state.Pane,
	intent state.HerdrIntent,
	report agentSessionReport,
) bool {
	launch := intent.Launch
	if launch == nil || pane.HerdrProcessIdentity == nil || !pane.HerdrProcessIdentity.Valid() {
		return false
	}
	identity := []bool{
		backend.NormalizeName(pane.Backend) == backend.Herdr,
		!pane.IsShell(), !pane.IsAttachedAgent(), pane.Parent == intent.Parent,
		pane.RuntimeParent == intent.RuntimeParent, pane.IssueNum == intent.IssueNum,
		pane.TaskID == intent.TaskID, pane.WorktreePath == intent.WorktreePath,
		pane.PaneID == intent.Resource.PaneID,
		pane.HerdrWorkspaceID == intent.Resource.WorkspaceID,
		pane.HerdrWorkspaceLabel == intent.WorkspaceLabel,
		pane.HerdrTerminalID == intent.Resource.TerminalID,
		pane.HerdrRepoKey == intent.Resource.RepoKey, pane.HerdrRepoRoot == intent.Resource.RepoRoot,
		pane.HerdrSession == intent.Session, pane.HerdrSocketPath == intent.SocketPath,
		pane.Agent == "codex", pane.HerdrAgentID != "", !pane.PlanMode,
		pane.HerdrDirectAgentLaunch, pane.HerdrLaunchExecutable == launch.Executable,
		slices.Equal(pane.HerdrLaunchArgs, launch.Args),
		agentSessionRelayRowSessionMatches(pane, report),
	}
	if launch.AgentName != "" {
		identity = append(identity, pane.HerdrAgentID == launch.AgentName)
	}
	return !slices.Contains(identity, false)
}

func agentSessionRelayRowSessionMatches(pane state.Pane, report agentSessionReport) bool {
	ref := pane.HerdrAgentSession
	return ref == nil || ref.Valid() && ref.Source == "herdr:codex" && ref.Agent == "codex" &&
		ref.Kind == "id" && ref.Value == report.Params.AgentSessionID
}

func validateAgentSessionReportResponse(response []byte, requestID string) error {
	var envelope agentSessionReportResponse
	if err := decodeOne(response, &envelope); err != nil || envelope.ID != requestID ||
		envelope.Result == nil || envelope.Result.Type != "ok" || envelope.Error != nil {
		return fmt.Errorf("herdr agent-session report returned an unexpected response")
	}
	return nil
}

func completeAgentSessionRelayReport(writer io.Writer, response []byte, requestID string) error {
	responseErr := validateAgentSessionReportResponse(response, requestID)
	_, writeErr := writer.Write(response)
	if responseErr != nil {
		return errors.Join(responseErr, writeErr)
	}
	return nil // An upstream success is final even when the reporting client disconnected.
}

func forwardAuthorizedAgentSessionReport(
	intent state.HerdrIntent,
	report agentSessionReport,
) ([]byte, error) {
	deadline, err := agentSessionRelayConnectionDeadline(intent, time.Now())
	if err != nil {
		return nil, err
	}
	return forwardAgentSessionReport(intent.SocketPath, report, deadline)
}

func forwardAgentSessionReport(
	socketPath string,
	report agentSessionReport,
	deadline time.Time,
) ([]byte, error) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect restricted report to owned Herdr socket: %w", err)
	}
	defer func() {
		_ = connection.Close() // The request result is more useful than a close error.
	}()
	if deadlineErr := connection.SetDeadline(deadline); deadlineErr != nil {
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
