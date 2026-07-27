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
		IntentID:             "issue:3:527:528",
		Parent:               "0524",
		IssueNum:             527,
		Backend:              backend.Herdr,
		OperationState:       HerdrOperationActive,
		Phase:                HerdrPhaseWorktreePlanned,
		SourceRootPhysical:   repo,
		SourceGitDirPhysical: filepath.Join(repo, ".git"),
		SourceGitDirDevice:   1,
		SourceGitDirInode:    2,
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
			IntentID:             "ready-is-deferred",
			Parent:               "524",
			Backend:              backend.Herdr,
			OperationState:       HerdrOperationActive,
			Phase:                phase,
			SourceRootPhysical:   "/repo",
			SourceGitDirPhysical: "/repo/.git",
			SourceGitDirDevice:   1,
			SourceGitDirInode:    2,
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

func TestHerdrControlLoadRejectsIntentWithoutParent(t *testing.T) {
	repo := initHerdrControlRepo(t)
	path, err := HerdrControlPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{
		"schema_id":"fanout.herdr-control.v1",
		"revision":0,
		"intents":[{
			"intent_id":"missing-parent",
			"backend":"herdr",
			"operation_state":"active",
			"phase":"worktree-planned",
			"source_root_physical":"/repo",
			"source_git_dir_physical":"/repo/.git",
			"source_git_dir_device":1,
			"source_git_dir_inode":2
		}],
		"branch_lineages":[]
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHerdrControl(repo); err == nil || !strings.Contains(err.Error(), "has no parent") {
		t.Fatalf("LoadHerdrControl() error = %v, want missing parent rejection", err)
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
	a, err := HerdrIntentID("0524", 527, "", "/repo", "/repo/.git", 1, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HerdrIntentID("524", 527, "", "/repo", "/repo/.git", 1, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical intent ids differ: %q != %q", a, b)
	}
	recreated, err := HerdrIntentID("524", 527, "", "/repo", "/repo/.git", 1, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if a == recreated {
		t.Fatalf("recreated source checkout reused issue intent id %q", a)
	}
}

func TestHerdrIssueLessPlanIdentityAndBindingsAreWorktreeAndSpecScoped(t *testing.T) {
	const (
		specA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		specB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	idA, err := HerdrIntentID("plan:alpha", 0, "task-a", "/repo/a", "/git/a", 1, 2, specA)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := HerdrIntentID("plan:alpha", 0, "task-a", "/repo/b", "/git/b", 1, 3, specB)
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
			SourceRootPhysical: "/repo/a", SourceGitDirPhysical: "/git/a",
			SourceGitDirDevice: 1, SourceGitDirInode: 2, PlanSpecIdentity: specA,
			Backend: backend.Herdr, OperationState: HerdrOperationActive,
		},
		{
			IntentID: idB, Parent: "plan:alpha", TaskID: "task-a",
			SourceRootPhysical: "/repo/b", SourceGitDirPhysical: "/git/b",
			SourceGitDirDevice: 1, SourceGitDirInode: 3, PlanSpecIdentity: specB,
			Backend: backend.Herdr, OperationState: HerdrOperationActive,
		},
	}
	got, err := store.ProvisionalBindings(HerdrBindingScope{
		Parent:               "plan:alpha",
		SourceRootPhysical:   "/repo/a",
		SourceGitDirPhysical: "/git/a",
		SourceGitDirDevice:   1,
		SourceGitDirInode:    2,
		PlanSpecIdentity:     specA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "plan:alpha", Backend: backend.Herdr}) {
		t.Fatalf("scoped bindings = %+v", got)
	}
	_, err = store.ProvisionalBindings(HerdrBindingScope{
		Parent:               "plan:alpha",
		SourceRootPhysical:   "/repo/a",
		SourceGitDirPhysical: "/git/a",
		SourceGitDirDevice:   1,
		SourceGitDirInode:    3,
		PlanSpecIdentity:     specA,
	})
	if err == nil || !strings.Contains(err.Error(), "git-dir identity drift") {
		t.Fatalf("recreated checkout error = %v, want git-dir identity drift", err)
	}

	_, err = store.ProvisionalBindings(HerdrBindingScope{
		Parent:               "plan:alpha",
		SourceRootPhysical:   "/repo/a",
		SourceGitDirPhysical: "/git/a",
		SourceGitDirDevice:   1,
		SourceGitDirInode:    2,
		PlanSpecIdentity:     specB,
	})
	if err == nil || !strings.Contains(err.Error(), "planspec identity drift") {
		t.Fatalf("drift error = %v, want unresolved same-root plan rejection", err)
	}
}

func TestHerdrCoordinatorIntentIDIsSourceCheckoutScoped(t *testing.T) {
	a, err := HerdrCoordinatorIntentID("0524", "/repo/a", "/git/a", 1, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HerdrCoordinatorIntentID("524", "/repo/a", "/git/a", 1, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical parent coordinator ids differ: %q != %q", a, b)
	}
	recreated, err := HerdrCoordinatorIntentID("524", "/repo/a", "/git/a", 1, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if a == recreated {
		t.Fatalf("recreated source checkout reused coordinator id %q", a)
	}
	const (
		specA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		specB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	planA, err := HerdrCoordinatorIntentID("plan:alpha", "/repo/a", "/git/a", 1, 2, specA)
	if err != nil {
		t.Fatal(err)
	}
	planB, err := HerdrCoordinatorIntentID("plan:alpha", "/repo/b", "/git/b", 1, 3, specB)
	if err != nil {
		t.Fatal(err)
	}
	if planA == planB {
		t.Fatalf("issue-less plan coordinator ids collide: %q", planA)
	}
}

func TestHerdrControlRejectsDuplicateUnresolvedChildReservations(t *testing.T) {
	base := HerdrLaunchIntent{
		IntentID:             "child-a",
		Parent:               "524",
		Backend:              backend.Herdr,
		Operation:            "child-worktree",
		OperationState:       HerdrOperationActive,
		Phase:                HerdrPhaseWorktreePlanned,
		SourceRootPhysical:   "/repo",
		SourceGitDirPhysical: "/repo/.git",
		SourceGitDirDevice:   1,
		SourceGitDirInode:    2,
		FullBranchRef:        "refs/heads/fanout/child-a",
		WorktreePath:         "/repo/.fanout/worktrees/child-a",
	}
	tests := []struct {
		name   string
		mutate func(*HerdrLaunchIntent)
		want   string
	}{
		{
			name: "branch",
			mutate: func(intent *HerdrLaunchIntent) {
				intent.WorktreePath = "/repo/.fanout/worktrees/child-b"
			},
			want: "reserve branch",
		},
		{
			name: "path",
			mutate: func(intent *HerdrLaunchIntent) {
				intent.FullBranchRef = "refs/heads/fanout/child-b"
			},
			want: "reserve path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := base
			other.IntentID = "child-b"
			other.OperationState = HerdrOperationManualCleanupRequired
			tt.mutate(&other)
			store := emptyHerdrControl()
			store.Intents = []HerdrLaunchIntent{base, other}
			err := validateHerdrControlStore(store)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateHerdrControlStore() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHerdrControlRejectsDuplicateUnresolvedCoordinatorReservations(t *testing.T) {
	base := HerdrLaunchIntent{
		IntentID:             "coordinator-a",
		Parent:               "524",
		Backend:              backend.Herdr,
		Operation:            "coordinator-workspace",
		OperationState:       HerdrOperationActive,
		Phase:                HerdrPhaseWorkspacePlanned,
		SourceRootPhysical:   "/repo/a",
		SourceGitDirPhysical: "/git/a",
		SourceGitDirDevice:   1,
		SourceGitDirInode:    2,
	}
	other := base
	other.IntentID = "coordinator-b"
	other.SourceRootPhysical = "/repo/b"
	other.SourceGitDirPhysical = "/git/b"
	other.SourceGitDirInode = 3
	store := emptyHerdrControl()
	store.Intents = []HerdrLaunchIntent{base, other}
	err := validateHerdrControlStore(store)
	if err == nil || !strings.Contains(err.Error(), "reserve owner") {
		t.Fatalf("validateHerdrControlStore() error = %v, want coordinator reservation rejection", err)
	}

	const planIdentity = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base.Parent = "plan:alpha"
	base.PlanSpecIdentity = planIdentity
	other = base
	other.IntentID = "coordinator-plan-b"
	other.SourceGitDirPhysical = "/git/recreated"
	other.SourceGitDirInode = 3
	store.Intents = []HerdrLaunchIntent{base, other}
	err = validateHerdrControlStore(store)
	if err == nil || !strings.Contains(err.Error(), "reserve owner") {
		t.Fatalf("validateHerdrControlStore() plan error = %v, want same-root reservation rejection", err)
	}

	other.SourceRootPhysical = "/repo/b"
	if err := validateHerdrControlStore(HerdrControlStore{
		SchemaID: HerdrControlSchemaID,
		Intents:  []HerdrLaunchIntent{base, other},
		Lineages: []HerdrBranchLineage{},
	}); err != nil {
		t.Fatalf("different-root plan coordinators rejected: %v", err)
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
