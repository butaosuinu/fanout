package herdrrun

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type agentPromptEnvelope struct {
	ID     string             `json:"id"`
	Result *agentPromptResult `json:"result"`
}

type agentPromptResult struct {
	Type  string    `json:"type"`
	Agent agentJSON `json:"agent"`
}

// PrepareNudge completes the owned-route preflight before the caller's final
// cooperative-state gate. The returned function issues only agent prompt.
//
// It deliberately does not restore a dropped agent name the way the other
// mutations do. A NudgeTarget carries the route, terminal, and agent identity
// but no checkout provenance, so it cannot satisfy the full-identity match
// restoreOwnedAgentName renames on, and matching on less would make the rename
// a weaker mutation than the gate it repairs. The prompt itself is unaffected:
// every gate admits the unnamed record, and the next focus, send, or close
// puts the name back.
func (s *OwnedSession) PrepareNudge(ctx context.Context, target corebackend.NudgeTarget, line string) (corebackend.NudgePrompt, error) {
	if err := validateNudgeRequest(s, line); err != nil {
		return nil, err
	}
	admission, lock, err := s.backend.acquireOwnedMutation(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockPrivateFile(lock)
	if !validNudgeTarget(target, admission) {
		return nil, fmt.Errorf("%w: saved nudge target is incomplete or belongs to a foreign route", corebackend.ErrOwnedIdentityMismatch)
	}
	target.AgentSession = cloneAgentSession(target.AgentSession)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return nil, err
	}
	return func(promptCtx context.Context) error {
		return s.backend.runNudgePrompt(promptCtx, probed, target, line)
	}, nil
}

// Nudge preserves the direct infra entrypoint for callers that do not have a
// separate cooperative-state gate.
func (s *OwnedSession) Nudge(ctx context.Context, target corebackend.NudgeTarget, line string) error {
	prompt, err := s.PrepareNudge(ctx, target, line)
	if err != nil {
		return err
	}
	return prompt(ctx)
}

func validateNudgeRequest(session *OwnedSession, line string) error {
	if session == nil || session.backend == nil {
		return fmt.Errorf("herdr owned session is nil")
	}
	if strings.ContainsAny(line, "\x00\r\n") {
		return fmt.Errorf("herdr nudge contains a NUL, CR, or LF byte")
	}
	return nil
}

func (b *Backend) runNudgePrompt(ctx context.Context, probed probeResult, target corebackend.NudgeTarget, line string) error {
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route,
		"agent", "prompt", target.Ref.Pane, line)
	if err != nil {
		return methodUnavailable("agent.prompt")
	}
	identity := corebackend.OwnedPaneIdentity{
		Ref: target.Ref, TerminalID: target.TerminalID,
		Agent: target.Agent, AgentID: target.AgentID,
		AgentSession: cloneAgentSession(target.AgentSession),
	}
	if validateAgentPromptResponse(out, identity) != nil {
		return methodUnavailable("agent.prompt")
	}
	return nil
}

func validNudgeTarget(target corebackend.NudgeTarget, admission ownedAdmission) bool {
	checks := []bool{
		target.Ref.Backend == corebackend.Herdr,
		target.SessionID == admission.marker.Session,
		target.SocketPath == admission.marker.SocketPath,
		target.Ref.Workspace != "", target.Ref.Pane != "",
		target.TerminalID != "", target.AgentID != "",
		strings.TrimSpace(target.Agent) != "",
	}
	for _, ok := range checks {
		if !ok {
			return false
		}
	}
	// The response check that runs after the prompt is delivered pins the
	// conversation to this provider, so the preflight has to hold the target to
	// the same rule. Admitting a target it would later reject turns a delivered
	// prompt into a reported failure, and the retry sends it twice.
	return corebackend.AgentSessionAdmits(target.Agent, target.AgentSession, target.AgentSession)
}

func validateAgentPromptResponse(data []byte, target corebackend.OwnedPaneIdentity) error {
	var envelope agentPromptEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return err
	}
	if envelope.ID != "cli:agent:prompt" || envelope.Result == nil || envelope.Result.Type != "agent_prompted" {
		return fmt.Errorf("unexpected agent prompt envelope")
	}
	return validatePromptedAgentIdentity(envelope.Result.Agent, target)
}

// validatePromptedAgentIdentity checks that the agent the runtime just prompted
// is the one the caller addressed. It runs after delivery, so it admits exactly
// what the preflight did and no more.
func validatePromptedAgentIdentity(agent agentJSON, target corebackend.OwnedPaneIdentity) error {
	if agent.TerminalID != target.TerminalID || agent.WorkspaceID != target.Ref.Workspace || agent.TabID == "" ||
		agent.PaneID != target.Ref.Pane || agent.Focused == nil || agent.Revision == nil {
		return fmt.Errorf("%w: prompted agent identity changed", corebackend.ErrOwnedIdentityMismatch)
	}
	name := optionalString(agent.Name)
	provider := optionalString(agent.Agent)
	// AgentRecordMatches deliberately says nothing about the provider, so every
	// caller pairs it with its own provider check. Without one here, an
	// anonymous record would be admitted on the recorded name's shape alone,
	// and a pane whose provider changed after the preflight would report a
	// prompt delivered to a different agent as a success.
	if provider != target.Agent {
		return fmt.Errorf("%w: prompted agent provider changed", corebackend.ErrOwnedIdentityMismatch)
	}
	// This check runs after the prompt was delivered, so it admits the same
	// record the preflight did. Holding it to the name alone would report a
	// delivered prompt as a failure whenever the runtime dropped that name in
	// the gap, and the retry would send the prompt twice.
	if !corebackend.AgentRecordMatches(cmp.Or(name, provider), name != "", target.AgentID) {
		return fmt.Errorf("%w: prompted agent name changed", corebackend.ErrOwnedIdentityMismatch)
	}
	if !agentPromptSessionMatches(target.Agent, agent.AgentSession, target.AgentSession) {
		return fmt.Errorf("%w: prompted agent session changed", corebackend.ErrOwnedIdentityMismatch)
	}
	return nil
}

// agentPromptSessionMatches runs on the response to a prompt that has already
// been delivered, so it has to admit exactly what the pre-send gate admitted.
// Holding it to the byte-exact reference instead would report a prompt the
// agent received as a failure whenever the provider replaced its conversation
// in the gap, which is the one outcome a mutation must never produce.
func agentPromptSessionMatches(provider string, current *agentSessionJSON, expected *corebackend.AgentSessionRef) bool {
	ref, present, err := parseAgentSession(current)
	if err != nil {
		return false
	}
	if !present {
		return corebackend.AgentSessionAdmits(provider, expected, nil)
	}
	return corebackend.AgentSessionAdmits(provider, expected, &corebackend.AgentSessionRef{
		Source: ref.source, Agent: ref.agent, Kind: ref.kind, Value: ref.value,
	})
}
