package arch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// purity, the tools/ stdlib-only pin, the cmd/... import ban, the package
// main location check, and the ban on bare *.go files at tree/layer roots.
// godep-cruiser scans every *.go file under the whole repo root (tests
// included, build constraints not evaluated, skipping testdata/, vendor/, and
// dot- or underscore-prefixed directories). That is deliberately wider than
// the internal/+cmd/+tools/ walk of the previous handwritten tests: a Go file
// outside those three trees must parse and is rejected outright by
// no-go-files-outside-trees, whatever it imports, instead of escaping every
// rule.
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

// TestScanTreesPresent pins that internal/, cmd/, and tools/ each still hold
// Go files the godep-cruiser scanner actually sees, so a renamed or deleted
// tree (or one holding only skipped fixtures) cannot make the rules - whose
// from patterns would then match nothing - pass vacuously. godep-cruiser
// itself only errors when the whole scan root has zero Go files. The walk
// applies the scanner's skip rules; a Go file under testdata/, vendor/, or a
// dot-/underscore-prefixed directory must not count as presence.
func TestScanTreesPresent(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	for _, top := range []string{"internal", "cmd", "tools"} {
		topPath := filepath.Join(root, top)
		found := false
		err := filepath.WalkDir(topPath, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if p != topPath && (name == "testdata" || name == "vendor" ||
					strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".go") {
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
			t.Errorf("%s/ holds no scannable Go files, want at least one (the layer rules would pass vacuously)", top)
		}
	}
}

// TestRuleSeveritiesPinned guards the guard: godep-cruiser defaults an omitted
// rule severity to warn, and archtest only fails the test on error-severity
// violations, so a rule missing "severity": "error" (or a dropped
// allowedSeverity) would silently demote its violations to log lines.
func TestRuleSeveritiesPinned(t *testing.T) {
	configuration, err := config.LoadFile("godep-cruiser.json")
	if err != nil {
		t.Fatalf("config.LoadFile(godep-cruiser.json) = %v, want nil", err)
	}
	if configuration.AllowedSeverity != config.SeverityError {
		t.Errorf("allowedSeverity = %q, want %q (the fail-closed matrix must fail the build)", configuration.AllowedSeverity, config.SeverityError)
	}
	for _, rule := range configuration.Forbidden {
		if rule.Severity != config.SeverityError {
			t.Errorf("forbidden rule %q severity = %q, want %q", rule.Name, rule.Severity, config.SeverityError)
		}
	}
	for _, rule := range configuration.Required {
		if rule.Severity != config.SeverityError {
			t.Errorf("required rule %q severity = %q, want %q", rule.Name, rule.Severity, config.SeverityError)
		}
	}
}

// TestPurityDenylistConsistent pins the relationship between the three
// hand-copied core purity denylists in godep-cruiser.json: JSON cannot share
// one list across rules, so this keeps the copies from drifting. The agent
// list must be the base list minus os and os/exec, and the planspec list the
// base minus os; a package added to the base rule alone would otherwise leave
// the exempted subtrees unguarded (their from.pathNot removes them from the
// base rule entirely).
func TestPurityDenylistConsistent(t *testing.T) {
	configuration, err := config.LoadFile("godep-cruiser.json")
	if err != nil {
		t.Fatalf("config.LoadFile(godep-cruiser.json) = %v, want nil", err)
	}
	lists := make(map[string][]string)
	for _, rule := range configuration.Forbidden {
		if !strings.HasPrefix(rule.Name, "core-purity-stdlib") {
			continue
		}
		if len(rule.To.Path) != 1 {
			t.Fatalf("rule %q to.path has %d patterns, want 1", rule.Name, len(rule.To.Path))
		}
		lists[rule.Name] = parseDenylist(t, rule.Name, rule.To.Path[0])
	}
	base, ok := lists["core-purity-stdlib"]
	if !ok {
		t.Fatal("rule core-purity-stdlib not found")
	}
	for rule, drops := range map[string][]string{
		"core-purity-stdlib-agent":    {"os", "os/exec"},
		"core-purity-stdlib-planspec": {"os"},
	} {
		got, ok := lists[rule]
		if !ok {
			t.Errorf("rule %s not found", rule)
			continue
		}
		want := slices.DeleteFunc(slices.Clone(base), func(s string) bool { return slices.Contains(drops, s) })
		if !slices.Equal(got, want) {
			t.Errorf("%s denylist = %v, want base minus %v = %v", rule, got, drops, want)
		}
	}
}

// parseDenylist splits a ^(a|b|c)$ exact-match alternation into its sorted
// members.
func parseDenylist(t *testing.T, rule, pattern string) []string {
	t.Helper()
	inner, okPrefix := strings.CutPrefix(pattern, "^(")
	inner, okSuffix := strings.CutSuffix(inner, ")$")
	if !okPrefix || !okSuffix {
		t.Fatalf("rule %q to.path %q is not in ^(a|b)$ exact-match form", rule, pattern)
	}
	members := strings.Split(inner, "|")
	slices.Sort(members)
	return members
}
