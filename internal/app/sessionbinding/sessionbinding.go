// Package sessionbinding persists the Herdr agent session a state row's pane
// currently reports: the first one observed for an otherwise complete row, and
// the replacement after the provider starts a new conversation in that pane.
//
// This is the rebinding path for every agent. The telemetry emitter rebinds
// too, but only providers that emit reach it (validTelemetryAgent), so a
// direct Codex pane would otherwise keep a stale reference and stay out of
// resume, which matches on the recorded value.
package sessionbinding

import (
	"errors"
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// StateLoader records the agent session each row's pane currently reports,
// under that row's own state lock, then returns the same merged state shape as
// sessionview.MergedStateLoader. The runtime is observed once and that single
// observation feeds both the merge and the binding decision.
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
		roots := bindingRoots(projectRoot, store.Panes, live)
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
}

func bindingRoots(projectRoot string, panes []state.Pane, live []backend.LivePane) []string {
	seen := map[string]bool{}
	var roots []string
	for i, pane := range panes {
		if _, ok := currentSessionBinding(panes, i, live); !ok {
			continue
		}
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
