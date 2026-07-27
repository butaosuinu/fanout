// Package gitstat reads lightweight worktree change statistics through git.
package gitstat

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/infra/execx"
)

const patchFileLimit = 256 * 1024

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

// FileStat describes one changed file in a review patch.
type FileStat struct {
	Path          string
	Additions     int
	Deletions     int
	Binary        bool
	PatchIncluded bool
	OmittedReason string
}

// Patch is a merge-base-relative worktree patch and its complete file list.
type Patch struct {
	MergeBase string
	Patch     string
	Files     []FileStat
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

// WorktreePatch returns the committed, staged, unstaged, and untracked changes
// since the strict merge-base with baseRef. Binary files and files larger than
// 256 KiB remain in Files but are omitted from Patch.
func (r Runner) WorktreePatch(path, baseRef string) (Patch, error) {
	path = strings.TrimSpace(path)
	mergeBase, err := r.MergeBase(path, baseRef)
	if err != nil {
		return Patch{}, err
	}

	trackedOut, err := r.git(
		"-C", path,
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"--numstat", "-z", mergeBase, "--",
	)
	if err != nil {
		return Patch{}, err
	}
	tracked, err := parseNumStat(trackedOut)
	if err != nil {
		return Patch{}, fmt.Errorf("parse tracked numstat: %w", err)
	}

	files := make([]patchFile, 0, len(tracked))
	for _, stat := range tracked {
		files = append(files, patchFile{FileStat: stat, tracked: true})
	}

	untrackedOut, err := r.git("-C", path, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Patch{}, err
	}
	untracked, err := parseNULPaths(untrackedOut)
	if err != nil {
		return Patch{}, fmt.Errorf("parse untracked paths: %w", err)
	}
	for _, rel := range untracked {
		stat, statErr := r.untrackedFileStat(path, rel)
		if statErr != nil {
			return Patch{}, statErr
		}
		files = append(files, patchFile{FileStat: stat})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	result := Patch{
		MergeBase: mergeBase,
		Files:     make([]FileStat, 0, len(files)),
	}
	var patch strings.Builder
	for _, file := range files {
		stat := file.FileStat
		if stat.OmittedReason == "" {
			tooLarge, sizeErr := r.patchFileTooLarge(path, mergeBase, file)
			if sizeErr != nil {
				return Patch{}, sizeErr
			}
			if tooLarge {
				stat.Additions = 0
				stat.Deletions = 0
				stat.OmittedReason = "tooLarge"
			}
		}
		if stat.OmittedReason == "" {
			var out []byte
			if file.tracked {
				out, err = r.git(
					"-C", path,
					"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
					mergeBase, "--", stat.Path,
				)
			} else {
				var code int
				out, code, err = r.gitExitCode(
					"-C", path,
					"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
					"--no-index", "--", "/dev/null", stat.Path,
				)
				if code == 1 {
					err = nil
				}
			}
			if err != nil {
				return Patch{}, err
			}
			patch.WriteString(string(out))
			stat.PatchIncluded = true
		}
		result.Files = append(result.Files, stat)
	}
	result.Patch = patch.String()
	return result, nil
}

type patchFile struct {
	FileStat
	tracked bool
}

func (r Runner) untrackedFileStat(path, rel string) (FileStat, error) {
	fullPath, err := containedPath(path, rel)
	if err != nil {
		return FileStat{}, err
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		return FileStat{}, fmt.Errorf("inspect untracked file %q: %w", rel, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return FileStat{}, fmt.Errorf("untracked path %q is not a regular file or symlink", rel)
	}
	if info.Size() > patchFileLimit {
		return FileStat{Path: rel, OmittedReason: "tooLarge"}, nil
	}

	out, code, err := r.gitExitCode(
		"-C", path,
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"--no-index", "--numstat", "-z", "--", "/dev/null", rel,
	)
	if code == 1 {
		err = nil
	}
	if err != nil {
		return FileStat{}, err
	}
	stats, err := parseNumStat(out)
	if err != nil {
		return FileStat{}, fmt.Errorf("parse untracked numstat for %q: %w", rel, err)
	}
	if len(stats) != 1 {
		return FileStat{}, fmt.Errorf("parse untracked numstat for %q: got %d files, want 1", rel, len(stats))
	}
	stat := stats[0]
	stat.Path = rel
	if stat.Binary {
		stat.OmittedReason = "binary"
	}
	return stat, nil
}

func (r Runner) patchFileTooLarge(path, mergeBase string, file patchFile) (bool, error) {
	if !file.tracked {
		return file.OmittedReason == "tooLarge", nil
	}

	fullPath, err := containedPath(path, file.Path)
	if err != nil {
		return false, err
	}
	info, statErr := os.Lstat(fullPath)
	if statErr == nil {
		if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			if info.Size() > patchFileLimit {
				return true, nil
			}
		}
	} else if !os.IsNotExist(statErr) {
		return false, fmt.Errorf("inspect tracked file %q: %w", file.Path, statErr)
	}

	out, err := r.git("-C", path, "ls-tree", "-l", "-z", mergeBase, "--", file.Path)
	if err != nil {
		return false, err
	}
	if len(out) == 0 {
		return false, nil
	}
	metadata, _, found := bytes.Cut(out, []byte{'\t'})
	if !found {
		return false, fmt.Errorf("parse base file size for %q: missing path separator", file.Path)
	}
	fields := strings.Fields(string(metadata))
	if len(fields) != 4 {
		return false, fmt.Errorf("parse base file size for %q: malformed ls-tree output", file.Path)
	}
	if fields[3] == "-" {
		return false, nil
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return false, fmt.Errorf("parse base file size for %q: %w", file.Path, err)
	}
	return size > patchFileLimit, nil
}

func containedPath(root, rel string) (string, error) {
	relPath := filepath.FromSlash(rel)
	if rel == "" || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("invalid repository-relative path %q", rel)
	}
	clean := filepath.Clean(relPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repository-relative path escapes worktree: %q", rel)
	}
	return filepath.Join(root, clean), nil
}

func parseNumStat(out []byte) ([]FileStat, error) {
	records, err := splitNUL(out)
	if err != nil {
		return nil, err
	}
	stats := make([]FileStat, 0, len(records))
	for i := 0; i < len(records); i++ {
		fields := bytes.SplitN(records[i], []byte{'\t'}, 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed numstat record")
		}
		path := fields[2]
		if len(path) == 0 {
			if i+2 >= len(records) {
				return nil, fmt.Errorf("malformed rename numstat record")
			}
			path = records[i+2]
			i += 2
		}

		additions, addBinary, err := parseNumStatCount(fields[0])
		if err != nil {
			return nil, err
		}
		deletions, deleteBinary, err := parseNumStatCount(fields[1])
		if err != nil {
			return nil, err
		}
		if addBinary != deleteBinary {
			return nil, fmt.Errorf("numstat binary markers do not match")
		}
		stat := FileStat{
			Path:      string(path),
			Additions: additions,
			Deletions: deletions,
			Binary:    addBinary,
		}
		if stat.Binary {
			stat.OmittedReason = "binary"
		}
		stats = append(stats, stat)
	}
	return stats, nil
}

func parseNumStatCount(field []byte) (int, bool, error) {
	if bytes.Equal(field, []byte("-")) {
		return 0, true, nil
	}
	count, err := strconv.Atoi(string(field))
	if err != nil {
		return 0, false, fmt.Errorf("invalid numstat count %q", field)
	}
	return count, false, nil
}

func parseNULPaths(out []byte) ([]string, error) {
	records, err := splitNUL(out)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(records))
	for _, record := range records {
		if len(record) == 0 {
			return nil, fmt.Errorf("empty path")
		}
		paths = append(paths, string(record))
	}
	return paths, nil
}

func splitNUL(out []byte) ([][]byte, error) {
	if len(out) == 0 {
		return [][]byte{}, nil
	}
	if out[len(out)-1] != 0 {
		return nil, fmt.Errorf("missing NUL terminator")
	}
	return bytes.Split(out[:len(out)-1], []byte{0}), nil
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

func (r Runner) gitExitCode(args ...string) ([]byte, int, error) {
	return execx.OutputExitCode(r.Cwd, gitEnv(), "git", args...)
}

// gitEnv returns the extra environment entries execx.Output appends to
// os.Environ() for every git invocation.
func gitEnv() []string {
	return []string{"LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_LITERAL_PATHSPECS=1"}
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
