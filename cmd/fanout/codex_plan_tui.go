package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
)

const (
	codexPlanTUICommand              = "__codex-plan-tui"
	codexPlanTUIStatusReady          = "ready"
	codexPlanTUIStatusFailed         = "failed"
	codexPlanDefaultEffort           = "xhigh"
	codexRemoteAppConnectTimeout     = 10 * time.Second
	codexRemoteTUIStartupGrace       = 3 * time.Second
	codexPlanUserInputFallbackAnswer = "fanout Codex Plan Mode is starting interactively; proceed with the implementation plan using stated assumptions, and call out any ambiguity instead of asking for input."
	webSocketGUID                    = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

type codexPlanTUIConfig struct {
	CodexPath  string
	Prompt     string
	StatusFile string
	Help       bool
}

type codexPlanTUIStatus struct {
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	ThreadID  string `json:"threadId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Remote    string `json:"remote,omitempty"`
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

type appServerMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type codexAppClient struct {
	conn *websocketJSONConn
}

type codexRemoteAppServer struct {
	Addr string

	cmd  *exec.Cmd
	done chan struct{}
	logs *lockedBuffer

	mu  sync.Mutex
	err error
}

type websocketJSONConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func isCodexPlanTUIRequest(args []string) bool {
	return len(args) > 0 && args[0] == codexPlanTUICommand
}

func cmdCodexPlanTUI(args []string, lg *log.Logger) exitcode.Code {
	cfg, code := parseCodexPlanTUIArgs(args, lg)
	if code != exitcode.OK {
		return code
	}
	if cfg.Help {
		return exitcode.OK
	}
	if err := runCodexPlanTUI(cfg, os.Stdout, os.Stderr); err != nil {
		lg.Err("codex plan mode TUI: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func parseCodexPlanTUIArgs(args []string, lg *log.Logger) (codexPlanTUIConfig, exitcode.Code) {
	cfg := codexPlanTUIConfig{CodexPath: "codex"}
	for i := 0; i < len(args); {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprint(lg.Stdout(), "Usage: fanout __codex-plan-tui --codex <path> --prompt <prompt> --status-file <path>\n")
			cfg.Help = true
			return cfg, exitcode.OK
		case "--codex":
			if i+1 >= len(args) {
				lg.Err("--codex requires an argument")
				return codexPlanTUIConfig{}, exitcode.Env
			}
			cfg.CodexPath = args[i+1]
			i += 2
		case "--prompt":
			if i+1 >= len(args) {
				lg.Err("--prompt requires an argument")
				return codexPlanTUIConfig{}, exitcode.Env
			}
			cfg.Prompt = args[i+1]
			i += 2
		case "--status-file":
			if i+1 >= len(args) {
				lg.Err("--status-file requires an argument")
				return codexPlanTUIConfig{}, exitcode.Env
			}
			cfg.StatusFile = args[i+1]
			i += 2
		default:
			lg.Err("unknown codex plan TUI option: %s", args[i])
			return codexPlanTUIConfig{}, exitcode.Invocation
		}
	}
	if strings.TrimSpace(cfg.Prompt) == "" {
		lg.Err("--prompt is required")
		return codexPlanTUIConfig{}, exitcode.Env
	}
	if strings.TrimSpace(cfg.StatusFile) == "" {
		lg.Err("--status-file is required")
		return codexPlanTUIConfig{}, exitcode.Env
	}
	return cfg, exitcode.OK
}

func runCodexPlanTUI(cfg codexPlanTUIConfig, stdout, stderr io.Writer) (err error) {
	ready := false
	defer func() {
		if err != nil && !ready {
			_ = writeCodexPlanTUIStatus(cfg.StatusFile, codexPlanTUIStatus{
				Status: codexPlanTUIStatusFailed,
				Error:  err.Error(),
			})
		}
	}()

	server, err := startCodexRemoteAppServer(cfg.CodexPath)
	if err != nil {
		return err
	}
	defer server.Close()
	stopSignalCleanup := installCodexAppServerSignalCleanup(server)
	defer stopSignalCleanup()

	client, err := connectCodexRemoteAppClient(server, codexRemoteAppConnectTimeout)
	if err != nil {
		return err
	}
	defer client.Close()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}
	thread, err := setupCodexPlanThread(client, cwd)
	if err != nil {
		return err
	}
	if err = startCodexPlanTurn(client, thread, cwd, cfg.Prompt); err != nil {
		return err
	}

	drainDone := make(chan error, 1)
	go func() { drainDone <- drainCodexAppServer(client) }()

	tui, tuiDone, err := startCodexRemoteTUI(cfg.CodexPath, server.Addr, thread.SessionID, stdout, stderr)
	if err != nil {
		return err
	}
	tuiStopped := false
	defer func() {
		if !tuiStopped {
			stopProcess(tui, tuiDone)
		}
	}()

	select {
	case tuiErr := <-tuiDone:
		tuiStopped = true
		if tuiErr != nil {
			return fmt.Errorf("codex TUI resume exited during startup: %w", tuiErr)
		}
		return fmt.Errorf("codex TUI resume exited during startup")
	case drainErr := <-drainDone:
		if drainErr != nil {
			return fmt.Errorf("codex app-server disconnected during TUI startup: %w", drainErr)
		}
		return fmt.Errorf("codex app-server disconnected during TUI startup")
	case <-time.After(codexRemoteTUIStartupGrace):
	}

	if err = writeCodexPlanTUIStatus(cfg.StatusFile, codexPlanTUIStatus{
		Status:    codexPlanTUIStatusReady,
		ThreadID:  thread.ID,
		SessionID: thread.SessionID,
		Remote:    server.Addr,
	}); err != nil {
		return fmt.Errorf("write Codex Plan TUI status: %w", err)
	}
	ready = true
	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone)
	tuiStopped = tuiExited
	return err
}

func setupCodexPlanThread(client *codexAppClient, cwd string) (codexThreadInfo, error) {
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
		return codexThreadInfo{}, err
	}
	if err := client.Notify("initialized"); err != nil {
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

func codexThreadStartParams(cwd, model string) map[string]any {
	return map[string]any{
		"cwd":                cwd,
		"model":              model,
		"sessionStartSource": "startup",
		"threadSource":       "user",
		"ephemeral":          false,
	}
}

func startCodexPlanTurn(client *codexAppClient, thread codexThreadInfo, cwd, prompt string) error {
	params := codexTurnStartParams(thread.ID, cwd, thread.Model, prompt, nil)
	if thread.UseTurnCollaborationMode {
		params["collaborationMode"] = codexPlanCollaborationMode(thread.Model, thread.PlanEffort)
	}
	if _, err := client.Request("fanout-turn", "turn/start", params); err != nil {
		return err
	}
	return nil
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

func startCodexRemoteTUI(codexPath, remoteAddr, sessionID string, stdout, stderr io.Writer) (*exec.Cmd, chan error, error) {
	cmd := exec.Command(codexPath, "--remote", remoteAddr, "resume", sessionID)
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

func startCodexRemoteAppServer(codexPath string) (*codexRemoteAppServer, error) {
	port, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	addr := "ws://127.0.0.1:" + strconv.Itoa(port)
	logs := &lockedBuffer{}
	cmd := exec.Command(codexPath, "app-server", "--listen", addr)
	cmd.Stdout = logs
	cmd.Stderr = logs
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s app-server: %w", codexPath, err)
	}
	server := &codexRemoteAppServer{
		Addr: addr,
		cmd:  cmd,
		done: make(chan struct{}),
		logs: logs,
	}
	go func() {
		err := cmd.Wait()
		server.mu.Lock()
		server.err = err
		server.mu.Unlock()
		close(server.done)
	}()
	return server, nil
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve localhost port for Codex app-server: %w", err)
	}
	// Close error is irrelevant: the listener only reserved the port.
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("reserve localhost port for Codex app-server: unexpected address %s", ln.Addr())
	}
	return addr.Port, nil
}

func connectCodexRemoteAppClient(server *codexRemoteAppServer, timeout time.Duration) (*codexAppClient, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if ok, err := server.Exited(); ok {
			return nil, fmt.Errorf("codex app-server exited before websocket connection: %w%s", err, serverLogSuffix(server))
		}
		conn, err := dialWebSocket(server.Addr, time.Second)
		if err == nil {
			return &codexAppClient{conn: conn}, nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = contextDeadlineExceeded(timeout)
	}
	return nil, fmt.Errorf("connect Codex app-server websocket %s: %w%s", server.Addr, lastErr, serverLogSuffix(server))
}

func contextDeadlineExceeded(timeout time.Duration) error {
	return fmt.Errorf("timed out after %s", timeout)
}

func serverLogSuffix(server *codexRemoteAppServer) string {
	if server == nil || server.logs == nil {
		return ""
	}
	logs := strings.TrimSpace(server.logs.String())
	if logs == "" {
		return ""
	}
	return "; app-server output: " + logs
}

func (s *codexRemoteAppServer) Exited() (bool, error) {
	if s == nil {
		return true, nil
	}
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return true, s.err
	default:
		return false, nil
	}
}

func (s *codexRemoteAppServer) Close() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	if ok, _ := s.Exited(); ok {
		return
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	_ = s.cmd.Process.Kill()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
}

func installCodexAppServerSignalCleanup(server *codexRemoteAppServer) func() {
	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			server.Close()
			signal.Stop(sigCh)
			os.Exit(signalExitCode(sig))
		case <-stopCh:
		}
	}()
	return func() {
		signal.Stop(sigCh)
		close(stopCh)
	}
}

func signalExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func dialWebSocket(rawURL string, timeout time.Duration) (*websocketJSONConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse websocket URL: %w", err)
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported websocket URL scheme %q", u.Scheme)
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.Dial("tcp", u.Host)
	if err != nil {
		return nil, err
	}
	if err = conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	keyBytes := make([]byte, 16)
	if _, err = rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("generate websocket key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err = io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write websocket handshake: %w", err)
	}
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read websocket handshake: %w", err)
	}
	// The handshake response body is unused: a 101 carries no body, and for any
	// other status the connection is discarded below. Close never reads from br.
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake returned %s", resp.Status)
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), webSocketAccept(key); got != want {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake accept mismatch")
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &websocketJSONConn{conn: conn, br: br}, nil
}

func webSocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + webSocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (w *websocketJSONConn) Send(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.writeFrame(0x1, body)
}

func (w *websocketJSONConn) Receive() (appServerMessage, error) {
	payload, err := w.readTextMessage()
	if err != nil {
		return appServerMessage{}, err
	}
	return parseAppServerLine(payload)
}

func (w *websocketJSONConn) Close() error {
	if w == nil || w.conn == nil {
		return nil
	}
	_ = w.writeFrame(0x8, nil)
	return w.conn.Close()
}

func (w *websocketJSONConn) readTextMessage() ([]byte, error) {
	var fragments bytes.Buffer
	for {
		fin, opcode, payload, err := w.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x1:
			if fin {
				return payload, nil
			}
			fragments.Write(payload)
		case 0x0:
			if fragments.Len() == 0 {
				return nil, fmt.Errorf("websocket continuation without initial text frame")
			}
			fragments.Write(payload)
			if fin {
				return fragments.Bytes(), nil
			}
		case 0x8:
			return nil, io.EOF
		case 0x9:
			if err := w.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA:
			continue
		default:
			return nil, fmt.Errorf("unsupported websocket opcode 0x%x", opcode)
		}
	}
}

func (w *websocketJSONConn) readFrame() (bool, byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(w.br, header); err != nil {
		return false, 0, nil, err
	}
	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(w.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	if length > 64*1024*1024 {
		return false, 0, nil, fmt.Errorf("websocket frame too large: %d bytes", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(w.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

func (w *websocketJSONConn) writeFrame(opcode byte, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, byte(0x80|length))
	case length <= 0xffff:
		header = append(header, 0x80|126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(length))
		header = append(header, ext[:]...)
	default:
		header = append(header, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		header = append(header, ext[:]...)
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("generate websocket mask: %w", err)
	}
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	if _, err := w.conn.Write(header); err != nil {
		return err
	}
	if len(masked) == 0 {
		return nil
	}
	_, err := w.conn.Write(masked)
	return err
}

func writeCodexPlanTUIStatus(path string, status codexPlanTUIStatus) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *codexAppClient) Request(id, method string, params any) (json.RawMessage, error) {
	if err := sendAppRequest(c, id, method, params); err != nil {
		return nil, err
	}
	return readUntilResponse(c, id)
}

func (c *codexAppClient) Notify(method string) error {
	return sendAppNotification(c, method)
}

func (c *codexAppClient) Close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
}

func (c *codexAppClient) Send(v any) error {
	if c == nil || c.conn == nil {
		return io.ErrClosedPipe
	}
	return c.conn.Send(v)
}

func (c *codexAppClient) Receive() (appServerMessage, error) {
	if c == nil || c.conn == nil {
		return appServerMessage{}, io.ErrClosedPipe
	}
	return c.conn.Receive()
}

func stopProcess(cmd *exec.Cmd, done chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-done:
		return
	default:
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func sendAppRequest(client *codexAppClient, id, method string, params any) error {
	if err := client.Send(map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}); err != nil {
		return fmt.Errorf("send app-server request %s: %w", method, err)
	}
	return nil
}

func sendAppNotification(client *codexAppClient, method string) error {
	if err := client.Send(map[string]any{"method": method}); err != nil {
		return fmt.Errorf("send app-server notification %s: %w", method, err)
	}
	return nil
}

func sendAppResponse(client *codexAppClient, id json.RawMessage, result any) error {
	if len(id) == 0 {
		return fmt.Errorf("cannot respond to app-server request without id")
	}
	if err := client.Send(map[string]any{
		"id":     id,
		"result": result,
	}); err != nil {
		return fmt.Errorf("send app-server response: %w", err)
	}
	return nil
}

func sendAppError(client *codexAppClient, id json.RawMessage, message string) error {
	if len(id) == 0 {
		return fmt.Errorf("cannot send app-server error without id")
	}
	if err := client.Send(map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    -32000,
			"message": message,
		},
	}); err != nil {
		return fmt.Errorf("send app-server error response: %w", err)
	}
	return nil
}

func readUntilResponse(client *codexAppClient, id string) (json.RawMessage, error) {
	for {
		msg, err := client.Receive()
		if err != nil {
			return nil, err
		}
		if isServerRequest(msg) {
			if err := handleServerRequest(client, msg); err != nil {
				return nil, err
			}
			continue
		}
		if msg.Method == "error" {
			message, willRetry := errorNotification(msg.Params)
			if willRetry {
				continue
			}
			if message == "" {
				message = "codex app-server reported an error"
			}
			return nil, errors.New(message)
		}
		if !messageIDMatches(msg.ID, id) {
			continue
		}
		if len(msg.Error) > 0 {
			return nil, fmt.Errorf("app-server request %s failed: %s", id, appServerErrorSummary(msg.Error))
		}
		return msg.Result, nil
	}
}

func waitForCodexTUIAfterReady(tuiDone, drainDone <-chan error) (bool, error) {
	select {
	case tuiErr := <-tuiDone:
		return true, tuiErr
	case drainErr := <-drainDone:
		if drainErr != nil {
			return false, fmt.Errorf("codex app-server request handling failed while Codex TUI was attached: %w", drainErr)
		}
		return false, fmt.Errorf("codex app-server disconnected while Codex TUI was attached")
	}
}

func drainCodexAppServer(client *codexAppClient) error {
	for {
		msg, err := client.Receive()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return err
		}
		if isServerRequest(msg) {
			if err := handleServerRequest(client, msg); err != nil {
				return err
			}
		}
	}
}

func handleServerRequest(client *codexAppClient, msg appServerMessage) error {
	switch msg.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return sendAppResponse(client, msg.ID, map[string]any{"decision": "decline"})
	case "item/tool/requestUserInput", "tool/requestUserInput":
		return sendAppResponse(client, msg.ID, requestUserInputResponse(msg.Params))
	case "item/permissions/requestApproval":
		return sendAppResponse(client, msg.ID, map[string]any{
			"permissions": map[string]any{},
			"scope":       "turn",
		})
	case "mcpServer/elicitation/request":
		return sendAppResponse(client, msg.ID, map[string]any{"action": "decline"})
	case "execCommandApproval", "applyPatchApproval":
		return sendAppResponse(client, msg.ID, map[string]any{"decision": "denied"})
	case "item/tool/call":
		return sendAppResponse(client, msg.ID, map[string]any{
			"success": false,
			"contentItems": []map[string]any{
				{
					"type": "inputText",
					"text": "fanout Codex Plan Mode controller cannot execute dynamic app tools.",
				},
			},
		})
	}
	message := fmt.Sprintf("unsupported app-server request %q from Codex during Plan TUI setup", msg.Method)
	if err := sendAppError(client, msg.ID, message); err != nil {
		return err
	}
	return errors.New(message)
}

func requestUserInputResponse(raw json.RawMessage) map[string]any {
	var params struct {
		Questions []struct {
			ID string `json:"id"`
		} `json:"questions"`
	}
	answers := map[string]map[string][]string{}
	if err := json.Unmarshal(raw, &params); err == nil {
		for _, question := range params.Questions {
			id := strings.TrimSpace(question.ID)
			if id == "" {
				continue
			}
			answers[id] = map[string][]string{
				"answers": {codexPlanUserInputFallbackAnswer},
			}
		}
	}
	return map[string]any{"answers": answers}
}

func parseAppServerLine(line []byte) (appServerMessage, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return appServerMessage{}, nil
	}
	var msg appServerMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return appServerMessage{}, fmt.Errorf("parse app-server JSON %q: %w", string(line), err)
	}
	return msg, nil
}

func isServerRequest(msg appServerMessage) bool {
	return len(msg.ID) > 0 && msg.Method != "" && len(msg.Result) == 0 && len(msg.Error) == 0
}

func messageIDMatches(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var got string
	if err := json.Unmarshal(raw, &got); err == nil {
		return got == want
	}
	return strings.TrimSpace(string(raw)) == want
}

func appServerErrorSummary(raw json.RawMessage) string {
	var shaped struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(raw, &shaped); err == nil && shaped.Message != "" {
		if shaped.Code != nil {
			return fmt.Sprintf("%s (code %v)", shaped.Message, shaped.Code)
		}
		return shaped.Message
	}
	return string(raw)
}

func resolveCodexSettings(client *codexAppClient, cwd string) (codexResolvedSettings, error) {
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
		"includeHidden": false,
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
	return codexResolvedSettings{
		Model:           strings.TrimSpace(res.Config.Model),
		ReasoningEffort: effort,
	}
}

func modelListSelection(raw json.RawMessage, preferred string) (codexModelSelection, error) {
	var res struct {
		Data []struct {
			ID                                string   `json:"id"`
			Model                             string   `json:"model"`
			Hidden                            bool     `json:"hidden"`
			IsDefault                         bool     `json:"isDefault"`
			SupportedReasoningEfforts         []string `json:"supportedReasoningEfforts"`
			SupportedReasoningEffortsFallback []string `json:"supported_reasoning_efforts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return codexModelSelection{}, fmt.Errorf("parse model/list response: %w", err)
	}
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		for _, model := range res.Data {
			if model.Hidden {
				continue
			}
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
	for _, effort := range []string{"xhigh", "high", "medium", "low", "minimal"} {
		if available[effort] {
			return effort
		}
	}
	for _, effort := range supported {
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

func errorNotification(raw json.RawMessage) (message string, willRetry bool) {
	var res struct {
		WillRetry bool `json:"willRetry"`
		Error     struct {
			Message           string `json:"message"`
			AdditionalDetails string `json:"additionalDetails"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", false
	}
	if res.Error.AdditionalDetails != "" {
		return res.Error.Message + ": " + res.Error.AdditionalDetails, res.WillRetry
	}
	return res.Error.Message, res.WillRetry
}
