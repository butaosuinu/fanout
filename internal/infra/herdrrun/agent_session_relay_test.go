package herdrrun

import (
	"bufio"
	"errors"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestValidateAgentSessionReportRequiresRestrictedMethodAndIdentity(t *testing.T) {
	intent := state.LaunchIntent{
		Kind: state.IntentWorktree, Resource: state.RuntimeResource{PaneID: "w1:p1"},
	}
	report := validAgentSessionReport()
	if err := validateAgentSessionReport(report, intent); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*agentSessionReport)
	}{
		{name: "empty request id", mutate: func(report *agentSessionReport) { report.ID = "" }},
		{name: "long request id", mutate: func(report *agentSessionReport) { report.ID = strings.Repeat("x", 257) }},
		{name: "other method", mutate: func(report *agentSessionReport) { report.Method = "workspace.close" }},
		{name: "other pane", mutate: func(report *agentSessionReport) { report.Params.PaneID = "w2:p1" }},
		{name: "other source", mutate: func(report *agentSessionReport) { report.Params.Source = "herdr:claude" }},
		{name: "other agent", mutate: func(report *agentSessionReport) { report.Params.Agent = "claude" }},
		{name: "zero sequence", mutate: func(report *agentSessionReport) { report.Params.Seq = 0 }},
		{name: "empty session", mutate: func(report *agentSessionReport) { report.Params.AgentSessionID = "" }},
		{name: "leading session space", mutate: func(report *agentSessionReport) { report.Params.AgentSessionID = " session-1" }},
		{name: "trailing session space", mutate: func(report *agentSessionReport) { report.Params.AgentSessionID = "session-1 " }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := report
			test.mutate(&changed)
			if err := validateAgentSessionReport(changed, intent); err == nil {
				t.Fatal("agent-session relay accepted a report outside its launch identity")
			}
		})
	}
}

func TestValidateAgentSessionReportPinsResumeConversation(t *testing.T) {
	intent := state.LaunchIntent{
		Kind: state.IntentResume, Resource: state.RuntimeResource{PaneID: "w1:p1"},
		ResumeAgentSession: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-1",
		},
	}
	report := validAgentSessionReport()
	if err := validateAgentSessionReport(report, intent); err != nil {
		t.Fatal(err)
	}
	report.Params.AgentSessionID = "session-2"
	if err := validateAgentSessionReport(report, intent); err == nil {
		t.Fatal("resume relay accepted a different Codex conversation")
	}
	report.Params.AgentSessionID = "session-1"
	intent.ResumeAgentSession = nil
	if err := validateAgentSessionReport(report, intent); err == nil {
		t.Fatal("resume relay accepted a missing saved conversation")
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

func TestReadAgentSessionReportRequiresOneBoundedJSONValue(t *testing.T) {
	valid := `{"id":"hook","method":"pane.report_agent_session","params":{` +
		`"pane_id":"w1:p1","source":"herdr:codex","agent":"codex","seq":1,` +
		`"agent_session_id":"session-1"}}`
	if _, err := readAgentSessionReport(strings.NewReader(valid + ` {}` + "\n")); err == nil {
		t.Fatal("agent-session relay accepted trailing JSON")
	}
	oversize := strings.Repeat(" ", maxAgentSessionReportBytes) + "\n"
	if _, err := readAgentSessionReport(strings.NewReader(oversize)); !errors.Is(err, bufio.ErrBufferFull) {
		t.Fatalf("oversize error = %v, want buffer limit", err)
	}
}

func TestCodexAgentSessionRelayLaunchIncludesAttachAndExcludesControllers(t *testing.T) {
	for _, kind := range []state.LaunchIntentKind{
		state.IntentWorktree, state.IntentResume, state.IntentCoordinator,
	} {
		intent := state.LaunchIntent{Kind: kind, Launch: &state.LaunchCapsule{Agent: "codex"}}
		if !codexAgentSessionRelayLaunch(intent) {
			t.Fatalf("Codex %s launch did not enable the restricted relay", kind)
		}
	}
	tests := []state.LaunchIntent{
		{},
		{Kind: state.IntentWorktree, Launch: &state.LaunchCapsule{Agent: "claude"}},
		{Kind: state.IntentWorktree, Launch: &state.LaunchCapsule{
			Agent: "codex", CodexPlanStatusPath: "/status",
		}},
		{Kind: state.IntentWorktree, Launch: &state.LaunchCapsule{
			Agent: "codex", CodexTeamStatusPath: "/status",
		}},
	}
	for _, intent := range tests {
		if codexAgentSessionRelayLaunch(intent) {
			t.Fatalf("restricted Codex relay enabled for unsupported launch %+v", intent)
		}
	}
}

func validAgentSessionReport() agentSessionReport {
	return agentSessionReport{
		ID: "herdr:codex:1", Method: "pane.report_agent_session",
		Params: agentSessionReportParams{
			PaneID: "w1:p1", Source: "herdr:codex", Agent: "codex",
			Seq: 1, AgentSessionID: "session-1", SessionStartSource: "resume",
		},
	}
}
