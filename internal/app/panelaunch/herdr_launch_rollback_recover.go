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
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	ownerProjectRoot, runtimeParent, intentID string,
) (state.HerdrIntent, bool, error) {
	original, found := locked.FindIntent(intentID)
	rollbackID, err := state.HerdrRollbackIntentID(intentID)
	if err != nil {
		return state.HerdrIntent{}, false, err
	}
	if rollback, rollbackFound := locked.FindIntent(rollbackID); rollbackFound {
		if err := recoverInterruptedHerdrLaunchRollback(
			ctx, runtime, locked, req, source, ownerProjectRoot,
			runtimeParent, original, found, rollback,
		); err != nil {
			return state.HerdrIntent{}, false, err
		}
		original, found = locked.FindIntent(intentID)
	}
	if found && original.Status == state.HerdrIntentManualCleanupRequired {
		return state.HerdrIntent{}, false, herdrManualCleanupError(original)
	}
	return original, found, nil
}

func recoverInterruptedHerdrLaunchRollback(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	ownerProjectRoot, runtimeParent string,
	original state.HerdrIntent,
	originalFound bool,
	rollback state.HerdrIntent,
) error {
	if err := validateInterruptedHerdrRollback(
		req, ownerProjectRoot, runtimeParent, original, originalFound, rollback,
	); err != nil {
		return failInterruptedHerdrRollback(locked, original, originalFound, rollback, err)
	}
	switch rollback.Status {
	case state.HerdrIntentPlanned:
		return restoreUnissuedHerdrLaunchRollback(locked, original, originalFound, rollback)
	case state.HerdrIntentIssued:
		return classifyIssuedHerdrLaunchRollback(ctx, runtime, locked, req, source, original, originalFound, rollback)
	case state.HerdrIntentManualCleanupRequired:
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
	original state.HerdrIntent,
	originalFound bool,
	rollback state.HerdrIntent,
) error {
	requirements := []bool{
		rollback.Kind == state.HerdrIntentRollback,
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

func herdrRollbackMatchesOriginal(original, rollback state.HerdrIntent) bool {
	return original.Kind == state.HerdrIntentWorktree &&
		original.Status == state.HerdrIntentManualCleanupRequired &&
		herdrLaunchRollbackIdentity(original) == herdrLaunchRollbackIdentity(rollback)
}

type herdrRollbackIdentity struct {
	parent, runtimeParent, ownerProjectRoot, taskID        string
	slug, branchName, fullBranchRef, baseSHA, expectedHead string
	worktreePath, workspaceLabel, session, socketPath      string
	issueNum                                               int
	branchExisted, branchCreated                           bool
	resource, coordinator                                  state.HerdrResource
}

func herdrLaunchRollbackIdentity(intent state.HerdrIntent) herdrRollbackIdentity {
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
	locked *state.LockedHerdrIntents,
	original state.HerdrIntent,
	originalFound bool,
	rollback state.HerdrIntent,
) error {
	if !originalFound {
		return markHerdrIntentManual(
			locked, rollback,
			fmt.Errorf("planned Herdr launch rollback lost its worktree intent"),
		)
	}
	original.Status = state.HerdrIntentRealized
	original.Failure = ""
	locked.UpsertIntent(original)
	locked.RemoveIntent(rollback.ID)
	return locked.Save()
}

func classifyIssuedHerdrLaunchRollback(
	ctx context.Context,
	runtime HerdrWorktreeRuntime,
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	source worktree.RepoIdentity,
	original state.HerdrIntent,
	originalFound bool,
	rollback state.HerdrIntent,
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
		recoveryCtx, locked, req, original, originalFound, rollback,
	)
}

func finishAbsentHerdrLaunchRollback(
	ctx context.Context,
	locked *state.LockedHerdrIntents,
	req HerdrWorktreeRequest,
	original state.HerdrIntent,
	originalFound bool,
	rollback state.HerdrIntent,
) error {
	if originalFound && original.Launch != nil {
		if err := removeUnpublishedHerdrEnvironment(original.Launch.EnvFilePath); err != nil {
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
	rollback state.HerdrIntent,
) error {
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == rollback.Resource.WorkspaceID || workspace.Label == rollback.WorkspaceLabel {
			return fmt.Errorf("Herdr launch rollback target remains live")
		}
	}
	checkout, err := worktree.ObserveCheckout(ctx, req.SourceRoot, rollback.WorktreePath)
	if err != nil {
		return err
	}
	if !checkout.PathAbsent || checkout.Registered {
		return fmt.Errorf("Herdr launch rollback checkout remains registered")
	}
	return nil
}

func failInterruptedHerdrRollback(
	locked *state.LockedHerdrIntents,
	original state.HerdrIntent,
	originalFound bool,
	rollback state.HerdrIntent,
	cause error,
) error {
	if !originalFound {
		return markHerdrIntentManual(locked, rollback, cause)
	}
	return failHerdrRollback(locked, original, rollback, cause)
}
