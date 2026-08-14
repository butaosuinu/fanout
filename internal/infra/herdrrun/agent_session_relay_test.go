package herdrrun

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestAgentSessionRelayRequestRequiresAbsoluteStatePath(t *testing.T) {
	t.Setenv(agentSessionRelayModeEnv, agentSessionRelayServe)
	t.Setenv(agentSessionRelayControlEnv, "/owned/herdr-intents.json")
	t.Setenv(agentSessionRelayExecutableEnv, "/owned/fanout")
	t.Setenv(agentSessionRelayIntentEnv, "issue:524:532")
	t.Setenv(agentSessionRelayNonceEnv, strings.Repeat("a", 32))
	t.Setenv(agentSessionRelaySocketEnv, "/owned/relay.sock")
	t.Setenv(agentSessionRelayStateEnv, "relative-state.json")
	if _, err := agentSessionRelayRequestFromEnvironment(); err == nil {
		t.Fatal("relative relay state path was accepted")
	}
	t.Setenv(agentSessionRelayStateEnv, "/repo/.fanout/state.json")
	if _, err := agentSessionRelayRequestFromEnvironment(); err != nil {
		t.Fatalf("absolute relay state path error = %v", err)
	}
}

func TestWaitForAgentSessionRelayReadyRequiresChildMarker(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = reader.Close()
		_ = writer.Close()
	}()
	go func() {
		_, _ = writer.Write([]byte{1}) // The readiness assertion reports a failed test read.
	}()
	if err := waitForAgentSessionRelayReady(reader); err != nil {
		t.Fatal(err)
	}
}

func TestRelayAgentSessionReportRejectsInactiveIntentBeforeForward(t *testing.T) {
	controlPath := filepath.Join(t.TempDir(), "herdr-intents.json")
	if err := os.WriteFile(controlPath, []byte(`{"schemaVersion":1,"intents":[]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := agentSessionRelayRequest{
		controlPath: controlPath, intentID: "removed-intent", nonce: strings.Repeat("a", 32),
		statePath: filepath.Join(t.TempDir(), "state.json"),
	}
	intent := testAgentSessionRelayIntent(request.statePath)
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

	err := relayAgentSessionReport(request, intent, server)
	if err == nil || !strings.Contains(err.Error(), "launch identity is no longer current") {
		t.Fatalf("inactive intent error = %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizeAgentSessionRelayReportAcceptsExactFinalizedRow(t *testing.T) {
	controlPath := filepath.Join(t.TempDir(), "herdr-intents.json")
	if err := os.WriteFile(controlPath, []byte(`{"schemaVersion":1,"intents":[]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	intent := testAgentSessionRelayIntent(statePath)
	report := testAgentSessionRelayReport()
	recordAgentSessionRelayPane(t, statePath, testAgentSessionRelayPane(intent))
	request := agentSessionRelayRequest{
		controlPath: controlPath, intentID: intent.ID, nonce: intent.Launch.Nonce,
		statePath: statePath,
	}

	if err := authorizeAgentSessionRelayReport(request, intent, report); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizedAgentSessionRelayRowRejectsChangedOrAmbiguousIdentity(t *testing.T) {
	intent := testAgentSessionRelayIntent("/repo/.fanout/state.json")
	report := testAgentSessionRelayReport()
	exact := testAgentSessionRelayPane(intent)
	changed := exact
	changed.HerdrTerminalID = "terminal-reused"
	if uniqueFinalizedAgentSessionRelayRow(state.Store{Panes: []state.Pane{changed}}, intent, report) {
		t.Fatal("changed finalized row authorized the relay")
	}
	if uniqueFinalizedAgentSessionRelayRow(state.Store{Panes: []state.Pane{exact, exact}}, intent, report) {
		t.Fatal("duplicate finalized rows authorized the relay")
	}

	ref := backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-other",
	}
	exact.HerdrAgentSession = &ref
	if uniqueFinalizedAgentSessionRelayRow(state.Store{Panes: []state.Pane{exact}}, intent, report) {
		t.Fatal("finalized row bound to another conversation authorized the relay")
	}
}

func testAgentSessionRelayIntent(statePath string) state.HerdrIntent {
	return state.HerdrIntent{
		ID: "issue:524:532", Kind: state.HerdrIntentWorktree, Status: state.HerdrIntentRealized,
		Parent: "524", RuntimeParent: "524", IssueNum: 532,
		WorktreePath: "/repo/worktree", WorkspaceLabel: "workspace-label",
		Resource: state.HerdrResource{
			WorkspaceID: "w1", PaneID: "w1:p1", TerminalID: "terminal-1",
			CurrentPath: "/repo/worktree", RepoKey: "/repo/.git", RepoRoot: "/repo",
		},
		Session: "owned-session", SocketPath: "/owned/herdr.sock",
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(),
		Launch: &state.HerdrLaunch{
			Nonce: strings.Repeat("a", 32), Agent: "codex", AgentName: "agent-1",
			Executable: "/opt/codex", Args: []string{"prompt"}, AgentSessionStatePath: statePath,
			LauncherReady: true, TokenIssued: true,
		},
	}
}

func testAgentSessionRelayReport() agentSessionReport {
	return agentSessionReport{
		ID: "hook", Method: "pane.report_agent_session",
		Params: agentSessionReportParams{
			PaneID: "w1:p1", Source: "herdr:codex", Agent: "codex",
			Seq: 1, AgentSessionID: "session-1",
		},
	}
}

func testAgentSessionRelayPane(intent state.HerdrIntent) state.Pane {
	return state.Pane{
		Parent: intent.Parent, RuntimeParent: intent.RuntimeParent, IssueNum: intent.IssueNum,
		Backend: backend.Herdr, PaneID: intent.Resource.PaneID, Agent: "codex",
		HerdrWorkspaceID: intent.Resource.WorkspaceID, HerdrWorkspaceLabel: intent.WorkspaceLabel,
		HerdrTerminalID: intent.Resource.TerminalID, HerdrRepoKey: intent.Resource.RepoKey,
		HerdrRepoRoot: intent.Resource.RepoRoot, HerdrAgentID: intent.Launch.AgentName,
		HerdrProcessIdentity: &backend.ProcessIdentity{
			ShellPID: 11, ForegroundProcessGroup: 12, AgentPID: 13,
		},
		HerdrSession: intent.Session, HerdrSocketPath: intent.SocketPath,
		HerdrLaunchExecutable: intent.Launch.Executable,
		HerdrLaunchArgs:       slices.Clone(intent.Launch.Args), HerdrDirectAgentLaunch: true,
		WorktreePath: intent.WorktreePath,
	}
}

func recordAgentSessionRelayPane(t *testing.T, path string, pane state.Pane) {
	t.Helper()
	locked, err := state.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.RecordPane(pane); err != nil {
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
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
