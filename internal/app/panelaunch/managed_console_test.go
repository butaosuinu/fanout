package panelaunch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// attachOnlyManagedRuntime fakes just the attach forms managedConsoleResult
// consumes; every other ManagedSessionRuntime method panics via the nil embed.
type attachOnlyManagedRuntime struct {
	ManagedSessionRuntime
	attachBase []string
	attach     backend.AttachExec
}

func (f *attachOnlyManagedRuntime) AttachForms(base []string) (string, backend.AttachExec, error) {
	f.attachBase = base
	return "ATTACH='command'", f.attach, nil
}

// capsuleOnlyManagedRuntime fakes just the two environment ports the launch
// capsule builders consume; every other method panics via the nil embed.
type capsuleOnlyManagedRuntime struct {
	ManagedLaunchRuntime
	prepared []string
}

func (f *capsuleOnlyManagedRuntime) WorkloadEnvironment(caller []string, fanoutPath string) ([]string, error) {
	return append(append([]string{}, caller...), "FANOUT_BIN="+fanoutPath), nil
}

func (f *capsuleOnlyManagedRuntime) PrepareWorkloadEnvironment(nonce string, environment []string) (string, int, error) {
	f.prepared = append([]string{}, environment...)
	return "/capsule/env-" + nonce + ".json", len(environment), nil
}

func TestNewManagedConsoleLaunchRunsPinnedFanoutWithShellHandoff(t *testing.T) {
	owned := &capsuleOnlyManagedRuntime{}
	route := backend.OwnedLaunchRoute{LauncherPath: "/owned/launcher/fanout", EmitterPath: "/owned/launcher/fanout"}
	capsule, err := newManagedConsoleLaunch(owned, route, "/bin/zsh", []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if capsule.Executable != route.LauncherPath {
		t.Fatalf("console capsule executable = %q, want the pinned fanout %q", capsule.Executable, route.LauncherPath)
	}
	// The reserved argv token keeps the exec'd TUI distinguishable from the
	// idle pane launcher, which is the same binary with an empty argv tail.
	if !reflect.DeepEqual(capsule.Args, []string{ManagedConsoleWorkloadArg}) {
		t.Fatalf("console capsule args = %v, want the reserved console token", capsule.Args)
	}
	last := owned.prepared[len(owned.prepared)-1]
	if last != backend.ConsoleShellEnv+"=/bin/zsh" {
		t.Fatalf("console capsule environment tail = %q, want the hand-off shell", last)
	}
}

func TestNewManagedShellLaunchKeepsTheOperatorShellWorkload(t *testing.T) {
	owned := &capsuleOnlyManagedRuntime{}
	route := backend.OwnedLaunchRoute{LauncherPath: "/owned/launcher/fanout", EmitterPath: "/owned/launcher/fanout"}
	capsule, err := newManagedShellLaunch(owned, route, "/bin/zsh", []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if capsule.Executable != "/bin/zsh" {
		t.Fatalf("manual shell capsule executable = %q, want /bin/zsh", capsule.Executable)
	}
	for _, entry := range owned.prepared {
		if strings.HasPrefix(entry, backend.ConsoleShellEnv+"=") {
			t.Fatalf("manual shell capsule recorded a console hand-off: %v", owned.prepared)
		}
	}
}

func TestClassifyManagedConsoleProcessAcceptsTUIAndHandoffShell(t *testing.T) {
	route := backend.OwnedLaunchRoute{LauncherPath: "/owned/launcher/fanout", EmitterPath: "/owned/launcher/fanout"}
	intent := state.LaunchIntent{
		WorktreePath: "/repo",
		Launch: &state.LaunchCapsule{
			Executable: route.LauncherPath,
			Args:       []string{ManagedConsoleWorkloadArg},
		},
	}
	paneProcess := func(executable string, argv []string) backend.PaneProcessInfo {
		return backend.PaneProcessInfo{
			ShellPID: 42, ForegroundProcessGroup: 42,
			ForegroundProcesses: []backend.PaneProcess{{
				PID: 42, ParentPID: 1, ProcessGroup: 42, Executable: executable,
				CWD: intent.WorktreePath, Argv0: executable, Argv: argv,
			}},
		}
	}
	tests := []struct {
		name    string
		process backend.PaneProcessInfo
		want    string // "", "pending", or an error fragment
	}{
		{
			name:    "exec'd console TUI is started",
			process: paneProcess(route.LauncherPath, []string{ManagedConsoleWorkloadArg}),
		},
		{
			// An init failure hands the pane to the operator shell; the pane is
			// healthy, so the intent must not fall to manual cleanup.
			name:    "handed-off operator shell is started",
			process: paneProcess("/bin/zsh", nil),
		},
		{
			name:    "launcher still waiting for its token is pending",
			process: paneProcess(route.LauncherPath, nil),
			want:    "pending",
		},
		{
			name:    "foreign process is a mismatch",
			process: paneProcess("/usr/bin/vim", nil),
			want:    "identity does not match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyManagedConsoleProcess(tt.process, intent, route, "/bin/zsh")
			switch tt.want {
			case "":
				if err != nil {
					t.Fatalf("classifyManagedConsoleProcess() = %v, want started", err)
				}
			case "pending":
				if !errors.Is(err, managedLaunchTransitionPending{}) {
					t.Fatalf("classifyManagedConsoleProcess() = %v, want pending", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("classifyManagedConsoleProcess() = %v, want %q", err, tt.want)
				}
			}
		})
	}
}

func TestValidateManagedConsoleLaunchRefusesNonConsoleWorkloads(t *testing.T) {
	route := backend.OwnedLaunchRoute{LauncherPath: "/owned/launcher/fanout", EmitterPath: "/owned/launcher/fanout"}
	validate := validateManagedConsoleLaunch(route)
	tests := []struct {
		name    string
		launch  *state.LaunchCapsule
		wantErr string
	}{
		{
			name:   "pinned console workload is admitted",
			launch: &state.LaunchCapsule{Executable: route.LauncherPath, Args: []string{ManagedConsoleWorkloadArg}},
		},
		{
			// A journal intent saved by a pre-upgrade bootstrap still names the
			// operator shell; its token must never be issued.
			name:    "saved operator-shell capsule is refused",
			launch:  &state.LaunchCapsule{Executable: "/bin/zsh"},
			wantErr: "does not run the pinned console workload",
		},
		{
			name:    "missing console token is refused",
			launch:  &state.LaunchCapsule{Executable: route.LauncherPath},
			wantErr: "does not run the pinned console workload",
		},
		{
			name:    "agent capsule is refused by the shell shape check",
			launch:  &state.LaunchCapsule{Executable: route.LauncherPath, Args: []string{ManagedConsoleWorkloadArg}, Agent: "claude"},
			wantErr: "invalid launch capsule",
		},
		{name: "nil capsule is refused", wantErr: "invalid launch capsule"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(tt.launch)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateManagedConsoleLaunch()(%+v) = %v, want nil", tt.launch, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateManagedConsoleLaunch()(%+v) = %v, want %q", tt.launch, err, tt.wantErr)
			}
		})
	}
}

func TestManagedConsoleResultCarriesBothAttachForms(t *testing.T) {
	attach := backend.AttachExec{
		Path: "/pinned/client",
		Argv: []string{"/pinned/client"},
		Env:  []string{"PATH=/usr/bin"},
	}
	owned := &attachOnlyManagedRuntime{attach: attach}
	caller := []string{"PATH=/usr/bin", "TERM=xterm"}
	pane := state.Pane{PaneID: "pane-1"}
	result, err := managedConsoleResult(owned, pane, caller)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(owned.attachBase, caller) {
		t.Fatalf("AttachExec base = %v, want the caller environment %v", owned.attachBase, caller)
	}
	want := ManagedConsoleResult{Pane: pane, AttachCommand: "ATTACH='command'", Attach: attach}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("managedConsoleResult() = %+v, want %+v", result, want)
	}
}

func TestFindManagedConsolePaneAcrossLinkedWorktrees(t *testing.T) {
	root, linked := managedConsoleTestWorktrees(t)
	pane := managedConsoleTestPane(linked, "workspace-linked", "pane-linked")
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
	got, found, err := findManagedConsolePane(root, current)
	if err != nil || !found {
		t.Fatalf("findManagedConsolePane() = %+v, %t, %v", got, found, err)
	}
	if got.PaneID != pane.PaneID || got.SourceProjectRoot != linked {
		t.Fatalf("found pane = %+v, want pane %q from %q", got, pane.PaneID, linked)
	}
}

func TestFindManagedConsolePaneRejectsDuplicateAcrossWorktrees(t *testing.T) {
	root, linked := managedConsoleTestWorktrees(t)
	for _, item := range []struct {
		root string
		pane state.Pane
	}{
		{root: root, pane: managedConsoleTestPane(root, "workspace-root", "pane-root")},
		{root: linked, pane: managedConsoleTestPane(linked, "workspace-linked", "pane-linked")},
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
	if _, _, err := findManagedConsolePane(root, current); err == nil {
		t.Fatal("findManagedConsolePane() accepted duplicate console rows")
	}
}

func TestManagedConsolePaneInStoreDoesNotIgnoreIncompleteIdentity(t *testing.T) {
	pane := managedConsoleTestPane("/repo", "workspace-root", "")
	got, found, err := managedConsolePaneInStore("/repo", state.Store{Panes: []state.Pane{pane}})
	if err != nil || !found {
		t.Fatalf("managedConsolePaneInStore() = %+v, %t, %v, want saved row", got, found, err)
	}
	if got.PaneID != "" {
		t.Fatalf("saved pane id = %q, want incomplete identity preserved for fail-closed validation", got.PaneID)
	}
	if err := validateSavedManagedConsoleShape(got); err == nil {
		t.Fatal("incomplete saved console identity was accepted for stale cleanup")
	}
}

func TestManagedShellStatePaneUsesAdmittedCanonicalPath(t *testing.T) {
	intent := state.LaunchIntent{WorktreePath: "/canonical/repo"}
	live := backend.LivePane{
		Ref:            backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		WorkspaceLabel: "fanout-manual-owned",
		TerminalID:     "terminal-1",
		SessionID:      "fanout-owned",
		SocketPath:     "/tmp/fanout-owned.sock",
	}
	pane := managedShellStatePane(intent, live, -1, "shell", "Shell", "")
	if pane.WorktreePath != intent.WorktreePath {
		t.Fatalf("saved path = %q, want admitted path %q", pane.WorktreePath, intent.WorktreePath)
	}
}

func TestValidateManagedConsoleIntentRootRejectsLinkedWorktreeRecovery(t *testing.T) {
	intent := state.LaunchIntent{WorktreePath: "/repo/linked-a"}
	if err := validateManagedConsoleIntentRoot(intent, "/repo/linked-b"); err == nil {
		t.Fatal("console recovery accepted an intent owned by another linked worktree")
	}
	if err := validateManagedConsoleIntentRoot(intent, intent.WorktreePath); err != nil {
		t.Fatalf("console recovery rejected its owning worktree: %v", err)
	}
}

func TestStaleManagedConsoleRecoveryRequiresIdentityMismatch(t *testing.T) {
	if staleManagedConsoleRecoverable(os.ErrDeadlineExceeded) {
		t.Fatal("transient observation error qualified for destructive stale recovery")
	}
	mismatch := fmt.Errorf("bind saved console: %w", backend.ErrOwnedIdentityMismatch)
	if !staleManagedConsoleRecoverable(mismatch) {
		t.Fatal("owned identity mismatch did not qualify for stale recovery")
	}
}

func TestStaleManagedConsoleTargetAdmitsOwnedRouteWithNewProcessIdentity(t *testing.T) {
	saved := managedConsoleTestPane("/repo", "workspace-root", "pane-old")
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

	got, err := staleManagedConsoleTarget(saved, []backend.LivePane{live})
	if err != nil {
		t.Fatalf("staleManagedConsoleTarget() = %+v, %v", got, err)
	}
	if got.Ref.Pane != live.Ref.Pane || got.TerminalID != live.TerminalID {
		t.Fatalf("stale target = %+v, want current process identity %+v", got, live)
	}

	live.WorkspaceLabel = "foreign"
	if _, err := staleManagedConsoleTarget(saved, []backend.LivePane{live}); err == nil {
		t.Fatal("staleManagedConsoleTarget() accepted a workspace with a foreign label")
	}
}

func TestStaleManagedConsoleWorkspaceWithoutPaneRequiresManualCleanup(t *testing.T) {
	saved := managedConsoleTestPane("/repo", "workspace-root", "pane-old")
	workspaces := []backend.WorkspaceObservation{{
		WorkspaceID: saved.WorkspaceID,
		Label:       saved.WorkspaceLabel,
	}}
	present, err := savedManagedConsoleWorkspacePresent(saved, workspaces)
	if err != nil || !present {
		t.Fatalf("savedManagedConsoleWorkspacePresent() = %t, %v, want present", present, err)
	}
	if _, err := staleManagedConsoleTarget(saved, nil); !errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("staleManagedConsoleTarget() error = %v, want manual cleanup", err)
	}
	workspaces[0].Label = "foreign"
	if _, err := savedManagedConsoleWorkspacePresent(saved, workspaces); err == nil {
		t.Fatal("savedManagedConsoleWorkspacePresent() accepted a foreign workspace label")
	}
}

func TestAbsentManagedConsoleWorkspaceAllowsSavedRowRemoval(t *testing.T) {
	saved := managedConsoleTestPane("/repo", "workspace-root", "pane-old")
	present, err := savedManagedConsoleWorkspacePresent(saved, nil)
	if err != nil || present {
		t.Fatalf("savedManagedConsoleWorkspacePresent() = %t, %v, want absent", present, err)
	}
}

func TestRemoveSavedManagedConsoleRowFromOwningLinkedWorktree(t *testing.T) {
	root, linked := managedConsoleTestWorktrees(t)
	pane := managedConsoleTestPane(linked, "workspace-linked", "pane-linked")
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
	err = removeSavedManagedConsoleRow(locked, root, pane)
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

func TestRemoveStaleManagedConsoleStateRemovesCompletedIntentBeforeRow(t *testing.T) {
	root, _ := managedConsoleTestWorktrees(t)
	pane := managedConsoleTestPane(root, "workspace-root", "pane-root")
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
	intentID, err := state.CoordinatorIntentID(ManagedConsoleRuntimeParent, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(state.LaunchIntent{
		ID: intentID, Kind: state.IntentCoordinator, Status: state.IntentRealized,
		Parent: ManagedConsoleRuntimeParent, RuntimeParent: ManagedConsoleRuntimeParent,
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
	if err = removeStaleManagedConsoleState(locked, root, pane); err != nil {
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

func managedConsoleTestWorktrees(t *testing.T) (string, string) {
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

func managedConsoleTestPane(root, workspace, paneID string) state.Pane {
	return state.Pane{
		Parent: ManualParentRef, RuntimeParent: ManagedConsoleRuntimeParent,
		IssueNum: -1, Kind: state.PaneKindShell, Slug: "herdr-console",
		Backend: backend.Herdr, PaneID: paneID,
		WorkspaceID: workspace, WorkspaceLabel: "fanout-console-owned",
		TerminalID: "terminal-" + paneID,
		SessionID:  "fanout-owned", SocketPath: "/tmp/fanout-owned.sock",
		Agent: state.PaneKindShell, DisplayName: "Herdr console",
		WorktreePath: root, CreatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
	}
}
