package herdrrun

import (
	"reflect"
	"testing"
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
				Base: "0123456789012345678901234567890123456789", Path: "/repo/child", Label: "nonce", NoFocus: true,
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
				Kind: WorktreeOpen, WorkspaceID: "w1", Path: "/repo/child", Label: "nonce", NoFocus: true,
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
