// Package sessionbinding persists the current Herdr location and agent session
// reported for a state row.
//
// This is the rebinding path for every agent. The telemetry emitter rebinds
// too, but only providers that emit reach it (validTelemetryAgent), so a
// direct Codex pane would otherwise keep stale location and conversation
// references and stay out of resume, which matches on the recorded values.
package sessionbinding

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// StateLoader records each agent row's current runtime location and session
// under that row's own state lock, then returns the same merged state shape as
// sessionview.MergedStateLoader. The runtime is observed once and that single
// observation feeds the merge and both binding decisions.
func StateLoader(
	projectRoot string,
	listLive func() ([]backend.LivePane, error),
) func() (state.Store, error) {
	return func() (state.Store, error) {
		if listLive == nil {
			return sessionview.MergedStateLoader(projectRoot, nil)()
		}
		live, liveErr := listLive()
		cachedLive := func() ([]backend.LivePane, error) { return live, liveErr }
		store, err := sessionview.MergedStateLoader(projectRoot, cachedLive)()
		if err != nil {
			return store, err
		}
		return bindCurrentState(projectRoot, store, live, cachedLive)
	}
}

func bindCurrentState(
	projectRoot string,
	store state.Store,
	live []backend.LivePane,
	cachedLive func() ([]backend.LivePane, error),
) (state.Store, error) {
	roots, err := bindingRoots(projectRoot, store.Panes, live)
	if err != nil {
		return state.Store{}, err
	}
	if len(roots) == 0 {
		return store, nil
	}
	for _, root := range roots {
		if err := bindOwnedAgentSessions(root, live); err != nil {
			return state.Store{}, fmt.Errorf("bind Herdr agent session in %s: %w", root, err)
		}
	}
	return sessionview.MergedStateLoader(projectRoot, cachedLive)()
}

// ReloadPane refreshes the owning state row, then resolves it through the
// repository-wide row identity that stays stable when runtime location moves.
func ReloadPane(
	projectRoot string,
	expected state.Pane,
	listLive func() ([]backend.LivePane, error),
) (state.Pane, error) {
	store, err := StateLoader(projectRoot, listLive)()
	if err != nil {
		return state.Pane{}, err
	}
	pane, found, err := reloadedPane(store, expected)
	if err != nil {
		return state.Pane{}, err
	}
	if !found {
		return state.Pane{}, fmt.Errorf("saved managed pane row disappeared during refresh")
	}
	return pane, nil
}

func reloadedPane(store state.Store, expected state.Pane) (state.Pane, bool, error) {
	if strings.TrimSpace(expected.EmitterRowKey) != "" {
		index, err := store.EmitterRowIndex(
			expected.EmitterRowKey, filepath.Clean(expected.WorktreePath), expected.WorkspaceLabel,
		)
		if err != nil || index < 0 {
			return state.Pane{}, false, err
		}
		return store.Panes[index], true, nil
	}
	var pane state.Pane
	var found bool
	if strings.TrimSpace(expected.TaskID) != "" {
		pane, found = store.FindTask(expected.Parent, expected.TaskID)
	} else {
		pane, found = store.Find(expected.Parent, expected.IssueNum)
	}
	if found && (pane.WorkspaceLabel != expected.WorkspaceLabel ||
		filepath.Clean(pane.WorktreePath) != filepath.Clean(expected.WorktreePath)) {
		return state.Pane{}, false, fmt.Errorf("saved managed pane row identity changed during refresh")
	}
	return pane, found, nil
}

func bindingRoots(
	projectRoot string,
	panes []state.Pane,
	live []backend.LivePane,
) ([]string, error) {
	owners := bindingOwnerRoots(projectRoot, panes)
	var roots []string
	for _, root := range owners {
		store, err := state.LoadProject(root)
		if err != nil {
			return nil, fmt.Errorf("load agent binding owner %s: %w", root, err)
		}
		if paneBindingsChanged(store.Panes, live) {
			roots = append(roots, root)
		}
	}
	return roots, nil
}

func bindingOwnerRoots(projectRoot string, panes []state.Pane) []string {
	seen := map[string]bool{}
	var roots []string
	for _, pane := range panes {
		for _, root := range paneBindingOwners(projectRoot, pane) {
			if seen[root] {
				continue
			}
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots
}

func paneBindingsChanged(panes []state.Pane, live []backend.LivePane) bool {
	for index, pane := range panes {
		_, locationChanged, _ := panelaunch.ReconcileManagedPaneLocationFromLive(pane, live)
		_, sessionChanged := currentSessionBinding(panes, index, live)
		if locationChanged || sessionChanged {
			return true
		}
	}
	return false
}

func paneBindingOwners(projectRoot string, pane state.Pane) []string {
	if len(pane.SourceProjectRoots) > 0 {
		return pane.SourceProjectRoots
	}
	root := strings.TrimSpace(pane.SourceProjectRoot)
	if root == "" {
		root = projectRoot
	}
	return []string{root}
}

func bindOwnedAgentSessions(projectRoot string, live []backend.LivePane) (err error) {
	locked, err := state.LockProject(projectRoot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	changed := false
	for i := range locked.Panes {
		pane, locationChanged, locationErr := panelaunch.ReconcileManagedPaneLocationFromLive(locked.Panes[i], live)
		if locationErr == nil && locationChanged {
			locked.Panes[i] = pane
			changed = true
		}
		ref, ok := currentSessionBinding(locked.Panes, i, live)
		if !ok {
			continue
		}
		locked.Panes[i].AgentSession = ref
		changed = true
	}
	if changed {
		return locked.Save()
	}
	return nil
}

// currentSessionBinding returns the conversation row target should record, or
// false when it already records what its pane reports. A row with none yet
// takes the first-bind rule; a row that has one takes the provider's
// replacement, which the liveness matcher has already limited to a conversation
// the same runtime issued for the same provider.
func currentSessionBinding(
	panes []state.Pane,
	target int,
	live []backend.LivePane,
) (*backend.AgentSessionRef, bool) {
	if panes[target].AgentSession == nil {
		return UniqueSessionBinding(panes, target, live)
	}
	return replacementSessionBinding(panes[target], live)
}

func replacementSessionBinding(
	pane state.Pane,
	live []backend.LivePane,
) (*backend.AgentSessionRef, bool) {
	if !recordsAgentSession(pane) {
		return nil, false
	}
	current, ok := pane.RuntimeBinding().UniqueLive(live, runtimeRowOptions()...)
	if !ok || current.AgentSession == nil ||
		backend.SameAgentSession(pane.AgentSession, current.AgentSession) {
		return nil, false
	}
	ref := *current.AgentSession
	return &ref, true
}

// UniqueSessionBinding returns the first valid late session only when one
// persisted row and one live observation share the same launch identity.
func UniqueSessionBinding(
	panes []state.Pane,
	target int,
	live []backend.LivePane,
) (*backend.AgentSessionRef, bool) {
	if !agentSessionUnbound(panes[target]) {
		return nil, false
	}
	current, ok := uniqueSessionObservation(panes[target], live)
	if !ok || countRowsForObservation(panes, current) != 1 {
		return nil, false
	}
	ref := *current.AgentSession
	return &ref, true
}

// FirstBindMatches reports whether current is the observation of pane's own
// pane while the row's conversation is still unrecorded. The recorded
// conversation is deliberately not consulted, so a row that already carries one
// still counts as a claimant of the observation.
func FirstBindMatches(pane state.Pane, current backend.LivePane) bool {
	return pane.RuntimeBinding().MatchesLive(current, firstBindOptions()...)
}

// runtimeRowOptions restricts a match to rows of the runtime that records
// conversations at all; a rebind adds no other variance, because the row
// already has a conversation to compare against.
func runtimeRowOptions() []backend.MatchOption {
	return []backend.MatchOption{backend.RequireRuntime(backend.Herdr)}
}

// firstBindOptions is the variance the first binding runs under: the row must
// be a runtime row of the same backend, and the observed conversation is
// admitted on its own validity because the row has none to compare against.
func firstBindOptions() []backend.MatchOption {
	return append(runtimeRowOptions(), backend.AllowUnboundAgentSession())
}

func uniqueSessionObservation(
	pane state.Pane,
	live []backend.LivePane,
) (backend.LivePane, bool) {
	return pane.RuntimeBinding().UniqueLive(live, firstBindOptions()...)
}

func countRowsForObservation(panes []state.Pane, current backend.LivePane) int {
	count := 0
	for _, pane := range panes {
		if FirstBindMatches(pane, current) {
			count++
		}
	}
	return count
}

func agentSessionUnbound(pane state.Pane) bool {
	return recordsAgentSession(pane) && pane.AgentSession == nil
}

// recordsAgentSession reports whether pane is a row on the runtime that records
// conversations, complete enough for one to be bound to it.
func recordsAgentSession(pane state.Pane) bool {
	return backend.NormalizeName(pane.Backend) == backend.Herdr &&
		strings.TrimSpace(pane.Agent) != "" && strings.TrimSpace(pane.AgentID) != ""
}
