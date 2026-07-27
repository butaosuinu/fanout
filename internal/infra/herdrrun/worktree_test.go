package herdrrun

import (
	"context"
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
		req  WorktreeMutationRequest
		want []string
		id   string
		kind string
	}{
		{
			name: "coordinator",
			req: WorktreeMutationRequest{
				Kind: WorkspaceCreate, CWD: "/repo", Label: "nonce", NoFocus: true,
			},
			want: []string{
				"workspace", "create", "--cwd", "/repo", "--label", "nonce", "--no-focus",
			},
			id: "cli:workspace:create", kind: "workspace_created",
		},
		{
			name: "fresh branch",
			req: WorktreeMutationRequest{
				Kind: WorktreeCreate, Coordinator: coordinator,
				Branch: "fanout/child", Base: strings.Repeat("1", 40),
				Path: "/repo/.fanout/worktrees/child", Label: "nonce", NoFocus: true,
			},
			want: []string{
				"worktree", "create", "--workspace", "w1",
				"--branch", "fanout/child", "--base", strings.Repeat("1", 40),
				"--path", "/repo/.fanout/worktrees/child",
				"--label", "nonce", "--no-focus", "--json",
			},
			id: "cli:worktree:create", kind: "worktree_created",
		},
		{
			name: "existing branch omits base",
			req: WorktreeMutationRequest{
				Kind: WorktreeCreate, Coordinator: coordinator,
				Branch: "fanout/existing", Path: "/repo/.fanout/worktrees/existing",
				Label: "nonce", NoFocus: true,
			},
			want: []string{
				"worktree", "create", "--workspace", "w1",
				"--branch", "fanout/existing",
				"--path", "/repo/.fanout/worktrees/existing",
				"--label", "nonce", "--no-focus", "--json",
			},
			id: "cli:worktree:create", kind: "worktree_created",
		},
		{
			name: "open",
			req: WorktreeMutationRequest{
				Kind: WorktreeOpen, Coordinator: coordinator,
				Path: "/repo/.fanout/worktrees/child", Label: "nonce", NoFocus: true,
			},
			want: []string{
				"worktree", "open", "--workspace", "w1",
				"--path", "/repo/.fanout/worktrees/child",
				"--label", "nonce", "--no-focus", "--json",
			},
			id: "cli:worktree:open", kind: "worktree_opened",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, id, kind := worktreeMutationArgs(tt.req)
			if !reflect.DeepEqual(args, tt.want) || id != tt.id || kind != tt.kind {
				t.Fatalf("got (%#v,%q,%q), want (%#v,%q,%q)", args, id, kind, tt.want, tt.id, tt.kind)
			}
		})
	}
}

func TestDecodeMutationRejectionRequiresExactEnvelope(t *testing.T) {
	data := []byte(`{"id":"cli:worktree:create","error":{"code":"worktree_create_failed","message":"already exists"}}`)
	got, ok := decodeMutationRejection(data, "cli:worktree:create")
	if !ok || got.Code != "worktree_create_failed" || !errors.Is(got, ErrMutationRejected) {
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
	if !errors.Is(err, ErrMutationNotIssued) || !errors.Is(err, cause) {
		t.Fatalf("mutation-not-issued error = %v", err)
	}
	var typed MutationNotIssuedError
	if !errors.As(err, &typed) || !errors.Is(typed.Cause, cause) {
		t.Fatalf("mutation-not-issued type = %#v", typed)
	}
	if _, err := (&Backend{}).runWorktreeMutation(
		context.Background(),
		"/unused/herdr",
		route{},
	); !errors.Is(err, ErrMutationNotIssued) {
		t.Fatalf("deadline-less command error = %v", err)
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

func TestValidateAlreadyOpenRequiresSavedWorkspaceAndLabel(t *testing.T) {
	req := WorktreeMutationRequest{
		Kind: WorktreeOpen, ExpectedAlreadyOpenID: "w2", ExpectedAlreadyOpenLabel: "nonce",
	}
	got := WorkspaceObservation{WorkspaceID: "w2", Label: "nonce"}
	if err := validateAlreadyOpen(req, got, true, true); err != nil {
		t.Fatal(err)
	}
	got.WorkspaceID = "w3"
	if err := validateAlreadyOpen(req, got, true, true); err == nil {
		t.Fatal("foreign already_open workspace unexpectedly accepted")
	}
	got.WorkspaceID = "w2"
	if err := validateAlreadyOpen(req, got, true, false); err == nil {
		t.Fatal("workspace absent from pre-state unexpectedly accepted")
	}
	if err := validateAlreadyOpen(req, got, false, true); err == nil {
		t.Fatal("prebound workspace replacement unexpectedly accepted")
	}
	if err := validateAlreadyOpen(
		WorktreeMutationRequest{Kind: WorktreeCreate},
		WorkspaceObservation{},
		true,
		false,
	); err == nil {
		t.Fatal("worktree create already_open unexpectedly accepted")
	}
}

func TestExpectedAlreadyOpenBindingRequiresExactPreState(t *testing.T) {
	req := WorktreeMutationRequest{
		Kind: WorktreeOpen, Path: "/repo/child",
		SourceRepoKey: "/repo/.git", SourceRepoRoot: "/repo",
		ExpectedAlreadyOpenID: "w2", ExpectedAlreadyOpenLabel: "nonce",
	}
	workspace := WorkspaceObservation{
		WorkspaceID: "w2", Label: "nonce", Path: req.Path,
		RepoKey: req.SourceRepoKey, RepoRoot: req.SourceRepoRoot, CWD: req.Path,
	}
	if !hasExpectedAlreadyOpenBinding(req, []WorkspaceObservation{workspace}) {
		t.Fatal("exact pre-state binding was not recognized")
	}
	changed := workspace
	changed.RepoKey = "/foreign/.git"
	if hasExpectedAlreadyOpenBinding(req, []WorkspaceObservation{changed}) {
		t.Fatal("foreign pre-state provenance was accepted")
	}
	if hasExpectedAlreadyOpenBinding(req, []WorkspaceObservation{workspace, workspace}) {
		t.Fatal("duplicate pre-state bindings were accepted")
	}
}

func TestValidateEmptyPluginListFailsClosed(t *testing.T) {
	if err := validateEmptyPluginList(
		[]byte(`{"id":"cli:plugin:list","result":{"type":"plugin_list","plugins":[]}}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := validateEmptyPluginList(
		[]byte(`{"id":"cli:plugin:list","result":{"type":"plugin_list","plugins":[{"id":"setup"}]}}`),
	); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty plugin error = %v", err)
	}
	if err := validateEmptyPluginList(
		[]byte(`{"id":"cli:plugin:list","result":{"type":"plugin_list"}}`),
	); err == nil {
		t.Fatal("missing plugins field unexpectedly accepted")
	}
}

func TestValidateBoundCoordinatorRequiresExactIdentity(t *testing.T) {
	expected := testCoordinatorObservation()
	if err := validateBoundCoordinator(expected, []WorkspaceObservation{expected}); err != nil {
		t.Fatal(err)
	}
	changed := expected
	changed.TerminalID = "term-2"
	if err := validateBoundCoordinator(expected, []WorkspaceObservation{changed}); err == nil {
		t.Fatal("changed coordinator unexpectedly accepted")
	}
	if err := validateBoundCoordinator(expected, nil); err == nil {
		t.Fatal("missing coordinator unexpectedly accepted")
	}
}

func TestWorkspaceObservationWithholdsAmbiguousRootPane(t *testing.T) {
	focused := false
	workspace := workspaceJSON{WorkspaceID: "w1", Label: "coordinator", Focused: &focused}
	got, err := workspaceObservation(workspace, []paneJSON{
		{PaneID: "w1:p1", WorkspaceID: "w1", TerminalID: "term-1"},
		{PaneID: "w1:p2", WorkspaceID: "w1", TerminalID: "term-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pane.Pane != "" || got.TerminalID != "" {
		t.Fatalf("ambiguous root identity was retained: %+v", got)
	}
}

func testCoordinatorObservation() WorkspaceObservation {
	return WorkspaceObservation{
		WorkspaceID: "w1",
		Label:       "coordinator",
		Pane: corebackend.PaneRef{
			Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1",
		},
		TerminalID: "term-1",
		CWD:        "/repo",
	}
}
