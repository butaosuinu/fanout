package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/naming"
	"github.com/butaosuinu/fanout/internal/planspec"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/worktree"
)

func TestManualPromptWithBriefingActionPreservesTrailingMention(t *testing.T) {
	// A prompt ending in an @-mention must keep a whitespace terminator before
	// the appended sentence, else "@cmd/main.go" + ". read" merges into the
	// non-existent path "@cmd/main.go.".
	got := manualPromptWithBriefingAction("look at @cmd/main.go", "/tmp/b.md", "begin")
	if strings.Contains(got, "@cmd/main.go.") {
		t.Fatalf("trailing mention corrupted by the briefing sentence: %q", got)
	}
	if !strings.Contains(got, "@cmd/main.go . read /tmp/b.md") {
		t.Fatalf("mention not space-separated from sentence: %q", got)
	}

	// A prompt that does not end in a mention keeps the plain ". read" join.
	plain := manualPromptWithBriefingAction("inspect the API", "/tmp/b.md", "begin")
	if !strings.Contains(plain, "inspect the API. read /tmp/b.md") {
		t.Fatalf("non-mention prompt should keep the plain period join: %q", plain)
	}
}

func TestShortIssueTitleTruncatesOnRuneBoundary(t *testing.T) {
	title := strings.Repeat("あ", 61)

	got := shortIssueTitle(title)

	if !utf8.ValidString(got) {
		t.Fatalf("short title is invalid UTF-8: %q", got)
	}
	if gotRunes := utf8.RuneCountInString(got); gotRunes != 60 {
		t.Fatalf("short title rune count = %d, want 60", gotRunes)
	}
	if got != strings.Repeat("あ", 60) {
		t.Fatalf("unexpected short title: %q", got)
	}
}

func TestShortIssueTitleKeepsSixtyRunes(t *testing.T) {
	title := strings.Repeat("界", 60)

	if got := shortIssueTitle(title); got != title {
		t.Fatalf("shortIssueTitle changed 60-rune title:\nwant %q\ngot  %q", title, got)
	}
}

func TestStatePaneCapturesCreatedPaneFields(t *testing.T) {
	now := time.Date(2026, 6, 4, 1, 2, 3, 0, time.UTC)
	req := paneRequest{
		ParentRef:           "81",
		Number:              83,
		Slug:                "state-idempotency-83",
		DisplayNameOverride: "State Idempotency",
		BranchName:          "fanout/state-idempotency-83",
		Prompt:              "[fanout #83 of #81] state-idempotency-83: read /tmp/fanout-fanout-83.md and begin.",
		Agent:               "codex",
		Wave:                "wave5",
		CodexPlanMode:       true,
		Worktree:            worktree.Plan{BaseBranch: "main"},
	}

	got := statePane(req, "%42", "/repo/.fanout/worktrees/state-idempotency-83", now, codexPlanTUIStatus{
		ThreadID:  "thread-1",
		SessionID: "session-1",
	})

	if got.Parent != "81" || got.IssueNum != 83 || got.PaneID != "%42" {
		t.Fatalf("state pane identity = %+v", got)
	}
	if got.DisplayName != "State Idempotency" {
		t.Fatalf("displayName = %q, want State Idempotency", got.DisplayName)
	}
	if got.CreatedAt != "2026-06-04T01:02:03Z" {
		t.Fatalf("createdAt = %q", got.CreatedAt)
	}
	if got.Wave != "wave5" {
		t.Fatalf("wave = %q, want wave5", got.Wave)
	}
	if got.BaseBranch != "main" {
		t.Fatalf("baseBranch = %q, want main", got.BaseBranch)
	}
	if got.AgentStatus != "running" {
		t.Fatalf("agentStatus = %q, want running (起動時記録)", got.AgentStatus)
	}
	if !got.CodexPlanMode {
		t.Fatal("codexPlanMode = false, want passthrough of req.CodexPlanMode")
	}
	if got.CodexThreadID != "thread-1" || got.CodexSessionID != "session-1" {
		t.Fatalf("codex session ids = %q/%q, want thread-1/session-1", got.CodexThreadID, got.CodexSessionID)
	}
}

func TestStatePaneCapturesTaskID(t *testing.T) {
	now := time.Date(2026, 6, 13, 1, 2, 3, 0, time.UTC)
	req := paneRequest{
		ParentRef:  "plan:launch-plan",
		TaskID:     "api-client",
		Slug:       "extract-api-client-api-client",
		BranchName: "fanout/extract-api-client-api-client",
		Prompt:     "[fanout api-client of plan:launch-plan]",
		Agent:      "claude",
		Worktree:   worktree.Plan{BaseBranch: "main"},
	}

	got := statePane(req, "%42", "/repo/.fanout/worktrees/extract-api-client-api-client", now, codexPlanTUIStatus{})

	if got.Parent != "plan:launch-plan" || got.IssueNum != 0 || got.TaskID != "api-client" {
		t.Fatalf("task state identity = %+v, want plan parent, issueNum 0, taskID", got)
	}
}

func TestCreatePaneAcceptsManualRequestWithoutParentIssue(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", DryRun: true, NoRefresh: true}
	if got := newManualPaneRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), manualPaneOptions{Title: "First Manual"}); got.Number != -1 {
		t.Fatalf("first manual number = %d, want -1", got.Number)
	}
	store := state.Store{Panes: []state.Pane{{Parent: manualPaneParentRef, IssueNum: -1}}}
	req := newManualPaneRequest(cfg, "/repo", store, hooks.EmptyConfig(), manualPaneOptions{
		Title:  "Manual Diagnostics",
		Body:   "extra context",
		Agent:  "codex",
		Prompt: "inspect the workspace",
	})
	if req.ParentRef != manualPaneParentRef || req.Number != -2 {
		t.Fatalf("manual identity = parent %q number %d, want %q -2", req.ParentRef, req.Number, manualPaneParentRef)
	}
	if req.Agent != "codex" || !strings.Contains(req.Prompt, "inspect the workspace") {
		t.Fatalf("manual launch = prompt %q agent %q", req.Prompt, req.Agent)
	}
	if req.BriefingPath != "/tmp/fanout-repo--2.md" || req.BriefingBody != "extra context" {
		t.Fatalf("manual briefing = path %q body %q", req.BriefingPath, req.BriefingBody)
	}
	if !strings.Contains(req.Prompt, "read /tmp/fanout-repo--2.md for additional context and begin") {
		t.Fatalf("manual prompt does not reference briefing path: %q", req.Prompt)
	}
	_ = os.Remove(req.BriefingPath)
	t.Cleanup(func() { _ = os.Remove(req.BriefingPath) })

	var stdout, stderr bytes.Buffer
	lg := log.NewWith(&stdout, &stderr, false)
	info := &fanoutruntime.Info{Target: "%caller", ProjectRoot: "/repo"}
	if !createPane(cfg, lg, info, req, nil, log.Palette{}, "fanout") {
		t.Fatalf("createPane() = false, stderr:\n%s", stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"#-2: Manual Diagnostics", "slug -> manual-2-manual-diagnostics-pane", "dry-run complete"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(req.BriefingPath); !os.IsNotExist(err) {
		t.Fatalf("manual dry-run wrote briefing file %s: %v", req.BriefingPath, err)
	}
}

func TestNewManualPaneRequestSkipsOrphanedWorktreeSlug(t *testing.T) {
	repo := t.TempDir()
	cfg := &cliflags.Config{Agent: "claude", DryRun: true, NoRefresh: true}
	// Simulate a state-only close: manual-1's worktree dir survives with no state
	// row, so the fresh same-titled request must skip that slug, not regenerate it.
	orphan := filepath.Join(repo, ".fanout", "worktrees", "manual-1-inspect-api-pane")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	req := newManualPaneRequest(cfg, repo, state.Store{}, hooks.EmptyConfig(), manualPaneOptions{Title: "inspect api"})

	if req.Slug != "manual-2-inspect-api-pane" || req.BranchName != "fanout/manual-2-inspect-api-pane" {
		t.Fatalf("slug/branch = %q/%q, want manual-2-inspect-api-pane (skip orphaned manual-1)", req.Slug, req.BranchName)
	}
	if req.Number != -2 {
		t.Fatalf("number = %d, want -2 (still state-unique after the skip)", req.Number)
	}
}

func TestNewManualPaneRequestCodexPlanModeUsesPlanControllerAndInlinePrompt(t *testing.T) {
	codexPlanMode := true
	cfg := &cliflags.Config{
		Agent:         "codex",
		DryRun:        true,
		NoRefresh:     true,
		CodexPlanMode: &codexPlanMode,
	}
	req := newManualPaneRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), manualPaneOptions{
		Title:  "Inspect API",
		Agent:  "codex",
		Prompt: "Inspect API",
	})

	if !req.CodexPlanMode {
		t.Fatal("CodexPlanMode = false, want true")
	}
	if req.BriefingPath != "" || req.BriefingBody != "" {
		t.Fatalf("manual Codex Plan Mode should not use briefing file: path %q body %q", req.BriefingPath, req.BriefingBody)
	}
	if req.CodexPlanStatusPath != "/tmp/fanout-codex-plan-repo--1.json" {
		t.Fatalf("codex plan status path = %q", req.CodexPlanStatusPath)
	}
	for _, want := range []string{
		"manual fanout Codex Plan Mode session",
		"Body:\nInspect API",
		"Before presenting a plan, follow normal Codex planning behavior",
		"After that investigation, present the implementation plan",
		"<proposed_plan>...</proposed_plan>",
	} {
		if !strings.Contains(req.Prompt, want) {
			t.Fatalf("manual plan prompt missing %q:\n%s", want, req.Prompt)
		}
	}
	for _, unexpected := range []string{
		"You are assigned GitHub issue",
		"commit and push",
		"Open a pull request",
		"$post-work-review",
		"codex review --uncommitted",
		"Your first response must",
		"read /tmp/",
	} {
		if strings.Contains(req.Prompt, unexpected) {
			t.Fatalf("manual plan prompt contains %q:\n%s", unexpected, req.Prompt)
		}
	}

	cmd, err := buildAgentCommand(cfg, req, "fanout-go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"fanout-go __codex-plan-tui --codex codex",
		"--prompt 'You are starting a manual fanout Codex Plan Mode session in this repository.",
		"<proposed_plan>...</proposed_plan>",
		"--status-file /tmp/fanout-codex-plan-repo--1.json",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("agent command missing %q:\n%s", want, cmd)
		}
	}
}

func TestNewManualPaneRequestCodexPlanModePreservesMultilinePrompt(t *testing.T) {
	codexPlanMode := true
	cfg := &cliflags.Config{Agent: "codex", DryRun: true, CodexPlanMode: &codexPlanMode}
	req := newManualPaneRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), manualPaneOptions{
		Title:  "Inspect API",
		Body:   "Inspect API\n\nCheck handlers",
		Agent:  "codex",
		Prompt: "Inspect API",
	})

	if req.BriefingPath != "" || req.BriefingBody != "" {
		t.Fatalf("manual Codex Plan Mode should not use briefing file: path %q body %q", req.BriefingPath, req.BriefingBody)
	}
	if !strings.Contains(req.Prompt, "Body:\nInspect API\n\nCheck handlers") {
		t.Fatalf("manual plan prompt did not preserve multiline prompt:\n%s", req.Prompt)
	}
	if !strings.Contains(req.Prompt, "<proposed_plan>...</proposed_plan>") {
		t.Fatalf("manual plan prompt = %q, want plan action", req.Prompt)
	}
	cmd, err := buildAgentCommand(cfg, req, "fanout-go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "Body:\nInspect API\n\nCheck handlers") {
		t.Fatalf("manual plan command did not preserve multiline prompt:\n%s", cmd)
	}
}

func TestPlanAndManualPaneRequestsAllowMissingOrigin(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude"}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "Do it"}

	taskReq := newTaskPaneRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)
	if !taskReq.Worktree.AllowMissingOrigin {
		t.Fatal("task pane AllowMissingOrigin = false, want true")
	}
	if taskReq.Worktree.RefreshBestEffort {
		t.Fatal("task pane RefreshBestEffort = true, want false (plan tasks keep strict refresh)")
	}

	manualReq := newManualPaneRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), manualPaneOptions{Title: "Manual diagnostics"})
	if !manualReq.Worktree.AllowMissingOrigin {
		t.Fatal("manual pane AllowMissingOrigin = false, want true")
	}
	if !manualReq.Worktree.RefreshBestEffort {
		t.Fatal("manual pane RefreshBestEffort = false, want true (TUI new sessions tolerate a dirty local base)")
	}
}

func TestCreatePaneIssueDryRunDoesNotWriteBriefing(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := &cliflags.Config{ParentRef: "100", Agent: "claude", BaseBranch: "main", DryRun: true, NoRefresh: true}
	req := newPaneRequest(cfg, projectRoot, ghissue.Issue{Number: 101, Title: "First child", Body: "body"}, settings.Defaults(), hooks.EmptyConfig(), false, nil)

	assertCreatePaneDryRunDoesNotWriteBriefing(t, cfg, projectRoot, req)
}

func TestCreatePaneTaskDryRunDoesNotWriteBriefing(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := &cliflags.Config{Agent: "claude", BaseBranch: "main", DryRun: true, NoRefresh: true}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "## Goal\nExtract it"}
	req := newTaskPaneRequest(cfg, projectRoot, spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	assertCreatePaneDryRunDoesNotWriteBriefing(t, cfg, projectRoot, req)
}

func assertCreatePaneDryRunDoesNotWriteBriefing(t *testing.T, cfg *cliflags.Config, projectRoot string, req paneRequest) {
	t.Helper()
	if !cfg.DryRun {
		t.Fatal("test helper requires cfg.DryRun")
	}
	if req.BriefingPath == "" || req.BriefingBody == "" {
		t.Fatalf("briefing path/body must be populated: path %q body len %d", req.BriefingPath, len(req.BriefingBody))
	}
	_ = os.Remove(req.BriefingPath)
	t.Cleanup(func() { _ = os.Remove(req.BriefingPath) })

	var stdout, stderr bytes.Buffer
	lg := log.NewWith(&stdout, &stderr, false)
	info := &fanoutruntime.Info{Target: "%caller", ProjectRoot: projectRoot}
	if !createPane(cfg, lg, info, req, nil, log.Palette{}, "fanout") {
		t.Fatalf("createPane() = false, stderr:\n%s", stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), "briefing size") {
		t.Fatalf("dry-run output missing briefing size:\n%s", stdout.String())
	}
	if _, err := os.Stat(req.BriefingPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote briefing file %s: %v", req.BriefingPath, err)
	}
}

func TestManualPaneSlugBoundsPromptDerivedSlug(t *testing.T) {
	got := manualPaneSlug(strings.Repeat("a", naming.MaxSlugLength+50), -12)

	if len(got) > naming.MaxSlugLength {
		t.Fatalf("manualPaneSlug len = %d, want <= %d: %q", len(got), naming.MaxSlugLength, got)
	}
	if !strings.HasPrefix(got, "manual-12-") || !strings.HasSuffix(got, "-pane") {
		t.Fatalf("manualPaneSlug = %q, want manual-12-...-pane", got)
	}
	if strings.HasSuffix(got, "-12") {
		t.Fatalf("manualPaneSlug = %q, must not end with an issue-like numeric suffix", got)
	}
}

func TestNewPaneRequestQualifiesDefaultSlugForSharedChild(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "claude"}
	issue := ghissue.Issue{Number: 501, Title: "Shared child", Body: "body"}

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), true, nil)

	if got.Slug != "shared-child-parent-200-501" {
		t.Fatalf("slug = %q, want shared-child-parent-200-501", got.Slug)
	}
	if got.BranchName != "fanout/shared-child-parent-200-501" {
		t.Fatalf("branch = %q, want fanout/shared-child-parent-200-501", got.BranchName)
	}
	if !strings.Contains(got.Prompt, "[fanout #501 of #200] shared-child-parent-200-501:") {
		t.Fatalf("prompt = %q, want parent-qualified slug", got.Prompt)
	}
}

func TestNewPaneRequestPassesResolvedBaseBranchToBriefing(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "claude", BaseBranch: "release/v1"}
	issue := ghissue.Issue{Number: 501, Title: "Release child", Body: "body"}

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)

	if !strings.Contains(got.BriefingBody, "git diff --name-only release/v1...HEAD") {
		t.Fatalf("briefing did not include selected base branch:\n%s", got.BriefingBody)
	}
}

func TestNewPaneRequestCarriesIssueWave(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "claude"}
	issue := ghissue.Issue{Number: 501, Title: "Wave child", Body: "body", Wave: "wave5"}

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)

	if got.Wave != "wave5" {
		t.Fatalf("Wave = %q, want wave5", got.Wave)
	}
}

func TestNewPaneRequestUsesIssueAgentOverride(t *testing.T) {
	cfg := &cliflags.Config{
		ParentRef: "200",
		Agent:     "claude",
		AgentOverrides: []cliflags.AgentOverride{
			{Target: "501", Name: "codex"},
		},
	}
	issue := ghissue.Issue{Number: 501, Title: "Codex child", Body: "body"}

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)

	if got.Agent != "codex" {
		t.Fatalf("Agent = %q, want codex", got.Agent)
	}
	if !strings.Contains(got.BriefingBody, "$post-work-review") {
		t.Fatalf("briefing did not use codex-specific guidance:\n%s", got.BriefingBody)
	}
	if strings.Contains(got.BriefingBody, "Optional: Agent Teams") {
		t.Fatalf("codex briefing contains Claude-only Agent Teams guidance:\n%s", got.BriefingBody)
	}
}

func TestNewWatchPaneRequestUsesReservedParentAndIssueBriefing(t *testing.T) {
	codexPlanMode := true
	cfg := &cliflags.Config{
		ParentRef:     "220",
		Agent:         "codex",
		BaseBranch:    "main",
		BranchPrefix:  "watch/",
		CodexPlanMode: &codexPlanMode,
	}
	issue := ghissue.Issue{Number: 223, Title: "Watch runtime helper", Body: "body"}

	got := newWatchPaneRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig())

	if got.ParentRef != watchPaneParentRef || got.Number != 223 || got.TaskID != "" {
		t.Fatalf("watch identity = parent %q number %d task %q, want %q/223 with no task", got.ParentRef, got.Number, got.TaskID, watchPaneParentRef)
	}
	if got.Slug != "watch-runtime-helper-223" || got.BranchName != "watch/watch-runtime-helper-223" {
		t.Fatalf("slug/branch = %q/%q", got.Slug, got.BranchName)
	}
	if got.Worktree.WorktreePath != "/repo/.fanout/worktrees/watch-runtime-helper-223" {
		t.Fatalf("worktree path = %q", got.Worktree.WorktreePath)
	}
	if got.BriefingPath != "/tmp/fanout-repo-223.md" {
		t.Fatalf("briefing path = %q", got.BriefingPath)
	}
	wantPrompt := "[fanout #223 of #@watch] watch-runtime-helper-223: Watch runtime helper. read /tmp/fanout-repo-223.md and begin."
	if got.Prompt != wantPrompt {
		t.Fatalf("prompt = %q, want %q", got.Prompt, wantPrompt)
	}
	if got.CodexPlanMode || got.CodexPlanStatusPath != "" {
		t.Fatalf("codex plan mode = %t status %q, want disabled for watch work pane", got.CodexPlanMode, got.CodexPlanStatusPath)
	}
	for _, want := range []string{
		"You are assigned GitHub issue #223",
		`Open a pull request with "Closes #223"`,
		"Closes #223",
	} {
		if !strings.Contains(got.BriefingBody, want) {
			t.Fatalf("briefing missing %q:\n%s", want, got.BriefingBody)
		}
	}
	if strings.Contains(got.BriefingBody, "<proposed_plan>") {
		t.Fatalf("watch briefing used Codex Plan Mode body:\n%s", got.BriefingBody)
	}

	pane := statePane(got, "%42", got.Worktree.WorktreePath, time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC), codexPlanTUIStatus{})
	if pane.Parent != watchPaneParentRef || pane.IssueNum != 223 {
		t.Fatalf("state key = %q/%d, want %q/223", pane.Parent, pane.IssueNum, watchPaneParentRef)
	}
}

func TestNewPaneRequestPassesResolvedSettingsAgentAndTeamToBriefing(t *testing.T) {
	cfg := &cliflags.Config{
		ParentRef:  "100",
		Agent:      "claude",
		BaseBranch: "main",
		AgentOverrides: []cliflags.AgentOverride{
			{Target: "501", Name: "codex"},
		},
	}
	resolvedSettings := settings.Defaults()
	resolvedSettings.AutoPullRequest = false
	issue := ghissue.Issue{Number: 501, Title: "First child", Body: "body"}
	teamCtx := buildTeamContext("/repo/project_root", "100", []ghissue.Issue{
		issue,
		{Number: 502, Title: "Second child"},
	})

	got := newPaneRequest(cfg, "/repo/project_root", issue, resolvedSettings, hooks.EmptyConfig(), false, teamCtx)

	if got.Agent != "codex" {
		t.Fatalf("Agent = %q, want codex from issue override", got.Agent)
	}
	for _, want := range []string{
		"## Coordinating with your sibling panes",
		"You are the pane for issue #501 (parent #100)",
		"- #501: First child (you)",
		"- #502: Second child",
		"/tmp/fanout-project_root-100.db",
		"$post-work-review",
		"Only after the review loop is clean should you commit and push the branch",
	} {
		if !strings.Contains(got.BriefingBody, want) {
			t.Fatalf("briefing missing %q:\n%s", want, got.BriefingBody)
		}
	}
	for _, unwanted := range []string{
		`Open a pull request with "Closes #501"`,
		"structure the PR body",
		"Diagram gate",
		"Optional: Agent Teams",
		"run the `/code-review` slash command",
		"open the PR",
	} {
		if strings.Contains(got.BriefingBody, unwanted) {
			t.Fatalf("briefing contains %q despite resolved settings/agent:\n%s", unwanted, got.BriefingBody)
		}
	}
}

func TestNewPaneRequestCodexPlanModeUsesPlanPromptAndBriefing(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "codex", CodexPlanMode: new(true)}
	issue := ghissue.Issue{Number: 501, Title: "Plan child", Body: "body"}

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)

	if !got.CodexPlanMode {
		t.Fatal("CodexPlanMode = false, want true")
	}
	if !strings.Contains(got.Prompt, "read /tmp/fanout-repo-501.md and investigate, then propose a plan.") {
		t.Fatalf("prompt = %q, want plan action", got.Prompt)
	}
	if !strings.Contains(got.BriefingBody, "Before presenting a plan, follow normal Codex planning behavior") {
		t.Fatalf("briefing did not require investigation before planning:\n%s", got.BriefingBody)
	}
	if !strings.Contains(got.BriefingBody, "<proposed_plan>...</proposed_plan>") {
		t.Fatalf("briefing did not require proposed_plan wrapper:\n%s", got.BriefingBody)
	}
	for _, unexpected := range []string{"commit and push", "Open a pull request", "Your first response must"} {
		if strings.Contains(got.BriefingBody, unexpected) {
			t.Fatalf("plan briefing contains implementation instruction %q:\n%s", unexpected, got.BriefingBody)
		}
	}
}

func TestNewTaskPaneRequestUsesTaskBriefingPathAndPrompt(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", BaseBranch: "release/v1"}
	spec := planspec.Spec{
		Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"},
	}
	task := planspec.Task{
		ID:          "api-client",
		Title:       "Extract API client",
		Briefing:    "## Goal\nExtract it",
		DisplayName: "API client",
		Wave:        "2",
	}

	got := newTaskPaneRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	if got.ParentRef != "plan:launch-plan" || got.TaskID != "api-client" || got.Number != 0 {
		t.Fatalf("task identity = parent %q task %q issue %d", got.ParentRef, got.TaskID, got.Number)
	}
	if got.Slug != "launch-plan-extract-api-client-api-client" || got.BranchName != "fanout/launch-plan-extract-api-client-api-client" {
		t.Fatalf("slug/branch = %q/%q", got.Slug, got.BranchName)
	}
	if got.BriefingPath != "/tmp/fanout-repo-launch%2Dplan-api%2Dclient.md" {
		t.Fatalf("briefing path = %q", got.BriefingPath)
	}
	if !strings.Contains(got.Prompt, "[fanout api-client of plan:launch-plan] launch-plan-extract-api-client-api-client: Extract API client. read /tmp/fanout-repo-launch%2Dplan-api%2Dclient.md and begin.") {
		t.Fatalf("prompt = %q", got.Prompt)
	}
	if got.DisplayNameOverride != "API client" || got.Wave != "2" {
		t.Fatalf("display/wave = %q/%q", got.DisplayNameOverride, got.Wave)
	}
	if !strings.Contains(got.BriefingBody, "Plan: launch-plan / Task: api-client") {
		t.Fatalf("task briefing missing plan/task footer:\n%s", got.BriefingBody)
	}
}

func TestNewTaskPaneRequestUsesTaskAgentOverride(t *testing.T) {
	cfg := &cliflags.Config{
		Agent:      "claude",
		BaseBranch: "main",
		AgentOverrides: []cliflags.AgentOverride{
			{Target: "api-client", Name: "codex"},
		},
	}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "## Goal\nExtract it"}

	got := newTaskPaneRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	if got.Agent != "codex" {
		t.Fatalf("Agent = %q, want codex", got.Agent)
	}
	if !strings.Contains(got.BriefingBody, "$post-work-review") {
		t.Fatalf("task briefing did not use codex-specific guidance:\n%s", got.BriefingBody)
	}
}

func TestNewTaskPaneRequestCollapsesMultilineTitleInPrompt(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", BaseBranch: "main"}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}
	task := planspec.Task{
		ID:       "api-client",
		Title:    "Extract API\nclient\tlayer",
		Briefing: "## Goal\nExtract it",
	}

	got := newTaskPaneRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	if strings.ContainsAny(got.Prompt, "\n\t") {
		t.Fatalf("prompt contains embedded newline/tab: %q", got.Prompt)
	}
	if !strings.Contains(got.Prompt, "Extract API client layer. read ") {
		t.Fatalf("prompt did not collapse task title whitespace: %q", got.Prompt)
	}
}

func TestNewTaskPaneRequestQualifiesDefaultSlugByPlan(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", BaseBranch: "main"}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "## Goal\nExtract it"}

	first := newTaskPaneRequest(cfg, "/repo", planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}, task, settings.Defaults(), hooks.EmptyConfig(), nil)
	second := newTaskPaneRequest(cfg, "/repo", planspec.Spec{Plan: planspec.Plan{Slug: "cleanup-plan", Title: "Cleanup plan"}}, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	if first.Slug == second.Slug || first.BranchName == second.BranchName {
		t.Fatalf("default task slugs must be plan-qualified, got %q/%q and %q/%q", first.Slug, first.BranchName, second.Slug, second.BranchName)
	}

	task.Slug = "shared-api-client"
	first = newTaskPaneRequest(cfg, "/repo", planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}, task, settings.Defaults(), hooks.EmptyConfig(), nil)
	second = newTaskPaneRequest(cfg, "/repo", planspec.Spec{Plan: planspec.Plan{Slug: "cleanup-plan", Title: "Cleanup plan"}}, task, settings.Defaults(), hooks.EmptyConfig(), nil)
	if first.Slug != "shared-api-client" || second.Slug != "shared-api-client" {
		t.Fatalf("explicit slug should be shared exactly, got %q and %q", first.Slug, second.Slug)
	}
}

func TestBuildAgentCommandStartsCodexPlanTUIControllerInPlanModeDryRun(t *testing.T) {
	cfg := &cliflags.Config{Agent: "codex", DryRun: true, CodexPlanMode: new(true)}
	req := paneRequest{
		Number:              1,
		Prompt:              "[fanout #1] plan",
		Agent:               "codex",
		CodexPlanMode:       true,
		CodexPlanStatusPath: "/tmp/fanout-codex-plan-repo-1.json",
	}

	got, err := buildAgentCommand(cfg, req, "fanout-go")
	if err != nil {
		t.Fatal(err)
	}

	want := "fanout-go __codex-plan-tui --codex codex --prompt '[fanout #1] plan' --status-file /tmp/fanout-codex-plan-repo-1.json"
	if got != want {
		t.Fatalf("buildAgentCommand() = %q, want %q", got, want)
	}
}

func TestWaitForCodexPlanTUIReadyReadsReadyStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := writeCodexPlanTUIStatus(path, codexPlanTUIStatus{Status: codexPlanTUIStatusReady}); err != nil {
		t.Fatal(err)
	}

	if err := waitForCodexPlanTUIReady(path, time.Second); err != nil {
		t.Fatalf("waitForCodexPlanTUIReady() failed: %v", err)
	}
}

func TestWaitForCodexPlanTUIReadyReturnsFailedStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := writeCodexPlanTUIStatus(path, codexPlanTUIStatus{Status: codexPlanTUIStatusFailed, Error: "boom"}); err != nil {
		t.Fatal(err)
	}

	err := waitForCodexPlanTUIReady(path, time.Second)

	if err == nil || err.Error() != "boom" {
		t.Fatalf("waitForCodexPlanTUIReady() error = %v, want boom", err)
	}
}

func TestWaitForCodexPlanTUIReadyTimesOutWithoutStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	err := waitForCodexPlanTUIReady(path, time.Millisecond)

	if err == nil || !strings.Contains(err.Error(), errCodexPlanStartupTimeout.Error()) {
		t.Fatalf("waitForCodexPlanTUIReady() error = %v, want timeout", err)
	}
}

func TestParentDisplay(t *testing.T) {
	tests := []struct {
		name   string
		parent string
		want   string
	}{
		{name: "numeric issue gets a hash prefix", parent: "123", want: "#123"},
		{name: "plan parent passes through", parent: "plan:launch-plan", want: "plan:launch-plan"},
		{name: "manual parent shows @manual", parent: manualPaneParentRef, want: "@manual"},
		{name: "watch parent shows @watch", parent: watchPaneParentRef, want: "@watch"},
		{name: "project url is stripped to its path", parent: "https://github.com/orgs/octo/projects/3", want: "orgs/octo/projects/3"},
		{name: "empty parent stays empty", parent: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parentDisplay(tt.parent); got != tt.want {
				t.Errorf("parentDisplay(%q) = %q, want %q", tt.parent, got, tt.want)
			}
		})
	}
}

func TestPaneBorderLabel(t *testing.T) {
	cases := []struct {
		name string
		req  paneRequest
		want string
	}{
		{
			name: "issue child uses slug",
			req:  paneRequest{ParentRef: "123", Slug: "fix-login-bug-123"},
			want: "#123 · fix-login-bug-123",
		},
		{
			name: "display name override wins over slug",
			req:  paneRequest{ParentRef: "123", Slug: "fix-login-bug-123", DisplayNameOverride: "Login fix"},
			want: "#123 · Login fix",
		},
		{
			name: "plan parent passes through",
			req:  paneRequest{ParentRef: "plan:my-feature", Slug: "task-slug"},
			want: "plan:my-feature · task-slug",
		},
		{
			name: "manual parent passes through",
			req:  paneRequest{ParentRef: manualPaneParentRef, Slug: "scratch"},
			want: "@manual · scratch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := paneBorderLabel(tc.req); got != tc.want {
				t.Errorf("paneBorderLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}
