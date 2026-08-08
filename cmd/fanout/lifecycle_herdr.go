package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func runtimeLifecycleOptions(projectRoot, statePath string, hookConfig hooks.Config) lifecycle.Options {
	runtimeBackend := tmuxbackend.New()
	return lifecycle.Options{
		ProjectRoot:  projectRoot,
		StatePath:    statePath,
		Hooks:        hookConfig,
		CloseOwned:   runtimeBackend.CloseOwned,
		HerdrRuntime: newHerdrLifecycleFactory(projectRoot),
	}
}

func newHerdrLifecycleFactory(projectRoot string) lifecycle.HerdrRuntimeFactory {
	return func(ctx context.Context, pane state.Pane) (lifecycle.HerdrRuntime, error) {
		identity, err := worktree.ResolveRepoIdentity(ctx, projectRoot)
		if err != nil {
			return nil, err
		}
		if filepath.Clean(pane.HerdrRepoKey) != identity.RepoKey ||
			filepath.Clean(pane.HerdrRepoRoot) != identity.RepoRoot {
			return nil, fmt.Errorf("saved Herdr row belongs to a different repository")
		}
		owned, err := herdrrun.OpenOwned(ctx, herdrrun.OwnedOptions{GitCommonDir: identity.RepoKey})
		if err != nil {
			return nil, err
		}
		if owned.Session != pane.HerdrSession || owned.SocketPath != pane.HerdrSocketPath {
			return nil, fmt.Errorf("saved Herdr row belongs to a different owned route")
		}
		return owned, nil
	}
}
