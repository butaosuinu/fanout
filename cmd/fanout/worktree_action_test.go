package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
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
		Backend:      backend.Herdr,
	}

	got := attachTargetFromStatePane(pane)

	if got.TargetPath != pane.WorktreePath || got.SourceParent != "100" || got.SourceIssueNum != 101 {
		t.Fatalf("target = %+v", got)
	}
	if got.SourceBranchName != "fanout/child-101" || got.SourceLabel != "#101" {
		t.Fatalf("source branch/label = %q/%q", got.SourceBranchName, got.SourceLabel)
	}
	if got.Backend != backend.Herdr {
		t.Fatalf("Backend = %q, want herdr", got.Backend)
	}
}

func TestAttachTargetFromAttachedAgentPreservesOriginalSourceIdentity(t *testing.T) {
	pane := state.Pane{
		Parent:         "@manual",
		IssueNum:       -1,
		Kind:           state.PaneKindAttachedAgent,
		DisplayName:    "codex for #101",
		BranchName:     "fanout/child-101",
		WorktreePath:   "/repo/.fanout/worktrees/child",
		SourceParent:   "100",
		SourceIssueNum: 101,
		Backend:        backend.Herdr,
	}

	got := attachTargetFromStatePane(pane)

	if got.SourceParent != "100" || got.SourceIssueNum != 101 || got.SourceTaskID != "" {
		t.Fatalf("source identity = parent %q issue %d task %q, want 100/101/no task", got.SourceParent, got.SourceIssueNum, got.SourceTaskID)
	}
	if got.SourceLabel != "#101" {
		t.Fatalf("SourceLabel = %q, want #101", got.SourceLabel)
	}
	if got.Backend != backend.Herdr {
		t.Fatalf("Backend = %q, want herdr", got.Backend)
	}
}

func TestAttachTargetFromSyntheticCoordinatorUsesActualIssueParent(t *testing.T) {
	for _, slug := range []string{
		panelaunch.PlanIssueSlug(425, -1),
		panelaunch.OrchestratorIssueSlug(425, -1),
	} {
		t.Run(slug, func(t *testing.T) {
			got := attachTargetFromStatePane(state.Pane{
				Parent:   panelaunch.ManualParentRef,
				IssueNum: -1,
				Slug:     slug,
			})
			if got.SourceParent != "425" || got.SourceIssueNum != 425 || got.SourceLabel != "#425" {
				t.Fatalf("target = %+v, want actual issue 425 provenance", got)
			}
		})
	}
}

func TestFindRecordedPaneByIDRequiresLivePathUnderRecordedWorktree(t *testing.T) {
	repo := t.TempDir()
	worktree := filepath.Join(repo, ".fanout", "worktrees", "child")
	writeRawLifecycleState(t, repo, state.Pane{
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

func TestFindRecordedPaneByIDSearchesRawSiblingStores(t *testing.T) {
	repo := t.TempDir()
	sibling := t.TempDir()
	homeWorktree := filepath.Join(repo, ".fanout", "worktrees", "child")
	siblingWorktree := filepath.Join(sibling, ".fanout", "worktrees", "child")
	writeRawLifecycleState(t, repo, state.Pane{
		Parent:       "100",
		IssueNum:     101,
		PaneID:       "%home",
		WorktreePath: homeWorktree,
	})
	writeRawLifecycleState(t, sibling, state.Pane{
		Parent:       "100",
		IssueNum:     101,
		PaneID:       "%sibling",
		WorktreePath: siblingWorktree,
	})
	origRoots := worktreeActionListRoots
	origLive := worktreeActionLivePanes
	t.Cleanup(func() {
		worktreeActionListRoots = origRoots
		worktreeActionLivePanes = origLive
	})
	worktreeActionListRoots = func(string) ([]string, error) {
		return []string{repo, sibling}, nil
	}
	worktreeActionLivePanes = func() ([]tmuxrun.LivePane, error) {
		return []tmuxrun.LivePane{{ID: "%sibling", CurrentPath: siblingWorktree, WorktreePath: siblingWorktree}}, nil
	}

	got, err := findRecordedPaneByID(repo, "%sibling")
	if err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "%sibling" || got.SourceProjectRoot != sibling {
		t.Fatalf("pane = %+v, want raw sibling pane tagged with source root", got)
	}
}

func TestFindRecordedPaneByIDAllowsProjectRootHintWhenCurrentPathIsStale(t *testing.T) {
	repo := t.TempDir()
	worktree := filepath.Join(repo, ".fanout", "worktrees", "child")
	writeRawLifecycleState(t, repo, state.Pane{
		Parent:       "100",
		IssueNum:     101,
		PaneID:       "%42",
		WorktreePath: worktree,
	})
	orig := worktreeActionLivePanes
	t.Cleanup(func() { worktreeActionLivePanes = orig })
	worktreeActionLivePanes = func() ([]tmuxrun.LivePane, error) {
		return []tmuxrun.LivePane{{ID: "%42", CurrentPath: repo, ProjectRoot: repo}}, nil
	}

	if _, err := findRecordedPaneByID(repo, "%42"); err != nil {
		t.Fatalf("findRecordedPaneByID() error = %v, want project-root hint fallback", err)
	}
}

func TestFindRecordedPaneByIDRejectsProjectRootHintForDuplicatePaneID(t *testing.T) {
	repo := t.TempDir()
	first := filepath.Join(repo, ".fanout", "worktrees", "first")
	second := filepath.Join(repo, ".fanout", "worktrees", "second")
	writeRawLifecycleState(t, repo,
		state.Pane{Parent: "100", IssueNum: 101, PaneID: "%42", WorktreePath: first},
		state.Pane{Parent: "100", IssueNum: 102, PaneID: "%42", WorktreePath: second},
	)
	orig := worktreeActionLivePanes
	t.Cleanup(func() { worktreeActionLivePanes = orig })
	worktreeActionLivePanes = func() ([]tmuxrun.LivePane, error) {
		return []tmuxrun.LivePane{{ID: "%42", CurrentPath: repo, ProjectRoot: repo}}, nil
	}

	_, err := findRecordedPaneByID(repo, "%42")
	if err == nil || !strings.Contains(err.Error(), "worktree identity is ambiguous") {
		t.Fatalf("findRecordedPaneByID() error = %v, want ambiguous worktree identity", err)
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

// TestFindRecordedPaneByIDRequiresLivenessKeyForKeyedCoordinator pins the
// prefix+M identity check for the plan fan-out coordinator: recorded at the
// repo root, a reused pane id always passes the path checks, so a row with a
// ShellKey must match the live pane's @fanout_shell_key regardless of kind.
func TestFindRecordedPaneByIDRequiresLivenessKeyForKeyedCoordinator(t *testing.T) {
	repo := t.TempDir()
	writeLifecycleState(t, repo, state.Pane{
		Parent:       "@manual",
		IssueNum:     -1,
		Kind:         state.PaneKindAttachedAgent,
		PaneID:       "%88",
		ShellKey:     "shell-coordinator",
		Agent:        "claude",
		WorktreePath: repo,
	})
	orig := worktreeActionLivePanes
	t.Cleanup(func() { worktreeActionLivePanes = orig })

	worktreeActionLivePanes = func() ([]tmuxrun.LivePane, error) {
		return []tmuxrun.LivePane{{ID: "%88", CurrentPath: filepath.Join(repo, "subdir"), ShellKey: ""}}, nil
	}
	_, err := findRecordedPaneByID(repo, "%88")
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("findRecordedPaneByID() error = %v, want keyed identity mismatch", err)
	}

	worktreeActionLivePanes = func() ([]tmuxrun.LivePane, error) {
		return []tmuxrun.LivePane{{ID: "%88", CurrentPath: repo, ShellKey: "shell-coordinator"}}, nil
	}
	got, err := findRecordedPaneByID(repo, "%88")
	if err != nil {
		t.Fatal(err)
	}
	if got.ShellKey != "shell-coordinator" {
		t.Fatalf("ShellKey = %q, want the recorded coordinator row", got.ShellKey)
	}
}
