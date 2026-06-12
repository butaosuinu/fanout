package team

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DBPathEnv overrides the computed team DB path when set (FANOUT_DB_PATH).
const DBPathEnv = "FANOUT_DB_PATH"

var nonAlnumRE = regexp.MustCompile("[^a-z0-9]+")

// DBPath returns the per-parent team DB path,
// /tmp/fanout-<repo_slug>-<parent_key>.db, where repo_slug is
// filepath.Base(projectRoot) — the same convention as briefing.Path
// (/tmp/fanout-<repo>-<N>.md). /tmp is used because every sibling worktree
// reaches the same path there. A non-empty FANOUT_DB_PATH wins over
// everything and is returned verbatim.
func DBPath(projectRoot, parentRef string) string {
	if override := os.Getenv(DBPathEnv); override != "" {
		return override
	}
	return fmt.Sprintf("/tmp/fanout-%s-%s.db", filepath.Base(projectRoot), ParentDBSlug(parentRef))
}

// ParentDBSlug normalizes a parent ref into the <parent_key> path token.
// Pure issue numbers collapse leading zeros ("0068" -> "68"), matching the
// numeric parent equivalence used by the state idempotency key, so both
// spellings share one DB. Anything else (a Projects URL, "@manual") is
// lowercased, runs of [^a-z0-9] become "-", and leading/trailing dashes are
// trimmed. An empty result falls back to "parent".
func ParentDBSlug(parentRef string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(parentRef)); err == nil && n >= 0 {
		return strconv.Itoa(n)
	}
	slug := strings.Trim(nonAlnumRE.ReplaceAllString(strings.ToLower(parentRef), "-"), "-")
	if slug == "" {
		return "parent"
	}
	return slug
}
