package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func TestBufferedLaunchNoticeCollectsAndDeduplicatesWarnings(t *testing.T) {
	stderr := bytes.NewBufferString("[warn] child #501: bridge disabled\n[warn] child #501: bridge disabled\n[warn] child #502: base branch refresh skipped: offline\n")
	want := "child #501: bridge disabled; base branch refresh skipped: offline"
	if got := bufferedLaunchNotice(*stderr); got != want {
		t.Fatalf("bufferedLaunchNotice() = %q, want %q", got, want)
	}
}

func TestCoordinatorRuntimeRequestRemovesTmuxIdentityForHerdr(t *testing.T) {
	req := panelaunch.Request{ShellKey: "tmux-key", AgentStartGate: "tmux-gate"}
	herdrReq := coordinatorRuntimeRequest(backend.MutationJournaled, "500", req)
	if herdrReq.ShellKey != "" || herdrReq.AgentStartGate != "" || herdrReq.RuntimeParent != "500" {
		t.Fatalf("Herdr coordinator request = %+v, want no tmux identity", herdrReq)
	}
	if tmuxReq := coordinatorRuntimeRequest(backend.MutationAtomic, "500", req); tmuxReq.ShellKey != req.ShellKey || tmuxReq.AgentStartGate != req.AgentStartGate || tmuxReq.RuntimeParent != "" {
		t.Fatalf("tmux coordinator request = %+v, want unchanged tmux identity", tmuxReq)
	}
	if manualReq := coordinatorRuntimeRequest(backend.MutationJournaled, panelaunch.ManualParentRef, req); manualReq.RuntimeParent != "" {
		t.Fatalf("manual Herdr coordinator request = %+v, want independent synthetic identity", manualReq)
	}
}

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
// request: its one-line prompt invokes the fanout-plan skill on the full prompt
// written to the briefing file, its mode is explicit, and its liveness key
// survives into the request (the repo-root WorktreePath is too broad for
// path-based liveness, so the key is the row's identity).
func TestNewPlanPromptPaneRequestWritesSkillInvocation(t *testing.T) {
	const prompt = "Build a full-text search over issues.\nInclude ranking and filters."
	planMode := false
	req := newPlanPromptPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), prompt, &cliflags.Config{Agent: "claude", PlanMode: &planMode}, "shell-coordinator-key")

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
	if req.PlanMode() || req.LaunchMode != agent.ModeBuild {
		t.Fatalf("req.LaunchMode = %q, want explicit build coordinator", req.LaunchMode)
	}
	if req.ParentRef != panelaunch.ManualParentRef {
		t.Fatalf("req.ParentRef = %q, want %q", req.ParentRef, panelaunch.ManualParentRef)
	}
	if req.ShellKey != "shell-coordinator-key" {
		t.Fatalf("req.ShellKey = %q, want the liveness key passed through", req.ShellKey)
	}
}

func TestNewPlanPromptPaneRequestKeepsOpenCodeInBuildMode(t *testing.T) {
	planMode := true
	req := newPlanPromptPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), "Plan the migration", &cliflags.Config{Agent: "opencode", PlanMode: &planMode}, "shell-coordinator-key")

	if req.PlanMode() || req.LaunchMode != agent.ModeBuild {
		t.Fatalf("req.LaunchMode = %q, want explicit build coordinator for OpenCode", req.LaunchMode)
	}
}

func TestNewPlanPromptPaneRequestBoundsLongSingleLineTitle(t *testing.T) {
	prompt := strings.Repeat("x", 150_000)
	planMode := true
	req := newPlanPromptPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), prompt, &cliflags.Config{Agent: "codex", DryRun: true, PlanMode: &planMode}, "shell-coordinator-key")
	wantTitle := "plan: " + strings.Repeat("x", 54)

	if req.Title != wantTitle || req.DisplayNameOverride != wantTitle || req.ShortTitle != wantTitle {
		t.Fatalf("plan title/display/short lengths = %d/%d/%d, want bounded %d-byte title", len(req.Title), len(req.DisplayNameOverride), len(req.ShortTitle), len(wantTitle))
	}
	if req.Body != prompt || req.BriefingBody != prompt {
		t.Fatalf("long prompt body/briefing lengths = %d/%d, want %d", len(req.Body), len(req.BriefingBody), len(prompt))
	}
	if strings.Contains(req.Prompt, prompt) {
		t.Fatalf("launch prompt embeds the %d-byte briefing", len(prompt))
	}
	if !req.PlanMode() || req.CodexPlanStatusPath != "/tmp/fanout-codex-plan-repo--1.json" {
		t.Fatalf("plan mode/status = %t/%q, want Codex coordinator plan handshake", req.PlanMode(), req.CodexPlanStatusPath)
	}
}

// TestNewIssuePlanPaneRequestWritesIssueCoordinatorBrief pins the issue-sourced
// plan coordinator pane request: its one-line prompt invokes the fanout-plan
// skill on the issue-derived coordinator brief, and its briefing carries the
// issue title/body, the worker --agent override, and the "Refs #N" (never
// "Closes") requirement.
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
			planMode := true
			cfg := &cliflags.Config{Agent: tt.coordinator, DryRun: true, PlanMode: &planMode}
			req := newIssuePlanPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), issue, cfg, "codex", "shell-issue-plan-key")

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
			if !req.PlanMode() || req.LaunchMode != agent.ModePlan {
				t.Fatalf("req.LaunchMode = %q, want plan coordinator", req.LaunchMode)
			}
			if tt.coordinator == "codex" && req.CodexPlanStatusPath != "/tmp/fanout-codex-plan-repo--1.json" {
				t.Fatalf("codex status path = %q, want coordinator handshake path", req.CodexPlanStatusPath)
			}
			if tt.coordinator != "codex" && req.CodexPlanStatusPath != "" {
				t.Fatalf("non-Codex status path = %q, want empty", req.CodexPlanStatusPath)
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

// writeSavedPlanSpec writes .fanout/plans/<slug>.json declaring source, the
// provenance PlanLinkedIssueNums verifies before linking plan task rows.
func writeSavedPlanSpec(t *testing.T, projectRoot, slug, source string) {
	t.Helper()
	dir := filepath.Join(projectRoot, ".fanout", "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"version":1,"plan":{"slug":%q,"title":"t","source":%q},"tasks":[]}`, slug, source)
	if err := os.WriteFile(filepath.Join(dir, slug+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGuardIssuePlanCoordinator pins the plan-checkbox dedupe run on the locked
// store: a recorded coordinator, plan task rows whose saved spec declares the
// issue, or any recorded pane for the issue blocks a second launch.
func TestGuardIssuePlanCoordinator(t *testing.T) {
	tests := []struct {
		name    string
		panes   []state.Pane
		spec    [2]string // slug, source — written to .fanout/plans when set
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
			// still own the issue through the saved spec's declared source, so a
			// second coordinator (and spec regen) is rejected.
			name:    "surviving plan task rows with declared source block",
			panes:   []state.Pane{{Parent: "plan:issue-123-add-search", TaskID: "base-types", Slug: "issue-123-add-search-base-types"}},
			spec:    [2]string{"issue-123-add-search", "issue #123"},
			wantErr: "issue #123 already has a plan session",
		},
		{
			// An issue-like plan slug without declared provenance must not block
			// the issue (a hand-authored plan may just be named that way).
			name:    "issue-like plan slug without declared source never blocks",
			panes:   []state.Pane{{Parent: "plan:issue-123-migration", TaskID: "move", Slug: "issue-123-migration-move"}},
			spec:    [2]string{"issue-123-migration", "path-or-conversation-label"},
			wantErr: "",
		},
		{
			name:    "recorded work pane for the issue blocks",
			panes:   []state.Pane{{Parent: "@watch", IssueNum: 123, Slug: "fix-search-123"}},
			wantErr: "issue #123 already has a fanout pane",
		},
		{
			// A legacy/other-parent row identified only by its worktree suffix is
			// fanned in the normal lanes, so the plan lane refuses it too.
			name:    "worktree-suffix fallback row blocks",
			panes:   []state.Pane{{Parent: "900", IssueNum: 999, Slug: "api-client-123", WorktreePath: "/repo/.fanout/worktrees/api-client-123"}},
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
			root := t.TempDir()
			if tt.spec[0] != "" {
				writeSavedPlanSpec(t, root, tt.spec[0], tt.spec[1])
			}
			err := guardIssuePlanCoordinator(root, state.Store{Panes: tt.panes}, 123)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("guardIssuePlanCoordinator(root, store, 123) = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr):
				t.Fatalf("guardIssuePlanCoordinator(root, store, 123) = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestHasRecordedIssuePaneSeesPlanSessions pins the reverse dedupe: while a
// coordinator or its surviving plan task rows exist, the normal issue lane
// (launchStandaloneIssuePane and the watcher) must not start a second session
// for the same issue.
func TestHasRecordedIssuePaneSeesPlanSessions(t *testing.T) {
	root := t.TempDir()
	writeSavedPlanSpec(t, root, "issue-474-add-search", "issue #474")
	store := state.Store{Panes: []state.Pane{
		{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "plan-issue-123-1"},
		{Parent: "plan:issue-474-add-search", TaskID: "base", Slug: "issue-474-add-search-base"},
	}}
	if !hasRecordedIssuePane(root, store, 123) {
		t.Fatal("hasRecordedIssuePane(root, store, 123) = false, want true for a recorded plan coordinator")
	}
	if !hasRecordedIssuePane(root, store, 474) {
		t.Fatal("hasRecordedIssuePane(root, store, 474) = false, want true for surviving plan task rows")
	}
	if hasRecordedIssuePane(root, store, 12) {
		t.Fatal("hasRecordedIssuePane(root, store, 12) = true, want false for a different issue")
	}
}

func TestLinkedIssueSessionGuardsRejectSiblingOwnership(t *testing.T) {
	tests := []struct {
		name     string
		pane     state.Pane
		guard    func(string, state.Store) error
		wantText string
	}{
		{
			name:     "plan coordinator rejects sibling orchestrator",
			pane:     state.Pane{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "orchestrator-issue-123-1"},
			guard:    func(root string, store state.Store) error { return guardLinkedIssuePlanCoordinator(root, store, 123) },
			wantText: "issue #123 already has a fanout pane",
		},
		{
			name:     "orchestrator rejects sibling plan coordinator",
			pane:     state.Pane{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "plan-issue-123-1"},
			guard:    func(root string, store state.Store) error { return guardLinkedIssueOrchestrator(root, store, 123) },
			wantText: "issue #123 already has a plan session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initLifecycleRepo(t)
			sibling := filepath.Join(t.TempDir(), "sibling")
			gitCmdTest(t, repo, "worktree", "add", "-b", "issue-session-sibling", sibling, "HEAD")
			writeRawLifecycleState(t, sibling, tt.pane)
			store, err := state.LoadProject(repo)
			if err != nil {
				t.Fatal(err)
			}
			if guardErr := tt.guard(repo, store); guardErr == nil || !strings.Contains(guardErr.Error(), tt.wantText) {
				t.Fatalf("linked issue guard error = %v, want %q", guardErr, tt.wantText)
			}
		})
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
	writeSavedPlanSpec(t, repo, "issue-123-add-search", "issue #123")

	_, err = launchParentIssueFanout(repo, "fanout-test", "fanout", tuiIssueLaunchConfig(123, "claude", nil))
	if err == nil || !strings.Contains(err.Error(), "issue #123 already has a plan session") {
		t.Fatalf("launchParentIssueFanout() error = %v, want plan-session rejection", err)
	}
}

// installFakeAgentCLIs puts executable claude/codex stubs next to the fake gh
// so the up-front agent ValidateInstalled checks pass in agent-less CI
// environments; the shim dir already leads PATH.
func installFakeAgentCLIs(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLaunchIssuePlanFromTUIRejectsClosedIssue pins the launch-time re-fetch:
// a picker row gone stale (issue closed meanwhile) is rejected by state.
func TestLaunchIssuePlanFromTUIRejectsClosedIssue(t *testing.T) {
	shimArgs := installTUIWatcherGHScript(t, `
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
	installFakeAgentCLIs(t, filepath.Dir(shimArgs))
	_, err := launchIssuePlanFromTUI(t.TempDir(), "fanout-test", "fanout", hooks.EmptyConfig(), 7, "claude", "codex")
	if err == nil || !strings.Contains(err.Error(), "issue #7 is not OPEN") {
		t.Fatalf("launchIssuePlanFromTUI(closed issue) error = %v, want not-OPEN rejection", err)
	}
}

// TestLaunchIssuePlanFromTUIBackstopsOpenChildren pins the gray-out backstop:
// the picker's child marker can be stale, so an issue that has OPEN children at
// launch time is rejected instead of getting a plan coordinator.
func TestLaunchIssuePlanFromTUIBackstopsOpenChildren(t *testing.T) {
	shimArgs := installTUIWatcherGHScript(t, `
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
	installFakeAgentCLIs(t, filepath.Dir(shimArgs))
	_, err := launchIssuePlanFromTUI(t.TempDir(), "fanout-test", "fanout", hooks.EmptyConfig(), 7, "claude", "codex")
	if err == nil || !strings.Contains(err.Error(), "issue #7 has 1 open children; uncheck the plan checkbox") {
		t.Fatalf("launchIssuePlanFromTUI(open children) error = %v, want backstop rejection", err)
	}
}

// TestLaunchIssuePlanFromTUIValidatesBeforeGH pins the fail-fast validation:
// a bad issue number, an unknown agent name, or an uninstalled agent CLI must
// be rejected before any gh call, so no gh binary is needed on PATH. The
// worker is every task's default agent, so its missing CLI must fail here
// instead of after the coordinator pane launched.
func TestLaunchIssuePlanFromTUIValidatesBeforeGH(t *testing.T) {
	tests := []struct {
		name        string
		issueNum    int
		coordinator string
		worker      string
		installed   []string
		wantErr     string
	}{
		{name: "rejects non-positive issue number", issueNum: 0, coordinator: "claude", worker: "codex", wantErr: "issue number is required"},
		{name: "rejects unknown coordinator agent", issueNum: 7, coordinator: "bogus", worker: "codex", wantErr: `unknown agent "bogus"`},
		{name: "rejects unknown worker agent", issueNum: 7, coordinator: "claude", worker: "bogus", wantErr: `unknown agent "bogus"`},
		{name: "rejects uninstalled coordinator agent", issueNum: 7, coordinator: "claude", worker: "codex", installed: []string{"codex"}, wantErr: `agent "claude" is not installed`},
		{name: "rejects uninstalled worker agent", issueNum: 7, coordinator: "claude", worker: "codex", installed: []string{"claude"}, wantErr: `agent "codex" is not installed`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// PATH holds only the case's fake agent CLIs: a validation that leaked
			// to a gh call would fail with a gh-not-found error instead of the
			// expected message, exposing the bug.
			binDir := t.TempDir()
			for _, name := range tt.installed {
				if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", binDir)
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

func TestLaunchPlanPromptFromTUIReturnsCoordinatorPaneID(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	isolateBackendEnv(t)
	installFakeExecutable(t, "claude")
	installTUITmuxShim(t, "%88")
	req := fanouttui.LaunchRequest{
		Prompt:     "Ship search",
		PlanFanout: true,
		Agents:     []string{"claude"},
	}

	result, err := launchPlanPromptFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CreatedPaneIDs) != 1 || result.CreatedPaneIDs[0] != "%88" {
		t.Fatalf("created pane ids = %#v, want [%%88]", result.CreatedPaneIDs)
	}
	if !strings.Contains(result.Notice, "started plan coordinator (claude)") {
		t.Fatalf("notice = %q, want coordinator success", result.Notice)
	}
}

func TestLaunchPlanPromptFromTUIReturnsClaudeModeFallbackNotice(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	isolateBackendEnv(t)
	binDir := t.TempDir()
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nprintf '2.1.206 (Claude Code)\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	installTUITmuxShim(t, "%89")
	t.Setenv("FANOUT_NEW_SESSION_PLAN_MODE", "1")

	result, err := launchPlanPromptFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.LaunchRequest{
		Prompt:     "Ship search",
		PlanFanout: true,
		Agents:     []string{"claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Notice, "Claude Code 2.1.207+ is required") {
		t.Fatalf("notice = %q, want Claude Plan Mode fallback warning", result.Notice)
	}
}

func TestTUIAgentLaunchFailuresDoNotOverwriteExistingState(t *testing.T) {
	tests := []struct {
		name   string
		launch func(string) error
	}{
		{
			name: "manual prompt",
			launch: func(repo string) error {
				_, err := launchManualPaneFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.LaunchRequest{
					Prompt: "inspect workspace",
					Agents: []string{"claude"},
				})
				return err
			},
		},
		{
			name: "plan prompt",
			launch: func(repo string) error {
				_, err := launchPlanPromptFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.LaunchRequest{
					Prompt:     "plan workspace changes",
					PlanFanout: true,
					Agents:     []string{"claude"},
				})
				return err
			},
		},
		{
			name: "issue plan",
			launch: func(repo string) error {
				installTUIWatcherGHScript(t, `
case "$args" in
"issue view 7 --json number,title,state,body,labels")
  printf '{"number":7,"title":"Plan issue","state":"OPEN","body":"body","labels":[]}'
  ;;
"api --paginate --slurp repos/{owner}/{repo}/issues/7/sub_issues?per_page=100")
  printf '[[]]'
  ;;
"issue view 7 --json body -q .body")
  printf 'body\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
				_, err := launchIssuePlanFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), 7, "claude", "claude")
				return err
			},
		},
		{
			name: "attached agent",
			launch: func(repo string) error {
				_, err := launchAttachedAgent(repo, "%1", "fanout", hooks.EmptyConfig(), fanouttui.AttachLaunchRequest{
					Prompt: "inspect source pane",
					Agents: []string{"claude"},
					Target: fanouttui.AttachTarget{
						TargetPath:   repo,
						Backend:      backend.Herdr,
						SourceParent: panelaunch.ManualParentRef,
					},
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			initTUITestGitRepo(t, repo)
			installFakeExecutable(t, "claude")
			isolateBackendEnv(t)
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("TMUX", "nested-tmux")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			locked, lockErr := state.LockProject(repo)
			if lockErr != nil {
				t.Fatal(lockErr)
			}
			if err := locked.RecordPane(state.Pane{
				Parent:       panelaunch.ManualParentRef,
				IssueNum:     -1,
				Slug:         "legacy-manual",
				PaneID:       "%90",
				WorktreePath: repo,
			}); err != nil {
				t.Fatal(err)
			}
			if err := locked.Unlock(); err != nil {
				t.Fatal(err)
			}
			stateBefore, err := os.ReadFile(state.Path(repo))
			if err != nil {
				t.Fatal(err)
			}
			err = tt.launch(repo)
			if err == nil {
				t.Fatal("launch succeeded with the test Herdr shim")
			}
			stateAfter, err := os.ReadFile(state.Path(repo))
			if err != nil {
				t.Fatal(err)
			}
			if string(stateAfter) != string(stateBefore) {
				t.Fatalf("state changed before herdr rejection:\n%s", stateAfter)
			}
			for _, path := range []string{
				filepath.Join(repo, ".fanout", "briefings"),
				filepath.Join(repo, ".fanout", "worktrees"),
			} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("herdr rejection created %s: %v", path, statErr)
				}
			}
		})
	}
}

func TestAttachResolverParentUsesSourceProvenance(t *testing.T) {
	repo := t.TempDir()
	writeSavedPlanSpec(t, repo, "issue-plan", "issue #425")
	tests := []struct {
		name   string
		target fanouttui.AttachTarget
		want   string
	}{
		{
			name: "watch row",
			target: fanouttui.AttachTarget{
				SourceParent:   panelaunch.WatchParentRef,
				SourceIssueNum: 425,
			},
			want: "425",
		},
		{
			name: "empty parent",
			want: panelaunch.ManualParentRef,
		},
		{
			name: "manual coordinator provenance",
			target: fanouttui.AttachTarget{
				SourceParent:   panelaunch.ManualParentRef,
				SourceIssueNum: 425,
			},
			want: "425",
		},
		{
			name:   "issue sourced plan task",
			target: fanouttui.AttachTarget{SourceParent: "plan:issue-plan"},
			want:   "425",
		},
		{
			name:   "ordinary parent",
			target: fanouttui.AttachTarget{SourceParent: "423"},
			want:   "423",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attachResolverParent(repo, tt.target); got != tt.want {
				t.Fatalf("attachResolverParent(%+v) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}
