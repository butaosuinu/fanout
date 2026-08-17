package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
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

func TestCodexPlanRuntimeBackendPrefersTmuxPaneOverInheritedEmitter(t *testing.T) {
	env := map[string]string{
		"TMUX_PANE":          "%7",
		telemetry.BackendEnv: string(backend.Herdr),
	}
	getenv := func(name string) string { return env[name] }

	if got := codexPlanRuntimeBackend(getenv); got != backend.Tmux {
		t.Fatalf("Codex Plan runtime backend = %q, want tmux", got)
	}
	delete(env, "TMUX_PANE")
	if got := codexPlanRuntimeBackend(getenv); got != backend.Herdr {
		t.Fatalf("Codex Plan runtime backend without tmux pane = %q, want herdr", got)
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
	base := backend.OwnedPaneIdentity{
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
	got, err := managedCodexPlanCaptureTarget(base, []backend.LivePane{live})
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
			if _, err := managedCodexPlanCaptureTarget(base, panes); err == nil {
				t.Fatal("capture target accepted ambiguous or mismatched live identity")
			}
		})
	}
}

func TestBestEffortScreenCaptureDoesNotBlockController(t *testing.T) {
	started := make(chan struct{}, 2)
	finished := make(chan struct{}, 2)
	capture := newBestEffortScreenCapture(25*time.Millisecond, func(ctx context.Context) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		finished <- struct{}{}
		return "", ctx.Err()
	})

	if _, err := capture(); !errors.Is(err, errManagedCodexPlanCapturePending) {
		t.Fatalf("initial capture error = %v, want pending", err)
	}
	<-started
	before := time.Now()
	if _, err := capture(); !errors.Is(err, errManagedCodexPlanCapturePending) {
		t.Fatalf("busy capture error = %v, want cached pending", err)
	}
	if elapsed := time.Since(before); elapsed > 100*time.Millisecond {
		t.Fatalf("busy capture blocked controller for %v", elapsed)
	}
	<-finished
	if _, err := capture(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("completed capture error = %v, want deadline", err)
	}
}

func TestBestEffortScreenCaptureReturnsCacheDuringRefresh(t *testing.T) {
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	calls := 0
	capture := newBestEffortScreenCapture(time.Second, func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "approval viewport", nil
		}
		if calls == 2 {
			close(secondStarted)
			<-releaseSecond
		}
		return "working viewport", nil
	})

	if _, err := capture(); !errors.Is(err, errManagedCodexPlanCapturePending) {
		t.Fatalf("initial capture error = %v, want pending", err)
	}
	deadline := time.After(time.Second)
	for {
		screen, err := capture()
		if err == nil && screen == "approval viewport" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first screen was not cached")
		case <-time.After(time.Millisecond):
		}
	}
	<-secondStarted
	before := time.Now()
	screen, err := capture()
	if err != nil || screen != "approval viewport" {
		t.Fatalf("refresh cache = %q, %v", screen, err)
	}
	if elapsed := time.Since(before); elapsed > 100*time.Millisecond {
		t.Fatalf("cached capture blocked controller for %v", elapsed)
	}
	close(releaseSecond)
}
