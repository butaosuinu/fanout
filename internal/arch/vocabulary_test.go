package arch

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runtimeVocabularyAllowFile holds the reviewed exemptions. It sits beside the
// godep-cruiser rules because it is the same kind of canon: a rule plus the
// list of places the rule deliberately does not reach.
const runtimeVocabularyAllowFile = "runtime-vocabulary-allow.json"

// minRuntimeVocabularyFiles keeps the scan from passing vacuously. A renamed
// tree, a broken walk, or a skip rule that swallows too much would otherwise
// make a zero-finding run look clean. The trees below held 81 non-test files
// when this was written; 60 is a floor, not a target.
const minRuntimeVocabularyFiles = 60

// runtimeVocabularyNames are the runtime names app and cmd must not spell in
// code. Matching is case-insensitive and on substrings, so Tmux, tmuxrun, and
// herdrProcess all match.
var runtimeVocabularyNames = []string{"tmux", "herdr"}

// runtimeVocabularyTrees are the layers that must stay runtime-neutral.
// internal/infra is excluded by design: the adapters there exist to name a
// runtime. internal/ui is not covered yet - it still imports tmuxrun directly.
var runtimeVocabularyTrees = []struct {
	name string
	path string
}{
	{name: "composition root", path: "cmd/fanout"},
	{name: "app layer", path: "internal/app"},
}

// vocabularyAllowlist is the on-disk exemption canon. Files exempt a whole
// source file; Identifiers exempt one exact identifier name everywhere; Tags
// exempt one exact struct tag value everywhere.
type vocabularyAllowlist struct {
	Files       []vocabularyAllowEntry `json:"files"`
	Identifiers []vocabularyAllowEntry `json:"identifiers"`
	Tags        []vocabularyAllowEntry `json:"tags"`
}

// vocabularyAllowEntry is one exemption. Name is a repo-relative path for a
// file entry, an exact identifier for an identifier entry, and the exact tag
// literal (backquotes included) for a tag entry. Files, mandatory on an
// identifier or tag entry, pins the exemption to (file, occurrence count)
// pairs: a new file spelling the name AND a new occurrence in an already
// listed file both fail until the entry is updated with the reason
// re-reviewed. Count pinning is what keeps an exempted data-read constant
// (backend.Tmux) from quietly gaining a new name-based branch in a file that
// already reads it.
type vocabularyAllowEntry struct {
	Name   string                `json:"name"`
	Reason string                `json:"reason"`
	Files  []vocabularyAllowFile `json:"files,omitempty"`
}

// vocabularyAllowFile is one (file, expected occurrence count) pin.
type vocabularyAllowFile struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// vocabularyFinding is one place a runtime name is spelled in code.
type vocabularyFinding struct {
	File string
	Line int
	Kind string
	Text string
}

func (f vocabularyFinding) String() string {
	return fmt.Sprintf("%s:%d: %s %q spells a runtime name", f.File, f.Line, f.Kind, f.Text)
}

// TestRuntimeVocabulary pins that cmd/fanout and internal/app do not spell a
// concrete runtime name (tmux, herdr) in code: file names, import paths,
// identifiers, and struct tags. Those layers select a runtime through
// core/backend's capabilities and MutationModel, so a runtime name in an
// identifier means a lane is branching on the runtime again.
//
// String literals and comments are exempt wholesale, and deliberately so.
// Operator-facing strings (command names, env vars, error text) and comments
// that describe how a runtime behaves are legitimate and outnumber the code
// findings several times over; allowlisting them one by one would be hundreds
// of entries of pure friction for no enforcement value. Struct tags are
// checked despite being literals, because a tag names a wire field.
//
// The complement rules app-no-runtime-adapters / cmd-no-runtime-adapters in
// godep-cruiser.json cover the import edge; this test covers the naming.
func TestRuntimeVocabulary(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	// Loaded by bare name from the package directory, exactly as
	// TestArchitecture loads godep-cruiser.json.
	allow, err := loadVocabularyAllowlist(runtimeVocabularyAllowFile)
	if err != nil {
		t.Fatalf("loadVocabularyAllowlist() = %v, want nil", err)
	}
	scan := scanRuntimeVocabulary(t, root)

	t.Run("app and cmd spell no runtime name in code", func(t *testing.T) {
		for _, finding := range scan.findings {
			if allow.covers(finding) {
				continue
			}
			t.Errorf("%s; rename it to the capability it uses, or add it to %s with a reason",
				finding, runtimeVocabularyAllowFile)
		}
	})

	t.Run("every allowlist entry is live and counts match", func(t *testing.T) {
		for _, drift := range allow.drifted() {
			t.Errorf("%s: %s", runtimeVocabularyAllowFile, drift)
		}
	})

	t.Run("scan covered both trees", func(t *testing.T) {
		if scan.files < minRuntimeVocabularyFiles {
			t.Errorf("scanned %d non-test .go files, want at least %d; the walk or the tree list is broken",
				scan.files, minRuntimeVocabularyFiles)
		}
		// Iterate the configured list, not the found keys: a tree that yielded
		// nothing never gets a perTree key and would pass a found-key loop.
		for _, tree := range runtimeVocabularyTrees {
			if scan.perTree[tree.path] == 0 {
				t.Errorf("tree %s contributed no files; it was renamed or removed", tree.path)
			}
		}
	})
}

// vocabularyScan is one walk's result: every finding, plus the file counts the
// vacuous-pass guard checks.
type vocabularyScan struct {
	findings []vocabularyFinding
	files    int
	perTree  map[string]int
}

func scanRuntimeVocabulary(t *testing.T, root string) vocabularyScan {
	t.Helper()
	scan := vocabularyScan{perTree: map[string]int{}}
	fset := token.NewFileSet()
	for _, tree := range runtimeVocabularyTrees {
		walkErr := filepath.WalkDir(filepath.Join(root, tree.path), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return skipScannedDir(d.Name())
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			scan.files++
			scan.perTree[tree.path]++
			found, parseErr := inspectFileVocabulary(fset, path, filepath.ToSlash(rel))
			if parseErr != nil {
				return parseErr
			}
			scan.findings = append(scan.findings, found...)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s = %v, want nil", tree.path, walkErr)
		}
	}
	return scan
}

// skipScannedDir applies godep-cruiser's own skip rules so the two scans agree
// on what counts as source.
func skipScannedDir(name string) error {
	if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return fs.SkipDir
	}
	return nil
}

// inspectFileVocabulary reports the runtime names one file spells in code. The
// file is parsed without parser.ParseComments, so comments never reach the AST
// and cannot be flagged.
func inspectFileVocabulary(fset *token.FileSet, path, rel string) ([]vocabularyFinding, error) {
	var findings []vocabularyFinding
	if name := filepath.Base(rel); matchesRuntimeName(name) {
		findings = append(findings, vocabularyFinding{File: rel, Line: 0, Kind: "file name", Text: name})
	}
	// The directory is the package's own import path. Checking it catches a
	// runtime-named package (internal/app/herdrbridge/...) that no neutral
	// code imports yet, which the import check alone would never see.
	if dir := filepath.ToSlash(filepath.Dir(rel)); matchesRuntimeName(dir) {
		findings = append(findings, vocabularyFinding{File: rel, Line: 0, Kind: "package path", Text: dir})
	}
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	for _, spec := range file.Imports {
		if matchesRuntimeName(spec.Path.Value) {
			findings = append(findings, vocabularyFinding{
				File: rel, Line: fset.Position(spec.Path.Pos()).Line,
				Kind: "import", Text: strings.Trim(spec.Path.Value, `"`),
			})
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			if matchesRuntimeName(node.Name) {
				findings = append(findings, vocabularyFinding{
					File: rel, Line: fset.Position(node.Pos()).Line,
					Kind: "identifier", Text: node.Name,
				})
			}
		case *ast.Field:
			// Struct tags are string literals, but a tag names a wire field
			// rather than describing a runtime, so it is held to the rule.
			if node.Tag != nil && matchesRuntimeName(node.Tag.Value) {
				findings = append(findings, vocabularyFinding{
					File: rel, Line: fset.Position(node.Tag.Pos()).Line,
					Kind: "struct tag", Text: node.Tag.Value,
				})
			}
		}
		return true
	})
	return findings, nil
}

func matchesRuntimeName(s string) bool {
	lowered := strings.ToLower(s)
	for _, name := range runtimeVocabularyNames {
		if strings.Contains(lowered, name) {
			return true
		}
	}
	return false
}

// allowlistIndex is the loaded allowlist plus the observation tracking the
// drift check reads back. Scoped entries are keyed "<kind>:<name>@<path>" with
// an expected occurrence count; file entries cover only the file-name finding.
type allowlistIndex struct {
	fileEntries map[string]bool
	fileUsed    map[string]bool
	expected    map[string]int
	observed    map[string]int
}

func loadVocabularyAllowlist(path string) (*allowlistIndex, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed vocabularyAllowlist
	// An unknown field is a typo (e.g. "file" for "files") that would
	// silently widen an exemption back to unscoped; reject it.
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	index := &allowlistIndex{
		fileEntries: map[string]bool{}, fileUsed: map[string]bool{},
		expected: map[string]int{}, observed: map[string]int{},
	}
	for _, entry := range parsed.Files {
		if strings.TrimSpace(entry.Reason) == "" {
			return nil, fmt.Errorf("file entry %q has no reason", entry.Name)
		}
		if len(entry.Files) != 0 {
			return nil, fmt.Errorf("file entry %q scopes by files; a file entry is its own scope", entry.Name)
		}
		index.fileEntries[entry.Name] = true
	}
	categories := []struct {
		kind    string
		entries []vocabularyAllowEntry
	}{
		{kind: "identifier", entries: parsed.Identifiers},
		{kind: "struct tag", entries: parsed.Tags},
	}
	for _, category := range categories {
		for _, entry := range category.entries {
			// A reasonless exemption is indistinguishable from an accident.
			if strings.TrimSpace(entry.Reason) == "" {
				return nil, fmt.Errorf("%s entry %q has no reason", category.kind, entry.Name)
			}
			// An unscoped exemption would be valid in every file forever;
			// every entry pins (file, count) pairs.
			if len(entry.Files) == 0 {
				return nil, fmt.Errorf("%s entry %q has no files; scope it to (path, count) pins", category.kind, entry.Name)
			}
			for _, file := range entry.Files {
				if file.Path == "" || file.Count < 1 {
					return nil, fmt.Errorf("%s entry %q pins %q with count %d; want a path and a count >= 1",
						category.kind, entry.Name, file.Path, file.Count)
				}
				index.expected[category.kind+":"+entry.Name+"@"+file.Path] = file.Count
			}
		}
	}
	return index, nil
}

// covers reports whether the finding is exempt. A file entry exempts only the
// file NAME finding: everything inside the file (identifiers, imports, tags)
// still needs its own scoped entry, so a runtime-named file cannot quietly
// grow a name-based branch under a blanket exemption. A scoped entry admits
// occurrences up to its pinned count; extra occurrences overflow and are
// reported as ordinary findings, so a new use of an exempted name in an
// already listed file still fails.
func (a *allowlistIndex) covers(finding vocabularyFinding) bool {
	if finding.Kind == "file name" && a.fileEntries[finding.File] {
		a.fileUsed[finding.File] = true
		return true
	}
	key := finding.Kind + ":" + finding.Text + "@" + finding.File
	if a.observed[key] < a.expected[key] {
		a.observed[key]++
		return true
	}
	return false
}

// drifted returns every entry whose reality no longer matches the pin: a file
// entry that matched nothing, a scoped entry that matched nothing (stale), and
// a scoped entry whose observed count fell below the pin (the count must be
// lowered so the exemption cannot cover a future new occurrence). Overflowing
// occurrences already failed as findings in covers.
func (a *allowlistIndex) drifted() []string {
	var drift []string
	for path := range a.fileEntries {
		if !a.fileUsed[path] {
			drift = append(drift, fmt.Sprintf("file entry %q matches nothing; delete it", path))
		}
	}
	for key, want := range a.expected {
		switch got := a.observed[key]; {
		case got == 0:
			drift = append(drift, fmt.Sprintf("entry %q matches nothing; delete it", key))
		case got < want:
			drift = append(drift, fmt.Sprintf("entry %q pins %d occurrences but only %d remain; lower the count", key, want, got))
		}
	}
	// Map iteration is unordered; sort so a failure reports the same list twice.
	slices.Sort(drift)
	return drift
}
