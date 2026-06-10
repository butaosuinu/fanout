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
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/naming"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
)

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
	}

	got := statePane(req, "%42", "/repo/.fanout/worktrees/state-idempotency-83", now)

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
}

func TestCreatePaneAcceptsManualRequestWithoutParentIssue(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", DryRun: true, NoRefresh: true}
	if got := newManualPaneRequest(cfg, "/repo", state.Store{}, manualPaneOptions{Title: "First Manual"}); got.Number != -1 {
		t.Fatalf("first manual number = %d, want -1", got.Number)
	}
	store := state.Store{Panes: []state.Pane{{Parent: manualPaneParentRef, IssueNum: -1}}}
	req := newManualPaneRequest(cfg, "/repo", store, manualPaneOptions{
		Title:  "Manual Diagnostics",
		Body:   "extra context",
		Slug:   "manual-diagnostics",
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
	for _, want := range []string{"#-2: Manual Diagnostics", "slug -> manual-diagnostics", "dry-run complete"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(req.BriefingPath); !os.IsNotExist(err) {
		t.Fatalf("manual dry-run wrote briefing file %s: %v", req.BriefingPath, err)
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

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), true)

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

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), false)

	if !strings.Contains(got.BriefingBody, "git diff --name-only release/v1...HEAD") {
		t.Fatalf("briefing did not include selected base branch:\n%s", got.BriefingBody)
	}
}

func TestNewPaneRequestCarriesIssueWave(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "claude"}
	issue := ghissue.Issue{Number: 501, Title: "Wave child", Body: "body", Wave: "wave5"}

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), false)

	if got.Wave != "wave5" {
		t.Fatalf("Wave = %q, want wave5", got.Wave)
	}
}

func TestNewPaneRequestCodexPlanModeUsesPlanPromptAndBriefing(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "codex", CodexPlanMode: boolPtr(true)}
	issue := ghissue.Issue{Number: 501, Title: "Plan child", Body: "body"}

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), false)

	if !got.CodexPlanMode {
		t.Fatal("CodexPlanMode = false, want true")
	}
	if !strings.Contains(got.Prompt, "read /tmp/fanout-repo-501.md and propose a plan.") {
		t.Fatalf("prompt = %q, want plan action", got.Prompt)
	}
	if !strings.Contains(got.BriefingBody, "<proposed_plan>...</proposed_plan>") {
		t.Fatalf("briefing did not require proposed_plan wrapper:\n%s", got.BriefingBody)
	}
	for _, unexpected := range []string{"commit and push", "Open a pull request"} {
		if strings.Contains(got.BriefingBody, unexpected) {
			t.Fatalf("plan briefing contains implementation instruction %q:\n%s", unexpected, got.BriefingBody)
		}
	}
}

func TestBuildAgentCommandStartsCodexPlanTUIControllerInPlanModeDryRun(t *testing.T) {
	cfg := &cliflags.Config{Agent: "codex", DryRun: true, CodexPlanMode: boolPtr(true)}
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
