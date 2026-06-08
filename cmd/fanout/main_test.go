package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
)

func TestExecutePlanSleepsBetweenDryRunIssues(t *testing.T) {
	dir := t.TempDir()

	oldSleep := sleepBetweenIssues
	var sleeps []time.Duration
	sleepBetweenIssues = func(d time.Duration) {
		sleeps = append(sleeps, d)
	}
	t.Cleanup(func() { sleepBetweenIssues = oldSleep })

	cfg := &cliflags.Config{
		Agent:        "claude",
		DryRun:       true,
		SleepBetween: 0.25,
	}
	lg := log.NewWith(io.Discard, io.Discard, false)
	info := &fanoutruntime.Info{
		Session:     "test",
		Target:      "%caller",
		ProjectRoot: dir,
	}
	targets := []ghissue.Issue{
		{Number: 1, Title: "one", State: "OPEN", Body: "body"},
		{Number: 2, Title: "two", State: "OPEN", Body: "body"},
	}

	result := executePlan(cfg, lg, info, ghissue.Runner{}, targets, settings.Defaults(), nil, nil, log.Palette{})

	if result.Created != 2 || result.Failed != 0 {
		t.Fatalf("executePlan result = %+v, want 2 created and 0 failed", result)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls = %d, want 1", len(sleeps))
	}
	if want := 250 * time.Millisecond; sleeps[0] != want {
		t.Fatalf("sleep duration = %s, want %s", sleeps[0], want)
	}
}

func TestCreatePaneForIssueFailsWhenWorktreeAppearsDuringLaunch(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init")
	installFakeExecutable(t, "claude")
	worktreePath := filepath.Join(repo, ".fanout", "worktrees", "duplicate-title-77")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &cliflags.Config{
		Agent:      "claude",
		ParentRef:  "81",
		BaseBranch: "main",
		NoRefresh:  true,
	}
	var stderr bytes.Buffer
	lg := log.NewWith(io.Discard, &stderr, false)
	info := &fanoutruntime.Info{
		Session:     "test",
		Target:      "%caller",
		ProjectRoot: repo,
	}
	issue := ghissue.Issue{Number: 77, Title: "Duplicate Title", State: "OPEN", Body: "body"}

	if createPaneForIssue(cfg, lg, info, issue, settings.Defaults(), nil, false, log.Palette{}) {
		t.Fatal("createPaneForIssue() = true, want false for launch-time worktree collision")
	}
	if got := stderr.String(); !strings.Contains(got, "worktree path already exists during launch") {
		t.Fatalf("stderr = %q, want launch collision message", got)
	}
}

func TestCreatePaneForIssueRejectsUnsupportedRefreshBaseInDryRun(t *testing.T) {
	repo := t.TempDir()
	cfg := &cliflags.Config{
		Agent:      "claude",
		ParentRef:  "81",
		BaseBranch: "refs/heads/main",
		DryRun:     true,
	}
	var stderr bytes.Buffer
	lg := log.NewWith(io.Discard, &stderr, false)
	info := &fanoutruntime.Info{
		Session:     "test",
		Target:      "%caller",
		ProjectRoot: repo,
	}
	issue := ghissue.Issue{Number: 77, Title: "Bad Base", State: "OPEN", Body: "body"}

	if createPaneForIssue(cfg, lg, info, issue, settings.Defaults(), nil, false, log.Palette{}) {
		t.Fatal("createPaneForIssue() = true, want false for unsupported refresh base")
	}
	if got := stderr.String(); !strings.Contains(got, `base branch "refs/heads/main" is not refreshable`) {
		t.Fatalf("stderr = %q, want unsupported base message", got)
	}
}

func TestLoadRunStateIgnoresLockFileWhenNoWorktreeIsPrepared(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init")

	cfg := &cliflags.Config{}
	lg := log.NewWith(io.Discard, io.Discard, false)
	_, recorder, code := loadRunState(cfg, repo, lg)
	if code != exitcode.OK {
		t.Fatalf("loadRunState code = %d, want %d", code, exitcode.OK)
	}
	if recorder == nil {
		t.Fatal("loadRunState returned nil recorder for live run")
	}
	t.Cleanup(func() { _ = recorder.Unlock() })

	if _, err := os.Stat(filepath.Join(repo, ".fanout", "state.json.lock")); err != nil {
		t.Fatalf("state lock was not created: %v", err)
	}
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exclude), ".fanout/state.json.lock\n") {
		t.Fatalf("exclude = %q, want state lock pattern", exclude)
	}
}

func installFakeExecutable(t *testing.T, name string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestInvokedCommandNameUsesBinaryName(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "path binary", args: []string{"/tmp/build/fanout-go"}, want: "fanout-go"},
		{name: "relative binary", args: []string{"./fanout-go"}, want: "fanout-go"},
		{name: "empty args", args: nil, want: "fanout"},
		{name: "empty argv0", args: []string{""}, want: "fanout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := invokedCommandName(tc.args); got != tc.want {
				t.Fatalf("invokedCommandName(%#v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func gitCmdTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func TestIsVersionRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "long", args: []string{"--version"}, want: true},
		{name: "short", args: []string{"-V"}, want: true},
		{name: "mixed with parent", args: []string{"123", "--version"}, want: false},
		{name: "unknown capital", args: []string{"-v"}, want: false},
		{name: "empty", args: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isVersionRequest(tc.args); got != tc.want {
				t.Fatalf("isVersionRequest(%#v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestIsCheckUpdateRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "flag", args: []string{"--check-update"}, want: true},
		{name: "subcommand", args: []string{"check-update"}, want: true},
		{name: "mixed with parent", args: []string{"123", "--check-update"}, want: false},
		{name: "short not supported", args: []string{"-U"}, want: false},
		{name: "empty", args: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCheckUpdateRequest(tc.args); got != tc.want {
				t.Fatalf("isCheckUpdateRequest(%#v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestIsTUIRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "empty args default to tui", args: nil, want: true},
		{name: "subcommand", args: []string{"tui"}, want: true},
		{name: "parent issue", args: []string{"123"}, want: false},
		{name: "tui with extra arg", args: []string{"tui", "--debug"}, want: false},
		{name: "help", args: []string{"--help"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTUIRequest(tc.args); got != tc.want {
				t.Fatalf("isTUIRequest(%#v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestIsUpdateRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "subcommand", args: []string{"update"}, want: true},
		{name: "subcommand with flags", args: []string{"update", "--version", "v1.2.3"}, want: true},
		{name: "check-update", args: []string{"check-update"}, want: false},
		{name: "top-level version", args: []string{"--version"}, want: false},
		{name: "empty", args: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUpdateRequest(tc.args); got != tc.want {
				t.Fatalf("isUpdateRequest(%#v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestVersionLineUsesInjectedValues(t *testing.T) {
	oldVersion := version
	oldCommit := commit
	version = "v1.2.3"
	commit = "abc1234"
	t.Cleanup(func() {
		version = oldVersion
		commit = oldCommit
	})

	if got, want := versionLine(), "fanout v1.2.3 (abc1234)"; got != want {
		t.Fatalf("versionLine() = %q, want %q", got, want)
	}
}

func TestFanoutTUISessionNameIsStableAndSanitized(t *testing.T) {
	got := fanoutTUISessionName("/tmp/My Repo")
	if !strings.HasPrefix(got, "fanout-my-repo-") {
		t.Fatalf("fanoutTUISessionName() = %q, want sanitized prefix", got)
	}
	if len(got) != len("fanout-my-repo-")+8 {
		t.Fatalf("fanoutTUISessionName() = %q, want 8 hex suffix", got)
	}
	if got != fanoutTUISessionName("/tmp/My Repo") {
		t.Fatalf("fanoutTUISessionName() is not stable")
	}
}

func TestTUILaunchCommandChangesToProjectRoot(t *testing.T) {
	got := tuiLaunchCommand("fanout", "/tmp/My Repo")
	if !strings.HasPrefix(got, "cd '/tmp/My Repo' && ") {
		t.Fatalf("tuiLaunchCommand() = %q, want cd into quoted project root", got)
	}
	if !strings.HasSuffix(got, " tui") {
		t.Fatalf("tuiLaunchCommand() = %q, want tui subcommand suffix", got)
	}
}
