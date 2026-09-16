package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func cleanupPlanCoordinator(opts Options, locked *state.LockedStore, parent string, lg Logger) exitcode.Code {
	if !strings.HasPrefix(parent, "plan:") || len(taskPanesForParent(locked.PanesForParent(parent))) != 0 {
		return exitcode.OK
	}
	for _, pane := range append([]state.Pane(nil), locked.Panes...) {
		if !workspaceRuntimeRow(pane) || pane.RuntimeParent != parent || !pane.IsShell() {
			continue
		}
		if err := retirePlanCoordinator(opts, locked, pane); err != nil {
			lg.Err("--cleanup: retire plan coordinator: %v", err)
			return exitcode.Env
		}
		lg.Ok("--cleanup: retired coordinator for %s", parent)
	}
	return exitcode.OK
}

func retirePlanCoordinator(opts Options, locked *state.LockedStore, pane state.Pane) error {
	journal, err := locked.LaunchJournal(opts.ProjectRoot)
	if err != nil {
		return err
	}
	intent, err := planCoordinatorRetirementIntent(journal, opts.ProjectRoot, pane)
	if err != nil {
		return err
	}
	if opts.WorkspaceRuntime == nil {
		return fmt.Errorf("herdr lifecycle runtime is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCleanupTimeout)
	defer cancel()
	runtime, err := opts.WorkspaceRuntime(ctx, pane)
	if err != nil {
		return err
	}
	if err := closePlanCoordinator(ctx, runtime, journal, intent, pane); err != nil {
		return err
	}
	return retirePlanCoordinatorState(locked, journal, pane, intent.ID)
}

func planCoordinatorRetirementIntent(journal *state.LockedLaunchJournal, root string, pane state.Pane) (state.LaunchIntent, error) {
	id, owner, err := coordinatorIntentIdentity(root, pane)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	intent, found := journal.FindIntent(id)
	checked := intent
	if intent.Status == state.IntentManualCleanupRequired && intent.Failure == panelaunch.ManagedCoordinatorClosePending {
		checked.Status = state.IntentRealized
	}
	if !found || !coordinatorIntentMatches(checked, pane, owner, root) {
		return state.LaunchIntent{}, fmt.Errorf("saved Herdr coordinator intent does not match retirement")
	}
	return intent, panelaunch.ValidateManagedCoordinatorRetirement(pane, intent)
}

func closePlanCoordinator(ctx context.Context, runtime WorkspaceRuntime, journal *state.LockedLaunchJournal, intent state.LaunchIntent, pane state.Pane) error {
	if err := runtime.VerifyOwned(ctx); err != nil {
		return err
	}
	workspaces, err := runtime.ObserveWorkspaces(ctx)
	if err != nil {
		return err
	}
	workspace, err := findUniqueWorkspace(workspaces, true, coordinatorWorkspacePredicate(intent.Resource))
	if err != nil || workspace == nil {
		return err
	}
	if intent.Status == state.IntentManualCleanupRequired {
		return fmt.Errorf("%w: %s; workspace is still present", ErrManualCleanupRequired, panelaunch.ManagedCoordinatorClosePending)
	}
	bound, err := runtime.BindOwnedWorkspaceClose(backend.OwnedPaneIdentity{
		Ref: paneRefFromState(pane), SessionID: pane.SessionID, SocketPath: pane.SocketPath,
		WorkspaceLabel: pane.WorkspaceLabel, TerminalID: pane.TerminalID, CurrentPath: pane.WorktreePath,
	})
	if err != nil {
		return err
	}
	return issuePlanCoordinatorClose(journal, intent, bound, paneRefFromState(pane))
}

func issuePlanCoordinatorClose(journal *state.LockedLaunchJournal, intent state.LaunchIntent, bound backend.OwnedClosingBackend, ref backend.PaneRef) error {
	pending := intent
	pending.Status, pending.Failure = state.IntentManualCleanupRequired, panelaunch.ManagedCoordinatorClosePending
	journal.UpsertIntent(pending)
	if err := journal.Save(); err != nil {
		return err
	}
	result, err := bound.CloseOwned(backend.CloseRequest{Ref: backend.PaneRef{Backend: ref.Backend, Pane: ref.Pane}})
	// Generic workspace close returns these sentinels only from fences before the mutation is sent.
	if errors.Is(err, backend.ErrOwnedIdentityMismatch) || errors.Is(err, backend.ErrOwnedWorkspaceHasUnadmittedPane) {
		journal.UpsertIntent(intent)
		return errors.Join(fmt.Errorf("%w: %w", ErrManualCleanupRequired, err), journal.Save())
	}
	if err != nil {
		return err
	}
	if result.Status != backend.CloseConfirmed {
		return fmt.Errorf("%w: coordinator workspace close was not confirmed", ErrManualCleanupRequired)
	}
	return nil
}

func retirePlanCoordinatorState(locked *state.LockedStore, journal *state.LockedLaunchJournal, pane state.Pane, intentID string) error {
	previous := locked.Store
	previous.Panes = append([]state.Pane(nil), locked.Panes...)
	if err := removePaneStateRows(locked, []state.Pane{pane}); err != nil {
		return restorePaneStateAfterRetirementFailure(locked, previous, err)
	}
	journal.RemoveIntent(intentID)
	if err := journal.Save(); err != nil {
		return restorePaneStateAfterRetirementFailure(locked, previous, err)
	}
	return nil
}
