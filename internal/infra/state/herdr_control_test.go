package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

func TestHerdrControlBindingsIncludeRowsAndEveryIntentStatus(t *testing.T) {
	repo := newHerdrControlRepo(t)
	store := emptyHerdrControl()
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

	rows := store.RowBindings()
	if len(rows) != 1 || rows[0] != (backend.Binding{Parent: "425", Backend: backend.Herdr}) {
		t.Fatalf("row bindings = %#v", rows)
	}
	intents := store.ProvisionalBindings()
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

func TestHerdrControlRejectsRowIntentReservationConflict(t *testing.T) {
	repo := newHerdrControlRepo(t)
	store := emptyHerdrControl()
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
	issue, err := HerdrWorktreeIntentID("00425", 426, "")
	if err != nil {
		t.Fatal(err)
	}
	alias, err := HerdrWorktreeIntentID("425", 426, "")
	if err != nil {
		t.Fatal(err)
	}
	if issue != alias {
		t.Fatalf("numeric parent aliases differ: %q != %q", issue, alias)
	}
	task, err := HerdrWorktreeIntentID("plan:demo", 0, "api:client")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task, "plan:demo") || !strings.Contains(task, "api:client") {
		t.Fatalf("task id = %q, want length-prefixed plan/task identity", task)
	}
	if _, err := HerdrWorktreeIntentID("plan:demo", 1, "task"); err == nil {
		t.Fatal("issue and task identity unexpectedly accepted together")
	}
}

func TestHerdrControlRejectsIncompleteRealizedIntent(t *testing.T) {
	repo := newHerdrControlRepo(t)
	intent := testHerdrCoordinatorIntent(repo, "425")
	intent.Status = HerdrIntentRealized
	store := emptyHerdrControl()
	store.Intents = append(store.Intents, intent)
	if err := validateHerdrControl(store); err == nil || !strings.Contains(err.Error(), "resource is incomplete") {
		t.Fatalf("realized intent validation error = %v", err)
	}
}

func testHerdrCoordinatorIntent(repo, parent string) HerdrIntent {
	id, err := HerdrCoordinatorIntentID(parent)
	if err != nil {
		panic(err)
	}
	return HerdrIntent{
		ID: id, Kind: HerdrIntentCoordinator, Status: HerdrIntentPlanned,
		Parent:  parentref.Canon(strings.TrimSpace(parent)),
		Backend: backend.Herdr, WorktreePath: repo,
		WorkspaceLabel: "fanout-coordinator-token", Session: "fanout-test",
		SocketPath: "/private/tmp/fanout-test/herdr.sock",
		TimeoutMS:  300000, ExpiresUnixMS: 2000000000000,
	}
}

func testHerdrWorktreeIntent(repo, parent string, issue int, slug string) HerdrIntent {
	id, err := HerdrWorktreeIntentID(parent, issue, "")
	if err != nil {
		panic(err)
	}
	return HerdrIntent{
		ID: id, Kind: HerdrIntentWorktree, Status: HerdrIntentPlanned,
		Parent:   parentref.Canon(strings.TrimSpace(parent)),
		IssueNum: issue, Backend: backend.Herdr,
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
	id, err := HerdrWorktreeIntentID(parent, 426, "")
	if err != nil {
		panic(err)
	}
	return HerdrRow{
		ID: id, Kind: HerdrIntentWorktree, Parent: parent,
		IssueNum: 426, Backend: backend.Herdr,
		Slug: "child", BranchName: "fanout/child",
		FullBranchRef: "refs/heads/fanout/child",
		BaseBranch:    "main", BaseSHA: strings.Repeat("1", 40),
		ExpectedHead: strings.Repeat("1", 40), WorktreePath: "/repo/child",
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
