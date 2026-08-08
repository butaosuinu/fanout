package panelaunch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestFindHerdrConsolePaneAcrossLinkedWorktrees(t *testing.T) {
	root, linked := herdrConsoleTestWorktrees(t)
	pane := herdrConsoleTestPane(linked, "workspace-linked", "pane-linked")
	recorder, err := state.LockProject(linked)
	if err != nil {
		t.Fatal(err)
	}
	err = recorder.RecordPane(pane)
	if err != nil {
		t.Fatal(err)
	}
	err = recorder.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	current, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := findHerdrConsolePane(root, current)
	if err != nil || !found {
		t.Fatalf("findHerdrConsolePane() = %+v, %t, %v", got, found, err)
	}
	if got.PaneID != pane.PaneID || got.SourceProjectRoot != linked {
		t.Fatalf("found pane = %+v, want pane %q from %q", got, pane.PaneID, linked)
	}
}

func TestFindHerdrConsolePaneRejectsDuplicateAcrossWorktrees(t *testing.T) {
	root, linked := herdrConsoleTestWorktrees(t)
	for _, item := range []struct {
		root string
		pane state.Pane
	}{
		{root: root, pane: herdrConsoleTestPane(root, "workspace-root", "pane-root")},
		{root: linked, pane: herdrConsoleTestPane(linked, "workspace-linked", "pane-linked")},
	} {
		recorder, err := state.LockProject(item.root)
		if err != nil {
			t.Fatal(err)
		}
		if err := recorder.RecordPane(item.pane); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Unlock(); err != nil {
			t.Fatal(err)
		}
	}
	current, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := findHerdrConsolePane(root, current); err == nil {
		t.Fatal("findHerdrConsolePane() accepted duplicate console rows")
	}
}

func TestHerdrConsolePaneInStoreDoesNotIgnoreIncompleteIdentity(t *testing.T) {
	pane := herdrConsoleTestPane("/repo", "workspace-root", "")
	got, found, err := herdrConsolePaneInStore("/repo", state.Store{Panes: []state.Pane{pane}})
	if err != nil || !found {
		t.Fatalf("herdrConsolePaneInStore() = %+v, %t, %v, want saved row", got, found, err)
	}
	if got.PaneID != "" {
		t.Fatalf("saved pane id = %q, want incomplete identity preserved for fail-closed validation", got.PaneID)
	}
	if err := validateSavedHerdrConsoleShape(got); err == nil {
		t.Fatal("incomplete saved console identity was accepted for stale cleanup")
	}
}

func TestHerdrShellStatePaneUsesAdmittedCanonicalPath(t *testing.T) {
	intent := state.HerdrIntent{WorktreePath: "/canonical/repo"}
	live := backend.LivePane{
		Ref:            backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		WorkspaceLabel: "fanout-manual-owned",
		TerminalID:     "terminal-1",
		SessionID:      "fanout-owned",
		SocketPath:     "/tmp/fanout-owned.sock",
	}
	pane := herdrShellStatePane(intent, live, -1, "shell", "Shell", "")
	if pane.WorktreePath != intent.WorktreePath {
		t.Fatalf("saved path = %q, want admitted path %q", pane.WorktreePath, intent.WorktreePath)
	}
}

func TestValidateHerdrConsoleIntentRootRejectsLinkedWorktreeRecovery(t *testing.T) {
	intent := state.HerdrIntent{WorktreePath: "/repo/linked-a"}
	if err := validateHerdrConsoleIntentRoot(intent, "/repo/linked-b"); err == nil {
		t.Fatal("console recovery accepted an intent owned by another linked worktree")
	}
	if err := validateHerdrConsoleIntentRoot(intent, intent.WorktreePath); err != nil {
		t.Fatalf("console recovery rejected its owning worktree: %v", err)
	}
}

func TestStaleHerdrConsoleTargetAdmitsOwnedRouteWithNewProcessIdentity(t *testing.T) {
	saved := herdrConsoleTestPane("/repo", "workspace-root", "pane-old")
	saved.SourceProjectRoot = "/repo"
	live := backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: saved.HerdrWorkspaceID, Pane: "pane-new",
		},
		WorkspaceLabel: saved.HerdrWorkspaceLabel,
		TerminalID:     "terminal-new",
		SessionID:      saved.HerdrSession,
		SocketPath:     saved.HerdrSocketPath,
		CurrentPath:    saved.WorktreePath,
	}

	got, found, err := staleHerdrConsoleTarget(saved, []backend.LivePane{live})
	if err != nil || !found {
		t.Fatalf("staleHerdrConsoleTarget() = %+v, %t, %v", got, found, err)
	}
	if got.Ref.Pane != live.Ref.Pane || got.TerminalID != live.TerminalID {
		t.Fatalf("stale target = %+v, want current process identity %+v", got, live)
	}

	live.WorkspaceLabel = "foreign"
	if _, _, err := staleHerdrConsoleTarget(saved, []backend.LivePane{live}); err == nil {
		t.Fatal("staleHerdrConsoleTarget() accepted a workspace with a foreign label")
	}
}

func TestRemoveSavedHerdrConsoleRowFromOwningLinkedWorktree(t *testing.T) {
	root, linked := herdrConsoleTestWorktrees(t)
	pane := herdrConsoleTestPane(linked, "workspace-linked", "pane-linked")
	recorder, err := state.LockProject(linked)
	if err != nil {
		t.Fatal(err)
	}
	err = recorder.RecordPane(pane)
	if err != nil {
		t.Fatal(err)
	}
	err = recorder.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	pane.SourceProjectRoot = linked

	locked, err := state.LockProjectForLaunch(root)
	if err != nil {
		t.Fatal(err)
	}
	err = removeSavedHerdrConsoleRow(locked, root, pane)
	if err != nil {
		t.Fatal(err)
	}
	err = locked.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	store, err := state.LoadProject(linked)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := store.Find(pane.Parent, pane.IssueNum); found {
		t.Fatal("saved Herdr console row remains in the owning linked worktree")
	}
}

func herdrConsoleTestWorktrees(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	gitCmdTest(t, root, "init", "-q")
	gitCmdTest(t, root, "config", "user.email", "fanout-test@example.invalid")
	gitCmdTest(t, root, "config", "user.name", "fanout test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, root, "add", "README.md")
	gitCmdTest(t, root, "commit", "-qm", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	gitCmdTest(t, root, "worktree", "add", "-qb", "linked", linked)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalLinked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalRoot, canonicalLinked
}

func herdrConsoleTestPane(root, workspace, paneID string) state.Pane {
	return state.Pane{
		Parent: ManualParentRef, RuntimeParent: HerdrConsoleRuntimeParent,
		IssueNum: -1, Kind: state.PaneKindShell, Slug: "herdr-console",
		Backend: backend.Herdr, PaneID: paneID,
		HerdrWorkspaceID: workspace, HerdrWorkspaceLabel: "fanout-console-owned",
		HerdrTerminalID: "terminal-" + paneID,
		HerdrSession:    "fanout-owned", HerdrSocketPath: "/tmp/fanout-owned.sock",
		Agent: state.PaneKindShell, DisplayName: "Herdr console",
		WorktreePath: root, CreatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
	}
}
