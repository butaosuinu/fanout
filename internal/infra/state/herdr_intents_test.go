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
)

func TestHerdrIntentsAreSharedAcrossLinkedWorktrees(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	runHerdrIntentsGit(t, repo, "worktree", "add", "-b", "sibling", sibling, "HEAD")

	project, view := lockHerdrIntentsForTest(t, repo)
	intent := testHerdrCoordinatorIntent(repo, "0425")
	view.UpsertIntent(intent)
	if err := view.Save(); err != nil {
		t.Fatal(err)
	}
	if err := project.Unlock(); err != nil {
		t.Fatal(err)
	}

	fromSibling, err := LoadHerdrIntents(sibling)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := fromSibling.FindIntent(intent.ID)
	if !ok || got.Parent != "425" {
		t.Fatalf("shared intent = (%+v, %t), want saved coordinator", got, ok)
	}
	repoPath, err := HerdrIntentsPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	siblingPath, err := HerdrIntentsPath(sibling)
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
	repo := newHerdrIntentsRepo(t)
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
	repo := newHerdrIntentsRepo(t)
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
	intentsPath, err := HerdrIntentsPath(repo)
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

func TestHerdrIntentsRejectExistingJournalWithoutSchemaVersion(t *testing.T) {
	for _, contents := range []string{`{}`, `{"schemaVersion":0}`} {
		t.Run(contents, func(t *testing.T) {
			repo := newHerdrIntentsRepo(t)
			path, err := HerdrIntentsPath(repo)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadHerdrIntents(repo); err == nil ||
				!strings.Contains(err.Error(), "unsupported Herdr intents schema version 0") {
				t.Fatalf("schema-less journal error = %v", err)
			}
		})
	}
}

func TestHerdrIntentsRejectFutureSchemaVersion(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	path, err := HerdrIntentsPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHerdrIntents(repo); err == nil ||
		!strings.Contains(err.Error(), "unsupported Herdr intents schema version 2") {
		t.Fatalf("future schema error = %v", err)
	}
}

func TestProjectStateLockSerializesHerdrControlWriter(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	project, err := LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	controlPath, err := HerdrIntentsPath(repo)
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
	repo := newHerdrIntentsRepo(t)
	store := emptyHerdrIntents()
	statuses := []HerdrIntentStatus{
		HerdrIntentPlanned,
		HerdrIntentIssued,
		HerdrIntentRealized,
		HerdrIntentManualCleanupRequired,
	}
	for i, status := range statuses {
		intent := testHerdrCoordinatorIntent(repo, strconv.Itoa(500+i))
		intent.Status = status
		if status == HerdrIntentRealized {
			intent.Resource = testHerdrCoordinatorResource(repo)
		}
		if status == HerdrIntentManualCleanupRequired {
			intent.Failure = "response loss"
		}
		store.Intents = append(store.Intents, intent)
	}
	if err := validateHerdrIntents(store); err != nil {
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

func TestHerdrPlanBindingsAreOwnerWorktreeLocal(t *testing.T) {
	first := testHerdrCoordinatorIntent("/repo/one", "plan:demo")
	second := testHerdrCoordinatorIntent("/repo/two", "plan:demo")
	if first.ID == second.ID {
		t.Fatalf("plan intent IDs collide across owner roots: %s", first.ID)
	}
	intents := emptyHerdrIntents()
	intents.Intents = []HerdrIntent{first, second}
	if err := validateHerdrIntents(intents); err != nil {
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
	intent := testHerdrCoordinatorIntent("/repo/one", "plan:demo")
	intent.RuntimeParent = "425"
	intent.ID, _ = HerdrCoordinatorIntentID(intent.RuntimeParent, "", 0)
	store := emptyHerdrIntents()
	store.Intents = append(store.Intents, intent)

	got := store.ProvisionalBindings("/repo/two")
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "425", Backend: backend.Herdr}) {
		t.Fatalf("issue-sourced plan bindings = %#v", got)
	}
}

func TestHerdrSyntheticBindingsProjectWatcherIssuesAndKeepResolvedParents(t *testing.T) {
	store := emptyHerdrIntents()
	store.Intents = []HerdrIntent{
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
	repo := newHerdrIntentsRepo(t)
	project, view := lockHerdrIntentsForTest(t, repo)
	t.Cleanup(func() {
		if err := project.Unlock(); err != nil {
			t.Errorf("unlock: %v", err)
		}
	})
	first := testHerdrWorktreeIntent(repo, "425", 426, "first")
	second := testHerdrWorktreeIntent(repo, "425", 427, "second")
	second.BranchName = first.BranchName
	second.FullBranchRef = first.FullBranchRef
	view.Intents = []HerdrIntent{first, second}
	if err := view.Save(); err == nil || !strings.Contains(err.Error(), "reserve the same branch:") {
		t.Fatalf("duplicate branch save error = %v", err)
	}

	second.BranchName = "fanout/second"
	second.FullBranchRef = "refs/heads/" + second.BranchName
	second.WorktreePath = first.WorktreePath
	view.Intents = []HerdrIntent{first, second}
	if err := view.Save(); err == nil || !strings.Contains(err.Error(), "reserve the same path:") {
		t.Fatalf("duplicate path save error = %v", err)
	}
}

func TestHerdrIntentIDsUseTmuxIssueAndTaskKeys(t *testing.T) {
	issue, err := HerdrWorktreeIntentID("00425", "", 426, "")
	if err != nil {
		t.Fatal(err)
	}
	alias, err := HerdrWorktreeIntentID("425", "", 426, "")
	if err != nil {
		t.Fatal(err)
	}
	if issue != alias {
		t.Fatalf("numeric parent aliases differ: %q != %q", issue, alias)
	}
	task, err := HerdrWorktreeIntentID("plan:demo", "/repo/one", 0, "api:client")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task, "plan:demo") || !strings.Contains(task, "api:client") {
		t.Fatalf("task id = %q, want length-prefixed plan/task identity", task)
	}
	if _, mixedErr := HerdrWorktreeIntentID("plan:demo", "/repo/one", 1, "task"); mixedErr == nil {
		t.Fatal("issue and task identity unexpectedly accepted together")
	}
	manual, err := HerdrWorktreeIntentID("@manual", "/repo/one", -1, "")
	if err != nil {
		t.Fatal(err)
	}
	otherManual, err := HerdrWorktreeIntentID("@manual", "/repo/two", -1, "")
	if err != nil {
		t.Fatal(err)
	}
	if manual == otherManual {
		t.Fatalf("manual worktree IDs collide across owner roots: %q", manual)
	}
	if _, err := HerdrWorktreeIntentID("425", "", -1, ""); err == nil {
		t.Fatal("negative issue number was accepted for a non-manual parent")
	}
}

func TestHerdrRollbackIntentKeepsIndependentMutationRecord(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	original := testHerdrWorktreeIntent(repo, "425", 426, "rollback")
	original.Status = HerdrIntentRealized
	original.Resource = HerdrResource{
		WorkspaceID: "w2", Label: original.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: original.WorktreePath,
		RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	rollback := original
	rollback.ID, _ = HerdrRollbackIntentID(original.ID)
	rollback.Kind = HerdrIntentRollback
	rollback.Status = HerdrIntentPlanned
	store := emptyHerdrIntents()
	store.Intents = []HerdrIntent{original, rollback}
	if err := validateHerdrIntents(store); err != nil {
		t.Fatal(err)
	}
	rollback.Launch = &HerdrLaunch{}
	if err := validateHerdrIntent(rollback); err == nil {
		t.Fatal("rollback intent accepted an agent launch capsule")
	}
}

func TestHerdrLaunchRequiresAbsoluteTeamPaths(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	intent := testHerdrWorktreeIntent(repo, "425", 426, "team-status")
	intent.Status = HerdrIntentRealized
	intent.Resource = HerdrResource{
		WorkspaceID: "w2", Label: intent.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: intent.WorktreePath,
		RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	intent.Launch = &HerdrLaunch{
		Nonce: strings.Repeat("a", 32), Agent: "codex",
		AgentName: "fanout-0123456789abcdef01234567", Executable: "/bin/fanout",
		TeamDBPath: "relative-team.db", EnvFilePath: "/tmp/env", EnvNameCount: 1,
	}
	if err := validateHerdrIntent(intent); err == nil || !strings.Contains(err.Error(), "launch fields are incomplete") {
		t.Fatalf("relative team DB path error = %v", err)
	}
	intent.Launch.TeamDBPath = "/tmp/team.db"
	intent.Launch.CodexTeamStatusPath = "relative-status.json"
	if err := validateHerdrIntent(intent); err == nil || !strings.Contains(err.Error(), "launch fields are incomplete") {
		t.Fatalf("relative team status path error = %v", err)
	}
	intent.Launch.CodexTeamStatusPath = "/tmp/team-status.json"
	if err := validateHerdrIntent(intent); err != nil {
		t.Fatalf("absolute team status path error = %v", err)
	}
}

func TestHerdrCoordinatorLaunchAllowsShellAndRejectsPartialAgentIdentity(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	intent := testHerdrCoordinatorIntent(repo, "425")
	intent.Status = HerdrIntentIssued
	intent.Launch = &HerdrLaunch{
		Nonce:        strings.Repeat("a", 32),
		Executable:   "/bin/zsh",
		EnvFilePath:  "/private/tmp/fanout-test/env.json",
		EnvNameCount: 1,
	}
	if err := validateHerdrIntent(intent); err != nil {
		t.Fatalf("issued coordinator shell launch = %v, want success", err)
	}

	intent.Launch.Agent = "codex"
	if err := validateHerdrIntent(intent); err == nil ||
		!strings.Contains(err.Error(), "launch agent identity is partial") {
		t.Fatalf("partial agent identity error = %v", err)
	}
}

func TestHerdrCoordinatorIntentIDsUseSyntheticIssueNumbers(t *testing.T) {
	firstManual, err := HerdrCoordinatorIntentID("@manual", "/repo/one", -1)
	if err != nil {
		t.Fatal(err)
	}
	secondManual, err := HerdrCoordinatorIntentID("@manual", "/repo/one", -2)
	if err != nil {
		t.Fatal(err)
	}
	otherOwner, err := HerdrCoordinatorIntentID("@manual", "/repo/two", -1)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := HerdrCoordinatorIntentID("@watch", "", 425)
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
	if _, err := HerdrCoordinatorIntentID("@manual", "/repo/one", 0); err == nil {
		t.Fatal("manual coordinator without synthetic issue number was accepted")
	}
	if _, err := HerdrCoordinatorIntentID("@watch", "", 0); err == nil {
		t.Fatal("watch coordinator without issue number was accepted")
	}
	if _, err := HerdrCoordinatorIntentID("425", "", 426); err == nil {
		t.Fatal("numeric coordinator with synthetic issue number was accepted")
	}
}

func TestHerdrControlRejectsIncompleteRealizedIntent(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	intent := testHerdrCoordinatorIntent(repo, "425")
	intent.Status = HerdrIntentRealized
	store := emptyHerdrIntents()
	store.Intents = append(store.Intents, intent)
	if err := validateHerdrIntents(store); err == nil || !strings.Contains(err.Error(), "resource is incomplete") {
		t.Fatalf("realized intent validation error = %v", err)
	}
}

func TestHerdrControlAcceptsSHA256ObjectIDs(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	intent := testHerdrWorktreeIntent(repo, "425", 426, "sha256")
	intent.BaseSHA = strings.Repeat("1", 64)
	intent.ExpectedHead = strings.Repeat("2", 64)
	store := emptyHerdrIntents()
	store.Intents = append(store.Intents, intent)
	if err := validateHerdrIntents(store); err != nil {
		t.Fatal(err)
	}
}

func TestHerdrControlIssuedWorktreeMayRetainRealizedResourceForOpenRecovery(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	intent := testHerdrWorktreeIntent(repo, "425", 426, "reopen")
	intent.Status = HerdrIntentIssued
	intent.Resource = HerdrResource{
		WorkspaceID: "w2", Label: intent.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2",
		CurrentPath: intent.WorktreePath,
		RepoKey:     filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	if err := validateHerdrIntent(intent); err != nil {
		t.Fatalf("issued worktree open intent: %v", err)
	}

	coordinator := testHerdrCoordinatorIntent(repo, "425")
	coordinator.Status = HerdrIntentIssued
	coordinator.Resource = testHerdrCoordinatorResource(repo)
	if err := validateHerdrIntent(coordinator); err == nil ||
		!strings.Contains(err.Error(), "resource before realization") {
		t.Fatalf("issued coordinator resource error = %v", err)
	}
}

func TestHerdrControlRejectsResourceCurrentPathMismatch(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	intent := testHerdrWorktreeIntent(repo, "425", 426, "mismatch")
	intent.Status = HerdrIntentRealized
	intent.Resource = HerdrResource{
		WorkspaceID: "w2", Label: intent.WorkspaceLabel,
		PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: repo,
		RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
	}
	err := validateHerdrIntent(intent)
	if err == nil || !strings.Contains(err.Error(), "current path") {
		t.Fatalf("error = %v, want current-path rejection", err)
	}
}

func TestHerdrControlValidatesEmitterLaunchFields(t *testing.T) {
	repo := newHerdrIntentsRepo(t)
	valid := func() HerdrIntent {
		intent := testHerdrWorktreeIntent(repo, "425", 426, "telemetry")
		intent.Status = HerdrIntentRealized
		intent.Resource = HerdrResource{
			WorkspaceID: "w2", Label: intent.WorkspaceLabel,
			PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: intent.WorktreePath,
			RepoKey: filepath.Join(repo, ".git"), RepoRoot: repo,
		}
		intent.Launch = &HerdrLaunch{
			Nonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
			PendingReportedState: "working", Agent: "claude", AgentName: "fanout-agent",
			Executable: "/opt/bin/claude", Args: []string{"prompt"},
			EnvFilePath: "/tmp/fanout-env.json", EnvNameCount: 1,
		}
		return intent
	}
	if err := validateHerdrIntent(valid()); err != nil {
		t.Fatal(err)
	}
	codex := valid()
	codex.Launch.Agent = "codex"
	codex.Launch.CodexPlanStatusPath = filepath.Join(t.TempDir(), "status.json")
	if err := validateHerdrIntent(codex); err != nil {
		t.Fatalf("Codex Plan emitter fields: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*HerdrLaunch)
	}{
		{name: "pending without nonce", mutate: func(launch *HerdrLaunch) { launch.EmitterNonce = "" }},
		{name: "Codex emitter without Plan mode", mutate: func(launch *HerdrLaunch) { launch.Agent = "codex" }},
		{name: "unsupported emitter", mutate: func(launch *HerdrLaunch) { launch.Agent = "opencode" }},
		{name: "synthetic pending", mutate: func(launch *HerdrLaunch) { launch.PendingReportedState = "running" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := valid()
			test.mutate(intent.Launch)
			if err := validateHerdrIntent(intent); err == nil {
				t.Fatal("validateHerdrIntent() accepted invalid emitter fields")
			}
		})
	}
}

func testHerdrCoordinatorIntent(repo, parent string) HerdrIntent {
	ownerProjectRoot, err := HerdrOwnerProjectRoot(parent, repo)
	if err != nil {
		panic(err)
	}
	id, err := HerdrCoordinatorIntentID(parent, ownerProjectRoot, 0)
	if err != nil {
		panic(err)
	}
	return HerdrIntent{
		ID: id, Kind: HerdrIntentCoordinator, Status: HerdrIntentPlanned,
		Parent:           parentref.Canon(strings.TrimSpace(parent)),
		RuntimeParent:    parentref.Canon(strings.TrimSpace(parent)),
		OwnerProjectRoot: ownerProjectRoot,
		WorktreePath:     repo,
		WorkspaceLabel:   "fanout-coordinator-token", Session: "fanout-test",
		SocketPath:    "/private/tmp/fanout-test/herdr.sock",
		ExpiresUnixMS: 2000000000000,
	}
}

func lockHerdrIntentsForTest(t *testing.T, repo string) (*LockedStore, *LockedHerdrIntents) {
	t.Helper()
	project, err := LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	view, err := project.HerdrIntents(repo)
	if err != nil {
		_ = project.Unlock()
		t.Fatal(err)
	}
	return project, view
}

func testHerdrWorktreeIntent(repo, parent string, issue int, slug string) HerdrIntent {
	ownerProjectRoot, err := HerdrOwnerProjectRoot(parent, repo)
	if err != nil {
		panic(err)
	}
	id, err := HerdrWorktreeIntentID(parent, ownerProjectRoot, issue, "")
	if err != nil {
		panic(err)
	}
	return HerdrIntent{
		ID: id, Kind: HerdrIntentWorktree, Status: HerdrIntentPlanned,
		Parent:           parentref.Canon(strings.TrimSpace(parent)),
		RuntimeParent:    parentref.Canon(strings.TrimSpace(parent)),
		OwnerProjectRoot: ownerProjectRoot,
		IssueNum:         issue,
		Slug:             slug, BranchName: "fanout/" + slug,
		FullBranchRef: "refs/heads/fanout/" + slug,
		BaseBranch:    "main", BaseSHA: strings.Repeat("1", 40), ExpectedHead: strings.Repeat("1", 40),
		WorktreePath:   filepath.Join(repo, ".fanout", "worktrees", slug),
		WorkspaceLabel: "fanout-worktree-" + slug,
		Coordinator:    testHerdrCoordinatorResource(repo),
		Session:        "fanout-test", SocketPath: "/private/tmp/fanout-test/herdr.sock",
		ExpiresUnixMS: 2000000000000,
	}
}

func testHerdrCoordinatorResource(repo string) HerdrResource {
	return HerdrResource{
		WorkspaceID: "w1", Label: "fanout-coordinator-token",
		PaneID: "w1:p1", TerminalID: "term-1", CurrentPath: repo,
	}
}

func newHerdrIntentsRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runHerdrIntentsGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrIntentsGit(t, repo, "add", "README.md")
	runHerdrIntentsGit(t, repo, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "init")
	return repo
}

func runHerdrIntentsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
