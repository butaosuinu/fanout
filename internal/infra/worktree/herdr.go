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
	cmd := exec.Command("git", "worktree", "list", "--porcelain", "-z")
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
	pathInfo, err := os.Lstat(checkoutPath)
	if err != nil {
		return nil, fmt.Errorf("inspect herdr checkout root: %w", err)
	}
	if !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("herdr checkout root is not a non-symlink directory")
	}
	checkoutRoot, err := os.OpenRoot(checkoutPath)
	if err != nil {
		return nil, fmt.Errorf("open herdr checkout root: %w", err)
	}
	proof := &herdrCheckoutProof{checkoutRoot: checkoutRoot}
	fail := func(err error) (*herdrCheckoutProof, error) {
		proof.close()
		return nil, err
	}
	openedCheckoutInfo, err := checkoutRoot.Stat(".")
	if err != nil || !os.SameFile(pathInfo, openedCheckoutInfo) {
		return fail(fmt.Errorf("herdr checkout root identity changed while opening"))
	}
	registered, err := checkoutRegistered(projectRoot, checkoutPath)
	if err != nil {
		return fail(err)
	}
	if !registered {
		return fail(fmt.Errorf("herdr checkout is not registered"))
	}

	dotGitInfo, err := checkoutRoot.Lstat(".git")
	if err != nil || !dotGitInfo.Mode().IsRegular() {
		return fail(fmt.Errorf("herdr checkout .git is not a regular file"))
	}
	dotGit, err := checkoutRoot.Open(".git")
	if err != nil {
		return fail(fmt.Errorf("open herdr checkout .git: %w", err))
	}
	openedDotGitInfo, statErr := dotGit.Stat()
	if statErr != nil || !os.SameFile(dotGitInfo, openedDotGitInfo) {
		_ = dotGit.Close()
		return fail(fmt.Errorf("herdr checkout .git identity changed while opening"))
	}
	dotGitData, readErr := readSmallHerdrMetadata(dotGit)
	closeErr := dotGit.Close()
	if readErr != nil {
		return fail(fmt.Errorf("read herdr checkout .git: %w", readErr))
	}
	if closeErr != nil {
		return fail(fmt.Errorf("close herdr checkout .git: %w", closeErr))
	}
	gitDirPath, err := parseHerdrGitDirFile(dotGitData)
	if err != nil {
		return fail(err)
	}

	commonDir, err := gitTrim(projectRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fail(fmt.Errorf("resolve herdr common git dir: %w", err))
	}
	commonDir, err = physicalHerdrPath(commonDir)
	if err != nil {
		return fail(fmt.Errorf("canonicalize herdr common git dir: %w", err))
	}
	gitDirPath = filepath.Clean(gitDirPath)
	physicalGitDir, err := filepath.EvalSymlinks(gitDirPath)
	if err != nil || filepath.Clean(physicalGitDir) != gitDirPath {
		return fail(fmt.Errorf("herdr linked-worktree git dir is not a canonical non-symlink path"))
	}
	if filepath.Dir(gitDirPath) != filepath.Join(commonDir, "worktrees") {
		return fail(fmt.Errorf("herdr linked-worktree git dir is outside the repository worktrees directory"))
	}
	gitDirInfo, err := os.Lstat(gitDirPath)
	if err != nil || !gitDirInfo.IsDir() || gitDirInfo.Mode()&os.ModeSymlink != 0 {
		return fail(fmt.Errorf("herdr linked-worktree git dir is not a non-symlink directory"))
	}
	gitDirRoot, err := os.OpenRoot(gitDirPath)
	if err != nil {
		return fail(fmt.Errorf("open herdr linked-worktree git dir: %w", err))
	}
	proof.gitDirRoot = gitDirRoot
	proof.gitDirPath = gitDirPath
	openedGitDirInfo, err := gitDirRoot.Stat(".")
	if err != nil || !os.SameFile(gitDirInfo, openedGitDirInfo) {
		return fail(fmt.Errorf("herdr linked-worktree git dir identity changed while opening"))
	}
	backlinkData, err := readHerdrRootMetadata(gitDirRoot, "gitdir")
	if err != nil {
		return fail(err)
	}
	backlinkPath := filepath.Clean(strings.TrimSpace(string(backlinkData)))
	if !filepath.IsAbs(backlinkPath) {
		return fail(fmt.Errorf("herdr linked-worktree backlink is not absolute"))
	}
	backlinkInfo, err := os.Stat(backlinkPath)
	if err != nil || !os.SameFile(dotGitInfo, backlinkInfo) {
		return fail(fmt.Errorf("herdr linked-worktree backlink does not identify the checkout .git file"))
	}
	headData, err := readHerdrRootMetadata(gitDirRoot, "HEAD")
	if err != nil {
		return fail(err)
	}
	if string(headData) != "ref: "+fullRef+"\n" {
		return fail(fmt.Errorf("herdr linked-worktree HEAD does not match %s", fullRef))
	}
	branchOID, found, err := ObserveBranch(projectRoot, fullRef)
	if err != nil {
		return fail(err)
	}
	if !found || branchOID != headSHA {
		return fail(fmt.Errorf("herdr checkout branch %s does not point at %s", fullRef, headSHA))
	}
	currentPathInfo, err := os.Lstat(checkoutPath)
	if err != nil || !os.SameFile(pathInfo, currentPathInfo) {
		return fail(fmt.Errorf("herdr checkout root identity changed during proof"))
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
