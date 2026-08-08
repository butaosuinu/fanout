// Package sessionbinding persists verified Herdr agent-session bindings for
// owned state rows, including Codex session rebinds after a cold restart.
package sessionbinding

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type codexRebinder func(
	context.Context,
	string,
	state.Pane,
	time.Duration,
) (backend.LivePane, bool)

// StateLoader binds a valid late agent session under the owning state lock,
// then returns the same merged state shape as sessionview.MergedStateLoader.
func StateLoader(
	projectRoot string,
	listLive func() ([]backend.LivePane, error),
) func() (state.Store, error) {
	return stateLoader(projectRoot, listLive, rebindOwnedCodexAgent)
}

func stateLoader(
	projectRoot string,
	listLive func() ([]backend.LivePane, error),
	rebind codexRebinder,
) func() (state.Store, error) {
	return func() (state.Store, error) {
		store, err := sessionview.MergedStateLoader(projectRoot, listLive)()
		if err != nil || listLive == nil || !hasBindableHerdrAgent(store.Panes) {
			return store, err
		}
		live, _ := listLive()
		for _, root := range bindingRoots(projectRoot, store.Panes) {
			if err := bindOwnedHerdrAgentSessions(root, live); err != nil {
				return state.Store{}, fmt.Errorf("bind Herdr agent session in %s: %w", root, err)
			}
			live, err = rebindOwnedCodexSessions(root, live, rebind)
			if err != nil {
				return state.Store{}, fmt.Errorf("rebind Herdr Codex session in %s: %w", root, err)
			}
		}
		cachedLive := func() ([]backend.LivePane, error) { return live, nil }
		return sessionview.MergedStateLoader(projectRoot, cachedLive)()
	}
}

func hasBindableHerdrAgent(panes []state.Pane) bool {
	return slices.ContainsFunc(panes, herdrAgentNeedsBinding)
}

func bindingRoots(projectRoot string, panes []state.Pane) []string {
	seen := map[string]bool{}
	var roots []string
	for _, pane := range panes {
		if !herdrAgentNeedsBinding(pane) {
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

func herdrAgentNeedsBinding(pane state.Pane) bool {
	return herdrAgentSessionUnbound(pane) || codexResumeBaseline(pane)
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
		ref, ok := uniqueHerdrSessionBinding(locked.Panes, i, live)
		if !ok {
			continue
		}
		locked.Panes[i].HerdrAgentSession = ref
		changed = true
	}
	if changed {
		return locked.Save()
	}
	return nil
}

func uniqueHerdrSessionBinding(
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

func uniqueHerdrSessionObservation(
	pane state.Pane,
	live []backend.LivePane,
) (backend.LivePane, bool) {
	var matched backend.LivePane
	count := 0
	for _, current := range live {
		if sessionview.HerdrPaneMatchesForSessionBinding(pane, current) {
			matched = current
			count++
		}
	}
	return matched, count == 1
}

func countHerdrRowsForObservation(panes []state.Pane, current backend.LivePane) int {
	count := 0
	for _, pane := range panes {
		if sessionview.HerdrPaneMatchesForSessionBinding(pane, current) {
			count++
		}
	}
	return count
}

func herdrAgentSessionUnbound(pane state.Pane) bool {
	return backend.NormalizeName(pane.Backend) == backend.Herdr &&
		strings.TrimSpace(pane.Agent) != "" && strings.TrimSpace(pane.HerdrAgentID) != "" &&
		pane.HerdrAgentSession == nil
}

func codexResumeBaseline(pane state.Pane) bool {
	process := pane.HerdrProcessIdentity
	session := pane.HerdrAgentSession
	requirements := []bool{
		backend.NormalizeName(pane.Backend) == backend.Herdr,
		pane.Agent == "codex",
		session != nil,
		process != nil,
		filepath.IsAbs(pane.HerdrLaunchExecutable),
		filepath.Clean(pane.HerdrLaunchExecutable) == pane.HerdrLaunchExecutable,
		pane.HerdrLaunchArgs != nil,
	}
	if slices.Contains(requirements, false) {
		return false
	}
	return session.Source == "herdr:codex" && session.Agent == "codex" &&
		session.Kind == "id" && session.Valid() && validProcessIdentity(*process)
}

func validProcessIdentity(identity backend.ProcessIdentity) bool {
	return identity.ShellPID > 1 && identity.ForegroundProcessGroup > 1 && identity.AgentPID > 1
}

func rebindOwnedCodexSessions(
	projectRoot string,
	live []backend.LivePane,
	rebind codexRebinder,
) ([]backend.LivePane, error) {
	if rebind == nil {
		return live, nil
	}
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return live, err
	}
	live = slices.Clone(live)
	for _, pane := range store.Panes {
		if !codexResumeBaseline(pane) || !hasUniqueResumeObservation(store.Panes, pane, live) {
			continue
		}
		current, ok := rebind(context.Background(), projectRoot, pane, 0)
		if !ok {
			continue
		}
		if err := persistCodexRebind(projectRoot, pane, current); err != nil {
			return live, err
		}
		replaceResumeObservation(pane, live, current)
	}
	return live, nil
}

func replaceResumeObservation(pane state.Pane, live []backend.LivePane, current backend.LivePane) {
	for i := range live {
		if sessionview.HerdrPaneMatchesForCodexResume(pane, live[i]) {
			live[i] = current
			return
		}
	}
}

func hasUniqueResumeObservation(
	panes []state.Pane,
	target state.Pane,
	live []backend.LivePane,
) bool {
	current, ok := uniqueResumeObservation(target, live)
	return ok && countResumeRows(panes, current) == 1
}

func uniqueResumeObservation(pane state.Pane, live []backend.LivePane) (backend.LivePane, bool) {
	var matched backend.LivePane
	count := 0
	for _, current := range live {
		if sessionview.HerdrPaneMatchesForCodexResume(pane, current) {
			matched = current
			count++
		}
	}
	return matched, count == 1
}

func countResumeRows(panes []state.Pane, current backend.LivePane) int {
	count := 0
	for _, pane := range panes {
		if sessionview.HerdrPaneMatchesForCodexResume(pane, current) {
			count++
		}
	}
	return count
}

func rebindOwnedCodexAgent(
	ctx context.Context,
	projectRoot string,
	pane state.Pane,
	totalTimeout time.Duration,
) (backend.LivePane, bool) {
	owned, ok := openOwnedResumeRuntime(ctx, projectRoot, pane)
	if !ok {
		return backend.LivePane{}, false
	}
	result := owned.Backend().Wait(ctx, totalTimeout, func(panes []backend.LivePane) bool {
		current, ok := uniqueResumeObservation(pane, panes)
		return ok && current.NativeAgentState != "idle"
	})
	if result.Status != herdrrun.WaitMatched {
		return backend.LivePane{}, false
	}
	current, ok := uniqueResumeObservation(pane, result.Panes)
	if !ok {
		return backend.LivePane{}, false
	}
	processIdentity, ok := matchCodexResumeProcess(ctx, owned, pane, current.Ref.Pane)
	if !ok {
		return backend.LivePane{}, false
	}
	current.ProcessIdentity = &processIdentity
	return current, true
}

func openOwnedResumeRuntime(
	ctx context.Context,
	projectRoot string,
	pane state.Pane,
) (*herdrrun.OwnedSession, bool) {
	identity, err := worktree.ResolveRepoIdentity(ctx, projectRoot)
	if err != nil {
		return nil, false
	}
	owned, err := herdrrun.OpenOwned(ctx, herdrrun.OwnedOptions{GitCommonDir: identity.RepoKey})
	if err != nil || owned.Session != pane.HerdrSession || owned.SocketPath != pane.HerdrSocketPath {
		return nil, false
	}
	return owned, true
}

func matchCodexResumeProcess(
	ctx context.Context,
	owned *herdrrun.OwnedSession,
	pane state.Pane,
	paneID string,
) (backend.ProcessIdentity, bool) {
	process, err := owned.ProcessInfo(ctx, paneID)
	if err != nil {
		return backend.ProcessIdentity{}, false
	}
	processIdentity, err := herdrrun.MatchAgentProcess(
		process,
		pane.HerdrLaunchExecutable,
		[]string{"resume", pane.HerdrAgentSession.Value},
		pane.WorktreePath,
	)
	return processIdentity, err == nil
}

func persistCodexRebind(
	projectRoot string,
	baseline state.Pane,
	current backend.LivePane,
) (err error) {
	locked, err := state.LockProject(projectRoot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	for i := range locked.Panes {
		if !samePaneKey(locked.Panes[i], baseline) || !sameCodexResumeBaseline(locked.Panes[i], baseline) {
			continue
		}
		if current.ProcessIdentity == nil || !validProcessIdentity(*current.ProcessIdentity) ||
			!sessionview.HerdrPaneMatchesForCodexResume(locked.Panes[i], current) {
			return nil
		}
		locked.Panes[i].HerdrTerminalID = current.TerminalID
		locked.Panes[i].HerdrAgentID = current.AgentID
		processIdentity := *current.ProcessIdentity
		locked.Panes[i].HerdrProcessIdentity = &processIdentity
		return locked.Save()
	}
	return nil
}

func samePaneKey(left, right state.Pane) bool {
	if left.Parent != right.Parent {
		return false
	}
	if left.TaskID != "" || right.TaskID != "" {
		return left.TaskID != "" && left.TaskID == right.TaskID
	}
	return left.IssueNum == right.IssueNum
}

func sameCodexResumeBaseline(left, right state.Pane) bool {
	if !codexResumeBaseline(left) || !codexResumeBaseline(right) {
		return false
	}
	identity := []bool{
		left.Backend == right.Backend,
		left.PaneID == right.PaneID,
		left.HerdrWorkspaceID == right.HerdrWorkspaceID,
		left.HerdrTerminalID == right.HerdrTerminalID,
		left.HerdrRepoKey == right.HerdrRepoKey,
		left.HerdrAgentID == right.HerdrAgentID,
		left.HerdrSession == right.HerdrSession,
		left.HerdrSocketPath == right.HerdrSocketPath,
		left.HerdrLaunchExecutable == right.HerdrLaunchExecutable,
		left.WorktreePath == right.WorktreePath,
		*left.HerdrAgentSession == *right.HerdrAgentSession,
		*left.HerdrProcessIdentity == *right.HerdrProcessIdentity,
		slices.Equal(left.HerdrLaunchArgs, right.HerdrLaunchArgs),
	}
	return !slices.Contains(identity, false)
}
