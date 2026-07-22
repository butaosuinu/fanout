package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/run"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func installFakeExecutable(t *testing.T, name string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then printf '2.1.207 (Claude Code)\\n'; fi\nexit 0\n"), 0o755); err != nil {
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
		{name: "parent issue", args: []string{"123"}, want: false},
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
	t.Setenv(fanouttui.EnhancedKeysEnv, "")
	got := tuiLaunchCommand("fanout", "/tmp/My Repo")
	if !strings.HasPrefix(got, "cd '/tmp/My Repo' && ") {
		t.Fatalf("tuiLaunchCommand() = %q, want cd into quoted project root", got)
	}
	if strings.HasSuffix(got, " tui") {
		t.Fatalf("tuiLaunchCommand() = %q, did not expect tui subcommand suffix", got)
	}
}

func TestTUILaunchCommandForwardsEnhancedKeysValue(t *testing.T) {
	// The current value is always forwarded — including the empty (default-on)
	// case — so the relaunched console matches this process and overrides any
	// stale FANOUT_TUI_ENHANCED_KEYS captured in the tmux session environment.
	for _, value := range []string{"", "0", "1"} {
		t.Setenv(fanouttui.EnhancedKeysEnv, value)
		got := tuiLaunchCommand("fanout", "/tmp/repo")
		if !strings.Contains(got, " && "+fanouttui.EnhancedKeysEnv+"="+run.ShellQuote(value)+" ") {
			t.Fatalf("tuiLaunchCommand() = %q with %s=%q, want forwarded env prefix", got, fanouttui.EnhancedKeysEnv, value)
		}
	}
}
