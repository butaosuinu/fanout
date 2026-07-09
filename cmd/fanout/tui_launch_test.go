package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func TestPlanSkillPromptPerAgent(t *testing.T) {
	path := planPromptPath("/repo", 1)
	tests := []struct {
		name  string
		agent string
		want  string
	}{
		{name: "claude uses the slash command", agent: "claude", want: "/fanout plan " + path},
		{name: "codex uses the dollar invocation", agent: "codex", want: "$fanout-plan " + path},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planSkillPrompt(tt.agent, path); got != tt.want {
				t.Fatalf("planSkillPrompt(%q, %q) = %q, want %q", tt.agent, path, got, tt.want)
			}
		})
	}
}

// TestNewPlanPromptPaneRequestWritesSkillInvocation pins the coordinator pane
// request: a plain (non-Codex-Plan-Mode) agent whose one-line prompt invokes
// the fanout-plan skill on the full prompt written to the briefing file, and
// whose liveness key survives into the request (the repo-root WorktreePath is
// too broad for path-based liveness, so the key is the row's identity).
func TestNewPlanPromptPaneRequestWritesSkillInvocation(t *testing.T) {
	const prompt = "Build a full-text search over issues.\nInclude ranking and filters."
	req := newPlanPromptPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), prompt, "claude", "shell-coordinator-key")

	if !strings.HasPrefix(req.Prompt, "/fanout plan ") {
		t.Fatalf("req.Prompt = %q, want a /fanout plan invocation", req.Prompt)
	}
	if !strings.Contains(req.Prompt, req.BriefingPath) {
		t.Fatalf("req.Prompt %q does not reference briefing path %q", req.Prompt, req.BriefingPath)
	}
	if req.BriefingBody != prompt {
		t.Fatalf("req.BriefingBody = %q, want the full prompt", req.BriefingBody)
	}
	wantPrefix := briefing.Dir("/repo") + "/fanout-repo-plan-prompt-"
	if !strings.HasPrefix(req.BriefingPath, wantPrefix) {
		t.Fatalf("req.BriefingPath = %q, want %q prefix", req.BriefingPath, wantPrefix)
	}
	if req.CodexPlanMode {
		t.Fatal("req.CodexPlanMode = true, want false for a plan coordinator")
	}
	if req.ParentRef != panelaunch.ManualParentRef {
		t.Fatalf("req.ParentRef = %q, want %q", req.ParentRef, panelaunch.ManualParentRef)
	}
	if req.ShellKey != "shell-coordinator-key" {
		t.Fatalf("req.ShellKey = %q, want the liveness key passed through", req.ShellKey)
	}
}

// TestNewIssuePlanPaneRequestWritesIssueCoordinatorBrief pins the issue-sourced
// plan coordinator pane request: a plain (non-Codex-Plan-Mode) agent whose
// one-line prompt invokes the fanout-plan skill on the issue-derived coordinator
// brief, and whose briefing carries the issue title/body, the worker --agent
// override, and the "Refs #N" (never "Closes") requirement.
func TestNewIssuePlanPaneRequestWritesIssueCoordinatorBrief(t *testing.T) {
	issue := ghissue.Issue{
		Number: 123,
		Title:  "Add full-text search",
		Body:   "Users cannot search issues by keyword.",
	}
	tests := []struct {
		name        string
		coordinator string
		wantPrefix  string
	}{
		{name: "claude coordinator uses the slash command", coordinator: "claude", wantPrefix: "/fanout plan "},
		{name: "codex coordinator uses the dollar invocation", coordinator: "codex", wantPrefix: "$fanout-plan "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newIssuePlanPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), issue, tt.coordinator, "codex", "shell-issue-plan-key")

			if !strings.HasPrefix(req.Prompt, tt.wantPrefix) {
				t.Fatalf("req.Prompt = %q, want %q prefix", req.Prompt, tt.wantPrefix)
			}
			if !strings.Contains(req.Prompt, req.BriefingPath) {
				t.Fatalf("req.Prompt %q does not reference briefing path %q", req.Prompt, req.BriefingPath)
			}
			// The trailing -1 is the synthetic pane number: per-launch unique so a
			// relaunch never overwrites a brief an earlier coordinator still reads.
			if base := filepath.Base(req.BriefingPath); base != "fanout-repo-plan-issue-123-1.md" {
				t.Fatalf("briefing basename = %q, want fanout-repo-plan-issue-123-1.md", base)
			}
			if !strings.Contains(req.BriefingBody, issue.Title) {
				t.Fatalf("req.BriefingBody = %q, want the issue title", req.BriefingBody)
			}
			if !strings.Contains(req.BriefingBody, issue.Body) {
				t.Fatalf("req.BriefingBody = %q, want the issue body", req.BriefingBody)
			}
			// The worker agent flows into the fan-out command; a codex worker pins the override.
			if !strings.Contains(req.BriefingBody, "--agent codex") {
				t.Fatalf("req.BriefingBody = %q, want a --agent codex worker override", req.BriefingBody)
			}
			if !strings.Contains(req.BriefingBody, "Refs #123") {
				t.Fatalf("req.BriefingBody = %q, want a Refs #123 requirement", req.BriefingBody)
			}
			if req.CodexPlanMode {
				t.Fatal("req.CodexPlanMode = true, want false for a plan coordinator")
			}
			if req.ParentRef != panelaunch.ManualParentRef {
				t.Fatalf("req.ParentRef = %q, want %q", req.ParentRef, panelaunch.ManualParentRef)
			}
			if req.ShellKey != "shell-issue-plan-key" {
				t.Fatalf("req.ShellKey = %q, want the liveness key passed through", req.ShellKey)
			}
			if req.Slug != "plan-issue-123-1" {
				t.Fatalf("req.Slug = %q, want plan-issue-123-1", req.Slug)
			}
			if want := "plan: #123 Add full-text search"; req.Title != want {
				t.Fatalf("req.Title = %q, want %q", req.Title, want)
			}
		})
	}
}

// TestGuardIssuePlanCoordinator pins the plan-checkbox dedupe run on the locked
// store: a recorded coordinator (manual-parent row with the per-issue slug
// prefix) or any recorded pane for the issue blocks a second launch.
func TestGuardIssuePlanCoordinator(t *testing.T) {
	tests := []struct {
		name    string
		panes   []state.Pane
		wantErr string
	}{
		{
			name:    "empty store allows the launch",
			panes:   nil,
			wantErr: "",
		},
		{
			name:    "recorded coordinator for the issue blocks",
			panes:   []state.Pane{{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "plan-issue-123-1"}},
			wantErr: "issue #123 already has a plan session",
		},
		{
			name:    "coordinator for a different issue does not alias by prefix",
			panes:   []state.Pane{{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "plan-issue-1234-1"}},
			wantErr: "",
		},
		{
			// The coordinator closed after the live fan-out; its plan task rows
			// still own the issue, so a second coordinator (and spec regen) is
			// rejected.
			name:    "surviving plan task rows for the issue block",
			panes:   []state.Pane{{Parent: "plan:issue-123-add-search", TaskID: "base-types", Slug: "issue-123-add-search-base-types"}},
			wantErr: "issue #123 already has a plan session",
		},
		{
			name:    "recorded work pane for the issue blocks",
			panes:   []state.Pane{{Parent: "@watch", IssueNum: 123, Slug: "fix-search-123"}},
			wantErr: "issue #123 already has a fanout pane",
		},
		{
			name:    "prompt coordinator rows never block an issue launch",
			panes:   []state.Pane{{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "plan-prompt-1"}},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guardIssuePlanCoordinator(state.Store{Panes: tt.panes}, 123)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("guardIssuePlanCoordinator(store, 123) = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr):
				t.Fatalf("guardIssuePlanCoordinator(store, 123) = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestPlanPaneIssueNum pins the issue links both dedupe directions rely on
// (guardIssuePlanCoordinator, hasRecordedIssuePane, recordedIssueNumbers): a
// coordinator's own slug under the manual parent, and a plan task's parent ref
// following the briefing's issue-<num>-<title> naming.
func TestPlanPaneIssueNum(t *testing.T) {
	tests := []struct {
		name string
		pane state.Pane
		want int
		ok   bool
	}{
		{name: "issue coordinator slug parses", pane: state.Pane{Parent: panelaunch.ManualParentRef, Slug: "plan-issue-123-4"}, want: 123, ok: true},
		{name: "prompt coordinator slug is not an issue link", pane: state.Pane{Parent: panelaunch.ManualParentRef, Slug: "plan-prompt-4"}, ok: false},
		{name: "missing launch suffix is rejected", pane: state.Pane{Parent: panelaunch.ManualParentRef, Slug: "plan-issue-123"}, ok: false},
		{name: "non-numeric issue segment is rejected", pane: state.Pane{Parent: panelaunch.ManualParentRef, Slug: "plan-issue-abc-1"}, ok: false},
		{name: "empty issue segment is rejected", pane: state.Pane{Parent: panelaunch.ManualParentRef, Slug: "plan-issue--1"}, ok: false},
		{name: "plan task parent following the naming links", pane: state.Pane{Parent: "plan:issue-474-add-search", Slug: "issue-474-add-search-base"}, want: 474, ok: true},
		{name: "plan parent without the issue prefix has no link", pane: state.Pane{Parent: "plan:launch-plan", Slug: "launch-plan-base"}, ok: false},
		// A work pane whose generated slug happens to start with plan-issue-
		// (issue #999 titled "Plan issue 123 migration") must not alias #123.
		{name: "non-manual pane slug is never parsed", pane: state.Pane{Parent: "700", IssueNum: 999, Slug: "plan-issue-123-migration"}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := planPaneIssueNum(tt.pane)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("planPaneIssueNum(%+v) = %d, %v, want %d, %v", tt.pane, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestHasRecordedIssuePaneSeesPlanSessions pins the reverse dedupe: while a
// coordinator or its surviving plan task rows exist, the normal issue lane
// (launchStandaloneIssuePane and the watcher) must not start a second session
// for the same issue.
func TestHasRecordedIssuePaneSeesPlanSessions(t *testing.T) {
	store := state.Store{Panes: []state.Pane{
		{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "plan-issue-123-1"},
		{Parent: "plan:issue-474-add-search", TaskID: "base", Slug: "issue-474-add-search-base"},
	}}
	if !hasRecordedIssuePane(store, 123) {
		t.Fatal("hasRecordedIssuePane(store, 123) = false, want true for a recorded plan coordinator")
	}
	if !hasRecordedIssuePane(store, 474) {
		t.Fatal("hasRecordedIssuePane(store, 474) = false, want true for surviving plan task rows")
	}
	if hasRecordedIssuePane(store, 12) {
		t.Fatal("hasRecordedIssuePane(store, 12) = true, want false for a different issue")
	}
}

// TestLaunchParentIssueFanoutRejectsPlanSession pins the parent-lane guard: an
// issue owned by a plan session (here: surviving plan task rows) must not also
// fan out its children, even when it gained OPEN children after the plan
// coordinator launched.
func TestLaunchParentIssueFanoutRejectsPlanSession(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "plan:issue-123-add-search", TaskID: "base", Slug: "issue-123-add-search-base", PaneID: "%9"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}

	_, err = launchParentIssueFanout(repo, "fanout-test", "fanout", tuiIssueLaunchConfig(123, "claude", nil))
	if err == nil || !strings.Contains(err.Error(), "issue #123 already has a plan session") {
		t.Fatalf("launchParentIssueFanout() error = %v, want plan-session rejection", err)
	}
}

// TestLaunchIssuePlanFromTUIRejectsClosedIssue pins the launch-time re-fetch:
// a picker row gone stale (issue closed meanwhile) is rejected by state.
func TestLaunchIssuePlanFromTUIRejectsClosedIssue(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"issue view 7 --json number,title,state,body,labels")
  printf '{"number":7,"title":"Stale row","state":"CLOSED","body":"","labels":[]}'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	_, err := launchIssuePlanFromTUI(t.TempDir(), "fanout-test", "fanout", hooks.EmptyConfig(), 7, "claude", "codex")
	if err == nil || !strings.Contains(err.Error(), "issue #7 is not OPEN") {
		t.Fatalf("launchIssuePlanFromTUI(closed issue) error = %v, want not-OPEN rejection", err)
	}
}

// TestLaunchIssuePlanFromTUIBackstopsOpenChildren pins the gray-out backstop:
// the picker's child marker can be stale, so an issue that has OPEN children at
// launch time is rejected instead of getting a plan coordinator.
func TestLaunchIssuePlanFromTUIBackstopsOpenChildren(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"issue view 7 --json number,title,state,body,labels")
  printf '{"number":7,"title":"Epic","state":"OPEN","body":"","labels":[]}'
  ;;
"api --paginate --slurp repos/{owner}/{repo}/issues/7/sub_issues?per_page=100")
  printf '[[{"number":8,"title":"child","state":"open"}]]'
  ;;
"issue view 7 --json body -q .body")
  printf ''
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	_, err := launchIssuePlanFromTUI(t.TempDir(), "fanout-test", "fanout", hooks.EmptyConfig(), 7, "claude", "codex")
	if err == nil || !strings.Contains(err.Error(), "issue #7 has 1 open children; uncheck the plan checkbox") {
		t.Fatalf("launchIssuePlanFromTUI(open children) error = %v, want backstop rejection", err)
	}
}

// TestLaunchIssuePlanFromTUIValidatesBeforeGH pins the fail-fast validation:
// a bad issue number or an unknown agent name must be rejected before any gh
// call, so no gh binary is needed on PATH.
func TestLaunchIssuePlanFromTUIValidatesBeforeGH(t *testing.T) {
	tests := []struct {
		name        string
		issueNum    int
		coordinator string
		worker      string
		wantErr     string
	}{
		{name: "rejects non-positive issue number", issueNum: 0, coordinator: "claude", worker: "codex", wantErr: "issue number is required"},
		{name: "rejects unknown coordinator agent", issueNum: 7, coordinator: "bogus", worker: "codex", wantErr: `unknown agent "bogus"`},
		{name: "rejects unknown worker agent", issueNum: 7, coordinator: "claude", worker: "bogus", wantErr: `unknown agent "bogus"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// An empty PATH: a validation that leaked to a gh call would fail with a
			// gh-not-found error instead of the expected message, exposing the bug.
			t.Setenv("PATH", t.TempDir())
			_, err := launchIssuePlanFromTUI(t.TempDir(), "fanout-test", "fanout", hooks.EmptyConfig(), tt.issueNum, tt.coordinator, tt.worker)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("launchIssuePlanFromTUI() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLaunchPlanPromptFromTUIRejectsMultipleAgents(t *testing.T) {
	req := fanouttui.LaunchRequest{
		Prompt:     "Ship it",
		PlanFanout: true,
		Agents:     []string{"claude", "codex"},
	}
	_, err := launchPlanPromptFromTUI(t.TempDir(), "fanout-test", "fanout", hooks.Config{}, req)
	if err == nil || !strings.Contains(err.Error(), "select exactly one") {
		t.Fatalf("launchPlanPromptFromTUI() error = %v, want single-agent rejection", err)
	}
}
