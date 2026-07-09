package codexapp

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCodexRemoteTUIArgsResumesSession(t *testing.T) {
	got := codexRemoteTUIArgs("ws://127.0.0.1:1234", "session-1")
	want := []string{"--remote", "ws://127.0.0.1:1234", "resume", "session-1"}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codexRemoteTUIArgs() = %#v, want %#v", got, want)
	}
}

func TestCodexRemoteTUIArgsStartsFreshSession(t *testing.T) {
	got := codexRemoteTUIArgs("ws://127.0.0.1:1234", "")
	want := []string{"--remote", "ws://127.0.0.1:1234"}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codexRemoteTUIArgs() = %#v, want %#v", got, want)
	}
}

func TestCodexPlanSettingsUpdateParamsUsesPlanMode(t *testing.T) {
	got := codexPlanSettingsUpdateParams("thread-1", "gpt-test", "high")

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var shaped struct {
		ThreadID          string `json:"threadId"`
		CollaborationMode struct {
			Mode     string `json:"mode"`
			Settings struct {
				Model                 string  `json:"model"`
				ReasoningEffort       string  `json:"reasoning_effort"`
				DeveloperInstructions *string `json:"developer_instructions"`
			} `json:"settings"`
		} `json:"collaborationMode"`
	}
	if err := json.Unmarshal(body, &shaped); err != nil {
		t.Fatal(err)
	}
	if shaped.ThreadID != "thread-1" {
		t.Fatalf("threadId = %q, want thread-1", shaped.ThreadID)
	}
	if shaped.CollaborationMode.Mode != "plan" {
		t.Fatalf("mode = %q, want plan", shaped.CollaborationMode.Mode)
	}
	if shaped.CollaborationMode.Settings.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", shaped.CollaborationMode.Settings.Model)
	}
	if shaped.CollaborationMode.Settings.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", shaped.CollaborationMode.Settings.ReasoningEffort)
	}
	if shaped.CollaborationMode.Settings.DeveloperInstructions != nil {
		t.Fatalf("developer_instructions = %q, want nil", *shaped.CollaborationMode.Settings.DeveloperInstructions)
	}
}

func TestCodexRemoteTUIResumeIDPrefersSessionID(t *testing.T) {
	got := codexRemoteTUIResumeID(codexThreadInfo{ID: "thread-1", SessionID: "session-1"})
	if got != "session-1" {
		t.Fatalf("codexRemoteTUIResumeID() = %q, want session-1", got)
	}
}

func TestCodexRemoteTUIResumeIDFallsBackToThreadID(t *testing.T) {
	got := codexRemoteTUIResumeID(codexThreadInfo{ID: "thread-1"})
	if got != "thread-1" {
		t.Fatalf("codexRemoteTUIResumeID() = %q, want thread-1", got)
	}
}

func TestCodexThreadStartParamsCreatesPersistentStartupThread(t *testing.T) {
	got := codexThreadStartParams("/repo", "gpt-test")

	if got["cwd"] != "/repo" {
		t.Fatalf("cwd = %q, want /repo", got["cwd"])
	}
	if got["model"] != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", got["model"])
	}
	if got["sessionStartSource"] != "startup" {
		t.Fatalf("sessionStartSource = %q, want startup", got["sessionStartSource"])
	}
	if got["threadSource"] != "user" {
		t.Fatalf("threadSource = %q, want user", got["threadSource"])
	}
	if got["ephemeral"] != false {
		t.Fatalf("ephemeral = %v, want false", got["ephemeral"])
	}
}

func TestCodexTurnStartParamsSubmitsPromptThroughAppServer(t *testing.T) {
	got := codexTurnStartParams("thread-1", "/repo", "gpt-test", "hello plan", nil)

	if got["threadId"] != "thread-1" {
		t.Fatalf("threadId = %q, want thread-1", got["threadId"])
	}
	if got["cwd"] != "/repo" {
		t.Fatalf("cwd = %q, want /repo", got["cwd"])
	}
	if got["model"] != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", got["model"])
	}
	input, ok := got["input"].([]map[string]any)
	if !ok {
		t.Fatalf("input has type %T, want []map[string]any", got["input"])
	}
	if len(input) != 1 || input[0]["type"] != "text" || input[0]["text"] != "hello plan" {
		t.Fatalf("input = %#v, want one text prompt", input)
	}
	if _, ok := got["collaborationMode"]; ok {
		t.Fatalf("collaborationMode was included without fallback: %#v", got["collaborationMode"])
	}
}

func TestCodexTurnStartParamsCanCarryPlanCollaborationMode(t *testing.T) {
	got := codexTurnStartParams("thread-1", "/repo", "gpt-test", "hello plan", codexPlanCollaborationMode("gpt-test", "medium"))

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var shaped struct {
		CollaborationMode struct {
			Mode     string `json:"mode"`
			Settings struct {
				Model           string `json:"model"`
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"settings"`
		} `json:"collaborationMode"`
	}
	if err := json.Unmarshal(body, &shaped); err != nil {
		t.Fatal(err)
	}
	if shaped.CollaborationMode.Mode != "plan" {
		t.Fatalf("mode = %q, want plan", shaped.CollaborationMode.Mode)
	}
	if shaped.CollaborationMode.Settings.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", shaped.CollaborationMode.Settings.Model)
	}
	if shaped.CollaborationMode.Settings.ReasoningEffort != "medium" {
		t.Fatalf("reasoning_effort = %q, want medium", shaped.CollaborationMode.Settings.ReasoningEffort)
	}
}

func TestConfigModelReadsConfiguredModel(t *testing.T) {
	got := configModel(json.RawMessage(`{"config":{"model":"gpt-test"}}`))
	if got != "gpt-test" {
		t.Fatalf("configModel() = %q, want gpt-test", got)
	}
}

func TestModelListDefaultPrefersVisibleDefault(t *testing.T) {
	got, err := modelListDefault(json.RawMessage(`{"data":[
		{"id":"hidden-id","model":"hidden-model","hidden":true,"isDefault":true},
		{"id":"fallback-id","model":"fallback-model","hidden":false,"isDefault":false},
		{"id":"default-id","model":"default-model","hidden":false,"isDefault":true}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "default-model" {
		t.Fatalf("modelListDefault() = %q, want default-model", got)
	}
}

func TestCodexPlanEffortReadsPlanMode(t *testing.T) {
	got, err := codexPlanEffort(json.RawMessage(`{"data":[
		{"name":"Default","mode":"default","reasoning_effort":"low"},
		{"name":"Plan","mode":"plan","settings":{"reasoning_effort":"high"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "high" {
		t.Fatalf("codexPlanEffort() = %q, want high", got)
	}
}

func TestCodexPlanEffortDefaultsWhenPlanHasNoEffort(t *testing.T) {
	got, err := codexPlanEffort(json.RawMessage(`{"data":[{"name":"Plan","mode":"plan"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "medium" {
		t.Fatalf("codexPlanEffort() = %q, want medium", got)
	}
}

func TestParseThreadStartFallsBackToThreadIDAsSessionID(t *testing.T) {
	got, err := parseThreadStart([]byte(`{"thread":{"id":"thread-1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "thread-1" || got.SessionID != "thread-1" {
		t.Fatalf("parseThreadStart() = %+v, want thread id reused as session id", got)
	}
}

func TestParseThreadStartReturnsThreadAndSessionID(t *testing.T) {
	got, err := parseThreadStart([]byte(`{"thread":{"id":"thread-1","sessionId":"session-1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "thread-1" || got.SessionID != "session-1" {
		t.Fatalf("parseThreadStart() = %+v, want thread/session ids", got)
	}
}

func TestUnsupportedCodexAppServerMethodDetection(t *testing.T) {
	err := errors.New(`app-server request fanout-plan-mode failed: unknown variant "thread/settings/update"`)
	if !isUnsupportedCodexAppServerMethod(err) {
		t.Fatalf("isUnsupportedCodexAppServerMethod() = false, want true")
	}
}

// recordedStates collects setState calls for synchronous notification-drain
// assertions.
func recordedStates(states *[]string) func(string) {
	return func(state string) { *states = append(*states, state) }
}

func TestClientCloseClosesWatchGate(t *testing.T) {
	client := &client{conn: &websocketJSONConn{}}

	client.Close()

	if canWatchAppServer(client) {
		t.Fatal("canWatchAppServer() = true after Close, want false")
	}
}

func TestClientCloseUnblocksReceive(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		_, _ = io.Copy(io.Discard, peer)
	}()
	client := &client{conn: &websocketJSONConn{conn: conn, br: bufio.NewReader(conn)}}
	receiveDone := make(chan error, 1)
	go func() {
		_, err := client.receive()
		receiveDone <- err
	}()

	client.Close()

	select {
	case err := <-receiveDone:
		if err == nil {
			t.Fatal("receive error = nil after Close, want error")
		}
	case <-time.After(time.Second):
		t.Fatal("receive did not unblock after Close")
	}
	peer.Close()
	<-peerDone
	if canWatchAppServer(client) {
		t.Fatal("canWatchAppServer() = true after receive Close, want false")
	}
}

func TestRequestUserInputResponseKeepsDiscoveryBeforePlan(t *testing.T) {
	got := requestUserInputResponse([]byte(`{"questions":[{"id":"scope"}]}`))

	answers, ok := got["answers"].(map[string]map[string][]string)
	if !ok {
		t.Fatalf("answers = %#v, want question answers", got["answers"])
	}
	scopeAnswers := answers["scope"]["answers"]
	if len(scopeAnswers) != 1 {
		t.Fatalf("scope answers = %#v, want one fallback answer", scopeAnswers)
	}
	answer := scopeAnswers[0]
	for _, want := range []string{
		"continue normal non-mutating discovery",
		"before presenting the implementation plan",
		"remaining ambiguity",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("fallback answer missing %q: %q", want, answer)
		}
	}
	if strings.Contains(answer, "proceed with the implementation plan") {
		t.Fatalf("fallback answer still shortcuts to plan: %q", answer)
	}
}

func TestWebSocketAcceptMatchesRFCExample(t *testing.T) {
	got := webSocketAccept("dGhlIHNhbXBsZSBub25jZQ==")
	const want = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Fatalf("webSocketAccept() = %q, want %q", got, want)
	}
}

func TestWaitForCodexTUIAfterReadyReturnsTUIExit(t *testing.T) {
	tuiDone := make(chan error, 1)
	drainDone := make(chan error, 1)
	tuiDone <- nil

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, &client{}, nil, false)

	if !tuiExited {
		t.Fatal("tuiExited = false, want true")
	}
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

func TestWaitForCodexTUIAfterReadyReturnsDrainError(t *testing.T) {
	tuiDone := make(chan error, 1)
	drainDone := make(chan error, 1)
	drainDone <- errors.New("unsupported request")

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, &client{}, nil, false)

	if tuiExited {
		t.Fatal("tuiExited = true, want false")
	}
	if err == nil || !strings.Contains(err.Error(), "unsupported request") {
		t.Fatalf("error = %v, want unsupported request", err)
	}
}

func TestWaitForCodexTUIAfterReadyIgnoresCompletedTurnDrain(t *testing.T) {
	tuiDone := make(chan error, 1)
	drainDone := make(chan error, 1)
	drainDone <- nil
	tuiDone <- nil

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, &client{}, nil, false)

	if !tuiExited {
		t.Fatal("tuiExited = false, want true")
	}
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

func TestWaitForCodexTUIAfterReadyIgnoresPostReadyWatcherError(t *testing.T) {
	conn, peer := net.Pipe()
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		_, _ = io.Copy(io.Discard, peer)
	}()
	client := &client{conn: &websocketJSONConn{conn: conn, br: bufio.NewReader(conn)}}
	defer func() {
		client.Close()
		_ = peer.Close()
		<-peerDone
	}()
	tuiDone := make(chan error, 1)
	drainDone := make(chan error, 1)
	drainDone <- nil
	resultDone := make(chan struct {
		tuiExited bool
		err       error
	}, 1)
	go func() {
		tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, client, nil, false)
		resultDone <- struct {
			tuiExited bool
			err       error
		}{tuiExited: tuiExited, err: err}
	}()

	// Send a malformed server text frame. This makes the post-ready
	// notification watcher fail, but the interactive TUI owns its own
	// connection and should keep running.
	if _, err := peer.Write([]byte{0x81, 0x01, '{'}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-resultDone:
		t.Fatalf("wait returned before TUI exit: tuiExited=%v err=%v", result.tuiExited, result.err)
	case <-time.After(100 * time.Millisecond):
	}

	tuiDone <- nil
	result := <-resultDone
	if !result.tuiExited {
		t.Fatal("tuiExited = false, want true")
	}
	if result.err != nil {
		t.Fatalf("error = %v, want nil", result.err)
	}
}

func TestWaitForCodexRemoteTUIStartupRejectsEarlyTUIExit(t *testing.T) {
	tuiDone := make(chan error, 1)
	drainDone := make(chan error, 1)
	server := &appServer{done: make(chan struct{}), logs: &lockedBuffer{}}
	tuiDone <- errors.New("early exit")

	gotDrain, err := waitForCodexRemoteTUIStartup(tuiDone, drainDone, server)

	if gotDrain != nil {
		t.Fatalf("drainDone = %#v, want nil on startup failure", gotDrain)
	}
	if err == nil || !strings.Contains(err.Error(), "early exit") {
		t.Fatalf("error = %v, want early exit", err)
	}
}

func TestCodexTurnCompletionAgentStateMapsTerminalStates(t *testing.T) {
	if got := codexTurnCompletionAgentState(codexTurnCompletion{Matched: true, Status: "completed"}); got != "plan" {
		t.Fatalf("codexTurnCompletionAgentState(completed) = %q, want plan", got)
	}
	if got := codexTurnCompletionAgentState(codexTurnCompletion{Matched: true, Status: "failed"}); got != "idle" {
		t.Fatalf("codexTurnCompletionAgentState(failed) = %q, want idle", got)
	}
	if got := codexTurnCompletionAgentState(codexTurnCompletion{Matched: true, Status: "interrupted"}); got != "idle" {
		t.Fatalf("codexTurnCompletionAgentState(interrupted) = %q, want idle", got)
	}
	if got := codexTurnCompletionAgentState(codexTurnCompletion{}); got != "" {
		t.Fatalf("codexTurnCompletionAgentState(unmatched) = %q, want empty", got)
	}
}

func TestCodexTurnNotificationAgentStateMarksStartedAndCompleted(t *testing.T) {
	started := appServerMessage{Method: "turn/started"}
	if got := codexTurnNotificationAgentState(started); got != "working" {
		t.Fatalf("codexTurnNotificationAgentState(turn/started) = %q, want working", got)
	}

	completed := appServerMessage{
		Method: "turn/completed",
		Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","threadId":"thread-1","status":"completed"}}`),
	}
	if got := codexTurnNotificationAgentState(completed); got != "plan" {
		t.Fatalf("codexTurnNotificationAgentState(turn/completed) = %q, want plan", got)
	}

	failed := appServerMessage{
		Method: "turn/completed",
		Params: json.RawMessage(`{"turn":{"status":"failed"}}`),
	}
	if got := codexTurnNotificationAgentState(failed); got != "idle" {
		t.Fatalf("codexTurnNotificationAgentState(failed turn/completed) = %q, want idle", got)
	}
}

func TestDrainCodexAppServerNotificationsUntilClosedDoesNotHandleServerRequests(t *testing.T) {
	var states []string

	receiver := &fakeAppServerReceiver{messages: []appServerMessage{
		{
			ID:     json.RawMessage(`"req-1"`),
			Method: "tool/requestUserInput",
			Params: json.RawMessage(`{"questions":[{"id":"scope"}]}`),
		},
		{Method: "turn/started"},
		{
			Method: "turn/completed",
			Params: json.RawMessage(`{"turn":{"status":"interrupted"}}`),
		},
	}}

	if err := drainCodexAppServerNotificationsUntilClosed(receiver, recordedStates(&states)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(states, []string{"working", "idle"}) {
		t.Fatalf("states = %#v, want working then idle", states)
	}
}

func TestDrainCodexAppServerDuringStartupHandlesRequestsAndStates(t *testing.T) {
	var states []string
	client := &fakeStartupAppServerClient{messages: []appServerMessage{
		{
			ID:     json.RawMessage(`"req-1"`),
			Method: "tool/requestUserInput",
			Params: json.RawMessage(`{"questions":[{"id":"scope"}]}`),
		},
		{Method: "turn/started"},
		{
			Method: "turn/completed",
			Params: json.RawMessage(`{"turn":{"status":"completed"}}`),
		},
	}}

	if err := drainCodexAppServerDuringStartup(client, recordedStates(&states)); err != nil {
		t.Fatal(err)
	}
	if len(client.sent) != 1 {
		t.Fatalf("sent responses = %d, want 1", len(client.sent))
	}
	if !slices.Equal(states, []string{"blocked", "working", "working", "plan"}) {
		t.Fatalf("states = %#v, want blocked, working, working, plan", states)
	}
}

func TestDrainCodexAppServerNotificationsReportsThreadStarted(t *testing.T) {
	var states []string
	threadReady := make(chan codexThreadInfo, 1)

	receiver := &fakeAppServerReceiver{messages: []appServerMessage{
		{
			Method: "thread/started",
			Params: json.RawMessage(`{"thread":{"id":"thread-1","sessionId":"session-1"}}`),
		},
		{Method: "turn/started"},
		{
			Method: "turn/completed",
			Params: json.RawMessage(`{"turn":{"status":"completed"}}`),
		},
	}}

	if err := drainCodexAppServerNotificationsUntilClosedWithThread(receiver, recordedStates(&states), threadReady); err != nil {
		t.Fatal(err)
	}
	thread := <-threadReady
	if thread.ID != "thread-1" || thread.SessionID != "session-1" {
		t.Fatalf("thread = %+v, want thread/session ids", thread)
	}
	if !slices.Equal(states, []string{"working", "plan"}) {
		t.Fatalf("states = %#v, want working then plan", states)
	}
}

func TestServerRequestAgentStateMarksBlockingRequests(t *testing.T) {
	methods := []string{
		"item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/tool/requestUserInput",
		"tool/requestUserInput",
		"item/permissions/requestApproval",
		"mcpServer/elicitation/request",
		"execCommandApproval",
		"applyPatchApproval",
	}
	for _, method := range methods {
		if got := serverRequestAgentState(method); got != "blocked" {
			t.Fatalf("serverRequestAgentState(%q) = %q, want blocked", method, got)
		}
	}
	if got := serverRequestAgentState("item/tool/call"); got != "" {
		t.Fatalf("serverRequestAgentState(item/tool/call) = %q, want empty", got)
	}
}

func TestHandleServerRequestRestoresWorkingAfterAutoHandledBlockingRequest(t *testing.T) {
	var states []string

	client := &fakeCodexAppClient{}
	err := handleServerRequestWithState(client, appServerMessage{
		ID:     json.RawMessage(`"req-1"`),
		Method: "tool/requestUserInput",
		Params: json.RawMessage(`{"questions":[{"id":"scope"}]}`),
	}, recordedStates(&states))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(states, []string{"blocked", "working"}) {
		t.Fatalf("states = %#v, want blocked then working", states)
	}
	if len(client.sent) != 1 {
		t.Fatalf("sent responses = %d, want 1", len(client.sent))
	}
}

func TestSignalExitCodeUsesConventionalShellSignalStatus(t *testing.T) {
	if got, want := signalExitCode(syscall.SIGHUP), 129; got != want {
		t.Fatalf("signalExitCode(SIGHUP) = %d, want %d", got, want)
	}
}

func TestCodexThreadStartedNotificationFallsBackToThreadIDAsSessionID(t *testing.T) {
	got, ok := codexThreadStartedNotification(appServerMessage{
		Method: "thread/started",
		Params: json.RawMessage(`{"thread":{"id":"thread-1"}}`),
	})
	if !ok {
		t.Fatal("codexThreadStartedNotification() ok = false, want true")
	}
	if got.ID != "thread-1" || got.SessionID != "thread-1" {
		t.Fatalf("codexThreadStartedNotification() = %+v, want thread id reused as session id", got)
	}
}

func TestWaitForCodexTUIAfterReadyIgnoresFreshWatcherError(t *testing.T) {
	tuiDone := make(chan error, 1)
	drainDone := make(chan error, 1)
	tuiDone <- nil
	drainDone <- errors.New("watcher parse error")

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, &client{}, nil, true)

	if !tuiExited {
		t.Fatal("tuiExited = false, want true")
	}
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

type fakeCodexAppClient struct {
	sent []any
}

type fakeStartupAppServerClient struct {
	messages []appServerMessage
	sent     []any
}

type fakeAppServerReceiver struct {
	messages []appServerMessage
}

func (f *fakeStartupAppServerClient) receive() (appServerMessage, error) {
	if len(f.messages) == 0 {
		return appServerMessage{}, io.EOF
	}
	msg := f.messages[0]
	f.messages = f.messages[1:]
	return msg, nil
}

func (f *fakeStartupAppServerClient) send(v any) error {
	f.sent = append(f.sent, v)
	return nil
}

func (f *fakeAppServerReceiver) receive() (appServerMessage, error) {
	if len(f.messages) == 0 {
		return appServerMessage{}, io.EOF
	}
	msg := f.messages[0]
	f.messages = f.messages[1:]
	return msg, nil
}

func (f *fakeCodexAppClient) send(v any) error {
	f.sent = append(f.sent, v)
	return nil
}
