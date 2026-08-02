package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// resolveBase resolves the diff base to a concrete commit. An empty explicit ref
// falls back to origin/main then main (whichever git rev-parse --verify finds
// first); a non-empty ref is verified as-is. The returned value is the
// merge-base of that ref and HEAD, so the diff reflects only what this branch
// added, not commits the base gained since it forked.
func resolveBase(explicit string) (string, error) {
	ref := explicit
	if ref == "" {
		for _, cand := range []string{"origin/main", "main"} {
			if _, err := runGit("rev-parse", "--verify", "--quiet", cand); err == nil {
				ref = cand
				break
			}
		}
		if ref == "" {
			return "", errors.New("no base ref found (tried origin/main, main); pass --base")
		}
	} else if _, err := runGit("rev-parse", "--verify", "--quiet", ref); err != nil {
		return "", fmt.Errorf("base ref %q not found: %w", ref, err)
	}
	mb, err := runGit("merge-base", ref, "HEAD")
	if err != nil {
		return "", fmt.Errorf("merge-base %s HEAD: %w", ref, err)
	}
	return strings.TrimSpace(mb), nil
}

// loadDiff runs the three read-only git diffs (--name-status -z -M,
// --numstat -z -M, and -U0) between base and the working tree and assembles them
// into a Diff: the file list with rename detection, per-file added/deleted
// counts, and the +/- content lines the escalation greps scan. Dropping HEAD
// makes the base the only argument, so the diff spans base..working tree and
// sees uncommitted edits (untracked files stay invisible to git diff). The two
// NUL-delimited summaries preserve every path byte except NUL;
// core.quotepath=off keeps non-ASCII paths literal in the unified diff.
func loadDiff(base string) (Diff, error) {
	nameStatus, err := runGit("-c", "core.quotepath=off", "diff", "--name-status", "-z", "-M", base)
	if err != nil {
		return Diff{}, err
	}
	numstat, err := runGit("-c", "core.quotepath=off", "diff", "--numstat", "-z", "-M", base)
	if err != nil {
		return Diff{}, err
	}
	// -M here too: without rename detection a pure rename dumps the whole file
	// as removed+added lines, and pre-existing t.Skip / invariant references
	// would misfire S3/S10 on a PR that only moved the file.
	unified, err := runGit("-c", "core.quotepath=off", "diff", "-U0", "-M", base)
	if err != nil {
		return Diff{}, err
	}

	files := parseNameStatus(nameStatus)
	counts := parseNumstat(numstat)
	for i := range files {
		if e, ok := counts[files[i].Path]; ok {
			files[i].Added, files[i].Deleted = e.added, e.deleted
		} else if e, ok := counts[files[i].OldPath]; ok {
			files[i].Added, files[i].Deleted = e.added, e.deleted
		}
	}
	added, removed := parseUnified(unified)
	return Diff{Files: files, AddedLines: added, RemovedLines: removed}, nil
}

// runGit runs git with args and returns stdout, wrapping a non-zero exit with
// the trimmed stderr. Every caller is read-only.
func runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// parseNameStatus parses `git diff --name-status -z -M` output. NUL separates
// the status and every path, so tabs and newlines remain part of a path. A/M/D
// carry one path; renames and copies carry old and new paths.
func parseNameStatus(out string) []FileChange {
	var files []FileChange
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			continue
		}
		fc := FileChange{Status: status[0]}
		switch fc.Status {
		case 'R', 'C':
			if i+1 >= len(fields) {
				return files
			}
			fc.OldPath, fc.Path = fields[i], fields[i+1]
			i += 2
		default:
			if i >= len(fields) {
				return files
			}
			fc.Path = fields[i]
			i++
		}
		files = append(files, fc)
	}
	return files
}

// numstatEntry is one parsed `git diff --numstat` row. A binary file reports "-"
// for both columns and is recorded as -1/-1.
type numstatEntry struct {
	added   int
	deleted int
}

// parseNumstat parses `git diff --numstat -z -M` output into a per-path count
// map. Columns are added, deleted, path; a binary file's "-" columns become -1.
// A rename or copy has an empty path column followed by NUL-delimited old and
// new paths, and keys on the new path to match the name-status entry.
func parseNumstat(out string) map[string]numstatEntry {
	counts := make(map[string]numstatEntry)
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); {
		record := fields[i]
		i++
		if record == "" {
			continue
		}
		columns := strings.SplitN(record, "\t", 3)
		if len(columns) < 3 {
			continue
		}
		var e numstatEntry
		if columns[0] == "-" || columns[1] == "-" {
			e.added, e.deleted = -1, -1
		} else {
			e.added, _ = strconv.Atoi(columns[0])
			e.deleted, _ = strconv.Atoi(columns[1])
		}
		path := columns[2]
		if path == "" {
			if i+1 >= len(fields) {
				return counts
			}
			path = fields[i+1]
			i += 2
		}
		counts[path] = e
	}
	return counts
}

// parseUnified extracts the +/- content lines per file from `git diff -U0`
// output as a small state machine. Each `diff --git ` line opens a header and
// resets the current file; while in the header the `--- a/<path>` line records
// the old path and the `+++ b/<path>` line sets the current file; the first
// `@@` hunk line closes the header. Added lines key to the new path and
// removed lines to the old path — a deletion or a rename onto a different
// path (including code renamed to .md) keeps its dropped content attributed
// to the code-side path, so S10 still sees invariant references it removed.
// Only outside the header does a leading +/- mark a content line, so an added
// line that reads `++ x` (raw `+++ x`) or a removed line `-- x` (raw `--- x`)
// is counted as content, not mistaken for a header. A real content line always
// carries a +/- marker, so it can never look like a `diff --git ` line.
func parseUnified(out string) (added, removed map[string][]string) {
	added = make(map[string][]string)
	removed = make(map[string][]string)
	var cur, oldPath string
	inHeader := false
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHeader = true
			cur, oldPath = "", ""
		case inHeader && strings.HasPrefix(line, "--- "):
			if p := parseDiffHeaderPath(line[4:]); p != "/dev/null" {
				oldPath = p
			}
		case inHeader && strings.HasPrefix(line, "+++ "):
			if p := parseDiffHeaderPath(line[4:]); p != "/dev/null" {
				cur = p
			} else {
				cur = "" // deletion: nothing was added under the new path
			}
		case strings.HasPrefix(line, "@@"):
			inHeader = false
		case !inHeader && strings.HasPrefix(line, "+"):
			if cur != "" {
				added[cur] = append(added[cur], line[1:])
			}
		case !inHeader && strings.HasPrefix(line, "-"):
			if oldPath != "" {
				removed[oldPath] = append(removed[oldPath], line[1:])
			}
		}
	}
	return added, removed
}

// parseDiffHeaderPath decodes Git's C-quoted ---/+++ path before dropping its
// a/ or b/ prefix. Git quotes paths containing control bytes even when
// core.quotepath=off, so leaving the quotes intact would make AddedLines and
// RemovedLines use different keys from the NUL-delimited file list.
func parseDiffHeaderPath(p string) string {
	p = strings.TrimRight(p, "\r")
	if strings.HasPrefix(p, `"`) {
		if unquoted, err := strconv.Unquote(p); err == nil {
			p = unquoted
		}
	}
	return stripDiffPathPrefix(p)
}

// stripDiffPathPrefix drops the git diff `a/` or `b/` path prefix. /dev/null and
// any other path pass through unchanged.
func stripDiffPathPrefix(p string) string {
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}
