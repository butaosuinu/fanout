package hooks

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type testLogger struct {
	stderr bytes.Buffer
}

func TestMain(m *testing.M) {
	if IsBackgroundRunnerRequest(os.Args[1:]) {
		os.Exit(RunBackgroundRunner(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
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

func TestLoadUserConfigParsesCommandHooks(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeHooksConfig(t, xdg, `{
  "hooks": {
    "worktree_created": [
      {
        "hooks": [
          {"type": "command", "command": " echo worktree ", "timeout": 10, "statusMessage": "Running worktree hook"},
          {"type": "notify", "command": "ignored"}
        ]
      }
    ],
    "before_pane_create": [
      {"hooks": [{"type": "command", "command": "echo default timeout"}]}
    ],
    "pre_merge": [
      {"hooks": [{"type": "command", "command": "echo invalid timeout", "timeout": 0}]}
    ],
    "unknown_event": [
      {"hooks": [{"type": "command", "command": "ignored"}]}
    ]
  }
}`)
	lg := &testLogger{}

	got := LoadUserConfig(lg)

	if got.Path != filepath.Join(xdg, "fanout", "hooks.json") {
		t.Fatalf("Config.Path = %q", got.Path)
	}
	if cmds := got.Events[WorktreeCreated]; len(cmds) != 1 {
		t.Fatalf("worktree_created commands = %#v, want 1 command", cmds)
	} else {
		if cmds[0].Command != "echo worktree" {
			t.Fatalf("Command = %q, want trimmed command", cmds[0].Command)
		}
		if cmds[0].Timeout != 10*time.Second {
			t.Fatalf("Timeout = %s, want 10s", cmds[0].Timeout)
		}
		if cmds[0].StatusMessage != "Running worktree hook" {
			t.Fatalf("StatusMessage = %q", cmds[0].StatusMessage)
		}
	}
	if cmds := got.Events[BeforePaneCreate]; len(cmds) != 1 || cmds[0].Timeout != defaultTimeout {
		t.Fatalf("before_pane_create commands = %#v, want default timeout", cmds)
	}
	if cmds := got.Events[PreMerge]; len(cmds) != 0 {
		t.Fatalf("pre_merge commands = %#v, want invalid timeout skipped", cmds)
	}
	for _, want := range []string{
		`unsupported type "notify"`,
		"timeout must be positive seconds",
		`unknown event "unknown_event"`,
	} {
		if !strings.Contains(lg.stderr.String(), want) {
			t.Fatalf("stderr = %q, want %q", lg.stderr.String(), want)
		}
	}
}

func TestLoadUserConfigSkipsWhenUserConfigDirIsUnavailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	if got := UserConfigPath(); got != "" {
		t.Fatalf("UserConfigPath() = %q, want empty path without user config dir", got)
	}
	cfg := LoadUserConfig(&testLogger{})
	if cfg.Path != "" {
		t.Fatalf("Config.Path = %q, want empty path", cfg.Path)
	}
	if len(cfg.Events) != 0 {
		t.Fatalf("Config.Events = %#v, want no hooks", cfg.Events)
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
		PaneID:       "%42",
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
	cfg := Config{Events: map[Type][]Command{
		WorktreeCreated: {{
			Command: `printf '%s\n' "$FANOUT_PARENT|$FANOUT_ISSUE_NUM|$FANOUT_TASK_ID|$DMUX_SLUG|$(pwd)" > "$HOOK_OUT"`,
			Timeout: time.Second,
		}},
	}}

	result := RunBlocking(WorktreeCreated, Context{
		ProjectRoot: projectRoot,
		Parent:      "100",
		IssueNum:    101,
		TaskID:      "api-client",
		Slug:        "api-client-101",
	}, cfg, &testLogger{})
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
	cfg := Config{Events: map[Type][]Command{
		PreMerge: {{
			Command: "echo failing hook; exit 7",
			Timeout: time.Second,
		}},
	}}

	result := RunBlocking(PreMerge, Context{ProjectRoot: projectRoot}, cfg, &testLogger{})
	if result.OK() {
		t.Fatal("RunBlocking() succeeded, want failure")
	}
	if result.Command != "echo failing hook; exit 7" {
		t.Fatalf("Result.Command = %q", result.Command)
	}
	if !strings.Contains(result.Err.Error(), "pre_merge hook command failed") {
		t.Fatalf("Result.Err = %v, want hook context", result.Err)
	}
	if !strings.Contains(string(result.Output), "failing hook") {
		t.Fatalf("Result.Output = %q, want captured hook output", string(result.Output))
	}
}

func TestRunBlockingTimesOut(t *testing.T) {
	cfg := Config{Events: map[Type][]Command{
		PreMerge: {{
			Command: "sleep 1",
			Timeout: time.Millisecond,
		}},
	}}

	result := RunBlocking(PreMerge, Context{ProjectRoot: t.TempDir()}, cfg, &testLogger{})
	if result.OK() {
		t.Fatal("RunBlocking() succeeded, want timeout")
	}
	if !strings.Contains(result.Err.Error(), "timed out after") {
		t.Fatalf("Result.Err = %v, want timeout", result.Err)
	}
}

func TestRunBlockingKillsShellChildrenOnTimeout(t *testing.T) {
	cfg := Config{Events: map[Type][]Command{
		PreMerge: {{
			Command: "sleep 2; printf late",
			Timeout: 10 * time.Millisecond,
		}},
	}}

	start := time.Now()
	result := RunBlocking(PreMerge, Context{ProjectRoot: t.TempDir()}, cfg, &testLogger{})
	elapsed := time.Since(start)
	if result.OK() {
		t.Fatal("RunBlocking() succeeded, want timeout")
	}
	if !strings.Contains(result.Err.Error(), "timed out after") {
		t.Fatalf("Result.Err = %v, want timeout", result.Err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("RunBlocking() took %s, want process group killed promptly", elapsed)
	}
}

func TestRunBlockingKillsProcessGroupOnInterrupt(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "hook.pid")
	t.Setenv("HOOK_PID", pidPath)
	cfg := Config{Events: map[Type][]Command{
		PreMerge: {{
			Command: `printf '%s' "$$" > "$HOOK_PID"; sleep 10`,
			Timeout: time.Minute,
		}},
	}}

	done := make(chan Result, 1)
	go func() {
		done <- RunBlocking(PreMerge, Context{ProjectRoot: t.TempDir()}, cfg, &testLogger{})
	}()

	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(pidPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook did not write pid before deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("Kill(SIGINT): %v", err)
	}

	select {
	case result := <-done:
		if result.OK() {
			t.Fatal("RunBlocking() succeeded, want interrupt error")
		}
		if !strings.Contains(result.Err.Error(), "interrupted by") {
			t.Fatalf("Result.Err = %v, want interrupt", result.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunBlocking() did not return after interrupt")
	}
}

func TestRunBackgroundStartsCommand(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "hook.out")
	t.Setenv("HOOK_OUT", outPath)
	cfg := Config{Events: map[Type][]Command{
		BeforePaneCreate: {{
			Command: `printf done > "$HOOK_OUT"`,
			Timeout: time.Second,
		}},
	}}

	result := RunBackground(BeforePaneCreate, Context{ProjectRoot: t.TempDir()}, cfg, &testLogger{})
	if !result.Ran || !result.OK() {
		t.Fatalf("RunBackground() = %+v, want ran successfully", result)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(outPath)
		if err == nil && string(data) == "done" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(outPath)
	t.Fatalf("background hook output = %q, err=%v; want done", string(data), err)
}

func TestRunBackgroundRunsCommandsInOrder(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "hook.out")
	t.Setenv("HOOK_OUT", outPath)
	cfg := Config{Events: map[Type][]Command{
		BeforePaneCreate: {
			{
				Command: `sleep 0.05; printf first >> "$HOOK_OUT"`,
				Timeout: time.Second,
			},
			{
				Command: `printf ',second' >> "$HOOK_OUT"`,
				Timeout: time.Second,
			},
		},
	}}

	result := RunBackground(BeforePaneCreate, Context{ProjectRoot: t.TempDir()}, cfg, &testLogger{})
	if !result.Ran || !result.OK() {
		t.Fatalf("RunBackground() = %+v, want ran successfully", result)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(outPath)
		if err == nil && string(data) == "first,second" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(outPath)
	t.Fatalf("background hook output = %q, err=%v; want first,second", string(data), err)
}

func TestRunBackgroundDoesNotHoldCapturedOutput(t *testing.T) {
	if os.Getenv("FANOUT_HOOK_STDIO_PROBE") == "1" {
		cfg := Config{Events: map[Type][]Command{
			BeforePaneCreate: {{
				Command: "sleep 1",
				Timeout: 2 * time.Second,
			}},
		}}
		result := RunBackground(BeforePaneCreate, Context{ProjectRoot: t.TempDir()}, cfg, &testLogger{})
		if !result.Ran || !result.OK() {
			t.Fatalf("RunBackground() = %+v, want runner started", result)
		}
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable(): %v", err)
	}
	cmd := exec.Command(exe, "-test.run", "^TestRunBackgroundDoesNotHoldCapturedOutput$")
	cmd.Env = append(os.Environ(), "FANOUT_HOOK_STDIO_PROBE=1")
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("probe failed after %s: %v\n%s", elapsed, err, out)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("probe took %s; background runner likely held captured stdio\n%s", elapsed, out)
	}
}

func writeHooksConfig(t *testing.T, xdg, body string) string {
	t.Helper()
	path := filepath.Join(xdg, "fanout", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}
