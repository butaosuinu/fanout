package main

import (
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"
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

func TestCodexPlanThreadLeavesInitialTurnForTUIAfterSettingsUpdate(t *testing.T) {
	client := &fakeCodexAppClient{}

	thread, err := setupCodexPlanThread(client, "/repo")
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
		t.Fatal("setupCodexPlanThread started a turn; want initial prompt left for the interactive TUI")
	}
}

func TestCodexPlanThreadStartsInitialTurnWithFallbackCollaborationMode(t *testing.T) {
	client := &fakeCodexAppClient{
		methodErrors: map[string]error{
			"thread/settings/update": errors.New(`unknown method "thread/settings/update"`),
		},
	}

	thread, err := setupCodexPlanThread(client, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !thread.UseTurnCollaborationMode {
		t.Fatal("UseTurnCollaborationMode = false, want true when thread/settings/update is unsupported")
	}
	if err = startCodexPlanTurn(client, thread, "/repo", "hello fallback"); err != nil {
		t.Fatal(err)
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

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone)

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

	tuiExited, err := waitForCodexTUIAfterReady(tuiDone, drainDone)

	if tuiExited {
		t.Fatal("tuiExited = true, want false")
	}
	if err == nil || !strings.Contains(err.Error(), "unsupported request") {
		t.Fatalf("error = %v, want unsupported request", err)
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
}

type fakeCodexRequest struct {
	id     string
	method string
	params any
}

func (f *fakeCodexAppClient) Request(id, method string, params any) (json.RawMessage, error) {
	f.calls = append(f.calls, fakeCodexRequest{id: id, method: method, params: params})
	if err := f.methodErrors[method]; err != nil {
		return nil, err
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
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].method == method {
			return f.calls[i]
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
