package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		IntentID:           "issue:3:527:528",
		Parent:             "0524",
		IssueNum:           527,
		Backend:            backend.Herdr,
		OperationState:     HerdrOperationActive,
		Phase:              HerdrPhaseWorktreePlanned,
		SourceRootPhysical: repo,
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
	bindings, err := got.ProvisionalBindings(HerdrBindingScope{})
	if err != nil {
		t.Fatal(err)
	}
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
	got, err := store.ProvisionalBindings(HerdrBindingScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "524", Backend: backend.Herdr}) {
		t.Fatalf("bindings = %+v, want only manual-cleanup parent", got)
	}
}

func TestHerdrControlRejectsLauncherReadyPhasesUntilIssue528(t *testing.T) {
	for _, phase := range []HerdrLaunchPhase{HerdrPhaseWorkspaceReady, HerdrPhaseWorktreeReady} {
		store := emptyHerdrControl()
		store.Intents = []HerdrLaunchIntent{{
			IntentID:           "ready-is-deferred",
			Parent:             "524",
			Backend:            backend.Herdr,
			OperationState:     HerdrOperationActive,
			Phase:              phase,
			SourceRootPhysical: "/repo",
		}}
		err := validateHerdrControlStore(store)
		if err == nil || !strings.Contains(err.Error(), "deferred to launcher readiness issue #528") {
			t.Fatalf("phase %q validation error = %v, want deferred readiness", phase, err)
		}
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

func TestHerdrControlRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "all fields", body: `{}`},
		{
			name: "revision",
			body: `{"schema_id":"fanout.herdr-control.v1","intents":[],"branch_lineages":[]}`,
		},
		{
			name: "intents",
			body: `{"schema_id":"fanout.herdr-control.v1","revision":0,"branch_lineages":[]}`,
		},
		{
			name: "branch lineages",
			body: `{"schema_id":"fanout.herdr-control.v1","revision":0,"intents":[]}`,
		},
		{
			name: "null intents",
			body: `{"schema_id":"fanout.herdr-control.v1","revision":0,"intents":null,"branch_lineages":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initHerdrControlRepo(t)
			path, err := HerdrControlPath(repo)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadHerdrControl(repo); err == nil ||
				!strings.Contains(err.Error(), "missing required fields") {
				t.Fatalf("LoadHerdrControl() error = %v, want missing required fields", err)
			}
		})
	}
}

func TestHerdrControlRejectsUnknownFieldsAtEveryStructLevel(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "top level",
			body: `{"schema_id":"fanout.herdr-control.v1","revision":0,"intents":[],"branch_lineages":[],"future":true}`,
		},
		{
			name: "nested intent",
			body: `{"schema_id":"fanout.herdr-control.v1","revision":0,"intents":[{"future":true}],"branch_lineages":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initHerdrControlRepo(t)
			path, err := HerdrControlPath(repo)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadHerdrControl(repo); err == nil ||
				!strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("LoadHerdrControl() error = %v, want unknown field rejection", err)
			}
		})
	}
}

func TestEnsureHerdrControlDirReadoptsConcurrentCreation(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "fanout")
	firstLstat := true
	lstat := func(name string) (os.FileInfo, error) {
		if firstLstat {
			firstLstat = false
			return nil, os.ErrNotExist
		}
		return os.Lstat(name)
	}
	mkdir := func(name string, mode os.FileMode) error {
		if err := os.Mkdir(name, mode); err != nil {
			return err
		}
		return os.ErrExist
	}
	if err := ensureHerdrControlDirWith(path, lstat, mkdir); err != nil {
		t.Fatalf("ensureHerdrControlDirWith() = %v, want concurrent directory adoption", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateHerdrControlDirInfo(path, info); err != nil {
		t.Fatal(err)
	}
}

func TestHerdrIntentIDCanonicalizesNumericParent(t *testing.T) {
	a, err := HerdrIntentID("0524", 527, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HerdrIntentID("524", 527, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical intent ids differ: %q != %q", a, b)
	}
}

func TestHerdrIssueLessPlanIdentityAndBindingsAreWorktreeAndSpecScoped(t *testing.T) {
	const (
		specA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		specB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	idA, err := HerdrIntentID("plan:alpha", 0, "task-a", "/repo/a", specA)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := HerdrIntentID("plan:alpha", 0, "task-a", "/repo/b", specB)
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB {
		t.Fatalf("scoped task ids collide: %q", idA)
	}
	store := emptyHerdrControl()
	store.Intents = []HerdrLaunchIntent{
		{
			IntentID: idA, Parent: "plan:alpha", TaskID: "task-a",
			SourceRootPhysical: "/repo/a", PlanSpecIdentity: specA,
			Backend: backend.Herdr, OperationState: HerdrOperationActive,
		},
		{
			IntentID: idB, Parent: "plan:alpha", TaskID: "task-a",
			SourceRootPhysical: "/repo/b", PlanSpecIdentity: specB,
			Backend: backend.Herdr, OperationState: HerdrOperationActive,
		},
	}
	got, err := store.ProvisionalBindings(HerdrBindingScope{
		Parent:             "plan:alpha",
		SourceRootPhysical: "/repo/a",
		PlanSpecIdentity:   specA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "plan:alpha", Backend: backend.Herdr}) {
		t.Fatalf("scoped bindings = %+v", got)
	}

	_, err = store.ProvisionalBindings(HerdrBindingScope{
		Parent:             "plan:alpha",
		SourceRootPhysical: "/repo/a",
		PlanSpecIdentity:   specB,
	})
	if err == nil || !strings.Contains(err.Error(), "planspec identity drift") {
		t.Fatalf("drift error = %v, want unresolved same-root plan rejection", err)
	}
}

func TestHerdrCoordinatorIntentIDIsParentSingleton(t *testing.T) {
	a, err := HerdrCoordinatorIntentID("0524", "/repo/a", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HerdrCoordinatorIntentID("524", "/repo/b", "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("parent coordinator ids differ: %q != %q", a, b)
	}
	const (
		specA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		specB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	planA, err := HerdrCoordinatorIntentID("plan:alpha", "/repo/a", specA)
	if err != nil {
		t.Fatal(err)
	}
	planB, err := HerdrCoordinatorIntentID("plan:alpha", "/repo/b", specB)
	if err != nil {
		t.Fatal(err)
	}
	if planA == planB {
		t.Fatalf("issue-less plan coordinator ids collide: %q", planA)
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
