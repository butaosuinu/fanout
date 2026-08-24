package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/core/backend"
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
	isolateBackendEnv(t)
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
	if len(result.CreatedBindings) != 1 || result.CreatedBindings[0].Ref.Pane != "%91" ||
		result.CreatedBindings[0].Row.IssueNum != 501 {
		t.Fatalf("created bindings = %+v, want standalone row", result.CreatedBindings)
	}
}

func TestFinishTUIIssueParentLaunchPreservesPartialSuccess(t *testing.T) {
	result, err := finishTUIIssueParentLaunch(500, false, "", parentIssueFanoutResult{
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
	result, err := finishTUIIssueParentLaunch(500, false, "", parentIssueFanoutResult{}, wantErr)
	if !errors.Is(err, wantErr) {
		t.Fatalf("finishTUIIssueParentLaunch() error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(result, fanouttui.LaunchResult{}) {
		t.Fatalf("result = %#v, want empty result", result)
	}
}

func TestFinishTUIIssueParentLaunchPreservesCreationOrder(t *testing.T) {
	partialErr := errors.New("launch child #502: boom")
	tests := []struct {
		name               string
		orchestratorAfter  bool
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
			name:               "herdr orchestrator follows created children",
			orchestratorAfter:  true,
			orchestratorPaneID: "%90",
			result:             parentIssueFanoutResult{CreatedPaneIDs: []string{"%91", "%92"}},
			wantNotice:         "fanned out #500: started orchestrator + 2 child pane(s)",
			wantPaneIDs:        []string{"%91", "%92", "%90"},
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
			name:               "herdr partial child failure keeps creation order",
			orchestratorAfter:  true,
			orchestratorPaneID: "%90",
			result:             parentIssueFanoutResult{CreatedPaneIDs: []string{"%91"}},
			launchErr:          partialErr,
			wantNotice:         "started orchestrator + 1 child pane(s), then failed: launch child #502: boom",
			wantPaneIDs:        []string{"%91", "%90"},
		},
		{
			name:               "orchestrator warning survives partial child failure",
			orchestratorPaneID: "%90",
			result: parentIssueFanoutResult{
				CreatedPaneIDs: []string{"%91"},
				Notice:         codexOrchestratorPlanFallbackNotice,
			},
			launchErr:   partialErr,
			wantNotice:  "started orchestrator + 1 child pane(s), then failed: launch child #502: boom; " + codexOrchestratorPlanFallbackNotice,
			wantPaneIDs: []string{"%90", "%91"},
		},
		{
			name:               "orchestrator-only cleanup failure remains partial success",
			orchestratorPaneID: "%90",
			launchErr:          partialErr,
			wantNotice:         "started orchestrator, then failed: launch child #502: boom",
			wantPaneIDs:        []string{"%90"},
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
			name: "launch warning is appended to success notice",
			result: parentIssueFanoutResult{
				CreatedPaneIDs: []string{"%91"},
				Notice:         "child #501: plan mode takes precedence over --team; Codex team bridge is disabled for this pane",
			},
			wantNotice:  "fanned out #500: created 1 pane(s); child #501: plan mode takes precedence over --team; Codex team bridge is disabled for this pane",
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
			got, err := finishTUIIssueParentLaunch(500, tt.orchestratorAfter, tt.orchestratorPaneID, tt.result, tt.launchErr)
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

func TestFinishTUIIssueParentLaunchPreservesBindingCreationOrder(t *testing.T) {
	child := backend.PaneBinding{Ref: backend.PaneRef{Backend: backend.Herdr, Pane: "w1:p1"}}
	orchestrator := backend.PaneBinding{Ref: backend.PaneRef{Backend: backend.Herdr, Pane: "w2:p1"}}
	result, err := finishTUIIssueParentLaunch(500, true, orchestrator.Ref.Pane, parentIssueFanoutResult{
		CreatedPaneIDs: []string{child.Ref.Pane}, CreatedBindings: []backend.PaneBinding{child},
		OrchestratorBinding: orchestrator,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []backend.PaneBinding{child, orchestrator}
	if !reflect.DeepEqual(result.CreatedBindings, want) {
		t.Fatalf("CreatedBindings = %+v, want %+v", result.CreatedBindings, want)
	}
}

func TestLaunchIssueSessionFromTUIParentLaunchesOrchestratorFirst(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	tmuxLogPath := installTUISequentialTmuxShim(t, repo)
	t.Setenv("TMUX_SHIM_REQUIRE_CHILD_STATE_BEFORE_GATE_RELEASE", "1")
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
	if !orchestrator.PlanMode {
		t.Fatalf("orchestrator state = %+v, want plan mode from default settings", orchestrator)
	}
	child, ok := store.Find("500", 501)
	if !ok || child.PaneID != "%92" {
		t.Fatalf("child state = %+v/%v, want #501 pane %%92", child, ok)
	}
	if _, err := os.Stat(orchestratorIssueBriefingPath(repo, 500, -1)); err != nil {
		t.Fatalf("orchestrator briefing: %v", err)
	}
	tmuxLog := readTUITmuxLog(t, tmuxLogPath)
	gateLock := strings.Index(tmuxLog, "wait-for\n-L\nfanout-orchestrator-start-")
	firstSplit := strings.Index(tmuxLog, "split-window\n")
	lastSplit := strings.LastIndex(tmuxLog, "split-window\n")
	gateRelease := strings.Index(tmuxLog, "wait-for\n-U\nfanout-orchestrator-start-")
	if gateLock < 0 || firstSplit < 0 || lastSplit == firstSplit || gateRelease < 0 ||
		gateLock >= firstSplit || firstSplit >= lastSplit || lastSplit >= gateRelease {
		t.Fatalf("tmux gate order = lock %d, first split %d, last split %d, release %d:\n%s", gateLock, firstSplit, lastSplit, gateRelease, tmuxLog)
	}
	if !strings.Contains(tmuxLog, "tmux wait-for -L") {
		t.Fatalf("orchestrator split command does not wait on the start gate:\n%s", tmuxLog)
	}
	if !strings.Contains(tmuxLog, "--permission-mode plan") {
		t.Fatalf("orchestrator split command does not start Claude in plan mode:\n%s", tmuxLog)
	}
}

func TestHerdrIssueOrchestratorStartsOnlyAfterAdmissibleChildOutcome(t *testing.T) {
	tests := []struct {
		name     string
		model    backend.MutationModel
		progress run.IssueAfterContext
		want     bool
	}{
		{name: "all children completed", model: backend.MutationJournaled, progress: run.IssueAfterContext{Created: 2}, want: true},
		{name: "partial child success", model: backend.MutationJournaled, progress: run.IssueAfterContext{Created: 1, Failed: 1}, want: true},
		{name: "children already existed", model: backend.MutationJournaled, want: true},
		{name: "first child failed", model: backend.MutationJournaled, progress: run.IssueAfterContext{Failed: 1}},
		{name: "atomic lane keeps wait-for path", model: backend.MutationAtomic, progress: run.IssueAfterContext{Created: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchOrchestratorAfterChildren(tt.model, tt.progress); got != tt.want {
				t.Fatalf("launchOrchestratorAfterChildren() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestLaunchIssueSessionFromTUICodexOrchestratorFallsBackFromPlanMode(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	tmuxLogPath := installTUISequentialTmuxShim(t, repo)
	installTUIParentLaunchGHScript(t)
	installFakeExecutable(t, "codex")

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 500, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Notice, codexOrchestratorPlanFallbackNotice) {
		t.Fatalf("notice = %q, want Codex Plan Mode fallback", result.Notice)
	}
	tmuxLog := readTUITmuxLog(t, tmuxLogPath)
	if strings.Contains(tmuxLog, "__codex-plan-tui") {
		t.Fatalf("tmux log starts the gated orchestrator through Codex Plan Mode:\n%s", tmuxLog)
	}

	store, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	orchestrator, ok := store.Find(panelaunch.ManualParentRef, -1)
	if !ok || orchestrator.Agent != "codex" || orchestrator.PlanMode {
		t.Fatalf("orchestrator state = %+v/%v, want normal codex", orchestrator, ok)
	}
}

func TestLaunchIssueSessionFromTUIReportsClaudeModeFallback(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	tmuxLogPath := installTUISequentialTmuxShim(t, repo)
	installTUIParentLaunchGHScript(t)
	binDir := t.TempDir()
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nprintf '2.1.206 (Claude Code)\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 500, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantWarning := "#-1: Claude Code 2.1.207+ is required for explicit plan mode"
	if !strings.Contains(result.Notice, wantWarning) {
		t.Fatalf("notice = %q, want orchestrator mode fallback %q", result.Notice, wantWarning)
	}
	if tmuxLog := readTUITmuxLog(t, tmuxLogPath); strings.Contains(tmuxLog, "--permission-mode plan") {
		t.Fatalf("tmux log keeps unsupported Claude plan flags:\n%s", tmuxLog)
	}
	store, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	orchestrator, ok := store.Find(panelaunch.ManualParentRef, -1)
	if !ok || orchestrator.PlanMode {
		t.Fatalf("orchestrator state = %+v/%v, want effective non-plan fallback", orchestrator, ok)
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
	installTUISequentialTmuxShim(t, repo)
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
	tmuxLogPath := installTUISequentialTmuxShim(t, repo)
	t.Setenv("TMUX_SHIM_FAIL_SPLIT", "92")
	installTUIParentLaunchGHScript(t)

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 500, "claude", nil)
	if err == nil {
		t.Fatalf("launchIssueSessionFromTUI() = %#v, nil; want fan-out error", result)
	}
	if !strings.Contains(err.Error(), "tmux split-window") {
		t.Fatalf("launchIssueSessionFromTUI() error = %v, want original child launch failure", err)
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

func TestLaunchIssueSessionFromTUIReturnsOrchestratorWhenCleanupFails(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	tmuxLogPath := installTUISequentialTmuxShim(t, repo)
	t.Setenv("TMUX_SHIM_FAIL_SPLIT", "92")
	t.Setenv("TMUX_SHIM_FAIL_KILL", "%91")
	installTUIParentLaunchGHScript(t)

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 500, "claude", nil)
	if err != nil {
		t.Fatalf("launchIssueSessionFromTUI() error = %v, want partial success", err)
	}
	if !reflect.DeepEqual(result.CreatedPaneIDs, []string{"%91"}) {
		t.Fatalf("created pane ids = %#v, want remaining orchestrator %%91", result.CreatedPaneIDs)
	}
	for _, want := range []string{"started orchestrator, then failed", "tmux split-window", "cleanup issue orchestrator", "tmux kill-pane"} {
		if !strings.Contains(result.Notice, want) {
			t.Fatalf("notice = %q, want %q", result.Notice, want)
		}
	}
	store, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(store.Panes) != 1 || store.Panes[0].PaneID != "%91" {
		t.Fatalf("state panes = %+v, want remaining orchestrator %%91", store.Panes)
	}
	tmuxLog := readTUITmuxLog(t, tmuxLogPath)
	if strings.Contains(tmuxLog, "wait-for\n-U\nfanout-orchestrator-start-") {
		t.Fatalf("cleanup failure released the orchestrator gate:\n%s", tmuxLog)
	}
}

func TestLaunchIssueSessionFromTUISkipsOrchestratorWhenEveryChildBlocked(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	tmuxLogPath := installTUISequentialTmuxShim(t, repo)
	installTUIBlockedParentLaunchGHScript(t)

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), 500, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CreatedPaneIDs) != 0 {
		t.Fatalf("created pane ids = %#v, want none while every child is blocked", result.CreatedPaneIDs)
	}
	if !strings.Contains(result.Notice, "blocked/deferred children remain") {
		t.Fatalf("notice = %q, want deferred context", result.Notice)
	}
	if body, readErr := os.ReadFile(tmuxLogPath); readErr == nil {
		if strings.Contains(string(body), "split-window") || strings.Contains(string(body), "wait-for") {
			t.Fatalf("tmux ran for an all-blocked parent:\n%s", body)
		}
	} else if !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	store, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(store.Panes) != 0 {
		t.Fatalf("state panes = %+v, want no launch side effects", store.Panes)
	}
	if _, statErr := os.Stat(orchestratorIssueBriefingPath(repo, 500, -1)); !os.IsNotExist(statErr) {
		t.Fatalf("orchestrator briefing exists for an all-blocked parent: %v", statErr)
	}
}

func TestLaunchIssueSessionFromTUIAllowsNonCodexPlanChild(t *testing.T) {
	repo := prepareTUIParentLaunchRepo(t)
	tmuxLogPath := installTUISequentialTmuxShim(t, repo)
	installTUIParentLaunchGHScript(t)
	installFakeExecutable(t, "codex")
	t.Setenv("FANOUT_CHILD_PLAN_MODE", "true")
	resolved := settings.Defaults()
	resolved.ChildPlanMode = true

	result, err := launchIssueSessionFromTUI(repo, "fanout-test", "fanout", resolved, hooks.EmptyConfig(), 500, "codex", map[string]string{"501": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CreatedPaneIDs) != 2 {
		t.Fatalf("created pane ids = %#v, want orchestrator and child", result.CreatedPaneIDs)
	}
	tmuxLog := readTUITmuxLog(t, tmuxLogPath)
	if !strings.Contains(tmuxLog, "claude --settings") || !strings.Contains(tmuxLog, "--permission-mode plan") {
		t.Fatalf("tmux log = %q, want Claude plan launch", tmuxLog)
	}
	store, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(store.Panes) != 2 || store.Panes[1].Agent != "claude" || !store.Panes[1].PlanMode {
		t.Fatalf("state panes = %+v, want Claude plan child", store.Panes)
	}
}

func prepareTUIParentLaunchRepo(t *testing.T) string {
	t.Helper()
	isolateBackendEnv(t)
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	origin := t.TempDir()
	gitCmdTest(t, origin, "init", "--bare")
	gitCmdTest(t, repo, "remote", "add", "origin", origin)
	gitCmdTest(t, repo, "push", "-u", "origin", "main")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FANOUT_CHILD_PLAN_MODE", "false")
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

func installTUIBlockedParentLaunchGHScript(t *testing.T) {
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
  printf '%s' '{"body":"## Blocked by\n- #601","labels":[]}'
  ;;
"issue view 601 --json state -q .state")
  printf 'OPEN\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
}

func installTUISequentialTmuxShim(t *testing.T, repo string) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "tmux-args.txt")
	counterPath := filepath.Join(dir, "tmux-counter.txt")
	shellKeyPath := filepath.Join(dir, "tmux-shell-key.txt")
	killedPanesPath := filepath.Join(dir, "tmux-killed-panes.txt")
	if err := os.WriteFile(counterPath, []byte("90\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shellKeyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(killedPanesPath, nil, 0o644); err != nil {
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
    if [[ "${TMUX_SHIM_FAIL_SPLIT:-}" == "$current" ]]; then
      printf 'split failed for %%%d\n' "$current" >&2
      exit 17
    fi
    printf '%s\n' "$current" > "$TMUX_SHIM_COUNTER"
    printf '%%%d\n' "$current"
    ;;
  display-message)
    if [[ "$*" == *window_width* ]]; then
      printf '@1\t200\t50\n'
    fi
    ;;
  list-panes)
    if [[ "$*" == *pane_current_path* ]]; then
      read -r current < "$TMUX_SHIM_COUNTER"
      pane=91
      while (( pane <= current )); do
        if ! grep -Fxq "%$pane" "$TMUX_SHIM_KILLED_PANES"; then
          printf '%%%d\t%s\n' "$pane" "$TMUX_SHIM_LIVE_PATH"
        fi
        pane=$((pane + 1))
      done
    elif [[ "$*" == *pane_title* ]]; then
      read -r current < "$TMUX_SHIM_COUNTER"
      pane=91
      while (( pane <= current )); do
        if ! grep -Fxq "%$pane" "$TMUX_SHIM_KILLED_PANES"; then
          printf '%%%d\tpane-%d\n' "$pane" "$pane"
        fi
        pane=$((pane + 1))
      done
    elif [[ "$*" == *fanout_shell_key* ]]; then
      read -r current < "$TMUX_SHIM_COUNTER"
      shell_key="$(sed -n '1p' "$TMUX_SHIM_SHELL_KEY")"
      pane=91
      while (( pane <= current )); do
        if ! grep -Fxq "%$pane" "$TMUX_SHIM_KILLED_PANES"; then
          if (( pane == 91 )); then
            printf '%%%d\t%s\n' "$pane" "$shell_key"
          else
            printf '%%%d\t\n' "$pane"
          fi
        fi
        pane=$((pane + 1))
      done
    elif [[ "$*" == *fanout_project_root* || "$*" == *fanout_worktree_path* ]]; then
      read -r current < "$TMUX_SHIM_COUNTER"
      pane=91
      while (( pane <= current )); do
        if ! grep -Fxq "%$pane" "$TMUX_SHIM_KILLED_PANES"; then
          printf '%%%d\t%s\n' "$pane" "$TMUX_SHIM_LIVE_PATH"
        fi
        pane=$((pane + 1))
      done
    elif [[ "$*" == *fanout_role* ]]; then
      printf '%%0\t0\t1\tconsole\t\n'
      read -r current < "$TMUX_SHIM_COUNTER"
      pane=91
      index=1
      while (( pane <= current )); do
        if ! grep -Fxq "%$pane" "$TMUX_SHIM_KILLED_PANES"; then
          printf '%%%d\t%d\t0\t\t\n' "$pane" "$index"
        fi
        pane=$((pane + 1))
        index=$((index + 1))
      done
    fi
    ;;
  set-option)
    if [[ "${5:-}" == "@fanout_shell_key" ]]; then
      printf '%s\n' "${6:-}" > "$TMUX_SHIM_SHELL_KEY"
    fi
    ;;
  wait-for)
    if [[ "${2:-}" == "-U" && "${TMUX_SHIM_REQUIRE_CHILD_STATE_BEFORE_GATE_RELEASE:-}" == "1" ]]; then
      if ! grep -Eq '"issueNum"[[:space:]]*:[[:space:]]*501' "$TMUX_SHIM_LIVE_PATH/.fanout/state.json"; then
        printf 'child state missing before gate release\n' >&2
        exit 29
      fi
    fi
    ;;
  kill-pane)
    if [[ "${TMUX_SHIM_FAIL_KILL:-}" == "${3:-}" ]]; then
      printf 'kill failed for %s\n' "${3:-}" >&2
      exit 23
    fi
    printf '%s\n' "${3:-}" >> "$TMUX_SHIM_KILLED_PANES"
    ;;
  select-pane|select-layout|set-window-option|bind-key)
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
	t.Setenv("TMUX_SHIM_SHELL_KEY", shellKeyPath)
	t.Setenv("TMUX_SHIM_KILLED_PANES", killedPanesPath)
	t.Setenv("TMUX_SHIM_LIVE_PATH", repo)
	t.Setenv("TMUX_SHIM_FAIL_SPLIT", "")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

// TestLaunchIssueSessionFromTUITranslatesAlreadyFanned pins the standalone
// lane: a childless issue whose pane is already recorded surfaces a readable
// message instead of the raw sentinel error.
func TestLaunchIssueSessionFromTUITranslatesAlreadyFanned(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	isolateBackendEnv(t)
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
