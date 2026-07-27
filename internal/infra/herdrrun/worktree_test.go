package herdrrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	worktreeinfra "github.com/butaosuinu/fanout/internal/infra/worktree"
)

func TestRunMutationContextUsesEntireCallerDeadline(t *testing.T) {
	fake := newFakeHerdr("fanout-test", "/private/tmp/fanout-test/herdr.sock")
	fake.respond = func([]string) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	}
	backend := newTestBackend(t, "fanout-test", "/private/tmp/fanout-test/herdr.sock", fake)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := backend.runMutationContext(
		ctx,
		"/private/tmp/herdr-0.7.5",
		route{session: "fanout-test", socketPath: "/private/tmp/fanout-test/herdr.sock"},
		"workspace",
		"create",
	); err != nil {
		t.Fatal(err)
	}
	if len(fake.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(fake.commands))
	}
	if got := fake.commands[0].timeout; got <= commandTimeout || got > 30*time.Second {
		t.Fatalf("mutation timeout = %v, want caller remainder above %v", got, commandTimeout)
	}
}

func TestReadCommandRetryClassificationOnlyMarksTimeoutAndNonZeroExit(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, &exec.ExitError{}} {
		if got := markRetryableRead(err); !errors.Is(got, ErrRetryableRead) {
			t.Errorf("markRetryableRead(%T) = %v, want retryable", err, got)
		}
	}
	if got := markRetryableRead(errors.New("malformed snapshot")); errors.Is(got, ErrRetryableRead) {
		t.Fatalf("malformed response classified as retryable: %v", got)
	}
}

func TestWorktreeMutationArgsPinExact075CLI(t *testing.T) {
	tests := []struct {
		name       string
		req        WorktreeMutationRequest
		wantArgs   []string
		wantID     string
		wantResult string
	}{
		{
			name: "workspace create",
			req: withTestMutationIdentity(WorktreeMutationRequest{
				Kind: WorkspaceCreate, CWD: "/repo", Label: "nonce", NoFocus: true,
			}),
			wantArgs:   []string{"workspace", "create", "--cwd", "/repo", "--label", "nonce", "--no-focus"},
			wantID:     "cli:workspace:create",
			wantResult: "workspace_created",
		},
		{
			name: "worktree create",
			req: withTestMutationIdentity(WorktreeMutationRequest{
				Kind: WorktreeCreate, WorkspaceID: "w1", Branch: "fanout/child",
				CoordinatorWorkspaceLabel: "coordinator", CoordinatorPaneID: "w1:p1",
				CoordinatorTerminalID: "terminal-1", CoordinatorWorkspaceCWD: "/repo",
				ExpectedRepoKey: "/repo/.git", ExpectedRepoRoot: "/repo",
				Base: "0123456789012345678901234567890123456789", Path: "/repo/child",
				Label: "nonce", NoFocus: true,
			}),
			wantArgs: []string{
				"worktree", "create", "--workspace", "w1", "--branch", "fanout/child",
				"--base", "0123456789012345678901234567890123456789",
				"--path", "/repo/child", "--label", "nonce", "--no-focus", "--json",
			},
			wantID:     "cli:worktree:create",
			wantResult: "worktree_created",
		},
		{
			name: "worktree open",
			req: withTestMutationIdentity(WorktreeMutationRequest{
				Kind: WorktreeOpen, WorkspaceID: "w1",
				CoordinatorWorkspaceLabel: "coordinator", CoordinatorPaneID: "w1:p1",
				CoordinatorTerminalID: "terminal-1", CoordinatorWorkspaceCWD: "/repo",
				ExpectedRepoKey: "/repo/.git", ExpectedRepoRoot: "/repo",
				Base: "0123456789012345678901234567890123456789",
				Path: "/repo/child", Label: "nonce", NoFocus: true,
			}),
			wantArgs: []string{
				"worktree", "open", "--workspace", "w1", "--path", "/repo/child",
				"--label", "nonce", "--no-focus", "--json",
			},
			wantID:     "cli:worktree:open",
			wantResult: "worktree_opened",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateWorktreeMutationRequest(tt.req); err != nil {
				t.Fatal(err)
			}
			args, id, result := worktreeMutationArgs(tt.req)
			if !reflect.DeepEqual(args, tt.wantArgs) || id != tt.wantID || result != tt.wantResult {
				t.Fatalf("got (%v,%q,%q), want (%v,%q,%q)", args, id, result, tt.wantArgs, tt.wantID, tt.wantResult)
			}
		})
	}
}

func TestValidateBoundCoordinatorRequiresExactPersistedIdentity(t *testing.T) {
	req := withTestMutationIdentity(WorktreeMutationRequest{
		Kind: WorktreeCreate, WorkspaceID: "w1",
		CoordinatorWorkspaceLabel: "coordinator", CoordinatorPaneID: "w1:p1",
		CoordinatorTerminalID: "terminal-1", CoordinatorWorkspaceCWD: "/repo",
		ExpectedRepoKey: "/repo/.git", ExpectedRepoRoot: "/repo",
		Branch: "fanout/child", Base: "0123456789012345678901234567890123456789",
		Path: "/repo/child", Label: "nonce", NoFocus: true,
	})
	observed := []WorkspaceObservation{{
		WorkspaceID: "w1",
		Label:       "coordinator",
		Pane: corebackend.PaneRef{
			Backend:   corebackend.Herdr,
			Workspace: "w1",
			Pane:      "w1:p1",
		},
		TerminalID: "terminal-1",
		CWD:        "/repo",
	}}
	if err := validateBoundCoordinator(req, observed); err != nil {
		t.Fatal(err)
	}
	observed[0].Label = "foreign"
	if err := validateBoundCoordinator(req, observed); err == nil {
		t.Fatal("foreign coordinator label unexpectedly accepted")
	}
}

func withTestMutationIdentity(req WorktreeMutationRequest) WorktreeMutationRequest {
	req.SourceRootPhysical = "/repo"
	req.SourceGitDirPhysical = "/repo/.git"
	req.SourceGitDirDevice = 1
	req.SourceGitDirInode = 2
	req.SourceRepoKey = "/repo/.git"
	if req.Kind == WorktreeCreate || req.Kind == WorktreeOpen {
		req.ProjectRoot = "/repo"
		req.ResolvedBaseRef = "refs/heads/main"
		req.FullBranchRef = "refs/heads/fanout/child"
	}
	return req
}

func TestValidateMutationAdmissionRejectsCoordinatorSourceReplacement(t *testing.T) {
	root, _, identity := newMutationAdmissionRepo(t)
	req := WorktreeMutationRequest{
		Kind:                 WorkspaceCreate,
		SourceRootPhysical:   identity.RepoRoot,
		SourceGitDirPhysical: identity.GitDir,
		SourceGitDirDevice:   identity.GitDirDevice,
		SourceGitDirInode:    identity.GitDirInode,
		SourceRepoKey:        identity.RepoKey,
		CWD:                  root,
	}
	if err := validateMutationAdmission(req); err != nil {
		t.Fatalf("initial coordinator admission: %v", err)
	}

	if err := os.Rename(root, root+"-displaced"); err != nil {
		t.Fatal(err)
	}
	initMutationAdmissionRepo(t, root)
	if err := validateMutationAdmission(req); err == nil {
		t.Fatal("same-path coordinator source replacement unexpectedly admitted")
	}
}

func TestValidateMutationAdmissionRejectsChildDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, childMutationAdmissionFixture)
	}{
		{
			name: "source replacement",
			mutate: func(t *testing.T, fixture childMutationAdmissionFixture) {
				t.Helper()
				if err := os.Rename(fixture.root, fixture.root+"-displaced"); err != nil {
					t.Fatal(err)
				}
				initMutationAdmissionRepo(t, fixture.root)
			},
		},
		{
			name: "parent replacement",
			mutate: func(t *testing.T, fixture childMutationAdmissionFixture) {
				t.Helper()
				worktreesRoot := filepath.Dir(fixture.checkout)
				if err := os.Rename(worktreesRoot, worktreesRoot+"-displaced"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(worktreesRoot)+"-displaced", worktreesRoot); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "base advance",
			mutate: func(t *testing.T, fixture childMutationAdmissionFixture) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(fixture.root, "second.txt"),
					[]byte("second\n"),
					0o644,
				); err != nil {
					t.Fatal(err)
				}
				runMutationAdmissionGit(t, fixture.root, "add", "second.txt")
				runMutationAdmissionGit(t, fixture.root, "commit", "-m", "second")
			},
		},
		{
			name: "reserved branch advance",
			mutate: func(t *testing.T, fixture childMutationAdmissionFixture) {
				t.Helper()
				tree := runMutationAdmissionGit(t, fixture.root, "rev-parse", "HEAD^{tree}")
				advanced := runMutationAdmissionGit(
					t,
					fixture.root,
					"commit-tree",
					tree,
					"-p",
					fixture.baseSHA,
					"-m",
					"reserved branch drift",
				)
				runMutationAdmissionGit(
					t,
					fixture.root,
					"update-ref",
					fixture.request.FullBranchRef,
					advanced,
					fixture.baseSHA,
				)
			},
		},
		{
			name: "checkout path appears",
			mutate: func(t *testing.T, fixture childMutationAdmissionFixture) {
				t.Helper()
				if err := os.Mkdir(fixture.checkout, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newChildMutationAdmissionFixture(t)
			if err := validateMutationAdmission(fixture.request); err != nil {
				t.Fatalf("initial child admission: %v", err)
			}
			tt.mutate(t, fixture)
			if err := validateMutationAdmission(fixture.request); err == nil {
				t.Fatal("drifted child mutation unexpectedly admitted")
			}
		})
	}
}

type childMutationAdmissionFixture struct {
	root     string
	checkout string
	baseSHA  string
	request  WorktreeMutationRequest
}

func newChildMutationAdmissionFixture(t *testing.T) childMutationAdmissionFixture {
	t.Helper()
	root, baseSHA, identity := newMutationAdmissionRepo(t)
	checkout := filepath.Join(root, ".fanout", "worktrees", "child")
	if err := worktreeinfra.EnsureHerdrWorktreeParent(root, checkout); err != nil {
		t.Fatal(err)
	}
	const fullBranchRef = "refs/heads/fanout/child"
	if err := worktreeinfra.ReserveBranchGitDir(identity.GitDir, fullBranchRef, baseSHA); err != nil {
		t.Fatal(err)
	}
	return childMutationAdmissionFixture{
		root:     root,
		checkout: checkout,
		baseSHA:  baseSHA,
		request: WorktreeMutationRequest{
			Kind:                 WorktreeCreate,
			SourceRootPhysical:   identity.RepoRoot,
			SourceGitDirPhysical: identity.GitDir,
			SourceGitDirDevice:   identity.GitDirDevice,
			SourceGitDirInode:    identity.GitDirInode,
			SourceRepoKey:        identity.RepoKey,
			ProjectRoot:          root,
			ResolvedBaseRef:      "refs/heads/main",
			FullBranchRef:        fullBranchRef,
			Base:                 baseSHA,
			Path:                 checkout,
		},
	}
}

func newMutationAdmissionRepo(t *testing.T) (string, string, worktreeinfra.HerdrRepoIdentity) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	baseSHA := initMutationAdmissionRepo(t, root)
	identity, err := worktreeinfra.ResolveHerdrRepoIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, baseSHA, identity
}

func initMutationAdmissionRepo(t *testing.T, root string) string {
	t.Helper()
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runMutationAdmissionGit(t, root, "init", "-b", "main")
	runMutationAdmissionGit(t, root, "config", "user.name", "Fanout Test")
	runMutationAdmissionGit(t, root, "config", "user.email", "fanout@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runMutationAdmissionGit(t, root, "add", "README.md")
	runMutationAdmissionGit(t, root, "commit", "-m", "initial")
	return runMutationAdmissionGit(t, root, "rev-parse", "HEAD")
}

func runMutationAdmissionGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestValidateMutationResultProvenancePinsRepoKeyAndRoot(t *testing.T) {
	req := WorktreeMutationRequest{
		Kind:             WorktreeCreate,
		Path:             "/repo/child",
		ExpectedRepoKey:  "/repo/.git",
		ExpectedRepoRoot: "/repo",
	}
	got := WorkspaceObservation{
		Path:     "/repo/child",
		RepoKey:  "/repo/.git",
		RepoRoot: "/repo",
		CWD:      "/repo/child",
	}
	if err := validateMutationResultProvenance(req, got); err != nil {
		t.Fatal(err)
	}
	got.RepoRoot = "/foreign"
	if err := validateMutationResultProvenance(req, got); err == nil {
		t.Fatal("foreign repo root unexpectedly accepted")
	}
	got.RepoRoot = "/repo"
	got.RepoKey = "/foreign/.git"
	if err := validateMutationResultProvenance(req, got); err == nil {
		t.Fatal("foreign repo key unexpectedly accepted")
	}
}

func TestValidateMutationResponseProvenanceRequiresExactWorktreeTuple(t *testing.T) {
	req := WorktreeMutationRequest{
		Kind:             WorktreeCreate,
		Path:             "/repo/child",
		ExpectedRepoKey:  "/repo/.git",
		ExpectedRepoRoot: "/repo",
	}
	workspace := workspaceJSON{Worktree: &worktreeInfoJSON{
		CheckoutPath: "/repo/child",
		RepoKey:      "/repo/.git",
		RepoRoot:     "/repo",
	}}
	if err := validateMutationResponseProvenance(req, workspace); err != nil {
		t.Fatal(err)
	}
	workspace.Worktree = nil
	if err := validateMutationResponseProvenance(req, workspace); err == nil {
		t.Fatal("missing response worktree provenance unexpectedly accepted")
	}
	workspace.Worktree = &worktreeInfoJSON{
		CheckoutPath: "/repo/child",
		RepoKey:      "/foreign/.git",
		RepoRoot:     "/repo",
	}
	if err := validateMutationResponseProvenance(req, workspace); err == nil {
		t.Fatal("foreign response repo key unexpectedly accepted")
	}
	req.Kind = WorkspaceCreate
	if err := validateMutationResponseProvenance(req, workspace); err == nil {
		t.Fatal("coordinator response with worktree provenance unexpectedly accepted")
	}
}

func TestDecodeWorktreeOpenRequiresAlreadyOpen(t *testing.T) {
	const base = `{"id":"cli:worktree:open","result":{"type":"worktree_opened","workspace":{"workspace_id":"w2","label":"nonce","focused":false}`
	if _, err := decodeWorktreeMutationResponse(
		[]byte(base+`}}}`),
		"cli:worktree:open",
		"worktree_opened",
	); err == nil {
		t.Fatal("missing already_open unexpectedly accepted")
	}
	got, err := decodeWorktreeMutationResponse(
		[]byte(base+`,"already_open":true}}`),
		"cli:worktree:open",
		"worktree_opened",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.AlreadyOpen == nil || !*got.AlreadyOpen || got.Workspace.WorkspaceID != "w2" {
		t.Fatalf("decoded result = %+v", got)
	}
}

func TestValidateEmptyPluginListFailsClosed(t *testing.T) {
	if err := validateEmptyPluginList([]byte(
		`{"id":"cli:plugin:list","result":{"type":"plugin_list","plugins":[]}}`,
	)); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"id":"cli:plugin:list","result":{"type":"plugin_list","plugins":[{"id":"setup"}]}}`,
		`{"id":"cli:plugin:list","result":{"type":"plugin_list"}}`,
		`{"id":"cli:plugin:list","result":{"type":"unknown","plugins":[]}}`,
	} {
		if err := validateEmptyPluginList([]byte(body)); err == nil {
			t.Fatalf("unsafe plugin response accepted: %s", body)
		}
	}
}

func TestVerifyEmptyPluginRegistryUsesFreshOwnedRouteRead(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.respond = func(args []string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"plugin", "list", "--json"}) {
			t.Fatalf("plugin registry args = %v", args)
		}
		return []byte(
			`{"id":"cli:plugin:list","result":{"type":"plugin_list","plugins":[]}}`,
		), nil
	}
	backend := newTestBackend(t, session, socket, fake)
	err := backend.verifyEmptyPluginRegistry(context.Background(), probeResult{
		binary: "/private/tmp/herdr-0.7.5",
		route:  route{session: session, socketPath: socket},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.commands) != 1 {
		t.Fatalf("plugin registry command count = %d, want 1", len(fake.commands))
	}
}

func TestWorkspaceObservationAllowsUnrelatedMultiPaneWorkspaceButWithholdsRootIdentity(t *testing.T) {
	focused := false
	cwd := "/repo"
	observation, err := workspaceObservation(
		workspaceJSON{WorkspaceID: "w1", Label: "existing", Focused: &focused},
		[]paneJSON{
			{PaneID: "w1:p1", WorkspaceID: "w1", TerminalID: "terminal-1", CWD: &cwd},
			{PaneID: "w1:p2", WorkspaceID: "w1", TerminalID: "terminal-2", CWD: &cwd},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if observation.WorkspaceID != "w1" || observation.Label != "existing" ||
		observation.Pane.Pane != "" || observation.TerminalID != "" || observation.CWD != "" {
		t.Fatalf("multi-pane observation = %+v", observation)
	}
	if _, err := workspaceObservation(
		workspaceJSON{WorkspaceID: "w2", Label: "empty", Focused: &focused},
		nil,
	); err == nil {
		t.Fatal("workspace without a pane unexpectedly accepted")
	}
}
