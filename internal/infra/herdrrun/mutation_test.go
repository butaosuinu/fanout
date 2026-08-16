package herdrrun

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

func TestWorktreeMutationArgsPinHerdr075CLI(t *testing.T) {
	coordinator := testCoordinatorObservation()
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "coordinator",
			args: workspaceCreateArgs(corebackend.WorkspaceCreateRequest{
				CWD: "/repo", SourceRepoKey: "/repo/.git", Label: "nonce",
			}),
			want: []string{
				"workspace", "create", "--cwd", "/repo", "--label", "nonce", "--no-focus",
			},
		},
		{
			name: "fresh branch",
			args: worktreeCreateArgs(corebackend.WorktreeCreateRequest{
				Coordinator: coordinator,
				Branch:      "fanout/child", Base: strings.Repeat("1", 40),
				Path: "/repo/.fanout/worktrees/child", Label: "nonce",
			}),
			want: []string{
				"worktree", "create", "--workspace", "w1",
				"--branch", "fanout/child", "--base", strings.Repeat("1", 40),
				"--path", "/repo/.fanout/worktrees/child",
				"--label", "nonce", "--no-focus", "--json",
			},
		},
		{
			name: "existing branch omits base",
			args: worktreeCreateArgs(corebackend.WorktreeCreateRequest{
				Coordinator: coordinator,
				Branch:      "fanout/existing", Path: "/repo/.fanout/worktrees/existing",
				Label: "nonce",
			}),
			want: []string{
				"worktree", "create", "--workspace", "w1",
				"--branch", "fanout/existing",
				"--path", "/repo/.fanout/worktrees/existing",
				"--label", "nonce", "--no-focus", "--json",
			},
		},
		{
			name: "open",
			args: worktreeOpenArgs(corebackend.WorktreeOpenRequest{
				Coordinator: coordinator,
				Path:        "/repo/.fanout/worktrees/child", Label: "nonce",
			}),
			want: []string{
				"worktree", "open", "--workspace", "w1",
				"--path", "/repo/.fanout/worktrees/child",
				"--label", "nonce", "--no-focus", "--json",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.args, tt.want) {
				t.Fatalf("args = %#v, want %#v", tt.args, tt.want)
			}
		})
	}
}

func TestDecodeMutationRejectionRequiresExactEnvelope(t *testing.T) {
	data := []byte(`{"id":"cli:worktree:create","error":{"code":"worktree_create_failed","message":"already exists"}}`)
	got, ok := decodeMutationRejection(data, "cli:worktree:create")
	if !ok || got.Code != "worktree_create_failed" || !errors.Is(got, corebackend.ErrMutationRejected) {
		t.Fatalf("rejection = (%+v,%t)", got, ok)
	}
	if _, ok := decodeMutationRejection(data, "cli:workspace:create"); ok {
		t.Fatal("wrong envelope id unexpectedly accepted")
	}
	if _, ok := decodeMutationRejection(
		[]byte(`{"id":"cli:worktree:create","error":{"code":"","message":"missing"}}`),
		"cli:worktree:create",
	); ok {
		t.Fatal("incomplete error envelope unexpectedly accepted")
	}
}

func TestMutationNotIssuedErrorPreservesClassificationAndCause(t *testing.T) {
	cause := errors.New("owned admission failed")
	err := mutationNotIssued(cause)
	if !errors.Is(err, corebackend.ErrMutationNotIssued) || !errors.Is(err, cause) {
		t.Fatalf("mutation-not-issued error = %v", err)
	}
	var typed corebackend.MutationNotIssuedError
	if !errors.As(err, &typed) || !errors.Is(typed.Cause, cause) {
		t.Fatalf("mutation-not-issued type = %#v", typed)
	}
}

func TestDecodeWorktreeMutationResponsePinsResultShape(t *testing.T) {
	data := []byte(`{
	  "id":"cli:worktree:create",
	  "result":{
	    "type":"worktree_created",
	    "workspace":{
	      "workspace_id":"w2",
	      "label":"nonce",
	      "focused":false,
	      "worktree":{
	        "repo_key":"/repo/.git",
	        "repo_name":"repo",
	        "repo_root":"/repo",
	        "checkout_path":"/repo/.fanout/worktrees/child",
	        "is_linked_worktree":true
	      }
	    }
	  }
	}`)
	got, err := decodeWorktreeMutationResponse(data, "cli:worktree:create", "worktree_created")
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace.WorkspaceID != "w2" || got.Workspace.Worktree == nil ||
		got.Workspace.Worktree.CheckoutPath != "/repo/.fanout/worktrees/child" {
		t.Fatalf("decoded response = %+v", got)
	}
	if _, err := decodeWorktreeMutationResponse(
		data,
		"cli:worktree:open",
		"worktree_opened",
	); err == nil {
		t.Fatal("wrong result shape unexpectedly accepted")
	}
}

func TestValidateAlreadyOpenRequiresIntentBoundWorkspaceAndLabel(t *testing.T) {
	spec := mutationSpec{
		kind:                     corebackend.WorktreeOpen,
		expectedAlreadyOpenID:    "w2",
		expectedAlreadyOpenLabel: "nonce",
	}
	workspace := workspaceJSON{WorkspaceID: "w2", Label: "nonce"}
	if err := validateAlreadyOpen(spec, workspace, true); err != nil {
		t.Fatal(err)
	}
	foreign := workspace
	foreign.WorkspaceID = "w3"
	if err := validateAlreadyOpen(spec, foreign, true); err == nil {
		t.Fatal("foreign already_open workspace unexpectedly accepted")
	}
	unbound := mutationSpec{kind: corebackend.WorktreeOpen}
	if err := validateAlreadyOpen(unbound, workspace, true); err == nil {
		t.Fatal("already_open without an intent binding unexpectedly accepted")
	}
	if err := validateAlreadyOpen(mutationSpec{kind: corebackend.WorktreeCreate}, workspace, true); err == nil {
		t.Fatal("worktree create already_open unexpectedly accepted")
	}
}

func TestValidateEmptyPluginListFailsClosed(t *testing.T) {
	if err := validateEmptyPluginList(
		[]byte(`{"id":"cli:plugin","result":{"type":"plugin_list","plugins":[]}}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := validateEmptyPluginList(
		[]byte(`{"id":"cli:plugin","result":{"type":"plugin_list","plugins":[{"id":"setup"}]}}`),
	); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty plugin error = %v", err)
	}
	if err := validateEmptyPluginList(
		[]byte(`{"id":"cli:plugin","result":{"type":"plugin_list"}}`),
	); err == nil {
		t.Fatal("missing plugins field unexpectedly accepted")
	}
}

func TestWorkspaceObservationPreservesPanesWithoutGuessingRoot(t *testing.T) {
	focused := false
	rootCWD := "/repo"
	extraCWD := "/repo/subdir"
	workspace := workspaceJSON{WorkspaceID: "w1", Label: "coordinator", Focused: &focused}
	got := workspaceObservation(workspace, []paneJSON{
		{PaneID: "w1:p1", WorkspaceID: "w1", TerminalID: "term-1", CWD: &rootCWD},
		{PaneID: "w1:p2", WorkspaceID: "w1", TerminalID: "term-2", CWD: &extraCWD},
	})
	if got.Pane.Pane != "" || got.TerminalID != "" {
		t.Fatalf("multi-pane workspace guessed a root identity: %+v", got)
	}
	if len(got.Panes) != 2 ||
		got.Panes[0].Pane.Pane != "w1:p1" ||
		got.Panes[0].TerminalID != "term-1" ||
		got.Panes[0].CWD != rootCWD {
		t.Fatalf("multi-pane observation = %+v, want saved root pane available", got)
	}
	paneLess := workspaceObservation(workspace, nil)
	if paneLess.WorkspaceID != "w1" || paneLess.Label != "coordinator" ||
		len(paneLess.Panes) != 0 || paneLess.Pane.Pane != "" || paneLess.TerminalID != "" {
		t.Fatalf("pane-less observation = %+v, want workspace identity without an invented pane", paneLess)
	}
}

func testCoordinatorObservation() corebackend.WorkspaceObservation {
	return corebackend.WorkspaceObservation{
		WorkspaceID: "w1",
		Label:       "coordinator",
		Pane: corebackend.PaneRef{
			Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1",
		},
		TerminalID: "term-1",
		CWD:        "/repo",
	}
}
