package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
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

var (
	worktreeActionLivePanes = tmuxrun.ListLivePanes
	worktreeActionListRoots = worktree.ListRoots
)

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
	ownerRoot := paneOwnerProjectRoot(projectRoot, source)

	reader := bufio.NewReader(os.Stdin)
	action := strings.TrimSpace(flags.action)
	if action == "" {
		action = promptWorktreeAction(reader, sourceLabelForStatePane(source))
	}
	switch action {
	case "1", "attach", "agent":
		return runWorktreeAttachAction(ownerRoot, flags, reader, target, commandName, hooks.LoadUserConfig(lg), lg)
	case "2", "shell", "terminal":
		if err := launchShellPane(ownerRoot, flags.paneID, fanouttui.ShellLaunchRequest{
			TargetPath:        target.TargetPath,
			SourceProjectRoot: ownerRoot,
			Source:            target.SourceLabel,
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
	panes, err := rawActionStatePanes(projectRoot)
	if err != nil {
		return state.Pane{}, err
	}
	live, err := worktreeActionLivePanes()
	if err != nil {
		return state.Pane{}, fmt.Errorf("list live tmux panes: %w", err)
	}
	liveByID := map[string]tmuxrun.LivePane{}
	for _, pane := range live {
		liveByID[pane.ID] = pane
	}
	var candidates []state.Pane
	for _, pane := range panes {
		if pane.PaneID != paneID {
			continue
		}
		candidates = append(candidates, pane)
	}
	if len(candidates) == 0 {
		return state.Pane{}, fmt.Errorf("pane %s is not recorded in fanout state", paneID)
	}
	cur, ok := liveByID[paneID]
	if !ok {
		return state.Pane{}, fmt.Errorf("pane %s no longer matches its recorded fanout worktree: pane is not live", paneID)
	}
	projectRootFallback := len(candidates) == 1
	mismatch := ""
	for _, pane := range candidates {
		matches, reason := recordedPaneMatchesLive(pane, cur, projectRootFallback)
		if matches {
			return pane, nil
		}
		mismatch = reason
	}
	return state.Pane{}, fmt.Errorf("pane %s no longer matches its recorded fanout worktree: %s", paneID, mismatch)
}

func rawActionStatePanes(projectRoot string) ([]state.Pane, error) {
	roots, _ := worktreeActionListRoots(projectRoot)
	seen := map[string]bool{}
	var panes []state.Pane
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		st, err := state.Load(state.Path(root))
		if err != nil {
			if root == projectRoot {
				return nil, err
			}
			continue
		}
		for _, pane := range st.Panes {
			pane.SourceProjectRoot = root
			if len(pane.SourceProjectRoots) == 0 {
				pane.SourceProjectRoots = []string{root}
			}
			panes = append(panes, pane)
		}
	}
	return panes, nil
}

func recordedPaneMatchesLive(pane state.Pane, live tmuxrun.LivePane, projectRootFallback bool) (bool, string) {
	// Any row recorded with a ShellKey (shell terminals, the plan fan-out
	// coordinator at the repo root) is identified by @fanout_shell_key: its
	// WorktreePath contains every fanout pane, so the path checks below cannot
	// detect a reused pane id.
	if pane.IsShell() || strings.TrimSpace(pane.ShellKey) != "" {
		shellKey := strings.TrimSpace(pane.ShellKey)
		if shellKey == "" {
			return false, "recorded shell pane has no shell identity key"
		}
		if live.ShellKey != shellKey {
			return false, "live pane identity changed"
		}
		return true, ""
	}
	worktreePath := strings.TrimSpace(pane.WorktreePath)
	if worktreePath == "" {
		return false, "recorded pane has no worktree path"
	}
	if liveWorktree := strings.TrimSpace(live.WorktreePath); liveWorktree != "" {
		if !samePath(worktreePath, liveWorktree) {
			return false, fmt.Sprintf("live worktree %q does not match recorded worktree %q", live.WorktreePath, pane.WorktreePath)
		}
		return true, ""
	}
	if !pathWithinRoot(worktreePath, live.CurrentPath) {
		if projectRootFallback && projectRootMatches(pane, live.ProjectRoot) {
			return true, ""
		}
		if !projectRootFallback && projectRootMatches(pane, live.ProjectRoot) {
			return false, "worktree identity is ambiguous without a live worktree path"
		}
		return false, fmt.Sprintf("live cwd %q is not under recorded worktree %q", live.CurrentPath, pane.WorktreePath)
	}
	return true, ""
}

func paneOwnerProjectRoot(defaultRoot string, pane state.Pane) string {
	if root := strings.TrimSpace(pane.SourceProjectRoot); root != "" {
		return root
	}
	return defaultRoot
}

func projectRootMatches(pane state.Pane, liveProjectRoot string) bool {
	liveProjectRoot = strings.TrimSpace(liveProjectRoot)
	if liveProjectRoot == "" {
		return false
	}
	if root := strings.TrimSpace(pane.SourceProjectRoot); root != "" {
		return samePath(root, liveProjectRoot)
	}
	for _, root := range pane.SourceProjectRoots {
		if samePath(root, liveProjectRoot) {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	return a != "." && b != "." && a == b
}

func pathWithinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == "." || path == "." {
		return false
	}
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	sourceParent, sourceIssueNum, sourceTaskID, sourceLabel := attachSourceIdentityFromStatePane(pane)
	return fanouttui.AttachTarget{
		TargetPath:        pane.WorktreePath,
		SourceProjectRoot: pane.SourceProjectRoot,
		SourceParent:      sourceParent,
		SourceIssueNum:    sourceIssueNum,
		SourceTaskID:      sourceTaskID,
		SourceBranchName:  pane.BranchName,
		SourceLabel:       sourceLabel,
	}
}

func attachSourceIdentityFromStatePane(pane state.Pane) (parent string, issueNum int, taskID, label string) {
	if !pane.IsAttachedAgent() {
		return pane.Parent, pane.IssueNum, pane.TaskID, sourceLabelForStatePane(pane)
	}
	parent = strings.TrimSpace(pane.SourceParent)
	if parent == "" {
		parent = pane.Parent
	}
	if pane.SourceIssueNum > 0 {
		issueNum = pane.SourceIssueNum
	}
	taskID = strings.TrimSpace(pane.SourceTaskID)
	switch {
	case taskID != "":
		label = taskID
	case issueNum > 0:
		label = fmt.Sprintf("#%d", issueNum)
	default:
		label = sourceLabelForStatePane(pane)
	}
	return parent, issueNum, taskID, label
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
