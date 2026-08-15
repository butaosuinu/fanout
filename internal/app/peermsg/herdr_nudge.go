package peermsg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/herdrprocess"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
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
	prompt, _, latestState, err := prepareHerdrNudge(ctx, pane, deps)
	if err != nil {
		return latestState, err.Error(), false
	}
	if err := prompt(ctx); err != nil {
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

func prepareHerdrNudge(ctx context.Context, pane state.Pane, deps Deps) (backend.NudgePrompt, state.Pane, string, error) {
	if deps.ReadLockedState == nil {
		return nil, state.Pane{}, "", fmt.Errorf("herdr nudge runtime is unavailable")
	}
	latest, latestState, err := recheckHerdrNudgeState(ctx, pane, deps)
	if err != nil {
		return nil, latest, latestState, err
	}
	prompt, err := prepareHerdrPrompt(ctx, latest, deps.OpenHerdr)
	if err != nil {
		return nil, latest, latestState, err
	}
	final, finalState, err := recheckHerdrNudgeState(ctx, latest, deps)
	if err != nil {
		return nil, final, finalState, err
	}
	return prompt, final, finalState, nil
}

func prepareHerdrPrompt(ctx context.Context, pane state.Pane, open func(context.Context, string) (HerdrNudger, error)) (backend.NudgePrompt, error) {
	runtime, err := openHerdrNudgeRuntime(ctx, open, pane.HerdrRepoKey)
	if err != nil {
		return nil, err
	}
	err = verifyHerdrNudgeRuntime(ctx, runtime, pane)
	if err != nil {
		return nil, err
	}
	prompt, err := runtime.PrepareNudge(ctx, herdrNudgeTarget(pane), nudgeText)
	if err != nil {
		return nil, fmt.Errorf("prepare agent prompt: %w", err)
	}
	if prompt == nil {
		return nil, fmt.Errorf("prepare agent prompt: runtime returned no prompt")
	}
	return prompt, nil
}

func openHerdrNudgeRuntime(ctx context.Context, open func(context.Context, string) (HerdrNudger, error), repoKey string) (HerdrNudger, error) {
	if open == nil || strings.TrimSpace(repoKey) == "" {
		return nil, fmt.Errorf("herdr nudge runtime is unavailable")
	}
	runtime, err := open(ctx, repoKey)
	if err != nil {
		return nil, fmt.Errorf("open herdr runtime: %w", err)
	}
	if runtime == nil {
		return nil, fmt.Errorf("herdr nudge runtime is unavailable")
	}
	return runtime, nil
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
		Agent:        pane.Agent,
	})
	if err != nil {
		return fmt.Errorf("verify recipient process identity: %w", err)
	}
	return nil
}

func uniqueHerdrNudgePane(recorded state.Pane, panes []backend.LivePane) bool {
	_, ok := recorded.RuntimeBinding().UniqueLive(panes)
	return ok
}

func recheckHerdrNudgeState(ctx context.Context, recorded state.Pane, deps Deps) (state.Pane, string, error) {
	var latest state.Pane
	err := deps.ReadLockedState(ctx, func(store state.Store) error {
		var bindingErr error
		latest, bindingErr = currentHerdrNudgeBinding(store, recorded)
		if bindingErr != nil {
			return bindingErr
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

func currentHerdrNudgeBinding(store state.Store, recorded state.Pane) (state.Pane, error) {
	latest, matches := uniqueNudgeRecipient(store, recorded.Parent, recorded.IssueNum, recorded.TaskID)
	byRow, rowFound := uniqueHerdrNudgeRow(store, recorded.EmitterRowKey)
	if matches != 1 || !rowFound || !sameHerdrNudgeBinding(latest, byRow) ||
		!sameHerdrNudgeBinding(recorded, latest) || !validHerdrNudgeGeneration(latest) {
		return state.Pane{}, fmt.Errorf("recipient launch binding changed before prompt")
	}
	return latest, nil
}

func validHerdrNudgeGeneration(pane state.Pane) bool {
	return telemetry.ValidNonce(pane.LaunchNonce) && telemetry.ValidNonce(pane.EmitterNonce) &&
		pane.HerdrAgentID == naming.HerdrAgentName(pane.HerdrRepoKey, pane.EmitterRowKey, pane.LaunchNonce)
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
	return left.RuntimeBinding().Equal(right.RuntimeBinding())
}

// herdrNudgeTarget builds the runtime prompt target out of the same recorded
// binding every preflight gate compared, so the prompt cannot be addressed to
// an identity the gates never saw.
func herdrNudgeTarget(pane state.Pane) backend.NudgeTarget {
	binding := pane.RuntimeBinding()
	return backend.NudgeTarget{
		Ref: binding.Ref, SessionID: binding.SessionID, SocketPath: binding.SocketPath,
		TerminalID: binding.TerminalID, AgentID: binding.AgentID,
		AgentSession: binding.AgentSession,
	}
}
