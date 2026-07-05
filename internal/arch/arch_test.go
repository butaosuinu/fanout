package arch

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// layer identifies one of the four architecture layers plus the cmd
// entrypoints and this meta package.
type layer string

const (
	layerCore  layer = "core"
	layerApp   layer = "app"
	layerInfra layer = "infra"
	layerUI    layer = "ui"
	layerCmd   layer = "cmd"
	layerMeta  layer = "meta" // internal/arch itself: test-only, imports no module packages
)

// explicitLayers classifies the packages that live outside the four layer
// directories. Since the 4-layer move completed, only the meta package remains;
// every other package derives its layer from its internal/<layer>/ prefix and
// TestInternalTreeShape rejects new top-level directories under internal/.
var explicitLayers = map[string]layer{
	"internal/arch": layerMeta,
}

// prefixLayers derives the layer from the package path once packages live
// under their layer directory.
var prefixLayers = []struct {
	prefix string
	l      layer
}{
	{"internal/core/", layerCore},
	{"internal/app/", layerApp},
	{"internal/infra/", layerInfra},
	{"internal/ui/", layerUI},
	{"cmd/", layerCmd},
}

// allowedImports is the layer dependency direction: a key layer may import
// only the value layers. cmd deliberately has no cmd entry: all shared cmd
// helpers belong in internal/, and TestPackageMainOnlyInCmd bans every import
// of cmd/... outright.
var allowedImports = map[layer]map[layer]bool{
	layerMeta:  {},
	layerCore:  {layerCore: true},
	layerApp:   {layerCore: true, layerApp: true, layerInfra: true},
	layerInfra: {layerCore: true, layerInfra: true},
	layerUI:    {layerCore: true, layerApp: true, layerInfra: true, layerUI: true},
	layerCmd:   {layerCore: true, layerApp: true, layerInfra: true, layerUI: true},
}

// legacyDirectionAllowlist grandfathers layer violations that existed before
// this test was introduced. It maps a module-relative file path to the
// module-relative import paths that file may keep. Do not add entries for new
// code; TestLayerImportDirection fails on entries whose offending import is
// gone, forcing deletion.
var legacyDirectionAllowlist = map[string][]string{
	// infra -> app: the test pins that the team DB path and briefing.Path
	// derive the same parent slug; decouple that fixture to remove this.
	"internal/infra/team/path_test.go": {"internal/app/briefing"},
}

// coreForbiddenStdlib is the stdlib denylist for non-test files in core
// packages: no process, network, filesystem, or database access from the core
// layer. The list is exact-match on purpose — pure-computation packages under
// the same trees (net/url, net/netip) stay allowed. Third-party modules are
// banned wholesale in TestCorePurity, so wrappers cannot smuggle IO in.
var coreForbiddenStdlib = map[string]bool{
	"database/sql": true,
	"io/ioutil":    true,
	"net":          true,
	"net/http":     true,
	"os":           true,
	"os/exec":      true,
	"syscall":      true,
}

// corePurityAllowlist exempts specific core packages from part of the stdlib
// denylist. Lookup walks parent directories, so subpackages inherit their
// parent's exemption.
var corePurityAllowlist = map[string]map[string]bool{
	"internal/core/agent":    {"os": true, "os/exec": true},
	"internal/core/planspec": {"os": true},
}

// classify resolves a module-relative package path to its layer: the layer
// directory prefixes win (a stale explicit entry cannot override them), then
// the explicit map, walking parent dirs so subpackages of a mapped package
// inherit its layer.
func classify(pkgPath string) (layer, bool) {
	for _, p := range prefixLayers {
		if strings.HasPrefix(pkgPath, p.prefix) {
			return p.l, true
		}
	}
	for dir := pkgPath; dir != "" && dir != "." && dir != "/"; dir = path.Dir(dir) {
		if l, ok := explicitLayers[dir]; ok {
			return l, true
		}
	}
	return "", false
}

// purityAllowed reports whether pkgDir (or one of its parents) is exempted
// from the purity ban on importPath.
func purityAllowed(pkgDir, importPath string) bool {
	for dir := pkgDir; dir != "" && dir != "." && dir != "/"; dir = path.Dir(dir) {
		if corePurityAllowlist[dir][importPath] {
			return true
		}
	}
	return false
}

// moduleRel returns the module-relative package path of a module-internal
// import, or ok=false for imports outside this module. An import of the
// module root package resolves to "." so it surfaces as unclassified instead
// of silently escaping every rule.
func moduleRel(importPath string) (string, bool) {
	if importPath == scanned.modulePath {
		return ".", true
	}
	return strings.CutPrefix(importPath, scanned.modulePath+"/")
}

// stdlibImport reports whether the import path is a standard-library package:
// the first path segment of a stdlib import never contains a dot.
func stdlibImport(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

type importRef struct {
	path string // import path as written in the source file
	line int
}

type goFile struct {
	relPath string // module-relative file path, slash-separated
	pkgDir  string // module-relative package directory, slash-separated
	pkgName string // package clause identifier
	pkgLine int    // line of the package clause
	imports []importRef
}

type repoScan struct {
	root       string
	modulePath string
	files      []goFile
}

var (
	scanOnce sync.Once
	scanned  repoScan
	scanErr  error
)

// repoFiles parses every *.go file under internal/ and cmd/ (imports only),
// once per test binary, and resolves the module path from go.mod.
func repoFiles(t *testing.T) []goFile {
	t.Helper()
	scanOnce.Do(func() { scanned, scanErr = scanRepo() })
	if scanErr != nil {
		t.Fatalf("scanRepo() = %v, want nil", scanErr)
	}
	return scanned.files
}

// repoRoot walks up from the working directory (the package dir under
// `go test`) to the first go.mod. runtime.Caller is useless here: its path is
// compile-time and breaks under -trimpath.
func repoRoot() (string, error) {
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

func readModulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(string(data)) {
		if mod, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(mod), nil
		}
	}
	return "", errors.New("go.mod has no module directive")
}

func scanRepo() (repoScan, error) {
	root, err := repoRoot()
	if err != nil {
		return repoScan{}, err
	}
	modPath, err := readModulePath(root)
	if err != nil {
		return repoScan{}, err
	}
	fset := token.NewFileSet()
	scan := repoScan{root: root, modulePath: modPath}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// testdata/ and _/. prefixed dirs are ignored by the Go
				// toolchain, so their contents are not part of any build.
				if p != filepath.Join(root, top) &&
					(d.Name() == "testdata" || strings.HasPrefix(d.Name(), "_") || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			relSlash := filepath.ToSlash(rel)
			f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			gf := goFile{
				relPath: relSlash,
				pkgDir:  path.Dir(relSlash),
				pkgName: f.Name.Name,
				pkgLine: fset.Position(f.Name.Pos()).Line,
			}
			for _, imp := range f.Imports {
				ip, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return err
				}
				gf.imports = append(gf.imports, importRef{path: ip, line: fset.Position(imp.Path.Pos()).Line})
			}
			scan.files = append(scan.files, gf)
			return nil
		})
		if err != nil {
			return repoScan{}, err
		}
	}
	return scan, nil
}

// TestAllPackagesClassified guarantees every package under internal/ and cmd/,
// and every module-internal import target, resolves to a layer, so a new
// package cannot ship unclassified.
func TestAllPackagesClassified(t *testing.T) {
	seen := make(map[string]bool)
	var unclassified []string
	record := func(pkgPath string) {
		if seen[pkgPath] {
			return
		}
		if _, ok := classify(pkgPath); ok {
			return
		}
		seen[pkgPath] = true
		unclassified = append(unclassified, pkgPath)
	}
	for _, f := range repoFiles(t) {
		record(f.pkgDir)
		for _, imp := range f.imports {
			if rel, ok := moduleRel(imp.path); ok {
				record(rel)
			}
		}
	}
	slices.Sort(unclassified)
	for _, pkg := range unclassified {
		t.Errorf("classify(%q) = unclassified, want a layer (add it to explicitLayers or move it under internal/<layer>/)", pkg)
	}
}

// TestInternalTreeShape pins internal/'s top level to the four layer
// directories plus this meta package. It checks the directory entries
// themselves, not just parsed Go files, so a retired directory resurrected by
// stray non-Go files (a stale built bundle, fixtures) is flagged too.
func TestInternalTreeShape(t *testing.T) {
	repoFiles(t) // ensure scanned.root is resolved
	allowed := map[string]bool{"core": true, "app": true, "infra": true, "ui": true, "arch": true}
	entries, err := os.ReadDir(filepath.Join(scanned.root, "internal"))
	if err != nil {
		t.Fatalf("ReadDir(internal) = %v, want nil", err)
	}
	for _, e := range entries {
		name := e.Name()
		if allowed[name] || strings.HasPrefix(name, ".") {
			continue
		}
		t.Errorf("internal/%s is outside the layer directories (move it under internal/core|app|infra|ui/)", name)
	}
}

// TestExplicitLayerMapIsCurrent fails on explicitLayers entries whose package
// directory no longer exists, so a stale entry cannot silently classify an
// unrelated future package after a move.
func TestExplicitLayerMapIsCurrent(t *testing.T) {
	dirs := make(map[string]bool)
	for _, f := range repoFiles(t) {
		dirs[f.pkgDir] = true
	}
	var keys []string
	for key := range explicitLayers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		found := dirs[key]
		for dir := range dirs {
			if strings.HasPrefix(dir, key+"/") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("explicitLayers[%q] is stale: no Go files under that directory (delete the entry after moving the package)", key)
		}
	}
}

// TestLayerImportDirection enforces the core/app/infra/ui/cmd dependency
// direction on every file, tests included, and fails on stale allowlist
// entries so a grandfathered exception dies with its violation.
func TestLayerImportDirection(t *testing.T) {
	used := make(map[string]bool) // "relPath\x00importRel" of exercised allowlist entries
	for _, f := range repoFiles(t) {
		src, ok := classify(f.pkgDir)
		if !ok {
			continue // reported by TestAllPackagesClassified
		}
		for _, imp := range f.imports {
			rel, ok := moduleRel(imp.path)
			if !ok {
				continue
			}
			dst, ok := classify(rel)
			if !ok {
				continue // reported by TestAllPackagesClassified
			}
			if allowedImports[src][dst] {
				continue
			}
			if slices.Contains(legacyDirectionAllowlist[f.relPath], rel) {
				used[f.relPath+"\x00"+rel] = true
				continue
			}
			t.Errorf("%s:%d: %s may not import %s (%s -> %s forbidden)", f.relPath, imp.line, f.pkgDir, rel, src, dst)
		}
	}
	for relPath, imports := range legacyDirectionAllowlist {
		for _, rel := range imports {
			if !used[relPath+"\x00"+rel] {
				t.Errorf("legacyDirectionAllowlist[%q] entry %q is stale: the import is gone, delete the entry", relPath, rel)
			}
		}
	}
}

// TestCorePurity keeps non-test files of core packages free of the
// process/network/filesystem/database stdlib packages and of third-party
// modules, so the core layer stays pure computation.
func TestCorePurity(t *testing.T) {
	for _, f := range repoFiles(t) {
		if strings.HasSuffix(f.relPath, "_test.go") {
			continue // fixture IO in tests is fine
		}
		if l, ok := classify(f.pkgDir); !ok || l != layerCore {
			continue
		}
		for _, imp := range f.imports {
			if _, ok := moduleRel(imp.path); ok {
				continue // module-internal: covered by TestLayerImportDirection
			}
			if purityAllowed(f.pkgDir, imp.path) {
				continue
			}
			switch {
			case !stdlibImport(imp.path):
				t.Errorf("%s:%d: %s may not import %q (core layer bans third-party modules)", f.relPath, imp.line, f.pkgDir, imp.path)
			case coreForbiddenStdlib[imp.path]:
				t.Errorf("%s:%d: %s may not import %q (core stdlib purity)", f.relPath, imp.line, f.pkgDir, imp.path)
			}
		}
	}
}

// TestPackageMainOnlyInCmd pins package main declarations to cmd/fanout and
// forbids every package from importing cmd/... .
func TestPackageMainOnlyInCmd(t *testing.T) {
	for _, f := range repoFiles(t) {
		underCmdFanout := f.pkgDir == "cmd/fanout" || strings.HasPrefix(f.pkgDir, "cmd/fanout/")
		if f.pkgName == "main" && !underCmdFanout {
			t.Errorf("%s:%d: package main is only allowed under cmd/fanout", f.relPath, f.pkgLine)
		}
		for _, imp := range f.imports {
			if imp.path != scanned.modulePath+"/cmd" && !strings.HasPrefix(imp.path, scanned.modulePath+"/cmd/") {
				continue
			}
			rel, _ := moduleRel(imp.path)
			t.Errorf("%s:%d: %s may not import %s (importing cmd/... is forbidden)", f.relPath, imp.line, f.pkgDir, rel)
		}
	}
}

// TestScanSanity pins the scan itself: the walk must see both trees and the
// module path must come from go.mod, so a broken root resolution cannot make
// every other test pass vacuously on an empty file set.
func TestScanSanity(t *testing.T) {
	files := repoFiles(t)
	counts := map[string]int{}
	for _, f := range files {
		top, _, _ := strings.Cut(f.pkgDir, "/")
		counts[top]++
	}
	if counts["internal"] == 0 || counts["cmd"] == 0 {
		t.Errorf("scanRepo() saw internal=%d cmd=%d files, want both > 0", counts["internal"], counts["cmd"])
	}
	if scanned.modulePath == "" || strings.ContainsAny(scanned.modulePath, " \t\"") {
		t.Errorf("readModulePath() = %q, want a plausible module path", scanned.modulePath)
	}
}
