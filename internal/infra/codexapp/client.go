package codexapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const codexPlanUserInputFallbackAnswer = "fanout Codex Plan Mode is starting interactively; continue normal non-mutating discovery before presenting the implementation plan, and call out any remaining ambiguity in the plan."

type appServerMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// client is a JSON-RPC client over one websocket connection to a Codex
// app-server.
type client struct {
	conn *websocketJSONConn
}

// requester is the request-only slice of client that the Plan Mode setup
// helpers consume; tests substitute a fake implementation.
type requester interface {
	Request(id, method string, params any) (json.RawMessage, error)
}

// sessionClient adds JSON-RPC notifications on top of requester.
type sessionClient interface {
	requester
	Notify(method string) error
}

type sender interface {
	send(v any) error
}

// dialClient connects a client to a running app-server websocket address.
func dialClient(addr string, timeout time.Duration) (*client, error) {
	conn, err := dialWebSocket(addr, timeout)
	if err != nil {
		return nil, err
	}
	return &client{conn: conn}, nil
}

func (c *client) Request(id, method string, params any) (json.RawMessage, error) {
	if err := sendAppRequest(c, id, method, params); err != nil {
		return nil, err
	}
	return readUntilResponse(c, id)
}

func (c *client) Notify(method string) error {
	return sendAppNotification(c, method)
}

func (c *client) Close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
}

func (c *client) send(v any) error {
	if c == nil || c.conn == nil {
		return io.ErrClosedPipe
	}
	return c.conn.Send(v)
}

func (c *client) receive() (appServerMessage, error) {
	if c == nil || c.conn == nil {
		return appServerMessage{}, io.ErrClosedPipe
	}
	return c.conn.Receive()
}

func sendAppRequest(client sender, id, method string, params any) error {
	if err := client.send(map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}); err != nil {
		return fmt.Errorf("send app-server request %s: %w", method, err)
	}
	return nil
}

func sendAppNotification(client sender, method string) error {
	if err := client.send(map[string]any{"method": method}); err != nil {
		return fmt.Errorf("send app-server notification %s: %w", method, err)
	}
	return nil
}

func sendAppResponse(client sender, id json.RawMessage, result any) error {
	if len(id) == 0 {
		return fmt.Errorf("cannot respond to app-server request without id")
	}
	if err := client.send(map[string]any{
		"id":     id,
		"result": result,
	}); err != nil {
		return fmt.Errorf("send app-server response: %w", err)
	}
	return nil
}

func sendAppError(client sender, id json.RawMessage, message string) error {
	if len(id) == 0 {
		return fmt.Errorf("cannot send app-server error without id")
	}
	if err := client.send(map[string]any{
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

func readUntilResponse(client *client, id string) (json.RawMessage, error) {
	for {
		msg, err := client.receive()
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

func handleServerRequest(client sender, msg appServerMessage) error {
	if state := serverRequestAgentState(msg.Method); state != "" {
		setPlanTUIAgentState(state)
		err := handleServerRequestResponse(client, msg)
		if err == nil {
			setPlanTUIAgentState("working")
		}
		return err
	}
	return handleServerRequestResponse(client, msg)
}

func serverRequestAgentState(method string) string {
	switch method {
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/tool/requestUserInput",
		"tool/requestUserInput",
		"item/permissions/requestApproval",
		"mcpServer/elicitation/request",
		"execCommandApproval",
		"applyPatchApproval":
		return "blocked"
	default:
		return ""
	}
}

func handleServerRequestResponse(client sender, msg appServerMessage) error {
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
