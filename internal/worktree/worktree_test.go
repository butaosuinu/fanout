package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPrepareRefreshesBaseBeforeCreatingWorktree(t *testing.T) {
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "repo")

	gitTest(t, "", "init", "--bare", origin)
	gitTest(t, "", "init", seed)
	gitTest(t, seed, "checkout", "-b", "main")
	writeFile(t, filepath.Join(seed, "file.txt"), "one\n")
	gitTest(t, seed, "add", "file.txt")
	gitTest(t, seed, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "one")
	gitTest(t, seed, "remote", "add", "origin", origin)
	gitTest(t, seed, "push", "-u", "origin", "main")

	gitTest(t, "", "clone", origin, repo)
	gitTest(t, repo, "checkout", "main")
	oldHead := gitOutput(t, repo, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(seed, "file.txt"), "two\n")
	gitTest(t, seed, "add", "file.txt")
	gitTest(t, seed, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "two")
	gitTest(t, seed, "push", "origin", "main")
	newHead := gitOutput(t, seed, "rev-parse", "HEAD")
	if oldHead == newHead {
		t.Fatal("test setup did not advance origin")
	}

	res, err := Prepare(Options{
		ProjectRoot: repo,
		Slug:        "child-101",
		BranchName:  "fanout/child-101",
		BaseBranch:  "main",
	})
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}
	if res.AlreadyExists {
		t.Fatal("Prepare() unexpectedly reported existing worktree")
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "HEAD"); got != newHead {
		t.Fatalf("worktree HEAD = %s, want %s", got, newHead)
	}
	if got := gitOutput(t, repo, "rev-parse", "main"); got != newHead {
		t.Fatalf("local main = %s, want %s", got, newHead)
	}
	if got := gitOutput(t, repo, "status", "--short"); got != "" {
		t.Fatalf("parent repo status after Prepare() = %q, want clean", got)
	}
}

func TestPrepareSupportsOriginQualifiedBase(t *testing.T) {
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "repo")

	gitTest(t, "", "init", "--bare", origin)
	gitTest(t, "", "init", seed)
	gitTest(t, seed, "checkout", "-b", "main")
	writeFile(t, filepath.Join(seed, "file.txt"), "one\n")
	gitTest(t, seed, "add", "file.txt")
	gitTest(t, seed, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "one")
	gitTest(t, seed, "remote", "add", "origin", origin)
	gitTest(t, seed, "push", "-u", "origin", "main")

	gitTest(t, "", "clone", origin, repo)
	gitTest(t, repo, "checkout", "main")
	oldLocalHead := gitOutput(t, repo, "rev-parse", "main")

	writeFile(t, filepath.Join(seed, "file.txt"), "two\n")
	gitTest(t, seed, "add", "file.txt")
	gitTest(t, seed, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "two")
	gitTest(t, seed, "push", "origin", "main")
	newHead := gitOutput(t, seed, "rev-parse", "HEAD")

	res, err := Prepare(Options{
		ProjectRoot: repo,
		Slug:        "child-101",
		BranchName:  "fanout/child-101",
		BaseBranch:  "origin/main",
	})
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "HEAD"); got != newHead {
		t.Fatalf("worktree HEAD = %s, want %s", got, newHead)
	}
	if got := gitOutput(t, repo, "rev-parse", "main"); got != oldLocalHead {
		t.Fatalf("local main = %s, want unchanged %s", got, oldLocalHead)
	}
}

func TestPrepareBestEffortToleratesDirtyCheckedOutBase(t *testing.T) {
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "repo")

	gitTest(t, "", "init", "--bare", origin)
	gitTest(t, "", "init", seed)
	gitTest(t, seed, "checkout", "-b", "main")
	writeFile(t, filepath.Join(seed, "file.txt"), "one\n")
	gitTest(t, seed, "add", "file.txt")
	gitTest(t, seed, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "one")
	gitTest(t, seed, "remote", "add", "origin", origin)
	gitTest(t, seed, "push", "-u", "origin", "main")

	gitTest(t, "", "clone", origin, repo)
	gitTest(t, repo, "checkout", "main")
	localHead := gitOutput(t, repo, "rev-parse", "main")

	// Advance origin so a refresh would try to fast-forward the local main.
	writeFile(t, filepath.Join(seed, "file.txt"), "two\n")
	gitTest(t, seed, "add", "file.txt")
	gitTest(t, seed, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "two")
	gitTest(t, seed, "push", "origin", "main")

	// Make the checked-out main dirty so the fast-forward refuses (the TUI `n` repro).
	writeFile(t, filepath.Join(repo, "file.txt"), "local edit\n")

	// Strict refresh still refuses on a dirty checked-out base.
	if _, err := Prepare(Options{
		ProjectRoot: repo,
		Slug:        "strict",
		BranchName:  "fanout/strict",
		BaseBranch:  "main",
	}); err == nil {
		t.Fatal("Prepare() strict = nil error, want refusal on dirty checked-out base")
	}

	// Best-effort tolerates the failed refresh and branches off local main as-is.
	res, err := Prepare(Options{
		ProjectRoot:       repo,
		Slug:              "best-effort",
		BranchName:        "fanout/best-effort",
		BaseBranch:        "main",
		RefreshBestEffort: true,
	})
	if err != nil {
		t.Fatalf("Prepare() best-effort failed: %v", err)
	}
	if res.RefreshWarning == nil {
		t.Fatal("Prepare() best-effort RefreshWarning = nil, want the skipped refresh error")
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "HEAD"); got != localHead {
		t.Fatalf("worktree HEAD = %s, want local main %s", got, localHead)
	}
	if got := gitOutput(t, repo, "rev-parse", "main"); got != localHead {
		t.Fatalf("local main = %s, want unchanged %s", got, localHead)
	}
	if got := gitOutput(t, repo, "status", "--short"); got == "" {
		t.Fatal("local edit was lost; repo status is clean")
	}
}

func TestPrepareBestEffortBranchesOffLocalCommitsWhenDiverged(t *testing.T) {
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "repo")

	gitTest(t, "", "init", "--bare", origin)
	gitTest(t, "", "init", seed)
	gitTest(t, seed, "checkout", "-b", "main")
	writeFile(t, filepath.Join(seed, "file.txt"), "one\n")
	gitTest(t, seed, "add", "file.txt")
	gitTest(t, seed, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "one")
	gitTest(t, seed, "remote", "add", "origin", origin)
	gitTest(t, seed, "push", "-u", "origin", "main")

	gitTest(t, "", "clone", origin, repo)
	gitTest(t, repo, "checkout", "main")

	// A committed local-only change leaves main ahead of origin; a strict refresh
	// would refuse to fast-forward, but the user opted to keep these commits.
	writeFile(t, filepath.Join(repo, "local.txt"), "local only\n")
	gitTest(t, repo, "add", "local.txt")
	gitTest(t, repo, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "local only")
	localHead := gitOutput(t, repo, "rev-parse", "main")

	res, err := Prepare(Options{
		ProjectRoot:       repo,
		Slug:              "ahead",
		BranchName:        "fanout/ahead",
		BaseBranch:        "main",
		RefreshBestEffort: true,
	})
	if err != nil {
		t.Fatalf("Prepare() best-effort failed: %v", err)
	}
	if res.RefreshWarning == nil {
		t.Fatal("Prepare() best-effort RefreshWarning = nil, want the tolerated divergence error")
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "HEAD"); got != localHead {
		t.Fatalf("worktree HEAD = %s, want local main %s (local commits must be included)", got, localHead)
	}
	if _, err := os.Stat(filepath.Join(res.WorktreePath, "local.txt")); err != nil {
		t.Fatalf("worktree missing local-only commit content: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", "main"); got != localHead {
		t.Fatalf("local main = %s, want unchanged %s", got, localHead)
	}
}

func TestPrepareAllowsMissingOriginFromCurrentBranch(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	baseHead := gitOutput(t, repo, "rev-parse", "HEAD")

	res, err := Prepare(Options{
		ProjectRoot:        repo,
		Slug:               "local-task",
		BranchName:         "fanout/local-task",
		AllowMissingOrigin: true,
	})
	if err != nil {
		t.Fatalf("Prepare() failed without origin: %v", err)
	}
	if res.Refresh {
		t.Fatal("Prepare() plan refresh = true, want false without origin")
	}
	if !strings.Contains(res.RefreshSkippedReason, "origin remote is not configured") {
		t.Fatalf("RefreshSkippedReason = %q, want origin skip message", res.RefreshSkippedReason)
	}
	if res.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want current branch main", res.BaseBranch)
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "HEAD"); got != baseHead {
		t.Fatalf("worktree HEAD = %s, want local base %s", got, baseHead)
	}
}

func TestPrepareAllowsMissingOriginWithExplicitLocalBase(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	baseHead := gitOutput(t, repo, "rev-parse", "main")

	res, err := Prepare(Options{
		ProjectRoot:        repo,
		Slug:               "local-base-task",
		BranchName:         "fanout/local-base-task",
		BaseBranch:         "main",
		AllowMissingOrigin: true,
	})
	if err != nil {
		t.Fatalf("Prepare() failed with explicit local base and no origin: %v", err)
	}
	if res.Refresh {
		t.Fatal("Prepare() plan refresh = true, want false without origin")
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "HEAD"); got != baseHead {
		t.Fatalf("worktree HEAD = %s, want local base %s", got, baseHead)
	}
}

func TestPrepareRejectsOriginQualifiedBaseWithoutOrigin(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)

	_, err := Prepare(Options{
		ProjectRoot:        repo,
		Slug:               "remote-base-task",
		BranchName:         "fanout/remote-base-task",
		BaseBranch:         "origin/main",
		AllowMissingOrigin: true,
	})
	if err == nil || !strings.Contains(err.Error(), `base branch "origin/main" requires origin remote`) {
		t.Fatalf("Prepare() error = %v, want origin-required error", err)
	}
}

func TestPrepareSkipsExistingWorktreePath(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	path := filepath.Join(dir, ".fanout", "worktrees", "child-1")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Prepare(Options{
		ProjectRoot: dir,
		Slug:        "child-1",
		BranchName:  "fanout/child-1",
		BaseBranch:  "main",
		NoRefresh:   true,
	})
	if err != nil {
		t.Fatalf("Prepare() existing path returned error: %v", err)
	}
	if !res.AlreadyExists {
		t.Fatal("Prepare() existing path did not report AlreadyExists")
	}
}

func TestPrepareSkipsExistingWorktreeFileWithoutCreatingBranch(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	writeFile(t, filepath.Join(dir, "file.txt"), "one\n")
	gitTest(t, dir, "add", "file.txt")
	gitTest(t, dir, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "one")
	baseBranch := gitOutput(t, dir, "branch", "--show-current")

	path := filepath.Join(dir, ".fanout", "worktrees", "child-1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "not a directory\n")

	res, err := Prepare(Options{
		ProjectRoot: dir,
		Slug:        "child-1",
		BranchName:  "fanout/child-1",
		BaseBranch:  baseBranch,
		NoRefresh:   true,
	})
	if err != nil {
		t.Fatalf("Prepare() existing file returned error: %v", err)
	}
	if !res.AlreadyExists {
		t.Fatal("Prepare() existing file did not report AlreadyExists")
	}
	if out := gitOutputAllowError(t, dir, "rev-parse", "--verify", "fanout/child-1"); out.ok {
		t.Fatalf("branch was created at %s", out.value)
	}
}

func TestPreparePrunesStaleWorktreeRegistrationBeforeAdd(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	writeFile(t, filepath.Join(dir, "file.txt"), "one\n")
	gitTest(t, dir, "add", "file.txt")
	gitTest(t, dir, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "one")
	baseBranch := gitOutput(t, dir, "branch", "--show-current")

	stalePath := filepath.Join(dir, ".fanout", "worktrees", "child-1")
	gitTest(t, dir, "worktree", "add", "-b", "fanout/stale-child-1", stalePath, baseBranch)
	if err := os.RemoveAll(stalePath); err != nil {
		t.Fatal(err)
	}

	res, err := Prepare(Options{
		ProjectRoot: dir,
		Slug:        "child-1",
		BranchName:  "fanout/child-1",
		BaseBranch:  baseBranch,
		NoRefresh:   true,
	})
	if err != nil {
		t.Fatalf("Prepare() with stale worktree registration failed: %v", err)
	}
	if res.AlreadyExists {
		t.Fatal("Prepare() unexpectedly reported existing worktree")
	}
	if !res.BranchCreated {
		t.Fatal("Prepare() did not report BranchCreated for a fresh branch")
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); got != "fanout/child-1" {
		t.Fatalf("new worktree branch = %q, want fanout/child-1", got)
	}
	if got := gitOutput(t, dir, "status", "--short"); got != "" {
		t.Fatalf("parent repo status after Prepare() = %q, want clean", got)
	}
}

func TestPrepareReusesExistingChildBranch(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	writeFile(t, filepath.Join(dir, "file.txt"), "base\n")
	gitTest(t, dir, "add", "file.txt")
	gitTest(t, dir, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "base")
	baseBranch := gitOutput(t, dir, "branch", "--show-current")

	gitTest(t, dir, "switch", "-c", "fanout/child-1")
	writeFile(t, filepath.Join(dir, "file.txt"), "child branch\n")
	gitTest(t, dir, "add", "file.txt")
	gitTest(t, dir, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "child")
	childHead := gitOutput(t, dir, "rev-parse", "HEAD")
	gitTest(t, dir, "switch", baseBranch)

	res, err := Prepare(Options{
		ProjectRoot: dir,
		Slug:        "child-1",
		BranchName:  "fanout/child-1",
		BaseBranch:  baseBranch,
		NoRefresh:   true,
	})
	if err != nil {
		t.Fatalf("Prepare() with existing branch failed: %v", err)
	}
	if res.AlreadyExists {
		t.Fatal("Prepare() unexpectedly reported existing worktree")
	}
	if res.BranchCreated {
		t.Fatal("Prepare() reported BranchCreated for an existing branch")
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "HEAD"); got != childHead {
		t.Fatalf("worktree HEAD = %s, want existing child branch %s", got, childHead)
	}
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); got != "fanout/child-1" {
		t.Fatalf("new worktree branch = %q, want fanout/child-1", got)
	}
	body, err := os.ReadFile(filepath.Join(res.WorktreePath, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != "child branch" {
		t.Fatalf("worktree file content = %q, want existing branch content", got)
	}
	if got := gitOutput(t, dir, "status", "--short"); got != "" {
		t.Fatalf("parent repo status after Prepare() = %q, want clean", got)
	}
}

func TestEnsureLocalExcludeIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")

	if err := EnsureLocalExclude(dir); err != nil {
		t.Fatalf("EnsureLocalExclude() failed: %v", err)
	}
	if err := EnsureLocalExclude(dir); err != nil {
		t.Fatalf("EnsureLocalExclude() second call failed: %v", err)
	}

	excludePath := gitOutput(t, dir, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(dir, excludePath)
	}
	body, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range localExcludePatterns {
		if got := countLines(string(body), pattern); got != 1 {
			t.Fatalf("exclude pattern %s count = %d, want 1\n%s", pattern, got, body)
		}
	}
}

func countLines(body, want string) int {
	var count int
	for line := range strings.SplitSeq(body, "\n") {
		if line == want {
			count++
		}
	}
	return count
}

func TestCleanupCreatedRemovesWorktreeAndBranch(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	writeFile(t, filepath.Join(dir, "file.txt"), "one\n")
	gitTest(t, dir, "add", "file.txt")
	gitTest(t, dir, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "one")
	baseBranch := gitOutput(t, dir, "branch", "--show-current")

	res, err := Prepare(Options{
		ProjectRoot: dir,
		Slug:        "child-1",
		BranchName:  "fanout/child-1",
		BaseBranch:  baseBranch,
		NoRefresh:   true,
	})
	if err != nil {
		t.Fatalf("Prepare() failed: %v", err)
	}

	if err := CleanupCreated(res); err != nil {
		t.Fatalf("CleanupCreated() failed: %v", err)
	}
	if _, err := os.Stat(res.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree path still exists or stat failed unexpectedly: %v", err)
	}
	if out := gitOutputAllowError(t, dir, "rev-parse", "--verify", "fanout/child-1"); out.ok {
		t.Fatalf("branch still exists at %s", out.value)
	}
	if got := gitOutput(t, dir, "status", "--short"); got != "" {
		t.Fatalf("parent repo status after cleanup = %q, want clean", got)
	}
}

func TestCleanupCreatedPreservesReusedBranch(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	writeFile(t, filepath.Join(dir, "file.txt"), "base\n")
	gitTest(t, dir, "add", "file.txt")
	gitTest(t, dir, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "base")
	baseBranch := gitOutput(t, dir, "branch", "--show-current")

	gitTest(t, dir, "switch", "-c", "fanout/child-1")
	writeFile(t, filepath.Join(dir, "file.txt"), "child branch\n")
	gitTest(t, dir, "add", "file.txt")
	gitTest(t, dir, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "child")
	childHead := gitOutput(t, dir, "rev-parse", "HEAD")
	gitTest(t, dir, "switch", baseBranch)

	res, err := Prepare(Options{
		ProjectRoot: dir,
		Slug:        "child-1",
		BranchName:  "fanout/child-1",
		BaseBranch:  baseBranch,
		NoRefresh:   true,
	})
	if err != nil {
		t.Fatalf("Prepare() with existing branch failed: %v", err)
	}
	if res.BranchCreated {
		t.Fatal("Prepare() reported BranchCreated for an existing branch")
	}

	if err := CleanupCreated(res); err != nil {
		t.Fatalf("CleanupCreated() failed: %v", err)
	}
	if _, err := os.Stat(res.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree path still exists or stat failed unexpectedly: %v", err)
	}
	if got := gitOutput(t, dir, "rev-parse", "--verify", "fanout/child-1"); got != childHead {
		t.Fatalf("branch HEAD after cleanup = %s, want preserved %s", got, childHead)
	}
	if got := gitOutput(t, dir, "status", "--short"); got != "" {
		t.Fatalf("parent repo status after cleanup = %q, want clean", got)
	}
}

func TestListRootsIncludesSiblingsAndExcludesFanoutChildren(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	top := gitOutput(t, repo, "rev-parse", "--show-toplevel")

	sibling := filepath.Join(t.TempDir(), "sibling")
	gitTest(t, repo, "worktree", "add", "-b", "feat-sib", sibling)
	siblingTop := gitOutput(t, sibling, "rev-parse", "--show-toplevel")

	child := filepath.Join(repo, ".fanout", "worktrees", "child-1")
	gitTest(t, repo, "worktree", "add", "-b", "fanout/child-1", child)

	roots, err := ListRoots(top)
	if err != nil {
		t.Fatalf("ListRoots: %v", err)
	}
	if !slices.Contains(roots, top) {
		t.Fatalf("roots missing projectRoot %q: %v", top, roots)
	}
	if !slices.Contains(roots, siblingTop) {
		t.Fatalf("roots missing sibling worktree %q: %v", siblingTop, roots)
	}
	childMarker := string(filepath.Separator) + filepath.Join(".fanout", "worktrees") + string(filepath.Separator)
	for _, r := range roots {
		if strings.Contains(r, childMarker) {
			t.Fatalf("roots must exclude fanout child worktrees, got %q in %v", r, roots)
		}
	}
}

func TestListRootsSkipsPrunableWorktrees(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	top := gitOutput(t, repo, "rev-parse", "--show-toplevel")

	stale := filepath.Join(t.TempDir(), "stale")
	gitTest(t, repo, "worktree", "add", "-b", "feat-stale", stale)
	// Removing the directory makes git annotate the entry `prunable`.
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}

	roots, err := ListRoots(top)
	if err != nil {
		t.Fatalf("ListRoots: %v", err)
	}
	for _, r := range roots {
		if strings.Contains(r, "stale") {
			t.Fatalf("prunable worktree must be skipped, got %q in %v", r, roots)
		}
	}
}

func TestListRootsFallsBackOutsideGitRepo(t *testing.T) {
	dir := t.TempDir()
	roots, err := ListRoots(dir)
	if err == nil {
		t.Fatal("expected an error when projectRoot is not a git work tree")
	}
	if len(roots) != 1 || roots[0] != dir {
		t.Fatalf("fallback roots = %v, want [%s]", roots, dir)
	}
}

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return string(out)
}

type gitOutputResult struct {
	value string
	ok    bool
}

func gitOutputAllowError(t *testing.T, dir string, args ...string) gitOutputResult {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return gitOutputResult{value: string(out)}
	}
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return gitOutputResult{value: string(out), ok: true}
}

func TestListFilesReturnsTrackedFilesOnly(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)

	// untracked + ignored files exist only in the owner checkout, not in a
	// fresh worktree off base, so they must not appear as completion candidates.
	writeFile(t, filepath.Join(repo, "untracked.txt"), "new\n")
	writeFile(t, filepath.Join(repo, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(repo, "ignored.txt"), "nope\n")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "sub", "nested.go"), "package sub\n")
	gitTest(t, repo, "add", "sub/nested.go")
	gitTest(t, repo, "commit", "-m", "add nested")

	files, err := ListFiles(repo)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	for _, want := range []string{"file.txt", "sub/nested.go"} {
		if !slices.Contains(files, want) {
			t.Fatalf("ListFiles missing tracked file %q: %v", want, files)
		}
	}
	for _, unwanted := range []string{"untracked.txt", "ignored.txt", ".gitignore"} {
		if slices.Contains(files, unwanted) {
			t.Fatalf("ListFiles must list tracked files only, got %q: %v", unwanted, files)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newCommittedRepoWithoutOrigin(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitTest(t, "", "init", "-b", "main", repo)
	gitTest(t, repo, "config", "user.name", "Fanout Test")
	gitTest(t, repo, "config", "user.email", "fanout@example.test")
	writeFile(t, filepath.Join(repo, "file.txt"), "base\n")
	gitTest(t, repo, "add", "file.txt")
	gitTest(t, repo, "commit", "-m", "base")
	return repo
}
