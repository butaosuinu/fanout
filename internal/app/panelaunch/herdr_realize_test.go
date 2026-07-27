package panelaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	policyErr  error
	observeErr error
	mutate     func(herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error)
}

func (f *fakeHerdrRealizeRuntime) WorktreeRoute(
	context.Context,
) (herdrrun.OwnedWorktreeRoute, error) {
	return f.route, nil
}

func (f *fakeHerdrRealizeRuntime) VerifyWorktreeSetupPolicy(context.Context) error {
	return f.policyErr
}

func (f *fakeHerdrRealizeRuntime) ObserveWorkspaces(ctx context.Context) ([]herdrrun.WorkspaceObservation, error) {
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

func TestRealizeHerdrUsesFinalRowsAsIdempotentBindings(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	runtime := &fakeHerdrRealizeRuntime{}
	installSuccessfulHerdrMutations(t, repo, runtime)
	hooks := deterministicHerdrRealizeHooks()
	coordinator := realizeTestHerdrCoordinator(t, repo, runtime, hooks)
	finalizeHerdrTestIntent(t, repo, coordinator)

	coordinatorResult, err := realizeHerdrCoordinator(
		context.Background(),
		testHerdrCoordinatorRequest(repo),
		runtime,
		hooks,
	)
	if err != nil || !coordinatorResult.AlreadyFinalized ||
		coordinatorResult.Row.ID != coordinator.ID || len(runtime.mutations) != 1 {
		t.Fatalf(
			"final coordinator replay = %+v err=%v mutations=%d",
			coordinatorResult,
			err,
			len(runtime.mutations),
		)
	}

	req := testHerdrWorktreeRequest(repo, "final-row", 432)
	child, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("child with finalized coordinator error = %v", err)
	}
	if child.Intent.Coordinator != coordinator.Resource {
		t.Fatalf("child coordinator = %+v, want finalized %+v", child.Intent.Coordinator, coordinator.Resource)
	}
	finalizeHerdrTestIntent(t, repo, child.Intent)

	finalChild, err := realizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if err != nil || !finalChild.AlreadyFinalized ||
		finalChild.Row.ID != child.Intent.ID || finalChild.Pane != child.Pane ||
		len(runtime.mutations) != 2 {
		t.Fatalf(
			"final child replay = %+v err=%v mutations=%d",
			finalChild,
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
}

func TestRealizeHerdrWorktreePreservesIssuedIntentWhenRecoveryContextIsCanceled(t *testing.T) {
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
		if req.Kind != herdrrun.WorktreeCreate {
			return herdrrun.WorktreeMutationResult{}, errors.New("unsupported fake mutation")
		}
		workspaceID := "w" + strconv.Itoa(nextWorkspace)
		nextWorkspace++
		gitCmdTest(t, repo, "worktree", "add", req.Path, req.Branch)
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
	now := time.Now().UTC().Add(time.Hour)
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
		OwnerProjectRoot: intent.OwnerProjectRoot,
		IssueNum:         intent.IssueNum, TaskID: intent.TaskID, Backend: intent.Backend,
		Slug: intent.Slug, BranchName: intent.BranchName,
		FullBranchRef: intent.FullBranchRef, BaseBranch: intent.BaseBranch,
		BaseSHA: intent.BaseSHA, ExpectedHead: intent.ExpectedHead,
		WorktreePath: intent.WorktreePath, Resource: intent.Resource,
		Session: intent.Session, SocketPath: intent.SocketPath,
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
