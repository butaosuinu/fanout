package herdrrun

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strings"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

func validateStatus(status statusJSON, requested route, admitted binaryAdmission) (route, error) {
	if validateAdmittedVersion(status.Client.Version) != nil || status.Client.Version != admitted.version || status.Client.Channel != "stable" {
		return route{}, fmt.Errorf(
			"unsupported herdr client version=%q channel=%q (required: version=%s channel=stable)",
			status.Client.Version,
			status.Client.Channel,
			admitted.version,
		)
	}
	if status.Client.Session == nil || *status.Client.Session != requested.session {
		return route{}, fmt.Errorf("herdr client session is %q, want %q", optionalString(status.Client.Session), requested.session)
	}
	if status.Server.Status != "running" || !status.Server.Running {
		return route{}, fmt.Errorf("herdr named session %q is not running", requested.session)
	}
	if status.Server.Version == nil || validateAdmittedVersion(optionalString(status.Server.Version)) != nil || *status.Server.Version != admitted.version {
		return route{}, fmt.Errorf(
			"unsupported herdr server version=%q (required: version=%s)",
			optionalString(status.Server.Version),
			admitted.version,
		)
	}
	if status.Server.Session == nil || *status.Server.Session != requested.session {
		return route{}, fmt.Errorf("herdr server session is %q, want %q", optionalString(status.Server.Session), requested.session)
	}
	if status.Server.RestartNeeded == nil || *status.Server.RestartNeeded ||
		status.Update.RestartNeeded == nil || *status.Update.RestartNeeded {
		return route{}, fmt.Errorf("herdr session %q requires a client/server restart", requested.session)
	}
	if strings.TrimSpace(status.Server.Socket) == "" || !filepath.IsAbs(status.Server.Socket) {
		return route{}, fmt.Errorf("herdr status returned an invalid socket path %q", status.Server.Socket)
	}
	if requested.socketPath != "" && status.Server.Socket != requested.socketPath {
		return route{}, fmt.Errorf("herdr status socket is %q, want %q", status.Server.Socket, requested.socketPath)
	}
	return route{session: requested.session, socketPath: status.Server.Socket}, nil
}

type agentSessionKey struct {
	source string
	agent  string
	kind   string
	value  string
}

func projectSnapshot(envelope snapshotEnvelope, probed probeResult) ([]corebackend.LivePane, error) {
	if envelope.ID != "cli:api:snapshot" || envelope.Result == nil || envelope.Result.Type != "session_snapshot" {
		return nil, fmt.Errorf("unexpected herdr snapshot envelope")
	}
	snapshot := envelope.Result.Snapshot
	if snapshot.Version != probed.version {
		return nil, fmt.Errorf(
			"unsupported herdr snapshot version=%q (required: version=%s)",
			snapshot.Version,
			probed.version,
		)
	}
	if snapshot.Workspaces == nil || snapshot.Tabs == nil || snapshot.Panes == nil || snapshot.Layouts == nil || snapshot.Agents == nil {
		return nil, fmt.Errorf("herdr snapshot is missing a required collection")
	}

	workspaces := make(map[string]workspaceJSON, len(*snapshot.Workspaces))
	for _, workspace := range *snapshot.Workspaces {
		if strings.TrimSpace(workspace.WorkspaceID) == "" || strings.TrimSpace(workspace.Label) == "" || workspace.Focused == nil {
			return nil, fmt.Errorf("herdr snapshot contains an empty workspace id")
		}
		if workspace.Worktree != nil &&
			(strings.TrimSpace(workspace.Worktree.RepoKey) == "" ||
				strings.TrimSpace(workspace.Worktree.CheckoutPath) == "" ||
				strings.TrimSpace(workspace.Worktree.RepoRoot) == "") {
			return nil, fmt.Errorf("herdr workspace %q has incomplete worktree provenance", workspace.WorkspaceID)
		}
		if _, duplicate := workspaces[workspace.WorkspaceID]; duplicate {
			return nil, fmt.Errorf("herdr snapshot contains duplicate workspace id %q", workspace.WorkspaceID)
		}
		workspaces[workspace.WorkspaceID] = workspace
	}

	panesByID := make(map[string]paneJSON, len(*snapshot.Panes))
	terminalIDs := make(map[string]string, len(*snapshot.Panes))
	sessionRefPanes := make(map[agentSessionKey][]string, len(*snapshot.Panes))
	sessionRefsByPane := make(map[string]agentSessionKey, len(*snapshot.Panes))
	for _, pane := range *snapshot.Panes {
		if strings.TrimSpace(pane.PaneID) == "" || strings.TrimSpace(pane.TerminalID) == "" || strings.TrimSpace(pane.WorkspaceID) == "" || strings.TrimSpace(pane.TabID) == "" || pane.Focused == nil || pane.Revision == nil {
			return nil, fmt.Errorf("herdr snapshot contains a pane with incomplete identity")
		}
		if _, duplicate := panesByID[pane.PaneID]; duplicate {
			return nil, fmt.Errorf("herdr snapshot contains duplicate pane id %q", pane.PaneID)
		}
		if previous, duplicate := terminalIDs[pane.TerminalID]; duplicate {
			return nil, fmt.Errorf("herdr snapshot reuses terminal id %q for panes %q and %q", pane.TerminalID, previous, pane.PaneID)
		}
		if _, ok := workspaces[pane.WorkspaceID]; !ok {
			return nil, fmt.Errorf("herdr pane %q references unknown workspace %q", pane.PaneID, pane.WorkspaceID)
		}
		if !validNativeAgentState(pane.AgentStatus) {
			return nil, fmt.Errorf("herdr pane %q has unknown agent status %q", pane.PaneID, pane.AgentStatus)
		}
		ref, present, err := parseAgentSession(pane.AgentSession)
		if err != nil {
			return nil, fmt.Errorf("herdr pane %q: %w", pane.PaneID, err)
		}
		if present {
			sessionRefPanes[ref] = append(sessionRefPanes[ref], pane.PaneID)
			sessionRefsByPane[pane.PaneID] = ref
		}
		panesByID[pane.PaneID] = pane
		terminalIDs[pane.TerminalID] = pane.PaneID
	}

	agentsByPane := make(map[string]agentJSON, len(*snapshot.Agents))
	for _, agent := range *snapshot.Agents {
		pane, ok := panesByID[agent.PaneID]
		if !ok {
			return nil, fmt.Errorf("herdr agent references unknown pane %q", agent.PaneID)
		}
		if _, duplicate := agentsByPane[agent.PaneID]; duplicate {
			return nil, fmt.Errorf("herdr snapshot contains duplicate agent records for pane %q", agent.PaneID)
		}
		if agent.Focused == nil || agent.Revision == nil {
			return nil, fmt.Errorf("herdr agent for pane %q has incomplete identity", agent.PaneID)
		}
		if agent.TerminalID != pane.TerminalID || agent.WorkspaceID != pane.WorkspaceID || agent.TabID != pane.TabID || agent.AgentStatus != pane.AgentStatus || *agent.Focused != *pane.Focused || *agent.Revision != *pane.Revision {
			return nil, fmt.Errorf("herdr agent identity disagrees with pane %q", agent.PaneID)
		}
		agentRef, agentRefPresent, err := parseAgentSession(agent.AgentSession)
		if err != nil {
			return nil, fmt.Errorf("herdr agent for pane %q: %w", agent.PaneID, err)
		}
		paneRef, paneRefPresent := sessionRefsByPane[agent.PaneID]
		if agentRefPresent != paneRefPresent || (agentRefPresent && agentRef != paneRef) {
			return nil, fmt.Errorf("herdr agent session ref disagrees with pane %q", agent.PaneID)
		}
		agentsByPane[agent.PaneID] = agent
	}
	if duplicate := duplicateLiveAgentSession(sessionRefPanes, agentsByPane); duplicate != "" {
		return nil, fmt.Errorf("herdr live agent reports duplicate agent session ref on pane %q", duplicate)
	}
	live := make([]corebackend.LivePane, 0, len(*snapshot.Panes))
	for _, pane := range *snapshot.Panes {
		workspace := workspaces[pane.WorkspaceID]
		currentPath := optionalString(pane.CWD)
		projectRoot := ""
		worktreePath := ""
		repoKey := ""
		if workspace.Worktree != nil {
			currentPath = workspace.Worktree.CheckoutPath
			repoKey = workspace.Worktree.RepoKey
			projectRoot = workspace.Worktree.RepoRoot
			worktreePath = workspace.Worktree.CheckoutPath
		}
		agent, agentPresent := agentsByPane[pane.PaneID]
		agentID, agentProvider, agentNamed := projectAgentIdentity(agent, agentPresent)
		var agentSession *corebackend.AgentSessionRef
		if ref, present := sessionRefsByPane[pane.PaneID]; present {
			agentSession = &corebackend.AgentSessionRef{
				Source: ref.source,
				Agent:  ref.agent,
				Kind:   ref.kind,
				Value:  ref.value,
			}
		}
		live = append(live, corebackend.LivePane{
			Ref: corebackend.PaneRef{
				Backend:   corebackend.Herdr,
				Workspace: pane.WorkspaceID,
				Pane:      pane.PaneID,
			},
			CurrentPath:      currentPath,
			Title:            optionalString(pane.Title),
			FocusKnown:       true,
			Focused:          *pane.Focused,
			AgentState:       corebackend.MapReportedAgentState(agentPresent, pane.AgentStatus, ""),
			NativeAgentState: pane.AgentStatus,
			WorkspaceLabel:   workspace.Label,
			TerminalID:       pane.TerminalID,
			AgentID:          agentID,
			AgentNamed:       agentNamed,
			AgentProvider:    agentProvider,
			AgentSession:     agentSession,
			AgentPresent:     agentPresent,
			RepoKey:          repoKey,
			ProjectRoot:      projectRoot,
			WorktreePath:     worktreePath,
			SessionID:        probed.route.session,
			SocketPath:       probed.route.socketPath,
		})
	}
	return live, nil
}

func duplicateLiveAgentSession(
	refs map[agentSessionKey][]string,
	agents map[string]agentJSON,
) string {
	for _, paneIDs := range refs {
		if len(paneIDs) < 2 {
			continue
		}
		for _, paneID := range paneIDs {
			if _, present := agents[paneID]; present {
				return paneID
			}
		}
	}
	return ""
}

// projectAgentIdentity reports the agent record's identity: the name the
// runtime holds for it, the provider, and whether that name is the record's own.
//
// AgentID falls back to the provider for an unnamed record because that is how
// fanout finds the agent it has just launched, before it renames it. The flag
// keeps that fallback from reading as a real name later, when a provider
// restarting its conversation in place makes the runtime drop the name again.
func projectAgentIdentity(agent agentJSON, present bool) (string, string, bool) {
	if !present {
		return "", "", false
	}
	name := optionalString(agent.Name)
	provider := optionalString(agent.Agent)
	return cmp.Or(name, provider), provider, name != ""
}

func parseAgentSession(ref *agentSessionJSON) (agentSessionKey, bool, error) {
	if ref == nil {
		return agentSessionKey{}, false, nil
	}
	if ref.Source == nil || ref.Agent == nil || ref.Kind == nil || ref.Value == nil ||
		strings.TrimSpace(*ref.Source) == "" || strings.TrimSpace(*ref.Agent) == "" || strings.TrimSpace(*ref.Value) == "" {
		return agentSessionKey{}, false, fmt.Errorf("agent session ref is incomplete")
	}
	if *ref.Kind != "id" && *ref.Kind != "path" {
		return agentSessionKey{}, false, fmt.Errorf("agent session ref has unknown kind %q", *ref.Kind)
	}
	return agentSessionKey{
		source: *ref.Source,
		agent:  *ref.Agent,
		kind:   *ref.Kind,
		value:  *ref.Value,
	}, true, nil
}

func validNativeAgentState(raw string) bool {
	switch raw {
	case "working", "blocked", "idle", "done", "unknown":
		return true
	default:
		return false
	}
}
