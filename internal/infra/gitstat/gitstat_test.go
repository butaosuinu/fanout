package gitstat

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseShortStat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		out       string
		additions int
		deletions int
	}{
		{name: "empty", out: "", additions: 0, deletions: 0},
		{name: "both plural", out: " 2 files changed, 12 insertions(+), 3 deletions(-)\n", additions: 12, deletions: 3},
		{name: "singular insertion", out: " 1 file changed, 1 insertion(+)\n", additions: 1, deletions: 0},
		{name: "singular deletion", out: " 1 file changed, 1 deletion(-)\n", additions: 0, deletions: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseShortStat(tc.out)
			if got.Additions != tc.additions || got.Deletions != tc.deletions {
				t.Fatalf("parseShortStat() = +%d/-%d, want +%d/-%d", got.Additions, got.Deletions, tc.additions, tc.deletions)
			}
		})
	}
}

func TestRunnerWorktreeCountsTrackedDiffAndPorcelainDirty(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "tracked.txt")
	gitTest(t, repo, "commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Runner{}.Worktree(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 2 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +2/-0 dirty", got)
	}
}

func TestRunnerWorktreeForcesUntrackedFilesIntoDirtyCheck(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	gitTest(t, repo, "commit", "--allow-empty", "-m", "initial")
	gitTest(t, repo, "config", "status.showUntrackedFiles", "no")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Runner{}.Worktree(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 0 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +0/-0 dirty", got)
	}
}

// initBaseRepo creates a repo whose "main" branch holds tracked.txt = "one\n"
// and checks out a "feature" branch with one committed line ("two\n") plus one
// uncommitted line ("three\n"): +2 vs main's merge-base, +1 vs HEAD.
func initBaseRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "tracked.txt")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "tracked.txt")
	gitTest(t, repo, "commit", "-m", "committed work")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestRunnerWorktreeCountsCommittedAndUncommittedAgainstBase(t *testing.T) {
	repo := initBaseRepo(t)

	got, err := Runner{}.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 2 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +2/-0 dirty (committed + uncommitted vs main)", got)
	}
}

func TestRunnerWorktreeFallsBackToOriginPrefixedBase(t *testing.T) {
	repo := initBaseRepo(t)
	// Move main behind origin/main only: the bare "main" candidate must fail
	// and the "origin/"+base fallback must resolve.
	gitTest(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	gitTest(t, repo, "branch", "-D", "main")

	got, err := Runner{}.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 2 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +2/-0 dirty via origin/main", got)
	}
}

func TestRunnerWorktreeEmptyBaseUsesOriginHEAD(t *testing.T) {
	repo := initBaseRepo(t)
	gitTest(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	gitTest(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	got, err := Runner{}.Worktree(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 2 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +2/-0 dirty via origin/HEAD", got)
	}
}

func TestRunnerWorktreeUnresolvableBaseFallsBackToHEAD(t *testing.T) {
	repo := initBaseRepo(t)

	got, err := Runner{}.Worktree(repo, "no-such-branch")
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 1 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +1/-0 dirty (HEAD fallback counts only uncommitted)", got)
	}
}

func TestRunnerWorktreePatch(t *testing.T) {
	tests := []struct {
		name            string
		baseRef         string
		prepare         func(*testing.T, string)
		wantErrContains string
		check           func(*testing.T, string, Patch)
	}{
		{
			name:    "tracked committed and uncommitted changes",
			baseRef: "main",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				writeGitstatFile(t, repo, "tracked.txt", []byte("one\ntwo\n"))
				gitTest(t, repo, "add", "tracked.txt")
				gitTest(t, repo, "commit", "-m", "committed work")
				writeGitstatFile(t, repo, "tracked.txt", []byte("one\ntwo\nthree\n"))
			},
			check: func(t *testing.T, repo string, got Patch) {
				t.Helper()
				wantMergeBase := gitTestOutput(t, repo, "rev-parse", "main")
				if got.MergeBase != wantMergeBase {
					t.Fatalf("WorktreePatch().MergeBase = %q, want %q", got.MergeBase, wantMergeBase)
				}
				assertFileStat(t, got.Files, FileStat{
					Path:          "tracked.txt",
					Additions:     2,
					PatchIncluded: true,
				})
				if !strings.Contains(got.Patch, "diff --git a/tracked.txt b/tracked.txt") ||
					!strings.Contains(got.Patch, "+two") ||
					!strings.Contains(got.Patch, "+three") {
					t.Fatalf("WorktreePatch().Patch = %q, want tracked committed and uncommitted lines", got.Patch)
				}
			},
		},
		{
			name:            "base resolution failure",
			baseRef:         "no-such-branch",
			wantErrContains: "no-such-branch",
		},
		{
			name:            "current branch is not a base",
			baseRef:         "feature",
			wantErrContains: "current branch",
		},
		{
			name:    "symbolic alias to current branch is not a base",
			baseRef: "refs/tags/base-alias",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				gitTest(t, repo, "symbolic-ref", "refs/tags/base-alias", "refs/heads/feature")
			},
			wantErrContains: "current branch",
		},
		{
			name:    "untracked text file",
			baseRef: "main",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				writeGitstatFile(t, repo, "untracked space.txt", []byte("first\nsecond\n"))
			},
			check: func(t *testing.T, _ string, got Patch) {
				t.Helper()
				assertFileStat(t, got.Files, FileStat{
					Path:          "untracked space.txt",
					Additions:     2,
					PatchIncluded: true,
				})
				if !strings.Contains(got.Patch, "diff --git a/untracked space.txt b/untracked space.txt") ||
					!strings.Contains(got.Patch, "+first") {
					t.Fatalf("WorktreePatch().Patch = %q, want untracked file patch", got.Patch)
				}
			},
		},
		{
			name:    "binary file is listed without patch",
			baseRef: "main",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				writeGitstatFile(t, repo, "binary.dat", []byte{'a', 0, 'b'})
			},
			check: func(t *testing.T, _ string, got Patch) {
				t.Helper()
				assertFileStat(t, got.Files, FileStat{
					Path:          "binary.dat",
					Binary:        true,
					OmittedReason: "binary",
				})
				if got.Patch != "" {
					t.Fatalf("WorktreePatch().Patch = %q, want binary file omitted", got.Patch)
				}
			},
		},
		{
			name:    "empty diff",
			baseRef: "main",
			check: func(t *testing.T, _ string, got Patch) {
				t.Helper()
				if got.Files == nil || len(got.Files) != 0 {
					t.Fatalf("WorktreePatch().Files = %#v, want non-nil empty slice", got.Files)
				}
				if got.Patch != "" {
					t.Fatalf("WorktreePatch().Patch = %q, want empty", got.Patch)
				}
			},
		},
		{
			name:    "file over limit is listed without patch",
			baseRef: "main",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				writeGitstatFile(t, repo, "large.txt", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
			},
			check: func(t *testing.T, _ string, got Patch) {
				t.Helper()
				assertFileStat(t, got.Files, FileStat{
					Path:          "large.txt",
					OmittedReason: "tooLarge",
				})
				if got.Patch != "" {
					t.Fatalf("WorktreePatch().Patch has %d bytes, want oversized file omitted", len(got.Patch))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initPatchRepo(t)
			if tt.prepare != nil {
				tt.prepare(t, repo)
			}

			got, err := Runner{}.WorktreePatch(repo, tt.baseRef)
			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("WorktreePatch() = %#v, want error containing %q", got, tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("WorktreePatch() error = %q, want it to contain %q", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, repo, got)
		})
	}
}

func TestRunnerWorktreePatchResolvesRelativePathFromRunnerCwd(t *testing.T) {
	repo := initPatchRepo(t)
	writeGitstatFile(t, repo, "large.txt", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
	gitTest(t, repo, "add", "large.txt")

	got, err := (Runner{Cwd: filepath.Dir(repo)}).WorktreePatch(filepath.Base(repo), "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "large.txt",
		OmittedReason: "tooLarge",
	})
	if got.Patch != "" {
		t.Fatalf("WorktreePatch().Patch has %d bytes, want oversized relative-path file omitted", len(got.Patch))
	}
}

func TestRunnerWorktreePatchUsesExactTrackedPathspec(t *testing.T) {
	repo := initPatchRepo(t)
	if err := os.Remove(filepath.Join(repo, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "tracked.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitstatFile(
		t,
		repo,
		"tracked.txt/large.txt",
		bytes.Repeat([]byte("x\n"), patchFileLimit/2+1),
	)
	gitTest(t, repo, "add", "-A")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("WorktreePatch().Files = %#v, want parent deletion and descendant addition", got.Files)
	}
	parent := findFileStat(t, got.Files, "tracked.txt")
	if !parent.PatchIncluded {
		t.Fatalf("parent FileStat = %#v, want patch included", parent)
	}
	child := findFileStat(t, got.Files, "tracked.txt/large.txt")
	if child.PatchIncluded || child.OmittedReason != "tooLarge" {
		t.Fatalf("child FileStat = %#v, want oversized patch omitted", child)
	}
	if blocks := strings.Count(got.Patch, "diff --git "); blocks != 1 {
		t.Fatalf("WorktreePatch().Patch has %d file blocks, want only the parent deletion", blocks)
	}
	if strings.Contains(got.Patch, "tracked.txt/large.txt") {
		t.Fatalf("WorktreePatch().Patch includes oversized descendant:\n%s", got.Patch)
	}
}

func TestRunnerWorktreePatchHandlesDirectoryReplacedByFile(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	if err := os.Mkdir(filepath.Join(repo, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitstatFile(t, repo, "dir/a", []byte("old\n"))
	gitTest(t, repo, "add", "dir/a")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	if err := os.RemoveAll(filepath.Join(repo, "dir")); err != nil {
		t.Fatal(err)
	}
	writeGitstatFile(t, repo, "dir", []byte("new\n"))
	gitTest(t, repo, "add", "-A")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("WorktreePatch().Files = %#v, want file addition and descendant deletion", got.Files)
	}
	if added := findFileStat(t, got.Files, "dir"); !added.PatchIncluded {
		t.Fatalf("added FileStat = %#v, want patch included", added)
	}
	if deleted := findFileStat(t, got.Files, "dir/a"); !deleted.PatchIncluded {
		t.Fatalf("deleted FileStat = %#v, want patch included", deleted)
	}
	if blocks := strings.Count(got.Patch, "diff --git "); blocks != 2 {
		t.Fatalf("WorktreePatch().Patch has %d file blocks, want addition and deletion", blocks)
	}
}

func TestRunnerWorktreePatchDoesNotFollowParentSymlink(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	if err := os.Mkdir(filepath.Join(repo, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitstatFile(t, repo, "dir/a", []byte("old\n"))
	gitTest(t, repo, "add", "dir/a")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")

	external := t.TempDir()
	writeGitstatFile(t, external, "a", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
	if err := os.RemoveAll(filepath.Join(repo, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(repo, "dir")); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "-A")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	deleted := findFileStat(t, got.Files, "dir/a")
	if !deleted.PatchIncluded || deleted.OmittedReason != "" {
		t.Fatalf("deleted FileStat = %#v, want small base-side deletion included", deleted)
	}
	if !strings.Contains(got.Patch, "-old") {
		t.Fatalf("WorktreePatch().Patch = %q, want base-side deletion", got.Patch)
	}
}

func TestRunnerWorktreePatchIgnoresUntrackedReplacementForTrackedSize(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, ".gitignore", []byte("ignored.txt\n"))
	writeGitstatFile(t, repo, "ignored.txt", []byte("old\n"))
	gitTest(t, repo, "add", ".gitignore")
	gitTest(t, repo, "add", "-f", "ignored.txt")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	gitTest(t, repo, "rm", "--cached", "ignored.txt")
	writeGitstatFile(t, repo, "ignored.txt", bytes.Repeat([]byte{'x'}, patchFileLimit+1))

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "ignored.txt",
		Deletions:     1,
		PatchIncluded: true,
	})
	if !strings.Contains(got.Patch, "-old") {
		t.Fatalf("WorktreePatch().Patch = %q, want tracked base-side deletion", got.Patch)
	}
}

func TestRunnerWorktreePatchOnlyCallsReadOnlyGitSubcommands(t *testing.T) {
	repo := initPatchRepo(t)
	writeGitstatFile(t, repo, "tracked.txt", []byte("one\nstaged\n"))
	gitTest(t, repo, "add", "tracked.txt")
	writeGitstatFile(t, repo, "untracked.txt", []byte("new\n"))

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	beforeIndex := gitBinaryOutput(t, realGit, repo, "diff", "--cached", "--binary")
	beforeStatus := gitBinaryOutput(t, realGit, repo, "status", "--porcelain=v1", "-z")
	beforeTracked, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	beforeUntracked, err := os.ReadFile(filepath.Join(repo, "untracked.txt"))
	if err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "git-subcommands.log")
	t.Setenv("FANOUT_GITSTAT_REAL_GIT", realGit)
	t.Setenv("FANOUT_GITSTAT_LOG", logPath)
	installGitstatShim(t, "git", `
case "$1" in
  -C) subcommand=$3 ;;
  *) subcommand=$1 ;;
esac
printf '%s\n' "$subcommand" >> "$FANOUT_GITSTAT_LOG"
exec "$FANOUT_GITSTAT_REAL_GIT" "$@"
`)

	if _, patchErr := (Runner{}).WorktreePatch(repo, "main"); patchErr != nil {
		t.Fatal(patchErr)
	}

	afterIndex := gitBinaryOutput(t, realGit, repo, "diff", "--cached", "--binary")
	afterStatus := gitBinaryOutput(t, realGit, repo, "status", "--porcelain=v1", "-z")
	afterTracked, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	afterUntracked, err := os.ReadFile(filepath.Join(repo, "untracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterIndex, beforeIndex) ||
		!bytes.Equal(afterStatus, beforeStatus) ||
		!bytes.Equal(afterTracked, beforeTracked) ||
		!bytes.Equal(afterUntracked, beforeUntracked) {
		t.Fatal("WorktreePatch changed the index or worktree")
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{
		"check-ref-format": true,
		"rev-parse":        true,
		"merge-base":       true,
		"diff":             true,
		"ls-files":         true,
		"ls-tree":          true,
		"symbolic-ref":     true,
	}
	for subcommand := range strings.FieldsSeq(string(logged)) {
		if !readOnly[subcommand] {
			t.Fatalf("WorktreePatch called non-read-only git subcommand %q; log:\n%s", subcommand, logged)
		}
	}
}

func initPatchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "tracked.txt", []byte("one\n"))
	gitTest(t, repo, "add", "tracked.txt")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	return repo
}

func writeGitstatFile(t *testing.T, repo, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFileStat(t *testing.T, got []FileStat, want FileStat) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("WorktreePatch().Files = %#v, want one file", got)
	}
	if got[0] != want {
		t.Fatalf("WorktreePatch().Files[0] = %#v, want %#v", got[0], want)
	}
}

func findFileStat(t *testing.T, stats []FileStat, path string) FileStat {
	t.Helper()
	for _, stat := range stats {
		if stat.Path == path {
			return stat
		}
	}
	t.Fatalf("WorktreePatch().Files = %#v, want path %q", stats, path)
	return FileStat{}
}

func TestRunnerMergeBase(t *testing.T) {
	for _, tc := range []struct {
		name            string
		baseRef         string
		prepare         func(*testing.T, string)
		wantRef         string
		wantErrContains string
	}{
		{
			name:    "explicit base",
			baseRef: "main",
			wantRef: "refs/heads/main",
		},
		{
			name:    "short base ignores same name tag",
			baseRef: "main",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				gitTest(t, repo, "tag", "main", "HEAD")
			},
			wantRef: "refs/heads/main",
		},
		{
			name:    "short base falls back to origin branch",
			baseRef: "main",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				gitTest(t, repo, "update-ref", "refs/remotes/origin/main", "refs/heads/main")
				gitTest(t, repo, "branch", "-D", "main")
			},
			wantRef: "refs/remotes/origin/main",
		},
		{
			name:            "missing base",
			baseRef:         "no-such-branch",
			wantErrContains: "no-such-branch",
		},
		{
			name:            "fork point option is not a base",
			baseRef:         "--fork-point",
			wantErrContains: "--fork-point",
		},
		{
			name:            "octopus option is not a base",
			baseRef:         "--octopus",
			wantErrContains: "--octopus",
		},
		{
			name:    "detached HEAD ref fails closed",
			baseRef: "HEAD",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				gitTest(t, repo, "checkout", "--detach", "HEAD")
			},
			wantErrContains: "HEAD",
		},
		{
			name:            "missing origin HEAD",
			wantErrContains: "origin/HEAD",
		},
		{
			name: "detached HEAD uses origin HEAD",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				gitTest(t, repo, "update-ref", "refs/remotes/origin/main", "main")
				gitTest(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
				gitTest(t, repo, "checkout", "--detach", "HEAD")
			},
			wantRef: "main",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initBaseRepo(t)
			if tc.prepare != nil {
				tc.prepare(t, repo)
			}

			got, err := Runner{}.MergeBase(repo, tc.baseRef)
			if tc.wantErrContains != "" {
				if err == nil {
					t.Fatalf("MergeBase() = %q, want error containing %q", got, tc.wantErrContains)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("MergeBase() error = %q, want it to contain %q", err, tc.wantErrContains)
				}
				if got != "" {
					t.Fatalf("MergeBase() = %q on error, want empty SHA", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			want := gitTestOutput(t, repo, "rev-parse", tc.wantRef)
			if got != want {
				t.Fatalf("MergeBase() = %q, want %q", got, want)
			}
		})
	}
}

func TestGitEnvDisablesOptionalLocks(t *testing.T) {
	env := map[string]string{}
	for _, item := range gitEnv() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	if env["LC_ALL"] != "C" {
		t.Fatalf("LC_ALL = %q, want C", env["LC_ALL"])
	}
	if env["GIT_OPTIONAL_LOCKS"] != "0" {
		t.Fatalf("GIT_OPTIONAL_LOCKS = %q, want 0", env["GIT_OPTIONAL_LOCKS"])
	}
	if env["GIT_LITERAL_PATHSPECS"] != "1" {
		t.Fatalf("GIT_LITERAL_PATHSPECS = %q, want 1", env["GIT_LITERAL_PATHSPECS"])
	}
}

func gitTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitBinaryOutput(t *testing.T, binary, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", binary, args, err)
	}
	return out
}

func installGitstatShim(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
