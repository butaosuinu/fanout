package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

func TestParseWorktreeActionFlagsUsesPaneArg(t *testing.T) {
	flags, code := parseWorktreeActionFlags([]string{
		"__worktree-action",
		"--pane", "%42",
		"--action", "attach",
		"--agent", "codex",
		"--prompt", "inspect",
	}, discardLogger())

	if code != 0 {
		t.Fatalf("parseWorktreeActionFlags code = %d, want 0", code)
	}
	if flags.paneID != "%42" || flags.action != "attach" || flags.agent != "codex" || flags.prompt != "inspect" {
		t.Fatalf("flags = %+v", flags)
	}
}

func TestAttachTargetFromStatePaneCarriesSourceIdentity(t *testing.T) {
	pane := state.Pane{
		Parent:       "100",
		IssueNum:     101,
		Slug:         "child-101",
		BranchName:   "fanout/child-101",
		DisplayName:  "child",
		WorktreePath: "/repo/.fanout/worktrees/child",
	}

	got := attachTargetFromStatePane(pane)

	if got.TargetPath != pane.WorktreePath || got.SourceParent != "100" || got.SourceIssueNum != 101 {
		t.Fatalf("target = %+v", got)
	}
	if got.SourceBranchName != "fanout/child-101" || got.SourceLabel != "#101" {
		t.Fatalf("source branch/label = %q/%q", got.SourceBranchName, got.SourceLabel)
	}
}

func TestFindRecordedPaneByIDRequiresLivePathUnderRecordedWorktree(t *testing.T) {
	repo := t.TempDir()
	worktree := filepath.Join(repo, ".fanout", "worktrees", "child")
	writeLifecycleState(t, repo, state.Pane{
		Parent:       "100",
		IssueNum:     101,
		PaneID:       "%42",
		WorktreePath: worktree,
	})
	orig := worktreeActionLivePanes
	t.Cleanup(func() { worktreeActionLivePanes = orig })
	worktreeActionLivePanes = func() ([]tmuxrun.LivePane, error) {
		return []tmuxrun.LivePane{{ID: "%42", CurrentPath: filepath.Join(worktree, "subdir")}}, nil
	}

	got, err := findRecordedPaneByID(repo, "%42")
	if err != nil {
		t.Fatal(err)
	}
	if got.IssueNum != 101 {
		t.Fatalf("IssueNum = %d, want 101", got.IssueNum)
	}

	worktreeActionLivePanes = func() ([]tmuxrun.LivePane, error) {
		return []tmuxrun.LivePane{{ID: "%42", CurrentPath: filepath.Join(repo, "other")}}, nil
	}
	_, err = findRecordedPaneByID(repo, "%42")
	if err == nil || !strings.Contains(err.Error(), "is not under recorded worktree") {
		t.Fatalf("findRecordedPaneByID() error = %v, want path mismatch", err)
	}
}

func TestFindRecordedPaneByIDRequiresShellKeyForShellRows(t *testing.T) {
	repo := t.TempDir()
	writeLifecycleState(t, repo, state.Pane{
		Parent:       "@manual",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		PaneID:       "%77",
		ShellKey:     "shell-old",
		WorktreePath: repo,
	})
	orig := worktreeActionLivePanes
	t.Cleanup(func() { worktreeActionLivePanes = orig })
	worktreeActionLivePanes = func() ([]tmuxrun.LivePane, error) {
		return []tmuxrun.LivePane{{ID: "%77", CurrentPath: repo, ShellKey: "shell-new"}}, nil
	}

	_, err := findRecordedPaneByID(repo, "%77")
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("findRecordedPaneByID() error = %v, want shell identity mismatch", err)
	}
}
