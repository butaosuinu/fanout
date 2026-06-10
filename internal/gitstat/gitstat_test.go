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

	got, err := Runner{}.Worktree(repo)
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

	got, err := Runner{}.Worktree(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 0 || got.Deletions != 0 || !got.Dirty {
		t.Fatalf("Worktree() = %+v, want +0/-0 dirty", got)
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

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
