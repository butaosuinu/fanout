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
	got := codexRemoteTUIArgs("ws://127.0.0.1:1234", "session-1", "hello plan")
	want := []string{"--remote", "ws://127.0.0.1:1234", "resume", "session-1", "--", "hello plan"}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codexRemoteTUIArgs() = %#v, want %#v", got, want)
	}
}

func TestCodexRemoteTUIArgsSeparatesDashLeadingPrompt(t *testing.T) {
	got := codexRemoteTUIArgs("ws://127.0.0.1:1234", "session-1", "-- investigate")
	want := []string{"--remote", "ws://127.0.0.1:1234", "resume", "session-1", "--", "-- investigate"}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codexRemoteTUIArgs() = %#v, want %#v", got, want)
	}
}

func TestCodexRemoteTUIArgsCanResumeWithoutPromptForFallbackTurn(t *testing.T) {
	got := codexRemoteTUIArgs("ws://127.0.0.1:1234", "session-1", "")
	want := []string{"--remote", "ws://127.0.0.1:1234", "resume", "session-1"}

	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codexRemoteTUIArgs() = %#v, want %#v", got, want)
	}
}

func TestCodexTurnStartParamsCanCarryPlanCollaborationMode(t *testing.T) {
	got := codexTurnStartParams("thread-1", "/repo", "gpt-test", "hello plan", codexPlanCollaborationMode("gpt-test", "medium"))

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
	if shaped.CollaborationMode.Settings.ReasoningEffort != "medium" {
		t.Fatalf("reasoning_effort = %q, want medium", shaped.CollaborationMode.Settings.ReasoningEffort)
	}
	if len(shaped.Input) != 1 || shaped.Input[0].Type != "text" || shaped.Input[0].Text != "hello plan" {
		t.Fatalf("input = %#v, want one text prompt", shaped.Input)
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
