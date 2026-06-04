package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
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
	if got := gitOutput(t, res.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); got != "fanout/child-1" {
		t.Fatalf("new worktree branch = %q, want fanout/child-1", got)
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
	for _, line := range strings.Split(body, "\n") {
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

	if err := CleanupCreated(res.Plan); err != nil {
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

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
