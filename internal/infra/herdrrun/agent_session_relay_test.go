package herdrrun

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestValidateAgentSessionReportRequiresRestrictedMethodAndIdentity(t *testing.T) {
	intent := state.LaunchIntent{
		Kind: state.IntentWorktree, Resource: state.RuntimeResource{PaneID: "w1:p1"},
	}
	report := validAgentSessionReport()
	if err := validateAgentSessionReport(report, intent, false); err != nil {
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
			if err := validateAgentSessionReport(changed, intent, false); err == nil {
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
	if err := validateAgentSessionReport(report, intent, true); err != nil {
		t.Fatal(err)
	}
	report.Params.AgentSessionID = "session-2"
	if err := validateAgentSessionReport(report, intent, true); err == nil {
		t.Fatal("resume relay accepted a different Codex conversation")
	}
	if err := validateAgentSessionReport(report, intent, false); err != nil {
		t.Fatalf("established resume relay rejected a later Codex conversation: %v", err)
	}
	report.Params.AgentSessionID = "session-1"
	intent.ResumeAgentSession = nil
	if err := validateAgentSessionReport(report, intent, true); err == nil {
		t.Fatal("resume relay accepted a missing saved conversation")
	}
}

func TestAgentSessionRelaySurvivesFinalizationAndStopsWithWorkload(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	socketDir := t.TempDir()
	ownedPath := filepath.Join(socketDir, "owned.sock")
	owned := listenTestUnix(t, ownedPath)
	defer func() {
		_ = owned.Close() // The test owns this listener and checks request results instead.
	}()
	forwarded := make(chan agentSessionReport, 2)
	go serveTestAgentSessionReports(owned, forwarded, 2)

	relayPath := filepath.Join(socketDir, "relay.sock")
	relay := listenTestUnix(t, relayPath)
	lifetimeRead, lifetimeWrite, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	defer func() {
		_ = lifetimeRead.Close() // The relay result is authoritative in this test.
	}()
	intent := state.LaunchIntent{
		Kind: state.IntentResume, SocketPath: ownedPath,
		// The serve process has already authenticated the active intent. Its old
		// operation deadline represents the normal post-launch finalized state.
		ExpiresUnixMS: time.Now().Add(-time.Minute).UnixMilli(),
		Resource:      state.RuntimeResource{PaneID: "w1:p1"},
		ResumeAgentSession: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-1",
		},
	}
	done := make(chan error, 1)
	go func() { done <- serveAgentSessionReports(intent, relay, lifetimeRead) }()

	mismatch := validAgentSessionReport()
	mismatch.Params.AgentSessionID = "session-2"
	if err := sendTestAgentSessionReport(relayPath, mismatch); err == nil {
		t.Fatal("resume relay forwarded a different initial conversation")
	}
	if err := sendTestAgentSessionReport(relayPath, validAgentSessionReport()); err != nil {
		t.Fatal(err)
	}
	if err := sendTestAgentSessionReport(relayPath, mismatch); err != nil {
		t.Fatalf("resume relay rejected /new after establishing the saved conversation: %v", err)
	}
	if first, second := <-forwarded, <-forwarded; first.Params.AgentSessionID != "session-1" ||
		second.Params.AgentSessionID != "session-2" {
		t.Fatalf("forwarded sessions = %q, %q", first.Params.AgentSessionID, second.Params.AgentSessionID)
	}
	pending, dialErr := net.DialUnix("unix", nil, &net.UnixAddr{Name: relayPath, Net: "unix"})
	if dialErr != nil {
		t.Fatal(dialErr)
	}
	if err := pending.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := lifetimeWrite.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay remained alive after the workload lifetime closed")
	}
	if _, err := pending.Write([]byte(`{}` + "\n")); err == nil {
		_, err = bufio.NewReader(pending).ReadByte()
		if err == nil {
			t.Fatal("relay kept an accepted connection open after the workload lifetime closed")
		}
	}
	_ = pending.Close() // The lifetime assertion is complete.
}

func TestAgentSessionRelayProcessLifetimeEndsBeforeDescendant(t *testing.T) {
	cmd := exec.Command("sh", "-c", "read ready; sleep 30 >/dev/null 2>&1 & echo $!")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := 0
	t.Cleanup(func() {
		_ = stdin.Close()      // Unblock the test workload if the assertion failed before release.
		_ = cmd.Process.Kill() // The successful path has already reaped the exact workload.
		_ = cmd.Wait()         // The process may already have been reaped by the successful path.
		if childPID > 1 {
			if killErr := syscall.Kill(childPID, syscall.SIGKILL); killErr != nil &&
				!errors.Is(killErr, syscall.ESRCH) {
				t.Errorf("stop descendant: %v", killErr)
			}
		}
	})
	lifetime, err := newAgentSessionRelayProcessLifetime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = lifetime.Close() // The wait result is authoritative.
	}()
	if _, err := stdin.Write([]byte("ready\n")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close() // The exact workload has all input needed to exit.
	childLine, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	childPID, err = strconv.Atoi(strings.TrimSpace(childLine))
	if err != nil || childPID <= 1 {
		t.Fatalf("descendant pid = %q: %v", childLine, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, waitErr := io.Copy(io.Discard, lifetime)
		done <- waitErr
	}()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			t.Fatal(waitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("relay lifetime followed the workload descendant")
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("descendant exited with the exact workload: %v", err)
	}
}

func TestWaitForAgentSessionRelayReadyRequiresExactACK(t *testing.T) {
	tests := []struct {
		name string
		ack  string
		want bool
	}{
		{name: "ready", ack: agentSessionRelayReadyACK, want: true},
		{name: "wrong", ack: "X"},
		{name: "missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			read, write, pipeErr := os.Pipe()
			if pipeErr != nil {
				t.Fatal(pipeErr)
			}
			if test.ack != "" {
				if _, writeErr := write.Write([]byte(test.ack)); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if closeErr := write.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			readyErr := waitForAgentSessionRelayReady(read)
			_ = read.Close() // The handshake result is authoritative.
			if (readyErr == nil) != test.want {
				t.Fatalf("waitForAgentSessionRelayReady() error = %v, want success %t", readyErr, test.want)
			}
		})
	}
}

func TestAcceptedAgentSessionReportResponseRequiresSuccessForSameRequest(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     bool
	}{
		{name: "success", response: `{"id":"hook","result":{"type":"ok"}}`, want: true},
		{name: "other request", response: `{"id":"other","result":{"type":"ok"}}`},
		{name: "error", response: `{"id":"hook","error":{"message":"rejected"}}`},
		{name: "null result", response: `{"id":"hook","result":null}`},
		{name: "invalid envelope", response: `{"id":"hook","result":{}} trailing`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := acceptedAgentSessionReportResponse([]byte(test.response), "hook"); got != test.want {
				t.Fatalf("acceptedAgentSessionReportResponse() = %t, want %t", got, test.want)
			}
		})
	}
}

func listenTestUnix(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func serveTestAgentSessionReports(
	listener *net.UnixListener,
	forwarded chan<- agentSessionReport,
	count int,
) {
	for range count {
		connection, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		report, err := readAgentSessionReport(connection)
		if err == nil {
			forwarded <- report
			response, marshalErr := json.Marshal(agentSessionReportResponse{ID: report.ID, Result: []byte(`{}`)})
			if marshalErr == nil {
				_, _ = connection.Write(append(response, '\n')) // The client assertion checks delivery.
			}
		}
		_ = connection.Close()
	}
}

func sendTestAgentSessionReport(path string, report agentSessionReport) error {
	connection, dialErr := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if dialErr != nil {
		return dialErr
	}
	defer func() {
		_ = connection.Close() // The response read is authoritative.
	}()
	if deadlineErr := connection.SetDeadline(time.Now().Add(time.Second)); deadlineErr != nil {
		return deadlineErr
	}
	payload, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		return marshalErr
	}
	if _, writeErr := connection.Write(append(payload, '\n')); writeErr != nil {
		return writeErr
	}
	_, readErr := bufio.NewReader(connection).ReadBytes('\n')
	return readErr
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
