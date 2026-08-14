package herdrrun

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestValidateAgentSessionReportPinsResumeConversationAndPane(t *testing.T) {
	intent := state.HerdrIntent{
		Kind:     state.HerdrIntentResume,
		Resource: state.HerdrResource{PaneID: "w1:p1"},
		ResumeAgentSession: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-1",
		},
	}
	report := agentSessionReport{
		ID: "herdr:codex:1", Method: "pane.report_agent_session",
		Params: agentSessionReportParams{
			PaneID: "w1:p1", Source: "herdr:codex", Agent: "codex",
			Seq: 1, AgentSessionID: "session-1", SessionStartSource: "resume",
		},
	}
	if err := validateAgentSessionReport(report, intent); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*agentSessionReport){
		"method":   func(report *agentSessionReport) { report.Method = "workspace.close" },
		"pane":     func(report *agentSessionReport) { report.Params.PaneID = "w2:p1" },
		"provider": func(report *agentSessionReport) { report.Params.Agent = "claude" },
		"session":  func(report *agentSessionReport) { report.Params.AgentSessionID = "session-2" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := report
			mutate(&changed)
			if err := validateAgentSessionReport(changed, intent); err == nil {
				t.Fatal("foreign agent-session report was accepted")
			}
		})
	}
}

func TestReadAgentSessionReportRejectsExtraControlFields(t *testing.T) {
	input := `{"id":"hook","method":"pane.report_agent_session","params":{` +
		`"pane_id":"w1:p1","source":"herdr:codex","agent":"codex","seq":1,` +
		`"agent_session_id":"session-1"},"workspace_id":"w1"}` + "\n"
	if _, err := readAgentSessionReport(strings.NewReader(input)); err == nil {
		t.Fatal("agent-session relay accepted an extra control-plane field")
	}
}

func TestRelayAgentSessionReportRejectsInactiveIntentBeforeForward(t *testing.T) {
	controlPath := filepath.Join(t.TempDir(), "herdr-intents.json")
	if err := os.WriteFile(controlPath, []byte(`{"schemaVersion":1,"intents":[]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := agentSessionRelayRequest{
		controlPath: controlPath, intentID: "removed-intent", nonce: strings.Repeat("a", 32),
	}
	server, client := net.Pipe()
	defer func() {
		_ = server.Close()
		_ = client.Close()
	}()
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(client, `{"id":"hook","method":"pane.report_agent_session","params":{`+
			`"pane_id":"w1:p1","source":"herdr:codex","agent":"codex","seq":1,`+
			`"agent_session_id":"session-1"}}`+"\n")
		writeDone <- err
	}()

	err := relayAgentSessionReport(request, server)
	if err == nil || !strings.Contains(err.Error(), "launch intent is not active") {
		t.Fatalf("inactive intent error = %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestDirectCodexIntegrationLaunchExcludesControllersAndOtherAgents(t *testing.T) {
	direct := state.HerdrIntent{
		Kind: state.HerdrIntentWorktree, Launch: &state.HerdrLaunch{Agent: "codex"},
	}
	resume := direct
	resume.Kind = state.HerdrIntentResume
	if !directCodexIntegrationLaunch(direct) || !directCodexIntegrationLaunch(resume) {
		t.Fatal("direct Codex launch did not enable the restricted relay")
	}
	plan := direct
	plan.Launch = &state.HerdrLaunch{Agent: "codex", CodexPlanStatusPath: "/status"}
	attached := direct
	attached.Kind = state.HerdrIntentCoordinator
	claude := state.HerdrIntent{
		Kind: state.HerdrIntentWorktree, Launch: &state.HerdrLaunch{Agent: "claude"},
	}
	if directCodexIntegrationLaunch(plan) || directCodexIntegrationLaunch(attached) ||
		directCodexIntegrationLaunch(claude) {
		t.Fatal("restricted Codex relay enabled for an unsupported launch")
	}
}
