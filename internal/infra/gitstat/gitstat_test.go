package gitstat

import (
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
			wantRef: "main",
		},
		{
			name:            "missing base",
			baseRef:         "no-such-branch",
			wantErrContains: "no-such-branch",
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
