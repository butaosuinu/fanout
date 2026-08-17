// Package gitstat reads lightweight worktree change statistics through git.
package gitstat

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/infra/execx"
)

const patchFileLimit = 256 * 1024

// Stat is the small pane-local change summary fanout displays in monitoring UI.
type Stat struct {
	Additions int
	Deletions int
	Dirty     bool
}

// FileStat describes one changed file in a review patch.
type FileStat struct {
	Path string
	// OldPath is the merge-base-side path of a detected rename, empty otherwise.
	// Path always carries the final path, so callers keep keying by Path.
	OldPath       string
	Additions     int
	Deletions     int
	Binary        bool
	PatchIncluded bool
	OmittedReason string
}

// Patch is a merge-base-relative worktree patch and its complete file list.
type Patch struct {
	MergeBase string
	// Head is the commit the worktree was on when the patch was taken. A reader
	// needs it to tell what the patch is a diff *of*: the same branch name can
	// sit on a different commit here than it does on the remote.
	Head  string
	Patch string
	Files []FileStat
}

// Runner shells out to git. Cwd is optional and only affects process startup;
// worktree selection is always explicit through git -C.
type Runner struct {
	Cwd           string
	Context       context.Context
	MaxFiles      int
	MaxPatchBytes int
	// UntrackedCache is optional. Supply one when the same Runner keeps
	// re-measuring a worktree, as the dashboard poller does.
	UntrackedCache *UntrackedStatCache
}

// UntrackedStatCache memoizes untracked-file counts for a short while.
//
// Counting one untracked file costs a git process, and Runner.Worktree runs on
// the dashboard's 2-second tick. Uncached, a worktree holding 500 un-ignored
// files spends ~4.6s per poll and starves the refresh loop.
//
// Entries key on what the file contains — size and mtime both survive an
// in-place rewrite (`cp -p` of a same-sized file restores the timestamp to the
// nanosecond), so stat metadata would miss a change outright. Hashing costs far
// less than the process it saves: 500 files at the 256 KiB ceiling hash in
// ~190ms against ~4.6s of git startup.
//
// Content is not the only input to git's verdict, though. .gitattributes (in
// the worktree, in $GIT_DIR/info, or the user file), core.bigFileThreshold, and
// diff.<driver>.binary all reclassify the same bytes, and that list is not
// closed. Rather than enumerate it — every miss leaves the session list
// answering from a verdict the cacheless diff viewer no longer shares — entries
// simply expire. Staleness is then bounded by untrackedEntryTTL instead of
// lasting until the file itself changes.
//
// Entries live per worktree and each collection pass replaces its worktree's
// set wholesale, so the cache holds exactly what is on disk. A shared capacity
// cap would be worse than no cache: one sweep past the limit evicts the stable
// entries of every other worktree, and the next tick re-runs a git process for
// each of them.
//
// The zero value is not usable; call NewUntrackedStatCache. A nil cache is
// valid and simply disables memoization.
type UntrackedStatCache struct {
	mu         sync.Mutex
	byWorktree map[string]untrackedWorktreeEntry
	now        func() time.Time
}

type untrackedWorktreeEntry struct {
	entries map[string]untrackedStat
	swept   time.Time
}

// untrackedStat is one memoized count and when git produced it.
type untrackedStat struct {
	stat     FileStat
	measured time.Time
}

// untrackedEntryTTL bounds how long a memoized count may outlive a change to
// something other than the file's own bytes. At the 2-second tick this still
// removes ~93% of the per-file git processes, and it closes the whole class of
// "some other input to git's text/binary verdict moved" in one rule.
const untrackedEntryTTL = 30 * time.Second

// untrackedCacheTTL drops worktrees nobody sweeps any more. A cleaned-up
// worktree is never collected again, so without this its entries would live
// until the process exits — and creating and cleaning up sessions is the normal
// fanout loop.
//
// The bound is time, not a worktree count: a count evicts by rank, so a
// dashboard watching more worktrees than the cap would evict the very entry it
// is about to need and miss on every single one. Anything still being swept is
// refreshed far inside this window at the 2-second tick, so only genuinely
// abandoned worktrees age out.
const untrackedCacheTTL = 5 * time.Minute

func NewUntrackedStatCache() *UntrackedStatCache {
	return &UntrackedStatCache{
		byWorktree: map[string]untrackedWorktreeEntry{},
		now:        time.Now,
	}
}

// lookup returns a live entry with the time git produced it, so the caller can
// hand that time back to replace instead of the entry silently restarting its
// TTL when it is written out again.
func (c *UntrackedStatCache) lookup(worktree, key string) (untrackedStat, bool) {
	if c == nil {
		return untrackedStat{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.byWorktree[worktree].entries[key]
	if !ok || c.now().Sub(entry.measured) > untrackedEntryTTL {
		return untrackedStat{}, false
	}
	return entry, true
}

// replace swaps in what one collection pass produced. Entries the pass did not
// see describe files that are gone or changed, so dropping them keeps the cache
// bounded by the worktree without ever evicting a live entry.
//
// An entry carrying a measurement time came from cache and keeps it; only a
// zero time means this pass ran git, and only that restarts the TTL. Inferring
// freshness from the stored time instead would be wrong: a pass long enough to
// cross the TTL would treat its own cache hits as new measurements and give a
// stale verdict another full window.
func (c *UntrackedStatCache) replace(worktree string, entries map[string]untrackedStat) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.dropAbandonedWorktrees(now)
	if len(entries) == 0 {
		delete(c.byWorktree, worktree)
		return
	}
	next := make(map[string]untrackedStat, len(entries))
	for key, entry := range entries {
		if entry.measured.IsZero() {
			entry.measured = now
		}
		next[key] = entry
	}
	c.byWorktree[worktree] = untrackedWorktreeEntry{entries: next, swept: now}
}

func (c *UntrackedStatCache) dropAbandonedWorktrees(now time.Time) {
	for path, entry := range c.byWorktree {
		if now.Sub(entry.swept) > untrackedCacheTTL {
			delete(c.byWorktree, path)
		}
	}
}

// untrackedCacheKey identifies the file by worktree, path, file type, and
// content hash. The path stays in the key because .gitattributes can classify
// the same bytes differently at a different path; the other things that
// reclassify content in place are handled by the entry TTL, not by the key.
func untrackedCacheKey(path, rel string, info os.FileInfo) (string, error) {
	content, err := untrackedContent(path, rel, info)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"%s\x00%s\x00%d\x00%x",
		path, rel, info.Mode().Type(), sha256.Sum256(content),
	), nil
}

// untrackedContent reads what git would compare: the link target for a symlink,
// the file bytes otherwise.
func untrackedContent(path, rel string, info os.FileInfo) ([]byte, error) {
	fullPath, err := containedPath(path, rel)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(fullPath)
		if readErr != nil {
			return nil, fmt.Errorf("read symlink: %w", readErr)
		}
		return []byte(target), nil
	}
	return os.ReadFile(fullPath)
}

// diffFlags is the flag set every diff in this package shares. Each entry pins
// a behavior a repo config knob could otherwise change: --find-renames defeats
// diff.renames (which can switch detection off or widen it to copies), -l0
// defeats diff.renameLimit (which silently drops exhaustive detection and hands
// back delete+add pairs), and --no-ext-diff / --no-textconv pin the bytes being
// compared.
func diffFlags(renames string) []string {
	return []string{
		"--no-ext-diff", "--no-textconv", "--no-color",
		renames, "-l0", "--ignore-submodules=none",
	}
}

// gitPinArgs are the git-level options every diff in this package shares.
// core.bigFileThreshold decides text vs binary without reading content, so
// leaving it to repo config lets a repo turn ordinary text files into `-/-`
// binary rows — and the untracked count is memoized, so a mid-run change would
// keep answering from a stale verdict. Every file these commands touch is under
// the package's own 256 KiB ceiling, so git's default just means the content
// decides.
func gitPinArgs() []string {
	return []string{"-c", "core.bigFileThreshold=512m"}
}

// diffCmd builds the argv for a diff inside path. Every diff goes through here
// so the pinned git-level options cannot be forgotten on one route and applied
// on another — that split is what let metadata and patch disagree before.
func diffCmd(path string, tail ...string) []string {
	return append(append(gitPinArgs(), "-C", path, "diff"), tail...)
}

func numStatArgs(base, renames string) []string {
	return append(diffFlags(renames), "--numstat", "-z", base, "--")
}

func patchArgs(base string) []string {
	return append(diffFlags("--find-renames"), "--submodule=short", base)
}

// Worktree returns additions/deletions against the merge-base with baseRef
// (= committed + staged + unstaged + untracked since the branch diverged) and
// dirty state from `git status --porcelain`. A baseRef that cannot be resolved
// silently falls back to HEAD (uncommitted-only diff).
//
// The counts come from the same collection WorktreePatch lists file by file,
// so the session list and the diff viewer report the same numbers. Do not
// reintroduce a second flag set here — that divergence is what once made the
// two surfaces report +2252/-1971 and +8058/-7777 for the same worktree.
func (r Runner) Worktree(path, baseRef string) (Stat, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Stat{}, fmt.Errorf("worktree path is empty")
	}
	// The collection reads the worktree directly, so a Cwd-relative path has to
	// be resolved the same way WorktreePatch resolves it — git would take it
	// from Cwd while os.Lstat would take it from the process working directory.
	path, err := r.resolveWorktreePath(path)
	if err != nil {
		return Stat{}, err
	}

	stat, diffErr := r.worktreeStat(path, r.diffBase(path, baseRef))
	statusOut, statusErr := r.git("-C", path, "status", "--porcelain", "--untracked-files=normal")
	if err := cmp.Or(diffErr, statusErr); err != nil {
		return Stat{}, err
	}

	stat.Dirty = strings.TrimSpace(string(statusOut)) != ""
	return stat, nil
}

func (r Runner) worktreeStat(path, base string) (Stat, error) {
	files, err := r.collectPatchFiles(path, base)
	if err != nil {
		return Stat{}, err
	}
	var stat Stat
	for _, file := range files {
		counted, countErr := r.countedFile(path, base, file)
		if countErr != nil {
			return Stat{}, countErr
		}
		stat.Additions += counted.Additions
		stat.Deletions += counted.Deletions
	}
	return stat, nil
}

// countedFile resolves the lines one collected file contributes. The numstat
// pass already answers that for every shape but a replacement, which arrives as
// a delete half and an add half while both surfaces report the delta between
// them — and reports nothing at all when it is binary, oversized, or unchanged.
func (r Runner) countedFile(path, mergeBase string, file patchFile) (FileStat, error) {
	if file.replacement == nil {
		return file.FileStat, nil
	}
	if file.Binary || file.replacement.Binary ||
		file.OmittedReason == "tooLarge" || file.replacement.OmittedReason == "tooLarge" {
		return FileStat{}, nil
	}
	tooLarge, err := r.patchFileTooLarge(path, mergeBase, file)
	if err != nil || tooLarge {
		return FileStat{}, err
	}
	_, stat, changed, err := r.replacementPatch(path, mergeBase, file)
	if err != nil || !changed {
		return FileStat{}, err
	}
	return stat, nil
}

// collectPatchFiles enumerates every changed file against mergeBase: tracked
// files from one numstat pass, then untracked files one by one. A path that is
// both tracked and untracked is a replacement, kept as a single entry.
func (r Runner) collectPatchFiles(path, mergeBase string) ([]patchFile, error) {
	tracked, err := r.numStat(path, mergeBase)
	if err != nil {
		return nil, err
	}

	files := make([]patchFile, 0, len(tracked))
	trackedByPath := make(map[string]int, len(tracked))
	for _, stat := range tracked {
		// Keyed by the final path only. An untracked file sitting at a rename
		// source is a separate addition, not the same file coming back.
		trackedByPath[stat.Path] = len(files)
		files = append(files, patchFile{FileStat: stat, tracked: true})
	}

	files, err = r.mergeUntracked(path, files, trackedByPath)
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// mergeUntracked appends the untracked files, folding a path that is tracked as
// well into that entry as a replacement instead of listing it twice.
func (r Runner) mergeUntracked(
	path string,
	files []patchFile,
	trackedByPath map[string]int,
) ([]patchFile, error) {
	untracked, err := r.untrackedPaths(path)
	if err != nil {
		return nil, err
	}
	if len(untracked) == 0 {
		r.UntrackedCache.replace(path, nil)
		return files, nil
	}
	if err = r.rejectOverFileLimit(files, untracked, trackedByPath); err != nil {
		return nil, err
	}
	stats, measured, err := r.measureUntracked(path, untracked)
	if err != nil {
		return nil, err
	}
	r.UntrackedCache.replace(path, measured)
	return mergePatchFiles(files, untracked, trackedByPath, stats), nil
}

// mergePatchFiles folds each measured untracked file into the list: a path that
// is tracked as well becomes that entry's replacement instead of a second row.
func mergePatchFiles(
	files []patchFile,
	untracked []string,
	trackedByPath map[string]int,
	stats []FileStat,
) []patchFile {
	for i, rel := range untracked {
		if index, ok := trackedByPath[rel]; ok {
			files[index].replacement = &stats[i]
			continue
		}
		files = append(files, patchFile{FileStat: stats[i]})
	}
	return files
}

// rejectOverFileLimit fails before anything is measured when the result cannot
// fit under MaxFiles. Counting one untracked file costs a git process, so a
// request far over the limit must not pay for every one of them only to be
// rejected at the end. A path that is both tracked and untracked folds into a
// single entry and can even drop out entirely, so this rejects only when the
// smallest possible result is still over.
func (r Runner) rejectOverFileLimit(
	files []patchFile,
	untracked []string,
	trackedByPath map[string]int,
) error {
	if r.MaxFiles <= 0 {
		return nil
	}
	added, replacements := 0, 0
	for _, rel := range untracked {
		if _, ok := trackedByPath[rel]; ok {
			replacements++
			continue
		}
		added++
	}
	least := len(files) + added - replacements
	if least <= r.MaxFiles {
		return nil
	}
	return fmt.Errorf("worktree patch contains %d files; limit is %d", least, r.MaxFiles)
}

// measureUntracked counts each untracked file, returning the stats in the order
// given plus the entries this pass may memoize (a file that changed while git
// was measuring it yields no key and is left out).
func (r Runner) measureUntracked(
	path string,
	untracked []string,
) ([]FileStat, map[string]untrackedStat, error) {
	stats := make([]FileStat, 0, len(untracked))
	measured := make(map[string]untrackedStat, len(untracked))
	for _, rel := range untracked {
		entry, key, err := r.untrackedFileStat(path, rel)
		if err != nil {
			return nil, nil, err
		}
		if key != "" {
			measured[key] = entry
		}
		stats = append(stats, entry.stat)
	}
	return stats, measured, nil
}

// numStat reads per-file counts with rename detection on.
//
// A rename whose two paths nest (`a` -> `a/b`) cannot be scoped by an exact
// two-path pathspec: the ancestor's descendant-excluding pathspec swallows the
// other side, and the patch degenerates to a bare deletion that contradicts the
// file list. Retry the whole collection without rename detection so both
// surfaces degrade together, at the cost of counting that worktree's renames as
// a delete plus an add.
func (r Runner) numStat(path, base string) ([]FileStat, error) {
	stats, err := r.numStatWith(path, base, "--find-renames")
	if err != nil || !hasNestedRename(stats) {
		return stats, err
	}
	return r.numStatWith(path, base, "--no-renames")
}

func (r Runner) numStatWith(path, base, renames string) ([]FileStat, error) {
	out, err := r.git(diffCmd(path, numStatArgs(base, renames)...)...)
	if err != nil {
		return nil, err
	}
	stats, err := parseNumStat(out)
	if err != nil {
		return nil, fmt.Errorf("parse tracked numstat: %w", err)
	}
	return stats, nil
}

func hasNestedRename(stats []FileStat) bool {
	for _, stat := range stats {
		if stat.OldPath == "" {
			continue
		}
		if strings.HasPrefix(stat.Path, stat.OldPath+"/") ||
			strings.HasPrefix(stat.OldPath, stat.Path+"/") {
			return true
		}
	}
	return false
}

// untrackedPaths lists the untracked files both surfaces measure.
//
// git collapses a nested checkout into a single directory entry with a trailing
// slash ("sub/"). Its contents belong to that other repository, so there is
// nothing here to diff — and treating it as a file would fail the whole
// collection, turning every dashboard row for the worktree into an error on
// every poll. Dropping it here keeps the session list and the diff viewer
// agreeing about what the worktree contains.
func (r Runner) untrackedPaths(path string) ([]string, error) {
	out, err := r.git("-C", path, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	paths, err := parseNULPaths(out)
	if err != nil {
		return nil, fmt.Errorf("parse untracked paths: %w", err)
	}
	files := make([]string, 0, len(paths))
	for _, rel := range paths {
		if strings.HasSuffix(rel, "/") {
			continue
		}
		files = append(files, rel)
	}
	return files, nil
}

// WorktreePatch returns the committed, staged, unstaged, and untracked changes
// since the strict merge-base with baseRef. Binary files and files larger than
// 256 KiB remain in Files but are omitted from Patch.
func (r Runner) WorktreePatch(path, baseRef string) (_ Patch, err error) {
	defer errs.Wrap(&err, "worktree patch %q", path)

	path, err = r.resolveWorktreePath(path)
	if err != nil {
		return Patch{}, err
	}
	mergeBase, err := r.MergeBase(path, baseRef)
	if err != nil {
		return Patch{}, err
	}
	files, err := r.collectPatchFiles(path, mergeBase)
	if err != nil {
		return Patch{}, err
	}

	result := Patch{
		MergeBase: mergeBase,
		Files:     make([]FileStat, 0, len(files)),
	}
	appendFile := func(stat FileStat) error {
		result.Files = append(result.Files, stat)
		if r.MaxFiles > 0 && len(result.Files) > r.MaxFiles {
			return fmt.Errorf("worktree patch contains %d files; limit is %d", len(result.Files), r.MaxFiles)
		}
		return nil
	}
	var patch strings.Builder
	collectionFull := false
	for _, file := range files {
		stat := file.FileStat
		if file.replacement != nil {
			stat.Additions = 0
			stat.Deletions = 0
			switch {
			case stat.OmittedReason == "tooLarge" ||
				file.replacement.OmittedReason == "tooLarge":
				stat.Binary = false
				stat.OmittedReason = "tooLarge"
			case stat.Binary || file.replacement.Binary:
				stat.Binary = true
				stat.OmittedReason = "binary"
			default:
				stat.OmittedReason = ""
			}
		}
		if stat.OmittedReason == "" ||
			(file.tracked && stat.OmittedReason == "binary") {
			tooLarge, sizeErr := r.patchFileTooLarge(path, mergeBase, file)
			if sizeErr != nil {
				return Patch{}, sizeErr
			}
			if tooLarge {
				// Counts stay: git already measured them, and only the patch
				// text is over budget. Zeroing them would put the viewer's
				// total back out of step with the session list.
				stat.Binary = false
				stat.OmittedReason = "tooLarge"
			}
		}
		replacementChecked := false
		if file.replacement != nil && stat.OmittedReason == "tooLarge" {
			changed, replacementErr := r.replacementChanged(path, mergeBase, file, true)
			if replacementErr != nil {
				return Patch{}, replacementErr
			}
			if !changed {
				continue
			}
			replacementChecked = true
		}
		if collectionFull {
			if file.replacement != nil && !replacementChecked {
				changed, replacementErr := r.replacementChanged(path, mergeBase, file, false)
				if replacementErr != nil {
					return Patch{}, replacementErr
				}
				if !changed {
					continue
				}
			}
			if stat.OmittedReason == "" {
				stat.OmittedReason = "collectionLimit"
			}
			if appendErr := appendFile(stat); appendErr != nil {
				return Patch{}, appendErr
			}
			continue
		}
		if file.replacement != nil && stat.OmittedReason == "binary" {
			_, _, changed, replacementErr := r.replacementPatch(path, mergeBase, file)
			if replacementErr != nil {
				return Patch{}, replacementErr
			}
			if !changed {
				continue
			}
		}
		if stat.OmittedReason == "" {
			var out []byte
			switch {
			case file.replacement != nil:
				var replacementStat FileStat
				var changed bool
				out, replacementStat, changed, err = r.replacementPatch(path, mergeBase, file)
				if err != nil {
					return Patch{}, err
				}
				if !changed {
					continue
				}
				stat.Additions = replacementStat.Additions
				stat.Deletions = replacementStat.Deletions
				stat.Binary = replacementStat.Binary
				stat.OmittedReason = replacementStat.OmittedReason
			case file.tracked:
				out, err = r.trackedPathPatch(path, mergeBase, stat)
			default:
				var code int
				out, code, err = r.gitExitCode(diffCmd(path,
					"--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
					"--no-index", "--", "/dev/null", stat.Path,
				)...)
				if code == 1 {
					err = nil
				}
			}
			if err != nil {
				return Patch{}, err
			}
			if stat.OmittedReason == "" {
				if r.MaxPatchBytes > 0 && len(out) > r.MaxPatchBytes-patch.Len() {
					collectionFull = true
					stat.OmittedReason = "collectionLimit"
				} else {
					patch.Write(out)
					stat.PatchIncluded = true
				}
			}
		}
		if appendErr := appendFile(stat); appendErr != nil {
			return Patch{}, appendErr
		}
	}
	result.Patch = patch.String()
	return result, nil
}

// WorktreeHead resolves the commit a worktree is on. An unborn branch has no
// commit yet, which is not an error: "" says there is no commit to compare
// against, and the caller decides what that means.
func (r Runner) WorktreeHead(path string) (string, error) {
	out, code, err := r.gitExitCode("-C", path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func (r Runner) resolveWorktreePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("worktree path is empty")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}

	base := r.Cwd
	if base == "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve worktree path %q: %w", path, err)
		}
		return absolute, nil
	}
	if !filepath.IsAbs(base) {
		absolute, err := filepath.Abs(base)
		if err != nil {
			return "", fmt.Errorf("resolve runner cwd %q: %w", base, err)
		}
		base = absolute
	}
	return filepath.Clean(filepath.Join(base, path)), nil
}

type patchFile struct {
	FileStat
	tracked     bool
	replacement *FileStat
}

// untrackedFileStat measures one untracked file and returns the cache key its
// counts belong to. An empty key means "do not memoize this": the file is
// oversized, or it changed while git was measuring it.
func (r Runner) untrackedFileStat(path, rel string) (_ untrackedStat, _ string, err error) {
	defer errs.Wrap(&err, "untracked file %q", rel)

	info, err := untrackedFileInfo(path, rel)
	if err != nil {
		return untrackedStat{}, "", err
	}
	if info.Size() > patchFileLimit {
		return untrackedStat{stat: FileStat{Path: rel, OmittedReason: "tooLarge"}}, "", nil
	}
	key, err := untrackedCacheKey(path, rel, info)
	if err != nil {
		return untrackedStat{}, "", err
	}
	if cached, ok := r.UntrackedCache.lookup(path, key); ok {
		return cached, key, nil
	}
	stat, err := r.addedFileStat(path, rel)
	if err != nil {
		return untrackedStat{}, "", err
	}
	// git re-read the file after we hashed it. If a writer got in between, the
	// counts describe different bytes than the key names — return them for this
	// pass but do not let them answer for that content later.
	if after, afterErr := untrackedFileKeyNow(path, rel); afterErr != nil || after != key {
		return untrackedStat{stat: stat}, "", nil
	}
	return untrackedStat{stat: stat}, key, nil
}

func untrackedFileKeyNow(path, rel string) (string, error) {
	info, err := untrackedFileInfo(path, rel)
	if err != nil {
		return "", err
	}
	return untrackedCacheKey(path, rel, info)
}

// untrackedFileInfo rejects anything that is not a regular file or a symlink —
// the only shapes git will diff against /dev/null.
func untrackedFileInfo(path, rel string) (os.FileInfo, error) {
	fullPath, err := containedPath(path, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return nil, errors.New("not a regular file or symlink")
	}
	return info, nil
}

// addedFileStat counts rel as a whole-file addition against /dev/null.
func (r Runner) addedFileStat(path, rel string) (FileStat, error) {
	out, code, err := r.gitExitCode(diffCmd(path,
		"--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"--no-index", "--numstat", "-z", "--", "/dev/null", rel,
	)...)
	if code == 1 {
		err = nil
	}
	if err != nil {
		return FileStat{}, err
	}
	stats, err := parseNumStat(out)
	if err != nil {
		return FileStat{}, fmt.Errorf("parse numstat: %w", err)
	}
	if len(stats) != 1 {
		return FileStat{}, fmt.Errorf("parse numstat: got %d files, want 1", len(stats))
	}
	stat := stats[0]
	stat.Path = rel
	// --no-index names both sides, so git reports the pair in the rename record
	// shape with /dev/null as the old path. This file is an addition, not a move.
	stat.OldPath = ""
	if stat.Binary {
		stat.OmittedReason = "binary"
	}
	return stat, nil
}

func (r Runner) patchFileTooLarge(path, mergeBase string, file patchFile) (_ bool, err error) {
	if !file.tracked {
		return file.OmittedReason == "tooLarge", nil
	}
	defer errs.Wrap(&err, "patch file size %q", file.Path)

	tooLarge, err := r.finalSideTooLarge(path, file.Path)
	if err != nil || tooLarge {
		return tooLarge, err
	}
	// A rename's merge-base blob lives at the old path; asking for the new one
	// returns an empty tree listing and the size check silently passes.
	return r.baseBlobTooLarge(path, mergeBase, cmp.Or(file.OldPath, file.Path))
}

// finalSideTooLarge measures the worktree (or hidden index) side of a tracked
// path. A path absent from the index has no final side to measure.
func (r Runner) finalSideTooLarge(path, rel string) (bool, error) {
	inIndex, err := r.pathInIndex(path, rel)
	if err != nil {
		return false, fmt.Errorf("inspect tracked index entry: %w", err)
	}
	if !inIndex {
		return false, nil
	}
	hidden, tooLarge, err := r.hiddenIndexFileTooLarge(path, rel)
	if err != nil {
		return false, fmt.Errorf("inspect hidden index entry: %w", err)
	}
	if hidden {
		return tooLarge, nil
	}
	info, err := lstatContained(path, rel)
	if err != nil {
		return false, fmt.Errorf("inspect tracked file: %w", err)
	}
	return info != nil &&
		(info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) &&
		info.Size() > patchFileLimit, nil
}

// baseBlobTooLarge measures the merge-base side of rel. A path that did not
// exist at the merge-base lists nothing and is not oversized.
func (r Runner) baseBlobTooLarge(path, mergeBase, rel string) (bool, error) {
	out, err := r.git("-C", path, "ls-tree", "-l", "-z", mergeBase, "--", rel)
	if err != nil {
		return false, err
	}
	if len(out) == 0 {
		return false, nil
	}
	metadata, _, found := bytes.Cut(out, []byte{'\t'})
	if !found {
		return false, errors.New("parse base file size: missing path separator")
	}
	fields := strings.Fields(string(metadata))
	if len(fields) != 4 {
		return false, errors.New("parse base file size: malformed ls-tree output")
	}
	if fields[3] == "-" {
		return false, nil
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return false, fmt.Errorf("parse base file size: %w", err)
	}
	return size > patchFileLimit, nil
}

type treeEntry struct {
	mode       string
	objectType string
	oid        string
}

func (r Runner) replacementPatch(
	path, mergeBase string,
	file patchFile,
) (_ []byte, _ FileStat, _ bool, err error) {
	defer errs.Wrap(&err, "replacement patch %q", file.Path)

	entry, err := r.mergeBaseTreeEntry(path, mergeBase, file.Path)
	if err != nil {
		return nil, FileStat{}, false, err
	}
	info, err := lstatContained(path, file.Path)
	if err != nil {
		return nil, FileStat{}, false, err
	}
	if info == nil {
		return nil, FileStat{}, false, errors.New("inspect: missing final side")
	}
	currentMode, err := worktreeGitMode(info)
	if err != nil {
		return nil, FileStat{}, false, fmt.Errorf("inspect: %w", err)
	}

	if entry.mode[:2] != currentMode[:2] {
		return r.modeClassReplacementPatch(path, mergeBase, file)
	}
	return r.contentReplacementPatch(path, file, entry)
}

// modeClassReplacementPatch handles a path whose object class changed (regular
// file <-> symlink). git cannot express that as one diff, so the patch is the
// base-side deletion followed by the final-side addition, and the counts are
// the sum of both halves.
func (r Runner) modeClassReplacementPatch(
	path, mergeBase string,
	file patchFile,
) ([]byte, FileStat, bool, error) {
	deleted, err := r.trackedPathPatch(path, mergeBase, file.FileStat)
	if err != nil {
		return nil, FileStat{}, false, err
	}
	added, code, err := r.gitExitCode(diffCmd(path,
		"--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"--no-index", "--", "/dev/null", file.Path,
	)...)
	if code == 1 {
		err = nil
	}
	if err != nil {
		return nil, FileStat{}, false, err
	}
	stat := FileStat{
		Path:      file.Path,
		Additions: file.Additions + file.replacement.Additions,
		Deletions: file.Deletions + file.replacement.Deletions,
	}
	return append(deleted, added...), stat, true, nil
}

// contentReplacementPatch compares a same-class replacement by materializing
// the merge-base blob outside the worktree and diffing the pair with
// --no-index. It reports changed=false when the two sides are identical.
func (r Runner) contentReplacementPatch(
	path string,
	file patchFile,
	entry treeEntry,
) (_ []byte, _ FileStat, _ bool, err error) {
	tempDir, err := replacementTempDir(path)
	if err != nil {
		return nil, FileStat{}, false, fmt.Errorf("create temp directory: %w", err)
	}
	defer func() {
		// The request-private base file is best-effort cleanup after every return path.
		_ = os.RemoveAll(tempDir)
	}()

	baseArg, finalArg, err := r.materializeReplacementPair(path, tempDir, file.Path, entry)
	if err != nil {
		return nil, FileStat{}, false, err
	}

	out, changed, err := r.replacementDiffPatch(tempDir, baseArg, finalArg)
	if err != nil {
		return nil, FileStat{}, false, err
	}
	if !changed {
		return nil, FileStat{}, false, nil
	}

	stat, err := r.replacementNumStat(tempDir, file.Path, baseArg, finalArg)
	if err != nil {
		return nil, FileStat{}, false, err
	}
	return out, stat, true, nil
}

// materializeReplacementPair writes the merge-base blob under tempDir/a and
// links the worktree in as tempDir/b, returning the two --no-index arguments.
func (r Runner) materializeReplacementPair(
	path, tempDir, rel string,
	entry treeEntry,
) (string, string, error) {
	relPath := filepath.FromSlash(rel)
	baseArg := filepath.Join("a", relPath)
	finalArg := filepath.Join("b", relPath)
	basePath := filepath.Join(tempDir, baseArg)
	if err := os.MkdirAll(filepath.Dir(basePath), 0o700); err != nil {
		return "", "", fmt.Errorf("create base directory: %w", err)
	}
	if err := r.materializeTreeEntry(path, entry, basePath); err != nil {
		return "", "", err
	}
	if err := os.Symlink(path, filepath.Join(tempDir, "b")); err != nil {
		return "", "", fmt.Errorf("link final side: %w", err)
	}
	return baseArg, finalArg, nil
}

// replacementDiffPatch runs the pair diff and reports whether the two sides
// differ at all — --no-index exits 1 exactly when they do.
func (r Runner) replacementDiffPatch(tempDir, baseArg, finalArg string) ([]byte, bool, error) {
	out, code, err := replacementDiff(r.context(), tempDir, false, baseArg, finalArg)
	changed := code == 1
	if changed {
		err = nil
	}
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

// replacementNumStat reads the counts for an already-materialized pair.
func (r Runner) replacementNumStat(tempDir, rel, baseArg, finalArg string) (FileStat, error) {
	out, code, err := replacementDiff(r.context(), tempDir, true, baseArg, finalArg)
	if code == 1 {
		err = nil
	}
	if err != nil {
		return FileStat{}, err
	}
	stats, err := parseNumStat(out)
	if err != nil {
		return FileStat{}, fmt.Errorf("parse numstat: %w", err)
	}
	if len(stats) != 1 {
		return FileStat{}, fmt.Errorf("parse numstat: got %d files, want 1", len(stats))
	}
	stat := stats[0]
	stat.Path = rel
	// Same --no-index two-path record shape as addedFileStat: the "old path" is
	// the materialized base copy, not a rename source.
	stat.OldPath = ""
	return stat, nil
}

func (r Runner) replacementChanged(
	path, mergeBase string,
	file patchFile,
	hashFinal bool,
) (_ bool, err error) {
	defer errs.Wrap(&err, "replacement %q", file.Path)

	entry, err := r.mergeBaseTreeEntry(path, mergeBase, file.Path)
	if err != nil {
		return false, err
	}
	fullPath, err := containedPath(path, file.Path)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		return false, fmt.Errorf("inspect: %w", err)
	}
	currentMode, err := worktreeGitMode(info)
	if err != nil {
		return false, fmt.Errorf("inspect: %w", err)
	}
	if entry.mode != currentMode {
		return true, nil
	}
	if entry.objectType != "blob" {
		return false, fmt.Errorf("compare base entry: unsupported object type %q", entry.objectType)
	}
	if hashFinal {
		out, hashErr := r.git("-C", path, "hash-object", "--no-filters", "--", file.Path)
		if hashErr != nil {
			return false, fmt.Errorf("hash oversized final side: %w", hashErr)
		}
		return strings.TrimSpace(string(out)) != entry.oid, nil
	}

	baseContent, err := r.git("-C", path, "cat-file", "blob", entry.oid)
	if err != nil {
		return false, err
	}
	var finalContent []byte
	if currentMode == "120000" {
		target, readErr := os.Readlink(fullPath)
		if readErr != nil {
			return false, fmt.Errorf("read symlink: %w", readErr)
		}
		finalContent = []byte(target)
	} else {
		finalContent, err = os.ReadFile(fullPath)
		if err != nil {
			return false, fmt.Errorf("read file: %w", err)
		}
	}
	return !bytes.Equal(baseContent, finalContent), nil
}

// trackedPathPatch renders one tracked file. A rename needs both of its paths
// in the pathspec, otherwise git sees only the final side and emits a bare
// addition instead of the rename the file list already promised.
func (r Runner) trackedPathPatch(path, mergeBase string, stat FileStat) ([]byte, error) {
	paths := []string{stat.Path}
	if stat.OldPath != "" {
		paths = append(paths, stat.OldPath)
	}
	return r.gitExactPaths(paths, diffCmd(path, patchArgs(mergeBase)...)...)
}

func replacementDiff(ctx context.Context, cwd string, numstat bool, oldPath, newPath string) ([]byte, int, error) {
	args := append(gitPinArgs(),
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"--no-index", "--src-prefix=", "--dst-prefix=",
	)
	if numstat {
		args = append(args, "--numstat", "-z")
	}
	args = append(args, "--", oldPath, newPath)
	return execx.OutputExitCodeContext(ctx, cwd, gitEnv(), "git", args...)
}

// mergeBaseTreeEntry omits rel from its messages: every caller already wraps
// the identity in, and repeating it reads as two different files.
func (r Runner) mergeBaseTreeEntry(path, mergeBase, rel string) (treeEntry, error) {
	out, err := r.git("-C", path, "ls-tree", "-z", mergeBase, "--", rel)
	if err != nil {
		return treeEntry{}, err
	}
	records, err := splitNUL(out)
	if err != nil {
		return treeEntry{}, fmt.Errorf("parse base entry: %w", err)
	}
	if len(records) != 1 {
		return treeEntry{}, fmt.Errorf("parse base entry: got %d entries, want 1", len(records))
	}
	metadata, entryPath, found := bytes.Cut(records[0], []byte{'\t'})
	if !found || string(entryPath) != rel {
		return treeEntry{}, errors.New("parse base entry: path mismatch")
	}
	fields := strings.Fields(string(metadata))
	if len(fields) != 3 || len(fields[0]) != 6 {
		return treeEntry{}, errors.New("parse base entry: malformed ls-tree output")
	}
	return treeEntry{
		mode:       fields[0],
		objectType: fields[1],
		oid:        fields[2],
	}, nil
}

func (r Runner) materializeTreeEntry(path string, entry treeEntry, target string) error {
	if entry.objectType != "blob" {
		return fmt.Errorf("materialize base entry: unsupported object type %q", entry.objectType)
	}
	content, err := r.git("-C", path, "cat-file", "blob", entry.oid)
	if err != nil {
		return err
	}
	if entry.mode == "120000" {
		if err := os.Symlink(string(content), target); err != nil {
			return fmt.Errorf("materialize base symlink: %w", err)
		}
		return nil
	}
	mode := os.FileMode(0o600)
	if entry.mode == "100755" {
		mode = 0o700
	}
	if err := os.WriteFile(target, content, mode); err != nil {
		return fmt.Errorf("materialize base file: %w", err)
	}
	if err := os.Chmod(target, mode); err != nil {
		return fmt.Errorf("set materialized base mode: %w", err)
	}
	return nil
}

func worktreeGitMode(info os.FileInfo) (string, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		return "120000", nil
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("unsupported final-side mode %s", info.Mode())
	}
	if info.Mode().Perm()&0o111 != 0 {
		return "100755", nil
	}
	return "100644", nil
}

func (r Runner) pathInIndex(path, rel string) (bool, error) {
	out, err := r.gitExactPath(rel, "-C", path, "ls-files", "--stage", "-z")
	if err != nil {
		return false, err
	}
	return len(out) > 0, nil
}

func (r Runner) hiddenIndexFileTooLarge(path, rel string) (bool, bool, error) {
	out, err := r.gitExactPath(rel, "-C", path, "ls-files", "-v", "--stage", "-z")
	if err != nil {
		return false, false, err
	}
	records, err := splitNUL(out)
	if err != nil {
		return false, false, fmt.Errorf("parse index entry: %w", err)
	}
	if len(records) == 0 {
		return false, false, nil
	}
	if len(records) != 1 || len(records[0]) < 3 || records[0][1] != ' ' {
		return false, false, fmt.Errorf("parse index entry: malformed output")
	}
	switch records[0][0] {
	case 'S', 's', 'h':
	default:
		return false, false, nil
	}

	metadata, entryPath, found := bytes.Cut(records[0][2:], []byte{'\t'})
	fields := strings.Fields(string(metadata))
	if !found || string(entryPath) != rel || len(fields) != 3 || fields[2] != "0" {
		return false, false, fmt.Errorf("parse index entry: malformed stage 0 output")
	}
	switch fields[0] {
	case "100644", "100755", "120000":
	case "160000":
		return true, false, nil
	default:
		return false, false, fmt.Errorf("unsupported hidden index mode %q", fields[0])
	}
	out, err = r.git("-C", path, "cat-file", "-s", fields[1])
	if err != nil {
		return false, false, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return false, false, fmt.Errorf("parse hidden index size: %w", err)
	}
	return true, size > patchFileLimit, nil
}

func replacementTempDir(worktree string) (string, error) {
	worktreeRoot, err := filepath.Abs(worktree)
	if err != nil {
		return "", fmt.Errorf("resolve worktree: %w", err)
	}
	worktreeRoot, err = filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		return "", fmt.Errorf("resolve worktree symlinks: %w", err)
	}

	tempRoot := filepath.Dir(worktreeRoot)
	inside, err := pathWithin(worktreeRoot, tempRoot)
	if err != nil {
		return "", fmt.Errorf("compare replacement temporary root: %w", err)
	}
	if inside {
		return "", fmt.Errorf("replacement temporary root %q is inside worktree", tempRoot)
	}
	tempDir, err := os.MkdirTemp(tempRoot, "fanout-gitstat-")
	if err != nil {
		return "", err
	}
	tempPath, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("resolve replacement temporary directory: %w", err)
	}
	inside, err = pathWithin(worktreeRoot, tempPath)
	if err != nil || inside {
		_ = os.RemoveAll(tempDir)
		if err != nil {
			return "", fmt.Errorf("validate replacement temporary directory: %w", err)
		}
		return "", fmt.Errorf("replacement temporary directory %q is inside worktree", tempPath)
	}
	return tempPath, nil
}

func pathWithin(root, path string) (bool, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	return rel == "." ||
		(rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
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

func lstatContained(root, rel string) (os.FileInfo, error) {
	fullPath, err := containedPath(root, rel)
	if err != nil {
		return nil, err
	}
	relPath, err := filepath.Rel(root, fullPath)
	if err != nil {
		return nil, err
	}

	current := root
	parts := strings.Split(relPath, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if os.IsNotExist(statErr) || errors.Is(statErr, syscall.ENOTDIR) {
				return nil, nil
			}
			return nil, statErr
		}
		if i < len(parts)-1 && info.Mode()&os.ModeSymlink != 0 {
			return nil, nil
		}
		if i == len(parts)-1 {
			return info, nil
		}
	}
	return nil, nil
}

func parseNumStat(out []byte) ([]FileStat, error) {
	records, err := splitNUL(out)
	if err != nil {
		return nil, err
	}
	stats := make([]FileStat, 0, len(records))
	for i := 0; i < len(records); {
		stat, consumed, recordErr := parseNumStatRecord(records[i:])
		if recordErr != nil {
			return nil, recordErr
		}
		stats = append(stats, stat)
		i += consumed
	}
	return stats, nil
}

// parseNumStatRecord reads one -z numstat entry from the head of records. A
// rename spends three records — the counts with an empty path field, then the
// old and the new path — so the caller advances by the returned count.
func parseNumStatRecord(records [][]byte) (FileStat, int, error) {
	fields := bytes.SplitN(records[0], []byte{'\t'}, 3)
	if len(fields) != 3 {
		return FileStat{}, 0, fmt.Errorf("malformed numstat record")
	}
	oldPath, path, consumed, err := numStatRecordPaths(records, fields[2])
	if err != nil {
		return FileStat{}, 0, err
	}
	additions, deletions, binary, err := parseNumStatCounts(fields[0], fields[1])
	if err != nil {
		return FileStat{}, 0, err
	}
	stat := FileStat{
		Path:      path,
		OldPath:   oldPath,
		Additions: additions,
		Deletions: deletions,
		Binary:    binary,
	}
	if binary {
		stat.OmittedReason = "binary"
	}
	return stat, consumed, nil
}

// numStatRecordPaths resolves the paths one numstat entry names and how many
// records it spends. A rename leaves the path field empty and follows with the
// old and the new path as two further records.
func numStatRecordPaths(records [][]byte, pathField []byte) (string, string, int, error) {
	if len(pathField) > 0 {
		return "", string(pathField), 1, nil
	}
	if len(records) < 3 {
		return "", "", 0, fmt.Errorf("malformed rename numstat record")
	}
	// Worktree paths are the adversarial-input boundary; a half-empty pair would
	// silently key a file by "".
	if len(records[1]) == 0 || len(records[2]) == 0 {
		return "", "", 0, fmt.Errorf("empty rename numstat path")
	}
	return string(records[1]), string(records[2]), 3, nil
}

// parseNumStatCounts reads the two count columns. git writes "-" in both for a
// binary file, so a half-marked pair means the output is not what we parsed.
func parseNumStatCounts(addField, deleteField []byte) (int, int, bool, error) {
	additions, addBinary, err := parseNumStatCount(addField)
	if err != nil {
		return 0, 0, false, err
	}
	deletions, deleteBinary, err := parseNumStatCount(deleteField)
	if err != nil {
		return 0, 0, false, err
	}
	if addBinary != deleteBinary {
		return 0, 0, false, fmt.Errorf("numstat binary markers do not match")
	}
	return additions, deletions, addBinary, nil
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
	if path == "" {
		return "", fmt.Errorf("worktree path is empty")
	}

	baseRef = strings.TrimSpace(baseRef)
	var candidates []string
	// checkRef is the check-ref-format argv tail for the one user-supplied form
	// baseRef takes. Collecting it lets the three branches share a single
	// validation check instead of repeating the same error three times.
	var checkRef []string
	switch {
	case strings.HasPrefix(baseRef, "refs/"):
		checkRef = []string{baseRef}
		candidates = append(candidates, baseRef)
	case strings.HasPrefix(baseRef, "origin/"):
		candidate := "refs/remotes/" + baseRef
		checkRef = []string{candidate}
		candidates = append(candidates, candidate)
	case baseRef != "":
		checkRef = []string{"--branch", baseRef}
		candidates = append(candidates, "refs/heads/"+baseRef, "refs/remotes/origin/"+baseRef)
	default:
		out, err := r.git("-C", path, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
		if err != nil {
			return "", fmt.Errorf("resolve origin/HEAD: %w", err)
		}
		head := strings.TrimSpace(string(out))
		if head == "" {
			return "", errors.New("resolve origin/HEAD: empty ref")
		}
		candidates = append(candidates, head)
	}
	if checkRef != nil {
		args := append([]string{"-C", path, "check-ref-format"}, checkRef...)
		if _, err := r.git(args...); err != nil {
			return "", fmt.Errorf("validate base ref %q: %w", baseRef, err)
		}
	}

	currentRef, err := r.currentBranchRef(path)
	if err != nil {
		return "", err
	}

	var lastErr error
	for _, candidate := range candidates {
		if currentRef != "" {
			isCurrent, currentErr := r.refIsCurrentBranch(path, candidate, currentRef)
			if currentErr != nil {
				return "", currentErr
			}
			if isCurrent {
				return "", fmt.Errorf("resolve merge-base for %q: base ref %q is the current branch", baseRef, candidate)
			}
		}

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

func (r Runner) currentBranchRef(path string) (string, error) {
	out, code, err := r.gitExitCode("-C", path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		if code == 1 {
			return "", nil
		}
		return "", fmt.Errorf("resolve current branch: %w", err)
	}
	currentRef := strings.TrimSpace(string(out))
	if !strings.HasPrefix(currentRef, "refs/heads/") {
		return "", fmt.Errorf("resolve current branch: unexpected ref %q", currentRef)
	}
	return currentRef, nil
}

func (r Runner) refIsCurrentBranch(path, candidate, currentRef string) (bool, error) {
	if sameBranchRef(candidate, currentRef) {
		return true, nil
	}

	out, code, err := r.gitExitCode("-C", path, "symbolic-ref", "--quiet", candidate)
	if err != nil {
		if code == 1 {
			return false, nil
		}
		return false, fmt.Errorf("resolve base symbolic ref %q: %w", candidate, err)
	}
	return sameBranchRef(strings.TrimSpace(string(out)), currentRef), nil
}

func sameBranchRef(candidate, currentRef string) bool {
	if candidate == currentRef {
		return true
	}
	currentBranch, ok := strings.CutPrefix(currentRef, "refs/heads/")
	if !ok {
		return false
	}
	remoteRef, ok := strings.CutPrefix(candidate, "refs/remotes/")
	if !ok {
		return false
	}
	_, remoteBranch, ok := strings.Cut(remoteRef, "/")
	return ok && remoteBranch == currentBranch
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
	return execx.OutputContext(r.context(), r.Cwd, gitEnv(), "git", args...)
}

func (r Runner) gitExitCode(args ...string) ([]byte, int, error) {
	return execx.OutputExitCodeContext(r.context(), r.Cwd, gitEnv(), "git", args...)
}

func (r Runner) gitExactPath(path string, args ...string) ([]byte, error) {
	return r.gitExactPaths([]string{path}, args...)
}

// gitExactPaths scopes args to exactly the named paths: each one is matched
// literally and its descendants are excluded, so a directory sharing a file's
// name cannot widen the result.
func (r Runner) gitExactPaths(paths []string, args ...string) ([]byte, error) {
	args = append(args, "--")
	for _, path := range paths {
		args = append(args, ":(top,literal)"+path)
	}
	for _, path := range paths {
		args = append(args, ":(top,exclude,glob)"+escapePathspecGlob(path)+"/**")
	}
	env := append(gitEnv(), "GIT_LITERAL_PATHSPECS=0")
	return execx.OutputContext(r.context(), r.Cwd, env, "git", args...)
}

func (r Runner) context() context.Context {
	if r.Context != nil {
		return r.Context
	}
	return context.Background()
}

func escapePathspecGlob(path string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"*", "\\*",
		"?", "\\?",
		"[", "\\[",
		"]", "\\]",
	)
	return replacer.Replace(path)
}

// gitEnv returns the extra environment entries execx.Output appends to
// os.Environ() for every git invocation.
func gitEnv() []string {
	return []string{"LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_LITERAL_PATHSPECS=1"}
}
