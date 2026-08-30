// Package lifecycle owns side-effecting pane lifecycle operations shared by
// the CLI and the long-running TUI.
package lifecycle

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// Options points lifecycle operations at a concrete fanout state file.
type Options struct {
	ProjectRoot         string
	StatePath           string
	Hooks               hooks.Config
	WatcherRunningLabel string
	RemoveIssueLabel    func(issueNum int, label string) error
	CloseOwned          func(backend.CloseRequest) (backend.CloseResult, error)
	// Relayout re-tiles a container after a pane is removed. It is nil for a
	// runtime that arranges its own workspace, and the repair is then skipped —
	// the same meaning an absent layout capability has on the launch side.
	Relayout         func(target string, trigger backend.LayoutTrigger) error
	WorkspaceRuntime WorkspaceRuntimeFactory
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

// CloseMode selects how much repository state a close operation removes.
type CloseMode int

const (
	// ClosePaneOnly closes the runtime pane and removes fanout state, leaving the
	// worktree and branch available outside fanout.
	ClosePaneOnly CloseMode = iota
	// CloseWorktree closes the runtime pane, removes the worktree, and leaves the
	// local branch intact. This is the historical --close behavior.
	CloseWorktree
	// CloseEverything also deletes the recorded local branch after removing the
	// worktree.
	CloseEverything
)

const watcherStandaloneParent = "@watch"

// Close verifies and closes the recorded runtime pane(s), then removes the
// recorded worktree(s) and all state rows matching parent and issueNum.
func Close(opts Options, parent string, issueNum int, lg Logger) exitcode.Code {
	return CloseWithMode(opts, parent, issueNum, CloseWorktree, lg)
}

// CloseWithMode closes all recorded panes matching parent and issueNum using
// mode to decide whether worktree and branch state should also be removed.
//
//nolint:funlen // Keep target selection, recovery, close, and retirement under one state lock.
func CloseWithMode(opts Options, parent string, issueNum int, mode CloseMode, lg Logger) exitcode.Code {
	locked, code := lockStateOnly("--close", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--close", locked, lg)

	allParentPanes := locked.PanesForParent(parent)
	panes := panesForIssue(allParentPanes, issueNum)
	if len(panes) == 0 {
		lg.Err("--close: #%d is not recorded for parent %s in %s", issueNum, parent, opts.StatePath)
		return exitcode.Invocation
	}
	panes, recoveryCode, handled := prepareCompletedWorkspaceRetirement(
		opts, locked, panes, mode, fmt.Sprintf("#%d", issueNum), lg,
	)
	if handled {
		return recoveryCode
	}
	if !validateCloseOperations(opts, panes, mode, lg) {
		return exitcode.Env
	}
	windows := map[string]struct{}{}
	defer relayoutClosedWindows(opts.Relayout, windows, lg)
	if mode.removesWorktree() && hasManagedWorktree(panes) {
		if err := worktree.EnsureLocalExclude(opts.ProjectRoot); err != nil {
			lg.Err("--close: prepare local git exclude: %v", err)
			return exitcode.Env
		}
	}
	if !closePaneRecordsLocked(opts, locked, panes, mode, lg, windows) {
		return exitcode.Env
	}
	if err := retirePaneStateRows(opts, locked, panes, mode); err != nil {
		lg.Err("#%d: remove fanout state: %v", issueNum, err)
		return exitcode.Env
	}
	removeWatcherRunningLabelBestEffort(opts, parent, issueNum, locked.PanesForParent(parent), lg)
	if mode.removesWorktree() && hasManagedWorktree(panes) && !pruneWorktrees(opts.ProjectRoot, lg) {
		return exitcode.Env
	}
	switch {
	case mode == ClosePaneOnly:
		lg.Ok("#%d: closed pane and removed state", issueNum)
	case hasManagedWorktree(panes):
		lg.Ok("#%d: closed fanout worktree and removed state", issueNum)
	default:
		lg.Ok("#%d: closed pane and removed state", issueNum)
	}
	return exitcode.OK
}

// CloseTask verifies and closes the recorded runtime pane(s), then removes the
// recorded worktree(s) and all task state rows matching parent and taskID.
func CloseTask(opts Options, parent, taskID string, lg Logger) exitcode.Code {
	return CloseTaskWithMode(opts, parent, taskID, CloseWorktree, lg)
}

// CloseTaskWithMode closes all recorded task panes matching parent and taskID
// using mode to decide whether worktree and branch state should also be removed.
//
//nolint:funlen // Keep target selection, recovery, close, and retirement under one state lock.
func CloseTaskWithMode(opts Options, parent, taskID string, mode CloseMode, lg Logger) exitcode.Code {
	locked, code := lockStateOnly("--close", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--close", locked, lg)

	allParentPanes := locked.PanesForParent(parent)
	panes := panesForTask(allParentPanes, taskID)
	if len(panes) == 0 {
		lg.Err("--close: task %s is not recorded for parent %s in %s", taskID, parent, opts.StatePath)
		return exitcode.Invocation
	}
	panes, recoveryCode, handled := prepareCompletedWorkspaceRetirement(
		opts, locked, panes, mode, taskID, lg,
	)
	if handled {
		return recoveryCode
	}
	if !validateCloseOperations(opts, panes, mode, lg) {
		return exitcode.Env
	}
	windows := map[string]struct{}{}
	defer relayoutClosedWindows(opts.Relayout, windows, lg)
	if mode.removesWorktree() && hasManagedWorktree(panes) {
		if err := worktree.EnsureLocalExclude(opts.ProjectRoot); err != nil {
			lg.Err("--close: prepare local git exclude: %v", err)
			return exitcode.Env
		}
	}
	if !closePaneRecordsLocked(opts, locked, panes, mode, lg, windows) {
		return exitcode.Env
	}
	if err := retirePaneStateRows(opts, locked, panes, mode); err != nil {
		lg.Err("%s: remove fanout state: %v", taskID, err)
		return exitcode.Env
	}
	if mode.removesWorktree() && hasManagedWorktree(panes) && !pruneWorktrees(opts.ProjectRoot, lg) {
		return exitcode.Env
	}
	switch {
	case mode == ClosePaneOnly:
		lg.Ok("%s: closed pane and removed state", taskID)
	case hasManagedWorktree(panes):
		lg.Ok("%s: closed fanout worktree and removed state", taskID)
	default:
		lg.Ok("%s: closed pane and removed state", taskID)
	}
	return exitcode.OK
}

// Merge fast-forwards the project checkout to the recorded child branch.
func Merge(opts Options, parent string, issueNum int, lg Logger) exitcode.Code {
	if code := validateState("--merge", opts, lg); code != exitcode.OK {
		return code
	}
	locked, code := lockStateOnly("--merge", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--merge", locked, lg)
	pane, ok := locked.Find(parent, issueNum)
	if !ok {
		lg.Err("--merge: #%d is not recorded for parent %s in %s", issueNum, parent, opts.StatePath)
		return exitcode.Invocation
	}
	if code := mergeRecordedPane(opts, pane, fmt.Sprintf("#%d", issueNum), lg); code != exitcode.OK {
		return code
	}
	removeWatcherRunningLabelBestEffort(opts, parent, pane.IssueNum, remainingIssuePanesAfter(locked.PanesForParent(parent), pane.IssueNum), lg)
	return exitcode.OK
}

// MergeTask fast-forwards the project checkout to the recorded task branch.
func MergeTask(opts Options, parent, taskID string, lg Logger) exitcode.Code {
	if code := validateState("--merge", opts, lg); code != exitcode.OK {
		return code
	}
	locked, code := lockStateOnly("--merge", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--merge", locked, lg)
	pane, ok := locked.FindTask(parent, taskID)
	if !ok {
		lg.Err("--merge: task %s is not recorded for parent %s in %s", taskID, parent, opts.StatePath)
		return exitcode.Invocation
	}
	return mergeRecordedPane(opts, pane, taskID, lg)
}

func mergeRecordedPane(opts Options, pane state.Pane, subject string, lg Logger) exitcode.Code {
	if strings.TrimSpace(pane.BranchName) == "" {
		lg.Err("--merge: %s has no branchName recorded in %s", subject, opts.StatePath)
		return exitcode.Invocation
	}
	if err := validateWorkspaceMergeOperation(opts, pane); err != nil {
		lg.Err("--merge: %s Herdr identity check failed: %v", subject, err)
		return exitcode.Env
	}
	targetBranch := currentBranch(opts.ProjectRoot)
	if !runBlockingHook(hooks.PreMerge, opts, pane, targetBranch, lg) {
		return exitcode.Env
	}
	out, err := gitLifecycle(opts.ProjectRoot, "merge", "--ff-only", pane.BranchName)
	if err != nil {
		lg.Err("--merge: git merge --ff-only %s failed for %s; no conflict resolution was attempted", pane.BranchName, subject)
		if details := strings.TrimSpace(string(out)); details != "" {
			fmt.Fprintln(lg.Stderr(), details)
		}
		return exitcode.Env
	}
	runBackgroundHook(hooks.PostMerge, opts, pane, targetBranch, lg)
	lg.Ok("%s: merged %s with --ff-only", subject, pane.BranchName)
	return exitcode.OK
}

// Cleanup closes every recorded child for parent whose issue is closed or has
// at least one merged closed-by PR.
//
//nolint:gocognit,gocyclo,funlen // Keep eligibility and per-child fail-soft cleanup in one lock-held orchestration.
func Cleanup(opts Options, parent string, lg Logger) exitcode.Code {
	locked, code := lockStateOnly("--cleanup", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--cleanup", locked, lg)

	allParentPanes := locked.PanesForParent(parent)
	panes := cleanupIssuePanes(allParentPanes)
	if len(panes) == 0 {
		lg.Info("--cleanup: no recorded panes for parent %s", parent)
		return exitcode.OK
	}

	panes, retired, err := retireCompletedIssueCleanups(opts, locked, parent, panes, lg)
	if err != nil {
		lg.Err("--cleanup: retire completed Herdr cleanup: %v", err)
		return exitcode.Env
	}
	if retired > 0 {
		lg.Ok("--cleanup: retired %d completed Herdr cleanup(s)", retired)
		if len(panes) == 0 {
			return exitcode.OK
		}
	}
	nums := paneIssueNumbers(panes)
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
	for _, issueNum := range sortedUnique(nums) {
		if !eligible[issueNum] {
			continue
		}
		issuePanes := panesSharingManagedWorktrees(locked.Panes, panesForIssue(panes, issueNum))
		if !validateCloseOperations(opts, issuePanes, CloseWorktree, lg) {
			return exitcode.Env
		}
	}
	if err := worktree.EnsureLocalExclude(opts.ProjectRoot); err != nil {
		lg.Err("--cleanup: prepare local git exclude: %v", err)
		return exitcode.Env
	}

	closed := 0
	failed := 0
	windows := map[string]struct{}{}
	defer relayoutClosedWindows(opts.Relayout, windows, lg)
	for _, issueNum := range sortedUnique(nums) {
		if !eligible[issueNum] {
			continue
		}
		issuePanes := panesSharingManagedWorktrees(locked.Panes, panesForIssue(panes, issueNum))
		if !cleanupPaneRecordsLocked(opts, locked, issuePanes, lg, windows) {
			failed++
			continue
		}
		if err := retirePaneStateRows(opts, locked, issuePanes, CloseWorktree); err != nil {
			lg.Err("#%d: remove fanout state: %v", issueNum, err)
			failed++
			continue
		}
		closed++
		removeWatcherRunningLabelBestEffort(opts, parent, issueNum, locked.PanesForParent(parent), lg)
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
//
//nolint:gocognit,gocyclo,funlen // Keep branch eligibility and per-task fail-soft cleanup in one lock-held orchestration.
func CleanupPlan(opts Options, parent string, lg Logger) exitcode.Code {
	locked, code := lockStateOnly("--cleanup", opts, lg)
	if code != exitcode.OK {
		return code
	}
	defer unlockState("--cleanup", locked, lg)

	allParentPanes := locked.PanesForParent(parent)
	panes := taskPanesForParent(allParentPanes)
	if len(panes) == 0 {
		lg.Info("--cleanup: no recorded plan task panes for parent %s", parent)
		return exitcode.OK
	}
	panes, retired, err := retireCompletedTaskCleanups(opts, locked, parent, panes, lg)
	if err != nil {
		lg.Err("--cleanup: retire completed Herdr cleanup: %v", err)
		return exitcode.Env
	}
	if retired > 0 {
		lg.Ok("--cleanup: retired %d completed Herdr cleanup(s)", retired)
		if len(panes) == 0 {
			return exitcode.OK
		}
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
	for _, taskID := range sortedTaskIDs(eligible) {
		taskPanes := panesSharingManagedWorktrees(locked.Panes, panesForTask(panes, taskID))
		if !validateCloseOperations(opts, taskPanes, CloseWorktree, lg) {
			return exitcode.Env
		}
	}
	if err := worktree.EnsureLocalExclude(opts.ProjectRoot); err != nil {
		lg.Err("--cleanup: prepare local git exclude: %v", err)
		return exitcode.Env
	}

	closed := 0
	failed := 0
	windows := map[string]struct{}{}
	defer relayoutClosedWindows(opts.Relayout, windows, lg)
	for _, taskID := range sortedTaskIDs(eligible) {
		taskPanes := panesSharingManagedWorktrees(locked.Panes, panesForTask(panes, taskID))
		if !cleanupPaneRecordsLocked(opts, locked, taskPanes, lg, windows) {
			failed++
			continue
		}
		if err := retirePaneStateRows(opts, locked, taskPanes, CloseWorktree); err != nil {
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

func validateState(mode string, opts Options, lg Logger) exitcode.Code {
	if opts.ProjectRoot == "" || !dirExists(opts.ProjectRoot) {
		lg.Err("%s: project_root is not a directory: %s (state=%s)", mode, emptyLabel(opts.ProjectRoot), opts.StatePath)
		return exitcode.Invocation
	}
	_, err := state.Load(opts.StatePath)
	if err != nil {
		lg.Err("%s: fanout state at %s is not valid JSON or has an invalid schema: %v", mode, opts.StatePath, err)
		return exitcode.Invocation
	}
	return exitcode.OK
}

func lockStateOnly(mode string, opts Options, lg Logger) (*state.LockedStore, exitcode.Code) {
	if opts.ProjectRoot == "" || !dirExists(opts.ProjectRoot) {
		lg.Err("%s: project_root is not a directory: %s (state=%s)", mode, emptyLabel(opts.ProjectRoot), opts.StatePath)
		return nil, exitcode.Invocation
	}
	locked, err := lockStateAfterJournalPrecheck(opts, stateFileNeedsJournalLock(opts.StatePath))
	if err != nil {
		lg.Err("%s: %v", mode, err)
		return nil, exitcode.Env
	}
	return locked, exitcode.OK
}

func lockStateAfterJournalPrecheck(opts Options, prechecked bool) (*state.LockedStore, error) {
	if prechecked {
		return state.LockProjectForLaunchAt(opts.ProjectRoot, opts.StatePath)
	}
	locked, err := state.Lock(opts.StatePath)
	if err != nil || !storeNeedsJournalLock(locked.Store) {
		return locked, err
	}
	if err := locked.Unlock(); err != nil {
		return nil, fmt.Errorf("unlock state before Herdr combined lock: %w", err)
	}
	return state.LockProjectForLaunchAt(opts.ProjectRoot, opts.StatePath)
}

func stateFileNeedsJournalLock(path string) bool {
	store, err := state.Load(path)
	if err != nil {
		return false
	}
	return storeNeedsJournalLock(store)
}

func storeNeedsJournalLock(store state.Store) bool {
	return slices.ContainsFunc(store.Panes, workspaceRuntimeRow)
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

func cleanupPaneRecordsLocked(opts Options, locked *state.LockedStore, panes []state.Pane, lg Logger, windows map[string]struct{}) bool {
	return closePaneRecordsLocked(opts, locked, panes, CloseWorktree, lg, windows)
}

// closePaneRecords stops every target pane before removing any worktree. This
// two-phase ordering prevents a partially failed close from deleting a cwd
// underneath an agent that is still running. The caller removes state only
// after this function succeeds, so a tmux inspection/close failure remains
// retryable with both the worktree and state row intact.
func closePaneRecordsLocked(opts Options, locked *state.LockedStore, panes []state.Pane, mode CloseMode, lg Logger, windows map[string]struct{}) bool {
	if !runBeforeWorktreeRemoveHooks(opts, locked, panes, mode, lg) {
		return false
	}
	if !closeSharedAttachedWorkspaceRows(opts, locked, panes, mode, lg) {
		return false
	}
	if !closeRuntimePanes(opts, panes, mode, lg, windows) {
		return false
	}
	return !mode.removesWorktree() || removeManagedWorktrees(opts, locked, panes, mode, lg)
}

func runBeforeWorktreeRemoveHooks(opts Options, locked *state.LockedStore, panes []state.Pane, mode CloseMode, lg Logger) bool {
	if !mode.removesWorktree() {
		return true
	}
	for _, pane := range panes {
		if pane.IsShell() || pane.IsAttachedAgent() || !recordedWorktreeExists(pane) {
			continue
		}
		if workspaceRuntimeRow(pane) {
			if !runWorkspaceCleanupHook(opts, locked, pane, mode, hooks.BeforeWorktreeRemove, lg) {
				return false
			}
			continue
		}
		if !runBlockingHook(hooks.BeforeWorktreeRemove, opts, pane, "", lg) {
			return false
		}
	}
	return true
}

func closeRuntimePanes(opts Options, panes []state.Pane, mode CloseMode, lg Logger, windows map[string]struct{}) bool {
	for _, pane := range panes {
		if workspaceRuntimeRow(pane) && mode.removesWorktree() {
			continue
		}
		runBackgroundHook(hooks.BeforePaneClose, opts, pane, "", lg)
		if !closeOwnedPane(opts, pane, lg, windows) {
			return false
		}
		runBackgroundHook(hooks.PaneClosed, opts, pane, "", lg)
	}
	return true
}

func removeManagedWorktrees(opts Options, locked *state.LockedStore, panes []state.Pane, mode CloseMode, lg Logger) bool {
	for _, pane := range panes {
		if pane.IsShell() || pane.IsAttachedAgent() {
			continue
		}
		if !removeManagedWorktree(opts, locked, pane, mode, lg) {
			return false
		}
	}
	return true
}

func removeManagedWorktree(opts Options, locked *state.LockedStore, pane state.Pane, mode CloseMode, lg Logger) bool {
	if workspaceRuntimeRow(pane) {
		if !runWorkspaceCleanupHook(opts, locked, pane, mode, hooks.BeforePaneClose, lg) {
			return false
		}
		if !closeWorkspaceWorktree(opts, locked, pane, mode, lg) {
			return false
		}
		if !completeWorkspaceCleanupHooks(opts, locked, pane, lg) {
			return false
		}
		return true
	}
	hadWorktree := recordedWorktreeExists(pane)
	if !removeWorktree(opts.ProjectRoot, pane, lg) {
		return false
	}
	if hadWorktree {
		runBackgroundHook(hooks.WorktreeRemoved, opts, pane, "", lg)
	}
	if mode == CloseEverything {
		_ = pruneWorktrees(opts.ProjectRoot, lg)
		deleteBranchBestEffort(opts.ProjectRoot, pane, lg)
	}
	return true
}

var (
	runWorkspaceBackgroundHook     = runBackgroundHook
	saveWorkspaceCleanupHookIntent = saveWorkspaceCleanupIntent
)

func runWorkspaceCleanupHook(opts Options, locked *state.LockedStore, pane state.Pane, mode CloseMode, hook hooks.Type, lg Logger) bool {
	if len(opts.Hooks.Events[hook]) == 0 {
		return allowUnconfiguredWorkspaceCleanupHook(opts, locked, pane, mode, lg)
	}
	journal, intent, err := prepareWorkspaceCleanupHook(opts, locked, pane, mode)
	if err != nil {
		lg.Err("%s: Herdr %s hook preflight failed: %v", paneLabel(pane), hook, err)
		return false
	}
	if rejectAmbiguousCleanupHookPhase(intent.CleanupHookPhase, pane, lg) {
		return false
	}
	issued, phase := cleanupHookCheckpoint(hook)
	if cleanupHookPhaseReached(intent.CleanupHookPhase, phase) {
		return true
	}
	hookPane := cleanupHookPane(pane, intent.Resource)
	if !persistCleanupHookPhase(journal, &intent, issued, hookPane, lg) {
		return false
	}
	if hook == hooks.BeforeWorktreeRemove {
		if !runBlockingHook(hook, opts, hookPane, "", lg) {
			return false
		}
	} else {
		runWorkspaceBackgroundHook(hook, opts, hookPane, "", lg)
	}
	return persistCleanupHookPhase(journal, &intent, phase, hookPane, lg)
}

func allowUnconfiguredWorkspaceCleanupHook(
	opts Options,
	locked *state.LockedStore,
	pane state.Pane,
	mode CloseMode,
	lg Logger,
) bool {
	journal, err := locked.LaunchJournal(opts.ProjectRoot)
	if err != nil {
		lg.Err("%s: load Herdr cleanup hook phase: %v", paneLabel(pane), err)
		return false
	}
	_, intentID, err := workspaceCleanupIntentIDs(opts.ProjectRoot, pane)
	if err != nil {
		lg.Err("%s: load Herdr cleanup hook identity: %v", paneLabel(pane), err)
		return false
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		return true
	}
	if err := validateSavedWorkspaceCleanup(intent, opts.ProjectRoot, pane, mode); err != nil {
		lg.Err("%s: validate Herdr cleanup hook phase: %v", paneLabel(pane), err)
		return false
	}
	return !rejectAmbiguousCleanupHookPhase(intent.CleanupHookPhase, pane, lg)
}

func cleanupHookCheckpoint(hook hooks.Type) (state.CleanupHookPhase, state.CleanupHookPhase) {
	if hook == hooks.BeforeWorktreeRemove {
		return state.CleanupHookBeforeWorktreeRemoveIssued, state.CleanupHookBeforeWorktreeRemove
	}
	return state.CleanupHookBeforePaneCloseIssued, state.CleanupHookBeforePaneClose
}

func rejectAmbiguousCleanupHookPhase(phase state.CleanupHookPhase, pane state.Pane, lg Logger) bool {
	if phase != state.CleanupHookBeforeWorktreeRemoveIssued &&
		phase != state.CleanupHookBeforePaneCloseIssued &&
		phase != state.CleanupHookPaneClosedIssued &&
		phase != state.CleanupHookWorktreeRemovedIssued {
		return false
	}
	lg.Err("%s: saved Herdr cleanup hook phase %q has an ambiguous dispatch outcome; refusing to replay", paneLabel(pane), phase)
	return true
}

func cleanupHookPane(pane state.Pane, resource state.RuntimeResource) state.Pane {
	pane.WorkspaceID = resource.WorkspaceID
	pane.PaneID = resource.PaneID
	pane.TerminalID = resource.TerminalID
	return pane
}

func persistCleanupHookPhase(
	journal *state.LockedLaunchJournal,
	intent *state.LaunchIntent,
	phase state.CleanupHookPhase,
	pane state.Pane,
	lg Logger,
) bool {
	intent.CleanupHookPhase = phase
	if err := saveWorkspaceCleanupHookIntent(journal, *intent); err != nil {
		lg.Err("%s: persist Herdr cleanup hook phase %q: %v", paneLabel(pane), phase, err)
		return false
	}
	return true
}

func cleanupHookPhaseReached(saved, want state.CleanupHookPhase) bool {
	if saved == "" {
		return want == state.CleanupHookBeforeWorktreeRemove || want == state.CleanupHookBeforePaneClose
	}
	phases := []state.CleanupHookPhase{
		state.CleanupHookPending,
		state.CleanupHookBeforeWorktreeRemove,
		state.CleanupHookBeforePaneClose,
		state.CleanupHookPaneClosed,
		state.CleanupHookWorktreeRemoved,
		state.CleanupHookCompleted,
	}
	savedIndex := slices.Index(phases, saved)
	wantIndex := slices.Index(phases, want)
	return savedIndex >= 0 && wantIndex >= 0 && savedIndex >= wantIndex
}

func completeWorkspaceCleanupHooks(opts Options, locked *state.LockedStore, pane state.Pane, lg Logger) bool {
	journal, err := locked.LaunchJournal(opts.ProjectRoot)
	if err != nil {
		lg.Err("%s: load Herdr cleanup hook phase: %v", paneLabel(pane), err)
		return false
	}
	_, intentID, err := workspaceCleanupIntentIDs(opts.ProjectRoot, pane)
	if err != nil {
		lg.Err("%s: load completed Herdr cleanup intent: %v", paneLabel(pane), err)
		return false
	}
	intent, found := journal.FindIntent(intentID)
	if !found {
		lg.Err("%s: completed Herdr cleanup intent is absent", paneLabel(pane))
		return false
	}
	if intent.CleanupHookPhase == state.CleanupHookCompleted {
		return true
	}
	if rejectAmbiguousCleanupHookPhase(intent.CleanupHookPhase, pane, lg) {
		return false
	}
	hookPane := cleanupHookPane(pane, intent.Resource)
	worktreeRemovedRequired, ok := prepareWorkspaceWorktreeRemovedHook(opts, &intent, hookPane, lg)
	if !ok {
		return false
	}
	if !workspaceCompletionHooksConfigured(opts, worktreeRemovedRequired) {
		return persistCleanupHookPhase(journal, &intent, state.CleanupHookCompleted, hookPane, lg)
	}
	return dispatchWorkspaceCompletionHooks(opts, journal, &intent, hookPane, worktreeRemovedRequired, lg)
}

func prepareWorkspaceWorktreeRemovedHook(
	opts Options,
	intent *state.LaunchIntent,
	pane state.Pane,
	lg Logger,
) (bool, bool) {
	configured := len(opts.Hooks.Events[hooks.WorktreeRemoved]) != 0
	if intent.CleanupWorktreeRemovedRequired == nil {
		if configured {
			lg.Err("%s: saved Herdr worktree_removed hook obligation is absent", paneLabel(pane))
			return false, false
		}
		required := false
		intent.CleanupWorktreeRemovedRequired = &required
		return false, true
	}
	if *intent.CleanupWorktreeRemovedRequired && !configured {
		lg.Err("%s: required Herdr worktree_removed hook is not configured", paneLabel(pane))
		return false, false
	}
	return *intent.CleanupWorktreeRemovedRequired, true
}

func dispatchWorkspaceCompletionHooks(
	opts Options,
	journal *state.LockedLaunchJournal,
	intent *state.LaunchIntent,
	pane state.Pane,
	worktreeRemovedRequired bool,
	lg Logger,
) bool {
	if !dispatchWorkspaceCompletionHook(
		opts, journal, intent, pane, hooks.PaneClosed,
		state.CleanupHookPaneClosedIssued, state.CleanupHookPaneClosed, lg,
	) {
		return false
	}
	if worktreeRemovedRequired && !dispatchWorkspaceCompletionHook(
		opts, journal, intent, pane, hooks.WorktreeRemoved,
		state.CleanupHookWorktreeRemovedIssued, state.CleanupHookWorktreeRemoved, lg,
	) {
		return false
	}
	return persistCleanupHookPhase(journal, intent, state.CleanupHookCompleted, pane, lg)
}

func dispatchWorkspaceCompletionHook(
	opts Options,
	journal *state.LockedLaunchJournal,
	intent *state.LaunchIntent,
	pane state.Pane,
	hook hooks.Type,
	issued, completed state.CleanupHookPhase,
	lg Logger,
) bool {
	if len(opts.Hooks.Events[hook]) == 0 || cleanupHookPhaseReached(intent.CleanupHookPhase, completed) {
		return true
	}
	if !persistCleanupHookPhase(journal, intent, issued, pane, lg) {
		return false
	}
	runWorkspaceBackgroundHook(hook, opts, pane, "", lg)
	return persistCleanupHookPhase(journal, intent, completed, pane, lg)
}

func workspaceCompletionHooksConfigured(opts Options, worktreeRemovedRequired bool) bool {
	return len(opts.Hooks.Events[hooks.PaneClosed]) != 0 ||
		worktreeRemovedRequired
}

func validateCloseOperations(opts Options, panes []state.Pane, mode CloseMode, lg Logger) bool {
	for _, pane := range panes {
		ref := paneRefFromState(pane)
		if workspaceRuntimeRow(pane) {
			if !validateWorkspaceRuntimeCloseOperation(opts, panes, pane, mode, lg) {
				return false
			}
			continue
		}
		// Every remaining row is closed by the atomic lane, which this build
		// realizes on tmux alone. A row recording any other runtime is refused
		// rather than closed through a runtime it never launched on.
		if ref.Backend != backend.Tmux {
			lg.Err("%s: runtime backend %s does not support lifecycle close", paneLabel(pane), ref.Backend)
			return false
		}
		if strings.TrimSpace(ref.Pane) == "" {
			continue
		}
		if opts.CloseOwned == nil {
			lg.Err("%s: identity-aware runtime pane close is not configured", paneLabel(pane))
			return false
		}
	}
	return true
}

func validateWorkspaceRuntimeCloseOperation(
	opts Options,
	panes []state.Pane,
	pane state.Pane,
	mode CloseMode,
	lg Logger,
) bool {
	sharedChild := !pane.IsAttachedAgent() && sharesAttachedWorkspaceRuntimeWorktree(pane, panes)
	sharedAttached := pane.IsAttachedAgent() && sharesWorkspaceRuntimeWorktree(pane, panes)
	if !sharedChild && !sharedAttached {
		return validateWorkspaceCloseOperation(opts, pane, mode, lg)
	}
	var err error
	if sharedChild {
		err = validateWorkspacePaneIdentity(pane)
	}
	if err != nil || opts.WorkspaceRuntime == nil {
		if err == nil {
			err = fmt.Errorf("herdr lifecycle runtime is not configured")
		}
		lg.Err("%s: %v; preserving workspace, worktree, and state", paneLabel(pane), err)
		return false
	}
	return true
}

func sharesWorkspaceRuntimeWorktree(pane state.Pane, panes []state.Pane) bool {
	path := normalizedWorktreePath(pane.WorktreePath)
	return path != "" && slices.ContainsFunc(panes, func(candidate state.Pane) bool {
		return workspaceRuntimeRow(candidate) && !candidate.IsShell() && !candidate.IsAttachedAgent() &&
			normalizedWorktreePath(candidate.WorktreePath) == path
	})
}

func sharesAttachedWorkspaceRuntimeWorktree(pane state.Pane, panes []state.Pane) bool {
	path := normalizedWorktreePath(pane.WorktreePath)
	return path != "" && slices.ContainsFunc(panes, func(candidate state.Pane) bool {
		return workspaceRuntimeRow(candidate) && candidate.IsAttachedAgent() &&
			normalizedWorktreePath(candidate.WorktreePath) == path
	})
}

func paneRefFromState(pane state.Pane) backend.PaneRef {
	return backend.PaneRef{
		Backend:   backend.NormalizeName(pane.Backend),
		Workspace: pane.WorkspaceID,
		Pane:      pane.PaneID,
	}
}

// relayoutClosedWindows re-tiles each affected window into the fanout grid.
// A window that emptied out (every pane killed) is gone, so the relayout
// no-ops on it. A runtime that lays itself out supplies no Relayout and the
// whole pass is skipped.
func relayoutClosedWindows(relayout func(string, backend.LayoutTrigger) error, windows map[string]struct{}, lg Logger) {
	if relayout == nil {
		return
	}
	for id := range windows {
		if err := relayout(id, backend.LayoutClose); err != nil {
			lg.Warn("relayout window %s: %v", id, err)
		}
	}
}

func (m CloseMode) removesWorktree() bool {
	return m == CloseWorktree || m == CloseEverything
}

func hasManagedWorktree(panes []state.Pane) bool {
	for _, pane := range panes {
		if !pane.IsShell() && !pane.IsAttachedAgent() {
			return true
		}
	}
	return false
}

func cleanupIssuePanes(panes []state.Pane) []state.Pane {
	var out []state.Pane
	for _, pane := range panes {
		if pane.IsShell() || pane.IsAttachedAgent() || pane.IssueNum <= 0 {
			continue
		}
		out = append(out, pane)
	}
	return out
}

func panesSharingManagedWorktrees(allPanes, managedPanes []state.Pane) []state.Pane {
	worktrees := map[string]bool{}
	out := make([]state.Pane, 0, len(managedPanes))
	seen := map[string]bool{}
	for _, pane := range managedPanes {
		if !pane.IsShell() && !pane.IsAttachedAgent() {
			if path := normalizedWorktreePath(pane.WorktreePath); path != "" {
				worktrees[path] = true
			}
		}
		key := paneStateKey(pane)
		if !seen[key] {
			seen[key] = true
			out = append(out, pane)
		}
	}
	if len(worktrees) == 0 {
		return out
	}
	for _, pane := range allPanes {
		if !pane.IsAttachedAgent() || !worktrees[normalizedWorktreePath(pane.WorktreePath)] {
			continue
		}
		key := paneStateKey(pane)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, pane)
	}
	return out
}

func normalizedWorktreePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func paneStateKey(pane state.Pane) string {
	return pane.Parent + "\x00" + strconv.Itoa(pane.IssueNum) + "\x00" + pane.TaskID + "\x00" + pane.Kind + "\x00" + pane.PaneID
}

func removePaneStateRows(locked *state.LockedStore, panes []state.Pane) error {
	targets := make(map[string]bool, len(panes))
	for _, pane := range panes {
		targets[paneRetirementKey(pane)] = true
	}
	kept := locked.Panes[:0]
	for _, pane := range locked.Panes {
		if !targets[paneRetirementKey(pane)] {
			kept = append(kept, pane)
		}
	}
	locked.Panes = kept
	return locked.Save()
}

func paneRetirementKey(pane state.Pane) string {
	if strings.TrimSpace(pane.TaskID) != "" && pane.IssueNum == 0 && !pane.IsAttachedAgent() {
		return pane.Parent + "\x00task\x00" + pane.TaskID
	}
	return pane.Parent + "\x00issue\x00" + strconv.Itoa(pane.IssueNum)
}

func prepareCompletedWorkspaceRetirement(
	opts Options,
	locked *state.LockedStore,
	panes []state.Pane,
	mode CloseMode,
	subject string,
	lg Logger,
) ([]state.Pane, exitcode.Code, bool) {
	if mode.removesWorktree() {
		panes = panesSharingManagedWorktrees(locked.Panes, panes)
	}
	retired, err := retireCompletedWorkspaceCleanup(opts, locked, panes, mode, lg)
	if err != nil {
		lg.Err("%s: retire completed Herdr cleanup: %v", subject, err)
		return panes, exitcode.Env, true
	}
	if retired {
		lg.Ok("%s: retired completed Herdr cleanup state", subject)
	}
	return panes, exitcode.OK, retired
}

func retireCompletedIssueCleanups(
	opts Options,
	locked *state.LockedStore,
	parent string,
	panes []state.Pane,
	lg Logger,
) ([]state.Pane, int, error) {
	retired := 0
	for _, issueNum := range sortedUnique(paneIssueNumbers(panes)) {
		group := panesSharingManagedWorktrees(locked.Panes, panesForIssue(panes, issueNum))
		done, err := retireCompletedWorkspaceCleanup(opts, locked, group, CloseWorktree, lg)
		if err != nil {
			return nil, retired, fmt.Errorf("#%d: %w", issueNum, err)
		}
		if done {
			retired++
		}
	}
	return cleanupIssuePanes(locked.PanesForParent(parent)), retired, nil
}

func paneIssueNumbers(panes []state.Pane) []int {
	nums := make([]int, 0, len(panes))
	for _, pane := range panes {
		nums = append(nums, pane.IssueNum)
	}
	return nums
}

func retireCompletedTaskCleanups(
	opts Options,
	locked *state.LockedStore,
	parent string,
	panes []state.Pane,
	lg Logger,
) ([]state.Pane, int, error) {
	tasks := map[string]bool{}
	for _, pane := range panes {
		tasks[strings.TrimSpace(pane.TaskID)] = true
	}
	retired := 0
	for _, taskID := range sortedTaskIDs(tasks) {
		group := panesSharingManagedWorktrees(locked.Panes, panesForTask(panes, taskID))
		done, err := retireCompletedWorkspaceCleanup(opts, locked, group, CloseWorktree, lg)
		if err != nil {
			return nil, retired, fmt.Errorf("%s: %w", taskID, err)
		}
		if done {
			retired++
		}
	}
	return taskPanesForParent(locked.PanesForParent(parent)), retired, nil
}

func retireCompletedWorkspaceCleanup(
	opts Options,
	locked *state.LockedStore,
	panes []state.Pane,
	mode CloseMode,
	lg Logger,
) (bool, error) {
	if !mode.removesWorktree() {
		return false, nil
	}
	managed, eligible := workspaceRetirementManagedCount(panes)
	if !eligible || managed == 0 {
		return false, nil
	}
	journal, err := locked.LaunchJournal(opts.ProjectRoot)
	if err != nil {
		return false, err
	}
	completed, err := countCompletedWorkspaceCleanups(journal, opts.ProjectRoot, panes, mode)
	if err != nil {
		return false, err
	}
	if completed == 0 {
		return false, nil
	}
	if completed != managed {
		return false, fmt.Errorf("completed Herdr cleanup does not cover every selected state row")
	}
	if !pruneWorktrees(opts.ProjectRoot, lg) {
		return false, errors.New("git worktree prune failed")
	}
	return true, retirePaneStateRows(opts, locked, panes, mode)
}

func workspaceRetirementManagedCount(panes []state.Pane) (int, bool) {
	managed := 0
	for _, pane := range panes {
		switch {
		case pane.IsAttachedAgent():
			continue
		case pane.IsShell() || !workspaceRuntimeRow(pane):
			return 0, false
		default:
			managed++
		}
	}
	return managed, true
}

func countCompletedWorkspaceCleanups(
	journal *state.LockedLaunchJournal,
	projectRoot string,
	panes []state.Pane,
	mode CloseMode,
) (int, error) {
	completed := 0
	for _, pane := range panes {
		if pane.IsAttachedAgent() {
			continue
		}
		done, err := completedWorkspaceCleanupMatches(journal, projectRoot, pane, mode)
		if err != nil {
			return 0, err
		}
		if done {
			completed++
		}
	}
	return completed, nil
}

func completedWorkspaceCleanupMatches(
	journal *state.LockedLaunchJournal,
	projectRoot string,
	pane state.Pane,
	mode CloseMode,
) (bool, error) {
	// Legacy task rows can carry a negative issue number, but that shape cannot
	// have a cleanup intent. Leave it to the normal lifecycle validation path.
	if strings.TrimSpace(pane.TaskID) != "" && pane.IssueNum != 0 {
		return false, nil
	}
	_, cleanupID, err := workspaceCleanupIntentIDs(projectRoot, pane)
	if err != nil {
		return false, err
	}
	intent, found := journal.FindIntent(cleanupID)
	if !found || intent.Status != state.IntentRealized || intent.CleanupHookPhase != state.CleanupHookCompleted {
		return false, nil
	}
	if err := validateWorkspacePaneIdentity(pane); err != nil {
		return false, err
	}
	if err := validateSavedWorkspaceCleanup(intent, projectRoot, pane, mode); err != nil {
		return false, err
	}
	return true, nil
}

func retirePaneStateRows(opts Options, locked *state.LockedStore, panes []state.Pane, mode CloseMode) error {
	previous := locked.Store
	previous.Panes = append([]state.Pane(nil), locked.Panes...)
	if err := removePaneStateRows(locked, panes); err != nil {
		return restorePaneStateAfterRetirementFailure(locked, previous, err)
	}
	if !mode.removesWorktree() || !slices.ContainsFunc(panes, workspaceRuntimeRow) {
		return nil
	}
	if err := retireWorkspaceIntents(opts, locked, panes); err != nil {
		return restorePaneStateAfterRetirementFailure(locked, previous, err)
	}
	return nil
}

func retireWorkspaceIntents(opts Options, locked *state.LockedStore, panes []state.Pane) error {
	journal, err := locked.LaunchJournal(opts.ProjectRoot)
	if err != nil {
		return err
	}
	for _, pane := range panes {
		if !workspaceRuntimeRow(pane) || pane.IsShell() || pane.IsAttachedAgent() {
			continue
		}
		worktreeID, cleanupID, err := workspaceCleanupIntentIDs(opts.ProjectRoot, pane)
		if err != nil {
			return err
		}
		journal.RemoveIntent(cleanupID)
		journal.RemoveIntent(worktreeID)
	}
	return journal.Save()
}

func restorePaneStateAfterRetirementFailure(
	locked *state.LockedStore,
	previous state.Store,
	cause error,
) error {
	locked.Store = previous
	if err := locked.Save(); err != nil {
		return errors.Join(cause, fmt.Errorf("restore fanout state after retirement failure: %w", err))
	}
	return cause
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

func deleteBranchBestEffort(projectRoot string, pane state.Pane, lg Logger) {
	branch := strings.TrimSpace(pane.BranchName)
	if branch == "" {
		lg.Warn("%s: no branchName recorded; skipping local branch delete", paneLabel(pane))
		return
	}
	if !localBranchExists(projectRoot, branch) {
		lg.Warn("%s: local branch already absent: %s", paneLabel(pane), branch)
		return
	}
	out, err := gitLifecycle(projectRoot, "branch", "-D", branch)
	if err != nil {
		lg.Warn("%s: git branch -D %s failed; leaving branch in place", paneLabel(pane), branch)
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintln(lg.Stderr(), s)
		}
		return
	}
}

func localBranchExists(projectRoot, branch string) bool {
	err := exec.Command("git", "-C", projectRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run()
	return err == nil
}

func closeOwnedPane(opts Options, pane state.Pane, lg Logger, windows map[string]struct{}) bool {
	if strings.TrimSpace(pane.PaneID) == "" {
		lg.Warn("%s: no pane id recorded; treating state as stale", paneLabel(pane))
		return true
	}
	result, err := opts.CloseOwned(backend.CloseRequest{
		Ref:          paneRefFromState(pane),
		WorktreePath: pane.WorktreePath,
		ShellKey:     strings.TrimSpace(pane.ShellKey),
	})
	if err != nil {
		lg.Err("%s: close tmux pane %s failed; preserving worktree and state: %v", paneLabel(pane), emptyLabel(pane.PaneID), err)
		return false
	}
	switch result.Status {
	case backend.CloseFailed:
		lg.Err("%s: close tmux pane %s was not confirmed; preserving worktree and state", paneLabel(pane), emptyLabel(pane.PaneID))
		return false
	case backend.CloseStale:
		lg.Warn("%s: pane %s is gone or its identity changed; treating state as stale", paneLabel(pane), emptyLabel(pane.PaneID))
		return true
	case backend.CloseConfirmed:
		// Continue below and record the container for cosmetic relayout.
	default:
		lg.Err("%s: close tmux pane %s returned unknown status %d; preserving worktree and state", paneLabel(pane), emptyLabel(pane.PaneID), result.Status)
		return false
	}
	if result.ContainerID != "" {
		windows[result.ContainerID] = struct{}{}
	}
	return true
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
		PaneID:       pane.PaneID,
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

func removeWatcherRunningLabelBestEffort(opts Options, parent string, issueNum int, remainingParentPanes []state.Pane, lg Logger) {
	issueNum, ok := watcherRunningLabelTarget(parent, issueNum, remainingParentPanes)
	if !ok {
		return
	}
	label := strings.TrimSpace(opts.WatcherRunningLabel)
	remove := opts.RemoveIssueLabel
	if label == "" || remove == nil {
		return
	}
	if err := remove(issueNum, label); err != nil {
		lg.Warn("#%d: remove watcher running label %q: %v", issueNum, label, err)
	}
}

func watcherRunningLabelTarget(parent string, issueNum int, remainingParentPanes []state.Pane) (int, bool) {
	if issueNum <= 0 {
		return 0, false
	}
	if strings.TrimSpace(parent) == watcherStandaloneParent {
		return issueNum, true
	}
	parentNum, err := strconv.Atoi(strings.TrimSpace(parent))
	if err != nil || parentNum <= 0 {
		return 0, false
	}
	if hasIssuePanes(remainingParentPanes) {
		return 0, false
	}
	return parentNum, true
}

func remainingIssuePanesAfter(panes []state.Pane, issueNum int) []state.Pane {
	remaining := make([]state.Pane, 0, len(panes))
	for _, pane := range panes {
		if pane.IssueNum == issueNum {
			continue
		}
		remaining = append(remaining, pane)
	}
	return remaining
}

func hasIssuePanes(panes []state.Pane) bool {
	for _, pane := range panes {
		if pane.IssueNum > 0 {
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
