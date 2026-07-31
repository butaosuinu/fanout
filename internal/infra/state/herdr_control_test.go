package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/parentref"
)

func TestHerdrControlIsSharedAcrossLinkedWorktrees(t *testing.T) {
	repo := newHerdrControlRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	runHerdrControlGit(t, repo, "worktree", "add", "-b", "sibling", sibling, "HEAD")

	locked, lockErr := LockHerdrControl(repo)
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	intent := testHerdrCoordinatorIntent(repo, "0425")
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}

	fromSibling, err := LoadHerdrControl(sibling)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := fromSibling.FindIntent(intent.ID)
	if !ok || got.Parent != "425" {
		t.Fatalf("shared intent = (%+v, %t), want saved coordinator", got, ok)
	}
	repoPath, err := HerdrControlPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	siblingPath, err := HerdrControlPath(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if repoPath != siblingPath {
		t.Fatalf("control paths differ:\nrepo: %s\nsibling: %s", repoPath, siblingPath)
	}
}

func TestHerdrControlRejectsRegistryFromDifferentCommonDirectory(t *testing.T) {
	first := newHerdrControlRepo(t)
	locked, err := LockHerdrControl(first)
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	firstPath, err := HerdrControlPath(first)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}

	second := newHerdrControlRepo(t)
	secondPath, err := HerdrControlPath(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(secondPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHerdrControl(second); err == nil ||
		!strings.Contains(err.Error(), "different git common directory") {
		t.Fatalf("copied registry error = %v", err)
	}
}

func TestNormalizeHerdrControlStatDeviceSupportsDarwinAndLinuxWidths(t *testing.T) {
	if got := normalizeHerdrControlStatDevice(int32(42)); got != 42 {
		t.Fatalf("normalizeHerdrControlStatDevice(int32) = %d, want 42", got)
	}
	if got := normalizeHerdrControlStatDevice(uint64(81)); got != 81 {
		t.Fatalf("normalizeHerdrControlStatDevice(uint64) = %d, want 81", got)
	}
}

func TestHerdrControlRejectsNonPrivateNamespace(t *testing.T) {
	validRegistry := []byte(`{"schemaVersion":1,"rows":[],"intents":[]}`)
	t.Run("common directory mode", func(t *testing.T) {
		repo := newHerdrControlRepo(t)
		commonDir := filepath.Join(repo, ".git")
		if err := os.Chmod(commonDir, 0o775); err != nil {
			t.Fatal(err)
		}
		if _, err := HerdrControlPath(repo); err == nil ||
			!strings.Contains(err.Error(), "writable by another uid") {
			t.Fatalf("writable common directory error = %v", err)
		}
	})
	t.Run("control directory mode", func(t *testing.T) {
		repo := newHerdrControlRepo(t)
		path, err := HerdrControlPath(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadHerdrControl(repo); err == nil ||
			!strings.Contains(err.Error(), "owner-only real directory") {
			t.Fatalf("permissive control directory error = %v", err)
		}
	})
	t.Run("control directory symlink", func(t *testing.T) {
		repo := newHerdrControlRepo(t)
		path, err := HerdrControlPath(repo)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "fanout")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadHerdrControl(repo); err == nil ||
			!strings.Contains(err.Error(), "owner-only real directory") {
			t.Fatalf("symlinked control directory error = %v", err)
		}
	})
	t.Run("registry mode", func(t *testing.T) {
		repo := newHerdrControlRepo(t)
		path, err := HerdrControlPath(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, validRegistry, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadHerdrControl(repo); err == nil ||
			!strings.Contains(err.Error(), "owner-only regular file") {
			t.Fatalf("permissive registry error = %v", err)
		}
	})
	t.Run("registry symlink", func(t *testing.T) {
		repo := newHerdrControlRepo(t)
		path, err := HerdrControlPath(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "registry.json")
		if err := os.WriteFile(target, validRegistry, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadHerdrControl(repo); err == nil {
			t.Fatal("symlinked registry was accepted")
		}
	})
	t.Run("lock mode", func(t *testing.T) {
		repo := newHerdrControlRepo(t)
		path, err := HerdrControlPath(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path+".lock", 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LockHerdrControl(repo); err == nil ||
			!strings.Contains(err.Error(), "owner-only regular file") {
			t.Fatalf("permissive lock error = %v", err)
		}
	})
}

func TestHerdrControlRejectsExistingRegistryWithoutSchemaVersion(t *testing.T) {
	for _, contents := range []string{`{}`, `{"schemaVersion":0}`} {
		t.Run(contents, func(t *testing.T) {
			repo := newHerdrControlRepo(t)
			path, err := HerdrControlPath(repo)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadHerdrControl(repo); err == nil ||
				!strings.Contains(err.Error(), "unsupported Herdr control schema version 0") {
				t.Fatalf("schema-less registry error = %v", err)
			}
		})
	}
}

func TestProjectStateLockSerializesHerdrControlWriter(t *testing.T) {
	repo := newHerdrControlRepo(t)
	project, err := LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	controlPath, err := HerdrControlPath(repo)
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

func TestHerdrControlBindingsIncludeRowsAndEveryIntentStatus(t *testing.T) {
	repo := newHerdrControlRepo(t)
	store := testEmptyHerdrControl()
	store.Rows = append(store.Rows, testHerdrRow("425"))
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
	if err := validateHerdrControl(store); err != nil {
		t.Fatal(err)
	}

	rows := store.RowBindings(repo)
	if len(rows) != 1 || rows[0] != (backend.Binding{Parent: "425", Backend: backend.Herdr}) {
		t.Fatalf("row bindings = %#v", rows)
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
	intents := testEmptyHerdrControl()
	intents.Intents = []HerdrIntent{first, second}
	if err := validateHerdrControl(intents); err != nil {
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

	toRow := func(intent HerdrIntent) HerdrRow {
		return HerdrRow{
			ID: intent.ID, Kind: intent.Kind, Parent: intent.Parent,
			RuntimeParent:    intent.RuntimeParent,
			OwnerProjectRoot: intent.OwnerProjectRoot, Backend: intent.Backend,
			WorktreePath:  intent.WorktreePath,
			BranchExisted: intent.BranchExisted, BranchCreated: intent.BranchCreated,
			Resource: testHerdrCoordinatorResource(intent.WorktreePath),
			Session:  intent.Session, SocketPath: intent.SocketPath,
		}
	}
	rows := testEmptyHerdrControl()
	rows.Rows = []HerdrRow{toRow(first), toRow(second)}
	if err := validateHerdrControl(rows); err != nil {
		t.Fatal(err)
	}
	if got := rows.RowBindings("/repo/one"); len(got) != 1 ||
		got[0].Parent != "plan:demo" {
		t.Fatalf("first plan row bindings = %#v", got)
	}
	if got := rows.RowBindings("/repo/two"); len(got) != 1 ||
		got[0].Parent != "plan:demo" {
		t.Fatalf("second plan row bindings = %#v", got)
	}
}

func TestHerdrIssueSourcedPlanBindingsUseResolvedParentAcrossWorktrees(t *testing.T) {
	intent := testHerdrCoordinatorIntent("/repo/one", "plan:demo")
	intent.RuntimeParent = "425"
	intent.ID, _ = HerdrCoordinatorIntentID(intent.RuntimeParent, "")
	store := testEmptyHerdrControl()
	store.Intents = append(store.Intents, intent)

	got := store.ProvisionalBindings("/repo/two")
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "425", Backend: backend.Herdr}) {
		t.Fatalf("issue-sourced plan bindings = %#v", got)
	}
}

func TestHerdrControlRejectsRowIntentReservationConflict(t *testing.T) {
	repo := newHerdrControlRepo(t)
	store := testEmptyHerdrControl()
	row := testHerdrRow("425")
	intent := testHerdrWorktreeIntent(repo, "500", 501, "other")
	intent.BranchName = row.BranchName
	intent.FullBranchRef = row.FullBranchRef
	store.Rows = append(store.Rows, row)
	store.Intents = append(store.Intents, intent)
	if err := validateHerdrControl(store); err == nil ||
		!strings.Contains(err.Error(), "reserve the same branch:") {
		t.Fatalf("row/intent reservation error = %v", err)
	}
}

func TestHerdrControlRejectsDuplicateBranchAndPathReservations(t *testing.T) {
	repo := newHerdrControlRepo(t)
	locked, err := LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := locked.Unlock(); err != nil {
			t.Errorf("unlock: %v", err)
		}
	})
	first := testHerdrWorktreeIntent(repo, "425", 426, "first")
	second := testHerdrWorktreeIntent(repo, "425", 427, "second")
	second.BranchName = first.BranchName
	second.FullBranchRef = first.FullBranchRef
	locked.Intents = []HerdrIntent{first, second}
	if err := locked.Save(); err == nil || !strings.Contains(err.Error(), "reserve the same branch:") {
		t.Fatalf("duplicate branch save error = %v", err)
	}

	second.BranchName = "fanout/second"
	second.FullBranchRef = "refs/heads/" + second.BranchName
	second.WorktreePath = first.WorktreePath
	locked.Intents = []HerdrIntent{first, second}
	if err := locked.Save(); err == nil || !strings.Contains(err.Error(), "reserve the same path:") {
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
	if _, err := HerdrWorktreeIntentID("plan:demo", "/repo/one", 1, "task"); err == nil {
		t.Fatal("issue and task identity unexpectedly accepted together")
	}
}

func TestHerdrControlRejectsIncompleteRealizedIntent(t *testing.T) {
	repo := newHerdrControlRepo(t)
	intent := testHerdrCoordinatorIntent(repo, "425")
	intent.Status = HerdrIntentRealized
	store := testEmptyHerdrControl()
	store.Intents = append(store.Intents, intent)
	if err := validateHerdrControl(store); err == nil || !strings.Contains(err.Error(), "resource is incomplete") {
		t.Fatalf("realized intent validation error = %v", err)
	}
}

func TestHerdrControlAcceptsSHA256ObjectIDs(t *testing.T) {
	repo := newHerdrControlRepo(t)
	intent := testHerdrWorktreeIntent(repo, "425", 426, "sha256")
	intent.BaseSHA = strings.Repeat("1", 64)
	intent.ExpectedHead = strings.Repeat("2", 64)
	store := testEmptyHerdrControl()
	store.Intents = append(store.Intents, intent)
	if err := validateHerdrControl(store); err != nil {
		t.Fatal(err)
	}
}

func TestHerdrControlRowsPreserveBranchOwnership(t *testing.T) {
	created := testHerdrRow("425")
	if err := validateHerdrRow(created); err != nil {
		t.Fatal(err)
	}
	existing := created
	existing.BranchCreated = false
	existing.BranchExisted = true
	if err := validateHerdrRow(existing); err != nil {
		t.Fatal(err)
	}
	missing := created
	missing.BranchCreated = false
	if err := validateHerdrRow(missing); err == nil ||
		!strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("missing branch ownership error = %v", err)
	}
	contradictory := created
	contradictory.BranchExisted = true
	if err := validateHerdrRow(contradictory); err == nil ||
		!strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("contradictory branch ownership error = %v", err)
	}
}

func TestHerdrControlIssuedWorktreeMayRetainRealizedResourceForOpenRecovery(t *testing.T) {
	repo := newHerdrControlRepo(t)
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

func testHerdrCoordinatorIntent(repo, parent string) HerdrIntent {
	ownerProjectRoot, err := HerdrOwnerProjectRoot(parent, repo)
	if err != nil {
		panic(err)
	}
	id, err := HerdrCoordinatorIntentID(parent, ownerProjectRoot)
	if err != nil {
		panic(err)
	}
	return HerdrIntent{
		ID: id, Kind: HerdrIntentCoordinator, Status: HerdrIntentPlanned,
		Parent:           parentref.Canon(strings.TrimSpace(parent)),
		RuntimeParent:    parentref.Canon(strings.TrimSpace(parent)),
		OwnerProjectRoot: ownerProjectRoot,
		Backend:          backend.Herdr, WorktreePath: repo,
		WorkspaceLabel: "fanout-coordinator-token", Session: "fanout-test",
		SocketPath: "/private/tmp/fanout-test/herdr.sock",
		TimeoutMS:  300000, ExpiresUnixMS: 2000000000000,
	}
}

func testEmptyHerdrControl() HerdrControlStore {
	return emptyHerdrControl(herdrControlCommonIdentity{
		path: "/repo/.git", device: 1, inode: 1,
	})
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
		IssueNum:         issue, Backend: backend.Herdr,
		Slug: slug, BranchName: "fanout/" + slug,
		FullBranchRef: "refs/heads/fanout/" + slug,
		BaseBranch:    "main", BaseSHA: strings.Repeat("1", 40), ExpectedHead: strings.Repeat("1", 40),
		WorktreePath:   filepath.Join(repo, ".fanout", "worktrees", slug),
		WorkspaceLabel: "fanout-worktree-" + slug,
		Coordinator:    testHerdrCoordinatorResource(repo),
		Session:        "fanout-test", SocketPath: "/private/tmp/fanout-test/herdr.sock",
		TimeoutMS: 300000, ExpiresUnixMS: 2000000000000,
	}
}

func testHerdrCoordinatorResource(repo string) HerdrResource {
	return HerdrResource{
		WorkspaceID: "w1", Label: "fanout-coordinator-token",
		PaneID: "w1:p1", TerminalID: "term-1", CurrentPath: repo,
	}
}

func testHerdrRow(parent string) HerdrRow {
	id, err := HerdrWorktreeIntentID(parent, "", 426, "")
	if err != nil {
		panic(err)
	}
	return HerdrRow{
		ID: id, Kind: HerdrIntentWorktree, Parent: parent,
		RuntimeParent: parent,
		IssueNum:      426, Backend: backend.Herdr,
		Slug: "child", BranchName: "fanout/child",
		FullBranchRef: "refs/heads/fanout/child",
		BaseBranch:    "main", BaseSHA: strings.Repeat("1", 40),
		ExpectedHead: strings.Repeat("1", 40), WorktreePath: "/repo/child",
		BranchCreated: true,
		Resource: HerdrResource{
			WorkspaceID: "w2", Label: "fanout-worktree-token",
			PaneID: "w2:p1", TerminalID: "term-2", CurrentPath: "/repo/child",
			RepoKey: "/repo/.git", RepoRoot: "/repo",
		},
		Session: "fanout-test", SocketPath: "/private/tmp/fanout-test/herdr.sock",
	}
}

func newHerdrControlRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runHerdrControlGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrControlGit(t, repo, "add", "README.md")
	runHerdrControlGit(t, repo, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "init")
	return repo
}

func runHerdrControlGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
