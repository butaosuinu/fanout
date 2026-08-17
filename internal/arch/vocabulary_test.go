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
// literal (backquotes included) for a tag entry. Files, on an identifier or
// tag entry, scopes the exemption to those repo-relative files: a new file
// spelling the name fails until it is listed here with the reason
// re-reviewed. Every identifier and tag entry is scoped; an unscoped
// exemption would let a new name-based branch or wire field ride in unseen.
type vocabularyAllowEntry struct {
	Name   string   `json:"name"`
	Reason string   `json:"reason"`
	Files  []string `json:"files,omitempty"`
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

	t.Run("every allowlist entry is live", func(t *testing.T) {
		for _, entry := range allow.unused() {
			t.Errorf("%s entry %q matches nothing; delete it", runtimeVocabularyAllowFile, entry)
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

// allowlistIndex is the loaded allowlist plus the hit tracking the stale-entry
// check reads back. Entries are keyed "<kind>:<name>" in one map so a hit and
// a staleness question are the same lookup.
type allowlistIndex struct {
	entries map[string]bool
	used    map[string]bool
}

func loadVocabularyAllowlist(path string) (*allowlistIndex, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed vocabularyAllowlist
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	index := &allowlistIndex{entries: map[string]bool{}, used: map[string]bool{}}
	categories := []struct {
		kind    string
		entries []vocabularyAllowEntry
	}{
		{kind: "file", entries: parsed.Files},
		{kind: "identifier", entries: parsed.Identifiers},
		{kind: "struct tag", entries: parsed.Tags},
	}
	for _, category := range categories {
		for _, entry := range category.entries {
			// A reasonless exemption is indistinguishable from an accident.
			if strings.TrimSpace(entry.Reason) == "" {
				return nil, fmt.Errorf("%s entry %q has no reason", category.kind, entry.Name)
			}
			if len(entry.Files) == 0 {
				index.entries[category.kind+":"+entry.Name] = true
				continue
			}
			// A scoped entry exempts (name, file) pairs; each file must stay
			// live on its own, so a rename cannot widen the exemption. File
			// entries already name a path, so scoping them again is a mistake.
			if category.kind == "file" {
				return nil, fmt.Errorf("file entry %q scopes by files; a file entry is its own scope", entry.Name)
			}
			for _, file := range entry.Files {
				index.entries[category.kind+":"+entry.Name+"@"+file] = true
			}
		}
	}
	return index, nil
}

// covers reports whether the finding is exempt, recording the hit so an entry
// that never fires can be reported as stale. A file entry exempts only the
// file NAME finding: everything inside the file (identifiers, imports, tags)
// still needs its own scoped entry, so a runtime-named file cannot quietly
// grow a name-based branch under a blanket exemption. A file-scoped entry is
// consulted before the global form, so scoping a name never loosens what an
// unscoped entry would have allowed.
func (a *allowlistIndex) covers(finding vocabularyFinding) bool {
	return (finding.Kind == "file name" && a.hit("file", finding.File)) ||
		a.hit(finding.Kind, finding.Text+"@"+finding.File) ||
		a.hit(finding.Kind, finding.Text)
}

func (a *allowlistIndex) hit(kind, name string) bool {
	key := kind + ":" + name
	if !a.entries[key] {
		return false
	}
	a.used[key] = true
	return true
}

// unused returns the entries that matched nothing, so a rename cannot leave a
// silent exemption behind.
func (a *allowlistIndex) unused() []string {
	var stale []string
	for key := range a.entries {
		if !a.used[key] {
			stale = append(stale, key)
		}
	}
	// Map iteration is unordered; sort so a failure reports the same list twice.
	slices.Sort(stale)
	return stale
}
