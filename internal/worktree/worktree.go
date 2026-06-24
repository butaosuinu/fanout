// Package worktree prepares git worktrees and fresh base branches for fanout.
package worktree

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const localExcludePattern = ".fanout/worktrees/"

var localExcludePatterns = []string{
	".fanout/.fanout-*.tmp",
	".fanout/state.json",
	".fanout/state.json.lock",
	".fanout/worktree-metadata.json",
	".fanout/dashboard.json",
	".fanout/dashboard.json.lock",
	".fanout/plans/",
	localExcludePattern,
}

type Options struct {
	ProjectRoot        string
	Slug               string
	BranchName         string
	BaseBranch         string
	NoRefresh          bool
	AllowMissingOrigin bool
	// RefreshBestEffort downgrades a failed base-branch refresh from a fatal
	// error to a skipped step, so the worktree is still created from the
	// un-refreshed local base. Manual TUI panes set this; issue/plan children
	// keep the strict fail-on-dirty-base behavior.
	RefreshBestEffort bool
}

type Plan struct {
	ProjectRoot          string
	WorktreePath         string
	BranchName           string
	BaseBranch           string
	AllowMissingOrigin   bool
	Refresh              bool
	RefreshBestEffort    bool
	RefreshDetails       RefreshDetails
	RefreshError         error
	RefreshSkippedReason string
}

type Result struct {
	Plan
	AlreadyExists bool
	BranchCreated bool
	// RefreshWarning holds the refresh error that was tolerated because
	// RefreshBestEffort was set; nil when refresh succeeded or was not run.
	RefreshWarning error
}

// BuildPlan resolves deterministic worktree paths and the base branch.
func BuildPlan(opts Options) Plan {
	base, missingOrigin := resolveBaseBranch(opts)
	refresh := !opts.NoRefresh
	refreshSkippedReason := ""
	var refreshDetails RefreshDetails
	var refreshErr error
	if missingOrigin {
		if originQualifiedBase(base) {
			refreshErr = fmt.Errorf("base branch %q requires origin remote, but origin is not configured", base)
			refresh = true
		} else {
			refresh = false
			refreshSkippedReason = "origin remote is not configured; using local base without refresh"
		}
	} else {
		refreshDetails, refreshErr = RefreshDetailsFor(base)
	}
	return Plan{
		ProjectRoot:          opts.ProjectRoot,
		WorktreePath:         filepath.Join(opts.ProjectRoot, ".fanout", "worktrees", opts.Slug),
		BranchName:           opts.BranchName,
		BaseBranch:           base,
		AllowMissingOrigin:   opts.AllowMissingOrigin,
		Refresh:              refresh,
		RefreshBestEffort:    opts.RefreshBestEffort,
		RefreshDetails:       refreshDetails,
		RefreshError:         refreshErr,
		RefreshSkippedReason: refreshSkippedReason,
	}
}

// Prepare refreshes the base branch and creates the child worktree.
func Prepare(opts Options) (Result, error) {
	plan := BuildPlan(opts)
	if err := EnsureLocalExclude(plan.ProjectRoot); err != nil {
		return Result{Plan: plan}, err
	}
	exists, err := pathExists(plan.WorktreePath)
	if err != nil {
		return Result{Plan: plan}, err
	}
	if exists {
		return Result{Plan: plan, AlreadyExists: true}, nil
	}
	var refreshWarning error
	if plan.Refresh {
		refreshErr := plan.RefreshError
		if refreshErr == nil {
			refreshErr = RefreshBase(plan.ProjectRoot, plan.BaseBranch)
		}
		if refreshErr != nil {
			// Best-effort downgrades a failed refresh to a warning so a manual
			// pane is still created off the un-refreshed local base. Only tolerate
			// it when the base is still usable for branching: a failure that left
			// the base unresolvable (e.g. a fetch failure before the local base
			// branch existed) must surface the clearer refresh error instead of a
			// confusing 'git worktree add' failure later.
			if !plan.RefreshBestEffort || !baseResolvable(plan.ProjectRoot, plan.BaseBranch) {
				return Result{Plan: plan}, refreshErr
			}
			refreshWarning = refreshErr
		}
	}
	if err := os.MkdirAll(filepath.Dir(plan.WorktreePath), 0o755); err != nil {
		return Result{Plan: plan, RefreshWarning: refreshWarning}, fmt.Errorf("create worktree parent: %w", err)
	}
	_, _ = git(plan.ProjectRoot, "worktree", "prune")
	branchWasPresent := branchExists(plan.ProjectRoot, plan.BranchName)
	args := []string{"worktree", "add"}
	if branchWasPresent {
		args = append(args, plan.WorktreePath, plan.BranchName)
	} else {
		args = append(args, "-b", plan.BranchName, plan.WorktreePath, plan.BaseBranch)
	}
	if _, err := git(plan.ProjectRoot, args...); err != nil {
		_, _ = git(plan.ProjectRoot, "worktree", "prune")
		if !branchWasPresent {
			_, _ = git(plan.ProjectRoot, "branch", "-D", plan.BranchName)
		}
		return Result{Plan: plan, RefreshWarning: refreshWarning}, fmt.Errorf("git worktree add: %w", err)
	}
	return Result{Plan: plan, BranchCreated: !branchWasPresent, RefreshWarning: refreshWarning}, nil
}

// EnsureLocalExclude keeps generated fanout runtime files out of the user's git status.
func EnsureLocalExclude(root string) (err error) {
	excludePath, err := gitTrim(root, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve git exclude path: %w", err)
	}
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(root, excludePath)
	}

	body, err := os.ReadFile(excludePath)
	switch {
	case err == nil && len(missingExcludePatterns(body)) == 0:
		return nil
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("read git exclude: %w", err)
	}
	missing := missingExcludePatterns(body)

	if err = os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create git exclude directory: %w", err)
	}
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open git exclude: %w", err)
	}
	// This is a write path: a failed Close can mean the appended patterns were
	// never flushed, so propagate it instead of discarding.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close git exclude: %w", cerr)
		}
	}()

	if len(body) > 0 && body[len(body)-1] != '\n' {
		if _, err = f.WriteString("\n"); err != nil {
			return fmt.Errorf("append git exclude newline: %w", err)
		}
	}
	for _, pattern := range missing {
		if _, err = f.WriteString(pattern + "\n"); err != nil {
			return fmt.Errorf("append git exclude pattern: %w", err)
		}
	}
	return nil
}

// CleanupCreated removes resources created by Prepare after a later launch failure.
func CleanupCreated(res Result) error {
	var errs []error
	plan := res.Plan
	if dirExists(plan.WorktreePath) {
		if _, err := git(plan.ProjectRoot, "worktree", "remove", "--force", plan.WorktreePath); err != nil {
			errs = append(errs, fmt.Errorf("git worktree remove: %w", err))
		}
	}
	if res.BranchCreated && plan.BranchName != "" {
		if _, err := git(plan.ProjectRoot, "branch", "-D", plan.BranchName); err != nil {
			errs = append(errs, fmt.Errorf("git branch -D %s: %w", plan.BranchName, err))
		}
	}
	_, _ = git(plan.ProjectRoot, "worktree", "prune")
	return errors.Join(errs...)
}

func missingExcludePatterns(body []byte) []string {
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	for sc.Scan() {
		seen[strings.TrimSpace(sc.Text())] = true
	}
	var missing []string
	for _, pattern := range localExcludePatterns {
		if !seen[pattern] {
			missing = append(missing, pattern)
		}
	}
	return missing
}

// ResolveDefaultBranch follows fanout's default branch fallback order.
func ResolveDefaultBranch(root string) string {
	return resolveDefaultBranch(root, false)
}

// ResolveDefaultBranchAllowMissingOrigin returns the current local branch when
// no origin remote exists, for local-only plan/manual pane runs.
func ResolveDefaultBranchAllowMissingOrigin(root string) string {
	return resolveDefaultBranch(root, true)
}

func resolveDefaultBranch(root string, allowMissingOrigin bool) string {
	if allowMissingOrigin && !hasOriginRemote(root) {
		return localBaseRef(root)
	}
	cmd := exec.Command("gh", "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" && s != "null" {
			return s
		}
	}
	if out, err := git(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		s := strings.TrimSpace(string(out))
		s = strings.TrimPrefix(s, "origin/")
		if s != "" {
			return s
		}
	}
	return "main"
}

func resolveBaseBranch(opts Options) (string, bool) {
	missingOrigin := opts.AllowMissingOrigin && !hasOriginRemote(opts.ProjectRoot)
	base := opts.BaseBranch
	if base == "" {
		if missingOrigin {
			return localBaseRef(opts.ProjectRoot), true
		}
		return ResolveDefaultBranch(opts.ProjectRoot), false
	}
	return base, missingOrigin
}

func hasOriginRemote(root string) bool {
	_, err := git(root, "remote", "get-url", "origin")
	return err == nil
}

func localBaseRef(root string) string {
	if branch, err := gitTrim(root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && branch != "" {
		return branch
	}
	return "HEAD"
}

func originQualifiedBase(base string) bool {
	return strings.HasPrefix(base, "origin/") || strings.HasPrefix(base, "refs/remotes/origin/")
}

type RefreshDetails struct {
	Base        string
	FetchBranch string
	OriginRef   string
	LocalBranch string
}

// RefreshDetailsFor describes how a base branch should be refreshed.
func RefreshDetailsFor(base string) (RefreshDetails, error) {
	if base == "" {
		base = "main"
	}
	switch {
	case strings.HasPrefix(base, "refs/remotes/origin/"):
		branch := strings.TrimPrefix(base, "refs/remotes/origin/")
		if branch == "" {
			return RefreshDetails{}, fmt.Errorf("base branch must include a branch name after refs/remotes/origin/")
		}
		return RefreshDetails{Base: base, FetchBranch: branch, OriginRef: "refs/remotes/origin/" + branch}, nil
	case strings.HasPrefix(base, "origin/"):
		branch := strings.TrimPrefix(base, "origin/")
		if branch == "" {
			return RefreshDetails{}, fmt.Errorf("base branch must include a branch name after origin/")
		}
		return RefreshDetails{Base: base, FetchBranch: branch, OriginRef: "refs/remotes/origin/" + branch}, nil
	case strings.HasPrefix(base, "refs/"):
		return RefreshDetails{}, fmt.Errorf("base branch %q is not refreshable; use a local branch name, origin/<branch>, or --no-refresh for a local ref", base)
	default:
		return RefreshDetails{
			Base:        base,
			FetchBranch: base,
			OriginRef:   "refs/remotes/origin/" + base,
			LocalBranch: base,
		}, nil
	}
}

// RefreshBase fetches origin/base and fast-forwards a local base branch when applicable.
func RefreshBase(root, base string) error {
	details, err := RefreshDetailsFor(base)
	if err != nil {
		return err
	}
	if _, err = git(root, "fetch", "--quiet", "--no-tags", "origin", details.FetchBranch); err != nil {
		return fmt.Errorf("git fetch origin %s: %w", details.FetchBranch, err)
	}
	originSHA, err := gitTrim(root, "rev-parse", "--verify", details.OriginRef)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", details.OriginRef, err)
	}
	if details.LocalBranch == "" {
		return nil
	}

	localRef := "refs/heads/" + details.LocalBranch
	localSHA, err := gitTrim(root, "rev-parse", "--verify", localRef)
	if err != nil {
		if _, err = git(root, "branch", details.LocalBranch, originSHA); err != nil {
			return fmt.Errorf("create local base branch %s: %w", details.LocalBranch, err)
		}
		return nil
	}
	if localSHA == originSHA {
		return nil
	}
	mergeBase, err := gitTrim(root, "merge-base", localSHA, originSHA)
	if err != nil {
		return fmt.Errorf("check fast-forward for %s: %w", details.LocalBranch, err)
	}
	if mergeBase != localSHA {
		return fmt.Errorf("local branch %s has diverged from origin/%s", details.LocalBranch, details.FetchBranch)
	}
	if _, err = git(root, "branch", "-f", details.LocalBranch, originSHA); err == nil {
		return nil
	}

	checkedOutPath := checkedOutWorktree(root, details.LocalBranch)
	if checkedOutPath == "" {
		return fmt.Errorf("local branch %s is checked out and could not be located", details.LocalBranch)
	}
	status, err := gitTrim(checkedOutPath, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("check %s cleanliness: %w", checkedOutPath, err)
	}
	if status != "" {
		return fmt.Errorf("local branch %s is checked out at %s with uncommitted changes; refusing to fast-forward", details.LocalBranch, checkedOutPath)
	}
	if _, err := git(checkedOutPath, "merge", "--ff-only", "--quiet", originSHA); err != nil {
		return fmt.Errorf("fast-forward %s at %s: %w", details.LocalBranch, checkedOutPath, err)
	}
	return nil
}

// ListRoots returns the absolute paths of every git worktree sharing
// projectRoot's repository, so dashboard-style surfaces can aggregate the
// .fanout/state.json each worktree records independently. It excludes fanout's
// own child worktrees (those under a ".fanout/worktrees/" segment, whose state
// is recorded in the owner, not the child) and always includes projectRoot
// itself. Worktrees git no longer treats as valid working trees are skipped:
// the bare main repository (no working tree) and prunable entries (the directory
// is gone or stale), so the dashboard/TUI never resurrect panes from a worktree
// that lifecycle actions could not safely target. On a `git worktree list`
// failure it returns {projectRoot} alongside the error so callers degrade to a
// single-root load.
func ListRoots(projectRoot string) ([]string, error) {
	out, err := git(projectRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return []string{projectRoot}, err
	}
	roots := []string{projectRoot}
	seen := map[string]bool{projectRoot: true}
	var current string
	skip := false
	childMarker := string(filepath.Separator) + filepath.FromSlash(localExcludePattern)
	flush := func() {
		path := current
		drop := skip
		current, skip = "", false
		if path == "" || drop {
			return
		}
		if seen[path] || strings.Contains(path, childMarker) {
			return
		}
		seen[path] = true
		roots = append(roots, path)
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			flush()
			current = after
			continue
		}
		// `bare` and `prunable [<reason>]` are stanza attribute lines; either
		// means the worktree has no usable working tree to read state from.
		if line == "bare" || line == "prunable" || strings.HasPrefix(line, "prunable ") {
			skip = true
		}
	}
	flush()
	return roots, nil
}

// ListFiles returns repository-relative paths that are tracked or untracked but
// not gitignored, via `git ls-files --cached --others --exclude-standard -z`.
// The NUL-delimited (-z) form avoids git's path quoting so callers receive raw
// relative paths even when they contain spaces or other special characters. It
// reads stdout only (not the shared CombinedOutput git helper) so a stderr
// advice/warning line on a zero exit cannot fuse into a path entry.
func ListFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(out), "\x00")
	files := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		files = append(files, p)
	}
	return files, nil
}

func checkedOutWorktree(root, branch string) string {
	out, err := git(root, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	var current string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			current = after
			continue
		}
		if strings.TrimPrefix(line, "branch refs/heads/") == branch {
			return current
		}
	}
	return ""
}

func gitTrim(dir string, args ...string) (string, error) {
	out, err := git(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return out, err
		}
		return out, fmt.Errorf("%w: %s", err, msg)
	}
	return out, nil
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func pathExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat worktree path: %w", err)
	}
	return true, nil
}

func branchExists(root, branch string) bool {
	if branch == "" {
		return false
	}
	_, err := gitTrim(root, "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

// baseResolvable reports whether base names a commit git can branch from, so a
// best-effort refresh failure is only tolerated when the resulting worktree add
// can still succeed.
func baseResolvable(root, base string) bool {
	if base == "" {
		return false
	}
	_, err := gitTrim(root, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	return err == nil
}
