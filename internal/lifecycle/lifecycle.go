// Package lifecycle owns side-effecting pane lifecycle operations shared by
// the CLI and the long-running TUI.
package lifecycle

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/worktree"
)

// Options points lifecycle operations at a concrete fanout state file.
type Options struct {
	ProjectRoot string
	StatePath   string
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
	locked, code := lockState("--close", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--close", locked, lg)

	panes := panesForIssue(locked.Store.PanesForParent(parent), issueNum)
	if len(panes) == 0 {
		lg.Err("--close: #%d is not recorded for parent %s in %s", issueNum, parent, opts.StatePath)
		return exitcode.Invocation
	}
	if !cleanupPaneRecords(opts.ProjectRoot, panes, lg) {
		return exitcode.Env
	}
	if err := locked.RemovePane(parent, issueNum); err != nil {
		lg.Err("#%d: remove fanout state: %v", issueNum, err)
		return exitcode.Env
	}
	if !pruneWorktrees(opts.ProjectRoot, lg) {
		return exitcode.Env
	}
	lg.Ok("#%d: closed fanout worktree and removed state", issueNum)
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
	out, err := gitLifecycle(opts.ProjectRoot, "merge", "--ff-only", pane.BranchName)
	if err != nil {
		lg.Err("--merge: git merge --ff-only %s failed for #%d; no conflict resolution was attempted", pane.BranchName, pane.IssueNum)
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintln(lg.Stderr(), s)
		}
		return exitcode.Env
	}
	lg.Ok("#%d: merged %s with --ff-only", pane.IssueNum, pane.BranchName)
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

	panes := locked.Store.PanesForParent(parent)
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
		if !cleanupPaneRecords(opts.ProjectRoot, issuePanes, lg) {
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

func cleanupPaneRecords(projectRoot string, panes []state.Pane, lg Logger) bool {
	ok := true
	for _, pane := range panes {
		if !removeWorktree(projectRoot, pane, lg) {
			ok = false
			continue
		}
		killPaneBestEffort(pane, lg)
	}
	return ok
}

func removeWorktree(projectRoot string, pane state.Pane, lg Logger) bool {
	if strings.TrimSpace(pane.WorktreePath) == "" {
		lg.Warn("#%d: no worktreePath recorded; skipping git worktree remove", pane.IssueNum)
		return true
	}
	if !dirExists(pane.WorktreePath) {
		lg.Warn("#%d: worktree path already absent: %s", pane.IssueNum, pane.WorktreePath)
		return true
	}
	out, err := gitLifecycle(projectRoot, "worktree", "remove", pane.WorktreePath, "--force")
	if err != nil {
		lg.Err("#%d: git worktree remove %s --force failed", pane.IssueNum, pane.WorktreePath)
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintln(lg.Stderr(), s)
		}
		return false
	}
	return true
}

func killPaneBestEffort(pane state.Pane, lg Logger) {
	if strings.TrimSpace(pane.PaneID) == "" {
		lg.Warn("#%d: no paneId recorded; skipping tmux kill-pane", pane.IssueNum)
		return
	}
	if err := tmuxrun.KillPane(pane.PaneID); err != nil {
		lg.Warn("#%d: tmux kill-pane %s failed; treating pane as stale: %v", pane.IssueNum, pane.PaneID, err)
	}
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

func sortedUnique(nums []int) []int {
	set := map[int]bool{}
	for _, num := range nums {
		set[num] = true
	}
	out := make([]int, 0, len(set))
	for num := range set {
		out = append(out, num)
	}
	sort.Ints(out)
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
