package worktree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var herdrCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type HerdrBaseResolution struct {
	BaseBranch string
	SHA        string
}

type HerdrRepoIdentity struct {
	RepoKey  string
	RepoRoot string
}

type HerdrCheckoutObservation struct {
	PathAbsent bool
	Registered bool
	BranchRef  string
	HeadSHA    string
	RepoKey    string
	RepoRoot   string
}

// ResolveHerdrRepoIdentity returns the physical Git common directory and
// source checkout root used by Herdr worktree provenance.
func ResolveHerdrRepoIdentity(root string) (HerdrRepoIdentity, error) {
	repoKey, err := gitTrim(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("resolve Herdr repo key: %w", err)
	}
	repoRoot, err := gitTrim(root, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("resolve Herdr repo root: %w", err)
	}
	repoKey, err = physicalHerdrPath(repoKey)
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("canonicalize Herdr repo key: %w", err)
	}
	repoRoot, err = physicalHerdrPath(repoRoot)
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("canonicalize Herdr repo root: %w", err)
	}
	return HerdrRepoIdentity{RepoKey: repoKey, RepoRoot: repoRoot}, nil
}

// ResolveHerdrBase applies the tmux base refresh gate and freezes the selected
// base to one commit SHA before branch reservation.
func ResolveHerdrBase(opts Options) (HerdrBaseResolution, error) {
	plan := BuildPlan(opts)
	if err := requireCleanHerdrSource(plan.ProjectRoot); err != nil {
		return HerdrBaseResolution{}, err
	}
	if plan.Refresh {
		refreshErr := plan.RefreshError
		if refreshErr == nil {
			refreshErr = RefreshBase(plan.ProjectRoot, plan.BaseBranch)
		}
		if refreshErr != nil {
			return HerdrBaseResolution{}, refreshErr
		}
	}
	if err := requireCleanHerdrSource(plan.ProjectRoot); err != nil {
		return HerdrBaseResolution{}, err
	}
	sha, err := gitTrim(plan.ProjectRoot, "rev-parse", "--verify", plan.BaseBranch+"^{commit}")
	if err != nil {
		return HerdrBaseResolution{}, fmt.Errorf("resolve Herdr base %q to a commit: %w", plan.BaseBranch, err)
	}
	sha = strings.ToLower(sha)
	if !herdrCommitSHA.MatchString(sha) {
		return HerdrBaseResolution{}, fmt.Errorf("resolve Herdr base %q: invalid commit %q", plan.BaseBranch, sha)
	}
	return HerdrBaseResolution{BaseBranch: plan.BaseBranch, SHA: sha}, nil
}

func requireCleanHerdrSource(root string) error {
	status, err := gitTrim(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("check Herdr source checkout cleanliness: %w", err)
	}
	if status != "" {
		return fmt.Errorf("source checkout %s has uncommitted changes; refusing Herdr worktree mutation", root)
	}
	return nil
}

func HerdrBranchRef(root, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.HasPrefix(branch, "refs/") {
		return "", fmt.Errorf("herdr branch must be an unqualified local branch name")
	}
	fullRef := "refs/heads/" + branch
	if _, err := git(root, "check-ref-format", fullRef); err != nil {
		return "", fmt.Errorf("invalid Herdr branch %q: %w", branch, err)
	}
	return fullRef, nil
}

func ObserveHerdrBranch(root, fullRef string) (string, bool, error) {
	if !strings.HasPrefix(fullRef, "refs/heads/") {
		return "", false, fmt.Errorf("invalid Herdr local branch ref %q", fullRef)
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", fullRef)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("observe Herdr branch %s: %w", fullRef, err)
	}
	sha := strings.ToLower(strings.TrimSpace(string(out)))
	if !herdrCommitSHA.MatchString(sha) {
		return "", false, fmt.Errorf("observe Herdr branch %s: invalid commit %q", fullRef, sha)
	}
	return sha, true, nil
}

// ReserveHerdrBranch atomically creates fullRef at baseSHA with an empty old
// OID. Existing refs fail without being modified.
func ReserveHerdrBranch(root, fullRef, baseSHA string) error {
	if !strings.HasPrefix(fullRef, "refs/heads/") || !herdrCommitSHA.MatchString(baseSHA) {
		return fmt.Errorf("invalid Herdr branch reservation %s -> %s", fullRef, baseSHA)
	}
	const emptyOID = "0000000000000000000000000000000000000000"
	if _, err := git(root, "update-ref", "--create-reflog", fullRef, baseSHA, emptyOID); err != nil {
		return fmt.Errorf("reserve Herdr branch %s at %s: %w", fullRef, baseSHA, err)
	}
	return nil
}

// DeleteReservedHerdrBranch compare-and-deletes a fanout-created branch only
// when its expected tip is unchanged and no linked worktree checks it out.
func DeleteReservedHerdrBranch(root, fullRef, expectedSHA string) error {
	if !strings.HasPrefix(fullRef, "refs/heads/") || !herdrCommitSHA.MatchString(expectedSHA) {
		return fmt.Errorf("invalid Herdr branch deletion %s at %s", fullRef, expectedSHA)
	}
	checkedOut, err := herdrBranchCheckedOut(root, fullRef)
	if err != nil {
		return err
	}
	if checkedOut {
		return fmt.Errorf("refusing to delete checked-out Herdr branch %s", fullRef)
	}
	current, found, err := ObserveHerdrBranch(root, fullRef)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if current != expectedSHA {
		return fmt.Errorf("herdr branch %s moved from %s to %s", fullRef, expectedSHA, current)
	}
	if _, err := git(root, "update-ref", "-d", fullRef, expectedSHA); err != nil {
		return fmt.Errorf("delete reserved Herdr branch %s: %w", fullRef, err)
	}
	return nil
}

func HerdrBranchAvailable(root, fullRef string) error {
	checkedOut, err := herdrBranchCheckedOut(root, fullRef)
	if err != nil {
		return err
	}
	if checkedOut {
		return fmt.Errorf("herdr branch %s is already checked out", fullRef)
	}
	return nil
}

// EnsureHerdrWorktreeParent creates the deterministic .fanout/worktrees
// parent and rejects symlinked path components or a foreign checkout leaf.
func EnsureHerdrWorktreeParent(projectRoot, checkoutPath string) error {
	return ensureHerdrWorktreeParent(projectRoot, checkoutPath, true)
}

// VerifyHerdrWorktreeParent repeats the deterministic no-symlink path check
// immediately before a Herdr mutation without creating missing directories.
func VerifyHerdrWorktreeParent(projectRoot, checkoutPath string) error {
	return ensureHerdrWorktreeParent(projectRoot, checkoutPath, false)
}

func ensureHerdrWorktreeParent(projectRoot, checkoutPath string, create bool) error {
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

func ObserveHerdrCheckout(root, checkoutPath string) (HerdrCheckoutObservation, error) {
	observation := HerdrCheckoutObservation{}
	info, err := os.Lstat(checkoutPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		observation.PathAbsent = true
	case err != nil:
		return observation, fmt.Errorf("inspect Herdr checkout path %s: %w", checkoutPath, err)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return observation, fmt.Errorf("herdr checkout path %s is not a real directory", checkoutPath)
	}

	entries, err := herdrWorktreeEntries(root)
	if err != nil {
		return observation, err
	}
	cleanPath := filepath.Clean(checkoutPath)
	if !observation.PathAbsent {
		cleanPath, err = physicalHerdrPath(cleanPath)
		if err != nil {
			return observation, fmt.Errorf("canonicalize Herdr checkout path: %w", err)
		}
	}
	for _, entry := range entries {
		entryPath, pathErr := physicalHerdrPath(entry.path)
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
	if !herdrCommitSHA.MatchString(observation.HeadSHA) {
		return observation, fmt.Errorf("herdr checkout %s has invalid HEAD %q", checkoutPath, observation.HeadSHA)
	}
	sourceIdentity, err := ResolveHerdrRepoIdentity(root)
	if err != nil {
		return observation, err
	}
	checkoutIdentity, err := ResolveHerdrRepoIdentity(checkoutPath)
	if err != nil {
		return observation, err
	}
	if checkoutIdentity.RepoKey != sourceIdentity.RepoKey {
		return observation, fmt.Errorf("herdr checkout %s belongs to a different Git common directory", checkoutPath)
	}
	observation.RepoKey = sourceIdentity.RepoKey
	observation.RepoRoot = sourceIdentity.RepoRoot
	return observation, nil
}

func VerifyHerdrCheckout(
	root, checkoutPath, fullRef, expectedHead, expectedRepoKey, expectedRepoRoot string,
) (HerdrCheckoutObservation, error) {
	observation, err := ObserveHerdrCheckout(root, checkoutPath)
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

type herdrWorktreeEntry struct {
	path   string
	branch string
}

func herdrBranchCheckedOut(root, fullRef string) (bool, error) {
	entries, err := herdrWorktreeEntries(root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.branch == fullRef {
			return true, nil
		}
	}
	return false, nil
}

func herdrWorktreeEntries(root string) ([]herdrWorktreeEntry, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list Git worktrees for Herdr: %w", err)
	}
	fields := strings.Split(string(out), "\x00")
	var entries []herdrWorktreeEntry
	var current herdrWorktreeEntry
	flush := func() {
		if current.path != "" {
			entries = append(entries, current)
		}
		current = herdrWorktreeEntry{}
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
		}
	}
	flush()
	return entries, nil
}

func physicalHerdrPath(path string) (string, error) {
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
