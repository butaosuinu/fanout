package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

func TestRestoreRecordedPanesRebindsLivePaneByTitle(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%old",
		Agent:        "claude",
		WorktreePath: wt,
	}})
	livePane := tmuxrun.LivePane{ID: "%new", CurrentPath: filepath.Join(wt, "internal"), Title: "Restore API"}

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live: map[string]tmuxrun.LivePane{
				livePane.ID: livePane,
			},
			PanesByTitle: map[string][]tmuxrun.LivePane{
				"Restore API": {livePane},
			},
		}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%new" {
		t.Fatalf("state panes = %+v, want rebound pane id %%new", got.Panes)
	}
	if report.Rebound != 1 || report.Restored != 0 || report.RemovedShells != 0 {
		t.Fatalf("report = %+v, want one rebound", report)
	}
}

func TestRestoreRecordedPanesRemovesStaleShellRows(t *testing.T) {
	root := t.TempDir()
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "@shell",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		PaneID:       "%9",
		ShellKey:     "shell-1",
		DisplayName:  "terminal",
		WorktreePath: root,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 0 {
		t.Fatalf("state panes = %+v, want stale shell removed", got.Panes)
	}
	if report.RemovedShells != 1 {
		t.Fatalf("RemovedShells = %d, want 1", report.RemovedShells)
	}
}

func TestRestoreRecordedPanesKeepsLiveShellWhenShellKeyMissing(t *testing.T) {
	root := t.TempDir()
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "@shell",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		PaneID:       "%9",
		ShellKey:     "shell-1",
		DisplayName:  "terminal",
		WorktreePath: root,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live: map[string]tmuxrun.LivePane{
				"%9": {ID: "%9", CurrentPath: root, Title: "terminal"},
			},
		}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%9" {
		t.Fatalf("state panes = %+v, want live shell row preserved", got.Panes)
	}
	if report.Skipped != 1 || report.RemovedShells != 0 {
		t.Fatalf("report = %+v, want one skipped shell and no removal", report)
	}
}

func TestRestoreRecordedPanesRemovesShellWhenLiveShellKeyDiffers(t *testing.T) {
	root := t.TempDir()
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "@shell",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		PaneID:       "%9",
		ShellKey:     "shell-1",
		DisplayName:  "terminal",
		WorktreePath: root,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live: map[string]tmuxrun.LivePane{
				"%9": {ID: "%9", CurrentPath: root, Title: "terminal", ShellKey: "shell-2"},
			},
		}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 0 {
		t.Fatalf("state panes = %+v, want reused shell row removed", got.Panes)
	}
	if report.RemovedShells != 1 {
		t.Fatalf("RemovedShells = %d, want 1", report.RemovedShells)
	}
}

func TestRestoreRecordedPanesSkipsRootWithoutStateFile(t *testing.T) {
	root := t.TempDir()
	snapshotCalled := false

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		snapshotCalled = true
		return tuiRestoreSnapshot{}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotCalled {
		t.Fatal("snapshot loader was called for a root without state")
	}
	if report.Changed() {
		t.Fatalf("report = %+v, want no changes", report)
	}
	if _, err := os.Stat(filepath.Join(root, ".fanout")); !os.IsNotExist(err) {
		t.Fatalf(".fanout stat error = %v, want missing directory", err)
	}
}

func TestRestoreRecordedPanesDoesNotRecreateLivePaneWithPathMismatch(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%old",
		Agent:        "claude",
		WorktreePath: wt,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live: map[string]tmuxrun.LivePane{
				"%old": {ID: "%old", CurrentPath: "/tmp", Title: "Restore API"},
			},
		}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%old" {
		t.Fatalf("state panes = %+v, want original live pane id preserved", got.Panes)
	}
	if report.Skipped != 1 || report.Restored != 0 || report.Rebound != 0 {
		t.Fatalf("report = %+v, want one skipped pane and no restore", report)
	}
}

func TestRestoreRecordedPanesSkipsDuplicateIssueClaimedByAnotherRoot(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "081",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%old",
		Agent:        "claude",
		WorktreePath: wt,
	}})
	claimed := map[string]bool{"issue\x0081\x00101": true}

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, claimed)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%old" {
		t.Fatalf("state panes = %+v, want duplicate row left unchanged", got.Panes)
	}
	if report.Skipped != 1 || report.Restored != 0 {
		t.Fatalf("report = %+v, want duplicate skipped without restore", report)
	}
}

func TestPreclaimLiveRestoreIdentitiesClaimsLiveSibling(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	wt := filepath.Join(sibling, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, sibling, []state.Pane{{
		Parent:       "081",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%live",
		Agent:        "claude",
		WorktreePath: wt,
	}})

	claimed := preclaimLiveRestoreIdentities([]string{root, sibling}, tuiRestoreSnapshot{
		Live: map[string]tmuxrun.LivePane{
			"%live": {ID: "%live", CurrentPath: filepath.Join(wt, "subdir"), Title: "Restore API"},
		},
	})

	if !claimed["issue\x0081\x00101"] {
		t.Fatalf("claimed = %#v, want live sibling issue claimed", claimed)
	}
}

func TestRestoreRecordedPanesRecreatesMissingAgentPaneWithResumeCommand(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := installRestoreTmuxAndAgentScripts(t, "claude")
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%old",
		Agent:        "claude",
		WorktreePath: wt,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%restored" || got.Panes[0].AgentStatus != "running" {
		t.Fatalf("state panes = %+v, want restored running pane %%restored", got.Panes)
	}
	if report.Restored != 1 {
		t.Fatalf("Restored = %d, want 1", report.Restored)
	}
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBody), "split-window") || !strings.Contains(string(logBody), "--continue") {
		t.Fatalf("tmux log = %q, want split-window with claude --continue", string(logBody))
	}
}

func TestRestoreAgentCommandUsesSavedCodexPlanThread(t *testing.T) {
	root := t.TempDir()
	installRestoreAgentScript(t, "codex")

	command, statusPath, err := restoreAgentCommand(state.Pane{
		IssueNum:       7,
		Agent:          "codex",
		CodexPlanMode:  true,
		CodexThreadID:  "thread-7",
		CodexSessionID: "session-7",
	}, root, "fanout")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"__codex-plan-tui", "--resume-thread-id", "thread-7", "--resume-session-id", "session-7", "--status-file"} {
		if !strings.Contains(command, want) {
			t.Fatalf("command = %q, missing %q", command, want)
		}
	}
	if statusPath == "" || !strings.Contains(statusPath, "fanout-codex-plan-") {
		t.Fatalf("statusPath = %q, want generated codex plan status path", statusPath)
	}
}

func TestRestoreAgentCommandRejectsCodexPlanWithoutThread(t *testing.T) {
	_, _, err := restoreAgentCommand(state.Pane{
		Agent:         "codex",
		CodexPlanMode: true,
		DisplayName:   "Plan pane",
	}, t.TempDir(), "fanout")

	if err == nil || !strings.Contains(err.Error(), "missing codex thread id") {
		t.Fatalf("restoreAgentCommand() error = %v, want missing thread id", err)
	}
}

func writeRestoreState(t *testing.T, root string, panes []state.Pane) {
	t.Helper()
	locked, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	locked.SchemaVersion = state.SchemaVersion
	locked.Panes = panes
	if err := locked.Save(); err != nil {
		t.Fatal(err)
	}
}

func readRestoreState(t *testing.T, root string) state.Store {
	t.Helper()
	store, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func installRestoreTmuxAndAgentScripts(t *testing.T, agentName string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "tmux.log")
	tmuxScript := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TMUX_RESTORE_LOG"
case "${1:-}" in
  split-window)
    printf '%%restored\n'
    ;;
  select-pane|set-option|kill-pane)
    ;;
  *)
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(tmuxScript), 0o755); err != nil {
		t.Fatal(err)
	}
	agentScript := "#!/usr/bin/env bash\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, agentName), []byte(agentScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_RESTORE_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func installRestoreAgentScript(t *testing.T, agentName string) {
	t.Helper()
	binDir := t.TempDir()
	agentScript := "#!/usr/bin/env bash\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, agentName), []byte(agentScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
