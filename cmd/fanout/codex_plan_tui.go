package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

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
	if backend.Name(getenv(telemetry.BackendEnv)) == backend.Herdr {
		return func(state string) {
			// Display telemetry is best-effort and must not affect the controller lifecycle.
			_ = stateemitter.Run([]string{state}, codexPlanEmitterEnv(getenv), herdrEmitterObserver{}, io.Discard)
		}
	}
	paneID := getenv("TMUX_PANE")
	return func(state string) {
		// A missing/replaced pane only loses display telemetry.
		_ = tmuxrun.SetPaneAgentState(paneID, state)
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
	if backend.Name(getenv(telemetry.BackendEnv)) != backend.Herdr {
		paneID := getenv("TMUX_PANE")
		return func() (string, error) {
			return tmuxrun.CapturePlanSource(paneID, codexPlanTUIScreenCaptureLines)
		}
	}
	capture, err := newHerdrCodexPlanCapture(getenv)
	if err != nil {
		return func() (string, error) { return "", err }
	}
	return capture
}

func newHerdrCodexPlanCapture(getenv func(string) string) (func() (string, error), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve Codex Plan controller cwd: %w", err)
	}
	repo, err := worktree.ResolveRepoIdentity(context.Background(), cwd)
	if err != nil {
		return nil, err
	}
	base := herdrrun.OwnedPaneIdentity{
		Ref: backend.PaneRef{
			Backend:   backend.Herdr,
			Workspace: getenv(telemetry.WorkspaceIDEnv), Pane: getenv(telemetry.PaneIDEnv),
		},
		SessionID: getenv(telemetry.SessionEnv), SocketPath: getenv(telemetry.SocketPathEnv),
		WorkspaceLabel: getenv(telemetry.WorkspaceLabelEnv), TerminalID: getenv(telemetry.TerminalIDEnv),
		RepoKey: repo.RepoKey, WorktreePath: cwd, CurrentPath: cwd,
		AgentID: getenv(telemetry.AgentIDEnv),
	}
	owned, err := herdrrun.OpenOwned(context.Background(), herdrrun.OwnedOptions{GitCommonDir: repo.RepoKey})
	if err != nil {
		return nil, err
	}
	return func() (string, error) {
		return captureHerdrCodexPlanScreen(owned, base)
	}, nil
}

func captureHerdrCodexPlanScreen(owned *herdrrun.OwnedSession, base herdrrun.OwnedPaneIdentity) (string, error) {
	panes, err := owned.LivePanes(context.Background())
	if err != nil {
		return "", err
	}
	target, err := herdrCodexPlanCaptureTarget(base, panes)
	if err != nil {
		return "", err
	}
	bound, err := owned.Backend().BindOwnedTarget(target)
	if err != nil {
		return "", err
	}
	return bound.Read(target.Ref, codexPlanTUIScreenCaptureLines)
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
		return herdrrun.OwnedPaneIdentity{}, fmt.Errorf("Codex Plan controller pane does not match exactly one live Herdr target")
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
