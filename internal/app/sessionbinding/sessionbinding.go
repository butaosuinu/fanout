// Package sessionbinding persists the first late Herdr agent session observed
// for an otherwise complete state row.
package sessionbinding

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// StateLoader binds a valid late agent session under the owning state lock,
// then returns the same merged state shape as sessionview.MergedStateLoader.
func StateLoader(
	projectRoot string,
	listLive func() ([]backend.LivePane, error),
) func() (state.Store, error) {
	return func() (state.Store, error) {
		store, err := sessionview.MergedStateLoader(projectRoot, listLive)()
		if err != nil || listLive == nil || !hasUnboundHerdrAgent(store.Panes) {
			return store, err
		}
		live, _ := listLive()
		for _, root := range bindingRoots(projectRoot, store.Panes) {
			if err := bindOwnedHerdrAgentSessions(root, live); err != nil {
				return state.Store{}, fmt.Errorf("bind Herdr agent session in %s: %w", root, err)
			}
		}
		cachedLive := func() ([]backend.LivePane, error) { return live, nil }
		return sessionview.MergedStateLoader(projectRoot, cachedLive)()
	}
}

func hasUnboundHerdrAgent(panes []state.Pane) bool {
	return slices.ContainsFunc(panes, herdrAgentSessionUnbound)
}

func bindingRoots(projectRoot string, panes []state.Pane) []string {
	seen := map[string]bool{}
	var roots []string
	for _, pane := range panes {
		if !herdrAgentSessionUnbound(pane) {
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

func bindOwnedHerdrAgentSessions(projectRoot string, live []backend.LivePane) (err error) {
	locked, err := state.LockProject(projectRoot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	changed := false
	for i := range locked.Panes {
		ref, ok := UniqueHerdrSessionBinding(locked.Panes, i, live)
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

// UniqueHerdrSessionBinding returns the first valid late session only when one
// persisted row and one live observation share the same launch identity.
func UniqueHerdrSessionBinding(
	panes []state.Pane,
	target int,
	live []backend.LivePane,
) (*backend.AgentSessionRef, bool) {
	if !herdrAgentSessionUnbound(panes[target]) {
		return nil, false
	}
	current, ok := uniqueHerdrSessionObservation(panes[target], live)
	if !ok || countHerdrRowsForObservation(panes, current) != 1 {
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

// firstBindOptions is the variance the first binding runs under: the row must
// be a runtime row of the same backend, and the observed conversation is
// admitted on its own validity because the row has none to compare against.
func firstBindOptions() []backend.MatchOption {
	return []backend.MatchOption{
		backend.RequireRuntime(backend.Herdr), backend.AllowUnboundAgentSession(),
	}
}

func uniqueHerdrSessionObservation(
	pane state.Pane,
	live []backend.LivePane,
) (backend.LivePane, bool) {
	return pane.RuntimeBinding().UniqueLive(live, firstBindOptions()...)
}

func countHerdrRowsForObservation(panes []state.Pane, current backend.LivePane) int {
	count := 0
	for _, pane := range panes {
		if FirstBindMatches(pane, current) {
			count++
		}
	}
	return count
}

func herdrAgentSessionUnbound(pane state.Pane) bool {
	return backend.NormalizeName(pane.Backend) == backend.Herdr &&
		strings.TrimSpace(pane.Agent) != "" && strings.TrimSpace(pane.AgentID) != "" &&
		pane.AgentSession == nil
}
