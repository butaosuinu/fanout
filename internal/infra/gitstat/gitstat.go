// Package gitstat reads lightweight worktree change statistics through git.
package gitstat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/infra/execx"
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

// Worktree returns additions/deletions from `git diff --shortstat` against the
// merge-base with baseRef (= committed + staged + unstaged total since the
// branch diverged) and dirty state from `git status --porcelain`. A baseRef
// that cannot be resolved silently falls back to HEAD (uncommitted-only diff).
func (r Runner) Worktree(path, baseRef string) (Stat, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Stat{}, fmt.Errorf("worktree path is empty")
	}

	diffOut, err := r.git("-C", path, "diff", "--shortstat", r.diffBase(path, baseRef))
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

// MergeBase resolves the merge-base of HEAD and baseRef for review. An empty
// baseRef resolves through origin/HEAD. Unlike diffBase, resolution failures
// are returned instead of falling back to HEAD.
func (r Runner) MergeBase(path, baseRef string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("worktree path is empty")
	}

	baseRef = strings.TrimSpace(baseRef)
	var candidates []string
	switch {
	case strings.HasPrefix(baseRef, "refs/"):
		if _, err := r.git("-C", path, "check-ref-format", baseRef); err != nil {
			return "", fmt.Errorf("validate base ref %q: %w", baseRef, err)
		}
		candidates = append(candidates, baseRef)
	case strings.HasPrefix(baseRef, "origin/"):
		candidate := "refs/remotes/" + baseRef
		if _, err := r.git("-C", path, "check-ref-format", candidate); err != nil {
			return "", fmt.Errorf("validate base ref %q: %w", baseRef, err)
		}
		candidates = append(candidates, candidate)
	case baseRef != "":
		if _, err := r.git("-C", path, "check-ref-format", "--branch", baseRef); err != nil {
			return "", fmt.Errorf("validate base ref %q: %w", baseRef, err)
		}
		candidates = append(candidates, "refs/heads/"+baseRef, "refs/remotes/origin/"+baseRef)
	default:
		out, err := r.git("-C", path, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
		if err != nil {
			return "", fmt.Errorf("resolve origin/HEAD: %w", err)
		}
		head := strings.TrimSpace(string(out))
		if head == "" {
			return "", fmt.Errorf("resolve origin/HEAD: empty ref")
		}
		candidates = append(candidates, head)
	}

	var lastErr error
	for _, candidate := range candidates {
		out, err := r.git("-C", path, "rev-parse", "--verify", "--end-of-options", candidate+"^{commit}")
		if err != nil {
			lastErr = err
			continue
		}
		baseSHA := strings.TrimSpace(string(out))
		if baseSHA == "" {
			lastErr = fmt.Errorf("git rev-parse %s returned an empty SHA", candidate)
			continue
		}

		out, err = r.git("-C", path, "merge-base", baseSHA, "HEAD")
		if err != nil {
			lastErr = err
			continue
		}
		if mergeBaseSHA := strings.TrimSpace(string(out)); mergeBaseSHA != "" {
			return mergeBaseSHA, nil
		}
		lastErr = fmt.Errorf("git merge-base %s HEAD returned an empty SHA", candidate)
	}
	return "", fmt.Errorf("resolve merge-base for %q: %w", baseRef, lastErr)
}

// diffBase resolves the ref the diff is measured against: the merge-base of
// HEAD and the recorded base branch (trying "origin/<base>" as well), or
// origin/HEAD for legacy rows without a recorded base. Resolution failures
// never become errors — they fall back to "HEAD", keeping the WorktreeErr
// contract that only the final diff/status calls may fail.
func (r Runner) diffBase(path, baseRef string) string {
	baseRef = strings.TrimSpace(baseRef)
	var candidates []string
	if baseRef != "" {
		candidates = append(candidates, baseRef)
		if !strings.HasPrefix(baseRef, "origin/") && !strings.HasPrefix(baseRef, "refs/") {
			candidates = append(candidates, "origin/"+baseRef)
		}
	} else if out, err := r.git("-C", path, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if head := strings.TrimSpace(string(out)); head != "" {
			candidates = append(candidates, head)
		}
	}
	for _, candidate := range candidates {
		out, err := r.git("-C", path, "merge-base", candidate, "HEAD")
		if err != nil {
			continue
		}
		if sha := strings.TrimSpace(string(out)); sha != "" {
			return sha
		}
	}
	return "HEAD"
}

func (r Runner) git(args ...string) ([]byte, error) {
	return execx.Output(r.Cwd, gitEnv(), "git", args...)
}

// gitEnv returns the extra environment entries execx.Output appends to
// os.Environ() for every git invocation.
func gitEnv() []string {
	return []string{"LC_ALL=C", "GIT_OPTIONAL_LOCKS=0"}
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
