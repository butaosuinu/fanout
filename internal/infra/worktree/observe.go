package worktree

// Repository identity, frozen base resolution, and checkout observation used
// by launch flows that verify Git pre- and postconditions themselves.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var commitSHAPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type BaseResolution struct {
	BaseBranch string
	SHA        string
}

type RepoIdentity struct {
	RepoKey  string
	RepoRoot string
}

type CheckoutObservation struct {
	PathAbsent bool
	Registered bool
	BranchRef  string
	HeadSHA    string
	RepoKey    string
	RepoRoot   string
}

// ResolveRepoIdentity returns the physical Git common directory and
// source checkout root used by Herdr worktree provenance.
func ResolveRepoIdentity(root string) (RepoIdentity, error) {
	repoKey, err := resolveGitPath(root, "--git-common-dir")
	if err != nil {
		return RepoIdentity{}, fmt.Errorf("resolve Herdr repo key: %w", err)
	}
	repoRoot, err := resolveGitPath(root, "--show-toplevel")
	if err != nil {
		return RepoIdentity{}, fmt.Errorf("resolve Herdr repo root: %w", err)
	}
	return RepoIdentity{RepoKey: repoKey, RepoRoot: repoRoot}, nil
}

func resolveGitPath(root, flag string) (string, error) {
	path, err := gitTrim(root, "rev-parse", flag)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("git rev-parse %s returned an empty path", flag)
	}
	if !filepath.IsAbs(path) {
		root, err = filepath.Abs(root)
		if err != nil {
			return "", err
		}
		path = filepath.Join(root, path)
	}
	return physicalPath(path)
}

// ResolveLaunchBase applies the tmux base refresh gate and freezes the
// selected base to one commit SHA before branch reservation.
func ResolveLaunchBase(ctx context.Context, opts Options) (BaseResolution, error) {
	plan, planErr := buildPlanContext(ctx, opts)
	if planErr != nil {
		return BaseResolution{}, planErr
	}
	if err := requireCleanSource(ctx, plan.ProjectRoot); err != nil {
		return BaseResolution{}, err
	}
	if plan.Refresh {
		refreshErr := plan.RefreshError
		if refreshErr == nil {
			refreshErr = RefreshBaseContext(ctx, plan.ProjectRoot, plan.BaseBranch)
		}
		if refreshErr != nil {
			return BaseResolution{}, refreshErr
		}
	}
	if err := requireCleanSource(ctx, plan.ProjectRoot); err != nil {
		return BaseResolution{}, err
	}
	sha, err := gitTrimContext(ctx, plan.ProjectRoot, "rev-parse", "--verify", plan.BaseBranch+"^{commit}")
	if err != nil {
		return BaseResolution{}, fmt.Errorf("resolve Herdr base %q to a commit: %w", plan.BaseBranch, err)
	}
	sha = strings.ToLower(sha)
	if !commitSHAPattern.MatchString(sha) {
		return BaseResolution{}, fmt.Errorf("resolve Herdr base %q: invalid commit %q", plan.BaseBranch, sha)
	}
	return BaseResolution{BaseBranch: plan.BaseBranch, SHA: sha}, nil
}

func requireCleanSource(ctx context.Context, root string) error {
	status, err := gitTrimContext(ctx, root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("check Herdr source checkout cleanliness: %w", err)
	}
	if status != "" {
		return fmt.Errorf("source checkout %s has uncommitted changes; refusing Herdr worktree mutation", root)
	}
	return nil
}

// EnsureWorktreeParentDir creates the deterministic .fanout/worktrees
// parent and rejects symlinked path components or a foreign checkout leaf.
func EnsureWorktreeParentDir(projectRoot, checkoutPath string) error {
	return ensureWorktreeParentDir(projectRoot, checkoutPath, true)
}

// VerifyWorktreeParentDir repeats the deterministic no-symlink path check
// immediately before a Herdr mutation without creating missing directories.
func VerifyWorktreeParentDir(projectRoot, checkoutPath string) error {
	return ensureWorktreeParentDir(projectRoot, checkoutPath, false)
}

func ensureWorktreeParentDir(projectRoot, checkoutPath string, create bool) error {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve Herdr project root: %w", err)
	}
	root = filepath.Clean(root)
	checkoutPath, err = filepath.Abs(checkoutPath)
	if err != nil {
		return fmt.Errorf("resolve Herdr checkout path: %w", err)
	}
	checkoutPath = filepath.Clean(checkoutPath)
	logicalParent := filepath.Join(root, ".fanout", "worktrees")
	if filepath.Dir(checkoutPath) != logicalParent || filepath.Base(checkoutPath) == "." {
		return fmt.Errorf("herdr checkout path %s is outside %s", checkoutPath, logicalParent)
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("canonicalize Herdr project root: %w", err)
	}
	root = filepath.Clean(physical)
	parent := filepath.Join(root, ".fanout", "worktrees")
	checkoutPath = filepath.Join(parent, filepath.Base(checkoutPath))
	for _, path := range []string{filepath.Join(root, ".fanout"), parent} {
		if directoryErr := ensureRealDirectory(path, create); directoryErr != nil {
			return directoryErr
		}
	}
	info, err := os.Lstat(checkoutPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("herdr checkout path %s is a symlink", checkoutPath)
		}
		return fmt.Errorf("herdr checkout path %s already exists", checkoutPath)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Herdr checkout path %s: %w", checkoutPath, err)
	}
	return nil
}

func ensureRealDirectory(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if mkdirErr := os.Mkdir(path, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return fmt.Errorf("create Herdr worktree directory %s: %w", path, mkdirErr)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect Herdr worktree directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("herdr worktree directory %s is not a real directory", path)
	}
	return nil
}

func ObserveCheckout(root, checkoutPath string) (CheckoutObservation, error) {
	observation := CheckoutObservation{}
	info, err := os.Lstat(checkoutPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		observation.PathAbsent = true
	case err != nil:
		return observation, fmt.Errorf("inspect Herdr checkout path %s: %w", checkoutPath, err)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return observation, fmt.Errorf("herdr checkout path %s is not a real directory", checkoutPath)
	}

	entries, err := worktreeEntries(context.Background(), root)
	if err != nil {
		return observation, err
	}
	cleanPath := filepath.Clean(checkoutPath)
	if !observation.PathAbsent {
		cleanPath, err = physicalPath(cleanPath)
		if err != nil {
			return observation, fmt.Errorf("canonicalize Herdr checkout path: %w", err)
		}
	}
	for _, entry := range entries {
		if filepath.Clean(entry.path) != cleanPath {
			continue
		}
		if observation.PathAbsent {
			observation.Registered = true
			observation.BranchRef = entry.branch
			break
		}
		entryPath, pathErr := physicalPath(entry.path)
		if pathErr != nil {
			return observation, fmt.Errorf("canonicalize registered Herdr worktree path: %w", pathErr)
		}
		if entryPath != cleanPath {
			continue
		}
		observation.Registered = true
		observation.BranchRef = entry.branch
		break
	}
	if observation.PathAbsent {
		return observation, nil
	}
	observation.HeadSHA, err = gitTrim(checkoutPath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return observation, fmt.Errorf("resolve Herdr checkout HEAD at %s: %w", checkoutPath, err)
	}
	observation.HeadSHA = strings.ToLower(observation.HeadSHA)
	if !commitSHAPattern.MatchString(observation.HeadSHA) {
		return observation, fmt.Errorf("herdr checkout %s has invalid HEAD %q", checkoutPath, observation.HeadSHA)
	}
	sourceIdentity, err := ResolveRepoIdentity(root)
	if err != nil {
		return observation, err
	}
	checkoutIdentity, err := ResolveRepoIdentity(checkoutPath)
	if err != nil {
		return observation, err
	}
	if checkoutIdentity.RepoKey != sourceIdentity.RepoKey {
		return observation, fmt.Errorf("herdr checkout %s belongs to a different Git common directory", checkoutPath)
	}
	if filepath.Clean(checkoutIdentity.RepoRoot) != cleanPath {
		return observation, fmt.Errorf(
			"herdr checkout %s does not resolve to its registered worktree root",
			checkoutPath,
		)
	}
	observation.RepoKey = sourceIdentity.RepoKey
	observation.RepoRoot = sourceIdentity.RepoRoot
	return observation, nil
}

func VerifyCheckout(
	root, checkoutPath, fullRef, expectedHead, expectedRepoKey, expectedRepoRoot string,
) (CheckoutObservation, error) {
	observation, err := ObserveCheckout(root, checkoutPath)
	if err != nil {
		return observation, err
	}
	if observation.PathAbsent || !observation.Registered {
		return observation, fmt.Errorf("herdr checkout %s is absent or unregistered", checkoutPath)
	}
	if observation.BranchRef != fullRef {
		return observation, fmt.Errorf("herdr checkout %s uses %s, want %s", checkoutPath, observation.BranchRef, fullRef)
	}
	if observation.HeadSHA != expectedHead {
		return observation, fmt.Errorf("herdr checkout %s HEAD is %s, want %s", checkoutPath, observation.HeadSHA, expectedHead)
	}
	if observation.RepoKey != expectedRepoKey || observation.RepoRoot != expectedRepoRoot {
		return observation, fmt.Errorf("herdr checkout %s belongs to a different repository", checkoutPath)
	}
	return observation, nil
}

type worktreeEntry struct {
	path     string
	branch   string
	bare     bool
	prunable bool
}

func worktreeEntries(ctx context.Context, root string) ([]worktreeEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list Git worktrees for Herdr: %w", err)
	}
	fields := strings.Split(string(out), "\x00")
	var entries []worktreeEntry
	var current worktreeEntry
	flush := func() {
		if current.path != "" {
			entries = append(entries, current)
		}
		current = worktreeEntry{}
	}
	for _, field := range fields {
		switch {
		case field == "":
			flush()
		case strings.HasPrefix(field, "worktree "):
			flush()
			current.path = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "branch "):
			current.branch = strings.TrimPrefix(field, "branch ")
		case field == "bare":
			current.bare = true
		case field == "prunable" || strings.HasPrefix(field, "prunable "):
			current.prunable = true
		}
	}
	flush()
	return entries, nil
}

func physicalPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = absolute
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}
