package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestNewIssueOrchestratorPaneRequest(t *testing.T) {
	issue := ghissue.Issue{
		Number: 500,
		Title:  "Coordinate child changes",
		Body:   "Keep the parent task list current.",
	}
	req := newIssueOrchestratorPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), issue, "codex", "shell-orchestrator-key")

	if req.Slug != "orchestrator-issue-500-1" {
		t.Fatalf("req.Slug = %q, want orchestrator-issue-500-1", req.Slug)
	}
	if base := filepath.Base(req.BriefingPath); base != "fanout-repo-orchestrator-issue-500-1.md" {
		t.Fatalf("briefing basename = %q, want fanout-repo-orchestrator-issue-500-1.md", base)
	}
	if !strings.Contains(req.Prompt, req.BriefingPath) {
		t.Fatalf("req.Prompt %q does not reference briefing path %q", req.Prompt, req.BriefingPath)
	}
	wantPrompt := "orchestrate fanout for #500: Coordinate child changes. read " + req.BriefingPath + " and begin."
	if req.Prompt != wantPrompt {
		t.Fatalf("req.Prompt = %q, want %q", req.Prompt, wantPrompt)
	}
	if strings.HasPrefix(req.Prompt, "[fanout #") {
		t.Fatalf("req.Prompt = %q, must not use the child fanout identity tag", req.Prompt)
	}
	if req.ParentRef != panelaunch.ManualParentRef {
		t.Fatalf("req.ParentRef = %q, want %q", req.ParentRef, panelaunch.ManualParentRef)
	}
	if req.ShellKey != "shell-orchestrator-key" {
		t.Fatalf("req.ShellKey = %q, want liveness key passthrough", req.ShellKey)
	}
	if req.Agent != "codex" {
		t.Fatalf("req.Agent = %q, want modal default agent codex", req.Agent)
	}
	if wantTitle := "orchestrator: #500 Coordinate child changes"; req.Title != wantTitle || req.DisplayNameOverride != wantTitle {
		t.Fatalf("request title/display = %q/%q, want %q", req.Title, req.DisplayNameOverride, wantTitle)
	}
	if req.CodexPlanMode {
		t.Fatal("req.CodexPlanMode = true, want false for an issue orchestrator")
	}
	if req.Worktree.WorktreePath != "" {
		t.Fatalf("req.Worktree.WorktreePath = %q, want project-root attach without a worktree", req.Worktree.WorktreePath)
	}
	if !strings.Contains(req.BriefingBody, "You are the orchestrator for GitHub issue #500") {
		t.Fatalf("req.BriefingBody = %q, want issue orchestrator heading", req.BriefingBody)
	}
	if !strings.Contains(req.BriefingBody, "`fanout 500 --status`") {
		t.Fatalf("req.BriefingBody = %q, want parent status instructions", req.BriefingBody)
	}
}

func TestGuardIssueOrchestrator(t *testing.T) {
	tests := []struct {
		name         string
		panes        []state.Pane
		wantRecorded bool
		wantErr      string
	}{
		{
			name: "empty store allows launch",
		},
		{
			name:         "orchestrator for issue is a successful skip",
			panes:        []state.Pane{{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "orchestrator-issue-123-1"}},
			wantRecorded: true,
		},
		{
			name:  "different issue does not alias by prefix",
			panes: []state.Pane{{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "orchestrator-issue-1234-1"}},
		},
		{
			name:    "plan session blocks coexistence",
			panes:   []state.Pane{{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "plan-issue-123-1"}},
			wantErr: "issue #123 already has a plan session",
		},
		{
			name: "plan session wins over an orchestrator row",
			panes: []state.Pane{
				{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "orchestrator-issue-123-1"},
				{Parent: panelaunch.ManualParentRef, IssueNum: -2, Slug: "plan-issue-123-2"},
			},
			wantErr: "issue #123 already has a plan session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guardIssueOrchestrator(t.TempDir(), state.Store{Panes: tt.panes}, 123)
			switch {
			case tt.wantRecorded && !errors.Is(err, errIssueOrchestratorRecorded):
				t.Fatalf("guardIssueOrchestrator() error = %v, want recorded sentinel", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("guardIssueOrchestrator() error = %v, want %q", err, tt.wantErr)
			case !tt.wantRecorded && tt.wantErr == "" && err != nil:
				t.Fatalf("guardIssueOrchestrator() error = %v, want nil", err)
			}
		})
	}
}

func TestHasRecordedIssuePaneSeesOrchestrator(t *testing.T) {
	store := state.Store{Panes: []state.Pane{
		{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: "orchestrator-issue-123-1"},
		{Parent: panelaunch.ManualParentRef, IssueNum: -2, Slug: "orchestrator-issue-1234-2"},
	}}
	if !hasRecordedIssuePane(t.TempDir(), store, 123) {
		t.Fatal("hasRecordedIssuePane(..., 123) = false, want true for an orchestrator row")
	}
	if hasRecordedIssuePane(t.TempDir(), store, 12) {
		t.Fatal("hasRecordedIssuePane(..., 12) = true, want exact issue matching")
	}
}
