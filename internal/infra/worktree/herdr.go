package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
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
	RepoKey      string
	RepoRoot     string
	GitDir       string
	GitDirDevice uint64
	GitDirInode  uint64
}

type herdrWorktreeParentProof struct {
	projectRoot   *os.Root
	fanoutRoot    *os.Root
	worktreesRoot *os.Root
	leaf          string
}

// EnsureHerdrWorktreeParent creates and opens the deterministic checkout
// parent one component at a time. Existing symlinks are never adopted.
func EnsureHerdrWorktreeParent(projectRoot, checkoutPath string) error {
	proof, err := openHerdrWorktreeParent(projectRoot, checkoutPath, true)
	if err != nil {
		return err
	}
	return proof.close()
}

// VerifyHerdrWorktreeParent proves that the deterministic checkout parent is
// still the same no-follow directory chain immediately before mutation.
func VerifyHerdrWorktreeParent(projectRoot, checkoutPath string) error {
	proof, err := openHerdrWorktreeParent(projectRoot, checkoutPath, false)
	if err != nil {
		return err
	}
	return proof.close()
}

func openHerdrWorktreeParent(
	projectRoot, checkoutPath string,
	create bool,
) (*herdrWorktreeParentProof, error) {
	projectRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve herdr project root: %w", err)
	}
	projectRoot = filepath.Clean(projectRoot)
	physicalRoot, err := physicalHerdrPath(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize herdr project root: %w", err)
	}
	checkoutPath, err = filepath.Abs(checkoutPath)
	if err != nil {
		return nil, fmt.Errorf("resolve herdr checkout path: %w", err)
	}
	checkoutPath = filepath.Clean(checkoutPath)
	leaf := filepath.Base(checkoutPath)
	expectedLexical := filepath.Join(projectRoot, ".fanout", "worktrees", leaf)
	if leaf == "." || leaf == string(os.PathSeparator) || checkoutPath != expectedLexical {
		return nil, fmt.Errorf(
			"herdr checkout path %s is outside deterministic physical parent %s",
			checkoutPath,
			filepath.Join(physicalRoot, ".fanout", "worktrees"),
		)
	}

	projectInfo, err := os.Lstat(physicalRoot)
	if err != nil || !projectInfo.IsDir() || projectInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("herdr project root is not a non-symlink directory")
	}
	root, err := os.OpenRoot(physicalRoot)
	if err != nil {
		return nil, fmt.Errorf("open herdr project root: %w", err)
	}
	proof := &herdrWorktreeParentProof{projectRoot: root, leaf: leaf}
	fail := func(err error) (*herdrWorktreeParentProof, error) {
		return nil, errors.Join(err, proof.close())
	}
	openedProjectInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(projectInfo, openedProjectInfo) {
		return fail(fmt.Errorf("herdr project root identity changed while opening"))
	}
	proof.fanoutRoot, err = openHerdrChildDirectory(root, ".fanout", create)
	if err != nil {
		return fail(err)
	}
	proof.worktreesRoot, err = openHerdrChildDirectory(proof.fanoutRoot, "worktrees", create)
	if err != nil {
		return fail(err)
	}
	leafInfo, err := proof.worktreesRoot.Lstat(leaf)
	if err == nil && leafInfo.Mode()&os.ModeSymlink != 0 {
		return fail(fmt.Errorf("herdr checkout leaf %s is a symlink", checkoutPath))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("inspect herdr checkout leaf: %w", err))
	}
	return proof, nil
}

func openHerdrChildDirectory(root *os.Root, name string, create bool) (*os.Root, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && create {
		if mkdirErr := root.Mkdir(name, 0o755); mkdirErr != nil &&
			!errors.Is(mkdirErr, os.ErrExist) {
			return nil, fmt.Errorf("create herdr directory %s: %w", name, mkdirErr)
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect herdr directory %s: %w", name, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("herdr directory %s is not a non-symlink directory", name)
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open herdr directory %s: %w", name, err)
	}
	openedInfo, statErr := child.Stat(".")
	currentInfo, currentErr := root.Lstat(name)
	if statErr != nil || currentErr != nil ||
		!os.SameFile(info, openedInfo) ||
		!os.SameFile(info, currentInfo) {
		// The identity failure is authoritative; closing this rejected handle is cleanup only.
		_ = child.Close()
		return nil, fmt.Errorf("herdr directory %s identity changed while opening", name)
	}
	return child, nil
}

func (p *herdrWorktreeParentProof) close() error {
	if p == nil {
		return nil
	}
	var errs []error
	for _, root := range []*os.Root{p.worktreesRoot, p.fanoutRoot, p.projectRoot} {
		if root != nil {
			errs = append(errs, root.Close())
		}
	}
	return errors.Join(errs...)
}

// ResolveHerdrRepoIdentity returns the physical Git common directory, source
// worktree root, and worktree-specific git directory expected in Herdr
// worktree provenance.
func ResolveHerdrRepoIdentity(root string) (HerdrRepoIdentity, error) {
	repoKey, err := gitTrim(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("resolve herdr repo key: %w", err)
	}
	repoRoot, err := gitTrim(root, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("resolve herdr repo root: %w", err)
	}
	gitDir, err := gitTrim(root, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("resolve herdr worktree git dir: %w", err)
	}
	repoKey, err = physicalHerdrPath(repoKey)
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("canonicalize herdr repo key: %w", err)
	}
	repoRoot, err = physicalHerdrPath(repoRoot)
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("canonicalize herdr repo root: %w", err)
	}
	gitDir, err = physicalHerdrPath(gitDir)
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("canonicalize herdr worktree git dir: %w", err)
	}
	info, err := os.Stat(gitDir)
	if err != nil {
		return HerdrRepoIdentity{}, fmt.Errorf("inspect herdr worktree git dir %s: %w", gitDir, err)
	}
	if !info.IsDir() {
		return HerdrRepoIdentity{}, fmt.Errorf("herdr worktree git dir %s is not a directory", gitDir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return HerdrRepoIdentity{}, fmt.Errorf("herdr worktree git dir %s has no physical identity", gitDir)
	}
	return HerdrRepoIdentity{
		RepoKey:      repoKey,
		RepoRoot:     repoRoot,
		GitDir:       gitDir,
		GitDirDevice: uint64(stat.Dev),
		GitDirInode:  stat.Ino,
	}, nil
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

// ObserveBranchGitDir reads a ref through a previously validated physical
// worktree-specific Git directory instead of a mutable checkout path.
func ObserveBranchGitDir(gitDir, fullRef string) (oid string, found bool, err error) {
	out, err := git(gitDir, "--git-dir=.", "rev-parse", "--verify", "--quiet", fullRef)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("observe branch %s through saved git dir: %w", fullRef, err)
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

// ReserveBranchGitDir atomically creates fullRef through the physical Git
// directory persisted in the launch intent.
func ReserveBranchGitDir(gitDir, fullRef, baseSHA string) error {
	if !strings.HasPrefix(fullRef, "refs/heads/") || !fullCommitSHA.MatchString(baseSHA) {
		return fmt.Errorf("invalid atomic branch reservation %s -> %s", fullRef, baseSHA)
	}
	const emptyOID = "0000000000000000000000000000000000000000"
	if _, err := git(
		gitDir,
		"--git-dir=.",
		"update-ref",
		"--create-reflog",
		fullRef,
		baseSHA,
		emptyOID,
	); err != nil {
		return fmt.Errorf("reserve branch %s at %s through saved git dir: %w", fullRef, baseSHA, err)
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

// VerifyReservedBranchGitDir checks the immutable base and reserved tip
// through the physical Git directory persisted in the launch intent.
func VerifyReservedBranchGitDir(gitDir, resolvedBaseRef, baseSHA, fullRef string) error {
	if err := VerifyReservedBranchBaseGitDir(gitDir, resolvedBaseRef, baseSHA); err != nil {
		return err
	}
	branchOID, found, err := ObserveBranchGitDir(gitDir, fullRef)
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

// VerifyReservedBranchBaseGitDir rechecks the immutable base through the
// physical Git directory persisted in the launch intent.
func VerifyReservedBranchBaseGitDir(gitDir, resolvedBaseRef, baseSHA string) error {
	currentBase, err := gitTrim(
		gitDir,
		"--git-dir=.",
		"rev-parse",
		"--verify",
		resolvedBaseRef+"^{commit}",
	)
	if err != nil {
		return fmt.Errorf("recheck resolved base %s through saved git dir: %w", resolvedBaseRef, err)
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

// CheckoutGitStateGitDir observes checkout registration and HEAD through the
// physical Git directory persisted in the launch intent.
func CheckoutGitStateGitDir(
	gitDir, checkoutPath, fullRef string,
) (pathAbsent, registered bool, headSHA string, err error) {
	info, statErr := os.Lstat(checkoutPath)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		pathAbsent = true
	case statErr != nil:
		return false, false, "", fmt.Errorf("inspect checkout path %s: %w", checkoutPath, statErr)
	case !info.IsDir():
		return false, false, "", fmt.Errorf("checkout path %s is not a directory", checkoutPath)
	}
	registered, err = checkoutRegisteredGitDir(gitDir, checkoutPath)
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
			return pathAbsent, registered, headSHA, fmt.Errorf("checkout branch %s does not match %s", branch, fullRef)
		}
	}
	return pathAbsent, registered, headSHA, nil
}

func checkoutRegistered(root, checkoutPath string) (bool, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("list git worktrees: %w", err)
	}
	return checkoutRegisteredInOutput(out, checkoutPath)
}

func checkoutRegisteredGitDir(gitDir, checkoutPath string) (bool, error) {
	out, err := git(gitDir, "--git-dir=.", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, fmt.Errorf("list git worktrees through saved git dir: %w", err)
	}
	return checkoutRegisteredInOutput(out, checkoutPath)
}

func checkoutRegisteredInOutput(out []byte, checkoutPath string) (bool, error) {
	want, err := filepath.Abs(checkoutPath)
	if err != nil {
		return false, fmt.Errorf("resolve checkout path %s: %w", checkoutPath, err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(want); resolveErr == nil {
		want = resolved
	}
	for field := range strings.SplitSeq(string(out), "\x00") {
		path, ok := strings.CutPrefix(field, "worktree ")
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

type herdrCheckoutProof struct {
	checkoutRoot *os.Root
	gitDirRoot   *os.Root
	gitDirPath   string
}

func (p *herdrCheckoutProof) close() {
	if p == nil {
		return
	}
	if p.gitDirRoot != nil {
		_ = p.gitDirRoot.Close()
	}
	if p.checkoutRoot != nil {
		_ = p.checkoutRoot.Close()
	}
}

// EnsureHerdrOwnershipMarker creates the checkout-side nonce proof or adopts
// the exact existing proof during response-loss reconciliation. The checkout
// and its linked-worktree Git dir are opened and pinned before marker I/O.
func EnsureHerdrOwnershipMarker(projectRoot, checkoutPath, fullRef, headSHA, nonce string) (string, error) {
	if strings.TrimSpace(nonce) == "" || strings.ContainsAny(nonce, "\r\n\x00") {
		return "", fmt.Errorf("invalid herdr worktree ownership nonce")
	}
	proof, err := openHerdrCheckoutProof(projectRoot, checkoutPath, fullRef, headSHA)
	if err != nil {
		return "", err
	}
	defer proof.close()
	markerPath := filepath.Join(proof.gitDirPath, herdrOwnershipMarkerName)
	f, err := proof.gitDirRoot.OpenFile(herdrOwnershipMarkerName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if verifyErr := verifyHerdrOwnershipMarkerFile(proof.gitDirRoot, nonce); verifyErr != nil {
			return "", errors.Join(fmt.Errorf("create herdr ownership marker: %w", err), verifyErr)
		}
		if syncErr := syncHerdrOwnershipMarkerDirectory(proof.gitDirRoot); syncErr != nil {
			return "", fmt.Errorf("sync existing herdr ownership marker directory: %w", syncErr)
		}
		return markerPath, nil
	}
	if _, err := f.WriteString(nonce + "\n"); err != nil {
		// The write error is the actionable failure; Close cannot make the
		// incomplete marker safe to adopt.
		_ = f.Close()
		return "", fmt.Errorf("write herdr ownership marker: %w", err)
	}
	if err := f.Sync(); err != nil {
		// The sync error is actionable; Close cannot make durability proven.
		_ = f.Close()
		return "", fmt.Errorf("sync herdr ownership marker: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close herdr ownership marker: %w", err)
	}
	if err := syncHerdrOwnershipMarkerDirectory(proof.gitDirRoot); err != nil {
		return "", fmt.Errorf("sync herdr ownership marker directory: %w", err)
	}
	if err := verifyHerdrOwnershipMarkerFile(proof.gitDirRoot, nonce); err != nil {
		return "", err
	}
	return markerPath, nil
}

func syncHerdrOwnershipMarkerDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}

func VerifyHerdrOwnershipMarker(projectRoot, checkoutPath, fullRef, headSHA, markerPath, nonce string) error {
	proof, err := openHerdrCheckoutProof(projectRoot, checkoutPath, fullRef, headSHA)
	if err != nil {
		return err
	}
	defer proof.close()
	wantPath := filepath.Join(proof.gitDirPath, herdrOwnershipMarkerName)
	if filepath.Clean(markerPath) != filepath.Clean(wantPath) {
		return fmt.Errorf("herdr ownership marker path changed: got %s want %s", markerPath, wantPath)
	}
	return verifyHerdrOwnershipMarkerFile(proof.gitDirRoot, nonce)
}

func openHerdrCheckoutProof(projectRoot, checkoutPath, fullRef, headSHA string) (*herdrCheckoutProof, error) {
	if !strings.HasPrefix(fullRef, "refs/heads/") || !fullCommitSHA.MatchString(headSHA) {
		return nil, fmt.Errorf("invalid herdr checkout proof request")
	}
	parentProof, err := openHerdrWorktreeParent(projectRoot, checkoutPath, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		// The checkout proof result is authoritative; parent handle cleanup cannot change it.
		_ = parentProof.close()
	}()
	pathInfo, err := parentProof.worktreesRoot.Lstat(parentProof.leaf)
	if err != nil {
		return nil, fmt.Errorf("inspect herdr checkout root: %w", err)
	}
	if !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("herdr checkout root is not a non-symlink directory")
	}
	checkoutRoot, err := parentProof.worktreesRoot.OpenRoot(parentProof.leaf)
	if err != nil {
		return nil, fmt.Errorf("open herdr checkout root: %w", err)
	}
	proof := &herdrCheckoutProof{checkoutRoot: checkoutRoot}
	fail := func(err error) error {
		proof.close()
		return err
	}
	openedCheckoutInfo, err := checkoutRoot.Stat(".")
	if err != nil || !os.SameFile(pathInfo, openedCheckoutInfo) {
		return nil, fail(fmt.Errorf("herdr checkout root identity changed while opening"))
	}
	registered, err := checkoutRegistered(projectRoot, checkoutPath)
	if err != nil {
		return nil, fail(err)
	}
	if !registered {
		return nil, fail(fmt.Errorf("herdr checkout is not registered"))
	}

	dotGitInfo, err := checkoutRoot.Lstat(".git")
	if err != nil || !dotGitInfo.Mode().IsRegular() {
		return nil, fail(fmt.Errorf("herdr checkout .git is not a regular file"))
	}
	dotGit, err := checkoutRoot.Open(".git")
	if err != nil {
		return nil, fail(fmt.Errorf("open herdr checkout .git: %w", err))
	}
	openedDotGitInfo, statErr := dotGit.Stat()
	if statErr != nil || !os.SameFile(dotGitInfo, openedDotGitInfo) {
		_ = dotGit.Close()
		return nil, fail(fmt.Errorf("herdr checkout .git identity changed while opening"))
	}
	dotGitData, readErr := readSmallHerdrMetadata(dotGit)
	closeErr := dotGit.Close()
	if readErr != nil {
		return nil, fail(fmt.Errorf("read herdr checkout .git: %w", readErr))
	}
	if closeErr != nil {
		return nil, fail(fmt.Errorf("close herdr checkout .git: %w", closeErr))
	}
	gitDirPath, err := parseHerdrGitDirFile(dotGitData)
	if err != nil {
		return nil, fail(err)
	}

	commonDir, err := gitTrim(projectRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fail(fmt.Errorf("resolve herdr common git dir: %w", err))
	}
	commonDir, err = physicalHerdrPath(commonDir)
	if err != nil {
		return nil, fail(fmt.Errorf("canonicalize herdr common git dir: %w", err))
	}
	gitDirPath = filepath.Clean(gitDirPath)
	physicalGitDir, err := filepath.EvalSymlinks(gitDirPath)
	if err != nil || filepath.Clean(physicalGitDir) != gitDirPath {
		return nil, fail(fmt.Errorf("herdr linked-worktree git dir is not a canonical non-symlink path"))
	}
	if filepath.Dir(gitDirPath) != filepath.Join(commonDir, "worktrees") {
		return nil, fail(fmt.Errorf("herdr linked-worktree git dir is outside the repository worktrees directory"))
	}
	gitDirInfo, err := os.Lstat(gitDirPath)
	if err != nil || !gitDirInfo.IsDir() || gitDirInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fail(fmt.Errorf("herdr linked-worktree git dir is not a non-symlink directory"))
	}
	gitDirRoot, err := os.OpenRoot(gitDirPath)
	if err != nil {
		return nil, fail(fmt.Errorf("open herdr linked-worktree git dir: %w", err))
	}
	proof.gitDirRoot = gitDirRoot
	proof.gitDirPath = gitDirPath
	openedGitDirInfo, err := gitDirRoot.Stat(".")
	if err != nil || !os.SameFile(gitDirInfo, openedGitDirInfo) {
		return nil, fail(fmt.Errorf("herdr linked-worktree git dir identity changed while opening"))
	}
	backlinkData, err := readHerdrRootMetadata(gitDirRoot, "gitdir")
	if err != nil {
		return nil, fail(err)
	}
	backlinkPath := filepath.Clean(strings.TrimSpace(string(backlinkData)))
	if !filepath.IsAbs(backlinkPath) {
		return nil, fail(fmt.Errorf("herdr linked-worktree backlink is not absolute"))
	}
	backlinkInfo, err := os.Stat(backlinkPath)
	if err != nil || !os.SameFile(dotGitInfo, backlinkInfo) {
		return nil, fail(fmt.Errorf("herdr linked-worktree backlink does not identify the checkout .git file"))
	}
	headData, err := readHerdrRootMetadata(gitDirRoot, "HEAD")
	if err != nil {
		return nil, fail(err)
	}
	if string(headData) != "ref: "+fullRef+"\n" {
		return nil, fail(fmt.Errorf("herdr linked-worktree HEAD does not match %s", fullRef))
	}
	branchOID, found, err := ObserveBranch(projectRoot, fullRef)
	if err != nil {
		return nil, fail(err)
	}
	if !found || branchOID != headSHA {
		return nil, fail(fmt.Errorf("herdr checkout branch %s does not point at %s", fullRef, headSHA))
	}
	currentPathInfo, err := parentProof.worktreesRoot.Lstat(parentProof.leaf)
	if err != nil || !os.SameFile(pathInfo, currentPathInfo) {
		return nil, fail(fmt.Errorf("herdr checkout root identity changed during proof"))
	}
	return proof, nil
}

func parseHerdrGitDirFile(data []byte) (string, error) {
	line := strings.TrimSuffix(string(data), "\n")
	if strings.ContainsAny(line, "\x00\r\n") {
		return "", fmt.Errorf("herdr checkout .git has invalid contents")
	}
	path, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok || !filepath.IsAbs(path) {
		return "", fmt.Errorf("herdr checkout .git has no absolute gitdir")
	}
	return path, nil
}

func readHerdrRootMetadata(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("herdr linked-worktree %s is not a regular file", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open herdr linked-worktree %s: %w", name, err)
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("herdr linked-worktree %s identity changed while opening", name)
	}
	data, readErr := readSmallHerdrMetadata(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read herdr linked-worktree %s: %w", name, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close herdr linked-worktree %s: %w", name, closeErr)
	}
	return data, nil
}

func readSmallHerdrMetadata(file *os.File) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, err
	}
	if len(data) > 4096 {
		return nil, fmt.Errorf("metadata exceeds 4096 bytes")
	}
	return data, nil
}

func verifyHerdrOwnershipMarkerFile(gitDirRoot *os.Root, nonce string) error {
	info, err := gitDirRoot.Lstat(herdrOwnershipMarkerName)
	if err != nil {
		return fmt.Errorf("inspect herdr ownership marker: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		return fmt.Errorf("herdr ownership marker has unsafe mode %s", info.Mode())
	}
	file, err := gitDirRoot.Open(herdrOwnershipMarkerName)
	if err != nil {
		return fmt.Errorf("open herdr ownership marker: %w", err)
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return fmt.Errorf("herdr ownership marker identity changed while opening")
	}
	body, readErr := readSmallHerdrMetadata(file)
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read herdr ownership marker: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close herdr ownership marker: %w", closeErr)
	}
	if string(body) != nonce+"\n" {
		return fmt.Errorf("herdr ownership marker nonce mismatch")
	}
	return nil
}
