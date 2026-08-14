package herdrrun

import (
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

func TestDirectCodexIntegrationLaunchExcludesControllersAndOtherAgents(t *testing.T) {
	direct := state.HerdrIntent{Launch: &state.HerdrLaunch{Agent: "codex"}}
	if !directCodexIntegrationLaunch(direct) {
		t.Fatal("direct Codex launch did not enable the restricted relay")
	}
	plan := direct
	plan.Launch = &state.HerdrLaunch{Agent: "codex", CodexPlanStatusPath: "/status"}
	claude := state.HerdrIntent{Launch: &state.HerdrLaunch{Agent: "claude"}}
	if directCodexIntegrationLaunch(plan) || directCodexIntegrationLaunch(claude) {
		t.Fatal("restricted Codex relay enabled for an unsupported launch")
	}
}
