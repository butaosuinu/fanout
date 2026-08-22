package backend

// PaneBinding is the durable identity fanout records for one pane row, plus
// the comparisons every liveness, rebinding, and nudge gate performs on it.
// Callers project it out of persistent state; matching never fills a component
// from a live observation.

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/naming"
)

// PaneRowKey is the persisted identity of the row a binding was projected
// from. Issue rows use Parent plus IssueNum; issue-less task rows use TaskID.
type PaneRowKey struct {
	Parent   string
	IssueNum int
	TaskID   string
}

// LaunchGeneration fences a binding to the single launch that produced it, so
// telemetry and prompts issued for an earlier generation cannot be replayed
// against a row that was relaunched underneath them.
type LaunchGeneration struct {
	RowKey       string
	Nonce        string
	EmitterNonce string
	Executable   string
	Args         []string
}

// Equal compares the whole generation, including the recorded launch command.
func (g LaunchGeneration) Equal(other LaunchGeneration) bool {
	return g.RowKey == other.RowKey && g.Nonce == other.Nonce &&
		g.EmitterNonce == other.EmitterNonce && g.Executable == other.Executable &&
		slices.Equal(g.Args, other.Args)
}

// PaneBinding is the durable identity of one recorded pane row: the row it was
// persisted under, the route and terminal it was launched on, the agent and
// conversation bound to it, the checkout it owns, and its launch generation.
//
// MatchesLive is the matcher for the recorded-binding evidence model
// (LiveIdentityRecordedBinding): it requires the ownership label, terminal
// record, and agent-session evidence that only such runtimes persist, so a
// foreground-path runtime's rows (LiveIdentityForegroundPath — tmux) can never
// satisfy it and match through sessionview's foreground-path arm instead. See
// LiveIdentityModel for the lane split.
type PaneBinding struct {
	// Row is the persisted row this binding was projected from.
	Row PaneRowKey

	// Ref, SessionID and SocketPath are the route the row was launched on.
	// Ref.Backend stays exactly as recorded so callers can normalize the legacy
	// empty value where they need to and compare it verbatim where they do not.
	Ref        PaneRef
	SessionID  string
	SocketPath string

	// WorkspaceLabel is the ownership label of the containing workspace and
	// TerminalID the terminal record the row was bound to.
	WorkspaceLabel string
	TerminalID     string

	// Agent is the recorded provider, AgentID its runtime agent record, and
	// AgentSession the logical conversation bound to the row. Shell marks a row
	// that must never carry any of the three.
	Agent        string
	AgentID      string
	AgentSession *AgentSessionRef
	Shell        bool

	// RepoKey and WorktreePath are the checkout provenance the row owns.
	RepoKey      string
	WorktreePath string

	// Launch fences the binding to one launch generation.
	Launch LaunchGeneration
}

// MatchOption expresses one deliberate variance in binding comparison. Every
// option exists for a call site whose recorded evidence genuinely differs.
type MatchOption func(*matchConfig)

type matchConfig struct {
	runtime                  Name
	allowUnboundAgentSession bool
}

func newMatchConfig(opts []MatchOption) matchConfig {
	var cfg matchConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// RequireRuntime rejects any pair whose runtime is not name. The recorded side
// normalizes the legacy empty value; a live observation always names its own
// runtime, so it is compared verbatim.
func RequireRuntime(name Name) MatchOption {
	return func(cfg *matchConfig) { cfg.runtime = name }
}

// AllowUnboundAgentSession admits a live conversation the binding has not
// recorded yet: the observed ref must be a valid one issued for the recorded
// provider, and the recorded ref is not consulted. It is the first-bind rule,
// where the row is complete but its conversation is still being discovered.
func AllowUnboundAgentSession() MatchOption {
	return func(cfg *matchConfig) { cfg.allowUnboundAgentSession = true }
}

// MatchesLive reports whether live is still the pane this binding was recorded
// for. Route, terminal, agent, and checkout evidence must all agree; a
// component the row never recorded is not evidence and cannot be filled in.
func (b PaneBinding) MatchesLive(live LivePane, opts ...MatchOption) bool {
	cfg := newMatchConfig(opts)
	if cfg.runtime != "" &&
		(NormalizeName(b.Ref.Backend) != cfg.runtime || live.Ref.Backend != cfg.runtime) {
		return false
	}
	return b.routeMatchesLive(live) && b.agentMatchesLive(live, cfg) && b.checkoutMatchesLive(live)
}

// UniqueLive returns the single live pane this binding matches. Zero matches
// and an ambiguous set are both reported as no match.
func (b PaneBinding) UniqueLive(panes []LivePane, opts ...MatchOption) (LivePane, bool) {
	var matched LivePane
	count := 0
	for _, live := range panes {
		if b.MatchesLive(live, opts...) {
			matched = live
			count++
		}
	}
	return matched, count == 1
}

// Equal reports whether two projections describe the same recorded row. It is
// the gate that refuses to act on a row rewritten between two reads.
//
// Equal compares Shell (derived from the row's Kind), which the matcher it
// consolidated did not. This is a deliberate fail-closed tightening: no code
// path rewrites Kind on an existing row, so the extra comparison is
// unreachable today and can only refuse, never admit, if that ever changes.
func (b PaneBinding) Equal(other PaneBinding) bool {
	same := []bool{
		b.Row == other.Row, b.Ref == other.Ref,
		b.SessionID == other.SessionID, b.SocketPath == other.SocketPath,
		b.WorkspaceLabel == other.WorkspaceLabel, b.TerminalID == other.TerminalID,
		b.Agent == other.Agent, b.AgentID == other.AgentID, b.Shell == other.Shell,
		SameAgentSession(b.AgentSession, other.AgentSession),
		b.RepoKey == other.RepoKey, b.WorktreePath == other.WorktreePath,
		b.Launch.Equal(other.Launch),
	}
	return !slices.Contains(same, false)
}

// routeMatchesLive requires the complete recorded route. The ownership label
// and the terminal record must be present on both sides: an unlabelled row or
// a terminal-less observation carries no evidence against id reuse.
func (b PaneBinding) routeMatchesLive(live LivePane) bool {
	same := []bool{
		live.Ref.Workspace == b.Ref.Workspace, live.Ref.Pane == b.Ref.Pane,
		live.SessionID == b.SessionID, live.SocketPath == b.SocketPath,
		strings.TrimSpace(b.WorkspaceLabel) != "", live.WorkspaceLabel == b.WorkspaceLabel,
		strings.TrimSpace(b.TerminalID) != "", strings.TrimSpace(live.TerminalID) != "",
		live.TerminalID == b.TerminalID,
	}
	return !slices.Contains(same, false)
}

// agentMatchesLive keeps an observed conversation from authorizing a row that
// never recorded one. Once recorded, the whole tuple stays exact.
func (b PaneBinding) agentMatchesLive(live LivePane, cfg matchConfig) bool {
	if cfg.allowUnboundAgentSession {
		return ExpectedAgentSession(live.AgentSession, b.Agent) && b.observedAgentMatches(live)
	}
	if b.Shell {
		return b.AgentID == "" && b.AgentSession == nil && !liveAgentPresent(live)
	}
	if strings.TrimSpace(b.Agent) == "" && b.AgentID == "" {
		return !liveAgentPresent(live)
	}
	same := []bool{
		liveAgentPresent(live), b.AgentID != "", b.observedAgentMatches(live),
		AgentSessionAdmits(b.Agent, b.AgentSession, live.AgentSession),
	}
	return !slices.Contains(same, false)
}

func (b PaneBinding) observedAgentMatches(live LivePane) bool {
	return live.AgentPresent && live.AgentProvider == b.Agent &&
		AgentRecordMatches(live.AgentID, live.AgentNamed, b.AgentID)
}

// AgentRecordMatches reports whether the observed agent record is the one the
// row recorded. It admits the recorded name, plus the one shape that is this
// row's record without carrying it: the runtime holds no name of its own, and
// the row recorded a name fanout minted.
//
// A provider that restarts its conversation in place makes the runtime
// re-register the agent anonymously, so reading that as a different agent would
// strand the row — stale in the display, refused by every gate, and with its
// telemetry invalidated — for the rest of the pane's life. An anonymous record
// is not another agent: the route, terminal, checkout, and provider around it
// are still compared exactly, and only the launch that owns the pane runs in it.
// fanout re-asserts the name the next time it mutates the pane.
//
// Every gate that compares an agent record goes through here, so the display,
// the owned operations, telemetry, and the prompt response all admit exactly
// the same set.
func AgentRecordMatches(observedAgentID string, observedNamed bool, recordedAgentID string) bool {
	if observedAgentID == recordedAgentID {
		return true
	}
	return !observedNamed && naming.IsManagedAgentName(recordedAgentID)
}

// checkoutMatchesLive keeps worktree provenance separate from the fallback
// used by a workspace that has none. Foreground cwd is never evidence.
func (b PaneBinding) checkoutMatchesLive(live LivePane) bool {
	recorded := strings.TrimSpace(b.WorktreePath)
	if recorded == "" {
		return false
	}
	recorded = filepath.Clean(recorded)
	repoKey := strings.TrimSpace(b.RepoKey)
	if liveHasCheckoutProvenance(live) {
		return exactCheckoutProvenance(repoKey, recorded, live)
	}

	// A workspace outside any checkout has no provenance to compare. Only the
	// saved cwd may support the match; subdirectories are not accepted.
	currentPath := strings.TrimSpace(live.CurrentPath)
	return repoKey == "" && currentPath != "" && filepath.Clean(currentPath) == recorded
}

func liveHasCheckoutProvenance(live LivePane) bool {
	return strings.TrimSpace(live.RepoKey) != "" || strings.TrimSpace(live.WorktreePath) != "" ||
		strings.TrimSpace(live.ProjectRoot) != ""
}

// exactCheckoutProvenance rejects partial provenance the way the runtime
// adapter does, so an alternative collector cannot widen the boundary.
func exactCheckoutProvenance(repoKey, recorded string, live LivePane) bool {
	worktreePath := strings.TrimSpace(live.WorktreePath)
	same := []bool{
		repoKey != "", strings.TrimSpace(live.RepoKey) == repoKey,
		worktreePath != "", strings.TrimSpace(live.ProjectRoot) != "",
		filepath.Clean(worktreePath) == recorded,
	}
	return !slices.Contains(same, false)
}

// liveAgentPresent reports whether an observation carries any agent evidence
// at all. A row without a recorded agent must observe none of it.
func liveAgentPresent(live LivePane) bool {
	return live.AgentPresent || live.AgentID != "" || live.AgentProvider != "" ||
		live.AgentSession != nil
}

// ExpectedAgentSession reports whether ref is a valid conversation reference
// the runtime issued for provider. The source check is pinned to the herdr
// runtime's frozen wire value ("herdr:<provider>") because it is the only
// runtime that records agent sessions; a runtime that starts recording them
// must widen this check alongside its persistence format.
func ExpectedAgentSession(ref *AgentSessionRef, provider string) bool {
	return ref != nil && ref.Valid() && ref.Agent == provider &&
		ref.Source == AgentSessionSource(provider)
}

// SameAgentSession compares two optional conversation references by value.
func SameAgentSession(left, right *AgentSessionRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// AgentSessionAdmits reports whether a row recording `recorded` for `provider`
// may still act on a pane now reporting `live`.
//
// The conversation is evidence about a pane, not the fence on it. A provider
// replaces its own conversation id while the pane, terminal, agent record, and
// launch stay put — Claude's /clear and Codex's /new both do — so freezing the
// first observed id would lock the row out of focus, peek, close, and cleanup
// for the life of that pane, with no way back. What fences these rows is
// everything around the conversation: route, terminal, checkout, and the
// per-launch AgentID, none of which another launch reproduces.
//
// Both sides are pinned to provider by name rather than checked for internal
// consistency, so a row cannot be acted on through a reference issued for a
// different provider — including the window before it has bound one, where
// there is no recorded reference to compare against. A row with no provider
// admits no conversation at all, which is what keeps a shell pane free of one.
func AgentSessionAdmits(provider string, recorded, live *AgentSessionRef) bool {
	if recorded == nil {
		return live == nil || ExpectedAgentSession(live, provider)
	}
	return ExpectedAgentSession(recorded, provider) && ExpectedAgentSession(live, provider)
}
