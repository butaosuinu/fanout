package panelaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// managedTestMutation flattens the typed mutation requests into one record so
// assertions can inspect every issued mutation uniformly.
type managedTestMutation struct {
	Kind                     backend.WorktreeMutationKind
	Coordinator              backend.WorkspaceObservation
	SourceRoot               string
	SourceRepoKey            string
	SourceRepoRoot           string
	CWD                      string
	Branch                   string
	Base                     string
	Path                     string
	Label                    string
	ExpectedAlreadyOpenID    string
	ExpectedAlreadyOpenLabel string
}

type fakeManagedRealizeRuntime struct {
	workspaces []backend.WorkspaceObservation
	mutations  []managedTestMutation
	route      backend.OwnedWorktreeRoute
	routeErr   error

	routeDeadline      time.Time
	routeHasDeadline   bool
	routeCalls         int
	observeDeadline    time.Time
	observeHasDeadline bool
	policyErr          error
	observeErr         error
	observeCalls       int
	mutate             func(managedTestMutation) (backend.WorktreeMutationResult, error)
}

func (f *fakeManagedRealizeRuntime) WorktreeRoute(
	ctx context.Context,
) (backend.OwnedWorktreeRoute, error) {
	f.routeCalls++
	f.routeDeadline, f.routeHasDeadline = ctx.Deadline()
	return f.route, f.routeErr
}

func (f *fakeManagedRealizeRuntime) VerifyWorktreeSetupPolicy(context.Context) error {
	return f.policyErr
}

func (f *fakeManagedRealizeRuntime) ObserveWorkspaces(ctx context.Context) ([]backend.WorkspaceObservation, error) {
	f.observeCalls++
	f.observeDeadline, f.observeHasDeadline = ctx.Deadline()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]backend.WorkspaceObservation(nil), f.workspaces...), f.observeErr
}

func (f *fakeManagedRealizeRuntime) CreateWorkspace(
	_ context.Context,
	req backend.WorkspaceCreateRequest,
) (backend.WorktreeMutationResult, error) {
	return f.record(managedTestMutation{
		Kind: backend.WorkspaceCreate, CWD: req.CWD,
		SourceRoot: req.CWD, SourceRepoKey: req.SourceRepoKey, SourceRepoRoot: req.CWD,
		Label: req.Label,
	})
}

func (f *fakeManagedRealizeRuntime) CreateWorktree(
	_ context.Context,
	req backend.WorktreeCreateRequest,
) (backend.WorktreeMutationResult, error) {
	return f.record(managedTestMutation{
		Kind: backend.WorktreeCreate, Coordinator: req.Coordinator,
		SourceRoot: req.SourceRepoRoot, SourceRepoKey: req.SourceRepoKey,
		SourceRepoRoot: req.SourceRepoRoot,
		Branch:         req.Branch, Base: req.Base, Path: req.Path, Label: req.Label,
	})
}

func (f *fakeManagedRealizeRuntime) OpenWorktree(
	_ context.Context,
	req backend.WorktreeOpenRequest,
) (backend.WorktreeMutationResult, error) {
	return f.record(managedTestMutation{
		Kind: backend.WorktreeOpen, Coordinator: req.Coordinator,
		SourceRoot: req.SourceRepoRoot, SourceRepoKey: req.SourceRepoKey,
		SourceRepoRoot: req.SourceRepoRoot,
		Path:           req.Path, Label: req.Label,
		ExpectedAlreadyOpenID:    req.ExpectedAlreadyOpenID,
		ExpectedAlreadyOpenLabel: req.ExpectedAlreadyOpenLabel,
	})
}

func (f *fakeManagedRealizeRuntime) record(
	m managedTestMutation,
) (backend.WorktreeMutationResult, error) {
	f.mutations = append(f.mutations, m)
	if f.mutate == nil {
		return backend.WorktreeMutationResult{}, errors.New("unexpected mutation")
	}
	return f.mutate(m)
}

func realizeManagedCoordinator(
	ctx context.Context,
	req ManagedCoordinatorRequest,
	runtime ManagedWorktreeRuntime,
	hooks ManagedRealizeHooks,
) (ManagedRealizeResult, error) {
	locked, err := state.LockProjectForLaunch(req.ProjectRoot)
	if err != nil {
		return ManagedRealizeResult{}, err
	}
	result, realizeErr := RealizeManagedCoordinator(ctx, req, runtime, locked, hooks)
	return result, errors.Join(realizeErr, locked.Unlock())
}

func realizeManagedWorktree(
	ctx context.Context,
	req ManagedWorktreeRequest,
	runtime ManagedWorktreeRuntime,
	hooks ManagedRealizeHooks,
) (ManagedRealizeResult, error) {
	locked, err := state.LockProjectForLaunch(req.ProjectRoot)
	if err != nil {
		return ManagedRealizeResult{}, err
	}
	result, realizeErr := RealizeManagedWorktree(ctx, req, runtime, locked, hooks)
	return result, errors.Join(realizeErr, locked.Unlock())
}

func TestRealizeManagedWorktreePersistsIntentAndSkipsReplay(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	coordinator := realizeTestManagedCoordinator(t, repo, runtime, hooks)

	req := testManagedWorktreeRequest(repo, "child", 426)
	result, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("realize worktree error = %v", err)
	}
	if result.Intent.Status != state.IntentRealized || !result.Intent.BranchCreated ||
		result.Intent.Resource.WorkspaceID != "w2" || result.Pane.Pane != "w2:p1" {
		t.Fatalf("realized result = %+v", result)
	}
	if result.Intent.Coordinator != coordinator.Resource {
		t.Fatalf("saved coordinator = %+v, want %+v", result.Intent.Coordinator, coordinator.Resource)
	}
	if len(runtime.mutations) != 2 {
		t.Fatalf("mutations = %d, want coordinator + child", len(runtime.mutations))
	}
	if runtime.mutations[1].Base == "" {
		t.Fatal("fresh branch worktree create omitted immutable base")
	}

	replayed, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("replay error = %v", err)
	}
	if replayed.Intent.ID != result.Intent.ID || len(runtime.mutations) != 2 {
		t.Fatalf("replay = %+v, mutations = %d; request was reissued", replayed, len(runtime.mutations))
	}
	runtime.route.GitCommonDir = filepath.Join(repo, "foreign.git")
	_, err = realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if err == nil {
		t.Fatal("foreign owned-session route unexpectedly accepted")
	}
}

func TestManagedCoordinatorWorkspaceLabelUsesLaunchNonce(t *testing.T) {
	nonce := strings.Repeat("a", 32)
	label, err := managedCoordinatorWorkspaceLabel(ManagedCoordinatorRequest{
		Parent: ManagedConsoleRuntimeParent,
		Launch: &state.LaunchCapsule{Nonce: nonce},
	}, func() (string, error) {
		t.Fatal("random label source called for an operation-bound coordinator")
		return "", nil
	})
	if err != nil || label != "fanout-console-"+nonce {
		t.Fatalf("console label = %q, %v", label, err)
	}
}

func TestRealizeManagedWorktreeLeavesIssuedIntentAfterRealizedSaveFailure(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	controlPath, err := state.LaunchJournalPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Dir(controlPath)
	t.Cleanup(func() {
		if chmodErr := os.Chmod(controlDir, 0o700); chmodErr != nil {
			t.Errorf("restore Herdr control directory mode: %v", chmodErr)
		}
	})
	successfulMutate := runtime.mutate
	runtime.mutate = func(
		mutationReq managedTestMutation,
	) (backend.WorktreeMutationResult, error) {
		result, mutateErr := successfulMutate(mutationReq)
		if mutateErr == nil && mutationReq.Kind == backend.WorktreeCreate {
			if chmodErr := os.Chmod(controlDir, 0o500); chmodErr != nil {
				t.Fatalf("make Herdr control directory read-only: %v", chmodErr)
			}
		}
		return result, mutateErr
	}

	req := testManagedWorktreeRequest(repo, "realized-save-failure", 426)
	_, err = realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, errManagedRealizedIntentSave) ||
		errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("realized save error = %v", err)
	}
	if chmodErr := os.Chmod(controlDir, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	intent, found := loadManagedWorktreeIntent(t, repo, req)
	if !found || intent.Status != state.IntentIssued ||
		intent.Resource.WorkspaceID != "" || intent.Failure != "" {
		t.Fatalf("persisted intent after realized save failure = (%+v,%t)", intent, found)
	}
}

func TestRealizeManagedWorktreeReopensVerifiedRealizedCheckout(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	req := testManagedWorktreeRequest(repo, "reopen", 426)
	realized, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	runtime.workspaces = runtime.workspaces[:1]
	mutationsBefore := len(runtime.mutations)

	reopened, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("reopen error = %v", err)
	}
	if len(runtime.mutations) != mutationsBefore+1 ||
		runtime.mutations[len(runtime.mutations)-1].Kind != backend.WorktreeOpen {
		t.Fatalf("reopen mutations = %+v", runtime.mutations[mutationsBefore:])
	}
	if reopened.Intent.Status != state.IntentRealized ||
		reopened.Intent.Resource.WorkspaceID == realized.Intent.Resource.WorkspaceID ||
		reopened.Intent.Resource.Label != realized.Intent.Resource.Label {
		t.Fatalf("reopened intent = %+v, original = %+v", reopened.Intent, realized.Intent)
	}
}

func TestRealizeManagedWorktreeKeepsRejectedOpenRetryable(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	req := testManagedWorktreeRequest(repo, "reopen-rejected", 427)
	realized, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	runtime.workspaces = runtime.workspaces[:1]
	successfulMutate := runtime.mutate
	runtime.mutate = func(
		mutationReq managedTestMutation,
	) (backend.WorktreeMutationResult, error) {
		if mutationReq.Kind == backend.WorktreeOpen {
			return backend.WorktreeMutationResult{}, backend.MutationRejectedError{
				Code: "worktree_open_failed", Message: "rejected before open",
			}
		}
		return successfulMutate(mutationReq)
	}

	_, err = realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, backend.ErrMutationRejected) {
		t.Fatalf("rejected reopen error = %v", err)
	}
	control, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := control.FindIntent(realized.Intent.ID)
	if !found || intent.Status != state.IntentRealized || intent.Failure != "" {
		t.Fatalf("rejected reopen intent = (%+v,%t), want retryable realized", intent, found)
	}

	runtime.mutate = successfulMutate
	reopened, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		reopened.Intent.Status != state.IntentRealized {
		t.Fatalf("retry reopen result = %+v, err=%v", reopened, err)
	}
}

func TestRealizeManagedRoutesCapTotalTimeout(t *testing.T) {
	for _, kind := range []string{"coordinator", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			stop := errors.New("stop after route")
			runtime := &fakeManagedRealizeRuntime{routeErr: stop}
			now := time.Now().UTC()
			hooks := ManagedRealizeHooks{Now: func() time.Time { return now }}

			var err error
			switch kind {
			case "coordinator":
				req := testManagedCoordinatorRequest(repo)
				_, err = realizeManagedCoordinator(context.Background(), req, runtime, hooks)
			case "worktree":
				req := testManagedWorktreeRequest(repo, "route-timeout", 426)
				_, err = realizeManagedWorktree(context.Background(), req, runtime, hooks)
			}
			if !errors.Is(err, stop) {
				t.Fatalf("route error = %v", err)
			}
			remaining := time.Until(runtime.routeDeadline)
			if !runtime.routeHasDeadline || remaining <= 0 ||
				remaining > maxManagedRecoveryClassificationTimeout {
				t.Fatalf(
					"route deadline = %v, %t (remaining %v), want at most %v from %v",
					runtime.routeDeadline,
					runtime.routeHasDeadline,
					remaining,
					maxManagedRecoveryClassificationTimeout,
					now,
				)
			}
		})
	}
}

func TestRealizeManagedFreshCancellationBeforeRouteDoesNotCreateIntent(t *testing.T) {
	for _, kind := range []string{"coordinator", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			runtime := &fakeManagedRealizeRuntime{}
			installSuccessfulManagedMutations(t, repo, runtime)
			baseHooks := deterministicManagedRealizeHooks()
			now := baseHooks.Now()
			ctx, cancel := context.WithCancel(context.Background())
			hooks := baseHooks
			hooks.Now = func() time.Time {
				cancel()
				return now
			}

			var err error
			switch kind {
			case "coordinator":
				_, err = realizeManagedCoordinator(ctx, testManagedCoordinatorRequest(repo), runtime, hooks)
			case "worktree":
				_, err = realizeManagedWorktree(
					ctx,
					testManagedWorktreeRequest(repo, "canceled-before-route", 439),
					runtime,
					hooks,
				)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("fresh cancellation error = %v", err)
			}
			if runtime.routeCalls != 0 {
				t.Fatalf("route calls = %d, want 0", runtime.routeCalls)
			}
			control, loadErr := state.LoadLaunchJournal(repo)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if len(control.Intents) != 0 {
				t.Fatalf("control after fresh cancellation = %+v", control)
			}
		})
	}
}

func TestRealizeManagedCoordinatorBoundsExpiredRouteClassification(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realized := realizeTestManagedCoordinator(t, repo, runtime, hooks)

	mutateManagedTestIntent(t, repo, realized.ID, func(intent *state.LaunchIntent) {
		intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	})

	if _, err := realizeManagedCoordinator(
		context.Background(),
		testManagedCoordinatorRequest(repo),
		runtime,
		hooks,
	); !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("expired coordinator recovery error = %v", err)
	}
	assertManagedRecoveryClassificationDeadline(
		t,
		"expired coordinator route",
		runtime.routeDeadline,
		runtime.routeHasDeadline,
	)
}

func TestRealizeManagedWorktreeUsesSavedRouteDeadline(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	req := testManagedWorktreeRequest(repo, "saved-route-deadline", 438)
	realized, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	mutateManagedTestIntent(t, repo, realized.Intent.ID, func(intent *state.LaunchIntent) {
		intent.ExpiresUnixMS = hooks.Now().Add(2 * time.Second).UnixMilli()
	})

	_, err = realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("saved-deadline retry error = %v", err)
	}
	remaining := time.Until(runtime.routeDeadline)
	if !runtime.routeHasDeadline || remaining <= 0 || remaining > 2*time.Second {
		t.Fatalf(
			"saved route deadline = %v, %t (remaining %v)",
			runtime.routeDeadline,
			runtime.routeHasDeadline,
			remaining,
		)
	}
}

func TestRealizeManagedRollsBackMutationNotIssued(t *testing.T) {
	t.Run("coordinator", func(t *testing.T) {
		repo := newManagedRealizeRepo(t)
		runtime := &fakeManagedRealizeRuntime{}
		installSuccessfulManagedMutations(t, repo, runtime)
		runtime.mutate = func(
			managedTestMutation,
		) (backend.WorktreeMutationResult, error) {
			return backend.WorktreeMutationResult{}, backend.MutationNotIssuedError{
				Cause: errors.New("owned admission failed"),
			}
		}

		_, err := realizeManagedCoordinator(
			context.Background(),
			testManagedCoordinatorRequest(repo),
			runtime,
			deterministicManagedRealizeHooks(),
		)
		if !errors.Is(err, backend.ErrMutationNotIssued) {
			t.Fatalf("coordinator error = %v", err)
		}
		control, err := state.LoadLaunchJournal(repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(control.Intents) != 0 {
			t.Fatalf("coordinator intents = %#v, want rollback", control.Intents)
		}
	})

	t.Run("worktree", func(t *testing.T) {
		repo := newManagedRealizeRepo(t)
		runtime := &fakeManagedRealizeRuntime{}
		installSuccessfulManagedMutations(t, repo, runtime)
		hooks := deterministicManagedRealizeHooks()
		realizeTestManagedCoordinator(t, repo, runtime, hooks)
		runtime.mutate = func(
			managedTestMutation,
		) (backend.WorktreeMutationResult, error) {
			return backend.WorktreeMutationResult{}, backend.MutationNotIssuedError{
				Cause: errors.New("owned admission failed"),
			}
		}

		req := testManagedWorktreeRequest(repo, "not-issued", 426)
		_, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
		if !errors.Is(err, backend.ErrMutationNotIssued) {
			t.Fatalf("worktree error = %v", err)
		}
		fullRef, err := worktree.LocalBranchRef(context.Background(), repo, req.BranchName)
		if err != nil {
			t.Fatal(err)
		}
		if head, found, observeErr := worktree.ObserveBranch(context.Background(), repo, fullRef); observeErr != nil {
			t.Fatal(observeErr)
		} else if found {
			t.Fatalf("not-issued branch = %s, want rollback", head)
		}
		control, err := state.LoadLaunchJournal(repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(control.Intents) != 1 ||
			control.Intents[0].Kind != state.IntentCoordinator {
			t.Fatalf("worktree intents = %#v, want coordinator only", control.Intents)
		}
	})
}

func TestRealizeManagedWorktreeRecoversCompletedUnissuedRollback(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	successfulMutate := runtime.mutate
	ctx, cancel := context.WithCancel(context.Background())
	runtime.mutate = func(
		req managedTestMutation,
	) (backend.WorktreeMutationResult, error) {
		if req.Kind == backend.WorktreeCreate {
			cancel()
			return backend.WorktreeMutationResult{}, context.DeadlineExceeded
		}
		return successfulMutate(req)
	}
	req := testManagedWorktreeRequest(repo, "rollback-recovery", 428)
	_, err := realizeManagedWorktree(ctx, req, runtime, hooks)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted rollback setup error = %v", err)
	}
	intent, found := loadManagedWorktreeIntent(t, repo, req)
	if !found || intent.Status != state.IntentIssued || !intent.BranchCreated {
		t.Fatalf("interrupted rollback intent = (%+v,%t)", intent, found)
	}
	if deleteErr := worktree.DeleteReservedBranch(
		context.Background(),
		repo,
		intent.FullBranchRef,
		intent.BaseSHA,
	); deleteErr != nil {
		t.Fatal(deleteErr)
	}

	runtime.mutate = successfulMutate
	mutationsBefore := len(runtime.mutations)
	_, err = realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if err == nil || !strings.Contains(err.Error(), "rollback; retry launch") {
		t.Fatalf("completed rollback recovery error = %v", err)
	}
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("completed rollback recovery reissued a mutation")
	}
	if _, found := loadManagedWorktreeIntent(t, repo, req); found {
		t.Fatal("completed rollback recovery kept the issued intent")
	}

	relaunched, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		relaunched.Intent.Status != state.IntentRealized {
		t.Fatalf("fresh launch after rollback recovery = %+v, err=%v", relaunched, err)
	}
}

func TestRealizeManagedWorktreeResumesPlannedLaunchRollback(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	req := testManagedWorktreeRequest(repo, "planned-launch-rollback", 535)
	result, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	persistInterruptedManagedLaunchRollback(t, repo, result.Intent, state.IntentPlanned)

	mutationsBefore := len(runtime.mutations)
	resumed, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) || resumed.Intent.ID != result.Intent.ID {
		t.Fatalf("planned rollback resume = %+v, err=%v", resumed, err)
	}
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("planned launch rollback recovery reissued a workspace mutation")
	}
	control, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	rollbackID, _ := state.RollbackIntentID(result.Intent.ID)
	if _, found := control.FindIntent(rollbackID); found {
		t.Fatal("planned launch rollback intent remains after resume")
	}
}

func TestRealizeManagedWorktreeClassifiesIssuedLaunchRollbackWithoutReissue(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	req := testManagedWorktreeRequest(repo, "issued-launch-rollback", 536)
	result, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	persistInterruptedManagedLaunchRollback(t, repo, result.Intent, state.IntentIssued)
	mutationsBefore := len(runtime.mutations)

	_, err = realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("live issued rollback error = %v, want manual cleanup", err)
	}
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("live issued launch rollback recovery reissued a workspace mutation")
	}
	control, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	rollbackID, _ := state.RollbackIntentID(result.Intent.ID)
	rollback, found := control.FindIntent(rollbackID)
	if !found || rollback.Status != state.IntentManualCleanupRequired {
		t.Fatalf("issued rollback = (%+v,%t), want manual cleanup", rollback, found)
	}
}

func TestRealizeManagedWorktreeCompletesAbsentIssuedLaunchRollback(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	req := testManagedWorktreeRequest(repo, "absent-launch-rollback", 537)
	result, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatal(err)
	}
	persistInterruptedManagedLaunchRollback(t, repo, result.Intent, state.IntentIssued)
	gitCmdTest(t, repo, "worktree", "remove", result.Intent.WorktreePath)
	kept := runtime.workspaces[:0]
	for _, workspace := range runtime.workspaces {
		if workspace.WorkspaceID != result.Intent.Resource.WorkspaceID {
			kept = append(kept, workspace)
		}
	}
	runtime.workspaces = kept
	mutationsBefore := len(runtime.mutations)

	relaunched, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		relaunched.Intent.Resource.WorkspaceID == result.Intent.Resource.WorkspaceID {
		t.Fatalf("absent issued rollback relaunch = %+v, err=%v", relaunched, err)
	}
	if len(runtime.mutations) != mutationsBefore+1 {
		t.Fatalf("workspace mutations = %d, want one fresh launch after classification", len(runtime.mutations)-mutationsBefore)
	}
	control, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	rollbackID, _ := state.RollbackIntentID(result.Intent.ID)
	if _, found := control.FindIntent(rollbackID); found {
		t.Fatal("issued launch rollback intent remains after absence classification")
	}
}

func persistInterruptedManagedLaunchRollback(
	t *testing.T,
	repo string,
	intent state.LaunchIntent,
	status state.LaunchIntentStatus,
) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(repo)
	if err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	rollback, err := beginManagedLaunchRollback(journal, intent, errors.New("interrupted launch"))
	if err == nil && status == state.IntentIssued {
		rollback.Status = status
		journal.UpsertIntent(rollback)
		err = journal.Save()
	}
	if unlockErr := locked.Unlock(); err == nil {
		err = unlockErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestRealizeManagedWorktreeChecksPolicyBeforeBranchReservation(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	runtime.policyErr = errors.New("unexpected owned plugin")

	req := testManagedWorktreeRequest(repo, "policy-blocked", 426)
	_, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if err == nil || !strings.Contains(err.Error(), "unexpected owned plugin") {
		t.Fatalf("policy error = %v", err)
	}
	fullRef, err := worktree.LocalBranchRef(context.Background(), repo, req.BranchName)
	if err != nil {
		t.Fatal(err)
	}
	if head, found, observeErr := worktree.ObserveBranch(context.Background(), repo, fullRef); observeErr != nil {
		t.Fatal(observeErr)
	} else if found {
		t.Fatalf("policy-blocked branch = %s, want absent", head)
	}
	intent, found := loadManagedWorktreeIntent(t, repo, req)
	if !found || intent.Status != state.IntentPlanned || intent.BranchCreated {
		t.Fatalf("policy-blocked intent = %+v, found=%t", intent, found)
	}
}

// The launch-lock binding recheck lives in cmd's backendSelectionVerifier;
// realize only resolves the runtime parent from the plan spec source.
func TestResolveManagedRuntimeParentUsesIssueSourcedPlanSpec(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	planDir := filepath.Join(repo, ".fanout", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(planDir, "demo.json"),
		[]byte(`{"version":1,"plan":{"slug":"demo","title":"Demo","source":"issue #425"},"tasks":[]}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	runtimeParent, err := resolveManagedRuntimeParent(
		repo,
		"plan:demo",
		repo,
		state.LaunchJournal{},
	)
	if err != nil || runtimeParent != "425" {
		t.Fatalf("resolveManagedRuntimeParent(plan:demo) = (%q, %v), want issue parent 425", runtimeParent, err)
	}
}

func TestWorkspaceHasManagedResourceMatchesSavedRootAmongMultiplePanes(t *testing.T) {
	expected := state.RuntimeResource{
		WorkspaceID: "w1",
		Label:       "fanout-coordinator-token",
		PaneID:      "w1:p1",
		TerminalID:  "term-1",
		CurrentPath: "/repo",
	}
	observation := backend.WorkspaceObservation{
		WorkspaceID: expected.WorkspaceID,
		Label:       expected.Label,
		RepoKey:     "/repo/.git",
		RepoRoot:    "/repo",
		Panes: []backend.WorkspacePaneObservation{
			{
				Pane: backend.PaneRef{
					Backend: backend.Herdr, Workspace: expected.WorkspaceID, Pane: expected.PaneID,
				},
				TerminalID: expected.TerminalID,
				CWD:        expected.CurrentPath,
			},
			{
				Pane: backend.PaneRef{
					Backend: backend.Herdr, Workspace: expected.WorkspaceID, Pane: "w1:p2",
				},
				TerminalID: "term-2",
				CWD:        "/repo/subdir",
			},
		},
	}
	if !workspaceHasManagedResource(observation, expected) {
		t.Fatal("saved root pane was not matched in multi-pane workspace")
	}
	expected.RepoKey = "/foreign/.git"
	if workspaceHasManagedResource(observation, expected) {
		t.Fatal("workspace with mismatched saved repository provenance was accepted")
	}
	expected.RepoKey = ""
	observation.Panes = observation.Panes[1:]
	if workspaceHasManagedResource(observation, expected) {
		t.Fatal("workspace without the saved root pane was accepted")
	}
}

func TestRealizeManagedPlanTaskReusesSavedChildNames(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	coordinatorReq := testManagedCoordinatorRequest(repo)
	coordinatorReq.Parent = "plan:demo"
	if _, err := realizeManagedCoordinator(
		context.Background(),
		coordinatorReq,
		runtime,
		hooks,
	); !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("plan coordinator error = %v", err)
	}

	req := testManagedWorktreeRequest(repo, "saved-task", 0)
	req.Parent = "plan:demo"
	req.TaskID = "task-1"
	child, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("initial plan task error = %v", err)
	}

	renamed := req
	renamed.Slug = "renamed-task"
	renamed.BranchName = "fanout/renamed-task"
	renamed.WorktreePath = filepath.Join(repo, ".fanout", "worktrees", renamed.Slug)
	reused, err := realizeManagedWorktree(context.Background(), renamed, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		reused.Intent.Slug != child.Intent.Slug ||
		reused.Intent.BranchName != child.Intent.BranchName ||
		reused.Intent.WorktreePath != child.Intent.WorktreePath ||
		len(runtime.mutations) != 2 {
		t.Fatalf(
			"renamed intent replay = %+v, original = %+v, err=%v, mutations=%d",
			reused.Intent,
			child.Intent,
			err,
			len(runtime.mutations),
		)
	}
}

func TestRealizeManagedWorktreeAdoptsResponseLossPostcondition(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	successfulMutate := runtime.mutate
	runtime.mutate = func(req managedTestMutation) (backend.WorktreeMutationResult, error) {
		result, err := successfulMutate(req)
		if err != nil {
			return result, err
		}
		if req.Kind == backend.WorktreeCreate {
			return backend.WorktreeMutationResult{}, errors.New("injected response loss")
		}
		return result, nil
	}
	result, err := realizeManagedWorktree(
		context.Background(),
		testManagedWorktreeRequest(repo, "response-loss", 427),
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("response-loss recovery error = %v", err)
	}
	if result.Intent.Status != state.IntentRealized || result.Intent.Resource.WorkspaceID == "" {
		t.Fatalf("response-loss result = %+v", result)
	}
}

func TestRealizeManagedWorktreeRecoversExpiredIssuedIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	req := testManagedWorktreeRequest(repo, "expired-issued", 432)
	realized, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	mutateManagedTestIntent(t, repo, realized.Intent.ID, func(intent *state.LaunchIntent) {
		intent.Status = state.IntentIssued
		intent.Resource = state.RuntimeResource{}
		intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	})

	mutationsBefore := len(runtime.mutations)
	recovered, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("expired issued recovery error = %v", err)
	}
	if recovered.Intent.Status != state.IntentRealized ||
		recovered.Intent.Resource.WorkspaceID == "" {
		t.Fatalf("expired issued recovery = %+v", recovered)
	}
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("expired issued intent reissued the Herdr mutation")
	}
	assertManagedRecoveryClassificationDeadline(
		t,
		"expired issued route",
		runtime.routeDeadline,
		runtime.routeHasDeadline,
	)
	assertManagedRecoveryClassificationDeadline(
		t,
		"expired issued observation",
		runtime.observeDeadline,
		runtime.observeHasDeadline,
	)
}

func TestRealizeManagedWorktreeDoesNotOpenExpiredRealizedIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	req := testManagedWorktreeRequest(repo, "expired-realized", 437)
	realized, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	live := make([]backend.WorkspaceObservation, 0, len(runtime.workspaces)-1)
	for _, workspace := range runtime.workspaces {
		if workspace.WorkspaceID != realized.Intent.Resource.WorkspaceID {
			live = append(live, workspace)
		}
	}
	runtime.workspaces = live

	mutateManagedTestIntent(t, repo, realized.Intent.ID, func(intent *state.LaunchIntent) {
		intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	})

	mutationsBefore := len(runtime.mutations)
	_, retryErr := realizeManagedWorktree(
		context.Background(),
		req,
		runtime,
		hooks,
	)
	if !errors.Is(retryErr, ErrManualCleanupRequired) {
		t.Fatalf("expired realized retry error = %v", retryErr)
	}
	assertManagedRecoveryClassificationDeadline(
		t,
		"expired realized route",
		runtime.routeDeadline,
		runtime.routeHasDeadline,
	)
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("expired realized intent issued a worktree open mutation")
	}
	control, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := control.FindIntent(realized.Intent.ID)
	if !found || intent.Status != state.IntentManualCleanupRequired {
		t.Fatalf("expired realized intent = (%+v, %t)", intent, found)
	}
}

// A precondition failure before the create is issued releases the reserved
// branch and the intent (non-issuance is proven by the planned status)
// instead of demanding manual cleanup.
func TestRealizeManagedWorktreePreconditionFailureReleasesPlannedIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	stop := errors.New("stop after branch reservation")
	runtime.observeErr = stop
	req := testManagedWorktreeRequest(repo, "precondition-release", 437)
	if _, err := realizeManagedWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, stop) {
		t.Fatalf("initial planned error = %v", err)
	}
	if err := os.MkdirAll(req.WorktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime.observeErr = nil

	_, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if err == nil || errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("precondition failure error = %v, want released non-manual failure", err)
	}
	if _, found := loadManagedWorktreeIntent(t, repo, req); found {
		t.Fatal("precondition failure kept the planned intent")
	}
	requireManagedBranch(t, repo, req, false)
}

func TestRealizeManagedWorktreeRollsBackExpiredPlannedIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	stop := errors.New("stop after branch reservation")
	runtime.observeErr = stop
	req := testManagedWorktreeRequest(repo, "expired-planned", 434)
	if _, err := realizeManagedWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, stop) {
		t.Fatalf("initial planned error = %v", err)
	}
	planned := requireManagedWorktreeIntent(t, repo, req)
	if !planned.BranchCreated || planned.Status != state.IntentPlanned {
		t.Fatalf("planned intent = %+v", planned)
	}
	mutateManagedTestIntent(t, repo, planned.ID, func(intent *state.LaunchIntent) {
		intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	})
	runtime.observeErr = nil
	routeCallsBefore := runtime.routeCalls

	if _, realizeErr := realizeManagedWorktree(
		context.Background(),
		req,
		runtime,
		hooks,
	); !errors.Is(
		realizeErr,
		errManagedIntentDeadlineExpired,
	) {
		t.Fatalf("expired planned error = %v", realizeErr)
	}
	if runtime.routeCalls != routeCallsBefore {
		t.Fatal("expired planned retry validated the Herdr route before rollback")
	}
	if _, found := loadManagedWorktreeIntent(t, repo, req); found {
		t.Fatal("expired planned intent was not removed")
	}
	requireManagedBranch(t, repo, req, false)
}

func TestRealizeManagedWorktreeRollsBackCanceledPlannedIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	stop := errors.New("stop after branch reservation")
	runtime.observeErr = stop
	req := testManagedWorktreeRequest(repo, "canceled-planned", 440)
	if _, err := realizeManagedWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, stop) {
		t.Fatalf("initial planned error = %v", err)
	}
	intent, found := loadManagedWorktreeIntent(t, repo, req)
	if !found || !intent.BranchCreated || intent.Status != state.IntentPlanned {
		t.Fatalf("planned intent = (%+v,%t)", intent, found)
	}

	runtime.observeErr = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancelHooks := hooks
	cancelHooks.Now = func() time.Time {
		cancel()
		return hooks.Now()
	}
	routeCallsBefore := runtime.routeCalls
	if _, realizeErr := realizeManagedWorktree(ctx, req, runtime, cancelHooks); !errors.Is(
		realizeErr,
		context.Canceled,
	) {
		t.Fatalf("canceled planned error = %v", realizeErr)
	}
	if runtime.routeCalls != routeCallsBefore {
		t.Fatal("canceled planned retry validated the Herdr route before rollback")
	}
	if _, stillFound := loadManagedWorktreeIntent(t, repo, req); stillFound {
		t.Fatal("canceled planned intent was not removed")
	}
	if head, branchFound, observeErr := worktree.ObserveBranch(
		context.Background(),
		repo,
		intent.FullBranchRef,
	); observeErr != nil {
		t.Fatal(observeErr)
	} else if branchFound {
		t.Fatalf("canceled planned branch remains at %s", head)
	}
}

func TestRealizeManagedWorktreeKeepsExpiredPlannedIntentWhenBranchOwnershipWasNotSaved(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	stop := errors.New("stop after branch reservation")
	runtime.observeErr = stop
	req := testManagedWorktreeRequest(repo, "expired-ambiguous-branch", 435)
	if _, err := realizeManagedWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, stop) {
		t.Fatalf("initial planned error = %v", err)
	}
	planned := requireManagedWorktreeIntent(t, repo, req)
	if !planned.BranchCreated {
		t.Fatalf("planned intent = %+v", planned)
	}
	mutateManagedTestIntent(t, repo, planned.ID, func(intent *state.LaunchIntent) {
		intent.BranchCreated = false
		intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	})
	runtime.observeErr = nil

	if _, realizeErr := realizeManagedWorktree(
		context.Background(),
		req,
		runtime,
		hooks,
	); !errors.Is(realizeErr, ErrManualCleanupRequired) {
		t.Fatalf("ambiguous branch ownership error = %v", realizeErr)
	}
	saved := requireManagedWorktreeIntent(t, repo, req)
	if saved.Status != state.IntentManualCleanupRequired ||
		!strings.Contains(saved.Failure, "branch exists without persisted ownership") {
		t.Fatalf("ambiguous ownership intent = %+v", saved)
	}
	requireManagedBranch(t, repo, req, true)
}

func TestRealizeManagedWorktreePreservesIssuedIntentWhenMutationContextIsCanceled(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	successfulMutate := runtime.mutate
	ctx, cancel := context.WithCancel(context.Background())
	runtime.mutate = func(req managedTestMutation) (backend.WorktreeMutationResult, error) {
		result, err := successfulMutate(req)
		if req.Kind == backend.WorktreeCreate {
			cancel()
			return result, context.DeadlineExceeded
		}
		return result, err
	}
	req := testManagedWorktreeRequest(repo, "canceled-recovery", 433)
	observesBefore := runtime.observeCalls
	result, err := realizeManagedWorktree(ctx, req, runtime, hooks)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery result = %+v, err=%v", result, err)
	}
	intent, found := loadManagedWorktreeIntent(t, repo, req)
	if !found || intent.Status != state.IntentIssued {
		t.Fatalf("canceled recovery intent = (%+v,%t)", intent, found)
	}
	if runtime.observeCalls != observesBefore+1 {
		t.Fatalf(
			"canceled mutation observations = %d, want only pre-mutation observation %d",
			runtime.observeCalls,
			observesBefore+1,
		)
	}

	runtime.mutate = successfulMutate
	mutationsBefore := len(runtime.mutations)
	recovered, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("fresh recovery error = %v", err)
	}
	if recovered.Intent.Status != state.IntentRealized ||
		recovered.Intent.Resource.WorkspaceID == "" {
		t.Fatalf("fresh recovery result = %+v", recovered)
	}
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("fresh recovery reissued the Herdr mutation")
	}
}

func TestRealizeManagedWorktreeFailsClosedOnAmbiguousResponseLoss(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	runtime.mutate = func(req managedTestMutation) (backend.WorktreeMutationResult, error) {
		if req.Kind == backend.WorktreeCreate {
			return backend.WorktreeMutationResult{}, errors.New("injected response loss")
		}
		return backend.WorktreeMutationResult{}, errors.New("unexpected mutation")
	}

	req := testManagedWorktreeRequest(repo, "ambiguous", 428)
	_, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("ambiguous response error = %v", err)
	}
	fullRef, refErr := worktree.LocalBranchRef(context.Background(), repo, req.BranchName)
	if refErr != nil {
		t.Fatal(refErr)
	}
	if _, found, observeErr := worktree.ObserveBranch(context.Background(), repo, fullRef); observeErr != nil || !found {
		t.Fatalf("ambiguous branch was removed: found=%t err=%v", found, observeErr)
	}
	control, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	intentID, _ := state.WorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	intent, found := control.FindIntent(intentID)
	if !found || intent.Status != state.IntentManualCleanupRequired {
		t.Fatalf("ambiguous intent = (%+v,%t)", intent, found)
	}

	mutationCount := len(runtime.mutations)
	if _, err := realizeManagedWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("manual cleanup replay error = %v", err)
	}
	if len(runtime.mutations) != mutationCount {
		t.Fatal("manual-cleanup intent reissued the Herdr mutation")
	}
}

func TestRealizeManagedWorktreeDeletesBranchOnlyAfterStructuredRejection(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	runtime.mutate = func(req managedTestMutation) (backend.WorktreeMutationResult, error) {
		if req.Kind == backend.WorktreeCreate {
			return backend.WorktreeMutationResult{}, backend.MutationRejectedError{
				Code: "worktree_create_failed", Message: "rejected before create",
			}
		}
		return backend.WorktreeMutationResult{}, errors.New("unexpected mutation")
	}

	req := testManagedWorktreeRequest(repo, "rejected", 429)
	_, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, backend.ErrMutationRejected) {
		t.Fatalf("structured rejection error = %v", err)
	}
	fullRef, refErr := worktree.LocalBranchRef(context.Background(), repo, req.BranchName)
	if refErr != nil {
		t.Fatal(refErr)
	}
	if _, found, observeErr := worktree.ObserveBranch(context.Background(), repo, fullRef); observeErr != nil || found {
		t.Fatalf("rejected branch = found:%t err:%v, want deleted", found, observeErr)
	}
	control, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	intentID, _ := state.WorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if _, found := control.FindIntent(intentID); found {
		t.Fatal("rejected non-mutation left a provisional intent")
	}
}

// A rejection followed by a transient Git failure is a double-failure
// window: this run stays retryable, but the retry (which has no rejection
// proof) fails closed to manual_cleanup_required per the canon's density
// rule instead of guessing about the reserved branch.
func TestRealizeManagedWorktreeRejectionWithGitFailureFailsClosedOnRetry(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	originalPath := os.Getenv("PATH")
	failingBin := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(failingBin, "git"),
		[]byte("#!/bin/sh\nexit 99\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	runtime.mutate = func(req managedTestMutation) (backend.WorktreeMutationResult, error) {
		if req.Kind != backend.WorktreeCreate {
			return backend.WorktreeMutationResult{}, errors.New("unexpected mutation")
		}
		t.Setenv("PATH", failingBin)
		return backend.WorktreeMutationResult{}, backend.MutationRejectedError{
			Code: "worktree_create_failed", Message: "rejected before create",
		}
	}

	req := testManagedWorktreeRequest(repo, "rejected-retry", 438)
	_, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, backend.ErrMutationRejected) || errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("structured rejection with Git failure error = %v", err)
	}
	t.Setenv("PATH", originalPath)

	control, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	intentID, _ := state.WorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	intent, found := control.FindIntent(intentID)
	if !found || intent.Status != state.IntentIssued {
		t.Fatalf("post-rejection intent = (%+v,%t), want retryable issued", intent, found)
	}

	mutationCount := len(runtime.mutations)
	runtime.mutate = func(managedTestMutation) (backend.WorktreeMutationResult, error) {
		t.Fatal("issued intent reissued the Herdr mutation")
		return backend.WorktreeMutationResult{}, nil
	}
	_, err = realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("double-failure retry error = %v, want fail-closed manual", err)
	}
	if len(runtime.mutations) != mutationCount {
		t.Fatalf("double-failure retry mutations = %d, want %d", len(runtime.mutations), mutationCount)
	}
	requireManagedBranch(t, repo, req, true)
}

func TestRealizeManagedWorktreeAdoptsExistingBranchWithoutBaseArgument(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	gitCmdTest(t, repo, "branch", "fanout/existing", "HEAD")
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	req := testManagedWorktreeRequest(repo, "existing", 430)
	result, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("existing branch error = %v", err)
	}
	if result.Intent.BranchCreated || !result.Intent.BranchExisted {
		t.Fatalf("branch ownership = existed:%t created:%t", result.Intent.BranchExisted, result.Intent.BranchCreated)
	}
	childMutation := runtime.mutations[len(runtime.mutations)-1]
	if childMutation.Base != "" {
		t.Fatalf("existing branch worktree create base = %q, want omitted", childMutation.Base)
	}
}

// TestRealizeManagedCoordinatorClassifiesRealizedIntentAgainstLiveSession pins
// the Herdr-side-operation classification of a realized coordinator: adopt the
// one workspace still carrying the label nonce even after ids changed,
// recreate after a proven Herdr-side close, keep a failed snapshot retryable,
// and fail closed only on an ambiguous label.
func TestRealizeManagedCoordinatorClassifiesRealizedIntentAgainstLiveSession(t *testing.T) {
	tests := []struct {
		name    string
		reshape func(*fakeManagedRealizeRuntime)
		check   func(*testing.T, *fakeManagedRealizeRuntime, state.LaunchIntent, ManagedRealizeResult, error, state.LaunchJournal)
	}{
		{
			name: "adopts the workspace moved by a Herdr-side operation",
			reshape: func(runtime *fakeManagedRealizeRuntime) {
				moved := runtime.workspaces[0]
				moved.WorkspaceID = "w9"
				moved.Pane = backend.PaneRef{Backend: backend.Herdr, Workspace: "w9", Pane: "w9:p9"}
				moved.TerminalID = "term-w9-moved"
				runtime.workspaces = []backend.WorkspaceObservation{moved}
			},
			check: func(t *testing.T, _ *fakeManagedRealizeRuntime, first state.LaunchIntent, result ManagedRealizeResult, err error, journal state.LaunchJournal) {
				t.Helper()
				if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
					t.Fatalf("resume moved coordinator = %v, want readiness deferral", err)
				}
				saved, found := journal.FindIntent(first.ID)
				if result.Intent.Resource.WorkspaceID != "w9" || !found ||
					saved.Status != state.IntentRealized || saved.Resource.TerminalID != "term-w9-moved" {
					t.Fatalf("adopted identity = result %+v, saved (%+v, %t); want observed w9 identity persisted", result.Intent.Resource, saved, found)
				}
			},
		},
		{
			name: "adopts an intact multi-pane workspace without rewriting identity",
			reshape: func(runtime *fakeManagedRealizeRuntime) {
				// Herdrrun reports the pane triple per pane once a workspace
				// holds more than the launcher's pane; the saved identity must
				// survive through the per-pane fallback.
				workspace := runtime.workspaces[0]
				root := backend.WorkspacePaneObservation{
					Pane: workspace.Pane, TerminalID: workspace.TerminalID, CWD: workspace.CWD,
				}
				extra := backend.WorkspacePaneObservation{
					Pane: backend.PaneRef{
						Backend: backend.Herdr, Workspace: workspace.WorkspaceID,
						Pane: workspace.WorkspaceID + ":p2",
					},
					TerminalID: "term-extra", CWD: workspace.CWD,
				}
				workspace.Pane, workspace.TerminalID, workspace.CWD = backend.PaneRef{}, "", ""
				workspace.Panes = []backend.WorkspacePaneObservation{root, extra}
				runtime.workspaces = []backend.WorkspaceObservation{workspace}
			},
			check: func(t *testing.T, _ *fakeManagedRealizeRuntime, first state.LaunchIntent, result ManagedRealizeResult, err error, journal state.LaunchJournal) {
				t.Helper()
				if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
					t.Fatalf("resume multi-pane coordinator = %v, want readiness deferral", err)
				}
				saved, found := journal.FindIntent(first.ID)
				if !found || saved.Status != state.IntentRealized || saved.Resource != first.Resource ||
					result.Intent.Resource != first.Resource {
					t.Fatalf("multi-pane adopt = result %+v, saved (%+v, %t); want the saved identity kept", result.Intent.Resource, saved, found)
				}
			},
		},
		{
			name: "adopts a moved multi-pane workspace through its unique root pane",
			reshape: func(runtime *fakeManagedRealizeRuntime) {
				workspace := runtime.workspaces[0]
				root := backend.WorkspacePaneObservation{
					Pane:       backend.PaneRef{Backend: backend.Herdr, Workspace: "w9", Pane: "w9:p9"},
					TerminalID: "term-w9-moved", CWD: workspace.CWD,
				}
				extra := backend.WorkspacePaneObservation{
					Pane:       backend.PaneRef{Backend: backend.Herdr, Workspace: "w9", Pane: "w9:p2"},
					TerminalID: "term-extra", CWD: "/elsewhere",
				}
				workspace.WorkspaceID = "w9"
				workspace.Pane, workspace.TerminalID, workspace.CWD = backend.PaneRef{}, "", ""
				workspace.Panes = []backend.WorkspacePaneObservation{root, extra}
				runtime.workspaces = []backend.WorkspaceObservation{workspace}
			},
			check: func(t *testing.T, _ *fakeManagedRealizeRuntime, first state.LaunchIntent, result ManagedRealizeResult, err error, journal state.LaunchJournal) {
				t.Helper()
				if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
					t.Fatalf("resume moved multi-pane coordinator = %v, want readiness deferral", err)
				}
				saved, found := journal.FindIntent(first.ID)
				if !found || saved.Status != state.IntentRealized ||
					result.Intent.Resource.PaneID != "w9:p9" || saved.Resource.TerminalID != "term-w9-moved" {
					t.Fatalf("multi-pane drift adopt = result %+v, saved (%+v, %t); want the root pane identity persisted", result.Intent.Resource, saved, found)
				}
			},
		},
		{
			name: "treats a relabeled live workspace as identity change",
			reshape: func(runtime *fakeManagedRealizeRuntime) {
				runtime.workspaces[0].Label = "fanout-coordinator-renamed"
			},
			check: func(t *testing.T, _ *fakeManagedRealizeRuntime, first state.LaunchIntent, _ ManagedRealizeResult, err error, journal state.LaunchJournal) {
				t.Helper()
				saved, found := journal.FindIntent(first.ID)
				if !errors.Is(err, ErrManualCleanupRequired) ||
					!found || saved.Status != state.IntentManualCleanupRequired {
					t.Fatalf("relabel = err %v, saved (%+v, %t); want manual cleanup, not a duplicate create", err, saved, found)
				}
			},
		},
		{
			name: "recreates after a Herdr-side workspace close",
			reshape: func(runtime *fakeManagedRealizeRuntime) {
				runtime.workspaces = nil
			},
			check: func(t *testing.T, runtime *fakeManagedRealizeRuntime, first state.LaunchIntent, result ManagedRealizeResult, err error, journal state.LaunchJournal) {
				t.Helper()
				if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
					t.Fatalf("resume closed coordinator = %v, want readiness deferral", err)
				}
				saved, found := journal.FindIntent(first.ID)
				if result.Intent.ID != first.ID || result.Intent.WorkspaceLabel == first.WorkspaceLabel ||
					!found || saved.Status != state.IntentRealized || len(runtime.workspaces) != 1 {
					t.Fatalf("recreate = result %+v, saved (%+v, %t); want same id under a fresh label", result.Intent, saved, found)
				}
			},
		},
		{
			name: "keeps a failed snapshot retryable",
			reshape: func(runtime *fakeManagedRealizeRuntime) {
				runtime.observeErr = errors.New("socket down")
			},
			check: func(t *testing.T, _ *fakeManagedRealizeRuntime, first state.LaunchIntent, _ ManagedRealizeResult, err error, journal state.LaunchJournal) {
				t.Helper()
				saved, found := journal.FindIntent(first.ID)
				if err == nil || errors.Is(err, ErrManualCleanupRequired) ||
					!found || saved.Status != state.IntentRealized {
					t.Fatalf("failed snapshot = err %v, saved (%+v, %t); want retryable realized intent", err, saved, found)
				}
			},
		},
		{
			name: "pins an ambiguous label to manual cleanup",
			reshape: func(runtime *fakeManagedRealizeRuntime) {
				duplicate := runtime.workspaces[0]
				duplicate.WorkspaceID = "w7"
				duplicate.Pane = backend.PaneRef{Backend: backend.Herdr, Workspace: "w7", Pane: "w7:p1"}
				runtime.workspaces = append(runtime.workspaces, duplicate)
			},
			check: func(t *testing.T, _ *fakeManagedRealizeRuntime, first state.LaunchIntent, _ ManagedRealizeResult, err error, journal state.LaunchJournal) {
				t.Helper()
				saved, found := journal.FindIntent(first.ID)
				if !errors.Is(err, ErrManualCleanupRequired) ||
					!found || saved.Status != state.IntentManualCleanupRequired {
					t.Fatalf("ambiguous label = err %v, saved (%+v, %t); want manual cleanup", err, saved, found)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			runtime := &fakeManagedRealizeRuntime{}
			installSuccessfulManagedMutations(t, repo, runtime)
			hooks := deterministicManagedRealizeHooks()
			first := realizeTestManagedCoordinator(t, repo, runtime, hooks)
			tt.reshape(runtime)
			result, err := realizeManagedCoordinator(
				context.Background(), testManagedCoordinatorRequest(repo), runtime, hooks,
			)
			journal, journalErr := state.LoadLaunchJournal(repo)
			if journalErr != nil {
				t.Fatal(journalErr)
			}
			tt.check(t, runtime, first, result, err, journal)
		})
	}
}

// TestRealizeManagedCoordinatorRefusesRecreateAfterIssuedToken pins the canon:
// a token-issued crash window never self-heals — a coordinator whose workspace
// vanished after its launch token went out is manual cleanup, not a recreate.
func TestRealizeManagedCoordinatorRefusesRecreateAfterIssuedToken(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	first := realizeTestManagedCoordinator(t, repo, runtime, hooks)
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, found := journal.FindIntent(first.ID)
	if !found {
		t.Fatal("realized intent is missing")
	}
	saved.Launch = &state.LaunchCapsule{
		Nonce: strings.Repeat("a", 32), Agent: "codex",
		AgentName:  "fanout-0123456789abcdef01234567",
		Executable: "/bin/codex", Args: []string{},
		EnvFilePath: "/tmp/env", EnvNameCount: 1,
		LauncherReady: true, TokenIssued: true,
	}
	journal.UpsertIntent(saved)
	if saveErr := journal.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	runtime.workspaces = nil
	mutations := len(runtime.mutations)
	_, err = realizeManagedCoordinator(
		context.Background(), testManagedCoordinatorRequest(repo), runtime, hooks,
	)
	final, journalErr := state.LoadLaunchJournal(repo)
	if journalErr != nil {
		t.Fatal(journalErr)
	}
	pinned, found := final.FindIntent(first.ID)
	if !errors.Is(err, ErrManualCleanupRequired) || len(runtime.mutations) != mutations ||
		!found || pinned.Status != state.IntentManualCleanupRequired {
		t.Fatalf(
			"issued-token recreate = err %v, mutations %d→%d, saved (%+v, %t); want manual cleanup with no new mutation",
			err, mutations, len(runtime.mutations), pinned, found,
		)
	}
}

func TestRealizeManagedCoordinatorAdoptsResponseLossAndNeverReissues(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	successfulMutate := runtime.mutate
	runtime.mutate = func(req managedTestMutation) (backend.WorktreeMutationResult, error) {
		result, err := successfulMutate(req)
		if err != nil {
			return result, err
		}
		return backend.WorktreeMutationResult{}, errors.New("injected coordinator response loss")
	}
	hooks := deterministicManagedRealizeHooks()
	req := testManagedCoordinatorRequest(repo)
	result, err := realizeManagedCoordinator(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		result.Intent.Status != state.IntentRealized {
		t.Fatalf("coordinator response-loss result = %+v err=%v", result, err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("coordinator mutations = %d, want 1", len(runtime.mutations))
	}
	if _, err := realizeManagedCoordinator(context.Background(), req, runtime, hooks); !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("coordinator replay error = %v", err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatal("coordinator response-loss replay reissued mutation")
	}
}

func TestRealizeManagedCoordinatorRejectsIssuedRecoveryFromChangedRepoIdentity(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	owner := filepath.Join(t.TempDir(), "issued-owner")
	retrySource := filepath.Join(t.TempDir(), "issued-retry-source")
	gitCmdTest(t, repo, "worktree", "add", "-b", "issued-coordinator-owner", owner, "HEAD")
	gitCmdTest(t, repo, "worktree", "add", "-b", "issued-coordinator-retry", retrySource, "HEAD")

	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, owner, runtime)
	successfulMutate := runtime.mutate
	snapshotErr := errors.New("injected coordinator recovery snapshot failure")
	responseErr := errors.New("injected coordinator response loss")
	runtime.mutate = func(req managedTestMutation) (backend.WorktreeMutationResult, error) {
		result, mutationErr := successfulMutate(req)
		if mutationErr != nil {
			return result, mutationErr
		}
		runtime.observeErr = snapshotErr
		return backend.WorktreeMutationResult{}, responseErr
	}
	hooks := deterministicManagedRealizeHooks()
	if _, err := realizeManagedCoordinator(
		context.Background(),
		testManagedCoordinatorRequest(owner),
		runtime,
		hooks,
	); !errors.Is(err, snapshotErr) {
		t.Fatalf("initial issued coordinator error = %v", err)
	}
	runtime.observeErr = nil
	control, err := state.LoadLaunchJournal(retrySource)
	if err != nil {
		t.Fatal(err)
	}
	if len(control.Intents) != 1 || control.Intents[0].Status != state.IntentIssued {
		t.Fatalf("issued coordinator intents = %+v", control.Intents)
	}
	coordinatorID := control.Intents[0].ID

	dotGitPath := filepath.Join(owner, ".git")
	originalDotGit, err := os.ReadFile(dotGitPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restoreErr := os.WriteFile(dotGitPath, originalDotGit, 0o644); restoreErr != nil {
			t.Errorf("restore issued owner .git file: %v", restoreErr)
		}
	})
	foreign := newManagedRealizeRepo(t)
	foreignDotGit := "gitdir: " + filepath.Join(foreign, ".git") + "\n"
	if writeErr := os.WriteFile(dotGitPath, []byte(foreignDotGit), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	_, err = realizeManagedCoordinator(
		context.Background(),
		testManagedCoordinatorRequest(retrySource),
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManualCleanupRequired) ||
		!strings.Contains(err.Error(), errManagedRealizedIdentityChanged.Error()) {
		t.Fatalf("changed issued coordinator repository identity error = %v", err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("changed issued coordinator repository identity mutations = %d, want 1", len(runtime.mutations))
	}
	control, err = state.LoadLaunchJournal(retrySource)
	if err != nil {
		t.Fatal(err)
	}
	persisted, found := control.FindIntent(coordinatorID)
	if !found || persisted.Status != state.IntentManualCleanupRequired {
		t.Fatalf("changed issued coordinator repository identity intent = (%+v,%t)", persisted, found)
	}
}

func TestRealizeManagedManualCoordinatorsUseScopedSyntheticIssueIdentity(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	req := testManagedCoordinatorRequest(repo)
	req.Parent = ManualParentRef
	req.IssueNum = -1

	first, err := realizeManagedCoordinator(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("first manual coordinator error = %v", err)
	}
	sibling := filepath.Join(t.TempDir(), "manual-sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "manual-sibling", sibling, "HEAD")
	req = testManagedCoordinatorRequest(sibling)
	req.Parent = ManualParentRef
	req.IssueNum = -1
	otherOwner, err := realizeManagedCoordinator(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("linked manual coordinator error = %v", err)
	}
	req = testManagedCoordinatorRequest(repo)
	req.Parent = ManualParentRef
	req.IssueNum = -2
	second, err := realizeManagedCoordinator(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("second manual coordinator error = %v", err)
	}
	if first.Intent.ID == otherOwner.Intent.ID || first.Intent.ID == second.Intent.ID ||
		first.Intent.IssueNum != -1 || otherOwner.Intent.IssueNum != -1 ||
		second.Intent.IssueNum != -2 || len(runtime.mutations) != 3 {
		t.Fatalf(
			"manual coordinators = (%+v, %+v, %+v), mutations=%d",
			first.Intent,
			otherOwner.Intent,
			second.Intent,
			len(runtime.mutations),
		)
	}
	control, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(control.Intents) != 3 {
		t.Fatalf("manual coordinator intents = %+v", control.Intents)
	}
}

func TestRealizeManagedManualWorktreeUsesNegativeSyntheticIssueIdentity(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	coordinatorReq := testManagedCoordinatorRequest(repo)
	coordinatorReq.Parent = ManualParentRef
	coordinatorReq.IssueNum = -1
	if _, err := realizeManagedCoordinator(
		context.Background(),
		coordinatorReq,
		runtime,
		hooks,
	); !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("manual coordinator error = %v", err)
	}

	req := testManagedWorktreeRequest(repo, "manual-child", -1)
	req.Parent = ManualParentRef
	result, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	identity, identityErr := worktree.ResolveRepoIdentity(context.Background(), repo)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		result.Intent.IssueNum != -1 || result.Intent.OwnerProjectRoot != identity.RepoRoot {
		t.Fatalf("manual worktree = %+v, err=%v", result.Intent, err)
	}
}

func TestRealizeManagedReusesNumericParentCoordinatorAcrossLinkedWorktrees(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	coordinator := realizeTestManagedCoordinator(t, repo, runtime, hooks)

	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "linked-coordinator", sibling, "HEAD")
	siblingCoordinatorReq := testManagedCoordinatorRequest(sibling)
	reused, err := realizeManagedCoordinator(
		context.Background(),
		siblingCoordinatorReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("linked coordinator reuse error = %v", err)
	}
	if reused.Intent.ID != coordinator.ID ||
		reused.Intent.WorktreePath != coordinator.WorktreePath ||
		len(runtime.mutations) != 1 {
		t.Fatalf(
			"linked coordinator = %+v, original = %+v, mutations = %d",
			reused.Intent,
			coordinator,
			len(runtime.mutations),
		)
	}
	successfulMutate := runtime.mutate
	runtime.mutate = func(
		mutationReq managedTestMutation,
	) (backend.WorktreeMutationResult, error) {
		result, mutateErr := successfulMutate(mutationReq)
		if mutateErr == nil && mutationReq.Kind == backend.WorktreeCreate {
			result.RepoRoot = coordinator.Resource.CurrentPath
			runtime.workspaces[len(runtime.workspaces)-1] = result.WorkspaceObservation
		}
		return result, mutateErr
	}

	childReq := testManagedWorktreeRequest(sibling, "linked-child", 435)
	child, err := realizeManagedWorktree(context.Background(), childReq, runtime, hooks)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("linked child error = %v", err)
	}
	childMutation := runtime.mutations[len(runtime.mutations)-1]
	if child.Intent.Coordinator != coordinator.Resource || len(runtime.mutations) != 2 ||
		childMutation.SourceRoot != coordinator.Resource.CurrentPath ||
		childMutation.SourceRepoRoot != coordinator.Resource.CurrentPath {
		t.Fatalf(
			"linked child coordinator = %+v, want %+v; mutation=%+v",
			child.Intent.Coordinator,
			coordinator.Resource,
			childMutation,
		)
	}

	other := filepath.Join(t.TempDir(), "other")
	gitCmdTest(t, repo, "worktree", "add", "-b", "linked-child-reuse", other, "HEAD")
	otherChildReq := testManagedWorktreeRequest(other, "linked-child", 435)
	reusedChild, err := realizeManagedWorktree(
		context.Background(),
		otherChildReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		reusedChild.Intent.ID != child.Intent.ID ||
		reusedChild.Intent.WorktreePath != child.Intent.WorktreePath ||
		len(runtime.mutations) != 2 {
		t.Fatalf(
			"linked child reuse = %+v, original = %+v, err = %v, mutations = %d",
			reusedChild.Intent,
			child.Intent,
			err,
			len(runtime.mutations),
		)
	}
}

func TestRealizeManagedCoordinatorRejectsSavedPathWithChangedRepoIdentity(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	owner := filepath.Join(t.TempDir(), "owner")
	retrySource := filepath.Join(t.TempDir(), "retry-source")
	gitCmdTest(t, repo, "worktree", "add", "-b", "coordinator-owner", owner, "HEAD")
	gitCmdTest(t, repo, "worktree", "add", "-b", "coordinator-retry", retrySource, "HEAD")

	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, owner, runtime)
	hooks := deterministicManagedRealizeHooks()
	coordinator := realizeTestManagedCoordinator(t, owner, runtime, hooks)

	dotGitPath := filepath.Join(owner, ".git")
	originalDotGit, err := os.ReadFile(dotGitPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restoreErr := os.WriteFile(dotGitPath, originalDotGit, 0o644); restoreErr != nil {
			t.Errorf("restore owner .git file: %v", restoreErr)
		}
	})
	foreign := newManagedRealizeRepo(t)
	foreignDotGit := "gitdir: " + filepath.Join(foreign, ".git") + "\n"
	if writeErr := os.WriteFile(dotGitPath, []byte(foreignDotGit), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	_, err = realizeManagedCoordinator(
		context.Background(),
		testManagedCoordinatorRequest(retrySource),
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManualCleanupRequired) ||
		!strings.Contains(err.Error(), errManagedRealizedIdentityChanged.Error()) {
		t.Fatalf("changed coordinator repository identity error = %v", err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("changed coordinator repository identity mutations = %d, want 1", len(runtime.mutations))
	}
	control, loadErr := state.LoadLaunchJournal(retrySource)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	persisted, found := control.FindIntent(coordinator.ID)
	if !found || persisted.Status != state.IntentManualCleanupRequired {
		t.Fatalf("changed coordinator repository identity intent = (%+v,%t)", persisted, found)
	}
}

func TestRealizeManagedResumesPlannedChildAtSavedOwnerAcrossLinkedWorktrees(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	savedReq := testManagedWorktreeRequest(repo, "linked-planned-child", 436)
	runtime.policyErr = errors.New("stop before child mutation")
	if _, err := realizeManagedWorktree(
		context.Background(),
		savedReq,
		runtime,
		hooks,
	); err == nil || !strings.Contains(err.Error(), "stop before child mutation") {
		t.Fatalf("initial planned child error = %v", err)
	}

	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "linked-planned-child", sibling, "HEAD")
	runtime.policyErr = nil
	retryReq := testManagedWorktreeRequest(sibling, "linked-planned-child", 436)
	result, err := realizeManagedWorktree(
		context.Background(),
		retryReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("linked planned child retry error = %v", err)
	}
	if result.Intent.WorktreePath != savedReq.WorktreePath || len(runtime.mutations) != 2 {
		t.Fatalf(
			"linked planned child = %+v, mutations = %d",
			result.Intent,
			len(runtime.mutations),
		)
	}
	childMutation := runtime.mutations[1]
	savedSource, sourceErr := worktree.ResolveRepoIdentity(context.Background(), repo)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	if childMutation.SourceRoot != savedSource.RepoRoot ||
		childMutation.SourceRepoRoot != savedSource.RepoRoot ||
		childMutation.Path != savedReq.WorktreePath {
		t.Fatalf("linked planned child mutation = %+v", childMutation)
	}
}

// The coordinator has no planned stage: a failure before the workspace create
// must leave no intent behind, so the retry starts fresh instead of entering
// recovery for a mutation that was never recorded.
// A structured rejection is a durable non-creation proof: even when the
// operation context has already expired, the coordinator intent is released
// instead of parking as issued (where the rejection proof would be lost).
func TestRealizeManagedCoordinatorRejectedCreateReleasesEvenAfterContextExpiry(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.mutate = func(managedTestMutation) (backend.WorktreeMutationResult, error) {
		cancel()
		return backend.WorktreeMutationResult{}, backend.MutationRejectedError{
			Code: "workspace_create_failed", Message: "rejected after deadline",
		}
	}

	_, err := realizeManagedCoordinator(ctx, testManagedCoordinatorRequest(repo), runtime, hooks)
	if !errors.Is(err, backend.ErrMutationRejected) || errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("rejected+expired coordinator error = %v", err)
	}
	control, loadErr := state.LoadLaunchJournal(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(control.Intents) != 0 {
		t.Fatalf("rejected+expired coordinator intents = %#v, want released", control.Intents)
	}
}

// A journal save failure while persisting the rejection proof must not drop
// the run into normal response-loss recovery: the in-hand rejection still
// classifies and rolls back the reserved branch.
func TestRealizeManagedWorktreeRejectionSaveFailureStillRollsBack(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)

	journalPath, pathErr := state.LaunchJournalPath(repo)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	journalDir := filepath.Dir(journalPath)
	runtime.mutate = func(m managedTestMutation) (backend.WorktreeMutationResult, error) {
		if m.Kind != backend.WorktreeCreate {
			t.Fatalf("unexpected mutation kind %q", m.Kind)
		}
		// Make the journal directory read-only so persisting the rejection
		// proof fails while local Git classification still works.
		if err := os.Chmod(journalDir, 0o500); err != nil {
			t.Fatal(err)
		}
		return backend.WorktreeMutationResult{}, backend.MutationRejectedError{
			Code: "worktree_create_failed", Message: "rejected with failing journal",
		}
	}
	t.Cleanup(func() {
		if err := os.Chmod(journalDir, 0o700); err != nil {
			t.Errorf("restore journal dir mode: %v", err)
		}
	})

	req := testManagedWorktreeRequest(repo, "rejected-save-failure", 438)
	_, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, backend.ErrMutationRejected) || errors.Is(err, ErrManualCleanupRequired) {
		t.Fatalf("rejected create with failing journal error = %v", err)
	}
	requireManagedBranch(t, repo, req, false)
}

func TestRealizeManagedCoordinatorPolicyFailureLeavesNoIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	runtime.policyErr = errors.New("stop before coordinator mutation")
	hooks := deterministicManagedRealizeHooks()
	if _, initialErr := realizeManagedCoordinator(
		context.Background(),
		testManagedCoordinatorRequest(repo),
		runtime,
		hooks,
	); initialErr == nil ||
		!strings.Contains(initialErr.Error(), "stop before coordinator mutation") {
		t.Fatalf("policy-blocked coordinator error = %v", initialErr)
	}
	if len(runtime.mutations) != 0 {
		t.Fatal("policy-blocked coordinator unexpectedly issued a mutation")
	}
	control, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(control.Intents) != 0 {
		t.Fatalf("policy-blocked coordinator intents = %#v, want none", control.Intents)
	}

	runtime.policyErr = nil
	if _, retryErr := realizeManagedCoordinator(
		context.Background(),
		testManagedCoordinatorRequest(repo),
		runtime,
		hooks,
	); !errors.Is(retryErr, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("fresh coordinator retry error = %v", retryErr)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("fresh coordinator retry mutations = %d, want 1", len(runtime.mutations))
	}
}

func TestRealizeManagedResolvesPlanRuntimeParentPerOwnerRoot(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	planDir := filepath.Join(repo, ".fanout", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(planDir, "demo.json")
	if err := os.WriteFile(
		planPath,
		[]byte(`{"version":1,"plan":{"slug":"demo","title":"Demo","source":"issue #425"},"tasks":[]}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	planReq := testManagedCoordinatorRequest(repo)
	planReq.Parent = "plan:demo"
	coordinator, err := realizeManagedCoordinator(
		context.Background(),
		planReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("issue-sourced coordinator error = %v", err)
	}
	wantID, err := state.CoordinatorIntentID("425", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.Intent.ID != wantID || coordinator.Intent.RuntimeParent != "425" {
		t.Fatalf("issue-sourced coordinator = %+v, want runtime parent 425", coordinator.Intent)
	}
	if removeErr := os.Remove(planPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	replayedPlan, err := realizeManagedCoordinator(
		context.Background(),
		planReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		replayedPlan.Intent.ID != wantID || len(runtime.mutations) != 1 {
		t.Fatalf(
			"same-owner saved runtime parent = %+v, err=%v, mutations=%d",
			replayedPlan,
			err,
			len(runtime.mutations),
		)
	}

	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "issue-plan-coordinator", sibling, "HEAD")
	siblingPlanDir := filepath.Join(sibling, ".fanout", "plans")
	if mkdirErr := os.MkdirAll(siblingPlanDir, 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	siblingPlanPath := filepath.Join(siblingPlanDir, "demo.json")
	if writeErr := os.WriteFile(
		siblingPlanPath,
		[]byte(`{"version":1,"plan":{"slug":"demo","title":"Demo","source":"issue #425"},"tasks":[]}`),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	siblingPlanReq := testManagedCoordinatorRequest(sibling)
	siblingPlanReq.Parent = "plan:demo"
	reusedPlan, err := realizeManagedCoordinator(
		context.Background(),
		siblingPlanReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		reusedPlan.Intent.ID != wantID {
		t.Fatalf("saved issue-sourced coordinator reuse = %+v, err=%v", reusedPlan, err)
	}
	if writeErr := os.WriteFile(
		siblingPlanPath,
		[]byte(`{"version":1,"plan":{"slug":"demo","title":"Demo","source":"issue #426"},"tasks":[]}`),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	otherOwnerPlan, err := realizeManagedCoordinator(
		context.Background(),
		siblingPlanReq,
		runtime,
		hooks,
	)
	wantOtherID, idErr := state.CoordinatorIntentID("426", "", 0)
	if idErr != nil {
		t.Fatal(idErr)
	}
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		otherOwnerPlan.Intent.ID != wantOtherID ||
		otherOwnerPlan.Intent.RuntimeParent != "426" ||
		len(runtime.mutations) != 2 {
		t.Fatalf(
			"different-owner runtime parent = %+v, err=%v, mutations=%d",
			otherOwnerPlan,
			err,
			len(runtime.mutations),
		)
	}

	issueReq := testManagedCoordinatorRequest(sibling)
	reusedIssue, err := realizeManagedCoordinator(
		context.Background(),
		issueReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		reusedIssue.Intent.ID != wantID || len(runtime.mutations) != 2 {
		t.Fatalf(
			"numeric issue coordinator reuse = %+v, err=%v, mutations=%d",
			reusedIssue,
			err,
			len(runtime.mutations),
		)
	}
}

// Coordinator identity drift before the child create refuses the mutation and
// releases the still-unissued child reservation (planned proves non-issuance).
func TestRealizeManagedWorktreeRejectsForeignCoordinatorBeforeChildMutation(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	runtime := &fakeManagedRealizeRuntime{}
	installSuccessfulManagedMutations(t, repo, runtime)
	hooks := deterministicManagedRealizeHooks()
	realizeTestManagedCoordinator(t, repo, runtime, hooks)
	runtime.workspaces[0].TerminalID = "foreign-terminal"

	req := testManagedWorktreeRequest(repo, "foreign-coordinator", 431)
	_, err := realizeManagedWorktree(context.Background(), req, runtime, hooks)
	if err == nil || errors.Is(err, ErrManagedLauncherReadinessDeferred) ||
		!strings.Contains(err.Error(), "coordinator") {
		t.Fatalf("foreign coordinator error = %v", err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("foreign coordinator issued child mutation; calls=%d", len(runtime.mutations))
	}
	requireManagedBranch(t, repo, req, false)
	if _, found := loadManagedWorktreeIntent(t, repo, req); found {
		t.Fatal("foreign coordinator kept the planned child intent")
	}
}

func realizeTestManagedCoordinator(
	t *testing.T,
	repo string,
	runtime *fakeManagedRealizeRuntime,
	hooks ManagedRealizeHooks,
) state.LaunchIntent {
	t.Helper()
	result, err := realizeManagedCoordinator(
		context.Background(),
		testManagedCoordinatorRequest(repo),
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrManagedLauncherReadinessDeferred) {
		t.Fatalf("realize coordinator: %v", err)
	}
	return result.Intent
}

func installSuccessfulManagedMutations(
	t *testing.T,
	repo string,
	runtime *fakeManagedRealizeRuntime,
) {
	t.Helper()
	identity, err := worktree.ResolveRepoIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	runtime.route = backend.OwnedWorktreeRoute{
		GitCommonDir: identity.RepoKey,
		Session:      "fanout-test",
		SocketPath:   "/private/tmp/fanout-test/herdr.sock",
	}
	nextWorkspace := 2
	runtime.mutate = func(req managedTestMutation) (backend.WorktreeMutationResult, error) {
		if req.Kind == backend.WorkspaceCreate {
			workspaceID := "w1"
			observation := backend.WorkspaceObservation{
				WorkspaceID: workspaceID,
				Label:       req.Label,
				Pane: backend.PaneRef{
					Backend: backend.Herdr, Workspace: workspaceID, Pane: workspaceID + ":p1",
				},
				TerminalID: "term-" + workspaceID,
				CWD:        req.CWD,
			}
			runtime.workspaces = append(runtime.workspaces, observation)
			return backend.WorktreeMutationResult{WorkspaceObservation: observation}, nil
		}
		if req.Kind != backend.WorktreeCreate && req.Kind != backend.WorktreeOpen {
			return backend.WorktreeMutationResult{}, errors.New("unsupported fake mutation")
		}
		workspaceID := "w" + strconv.Itoa(nextWorkspace)
		nextWorkspace++
		if req.Kind == backend.WorktreeCreate {
			gitCmdTest(t, repo, "worktree", "add", req.Path, req.Branch)
		}
		observation := backend.WorkspaceObservation{
			WorkspaceID: workspaceID,
			Label:       req.Label,
			Path:        req.Path,
			RepoKey:     req.SourceRepoKey,
			RepoRoot:    req.SourceRepoRoot,
			Pane: backend.PaneRef{
				Backend: backend.Herdr, Workspace: workspaceID, Pane: workspaceID + ":p1",
			},
			TerminalID: "term-" + workspaceID,
			CWD:        req.Path,
		}
		runtime.workspaces = append(runtime.workspaces, observation)
		return backend.WorktreeMutationResult{WorkspaceObservation: observation}, nil
	}
}

func testManagedCoordinatorRequest(repo string) ManagedCoordinatorRequest {
	return ManagedCoordinatorRequest{
		Parent: "425", ProjectRoot: repo, SourceRoot: repo, CWD: repo,
		ManagedSession: "fanout-test", SocketPath: "/private/tmp/fanout-test/herdr.sock",
		TotalTimeout: 300 * time.Second,
	}
}

func testManagedWorktreeRequest(repo, slug string, issueNum int) ManagedWorktreeRequest {
	return ManagedWorktreeRequest{
		Parent: "425", IssueNum: issueNum, ProjectRoot: repo, SourceRoot: repo,
		Slug: slug, BranchName: "fanout/" + slug, BaseBranch: "main",
		AllowMissingOrigin: true,
		WorktreePath:       filepath.Join(repo, ".fanout", "worktrees", slug),
		ManagedSession:     "fanout-test",
		SocketPath:         "/private/tmp/fanout-test/herdr.sock",
		TotalTimeout:       300 * time.Second,
	}
}

func deterministicManagedRealizeHooks() ManagedRealizeHooks {
	next := 0
	now := time.Now().UTC()
	return ManagedRealizeHooks{
		Now: func() time.Time {
			return now
		},
		RandomToken: func() (string, error) {
			next++
			return "token" + strconv.Itoa(next), nil
		},
	}
}

func assertManagedRecoveryClassificationDeadline(
	t *testing.T,
	name string,
	deadline time.Time,
	hasDeadline bool,
) {
	t.Helper()
	remaining := time.Until(deadline)
	if !hasDeadline || remaining <= 0 || remaining > maxManagedRecoveryClassificationTimeout {
		t.Fatalf(
			"%s deadline = %v, %t (remaining %v)",
			name,
			deadline,
			hasDeadline,
			remaining,
		)
	}
}

// testManagedIntentsLock adapts the combined launch lock to the journal-mutation
// shape the intent fixtures need.
type testManagedIntentsLock struct {
	project *state.LockedStore
	*state.LockedLaunchJournal
}

func (l *testManagedIntentsLock) Unlock() error { return l.project.Unlock() }

func lockManagedIntentsForTest(t *testing.T, repo string) *testManagedIntentsLock {
	t.Helper()
	project, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	view, err := project.LaunchJournal(repo)
	if err != nil {
		_ = project.Unlock()
		t.Fatal(err)
	}
	return &testManagedIntentsLock{project: project, LockedLaunchJournal: view}
}

// mutateManagedTestIntent edits one saved intent under the combined launch lock.
func mutateManagedTestIntent(t *testing.T, repo, intentID string, mutate func(*state.LaunchIntent)) {
	t.Helper()
	locked := lockManagedIntentsForTest(t, repo)
	intent, found := locked.FindIntent(intentID)
	if !found {
		t.Fatalf("intent %s not found", intentID)
	}
	mutate(&intent)
	locked.UpsertIntent(intent)
	if err := locked.Save(); err != nil {
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
}

// requireManagedWorktreeIntent loads the journal row for req, failing on absence.
func requireManagedWorktreeIntent(t *testing.T, repo string, req ManagedWorktreeRequest) state.LaunchIntent {
	t.Helper()
	intent, found := loadManagedWorktreeIntent(t, repo, req)
	if !found {
		t.Fatalf("intent for %s/%d/%s not found", req.Parent, req.IssueNum, req.TaskID)
	}
	return intent
}

func loadManagedWorktreeIntent(t *testing.T, repo string, req ManagedWorktreeRequest) (state.LaunchIntent, bool) {
	t.Helper()
	control, err := state.LoadLaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.WorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return control.FindIntent(intentID)
}

// requireManagedBranch asserts the reservation state of req's branch ref.
func requireManagedBranch(t *testing.T, repo string, req ManagedWorktreeRequest, want bool) {
	t.Helper()
	fullRef, err := worktree.LocalBranchRef(context.Background(), repo, req.BranchName)
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := worktree.ObserveBranch(context.Background(), repo, fullRef)
	if err != nil {
		t.Fatal(err)
	}
	if found != want {
		t.Fatalf("branch %s found=%t, want %t", fullRef, found, want)
	}
}

func newManagedRealizeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, repo, "add", "README.md")
	gitCmdTest(
		t,
		repo,
		"-c", "user.name=Fanout Test",
		"-c", "user.email=fanout@example.test",
		"commit", "-m", "init",
	)
	return repo
}

// DiscardWorkloadEnvironment delegates to the real capsule removal so rollback
// tests keep asserting the identity checks and the file that must disappear.
func (f *fakeManagedRealizeRuntime) DiscardWorkloadEnvironment(
	runtimeDir string,
	launch *state.LaunchCapsule,
) error {
	return herdrrun.DiscardWorkloadEnvironment(runtimeDir, launch)
}
