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

type fakeHerdrRealizeRuntime struct {
	workspaces []herdrrun.WorkspaceObservation
	mutations  []herdrrun.WorktreeMutationRequest
	route      herdrrun.OwnedWorktreeRoute
	routeErr   error

	routeDeadline      time.Time
	routeHasDeadline   bool
	routeCalls         int
	observeDeadline    time.Time
	observeHasDeadline bool
	policyErr          error
	observeErr         error
	observeCalls       int
	mutate             func(herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error)
}

func (f *fakeHerdrRealizeRuntime) WorktreeRoute(
	ctx context.Context,
) (herdrrun.OwnedWorktreeRoute, error) {
	f.routeCalls++
	f.routeDeadline, f.routeHasDeadline = ctx.Deadline()
	return f.route, f.routeErr
}

func (f *fakeHerdrRealizeRuntime) VerifyWorktreeSetupPolicy(context.Context) error {
	return f.policyErr
}

func (f *fakeHerdrRealizeRuntime) ObserveWorkspaces(ctx context.Context) ([]herdrrun.WorkspaceObservation, error) {
	f.observeCalls++
	f.observeDeadline, f.observeHasDeadline = ctx.Deadline()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]herdrrun.WorkspaceObservation(nil), f.workspaces...), f.observeErr
}

func (f *fakeHerdrRealizeRuntime) MutateWorktree(
	_ context.Context,
	req herdrrun.WorktreeMutationRequest,
) (herdrrun.WorktreeMutationResult, error) {
	f.mutations = append(f.mutations, req)
	if f.mutate == nil {
		return herdrrun.WorktreeMutationResult{}, errors.New("unexpected mutation")
	}
	return f.mutate(req)
}

func realizeHerdrCoordinator(
	ctx context.Context,
	req HerdrCoordinatorRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrRealizeHooks,
) (HerdrCoordinatorResult, error) {
	locked, err := state.LockProjectForLaunch(req.ProjectRoot)
	if err != nil {
		return HerdrCoordinatorResult{}, err
	}
	result, realizeErr := RealizeHerdrCoordinator(ctx, req, runtime, locked, hooks)
	return result, errors.Join(realizeErr, locked.Unlock())
}

func realizeHerdrWorktree(
	ctx context.Context,
	req HerdrWorktreeRequest,
	runtime HerdrWorktreeRuntime,
	hooks HerdrRealizeHooks,
) (HerdrWorktreeResult, error) {
	locked, err := state.LockProjectForLaunch(req.ProjectRoot)
	if err != nil {
		return HerdrWorktreeResult{}, err
	}
	result, realizeErr := RealizeHerdrWorktree(ctx, req, runtime, locked, hooks)
	return result, errors.Join(realizeErr, locked.Unlock())
}

func TestRealizeHerdrWorktreePersistsIntentAndSkipsReplay(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	coordinator := realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	req := testHerdrWorktreeRequest(repo, "child", 426)
	result, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("realize worktree error = %v", err)
	}
	if result.Intent.Status != state.HerdrIntentRealized || !result.Intent.BranchCreated ||
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

	replayed, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("replay error = %v", err)
	}
	if replayed.Intent.ID != result.Intent.ID || len(runtime.mutations) != 2 {
		t.Fatalf("replay = %+v, mutations = %d; request was reissued", replayed, len(runtime.mutations))
	}
	runtime.route.GitCommonDir = filepath.Join(repo, "foreign.git")
	_, err = realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if err == nil {
		t.Fatal("foreign owned-session route unexpectedly accepted")
	}
}

func TestRealizeHerdrWorktreeReopensVerifiedRealizedCheckout(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	req := testHerdrWorktreeRequest(repo, "reopen", 426)
	realized, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	runtime.workspaces = runtime.workspaces[:1]
	mutationsBefore := len(runtime.mutations)

	reopened, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("reopen error = %v", err)
	}
	if len(runtime.mutations) != mutationsBefore+1 ||
		runtime.mutations[len(runtime.mutations)-1].Kind != herdrrun.WorktreeOpen {
		t.Fatalf("reopen mutations = %+v", runtime.mutations[mutationsBefore:])
	}
	if reopened.Intent.Status != state.HerdrIntentRealized ||
		reopened.Intent.Resource.WorkspaceID == realized.Intent.Resource.WorkspaceID ||
		reopened.Intent.Resource.Label != realized.Intent.Resource.Label {
		t.Fatalf("reopened intent = %+v, original = %+v", reopened.Intent, realized.Intent)
	}
}

func TestRealizeHerdrWorktreeKeepsRejectedOpenRetryable(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	req := testHerdrWorktreeRequest(repo, "reopen-rejected", 427)
	realized, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	runtime.workspaces = runtime.workspaces[:1]
	successfulMutate := runtime.mutate
	runtime.mutate = func(
		mutationReq herdrrun.WorktreeMutationRequest,
	) (herdrrun.WorktreeMutationResult, error) {
		if mutationReq.Kind == herdrrun.WorktreeOpen {
			return herdrrun.WorktreeMutationResult{}, herdrrun.MutationRejectedError{
				Code: "worktree_open_failed", Message: "rejected before open",
			}
		}
		return successfulMutate(mutationReq)
	}

	_, err = realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, herdrrun.ErrMutationRejected) {
		t.Fatalf("rejected reopen error = %v", err)
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := control.FindIntent(realized.Intent.ID)
	if !found || intent.Status != state.HerdrIntentRealized || intent.Failure != "" {
		t.Fatalf("rejected reopen intent = (%+v,%t), want retryable realized", intent, found)
	}

	runtime.mutate = successfulMutate
	reopened, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		reopened.Intent.Status != state.HerdrIntentRealized {
		t.Fatalf("retry reopen result = %+v, err=%v", reopened, err)
	}
}

func TestRealizeHerdrRoutesCapTotalTimeout(t *testing.T) {
	for _, kind := range []string{"coordinator", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			repo := newHerdrRealizeRepo(t)
			stop := errors.New("stop after route")
			runtime := &fakeHerdrRealizeRuntime{routeErr: stop}
			now := time.Now().UTC()
			hooks := HerdrRealizeHooks{Now: func() time.Time { return now }}

			var err error
			switch kind {
			case "coordinator":
				req := testHerdrCoordinatorRequest(repo)
				_, err = realizeHerdrCoordinator(context.Background(), req, runtime, hooks)
			case "worktree":
				req := testHerdrWorktreeRequest(repo, "route-timeout", 426)
				_, err = realizeHerdrWorktree(context.Background(), req, runtime, hooks)
			}
			if !errors.Is(err, stop) {
				t.Fatalf("route error = %v", err)
			}
			remaining := time.Until(runtime.routeDeadline)
			if !runtime.routeHasDeadline || remaining <= 0 ||
				remaining > maxHerdrRecoveryClassificationTimeout {
				t.Fatalf(
					"route deadline = %v, %t (remaining %v), want at most %v from %v",
					runtime.routeDeadline,
					runtime.routeHasDeadline,
					remaining,
					maxHerdrRecoveryClassificationTimeout,
					now,
				)
			}
		})
	}
}

func TestRealizeHerdrFreshCancellationBeforeRouteDoesNotCreateIntent(t *testing.T) {
	for _, kind := range []string{"coordinator", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			repo := newHerdrRealizeRepo(t)
			runtime := &fakeHerdrRealizeRuntime{}
			installSuccessfulHerdrMutations(t, repo, runtime)
			baseHooks := deterministicHerdrRealizeHooks()
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
				_, err = realizeHerdrCoordinator(ctx, testHerdrCoordinatorRequest(repo), runtime, hooks)
			case "worktree":
				_, err = realizeHerdrWorktree(
					ctx,
					testHerdrWorktreeRequest(repo, "canceled-before-route", 439),
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
			control, loadErr := state.LoadHerdrControl(repo)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if len(control.Intents) != 0 || len(control.Rows) != 0 {
				t.Fatalf("control after fresh cancellation = %+v", control)
			}
		})
	}
}

func TestRealizeHerdrCoordinatorBoundsExpiredRouteClassification(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realized := realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	locked, err := state.LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := locked.FindIntent(realized.ID)
	if !found {
		t.Fatalf("intent %s not found", realized.ID)
	}
	intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	_, err = realizeHerdrCoordinator(
		context.Background(),
		testHerdrCoordinatorRequest(repo),
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("expired coordinator recovery error = %v", err)
	}
	assertHerdrRecoveryClassificationDeadline(
		t,
		"expired coordinator route",
		runtime.routeDeadline,
		runtime.routeHasDeadline,
	)
}

func TestRealizeHerdrWorktreeUsesSavedRouteDeadline(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	req := testHerdrWorktreeRequest(repo, "saved-route-deadline", 438)
	realized, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	locked, err := state.LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := locked.FindIntent(realized.Intent.ID)
	if !found {
		t.Fatalf("intent %s not found", realized.Intent.ID)
	}
	intent.ExpiresUnixMS = hooks.Now().Add(2 * time.Second).UnixMilli()
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	_, err = realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
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

func TestRealizeHerdrRollsBackMutationNotIssued(t *testing.T) {
	t.Run("coordinator", func(t *testing.T) {
		repo := newHerdrRealizeRepo(t)
		runtime := &fakeHerdrRealizeRuntime{}
		installSuccessfulHerdrMutations(t, repo, runtime)
		runtime.mutate = func(
			herdrrun.WorktreeMutationRequest,
		) (herdrrun.WorktreeMutationResult, error) {
			return herdrrun.WorktreeMutationResult{}, herdrrun.MutationNotIssuedError{
				Cause: errors.New("owned admission failed"),
			}
		}

		_, err := realizeHerdrCoordinator(
			context.Background(),
			testHerdrCoordinatorRequest(repo),
			runtime,
			deterministicHerdrRealizeHooks(),
		)
		if !errors.Is(err, herdrrun.ErrMutationNotIssued) {
			t.Fatalf("coordinator error = %v", err)
		}
		control, err := state.LoadHerdrControl(repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(control.Intents) != 0 {
			t.Fatalf("coordinator intents = %#v, want rollback", control.Intents)
		}
	})

	t.Run("worktree", func(t *testing.T) {
		repo := newHerdrRealizeRepo(t)
		runtime := &fakeHerdrRealizeRuntime{}
		installSuccessfulHerdrMutations(t, repo, runtime)
		hooks := deterministicHerdrRealizeHooks()
		realizeTestHerdrCoordinator(t, repo, runtime, hooks)
		runtime.mutate = func(
			herdrrun.WorktreeMutationRequest,
		) (herdrrun.WorktreeMutationResult, error) {
			return herdrrun.WorktreeMutationResult{}, herdrrun.MutationNotIssuedError{
				Cause: errors.New("owned admission failed"),
			}
		}

		req := testHerdrWorktreeRequest(repo, "not-issued", 426)
		_, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
		if !errors.Is(err, herdrrun.ErrMutationNotIssued) {
			t.Fatalf("worktree error = %v", err)
		}
		fullRef, err := worktree.HerdrBranchRef(repo, req.BranchName)
		if err != nil {
			t.Fatal(err)
		}
		if head, found, observeErr := worktree.ObserveHerdrBranch(repo, fullRef); observeErr != nil {
			t.Fatal(observeErr)
		} else if found {
			t.Fatalf("not-issued branch = %s, want rollback", head)
		}
		control, err := state.LoadHerdrControl(repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(control.Intents) != 1 ||
			control.Intents[0].Kind != state.HerdrIntentCoordinator {
			t.Fatalf("worktree intents = %#v, want coordinator only", control.Intents)
		}
	})
}

func TestRealizeHerdrWorktreeRecoversCompletedUnissuedRollback(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	successfulMutate := runtime.mutate
	ctx, cancel := context.WithCancel(context.Background())
	runtime.mutate = func(
		req herdrrun.WorktreeMutationRequest,
	) (herdrrun.WorktreeMutationResult, error) {
		if req.Kind == herdrrun.WorktreeCreate {
			cancel()
			return herdrrun.WorktreeMutationResult{}, context.DeadlineExceeded
		}
		return successfulMutate(req)
	}
	req := testHerdrWorktreeRequest(repo, "rollback-recovery", 428)
	_, err := realizeHerdrWorktree(ctx, req, runtime, hooks)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted rollback setup error = %v", err)
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.HerdrWorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := control.FindIntent(intentID)
	if !found || intent.Status != state.HerdrIntentIssued || !intent.BranchCreated {
		t.Fatalf("interrupted rollback intent = (%+v,%t)", intent, found)
	}
	if deleteErr := worktree.DeleteReservedHerdrBranch(
		repo,
		intent.FullBranchRef,
		intent.BaseSHA,
	); deleteErr != nil {
		t.Fatal(deleteErr)
	}

	runtime.mutate = successfulMutate
	mutationsBefore := len(runtime.mutations)
	_, err = realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if err == nil || !strings.Contains(err.Error(), "rollback; retry launch") {
		t.Fatalf("completed rollback recovery error = %v", err)
	}
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("completed rollback recovery reissued a mutation")
	}
	control, err = state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := control.FindIntent(intentID); found {
		t.Fatal("completed rollback recovery kept the issued intent")
	}

	relaunched, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		relaunched.Intent.Status != state.HerdrIntentRealized {
		t.Fatalf("fresh launch after rollback recovery = %+v, err=%v", relaunched, err)
	}
}

func TestRealizeHerdrWorktreeChecksPolicyBeforeBranchReservation(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	runtime.policyErr = errors.New("unexpected owned plugin")

	req := testHerdrWorktreeRequest(repo, "policy-blocked", 426)
	_, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if err == nil || !strings.Contains(err.Error(), "unexpected owned plugin") {
		t.Fatalf("policy error = %v", err)
	}
	fullRef, err := worktree.HerdrBranchRef(repo, req.BranchName)
	if err != nil {
		t.Fatal(err)
	}
	if head, found, observeErr := worktree.ObserveHerdrBranch(repo, fullRef); observeErr != nil {
		t.Fatal(observeErr)
	} else if found {
		t.Fatalf("policy-blocked branch = %s, want absent", head)
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.HerdrWorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := control.FindIntent(intentID)
	if !found || intent.Status != state.HerdrIntentPlanned || intent.BranchCreated {
		t.Fatalf("policy-blocked intent = %+v, found=%t", intent, found)
	}
}

func TestRealizeHerdrRejectsTmuxBindingBeforeIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	locked.Panes = append(locked.Panes, state.Pane{
		Parent: "425", IssueNum: 999, Backend: backend.Tmux,
	})
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	_, err = realizeHerdrCoordinator(
		context.Background(),
		testHerdrCoordinatorRequest(repo),
		runtime,
		deterministicHerdrRealizeHooks(),
	)
	if err == nil {
		t.Fatal("tmux binding unexpectedly allowed a Herdr intent")
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(control.Intents) != 0 {
		t.Fatalf("Herdr intents = %#v, want none", control.Intents)
	}
}

func TestVerifyHerdrStateBindingsResolvesIssueSourcedPlanParent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
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
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "sibling", sibling, "HEAD")
	locked, err := state.LockProject(sibling)
	if err != nil {
		t.Fatal(err)
	}
	locked.Panes = append(locked.Panes, state.Pane{
		Parent: "425", IssueNum: 426, Backend: backend.Tmux,
	})
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	runtimeParent, err := resolveHerdrRuntimeParent(
		repo,
		"plan:demo",
		repo,
		state.HerdrControlStore{},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = verifyHerdrStateBindings(repo, runtimeParent, state.Store{})
	if err == nil || !strings.Contains(err.Error(), "runtime backend for parent 425 became tmux") {
		t.Fatalf("issue-sourced plan binding error = %v", err)
	}
}

func TestWorkspaceHasHerdrResourceMatchesSavedRootAmongMultiplePanes(t *testing.T) {
	expected := state.HerdrResource{
		WorkspaceID: "w1",
		Label:       "fanout-coordinator-token",
		PaneID:      "w1:p1",
		TerminalID:  "term-1",
		CurrentPath: "/repo",
	}
	observation := herdrrun.WorkspaceObservation{
		WorkspaceID: expected.WorkspaceID,
		Label:       expected.Label,
		Panes: []herdrrun.WorkspacePaneObservation{
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
	if !workspaceHasHerdrResource(observation, expected) {
		t.Fatal("saved root pane was not matched in multi-pane workspace")
	}
	observation.Panes = observation.Panes[1:]
	if workspaceHasHerdrResource(observation, expected) {
		t.Fatal("workspace without the saved root pane was accepted")
	}
}

func TestRealizeHerdrUsesFinalRowsAsIdempotentBindings(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	coordinator := realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	finalizeHerdrTestIntent(t, repo, coordinator)
	routeCallsBefore := runtime.routeCalls
	runtime.routeErr = errors.New("route unavailable")

	coordinatorResult, err := realizeHerdrCoordinator(
		context.Background(),
		testHerdrCoordinatorRequest(repo),
		runtime,
		hooks,
	)
	if err != nil || !coordinatorResult.AlreadyFinalized ||
		coordinatorResult.Row.ID != coordinator.ID || len(runtime.mutations) != 1 ||
		runtime.routeCalls != routeCallsBefore {
		t.Fatalf(
			"final coordinator replay = %+v err=%v mutations=%d",
			coordinatorResult,
			err,
			len(runtime.mutations),
		)
	}
	runtime.routeErr = nil

	req := testHerdrWorktreeRequest(repo, "final-row", 432)
	child, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("child with finalized coordinator error = %v", err)
	}
	if child.Intent.Coordinator != coordinator.Resource {
		t.Fatalf("child coordinator = %+v, want finalized %+v", child.Intent.Coordinator, coordinator.Resource)
	}
	finalizeHerdrTestIntent(t, repo, child.Intent)
	routeCallsBefore = runtime.routeCalls
	runtime.routeErr = errors.New("route unavailable")

	finalChild, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if err != nil || !finalChild.AlreadyFinalized ||
		finalChild.Row.ID != child.Intent.ID || finalChild.Pane != child.Pane ||
		len(runtime.mutations) != 2 || runtime.routeCalls != routeCallsBefore {
		t.Fatalf(
			"final child replay = %+v err=%v mutations=%d",
			finalChild,
			err,
			len(runtime.mutations),
		)
	}
}

func TestRealizeHerdrPlanTaskReusesSavedChildNames(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	coordinatorReq := testHerdrCoordinatorRequest(repo)
	coordinatorReq.Parent = "plan:demo"
	if _, err := realizeHerdrCoordinator(
		context.Background(),
		coordinatorReq,
		runtime,
		hooks,
	); !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("plan coordinator error = %v", err)
	}

	req := testHerdrWorktreeRequest(repo, "saved-task", 0)
	req.Parent = "plan:demo"
	req.TaskID = "task-1"
	child, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("initial plan task error = %v", err)
	}

	renamed := req
	renamed.Slug = "renamed-task"
	renamed.BranchName = "fanout/renamed-task"
	renamed.WorktreePath = filepath.Join(repo, ".fanout", "worktrees", renamed.Slug)
	reused, err := realizeHerdrWorktree(context.Background(), renamed, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
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

	finalizeHerdrTestIntent(t, repo, child.Intent)
	finalized, err := realizeHerdrWorktree(context.Background(), renamed, runtime, hooks)
	if err != nil || !finalized.AlreadyFinalized ||
		finalized.Row.Slug != child.Intent.Slug ||
		finalized.Row.BranchName != child.Intent.BranchName ||
		finalized.Row.WorktreePath != child.Intent.WorktreePath ||
		len(runtime.mutations) != 2 {
		t.Fatalf(
			"renamed final replay = %+v, original = %+v, err=%v, mutations=%d",
			finalized.Row,
			child.Intent,
			err,
			len(runtime.mutations),
		)
	}
}

func TestRealizeHerdrWorktreeAdoptsResponseLossPostcondition(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	successfulMutate := runtime.mutate
	runtime.mutate = func(req herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error) {
		result, err := successfulMutate(req)
		if err != nil {
			return result, err
		}
		if req.Kind == herdrrun.WorktreeCreate {
			return herdrrun.WorktreeMutationResult{}, errors.New("injected response loss")
		}
		return result, nil
	}
	result, err := realizeHerdrWorktree(
		context.Background(),
		testHerdrWorktreeRequest(repo, "response-loss", 427),
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("response-loss recovery error = %v", err)
	}
	if result.Intent.Status != state.HerdrIntentRealized || result.Intent.Resource.WorkspaceID == "" {
		t.Fatalf("response-loss result = %+v", result)
	}
}

func TestRealizeHerdrWorktreeRecoversExpiredIssuedIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	req := testHerdrWorktreeRequest(repo, "expired-issued", 432)
	realized, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	locked, err := state.LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := locked.FindIntent(realized.Intent.ID)
	if !found {
		t.Fatalf("intent %s not found", realized.Intent.ID)
	}
	intent.Status = state.HerdrIntentIssued
	intent.Resource = state.HerdrResource{}
	intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	mutationsBefore := len(runtime.mutations)
	recovered, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("expired issued recovery error = %v", err)
	}
	if recovered.Intent.Status != state.HerdrIntentRealized ||
		recovered.Intent.Resource.WorkspaceID == "" {
		t.Fatalf("expired issued recovery = %+v", recovered)
	}
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("expired issued intent reissued the Herdr mutation")
	}
	assertHerdrRecoveryClassificationDeadline(
		t,
		"expired issued route",
		runtime.routeDeadline,
		runtime.routeHasDeadline,
	)
	assertHerdrRecoveryClassificationDeadline(
		t,
		"expired issued observation",
		runtime.observeDeadline,
		runtime.observeHasDeadline,
	)
}

func TestRealizeHerdrWorktreeDoesNotOpenExpiredRealizedIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	req := testHerdrWorktreeRequest(repo, "expired-realized", 437)
	realized, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("initial realization error = %v", err)
	}
	live := make([]herdrrun.WorkspaceObservation, 0, len(runtime.workspaces)-1)
	for _, workspace := range runtime.workspaces {
		if workspace.WorkspaceID != realized.Intent.Resource.WorkspaceID {
			live = append(live, workspace)
		}
	}
	runtime.workspaces = live

	locked, err := state.LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := locked.FindIntent(realized.Intent.ID)
	if !found {
		t.Fatalf("intent %s not found", realized.Intent.ID)
	}
	intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	mutationsBefore := len(runtime.mutations)
	_, retryErr := realizeHerdrWorktree(
		context.Background(),
		req,
		runtime,
		hooks,
	)
	if !errors.Is(retryErr, ErrHerdrManualCleanupRequired) {
		t.Fatalf("expired realized retry error = %v", retryErr)
	}
	assertHerdrRecoveryClassificationDeadline(
		t,
		"expired realized route",
		runtime.routeDeadline,
		runtime.routeHasDeadline,
	)
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("expired realized intent issued a worktree open mutation")
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intent, found = control.FindIntent(realized.Intent.ID)
	if !found || intent.Status != state.HerdrIntentManualCleanupRequired {
		t.Fatalf("expired realized intent = (%+v, %t)", intent, found)
	}
}

func TestRealizeHerdrWorktreeRollsBackExpiredPlannedIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	stop := errors.New("stop after branch reservation")
	runtime.observeErr = stop
	req := testHerdrWorktreeRequest(repo, "expired-planned", 434)
	if _, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, stop) {
		t.Fatalf("initial planned error = %v", err)
	}
	locked, err := state.LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.HerdrWorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := locked.FindIntent(intentID)
	if !found || !intent.BranchCreated || intent.Status != state.HerdrIntentPlanned {
		t.Fatalf("planned intent = (%+v,%t)", intent, found)
	}
	intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	runtime.observeErr = nil
	routeCallsBefore := runtime.routeCalls

	if _, realizeErr := realizeHerdrWorktree(
		context.Background(),
		req,
		runtime,
		hooks,
	); !errors.Is(
		realizeErr,
		errHerdrIntentDeadlineExpired,
	) {
		t.Fatalf("expired planned error = %v", realizeErr)
	}
	if runtime.routeCalls != routeCallsBefore {
		t.Fatal("expired planned retry validated the Herdr route before rollback")
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := control.FindIntent(intentID); found {
		t.Fatalf("expired planned intent %s was not removed", intentID)
	}
	if head, found, err := worktree.ObserveHerdrBranch(
		repo,
		intent.FullBranchRef,
	); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatalf("expired planned branch remains at %s", head)
	}
}

func TestRealizeHerdrWorktreeRollsBackCanceledPlannedIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	stop := errors.New("stop after branch reservation")
	runtime.observeErr = stop
	req := testHerdrWorktreeRequest(repo, "canceled-planned", 440)
	if _, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, stop) {
		t.Fatalf("initial planned error = %v", err)
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.HerdrWorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := control.FindIntent(intentID)
	if !found || !intent.BranchCreated || intent.Status != state.HerdrIntentPlanned {
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
	if _, realizeErr := realizeHerdrWorktree(ctx, req, runtime, cancelHooks); !errors.Is(
		realizeErr,
		context.Canceled,
	) {
		t.Fatalf("canceled planned error = %v", realizeErr)
	}
	if runtime.routeCalls != routeCallsBefore {
		t.Fatal("canceled planned retry validated the Herdr route before rollback")
	}
	control, err = state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := control.FindIntent(intentID); found {
		t.Fatalf("canceled planned intent %s was not removed", intentID)
	}
	if head, branchFound, observeErr := worktree.ObserveHerdrBranch(
		repo,
		intent.FullBranchRef,
	); observeErr != nil {
		t.Fatal(observeErr)
	} else if branchFound {
		t.Fatalf("canceled planned branch remains at %s", head)
	}
}

func TestRealizeHerdrWorktreeKeepsExpiredPlannedIntentWhenBranchOwnershipWasNotSaved(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	stop := errors.New("stop after branch reservation")
	runtime.observeErr = stop
	req := testHerdrWorktreeRequest(repo, "expired-ambiguous-branch", 435)
	if _, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, stop) {
		t.Fatalf("initial planned error = %v", err)
	}
	locked, err := state.LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.HerdrWorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := locked.FindIntent(intentID)
	if !found || !intent.BranchCreated {
		t.Fatalf("planned intent = (%+v,%t)", intent, found)
	}
	intent.BranchCreated = false
	intent.ExpiresUnixMS = hooks.Now().Add(-time.Second).UnixMilli()
	locked.UpsertIntent(intent)
	if saveErr := locked.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	runtime.observeErr = nil

	if _, realizeErr := realizeHerdrWorktree(
		context.Background(),
		req,
		runtime,
		hooks,
	); !errors.Is(realizeErr, ErrHerdrManualCleanupRequired) {
		t.Fatalf("ambiguous branch ownership error = %v", realizeErr)
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, found := control.FindIntent(intentID)
	if !found || saved.Status != state.HerdrIntentManualCleanupRequired ||
		!strings.Contains(saved.Failure, "branch exists without persisted ownership") {
		t.Fatalf("ambiguous ownership intent = (%+v,%t)", saved, found)
	}
	if head, found, err := worktree.ObserveHerdrBranch(repo, intent.FullBranchRef); err != nil {
		t.Fatal(err)
	} else if !found || head != intent.ExpectedHead {
		t.Fatalf("ambiguous branch = (%s,%t), want preserved at %s", head, found, intent.ExpectedHead)
	}
}

func TestRealizeHerdrWorktreePreservesIssuedIntentWhenMutationContextIsCanceled(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	successfulMutate := runtime.mutate
	ctx, cancel := context.WithCancel(context.Background())
	runtime.mutate = func(req herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error) {
		result, err := successfulMutate(req)
		if req.Kind == herdrrun.WorktreeCreate {
			cancel()
			return result, context.DeadlineExceeded
		}
		return result, err
	}
	req := testHerdrWorktreeRequest(repo, "canceled-recovery", 433)
	observesBefore := runtime.observeCalls
	result, err := realizeHerdrWorktree(ctx, req, runtime, hooks)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery result = %+v, err=%v", result, err)
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.HerdrWorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	intent, found := control.FindIntent(intentID)
	if !found || intent.Status != state.HerdrIntentIssued {
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
	recovered, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("fresh recovery error = %v", err)
	}
	if recovered.Intent.Status != state.HerdrIntentRealized ||
		recovered.Intent.Resource.WorkspaceID == "" {
		t.Fatalf("fresh recovery result = %+v", recovered)
	}
	if len(runtime.mutations) != mutationsBefore {
		t.Fatal("fresh recovery reissued the Herdr mutation")
	}
}

func TestRealizeHerdrWorktreeFailsClosedOnAmbiguousResponseLoss(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	runtime.mutate = func(req herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error) {
		if req.Kind == herdrrun.WorktreeCreate {
			return herdrrun.WorktreeMutationResult{}, errors.New("injected response loss")
		}
		return herdrrun.WorktreeMutationResult{}, errors.New("unexpected mutation")
	}

	req := testHerdrWorktreeRequest(repo, "ambiguous", 428)
	_, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("ambiguous response error = %v", err)
	}
	fullRef, refErr := worktree.HerdrBranchRef(repo, req.BranchName)
	if refErr != nil {
		t.Fatal(refErr)
	}
	if _, found, observeErr := worktree.ObserveHerdrBranch(repo, fullRef); observeErr != nil || !found {
		t.Fatalf("ambiguous branch was removed: found=%t err=%v", found, observeErr)
	}
	control, loadErr := state.LoadHerdrControl(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	intentID, _ := state.HerdrWorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	intent, found := control.FindIntent(intentID)
	if !found || intent.Status != state.HerdrIntentManualCleanupRequired {
		t.Fatalf("ambiguous intent = (%+v,%t)", intent, found)
	}

	mutationCount := len(runtime.mutations)
	if _, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks); !errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("manual cleanup replay error = %v", err)
	}
	if len(runtime.mutations) != mutationCount {
		t.Fatal("manual-cleanup intent reissued the Herdr mutation")
	}
}

func TestRealizeHerdrWorktreeDeletesBranchOnlyAfterStructuredRejection(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	runtime.mutate = func(req herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error) {
		if req.Kind == herdrrun.WorktreeCreate {
			return herdrrun.WorktreeMutationResult{}, herdrrun.MutationRejectedError{
				Code: "worktree_create_failed", Message: "rejected before create",
			}
		}
		return herdrrun.WorktreeMutationResult{}, errors.New("unexpected mutation")
	}

	req := testHerdrWorktreeRequest(repo, "rejected", 429)
	_, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, herdrrun.ErrMutationRejected) {
		t.Fatalf("structured rejection error = %v", err)
	}
	fullRef, refErr := worktree.HerdrBranchRef(repo, req.BranchName)
	if refErr != nil {
		t.Fatal(refErr)
	}
	if _, found, observeErr := worktree.ObserveHerdrBranch(repo, fullRef); observeErr != nil || found {
		t.Fatalf("rejected branch = found:%t err:%v, want deleted", found, observeErr)
	}
	control, loadErr := state.LoadHerdrControl(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	intentID, _ := state.HerdrWorktreeIntentID(req.Parent, "", req.IssueNum, req.TaskID)
	if _, found := control.FindIntent(intentID); found {
		t.Fatal("rejected non-mutation left a provisional intent")
	}
}

func TestRealizeHerdrWorktreeAdoptsExistingBranchWithoutBaseArgument(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	gitCmdTest(t, repo, "branch", "fanout/existing", "HEAD")
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	req := testHerdrWorktreeRequest(repo, "existing", 430)
	result, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
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

func TestRealizeHerdrCoordinatorAdoptsResponseLossAndNeverReissues(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	successfulMutate := runtime.mutate
	runtime.mutate = func(req herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error) {
		result, err := successfulMutate(req)
		if err != nil {
			return result, err
		}
		return herdrrun.WorktreeMutationResult{}, errors.New("injected coordinator response loss")
	}
	hooks := deterministicHerdrRealizeHooks()
	req := testHerdrCoordinatorRequest(repo)
	result, err := realizeHerdrCoordinator(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		result.Intent.Status != state.HerdrIntentRealized {
		t.Fatalf("coordinator response-loss result = %+v err=%v", result, err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("coordinator mutations = %d, want 1", len(runtime.mutations))
	}
	if _, err := realizeHerdrCoordinator(context.Background(), req, runtime, hooks); !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("coordinator replay error = %v", err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatal("coordinator response-loss replay reissued mutation")
	}
}

func TestRealizeHerdrReusesNumericParentCoordinatorAcrossLinkedWorktrees(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	coordinator := realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "linked-coordinator", sibling, "HEAD")
	siblingCoordinatorReq := testHerdrCoordinatorRequest(sibling)
	reused, err := realizeHerdrCoordinator(
		context.Background(),
		siblingCoordinatorReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
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

	childReq := testHerdrWorktreeRequest(sibling, "linked-child", 435)
	child, err := realizeHerdrWorktree(context.Background(), childReq, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("linked child error = %v", err)
	}
	if child.Intent.Coordinator != coordinator.Resource || len(runtime.mutations) != 2 {
		t.Fatalf(
			"linked child coordinator = %+v, want %+v; mutations = %d",
			child.Intent.Coordinator,
			coordinator.Resource,
			len(runtime.mutations),
		)
	}

	other := filepath.Join(t.TempDir(), "other")
	gitCmdTest(t, repo, "worktree", "add", "-b", "linked-child-reuse", other, "HEAD")
	otherChildReq := testHerdrWorktreeRequest(other, "linked-child", 435)
	reusedChild, err := realizeHerdrWorktree(
		context.Background(),
		otherChildReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
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
	finalizeHerdrTestIntent(t, repo, child.Intent)
	finalChild, err := realizeHerdrWorktree(
		context.Background(),
		otherChildReq,
		runtime,
		hooks,
	)
	if err != nil || !finalChild.AlreadyFinalized ||
		finalChild.Row.ID != child.Intent.ID ||
		finalChild.Row.WorktreePath != child.Intent.WorktreePath ||
		len(runtime.mutations) != 2 {
		t.Fatalf(
			"linked finalized child reuse = %+v, err = %v, mutations = %d",
			finalChild,
			err,
			len(runtime.mutations),
		)
	}
}

func TestRealizeHerdrResumesPlannedChildAtSavedOwnerAcrossLinkedWorktrees(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)

	savedReq := testHerdrWorktreeRequest(repo, "linked-planned-child", 436)
	runtime.policyErr = errors.New("stop before child mutation")
	if _, err := realizeHerdrWorktree(
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
	retryReq := testHerdrWorktreeRequest(sibling, "linked-planned-child", 436)
	result, err := realizeHerdrWorktree(
		context.Background(),
		retryReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
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
	savedSource, sourceErr := worktree.ResolveHerdrRepoIdentity(repo)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	if childMutation.ProjectRoot != repo || childMutation.SourceRoot != savedSource.RepoRoot ||
		childMutation.SourceRepoRoot != savedSource.RepoRoot ||
		childMutation.Path != savedReq.WorktreePath {
		t.Fatalf("linked planned child mutation = %+v", childMutation)
	}
}

func TestRealizeHerdrResumesPlannedCoordinatorAtSavedPathAcrossLinkedWorktrees(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	savedPath, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	runtime.policyErr = errors.New("stop before coordinator mutation")
	hooks := deterministicHerdrRealizeHooks()
	if _, initialErr := realizeHerdrCoordinator(
		context.Background(),
		testHerdrCoordinatorRequest(repo),
		runtime,
		hooks,
	); initialErr == nil ||
		!strings.Contains(initialErr.Error(), "stop before coordinator mutation") {
		t.Fatalf("initial planned coordinator error = %v", initialErr)
	}
	if len(runtime.mutations) != 0 {
		t.Fatal("planned coordinator unexpectedly issued a mutation")
	}

	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "linked-planned-coordinator", sibling, "HEAD")
	runtime.policyErr = nil
	resumed, err := realizeHerdrCoordinator(
		context.Background(),
		testHerdrCoordinatorRequest(sibling),
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		resumed.Intent.WorktreePath != savedPath ||
		resumed.Intent.Resource.CurrentPath != savedPath ||
		len(runtime.mutations) != 1 ||
		runtime.mutations[0].SourceRoot != savedPath ||
		runtime.mutations[0].SourceRepoRoot != savedPath ||
		runtime.mutations[0].CWD != savedPath {
		t.Fatalf(
			"resumed planned coordinator = %+v, err=%v, mutation=%+v",
			resumed.Intent,
			err,
			runtime.mutations,
		)
	}
}

func TestRealizeHerdrResolvesPlanRuntimeParentPerOwnerRoot(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
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
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	planReq := testHerdrCoordinatorRequest(repo)
	planReq.Parent = "plan:demo"
	coordinator, err := realizeHerdrCoordinator(
		context.Background(),
		planReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("issue-sourced coordinator error = %v", err)
	}
	wantID, err := state.HerdrCoordinatorIntentID("425", "")
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.Intent.ID != wantID || coordinator.Intent.RuntimeParent != "425" {
		t.Fatalf("issue-sourced coordinator = %+v, want runtime parent 425", coordinator.Intent)
	}
	if removeErr := os.Remove(planPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	replayedPlan, err := realizeHerdrCoordinator(
		context.Background(),
		planReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
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
	siblingPlanReq := testHerdrCoordinatorRequest(sibling)
	siblingPlanReq.Parent = "plan:demo"
	reusedPlan, err := realizeHerdrCoordinator(
		context.Background(),
		siblingPlanReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
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
	otherOwnerPlan, err := realizeHerdrCoordinator(
		context.Background(),
		siblingPlanReq,
		runtime,
		hooks,
	)
	wantOtherID, idErr := state.HerdrCoordinatorIntentID("426", "")
	if idErr != nil {
		t.Fatal(idErr)
	}
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
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

	issueReq := testHerdrCoordinatorRequest(sibling)
	reusedIssue, err := realizeHerdrCoordinator(
		context.Background(),
		issueReq,
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		reusedIssue.Intent.ID != wantID || len(runtime.mutations) != 2 {
		t.Fatalf(
			"numeric issue coordinator reuse = %+v, err=%v, mutations=%d",
			reusedIssue,
			err,
			len(runtime.mutations),
		)
	}
}

func TestRealizeHerdrWorktreeRejectsForeignCoordinatorBeforeBranch(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	runtime.workspaces[0].TerminalID = "foreign-terminal"

	req := testHerdrWorktreeRequest(repo, "foreign-coordinator", 431)
	_, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) {
		t.Fatalf("foreign coordinator error = %v", err)
	}
	fullRef, refErr := worktree.HerdrBranchRef(repo, req.BranchName)
	if refErr != nil {
		t.Fatal(refErr)
	}
	if _, found, observeErr := worktree.ObserveHerdrBranch(repo, fullRef); observeErr != nil || !found {
		t.Fatalf("branch reservation state = found:%t err:%v", found, observeErr)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("foreign coordinator issued child mutation; calls=%d", len(runtime.mutations))
	}
}

func realizeTestHerdrCoordinator(
	t *testing.T,
	repo string,
	runtime *fakeHerdrRealizeRuntime,
	hooks HerdrRealizeHooks,
) state.HerdrIntent {
	t.Helper()
	result, err := realizeHerdrCoordinator(
		context.Background(),
		testHerdrCoordinatorRequest(repo),
		runtime,
		hooks,
	)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("realize coordinator: %v", err)
	}
	return result.Intent
}

func installSuccessfulHerdrMutations(
	t *testing.T,
	repo string,
	runtime *fakeHerdrRealizeRuntime,
) {
	t.Helper()
	identity, err := worktree.ResolveHerdrRepoIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	runtime.route = herdrrun.OwnedWorktreeRoute{
		GitCommonDir: identity.RepoKey,
		Session:      "fanout-test",
		SocketPath:   "/private/tmp/fanout-test/herdr.sock",
	}
	nextWorkspace := 2
	runtime.mutate = func(req herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error) {
		if req.Kind == herdrrun.WorkspaceCreate {
			workspaceID := "w1"
			observation := herdrrun.WorkspaceObservation{
				WorkspaceID: workspaceID,
				Label:       req.Label,
				Pane: backend.PaneRef{
					Backend: backend.Herdr, Workspace: workspaceID, Pane: workspaceID + ":p1",
				},
				TerminalID: "term-" + workspaceID,
				CWD:        req.CWD,
			}
			runtime.workspaces = append(runtime.workspaces, observation)
			return herdrrun.WorktreeMutationResult{WorkspaceObservation: observation}, nil
		}
		if req.Kind != herdrrun.WorktreeCreate && req.Kind != herdrrun.WorktreeOpen {
			return herdrrun.WorktreeMutationResult{}, errors.New("unsupported fake mutation")
		}
		workspaceID := "w" + strconv.Itoa(nextWorkspace)
		nextWorkspace++
		if req.Kind == herdrrun.WorktreeCreate {
			gitCmdTest(t, repo, "worktree", "add", req.Path, req.Branch)
		}
		observation := herdrrun.WorkspaceObservation{
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
		return herdrrun.WorktreeMutationResult{WorkspaceObservation: observation}, nil
	}
}

func testHerdrCoordinatorRequest(repo string) HerdrCoordinatorRequest {
	return HerdrCoordinatorRequest{
		Parent: "425", ProjectRoot: repo, SourceRoot: repo, CWD: repo,
		HerdrSession: "fanout-test", SocketPath: "/private/tmp/fanout-test/herdr.sock",
		TotalTimeout: 300 * time.Second,
	}
}

func testHerdrWorktreeRequest(repo, slug string, issueNum int) HerdrWorktreeRequest {
	return HerdrWorktreeRequest{
		Parent: "425", IssueNum: issueNum, ProjectRoot: repo, SourceRoot: repo,
		Slug: slug, BranchName: "fanout/" + slug, BaseBranch: "main",
		AllowMissingOrigin: true,
		WorktreePath:       filepath.Join(repo, ".fanout", "worktrees", slug),
		HerdrSession:       "fanout-test",
		SocketPath:         "/private/tmp/fanout-test/herdr.sock",
		TotalTimeout:       300 * time.Second,
	}
}

func deterministicHerdrRealizeHooks() HerdrRealizeHooks {
	next := 0
	now := time.Now().UTC()
	return HerdrRealizeHooks{
		Now: func() time.Time {
			return now
		},
		RandomToken: func() (string, error) {
			next++
			return "token" + strconv.Itoa(next), nil
		},
	}
}

func assertHerdrRecoveryClassificationDeadline(
	t *testing.T,
	name string,
	deadline time.Time,
	hasDeadline bool,
) {
	t.Helper()
	remaining := time.Until(deadline)
	if !hasDeadline || remaining <= 0 || remaining > maxHerdrRecoveryClassificationTimeout {
		t.Fatalf(
			"%s deadline = %v, %t (remaining %v)",
			name,
			deadline,
			hasDeadline,
			remaining,
		)
	}
}

func finalizeHerdrTestIntent(t *testing.T, repo string, intent state.HerdrIntent) {
	t.Helper()
	locked, err := state.LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Errorf("unlock Herdr test control: %v", unlockErr)
		}
	}()
	if !locked.RemoveIntent(intent.ID) {
		t.Fatalf("intent %s was not present for finalization", intent.ID)
	}
	row := state.HerdrRow{
		ID: intent.ID, Kind: intent.Kind, Parent: intent.Parent,
		RuntimeParent:    intent.RuntimeParent,
		OwnerProjectRoot: intent.OwnerProjectRoot,
		IssueNum:         intent.IssueNum, TaskID: intent.TaskID, Backend: intent.Backend,
		Slug: intent.Slug, BranchName: intent.BranchName,
		FullBranchRef: intent.FullBranchRef, BaseBranch: intent.BaseBranch,
		BaseSHA: intent.BaseSHA, ExpectedHead: intent.ExpectedHead,
		WorktreePath:  intent.WorktreePath,
		BranchExisted: intent.BranchExisted, BranchCreated: intent.BranchCreated,
		Resource: intent.Resource,
		Session:  intent.Session, SocketPath: intent.SocketPath,
	}
	locked.UpsertRow(row)
	if err := locked.Save(); err != nil {
		t.Fatal(err)
	}
}

func newHerdrRealizeRepo(t *testing.T) string {
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
