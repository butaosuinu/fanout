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

const herdrOwnershipMarkerName = "fanout-herdr-owner"

var fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// HerdrBaseResolution freezes the user-facing selector, its canonical ref/name
// tuple, and the immutable commit used by branch reservation.
type HerdrBaseResolution struct {
	ResolvedRef   string
	ResolvedName  string
	EffectiveBase string
	PRBaseName    string
	SHA           string
}

type HerdrRepoIdentity struct {
	RepoKey  string
	RepoRoot string
}

// ResolveHerdrRepoIdentity returns the physical Git common directory and
// source worktree root expected in Herdr worktree provenance.
func ResolveHerdrRepoIdentity(root string) (HerdrRepoIdentity, error) {
	repoKey, err := gitTrim(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("resolve herdr repo key: %w", err)
	}
	repoRoot, err := gitTrim(root, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("resolve herdr repo root: %w", err)
	}
	repoKey, err = physicalHerdrPath(repoKey)
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("canonicalize herdr repo key: %w", err)
	}
	repoRoot, err = physicalHerdrPath(repoRoot)
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("canonicalize herdr repo root: %w", err)
	}
	return HerdrRepoIdentity{RepoKey: repoKey, RepoRoot: repoRoot}, nil
}

// ResolveHerdrBase performs fanout's base refresh/safety gate and resolves the
// selector once. Callers persist the returned tuple and must not re-resolve it
// during recovery.
func ResolveHerdrBase(root, selector string, noRefresh bool) (HerdrBaseResolution, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		selector = ResolveDefaultBranch(root)
	}
	if err := requireCleanSource(root); err != nil {
		return HerdrBaseResolution{}, err
	}
	if !noRefresh {
		if err := RefreshBase(root, selector); err != nil {
			return HerdrBaseResolution{}, err
		}
	}
	if err := requireCleanSource(root); err != nil {
		return HerdrBaseResolution{}, err
	}

	sha, err := gitTrim(root, "rev-parse", "--verify", selector+"^{commit}")
	if err != nil {
		return HerdrBaseResolution{}, fmt.Errorf("resolve base %q to a commit: %w", selector, err)
	}
	sha = strings.ToLower(sha)
	if !fullCommitSHA.MatchString(sha) {
		return HerdrBaseResolution{}, fmt.Errorf("resolve base %q: git returned invalid commit %q", selector, sha)
	}

	fullRef, refErr := gitTrim(root, "rev-parse", "--symbolic-full-name", selector)
	if refErr != nil {
		return HerdrBaseResolution{}, fmt.Errorf("canonicalize base %q: %w", selector, refErr)
	}
	if strings.Contains(fullRef, "\n") {
		return HerdrBaseResolution{}, fmt.Errorf("canonicalize base %q: ambiguous symbolic ref %q", selector, fullRef)
	}
	resolvedRef, resolvedName, prBase := canonicalHerdrBase(fullRef, sha)
	return HerdrBaseResolution{
		ResolvedRef:   resolvedRef,
		ResolvedName:  resolvedName,
		EffectiveBase: selector,
		PRBaseName:    prBase,
		SHA:           sha,
	}, nil
}

func canonicalHerdrBase(fullRef, sha string) (resolvedRef, resolvedName, prBase string) {
	fullRef = strings.TrimSpace(fullRef)
	switch {
	case strings.HasPrefix(fullRef, "refs/heads/"):
		name := strings.TrimPrefix(fullRef, "refs/heads/")
		return fullRef, name, name
	case strings.HasPrefix(fullRef, "refs/remotes/"):
		name := strings.TrimPrefix(fullRef, "refs/remotes/")
		if branch, ok := strings.CutPrefix(name, "origin/"); ok {
			prBase = branch
		}
		return fullRef, name, prBase
	case fullRef != "":
		return fullRef, fullRef, ""
	default:
		// A direct commit selector has no symbolic full name. Persist only the
		// peeled lowercase object identity; do not invent a ref alias.
		return sha, sha, ""
	}
}

func requireCleanSource(root string) error {
	status, err := gitTrim(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("check source checkout cleanliness: %w", err)
	}
	if status != "" {
		return fmt.Errorf("source checkout %s has uncommitted changes; refusing herdr worktree mutation", root)
	}
	return nil
}

// HerdrBranchRef validates and expands a generated local branch name.
func HerdrBranchRef(root, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.HasPrefix(branch, "refs/") {
		return "", fmt.Errorf("herdr branch must be an unqualified local branch name")
	}
	fullRef := "refs/heads/" + branch
	if _, err := git(root, "check-ref-format", fullRef); err != nil {
		return "", fmt.Errorf("invalid herdr branch %q: %w", branch, err)
	}
	return fullRef, nil
}

// ObserveBranch returns the current ref tip. found=false is the exact
// precondition required by ReserveBranch.
func ObserveBranch(root, fullRef string) (oid string, found bool, err error) {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", fullRef)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("observe branch %s: %w", fullRef, err)
	}
	oid = strings.ToLower(strings.TrimSpace(string(out)))
	if !fullCommitSHA.MatchString(oid) {
		return "", false, fmt.Errorf("observe branch %s: invalid object id %q", fullRef, oid)
	}
	return oid, true, nil
}

// ReserveBranch atomically creates fullRef at baseSHA with an empty old OID.
// Herdr is never asked to create the branch.
func ReserveBranch(root, fullRef, baseSHA string) error {
	if !strings.HasPrefix(fullRef, "refs/heads/") || !fullCommitSHA.MatchString(baseSHA) {
		return fmt.Errorf("invalid atomic branch reservation %s -> %s", fullRef, baseSHA)
	}
	const emptyOID = "0000000000000000000000000000000000000000"
	if _, err := git(root, "update-ref", "--create-reflog", fullRef, baseSHA, emptyOID); err != nil {
		return fmt.Errorf("reserve branch %s at %s: %w", fullRef, baseSHA, err)
	}
	return nil
}

// VerifyReservedBranch checks both the saved immutable base and the current
// reserved tip. It is used immediately before Herdr worktree mutation.
func VerifyReservedBranch(root, resolvedBaseRef, baseSHA, fullRef string) error {
	if err := VerifyReservedBranchBase(root, resolvedBaseRef, baseSHA); err != nil {
		return err
	}
	branchOID, found, err := ObserveBranch(root, fullRef)
	if err != nil {
		return err
	}
	if !found || branchOID != baseSHA {
		return fmt.Errorf("reserved branch %s no longer points at %s", fullRef, baseSHA)
	}
	return nil
}

func VerifyReservedBranchBase(root, resolvedBaseRef, baseSHA string) error {
	currentBase, err := gitTrim(root, "rev-parse", "--verify", resolvedBaseRef+"^{commit}")
	if err != nil {
		return fmt.Errorf("recheck resolved base %s: %w", resolvedBaseRef, err)
	}
	if strings.ToLower(currentBase) != baseSHA {
		return fmt.Errorf("resolved base %s moved from %s to %s", resolvedBaseRef, baseSHA, strings.ToLower(currentBase))
	}
	return nil
}

func CheckoutGitState(root, checkoutPath, fullRef string) (pathAbsent, registered bool, headSHA string, err error) {
	info, statErr := os.Lstat(checkoutPath)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		pathAbsent = true
	case statErr != nil:
		return false, false, "", fmt.Errorf("inspect checkout path %s: %w", checkoutPath, statErr)
	case !info.IsDir():
		return false, false, "", fmt.Errorf("checkout path %s is not a directory", checkoutPath)
	}
	registered, err = checkoutRegistered(root, checkoutPath)
	if err != nil {
		return pathAbsent, false, "", err
	}
	if !pathAbsent {
		headSHA, err = gitTrim(checkoutPath, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return pathAbsent, registered, "", fmt.Errorf("resolve checkout HEAD at %s: %w", checkoutPath, err)
		}
		headSHA = strings.ToLower(headSHA)
	}
	if registered {
		branch, branchErr := gitTrim(checkoutPath, "symbolic-ref", "--quiet", "HEAD")
		if branchErr != nil {
			return pathAbsent, registered, headSHA, fmt.Errorf("resolve checkout branch at %s: %w", checkoutPath, branchErr)
		}
		if branch != fullRef {
			return pathAbsent, registered, headSHA, fmt.Errorf("checkout %s uses %s, want %s", checkoutPath, branch, fullRef)
		}
	}
	return pathAbsent, registered, headSHA, nil
}

func checkoutRegistered(root, checkoutPath string) (bool, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("list git worktrees: %w", err)
	}
	want, err := filepath.Abs(checkoutPath)
	if err != nil {
		return false, fmt.Errorf("resolve checkout path %s: %w", checkoutPath, err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(want); resolveErr == nil {
		want = resolved
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		got, absErr := filepath.Abs(path)
		if absErr != nil {
			return false, fmt.Errorf("resolve registered worktree path %s: %w", path, absErr)
		}
		if resolved, resolveErr := filepath.EvalSymlinks(got); resolveErr == nil {
			got = resolved
		}
		if filepath.Clean(got) == filepath.Clean(want) {
			return true, nil
		}
	}
	return false, nil
}

func physicalHerdrPath(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q is not absolute", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// WriteHerdrOwnershipMarker exclusively creates the checkout-side half of the
// nonce proof inside the checkout's git dir.
func WriteHerdrOwnershipMarker(checkoutPath, nonce string) (string, error) {
	if strings.TrimSpace(nonce) == "" || strings.ContainsAny(nonce, "\r\n\x00") {
		return "", fmt.Errorf("invalid herdr worktree ownership nonce")
	}
	gitDir, err := gitTrim(checkoutPath, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("resolve checkout git dir: %w", err)
	}
	markerPath := filepath.Join(gitDir, herdrOwnershipMarkerName)
	f, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create herdr ownership marker: %w", err)
	}
	if _, err := f.WriteString(nonce + "\n"); err != nil {
		// The write error is the actionable failure; Close cannot make the
		// incomplete marker safe to adopt.
		_ = f.Close()
		return "", fmt.Errorf("write herdr ownership marker: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close herdr ownership marker: %w", err)
	}
	return markerPath, nil
}

func VerifyHerdrOwnershipMarker(checkoutPath, markerPath, nonce string) error {
	gitDir, err := gitTrim(checkoutPath, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return fmt.Errorf("resolve checkout git dir: %w", err)
	}
	wantPath := filepath.Join(gitDir, herdrOwnershipMarkerName)
	if filepath.Clean(markerPath) != filepath.Clean(wantPath) {
		return fmt.Errorf("herdr ownership marker path changed: got %s want %s", markerPath, wantPath)
	}
	info, err := os.Lstat(markerPath)
	if err != nil {
		return fmt.Errorf("inspect herdr ownership marker: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("herdr ownership marker has unsafe mode %s", info.Mode())
	}
	body, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("read herdr ownership marker: %w", err)
	}
	if string(body) != nonce+"\n" {
		return fmt.Errorf("herdr ownership marker nonce mismatch")
	}
	return nil
}
