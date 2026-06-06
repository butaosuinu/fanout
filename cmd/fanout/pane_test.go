package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/settings"
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
		Issue:               ghissue.Issue{Number: 83},
		Slug:                "state-idempotency-83",
		DisplayNameOverride: "State Idempotency",
		BranchName:          "fanout/state-idempotency-83",
		OneLinePrompt:       "[fanout #83 of #81] state-idempotency-83: read /tmp/fanout-fanout-83.md and begin.",
	}
	cfg := &cliflags.Config{ParentRef: "81", Agent: "codex"}

	got := statePane(cfg, req, "%42", "/repo/.fanout/worktrees/state-idempotency-83", now)

	if got.Parent != "81" || got.IssueNum != 83 || got.PaneID != "%42" {
		t.Fatalf("state pane identity = %+v", got)
	}
	if got.DisplayName != "State Idempotency" {
		t.Fatalf("displayName = %q, want State Idempotency", got.DisplayName)
	}
	if got.CreatedAt != "2026-06-04T01:02:03Z" {
		t.Fatalf("createdAt = %q", got.CreatedAt)
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
	if !strings.Contains(got.OneLinePrompt, "[fanout #501 of #200] shared-child-parent-200-501:") {
		t.Fatalf("prompt = %q, want parent-qualified slug", got.OneLinePrompt)
	}
}

func TestNewPaneRequestCodexPlanModeUsesPlanPromptAndBriefing(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "codex", CodexPlanMode: boolPtr(true)}
	issue := ghissue.Issue{Number: 501, Title: "Plan child", Body: "body"}

	got := newPaneRequest(cfg, "/repo", issue, settings.Defaults(), false)

	if !got.CodexPlanMode {
		t.Fatal("CodexPlanMode = false, want true")
	}
	if !strings.Contains(got.OneLinePrompt, "read /tmp/fanout-repo-501.md and propose a plan.") {
		t.Fatalf("prompt = %q, want plan action", got.OneLinePrompt)
	}
	if !strings.Contains(got.BriefingBody, "<proposed_plan>...</proposed_plan>") {
		t.Fatalf("briefing missing proposed_plan requirement:\n%s", got.BriefingBody)
	}
	if strings.Contains(got.BriefingBody, "commit and push") || strings.Contains(got.BriefingBody, "Open a pull request") {
		t.Fatalf("plan briefing should not ask for implementation workflow:\n%s", got.BriefingBody)
	}
}

func TestBuildAgentCommandUsesCodexPlanShimInDryRun(t *testing.T) {
	cfg := &cliflags.Config{Agent: "codex", DryRun: true, CodexPlanMode: boolPtr(true)}

	got, err := buildAgentCommand(cfg, "[fanout #1] plan")
	if err != nil {
		t.Fatal(err)
	}
	want := "fanout __codex-plan-mode --codex codex --prompt '[fanout #1] plan'"
	if got != want {
		t.Fatalf("buildAgentCommand() = %q, want %q", got, want)
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
