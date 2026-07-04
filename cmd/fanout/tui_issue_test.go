package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
)

func TestNewTUIListOpenIssuesFuncMarksRecordedSessions(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "700", IssueNum: 501, Slug: "child-501", PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	installTUIWatcherGHScript(t, `
case "$args" in
*"api graphql"*)
  printf '{"data":{"repository":{"issues":{"nodes":[{"number":501,"title":"recorded child","labels":{"nodes":[{"name":"bug"}]}},{"number":502,"title":"fresh","labels":{"nodes":[]}},{"number":700,"title":"fanned parent","labels":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	got, err := newTUIListOpenIssuesFunc(repo)()
	if err != nil {
		t.Fatal(err)
	}

	want := []fanouttui.IssueListItem{
		{Number: 501, Title: "recorded child", Labels: []string{"bug"}, HasSession: true},
		{Number: 502, Title: "fresh", Labels: []string{}},
		{Number: 700, Title: "fanned parent", Labels: []string{}, HasSession: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("newTUIListOpenIssuesFunc() = %#v, want %#v", got, want)
	}
}

func TestNewTUIListIssueChildrenFuncReturnsOpenChildrenWithWaves(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[{"number":501,"title":"frontend","state":"open"},{"number":502,"title":"done","state":"closed"}]]'
  ;;
"issue view 500 --json body -q .body")
  printf '%s\n' '### Wave 1' '- [ ] #501 frontend' '- [x] #502 done'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	got, err := newTUIListIssueChildrenFunc(t.TempDir())(500)
	if err != nil {
		t.Fatal(err)
	}

	// ghissue.TaskListWaves normalizes headings like "### Wave 1" to "wave1".
	want := []fanouttui.ChildTarget{{Number: 501, Title: "frontend", Wave: "wave1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("newTUIListIssueChildrenFunc() = %#v, want %#v", got, want)
	}
}

func TestLaunchIssueSessionFromTUIRejectsClosedIssue(t *testing.T) {
	installFakeExecutable(t, "claude")
	installTUIWatcherGHScript(t, `
case "$args" in
"issue view 501 --json number,title,state,body,labels")
  printf '{"number":501,"title":"stale row","state":"CLOSED","body":"","labels":[]}'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	_, err := launchIssueSessionFromTUI(t.TempDir(), "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 501, "claude", nil)
	if err == nil || !strings.Contains(err.Error(), "issue #501 is not OPEN") {
		t.Fatalf("launchIssueSessionFromTUI() error = %v, want not-OPEN rejection", err)
	}
}

// TestLaunchIssueSessionFromTUITranslatesAlreadyFanned pins the standalone
// lane: a childless issue whose pane is already recorded surfaces a readable
// message instead of the raw sentinel error.
func TestLaunchIssueSessionFromTUITranslatesAlreadyFanned(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "@watch", IssueNum: 501, Slug: "existing-501", PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	installFakeExecutable(t, "claude")
	installTUIWatcherGHScript(t, `
case "$args" in
"issue view 501 --json number,title,state,body,labels")
  printf '{"number":501,"title":"standalone","state":"OPEN","body":"body","labels":[]}'
  ;;
"api --paginate --slurp repos/{owner}/{repo}/issues/501/sub_issues?per_page=100")
  printf '[[]]'
  ;;
"issue view 501 --json body -q .body")
  printf 'body\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	_, err = launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 501, "claude", nil)
	if err == nil || !strings.Contains(err.Error(), "issue #501 already has a fanout pane") {
		t.Fatalf("launchIssueSessionFromTUI() error = %v, want already-has-pane message", err)
	}
}

func TestLaunchIssueSessionFromTUIRejectsUnknownAgent(t *testing.T) {
	installFakeExecutable(t, "claude")
	_, err := launchIssueSessionFromTUI(t.TempDir(), "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 501, "claude", map[string]string{"502": "gemini"})
	if err == nil || !strings.Contains(err.Error(), "gemini") {
		t.Fatalf("launchIssueSessionFromTUI() error = %v, want unknown-agent rejection", err)
	}
}

// TestValidateTUIAgentSelectionSkipsInstallCheckForOverrides pins the CLI
// parity: an override may target a deferred child, so only the default agent
// must be installed here — launch-lane validation covers actual targets.
func TestValidateTUIAgentSelectionSkipsInstallCheckForOverrides(t *testing.T) {
	// An exclusive PATH with only a fake claude, so a codex CLI on the host
	// cannot mask the uninstalled-agent paths.
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	if err := validateTUIAgentSelection("claude", map[string]string{"502": "codex"}); err != nil {
		t.Fatalf("validateTUIAgentSelection() error = %v, want uninstalled override tolerated", err)
	}
	if err := validateTUIAgentSelection("codex", nil); err == nil {
		t.Fatal("validateTUIAgentSelection() = nil, want uninstalled default agent rejected")
	}
}

// TestTUIIssueLaunchConfigCarriesAgentSelection guarantees the TUI selection
// reaches the fan-out exactly like repeatable --agent flags would.
func TestTUIIssueLaunchConfigCarriesAgentSelection(t *testing.T) {
	tests := []struct {
		name         string
		defaultAgent string
		overrides    map[string]string
		wantEffect   map[int]string
	}{
		{
			name:         "default only",
			defaultAgent: "claude",
			wantEffect:   map[int]string{43: "claude", 44: "claude"},
		},
		{
			name:         "override flips one child",
			defaultAgent: "claude",
			overrides:    map[string]string{"44": "codex"},
			wantEffect:   map[int]string{43: "claude", 44: "codex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tuiIssueLaunchConfig(42, tt.defaultAgent, tt.overrides)
			if cfg.Parent != 42 || cfg.ParentRef != "42" || !cfg.UnblockedOnly {
				t.Fatalf("cfg identity = %d/%q unblockedOnly=%v, want 42/42/true", cfg.Parent, cfg.ParentRef, cfg.UnblockedOnly)
			}
			for num, want := range tt.wantEffect {
				if got := cfg.EffectiveAgentForIssue(num); got != want {
					t.Fatalf("EffectiveAgentForIssue(%d) = %q, want %q", num, got, want)
				}
			}
		})
	}
}
