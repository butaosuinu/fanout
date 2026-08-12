package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
)

func TestNewIssueOrchestratorPaneRequest(t *testing.T) {
	issue := ghissue.Issue{
		Number: 500,
		Title:  "Coordinate child changes",
		Body:   "Keep the parent task list current.",
	}
	req, notice := newIssueOrchestratorPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), issue, "codex", false, "shell-orchestrator-key")

	if req.Slug != "orchestrator-issue-500-1" {
		t.Fatalf("req.Slug = %q, want orchestrator-issue-500-1", req.Slug)
	}
	if base := filepath.Base(req.BriefingPath); base != "fanout-repo-orchestrator-issue-500-1.md" {
		t.Fatalf("briefing basename = %q, want fanout-repo-orchestrator-issue-500-1.md", base)
	}
	if !strings.Contains(req.Prompt, req.BriefingPath) {
		t.Fatalf("req.Prompt %q does not reference briefing path %q", req.Prompt, req.BriefingPath)
	}
	wantPrompt := "orchestrate fanout for #500. read " + req.BriefingPath + " and begin."
	if req.Prompt != wantPrompt {
		t.Fatalf("req.Prompt = %q, want %q", req.Prompt, wantPrompt)
	}
	if strings.Contains(req.Prompt, issue.Title) {
		t.Fatalf("req.Prompt = %q, must keep the untrusted issue title inside the briefing", req.Prompt)
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
	if req.PlanMode() || req.LaunchMode != agent.ModeBuild {
		t.Fatalf("req.LaunchMode = %q, want explicit build mode", req.LaunchMode)
	}
	if notice != "" {
		t.Fatalf("notice = %q, want none with orchestrator plan mode disabled", notice)
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

func TestNewIssueOrchestratorPaneRequestLaunchMode(t *testing.T) {
	issue := ghissue.Issue{Number: 500, Title: "Coordinate child changes"}
	tests := []struct {
		name       string
		agentName  string
		planMode   bool
		wantMode   agent.LaunchMode
		wantNotice string
	}{
		{name: "claude plan", agentName: "claude", planMode: true, wantMode: agent.ModePlan},
		{name: "opencode plan", agentName: "opencode", planMode: true, wantMode: agent.ModePlan},
		{name: "codex plan falls back", agentName: "codex", planMode: true, wantMode: agent.ModeBuild, wantNotice: codexOrchestratorPlanFallbackNotice},
		{name: "claude build", agentName: "claude", wantMode: agent.ModeBuild},
		{name: "opencode build", agentName: "opencode", wantMode: agent.ModeBuild},
		{name: "codex build", agentName: "codex", wantMode: agent.ModeBuild},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, notice := newIssueOrchestratorPaneRequest("/repo", state.Store{}, hooks.EmptyConfig(), issue, tt.agentName, tt.planMode, "shell-orchestrator-key")
			if req.AgentStartGate == "" {
				t.Fatal("req.AgentStartGate is empty, want gated orchestrator launch")
			}
			if req.LaunchMode != tt.wantMode {
				t.Fatalf("req.LaunchMode = %q, want %q", req.LaunchMode, tt.wantMode)
			}
			if notice != tt.wantNotice {
				t.Fatalf("notice = %q, want %q", notice, tt.wantNotice)
			}
		})
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

func TestCleanupIssueOrchestratorHandlesStaleAndFailedPaneCleanup(t *testing.T) {
	tests := []struct {
		name          string
		liveKey       string
		killFails     bool
		wantErr       string
		wantKillRun   bool
		wantStateKept bool
	}{
		{
			name:    "reused pane id removes stale state without killing the live pane",
			liveKey: "shell-reused",
		},
		{
			name:          "kill failure keeps the state row",
			liveKey:       "shell-orchestrator",
			killFails:     true,
			wantErr:       "tmux kill-pane",
			wantKillRun:   true,
			wantStateKept: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			req := panelaunch.Request{
				ParentRef: panelaunch.ManualParentRef,
				Number:    -1,
				ShellKey:  "shell-orchestrator",
			}
			locked, err := state.LockProject(repo)
			if err != nil {
				t.Fatal(err)
			}
			if recordErr := locked.RecordPane(state.Pane{
				Parent:   req.ParentRef,
				IssueNum: req.Number,
				PaneID:   "%91",
				ShellKey: req.ShellKey,
			}); recordErr != nil {
				t.Fatal(recordErr)
			}
			if unlockErr := locked.Unlock(); unlockErr != nil {
				t.Fatal(unlockErr)
			}

			tmuxLog := installIssueOrchestratorCleanupTmuxShim(t, tt.liveKey, tt.killFails)
			err = cleanupIssueOrchestrator(repo, "fanout-test", tmuxbackend.New(), nil, req, "%91")
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("cleanupIssueOrchestrator() error = %v, want %q", err, tt.wantErr)
			}
			if tt.wantErr == "" && err != nil {
				t.Fatalf("cleanupIssueOrchestrator() error = %v, want nil", err)
			}
			store, loadErr := state.LoadProject(repo)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			_, stateKept := store.Find(req.ParentRef, req.Number)
			if stateKept != tt.wantStateKept {
				t.Fatalf("orchestrator state kept = %v, want %v; panes = %+v", stateKept, tt.wantStateKept, store.Panes)
			}
			body, readErr := os.ReadFile(tmuxLog)
			if readErr != nil {
				t.Fatal(readErr)
			}
			killRan := strings.Contains(string(body), "kill-pane\n-t\n%91\n---\n")
			if killRan != tt.wantKillRun {
				t.Fatalf("kill-pane ran = %v, want %v; tmux log:\n%s", killRan, tt.wantKillRun, body)
			}
		})
	}
}

func TestIssueOrchestratorIdentityUsesBackendNativeFields(t *testing.T) {
	recorded := state.Pane{PaneID: "w1:p1", ShellKey: ""}
	req := panelaunch.Request{ShellKey: "tmux-key"}
	if issueOrchestratorIdentityChanged(backend.Herdr, recorded, true, req, "w1:p1") {
		t.Fatal("Herdr identity treated the caller-only tmux ShellKey as authoritative")
	}
	if !issueOrchestratorIdentityChanged(backend.Tmux, recorded, true, req, "w1:p1") {
		t.Fatal("tmux identity ignored the liveness ShellKey")
	}
}

func installIssueOrchestratorCleanupTmuxShim(t *testing.T, liveKey string, killFails bool) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.txt")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "$TMUX_CLEANUP_LOG"
printf '%s\n' '---' >> "$TMUX_CLEANUP_LOG"
case "${1:-} ${2:-} ${3:-}" in
"list-panes -a -F")
	if [[ ! -s "$TMUX_CLEANUP_ACTIVE" ]]; then
		exit 0
	fi
  case "${4:-}" in
  *pane_current_path*) printf '%%91\t/repo\n' ;;
  *pane_title*) printf '%%91\torchestrator\n' ;;
  *fanout_shell_key*) printf '%%91\t%s\n' "$TMUX_LIVE_SHELL_KEY" ;;
	*fanout_project_root*|*fanout_worktree_path*) printf '%%91\t/repo\n' ;;
  esac
  ;;
"kill-pane -t %91")
  if [[ "$TMUX_KILL_FAILS" == "true" ]]; then
    exit 7
  fi
	: > "$TMUX_CLEANUP_ACTIVE"
  ;;
esac
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_CLEANUP_LOG", logPath)
	activePath := filepath.Join(dir, "active")
	if err := os.WriteFile(activePath, []byte("active\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_CLEANUP_ACTIVE", activePath)
	t.Setenv("TMUX_LIVE_SHELL_KEY", liveKey)
	t.Setenv("TMUX_KILL_FAILS", fmt.Sprintf("%t", killFails))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}
