package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/peermsg"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

type codexTeamTUIOptions struct {
	config codexapp.TeamTUIConfig
	self   string
	parent string
}

func isCodexTeamTUIRequest(args []string) bool {
	return len(args) > 0 && args[0] == codexapp.TeamTUICommand
}

func cmdCodexTeamTUI(args []string, lg *log.Logger) exitcode.Code {
	opts, help, code := parseCodexTeamTUIArgs(args, lg)
	if code != exitcode.OK || help {
		return code
	}

	watchReq := peermsg.Request{Verb: "watch", SelfRaw: opts.self, Parent: opts.parent}
	if self, err := strconv.Atoi(opts.self); err == nil {
		watchReq.Self = self
	}
	watcher, watchCode := peermsg.OpenWatcher(watchReq, peermsg.DefaultDeps(), lg)
	if watchCode != exitcode.OK {
		startupErr := fmt.Errorf("open Codex team message watcher (exit %d)", watchCode)
		if err := codexapp.WriteFailedStatus(opts.config.StatusFile, startupErr); err != nil {
			lg.Err("codex team TUI: write failed startup status: %v", err)
		}
		return watchCode
	}
	defer watcher.Close()

	opts.config.Version = version
	opts.config.SetAgentState = func(state string) {
		// Pane state is best-effort display telemetry; launch readiness uses the
		// status-file handshake below.
		_ = tmuxrun.SetPaneAgentState(os.Getenv("TMUX_PANE"), state)
	}
	opts.config.FetchMessages = func() ([]codexapp.InboundMessage, error) {
		events, err := watcher.Poll()
		if err != nil {
			return nil, err
		}
		messages := make([]codexapp.InboundMessage, len(events))
		for i, event := range events {
			messages[i] = codexapp.InboundMessage{Line: event.HumanLine()}
		}
		return messages, nil
	}
	if err := codexapp.RunTeamTUI(opts.config, os.Stdout, os.Stderr); err != nil {
		lg.Err("codex team TUI: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func parseCodexTeamTUIArgs(args []string, lg *log.Logger) (opts codexTeamTUIOptions, help bool, code exitcode.Code) {
	opts.config.CodexPath = "codex"
	for i := 0; i < len(args); {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprint(lg.Stdout(), "Usage: fanout __codex-team-tui --codex <path> --prompt <prompt> --self <issue|task-id> --parent <ref> --status-file <path>\n")
			return opts, true, exitcode.OK
		case "--codex":
			if i+1 >= len(args) {
				lg.Err("--codex requires an argument")
				return codexTeamTUIOptions{}, false, exitcode.Env
			}
			opts.config.CodexPath = args[i+1]
			i += 2
		case "--prompt":
			if i+1 >= len(args) {
				lg.Err("--prompt requires an argument")
				return codexTeamTUIOptions{}, false, exitcode.Env
			}
			opts.config.Prompt = args[i+1]
			i += 2
		case "--self":
			if i+1 >= len(args) {
				lg.Err("--self requires an argument")
				return codexTeamTUIOptions{}, false, exitcode.Env
			}
			opts.self = args[i+1]
			i += 2
		case "--parent":
			if i+1 >= len(args) {
				lg.Err("--parent requires an argument")
				return codexTeamTUIOptions{}, false, exitcode.Env
			}
			opts.parent = args[i+1]
			i += 2
		case "--status-file":
			if i+1 >= len(args) {
				lg.Err("--status-file requires an argument")
				return codexTeamTUIOptions{}, false, exitcode.Env
			}
			opts.config.StatusFile = args[i+1]
			i += 2
		default:
			lg.Err("unknown codex team TUI option: %s", args[i])
			return codexTeamTUIOptions{}, false, exitcode.Invocation
		}
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "--prompt", value: opts.config.Prompt},
		{name: "--self", value: opts.self},
		{name: "--parent", value: opts.parent},
		{name: "--status-file", value: opts.config.StatusFile},
	} {
		if strings.TrimSpace(required.value) == "" {
			lg.Err("%s is required", required.name)
			return codexTeamTUIOptions{}, false, exitcode.Env
		}
	}
	return opts, false, exitcode.OK
}
