package arch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/godep-cruiser/archtest"
	"github.com/butaosuinu/godep-cruiser/config"
	"github.com/butaosuinu/godep-cruiser/cruiser"
)

// TestArchitecture enforces the layer dependency rules via godep-cruiser.
// The rule canon is godep-cruiser.json in this directory: the layer direction
// matrix (allowed rules, fail-closed - an import matching no allowed rule is
// an error), the per-layer complement rules with fix guidance, core stdlib
// purity, the tools/ stdlib-only pin, the cmd/... import ban, and the package
// main location check. godep-cruiser scans every *.go file under the repo
// root (tests included, build constraints not evaluated, skipping testdata/,
// vendor/, and dot- or underscore-prefixed directories), matching the scan
// the previous handwritten tests performed.
//
// godep-cruiser-baseline.json grandfathers violations that predate the rules;
// entries whose violation is gone fail as stale, forcing deletion. Current
// entries: internal/infra/team/path_test.go -> internal/app/briefing pins
// that the team DB path and briefing.Path derive the same parent slug;
// decouple that fixture to remove both entries.
func TestArchitecture(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	configuration, err := config.LoadFile("godep-cruiser.json")
	if err != nil {
		t.Fatalf("config.LoadFile(godep-cruiser.json) = %v, want nil", err)
	}
	baseline, err := cruiser.LoadBaselineFile("godep-cruiser-baseline.json")
	if err != nil {
		t.Fatalf("cruiser.LoadBaselineFile(godep-cruiser-baseline.json) = %v, want nil", err)
	}
	archtest.Check(t, configuration, cruiser.Options{
		ScanRoot:  root,
		GoModPath: filepath.Join(root, "go.mod"),
		Baseline:  &baseline,
	})
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

// TestInternalTreeShape pins internal/'s top level to the four layer
// directories plus this meta package. It checks the directory entries
// themselves, not just Go files, so a retired directory resurrected by stray
// non-Go files (a stale built bundle, fixtures) is flagged too - godep-cruiser
// only sees Go files and cannot cover this.
func TestInternalTreeShape(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	allowed := map[string]bool{"core": true, "app": true, "infra": true, "ui": true, "arch": true}
	entries, err := os.ReadDir(filepath.Join(root, "internal"))
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

// TestToolsTreeShape forbids bare *.go files directly under tools/: every
// repo meta-tool lives in its own subdirectory (its own package main, pinned
// stdlib-only by the tools-stdlib-only rule). The package-main-location rule
// only rejects package main there; this closes the gap for other package
// clauses.
func TestToolsTreeShape(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "tools"))
	if err != nil {
		t.Fatalf("ReadDir(tools) = %v, want nil", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			t.Errorf("tools/%s is a bare file (every tool lives in its own tools/<tool>/ directory)", e.Name())
		}
	}
}

// TestScanTreesPresent pins that internal/, cmd/, and tools/ each still hold
// Go files, so a renamed or deleted tree cannot make the godep-cruiser rules
// (whose from patterns would then match nothing) pass vacuously. godep-cruiser
// itself only errors when the whole scan root has zero Go files.
func TestScanTreesPresent(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	for _, top := range []string{"internal", "cmd", "tools"} {
		found := false
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil {
			t.Errorf("WalkDir(%s) = %v, want nil", top, err)
			continue
		}
		if !found {
			t.Errorf("%s/ holds no Go files, want at least one (the layer rules would pass vacuously)", top)
		}
	}
}
