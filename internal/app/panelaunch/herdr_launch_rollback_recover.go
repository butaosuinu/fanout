package panelaunch

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func loadHerdrWorktreeIntentForRealization(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	ownerProjectRoot, runtimeParent, intentID string,
) (state.LaunchIntent, bool, error) {
	original, found := locked.FindIntent(intentID)
	rollbackID, err := state.RollbackIntentID(intentID)
	if err != nil {
		return state.LaunchIntent{}, false, err
	}
	if rollback, rollbackFound := locked.FindIntent(rollbackID); rollbackFound {
		if err := recoverInterruptedHerdrLaunchRollback(
			ctx, runtime, locked, req, source, ownerProjectRoot,
			runtimeParent, original, found, rollback,
		); err != nil {
			return state.LaunchIntent{}, false, err
		}
		original, found = locked.FindIntent(intentID)
	}
	if found && original.Status == state.IntentManualCleanupRequired {
		return state.LaunchIntent{}, false, herdrManualCleanupError(original)
	}
	return original, found, nil
}

func recoverInterruptedHerdrLaunchRollback(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	ownerProjectRoot, runtimeParent string,
	original state.LaunchIntent,
	originalFound bool,
	rollback state.LaunchIntent,
) error {
	if err := validateInterruptedHerdrRollback(
		req, ownerProjectRoot, runtimeParent, original, originalFound, rollback,
	); err != nil {
		return failInterruptedHerdrRollback(locked, original, originalFound, rollback, err)
	}
	switch rollback.Status {
	case state.IntentPlanned:
		return restoreUnissuedHerdrLaunchRollback(locked, original, originalFound, rollback)
	case state.IntentIssued:
		return classifyIssuedHerdrLaunchRollback(ctx, runtime, locked, req, source, original, originalFound, rollback)
	case state.IntentManualCleanupRequired:
		return herdrManualCleanupError(rollback)
	default:
		return failInterruptedHerdrRollback(
			locked, original, originalFound, rollback,
			fmt.Errorf("unknown Herdr launch rollback status %q", rollback.Status),
		)
	}
}

func validateInterruptedHerdrRollback(
	req HerdrWorktreeRequest,
	ownerProjectRoot, runtimeParent string,
	original state.LaunchIntent,
	originalFound bool,
	rollback state.LaunchIntent,
) error {
	requirements := []bool{
		rollback.Kind == state.IntentRollback,
		rollback.Parent == canonicalHerdrParent(req.Parent),
		rollback.RuntimeParent == runtimeParent,
		rollback.OwnerProjectRoot == ownerProjectRoot,
		rollback.IssueNum == req.IssueNum, rollback.TaskID == req.TaskID,
		rollback.Slug == req.Slug, rollback.BranchName == req.BranchName,
		filepath.Clean(rollback.WorktreePath) == filepath.Clean(req.WorktreePath),
		rollback.Session == req.HerdrSession, rollback.SocketPath == req.SocketPath,
	}
	for _, requirement := range requirements {
		if !requirement {
			return fmt.Errorf("saved Herdr launch rollback contradicts request")
		}
	}
	if originalFound && !herdrRollbackMatchesOriginal(original, rollback) {
		return fmt.Errorf("saved Herdr launch rollback contradicts its worktree intent")
	}
	return nil
}

func herdrRollbackMatchesOriginal(original, rollback state.LaunchIntent) bool {
	return original.Kind == state.IntentWorktree &&
		original.Status == state.IntentManualCleanupRequired &&
		herdrLaunchRollbackIdentity(original) == herdrLaunchRollbackIdentity(rollback)
}

type herdrRollbackIdentity struct {
	parent, runtimeParent, ownerProjectRoot, taskID        string
	slug, branchName, fullBranchRef, baseSHA, expectedHead string
	worktreePath, workspaceLabel, session, socketPath      string
	issueNum                                               int
	branchExisted, branchCreated                           bool
	resource, coordinator                                  state.RuntimeResource
}

func herdrLaunchRollbackIdentity(intent state.LaunchIntent) herdrRollbackIdentity {
	return herdrRollbackIdentity{
		parent: intent.Parent, runtimeParent: intent.RuntimeParent,
		ownerProjectRoot: intent.OwnerProjectRoot, taskID: intent.TaskID,
		slug: intent.Slug, branchName: intent.BranchName, fullBranchRef: intent.FullBranchRef,
		baseSHA: intent.BaseSHA, expectedHead: intent.ExpectedHead,
		worktreePath: intent.WorktreePath, workspaceLabel: intent.WorkspaceLabel,
		session: intent.Session, socketPath: intent.SocketPath, issueNum: intent.IssueNum,
		branchExisted: intent.BranchExisted, branchCreated: intent.BranchCreated,
		resource: intent.Resource, coordinator: intent.Coordinator,
	}
}

func restoreUnissuedHerdrLaunchRollback(
	locked *state.LockedLaunchJournal,
	original state.LaunchIntent,
	originalFound bool,
	rollback state.LaunchIntent,
) error {
	if !originalFound {
		return markHerdrIntentManual(
			locked, rollback,
			fmt.Errorf("planned Herdr launch rollback lost its worktree intent"),
		)
	}
	original.Status = state.IntentRealized
	original.Failure = ""
	locked.UpsertIntent(original)
	locked.RemoveIntent(rollback.ID)
	return locked.Save()
}

func classifyIssuedHerdrLaunchRollback(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	original state.LaunchIntent,
	originalFound bool,
	rollback state.LaunchIntent,
) error {
	recoveryCtx, cancel := context.WithTimeout(ctx, maxHerdrRecoveryClassificationTimeout)
	defer cancel()
	if err := verifyHerdrRealizeRoute(
		recoveryCtx, runtime, source.RepoKey, req.HerdrSession, req.SocketPath,
	); err != nil {
		return failInterruptedHerdrRollback(locked, original, originalFound, rollback, err)
	}
	if err := verifyIssuedHerdrRollbackAbsent(recoveryCtx, runtime, req, rollback); err != nil {
		return failInterruptedHerdrRollback(locked, original, originalFound, rollback, err)
	}
	return finishAbsentHerdrLaunchRollback(
		recoveryCtx, runtime, locked, req, original, originalFound, rollback,
	)
}

func finishAbsentHerdrLaunchRollback(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedLaunchJournal,
	req HerdrWorktreeRequest,
	original state.LaunchIntent,
	originalFound bool,
	rollback state.LaunchIntent,
) error {
	if originalFound && original.Launch != nil {
		if err := removeUnpublishedHerdrEnvironment(runtime, filepath.Dir(original.SocketPath), original.Launch); err != nil {
			return failInterruptedHerdrRollback(locked, original, true, rollback, err)
		}
	}
	if rollback.BranchCreated {
		if err := worktree.DeleteReservedBranch(
			ctx, req.SourceRoot, rollback.FullBranchRef, rollback.BaseSHA,
		); err != nil {
			return failInterruptedHerdrRollback(locked, original, originalFound, rollback, err)
		}
	}
	if originalFound {
		locked.RemoveIntent(original.ID)
	}
	locked.RemoveIntent(rollback.ID)
	return locked.Save()
}

func verifyIssuedHerdrRollbackAbsent(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	req HerdrWorktreeRequest,
	rollback state.LaunchIntent,
) error {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == rollback.Resource.WorkspaceID || workspace.Label == rollback.WorkspaceLabel {
			return fmt.Errorf("herdr launch rollback target remains live")
		}
	}
	checkout, err := worktree.ObserveCheckout(ctx, req.SourceRoot, rollback.WorktreePath)
	if err != nil {
		return err
	}
	if !checkout.PathAbsent || checkout.Registered {
		return fmt.Errorf("herdr launch rollback checkout remains registered")
	}
	return nil
}

func failInterruptedHerdrRollback(
	locked *state.LockedLaunchJournal,
	original state.LaunchIntent,
	originalFound bool,
	rollback state.LaunchIntent,
	cause error,
) error {
	if !originalFound {
		return markHerdrIntentManual(locked, rollback, cause)
	}
	return failHerdrRollback(locked, original, rollback, cause)
}
