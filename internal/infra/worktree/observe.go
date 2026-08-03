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

// ErrCheckoutMismatch marks a confirmed postcondition mismatch, as opposed to
// a failed observation that classified nothing.
var ErrCheckoutMismatch = errors.New("checkout does not match the recorded postconditions")

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
func ResolveRepoIdentity(ctx context.Context, root string) (RepoIdentity, error) {
	repoKey, err := resolveGitPath(ctx, root, "--git-common-dir")
	if err != nil {
		return RepoIdentity{}, fmt.Errorf("resolve Herdr repo key: %w", err)
	}
	repoRoot, err := resolveGitPath(ctx, root, "--show-toplevel")
	if err != nil {
		return RepoIdentity{}, fmt.Errorf("resolve Herdr repo root: %w", err)
	}
	return RepoIdentity{RepoKey: repoKey, RepoRoot: repoRoot}, nil
}

func resolveGitPath(ctx context.Context, root, flag string) (string, error) {
	out, err := gitStdout(ctx, root, "rev-parse", flag)
	if err != nil {
		return "", err
	}
	// Strip exactly the newline git appends; a path's own whitespace is data.
	path := strings.TrimSuffix(string(out), "\n")
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
	shaOut, shaErrOut, err := gitStdoutStderr(ctx, plan.ProjectRoot, "rev-parse", "--verify", plan.BaseBranch+"^{commit}")
	sha := strings.TrimSpace(string(shaOut))
	if err != nil {
		return BaseResolution{}, fmt.Errorf("resolve Herdr base %q to a commit: %w", plan.BaseBranch, err)
	}
	// git resolves an ambiguous short ref with exit 0 and a stderr warning;
	// the canon rejects ambiguous selectors before any mutation.
	if strings.Contains(shaErrOut, "ambiguous") {
		return BaseResolution{}, fmt.Errorf("resolve Herdr base %q: ref is ambiguous", plan.BaseBranch)
	}
	sha = strings.ToLower(sha)
	if !commitSHAPattern.MatchString(sha) {
		return BaseResolution{}, fmt.Errorf("resolve Herdr base %q: invalid commit %q", plan.BaseBranch, sha)
	}
	return BaseResolution{BaseBranch: plan.BaseBranch, SHA: sha}, nil
}

func requireCleanSource(ctx context.Context, root string) error {
	statusOut, err := gitStdout(ctx, root, "status", "--porcelain", "--untracked-files=all")
	status := strings.TrimSpace(string(statusOut))
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

func ObserveCheckout(ctx context.Context, root, checkoutPath string) (CheckoutObservation, error) {
	observation := CheckoutObservation{}
	info, err := os.Lstat(checkoutPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		observation.PathAbsent = true
	case err != nil:
		return observation, fmt.Errorf("inspect Herdr checkout path %s: %w", checkoutPath, err)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return observation, fmt.Errorf("%w: %s is not a real directory", ErrCheckoutMismatch, checkoutPath)
	}

	entries, err := worktreeEntries(ctx, root)
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
	headOut, err := gitStdout(ctx, checkoutPath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return observation, fmt.Errorf("resolve Herdr checkout HEAD at %s: %w", checkoutPath, err)
	}
	observation.HeadSHA = strings.ToLower(strings.TrimSpace(string(headOut)))
	if !commitSHAPattern.MatchString(observation.HeadSHA) {
		return observation, fmt.Errorf("herdr checkout %s has invalid HEAD %q", checkoutPath, observation.HeadSHA)
	}
	sourceIdentity, err := ResolveRepoIdentity(ctx, root)
	if err != nil {
		return observation, err
	}
	checkoutIdentity, err := ResolveRepoIdentity(ctx, checkoutPath)
	if err != nil {
		return observation, err
	}
	if checkoutIdentity.RepoKey != sourceIdentity.RepoKey {
		return observation, fmt.Errorf("%w: %s belongs to a different Git common directory", ErrCheckoutMismatch, checkoutPath)
	}
	if filepath.Clean(checkoutIdentity.RepoRoot) != cleanPath {
		return observation, fmt.Errorf(
			"%w: %s does not resolve to its registered worktree root",
			ErrCheckoutMismatch,
			checkoutPath,
		)
	}
	observation.RepoKey = sourceIdentity.RepoKey
	observation.RepoRoot = sourceIdentity.RepoRoot
	return observation, nil
}

func VerifyCheckout(
	ctx context.Context,
	root, checkoutPath, fullRef, expectedHead, expectedRepoKey, expectedRepoRoot string,
) (CheckoutObservation, error) {
	observation, err := ObserveCheckout(ctx, root, checkoutPath)
	if err != nil {
		return observation, err
	}
	if observation.PathAbsent || !observation.Registered {
		return observation, fmt.Errorf("%w: %s is absent or unregistered", ErrCheckoutMismatch, checkoutPath)
	}
	if observation.BranchRef != fullRef {
		return observation, fmt.Errorf("%w: %s uses %s, want %s", ErrCheckoutMismatch, checkoutPath, observation.BranchRef, fullRef)
	}
	if observation.HeadSHA != expectedHead {
		return observation, fmt.Errorf("%w: %s HEAD is %s, want %s", ErrCheckoutMismatch, checkoutPath, observation.HeadSHA, expectedHead)
	}
	if observation.RepoKey != expectedRepoKey || observation.RepoRoot != expectedRepoRoot {
		return observation, fmt.Errorf("%w: %s belongs to a different repository", ErrCheckoutMismatch, checkoutPath)
	}
	return observation, nil
}

// gitStdout runs git and returns stdout only, so warnings and advice on
// stderr never fuse into a parsed value.
func gitStdout(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, _, err := gitStdoutStderr(ctx, dir, args...)
	return out, err
}

// gitStdoutStderr additionally returns stderr (LC_ALL=C for a deterministic
// message) for callers that must inspect success-path warnings.
func gitStdoutStderr(ctx context.Context, dir string, args ...string) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, stderr.String(), contextErr
		}
		return nil, stderr.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, stderr.String(), nil
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
