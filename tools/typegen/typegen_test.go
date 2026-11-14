package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGeneratedTypes(t *testing.T) {
	root := repoRootDir(t)
	t.Chdir(root)
	files, err := generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, outputDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(files) {
		t.Errorf("generated directory has %d entries, want %d; remove stale outputs", len(entries), len(files))
	}
	for name, generated := range files {
		t.Run(name, func(t *testing.T) {
			committed, err := os.ReadFile(filepath.Join(root, outputDir, name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(generated, committed) {
				t.Error("generated types drifted; run make gen-types and commit the result")
			}
		})
	}
	checkWireTypes(t, files)
}

func TestGenerateWriteError(t *testing.T) {
	t.Chdir(repoRootDir(t))
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := generate(blocked); err == nil {
		t.Fatal("generate accepted an unwritable output directory")
	}
}

func checkWireTypes(t *testing.T, files map[string][]byte) {
	t.Helper()
	for name, fields := range map[string][]string{
		"sessionview.gen.ts": {
			"sessions: Session[] | null;", "panes: PaneView[] | null;",
			"prs: PRRef[] | null;", "blockers: Status[];", "sourceParent?: string;",
			"runtimeState: string;", "derived: PaneDerived;", "sort: PaneSortKeys;",
		},
		"ghissue.gen.ts": {"mergedAt: string | null;", "stack?: PRStack;", "entries?: PRStackEntry[];"},
		"dashboard.gen.ts": {
			"export interface PeekResponse", "export interface PlanResponse", "export interface DiffResponse",
			"files: DiffFileEntry[];", "additions: number | null;", "deletions: number | null;",
			"omittedReason: '' | 'binary' | 'tooLarge' | 'collectionLimit' | 'responseLimit';",
		},
	} {
		for _, field := range fields {
			if !bytes.Contains(files[name], []byte(field)) {
				t.Errorf("%s missing wire contract %q", name, field)
			}
		}
	}
	privateField := regexp.MustCompile(`(?mi)^\s*(savedPane|sourceProjectRoots?|closeUnconfirmed)\??:`)
	for name, data := range files {
		if privateField.Match(data) {
			t.Errorf("%s exposes a non-JSON field", name)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(line, "import ") &&
				(!strings.HasPrefix(line, "import type ") || !strings.Contains(line, "from './") || !strings.HasSuffix(line, ".gen';")) {
				t.Errorf("%s imports outside generated types: %s", name, line)
			}
		}
	}
}

// Match tools/reviewrisk's repoRootDir: go test starts in the package directory.
func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find repository go.mod")
		}
		dir = parent
	}
}
