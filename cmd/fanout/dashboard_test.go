package main

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/log"
)

func TestIsDashboardRequest(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"dashboard"}, true},
		{[]string{"dashboard", "--web"}, true},
		{[]string{"--status", "1"}, false},
		{[]string{"100"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isDashboardRequest(c.args); got != c.want {
			t.Errorf("isDashboardRequest(%v) = %v, want %v", c.args, got, c.want)
		}
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
