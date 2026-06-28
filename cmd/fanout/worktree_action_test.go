package main

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/state"
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
