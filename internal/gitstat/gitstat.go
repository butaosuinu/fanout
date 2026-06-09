// Package gitstat reads lightweight worktree change statistics through git.
package gitstat

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var (
	insertionRE = regexp.MustCompile(`(\d+)\s+insertions?\(\+\)`)
	deletionRE  = regexp.MustCompile(`(\d+)\s+deletions?\(-\)`)
)

// Stat is the small pane-local change summary fanout displays in monitoring UI.
type Stat struct {
	Additions int
	Deletions int
	Dirty     bool
}

// Runner shells out to git. Cwd is optional and only affects process startup;
// worktree selection is always explicit through git -C.
type Runner struct {
	Cwd string
}

// Worktree returns additions/deletions from `git diff --shortstat HEAD` and
// dirty state from `git status --porcelain`.
func (r Runner) Worktree(path string) (Stat, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Stat{}, fmt.Errorf("worktree path is empty")
	}

	diffOut, err := r.git("-C", path, "diff", "--shortstat", "HEAD")
	if err != nil {
		return Stat{}, err
	}
	statusOut, err := r.git("-C", path, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return Stat{}, err
	}

	stat := parseShortStat(string(diffOut))
	stat.Dirty = strings.TrimSpace(string(statusOut)) != ""
	return stat, nil
}

func (r Runner) git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if r.Cwd != "" {
		cmd.Dir = r.Cwd
	}
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return out, err
	}
	return out, nil
}

func gitEnv() []string {
	return append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
}

func parseShortStat(out string) Stat {
	return Stat{
		Additions: parseCount(insertionRE, out),
		Deletions: parseCount(deletionRE, out),
	}
}

func parseCount(re *regexp.Regexp, out string) int {
	m := re.FindStringSubmatch(out)
	if len(m) != 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}
