// Package worktree prepares git worktrees and fresh base branches for fanout.
package worktree

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/infra/execx"
)

const localExcludePattern = ".fanout/worktrees/"

var localExcludePatterns = []string{
	".fanout/.fanout-*.tmp",
	".fanout/state.json",
	".fanout/state.json.lock",
	".fanout/state.json.sequence",
	".fanout/worktree-metadata.json",
	".fanout/dashboard.json",
	".fanout/dashboard.json.lock",
	".fanout/plans/",
	".fanout/briefings/",
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
	plan, _ := buildPlanContext(context.Background(), opts)
	return plan
}

func buildPlanContext(ctx context.Context, opts Options) (Plan, error) {
	base, missingOrigin, err := resolveBaseBranchContext(ctx, opts)
	if err != nil {
		return Plan{}, err
	}
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
	}, nil
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
	branch, _ := resolveDefaultBranchContext(context.Background(), root, false)
	return branch
}

// ResolveDefaultBranchAllowMissingOrigin returns the current local branch when
// no origin remote exists, for local-only plan/manual pane runs.
func ResolveDefaultBranchAllowMissingOrigin(root string) string {
	branch, _ := resolveDefaultBranchContext(context.Background(), root, true)
	return branch
}

func resolveDefaultBranchContext(
	ctx context.Context,
	root string,
	allowMissingOrigin bool,
) (string, error) {
	if allowMissingOrigin {
		hasOrigin, err := hasOriginRemoteContext(ctx, root)
		if err != nil {
			return "", err
		}
		if !hasOrigin {
			return localBaseRefContext(ctx, root)
		}
	}
	cmd := exec.CommandContext(ctx, "gh", "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" && s != "null" {
			return s, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if out, err := gitContext(ctx, root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		s := strings.TrimSpace(string(out))
		s = strings.TrimPrefix(s, "origin/")
		if s != "" {
			return s, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "main", nil
}

func resolveBaseBranchContext(ctx context.Context, opts Options) (string, bool, error) {
	missingOrigin := false
	if opts.AllowMissingOrigin {
		hasOrigin, err := hasOriginRemoteContext(ctx, opts.ProjectRoot)
		if err != nil {
			return "", false, err
		}
		missingOrigin = !hasOrigin
	}
	base := opts.BaseBranch
	if base == "" {
		if missingOrigin {
			localBase, err := localBaseRefContext(ctx, opts.ProjectRoot)
			return localBase, true, err
		}
		defaultBase, err := resolveDefaultBranchContext(ctx, opts.ProjectRoot, false)
		return defaultBase, false, err
	}
	return base, missingOrigin, nil
}

func hasOriginRemoteContext(ctx context.Context, root string) (bool, error) {
	_, err := gitContext(ctx, root, "remote", "get-url", "origin")
	if contextErr := ctx.Err(); contextErr != nil {
		return false, contextErr
	}
	return err == nil, nil
}

func localBaseRefContext(ctx context.Context, root string) (string, error) {
	if branch, err := gitTrimContext(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && branch != "" {
		return branch, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "HEAD", nil
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
	return RefreshBaseContext(context.Background(), root, base)
}

// RefreshBaseContext is RefreshBase with cancellation and deadline support.
func RefreshBaseContext(ctx context.Context, root, base string) error {
	details, err := RefreshDetailsFor(base)
	if err != nil {
		return err
	}
	if _, err = gitContext(ctx, root, "fetch", "--quiet", "--no-tags", "origin", details.FetchBranch); err != nil {
		return fmt.Errorf("git fetch origin %s: %w", details.FetchBranch, err)
	}
	originSHA, err := gitTrimContext(ctx, root, "rev-parse", "--verify", details.OriginRef)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", details.OriginRef, err)
	}
	if details.LocalBranch == "" {
		return nil
	}

	localRef := "refs/heads/" + details.LocalBranch
	localSHA, err := gitTrimContext(ctx, root, "rev-parse", "--verify", localRef)
	if err != nil {
		if _, err = gitContext(ctx, root, "branch", details.LocalBranch, originSHA); err != nil {
			return fmt.Errorf("create local base branch %s: %w", details.LocalBranch, err)
		}
		return nil
	}
	if localSHA == originSHA {
		return nil
	}
	mergeBase, err := gitTrimContext(ctx, root, "merge-base", localSHA, originSHA)
	if err != nil {
		return fmt.Errorf("check fast-forward for %s: %w", details.LocalBranch, err)
	}
	if mergeBase != localSHA {
		return fmt.Errorf("local branch %s has diverged from origin/%s", details.LocalBranch, details.FetchBranch)
	}
	if _, err = gitContext(ctx, root, "branch", "-f", details.LocalBranch, originSHA); err == nil {
		return nil
	}

	checkedOutPath, checkedOutErr := checkedOutWorktree(ctx, root, details.LocalBranch)
	if checkedOutErr != nil {
		return fmt.Errorf("locate checked-out local branch %s: %w", details.LocalBranch, checkedOutErr)
	}
	if checkedOutPath == "" {
		return fmt.Errorf("local branch %s is checked out and could not be located", details.LocalBranch)
	}
	status, err := gitTrimContext(ctx, checkedOutPath, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("check %s cleanliness: %w", checkedOutPath, err)
	}
	if status != "" {
		return fmt.Errorf("local branch %s is checked out at %s with uncommitted changes; refusing to fast-forward", details.LocalBranch, checkedOutPath)
	}
	if _, err := gitContext(ctx, checkedOutPath, "merge", "--ff-only", "--quiet", originSHA); err != nil {
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
	entries, err := worktreeEntries(context.Background(), projectRoot)
	if err != nil {
		return []string{projectRoot}, err
	}
	roots := []string{projectRoot}
	seen := map[string]bool{projectRoot: true}
	childMarker := string(filepath.Separator) + filepath.FromSlash(localExcludePattern)
	for _, entry := range entries {
		// bare and prunable worktrees have no usable working tree to read
		// state from.
		if entry.bare || entry.prunable || seen[entry.path] ||
			strings.Contains(entry.path, childMarker) {
			continue
		}
		seen[entry.path] = true
		roots = append(roots, entry.path)
	}
	return roots, nil
}

// ListFiles returns repository-relative paths in the tree of the branch a fresh
// fanout worktree will be created from (the base branch tree), via
// `git ls-tree -r --name-only -z <base>`. It deliberately lists the base tree
// rather than the TUI's current index: a pane runs its agent in a worktree
// checked out from the base, so files that exist only in the current checkout
// (a feature branch, staged-but-uncommitted, or untracked) are absent there and
// completing them would produce dead @file mentions. It reads stdout only (not
// the shared CombinedOutput git helper) so a stderr advice/warning line on a
// zero exit cannot fuse into a path entry; the NUL-delimited (-z) form avoids
// git's path quoting.
func ListFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "-z", baseTreeRef(root))
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

// baseTreeRef resolves the ref whose tree a fresh fanout worktree branches from.
// It uses the same default-branch resolution as launch (ResolveDefaultBranch,
// which prefers GitHub's defaultBranchRef) so completion candidates and the
// created worktree never disagree on the base, then prefers that branch's remote
// tip (origin/<base>) — what the worktree refreshes to — when present.
func baseTreeRef(root string) string {
	base := ResolveDefaultBranchAllowMissingOrigin(root)
	if _, err := git(root, "rev-parse", "--verify", "--quiet", "origin/"+base); err == nil {
		return "origin/" + base
	}
	return base
}

func checkedOutWorktree(ctx context.Context, root, branch string) (string, error) {
	entries, err := worktreeEntries(ctx, root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.branch == "refs/heads/"+branch {
			return entry.path, nil
		}
	}
	return "", nil
}

func gitTrim(dir string, args ...string) (string, error) {
	out, err := git(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitTrimContext(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitContext(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func git(dir string, args ...string) ([]byte, error) {
	return execx.Combined(dir, "git", args...)
}

func gitContext(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return execx.CombinedContext(ctx, dir, "git", args...)
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
	_, found, err := ObserveBranch(context.Background(), root, "refs/heads/"+branch)
	return err == nil && found
}

// SlugInUse reports whether slug already owns a worktree directory under
// .fanout/worktrees or branchName already exists as a local branch — the
// leftovers a state-only close (close the pane, or remove the worktree but keep
// the branch) can produce. A caller deriving a fresh manual slug skips a number
// whose slug is in use so it neither fails preparing a duplicate worktree nor
// silently inherits an orphaned branch.
func SlugInUse(projectRoot, slug, branchName string) bool {
	if dirExists(filepath.Join(projectRoot, ".fanout", "worktrees", slug)) {
		return true
	}
	return branchExists(projectRoot, branchName)
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
