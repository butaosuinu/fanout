package main

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// These tests keep the classification rules in rules.go in lockstep with the
// package table in docs/architecture.ja.md (the H/M/A class canon). If a doc
// row and its rule drift apart, one of these fails in CI so the two are always
// edited together.

// docRow is one parsed data row of the "## パッケージ表" table: its layer and
// class columns plus the raw package cell (column 2) the tokens come from.
// catchAll marks the open-ended rows ("上記以外" / "…ほか") that describe a
// bucket rather than an enumerable set of packages.
type docRow struct {
	layer       string
	packageCell string
	class       Class
	catchAll    bool
	lineNo      int
}

// docLayerBase maps the table's layer column to the repo-relative base
// directory a bare package token lives under. tools/web tokens are already full
// repo-relative paths, so their base is empty.
var docLayerBase = map[string]string{
	"meta":  "internal",
	"core":  "internal/core",
	"app":   "internal/app",
	"infra": "internal/infra",
	"ui":    "internal/ui",
	"cmd":   "cmd/fanout",
	"tools": "",
	"web":   "",
}

// docKnownExts are the file-name extensions the doc table spells out. A token
// carrying one of these is treated as a file to classify directly; any other
// token is a directory that gets a synthetic __probe__ file.
var docKnownExts = []string{".go", ".tsx", ".ts", ".html", ".css", ".json", ".yaml", ".yml", ".md", ".sh"}

var backtickTokenRe = regexp.MustCompile("`([^`]+)`")

// TestDocSyncPackageTable is the forward direction: every package-table row in
// the doc must resolve, through classifyPath, to the class the row declares.
// Bare package tokens are probed as <dir>/__probe__.<ext>; file tokens (and the
// `dashboard`(`server.go`) form, where a directory token scopes the files) are
// classified as-is. A mismatch means the doc and rules.go disagree.
func TestDocSyncPackageTable(t *testing.T) {
	rows := parsePackageTable(t, readArchDoc(t))
	// A broken parser must not pass vacuously: the table has well over 45 rows.
	if len(rows) < 45 {
		t.Fatalf("parsePackageTable() = %d data rows, want >= 45 (the parser likely broke)", len(rows))
	}

	var catchAllPairs []string
	for _, row := range rows {
		if _, ok := docLayerBase[row.layer]; !ok {
			t.Errorf("line %d: unknown layer %q in the package table (add it to docLayerBase)", row.lineNo, row.layer)
			continue
		}
		if row.catchAll {
			catchAllPairs = append(catchAllPairs, row.layer+":"+row.class.String())
		}
		paths := docProbePaths(row)
		if len(paths) == 0 && !row.catchAll {
			t.Errorf("line %d: package cell %q yielded no classifiable token", row.lineNo, row.packageCell)
			continue
		}
		for _, p := range paths {
			r, ok := classifyPath(p)
			if !ok {
				t.Errorf("line %d: classifyPath(%q) is unclassified, but the doc table declares it class %v", row.lineNo, p, row.class)
				continue
			}
			if r.Class != row.class {
				t.Errorf("line %d: classifyPath(%q) class = %v, doc table declares %v", row.lineNo, p, r.Class, row.class)
			}
			if r.Source != SourceDocTable {
				t.Errorf("line %d: classifyPath(%q) matched extra rule %q, but a package-table row must map to a doc-table rule", row.lineNo, p, r.ID)
			}
		}
	}

	// The open-ended rows are pinned by (layer, class) so one cannot silently
	// appear, disappear, or change class. Today: cmd rest (M), the two tui
	// buckets (M for wiring, A for the view layer), and web display (A).
	slices.Sort(catchAllPairs)
	wantCatchAll := []string{"cmd:M", "ui:A", "ui:M", "web:A"}
	if !slices.Equal(catchAllPairs, wantCatchAll) {
		t.Errorf("catch-all rows (layer:class) = %v, want %v", catchAllPairs, wantCatchAll)
	}
}

// TestDocSyncReverse is the reverse direction: every SourceDocTable rule in
// rules.go must correspond to a package-table row of the same class. A rule with
// no matching row is stale (the doc dropped it); a class mismatch is a conflict.
// fileRules are matched per PATH, not per rule ID: several files share one ID
// (e.g. the cmd H set), so an ID-level check would keep passing after the doc
// dropped a single file's row while its sibling still supplies the ID.
func TestDocSyncReverse(t *testing.T) {
	docFiles := collectDocFilePaths(t)
	// fileRules iteration order is undefined, but each check is independent.
	for p, r := range fileRules {
		if r.Source != SourceDocTable {
			continue
		}
		got, ok := docFiles[p]
		if !ok {
			t.Errorf("fileRules[%q] (rule %q, class %v) is not enumerated in the package table (stale rule — remove it or restore the doc row)", p, r.ID, r.Class)
			continue
		}
		if got != r.Class {
			t.Errorf("fileRules[%q] (rule %q) is class %v in rules.go but %v in the doc table", p, r.ID, r.Class, got)
		}
	}
	// prefixRules are matched per PREFIX for the same reason: web/src/hooks/ and
	// web/src/lib/ share the web-transport ID, so dropping one dir from the doc
	// must still surface as a stale prefix rule.
	docDirs := collectDocDirClasses(t)
	for _, pr := range prefixRules {
		if pr.rule.Source != SourceDocTable {
			continue
		}
		dir := strings.TrimSuffix(pr.prefix, "/")
		classes, ok := docDirs[dir]
		if !ok {
			t.Errorf("prefixRules[%s] (rule %q, class %v) has no matching package-table row (stale rule — remove it or restore the doc row)", pr.prefix, pr.rule.ID, pr.rule.Class)
			continue
		}
		if !classes[pr.rule.Class] {
			t.Errorf("prefixRules[%s] (rule %q) is class %v in rules.go but the doc table assigns %s only %v", pr.prefix, pr.rule.ID, pr.rule.Class, dir, slices.Sorted(maps.Keys(classes)))
		}
	}
}

// TestInvariantPatternsAppearInDoc pins the S10 invariant greps to the doc: each
// DocLiteral must appear in architecture.ja.md's invariant catalog. A renamed
// invariant in the doc that leaves the grep pattern stale (or vice versa) fails
// here instead of silently dropping off the human-must-read watch list.
func TestInvariantPatternsAppearInDoc(t *testing.T) {
	content := readArchDoc(t)
	for _, ip := range invariantPatterns {
		if !strings.Contains(content, ip.DocLiteral) {
			t.Errorf("invariant %q: DocLiteral %q does not appear in docs/architecture.ja.md", ip.Name, ip.DocLiteral)
		}
	}
}

// TestRepoTreeFullyClassified walks every git-tracked file through classifyPath
// and asserts none is unclassified. It is the fail-closed backstop: a new
// top-level file or directory that falls through every rule surfaces here as a
// concrete path to add a rule for, mirroring internal/arch's
// TestAllPackagesClassified.
func TestRepoTreeFullyClassified(t *testing.T) {
	root := repoRootDir(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	// Guard against a broken invocation making the loop vacuous.
	if len(files) < 100 {
		t.Fatalf("git ls-files returned %d tracked files, want a full checkout (>= 100)", len(files))
	}
	var unclassified []string
	for _, f := range files {
		if f == "" {
			continue
		}
		if _, ok := classifyPath(f); !ok {
			unclassified = append(unclassified, f)
		}
	}
	slices.Sort(unclassified)
	for _, f := range unclassified {
		t.Errorf("classifyPath(%q) is unclassified: add a rule in rules.go (a tracked file fell through the fail-closed gap)", f)
	}
}

// docProbePaths returns the repo-relative paths a package-table row should
// classify to. File tokens (known extension) are classified directly, scoped by
// any directory token in the same cell (the `dashboard`(`server.go`) form).
// When the cell names only directories, each is probed as a synthetic
// __probe__ file. Glob tokens (`newpane*.go`) are skipped: they name a set, not
// a file.
func docProbePaths(row docRow) []string {
	base := docLayerBase[row.layer]
	ext := ".go"
	if row.layer == "web" {
		ext = ".tsx"
	}
	var dirTokens, fileTokens []string
	for _, tok := range backtickTokens(row.packageCell) {
		if strings.Contains(tok, "*") {
			continue
		}
		if hasKnownExt(tok) {
			fileTokens = append(fileTokens, tok)
		} else {
			dirTokens = append(dirTokens, tok)
		}
	}
	var paths []string
	if len(fileTokens) > 0 {
		// File tokens win: a directory token only scopes them, it is not
		// probed on its own (probing the bare tui dir would classify M, but the
		// A-view row is defined by its enumerated files).
		dirPrefix := base
		for _, d := range dirTokens {
			dirPrefix = path.Join(dirPrefix, d)
		}
		for _, f := range fileTokens {
			paths = append(paths, path.Join(dirPrefix, f))
		}
		return paths
	}
	for _, d := range dirTokens {
		paths = append(paths, path.Join(base, d, "__probe__"+ext))
	}
	return paths
}

// collectDocFilePaths returns every concrete file path the package table
// enumerates (file tokens scoped by their dir tokens), with the row's class.
// TestDocSyncReverse matches fileRules against it per path.
func collectDocFilePaths(t *testing.T) map[string]Class {
	t.Helper()
	out := make(map[string]Class)
	for _, row := range parsePackageTable(t, readArchDoc(t)) {
		base := docLayerBase[row.layer]
		var dirTokens, fileTokens []string
		for _, tok := range backtickTokens(row.packageCell) {
			if strings.Contains(tok, "*") {
				continue
			}
			if hasKnownExt(tok) {
				fileTokens = append(fileTokens, tok)
			} else {
				dirTokens = append(dirTokens, tok)
			}
		}
		dirPrefix := base
		for _, d := range dirTokens {
			dirPrefix = path.Join(dirPrefix, d)
		}
		for _, f := range fileTokens {
			out[path.Join(dirPrefix, f)] = row.class
		}
	}
	return out
}

// collectDocDirClasses returns every directory the package table names — each
// dir token resolved individually against the row's layer base, plus the bare
// base for file-only rows (the cmd rows enumerate files but still pin
// cmd/fanout) — mapped to the set of classes its rows declare. A dir can carry
// several classes (internal/ui/tui appears in an H, an M, and an A row), so
// TestDocSyncReverse checks membership, not equality.
func collectDocDirClasses(t *testing.T) map[string]map[Class]bool {
	t.Helper()
	out := make(map[string]map[Class]bool)
	add := func(dir string, c Class) {
		if out[dir] == nil {
			out[dir] = make(map[Class]bool)
		}
		out[dir][c] = true
	}
	for _, row := range parsePackageTable(t, readArchDoc(t)) {
		base := docLayerBase[row.layer]
		var dirTokens []string
		hasFile := false
		for _, tok := range backtickTokens(row.packageCell) {
			if strings.Contains(tok, "*") {
				continue
			}
			if hasKnownExt(tok) {
				hasFile = true
			} else {
				dirTokens = append(dirTokens, tok)
			}
		}
		for _, d := range dirTokens {
			add(path.Join(base, d), row.class)
		}
		if len(dirTokens) == 0 && hasFile && base != "" {
			add(base, row.class)
		}
	}
	return out
}

func hasKnownExt(tok string) bool {
	for _, e := range docKnownExts {
		if strings.HasSuffix(tok, e) {
			return true
		}
	}
	return false
}

func backtickTokens(s string) []string {
	m := backtickTokenRe.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(m))
	for _, g := range m {
		out = append(out, g[1])
	}
	return out
}

// parsePackageTable extracts the data rows of the "## パッケージ表" section,
// dropping the header and separator rows. It fails the test on a malformed row
// (not 4 columns, or a final cell that is not H/M/A) so parser drift is loud.
func parsePackageTable(t *testing.T, content string) []docRow {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "## パッケージ表" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("docs/architecture.ja.md: '## パッケージ表' section not found")
	}
	var rows []docRow
	for i := start + 1; i < len(lines); i++ {
		raw := lines[i]
		if strings.HasPrefix(raw, "## ") {
			break // next section
		}
		trimmed := strings.TrimSpace(raw)
		if !strings.HasPrefix(trimmed, "|") {
			continue // prose, not a table row
		}
		if isSeparatorRow(trimmed) {
			continue
		}
		cells, ok := tableCells(trimmed)
		if !ok {
			t.Fatalf("line %d: cannot parse table row into 4 cells: %s", i+1, raw)
		}
		if cells[3] == "Class" {
			continue // header row
		}
		class, ok := parseClassLabel(cells[3])
		if !ok {
			t.Fatalf("line %d: final cell %q is not a class (want H/M/A): %s", i+1, cells[3], raw)
		}
		pkgCell := cells[1]
		rows = append(rows, docRow{
			layer:       cells[0],
			packageCell: pkgCell,
			class:       class,
			catchAll:    strings.Contains(pkgCell, "以外") || strings.Contains(pkgCell, "ほか"),
			lineNo:      i + 1,
		})
	}
	return rows
}

// tableCells splits a markdown table row on its pipes and returns the four
// trimmed inner cells, or ok=false if the row is not 4-column.
func tableCells(row string) ([]string, bool) {
	parts := strings.Split(row, "|")
	if len(parts) < 3 {
		return nil, false
	}
	inner := parts[1 : len(parts)-1] // drop the empty ends the outer pipes make
	if len(inner) != 4 {
		return nil, false
	}
	cells := make([]string, len(inner))
	for i, c := range inner {
		cells[i] = strings.TrimSpace(c)
	}
	return cells, true
}

// isSeparatorRow reports whether a table row is the |---|---| separator (only
// pipes, dashes, colons, and spaces, with at least one dash).
func isSeparatorRow(row string) bool {
	for _, r := range row {
		switch r {
		case '|', '-', ':', ' ', '\t':
		default:
			return false
		}
	}
	return strings.Contains(row, "-")
}

func parseClassLabel(s string) (Class, bool) {
	switch s {
	case "H":
		return ClassH, true
	case "M":
		return ClassM, true
	case "A":
		return ClassA, true
	default:
		return ClassUnknown, false
	}
}

// readArchDoc returns the contents of docs/architecture.ja.md, resolving the
// repo root by walking up from the test's working directory.
func readArchDoc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRootDir(t), "docs", "architecture.ja.md"))
	if err != nil {
		t.Fatalf("read architecture.ja.md: %v", err)
	}
	return string(data)
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot() = %v, want the repo root", err)
	}
	return root
}

// findRepoRoot walks up from the working directory (the package dir under
// `go test`) to the first go.mod. It reimplements internal/arch's helper: this
// tree is stdlib-only and cannot import module packages.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod found above the test working directory")
		}
		dir = parent
	}
}
