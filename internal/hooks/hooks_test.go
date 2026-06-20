package hooks

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testLogger struct {
	stderr bytes.Buffer
}

func (l *testLogger) Warn(format string, a ...any) {
	fmt.Fprintf(&l.stderr, format+"\n", a...)
}

func (l *testLogger) Err(format string, a ...any) {
	fmt.Fprintf(&l.stderr, format+"\n", a...)
}

func (l *testLogger) Stderr() io.Writer {
	return &l.stderr
}

func TestFindUsesPriorityAndSkipsNonExecutable(t *testing.T) {
	projectRoot := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	writeHook(t, filepath.Join(xdg, "fanout", "hooks", string(WorktreeCreated)), "exit 0", 0o755)
	repoHook := writeHook(t, filepath.Join(projectRoot, ".fanout", "hooks", string(WorktreeCreated)), "exit 0", 0o755)
	topHook := writeHook(t, filepath.Join(projectRoot, ".fanout-hooks", string(WorktreeCreated)), "exit 0", 0o755)

	lg := &testLogger{}
	got, ok := Find(projectRoot, WorktreeCreated, lg)
	if !ok || got != topHook {
		t.Fatalf("Find() = %q, %v; want %q, true", got, ok, topHook)
	}

	if err := os.Chmod(topHook, 0o644); err != nil {
		t.Fatalf("Chmod(%q): %v", topHook, err)
	}
	got, ok = Find(projectRoot, WorktreeCreated, lg)
	if !ok || got != repoHook {
		t.Fatalf("Find() after chmod = %q, %v; want %q, true", got, ok, repoHook)
	}
	if !strings.Contains(lg.stderr.String(), "not executable") {
		t.Fatalf("stderr = %q, want not executable warning", lg.stderr.String())
	}
}

func TestEnvIncludesFanoutAndDMUXCompatibility(t *testing.T) {
	ctx := Context{
		ProjectRoot:  "/repo",
		Parent:       "100",
		IssueNum:     101,
		TaskID:       "api-client",
		Slug:         "api-client-101",
		Prompt:       "begin",
		Agent:        "codex",
		TmuxPaneID:   "%42",
		WorktreePath: "/repo/.fanout/worktrees/api-client-101",
		Branch:       "fanout/api-client-101",
		BaseBranch:   "main",
		TargetBranch: "main",
	}

	env := envMap(Env(ctx))

	for key, want := range map[string]string{
		"FANOUT_ROOT":          "/repo",
		"FANOUT_PARENT":        "100",
		"FANOUT_ISSUE_NUM":     "101",
		"FANOUT_TASK_ID":       "api-client",
		"FANOUT_SLUG":          "api-client-101",
		"FANOUT_PROMPT":        "begin",
		"FANOUT_AGENT":         "codex",
		"FANOUT_TMUX_PANE_ID":  "%42",
		"FANOUT_WORKTREE_PATH": "/repo/.fanout/worktrees/api-client-101",
		"FANOUT_BRANCH":        "fanout/api-client-101",
		"FANOUT_BASE_BRANCH":   "main",
		"FANOUT_TARGET_BRANCH": "main",
		"DMUX_ROOT":            "/repo",
		"DMUX_PANE_ID":         "fanout-100-api-client",
		"DMUX_SLUG":            "api-client-101",
		"DMUX_PROMPT":          "begin",
		"DMUX_AGENT":           "codex",
		"DMUX_TMUX_PANE_ID":    "%42",
		"DMUX_WORKTREE_PATH":   "/repo/.fanout/worktrees/api-client-101",
		"DMUX_BRANCH":          "fanout/api-client-101",
		"DMUX_TARGET_BRANCH":   "main",
	} {
		if got := env[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestEnvClearsManagedVariables(t *testing.T) {
	for _, key := range []string{
		"FANOUT_ISSUE_NUM",
		"FANOUT_TASK_ID",
		"FANOUT_TARGET_BRANCH",
		"DMUX_PANE_ID",
		"DMUX_BRANCH",
	} {
		t.Setenv(key, "stale")
	}

	env := envMap(Env(Context{ProjectRoot: "/repo"}))

	for _, key := range []string{
		"FANOUT_ISSUE_NUM",
		"FANOUT_TASK_ID",
		"FANOUT_TARGET_BRANCH",
		"DMUX_PANE_ID",
		"DMUX_BRANCH",
	} {
		if got := env[key]; got != "" {
			t.Fatalf("%s = %q, want cleared", key, got)
		}
	}
	if got := env["FANOUT_ROOT"]; got != "/repo" {
		t.Fatalf("FANOUT_ROOT = %q, want /repo", got)
	}
}

func TestRunBlockingPassesContextEnv(t *testing.T) {
	projectRoot := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "hook.out")
	t.Setenv("HOOK_OUT", outPath)
	writeHook(t, filepath.Join(projectRoot, ".fanout-hooks", string(WorktreeCreated)), `
printf '%s\n' "$FANOUT_PARENT|$FANOUT_ISSUE_NUM|$FANOUT_TASK_ID|$DMUX_SLUG|$(pwd)" > "$HOOK_OUT"
`, 0o755)

	result := RunBlocking(WorktreeCreated, Context{
		ProjectRoot: projectRoot,
		Parent:      "100",
		IssueNum:    101,
		TaskID:      "api-client",
		Slug:        "api-client-101",
	}, true, &testLogger{})
	if !result.OK() {
		t.Fatalf("RunBlocking() error: %v\n%s", result.Err, result.Output)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", outPath, err)
	}
	realProjectRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", projectRoot, err)
	}
	want := "100|101|api-client|api-client-101|" + realProjectRoot
	if got := strings.TrimSpace(string(data)); got != want {
		t.Fatalf("hook output = %q, want %q", got, want)
	}
}

func TestRunBlockingCapturesFailureOutput(t *testing.T) {
	projectRoot := t.TempDir()
	hookPath := writeHook(t, filepath.Join(projectRoot, ".fanout-hooks", string(PreMerge)), `
echo failing hook
exit 7
`, 0o755)

	result := RunBlocking(PreMerge, Context{ProjectRoot: projectRoot}, true, &testLogger{})
	if result.OK() {
		t.Fatal("RunBlocking() succeeded, want failure")
	}
	if result.Path != hookPath {
		t.Fatalf("Result.Path = %q, want %q", result.Path, hookPath)
	}
	if !strings.Contains(result.Err.Error(), "pre_merge hook") {
		t.Fatalf("Result.Err = %v, want hook context", result.Err)
	}
	if !strings.Contains(string(result.Output), "failing hook") {
		t.Fatalf("Result.Output = %q, want captured hook output", string(result.Output))
	}
}

func writeHook(t *testing.T, path, body string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	script := "#!/bin/sh\n" + strings.TrimLeft(body, "\n")
	if err := os.WriteFile(path, []byte(script), mode); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}
