package panelaunch

import (
	"context"
	"errors"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestPruneAbsentRealizedManagedWorktreeRequiresAbsentCheckout(t *testing.T) {
	tests := []struct {
		name           string
		removeCheckout bool
		wantFound      bool
	}{
		{name: "checkout remains", wantFound: true},
		{name: "checkout absent", removeCheckout: true, wantFound: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			runtime := &fakeManagedRealizeRuntime{}
			installSuccessfulManagedMutations(t, repo, runtime)
			hooks := deterministicManagedRealizeHooks()
			realizeTestManagedCoordinator(t, repo, runtime, hooks)
			req := testManagedWorktreeRequest(repo, "prune-child", 710)
			result, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
			if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
				t.Fatal(err)
			}
			runtime.workspaces = runtime.workspaces[:1]
			if test.removeCheckout {
				gitCmdTest(t, repo, "worktree", "remove", req.WorktreePath)
			}
			pruneManagedTestIntents(t, repo, runtime.ObserveWorkspaces)
			journal, err := state.LoadLaunchJournal(repo)
			if err != nil {
				t.Fatal(err)
			}
			_, found := journal.FindIntent(result.Intent.ID)
			if found != test.wantFound {
				t.Fatalf("pruneAbsentRealizedManagedIntents(removeCheckout=%t) found = %t, want %t", test.removeCheckout, found, test.wantFound)
			}
		})
	}
}

func TestPruneAbsentRealizedManagedIntentsRetainsAllOnSnapshotFailure(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	req := testManagedWorktreeRequest(repo, "snapshot-failure", 711)
	if _, err := realizeManagedWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	pruneManagedTestIntents(t, repo, func(context.Context) ([]backend.WorkspaceObservation, error) {
		return nil, errors.New("snapshot unavailable")
	})
	journal, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Intents) != 2 {
		t.Fatalf("pruneAbsentRealizedManagedIntents(snapshot failure) intents = %d, want 2", len(journal.Intents))
	}
}

func pruneManagedTestIntents(
	t *testing.T,
	repo string,
	observe func(context.Context) ([]backend.WorkspaceObservation, error),
) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(repo)
	if err == nil {
		err = pruneAbsentRealizedManagedIntents(context.Background(), journal, observe)
	}
	err = errors.Join(err, locked.Unlock())
	if err != nil {
		t.Fatal(err)
	}
}
