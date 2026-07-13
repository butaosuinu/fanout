package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func TestOpenIssueFromTUIOpensGitHubIssueURL(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"repo view --json nameWithOwner -q .nameWithOwner")
  printf 'octo/fanout\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	oldOpen := openTUIIssueBrowser
	t.Cleanup(func() { openTUIIssueBrowser = oldOpen })
	var opened string
	openTUIIssueBrowser = func(url string) error {
		opened = url
		return nil
	}

	if err := openIssueFromTUI(t.TempDir(), 42); err != nil {
		t.Fatal(err)
	}
	if opened != "https://github.com/octo/fanout/issues/42" {
		t.Fatalf("opened URL = %q, want GitHub issue URL", opened)
	}
}

func TestOpenIssueFromTUIReportsBrowserFailure(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"repo view --json nameWithOwner -q .nameWithOwner")
  printf 'octo/fanout\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	oldOpen := openTUIIssueBrowser
	t.Cleanup(func() { openTUIIssueBrowser = oldOpen })
	openTUIIssueBrowser = func(string) error { return errors.New("launcher unavailable") }

	err := openIssueFromTUI(t.TempDir(), 42)
	if err == nil || !strings.Contains(err.Error(), "open issue #42") || !strings.Contains(err.Error(), "launcher unavailable") {
		t.Fatalf("openIssueFromTUI() error = %v, want browser failure context", err)
	}
}

func TestIssueURLFromRepoRejectsInvalidInputs(t *testing.T) {
	if got, err := issueURLFromRepo(t.TempDir(), 0); err == nil || got != "" || !strings.Contains(err.Error(), "issue number is required") {
		t.Fatalf("issueURLFromRepo(0) = %q, %v; want issue number error", got, err)
	}

	installTUIWatcherGHScript(t, `
case "$args" in
"repo view --json nameWithOwner -q .nameWithOwner")
  printf 'octo/fanout/extra\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	if got, err := issueURLFromRepo(t.TempDir(), 42); err == nil || got != "" || !strings.Contains(err.Error(), "unexpected repo nameWithOwner") {
		t.Fatalf("issueURLFromRepo(invalid repo) = %q, %v; want repo validation error", got, err)
	}
}

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
	if err = locked.RecordPane(state.Pane{
		Parent:   panelaunch.ManualParentRef,
		IssueNum: -1,
		Slug:     panelaunch.OrchestratorIssueSlug(800, -1),
		PaneID:   "%2",
	}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	installTUIWatcherGHScript(t, `
case "$args" in
*"api graphql"*)
  printf '{"data":{"repository":{"issues":{"nodes":[{"number":501,"title":"recorded child","labels":{"nodes":[{"name":"bug"}]},"parent":{"number":700},"subIssuesSummary":{"total":0,"completed":0}},{"number":502,"title":"fresh","labels":{"nodes":[]},"parent":null,"subIssuesSummary":{"total":0,"completed":0}},{"number":700,"title":"fanned parent","labels":{"nodes":[]},"parent":null,"subIssuesSummary":{"total":3,"completed":1}},{"number":800,"title":"orchestrated parent","labels":{"nodes":[]},"parent":null,"subIssuesSummary":{"total":2,"completed":0}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}'
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

	// #501 is a child (parent #700), #502 standalone, #700 a fan-out parent
	// with two OPEN children (total 3 - completed 1); recorded panes mark #501,
	// its parent #700, and the orchestrator-owned parent #800 with HasSession.
	want := []fanouttui.IssueListItem{
		{Number: 501, Title: "recorded child", Labels: []string{"bug"}, HasSession: true, HasParent: true},
		{Number: 502, Title: "fresh", Labels: []string{}},
		{Number: 700, Title: "fanned parent", Labels: []string{}, HasSession: true, HasOpenChildren: true},
		{Number: 800, Title: "orchestrated parent", Labels: []string{}, HasSession: true, HasOpenChildren: true},
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

func TestLaunchIssueSessionFromTUIReturnsStandalonePaneID(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	origin := t.TempDir()
	gitCmdTest(t, origin, "init", "--bare")
	gitCmdTest(t, repo, "remote", "add", "origin", origin)
	gitCmdTest(t, repo, "push", "-u", "origin", "main")
	installFakeExecutable(t, "claude")
	installTUITmuxShim(t, "%91")
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

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 501, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Notice != "started session for #501" {
		t.Fatalf("notice = %q, want standalone success", result.Notice)
	}
	if !reflect.DeepEqual(result.CreatedPaneIDs, []string{"%91"}) {
		t.Fatalf("created pane ids = %#v, want [%%91]", result.CreatedPaneIDs)
	}
}

func TestFinishTUIIssueParentLaunchPreservesPartialSuccess(t *testing.T) {
	result, err := finishTUIIssueParentLaunch(500, "", parentIssueFanoutResult{
		CreatedPaneIDs: []string{"%91"},
	}, errors.New("launch child #502: boom"))
	if err != nil {
		t.Fatalf("finishTUIIssueParentLaunch() error = %v, want partial success", err)
	}
	if !reflect.DeepEqual(result.CreatedPaneIDs, []string{"%91"}) {
		t.Fatalf("created pane ids = %#v, want [%%91]", result.CreatedPaneIDs)
	}
	if !strings.Contains(result.Notice, "created 1 pane(s), then failed") ||
		!strings.Contains(result.Notice, "launch child #502: boom") {
		t.Fatalf("notice = %q, want partial failure context", result.Notice)
	}
}

func TestFinishTUIIssueParentLaunchReturnsErrorWithoutCreatedPane(t *testing.T) {
	wantErr := errors.New("launch failed")
	result, err := finishTUIIssueParentLaunch(500, "", parentIssueFanoutResult{}, wantErr)
	if !errors.Is(err, wantErr) {
		t.Fatalf("finishTUIIssueParentLaunch() error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(result, fanouttui.LaunchResult{}) {
		t.Fatalf("result = %#v, want empty result", result)
	}
}

func TestFinishTUIIssueParentLaunchPrependsOrchestrator(t *testing.T) {
	partialErr := errors.New("launch child #502: boom")
	tests := []struct {
		name               string
		orchestratorPaneID string
		result             parentIssueFanoutResult
		launchErr          error
		wantNotice         string
		wantPaneIDs        []string
	}{
		{
			name:               "orchestrator precedes created children",
			orchestratorPaneID: "%90",
			result:             parentIssueFanoutResult{CreatedPaneIDs: []string{"%91", "%92"}},
			wantNotice:         "fanned out #500: started orchestrator + 2 child pane(s)",
			wantPaneIDs:        []string{"%90", "%91", "%92"},
		},
		{
			name:               "orchestrator remains when every child already has a pane",
			orchestratorPaneID: "%90",
			wantNotice:         "started orchestrator for #500; children already have panes",
			wantPaneIDs:        []string{"%90"},
		},
		{
			name:               "partial child failure keeps orchestrator first",
			orchestratorPaneID: "%90",
			result:             parentIssueFanoutResult{CreatedPaneIDs: []string{"%91"}},
			launchErr:          partialErr,
			wantNotice:         "started orchestrator + 1 child pane(s), then failed: launch child #502: boom",
			wantPaneIDs:        []string{"%90", "%91"},
		},
		{
			name:               "deferred suffix is unchanged with orchestrator",
			orchestratorPaneID: "%90",
			result: parentIssueFanoutResult{
				Watch:          watch.ParentLaunchResult{Deferred: true},
				CreatedPaneIDs: []string{"%91"},
			},
			wantNotice:  "fanned out #500: started orchestrator + 1 child pane(s); blocked/deferred children remain - re-select the issue later",
			wantPaneIDs: []string{"%90", "%91"},
		},
		{
			name:        "empty orchestrator id preserves the legacy notice",
			result:      parentIssueFanoutResult{CreatedPaneIDs: []string{"%91"}},
			wantNotice:  "fanned out #500: created 1 pane(s)",
			wantPaneIDs: []string{"%91"},
		},
		{
			name:       "empty orchestrator id preserves the legacy no-op notice",
			wantNotice: "#500: no new panes (children already have one)",
		},
		{
			name:        "empty orchestrator id preserves the legacy partial notice",
			result:      parentIssueFanoutResult{CreatedPaneIDs: []string{"%91"}},
			launchErr:   partialErr,
			wantNotice:  "created 1 pane(s), then failed: launch child #502: boom",
			wantPaneIDs: []string{"%91"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := finishTUIIssueParentLaunch(500, tt.orchestratorPaneID, tt.result, tt.launchErr)
			if err != nil {
				t.Fatalf("finishTUIIssueParentLaunch() error = %v, want nil", err)
			}
			if got.Notice != tt.wantNotice {
				t.Fatalf("notice = %q, want %q", got.Notice, tt.wantNotice)
			}
			if !reflect.DeepEqual(got.CreatedPaneIDs, tt.wantPaneIDs) {
				t.Fatalf("created pane ids = %#v, want %#v", got.CreatedPaneIDs, tt.wantPaneIDs)
			}
		})
	}
}

func TestLaunchIssueSessionFromTUIParentLaunchesOrchestratorFirst(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	installTUISequentialTmuxShim(t)
	installTUIParentLaunchGHScript(t)

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 500, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.CreatedPaneIDs, []string{"%91", "%92"}) {
		t.Fatalf("created pane ids = %#v, want orchestrator then child", result.CreatedPaneIDs)
	}
	if result.Notice != "fanned out #500: started orchestrator + 1 child pane(s)" {
		t.Fatalf("notice = %q, want orchestrator success", result.Notice)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 2 {
		t.Fatalf("state panes = %+v, want orchestrator and child", store.Panes)
	}
	orchestrator, ok := store.Find(panelaunch.ManualParentRef, -1)
	if !ok {
		t.Fatalf("state panes = %+v, want @manual/-1 orchestrator", store.Panes)
	}
	if issueNum, parsed := panelaunch.OrchestratorPaneIssueNum(orchestrator); !parsed || issueNum != 500 {
		t.Fatalf("orchestrator state = %+v, want issue #500 provenance", orchestrator)
	}
	if orchestrator.PaneID != "%91" || orchestrator.WorktreePath != repo || !orchestrator.IsAttachedAgent() {
		t.Fatalf("orchestrator state = %+v, want pane %%91 attached at project root", orchestrator)
	}
	child, ok := store.Find("500", 501)
	if !ok || child.PaneID != "%92" {
		t.Fatalf("child state = %+v/%v, want #501 pane %%92", child, ok)
	}
	if _, err := os.Stat(orchestratorIssueBriefingPath(repo, 500, -1)); err != nil {
		t.Fatalf("orchestrator briefing: %v", err)
	}
}

func TestLaunchIssueSessionFromTUISkipsSecondOrchestrator(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{
		Parent:       panelaunch.ManualParentRef,
		IssueNum:     -1,
		Kind:         state.PaneKindAttachedAgent,
		Slug:         panelaunch.OrchestratorIssueSlug(500, -1),
		PaneID:       "%50",
		Agent:        "claude",
		WorktreePath: repo,
	}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	installTUISequentialTmuxShim(t)
	installTUIParentLaunchGHScript(t)

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 500, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.CreatedPaneIDs, []string{"%91"}) {
		t.Fatalf("created pane ids = %#v, want only the new child", result.CreatedPaneIDs)
	}
	if result.Notice != "fanned out #500: created 1 pane(s)" || strings.Contains(result.Notice, "orchestrator") {
		t.Fatalf("notice = %q, want legacy child-only success", result.Notice)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	orchestratorCount := 0
	for _, pane := range store.Panes {
		if issueNum, ok := panelaunch.OrchestratorPaneIssueNum(pane); ok && issueNum == 500 {
			orchestratorCount++
			if pane.PaneID != "%50" {
				t.Fatalf("orchestrator pane id = %q, want existing %%50", pane.PaneID)
			}
		}
	}
	if orchestratorCount != 1 {
		t.Fatalf("state panes = %+v, want exactly one orchestrator for #500", store.Panes)
	}
	child, ok := store.Find("500", 501)
	if !ok || child.PaneID != "%91" {
		t.Fatalf("child state = %+v/%v, want #501 pane %%91", child, ok)
	}
}

func TestLaunchIssueSessionFromTUICleansOrchestratorWhenFanoutCreatesNothing(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	tmuxLogPath := installTUISequentialTmuxShim(t)
	installTUIWatcherGHScript(t, `
case "$args" in
"issue view 500 --json number,title,state,body,labels")
  printf '{"number":500,"title":"parent","state":"OPEN","body":"parent body","labels":[]}'
  ;;
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  count_file="${GH_FAKE_ARGS}.subissue-count"
  count=0
  if [[ -f "$count_file" ]]; then
    count="$(cat "$count_file")"
  fi
  count=$((count + 1))
  printf '%s' "$count" > "$count_file"
  if [[ "$count" -eq 1 ]]; then
    printf '[[{"number":501,"title":"child","state":"open"}]]'
  else
    printf 'temporary sub-issue failure\n' >&2
    exit 1
  fi
  ;;
"issue view 500 --json body -q .body")
  printf 'parent body\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 500, "claude", nil)
	if err == nil {
		t.Fatalf("launchIssueSessionFromTUI() = %#v, nil; want fan-out error", result)
	}
	if !strings.Contains(err.Error(), "temporary sub-issue failure") {
		t.Fatalf("launchIssueSessionFromTUI() error = %v, want original fan-out failure", err)
	}
	if !reflect.DeepEqual(result, fanouttui.LaunchResult{}) {
		t.Fatalf("result = %#v, want empty result", result)
	}
	store, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(store.Panes) != 0 {
		t.Fatalf("state panes = %+v, want orchestrator cleanup", store.Panes)
	}
	tmuxLog := readTUITmuxLog(t, tmuxLogPath)
	if !tmuxLogHasCommand(tmuxLog, "kill-pane\n-t\n%91") {
		t.Fatalf("tmux log missing orchestrator cleanup:\n%s", tmuxLog)
	}
}

func prepareTUIParentLaunchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	origin := t.TempDir()
	gitCmdTest(t, origin, "init", "--bare")
	gitCmdTest(t, repo, "remote", "add", "origin", origin)
	gitCmdTest(t, repo, "push", "-u", "origin", "main")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FANOUT_CODEX_PLAN_MODE", "false")
	t.Setenv("FANOUT_DASHBOARD_KEYBIND", "false")
	t.Setenv("TMUX_PANE", "")
	installFakeExecutable(t, "claude")
	return repo
}

func installTUIParentLaunchGHScript(t *testing.T) {
	t.Helper()
	installTUIWatcherGHScript(t, `
case "$args" in
"issue view 500 --json number,title,state,body,labels")
  printf '{"number":500,"title":"parent","state":"OPEN","body":"parent body","labels":[]}'
  ;;
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[{"number":501,"title":"child","state":"open"}]]'
  ;;
"issue view 500 --json body -q .body")
  printf 'parent body\n'
  ;;
"issue view 501 --json body,labels")
  printf '{"body":"child body","labels":[]}'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
}

func installTUISequentialTmuxShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "tmux-args.txt")
	counterPath := filepath.Join(dir, "tmux-counter.txt")
	if err := os.WriteFile(counterPath, []byte("90\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "$TMUX_SHIM_ARGS"
printf '%s\n' '---' >> "$TMUX_SHIM_ARGS"
case "${1:-}" in
  split-window)
    read -r current < "$TMUX_SHIM_COUNTER"
    current=$((current + 1))
    printf '%s\n' "$current" > "$TMUX_SHIM_COUNTER"
    printf '%%%d\n' "$current"
    ;;
  display-message)
    if [[ "$*" == *window_width* ]]; then
      printf '@1\t200\t50\n'
    fi
    ;;
  list-panes)
    if [[ "$*" == *fanout_role* ]]; then
      printf '%%0\t0\t1\tconsole\t\n'
      read -r current < "$TMUX_SHIM_COUNTER"
      pane=91
      index=1
      while (( pane <= current )); do
        printf '%%%d\t%d\t0\t\t\n' "$pane" "$index"
        pane=$((pane + 1))
        index=$((index + 1))
      done
    fi
    ;;
  select-pane|set-option|select-layout|set-window-option|bind-key|kill-pane)
    ;;
  *)
    ;;
esac
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_SHIM_ARGS", argsPath)
	t.Setenv("TMUX_SHIM_COUNTER", counterPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
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
