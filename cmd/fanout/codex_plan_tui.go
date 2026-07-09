package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

func isCodexPlanTUIRequest(args []string) bool {
	return len(args) > 0 && args[0] == codexapp.PlanTUICommand
}

func cmdCodexPlanTUI(args []string, lg *log.Logger) exitcode.Code {
	cfg, help, code := parseCodexPlanTUIArgs(args, lg)
	if code != exitcode.OK || help {
		return code
	}
	cfg.Version = version
	cfg.SetAgentState = func(state string) {
		_ = tmuxrun.SetPaneAgentState(os.Getenv("TMUX_PANE"), state)
	}
	cfg.SendPlanPrompt = func(prompt string) error {
		return tmuxrun.SendLiteralLine(os.Getenv("TMUX_PANE"), prompt)
	}
	if err := codexapp.RunPlanTUI(cfg, os.Stdout, os.Stderr); err != nil {
		lg.Err("codex plan mode TUI: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
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
