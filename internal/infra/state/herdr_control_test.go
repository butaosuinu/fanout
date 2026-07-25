package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

func TestHerdrControlIsSharedAcrossLinkedWorktrees(t *testing.T) {
	repo := initHerdrControlRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	runHerdrControlGit(t, repo, "worktree", "add", "-b", "sibling", sibling, "HEAD")

	rootPath, err := HerdrControlPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	siblingPath, err := HerdrControlPath(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if rootPath != siblingPath {
		t.Fatalf("control paths differ: root=%s sibling=%s", rootPath, siblingPath)
	}

	locked, err := LockHerdrControl(sibling)
	if err != nil {
		t.Fatal(err)
	}
	intent := HerdrLaunchIntent{
		IntentID:       "issue:3:527:528",
		Parent:         "0524",
		IssueNum:       527,
		Backend:        backend.Herdr,
		OperationState: HerdrOperationActive,
		Phase:          HerdrPhaseWorktreePlanned,
	}
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	got, err := LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 {
		t.Fatalf("revision = %d, want 1", got.Revision)
	}
	if saved, ok := got.FindIntent(intent.IntentID); !ok || saved.Parent != intent.Parent {
		t.Fatalf("saved intent = %+v, %t", saved, ok)
	}
	bindings := got.ProvisionalBindings()
	if len(bindings) != 1 || bindings[0].Parent != "0524" || bindings[0].Backend != backend.Herdr {
		t.Fatalf("provisional bindings = %+v", bindings)
	}

	info, err := os.Stat(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("control mode = %o, want 600", info.Mode().Perm())
	}
	lockInfo, err := os.Stat(rootPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("control lock mode = %o, want 600", lockInfo.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(repo, ".fanout", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("worktree-local state unexpectedly created: %v", err)
	}
}

func TestHerdrControlProvisionalBindingsKeepManualCleanupSticky(t *testing.T) {
	store := emptyHerdrControl()
	store.Intents = []HerdrLaunchIntent{
		{Parent: "524", Backend: backend.Herdr, OperationState: HerdrOperationManualCleanupRequired},
		{Parent: "525", Backend: backend.Herdr, OperationState: HerdrOperationLaunchAborted},
		{Parent: "", Backend: backend.Herdr, OperationState: HerdrOperationActive},
	}
	got := store.ProvisionalBindings()
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "524", Backend: backend.Herdr}) {
		t.Fatalf("bindings = %+v, want only manual-cleanup parent", got)
	}
}

func TestHerdrControlRejectsSymlinkRegistryAndLock(t *testing.T) {
	repo := initHerdrControlRepo(t)
	path, err := HerdrControlPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(foreign, []byte(`{"schema_id":"fanout.herdr-control.v1","revision":0,"intents":[],"branch_lineages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHerdrControl(repo); err == nil {
		t.Fatal("symlink registry unexpectedly loaded")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := LockHerdrControl(repo); err == nil {
		t.Fatal("symlink lock unexpectedly opened")
	}
}

func TestHerdrIntentIDCanonicalizesNumericParent(t *testing.T) {
	a, err := HerdrIntentID("0524", 527, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HerdrIntentID("524", 527, "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical intent ids differ: %q != %q", a, b)
	}
}

func initHerdrControlRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runHerdrControlGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrControlGit(t, repo, "add", "tracked")
	runHerdrControlGit(t, repo, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "base")
	return repo
}

func runHerdrControlGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
