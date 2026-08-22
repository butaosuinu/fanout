package backend

// Identity of one fanout-owned pane and the no-wait nudge bound to it, plus
// the owned-session sentinels callers match with errors.Is. The admission,
// probe, and socket work that produce these values live in infra.

import (
	"context"
	"errors"
)

var (
	ErrOwnedIdentityMismatch = errors.New("herdr owned pane identity mismatch")
	ErrOwnedCheckoutRetained = errors.New("herdr owned checkout retained for manual reconciliation")
)

// ErrOwnedSessionNotFound reports that no persisted owned-session admission
// exists for the requested repository identity.
var ErrOwnedSessionNotFound = errors.New("fanout-owned herdr session does not exist")

// ErrOwnedGenerationStillLive reports that the exact saved server generation
// is still live, so an explicit restart has not issued an external mutation.
var ErrOwnedGenerationStillLive = errors.New("herdr owned server generation is still live")

type OwnedPaneIdentity struct {
	Ref            PaneRef
	SessionID      string
	SocketPath     string
	WorkspaceLabel string
	TerminalID     string
	RepoKey        string
	WorktreePath   string
	CurrentPath    string
	// Agent is the recorded provider. It pins which conversations may be
	// admitted for this pane, so it is set wherever AgentID is.
	Agent        string
	AgentID      string
	AgentSession *AgentSessionRef
}

// NudgeTarget binds one no-wait agent prompt to the route, pane, terminal,
// agent, and provider session that the caller revalidated.
type NudgeTarget struct {
	Ref          PaneRef
	SessionID    string
	SocketPath   string
	TerminalID   string
	Agent        string
	AgentID      string
	AgentSession *AgentSessionRef
}

// NudgePrompt is one fully preflighted no-wait agent prompt. Callers issue it
// at most once after their final cooperative-state gate.
type NudgePrompt func(context.Context) error
