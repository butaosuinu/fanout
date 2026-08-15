package panelaunch

import (
	"errors"
	"fmt"
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
	intent := state.LaunchIntent{WorktreePath: "/canonical/repo"}
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
	intent := state.LaunchIntent{WorktreePath: "/repo/linked-a"}
	if err := validateHerdrConsoleIntentRoot(intent, "/repo/linked-b"); err == nil {
		t.Fatal("console recovery accepted an intent owned by another linked worktree")
	}
	if err := validateHerdrConsoleIntentRoot(intent, intent.WorktreePath); err != nil {
		t.Fatalf("console recovery rejected its owning worktree: %v", err)
	}
}

func TestStaleHerdrConsoleRecoveryRequiresIdentityMismatch(t *testing.T) {
	if staleHerdrConsoleRecoverable(os.ErrDeadlineExceeded) {
		t.Fatal("transient observation error qualified for destructive stale recovery")
	}
	mismatch := fmt.Errorf("bind saved console: %w", backend.ErrOwnedIdentityMismatch)
	if !staleHerdrConsoleRecoverable(mismatch) {
		t.Fatal("owned identity mismatch did not qualify for stale recovery")
	}
}

func TestStaleHerdrConsoleTargetAdmitsOwnedRouteWithNewProcessIdentity(t *testing.T) {
	saved := herdrConsoleTestPane("/repo", "workspace-root", "pane-old")
	saved.SourceProjectRoot = "/repo"
	live := backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: saved.WorkspaceID, Pane: "pane-new",
		},
		WorkspaceLabel: saved.WorkspaceLabel,
		TerminalID:     "terminal-new",
		SessionID:      saved.SessionID,
		SocketPath:     saved.SocketPath,
		CurrentPath:    saved.WorktreePath,
	}

	got, err := staleHerdrConsoleTarget(saved, []backend.LivePane{live})
	if err != nil {
		t.Fatalf("staleHerdrConsoleTarget() = %+v, %v", got, err)
	}
	if got.Ref.Pane != live.Ref.Pane || got.TerminalID != live.TerminalID {
		t.Fatalf("stale target = %+v, want current process identity %+v", got, live)
	}

	live.WorkspaceLabel = "foreign"
	if _, err := staleHerdrConsoleTarget(saved, []backend.LivePane{live}); err == nil {
		t.Fatal("staleHerdrConsoleTarget() accepted a workspace with a foreign label")
	}
}

func TestStaleHerdrConsoleWorkspaceWithoutPaneRequiresManualCleanup(t *testing.T) {
	saved := herdrConsoleTestPane("/repo", "workspace-root", "pane-old")
	workspaces := []backend.WorkspaceObservation{{
		WorkspaceID: saved.WorkspaceID,
		Label:       saved.WorkspaceLabel,
	}}
	present, err := savedHerdrConsoleWorkspacePresent(saved, workspaces)
	if err != nil || !present {
		t.Fatalf("savedHerdrConsoleWorkspacePresent() = %t, %v, want present", present, err)
	}
	if _, err := staleHerdrConsoleTarget(saved, nil); !errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("staleHerdrConsoleTarget() error = %v, want manual cleanup", err)
	}
	workspaces[0].Label = "foreign"
	if _, err := savedHerdrConsoleWorkspacePresent(saved, workspaces); err == nil {
		t.Fatal("savedHerdrConsoleWorkspacePresent() accepted a foreign workspace label")
	}
}

func TestAbsentHerdrConsoleWorkspaceAllowsSavedRowRemoval(t *testing.T) {
	saved := herdrConsoleTestPane("/repo", "workspace-root", "pane-old")
	present, err := savedHerdrConsoleWorkspacePresent(saved, nil)
	if err != nil || present {
		t.Fatalf("savedHerdrConsoleWorkspacePresent() = %t, %v, want absent", present, err)
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

func TestRemoveStaleHerdrConsoleStateRemovesCompletedIntentBeforeRow(t *testing.T) {
	root, _ := herdrConsoleTestWorktrees(t)
	pane := herdrConsoleTestPane(root, "workspace-root", "pane-root")
	pane.SourceProjectRoot = root
	locked, err := state.LockProjectForLaunch(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	if err = locked.RecordPane(pane); err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.CoordinatorIntentID(HerdrConsoleRuntimeParent, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(state.LaunchIntent{
		ID: intentID, Kind: state.IntentCoordinator, Status: state.IntentRealized,
		Parent: HerdrConsoleRuntimeParent, RuntimeParent: HerdrConsoleRuntimeParent,
		WorktreePath: pane.WorktreePath, WorkspaceLabel: pane.WorkspaceLabel,
		Resource: state.RuntimeResource{
			WorkspaceID: pane.WorkspaceID, Label: pane.WorkspaceLabel,
			PaneID: pane.PaneID, TerminalID: pane.TerminalID, CurrentPath: pane.WorktreePath,
		},
		Session: pane.SessionID, SocketPath: pane.SocketPath,
		ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli(),
	})
	if err = journal.Save(); err != nil {
		t.Fatal(err)
	}
	if err = removeStaleHerdrConsoleState(locked, root, pane); err != nil {
		t.Fatal(err)
	}
	journal, err = locked.LaunchJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := journal.FindIntent(intentID); found {
		t.Fatal("completed console intent remains after stale cleanup")
	}
	if _, found := locked.Find(pane.Parent, pane.IssueNum); found {
		t.Fatal("stale console row remains after intent cleanup")
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
		WorkspaceID: workspace, WorkspaceLabel: "fanout-console-owned",
		TerminalID: "terminal-" + paneID,
		SessionID:  "fanout-owned", SocketPath: "/tmp/fanout-owned.sock",
		Agent: state.PaneKindShell, DisplayName: "Herdr console",
		WorktreePath: root, CreatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
	}
}
