package herdrrun

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

// BindOwnedWorkspaceClose admits an exact generic workspace on this session and
// returns the closer bound to it.
func (s *OwnedSession) BindOwnedWorkspaceClose(
	target corebackend.OwnedPaneIdentity,
) (corebackend.OwnedClosingBackend, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("herdr owned session is nil")
	}
	bound, err := s.backend.BindOwnedWorkspaceClose(target)
	if err != nil {
		return nil, err
	}
	return bound, nil
}

// CloseAttachedWorkspace revalidates a worktree-backed attached agent under
// the owned mutation lock, then closes only its exact workspace generation.
func (s *OwnedSession) CloseAttachedWorkspace(
	ctx context.Context,
	binding corebackend.PaneBinding,
) error {
	if s == nil || s.backend == nil {
		return mutationNotIssued(fmt.Errorf("herdr owned session is nil"))
	}
	if binding.Shell || strings.TrimSpace(binding.Agent) == "" ||
		strings.TrimSpace(binding.WorktreePath) == "" {
		return mutationNotIssued(fmt.Errorf("%w: attached workspace binding is incomplete", corebackend.ErrOwnedIdentityMismatch))
	}
	return s.backend.closeOwnedAttachedWorkspace(ctx, attachedTargetFromBinding(binding))
}

// VerifyAttachedWorkspaceClose runs the same immutable admission as
// CloseAttachedWorkspace without issuing the close mutation.
func (s *OwnedSession) VerifyAttachedWorkspaceClose(
	ctx context.Context,
	binding corebackend.PaneBinding,
) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("herdr owned session is nil")
	}
	if binding.Shell || strings.TrimSpace(binding.Agent) == "" ||
		strings.TrimSpace(binding.WorktreePath) == "" {
		return fmt.Errorf("%w: attached workspace binding is incomplete", corebackend.ErrOwnedIdentityMismatch)
	}
	return s.backend.verifyOwnedAttachedWorkspaceClose(ctx, attachedTargetFromBinding(binding))
}

func attachedTargetFromBinding(binding corebackend.PaneBinding) corebackend.OwnedPaneIdentity {
	target := ownedTargetFromBinding(binding)
	if binding.RepoKey == "" {
		// Generic attached workspaces record cwd, not ownership of the checkout.
		target.WorktreePath = ""
	}
	return target
}

// BindOwnedWorkspaceClose admits an exact generic workspace for close. It is
// limited to console/coordinator workspaces without linked worktrees; linked
// worktree close must retain the stronger ownership proof used by BindOwnedClose.
func (b *Backend) BindOwnedWorkspaceClose(target corebackend.OwnedPaneIdentity) (*Backend, error) {
	if target.RepoKey != "" || target.WorktreePath != "" {
		return nil, fmt.Errorf("%w: generic workspace close cannot own a checkout", corebackend.ErrOwnedIdentityMismatch)
	}
	bound, err := b.bindOwnedTarget(target, nil)
	if err != nil {
		return nil, err
	}
	bound.target.workspaceClose = true
	bound.target.closeFingerprint = corebackend.CloseRequest{Ref: corebackend.PaneRef{
		Backend: corebackend.Herdr,
		Pane:    target.Ref.Pane,
	}}
	return bound, nil
}

func (b *Backend) closeOwnedWorkspace(ctx context.Context, saved corebackend.OwnedPaneIdentity) (corebackend.CloseResult, error) {
	failed := corebackend.CloseResult{Status: corebackend.CloseFailed}
	notIssued := func(err error) error { return fmt.Errorf("%w: %w", corebackend.ErrOwnedMutationNotIssued, err) }
	err := b.withOwnedAdmission(ctx, ownedMutationLane, notIssued, func(call ownedCall) error {
		target, probed, view, err := call.resolveOwnedTargetView(ctx, saved)
		if err != nil {
			return notIssued(err)
		}
		if err := verifyGenericWorkspaceCheckout(view, target); err != nil {
			return notIssued(err)
		}
		if err := verifyWorkspaceClosePanes(view, target.Ref); err != nil {
			return notIssued(err)
		}
		if err := verifyWorkspaceCloseGroup(view, target.Ref.Workspace); err != nil {
			return notIssued(err)
		}
		return call.issueAndVerifyWorkspaceClose(ctx, probed, target.Ref.Workspace)
	})
	if err != nil {
		return failed, err
	}
	return corebackend.CloseResult{Status: corebackend.CloseConfirmed}, nil
}

func verifyGenericWorkspaceCheckout(view ownedSnapshotView, target corebackend.OwnedPaneIdentity) error {
	workspace := view.workspaces[target.Ref.Workspace]
	if workspace.isLinked || workspace.worktreePath != "" &&
		(filepath.Clean(workspace.worktreePath) != filepath.Clean(workspace.repoRoot) ||
			filepath.Clean(workspace.worktreePath) != filepath.Clean(target.CurrentPath)) {
		return fmt.Errorf("%w: generic workspace close cannot own a checkout", corebackend.ErrOwnedIdentityMismatch)
	}
	return nil
}

func verifyWorkspaceCloseGroup(view ownedSnapshotView, workspaceID string) error {
	target := view.workspaces[workspaceID]
	if target.repoKey == "" {
		return nil
	}
	for id, workspace := range view.workspaces {
		if id != workspaceID && workspace.repoKey == target.repoKey {
			return fmt.Errorf("%w: workspace close would also close repository member %s",
				corebackend.ErrOwnedIdentityMismatch, id)
		}
	}
	return nil
}

func verifyWorkspaceClosePanes(view ownedSnapshotView, target corebackend.PaneRef) error {
	if view.workspaceContainsOnly(target) {
		return nil
	}
	return fmt.Errorf(
		"%w: workspace %s contains a pane outside target %s",
		corebackend.ErrOwnedWorkspaceHasUnadmittedPane, target.Workspace, target.Pane,
	)
}

func (c ownedCall) issueAndVerifyWorkspaceClose(
	ctx context.Context,
	probed probeResult,
	workspaceID string,
) error {
	if _, err := c.b.runContext(ctx, commandTimeout, probed.binary, probed.route, "workspace", "close", workspaceID); err != nil {
		return methodUnavailable("workspace.close")
	}
	view, err := c.ownedSnapshotView(ctx)
	if err != nil {
		return err
	}
	if view.workspacePresent(workspaceID) {
		return fmt.Errorf("herdr workspace close returned success but workspace remains live")
	}
	return nil
}

func (b *Backend) closeOwnedAttachedWorkspace(
	ctx context.Context,
	saved corebackend.OwnedPaneIdentity,
) error {
	ctx, cancel := context.WithTimeout(ctx, 8*commandTimeout)
	defer cancel()
	return b.withOwnedAdmission(ctx, ownedMutationLane, mutationNotIssued, func(call ownedCall) error {
		target, probed, err := call.resolveAttachedWorkspaceCloseTarget(ctx, saved)
		if err != nil {
			return mutationNotIssued(err)
		}
		if issueErr := b.issueAttachedWorkspaceClose(ctx, probed, target.Ref.Workspace); issueErr != nil {
			return issueErr
		}
		view, err := call.ownedSnapshotView(ctx)
		if err != nil {
			return err
		}
		if view.workspacePresent(target.Ref.Workspace) {
			return fmt.Errorf("herdr attached workspace close returned success but workspace remains live")
		}
		return nil
	})
}

func (b *Backend) verifyOwnedAttachedWorkspaceClose(
	ctx context.Context,
	target corebackend.OwnedPaneIdentity,
) error {
	return b.withOwnedAdmission(ctx, ownedMutationLane, nil, func(call ownedCall) error {
		_, _, err := call.resolveAttachedWorkspaceCloseTarget(ctx, target)
		return err
	})
}

func (b *Backend) issueAttachedWorkspaceClose(
	ctx context.Context,
	probed probeResult,
	workspaceID string,
) error {
	callCtx, callCancel := context.WithTimeout(ctx, commandTimeout)
	defer callCancel()
	out, err := b.runWorktreeMutation(
		callCtx, probed.binary, probed.route, "workspace", "close", workspaceID,
	)
	if err != nil {
		if rejected, ok := decodeMutationRejection(out, err, "cli:workspace:close"); ok {
			return rejected
		}
		return err
	}
	return nil
}

func (c ownedCall) resolveAttachedWorkspaceCloseTarget(
	ctx context.Context,
	expected corebackend.OwnedPaneIdentity,
) (corebackend.OwnedPaneIdentity, probeResult, error) {
	if err := validateSavedTarget(expected, c.admission); err != nil {
		return corebackend.OwnedPaneIdentity{}, probeResult{}, err
	}
	view, err := c.ownedSnapshotView(ctx)
	if err != nil {
		return corebackend.OwnedPaneIdentity{}, probeResult{}, err
	}
	current, live := view.find(expected.Ref)
	if verifyErr := verifyAttachedWorkspaceCloseSnapshot(view, expected, current, live); verifyErr != nil {
		return corebackend.OwnedPaneIdentity{}, probeResult{}, verifyErr
	}
	probed, err := c.probe(ctx)
	return cloneOwnedPaneIdentity(expected), probed, err
}

func verifyAttachedWorkspaceCloseSnapshot(
	view ownedSnapshotView,
	expected corebackend.OwnedPaneIdentity,
	current ownedPaneView,
	live bool,
) error {
	if err := verifyAttachedWorkspaceScope(view, expected); err != nil {
		return err
	}
	if live && !ownedPaneMatches(expected, current) {
		return fmt.Errorf("%w: saved attached target identity changed", corebackend.ErrOwnedIdentityMismatch)
	}
	if live {
		return verifyWorkspaceClosePanes(view, expected.Ref)
	}
	if !view.paneLessAttachedWorkspaceMatches(expected) {
		return fmt.Errorf("%w: saved attached target is neither live nor a matching pane-less workspace", corebackend.ErrOwnedIdentityMismatch)
	}
	return nil
}

func verifyAttachedWorkspaceScope(view ownedSnapshotView, expected corebackend.OwnedPaneIdentity) error {
	for id, workspace := range view.workspaces {
		if id != expected.Ref.Workspace && workspace.label == expected.WorkspaceLabel {
			return fmt.Errorf("%w: attached workspace label is ambiguous", corebackend.ErrOwnedIdentityMismatch)
		}
	}
	if !view.workspaces[expected.Ref.Workspace].isLinked {
		return verifyWorkspaceCloseGroup(view, expected.Ref.Workspace)
	}
	return nil
}
