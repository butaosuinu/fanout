package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestIsDashboardRequest(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "bare dashboard verb", args: []string{"dashboard"}, want: true},
		{name: "dashboard with web flag", args: []string{"dashboard", "--web"}, want: true},
		{name: "status flag is not dashboard", args: []string{"--status", "1"}, want: false},
		{name: "issue number is not dashboard", args: []string{"100"}, want: false},
		{name: "no args is not dashboard", args: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDashboardRequest(tt.args); got != tt.want {
				t.Errorf("isDashboardRequest(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestDashboardHerdrPeekAdmissionReopensAndBindsOwnedSessionPerRequest(t *testing.T) {
	ready := false
	calls := 0
	opener := func(root string) (*herdrrun.OwnedSession, error) {
		calls++
		if root != "/repo" || !ready {
			return nil, errors.New("owned session unavailable")
		}
		return &herdrrun.OwnedSession{Session: "owned", SocketPath: "/tmp/owned.sock"}, nil
	}
	owns, _ := dashboardHerdrPeekPorts("/repo", opener)
	pane := sessionview.PaneView{SavedPane: state.Pane{
		Backend: backend.Herdr, PaneID: "w1:p1", HerdrWorkspaceID: "w1",
		HerdrWorkspaceLabel: "owned-label", HerdrTerminalID: "term-1",
		HerdrSession: "owned", HerdrSocketPath: "/tmp/owned.sock", WorktreePath: "/repo",
	}}
	admitted := owns(pane)
	if admitted {
		t.Fatal("missing owned session was admitted")
	}
	ready = true
	admitted = owns(pane)
	if admitted || calls != 2 {
		t.Fatalf("route-only admission = %t calls=%d, want false/2 without an exact backend binding", admitted, calls)
	}
}

func TestParseDashboardFlags(t *testing.T) {
	lg := log.New(false)

	f, code := parseDashboardFlags([]string{"--web", "--open", "--no-token", "--no-keybind", "--port", "8787"}, lg)
	if int(code) != 0 {
		t.Fatalf("parse code = %d, want 0", code)
	}
	if !f.open || !f.noToken || !f.noKeybind || f.port != 8787 {
		t.Fatalf("parsed flags = %+v", f)
	}

	if _, code := parseDashboardFlags([]string{"--bogus"}, lg); int(code) != 2 {
		t.Fatalf("unknown flag code = %d, want 2 (Invocation)", code)
	}
	if _, code := parseDashboardFlags([]string{"--port"}, lg); int(code) != 1 {
		t.Fatalf("missing port value code = %d, want 1 (Env)", code)
	}
	if _, code := parseDashboardFlags([]string{"--port", "70000"}, lg); int(code) != 1 {
		t.Fatalf("out-of-range port code = %d, want 1 (Env)", code)
	}
	if _, code := parseDashboardFlags([]string{"--port", "abc"}, lg); int(code) != 1 {
		t.Fatalf("non-numeric port code = %d, want 1 (Env)", code)
	}
}

func TestParsePort(t *testing.T) {
	if n, err := parsePort("0"); err != nil || n != 0 {
		t.Fatalf("parsePort(0) = %d,%v", n, err)
	}
	if n, err := parsePort("65535"); err != nil || n != 65535 {
		t.Fatalf("parsePort(65535) = %d,%v", n, err)
	}
	for _, bad := range []string{"", "65536", "-1", "1.5", "x"} {
		if _, err := parsePort(bad); err == nil {
			t.Errorf("parsePort(%q) should error", bad)
		}
	}
}

func TestNoKeybindOverride(t *testing.T) {
	if noKeybindOverride(false) != nil {
		t.Fatal("no --no-keybind -> nil (defer to lower layers)")
	}
	if v := noKeybindOverride(true); v == nil || *v != false {
		t.Fatalf("--no-keybind -> *false, got %v", v)
	}
}

func TestBindDashboardKeyForBackendIsTmuxOnly(t *testing.T) {
	argsPath := installTUIDashboardTmuxShim(t)
	bindDashboardKeyForBackend(discardLogger(), true, backend.Selection{Name: backend.Herdr})
	if body, err := os.ReadFile(argsPath); err == nil {
		t.Fatalf("herdr dashboard bound tmux keys:\n%s", body)
	} else if !os.IsNotExist(err) {
		t.Fatalf("read tmux argv log: %v", err)
	}

	bindDashboardKeyForBackend(discardLogger(), true, backend.Selection{Name: backend.Tmux})
	log := readTUITmuxLog(t, argsPath)
	for _, want := range []string{"bind-key\nD\nrun-shell", "bind-key\n-n\nF12\nrun-shell", "bind-key\nM\ndisplay-popup"} {
		if !tmuxLogHasCommand(log, want) {
			t.Fatalf("tmux dashboard log missing %q:\n%s", want, log)
		}
	}
}

func TestOpenBrowserBestEffortShowsDashboardURL(t *testing.T) {
	browser, status := stubDashboardOpenHooks(t, nil)

	openBrowserBestEffort("http://127.0.0.1:1234/?token=abc", discardLogger())

	if browser.openedURL != "http://127.0.0.1:1234/?token=abc" {
		t.Fatalf("opened URL = %q", browser.openedURL)
	}
	if got := status.message; !strings.Contains(got, "fanout dashboard: http://127.0.0.1:1234/?token=abc") {
		t.Fatalf("status message = %q", got)
	}
}

func TestOpenBrowserBestEffortShowsOpenFailure(t *testing.T) {
	browser, status := stubDashboardOpenHooks(t, errors.New("launcher unavailable"))

	openBrowserBestEffort("http://127.0.0.1:4321/?token=def", discardLogger())

	if browser.openedURL != "http://127.0.0.1:4321/?token=def" {
		t.Fatalf("opened URL = %q", browser.openedURL)
	}
	if got := status.message; !strings.Contains(got, "browser open failed") || !strings.Contains(got, "http://127.0.0.1:4321/?token=def") {
		t.Fatalf("status message = %q", got)
	}
}

func TestResolveRootFromTop(t *testing.T) {
	has := func(roots ...string) func(string) bool {
		set := map[string]bool{}
		for _, r := range roots {
			set[r] = true
		}
		return func(dir string) bool { return set[dir] }
	}

	// Current worktree has its own state -> use it.
	if got := resolveRootFromTop("/repo", has("/repo")); got != "/repo" {
		t.Fatalf("own-state: got %q want /repo", got)
	}
	// Fanned child pane under .fanout/worktrees/<slug> -> recover the parent.
	child := "/repo/.fanout/worktrees/fix-bug-12"
	if got := resolveRootFromTop(child, has("/repo")); got != "/repo" {
		t.Fatalf("child pane: got %q want /repo", got)
	}
	// Nested UNRELATED worktree (no .fanout/worktrees marker) must NOT climb into
	// an ancestor checkout that merely has its own state.
	dev := "/repo/.dmux/worktrees/dev"
	if got := resolveRootFromTop(dev, has("/repo")); got != dev {
		t.Fatalf("nested worktree: got %q want %q (must not escape to ancestor)", got, dev)
	}
	// Never-fanned worktree -> falls back to itself.
	if got := resolveRootFromTop("/fresh", has()); got != "/fresh" {
		t.Fatalf("fresh repo: got %q want /fresh", got)
	}
}

func TestResolveDisplayProjectRootFromFallsBackToTmuxPaneState(t *testing.T) {
	has := func(roots ...string) func(string) bool {
		set := map[string]bool{}
		for _, r := range roots {
			set[r] = true
		}
		return func(dir string) bool { return set[dir] }
	}

	tmuxTop := func(root string) func() (string, error) {
		return func() (string, error) { return root, nil }
	}

	// Codex/dmux can execute commands with a process cwd in a helper worktree
	// while tmux still reports the visible pane path in the repo that owns the
	// fanout state. Display commands should show the state-owning root instead
	// of silently rendering an empty Session list from the helper worktree.
	if got := resolveDisplayProjectRootFrom("/repo/.dmux/worktrees/codex", tmuxTop("/repo"), has("/repo")); got != "/repo" {
		t.Fatalf("tmux fallback root = %q want /repo", got)
	}

	// The process cwd still wins when it already has fanout state; this keeps a
	// normal checked-out worktree from being hijacked by a stale tmux cwd.
	if got := resolveDisplayProjectRootFrom("/worktree", tmuxTop("/repo"), has("/worktree", "/repo")); got != "/worktree" {
		t.Fatalf("cwd-owned root = %q want /worktree", got)
	}

	// A tmux pane that is itself in a fanned child worktree should resolve to
	// the owner checkout that holds .fanout/state.json.
	child := "/repo/.fanout/worktrees/child-1"
	if got := resolveDisplayProjectRootFrom("/empty", tmuxTop(child), has("/repo")); got != "/repo" {
		t.Fatalf("tmux child fallback root = %q want /repo", got)
	}

	// If neither side has fanout state, stay with the command cwd rather than
	// guessing an unrelated tmux location.
	if got := resolveDisplayProjectRootFrom("/empty", tmuxTop("/repo"), has()); got != "/empty" {
		t.Fatalf("no-state fallback root = %q want /empty", got)
	}
}

func TestRandomTokenIsHexAndUnique(t *testing.T) {
	a, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := randomToken()
	if len(a) != 32 || a == b {
		t.Fatalf("token a=%q b=%q (want 32 hex chars, unique)", a, b)
	}
}

type dashboardBrowserStub struct {
	openedURL string
}

type dashboardStatusStub struct {
	message string
}

func stubDashboardOpenHooks(t *testing.T, browserErr error) (*dashboardBrowserStub, *dashboardStatusStub) {
	t.Helper()
	oldBrowser := openDashboardBrowser
	oldStatus := showDashboardStatus
	browser := &dashboardBrowserStub{}
	status := &dashboardStatusStub{}
	openDashboardBrowser = func(url string) error {
		browser.openedURL = url
		return browserErr
	}
	showDashboardStatus = func(msg string) error {
		status.message = msg
		return nil
	}
	t.Cleanup(func() {
		openDashboardBrowser = oldBrowser
		showDashboardStatus = oldStatus
	})
	return browser, status
}
