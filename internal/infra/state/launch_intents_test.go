package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
)

func TestHerdrIntentsAreSharedAcrossLinkedWorktrees(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	runLaunchJournalGit(t, repo, "worktree", "add", "-b", "sibling", sibling, "HEAD")

	project, view := lockLaunchJournalForTest(t, repo)
	intent := testCoordinatorIntent(repo, "0425")
	view.UpsertIntent(intent)
	if err := view.Save(); err != nil {
		t.Fatal(err)
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}

	fromSibling, err := LoadLaunchJournal(sibling)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := fromSibling.FindIntent(intent.ID)
	if !ok || got.Parent != "425" {
		t.Fatalf("shared intent = (%+v, %t), want saved coordinator", got, ok)
	}
	repoPath, err := LaunchJournalPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	siblingPath, err := LaunchJournalPath(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if repoPath != siblingPath {
		t.Fatalf("control paths differ:\nrepo: %s\nsibling: %s", repoPath, siblingPath)
	}
}

// Group-writable .git (core.sharedRepository=group checkouts) must not block
// the combined launch lock: the journal follows state.json parity and has no
// namespace hardening of its own.
func TestLockProjectForLaunchAcceptsGroupWritableCommonDir(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	if err := os.Chmod(filepath.Join(repo, ".git"), 0o775); err != nil {
		t.Fatal(err)
	}
	project, err := LockProjectForLaunch(repo)
	if err != nil {
		t.Fatalf("LockProjectForLaunch() with group-writable .git = %v, want success", err)
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestLockProjectForLaunchContextReleasesIntentsAfterStateTimeout(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	stateOnly, err := Lock(Path(repo))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateOnly.Unlock() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, lockErr := LockProjectForLaunchContext(ctx, repo); !errors.Is(lockErr, context.DeadlineExceeded) {
		t.Fatalf("LockProjectForLaunchContext() error = %v, want context deadline", lockErr)
	}
	intentsPath, err := LaunchJournalPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	contender, err := os.OpenFile(intentsPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = contender.Close() }()
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("partially acquired Herdr intents lock was not released: %v", err)
	}
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

func TestLockProjectForLaunchAtCombinesCustomStateAndIntentLocks(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	customState := filepath.Join(t.TempDir(), "state.json")
	project, err := LockProjectForLaunchAt(repo, customState)
	if err != nil {
		t.Fatal(err)
	}
	view, err := project.LaunchJournal(repo)
	if err != nil {
		t.Fatal(errors.Join(err, project.Unlock()))
	}
	intent := testCoordinatorIntent(repo, "425")
	view.UpsertIntent(intent)
	if saveErr := view.Save(); saveErr != nil {
		t.Fatal(errors.Join(saveErr, project.Unlock()))
	}
	if unlockErr := project.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	loaded, err := LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := loaded.FindIntent(intent.ID); !found {
		t.Fatal("intent was not saved through the custom state lock")
	}
}

func TestHerdrIntentsRejectExistingJournalWithoutSchemaVersion(t *testing.T) {
	for _, contents := range []string{`{}`, `{"schemaVersion":0}`} {
		t.Run(contents, func(t *testing.T) {
			repo := newLaunchJournalRepo(t)
			path, err := LaunchJournalPath(repo)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadLaunchJournal(repo); err == nil ||
				!strings.Contains(err.Error(), "unsupported Herdr intents schema version 0") {
				t.Fatalf("schema-less journal error = %v", err)
			}
		})
	}
}

func TestHerdrIntentsRejectFutureSchemaVersion(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	path, err := LaunchJournalPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLaunchJournal(repo); err == nil ||
		!strings.Contains(err.Error(), "unsupported Herdr intents schema version 2") {
		t.Fatalf("future schema error = %v", err)
	}
}

func TestProjectStateLockSerializesHerdrControlWriter(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	project, err := LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	controlPath, err := LaunchJournalPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	contender, err := os.OpenFile(controlPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = contender.Close() }()

	err = syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("Herdr control lock while project state is locked = %v, want would block", err)
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("Herdr control lock after project unlock: %v", err)
	}
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("unlock contender: %v", err)
	}
}

func TestHerdrControlBindingsIncludeEveryIntentStatus(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	store := emptyLaunchJournal()
	statuses := []LaunchIntentStatus{
		IntentPlanned,
		IntentIssued,
		IntentRealized,
		IntentManualCleanupRequired,
	}
	for i, status := range statuses {
		intent := testCoordinatorIntent(repo, strconv.Itoa(500+i))
		intent.Status = status
		if status == IntentRealized {
			intent.Resource = testCoordinatorResource(repo)
		}
		if status == IntentManualCleanupRequired {
			intent.Failure = "response loss"
		}
		store.Intents = append(store.Intents, intent)
	}
	if err := validateLaunchJournal(store); err != nil {
		t.Fatal(err)
	}

	intents := store.ProvisionalBindings(repo)
	if len(intents) != len(statuses) {
		t.Fatalf("intent bindings = %#v, want %d", intents, len(statuses))
	}
	for _, binding := range intents {
		parent, err := strconv.Atoi(binding.Parent)
		if err != nil || parent < 500 || parent >= 500+len(statuses) ||
			binding.Backend != backend.Herdr {
			t.Fatalf("unexpected intent binding: %+v", binding)
		}
	}
}

func TestHerdrServerLifecycleIntentValidatesExactIdentity(t *testing.T) {
	for _, kind := range []LaunchIntentKind{IntentRestart, IntentShutdown} {
		t.Run(string(kind), func(t *testing.T) {
			intent := testServerIntent(kind)
			store := emptyLaunchJournal()
			store.Intents = append(store.Intents, intent)
			if err := validateLaunchJournal(store); err != nil {
				t.Fatal(err)
			}
			got, found, err := store.ServerLifecycleIntent()
			if err != nil || !found || got.ID != intent.ID || got.Server == nil || *got.Server != *intent.Server {
				t.Fatalf("ServerLifecycleIntent() = (%+v, %t, %v)", got, found, err)
			}
			if bindings := store.ProvisionalBindings("/repo"); len(bindings) != 0 {
				t.Fatalf("server lifecycle provisional bindings = %+v, want none", bindings)
			}
		})
	}
}

func TestHerdrServerLifecycleIntentAllowsOnlyIssuedShutdown(t *testing.T) {
	shutdown := testServerIntent(IntentShutdown)
	shutdown.Status = IntentIssued
	if err := validateIntent(shutdown); err != nil {
		t.Fatal(err)
	}
	restart := testServerIntent(IntentRestart)
	restart.Status = IntentIssued
	if err := validateIntent(restart); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("issued restart error = %v", err)
	}
}

func TestHerdrResumeIntentRequiresExactCodexSessionAndArgv(t *testing.T) {
	valid := testResumeIntent()
	if err := validateIntent(valid); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*LaunchIntent)
	}{
		{name: "wrong id", mutate: func(intent *LaunchIntent) { intent.ID = "resume:" + strings.Repeat("0", 64) }},
		{name: "missing ref", mutate: func(intent *LaunchIntent) { intent.ResumeAgentSession = nil }},
		{name: "path ref", mutate: func(intent *LaunchIntent) { intent.ResumeAgentSession.Kind = "path" }},
		{name: "wrong session", mutate: func(intent *LaunchIntent) { intent.Launch.Args[1] = "other" }},
		{name: "extra arg", mutate: func(intent *LaunchIntent) { intent.Launch.Args = append(intent.Launch.Args, "--full-auto") }},
		{name: "wrong provider", mutate: func(intent *LaunchIntent) { intent.Launch.Agent = "claude" }},
		{name: "preassigned agent name", mutate: func(intent *LaunchIntent) { intent.Launch.AgentName = "fanout-codex" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := testResumeIntent()
			test.mutate(&intent)
			if err := validateIntent(intent); err == nil {
				t.Fatal("validateIntent() accepted an inexact resume")
			}
		})
	}
}

func TestHerdrServerLifecycleIntentRejectsAmbiguousOrIncompleteRows(t *testing.T) {
	restart := testServerIntent(IntentRestart)
	shutdown := testServerIntent(IntentShutdown)
	store := emptyLaunchJournal()
	store.Intents = []LaunchIntent{restart, shutdown}
	if err := validateLaunchJournal(store); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("multiple lifecycle intents error = %v", err)
	}

	restart.Server.ServerPID = 0
	store.Intents = []LaunchIntent{restart}
	if err := validateLaunchJournal(store); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("restart without server pid error = %v", err)
	}

	regular := testCoordinatorIntent("/repo", "637")
	regular.Server = shutdown.Server
	if err := validateIntent(regular); err == nil || !strings.Contains(err.Error(), "unrelated server identity") {
		t.Fatalf("regular intent with server identity error = %v", err)
	}
	restart = testServerIntent(IntentRestart)
	restart.CleanupPhase = CleanupRemove
	if err := validateIntent(restart); err == nil || !strings.Contains(err.Error(), "unrelated fields") {
		t.Fatalf("server intent with cleanup phase error = %v", err)
	}
}

func TestHerdrPlanBindingsAreOwnerWorktreeLocal(t *testing.T) {
	first := testCoordinatorIntent("/repo/one", "plan:demo")
	second := testCoordinatorIntent("/repo/two", "plan:demo")
	if first.ID == second.ID {
		t.Fatalf("plan intent IDs collide across owner roots: %s", first.ID)
	}
	intents := emptyLaunchJournal()
	intents.Intents = []LaunchIntent{first, second}
	if err := validateLaunchJournal(intents); err != nil {
		t.Fatal(err)
	}
	if got := intents.ProvisionalBindings("/repo/one"); len(got) != 1 ||
		got[0].Parent != "plan:demo" {
		t.Fatalf("first plan intent bindings = %#v", got)
	}
	if got := intents.ProvisionalBindings("/repo/two"); len(got) != 1 ||
		got[0].Parent != "plan:demo" {
		t.Fatalf("second plan intent bindings = %#v", got)
	}
}

func TestHerdrIssueSourcedPlanBindingsUseResolvedParentAcrossWorktrees(t *testing.T) {
	intent := testCoordinatorIntent("/repo/one", "plan:demo")
	intent.RuntimeParent = "425"
	intent.ID, _ = CoordinatorIntentID(intent.RuntimeParent, "", 0)
	store := emptyLaunchJournal()
	store.Intents = append(store.Intents, intent)

	got := store.ProvisionalBindings("/repo/two")
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "425", Backend: backend.Herdr}) {
		t.Fatalf("issue-sourced plan bindings = %#v", got)
	}
}

func TestHerdrSyntheticBindingsProjectWatcherIssuesAndKeepResolvedParents(t *testing.T) {
	store := emptyLaunchJournal()
	store.Intents = []LaunchIntent{
		{RuntimeParent: "@manual", IssueNum: 424},
		{RuntimeParent: "@watch", IssueNum: 426},
		{RuntimeParent: "428"},
	}

	intents := store.ProvisionalBindings("/repo")
	wantIntents := []backend.Binding{
		{Parent: "426", Backend: backend.Herdr},
		{Parent: "428", Backend: backend.Herdr},
	}
	if !slices.Equal(intents, wantIntents) {
		t.Fatalf("synthetic intent bindings = %#v", intents)
	}
}

func TestHerdrIntentsRejectDuplicateBranchAndPathReservations(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	project, view := lockLaunchJournalForTest(t, repo)
	t.Cleanup(func() {
		if err := project.Unlock(); err != nil {
			t.Errorf("unlock: %v", err)
		}
	})
	first := testWorktreeIntent(repo, "425", 426, "first")
	second := testWorktreeIntent(repo, "425", 427, "second")
	second.BranchName = first.BranchName
	second.FullBranchRef = first.FullBranchRef
	view.Intents = []LaunchIntent{first, second}
	if err := view.Save(); err == nil || !strings.Contains(err.Error(), "reserve the same branch:") {
		t.Fatalf("duplicate branch save error = %v", err)
	}

	second.BranchName = "fanout/second"
	second.FullBranchRef = "refs/heads/" + second.BranchName
	second.WorktreePath = first.WorktreePath
	view.Intents = []LaunchIntent{first, second}
	if err := view.Save(); err == nil || !strings.Contains(err.Error(), "reserve the same path:") {
		t.Fatalf("duplicate path save error = %v", err)
	}
}

func TestHerdrIntentIDsUseTmuxIssueAndTaskKeys(t *testing.T) {
	issue, err := WorktreeIntentID("00425", "", 426, "")
	if err != nil {
		t.Fatal(err)
	}
	alias, err := WorktreeIntentID("425", "", 426, "")
	if err != nil {
		t.Fatal(err)
	}
	if issue != alias {
		t.Fatalf("numeric parent aliases differ: %q != %q", issue, alias)
	}
	task, err := WorktreeIntentID("plan:demo", "/repo/one", 0, "api:client")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task, "plan:demo") || !strings.Contains(task, "api:client") {
		t.Fatalf("task id = %q, want length-prefixed plan/task identity", task)
	}
	if _, mixedErr := WorktreeIntentID("plan:demo", "/repo/one", 1, "task"); mixedErr == nil {
		t.Fatal("issue and task identity unexpectedly accepted together")
	}
	manual, err := WorktreeIntentID("@manual", "/repo/one", -1, "")
	if err != nil {
		t.Fatal(err)
	}
	otherManual, err := WorktreeIntentID("@manual", "/repo/two", -1, "")
	if err != nil {
		t.Fatal(err)
	}
	if manual == otherManual {
		t.Fatalf("manual worktree IDs collide across owner roots: %q", manual)
	}
	if _, err := WorktreeIntentID("425", "", -1, ""); err == nil {
		t.Fatal("negative issue number was accepted for a non-manual parent")
	}
}

func TestHerdrRollbackIntentKeepsIndependentMutationRecord(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	original := testWorktreeIntent(repo, "425", 426, "rollback")
	original.Status = IntentRealized
	original.Resource = RuntimeResource{
		WorkspaceID: "w2", Label: original.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: original.WorktreePath,
		RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	rollback := original
	rollback.ID, _ = RollbackIntentID(original.ID)
	rollback.Kind = IntentRollback
	rollback.Status = IntentPlanned
	store := emptyLaunchJournal()
	store.Intents = []LaunchIntent{original, rollback}
	if err := validateLaunchJournal(store); err != nil {
		t.Fatal(err)
	}
	rollback.Launch = &LaunchCapsule{}
	if err := validateIntent(rollback); err == nil {
		t.Fatal("rollback intent accepted an agent launch capsule")
	}
}

func TestHerdrLaunchRequiresAbsoluteTeamPaths(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	intent := testWorktreeIntent(repo, "425", 426, "team-status")
	intent.Status = IntentRealized
	intent.Resource = RuntimeResource{
		WorkspaceID: "w2", Label: intent.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: intent.WorktreePath,
		RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	intent.Launch = &LaunchCapsule{
		Nonce: strings.Repeat("a", 32), Agent: "codex",
		AgentName: "fanout-0123456789abcdef01234567", Executable: "/bin/fanout",
		TeamDBPath: "relative-team.db", EnvFilePath: "/tmp/env", EnvNameCount: 1,
	}
	if err := validateIntent(intent); err == nil || !strings.Contains(err.Error(), "launch fields are incomplete") {
		t.Fatalf("relative team DB path error = %v", err)
	}
	intent.Launch.TeamDBPath = "/tmp/team.db"
	intent.Launch.CodexTeamStatusPath = "relative-status.json"
	if err := validateIntent(intent); err == nil || !strings.Contains(err.Error(), "launch fields are incomplete") {
		t.Fatalf("relative team status path error = %v", err)
	}
	intent.Launch.CodexTeamStatusPath = "/tmp/team-status.json"
	if err := validateIntent(intent); err != nil {
		t.Fatalf("absolute team status path error = %v", err)
	}
}

func TestHerdrCoordinatorLaunchAllowsShellAndRejectsPartialAgentIdentity(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	intent := testCoordinatorIntent(repo, "425")
	intent.Status = IntentIssued
	intent.Launch = &LaunchCapsule{
		Nonce:        strings.Repeat("a", 32),
		Executable:   "/bin/zsh",
		EnvFilePath:  "/private/tmp/fanout-test/env.json",
		EnvNameCount: 1,
	}
	if err := validateIntent(intent); err != nil {
		t.Fatalf("issued coordinator shell launch = %v, want success", err)
	}

	intent.Launch.Agent = "codex"
	if err := validateIntent(intent); err == nil ||
		!strings.Contains(err.Error(), "launch agent identity is partial") {
		t.Fatalf("partial agent identity error = %v", err)
	}
}

func TestHerdrCleanupIntentKeepsIndependentMutationRecord(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	cleanup := testCleanupIntent(repo, CleanupRemove, IntentPlanned)
	store := emptyLaunchJournal()
	store.Intents = []LaunchIntent{cleanup}
	if err := validateLaunchJournal(store); err != nil {
		t.Fatal(err)
	}
	cleanup.CleanupPhase = "unknown"
	if err := validateIntent(cleanup); err == nil || !strings.Contains(err.Error(), "cleanup fields are incomplete") {
		t.Fatalf("unknown cleanup phase error = %v", err)
	}
	cleanup = testCleanupIntent(repo, CleanupRemove, IntentPlanned)
	cleanup.CleanupHookPhase = "unknown"
	if err := validateIntent(cleanup); err == nil || !strings.Contains(err.Error(), "cleanup fields are incomplete") {
		t.Fatalf("unknown cleanup hook phase error = %v", err)
	}
	for _, phase := range []CleanupHookPhase{
		CleanupHookBeforeWorktreeRemoveIssued,
		CleanupHookBeforePaneCloseIssued,
	} {
		cleanup = testCleanupIntent(repo, CleanupRemove, IntentPlanned)
		cleanup.CleanupHookPhase = phase
		if err := validateIntent(cleanup); err != nil {
			t.Fatalf("cleanup hook phase %q: %v", phase, err)
		}
	}
	for _, phase := range []CleanupHookPhase{
		CleanupHookPaneClosedIssued,
		CleanupHookWorktreeRemovedIssued,
		CleanupHookCompleted,
	} {
		cleanup = testCleanupIntent(repo, CleanupRemove, IntentRealized)
		cleanup.CleanupHookPhase = phase
		if err := validateIntent(cleanup); err != nil {
			t.Fatalf("cleanup completion hook phase %q: %v", phase, err)
		}
	}
	cleanup = testCleanupIntent(repo, CleanupRemove, IntentRealized)
	required := false
	cleanup.CleanupWorktreeRemovedRequired = &required
	cleanup.CleanupHookPhase = CleanupHookWorktreeRemovedIssued
	if err := validateIntent(cleanup); err == nil || !strings.Contains(err.Error(), "cleanup fields are incomplete") {
		t.Fatalf("worktree_removed without obligation error = %v", err)
	}
}

func TestHerdrCleanupIntentCoexistsWithServerRestart(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	phases := []CleanupPhase{CleanupReopen, CleanupRemove, CleanupWorkspaceClose}
	statuses := []LaunchIntentStatus{
		IntentPlanned, IntentIssued, IntentManualCleanupRequired, IntentRealized,
	}
	for _, phase := range phases {
		for _, status := range statuses {
			t.Run(string(phase)+"/"+string(status), func(t *testing.T) {
				cleanup := testCleanupIntent(repo, phase, status)
				store := emptyLaunchJournal()
				store.Intents = []LaunchIntent{cleanup, testServerIntent(IntentRestart)}
				if err := validateLaunchJournal(store); err != nil {
					t.Fatal(err)
				}
				got, found, err := store.ServerLifecycleIntent()
				if err != nil || !found || got.Kind != IntentRestart {
					t.Fatalf("ServerLifecycleIntent() = (%+v, %t, %v)", got, found, err)
				}
			})
		}
	}
}

func testCleanupIntent(
	repo string,
	phase CleanupPhase,
	status LaunchIntentStatus,
) LaunchIntent {
	cleanup := testWorktreeIntent(repo, "425", 426, "cleanup")
	cleanup.ID, _ = CleanupIntentID(cleanup.ID)
	cleanup.Kind = IntentCleanup
	cleanup.Status = status
	cleanup.CleanupPhase = phase
	required := true
	cleanup.CleanupWorktreeRemovedRequired = &required
	cleanup.Resource = RuntimeResource{
		WorkspaceID: "w2", Label: cleanup.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: cleanup.WorktreePath,
		RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	if status == IntentManualCleanupRequired {
		cleanup.Failure = "server unavailable"
	}
	return cleanup
}

func TestHerdrCleanupIntentIDRejectsNestedCleanup(t *testing.T) {
	id, err := CleanupIntentID("issue:3:425:426")
	if err != nil {
		t.Fatal(err)
	}
	if id != "cleanup:issue:3:425:426" {
		t.Fatalf("cleanup id = %q", id)
	}
	if _, err := CleanupIntentID(id); err == nil {
		t.Fatal("nested cleanup intent id was accepted")
	}
}

func TestHerdrCoordinatorIntentIDsUseSyntheticIssueNumbers(t *testing.T) {
	firstManual, err := CoordinatorIntentID("@manual", "/repo/one", -1)
	if err != nil {
		t.Fatal(err)
	}
	secondManual, err := CoordinatorIntentID("@manual", "/repo/one", -2)
	if err != nil {
		t.Fatal(err)
	}
	otherOwner, err := CoordinatorIntentID("@manual", "/repo/two", -1)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := CoordinatorIntentID("@watch", "", 425)
	if err != nil {
		t.Fatal(err)
	}
	if firstManual == secondManual || firstManual == otherOwner || firstManual == watch ||
		secondManual == watch || otherOwner == watch {
		t.Fatalf(
			"synthetic coordinator IDs collide: %q, %q, %q, %q",
			firstManual,
			secondManual,
			otherOwner,
			watch,
		)
	}
	if _, err := CoordinatorIntentID("@manual", "/repo/one", 0); err == nil {
		t.Fatal("manual coordinator without synthetic issue number was accepted")
	}
	if _, err := CoordinatorIntentID("@watch", "", 0); err == nil {
		t.Fatal("watch coordinator without issue number was accepted")
	}
	if _, err := CoordinatorIntentID("425", "", 426); err == nil {
		t.Fatal("numeric coordinator with synthetic issue number was accepted")
	}
}

func TestHerdrControlRejectsIncompleteRealizedIntent(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	intent := testCoordinatorIntent(repo, "425")
	intent.Status = IntentRealized
	store := emptyLaunchJournal()
	store.Intents = append(store.Intents, intent)
	if err := validateLaunchJournal(store); err == nil || !strings.Contains(err.Error(), "resource is incomplete") {
		t.Fatalf("realized intent validation error = %v", err)
	}
}

func TestHerdrControlAcceptsSHA256ObjectIDs(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	intent := testWorktreeIntent(repo, "425", 426, "sha256")
	intent.BaseSHA = strings.Repeat("1", 64)
	intent.ExpectedHead = strings.Repeat("2", 64)
	store := emptyLaunchJournal()
	store.Intents = append(store.Intents, intent)
	if err := validateLaunchJournal(store); err != nil {
		t.Fatal(err)
	}
}

func TestHerdrControlIssuedWorktreeMayRetainRealizedResourceForOpenRecovery(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	intent := testWorktreeIntent(repo, "425", 426, "reopen")
	intent.Status = IntentIssued
	intent.Resource = RuntimeResource{
		WorkspaceID: "w2", Label: intent.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2",
		CurrentPath: intent.WorktreePath,
		RepoKey:     filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	if err := validateIntent(intent); err != nil {
		t.Fatalf("issued worktree open intent: %v", err)
	}

	coordinator := testCoordinatorIntent(repo, "425")
	coordinator.Status = IntentIssued
	coordinator.Resource = testCoordinatorResource(repo)
	if err := validateIntent(coordinator); err == nil ||
		!strings.Contains(err.Error(), "resource before realization") {
		t.Fatalf("issued coordinator resource error = %v", err)
	}
}

func TestHerdrControlRejectsResourceCurrentPathMismatch(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	intent := testWorktreeIntent(repo, "425", 426, "mismatch")
	intent.Status = IntentRealized
	intent.Resource = RuntimeResource{
		WorkspaceID: "w2", Label: intent.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: repo,
		RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	err := validateIntent(intent)
	if err == nil || !strings.Contains(err.Error(), "current path") {
		t.Fatalf("error = %v, want current-path rejection", err)
	}
}

func TestHerdrControlValidatesEmitterLaunchFields(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	valid := func() LaunchIntent {
		intent := testWorktreeIntent(repo, "425", 426, "telemetry")
		intent.Status = IntentRealized
		intent.Resource = RuntimeResource{
			WorkspaceID: "w2", Label: intent.WorkspaceLabel,
			PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: intent.WorktreePath,
			RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
		}
		intent.Launch = &LaunchCapsule{
			Nonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
			PendingReportedState: "working", PendingReportedSeq: 1,
			Agent: "claude", AgentName: "fanout-agent",
			Executable: "/opt/bin/claude", Args: []string{"prompt"},
			EnvFilePath: "/tmp/fanout-env.json", EnvNameCount: 1,
		}
		return intent
	}
	if err := validateIntent(valid()); err != nil {
		t.Fatal(err)
	}
	codex := valid()
	codex.Launch.Agent = "codex"
	codex.Launch.PendingReportedSeq = 0
	codex.Launch.CodexPlanStatusPath = filepath.Join(t.TempDir(), "status.json")
	if err := validateIntent(codex); err != nil {
		t.Fatalf("Codex Plan emitter fields: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*LaunchCapsule)
	}{
		{name: "pending without nonce", mutate: func(launch *LaunchCapsule) { launch.EmitterNonce = "" }},
		{name: "Claude pending without sequence", mutate: func(launch *LaunchCapsule) { launch.PendingReportedSeq = 0 }},
		{name: "Codex emitter without Plan mode", mutate: func(launch *LaunchCapsule) { launch.Agent = "codex" }},
		{name: "unsupported emitter", mutate: func(launch *LaunchCapsule) { launch.Agent = "opencode" }},
		{name: "synthetic pending", mutate: func(launch *LaunchCapsule) { launch.PendingReportedState = "running" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := valid()
			test.mutate(intent.Launch)
			if err := validateIntent(intent); err == nil {
				t.Fatal("validateIntent() accepted invalid emitter fields")
			}
		})
	}
}

func TestLoadLaunchJournalDiscardsLegacyClaudePendingTelemetry(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	intent := testWorktreeIntent(repo, "425", 426, "legacy-telemetry")
	intent.Status = IntentRealized
	intent.Resource = RuntimeResource{
		WorkspaceID: "w2", Label: intent.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: intent.WorktreePath,
		RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	session := backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "legacy-session",
	}
	intent.Launch = &LaunchCapsule{
		Nonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
		PendingReportedState: "working", PendingReportedSeq: 1, PendingAgentSession: &session,
		Agent: "claude", AgentName: "fanout-agent",
		Executable: "/opt/bin/claude", Args: []string{"prompt"},
		EnvFilePath: "/tmp/fanout-env.json", EnvNameCount: 1,
	}
	project, journal := lockLaunchJournalForTest(t, repo)
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(errors.Join(err, project.Unlock()))
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}

	path, err := LaunchJournalPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const sequenceField = `"pendingReportedSequence": 1,` + "\n"
	legacy := strings.Replace(string(contents), sequenceField, "", 1)
	if legacy == string(contents) {
		t.Fatal("saved journal lacks the pending sequence fixture")
	}
	if writeErr := os.WriteFile(path, []byte(legacy), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	loaded, err := LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, found := loaded.FindIntent(intent.ID)
	if !found || got.Launch.PendingReportedState != "" ||
		got.Launch.PendingReportedSeq != 0 || got.Launch.PendingAgentSession != nil {
		t.Fatalf("legacy pending telemetry = (%+v, %t), want discarded", got, found)
	}
}

func TestSaveSequencedClaudeIntentFencesLegacyFinalAndProvisionalEmitters(t *testing.T) {
	repo := newLaunchJournalRepo(t)
	legacyIntent := testClaudeEmitterIntent(t, repo, 426, "legacy", []string{"--settings", `{}`})
	legacyIntent.Launch.PendingReportedState = "working"
	legacyIntent.Launch.PendingReportedSeq = 1
	legacySession := backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "legacy-session",
	}
	legacyIntent.Launch.PendingAgentSession = &legacySession
	legacyPane := Pane{
		Parent: "425", IssueNum: 428, Backend: backend.Herdr, Agent: "claude",
		LaunchNonce: strings.Repeat("c", 32), EmitterNonce: strings.Repeat("d", 32),
		ReportedState: "blocked", StateRefinement: true,
		LaunchArgs: []string{"--settings", `{}`},
	}

	project, journal := lockLaunchJournalForTest(t, repo)
	project.Panes = []Pane{legacyPane}
	if err := project.Save(); err != nil {
		t.Fatal(errors.Join(err, project.Unlock()))
	}
	journal.UpsertIntent(legacyIntent)
	if err := journal.Save(); err != nil {
		t.Fatal(errors.Join(err, project.Unlock()))
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}

	project, journal = lockLaunchJournalForTest(t, repo)
	sequenced := testClaudeEmitterIntent(t, repo, 427, "sequenced", []string{
		"--settings", `{"command":"` + telemetry.SequenceCommand + `"}`,
	})
	journal.UpsertIntent(sequenced)
	if err := journal.Save(); err != nil {
		t.Fatal(errors.Join(err, project.Unlock()))
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}

	storedState, err := LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	gotPane, found := storedState.Find("425", 428)
	if !found || gotPane.ReportedState != "" || gotPane.StateRefinement ||
		gotPane.EmitterNonce == legacyPane.EmitterNonce || !telemetry.ValidNonce(gotPane.EmitterNonce) {
		t.Fatalf("legacy final emitter = (%+v, %t), want persisted fence", gotPane, found)
	}
	storedJournal, err := LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	gotIntent, found := storedJournal.FindIntent(legacyIntent.ID)
	if !found || gotIntent.Launch.PendingReportedState != "" || gotIntent.Launch.PendingReportedSeq != 0 ||
		gotIntent.Launch.PendingAgentSession != nil ||
		gotIntent.Launch.EmitterNonce == legacyIntent.Launch.EmitterNonce ||
		!telemetry.ValidNonce(gotIntent.Launch.EmitterNonce) {
		t.Fatalf("legacy provisional emitter = (%+v, %t), want fenced", gotIntent, found)
	}
	gotSequenced, found := storedJournal.FindIntent(sequenced.ID)
	if !found || gotSequenced.Launch.EmitterNonce != sequenced.Launch.EmitterNonce {
		t.Fatalf("sequenced emitter = (%+v, %t), want unchanged", gotSequenced, found)
	}
}

func testClaudeEmitterIntent(
	t *testing.T,
	repo string,
	issue int,
	slug string,
	args []string,
) LaunchIntent {
	t.Helper()
	intent := testWorktreeIntent(repo, "425", issue, slug)
	intent.Status = IntentRealized
	intent.Resource = RuntimeResource{
		WorkspaceID: "w-" + slug, Label: intent.WorkspaceLabel,
		PaneID: "w-" + slug + ":p1", TerminalID: "term-" + slug,
		CurrentPath: intent.WorktreePath, RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	intent.Launch = &LaunchCapsule{
		Nonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
		Agent: "claude", AgentName: "fanout-agent", Executable: "/opt/bin/claude", Args: args,
		EnvFilePath: "/tmp/fanout-env.json", EnvNameCount: 1,
	}
	return intent
}

func testCoordinatorIntent(repo, parent string) LaunchIntent {
	ownerProjectRoot, err := IntentOwnerProjectRoot(parent, repo)
	if err != nil {
		panic(err)
	}
	id, err := CoordinatorIntentID(parent, ownerProjectRoot, 0)
	if err != nil {
		panic(err)
	}
	return LaunchIntent{
		ID: id, Kind: IntentCoordinator, Status: IntentPlanned,
		Parent:           parentref.Canon(strings.TrimSpace(parent)),
		RuntimeParent:    parentref.Canon(strings.TrimSpace(parent)),
		OwnerProjectRoot: ownerProjectRoot,
		WorktreePath:     repo,
		WorkspaceLabel:   "fanout-coordinator-token", Session: "fanout-test",
		SocketPath:    "/private/tmp/fanout-test/herdr.sock",
		ExpiresUnixMS: 2000000000000,
	}
}

func testServerIntent(kind LaunchIntentKind) LaunchIntent {
	id, err := ServerIntentID(kind)
	if err != nil {
		panic(err)
	}
	return LaunchIntent{
		ID: id, Kind: kind, Status: IntentPlanned,
		Server: &RuntimeServerIdentity{
			GitCommonDir: "/repo/.git", RuntimeDir: "/tmp/fanout-herdr",
			Session: "fanout-owned", SocketPath: "/tmp/fanout-herdr/herdr.sock",
			ClientSocketPath: "/tmp/fanout-herdr/herdr-client.sock",
			OwnerNonce:       strings.Repeat("a", 64), SupervisorPID: 42,
			SupervisorStartToken: strings.Repeat("b", 64), ServerPID: 43,
			BinaryPath: "/usr/local/bin/herdr", BinarySHA256: strings.Repeat("c", 64),
			BinaryVersion: "0.7.5", LauncherPath: "/usr/local/bin/fanout",
			LauncherSHA256: strings.Repeat("d", 64),
		},
	}
}

func testResumeIntent() LaunchIntent {
	ref := &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "019f-session",
	}
	id, err := ResumeIntentID("fanout-owned", "/runtime/herdr.sock", "w1", "w1:p1")
	if err != nil {
		panic(err)
	}
	return LaunchIntent{
		ID: id, Kind: IntentResume, Status: IntentRealized,
		Parent: "524", RuntimeParent: "524", IssueNum: 532,
		WorktreePath: "/repo/worktree", WorkspaceLabel: "fanout-workspace-token",
		Resource: RuntimeResource{
			WorkspaceID: "w1", Label: "fanout-workspace-token", PaneID: "w1:p1",
			TerminalID: "term-new", CurrentPath: "/repo/worktree",
			RepoKey: "/repo/.git", RepoRoot: "/repo",
		},
		Session: "fanout-owned", SocketPath: "/runtime/herdr.sock",
		ExpiresUnixMS: 2000000000000, ResumeAgentSession: ref,
		Launch: &LaunchCapsule{
			Nonce: strings.Repeat("b", 32), Agent: "codex",
			Executable: "/opt/codex", Args: []string{"resume", ref.Value},
			EnvFilePath: "/runtime/env.json", EnvNameCount: 3,
		},
	}
}

func lockLaunchJournalForTest(t *testing.T, repo string) (*LockedStore, *LockedLaunchJournal) {
	t.Helper()
	project, err := LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	view, err := project.LaunchJournal(repo)
	if err != nil {
		_ = project.Unlock()
		t.Fatal(err)
	}
	return project, view
}

func testWorktreeIntent(repo, parent string, issue int, slug string) LaunchIntent {
	ownerProjectRoot, err := IntentOwnerProjectRoot(parent, repo)
	if err != nil {
		panic(err)
	}
	id, err := WorktreeIntentID(parent, ownerProjectRoot, issue, "")
	if err != nil {
		panic(err)
	}
	return LaunchIntent{
		ID: id, Kind: IntentWorktree, Status: IntentPlanned,
		Parent:           parentref.Canon(strings.TrimSpace(parent)),
		RuntimeParent:    parentref.Canon(strings.TrimSpace(parent)),
		OwnerProjectRoot: ownerProjectRoot,
		IssueNum:         issue,
		Slug:             slug, BranchName: "fanout/" + slug,
		FullBranchRef: "refs/heads/fanout/" + slug,
		BaseBranch:    "main", BaseSHA: strings.Repeat("1", 40), ExpectedHead: strings.Repeat("1", 40),
		WorktreePath:   filepath.Join(repo, ".fanout", "worktrees", slug),
		WorkspaceLabel: "fanout-worktree-" + slug,
		Coordinator:    testCoordinatorResource(repo),
		Session:        "fanout-test", SocketPath: "/private/tmp/fanout-test/herdr.sock",
		ExpiresUnixMS: 2000000000000,
	}
}

func testCoordinatorResource(repo string) RuntimeResource {
	return RuntimeResource{
		WorkspaceID: "w1", Label: "fanout-coordinator-token",
		PaneID: "w1:p1", TerminalID: "term-1", CurrentPath: repo,
	}
}

func newLaunchJournalRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runLaunchJournalGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLaunchJournalGit(t, repo, "add", "README.md")
	runLaunchJournalGit(t, repo, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "init")
	return repo
}

func runLaunchJournalGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
