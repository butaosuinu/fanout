package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAcceptsExistingSessionOverride(t *testing.T) {
	repo := newGitRepo(t)
	installTmuxShim(t)
	t.Setenv("TMUX_PANE", "%caller")
	chdir(t, repo)

	info, err := Resolve("target")
	if err != nil {
		t.Fatalf("Resolve() failed: %v", err)
	}
	if info.Session != "target" {
		t.Fatalf("Session = %q, want target", info.Session)
	}
	if info.Target != "target" {
		t.Fatalf("Target = %q, want target", info.Target)
	}
	wantRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if info.ProjectRoot != wantRoot {
		t.Fatalf("ProjectRoot = %q, want %q", info.ProjectRoot, wantRoot)
	}
}

func TestResolveTargetsInvokingPaneByDefault(t *testing.T) {
	repo := newGitRepo(t)
	installTmuxShim(t)
	t.Setenv("TMUX_PANE", "%caller")
	chdir(t, repo)

	info, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve() failed: %v", err)
	}
	if info.Session != "current" {
		t.Fatalf("Session = %q, want current", info.Session)
	}
	if info.Target != "%caller" {
		t.Fatalf("Target = %q, want invoking pane", info.Target)
	}
}

func TestResolveFallsBackToTmuxPaneID(t *testing.T) {
	repo := newGitRepo(t)
	installTmuxShim(t)
	t.Setenv("TMUX_PANE", "")
	chdir(t, repo)

	info, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve() failed: %v", err)
	}
	if info.Target != "%fallback" {
		t.Fatalf("Target = %q, want fallback pane id", info.Target)
	}
}

func TestResolveRejectsMissingSessionOverride(t *testing.T) {
	repo := newGitRepo(t)
	installTmuxShim(t)
	t.Setenv("TMUX_PANE", "%caller")
	chdir(t, repo)

	_, err := Resolve("missing")
	if err == nil {
		t.Fatal("Resolve() returned nil error")
	}
	if !strings.Contains(err.Error(), `tmux session "missing" is not available`) {
		t.Fatalf("error = %q, want missing session message", err)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	return repo
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

func installTmuxShim(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	tmuxPath := filepath.Join(binDir, "tmux")
	script := `#!/bin/sh
case "$1" in
  display-message)
    case "${3:-}" in
      "#{pane_id}") printf '%%fallback\n' ;;
      *) printf 'current\n' ;;
    esac
    exit 0
    ;;
  has-session)
    if [ "${3:-}" = target ]; then
      exit 0
    fi
    printf 'no such session: %s\n' "${3:-}" >&2
    exit 1
    ;;
esac
exit 2
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX", "test-tmux")
}
