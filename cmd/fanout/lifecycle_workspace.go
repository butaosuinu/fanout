package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func runtimeLifecycleOptions(projectRoot, statePath string, hookConfig hooks.Config) lifecycle.Options {
	runtimeBackend := paneruntime.NewTmux()
	return lifecycle.Options{
		ProjectRoot:      projectRoot,
		StatePath:        statePath,
		Hooks:            hookConfig,
		CloseOwned:       runtimeBackend.CloseOwned,
		Relayout:         runtimeBackend.Relayout,
		WorkspaceRuntime: newWorkspaceLifecycleFactory(projectRoot),
	}
}

// newWorkspaceLifecycleFactory binds a saved row to the repository-owned
// session that can retire its workspace. Every recheck below is an admission
// gate, not a convenience: a row whose repository or owned route drifted names
// a workspace this run must not close.
func newWorkspaceLifecycleFactory(projectRoot string) lifecycle.WorkspaceRuntimeFactory {
	return func(ctx context.Context, pane state.Pane) (lifecycle.WorkspaceRuntime, error) {
		identity, err := worktree.ResolveRepoIdentity(ctx, projectRoot)
		if err != nil {
			return nil, err
		}
		if !workspaceLifecycleRepositoryMatches(pane, identity) {
			return nil, fmt.Errorf("%w: saved Herdr row belongs to a different repository", backend.ErrOwnedIdentityMismatch)
		}
		owned, err := paneruntime.Open(ctx, identity.RepoKey)
		if err != nil {
			return nil, err
		}
		if owned.Session != pane.SessionID || owned.SocketPath != pane.SocketPath {
			return nil, fmt.Errorf("%w: saved Herdr row belongs to a different owned route", backend.ErrOwnedIdentityMismatch)
		}
		return owned, nil
	}
}

func workspaceLifecycleRepositoryMatches(pane state.Pane, identity worktree.RepoIdentity) bool {
	// Checkout-free coordinators are bound to their persisted intent before
	// opening this route; their state rows deliberately have no Git provenance.
	if pane.IsShell() && pane.RepoKey == "" && pane.RepoRoot == "" {
		return filepath.Clean(pane.WorktreePath) == identity.RepoRoot
	}
	return filepath.Clean(pane.RepoKey) == identity.RepoKey &&
		filepath.Clean(pane.RepoRoot) == identity.RepoRoot
}
