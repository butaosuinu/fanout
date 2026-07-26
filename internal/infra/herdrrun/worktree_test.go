package herdrrun

import (
	"reflect"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

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
			req: WorktreeMutationRequest{
				Kind: WorkspaceCreate, CWD: "/repo", Label: "nonce", NoFocus: true,
			},
			wantArgs:   []string{"workspace", "create", "--cwd", "/repo", "--label", "nonce", "--no-focus"},
			wantID:     "cli:workspace:create",
			wantResult: "workspace_created",
		},
		{
			name: "worktree create",
			req: WorktreeMutationRequest{
				Kind: WorktreeCreate, WorkspaceID: "w1", Branch: "fanout/child",
				CoordinatorWorkspaceLabel: "coordinator", CoordinatorPaneID: "w1:p1",
				CoordinatorTerminalID: "terminal-1", CoordinatorWorkspaceCWD: "/repo",
				ExpectedRepoKey: "/repo/.git", ExpectedRepoRoot: "/repo",
				Base: "0123456789012345678901234567890123456789", Path: "/repo/child",
				Label: "nonce", NoFocus: true,
			},
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
			req: WorktreeMutationRequest{
				Kind: WorktreeOpen, WorkspaceID: "w1",
				CoordinatorWorkspaceLabel: "coordinator", CoordinatorPaneID: "w1:p1",
				CoordinatorTerminalID: "terminal-1", CoordinatorWorkspaceCWD: "/repo",
				ExpectedRepoKey: "/repo/.git", ExpectedRepoRoot: "/repo",
				Path: "/repo/child", Label: "nonce", NoFocus: true,
			},
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
	req := WorktreeMutationRequest{
		Kind: WorktreeCreate, WorkspaceID: "w1",
		CoordinatorWorkspaceLabel: "coordinator", CoordinatorPaneID: "w1:p1",
		CoordinatorTerminalID: "terminal-1", CoordinatorWorkspaceCWD: "/repo",
		ExpectedRepoKey: "/repo/.git", ExpectedRepoRoot: "/repo",
		Branch: "fanout/child", Base: "0123456789012345678901234567890123456789",
		Path: "/repo/child", Label: "nonce", NoFocus: true,
	}
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
