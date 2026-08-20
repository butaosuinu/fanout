package panelaunch

import (
	"context"
	"errors"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func TestPruneAbsentRealizedManagedWorktreeRequiresResolvedCheckoutAndBranch(t *testing.T) {
	tests := []struct {
		name           string
		removeCheckout bool
		moveBranch     bool
		removeBranch   bool
		wantFound      bool
		wantBranch     bool
	}{
		{name: "checkout remains", wantFound: true, wantBranch: true},
		{name: "unchanged branch is deleted", removeCheckout: true},
		{
			name: "moved branch retains intent", removeCheckout: true, moveBranch: true,
			wantFound: true, wantBranch: true,
		},
		{name: "absent branch releases intent", removeCheckout: true, removeBranch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			runtime := &fakeManagedRealizeRuntime{}
			installSuccessfulManagedMutations(t, repo, runtime)
			hooks := deterministicManagedRealizeHooks()
			coordinator := realizeTestManagedCoordinator(t, repo, runtime, hooks)
			req := testManagedWorktreeRequest(repo, "prune-child", 710)
			result, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
			if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
				t.Fatal(err)
			}
			removeManagedTestIntent(t, repo, coordinator.ID)
			runtime.workspaces = nil
			if test.removeCheckout {
				gitCmdTest(t, repo, "worktree", "remove", req.WorktreePath)
			}
			if test.moveBranch {
				gitCmdTest(t, repo, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.invalid", "commit", "--allow-empty", "-m", "move branch")
				gitCmdTest(t, repo, "update-ref", result.Intent.FullBranchRef, "HEAD", result.Intent.BaseSHA)
			}
			if test.removeBranch {
				gitCmdTest(t, repo, "update-ref", "-d", result.Intent.FullBranchRef, result.Intent.BaseSHA)
			}
			pruneManagedTestIntents(t, repo, runtime.ObserveWorkspaces)
			journal, err := state.LoadLaunchJournal(repo)
			if err != nil {
				t.Fatal(err)
			}
			_, found := journal.FindIntent(result.Intent.ID)
			if found != test.wantFound {
				t.Fatalf("absentRealizedManagedIntents(removeCheckout=%t) found = %t, want %t", test.removeCheckout, found, test.wantFound)
			}
			_, branchFound, branchErr := worktree.ObserveBranch(
				context.Background(), repo, result.Intent.FullBranchRef,
			)
			if branchErr != nil {
				t.Fatal(branchErr)
			}
			if branchFound != test.wantBranch {
				t.Fatalf("releaseAbsentManagedIntents(%s) branch found = %t, want %t", test.name, branchFound, test.wantBranch)
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
		t.Fatalf("absentRealizedManagedIntents(snapshot failure) intents = %d, want 2", len(journal.Intents))
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
		var absent []state.LaunchIntent
		var allAbsent bool
		absent, allAbsent = absentRealizedManagedIntents(context.Background(), journal.LaunchJournal, observe)
		if allAbsent {
			err = releaseAbsentManagedIntents(context.Background(), journal, absent)
		}
	}
	err = errors.Join(err, locked.Unlock())
	if err != nil {
		t.Fatal(err)
	}
}

func removeManagedTestIntent(t *testing.T, repo, intentID string) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(repo)
	if err == nil && !journal.RemoveIntent(intentID) {
		err = errors.New("managed test intent not found")
	}
	if err == nil {
		err = journal.Save()
	}
	err = errors.Join(err, locked.Unlock())
	if err != nil {
		t.Fatal(err)
	}
}
