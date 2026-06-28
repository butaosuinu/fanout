package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/state"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
)

type worktreeActionFlags struct {
	paneID string
	action string
	agent  string
	prompt string
}

func isWorktreeActionRequest(args []string) bool {
	return len(args) > 0 && args[0] == "__worktree-action"
}

func cmdWorktreeAction(args []string, lg *log.Logger, commandName string) exitcode.Code {
	flags, code := parseWorktreeActionFlags(args, lg)
	if code != exitcode.OK {
		return code
	}
	projectRoot, err := resolveDisplayProjectRoot()
	if err != nil {
		lg.Err("worktree action: %v", err)
		return exitcode.Env
	}
	source, err := findRecordedPaneByID(projectRoot, flags.paneID)
	if err != nil {
		lg.Err("worktree action: %v", err)
		return exitcode.Env
	}
	target := attachTargetFromStatePane(source)
	if strings.TrimSpace(target.TargetPath) == "" {
		lg.Err("worktree action: pane %s has no recorded worktree", flags.paneID)
		return exitcode.Invocation
	}

	reader := bufio.NewReader(os.Stdin)
	action := strings.TrimSpace(flags.action)
	if action == "" {
		action = promptWorktreeAction(reader, sourceLabelForStatePane(source))
	}
	switch action {
	case "1", "attach", "agent":
		return runWorktreeAttachAction(projectRoot, flags, reader, target, commandName, hooks.LoadUserConfig(lg), lg)
	case "2", "shell", "terminal":
		if err := launchShellPane(projectRoot, flags.paneID, fanouttui.ShellLaunchRequest{
			TargetPath: target.TargetPath,
			Source:     target.SourceLabel,
		}); err != nil {
			lg.Err("worktree action: %v", err)
			return exitcode.Env
		}
		lg.Ok("opened terminal for %s", target.SourceLabel)
		return exitcode.OK
	case "", "q", "quit", "cancel":
		lg.Info("worktree action canceled")
		return exitcode.OK
	default:
		lg.Err("worktree action: unknown action %q", action)
		return exitcode.Invocation
	}
}

func parseWorktreeActionFlags(args []string, lg *log.Logger) (worktreeActionFlags, exitcode.Code) {
	var flags worktreeActionFlags
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--pane":
			if i+1 >= len(args) {
				lg.Err("worktree action: --pane requires an argument")
				return flags, exitcode.Invocation
			}
			i++
			flags.paneID = strings.TrimSpace(args[i])
		case "--action":
			if i+1 >= len(args) {
				lg.Err("worktree action: --action requires an argument")
				return flags, exitcode.Invocation
			}
			i++
			flags.action = strings.TrimSpace(args[i])
		case "--agent":
			if i+1 >= len(args) {
				lg.Err("worktree action: --agent requires an argument")
				return flags, exitcode.Invocation
			}
			i++
			flags.agent = strings.TrimSpace(args[i])
		case "--prompt":
			if i+1 >= len(args) {
				lg.Err("worktree action: --prompt requires an argument")
				return flags, exitcode.Invocation
			}
			i++
			flags.prompt = strings.TrimSpace(args[i])
		default:
			lg.Err("worktree action: unknown option %s", args[i])
			return flags, exitcode.Invocation
		}
	}
	if flags.paneID == "" {
		flags.paneID = strings.TrimSpace(os.Getenv("TMUX_PANE"))
	}
	if flags.paneID == "" {
		lg.Err("worktree action: --pane is required")
		return flags, exitcode.Invocation
	}
	return flags, exitcode.OK
}

func findRecordedPaneByID(projectRoot, paneID string) (state.Pane, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return state.Pane{}, fmt.Errorf("pane id is required")
	}
	store, err := sessionview.MergedStateLoader(projectRoot)()
	if err != nil {
		return state.Pane{}, err
	}
	for _, pane := range store.Panes {
		if pane.PaneID == paneID {
			return pane, nil
		}
	}
	return state.Pane{}, fmt.Errorf("pane %s is not recorded in fanout state", paneID)
}

func promptWorktreeAction(reader *bufio.Reader, sourceLabel string) string {
	fmt.Printf("fanout worktree actions for %s\n", sourceLabel)
	fmt.Println("1. Attach agent to this worktree")
	fmt.Println("2. Open shell in this worktree")
	fmt.Println("q. Cancel")
	fmt.Print("> ")
	line, _ := reader.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(line))
}

func runWorktreeAttachAction(projectRoot string, flags worktreeActionFlags, reader *bufio.Reader, target fanouttui.AttachTarget, commandName string, hookConfig hooks.Config, lg *log.Logger) exitcode.Code {
	agentName := strings.TrimSpace(flags.agent)
	if agentName == "" {
		agentName = defaultTUIAgent()
		fmt.Printf("agent [%s]: ", agentName)
		if line, _ := reader.ReadString('\n'); strings.TrimSpace(line) != "" {
			agentName = strings.TrimSpace(line)
		}
	}
	prompt := strings.TrimSpace(flags.prompt)
	if prompt == "" {
		fmt.Print("prompt: ")
		line, _ := reader.ReadString('\n')
		prompt = strings.TrimSpace(line)
	}
	if prompt == "" {
		lg.Err("worktree action: prompt is required")
		return exitcode.Invocation
	}
	notice, err := launchAttachedAgent(projectRoot, flags.paneID, commandName, hookConfig, fanouttui.AttachLaunchRequest{
		Prompt: prompt,
		Agents: []string{agentName},
		Target: target,
	})
	if err != nil {
		lg.Err("worktree action: %v", err)
		return exitcode.Env
	}
	if strings.TrimSpace(notice) != "" {
		lg.Info("%s", notice)
	}
	lg.Ok("attached %s to %s", agentName, target.SourceLabel)
	return exitcode.OK
}

func attachTargetFromStatePane(pane state.Pane) fanouttui.AttachTarget {
	return fanouttui.AttachTarget{
		TargetPath:       pane.WorktreePath,
		SourceParent:     pane.Parent,
		SourceIssueNum:   pane.IssueNum,
		SourceTaskID:     pane.TaskID,
		SourceBranchName: pane.BranchName,
		SourceLabel:      sourceLabelForStatePane(pane),
	}
}

func sourceLabelForStatePane(pane state.Pane) string {
	if strings.TrimSpace(pane.TaskID) != "" {
		return pane.TaskID
	}
	if pane.IssueNum > 0 {
		return fmt.Sprintf("#%d", pane.IssueNum)
	}
	if label := strings.TrimSpace(pane.DisplayName); label != "" {
		return label
	}
	if slug := strings.TrimSpace(pane.Slug); slug != "" {
		return slug
	}
	if pane.IsShell() {
		return "shell"
	}
	return "pane"
}
