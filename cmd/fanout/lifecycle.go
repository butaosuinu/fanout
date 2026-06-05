package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/worktree"
)

func cmdClose(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	rt, locked, code := lockLifecycleState("--close", lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockLifecycleState("--close", locked, lg)

	panes := panesForIssue(locked.Store.PanesForParent(cfg.ParentRef), cfg.CloseNum)
	if len(panes) == 0 {
		lg.Err("--close: #%d is not recorded for parent %s in %s", cfg.CloseNum, cfg.ParentRef, rt.statePath)
		return exitcode.Invocation
	}
	if !cleanupPaneRecords(rt.projectRoot, panes, lg) {
		return exitcode.Env
	}
	if err := locked.RemovePane(cfg.ParentRef, cfg.CloseNum); err != nil {
		lg.Err("#%d: remove fanout state: %v", cfg.CloseNum, err)
		return exitcode.Env
	}
	if !pruneWorktrees(rt.projectRoot, lg) {
		return exitcode.Env
	}
	lg.Ok("#%d: closed fanout worktree and removed state", cfg.CloseNum)
	return exitcode.OK
}

func cmdMerge(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	rt, store, code := loadLifecycleState("--merge", lg)
	if code != exitcode.OK {
		return code
	}
	pane, ok := store.Find(cfg.ParentRef, cfg.MergeNum)
	if !ok {
		lg.Err("--merge: #%d is not recorded for parent %s in %s", cfg.MergeNum, cfg.ParentRef, rt.statePath)
		return exitcode.Invocation
	}
	if strings.TrimSpace(pane.BranchName) == "" {
		lg.Err("--merge: #%d has no branchName recorded in %s", cfg.MergeNum, rt.statePath)
		return exitcode.Invocation
	}
	out, err := gitLifecycle(rt.projectRoot, "merge", "--ff-only", pane.BranchName)
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

func cmdCleanup(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	rt, locked, code := lockLifecycleState("--cleanup", lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockLifecycleState("--cleanup", locked, lg)

	panes := locked.Store.PanesForParent(cfg.ParentRef)
	if len(panes) == 0 {
		lg.Info("--cleanup: no recorded panes for parent %s", cfg.ParentRef)
		return exitcode.OK
	}

	nums := make([]int, 0, len(panes))
	for _, pane := range panes {
		nums = append(nums, pane.IssueNum)
	}
	children, code := statusChildren(rt.projectRoot, sortedUnique(nums), "--cleanup", lg)
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
		lg.Info("--cleanup: no merged or closed recorded panes for parent %s", cfg.ParentRef)
		return exitcode.OK
	}

	closed := 0
	failed := 0
	for _, issueNum := range sortedUnique(nums) {
		if !eligible[issueNum] {
			continue
		}
		issuePanes := panesForIssue(panes, issueNum)
		if !cleanupPaneRecords(rt.projectRoot, issuePanes, lg) {
			failed++
			continue
		}
		if err := locked.RemovePane(cfg.ParentRef, issueNum); err != nil {
			lg.Err("#%d: remove fanout state: %v", issueNum, err)
			failed++
			continue
		}
		closed++
	}
	if !pruneWorktrees(rt.projectRoot, lg) {
		failed++
	}
	if failed > 0 {
		lg.Err("--cleanup: closed %d pane(s), failed %d cleanup step(s)", closed, failed)
		return exitcode.Env
	}
	lg.Ok("--cleanup: closed %d merged/closed pane(s)", closed)
	return exitcode.OK
}

func loadLifecycleState(mode string, lg *log.Logger) (fanoutStateRuntime, state.Store, exitcode.Code) {
	rt, code := resolveStateRuntimeForMode(mode, lg)
	if code != exitcode.OK {
		return fanoutStateRuntime{}, state.Store{}, code
	}
	if rt.projectRoot == "" || !dirExists(rt.projectRoot) {
		lg.Err("%s: project_root is not a directory: %s (state=%s)", mode, emptyLabel(rt.projectRoot), rt.statePath)
		return fanoutStateRuntime{}, state.Store{}, exitcode.Invocation
	}
	store, err := state.Load(rt.statePath)
	if err != nil {
		lg.Err("%s: fanout state at %s is not valid JSON or has an invalid schema: %v", mode, rt.statePath, err)
		return fanoutStateRuntime{}, state.Store{}, exitcode.Invocation
	}
	return rt, store, exitcode.OK
}

func lockLifecycleState(mode string, lg *log.Logger) (fanoutStateRuntime, *state.LockedStore, exitcode.Code) {
	rt, code := resolveStateRuntimeForMode(mode, lg)
	if code != exitcode.OK {
		return fanoutStateRuntime{}, nil, code
	}
	if rt.projectRoot == "" || !dirExists(rt.projectRoot) {
		lg.Err("%s: project_root is not a directory: %s (state=%s)", mode, emptyLabel(rt.projectRoot), rt.statePath)
		return fanoutStateRuntime{}, nil, exitcode.Invocation
	}
	if err := worktree.EnsureLocalExclude(rt.projectRoot); err != nil {
		lg.Err("%s: prepare local git exclude: %v", mode, err)
		return fanoutStateRuntime{}, nil, exitcode.Env
	}
	locked, err := state.Lock(rt.statePath)
	if err != nil {
		lg.Err("%s: %v", mode, err)
		return fanoutStateRuntime{}, nil, exitcode.Env
	}
	return rt, locked, exitcode.OK
}

func unlockLifecycleState(mode string, locked *state.LockedStore, lg *log.Logger) {
	if err := locked.Unlock(); err != nil {
		lg.Warn("%s: unlock fanout state: %v", mode, err)
	}
}

func cleanupPaneRecords(projectRoot string, panes []state.Pane, lg *log.Logger) bool {
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

func removeWorktree(projectRoot string, pane state.Pane, lg *log.Logger) bool {
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

func killPaneBestEffort(pane state.Pane, lg *log.Logger) {
	if strings.TrimSpace(pane.PaneID) == "" {
		lg.Warn("#%d: no paneId recorded; skipping tmux kill-pane", pane.IssueNum)
		return
	}
	if err := tmuxrun.KillPane(pane.PaneID); err != nil {
		lg.Warn("#%d: tmux kill-pane %s failed; treating pane as stale: %v", pane.IssueNum, pane.PaneID, err)
	}
}

func pruneWorktrees(projectRoot string, lg *log.Logger) bool {
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
	return sortedKeys(set)
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
