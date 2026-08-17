package panelaunch

// Pre-send verification for Herdr realization: request validation, saved
// intent re-verification under the combined launch lock, and the Git and
// workspace postcondition checks the flows run around each mutation.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func verifyManagedWorktreePreconditions(
	ctx context.Context,
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	intent state.LaunchIntent,
) error {
	if err := worktree.VerifyWorktreeParentDir(req.ProjectRoot, intent.WorktreePath); err != nil {
		return err
	}
	if err := verifyManagedBranchPrecondition(ctx, req.SourceRoot, intent); err != nil {
		return err
	}
	checkout, err := worktree.ObserveCheckout(ctx, req.SourceRoot, intent.WorktreePath)
	if err != nil {
		return err
	}
	if !checkout.PathAbsent || checkout.Registered {
		return fmt.Errorf("herdr checkout appeared before mutation")
	}
	current, err := worktree.ResolveRepoIdentity(ctx, req.SourceRoot)
	if err != nil {
		return err
	}
	if current != source {
		return fmt.Errorf("herdr source repository identity changed")
	}
	return nil
}

// verifyManagedBranchPrecondition re-checks the reserved branch immediately
// before the mutation: it must still sit at the saved head and carry no
// checkout.
func verifyManagedBranchPrecondition(
	ctx context.Context,
	sourceRoot string,
	intent state.LaunchIntent,
) error {
	branch, found, branchErr := worktree.ObserveBranch(ctx, sourceRoot, intent.FullBranchRef)
	if branchErr != nil {
		return branchErr
	}
	if !found || branch != intent.ExpectedHead {
		return fmt.Errorf("herdr branch %s does not match saved head", intent.FullBranchRef)
	}
	return worktree.BranchAvailable(ctx, sourceRoot, intent.FullBranchRef)
}

// validateWorkspacePostcondition checks the created workspace against the
// intent. source is nil for the coordinator (whose workspace must carry no
// worktree provenance) and set for the child checkout.
func validateWorkspacePostcondition(
	intent state.LaunchIntent,
	source *worktree.RepoIdentity,
	observation backend.WorkspaceObservation,
) error {
	kind := "coordinator"
	provenanceOK := observation.Path == "" && observation.RepoKey == "" && observation.RepoRoot == ""
	if source != nil {
		kind = "worktree"
		provenanceOK = filepath.Clean(observation.Path) == filepath.Clean(intent.WorktreePath) &&
			observation.RepoKey == source.RepoKey && observation.RepoRoot == source.RepoRoot
	}
	if observation.WorkspaceID == "" || observation.Label != intent.WorkspaceLabel ||
		!provenanceOK ||
		observation.Pane.Backend != backend.Herdr || observation.Pane.Workspace != observation.WorkspaceID ||
		observation.Pane.Pane == "" || observation.TerminalID == "" ||
		filepath.Clean(observation.CWD) != filepath.Clean(intent.WorktreePath) {
		return fmt.Errorf("herdr %s postcondition does not match intent", kind)
	}
	return nil
}

func verifyCoordinatorObservation(
	expected state.RuntimeResource,
	workspaces []backend.WorkspaceObservation,
) error {
	for _, workspace := range workspaces {
		if workspace.WorkspaceID != expected.WorkspaceID {
			continue
		}
		if workspaceHasManagedResource(workspace, expected) {
			return nil
		}
		return fmt.Errorf("herdr coordinator identity changed before child mutation")
	}
	return fmt.Errorf("herdr coordinator workspace %s is not live", expected.WorkspaceID)
}

// validateSavedCoordinatorIntent re-verifies the intent the caller looked up
// by its derived ID, so identity checks cover the stored fields only.
func validateSavedCoordinatorIntent(
	req ManagedCoordinatorRequest,
	cwd string,
	runtimeParent string,
	runtimeOwnerProjectRoot string,
	intent state.LaunchIntent,
) error {
	if intent.Kind != state.IntentCoordinator ||
		intent.RuntimeParent != runtimeParent ||
		intent.IssueNum != req.IssueNum ||
		!savedManagedCoordinatorPathMatches(
			runtimeOwnerProjectRoot,
			intent.WorktreePath,
			cwd,
		) ||
		intent.Session != req.ManagedSession || intent.SocketPath != req.SocketPath {
		return fmt.Errorf("saved Herdr coordinator intent contradicts request")
	}
	return nil
}

// validateSavedWorktreeIntent re-verifies the intent the caller looked up by
// its derived ID, so identity checks cover the stored fields only.
func validateSavedWorktreeIntent(
	req ManagedWorktreeRequest,
	source worktree.RepoIdentity,
	coordinator state.RuntimeResource,
	ownerProjectRoot string,
	runtimeParent string,
	intent state.LaunchIntent,
) error {
	if intent.Kind != state.IntentWorktree ||
		intent.Parent != canonicalManagedParent(req.Parent) ||
		intent.RuntimeParent != runtimeParent ||
		intent.OwnerProjectRoot != ownerProjectRoot ||
		intent.IssueNum != req.IssueNum || intent.TaskID != req.TaskID ||
		!savedManagedWorktreePathValid(
			ownerProjectRoot,
			intent.Slug,
			intent.WorktreePath,
		) ||
		intent.Session != req.ManagedSession || intent.SocketPath != req.SocketPath ||
		intent.Coordinator != coordinator {
		return fmt.Errorf("saved Herdr worktree intent contradicts request")
	}
	if intent.Resource.RepoKey != "" &&
		!savedManagedWorktreeRepoMatches(ownerProjectRoot, intent.Resource, source) {
		return fmt.Errorf("saved Herdr worktree intent belongs to a different repository")
	}
	return nil
}

func validateManagedCoordinatorRequest(req ManagedCoordinatorRequest) error {
	if strings.TrimSpace(req.Parent) == "" || req.ProjectRoot == "" || req.SourceRoot == "" ||
		req.CWD == "" || req.ManagedSession == "" || req.SocketPath == "" {
		return fmt.Errorf("herdr coordinator request is incomplete")
	}
	if req.RuntimeParent != "" &&
		(canonicalManagedParent(req.Parent) != ManualParentRef || req.IssueNum >= 0) {
		return fmt.Errorf("explicit Herdr runtime parent requires a manual coordinator")
	}
	return nil
}

func validateManagedWorktreeRequest(req ManagedWorktreeRequest) error {
	if managedWorktreeRequestIncomplete(req) {
		return fmt.Errorf("herdr worktree request is incomplete")
	}
	if managedWorktreeRequestHasIssueKey(req) == (strings.TrimSpace(req.TaskID) != "") {
		return fmt.Errorf("herdr worktree request requires exactly one issue number or task id")
	}
	expected := filepath.Join(req.ProjectRoot, ".fanout", "worktrees", req.Slug)
	if filepath.Clean(req.WorktreePath) != filepath.Clean(expected) {
		return fmt.Errorf("herdr worktree path %s does not match slug %s", req.WorktreePath, req.Slug)
	}
	return nil
}

// managedWorktreeRequestIncomplete reports a request missing any field the
// worktree realization needs.
func managedWorktreeRequestIncomplete(req ManagedWorktreeRequest) bool {
	return strings.TrimSpace(req.Parent) == "" || req.ProjectRoot == "" || req.SourceRoot == "" ||
		req.Slug == "" || req.BranchName == "" || req.WorktreePath == "" ||
		req.ManagedSession == "" || req.SocketPath == ""
}

// managedWorktreeRequestHasIssueKey reports whether the request is keyed by an
// issue number: a real one, or the manual coordinator's negative sentinel.
func managedWorktreeRequestHasIssueKey(req ManagedWorktreeRequest) bool {
	return req.IssueNum > 0 ||
		canonicalManagedParent(req.Parent) == ManualParentRef && req.IssueNum < 0
}

func savedManagedCoordinatorPathMatches(ownerProjectRoot, savedPath, requestPath string) bool {
	savedPath = filepath.Clean(savedPath)
	if !filepath.IsAbs(savedPath) {
		return false
	}
	return ownerProjectRoot == "" || savedPath == filepath.Clean(requestPath)
}

func savedManagedWorktreePathValid(ownerProjectRoot, savedSlug, savedPath string) bool {
	savedPath = filepath.Clean(savedPath)
	if !filepath.IsAbs(savedPath) {
		return false
	}
	if ownerProjectRoot == "" {
		return true
	}
	worktreesDir := filepath.Dir(savedPath)
	fanoutDir := filepath.Dir(worktreesDir)
	if filepath.Base(savedPath) != savedSlug || filepath.Base(worktreesDir) != "worktrees" ||
		filepath.Base(fanoutDir) != ".fanout" {
		return false
	}
	savedRoot, err := filepath.EvalSymlinks(filepath.Dir(fanoutDir))
	return err == nil && filepath.Clean(savedRoot) == filepath.Clean(ownerProjectRoot)
}

func savedManagedWorktreeRepoMatches(
	ownerProjectRoot string,
	resource state.RuntimeResource,
	source worktree.RepoIdentity,
) bool {
	if resource.RepoKey != source.RepoKey {
		return false
	}
	return ownerProjectRoot == "" || resource.RepoRoot == source.RepoRoot
}
