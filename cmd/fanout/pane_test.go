package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
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
	if !createPane(cfg, lg, info, req, nil, log.Palette{}) {
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
