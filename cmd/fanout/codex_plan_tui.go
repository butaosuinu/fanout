package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/butaosuinu/fanout/internal/app/stateemitter"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const codexPlanTUIScreenCaptureLines = 2000

const codexPlanTelemetryQueueSize = 8

const herdrCodexPlanCaptureTimeout = time.Second

var errHerdrCodexPlanCapturePending = errors.New("herdr Codex Plan screen capture is pending")

func isCodexPlanTUIRequest(args []string) bool {
	return len(args) > 0 && args[0] == codexapp.PlanTUICommand
}

func cmdCodexPlanTUI(args []string, lg *log.Logger) exitcode.Code {
	cfg, help, code := parseCodexPlanTUIArgs(args, lg)
	if code != exitcode.OK || help {
		return code
	}
	cfg.Version = version
	cfg.CapturePlanScreen = codexPlanScreenCapture(os.Getenv)
	cfg.SetAgentState = codexPlanStateSink(os.Getenv)
	if err := codexapp.RunPlanTUI(cfg, os.Stdout, os.Stderr); err != nil {
		if code, ok := codexapp.SignalErrorExitCode(err); ok {
			return exitcode.Code(code)
		}
		lg.Err("codex plan mode TUI: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func codexPlanStateSink(getenv func(string) string) func(string) {
	if codexPlanRuntimeBackend(getenv) == backend.Herdr {
		return newBestEffortStateSink(func(state string) {
			// Display telemetry is best-effort and must not affect the controller lifecycle.
			_ = stateemitter.Run([]string{state}, codexPlanEmitterEnv(getenv), herdrEmitterObserver{}, io.Discard)
		})
	}
	paneID := getenv("TMUX_PANE")
	return func(state string) {
		// A missing/replaced pane only loses display telemetry.
		_ = tmuxrun.SetPaneAgentState(paneID, state)
	}
}

func codexPlanRuntimeBackend(getenv func(string) string) backend.Name {
	if strings.TrimSpace(getenv("TMUX_PANE")) != "" {
		return backend.Tmux
	}
	return backend.Name(getenv(telemetry.BackendEnv))
}

func newBestEffortStateSink(emit func(string)) func(string) {
	queue := make(chan string, codexPlanTelemetryQueueSize)
	go func() {
		for state := range queue {
			emit(state)
		}
	}()
	return func(state string) {
		select {
		case queue <- state:
		default:
		}
	}
}

func codexPlanEmitterEnv(getenv func(string) string) func(string) string {
	return func(name string) string {
		if name == telemetry.StatePathEnv {
			return getenv(telemetry.EmitterPathEnv)
		}
		return getenv(name)
	}
}

func codexPlanScreenCapture(getenv func(string) string) func() (string, error) {
	if codexPlanRuntimeBackend(getenv) != backend.Herdr {
		paneID := getenv("TMUX_PANE")
		return func() (string, error) {
			return tmuxrun.CapturePlanSource(paneID, codexPlanTUIScreenCaptureLines)
		}
	}
	return newHerdrCodexPlanCapture(getenv)
}

func newHerdrCodexPlanCapture(getenv func(string) string) func() (string, error) {
	base := herdrrun.OwnedPaneIdentity{
		Ref: backend.PaneRef{
			Backend:   backend.Herdr,
			Workspace: getenv(telemetry.WorkspaceIDEnv), Pane: getenv(telemetry.PaneIDEnv),
		},
		SessionID: getenv(telemetry.SessionEnv), SocketPath: getenv(telemetry.SocketPathEnv),
		WorkspaceLabel: getenv(telemetry.WorkspaceLabelEnv), TerminalID: getenv(telemetry.TerminalIDEnv),
		AgentID: getenv(telemetry.AgentIDEnv),
	}
	var owned *herdrrun.OwnedSession
	return newBestEffortScreenCapture(herdrCodexPlanCaptureTimeout, func(ctx context.Context) (string, error) {
		if owned == nil {
			var err error
			owned, base, err = openHerdrCodexPlanCapture(ctx, base)
			if err != nil {
				return "", err
			}
		}
		return captureHerdrCodexPlanScreen(ctx, owned, base)
	})
}

func openHerdrCodexPlanCapture(ctx context.Context, base herdrrun.OwnedPaneIdentity) (*herdrrun.OwnedSession, herdrrun.OwnedPaneIdentity, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, base, fmt.Errorf("resolve Codex Plan controller cwd: %w", err)
	}
	repo, err := worktree.ResolveRepoIdentity(ctx, cwd)
	if err != nil {
		return nil, base, err
	}
	base.RepoKey, base.WorktreePath, base.CurrentPath = repo.RepoKey, cwd, cwd
	owned, err := herdrrun.OpenOwned(ctx, herdrrun.OwnedOptions{GitCommonDir: repo.RepoKey})
	return owned, base, err
}

func captureHerdrCodexPlanScreen(ctx context.Context, owned *herdrrun.OwnedSession, base herdrrun.OwnedPaneIdentity) (string, error) {
	panes, err := owned.LivePanes(ctx)
	if err != nil {
		return "", err
	}
	target, err := herdrCodexPlanCaptureTarget(base, panes)
	if err != nil {
		return "", err
	}
	return owned.ReadOwnedPane(ctx, target, 0)
}

type bestEffortScreenCapture struct {
	request chan struct{}
	result  atomic.Pointer[bestEffortScreenCaptureResult]
}

type bestEffortScreenCaptureResult struct {
	screen string
	err    error
}

func newBestEffortScreenCapture(timeout time.Duration, read func(context.Context) (string, error)) func() (string, error) {
	worker := &bestEffortScreenCapture{request: make(chan struct{}, 1)}
	go worker.run(timeout, read)
	return worker.capture
}

func (w *bestEffortScreenCapture) run(timeout time.Duration, read func(context.Context) (string, error)) {
	for range w.request {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		screen, err := read(ctx)
		cancel()
		w.result.Store(&bestEffortScreenCaptureResult{screen: screen, err: err})
	}
}

func (w *bestEffortScreenCapture) capture() (string, error) {
	select {
	case w.request <- struct{}{}:
	default:
	}
	result := w.result.Load()
	if result == nil {
		return "", errHerdrCodexPlanCapturePending
	}
	return result.screen, result.err
}

func herdrCodexPlanCaptureTarget(base herdrrun.OwnedPaneIdentity, panes []backend.LivePane) (herdrrun.OwnedPaneIdentity, error) {
	matches := make([]backend.LivePane, 0, 1)
	for _, pane := range panes {
		identity := []bool{
			pane.Ref == base.Ref, pane.SessionID == base.SessionID, pane.SocketPath == base.SocketPath,
			pane.TerminalID == base.TerminalID, pane.RepoKey == base.RepoKey,
			pane.WorktreePath == base.WorktreePath, pane.CurrentPath == base.CurrentPath,
			pane.AgentPresent, pane.AgentProvider == "codex", pane.AgentID == base.AgentID,
		}
		if !slices.Contains(identity, false) {
			matches = append(matches, pane)
		}
	}
	if len(matches) != 1 {
		return herdrrun.OwnedPaneIdentity{}, fmt.Errorf("codex Plan controller pane does not match exactly one live Herdr target")
	}
	base.AgentSession = matches[0].AgentSession
	return base, nil
}

func parseCodexPlanTUIArgs(args []string, lg *log.Logger) (cfg codexapp.TUIConfig, help bool, code exitcode.Code) {
	cfg = codexapp.TUIConfig{CodexPath: "codex"}
	for i := 0; i < len(args); {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprint(lg.Stdout(), "Usage: fanout __codex-plan-tui --codex <path> (--prompt <prompt>|--resume-thread-id <thread>) --status-file <path>\n")
			return cfg, true, exitcode.OK
		case "--codex":
			if i+1 >= len(args) {
				lg.Err("--codex requires an argument")
				return codexapp.TUIConfig{}, false, exitcode.Env
			}
			cfg.CodexPath = args[i+1]
			i += 2
		case "--prompt":
			if i+1 >= len(args) {
				lg.Err("--prompt requires an argument")
				return codexapp.TUIConfig{}, false, exitcode.Env
			}
			cfg.Prompt = args[i+1]
			i += 2
		case "--status-file":
			if i+1 >= len(args) {
				lg.Err("--status-file requires an argument")
				return codexapp.TUIConfig{}, false, exitcode.Env
			}
			cfg.StatusFile = args[i+1]
			i += 2
		case "--resume-thread-id":
			if i+1 >= len(args) {
				lg.Err("--resume-thread-id requires an argument")
				return codexapp.TUIConfig{}, false, exitcode.Env
			}
			cfg.ResumeThreadID = args[i+1]
			i += 2
		case "--resume-session-id":
			if i+1 >= len(args) {
				lg.Err("--resume-session-id requires an argument")
				return codexapp.TUIConfig{}, false, exitcode.Env
			}
			cfg.ResumeSessionID = args[i+1]
			i += 2
		default:
			lg.Err("unknown codex plan TUI option: %s", args[i])
			return codexapp.TUIConfig{}, false, exitcode.Invocation
		}
	}
	if strings.TrimSpace(cfg.Prompt) == "" && strings.TrimSpace(cfg.ResumeThreadID) == "" {
		lg.Err("--prompt is required")
		return codexapp.TUIConfig{}, false, exitcode.Env
	}
	if strings.TrimSpace(cfg.StatusFile) == "" {
		lg.Err("--status-file is required")
		return codexapp.TUIConfig{}, false, exitcode.Env
	}
	return cfg, false, exitcode.OK
}
