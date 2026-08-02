package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newCleanRepo creates a throwaway git repo with a single commit, chdirs into
// it, and isolates git from user/system config. loadDiff diffs base..working
// tree, so a clean checkout guarantees an empty diff regardless of the state of
// the repo the test binary was built in. t.Chdir/t.Setenv mark the test serial.
func newCleanRepo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	git("add", "README.md")
	git("commit", "-m", "seed")

	t.Chdir(dir)
	// run()'s own git calls inherit the process env and cwd; isolate them too.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
}

// TestRunFlagValidation pins the operational-error exits: a bad flag, an unknown
// --format, and an unknown --fail-at all return 2 before any git call.
func TestRunFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"--nope"}},
		{name: "invalid format", args: []string{"--format", "bogus"}},
		{name: "invalid fail-at level", args: []string{"--fail-at", "bogus"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tt.args, &stdout, &stderr); got != 2 {
				t.Errorf("run(%v) = %d, want 2 (stderr: %s)", tt.args, got, stderr.String())
			}
		})
	}
}

// TestRunHelp pins that -h/--help is not an operational error: flag prints usage
// and run returns 0, never 2.
func TestRunHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "long help flag", args: []string{"--help"}},
		{name: "short help flag", args: []string{"-h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tt.args, &stdout, &stderr); got != 0 {
				t.Errorf("run(%v) = %d, want 0 (stderr: %s)", tt.args, got, stderr.String())
			}
		})
	}
}

// TestRunFailAtExitCode pins the advisory-gate semantics against an empty diff
// in a clean throwaway repo (--base HEAD -> base..working tree is empty, level
// none): --fail-at low stays 0 because none < low, while --fail-at none exits 1
// because none >= none. The default (no --fail-at) always exits 0.
func TestRunFailAtExitCode(t *testing.T) {
	newCleanRepo(t)
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "no fail-at always exits 0", args: []string{"--base", "HEAD"}, want: 0},
		{name: "level below fail-at exits 0", args: []string{"--base", "HEAD", "--fail-at", "low"}, want: 0},
		{name: "level at fail-at exits 1", args: []string{"--base", "HEAD", "--fail-at", "none"}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tt.args, &stdout, &stderr); got != tt.want {
				t.Errorf("run(%v) = %d, want %d (stderr: %s)", tt.args, got, tt.want, stderr.String())
			}
		})
	}
}

// TestRunEmitsFormat checks the happy path writes the requested format to stdout
// on an empty diff in a clean throwaway repo.
func TestRunEmitsFormat(t *testing.T) {
	newCleanRepo(t)
	tests := []struct {
		name       string
		format     string
		wantSubstr string
	}{
		{name: "text", format: "text", wantSubstr: "Review risk: NONE"},
		{name: "markdown", format: "markdown", wantSubstr: "<!-- review-risk -->"},
		{name: "json", format: "json", wantSubstr: `"level": "none"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run([]string{"--base", "HEAD", "--format", tt.format}, &stdout, &stderr); got != 0 {
				t.Fatalf("run(--format %s) = %d, want 0 (stderr: %s)", tt.format, got, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantSubstr) {
				t.Errorf("run(--format %s) stdout missing %q, got:\n%s", tt.format, tt.wantSubstr, stdout.String())
			}
		})
	}
}

// TestLoadDiffPreservesNULDelimitedBootstrapPath verifies the Git boundary, not
// just the parser: a legal tab in an ancestor directory must still trigger S5.
func TestLoadDiffPreservesNULDelimitedBootstrapPath(t *testing.T) {
	newCleanRepo(t)
	path := filepath.Join("docs", "x\ty", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("review contract\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	if _, err := runGit("add", "-N", "--", path); err != nil {
		t.Fatalf("git add -N %q: %v", path, err)
	}

	d, err := loadDiff("HEAD")
	if err != nil {
		t.Fatalf("loadDiff(HEAD): %v", err)
	}
	if len(d.Files) != 1 || d.Files[0].Path != path {
		t.Fatalf("loadDiff files = %#v, want exact path %q", d.Files, path)
	}
	if d.Files[0].Added != 1 || d.Files[0].Deleted != 0 {
		t.Fatalf("loadDiff stats for %q = +%d/-%d, want +1/-0", path, d.Files[0].Added, d.Files[0].Deleted)
	}
	for _, reason := range criticalReasons(d) {
		if reason.Signal == sigReviewGateChanged && reason.File == path {
			return
		}
	}
	t.Fatalf("criticalReasons(%q) missing %s: %#v", path, sigReviewGateChanged, criticalReasons(d))
}

// TestLoadDiffDecodesQuotedUnifiedPath verifies the third Git boundary used by
// content signals. Git C-quotes a tab in ---/+++ headers; loadDiff must still
// key added and removed lines by the same exact path as --name-status -z.
func TestLoadDiffDecodesQuotedUnifiedPath(t *testing.T) {
	newCleanRepo(t)
	path := filepath.Join("internal", "core", "x\ty", "foo_test.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	content := "package x\n\nfunc probe() {\n\tt.Skip(\"wip\")\n\trequireToken(r)\n}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	if _, err := runGit("add", "-N", "--", path); err != nil {
		t.Fatalf("git add -N %q: %v", path, err)
	}

	d, err := loadDiff("HEAD")
	if err != nil {
		t.Fatalf("loadDiff(HEAD): %v", err)
	}
	if got := d.AddedLines[path]; len(got) == 0 {
		t.Fatalf("AddedLines[%q] = %#v, want exact-path content", path, got)
	}
	if !hasReason(criticalReasons(d), sigSkipAdded, path) {
		t.Fatalf("criticalReasons(%q) missing %s: %#v", path, sigSkipAdded, criticalReasons(d))
	}
	if !hasReason(invariantReasons(d), sigInvariantHit, path) {
		t.Fatalf("invariantReasons(%q) missing %s: %#v", path, sigInvariantHit, invariantReasons(d))
	}
}

func hasReason(reasons []Reason, signal, path string) bool {
	for _, reason := range reasons {
		if reason.Signal == signal && reason.File == path {
			return true
		}
	}
	return false
}
