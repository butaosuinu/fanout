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

func TestCodexRemoteTUIArgsPassPromptToResume(t *testing.T) {
	got := codexRemoteTUIArgs("ws://127.0.0.1:1234", "thread-1", "hello plan")
	want := []string{"--remote", "ws://127.0.0.1:1234", "resume", "thread-1", "--", "hello plan"}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codexRemoteTUIArgs() = %#v, want %#v", got, want)
	}
}

func TestCodexRemoteTUIArgsSeparatesDashLeadingPrompt(t *testing.T) {
	got := codexRemoteTUIArgs("ws://127.0.0.1:1234", "thread-1", "-- investigate")
	want := []string{"--remote", "ws://127.0.0.1:1234", "resume", "thread-1", "--", "-- investigate"}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codexRemoteTUIArgs() = %#v, want %#v", got, want)
	}
}

func TestCodexRemoteTUIArgsCanResumeWithoutPromptForFallbackTurn(t *testing.T) {
	got := codexRemoteTUIArgs("ws://127.0.0.1:1234", "thread-1", "")
	want := []string{"--remote", "ws://127.0.0.1:1234", "resume", "thread-1"}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codexRemoteTUIArgs() = %#v, want %#v", got, want)
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

func TestCodexTurnStartParamsCanCarryPlanCollaborationMode(t *testing.T) {
	got := codexTurnStartParams("thread-1", "/repo", "gpt-test", "hello plan", codexPlanCollaborationMode("gpt-test", "xhigh"))

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var shaped struct {
		ThreadID          string `json:"threadId"`
		CollaborationMode struct {
			Mode     string `json:"mode"`
			Settings struct {
				Model           string `json:"model"`
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"settings"`
		} `json:"collaborationMode"`
		Input []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
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
	if shaped.CollaborationMode.Settings.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want xhigh", shaped.CollaborationMode.Settings.ReasoningEffort)
	}
	if len(shaped.Input) != 1 || shaped.Input[0].Type != "text" || shaped.Input[0].Text != "hello plan" {
		t.Fatalf("input = %#v, want one text prompt", shaped.Input)
	}
}

func TestCodexPlanThreadConfiguresPlanModeBeforeInitialTurn(t *testing.T) {
	client := &fakeCodexAppClient{}

	thread, err := setupCodexPlanThread(client, "/repo", "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if thread.UseTurnCollaborationMode {
		t.Fatal("UseTurnCollaborationMode = true, want false when thread/settings/update succeeds")
	}

	want := "initialize,collaborationMode/list,config/read,model/list,thread/start,thread/settings/update"
	if got := strings.Join(client.requestMethods(), ","); got != want {
		t.Fatalf("request order = %s, want %s", got, want)
	}
	if client.hasRequest("turn/start") {
		t.Fatal("setupCodexPlanThread started a turn; want RunPlanTUI to start the initial turn")
	}
}

func TestCodexPlanThreadStartsInitialTurnAfterSettingsUpdate(t *testing.T) {
	client := &fakeCodexAppClient{}

	thread, err := setupCodexPlanThread(client, "/repo", "test-version")
	if err != nil {
		t.Fatal(err)
	}
	turnStart, err := startCodexPlanTurn(client, thread, "/repo", "hello plan")
	if err != nil {
		t.Fatal(err)
	}
	if turnStart.Completed {
		t.Fatal("Completed = true, want false for in-progress turn response")
	}

	turn := client.lastRequest("turn/start").paramsMap(t)
	if _, ok := turn["collaborationMode"]; ok {
		t.Fatalf("turn/start collaborationMode = %#v, want absent after thread/settings/update", turn["collaborationMode"])
	}
	assertTurnStartText(t, turn, "hello plan")
}

func TestCodexPlanThreadStartsInitialTurnWhenSettingsUpdateIsUnsupported(t *testing.T) {
	client := &fakeCodexAppClient{
		methodErrors: map[string]error{
			"thread/settings/update": errors.New(`unknown method "thread/settings/update"`),
		},
	}

	thread, err := setupCodexPlanThread(client, "/repo", "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !thread.UseTurnCollaborationMode {
		t.Fatal("UseTurnCollaborationMode = false, want true when thread/settings/update is unsupported")
	}
	turnStart, err := startCodexPlanTurn(client, thread, "/repo", "hello fallback")
	if err != nil {
		t.Fatal(err)
	}
	if turnStart.Completed {
		t.Fatal("Completed = true, want false for in-progress turn response")
	}

	turn := client.lastRequest("turn/start").paramsMap(t)
	mode, ok := turn["collaborationMode"].(map[string]any)
	if !ok {
		t.Fatalf("turn/start missing fallback collaborationMode: %#v", turn)
	}
	if mode["mode"] != "plan" {
		t.Fatalf("collaborationMode.mode = %q, want plan", mode["mode"])
	}
	settings, ok := mode["settings"].(map[string]any)
	if !ok {
		t.Fatalf("collaborationMode.settings = %#v, want map", mode["settings"])
	}
	if settings["model"] != "gpt-test" || settings["reasoning_effort"] != "xhigh" {
		t.Fatalf("fallback settings = %#v, want model gpt-test and effort xhigh", settings)
	}
	assertTurnStartText(t, turn, "hello fallback")
}

func TestCodexPlanTurnStartReportsCompletedStatus(t *testing.T) {
	client := &fakeCodexAppClient{
		methodResults: map[string]json.RawMessage{
			"turn/start": json.RawMessage(`{"turn":{"id":"turn-1","status":"completed"}}`),
		},
	}
	thread := codexThreadInfo{ID: "thread-1", Model: "gpt-test", PlanEffort: "xhigh"}

	turnStart, err := startCodexPlanTurn(client, thread, "/repo", "hello")
	if err != nil {
		t.Fatal(err)
	}

	if !turnStart.Completed {
		t.Fatal("Completed = false, want true for completed turn response")
	}
	if turnStart.TurnID != "turn-1" {
		t.Fatalf("TurnID = %q, want turn-1", turnStart.TurnID)
	}
}

func TestCodexPlanTurnStartErrorsOnFailedStatus(t *testing.T) {
	client := &fakeCodexAppClient{
		methodResults: map[string]json.RawMessage{
			"turn/start": json.RawMessage(`{"turn":{"id":"turn-1","status":"failed"}}`),
		},
	}
	thread := codexThreadInfo{ID: "thread-1", Model: "gpt-test", PlanEffort: "xhigh"}

	_, err := startCodexPlanTurn(client, thread, "/repo", "hello")

	if err == nil || !strings.Contains(err.Error(), `status "failed"`) {
		t.Fatalf("error = %v, want failed status error", err)
	}
}

// recordedStates collects setState calls; read them only after the drain
// channel is received so the drain goroutine's writes are ordered before the
// assertion.
func recordedStates(states *[]string) func(string) {
	return func(state string) { *states = append(*states, state) }
}

// TestBeginCodexPlanTurnReportsWorkingThenPlan guarantees the fanout-driven
// initial turn reports "working" while the turn runs and "plan" once the
// turn/completed notification lands, in that order.
func TestBeginCodexPlanTurnReportsWorkingThenPlan(t *testing.T) {
	client := &fakeCodexAppClient{
		stream: []appServerMessage{{
			Method: "turn/completed",
			Params: json.RawMessage(`{"threadId":"thread-1","turn":{"status":"completed"}}`),
		}},
	}
	var states []string

	drainDone, err := beginCodexPlanTurn(client, codexThreadInfo{ID: "thread-1"}, "/repo", "hello plan", recordedStates(&states))
	if err != nil {
		t.Fatal(err)
	}
	if drainDone == nil {
		t.Fatal("drainDone = nil, want drain channel for in-progress turn")
	}
	if drainErr := <-drainDone; drainErr != nil {
		t.Fatalf("drain error = %v, want nil", drainErr)
	}
	if got := strings.Join(states, ","); got != "working,plan" {
		t.Fatalf("states = %q, want working,plan", got)
	}
}

// TestBeginCodexPlanTurnReportsPlanOnSynchronousCompletion covers the
// turn/start response that already carries a completed status: no drain is
// needed, "working" is skipped, and the client is closed.
func TestBeginCodexPlanTurnReportsPlanOnSynchronousCompletion(t *testing.T) {
	client := &fakeCodexAppClient{
		methodResults: map[string]json.RawMessage{
			"turn/start": json.RawMessage(`{"turn":{"id":"turn-1","status":"completed"}}`),
		},
	}
	var states []string

	drainDone, err := beginCodexPlanTurn(client, codexThreadInfo{ID: "thread-1"}, "/repo", "hello plan", recordedStates(&states))
	if err != nil {
		t.Fatal(err)
	}
	if drainDone != nil {
		t.Fatal("drainDone != nil, want nil for synchronously completed turn")
	}
	if got := strings.Join(states, ","); got != "plan" {
		t.Fatalf("states = %q, want plan", got)
	}
	if client.closed {
		t.Fatal("client closed after synchronous completion")
	}
}

// TestBeginCodexPlanTurnReportsIdleOnFailedTurn pins that a turn ending in a
// non-completed terminal status leaves the display state at a non-running idle.
func TestBeginCodexPlanTurnSkipsPlanOnFailedTurn(t *testing.T) {
	client := &fakeCodexAppClient{
		stream: []appServerMessage{{
			Method: "turn/completed",
			Params: json.RawMessage(`{"threadId":"thread-1","turn":{"status":"failed"}}`),
		}},
	}
	var states []string

	drainDone, err := beginCodexPlanTurn(client, codexThreadInfo{ID: "thread-1"}, "/repo", "hello plan", recordedStates(&states))
	if err != nil {
		t.Fatal(err)
	}
	if drainErr := <-drainDone; drainErr == nil || !strings.Contains(drainErr.Error(), `status "failed"`) {
		t.Fatalf("drain error = %v, want failed status error", drainErr)
	}
	if got := strings.Join(states, ","); got != "working,idle" {
		t.Fatalf("states = %q, want working,idle", got)
	}
}

// TestDrainCodexAppServerSkipsPlanWhenClientClosed pins that the drain's
// closed-connection exit (fanout shutting down, not turn completion) reports
// nothing: a dead pane must not end up labeled "plan".
func TestDrainCodexAppServerSkipsPlanWhenClientClosed(t *testing.T) {
	client := &fakeCodexAppClient{streamErr: net.ErrClosed}
	var states []string

	if err := drainCodexAppServerUntilTurnComplete(client, "thread-1", "", recordedStates(&states)); err != nil {
		t.Fatalf("drain error = %v, want nil on closed client", err)
	}
	if len(states) != 0 {
		t.Fatalf("states = %v, want none", states)
	}
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

func TestSeedCodexPlanThreadForResumeInjectsReadyAssistantItem(t *testing.T) {
	client := &fakeCodexAppClient{}

	if err := seedCodexPlanThreadForResume(client, "thread-1"); err != nil {
		t.Fatal(err)
	}

	req := client.lastRequest("thread/inject_items")
	params := req.paramsMap(t)
	if params["threadId"] != "thread-1" {
		t.Fatalf("threadId = %q, want thread-1", params["threadId"])
	}
	items, ok := params["items"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v, want one injected item", params["items"])
	}
	if items[0]["type"] != "message" || items[0]["role"] != "assistant" {
		t.Fatalf("item = %#v, want assistant message", items[0])
	}
	content, ok := items[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one output_text item", items[0]["content"])
	}
	if content[0]["type"] != "output_text" || content[0]["text"] != codexPlanSeedAssistantText {
		t.Fatalf("content = %#v, want Ready output_text", content[0])
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

func TestConfigSettingsReadsModelAndReasoningEffort(t *testing.T) {
	got := configSettings([]byte(`{"config":{"model":" gpt-test ","plan_mode_reasoning_effort":" xhigh "}}`))

	if got.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", got.Model)
	}
	if got.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want xhigh", got.ReasoningEffort)
	}
}

func TestConfigSettingsFallsBackToModelReasoningEffort(t *testing.T) {
	got := configSettings([]byte(`{"config":{"model":"gpt-test","model_reasoning_effort":"high"}}`))

	if got.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", got.ReasoningEffort)
	}
}

func TestConfigSettingsPlanModeReasoningEffortWins(t *testing.T) {
	got := configSettings([]byte(`{"config":{"model":"gpt-test","reasoning_effort":"low","model_reasoning_effort":"medium","plan_mode_reasoning_effort":"xhigh"}}`))

	if got.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want xhigh", got.ReasoningEffort)
	}
}

func TestCodexPlanCollaborationModeUsesXHighFallback(t *testing.T) {
	got := codexPlanCollaborationMode("gpt-test", codexPlanDefaultEffort)

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var shaped struct {
		Settings struct {
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(body, &shaped); err != nil {
		t.Fatal(err)
	}
	if shaped.Settings.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want xhigh", shaped.Settings.ReasoningEffort)
	}
}

func TestEnsureCodexPlanModeIgnoresAdvertisedEffort(t *testing.T) {
	err := ensureCodexPlanMode([]byte(`{"data":[{"mode":"plan","reasoning_effort":"medium","settings":{"reasoning_effort":"medium"}}]}`))
	if err != nil {
		t.Fatalf("ensureCodexPlanMode() failed: %v", err)
	}
}

func TestModelListSelectionReturnsSupportedReasoningEfforts(t *testing.T) {
	got, err := modelListSelection([]byte(`{"data":[{"id":"gpt-test","model":"gpt-test","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"},{"reasoningEffort":"high"}]}]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", got.Model)
	}
	if strings.Join(got.SupportedReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("supported efforts = %#v, want low,medium,high", got.SupportedReasoningEfforts)
	}
}

func TestModelListSelectionUsesPreferredHiddenModel(t *testing.T) {
	got, err := modelListSelection([]byte(`{"data":[{"id":"gpt-visible","model":"gpt-visible","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"low"}]},{"id":"gpt-hidden","model":"gpt-hidden","hidden":true,"supportedReasoningEfforts":[{"reasoningEffort":"xhigh"}]}]}`), "gpt-hidden")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-hidden" {
		t.Fatalf("model = %q, want gpt-hidden", got.Model)
	}
	if strings.Join(got.SupportedReasoningEfforts, ",") != "xhigh" {
		t.Fatalf("supported efforts = %#v, want xhigh", got.SupportedReasoningEfforts)
	}
}

func TestSupportedReasoningEffortKeepsSupportedRequestedEffort(t *testing.T) {
	got := supportedReasoningEffort("xhigh", []string{"low", "medium", "xhigh"})

	if got != "xhigh" {
		t.Fatalf("supportedReasoningEffort() = %q, want xhigh", got)
	}
}

func TestSupportedReasoningEffortFallsBackToStrongestSupportedEffort(t *testing.T) {
	got := supportedReasoningEffort("xhigh", []string{"low", "medium", "high", "ultra"})

	if got != "ultra" {
		t.Fatalf("supportedReasoningEffort() = %q, want ultra", got)
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

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, &client{}, nil)

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

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, &client{}, nil)

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

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone, &client{}, nil)

	if !tuiExited {
		t.Fatal("tuiExited = false, want true")
	}
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

func TestCodexTurnCompletedNotificationMatchesThread(t *testing.T) {
	msg := appServerMessage{
		Method: "turn/completed",
		Params: json.RawMessage(`{"threadId":"thread-1","turn":{"status":"completed"}}`),
	}

	completion := codexTurnCompletedNotification(msg, "thread-1", "")
	if !completion.Matched || completion.Status != "completed" {
		t.Fatalf("completion = %+v, want completed match", completion)
	}
	if codexTurnCompletedNotification(msg, "thread-2", "").Matched {
		t.Fatal("codexTurnCompletedNotification() matched the wrong thread")
	}
}

func TestCodexTurnCompletedNotificationMatchesNestedTurn(t *testing.T) {
	msg := appServerMessage{
		Method: "turn/completed",
		Params: json.RawMessage(`{"turn":{"id":"turn-1","threadId":"thread-1","status":"completed"}}`),
	}

	completion := codexTurnCompletedNotification(msg, "thread-1", "turn-1")
	if !completion.Matched || completion.Status != "completed" {
		t.Fatalf("completion = %+v, want completed match", completion)
	}
	if codexTurnCompletedNotification(msg, "thread-1", "turn-2").Matched {
		t.Fatal("codexTurnCompletedNotification() matched the wrong turn")
	}
}

func TestCodexTurnCompletedNotificationReportsFailedStatus(t *testing.T) {
	msg := appServerMessage{
		Method: "turn/completed",
		Params: json.RawMessage(`{"turn":{"id":"turn-1","status":"failed"}}`),
	}

	completion := codexTurnCompletedNotification(msg, "thread-1", "turn-1")
	if !completion.Matched || completion.Status != "failed" {
		t.Fatalf("completion = %+v, want failed match", completion)
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

type fakeCodexAppClient struct {
	calls         []fakeCodexRequest
	notifications []string
	methodErrors  map[string]error
	methodResults map[string]json.RawMessage
	// stream scripts receive for drain tests: messages are popped in order,
	// then streamErr (default net.ErrClosed, as if fanout closed the client)
	// is returned. sent records and closed flags, so beginCodexPlanTurn is
	// fully observable.
	stream    []appServerMessage
	streamErr error
	sent      []any
	closed    bool
}

type fakeAppServerReceiver struct {
	messages []appServerMessage
}

func (f *fakeAppServerReceiver) receive() (appServerMessage, error) {
	if len(f.messages) == 0 {
		return appServerMessage{}, io.EOF
	}
	msg := f.messages[0]
	f.messages = f.messages[1:]
	return msg, nil
}

type fakeCodexRequest struct {
	id     string
	method string
	params any
}

func (f *fakeCodexAppClient) receive() (appServerMessage, error) {
	if len(f.stream) == 0 {
		if f.streamErr != nil {
			return appServerMessage{}, f.streamErr
		}
		return appServerMessage{}, net.ErrClosed
	}
	msg := f.stream[0]
	f.stream = f.stream[1:]
	return msg, nil
}

func (f *fakeCodexAppClient) send(v any) error {
	f.sent = append(f.sent, v)
	return nil
}

func (f *fakeCodexAppClient) Close() {
	f.closed = true
}

func (f *fakeCodexAppClient) Request(id, method string, params any) (json.RawMessage, error) {
	f.calls = append(f.calls, fakeCodexRequest{id: id, method: method, params: params})
	if err := f.methodErrors[method]; err != nil {
		return nil, err
	}
	if result, ok := f.methodResults[method]; ok {
		return result, nil
	}
	switch method {
	case "collaborationMode/list":
		return json.RawMessage(`{"data":[{"mode":"plan"}]}`), nil
	case "config/read":
		return json.RawMessage(`{"config":{"model":"gpt-test","plan_mode_reasoning_effort":"xhigh"}}`), nil
	case "model/list":
		return json.RawMessage(`{"data":[{"id":"gpt-test","model":"gpt-test","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"},{"reasoningEffort":"high"},{"reasoningEffort":"xhigh"}]}]}`), nil
	case "thread/start":
		return json.RawMessage(`{"thread":{"id":"thread-1","sessionId":"session-1"}}`), nil
	case "turn/start":
		return json.RawMessage(`{"turn":{"status":"inProgress"}}`), nil
	default:
		return json.RawMessage(`{}`), nil
	}
}

func (f *fakeCodexAppClient) Notify(method string) error {
	f.notifications = append(f.notifications, method)
	return nil
}

func (f *fakeCodexAppClient) requestMethods() []string {
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, call.method)
	}
	return out
}

func (f *fakeCodexAppClient) lastRequest(method string) fakeCodexRequest {
	for _, call := range slices.Backward(f.calls) {
		if call.method == method {
			return call
		}
	}
	return fakeCodexRequest{}
}

func (f *fakeCodexAppClient) hasRequest(method string) bool {
	for _, call := range f.calls {
		if call.method == method {
			return true
		}
	}
	return false
}

func (r fakeCodexRequest) paramsMap(t *testing.T) map[string]any {
	t.Helper()
	params, ok := r.params.(map[string]any)
	if !ok {
		t.Fatalf("%s params = %#v, want map[string]any", r.method, r.params)
	}
	return params
}

func assertTurnStartText(t *testing.T, params map[string]any, want string) {
	t.Helper()
	input, ok := params["input"].([]map[string]any)
	if !ok || len(input) != 1 {
		t.Fatalf("turn/start input = %#v, want one text item", params["input"])
	}
	if input[0]["type"] != "text" || input[0]["text"] != want {
		t.Fatalf("turn/start input = %#v, want text %q", input[0], want)
	}
}
