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
	got := codexTurnStartParams("thread-1", "/repo", "gpt-test", "hello plan", codexPlanCollaborationMode("gpt-test", "xhigh"))

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
	if shaped.CollaborationMode.Settings.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want xhigh", shaped.CollaborationMode.Settings.ReasoningEffort)
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
