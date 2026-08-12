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
	intent, _, err := ensureHerdrServerIntent(journal, state.HerdrIntentRestart, opts)
	if err != nil {
		return nil, err
	}
	restarted, err := herdrrun.RestartOwned(ctx, opts, *intent.Server)
	if err != nil {
		return nil, err
	}
	if err = verifyRestartedHerdrRows(ctx, projectRoot, locked, restarted); err != nil {
		return nil, err
	}
	if err = completeHerdrServerLifecycle(locked, journal, intent.ID); err != nil {
		return nil, err
	}
	return restarted, nil
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
	intent, found, err := currentHerdrServerIntent(journal, state.HerdrIntentShutdown)
	if err != nil {
		return err
	}
	if !found {
		if err = rejectActiveHerdrIntents(journal.HerdrIntents); err != nil {
			return err
		}
		intent, err = prepareHerdrShutdown(ctx, projectRoot, journal, opts)
		if err != nil {
			return err
		}
	}
	if err = herdrrun.ShutdownOwned(ctx, opts, *intent.Server, !found); err != nil {
		return err
	}
	return completeHerdrServerLifecycle(locked, journal, intent.ID)
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
	restarted *herdrrun.OwnedSession,
) error {
	live, err := restarted.LivePanes(ctx)
	if err != nil {
		return fmt.Errorf("verify restarted Herdr snapshot: %w", err)
	}
	return staleHerdrRowsAfterRestart(ctx, projectRoot, locked, live)
}

func staleHerdrRowsAfterRestart(
	ctx context.Context,
	projectRoot string,
	locked *state.LockedStore,
	live []backend.LivePane,
) error {
	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return fmt.Errorf("list linked worktrees after Herdr restart: %w", err)
	}
	currentRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return fmt.Errorf("canonicalize Herdr restart root: %w", err)
	}
	seen := map[string]bool{}
	for _, root := range roots {
		canonicalRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return fmt.Errorf("canonicalize linked Herdr state root: %w", err)
		}
		if seen[canonicalRoot] {
			continue
		}
		seen[canonicalRoot] = true
		if err := staleHerdrRowsInRoot(ctx, canonicalRoot, canonicalRoot == currentRoot, locked, live); err != nil {
			return err
		}
	}
	return nil
}

func staleHerdrRowsInRoot(
	ctx context.Context,
	root string,
	current bool,
	locked *state.LockedStore,
	live []backend.LivePane,
) (err error) {
	if current {
		return staleHerdrRowsInStore(&locked.Store, live)
	}
	sibling, err := state.LockContext(ctx, state.Path(root))
	if err != nil {
		return fmt.Errorf("lock linked Herdr state in %s: %w", filepath.Clean(root), err)
	}
	defer func() { err = errors.Join(err, sibling.Unlock()) }()
	if err = staleHerdrRowsInStore(&sibling.Store, live); err != nil {
		return err
	}
	return sibling.Save()
}

func staleHerdrRowsInStore(store *state.Store, live []backend.LivePane) error {
	for i := range store.Panes {
		pane := &store.Panes[i]
		if backend.NormalizeName(pane.Backend) != backend.Herdr || pane.HerdrTerminalID == "" {
			continue
		}
		if err := requireRestartedHerdrRowStale(*pane, live); err != nil {
			return err
		}
		nonce, err := randomHerdrToken()
		if err != nil {
			return fmt.Errorf("rotate stale Herdr emitter nonce: %w", err)
		}
		pane.ReportedState = ""
		pane.StateRefinement = false
		pane.EmitterNonce = nonce
	}
	return nil
}

func requireRestartedHerdrRowStale(saved state.Pane, live []backend.LivePane) error {
	route := []string{
		saved.HerdrSession, saved.HerdrSocketPath, saved.HerdrWorkspaceID, saved.PaneID,
	}
	if slices.Contains(route, "") {
		return fmt.Errorf("saved Herdr row has incomplete restart identity")
	}
	matches := 0
	for _, current := range live {
		if !sameHerdrRestartRoute(saved, current) {
			continue
		}
		matches++
		if current.TerminalID == "" || current.TerminalID == saved.HerdrTerminalID {
			return fmt.Errorf("saved Herdr row terminal identity is not stale after restart")
		}
	}
	if matches > 1 {
		return fmt.Errorf("saved Herdr row has %d matching restarted panes", matches)
	}
	return nil
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
	return journal.Save()
}

func herdrServerAction(kind state.HerdrIntentKind) string {
	if kind == state.HerdrIntentShutdown {
		return "shutdown"
	}
	return "restart"
}
