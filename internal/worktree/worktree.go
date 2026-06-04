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

type Options struct {
	ProjectRoot string
	Slug        string
	BranchName  string
	BaseBranch  string
	NoRefresh   bool
}

type Plan struct {
	ProjectRoot    string
	WorktreePath   string
	BranchName     string
	BaseBranch     string
	Refresh        bool
	RefreshDetails RefreshDetails
	RefreshError   error
}

type Result struct {
	Plan
	AlreadyExists bool
}

// BuildPlan resolves deterministic worktree paths and the base branch.
func BuildPlan(opts Options) Plan {
	base := opts.BaseBranch
	if base == "" {
		base = ResolveDefaultBranch(opts.ProjectRoot)
	}
	refreshDetails, refreshErr := RefreshDetailsFor(base)
	return Plan{
		ProjectRoot:    opts.ProjectRoot,
		WorktreePath:   filepath.Join(opts.ProjectRoot, ".fanout", "worktrees", opts.Slug),
		BranchName:     opts.BranchName,
		BaseBranch:     base,
		Refresh:        !opts.NoRefresh,
		RefreshDetails: refreshDetails,
		RefreshError:   refreshErr,
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
	if plan.Refresh {
		if err := RefreshBase(plan.ProjectRoot, plan.BaseBranch); err != nil {
			return Result{Plan: plan}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(plan.WorktreePath), 0o755); err != nil {
		return Result{Plan: plan}, fmt.Errorf("create worktree parent: %w", err)
	}
	_, _ = git(plan.ProjectRoot, "worktree", "prune")
	branchWasPresent := branchExists(plan.ProjectRoot, plan.BranchName)
	if _, err := git(plan.ProjectRoot, "worktree", "add", "-b", plan.BranchName, plan.WorktreePath, plan.BaseBranch); err != nil {
		_, _ = git(plan.ProjectRoot, "worktree", "prune")
		if !branchWasPresent {
			_, _ = git(plan.ProjectRoot, "branch", "-D", plan.BranchName)
		}
		return Result{Plan: plan}, fmt.Errorf("git worktree add: %w", err)
	}
	return Result{Plan: plan}, nil
}

// EnsureLocalExclude keeps generated fanout worktrees out of the user's git status.
func EnsureLocalExclude(root string) error {
	excludePath, err := gitTrim(root, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve git exclude path: %w", err)
	}
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(root, excludePath)
	}

	body, err := os.ReadFile(excludePath)
	switch {
	case err == nil && hasExcludePattern(body):
		return nil
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("read git exclude: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create git exclude directory: %w", err)
	}
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open git exclude: %w", err)
	}
	defer f.Close()

	if len(body) > 0 && body[len(body)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return fmt.Errorf("append git exclude newline: %w", err)
		}
	}
	if _, err := f.WriteString(localExcludePattern + "\n"); err != nil {
		return fmt.Errorf("append git exclude pattern: %w", err)
	}
	return nil
}

// CleanupCreated removes a worktree and branch created by Prepare after a later launch failure.
func CleanupCreated(plan Plan) error {
	var errs []error
	if dirExists(plan.WorktreePath) {
		if _, err := git(plan.ProjectRoot, "worktree", "remove", "--force", plan.WorktreePath); err != nil {
			errs = append(errs, fmt.Errorf("git worktree remove: %w", err))
		}
	}
	if plan.BranchName != "" {
		if _, err := git(plan.ProjectRoot, "branch", "-D", plan.BranchName); err != nil {
			errs = append(errs, fmt.Errorf("git branch -D %s: %w", plan.BranchName, err))
		}
	}
	_, _ = git(plan.ProjectRoot, "worktree", "prune")
	return errors.Join(errs...)
}

func hasExcludePattern(body []byte) bool {
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == localExcludePattern {
			return true
		}
	}
	return false
}

// ResolveDefaultBranch follows fanout's default branch fallback order.
func ResolveDefaultBranch(root string) string {
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
	if _, err := git(root, "fetch", "--quiet", "--no-tags", "origin", details.FetchBranch); err != nil {
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
		if _, err := git(root, "branch", details.LocalBranch, originSHA); err != nil {
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
	if _, err := git(root, "branch", "-f", details.LocalBranch, originSHA); err == nil {
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

func checkedOutWorktree(root, branch string) string {
	out, err := git(root, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	var current string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "worktree ") {
			current = strings.TrimPrefix(line, "worktree ")
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
		return out, fmt.Errorf("%v: %s", err, msg)
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
