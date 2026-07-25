package panelaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type fakeHerdrWorktreeRuntime struct {
	t            *testing.T
	repo         string
	observations []herdrrun.WorkspaceObservation
	mutations    []herdrrun.WorktreeMutationRequest
	responseLoss bool
	policyErr    error
}

func (f *fakeHerdrWorktreeRuntime) VerifyWorktreeSetupPolicy(context.Context) error {
	return f.policyErr
}

func (f *fakeHerdrWorktreeRuntime) ObserveWorkspaces(context.Context) ([]herdrrun.WorkspaceObservation, error) {
	return slices.Clone(f.observations), nil
}

func (f *fakeHerdrWorktreeRuntime) MutateWorktree(_ context.Context, req herdrrun.WorktreeMutationRequest) (herdrrun.WorktreeMutationResult, error) {
	f.mutations = append(f.mutations, req)
	switch req.Kind {
	case herdrrun.WorktreeCreate:
		repoIdentity, err := worktree.ResolveHerdrRepoIdentity(f.repo)
		if err != nil {
			f.t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(req.Path), 0o755); err != nil {
			f.t.Fatal(err)
		}
		runHerdrLaunchGit(f.t, f.repo, "worktree", "add", req.Path, req.Branch)
		observation := herdrrun.WorkspaceObservation{
			WorkspaceID: "w-child",
			Label:       req.Label,
			Path:        req.Path,
			RepoKey:     repoIdentity.RepoKey,
			RepoRoot:    repoIdentity.RepoRoot,
			Pane: backend.PaneRef{
				Backend:   backend.Herdr,
				Workspace: "w-child",
				Pane:      "w-child:p1",
			},
			TerminalID: "terminal-child",
			CWD:        req.Path,
		}
		f.observations = append(f.observations, observation)
		if f.responseLoss {
			return herdrrun.WorktreeMutationResult{}, errors.New("injected response loss")
		}
		return herdrrun.WorktreeMutationResult{WorkspaceObservation: observation}, nil
	case herdrrun.WorkspaceCreate:
		observation := herdrrun.WorkspaceObservation{
			WorkspaceID: "w-coordinator",
			Label:       req.Label,
			Pane: backend.PaneRef{
				Backend:   backend.Herdr,
				Workspace: "w-coordinator",
				Pane:      "w-coordinator:p1",
			},
			TerminalID: "terminal-coordinator",
			CWD:        req.CWD,
		}
		f.observations = append(f.observations, observation)
		if f.responseLoss {
			return herdrrun.WorktreeMutationResult{}, errors.New("injected response loss")
		}
		return herdrrun.WorktreeMutationResult{WorkspaceObservation: observation}, nil
	default:
		f.t.Fatalf("unexpected mutation kind %s", req.Kind)
		return herdrrun.WorktreeMutationResult{}, nil
	}
}

func TestRealizeHerdrWorktreePersistsPhasesAndStopsAtLauncherBoundary(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	var phases []state.HerdrLaunchPhase
	result, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(&phases, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("error = %v, want deferred launcher readiness", err)
	}
	wantPhases := []state.HerdrLaunchPhase{
		state.HerdrPhaseBranchPlanned,
		state.HerdrPhaseBranchStarting,
		state.HerdrPhaseWorktreePlanned,
		state.HerdrPhaseWorktreeStarting,
		state.HerdrPhaseWorktreeRealized,
	}
	if !slices.Equal(phases, wantPhases) {
		t.Fatalf("phases = %v, want %v", phases, wantPhases)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("worktree mutations = %d, want 1", len(runtime.mutations))
	}
	mutation := runtime.mutations[0]
	if mutation.Kind != herdrrun.WorktreeCreate || mutation.WorkspaceID != "w-coordinator" ||
		mutation.CoordinatorWorkspaceLabel != "coordinator-nonce" ||
		mutation.CoordinatorPaneID != "w-coordinator:p1" ||
		mutation.CoordinatorTerminalID != "terminal-coordinator" ||
		mutation.CoordinatorWorkspaceCWD != result.Intent.Coordinator.CWD ||
		mutation.ExpectedRepoKey != result.Intent.HerdrRepoKey ||
		mutation.ExpectedRepoRoot != result.Intent.HerdrRepoRoot ||
		mutation.Branch != req.BranchName || mutation.Path != req.WorktreePath ||
		mutation.Label == "" || !mutation.NoFocus {
		t.Fatalf("mutation = %+v", mutation)
	}
	if result.Intent.Phase != state.HerdrPhaseWorktreeRealized ||
		result.Intent.OperationState != state.HerdrOperationActive ||
		result.Pane != (backend.PaneRef{Backend: backend.Herdr, Workspace: "w-child", Pane: "w-child:p1"}) {
		t.Fatalf("result = %+v", result)
	}
	if result.Intent.MutationReceipt == nil ||
		worktree.VerifyHerdrOwnershipMarker(
			repo,
			req.WorktreePath,
			result.Intent.FullBranchRef,
			result.Intent.LaunchHeadSHA,
			result.Intent.MutationReceipt.GitDirMarkerPath,
			result.Intent.WorktreeOwnershipNonce,
		) != nil {
		t.Fatalf("missing or invalid ownership marker: %+v", result.Intent.MutationReceipt)
	}

	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := control.FindIntent(result.Intent.IntentID)
	if !ok || saved.Phase != state.HerdrPhaseWorktreeRealized || len(control.Lineages) != 1 {
		t.Fatalf("saved control = %+v", control)
	}
	if _, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, "")); !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("realized replay error = %v, want deferred launcher readiness", err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("realized replay reissued mutation: %d", len(runtime.mutations))
	}
}

func TestHerdrStartingPhasesNeverBlindRetry(t *testing.T) {
	tests := []struct {
		name       string
		crashPhase state.HerdrLaunchPhase
		wantBranch bool
	}{
		{name: "branch starting", crashPhase: state.HerdrPhaseBranchStarting},
		{name: "worktree starting", crashPhase: state.HerdrPhaseWorktreeStarting, wantBranch: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newHerdrLaunchRepo(t)
			runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
			req := newHerdrWorktreeRequest(t, repo, runtime)
			_, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, tt.crashPhase))
			if err == nil || !strings.Contains(err.Error(), "injected crash") {
				t.Fatalf("first error = %v", err)
			}
			if len(runtime.mutations) != 0 {
				t.Fatalf("mutation issued before injected crash: %d", len(runtime.mutations))
			}
			_, err = RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
			if !errors.Is(err, ErrHerdrManualCleanupRequired) {
				t.Fatalf("replay error = %v, want manual cleanup", err)
			}
			if len(runtime.mutations) != 0 {
				t.Fatalf("replay issued mutation: %d", len(runtime.mutations))
			}
			fullRef := "refs/heads/" + req.BranchName
			_, found, observeErr := worktree.ObserveBranch(repo, fullRef)
			if observeErr != nil || found != tt.wantBranch {
				t.Fatalf("branch after crash = found:%t err:%v, want found:%t", found, observeErr, tt.wantBranch)
			}
		})
	}
}

func TestHerdrWorktreeStartingReconcilesCrashAfterExactMutation(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	_, err := RealizeHerdrWorktree(
		context.Background(),
		req,
		runtime,
		deterministicHerdrHooks(nil, state.HerdrPhaseWorktreeStarting),
	)
	if err == nil || !strings.Contains(err.Error(), "injected crash") {
		t.Fatalf("starting crash error = %v", err)
	}
	sourceRoot, err := physicalPath(req.SourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.HerdrIntentID(req.Parent, req.IssueNum, req.TaskID, sourceRoot, req.PlanSpecIdentity)
	if err != nil {
		t.Fatal(err)
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	starting, found := control.FindIntent(intentID)
	if !found || starting.Phase != state.HerdrPhaseWorktreeStarting || starting.MutationRequest == nil {
		t.Fatalf("saved starting intent = %+v, found=%t", starting, found)
	}
	_, err = runtime.MutateWorktree(context.Background(), toHerdrMutationRequest(*starting.MutationRequest))
	if err != nil {
		t.Fatal(err)
	}

	result, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		result.Intent.Phase != state.HerdrPhaseWorktreeRealized ||
		len(runtime.mutations) != 1 {
		t.Fatalf("starting recovery = result:%+v err:%v mutations:%d", result, err, len(runtime.mutations))
	}
}

func TestHerdrChildRejectsCoordinatorRequestDriftOnReplay(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	_, err := RealizeHerdrWorktree(
		context.Background(),
		req,
		runtime,
		deterministicHerdrHooks(nil, state.HerdrPhaseBranchPlanned),
	)
	if err == nil || !strings.Contains(err.Error(), "injected crash") {
		t.Fatalf("first error = %v", err)
	}
	req.CoordinatorWorkspaceID = "w-foreign"
	_, err = RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if err == nil || !strings.Contains(err.Error(), "contradicts the exact launch request") {
		t.Fatalf("coordinator drift error = %v", err)
	}
	if len(runtime.mutations) != 0 {
		t.Fatalf("coordinator drift issued %d mutations", len(runtime.mutations))
	}
}

func TestHerdrChildRejectsLiveCoordinatorDriftAndRetainsReservation(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	runtime.observations[0].Label = "foreign"
	_, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrManualCleanupRequired) ||
		!strings.Contains(err.Error(), "live herdr coordinator identity changed") {
		t.Fatalf("coordinator drift error = %v", err)
	}
	if len(runtime.mutations) != 0 {
		t.Fatalf("coordinator drift issued %d mutations", len(runtime.mutations))
	}
	if _, found, observeErr := worktree.ObserveBranch(repo, "refs/heads/"+req.BranchName); observeErr != nil || !found {
		t.Fatalf("reservation was not retained: found=%t err=%v", found, observeErr)
	}
}

func TestHerdrRealizationRejectsCrossRepositoryRootsBeforeMutation(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	foreign := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	req.SourceRoot = foreign
	_, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if err == nil || !strings.Contains(err.Error(), "belong to different repositories") {
		t.Fatalf("cross-repository child error = %v", err)
	}
	if len(runtime.mutations) != 0 {
		t.Fatalf("cross-repository child issued %d mutations", len(runtime.mutations))
	}
	if _, found, observeErr := worktree.ObserveBranch(repo, "refs/heads/"+req.BranchName); observeErr != nil || found {
		t.Fatalf("cross-repository child reserved branch: found=%t err=%v", found, observeErr)
	}

	coordinatorReq := HerdrCoordinatorRequest{
		Parent:          "524",
		ProjectRoot:     repo,
		SourceRoot:      repo,
		RootCWD:         foreign,
		HerdrSession:    "fanout-owned",
		HerdrSocketPath: "/tmp/fanout-owned.sock",
		TotalTimeout:    30 * time.Second,
	}
	_, err = RealizeHerdrCoordinator(
		context.Background(),
		coordinatorReq,
		runtime,
		deterministicHerdrHooks(nil, ""),
	)
	if err == nil || !strings.Contains(err.Error(), "do not identify one source repository root") {
		t.Fatalf("cross-repository coordinator error = %v", err)
	}
	if len(runtime.mutations) != 0 {
		t.Fatalf("cross-repository coordinator issued %d mutations", len(runtime.mutations))
	}
}

func TestHerdrWorktreeResponseLossReconcilesExactPostStateWithoutRetry(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo, responseLoss: true}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	result, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("response-loss error = %v", err)
	}
	if len(runtime.mutations) != 1 {
		t.Fatalf("mutations = %d, want 1", len(runtime.mutations))
	}
	if result.Intent.Phase != state.HerdrPhaseWorktreeRealized || result.Intent.MutationReceipt == nil {
		t.Fatalf("response-loss result = %+v, want realized receipt", result)
	}
	if _, statErr := os.Stat(req.WorktreePath); statErr != nil {
		t.Fatalf("response-loss checkout was not preserved: %v", statErr)
	}
	result, err = RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		result.Intent.Phase != state.HerdrPhaseWorktreeRealized ||
		len(runtime.mutations) != 1 {
		t.Fatalf("replay = err:%v mutations:%d", err, len(runtime.mutations))
	}
}

func TestHerdrWorktreeSetupPolicyFailsBeforeMutation(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{
		t:         t,
		repo:      repo,
		policyErr: errors.New("plugin registry is not empty"),
	}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	_, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrManualCleanupRequired) || !strings.Contains(err.Error(), "setup-hook policy") {
		t.Fatalf("policy error = %v", err)
	}
	if len(runtime.mutations) != 0 {
		t.Fatalf("policy failure issued %d mutations", len(runtime.mutations))
	}
}

func TestHerdrWorktreeDeadlineDoesNotExtendOnRecovery(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	start := time.Unix(1_700_000_000, 0)
	now := start
	counter := 0
	hooks := HerdrWorktreeHooks{
		Now: func() time.Time { return now },
		Random: func() (string, error) {
			counter++
			return "deadline-nonce-" + string(rune('a'+counter)), nil
		},
		PhaseSaved: func(phase state.HerdrLaunchPhase) error {
			if phase == state.HerdrPhaseWorktreePlanned {
				now = start.Add(req.TotalTimeout)
			}
			return nil
		},
	}
	_, err := RealizeHerdrWorktree(context.Background(), req, runtime, hooks)
	if !errors.Is(err, ErrHerdrManualCleanupRequired) || !strings.Contains(err.Error(), "deadline expired") {
		t.Fatalf("deadline error = %v", err)
	}
	if len(runtime.mutations) != 0 {
		t.Fatalf("expired launch issued %d mutations", len(runtime.mutations))
	}
	control, loadErr := state.LoadHerdrControl(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	sourceRoot, _ := physicalPath(req.SourceRoot)
	intentID, _ := state.HerdrIntentID(
		req.Parent,
		req.IssueNum,
		req.TaskID,
		sourceRoot,
		req.PlanSpecIdentity,
	)
	intent, _ := control.FindIntent(intentID)
	if intent.LaunchExpiresUnixMS != start.Add(req.TotalTimeout).UnixMilli() {
		t.Fatalf("saved expiry = %d, want fixed %d", intent.LaunchExpiresUnixMS, start.Add(req.TotalTimeout).UnixMilli())
	}
}

func TestHerdrWorktreeRealizedRecoveryDoesNotReissueMutation(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	req := newHerdrWorktreeRequest(t, repo, runtime)
	_, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, state.HerdrPhaseWorktreeRealized))
	if err == nil || !strings.Contains(err.Error(), "injected crash") {
		t.Fatalf("first error = %v", err)
	}
	result, err := RealizeHerdrWorktree(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("error = %v, want deferred launcher readiness", err)
	}
	if result.Intent.Phase != state.HerdrPhaseWorktreeRealized || len(runtime.mutations) != 1 {
		t.Fatalf("recovery result = %+v mutations=%d", result, len(runtime.mutations))
	}
}

func TestValidateAlreadyOpenPinsPreStateBinding(t *testing.T) {
	intent := state.HerdrLaunchIntent{
		WorktreeOwnershipNonce: "nonce",
		HerdrRepoKey:           "repo",
		HerdrRepoRoot:          "/repo",
		MutationRequest:        &state.HerdrMutationRequest{Kind: state.HerdrMutationWorktreeOpen},
		MutationPreState: &state.HerdrMutationPreState{
			ExpectedAlreadyOpenID:    "w1",
			ExpectedAlreadyOpenLabel: "nonce",
			Workspaces: []state.HerdrWorkspaceBinding{{
				WorkspaceID: "w1",
				Label:       "nonce",
				Path:        "/repo/child",
				RepoKey:     "repo",
				RepoRoot:    "/repo",
			}},
		},
		WorktreePath: "/repo/child",
	}
	good := herdrrun.WorktreeMutationResult{
		WorkspaceObservation: herdrrun.WorkspaceObservation{WorkspaceID: "w1", Label: "nonce"},
		AlreadyOpen:          true,
	}
	if err := validateAlreadyOpen(intent, good); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.WorkspaceID = "w2"
	if err := validateAlreadyOpen(intent, bad); err == nil {
		t.Fatal("foreign already_open workspace unexpectedly accepted")
	}
	intent.MutationPreState.ExpectedAlreadyOpenID = ""
	if err := validateAlreadyOpen(intent, good); err == nil {
		t.Fatal("already_open without pre-state binding unexpectedly accepted")
	}
	intent.MutationPreState.ExpectedAlreadyOpenID = "w1"
	intent.MutationPreState.Workspaces = nil
	if err := validateAlreadyOpen(intent, good); err == nil {
		t.Fatal("already_open without an observed pre-state workspace unexpectedly accepted")
	}
}

func TestRealizeHerdrCoordinatorPersistsWorkspacePhases(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	var phases []state.HerdrLaunchPhase
	result, err := RealizeHerdrCoordinator(context.Background(), HerdrCoordinatorRequest{
		Parent:          "524",
		ProjectRoot:     repo,
		SourceRoot:      repo,
		RootCWD:         repo,
		HerdrSession:    "fanout-owned",
		HerdrSocketPath: "/tmp/fanout-owned.sock",
		TotalTimeout:    30 * time.Second,
	}, runtime, deterministicHerdrHooks(&phases, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) {
		t.Fatalf("error = %v, want deferred launcher readiness", err)
	}
	want := []state.HerdrLaunchPhase{
		state.HerdrPhaseWorkspacePlanned,
		state.HerdrPhaseWorkspaceStarting,
		state.HerdrPhaseWorkspaceRealized,
	}
	if !slices.Equal(phases, want) || result.Intent.Phase != state.HerdrPhaseWorkspaceRealized || len(runtime.mutations) != 1 {
		t.Fatalf("phases=%v result=%+v mutations=%d", phases, result, len(runtime.mutations))
	}
	sourceIdentity, identityErr := worktree.ResolveHerdrRepoIdentity(repo)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	wantID, identityErr := state.HerdrCoordinatorIntentID("524", sourceIdentity.RepoRoot, "")
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	if result.Intent.IntentID != wantID || result.Intent.IssueNum != 0 || result.Intent.TaskID != "" {
		t.Fatalf("coordinator owner = id:%q issue:%d task:%q", result.Intent.IntentID, result.Intent.IssueNum, result.Intent.TaskID)
	}
}

func TestHerdrCoordinatorResponseLossReconcilesExactPostStateWithoutRetry(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo, responseLoss: true}
	req := HerdrCoordinatorRequest{
		Parent:          "524",
		ProjectRoot:     repo,
		SourceRoot:      repo,
		RootCWD:         repo,
		HerdrSession:    "fanout-owned",
		HerdrSocketPath: "/tmp/fanout-owned.sock",
		TotalTimeout:    30 * time.Second,
	}
	result, err := RealizeHerdrCoordinator(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		result.Intent.Phase != state.HerdrPhaseWorkspaceRealized ||
		result.Intent.MutationReceipt == nil ||
		len(runtime.mutations) != 1 {
		t.Fatalf("first coordinator response loss = err:%v mutations:%d", err, len(runtime.mutations))
	}
	result, err = RealizeHerdrCoordinator(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		result.Intent.Phase != state.HerdrPhaseWorkspaceRealized ||
		len(runtime.mutations) != 1 {
		t.Fatalf("coordinator replay = err:%v mutations:%d", err, len(runtime.mutations))
	}
}

func TestHerdrCoordinatorStartingReconcilesCrashAfterExactMutation(t *testing.T) {
	repo := newHerdrLaunchRepo(t)
	runtime := &fakeHerdrWorktreeRuntime{t: t, repo: repo}
	req := HerdrCoordinatorRequest{
		Parent:          "524",
		ProjectRoot:     repo,
		SourceRoot:      repo,
		RootCWD:         repo,
		HerdrSession:    "fanout-owned",
		HerdrSocketPath: "/tmp/fanout-owned.sock",
		TotalTimeout:    30 * time.Second,
	}
	_, err := RealizeHerdrCoordinator(
		context.Background(),
		req,
		runtime,
		deterministicHerdrHooks(nil, state.HerdrPhaseWorkspaceStarting),
	)
	if err == nil || !strings.Contains(err.Error(), "injected crash") {
		t.Fatalf("coordinator starting crash error = %v", err)
	}
	sourceIdentity, err := worktree.ResolveHerdrRepoIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := state.HerdrCoordinatorIntentID(req.Parent, sourceIdentity.RepoRoot, req.PlanSpecIdentity)
	if err != nil {
		t.Fatal(err)
	}
	control, err := state.LoadHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	starting, found := control.FindIntent(intentID)
	if !found || starting.Phase != state.HerdrPhaseWorkspaceStarting || starting.MutationRequest == nil {
		t.Fatalf("saved coordinator starting intent = %+v, found=%t", starting, found)
	}
	_, err = runtime.MutateWorktree(context.Background(), toHerdrMutationRequest(*starting.MutationRequest))
	if err != nil {
		t.Fatal(err)
	}

	result, err := RealizeHerdrCoordinator(context.Background(), req, runtime, deterministicHerdrHooks(nil, ""))
	if !errors.Is(err, ErrHerdrLauncherReadinessDeferred) ||
		result.Intent.Phase != state.HerdrPhaseWorkspaceRealized ||
		len(runtime.mutations) != 1 {
		t.Fatalf("coordinator starting recovery = result:%+v err:%v mutations:%d", result, err, len(runtime.mutations))
	}
}

func newHerdrWorktreeRequest(t *testing.T, repo string, runtime *fakeHerdrWorktreeRuntime) HerdrWorktreeRequest {
	t.Helper()
	req := HerdrWorktreeRequest{
		Parent:                 "524",
		IssueNum:               527,
		ProjectRoot:            repo,
		SourceRoot:             repo,
		Slug:                   "herdr-child",
		BranchName:             "fanout/herdr-child",
		BaseBranch:             "main",
		NoRefresh:              true,
		WorktreePath:           filepath.Join(repo, ".fanout", "worktrees", "herdr-child"),
		CoordinatorWorkspaceID: "w-coordinator",
		HerdrSession:           "fanout-owned",
		HerdrSocketPath:        "/tmp/fanout-owned.sock",
		TotalTimeout:           30 * time.Second,
	}
	seedHerdrCoordinator(t, repo, runtime, req)
	return req
}

func seedHerdrCoordinator(
	t *testing.T,
	repo string,
	runtime *fakeHerdrWorktreeRuntime,
	req HerdrWorktreeRequest,
) {
	t.Helper()
	sourceIdentity, err := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorID, err := state.HerdrCoordinatorIntentID(
		req.Parent,
		sourceIdentity.RepoRoot,
		req.PlanSpecIdentity,
	)
	if err != nil {
		t.Fatal(err)
	}
	const (
		workspaceID = "w-coordinator"
		label       = "coordinator-nonce"
		paneID      = "w-coordinator:p1"
		terminalID  = "terminal-coordinator"
	)
	locked, err := state.LockHerdrControl(repo)
	if err != nil {
		t.Fatal(err)
	}
	locked.UpsertIntent(state.HerdrLaunchIntent{
		IntentID:               coordinatorID,
		Parent:                 req.Parent,
		Backend:                backend.Herdr,
		Operation:              "coordinator-workspace",
		OperationState:         state.HerdrOperationActive,
		Phase:                  state.HerdrPhaseWorkspaceRealized,
		SourceRootPhysical:     sourceIdentity.RepoRoot,
		PlanSpecIdentity:       req.PlanSpecIdentity,
		WorktreePath:           sourceIdentity.RepoRoot,
		HerdrRepoKey:           sourceIdentity.RepoKey,
		HerdrRepoRoot:          sourceIdentity.RepoRoot,
		HerdrSession:           req.HerdrSession,
		HerdrSocketPath:        req.HerdrSocketPath,
		WorktreeOwnershipNonce: label,
		MutationRequest: &state.HerdrMutationRequest{
			Kind:    state.HerdrMutationWorkspaceCreate,
			CWD:     sourceIdentity.RepoRoot,
			Label:   label,
			NoFocus: true,
		},
		MutationPreState: &state.HerdrMutationPreState{
			Workspaces: []state.HerdrWorkspaceBinding{},
		},
		MutationReceipt: &state.HerdrMutationReceipt{
			WorkspaceID:    workspaceID,
			WorkspaceLabel: label,
			PaneID:         paneID,
			TerminalID:     terminalID,
			CWD:            sourceIdentity.RepoRoot,
		},
	})
	if err := locked.Save(); err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	runtime.observations = append(runtime.observations, herdrrun.WorkspaceObservation{
		WorkspaceID: workspaceID,
		Label:       label,
		Pane: backend.PaneRef{
			Backend:   backend.Herdr,
			Workspace: workspaceID,
			Pane:      paneID,
		},
		TerminalID: terminalID,
		CWD:        sourceIdentity.RepoRoot,
	})
}

func deterministicHerdrHooks(phases *[]state.HerdrLaunchPhase, crash state.HerdrLaunchPhase) HerdrWorktreeHooks {
	counter := 0
	return HerdrWorktreeHooks{
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
		Random: func() (string, error) {
			counter++
			return "nonce-" + string(rune('a'+counter)), nil
		},
		PhaseSaved: func(phase state.HerdrLaunchPhase) error {
			if phases != nil {
				*phases = append(*phases, phase)
			}
			if phase == crash {
				return errors.New("injected crash after " + string(phase))
			}
			return nil
		},
	}
}

func newHerdrLaunchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runHerdrLaunchGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHerdrLaunchGit(t, repo, "add", "tracked")
	runHerdrLaunchGit(t, repo, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "base")
	return repo
}

func runHerdrLaunchGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
