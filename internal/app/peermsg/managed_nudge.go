package peermsg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/agentprocess"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const managedNudgeTimeout = 30 * time.Second

// managedNudgeRow reports whether the recipient's hint must be prepared by its
// runtime instead of typed into a terminal. The row's recorded runtime name is
// deliberately the criterion. The recorded session route (session id plus
// socket path) looks like a data-shape equivalent, but nothing validates
// state.json on load, and this lane's identity gates compare a binding without
// its runtime name — uniqueNudgePane calls UniqueLive with no RequireRuntime
// option. Selecting the lane from route fields alone would let a row that
// records the direct-send runtime, yet carries session fields, address a live
// managed pane; the recorded name keeps the lane pinned to what the launch
// actually recorded.
func managedNudgeRow(pane state.Pane) bool {
	return backend.NormalizeName(pane.Backend) == backend.Herdr
}

func deliverManagedNudge(pane state.Pane, deps Deps) (agentState, reason string, nudged bool) {
	observedState, reason, candidate := managedNudgeCandidate(pane)
	if !candidate {
		return observedState, reason, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), managedNudgeTimeout)
	defer cancel()
	prompt, _, latestState, err := prepareManagedNudge(ctx, pane, deps)
	if err != nil {
		return latestState, err.Error(), false
	}
	if err := prompt(ctx); err != nil {
		return latestState, fmt.Sprintf("agent prompt failed: %v", err), false
	}
	return latestState, "", true
}

func managedNudgeCandidate(pane state.Pane) (string, string, bool) {
	observedState := strings.TrimSpace(pane.ReportedState)
	if !pane.StateRefinement {
		return observedState, "agent state is not refined for the current launch", false
	}
	if !shouldNudge(observedState) {
		return observedState, fmt.Sprintf("agent is not nudgeable (state %q)", observedState), false
	}
	return observedState, "", true
}

func prepareManagedNudge(ctx context.Context, pane state.Pane, deps Deps) (backend.NudgePrompt, state.Pane, string, error) {
	if deps.ReadLockedState == nil {
		return nil, state.Pane{}, "", fmt.Errorf("herdr nudge runtime is unavailable")
	}
	latest, latestState, err := recheckManagedNudgeState(ctx, pane, deps)
	if err != nil {
		return nil, latest, latestState, err
	}
	prompt, err := prepareRuntimePrompt(ctx, latest, deps.OpenRuntime)
	if err != nil {
		return nil, latest, latestState, err
	}
	final, finalState, err := recheckManagedNudgeState(ctx, latest, deps)
	if err != nil {
		return nil, final, finalState, err
	}
	return prompt, final, finalState, nil
}

func prepareRuntimePrompt(ctx context.Context, pane state.Pane, open func(context.Context, string) (NudgeRuntime, error)) (backend.NudgePrompt, error) {
	runtime, err := openNudgeRuntime(ctx, open, pane.RepoKey)
	if err != nil {
		return nil, err
	}
	err = verifyNudgeRuntime(ctx, runtime, pane)
	if err != nil {
		return nil, err
	}
	prompt, err := runtime.PrepareNudge(ctx, managedNudgeTarget(pane), nudgeText)
	if err != nil {
		return nil, fmt.Errorf("prepare agent prompt: %w", err)
	}
	if prompt == nil {
		return nil, fmt.Errorf("prepare agent prompt: runtime returned no prompt")
	}
	return prompt, nil
}

func openNudgeRuntime(ctx context.Context, open func(context.Context, string) (NudgeRuntime, error), repoKey string) (NudgeRuntime, error) {
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

func verifyNudgeRuntime(ctx context.Context, runtime NudgeRuntime, pane state.Pane) error {
	panes, err := runtime.LivePanes(ctx)
	if err != nil {
		return fmt.Errorf("read herdr panes: %w", err)
	}
	if !uniqueNudgePane(pane, panes) {
		return fmt.Errorf("recipient pane identity or worktree provenance changed")
	}
	return verifyNudgeProcess(ctx, runtime, pane)
}

func verifyNudgeProcess(ctx context.Context, runtime NudgeRuntime, pane state.Pane) error {
	process, err := runtime.ProcessInfo(ctx, pane.PaneID)
	if err != nil {
		return fmt.Errorf("read recipient process identity: %w", err)
	}
	if process.PaneID != pane.PaneID {
		return fmt.Errorf("recipient process identity belongs to another pane")
	}
	err = agentprocess.VerifyAgent(process, agentprocess.Identity{
		WorktreePath: pane.WorktreePath,
		Executable:   pane.LaunchExecutable,
		Args:         pane.LaunchArgs,
		Agent:        pane.Agent,
	})
	if err != nil {
		return fmt.Errorf("verify recipient process identity: %w", err)
	}
	return nil
}

func uniqueNudgePane(recorded state.Pane, panes []backend.LivePane) bool {
	_, ok := recorded.RuntimeBinding().UniqueLive(panes)
	return ok
}

func recheckManagedNudgeState(ctx context.Context, recorded state.Pane, deps Deps) (state.Pane, string, error) {
	var latest state.Pane
	err := deps.ReadLockedState(ctx, func(store state.Store) error {
		var bindingErr error
		latest, bindingErr = currentNudgeBinding(store, recorded)
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

func currentNudgeBinding(store state.Store, recorded state.Pane) (state.Pane, error) {
	latest, matches := uniqueNudgeRecipient(store, recorded.Parent, recorded.IssueNum, recorded.TaskID)
	row, rowErr := store.EmitterRowIndex(
		recorded.EmitterRowKey, recorded.WorktreePath, recorded.WorkspaceLabel,
	)
	if matches != 1 || rowErr != nil || row < 0 {
		return state.Pane{}, fmt.Errorf("recipient launch binding changed before prompt")
	}
	byRow := store.Panes[row]
	if !sameNudgeBinding(latest, byRow) ||
		!sameNudgeBinding(recorded, latest) || !validNudgeGeneration(latest) {
		return state.Pane{}, fmt.Errorf("recipient launch binding changed before prompt")
	}
	return latest, nil
}

func validNudgeGeneration(pane state.Pane) bool {
	if pane.Agent == "claude" &&
		(!telemetry.SequencedClaudeLaunch(pane.Agent, pane.LaunchArgs) ||
			telemetry.ClaudeSequenceWatermarkMissing(
				pane.Agent, pane.LaunchArgs, pane.StateRefinement, pane.ReportedStateSeq,
			)) {
		return false
	}
	return telemetry.ValidNonce(pane.LaunchNonce) && telemetry.ValidNonce(pane.EmitterNonce) &&
		pane.AgentID == naming.ManagedAgentName(pane.RepoKey, pane.EmitterRowKey, pane.LaunchNonce)
}

func sameNudgeBinding(left, right state.Pane) bool {
	return left.RuntimeBinding().Equal(right.RuntimeBinding())
}

// managedNudgeTarget builds the runtime prompt target out of the same recorded
// binding every preflight gate compared, so the prompt cannot be addressed to
// an identity the gates never saw.
func managedNudgeTarget(pane state.Pane) backend.NudgeTarget {
	binding := pane.RuntimeBinding()
	return backend.NudgeTarget{
		Ref: binding.Ref, SessionID: binding.SessionID, SocketPath: binding.SocketPath,
		TerminalID: binding.TerminalID, Agent: binding.Agent, AgentID: binding.AgentID,
		AgentSession: binding.AgentSession,
	}
}
