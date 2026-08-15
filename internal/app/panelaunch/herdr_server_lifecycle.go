package panelaunch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// ManagedServerIO is the owned-server lifecycle seam. The composition root binds
// every field to one repository's owned options, so the app drives the
// journal-fenced transaction without naming the runtime that performs it.
type ManagedServerIO struct {
	// InspectServer reads the saved server identity without mutating it.
	InspectServer func() (state.RuntimeServerIdentity, error)
	// ObserveWorkspaces opens the owned server read-only and lists everything
	// still holding a resource on it.
	ObserveWorkspaces func(context.Context) ([]backend.WorkspaceObservation, error)
	// RestartServer replaces the proven-dead generation named by the identity.
	RestartServer func(context.Context, state.RuntimeServerIdentity) (ManagedRestartedServer, error)
	// ShutdownServer retires the empty generation, calling the callback once at
	// the moment the signal becomes indeterminate.
	ShutdownServer func(context.Context, state.RuntimeServerIdentity, func() error) error
}

// ManagedRestartedServer is the replacement generation a restart produced: the
// surface saved rows are rebound through, and the session name to report.
type ManagedRestartedServer struct {
	Runtime ManagedRestartRuntime
	Session string
}

// RestartManagedServer explicitly replaces a proven-dead owned server while the
// combined state/intent lock fences every other fanout mutation. It returns the
// replacement session name.
func RestartManagedServer(
	ctx context.Context,
	projectRoot string,
	io ManagedServerIO,
) (_ string, err error) {
	defer errs.Wrap(&err, "restart Herdr owned server")
	locked, err := state.LockProjectForLaunchContext(ctx, projectRoot)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return "", err
	}
	intent, created, err := ensureManagedServerIntent(journal, state.IntentRestart, io)
	if err != nil {
		return "", err
	}
	restarted, err := io.RestartServer(ctx, *intent.Server)
	if err != nil {
		return "", releaseRejectedManagedRestart(journal, intent, created, err)
	}
	if err = verifyRestartedManagedRows(ctx, projectRoot, locked, journal, restarted.Runtime); err != nil {
		return "", err
	}
	markPlannedManagedReopenCleanupManual(journal)
	if err = completeManagedServerLifecycle(locked, journal, intent.ID); err != nil {
		return "", err
	}
	return restarted.Session, nil
}

func releaseRejectedManagedRestart(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
	created bool,
	cause error,
) error {
	if !created || !errors.Is(cause, backend.ErrOwnedGenerationStillLive) {
		return cause
	}
	return releaseManagedIntent(journal, intent.ID, cause)
}

// ShutdownManagedServer explicitly retires an empty owned server. A saved intent
// retry confirms absence and never repeats an ambiguous shutdown signal.
func ShutdownManagedServer(
	ctx context.Context,
	projectRoot string,
	io ManagedServerIO,
) (err error) {
	defer errs.Wrap(&err, "shutdown Herdr owned server")
	locked, err := state.LockProjectForLaunchContext(ctx, projectRoot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return err
	}
	intent, err := prepareOrResumeManagedShutdown(ctx, projectRoot, journal, io)
	if err != nil {
		return err
	}
	markIssued, err := managedShutdownIssueCallback(journal, intent)
	if err != nil {
		return err
	}
	if err = io.ShutdownServer(ctx, *intent.Server, markIssued); err != nil {
		return err
	}
	return completeManagedServerLifecycle(locked, journal, intent.ID)
}

func prepareOrResumeManagedShutdown(
	ctx context.Context,
	projectRoot string,
	journal *state.LockedLaunchJournal,
	io ManagedServerIO,
) (state.LaunchIntent, error) {
	intent, found, err := currentManagedServerIntent(journal, state.IntentShutdown)
	if err != nil || found {
		return intent, err
	}
	if err := rejectActiveManagedIntents(journal.LaunchJournal); err != nil {
		return state.LaunchIntent{}, err
	}
	return prepareManagedShutdown(ctx, projectRoot, journal, io)
}

func managedShutdownIssueCallback(
	journal *state.LockedLaunchJournal,
	intent state.LaunchIntent,
) (func() error, error) {
	if intent.Status == state.IntentIssued {
		return nil, nil
	}
	if intent.Status != state.IntentPlanned {
		return nil, fmt.Errorf("herdr shutdown intent has invalid status %q", intent.Status)
	}
	return func() error {
		intent.Status = state.IntentIssued
		journal.UpsertIntent(intent)
		return journal.Save()
	}, nil
}

func rejectActiveManagedIntents(journal state.LaunchJournal) error {
	if len(journal.Intents) != 0 {
		return fmt.Errorf("%d active Herdr intent rows remain", len(journal.Intents))
	}
	return nil
}

func ensureManagedServerIntent(
	journal *state.LockedLaunchJournal,
	kind state.LaunchIntentKind,
	io ManagedServerIO,
) (state.LaunchIntent, bool, error) {
	intent, found, err := currentManagedServerIntent(journal, kind)
	if err != nil || found {
		return intent, found, err
	}
	identity, err := io.InspectServer()
	if err != nil {
		return state.LaunchIntent{}, false, err
	}
	intent, err = newManagedServerIntent(kind, identity)
	if err != nil {
		return state.LaunchIntent{}, false, err
	}
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		return state.LaunchIntent{}, false, err
	}
	return intent, true, nil
}

func currentManagedServerIntent(
	journal *state.LockedLaunchJournal,
	kind state.LaunchIntentKind,
) (state.LaunchIntent, bool, error) {
	intent, found, err := journal.ServerLifecycleIntent()
	if err != nil || !found {
		return intent, found, err
	}
	if intent.Kind != kind {
		return state.LaunchIntent{}, false, fmt.Errorf(
			"herdr owned server %s is pending; refusing %s",
			managedServerAction(intent.Kind), managedServerAction(kind),
		)
	}
	return intent, true, nil
}

func newManagedServerIntent(
	kind state.LaunchIntentKind,
	identity state.RuntimeServerIdentity,
) (state.LaunchIntent, error) {
	id, err := state.ServerIntentID(kind)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	return state.LaunchIntent{
		ID: id, Kind: kind, Status: state.IntentPlanned, Server: &identity,
	}, nil
}

func prepareManagedShutdown(
	ctx context.Context,
	projectRoot string,
	journal *state.LockedLaunchJournal,
	io ManagedServerIO,
) (state.LaunchIntent, error) {
	identity, err := io.InspectServer()
	if err != nil {
		return state.LaunchIntent{}, err
	}
	err = rejectActiveManagedRows(projectRoot)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	workspaces, err := io.ObserveWorkspaces(ctx)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	if len(workspaces) != 0 {
		return state.LaunchIntent{}, fmt.Errorf(
			"herdr owned server has %d active or foreign workspace resources", len(workspaces),
		)
	}
	intent, err := newManagedServerIntent(state.IntentShutdown, identity)
	if err != nil {
		return state.LaunchIntent{}, err
	}
	journal.UpsertIntent(intent)
	return intent, journal.Save()
}

func rejectActiveManagedRows(projectRoot string) error {
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

func verifyRestartedManagedRows(
	ctx context.Context,
	projectRoot string,
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	restarted ManagedRestartRuntime,
) error {
	return resumeRestartedManagedRows(ctx, projectRoot, locked, journal, restarted, 0)
}

func sameManagedRestartRoute(saved state.Pane, current backend.LivePane) bool {
	return current.Ref.Backend == backend.Herdr &&
		current.SessionID == saved.SessionID && current.SocketPath == saved.SocketPath &&
		current.Ref.Workspace == saved.WorkspaceID && current.Ref.Pane == saved.PaneID
}

func completeManagedServerLifecycle(
	locked *state.LockedStore,
	journal *state.LockedLaunchJournal,
	intentID string,
) error {
	if err := locked.Save(); err != nil {
		return err
	}
	if !journal.RemoveIntent(intentID) {
		return fmt.Errorf("herdr server lifecycle intent %s disappeared before completion", intentID)
	}
	return journal.Save()
}

func markPlannedManagedReopenCleanupManual(journal *state.LockedLaunchJournal) {
	for i := range journal.Intents {
		intent := &journal.Intents[i]
		if intent.Kind != state.IntentCleanup || intent.CleanupPhase != state.CleanupReopen ||
			intent.Status != state.IntentPlanned {
			continue
		}
		intent.Status = state.IntentManualCleanupRequired
		intent.Failure = "Herdr server restart invalidated the saved cleanup coordinator identity"
	}
}

func managedServerAction(kind state.LaunchIntentKind) string {
	if kind == state.IntentShutdown {
		return "shutdown"
	}
	return "restart"
}
