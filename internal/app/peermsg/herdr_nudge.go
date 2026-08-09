package peermsg

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/herdrprocess"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const herdrNudgeTimeout = 30 * time.Second

func deliverHerdrNudge(pane state.Pane, deps Deps) (agentState, reason string, nudged bool) {
	observedState, reason, candidate := herdrNudgeCandidate(pane)
	if !candidate {
		return observedState, reason, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), herdrNudgeTimeout)
	defer cancel()
	runtime, latest, latestState, err := prepareHerdrNudge(ctx, pane, deps)
	if err != nil {
		return latestState, err.Error(), false
	}
	if err := runtime.Nudge(ctx, herdrNudgeTarget(latest), nudgeText); err != nil {
		return latestState, fmt.Sprintf("agent prompt failed: %v", err), false
	}
	return latestState, "", true
}

func herdrNudgeCandidate(pane state.Pane) (string, string, bool) {
	observedState := strings.TrimSpace(pane.ReportedState)
	if !pane.StateRefinement {
		return observedState, "agent state is not refined for the current launch", false
	}
	if !shouldNudge(observedState) {
		return observedState, fmt.Sprintf("agent is not nudgeable (state %q)", observedState), false
	}
	return observedState, "", true
}

func prepareHerdrNudge(ctx context.Context, pane state.Pane, deps Deps) (HerdrNudger, state.Pane, string, error) {
	if deps.OpenHerdr == nil || deps.ReadLockedState == nil {
		return nil, state.Pane{}, "", fmt.Errorf("herdr nudge runtime is unavailable")
	}
	runtime, err := deps.OpenHerdr(ctx)
	if err != nil {
		return nil, state.Pane{}, "", fmt.Errorf("open herdr runtime: %w", err)
	}
	if runtime == nil {
		return nil, state.Pane{}, "", fmt.Errorf("herdr nudge runtime is unavailable")
	}
	if err := verifyHerdrNudgeRuntime(ctx, runtime, pane); err != nil {
		return nil, state.Pane{}, "", err
	}
	latest, latestState, err := recheckHerdrNudgeState(pane, deps)
	return runtime, latest, latestState, err
}

func verifyHerdrNudgeRuntime(ctx context.Context, runtime HerdrNudger, pane state.Pane) error {
	panes, err := runtime.LivePanes(ctx)
	if err != nil {
		return fmt.Errorf("read herdr panes: %w", err)
	}
	if !uniqueHerdrNudgePane(pane, panes) {
		return fmt.Errorf("recipient pane identity or worktree provenance changed")
	}
	return verifyHerdrNudgeProcess(ctx, runtime, pane)
}

func verifyHerdrNudgeProcess(ctx context.Context, runtime HerdrNudger, pane state.Pane) error {
	process, err := runtime.ProcessInfo(ctx, pane.PaneID)
	if err != nil {
		return fmt.Errorf("read recipient process identity: %w", err)
	}
	if process.PaneID != pane.PaneID {
		return fmt.Errorf("recipient process identity belongs to another pane")
	}
	err = herdrprocess.VerifyAgent(process, herdrprocess.Identity{
		WorktreePath: pane.WorktreePath,
		Executable:   pane.HerdrLaunchExecutable,
		Args:         pane.HerdrLaunchArgs,
	})
	if err != nil {
		return fmt.Errorf("verify recipient process identity: %w", err)
	}
	return nil
}

func uniqueHerdrNudgePane(recorded state.Pane, panes []backend.LivePane) bool {
	matches := 0
	for _, pane := range panes {
		if herdrNudgePaneMatches(recorded, pane) {
			matches++
		}
	}
	return matches == 1
}

func herdrNudgePaneMatches(recorded state.Pane, current backend.LivePane) bool {
	return sessionview.HerdrPaneMatches(recorded, current)
}

func recheckHerdrNudgeState(recorded state.Pane, deps Deps) (state.Pane, string, error) {
	var latest state.Pane
	err := deps.ReadLockedState(func(store state.Store) error {
		var found bool
		latest, found = uniqueHerdrNudgeRow(store, recorded.EmitterRowKey)
		if !found || !sameHerdrNudgeBinding(recorded, latest) {
			return fmt.Errorf("recipient launch binding changed before prompt")
		}
		if !latest.StateRefinement {
			return fmt.Errorf("agent state is not refined for the current launch")
		}
		if !shouldNudge(latest.ReportedState) {
			return fmt.Errorf("agent is not nudgeable (state %q)", latest.ReportedState)
		}
		return nil
	})
	return latest, strings.TrimSpace(latest.ReportedState), err
}

func uniqueHerdrNudgeRow(store state.Store, rowKey string) (state.Pane, bool) {
	var matched state.Pane
	count := 0
	for _, pane := range store.Panes {
		if rowKey != "" && pane.EmitterRowKey == rowKey {
			matched = pane
			count++
		}
	}
	return matched, count == 1
}

func sameHerdrNudgeBinding(left, right state.Pane) bool {
	checks := []bool{
		left.Parent == right.Parent, left.IssueNum == right.IssueNum, left.TaskID == right.TaskID,
		left.Backend == right.Backend, left.PaneID == right.PaneID,
		left.HerdrWorkspaceID == right.HerdrWorkspaceID,
		left.HerdrTerminalID == right.HerdrTerminalID, left.HerdrRepoKey == right.HerdrRepoKey,
		left.HerdrAgentID == right.HerdrAgentID, left.HerdrSession == right.HerdrSession,
		left.HerdrSocketPath == right.HerdrSocketPath,
		sameAgentSession(left.HerdrAgentSession, right.HerdrAgentSession),
		left.Agent == right.Agent, left.WorktreePath == right.WorktreePath,
		left.EmitterRowKey == right.EmitterRowKey, left.LaunchNonce == right.LaunchNonce,
		left.EmitterNonce == right.EmitterNonce,
		left.HerdrLaunchExecutable == right.HerdrLaunchExecutable,
		slices.Equal(left.HerdrLaunchArgs, right.HerdrLaunchArgs),
	}
	return !slices.Contains(checks, false)
}

func sameAgentSession(left, right *backend.AgentSessionRef) bool {
	return left != nil && right != nil && *left == *right
}

func herdrNudgeTarget(pane state.Pane) herdrrun.NudgeTarget {
	return herdrrun.NudgeTarget{
		Ref: paneRef(pane), SessionID: pane.HerdrSession, SocketPath: pane.HerdrSocketPath,
		TerminalID: pane.HerdrTerminalID, AgentID: pane.HerdrAgentID,
		AgentSession: pane.HerdrAgentSession,
	}
}
