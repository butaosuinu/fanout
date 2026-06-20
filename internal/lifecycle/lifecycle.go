// Package lifecycle owns side-effecting pane lifecycle operations shared by
// the CLI and the long-running TUI.
package lifecycle

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/worktree"
)

// Options points lifecycle operations at a concrete fanout state file.
type Options struct {
	ProjectRoot string
	StatePath   string
	Hooks       hooks.Config
}

// Logger is the narrow logging surface lifecycle operations need.
type Logger interface {
	Info(format string, a ...any)
	Ok(format string, a ...any)
	Warn(format string, a ...any)
	Err(format string, a ...any)
	Stderr() io.Writer
}

type statusChild struct {
	Num         int
	State       string
	HasMergedPR bool
}

// Close removes the recorded worktree(s), best-effort kills tmux pane(s), and
// removes all state rows matching parent and issueNum.
func Close(opts Options, parent string, issueNum int, lg Logger) exitcode.Code {
	locked, code := lockStateOnly("--close", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--close", locked, lg)

	panes := panesForIssue(locked.PanesForParent(parent), issueNum)
	if len(panes) == 0 {
		lg.Err("--close: #%d is not recorded for parent %s in %s", issueNum, parent, opts.StatePath)
		return exitcode.Invocation
	}
	if hasManagedWorktree(panes) {
		if err := worktree.EnsureLocalExclude(opts.ProjectRoot); err != nil {
			lg.Err("--close: prepare local git exclude: %v", err)
			return exitcode.Env
		}
	}
	if !cleanupPaneRecords(opts, panes, lg) {
		return exitcode.Env
	}
	if err := locked.RemovePane(parent, issueNum); err != nil {
		lg.Err("#%d: remove fanout state: %v", issueNum, err)
		return exitcode.Env
	}
	if hasManagedWorktree(panes) && !pruneWorktrees(opts.ProjectRoot, lg) {
		return exitcode.Env
	}
	if hasManagedWorktree(panes) {
		lg.Ok("#%d: closed fanout worktree and removed state", issueNum)
	} else {
		lg.Ok("#%d: closed shell terminal and removed state", issueNum)
	}
	return exitcode.OK
}

// CloseTask removes the recorded worktree(s), best-effort kills tmux pane(s),
// and removes all task state rows matching parent and taskID.
func CloseTask(opts Options, parent, taskID string, lg Logger) exitcode.Code {
	locked, code := lockStateOnly("--close", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--close", locked, lg)

	panes := panesForTask(locked.PanesForParent(parent), taskID)
	if len(panes) == 0 {
		lg.Err("--close: task %s is not recorded for parent %s in %s", taskID, parent, opts.StatePath)
		return exitcode.Invocation
	}
	if hasManagedWorktree(panes) {
		if err := worktree.EnsureLocalExclude(opts.ProjectRoot); err != nil {
			lg.Err("--close: prepare local git exclude: %v", err)
			return exitcode.Env
		}
	}
	if !cleanupPaneRecords(opts, panes, lg) {
		return exitcode.Env
	}
	if err := locked.RemoveTaskPane(parent, taskID); err != nil {
		lg.Err("%s: remove fanout state: %v", taskID, err)
		return exitcode.Env
	}
	if hasManagedWorktree(panes) && !pruneWorktrees(opts.ProjectRoot, lg) {
		return exitcode.Env
	}
	if hasManagedWorktree(panes) {
		lg.Ok("%s: closed fanout worktree and removed state", taskID)
	} else {
		lg.Ok("%s: closed shell terminal and removed state", taskID)
	}
	return exitcode.OK
}

// Merge fast-forwards the project checkout to the recorded child branch.
func Merge(opts Options, parent string, issueNum int, lg Logger) exitcode.Code {
	store, code := loadState("--merge", opts, lg)
	if code != exitcode.OK {
		return code
	}
	pane, ok := store.Find(parent, issueNum)
	if !ok {
		lg.Err("--merge: #%d is not recorded for parent %s in %s", issueNum, parent, opts.StatePath)
		return exitcode.Invocation
	}
	if strings.TrimSpace(pane.BranchName) == "" {
		lg.Err("--merge: #%d has no branchName recorded in %s", issueNum, opts.StatePath)
		return exitcode.Invocation
	}
	targetBranch := currentBranch(opts.ProjectRoot)
	if !runBlockingHook(hooks.PreMerge, opts, pane, targetBranch, lg) {
		return exitcode.Env
	}
	out, err := gitLifecycle(opts.ProjectRoot, "merge", "--ff-only", pane.BranchName)
	if err != nil {
		lg.Err("--merge: git merge --ff-only %s failed for #%d; no conflict resolution was attempted", pane.BranchName, pane.IssueNum)
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintln(lg.Stderr(), s)
		}
		return exitcode.Env
	}
	runBackgroundHook(hooks.PostMerge, opts, pane, targetBranch, lg)
	lg.Ok("#%d: merged %s with --ff-only", pane.IssueNum, pane.BranchName)
	return exitcode.OK
}

// MergeTask fast-forwards the project checkout to the recorded task branch.
func MergeTask(opts Options, parent, taskID string, lg Logger) exitcode.Code {
	store, code := loadState("--merge", opts, lg)
	if code != exitcode.OK {
		return code
	}
	pane, ok := store.FindTask(parent, taskID)
	if !ok {
		lg.Err("--merge: task %s is not recorded for parent %s in %s", taskID, parent, opts.StatePath)
		return exitcode.Invocation
	}
	if strings.TrimSpace(pane.BranchName) == "" {
		lg.Err("--merge: task %s has no branchName recorded in %s", taskID, opts.StatePath)
		return exitcode.Invocation
	}
	targetBranch := currentBranch(opts.ProjectRoot)
	if !runBlockingHook(hooks.PreMerge, opts, pane, targetBranch, lg) {
		return exitcode.Env
	}
	out, err := gitLifecycle(opts.ProjectRoot, "merge", "--ff-only", pane.BranchName)
	if err != nil {
		lg.Err("--merge: git merge --ff-only %s failed for task %s; no conflict resolution was attempted", pane.BranchName, taskID)
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintln(lg.Stderr(), s)
		}
		return exitcode.Env
	}
	runBackgroundHook(hooks.PostMerge, opts, pane, targetBranch, lg)
	lg.Ok("%s: merged %s with --ff-only", taskID, pane.BranchName)
	return exitcode.OK
}

// Cleanup closes every recorded child for parent whose issue is closed or has
// at least one merged closed-by PR.
func Cleanup(opts Options, parent string, lg Logger) exitcode.Code {
	locked, code := lockState("--cleanup", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--cleanup", locked, lg)

	panes := cleanupIssuePanes(locked.PanesForParent(parent))
	if len(panes) == 0 {
		lg.Info("--cleanup: no recorded panes for parent %s", parent)
		return exitcode.OK
	}

	nums := make([]int, 0, len(panes))
	for _, pane := range panes {
		nums = append(nums, pane.IssueNum)
	}
	children, code := statusChildren(opts.ProjectRoot, sortedUnique(nums), "--cleanup", lg)
	if code != exitcode.OK {
		return code
	}
	eligible := map[int]bool{}
	for _, child := range children {
		if strings.EqualFold(child.State, "CLOSED") || child.HasMergedPR {
			eligible[child.Num] = true
		}
	}
	if len(eligible) == 0 {
		lg.Info("--cleanup: no merged or closed recorded panes for parent %s", parent)
		return exitcode.OK
	}

	closed := 0
	failed := 0
	for _, issueNum := range sortedUnique(nums) {
		if !eligible[issueNum] {
			continue
		}
		issuePanes := panesForIssue(panes, issueNum)
		if !cleanupPaneRecords(opts, issuePanes, lg) {
			failed++
			continue
		}
		if err := locked.RemovePane(parent, issueNum); err != nil {
			lg.Err("#%d: remove fanout state: %v", issueNum, err)
			failed++
			continue
		}
		closed++
	}
	if !pruneWorktrees(opts.ProjectRoot, lg) {
		failed++
	}
	if failed > 0 {
		lg.Err("--cleanup: closed %d pane(s), failed %d cleanup step(s)", closed, failed)
		return exitcode.Env
	}
	lg.Ok("--cleanup: closed %d merged/closed pane(s)", closed)
	return exitcode.OK
}

// CleanupPlan closes every recorded plan task for parent whose recorded branch
// has at least one MERGED PR.
func CleanupPlan(opts Options, parent string, lg Logger) exitcode.Code {
	locked, code := lockState("--cleanup", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--cleanup", locked, lg)

	panes := taskPanesForParent(locked.PanesForParent(parent))
	if len(panes) == 0 {
		lg.Info("--cleanup: no recorded plan task panes for parent %s", parent)
		return exitcode.OK
	}

	eligible := map[string]bool{}
	checkedBranches := map[string]bool{}
	gh := ghissue.Runner{Cwd: opts.ProjectRoot}
	for _, pane := range panes {
		taskID := strings.TrimSpace(pane.TaskID)
		branch := strings.TrimSpace(pane.BranchName)
		if taskID == "" || branch == "" || checkedBranches[branch] {
			continue
		}
		checkedBranches[branch] = true
		prs, err := gh.PRsForBranch(branch)
		if err != nil {
			lg.Err("--cleanup: gh pr list --head %s failed for task %s: %v", branch, taskID, err)
			return exitcode.GitHub
		}
		if branchHasMergedPR(prs) {
			for _, candidate := range panes {
				if strings.TrimSpace(candidate.BranchName) == branch {
					eligible[strings.TrimSpace(candidate.TaskID)] = true
				}
			}
		}
	}
	if len(eligible) == 0 {
		lg.Info("--cleanup: no merged recorded plan task panes for parent %s", parent)
		return exitcode.OK
	}

	closed := 0
	failed := 0
	for _, taskID := range sortedTaskIDs(eligible) {
		taskPanes := panesForTask(panes, taskID)
		if !cleanupPaneRecords(opts, taskPanes, lg) {
			failed++
			continue
		}
		if err := locked.RemoveTaskPane(parent, taskID); err != nil {
			lg.Err("%s: remove fanout state: %v", taskID, err)
			failed++
			continue
		}
		closed++
	}
	if !pruneWorktrees(opts.ProjectRoot, lg) {
		failed++
	}
	if failed > 0 {
		lg.Err("--cleanup: closed %d plan task pane(s), failed %d cleanup step(s)", closed, failed)
		return exitcode.Env
	}
	lg.Ok("--cleanup: closed %d merged plan task pane(s)", closed)
	return exitcode.OK
}

func loadState(mode string, opts Options, lg Logger) (state.Store, exitcode.Code) {
	if opts.ProjectRoot == "" || !dirExists(opts.ProjectRoot) {
		lg.Err("%s: project_root is not a directory: %s (state=%s)", mode, emptyLabel(opts.ProjectRoot), opts.StatePath)
		return state.Store{}, exitcode.Invocation
	}
	store, err := state.Load(opts.StatePath)
	if err != nil {
		lg.Err("%s: fanout state at %s is not valid JSON or has an invalid schema: %v", mode, opts.StatePath, err)
		return state.Store{}, exitcode.Invocation
	}
	return store, exitcode.OK
}

func lockState(mode string, opts Options, lg Logger) (*state.LockedStore, exitcode.Code) {
	if opts.ProjectRoot == "" || !dirExists(opts.ProjectRoot) {
		lg.Err("%s: project_root is not a directory: %s (state=%s)", mode, emptyLabel(opts.ProjectRoot), opts.StatePath)
		return nil, exitcode.Invocation
	}
	if err := worktree.EnsureLocalExclude(opts.ProjectRoot); err != nil {
		lg.Err("%s: prepare local git exclude: %v", mode, err)
		return nil, exitcode.Env
	}
	return lockStateOnly(mode, opts, lg)
}

func lockStateOnly(mode string, opts Options, lg Logger) (*state.LockedStore, exitcode.Code) {
	if opts.ProjectRoot == "" || !dirExists(opts.ProjectRoot) {
		lg.Err("%s: project_root is not a directory: %s (state=%s)", mode, emptyLabel(opts.ProjectRoot), opts.StatePath)
		return nil, exitcode.Invocation
	}
	locked, err := state.Lock(opts.StatePath)
	if err != nil {
		lg.Err("%s: %v", mode, err)
		return nil, exitcode.Env
	}
	return locked, exitcode.OK
}

func unlockState(mode string, locked *state.LockedStore, lg Logger) {
	if err := locked.Unlock(); err != nil {
		lg.Warn("%s: unlock fanout state: %v", mode, err)
	}
}

func statusChildren(projectRoot string, nums []int, mode string, lg Logger) ([]statusChild, exitcode.Code) {
	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("%s: failed to resolve repo (gh repo view) in %s", mode, projectRoot)
		return nil, exitcode.GitHub
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		lg.Err("%s: unexpected nameWithOwner from gh: %s", mode, nwo)
		return nil, exitcode.GitHub
	}

	children := make([]statusChild, 0, len(nums))
	for _, num := range nums {
		stateName, prs, err := gh.IssueWithPRs(owner, repo, num)
		if err != nil {
			lg.Err("%s: gh api graphql for #%d failed or returned no issue (auth / network / not found)", mode, num)
			return nil, exitcode.GitHub
		}
		child := statusChild{Num: num, State: stateName}
		for _, pr := range prs {
			if pr.State == "MERGED" {
				child.HasMergedPR = true
				break
			}
		}
		children = append(children, child)
	}
	return children, exitcode.OK
}

func cleanupPaneRecords(opts Options, panes []state.Pane, lg Logger) bool {
	ok := true
	for _, pane := range panes {
		if pane.IsShell() {
			runBackgroundHook(hooks.BeforePaneClose, opts, pane, "", lg)
			killShellPaneBestEffort(pane, lg)
			runBackgroundHook(hooks.PaneClosed, opts, pane, "", lg)
			continue
		}
		hadWorktree := recordedWorktreeExists(pane)
		if hadWorktree && !runBlockingHook(hooks.BeforeWorktreeRemove, opts, pane, "", lg) {
			ok = false
			continue
		}
		if !removeWorktree(opts.ProjectRoot, pane, lg) {
			ok = false
			continue
		}
		if hadWorktree {
			runBackgroundHook(hooks.WorktreeRemoved, opts, pane, "", lg)
		}
		runBackgroundHook(hooks.BeforePaneClose, opts, pane, "", lg)
		killPaneBestEffort(pane, lg)
		runBackgroundHook(hooks.PaneClosed, opts, pane, "", lg)
	}
	return ok
}

func hasManagedWorktree(panes []state.Pane) bool {
	for _, pane := range panes {
		if !pane.IsShell() {
			return true
		}
	}
	return false
}

func cleanupIssuePanes(panes []state.Pane) []state.Pane {
	var out []state.Pane
	for _, pane := range panes {
		if pane.IsShell() || pane.IssueNum <= 0 {
			continue
		}
		out = append(out, pane)
	}
	return out
}

func removeWorktree(projectRoot string, pane state.Pane, lg Logger) bool {
	if strings.TrimSpace(pane.WorktreePath) == "" {
		lg.Warn("%s: no worktreePath recorded; skipping git worktree remove", paneLabel(pane))
		return true
	}
	if !dirExists(pane.WorktreePath) {
		lg.Warn("%s: worktree path already absent: %s", paneLabel(pane), pane.WorktreePath)
		return true
	}
	out, err := gitLifecycle(projectRoot, "worktree", "remove", pane.WorktreePath, "--force")
	if err != nil {
		lg.Err("%s: git worktree remove %s --force failed", paneLabel(pane), pane.WorktreePath)
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintln(lg.Stderr(), s)
		}
		return false
	}
	return true
}

func killPaneBestEffort(pane state.Pane, lg Logger) {
	if strings.TrimSpace(pane.PaneID) == "" {
		lg.Warn("%s: no paneId recorded; skipping tmux kill-pane", paneLabel(pane))
		return
	}
	if err := tmuxrun.KillPane(pane.PaneID); err != nil {
		lg.Warn("%s: tmux kill-pane %s failed; treating pane as stale: %v", paneLabel(pane), pane.PaneID, err)
	}
}

func killShellPaneBestEffort(pane state.Pane, lg Logger) {
	if strings.TrimSpace(pane.PaneID) == "" {
		lg.Warn("%s: no paneId recorded; skipping tmux kill-pane", paneLabel(pane))
		return
	}
	shellKey := strings.TrimSpace(pane.ShellKey)
	if shellKey == "" {
		lg.Warn("%s: no shellKey recorded; skipping tmux kill-pane to avoid pane id reuse", paneLabel(pane))
		return
	}
	live, err := tmuxrun.ListLivePanes()
	if err != nil {
		lg.Warn("%s: tmux list-panes failed; skipping shell pane kill: %v", paneLabel(pane), err)
		return
	}
	for _, cur := range live {
		if cur.ID != pane.PaneID {
			continue
		}
		if cur.ShellKey != shellKey {
			lg.Warn("%s: shell pane %s identity changed; skipping tmux kill-pane to avoid pane id reuse", paneLabel(pane), pane.PaneID)
			return
		}
		killPaneBestEffort(pane, lg)
		return
	}
	lg.Warn("%s: shell pane %s is gone; skipping tmux kill-pane", paneLabel(pane), pane.PaneID)
}

func pruneWorktrees(projectRoot string, lg Logger) bool {
	out, err := gitLifecycle(projectRoot, "worktree", "prune")
	if err != nil {
		lg.Err("git worktree prune failed")
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintln(lg.Stderr(), s)
		}
		return false
	}
	return true
}

func gitLifecycle(projectRoot string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"-C", projectRoot}, args...)
	cmd := exec.Command("git", cmdArgs...)
	return cmd.CombinedOutput()
}

func currentBranch(projectRoot string) string {
	out, err := gitLifecycle(projectRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runBlockingHook(hook hooks.Type, opts Options, pane state.Pane, targetBranch string, lg Logger) bool {
	result := hooks.RunBlocking(hook, hookContext(opts.ProjectRoot, pane, targetBranch), opts.Hooks, lg)
	if result.OK() {
		return true
	}
	lg.Err("%s: %v", paneLabel(pane), result.Err)
	printHookOutput(result, lg)
	return false
}

func runBackgroundHook(hook hooks.Type, opts Options, pane state.Pane, targetBranch string, lg Logger) {
	hooks.RunBackground(hook, hookContext(opts.ProjectRoot, pane, targetBranch), opts.Hooks, lg)
}

func hookContext(projectRoot string, pane state.Pane, targetBranch string) hooks.Context {
	return hooks.Context{
		ProjectRoot:  projectRoot,
		Parent:       pane.Parent,
		IssueNum:     pane.IssueNum,
		TaskID:       pane.TaskID,
		Slug:         pane.Slug,
		Prompt:       pane.Prompt,
		Agent:        pane.Agent,
		TmuxPaneID:   pane.PaneID,
		WorktreePath: pane.WorktreePath,
		Branch:       pane.BranchName,
		BaseBranch:   pane.BaseBranch,
		TargetBranch: targetBranch,
	}
}

func printHookOutput(result hooks.Result, lg Logger) {
	if s := strings.TrimSpace(string(result.Output)); s != "" {
		fmt.Fprintln(lg.Stderr(), s)
	}
}

func recordedWorktreeExists(pane state.Pane) bool {
	return strings.TrimSpace(pane.WorktreePath) != "" && dirExists(pane.WorktreePath)
}

func sortedUnique(nums []int) []int {
	set := map[int]bool{}
	for _, num := range nums {
		set[num] = true
	}
	out := make([]int, 0, len(set))
	for num := range set {
		out = append(out, num)
	}
	slices.Sort(out)
	return out
}

func panesForIssue(panes []state.Pane, issueNum int) []state.Pane {
	var out []state.Pane
	for _, pane := range panes {
		if pane.IssueNum == issueNum {
			out = append(out, pane)
		}
	}
	return out
}

func panesForTask(panes []state.Pane, taskID string) []state.Pane {
	taskID = strings.TrimSpace(taskID)
	var out []state.Pane
	for _, pane := range panes {
		if strings.TrimSpace(pane.TaskID) == taskID {
			out = append(out, pane)
		}
	}
	return out
}

func taskPanesForParent(panes []state.Pane) []state.Pane {
	var out []state.Pane
	for _, pane := range panes {
		if strings.TrimSpace(pane.TaskID) != "" {
			out = append(out, pane)
		}
	}
	return out
}

func branchHasMergedPR(prs []ghissue.PRRef) bool {
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "MERGED") || pr.MergedAt != nil {
			return true
		}
	}
	return false
}

func sortedTaskIDs(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func paneLabel(pane state.Pane) string {
	if taskID := strings.TrimSpace(pane.TaskID); taskID != "" {
		return taskID
	}
	return fmt.Sprintf("#%d", pane.IssueNum)
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func emptyLabel(s string) string {
	if s == "" {
		return "<empty>"
	}
	return s
}
