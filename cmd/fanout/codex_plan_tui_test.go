package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

func TestParseCodexPlanTUIArgsAllowsResumeThreadWithoutPrompt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cfg, help, code := parseCodexPlanTUIArgs([]string{
		"--codex", "/bin/codex",
		"--resume-thread-id", "thread-1",
		"--resume-session-id", "session-1",
		"--status-file", "/tmp/status.json",
	}, log.NewWith(&stdout, &stderr, false))

	if help {
		t.Fatalf("parseCodexPlanTUIArgs help = true, want false")
	}

	if code != exitcode.OK {
		t.Fatalf("parseCodexPlanTUIArgs code = %v, stderr = %q", code, stderr.String())
	}
	if cfg.ResumeThreadID != "thread-1" || cfg.ResumeSessionID != "session-1" || cfg.Prompt != "" {
		t.Fatalf("cfg = %+v, want resume thread without prompt", cfg)
	}
}

func TestParseCodexPlanTUIArgsStillRequiresPromptOrResumeThread(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, _, code := parseCodexPlanTUIArgs([]string{
		"--codex", "/bin/codex",
		"--status-file", "/tmp/status.json",
	}, log.NewWith(&stdout, &stderr, false))

	if code != exitcode.Env {
		t.Fatalf("parseCodexPlanTUIArgs code = %v, want Env", code)
	}
	if !strings.Contains(stderr.String(), "--prompt is required") {
		t.Fatalf("stderr = %q, want prompt error", stderr.String())
	}
}

func TestCodexPlanEmitterEnvUsesPinnedEmitterStatePath(t *testing.T) {
	env := map[string]string{
		telemetry.StatePathEnv:   "/caller/.fanout/state.json",
		telemetry.EmitterPathEnv: "/owner/.fanout/state.json",
		telemetry.RowKeyEnv:      "issue:3:524:554",
	}
	getenv := codexPlanEmitterEnv(func(name string) string { return env[name] })
	if got := getenv(telemetry.StatePathEnv); got != env[telemetry.EmitterPathEnv] {
		t.Fatalf("emitter state path = %q", got)
	}
	if got := getenv(telemetry.RowKeyEnv); got != env[telemetry.RowKeyEnv] {
		t.Fatalf("emitter row key = %q", got)
	}
}

func TestBestEffortStateSinkDoesNotBlockController(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	sink := newBestEffortStateSink(func(state string) {
		select {
		case started <- state:
		default:
		}
		if state == "working" {
			<-release
		}
	})
	sink("working")
	if got := <-started; got != "working" {
		t.Fatalf("first state = %q", got)
	}
	done := make(chan struct{})
	go func() {
		for range codexPlanTelemetryQueueSize + 2 {
			sink("plan")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("best-effort state sink blocked the controller")
	}
	close(release)
	select {
	case got := <-started:
		if got != "plan" {
			t.Fatalf("queued state = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("queued state was not emitted")
	}
}

func TestHerdrCodexPlanCaptureTargetRequiresExactLiveIdentity(t *testing.T) {
	session := backend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-554"}
	base := herdrrun.OwnedPaneIdentity{
		Ref:       backend.PaneRef{Backend: backend.Herdr, Workspace: "workspace-1", Pane: "workspace-1:pane-1"},
		SessionID: "fanout-owned", SocketPath: "/tmp/herdr.sock", TerminalID: "terminal-1",
		RepoKey: "/repo/.git", WorktreePath: "/repo/worktree", CurrentPath: "/repo/worktree",
		AgentID: "fanout-agent",
	}
	live := backend.LivePane{
		Ref: base.Ref, SessionID: base.SessionID, SocketPath: base.SocketPath,
		TerminalID: base.TerminalID, RepoKey: base.RepoKey,
		WorktreePath: base.WorktreePath, CurrentPath: base.CurrentPath,
		AgentPresent: true, AgentProvider: "codex", AgentID: "fanout-agent", AgentSession: &session,
	}
	got, err := herdrCodexPlanCaptureTarget(base, []backend.LivePane{live})
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != live.AgentID || got.AgentSession != live.AgentSession {
		t.Fatalf("capture target = %+v", got)
	}

	wrong := live
	wrong.CurrentPath = "/repo/other"
	for name, panes := range map[string][]backend.LivePane{
		"missing": nil, "mismatch": {wrong}, "duplicate": {live, live},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := herdrCodexPlanCaptureTarget(base, panes); err == nil {
				t.Fatal("capture target accepted ambiguous or mismatched live identity")
			}
		})
	}
}
