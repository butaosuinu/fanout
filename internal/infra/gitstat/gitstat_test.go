package gitstat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseNumStat(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    []FileStat
		wantErr bool
	}{
		{name: "empty output", out: "", want: []FileStat{}},
		{
			name: "plain record",
			out:  "1\t2\tfile.txt\x00",
			want: []FileStat{{Path: "file.txt", Additions: 1, Deletions: 2}},
		},
		{
			// A rename leaves the path column empty and spends two more records.
			name: "rename record keeps both paths",
			out:  "3\t4\t\x00old.txt\x00new.txt\x00",
			want: []FileStat{{Path: "new.txt", OldPath: "old.txt", Additions: 3, Deletions: 4}},
		},
		{
			name: "binary record",
			out:  "-\t-\tblob.bin\x00",
			want: []FileStat{{Path: "blob.bin", Binary: true, OmittedReason: "binary"}},
		},
		{
			name: "binary rename",
			out:  "-\t-\t\x00a.bin\x00b.bin\x00",
			want: []FileStat{{Path: "b.bin", OldPath: "a.bin", Binary: true, OmittedReason: "binary"}},
		},
		{
			name: "rename does not swallow the record after it",
			out:  "0\t0\t\x00old.txt\x00new.txt\x005\t6\tlater.txt\x00",
			want: []FileStat{
				{Path: "new.txt", OldPath: "old.txt"},
				{Path: "later.txt", Additions: 5, Deletions: 6},
			},
		},
		{name: "record with too few columns is an error", out: "1\tfile.txt\x00", wantErr: true},
		{name: "non-numeric count is an error", out: "x\t2\tfile.txt\x00", wantErr: true},
		{name: "half-marked binary pair is an error", out: "-\t2\tfile.txt\x00", wantErr: true},
		{name: "truncated rename record is an error", out: "1\t2\t\x00old.txt\x00", wantErr: true},
		{name: "empty rename path is an error", out: "1\t2\t\x00\x00new.txt\x00", wantErr: true},
		{name: "missing NUL terminator is an error", out: "1\t2\tfile.txt", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNumStat([]byte(tt.out))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseNumStat(%q) = %+v, want error", tt.out, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNumStat(%q) = %v, want no error", tt.out, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseNumStat(%q) = %+v, want %+v", tt.out, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("parseNumStat(%q)[%d] = %+v, want %+v", tt.out, i, got[i], tt.want[i])
				}
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
	// +2 from tracked.txt, +1 from untracked.txt: the summary counts the same
	// files the diff viewer lists.
	if got.Additions != 3 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +3/-0 dirty", got)
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
	if got.Additions != 1 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +1/-0 dirty", got)
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

func TestRunnerWorktreePatchHonorsSharedContextDeadline(t *testing.T) {
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (Runner{Context: ctx}).WorktreePatch(t.TempDir(), "main")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WorktreePatch() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("WorktreePatch() returned after %s, want shared deadline", elapsed)
	}
}

func TestRunnerWorktreePatchHonorsRequestLimits(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeGitstatFile(t, repo, name, []byte("old\n"))
	}
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeGitstatFile(t, repo, name, []byte("new content\n"))
	}

	unlimited, err := (Runner{}).WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	next := strings.Index(unlimited.Patch, "\ndiff --git ")
	if next < 0 {
		t.Fatalf("unlimited patch has no second file group: %q", unlimited.Patch)
	}
	firstGroupBytes := next + 1
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "git-args.log")
	t.Setenv("FANOUT_GITSTAT_REAL_GIT", realGit)
	t.Setenv("FANOUT_GITSTAT_LOG", logPath)
	installGitstatShim(t, "git", `
printf '%s\n' "$*" >> "$FANOUT_GITSTAT_LOG"
exec "$FANOUT_GITSTAT_REAL_GIT" "$@"
`)
	limited, err := (Runner{MaxFiles: 3, MaxPatchBytes: firstGroupBytes}).WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if limited.Patch != unlimited.Patch[:firstGroupBytes] {
		t.Fatalf("limited patch = %q, want first complete file group", limited.Patch)
	}
	if len(limited.Files) != 3 || !limited.Files[0].PatchIncluded {
		t.Fatalf("limited files = %+v, want all metadata and first patch", limited.Files)
	}
	for i, file := range limited.Files[1:] {
		if file.PatchIncluded || file.OmittedReason != "collectionLimit" {
			t.Fatalf("limited.Files[%d] = %+v, want collectionLimit", i+1, file)
		}
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(logged), "\n") {
		if strings.Contains(line, " diff ") && strings.Contains(line, "c.txt") {
			t.Fatalf("patch commands continued after collection limit:\n%s", logged)
		}
	}

	_, err = (Runner{MaxFiles: 2}).WorktreePatch(repo, "main")
	if err == nil || !strings.Contains(err.Error(), "contains 3 files; limit is 2") {
		t.Fatalf("MaxFiles error = %v", err)
	}
}

func TestRunnerWorktreePatchCountsOnlyFinalChangesForFileLimit(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "a.txt", []byte("old\n"))
	writeGitstatFile(t, repo, "replace.txt", []byte("same\n"))
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	writeGitstatFile(t, repo, "a.txt", []byte("new\n"))
	gitTest(t, repo, "rm", "--cached", "replace.txt")

	got, err := (Runner{MaxFiles: 1}).WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "a.txt" || !got.Files[0].PatchIncluded {
		t.Fatalf("WorktreePatch() files = %+v, want only the final changed file", got.Files)
	}
}

func TestRunnerWorktreePatchClassifiesFilesAfterCollectionLimit(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "a.txt", []byte("old\n"))
	writeGitstatFile(t, repo, "b.txt", []byte("old\n"))
	writeGitstatFile(t, repo, "m-replace.txt", []byte("same\n"))
	writeGitstatFile(t, repo, "n-large-same.dat", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
	writeGitstatFile(t, repo, "z-binary.dat", []byte{'o', 0, 'l', 'd'})
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	writeGitstatFile(t, repo, "a.txt", []byte("new\n"))
	writeGitstatFile(t, repo, "b.txt", []byte("new\n"))
	gitTest(t, repo, "rm", "--cached", "m-replace.txt")
	gitTest(t, repo, "rm", "--cached", "n-large-same.dat")
	writeGitstatFile(t, repo, "z-binary.dat", bytes.Repeat([]byte{0}, patchFileLimit+1))

	unlimited, err := (Runner{}).WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	next := strings.Index(unlimited.Patch, "\ndiff --git ")
	if next < 0 {
		t.Fatalf("unlimited patch has no second file group: %q", unlimited.Patch)
	}
	for _, stat := range unlimited.Files {
		if stat.Path == "n-large-same.dat" {
			t.Fatalf("unlimited files = %+v, want identical oversized replacement omitted", unlimited.Files)
		}
	}
	firstGroupBytes := next + 1
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "git-args.log")
	t.Setenv("FANOUT_GITSTAT_REAL_GIT", realGit)
	t.Setenv("FANOUT_GITSTAT_LOG", logPath)
	installGitstatShim(t, "git", `
printf '%s\n' "$*" >> "$FANOUT_GITSTAT_LOG"
exec "$FANOUT_GITSTAT_REAL_GIT" "$@"
`)

	limited, err := (Runner{MaxFiles: 3, MaxPatchBytes: firstGroupBytes}).WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Files) != 3 {
		t.Fatalf("limited files = %+v, want a.txt, b.txt, and z-binary.dat", limited.Files)
	}
	if limited.Files[0].Path != "a.txt" || !limited.Files[0].PatchIncluded ||
		limited.Files[1].Path != "b.txt" || limited.Files[1].OmittedReason != "collectionLimit" ||
		limited.Files[2].Path != "z-binary.dat" || limited.Files[2].OmittedReason != "tooLarge" {
		t.Fatalf("limited files = %+v, want included/collectionLimit/tooLarge", limited.Files)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(logged), "\n") {
		if strings.Contains(line, "--src-prefix=") && strings.Contains(line, "m-replace.txt") {
			t.Fatalf("replacement patch command ran after collection limit:\n%s", logged)
		}
	}
	if !strings.Contains(string(logged), "hash-object --no-filters -- n-large-same.dat") {
		t.Fatalf("oversized replacement was not compared by object ID:\n%s", logged)
	}
}

func TestRunnerWorktreePatchClassifiesGitlinkReplacementAfterCollectionLimit(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "a.txt", []byte("old\n"))
	writeGitstatFile(t, repo, "b.txt", []byte("old\n"))
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "seed")
	oldCommit := gitTestOutput(t, repo, "rev-parse", "HEAD")
	gitTest(t, repo, "update-index", "--add", "--cacheinfo", "160000", oldCommit, "m-link")
	gitTest(t, repo, "commit", "-m", "add gitlink")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	writeGitstatFile(t, repo, "a.txt", []byte("new\n"))
	writeGitstatFile(t, repo, "b.txt", []byte("new\n"))
	gitTest(t, repo, "rm", "--cached", "m-link")
	writeGitstatFile(t, repo, "m-link", []byte("regular file\n"))

	unlimited, err := (Runner{}).WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	next := strings.Index(unlimited.Patch, "\ndiff --git ")
	if next < 0 {
		t.Fatalf("unlimited patch has no second file group: %q", unlimited.Patch)
	}
	limited, err := (Runner{MaxPatchBytes: next + 1}).WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	got := findFileStat(t, limited.Files, "m-link")
	want := FileStat{
		Path:          "m-link",
		OmittedReason: "collectionLimit",
	}
	if got != want {
		t.Fatalf("gitlink replacement FileStat = %#v, want %#v", got, want)
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
			name:    "oversized tracked binary current side is tooLarge",
			baseRef: "main",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				content := bytes.Repeat([]byte{'x'}, patchFileLimit+1)
				content[0] = 0
				writeGitstatFile(t, repo, "tracked.txt", content)
			},
			check: func(t *testing.T, _ string, got Patch) {
				t.Helper()
				assertFileStat(t, got.Files, FileStat{
					Path:          "tracked.txt",
					OmittedReason: "tooLarge",
				})
				if got.Patch != "" {
					t.Fatalf("WorktreePatch().Patch has %d bytes, want oversized binary omitted", len(got.Patch))
				}
			},
		},
		{
			name:    "oversized tracked binary base side is tooLarge",
			baseRef: "main",
			prepare: func(t *testing.T, repo string) {
				t.Helper()
				gitTest(t, repo, "checkout", "main")
				content := bytes.Repeat([]byte{'x'}, patchFileLimit+1)
				content[0] = 0
				writeGitstatFile(t, repo, "tracked.txt", content)
				gitTest(t, repo, "add", "tracked.txt")
				gitTest(t, repo, "commit", "-m", "large binary base")
				gitTest(t, repo, "checkout", "feature")
				gitTest(t, repo, "reset", "--hard", "main")
				writeGitstatFile(t, repo, "tracked.txt", []byte{'a', 0, 'b'})
			},
			check: func(t *testing.T, _ string, got Patch) {
				t.Helper()
				assertFileStat(t, got.Files, FileStat{
					Path:          "tracked.txt",
					OmittedReason: "tooLarge",
				})
				if got.Patch != "" {
					t.Fatalf("WorktreePatch().Patch has %d bytes, want oversized binary omitted", len(got.Patch))
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
				// Untracked and oversized: the size check short-circuits before
				// numstat runs, so there are no counts to report. Tracked files
				// keep theirs — see TestRunnerWorktreePatchKeepsOversizedCounts.
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
		Additions:     1,
		OmittedReason: "tooLarge",
	})
	if got.Patch != "" {
		t.Fatalf("WorktreePatch().Patch has %d bytes, want oversized relative-path file omitted", len(got.Patch))
	}
}

func TestRunnerWorktreePatchPreservesPathWhitespace(t *testing.T) {
	for _, name := range []string{" repo", "repo "} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, name)
			if err := os.Mkdir(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			initPatchRepoAt(t, repo)
			writeGitstatFile(t, repo, "tracked.txt", []byte("one\ntwo\n"))

			got, err := (Runner{Cwd: root}).WorktreePatch(name, "main")
			if err != nil {
				t.Fatal(err)
			}
			assertFileStat(t, got.Files, FileStat{
				Path:          "tracked.txt",
				Additions:     1,
				PatchIncluded: true,
			})
		})
	}
}

func TestRunnerWorktreePatchChecksSkippedIndexBlobSize(t *testing.T) {
	repo := initPatchRepo(t)
	writeGitstatFile(t, repo, "tracked.txt", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
	gitTest(t, repo, "add", "tracked.txt")
	gitTest(t, repo, "commit", "-m", "oversized tracked file")
	gitTest(t, repo, "update-index", "--skip-worktree", "tracked.txt")
	if err := os.Remove(filepath.Join(repo, "tracked.txt")); err != nil {
		t.Fatal(err)
	}

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "tracked.txt",
		Additions:     1,
		Deletions:     1,
		OmittedReason: "tooLarge",
	})
	if got.Patch != "" {
		t.Fatalf("WorktreePatch().Patch has %d bytes, want skipped oversized index blob omitted", len(got.Patch))
	}
}

func TestRunnerWorktreePatchChecksAssumeUnchangedIndexBlobSize(t *testing.T) {
	repo := initPatchRepo(t)
	writeGitstatFile(t, repo, "tracked.txt", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
	gitTest(t, repo, "add", "tracked.txt")
	gitTest(t, repo, "commit", "-m", "oversized tracked file")
	gitTest(t, repo, "update-index", "--assume-unchanged", "tracked.txt")
	if err := os.Remove(filepath.Join(repo, "tracked.txt")); err != nil {
		t.Fatal(err)
	}

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "tracked.txt",
		Additions:     1,
		Deletions:     1,
		OmittedReason: "tooLarge",
	})
	if got.Patch != "" {
		t.Fatalf("WorktreePatch().Patch has %d bytes, want assumed-unchanged oversized index blob omitted", len(got.Patch))
	}
}

func TestRunnerWorktreePatchUsesAssumedIndexBlobWhenWorktreeExists(t *testing.T) {
	for _, tt := range []struct {
		name            string
		indexContent    []byte
		worktreeContent []byte
		want            FileStat
	}{
		{
			name:            "oversized index",
			indexContent:    bytes.Repeat([]byte{'x'}, patchFileLimit+1),
			worktreeContent: []byte("small\n"),
			want: FileStat{
				Path:          "tracked.txt",
				Additions:     1,
				Deletions:     1,
				OmittedReason: "tooLarge",
			},
		},
		{
			name:            "oversized worktree",
			indexContent:    []byte("one\ntwo\n"),
			worktreeContent: bytes.Repeat([]byte{'x'}, patchFileLimit+1),
			want: FileStat{
				Path:          "tracked.txt",
				Additions:     1,
				PatchIncluded: true,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := initPatchRepo(t)
			writeGitstatFile(t, repo, "tracked.txt", tt.indexContent)
			gitTest(t, repo, "add", "tracked.txt")
			gitTest(t, repo, "commit", "-m", "tracked change")
			gitTest(t, repo, "update-index", "--assume-unchanged", "tracked.txt")
			writeGitstatFile(t, repo, "tracked.txt", tt.worktreeContent)

			got, err := Runner{}.WorktreePatch(repo, "main")
			if err != nil {
				t.Fatal(err)
			}
			assertFileStat(t, got.Files, tt.want)
			if tt.want.PatchIncluded && !strings.Contains(got.Patch, "+two") {
				t.Fatalf("WorktreePatch().Patch = %q, want index-side content", got.Patch)
			}
			if !tt.want.PatchIncluded && got.Patch != "" {
				t.Fatalf("WorktreePatch().Patch has %d bytes, want oversized index blob omitted", len(got.Patch))
			}
		})
	}
}

func TestReplacementTempDirStaysOutsideWorktree(t *testing.T) {
	worktree := t.TempDir()
	nestedTemp := filepath.Join(worktree, "tmp")
	if err := os.Mkdir(nestedTemp, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", nestedTemp)

	tempDir, err := replacementTempDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Errorf("remove replacement temp directory: %v", removeErr)
		}
	}()

	worktreeRoot, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	tempRoot, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	inside, err := pathWithin(worktreeRoot, tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if inside {
		t.Fatalf("replacementTempDir() = %q, want path outside worktree %q", tempRoot, worktreeRoot)
	}
}

func TestRunnerWorktreePatchOverridesIgnoreSubmodules(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "seed.txt", []byte("seed\n"))
	gitTest(t, repo, "add", "seed.txt")
	gitTest(t, repo, "commit", "-m", "seed")
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, sub, "init")
	gitTest(t, sub, "config", "user.email", "test@example.com")
	gitTest(t, sub, "config", "user.name", "Test User")
	writeGitstatFile(t, sub, "file.txt", []byte("old\n"))
	gitTest(t, sub, "add", "file.txt")
	gitTest(t, sub, "commit", "-m", "old submodule commit")
	oldCommit := gitTestOutput(t, sub, "rev-parse", "HEAD")
	gitTest(t, repo, "update-index", "--add", "--cacheinfo", "160000", oldCommit, "sub")
	gitTest(t, repo, "commit", "-m", "add gitlink")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	gitTest(t, repo, "rm", "--cached", "sub")
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitstatFile(t, repo, "sub/large.txt", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
	gitTest(t, repo, "add", "sub/large.txt")
	gitTest(t, repo, "config", "diff.ignoreSubmodules", "all")
	gitTest(t, repo, "config", "diff.submodule", "log")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("WorktreePatch().Files = %#v, want gitlink deletion and descendant addition", got.Files)
	}
	parent := findFileStat(t, got.Files, "sub")
	if parent.Additions != 0 || parent.Deletions != 1 || !parent.PatchIncluded {
		t.Fatalf("gitlink FileStat = %#v, want included deletion", parent)
	}
	child := findFileStat(t, got.Files, "sub/large.txt")
	if child.PatchIncluded || child.OmittedReason != "tooLarge" {
		t.Fatalf("descendant FileStat = %#v, want oversized patch omitted", child)
	}
	if blocks := strings.Count(got.Patch, "diff --git "); blocks != 1 {
		t.Fatalf("WorktreePatch().Patch has %d blocks, want only gitlink deletion", blocks)
	}
	if !strings.Contains(got.Patch, "-Subproject commit "+oldCommit) ||
		strings.Contains(got.Patch, "sub/large.txt") ||
		strings.Contains(got.Patch, "Submodule sub ") {
		t.Fatalf("WorktreePatch().Patch = %q, want short exact gitlink deletion", got.Patch)
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

func TestRunnerWorktreePatchMergesUntrackedReplacement(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    *FileStat
	}{
		{
			name:    "changed",
			content: []byte("new\n"),
			want: &FileStat{
				Path:          "replace.txt",
				Additions:     1,
				Deletions:     1,
				PatchIncluded: true,
			},
		},
		{
			name:    "unchanged",
			content: []byte("old\n"),
		},
		{
			name:    "oversized",
			content: bytes.Repeat([]byte{'x'}, patchFileLimit+1),
			want: &FileStat{
				Path:          "replace.txt",
				OmittedReason: "tooLarge",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			gitTest(t, repo, "init")
			gitTest(t, repo, "config", "user.email", "test@example.com")
			gitTest(t, repo, "config", "user.name", "Test User")
			writeGitstatFile(t, repo, "replace.txt", []byte("old\n"))
			gitTest(t, repo, "add", "replace.txt")
			gitTest(t, repo, "commit", "-m", "initial")
			gitTest(t, repo, "branch", "-M", "main")
			gitTest(t, repo, "checkout", "-b", "feature")
			gitTest(t, repo, "rm", "--cached", "replace.txt")
			writeGitstatFile(t, repo, "replace.txt", tt.content)

			got, err := Runner{}.WorktreePatch(repo, "main")
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == nil {
				if len(got.Files) != 0 || got.Patch != "" {
					t.Fatalf("WorktreePatch() = %#v, want final-side identical path omitted", got)
				}
				return
			}
			assertFileStat(t, got.Files, *tt.want)
			if tt.want.PatchIncluded {
				if blocks := strings.Count(got.Patch, "diff --git "); blocks != 1 {
					t.Fatalf("WorktreePatch().Patch has %d blocks, want one replacement block", blocks)
				}
				if !strings.Contains(got.Patch, "-old") || !strings.Contains(got.Patch, "+new") {
					t.Fatalf("WorktreePatch().Patch = %q, want old-to-new replacement", got.Patch)
				}
				if strings.Contains(got.Patch, "fanout-gitstat-") ||
					!strings.Contains(got.Patch, "diff --git a/replace.txt b/replace.txt") ||
					!strings.Contains(got.Patch, "--- a/replace.txt") ||
					!strings.Contains(got.Patch, "+++ b/replace.txt") {
					t.Fatalf("WorktreePatch().Patch has non-canonical replacement headers:\n%s", got.Patch)
				}
			} else if got.Patch != "" {
				t.Fatalf("WorktreePatch().Patch has %d bytes, want oversized replacement omitted", len(got.Patch))
			}
		})
	}
}

func TestRunnerWorktreePatchOmitsUnchangedBinaryReplacement(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "replace.dat", []byte{'o', 'l', 'd', 0, '\n'})
	gitTest(t, repo, "add", "replace.dat")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	gitTest(t, repo, "rm", "--cached", "replace.dat")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 0 || got.Patch != "" {
		t.Fatalf("WorktreePatch() = %#v, want unchanged binary replacement omitted", got)
	}
}

func TestRunnerWorktreePatchOmitsBinaryReplacementAfterAttributeOverride(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, ".gitattributes", []byte("replace.dat text\n"))
	writeGitstatFile(t, repo, "replace.dat", []byte("old\n"))
	gitTest(t, repo, "add", ".gitattributes", "replace.dat")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	gitTest(t, repo, "rm", "--cached", "replace.dat")
	writeGitstatFile(t, repo, "replace.dat", []byte{'n', 'e', 'w', 0, '\n'})

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "replace.dat",
		Binary:        true,
		OmittedReason: "binary",
	})
	if got.Patch != "" {
		t.Fatalf("WorktreePatch().Patch = %q, want binary replacement omitted", got.Patch)
	}
}

func TestRunnerWorktreePatchMergesSymlinkReplacement(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	if err := os.Symlink("old-target", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "link")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	gitTest(t, repo, "rm", "--cached", "link")
	if err := os.Remove(filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("new-target", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "link",
		Additions:     1,
		Deletions:     1,
		PatchIncluded: true,
	})
	if blocks := strings.Count(got.Patch, "diff --git "); blocks != 1 {
		t.Fatalf("WorktreePatch().Patch has %d blocks, want one symlink replacement block", blocks)
	}
	if !strings.Contains(got.Patch, "-old-target") || !strings.Contains(got.Patch, "+new-target") {
		t.Fatalf("WorktreePatch().Patch = %q, want symlink target replacement", got.Patch)
	}
}

func TestRunnerWorktreePatchMergesFileTypeReplacement(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "entry", []byte("old\n"))
	gitTest(t, repo, "add", "entry")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	gitTest(t, repo, "rm", "--cached", "entry")
	if err := os.Remove(filepath.Join(repo, "entry")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("new-target", filepath.Join(repo, "entry")); err != nil {
		t.Fatal(err)
	}

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "entry",
		Additions:     1,
		Deletions:     1,
		PatchIncluded: true,
	})
	if blocks := strings.Count(got.Patch, "diff --git "); blocks != 2 {
		t.Fatalf("WorktreePatch().Patch has %d blocks, want delete/add file group", blocks)
	}
	if !strings.Contains(got.Patch, "deleted file mode 100644") ||
		!strings.Contains(got.Patch, "new file mode 120000") {
		t.Fatalf("WorktreePatch().Patch = %q, want regular-to-symlink replacement", got.Patch)
	}
}

func TestRunnerWorktreePatchReplacementPrefersTooLargeOverBinary(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "replace.dat", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
	gitTest(t, repo, "add", "replace.dat")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
	gitTest(t, repo, "rm", "--cached", "replace.dat")
	writeGitstatFile(t, repo, "replace.dat", []byte{'a', 0, 'b'})

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "replace.dat",
		OmittedReason: "tooLarge",
	})
	if got.Patch != "" {
		t.Fatalf("WorktreePatch().Patch = %q, want oversized replacement omitted", got.Patch)
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
	// The subcommand is the first word that is not a git-level option, so skip
	// every leading flag — and the value of the ones that take one — rather than
	// only -C. Iterating with `for` leaves "$@" intact for the exec below.
	installGitstatShim(t, "git", `
skip=0
subcommand=
for arg in "$@"; do
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  case "$arg" in
    -C|-c) skip=1 ;;
    -*) ;;
    *) subcommand=$arg; break ;;
  esac
done
# config is only ever a read here; record its mode so a write would stand out.
if [ "$subcommand" = "config" ]; then
  for arg in "$@"; do
    case "$arg" in
      --get|--get-all|--list) subcommand="config $arg"; break ;;
    esac
  done
fi
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
		// Only the reading form is allowed; a bare "config" would be a write.
		"config --get":     true,
		"check-ref-format": true,
		"rev-parse":        true,
		"merge-base":       true,
		"diff":             true,
		"hash-object":      true,
		"ls-files":         true,
		"ls-tree":          true,
		"symbolic-ref":     true,
	}
	for subcommand := range strings.SplitSeq(strings.TrimSpace(string(logged)), "\n") {
		if !readOnly[subcommand] {
			t.Fatalf("WorktreePatch called non-read-only git subcommand %q; log:\n%s", subcommand, logged)
		}
	}
}

// The session list and the diff viewer must never disagree about how many
// lines a worktree changed; both read the same collection to guarantee it.
func TestRunnerWorktreeAndWorktreePatchAgreeOnTotals(t *testing.T) {
	repo := initPatchRepo(t)
	seedOnMain(t, repo, map[string][]byte{
		"moved.txt":    []byte("alpha\nbeta\ngamma\ndelta\nepsilon\n"),
		"still.txt":    []byte("kept\nas\nis\nexactly\nhere\n"),
		"dropped.txt":  []byte("gone\n"),
		"nested/.keep": nil,
	})

	// A rename with an edit, a pure rename, a deletion, a staged add, an
	// unstaged edit, an untracked file, and a binary change in one worktree.
	gitTest(t, repo, "mv", "moved.txt", "renamed.txt")
	writeGitstatFile(t, repo, "renamed.txt", []byte("alpha\nBETA\ngamma\ndelta\nepsilon\n"))
	gitTest(t, repo, "mv", "still.txt", "nested/still.txt")
	gitTest(t, repo, "rm", "dropped.txt")
	writeGitstatFile(t, repo, "staged.txt", []byte("staged\n"))
	gitTest(t, repo, "add", "-A")
	writeGitstatFile(t, repo, "tracked.txt", []byte("one\ntwo\n"))
	writeGitstatFile(t, repo, "untracked.txt", []byte("loose\nlines\n"))
	writeGitstatFile(t, repo, "blob.bin", []byte{'a', 0, 'b'})

	summary, err := Runner{}.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	patch, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	var additions, deletions, renames int
	for _, file := range patch.Files {
		additions += file.Additions
		deletions += file.Deletions
		if file.OldPath != "" {
			renames++
		}
	}
	// Guard the fixture itself: renames are the shape that broke parity, so a
	// run where git detected none proves nothing.
	if renames != 2 {
		t.Fatalf("WorktreePatch().Files = %#v, want 2 renames in the fixture", patch.Files)
	}
	if summary.Additions != additions || summary.Deletions != deletions {
		t.Fatalf(
			"Worktree() = +%d/-%d, want the WorktreePatch() total +%d/-%d (files %#v)",
			summary.Additions, summary.Deletions, additions, deletions, patch.Files,
		)
	}
}

func TestRunnerWorktreeCountsRenameOnce(t *testing.T) {
	repo := initPatchRepo(t)
	seedOnMain(t, repo, map[string][]byte{"moved.txt": []byte("one\ntwo\nthree\nfour\nfive\nsix\n")})
	gitTest(t, repo, "mv", "moved.txt", "renamed.txt")
	writeGitstatFile(t, repo, "renamed.txt", []byte("one\nTWO\nthree\nfour\nfive\nsix\n"))
	gitTest(t, repo, "add", "-A")

	got, err := Runner{}.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	// The moved file contributes its one edited line, not six deletions plus
	// six additions.
	if got.Additions != 1 || got.Deletions != 1 {
		t.Fatalf("Worktree() = +%d/-%d, want +1/-1", got.Additions, got.Deletions)
	}
}

// Counting an untracked file costs a git process and Worktree runs on a
// 2-second tick, so the count is memoized until the file itself changes.
func TestRunnerWorktreeMemoizesUntrackedCountsUntilTheFileChanges(t *testing.T) {
	repo := initPatchRepo(t)
	writeGitstatFile(t, repo, "untracked.txt", []byte("one\n"))
	cache := NewUntrackedStatCache()
	runner := Runner{UntrackedCache: cache}

	first, err := runner.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if first.Additions != 1 {
		t.Fatalf("Worktree() = +%d, want +1", first.Additions)
	}
	if n := cache.size(repo); n != 1 {
		t.Fatalf("cache holds %d entries for the worktree, want 1", n)
	}

	again, err := runner.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if again.Additions != first.Additions || cache.size(repo) != 1 {
		t.Fatalf("Worktree() = +%d with %d entries, want the memoized +%d", again.Additions, cache.size(repo), first.Additions)
	}

	writeGitstatFile(t, repo, "untracked.txt", []byte("one\ntwo\nthree\n"))
	changed, err := runner.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Additions != 3 {
		t.Fatalf("Worktree() = +%d after the file grew, want +3", changed.Additions)
	}
}

// Content is not the only input to git's text/binary verdict: .gitattributes,
// core.bigFileThreshold and diff.<driver>.binary all reclassify the same bytes,
// and that list is not closed. The entry TTL is what bounds the divergence from
// the cacheless diff viewer, so it has to actually expire.
func TestRunnerWorktreeRecountsAfterTTLWhenGitReclassifiesTheSameBytes(t *testing.T) {
	tests := []struct {
		name       string
		reclassify func(t *testing.T, repo string)
	}{
		{
			name: ".gitattributes",
			reclassify: func(t *testing.T, repo string) {
				t.Helper()
				writeGitstatFile(t, repo, ".gitattributes", []byte("blob.dat binary\n"))
			},
		},
		{
			name: "diff.<driver>.binary",
			reclassify: func(t *testing.T, repo string) {
				t.Helper()
				gitTest(t, repo, "config", "diff.probe.binary", "true")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initPatchRepo(t)
			// The driver case needs the attribute in place from the start; the
			// attribute case adds its own file below.
			writeGitstatFile(t, repo, ".gitattributes", []byte("blob.dat diff=probe\n"))
			gitTest(t, repo, "add", ".gitattributes")
			gitTest(t, repo, "commit", "-m", "attach a diff driver")
			writeGitstatFile(t, repo, "blob.dat", []byte("one\ntwo\nthree\n"))

			clock := time.Now()
			cache := NewUntrackedStatCache()
			cache.now = func() time.Time { return clock }
			runner := Runner{UntrackedCache: cache}

			before, err := runner.Worktree(repo, "main")
			if err != nil {
				t.Fatal(err)
			}
			// blob.dat +3 と、feature 側で commit した .gitattributes +1。
			if before.Additions != 4 {
				t.Fatalf("Worktree() = +%d, want +4 with blob.dat counted as text", before.Additions)
			}

			tt.reclassify(t, repo)
			// Still inside the TTL: the memoized count stands.
			clock = clock.Add(untrackedEntryTTL / 2)
			stale, err := runner.Worktree(repo, "main")
			if err != nil {
				t.Fatal(err)
			}
			if stale.Additions != 4 {
				t.Fatalf("Worktree() = +%d inside the TTL, want the memoized +4", stale.Additions)
			}

			clock = clock.Add(untrackedEntryTTL)
			after, err := runner.Worktree(repo, "main")
			if err != nil {
				t.Fatal(err)
			}
			// blob.dat は binary になり、残るのは .gitattributes の 1 行だけ。
			if after.Additions != 1 {
				t.Fatalf("Worktree() = +%d after the TTL, want +1 from the new verdict", after.Additions)
			}
		})
	}
}

// size と mtime だけを鍵にすると `cp -p` の上書きを取り逃がし、一覧だけが
// 古い行数を返し続けて viewer と恒久的に食い違う。
func TestRunnerWorktreeRecountsWhenContentChangesUnderTheSameStat(t *testing.T) {
	repo := initPatchRepo(t)
	target := filepath.Join(repo, "untracked.txt")
	writeGitstatFile(t, repo, "untracked.txt", []byte("one\ntwo\nthree\n"))
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{UntrackedCache: NewUntrackedStatCache()}
	if _, err = runner.Worktree(repo, "main"); err != nil {
		t.Fatal(err)
	}

	// 同じ byte 数・同じ mtime で中身だけ差し替える(`cp -p` と同じ形)。
	writeGitstatFile(t, repo, "untracked.txt", []byte("a\nb\nc\nd\ne\nf\ng\n"))
	if err = os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("fixture did not preserve size/mtime: %d/%v vs %d/%v",
			after.Size(), after.ModTime(), info.Size(), info.ModTime())
	}

	got, err := runner.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 7 {
		t.Fatalf("Worktree() = +%d, want +7 recounted from the new content", got.Additions)
	}
}

// git re-reads the file after the cache key is hashed, so untrackedFileStat
// re-derives the key afterwards and refuses to memoize when it moved. This
// pins the comparison that guard depends on: the key follows current content,
// and a file that vanished yields an error rather than a matching key.
func TestUntrackedFileKeyNowFollowsCurrentContent(t *testing.T) {
	repo := t.TempDir()
	writeGitstatFile(t, repo, "u.txt", []byte("one\n"))
	before, err := untrackedFileKeyNow(repo, "u.txt")
	if err != nil {
		t.Fatal(err)
	}
	if same, sameErr := untrackedFileKeyNow(repo, "u.txt"); sameErr != nil || same != before {
		t.Fatalf("untrackedFileKeyNow() = %q, %v on an unchanged file, want %q", same, sameErr, before)
	}

	writeGitstatFile(t, repo, "u.txt", []byte("two\n"))
	after, err := untrackedFileKeyNow(repo, "u.txt")
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("untrackedFileKeyNow() did not move after the content changed")
	}

	if err := os.Remove(filepath.Join(repo, "u.txt")); err != nil {
		t.Fatal(err)
	}
	if gone, goneErr := untrackedFileKeyNow(repo, "u.txt"); goneErr == nil {
		t.Fatalf("untrackedFileKeyNow() = %q on a removed file, want an error", gone)
	}
}

// A quiet file stays cacheable: the guard must not disable memoization
// wholesale, or the 2-second tick pays the full per-file cost again.
func TestUntrackedFileStatReturnsACacheKeyForAQuietFile(t *testing.T) {
	repo := initPatchRepo(t)
	writeGitstatFile(t, repo, "untracked.txt", []byte("one\n"))

	entry, key, err := Runner{}.untrackedFileStat(repo, "untracked.txt")
	if err != nil {
		t.Fatal(err)
	}
	if key == "" {
		t.Fatalf("untrackedFileStat() = %+v with no key; an unchanged file must be cacheable", entry.stat)
	}
	if entry.stat.Additions != 1 {
		t.Fatalf("untrackedFileStat() = %+v, want +1", entry.stat)
	}
}

// A cleaned-up worktree is never swept again, so its entries would otherwise
// live until the process exits. Creating and cleaning up sessions is the normal
// fanout loop, so a long-running TUI or poller would grow without bound.
func TestUntrackedStatCacheDropsWorktreesItStopsSweeping(t *testing.T) {
	clock := time.Now()
	cache := NewUntrackedStatCache()
	cache.now = func() time.Time { return clock }

	cache.replace("/abandoned", map[string]untrackedStat{"k": {}})
	cache.replace("/live", map[string]untrackedStat{"k": {}})

	clock = clock.Add(untrackedCacheTTL + time.Minute)
	cache.replace("/live", map[string]untrackedStat{"k": {}})

	if cache.size("/abandoned") != 0 {
		t.Fatal("a worktree nobody sweeps any more survived; the cache grows without bound")
	}
	if cache.size("/live") != 1 {
		t.Fatal("the worktree still being swept was dropped")
	}
}

// Bounding by worktree count instead of age would evict by rank: a dashboard
// watching more worktrees than the cap evicts the entry it is about to need and
// misses on every one of them, which is the starvation the cache exists to fix.
func TestUntrackedStatCacheKeepsEveryWorktreeItKeepsSweeping(t *testing.T) {
	clock := time.Now()
	cache := NewUntrackedStatCache()
	cache.now = func() time.Time { return clock }

	const worktrees = 200
	for round := range 3 {
		for i := range worktrees {
			cache.replace(fmt.Sprintf("/wt%d", i), map[string]untrackedStat{"k": {}})
		}
		clock = clock.Add(2 * time.Second)
		if round == 0 {
			continue
		}
		for i := range worktrees {
			if cache.size(fmt.Sprintf("/wt%d", i)) != 1 {
				t.Fatalf("worktree %d missed on round %d; a rank-based bound would thrash", i, round)
			}
		}
	}
}

// A pass long enough to cross the TTL must not treat its own cache hits as
// fresh measurements: re-stamping them would hand a stale verdict another full
// window, and the bound this cache promises would not hold.
func TestUntrackedStatCacheDoesNotRestampCacheHits(t *testing.T) {
	clock := time.Now()
	cache := NewUntrackedStatCache()
	cache.now = func() time.Time { return clock }
	cache.replace("/wt", map[string]untrackedStat{"k": {stat: FileStat{Additions: 1}}})

	// A later pass reads the entry while it is still live...
	clock = clock.Add(untrackedEntryTTL - time.Second)
	hit, ok := cache.lookup("/wt", "k")
	if !ok {
		t.Fatal("lookup() missed an entry that is still inside its TTL")
	}

	// ...and writes it back only after measuring other files pushed past it.
	clock = clock.Add(2 * time.Second)
	cache.replace("/wt", map[string]untrackedStat{"k": hit})

	if _, ok := cache.lookup("/wt", "k"); ok {
		t.Fatal("a cache hit restarted its own TTL; the staleness bound does not hold")
	}
}

func TestUntrackedStatCacheNilIsUsable(t *testing.T) {
	var absent *UntrackedStatCache
	absent.replace("/wt", map[string]untrackedStat{"k": {stat: FileStat{Path: "x"}}})
	if _, ok := absent.lookup("/wt", "k"); ok {
		t.Fatal("nil cache reported a hit; it must simply disable memoization")
	}
}

// A worktree sweep must not evict another worktree's entries. A shared
// capacity cap did exactly that: one large worktree wiped every stable entry
// and the next tick re-ran a git process per file.
func TestUntrackedStatCacheKeepsWorktreesIndependent(t *testing.T) {
	cache := NewUntrackedStatCache()
	big := map[string]untrackedStat{}
	for i := range 5000 {
		big[fmt.Sprintf("k%d", i)] = untrackedStat{}
	}
	cache.replace("/small", map[string]untrackedStat{"kept": {stat: FileStat{Path: "kept"}}})
	cache.replace("/big", big)

	if _, ok := cache.lookup("/small", "kept"); !ok {
		t.Fatal("a large sweep evicted another worktree's entry")
	}
	if cache.size("/big") != len(big) {
		t.Fatalf("cache holds %d entries for the large worktree, want %d", cache.size("/big"), len(big))
	}

	// 次の sweep が見つけなかった entry は落ちる(= disk にある分だけ保つ)。
	cache.replace("/big", map[string]untrackedStat{"k0": {}})
	if cache.size("/big") != 1 {
		t.Fatalf("cache holds %d entries after a smaller sweep, want 1", cache.size("/big"))
	}
	if _, ok := cache.lookup("/small", "kept"); !ok {
		t.Fatal("a sweep of one worktree dropped another worktree's entry")
	}
}

// A Cwd-relative worktree path has to reach os.Lstat resolved: git takes it
// from Cwd, the Go process would take it from its own working directory.
func TestRunnerWorktreeResolvesRelativePathBeforeReadingUntracked(t *testing.T) {
	repo := initPatchRepo(t)
	writeGitstatFile(t, repo, "untracked.txt", []byte("new\n"))

	got, err := (Runner{Cwd: filepath.Dir(repo)}).Worktree(filepath.Base(repo), "main")
	if err != nil {
		t.Fatalf("Worktree() = %v, want no error", err)
	}
	if got.Additions != 1 || got.Deletions != 0 {
		t.Fatalf("Worktree() = +%d/-%d, want +1/-0", got.Additions, got.Deletions)
	}
}

// diff.renameLimit skips exhaustive detection once the candidate matrix grows,
// handing back delete+add pairs — the inflation this package exists to avoid.
func TestRunnerWorktreePatchIgnoresRepoRenameLimit(t *testing.T) {
	repo := initPatchRepo(t)
	gitTest(t, repo, "config", "diff.renameLimit", "1")
	seed := map[string][]byte{}
	for i := range 5 {
		seed[fmt.Sprintf("moved%d.txt", i)] = fmt.Appendf(nil, "one\ntwo\nthree\nfour\nfive\n%d\n", i)
	}
	seedOnMain(t, repo, seed)
	for i := range 5 {
		gitTest(t, repo, "mv", fmt.Sprintf("moved%d.txt", i), fmt.Sprintf("renamed%d.txt", i))
		writeGitstatFile(t, repo, fmt.Sprintf("renamed%d.txt", i),
			fmt.Appendf(nil, "one\ntwo\nthree\nfour\nFIVE\n%d\n", i))
	}
	gitTest(t, repo, "add", "-A")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		stat := findFileStat(t, got.Files, fmt.Sprintf("renamed%d.txt", i))
		if stat.OldPath != fmt.Sprintf("moved%d.txt", i) {
			t.Fatalf("WorktreePatch().Files = %#v, want every move detected as a rename", got.Files)
		}
	}
}

// core.bigFileThreshold calls a file binary without reading it. Pinning it on
// only some commands would let files[] report a counted text file while the
// patch says "Binary files ... differ", breaking the wire contract that binary
// files carry no patch.
func TestRunnerWorktreePatchIgnoresRepoBigFileThreshold(t *testing.T) {
	repo := initPatchRepo(t)
	gitTest(t, repo, "config", "core.bigFileThreshold", "1")
	writeGitstatFile(t, repo, "untracked.txt", []byte("one\ntwo\n"))

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "untracked.txt",
		Additions:     2,
		PatchIncluded: true,
	})
	if strings.Contains(got.Patch, "Binary files") {
		t.Fatalf("WorktreePatch().Patch = %q, want the counted text patch", got.Patch)
	}

	summary, err := Runner{}.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Additions != 2 {
		t.Fatalf("Worktree() = +%d, want +2 to match the file list", summary.Additions)
	}
}

// The tracked numstat decides text vs binary too, so leaving it unpinned would
// turn ordinary tracked text files into -/- rows on both surfaces.
func TestRunnerWorktreePatchIgnoresRepoBigFileThresholdForTrackedFiles(t *testing.T) {
	repo := initPatchRepo(t)
	gitTest(t, repo, "config", "core.bigFileThreshold", "1")
	writeGitstatFile(t, repo, "tracked.txt", []byte("one\ntwo\nthree\n"))

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertFileStat(t, got.Files, FileStat{
		Path:          "tracked.txt",
		Additions:     2,
		PatchIncluded: true,
	})
	if strings.Contains(got.Patch, "Binary files") {
		t.Fatalf("WorktreePatch().Patch = %q, want the counted text patch", got.Patch)
	}
}

// Counting an untracked file costs a git process, so a request that cannot
// possibly fit under MaxFiles must be rejected before any of them run.
func TestRunnerWorktreePatchRejectsOverFileLimitBeforeMeasuring(t *testing.T) {
	repo := initPatchRepo(t)
	for i := range 6 {
		writeGitstatFile(t, repo, fmt.Sprintf("u%d.txt", i), []byte("one\n"))
	}
	logPath := filepath.Join(t.TempDir(), "git-args.log")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FANOUT_GITSTAT_REAL_GIT", realGit)
	t.Setenv("FANOUT_GITSTAT_LOG", logPath)
	installGitstatShim(t, "git", `
printf '%s\n' "$*" >> "$FANOUT_GITSTAT_LOG"
exec "$FANOUT_GITSTAT_REAL_GIT" "$@"
`)

	_, err = (Runner{MaxFiles: 2}).WorktreePatch(repo, "main")
	if err == nil || !strings.Contains(err.Error(), "limit is 2") {
		t.Fatalf("WorktreePatch() = %v, want a file-limit error", err)
	}
	logged, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(logged), "--no-index") {
		t.Fatalf("measured untracked files before rejecting the request:\n%s", logged)
	}
}

// git collapses a nested checkout into one directory entry ("sub/"). Treating
// it as a file fails the whole collection, so every dashboard row for the
// worktree would report an error on every poll.
func TestRunnerSkipsUntrackedNestedRepository(t *testing.T) {
	repo := initPatchRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test User"},
	} {
		gitTest(t, sub, args...)
	}
	writeGitstatFile(t, sub, "inner.txt", []byte("inner\n"))
	gitTest(t, sub, "add", "-A")
	gitTest(t, sub, "commit", "-m", "inner")
	writeGitstatFile(t, repo, "untracked.txt", []byte("one\n"))

	summary, err := Runner{}.Worktree(repo, "main")
	if err != nil {
		t.Fatalf("Worktree() = %v, want no error beside a nested checkout", err)
	}
	// The nested repo contributes nothing; the real untracked file still counts.
	if summary.Additions != 1 || !summary.Dirty {
		t.Fatalf("Worktree() = %+v, want +1/-0 dirty", summary)
	}

	patch, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatalf("WorktreePatch() = %v, want no error beside a nested checkout", err)
	}
	assertFileStat(t, patch.Files, FileStat{
		Path:          "untracked.txt",
		Additions:     1,
		PatchIncluded: true,
	})
}

func TestRunnerWorktreePatchIgnoresRepoRenameConfig(t *testing.T) {
	for _, renames := range []string{"false", "copies"} {
		t.Run("diff.renames="+renames, func(t *testing.T) {
			repo := initPatchRepo(t)
			gitTest(t, repo, "config", "diff.renames", renames)
			seedOnMain(t, repo, map[string][]byte{"moved.txt": []byte("one\ntwo\nthree\nfour\n")})
			gitTest(t, repo, "mv", "moved.txt", "renamed.txt")
			gitTest(t, repo, "add", "-A")

			got, err := Runner{}.WorktreePatch(repo, "main")
			if err != nil {
				t.Fatal(err)
			}
			stat := findFileStat(t, got.Files, "renamed.txt")
			if stat.OldPath != "moved.txt" {
				t.Fatalf("WorktreePatch().Files = %#v, want renamed.txt from moved.txt", got.Files)
			}
		})
	}
}

func TestRunnerWorktreePatchScopesRenamePathspecToBothPaths(t *testing.T) {
	repo := initPatchRepo(t)
	seedOnMain(t, repo, map[string][]byte{"moved.txt": []byte("one\ntwo\nthree\nfour\n")})
	gitTest(t, repo, "mv", "moved.txt", "renamed.txt")
	writeGitstatFile(t, repo, "tracked.txt", []byte("one\nsibling\n"))
	gitTest(t, repo, "add", "-A")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	stat := findFileStat(t, got.Files, "renamed.txt")
	if stat.OldPath != "moved.txt" || stat.Additions != 0 || stat.Deletions != 0 {
		t.Fatalf("WorktreePatch().Files = %#v, want a pure rename of moved.txt", got.Files)
	}
	if !strings.Contains(got.Patch, "rename from moved.txt") {
		t.Fatalf("WorktreePatch().Patch = %q, want a rename header", got.Patch)
	}
	if n := strings.Count(got.Patch, "diff --git "); n != 2 {
		t.Fatalf("WorktreePatch().Patch has %d file blocks, want 2 (rename + sibling)", n)
	}
}

func TestRunnerWorktreePatchFallsBackWhenRenamePathsNest(t *testing.T) {
	repo := initPatchRepo(t)
	seedOnMain(t, repo, map[string][]byte{"moved": []byte("one\ntwo\nthree\nfour\n")})
	if err := os.Remove(filepath.Join(repo, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "moved"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitstatFile(t, repo, "moved/inner.txt", []byte("one\ntwo\nthree\nfour\n"))
	gitTest(t, repo, "add", "-A")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	// git pairs `moved` with `moved/inner.txt`, but no pathspec can scope that
	// pair, so the whole collection drops rename detection.
	deleted := findFileStat(t, got.Files, "moved")
	added := findFileStat(t, got.Files, "moved/inner.txt")
	if deleted.OldPath != "" || added.OldPath != "" {
		t.Fatalf("WorktreePatch().Files = %#v, want no rename linkage", got.Files)
	}
	if deleted.Deletions != 4 || added.Additions != 4 {
		t.Fatalf("WorktreePatch().Files = %#v, want -4 and +4", got.Files)
	}
	if n := strings.Count(got.Patch, "diff --git "); n != 2 {
		t.Fatalf("WorktreePatch().Patch has %d file blocks, want 2", n)
	}

	summary, err := Runner{}.Worktree(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Additions != added.Additions || summary.Deletions != deleted.Deletions {
		t.Fatalf("Worktree() = +%d/-%d, want the same fallback totals", summary.Additions, summary.Deletions)
	}
}

func TestRunnerWorktreePatchKeepsOversizedCounts(t *testing.T) {
	repo := initPatchRepo(t)
	writeGitstatFile(t, repo, "tracked.txt", bytes.Repeat([]byte{'x'}, patchFileLimit+1))
	gitTest(t, repo, "add", "tracked.txt")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	// The patch text is over budget, the counts are not: git measured them in
	// the same numstat pass the session list sums.
	assertFileStat(t, got.Files, FileStat{
		Path:          "tracked.txt",
		Additions:     1,
		Deletions:     1,
		OmittedReason: "tooLarge",
	})
}

func TestRunnerWorktreePatchSizesRenameFromItsBasePath(t *testing.T) {
	repo := initPatchRepo(t)
	seedOnMain(t, repo, map[string][]byte{"moved.txt": bytes.Repeat([]byte{'x'}, patchFileLimit+1)})
	gitTest(t, repo, "mv", "moved.txt", "renamed.txt")
	gitTest(t, repo, "add", "-A")

	got, err := Runner{}.WorktreePatch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	// The merge-base blob lives at the old path; sizing the new one would find
	// nothing and let an oversized patch through.
	stat := findFileStat(t, got.Files, "renamed.txt")
	if stat.OmittedReason != "tooLarge" || stat.PatchIncluded {
		t.Fatalf("WorktreePatch().Files = %#v, want renamed.txt omitted as tooLarge", got.Files)
	}
}

// seedOnMain commits files on main and fast-forwards feature onto it, so the
// merge-base the collectors diff against already contains them. A rename is
// only detectable when its source is in that base.
func seedOnMain(t *testing.T, repo string, files map[string][]byte) {
	t.Helper()
	gitTest(t, repo, "checkout", "main")
	for name, content := range files {
		if dir := filepath.Dir(name); dir != "." {
			if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeGitstatFile(t, repo, name, content)
	}
	gitTest(t, repo, "add", "-A")
	gitTest(t, repo, "commit", "-m", "seed rename sources")
	gitTest(t, repo, "checkout", "feature")
	gitTest(t, repo, "reset", "--hard", "main")
}

func initPatchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initPatchRepoAt(t, repo)
	return repo
}

func initPatchRepoAt(t *testing.T, repo string) {
	t.Helper()
	gitTest(t, repo, "init")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test User")
	writeGitstatFile(t, repo, "tracked.txt", []byte("one\n"))
	gitTest(t, repo, "add", "tracked.txt")
	gitTest(t, repo, "commit", "-m", "initial")
	gitTest(t, repo, "branch", "-M", "main")
	gitTest(t, repo, "checkout", "-b", "feature")
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

// size reports how many entries a worktree holds, for assertions only.
func (c *UntrackedStatCache) size(worktree string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byWorktree[worktree].entries)
}
