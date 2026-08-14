package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// RestartHerdrServer explicitly replaces a proven-dead owned server while the
// combined state/intent lock fences every other fanout mutation.
func RestartHerdrServer(
	ctx context.Context,
	projectRoot string,
	opts herdrrun.OwnedOptions,
) (_ *herdrrun.OwnedSession, err error) {
	defer errs.Wrap(&err, "restart Herdr owned server")
	locked, err := state.LockProjectForLaunchContext(ctx, projectRoot)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return nil, err
	}
	intent, created, err := ensureHerdrServerIntent(journal, state.HerdrIntentRestart, opts)
	if err != nil {
		return nil, err
	}
	restarted, err := herdrrun.RestartOwned(ctx, opts, *intent.Server)
	if err != nil {
		return nil, releaseRejectedHerdrRestart(journal, intent, created, err)
	}
	if err = verifyRestartedHerdrRows(ctx, projectRoot, locked, journal, restarted); err != nil {
		return nil, err
	}
	markPlannedHerdrReopenCleanupManual(journal)
	if err = completeHerdrServerLifecycle(locked, journal, intent.ID); err != nil {
		return nil, err
	}
	return restarted, nil
}

func releaseRejectedHerdrRestart(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
	created bool,
	cause error,
) error {
	if !created || !errors.Is(cause, herdrrun.ErrOwnedGenerationStillLive) {
		return cause
	}
	return releaseHerdrIntent(journal, intent.ID, cause)
}

// ShutdownHerdrServer explicitly retires an empty owned server. A saved intent
// retry confirms absence and never repeats an ambiguous shutdown signal.
func ShutdownHerdrServer(
	ctx context.Context,
	projectRoot string,
	opts herdrrun.OwnedOptions,
) (err error) {
	defer errs.Wrap(&err, "shutdown Herdr owned server")
	locked, err := state.LockProjectForLaunchContext(ctx, projectRoot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	intent, err := prepareOrResumeHerdrShutdown(ctx, projectRoot, journal, opts)
	if err != nil {
		return err
	}
	markIssued, err := herdrShutdownIssueCallback(journal, intent)
	if err != nil {
		return err
	}
	if err = herdrrun.ShutdownOwned(ctx, opts, *intent.Server, markIssued); err != nil {
		return err
	}
	return completeHerdrServerLifecycle(locked, journal, intent.ID)
}

func prepareOrResumeHerdrShutdown(
	ctx context.Context,
	projectRoot string,
	journal *state.LockedHerdrIntents,
	opts herdrrun.OwnedOptions,
) (state.HerdrIntent, error) {
	intent, found, err := currentHerdrServerIntent(journal, state.HerdrIntentShutdown)
	if err != nil || found {
		return intent, err
	}
	if err := rejectActiveHerdrIntents(journal.HerdrIntents); err != nil {
		return state.HerdrIntent{}, err
	}
	return prepareHerdrShutdown(ctx, projectRoot, journal, opts)
}

func herdrShutdownIssueCallback(
	journal *state.LockedHerdrIntents,
	intent state.HerdrIntent,
) (func() error, error) {
	if intent.Status == state.HerdrIntentIssued {
		return nil, nil
	}
	if intent.Status != state.HerdrIntentPlanned {
		return nil, fmt.Errorf("herdr shutdown intent has invalid status %q", intent.Status)
	}
	return func() error {
		intent.Status = state.HerdrIntentIssued
		journal.UpsertIntent(intent)
		return journal.Save()
	}, nil
}

func rejectActiveHerdrIntents(journal state.HerdrIntents) error {
	if len(journal.Intents) != 0 {
		return fmt.Errorf("%d active Herdr intent rows remain", len(journal.Intents))
	}
	return nil
}

func ensureHerdrServerIntent(
	journal *state.LockedHerdrIntents,
	kind state.HerdrIntentKind,
	opts herdrrun.OwnedOptions,
) (state.HerdrIntent, bool, error) {
	intent, found, err := currentHerdrServerIntent(journal, kind)
	if err != nil || found {
		return intent, found, err
	}
	identity, err := herdrrun.InspectOwnedServer(opts)
	if err != nil {
		return state.HerdrIntent{}, false, err
	}
	intent, err = newHerdrServerIntent(kind, identity)
	if err != nil {
		return state.HerdrIntent{}, false, err
	}
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		return state.HerdrIntent{}, false, err
	}
	return intent, true, nil
}

func currentHerdrServerIntent(
	journal *state.LockedHerdrIntents,
	kind state.HerdrIntentKind,
) (state.HerdrIntent, bool, error) {
	intent, found, err := journal.ServerLifecycleIntent()
	if err != nil || !found {
		return intent, found, err
	}
	if intent.Kind != kind {
		return state.HerdrIntent{}, false, fmt.Errorf(
			"herdr owned server %s is pending; refusing %s",
			herdrServerAction(intent.Kind), herdrServerAction(kind),
		)
	}
	return intent, true, nil
}

func newHerdrServerIntent(
	kind state.HerdrIntentKind,
	identity state.HerdrServerIdentity,
) (state.HerdrIntent, error) {
	id, err := state.HerdrServerIntentID(kind)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	return state.HerdrIntent{
		ID: id, Kind: kind, Status: state.HerdrIntentPlanned, Server: &identity,
	}, nil
}

func prepareHerdrShutdown(
	ctx context.Context,
	projectRoot string,
	journal *state.LockedHerdrIntents,
	opts herdrrun.OwnedOptions,
) (state.HerdrIntent, error) {
	identity, err := herdrrun.InspectOwnedServer(opts)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	err = rejectActiveHerdrRows(projectRoot)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	owned, err := herdrrun.OpenOwned(ctx, opts)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	workspaces, err := owned.ObserveWorkspaces(ctx)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	if len(workspaces) != 0 {
		return state.HerdrIntent{}, fmt.Errorf(
			"herdr owned server has %d active or foreign workspace resources", len(workspaces),
		)
	}
	intent, err := newHerdrServerIntent(state.HerdrIntentShutdown, identity)
	if err != nil {
		return state.HerdrIntent{}, err
	}
	journal.UpsertIntent(intent)
	return intent, journal.Save()
}

func rejectActiveHerdrRows(projectRoot string) error {
	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return fmt.Errorf("list linked worktrees before Herdr shutdown: %w", err)
	}
	for _, root := range roots {
		store, err := state.LoadProject(root)
		if err != nil {
			return err
		}
		for _, pane := range store.Panes {
			if backend.NormalizeName(pane.Backend) == backend.Herdr {
				return fmt.Errorf("active Herdr state row remains in %s", filepath.Clean(root))
			}
		}
	}
	return nil
}

func verifyRestartedHerdrRows(
	ctx context.Context,
	projectRoot string,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	restarted HerdrRestartRuntime,
) error {
	return resumeRestartedHerdrRows(ctx, projectRoot, locked, journal, restarted, 0)
}

func sameHerdrRestartRoute(saved state.Pane, current backend.LivePane) bool {
	return current.Ref.Backend == backend.Herdr &&
		current.SessionID == saved.HerdrSession && current.SocketPath == saved.HerdrSocketPath &&
		current.Ref.Workspace == saved.HerdrWorkspaceID && current.Ref.Pane == saved.PaneID
}

func completeHerdrServerLifecycle(
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
	intentID string,
) error {
	if err := locked.Save(); err != nil {
		return err
	}
	if !journal.RemoveIntent(intentID) {
		return fmt.Errorf("herdr server lifecycle intent %s disappeared before completion", intentID)
	}
	journal.Intents = slices.DeleteFunc(journal.Intents, func(intent state.HerdrIntent) bool {
		return intent.Kind == state.HerdrIntentResume &&
			intent.Status != state.HerdrIntentManualCleanupRequired
	})
	return journal.Save()
}

func markPlannedHerdrReopenCleanupManual(journal *state.LockedHerdrIntents) {
	for i := range journal.Intents {
		intent := &journal.Intents[i]
		if intent.Kind != state.HerdrIntentCleanup || intent.CleanupPhase != state.HerdrCleanupReopen ||
			intent.Status != state.HerdrIntentPlanned {
			continue
		}
		intent.Status = state.HerdrIntentManualCleanupRequired
		intent.Failure = "Herdr server restart invalidated the saved cleanup coordinator identity"
	}
}

func herdrServerAction(kind state.HerdrIntentKind) string {
	if kind == state.HerdrIntentShutdown {
		return "shutdown"
	}
	return "restart"
}
