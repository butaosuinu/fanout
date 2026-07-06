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

// loadDiff runs the three read-only git diffs (--name-status -M, --numstat -M,
// and -U0) between base and the working tree and assembles them into a Diff: the
// file list with rename detection, per-file added/deleted counts, and the +/-
// content lines the escalation greps scan. Dropping HEAD makes the base the only
// argument, so the diff spans base..working tree and sees uncommitted edits
// (untracked files stay invisible to git diff). core.quotepath=off keeps
// non-ASCII paths literal so classifyPath matches them.
func loadDiff(base string) (Diff, error) {
	nameStatus, err := runGit("-c", "core.quotepath=off", "diff", "--name-status", "-M", base)
	if err != nil {
		return Diff{}, err
	}
	numstat, err := runGit("-c", "core.quotepath=off", "diff", "--numstat", "-M", base)
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

// parseNameStatus parses `git diff --name-status -M` output. Each line is a
// tab-separated status letter followed by one path (A/M/D) or, for renames and
// copies (R<score>/C<score>), the old and new paths.
func parseNameStatus(out string) []FileChange {
	var files []FileChange
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		fc := FileChange{Status: fields[0][0]}
		switch fc.Status {
		case 'R', 'C':
			if len(fields) < 3 {
				continue
			}
			fc.OldPath, fc.Path = fields[1], fields[2]
		default:
			fc.Path = fields[1]
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

// parseNumstat parses `git diff --numstat -M` output into a per-path count map.
// Columns are added, deleted, path; a binary file's "-" columns become -1. The
// path column is normalized through numstatPath so a rename's merged form keys
// on the new path, matching the name-status entry.
func parseNumstat(out string) map[string]numstatEntry {
	counts := make(map[string]numstatEntry)
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		var e numstatEntry
		if fields[0] == "-" || fields[1] == "-" {
			e.added, e.deleted = -1, -1
		} else {
			e.added, _ = strconv.Atoi(fields[0])
			e.deleted, _ = strconv.Atoi(fields[1])
		}
		counts[numstatPath(fields[2])] = e
	}
	return counts
}

// numstatPath collapses the merged rename forms `git diff --numstat -M` emits so
// the result is the new path, matching the name-status new path. A brace group
// `dir/{old => new}/rest` becomes `dir/new/rest`; an empty new side
// (`dir/{old => }/file.go`, a rename that only drops a path segment) collapses
// the doubled slash to `dir/file.go`, and an empty old side (`{ => new}`) yields
// just the new side. A brace-less whole-path `old => new` takes the new side. A
// field with no ` => ` passes through unchanged.
func numstatPath(field string) string {
	if open := strings.IndexByte(field, '{'); open >= 0 {
		if shut := strings.IndexByte(field[open:], '}'); shut >= 0 {
			shut += open
			if _, newPart, ok := strings.Cut(field[open+1:shut], " => "); ok {
				prefix, suffix := field[:open], field[shut+1:]
				// An empty new side (dir/{old => }/file.go) leaves prefix ending
				// in '/' and suffix beginning with '/'. Splicing an empty middle
				// between them doubles the separator (dir//file.go) and misses
				// the name-status new path, so drop the redundant slash.
				if newPart == "" {
					suffix = strings.TrimPrefix(suffix, "/")
				}
				return prefix + newPart + suffix
			}
		}
	}
	if _, newPath, ok := strings.Cut(field, " => "); ok {
		return newPath
	}
	return field
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
			if p := stripDiffPathPrefix(strings.TrimRight(line[4:], "\r")); p != "/dev/null" {
				oldPath = p
			}
		case inHeader && strings.HasPrefix(line, "+++ "):
			if p := stripDiffPathPrefix(strings.TrimRight(line[4:], "\r")); p != "/dev/null" {
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

// stripDiffPathPrefix drops the git diff `a/` or `b/` path prefix. /dev/null and
// any other path pass through unchanged.
func stripDiffPathPrefix(p string) string {
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}
