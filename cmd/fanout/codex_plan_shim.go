package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
)

type codexPlanShimConfig struct {
	CodexPath string
	Prompt    string
	Help      bool
}

type appServerMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func cmdCodexPlanShim(args []string, lg *log.Logger) exitcode.Code {
	cfg, code := parseCodexPlanShimArgs(args, lg)
	if code != exitcode.OK {
		return code
	}
	if cfg.Help {
		return exitcode.OK
	}
	if err := runCodexPlanShim(cfg, os.Stdout, os.Stderr); err != nil {
		lg.Err("codex plan mode: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func parseCodexPlanShimArgs(args []string, lg *log.Logger) (codexPlanShimConfig, exitcode.Code) {
	cfg := codexPlanShimConfig{CodexPath: "codex"}
	for i := 0; i < len(args); {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprint(lg.Stdout(), "Usage: fanout __codex-plan-mode --codex <path> --prompt <prompt>\n")
			cfg.Help = true
			return cfg, exitcode.OK
		case "--codex":
			if i+1 >= len(args) {
				lg.Err("--codex requires an argument")
				return codexPlanShimConfig{}, exitcode.Env
			}
			cfg.CodexPath = args[i+1]
			i += 2
		case "--prompt":
			if i+1 >= len(args) {
				lg.Err("--prompt requires an argument")
				return codexPlanShimConfig{}, exitcode.Env
			}
			cfg.Prompt = args[i+1]
			i += 2
		default:
			lg.Err("unknown codex plan shim option: %s", args[i])
			return codexPlanShimConfig{}, exitcode.Invocation
		}
	}
	if strings.TrimSpace(cfg.Prompt) == "" {
		lg.Err("--prompt is required")
		return codexPlanShimConfig{}, exitcode.Env
	}
	return cfg, exitcode.OK
}

func runCodexPlanShim(cfg codexPlanShimConfig, stdout, stderr io.Writer) error {
	cmd := exec.Command(cfg.CodexPath, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open app-server stdin: %w", err)
	}
	appStdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open app-server stdout: %w", err)
	}
	appStderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open app-server stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s app-server: %w", cfg.CodexPath, err)
	}

	waited := false
	defer func() {
		if waited {
			return
		}
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderr, appStderr)
		close(stderrDone)
	}()

	scanner := bufio.NewScanner(appStdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(stdin)
	stream := newCodexPlanStream(stdout, stderr)

	if err := sendAppRequest(enc, "fanout-init", "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "fanout-codex-plan",
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
	if _, err := readUntilResponse(scanner, enc, "fanout-init", stream); err != nil {
		return err
	}
	if err := sendAppNotification(enc, "initialized"); err != nil {
		return err
	}

	if err := sendAppRequest(enc, "fanout-modes", "collaborationMode/list", map[string]any{}); err != nil {
		return err
	}
	modeResult, err := readUntilResponse(scanner, enc, "fanout-modes", stream)
	if err != nil {
		return err
	}
	planEffort, err := codexPlanEffort(modeResult)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}
	model, err := resolveCodexModel(enc, scanner, stream, cwd)
	if err != nil {
		return err
	}

	if err := sendAppRequest(enc, "fanout-thread", "thread/start", map[string]any{
		"cwd":            cwd,
		"model":          model,
		"approvalPolicy": "never",
		"sandbox":        "read-only",
	}); err != nil {
		return err
	}
	threadResultRaw, err := readUntilResponse(scanner, enc, "fanout-thread", stream)
	if err != nil {
		return err
	}
	threadID, err := parseThreadStart(threadResultRaw)
	if err != nil {
		return err
	}

	if err := sendAppRequest(enc, "fanout-turn", "turn/start", map[string]any{
		"threadId": threadID,
		"input": []map[string]any{{
			"type":          "text",
			"text":          cfg.Prompt,
			"text_elements": []any{},
		}},
		"cwd":            cwd,
		"approvalPolicy": "never",
		"sandboxPolicy": map[string]any{
			"type":          "readOnly",
			"networkAccess": false,
		},
		"collaborationMode": map[string]any{
			"mode": "plan",
			"settings": map[string]any{
				"model":                  model,
				"reasoning_effort":       planEffort,
				"developer_instructions": nil,
			},
		},
	}); err != nil {
		return err
	}
	if _, err := readUntilResponse(scanner, enc, "fanout-turn", stream); err != nil {
		return err
	}
	if err := readUntilTurnCompleted(scanner, enc, stream); err != nil {
		return err
	}

	_ = stdin.Close()
	err = cmd.Wait()
	waited = true
	<-stderrDone
	if err != nil {
		return fmt.Errorf("codex app-server exited: %w", err)
	}
	return nil
}

func sendAppRequest(enc *json.Encoder, id, method string, params any) error {
	if err := enc.Encode(map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}); err != nil {
		return fmt.Errorf("send app-server request %s: %w", method, err)
	}
	return nil
}

func sendAppNotification(enc *json.Encoder, method string) error {
	if err := enc.Encode(map[string]any{"method": method}); err != nil {
		return fmt.Errorf("send app-server notification %s: %w", method, err)
	}
	return nil
}

func sendAppResponse(enc *json.Encoder, id json.RawMessage, result any) error {
	if len(id) == 0 {
		return fmt.Errorf("cannot respond to app-server request without id")
	}
	if err := enc.Encode(map[string]any{
		"id":     id,
		"result": result,
	}); err != nil {
		return fmt.Errorf("send app-server response: %w", err)
	}
	return nil
}

func readUntilResponse(scanner *bufio.Scanner, enc *json.Encoder, id string, stream *codexPlanStream) (json.RawMessage, error) {
	for scanner.Scan() {
		msg, err := parseAppServerLine(scanner.Bytes())
		if err != nil {
			return nil, err
		}
		if isServerRequest(msg) {
			if err := handleServerRequest(enc, msg); err != nil {
				return nil, err
			}
			continue
		}
		if err := stream.Handle(msg); err != nil {
			return nil, err
		}
		if !messageIDMatches(msg.ID, id) {
			continue
		}
		if len(msg.Error) > 0 {
			return nil, fmt.Errorf("app-server request %s failed: %s", id, appServerErrorSummary(msg.Error))
		}
		return msg.Result, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read app-server output: %w", err)
	}
	return nil, io.ErrUnexpectedEOF
}

func readUntilTurnCompleted(scanner *bufio.Scanner, enc *json.Encoder, stream *codexPlanStream) error {
	for scanner.Scan() {
		msg, err := parseAppServerLine(scanner.Bytes())
		if err != nil {
			return err
		}
		if isServerRequest(msg) {
			if err := handleServerRequest(enc, msg); err != nil {
				return err
			}
			continue
		}
		if err := stream.Handle(msg); err != nil {
			return err
		}
		if msg.Method != "turn/completed" {
			continue
		}
		stream.EnsureNewline()
		status, message := turnCompletion(msg.Params)
		if status == "failed" {
			if message == "" {
				message = "turn failed"
			}
			return errors.New(message)
		}
		if status != "completed" {
			return fmt.Errorf("turn ended with status %q", status)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read app-server output: %w", err)
	}
	return io.ErrUnexpectedEOF
}

func resolveCodexModel(enc *json.Encoder, scanner *bufio.Scanner, stream *codexPlanStream, cwd string) (string, error) {
	if err := sendAppRequest(enc, "fanout-config", "config/read", map[string]any{
		"includeLayers": false,
		"cwd":           cwd,
	}); err != nil {
		return "", err
	}
	configResult, configErr := readUntilResponse(scanner, enc, "fanout-config", stream)
	if configErr == nil {
		if model := configModel(configResult); model != "" {
			return model, nil
		}
	}

	if err := sendAppRequest(enc, "fanout-models", "model/list", map[string]any{
		"includeHidden": false,
	}); err != nil {
		return "", err
	}
	modelResult, modelErr := readUntilResponse(scanner, enc, "fanout-models", stream)
	if modelErr != nil {
		if configErr != nil {
			return "", fmt.Errorf("resolve codex model: config/read failed: %v; model/list failed: %w", configErr, modelErr)
		}
		return "", fmt.Errorf("resolve codex model from model/list: %w", modelErr)
	}
	model, err := modelListDefault(modelResult)
	if err != nil {
		if configErr != nil {
			return "", fmt.Errorf("resolve codex model: config/read failed: %v; model/list failed: %w", configErr, err)
		}
		return "", err
	}
	return model, nil
}

func handleServerRequest(enc *json.Encoder, msg appServerMessage) error {
	switch msg.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return sendAppResponse(enc, msg.ID, map[string]any{"decision": "decline"})
	case "item/tool/requestUserInput", "tool/requestUserInput":
		return sendAppResponse(enc, msg.ID, requestUserInputResponse(msg.Params))
	case "item/permissions/requestApproval":
		return sendAppResponse(enc, msg.ID, map[string]any{
			"permissions": map[string]any{},
			"scope":       "turn",
		})
	case "mcpServer/elicitation/request":
		return sendAppResponse(enc, msg.ID, map[string]any{"action": "decline"})
	case "execCommandApproval", "applyPatchApproval":
		return sendAppResponse(enc, msg.ID, map[string]any{"decision": "denied"})
	}
	return unsupportedServerRequest(msg)
}

const codexPlanUserInputFallbackAnswer = "fanout Codex Plan Mode is running non-interactively; proceed with the implementation plan using stated assumptions, and call out any ambiguity instead of asking for input."

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
				"answers": []string{codexPlanUserInputFallbackAnswer},
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

func unsupportedServerRequest(msg appServerMessage) error {
	return fmt.Errorf("unsupported app-server request %q from Codex; fanout Codex Plan Mode shim cannot continue", msg.Method)
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

func configModel(raw json.RawMessage) string {
	var res struct {
		Config struct {
			Model string `json:"model"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return ""
	}
	return strings.TrimSpace(res.Config.Model)
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
		if mode.ReasoningEffort != nil && *mode.ReasoningEffort != "" {
			return *mode.ReasoningEffort, nil
		}
		if mode.Settings != nil && mode.Settings.ReasoningEffort != nil && *mode.Settings.ReasoningEffort != "" {
			return *mode.Settings.ReasoningEffort, nil
		}
		return "medium", nil
	}
	return "", fmt.Errorf("codex app-server does not advertise collaborationMode.mode=plan")
}

func parseThreadStart(raw json.RawMessage) (threadID string, err error) {
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("parse thread/start response: %w", err)
	}
	if res.Thread.ID == "" {
		return "", fmt.Errorf("thread/start response did not include thread.id")
	}
	return res.Thread.ID, nil
}

func turnCompletion(raw json.RawMessage) (status, message string) {
	var res struct {
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				Message           string `json:"message"`
				AdditionalDetails string `json:"additionalDetails"`
			} `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", ""
	}
	if res.Turn.Error != nil {
		message = res.Turn.Error.Message
		if res.Turn.Error.AdditionalDetails != "" {
			message += ": " + res.Turn.Error.AdditionalDetails
		}
	}
	return res.Turn.Status, message
}

type codexPlanStream struct {
	stdout       io.Writer
	stderr       io.Writer
	printedItems map[string]bool
	wroteOutput  bool
	lastNewline  bool
}

func newCodexPlanStream(stdout, stderr io.Writer) *codexPlanStream {
	return &codexPlanStream{
		stdout:       stdout,
		stderr:       stderr,
		printedItems: map[string]bool{},
		lastNewline:  true,
	}
}

func (s *codexPlanStream) Handle(msg appServerMessage) error {
	switch msg.Method {
	case "":
		return nil
	case "item/plan/delta":
		return nil
	case "item/agentMessage/delta", "item/commandExecution/outputDelta":
		itemID, delta := deltaPayload(msg.Params)
		s.Write(delta, s.stdout)
		if itemID != "" {
			s.printedItems[itemID] = true
		}
	case "command/exec/outputDelta", "process/outputDelta":
		stream, output := base64OutputPayload(msg.Params)
		if stream == "stderr" {
			s.Write(output, s.stderr)
		} else {
			s.Write(output, s.stdout)
		}
	case "item/completed":
		s.PrintCompletedItem(msg.Params)
	case "turn/completed":
		s.PrintCompletedTurnItems(msg.Params)
	case "error":
		message, willRetry := errorNotification(msg.Params)
		if willRetry {
			return nil
		}
		if message == "" {
			message = "codex app-server reported a turn error"
		}
		return errors.New(message)
	}
	return nil
}

func (s *codexPlanStream) Write(text string, w io.Writer) {
	if text == "" {
		return
	}
	fmt.Fprint(w, text)
	s.wroteOutput = true
	s.lastNewline = strings.HasSuffix(text, "\n")
}

func (s *codexPlanStream) EnsureNewline() {
	if s.wroteOutput && !s.lastNewline {
		fmt.Fprintln(s.stdout)
		s.lastNewline = true
	}
}

func (s *codexPlanStream) PrintCompletedItem(raw json.RawMessage) {
	var res struct {
		Item streamItem `json:"item"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return
	}
	s.printItemIfNeeded(res.Item)
}

func (s *codexPlanStream) PrintCompletedTurnItems(raw json.RawMessage) {
	var res struct {
		Turn struct {
			Items []streamItem `json:"items"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return
	}
	for _, item := range res.Turn.Items {
		s.printItemIfNeeded(item)
	}
}

func (s *codexPlanStream) printItemIfNeeded(item streamItem) {
	if item.ID == "" || s.printedItems[item.ID] {
		return
	}
	switch item.Type {
	case "plan", "agentMessage":
		if item.Text == "" {
			return
		}
		if s.wroteOutput && !s.lastNewline {
			fmt.Fprintln(s.stdout)
		}
		s.Write(item.Text, s.stdout)
		s.EnsureNewline()
		s.printedItems[item.ID] = true
	}
}

type streamItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Text string `json:"text"`
}

func deltaPayload(raw json.RawMessage) (itemID, delta string) {
	var res struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", ""
	}
	return res.ItemID, res.Delta
}

func base64OutputPayload(raw json.RawMessage) (stream, output string) {
	var res struct {
		Stream      string `json:"stream"`
		DeltaBase64 string `json:"deltaBase64"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", ""
	}
	decoded, err := base64.StdEncoding.DecodeString(res.DeltaBase64)
	if err != nil {
		return res.Stream, ""
	}
	return res.Stream, string(decoded)
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
