// Package herdrrun implements fanout's read-only herdr runtime backend.
package herdrrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

const (
	commandName       = "herdr"
	supportedVersion  = "0.7.3"
	supportedProtocol = 16
	supportedSchema   = 1
	commandTimeout    = 5 * time.Second

	sessionEnv = "HERDR_SESSION"
	socketEnv  = "HERDR_SOCKET_PATH"
)

var _ corebackend.Backend = (*Backend)(nil)

// Backend observes one already-running named herdr session. Herdr v1 is
// deliberately read-only: every targeted read and mutation method returns an
// unsupported-operation error without invoking the CLI.
type Backend struct {
	session    string
	socketPath string
	probeMu    sync.Mutex
	lookPath   func(string) (string, error)
	output     commandOutput
}

type commandOutput func(binary string, env []string, args ...string) ([]byte, error)

type route struct {
	session    string
	socketPath string
}

type probeResult struct {
	binary string
	route  route
}

// New constructs a herdr backend for one named session. socketPath may be
// empty on the first probe; CheckAvailable resolves it through an explicit
// --session status call, then pins subsequent probes to the returned path.
func New(session, socketPath string) *Backend {
	return &Backend{
		session:    strings.TrimSpace(session),
		socketPath: socketPath,
		lookPath:   exec.LookPath,
		output:     runCommand,
	}
}

func (b *Backend) Name() corebackend.Name { return corebackend.Herdr }

// CheckAvailable verifies the exact CLI/server/schema tuple accepted by the
// v1 backend. It never starts or attaches a herdr server.
func (b *Backend) CheckAvailable() error {
	_, err := b.probe()
	return err
}

// ListLive returns the aggregate session.snapshot projection. The probe is
// repeated for each call so a client/server upgrade cannot silently widen the
// exact v1 compatibility allowlist.
func (b *Backend) ListLive() ([]corebackend.LivePane, error) {
	probed, err := b.probe()
	if err != nil {
		return nil, err
	}
	out, err := b.run(probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		return nil, fmt.Errorf("herdr api snapshot: %w", err)
	}
	var envelope snapshotEnvelope
	if err := decodeOne(out, &envelope); err != nil {
		return nil, fmt.Errorf("parse herdr api snapshot: %w", err)
	}
	return projectSnapshot(envelope, probed.route)
}

func (b *Backend) Launch(corebackend.LaunchRequest) (corebackend.PaneRef, error) {
	return corebackend.PaneRef{}, corebackend.Unsupported(corebackend.Herdr, "launch")
}

func (b *Backend) ReleaseStartGate(string) error {
	return corebackend.Unsupported(corebackend.Herdr, "release start gate")
}

func (b *Backend) Read(corebackend.PaneRef, int) (string, error) {
	return "", corebackend.Unsupported(corebackend.Herdr, "read")
}

func (b *Backend) SendLine(corebackend.PaneRef, string) error {
	return corebackend.Unsupported(corebackend.Herdr, "send line")
}

func (b *Backend) Focus(corebackend.PaneRef) error {
	return corebackend.Unsupported(corebackend.Herdr, "focus")
}

func (b *Backend) Close(corebackend.PaneRef) error {
	return corebackend.Unsupported(corebackend.Herdr, "close")
}

func (b *Backend) probe() (probeResult, error) {
	b.probeMu.Lock()
	defer b.probeMu.Unlock()

	if err := validateSessionName(b.session); err != nil {
		return probeResult{}, err
	}
	binary, err := b.lookPath(commandName)
	if err != nil {
		return probeResult{}, fmt.Errorf("herdr 0.7.3 is required: %w", err)
	}
	if !filepath.IsAbs(binary) {
		binary, err = filepath.Abs(binary)
		if err != nil {
			return probeResult{}, fmt.Errorf("resolve herdr executable: %w", err)
		}
	}

	initial := route{session: b.session, socketPath: b.socketPath}
	versionOut, err := b.run(binary, initial, "--version")
	if err != nil {
		return probeResult{}, fmt.Errorf("herdr --version: %w", err)
	}
	if got := strings.TrimSpace(string(versionOut)); got != "herdr "+supportedVersion {
		return probeResult{}, fmt.Errorf("unsupported herdr CLI version %q (required: %s)", got, supportedVersion)
	}

	statusArgs := []string{"status", "--json"}
	// In herdr 0.7.3 an explicit --session intentionally wins over
	// HERDR_SOCKET_PATH. Use it only to resolve the initial named-session socket;
	// an already verified socket is selected through the environment instead.
	if initial.socketPath == "" {
		statusArgs = append([]string{"--session", initial.session}, statusArgs...)
	}
	statusOut, err := b.run(binary, initial, statusArgs...)
	if err != nil {
		return probeResult{}, fmt.Errorf("herdr status --json: %w", err)
	}
	var status statusJSON
	if decodeErr := decodeOne(statusOut, &status); decodeErr != nil {
		return probeResult{}, fmt.Errorf("parse herdr status --json: %w", decodeErr)
	}
	verified, err := validateStatus(status, initial)
	if err != nil {
		return probeResult{}, err
	}

	schemaOut, err := b.run(binary, verified, "api", "schema", "--json")
	if err != nil {
		return probeResult{}, fmt.Errorf("herdr api schema --json: %w", err)
	}
	var schema schemaJSON
	if err := decodeOne(schemaOut, &schema); err != nil {
		return probeResult{}, fmt.Errorf("parse herdr api schema --json: %w", err)
	}
	if schema.Protocol != supportedProtocol || schema.SchemaVersion != supportedSchema {
		return probeResult{}, fmt.Errorf(
			"unsupported herdr API tuple protocol=%d schema_version=%d (required: protocol=%d schema_version=%d)",
			schema.Protocol,
			schema.SchemaVersion,
			supportedProtocol,
			supportedSchema,
		)
	}
	b.socketPath = verified.socketPath
	return probeResult{binary: binary, route: verified}, nil
}

func (b *Backend) run(binary string, target route, args ...string) ([]byte, error) {
	return b.output(binary, routeEnvironment(target), args...)
}

func routeEnvironment(target route) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == sessionEnv || key == socketEnv {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, sessionEnv+"="+target.session)
	if target.socketPath != "" {
		env = append(env, socketEnv+"="+target.socketPath)
	}
	return env
}

func runCommand(binary string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, context.DeadlineExceeded
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			return out, fmt.Errorf("%w: %s", err, stderr)
		}
	}
	return out, err
}

func validateSessionName(session string) error {
	if session == "" || session == "default" {
		return fmt.Errorf("herdr backend requires a non-default named session")
	}
	if len(session) > 64 || session == "." || session == ".." {
		return fmt.Errorf("invalid herdr session name %q", session)
	}
	for _, ch := range []byte(session) {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return fmt.Errorf("invalid herdr session name %q", session)
	}
	return nil
}

type statusJSON struct {
	Client struct {
		Version  string  `json:"version"`
		Channel  string  `json:"channel"`
		Protocol int     `json:"protocol"`
		Session  *string `json:"session"`
	} `json:"client"`
	Server struct {
		Status        string  `json:"status"`
		Running       bool    `json:"running"`
		Version       *string `json:"version"`
		Protocol      *int    `json:"protocol"`
		Compatible    *bool   `json:"compatible"`
		Socket        string  `json:"socket"`
		Session       *string `json:"session"`
		RestartNeeded *bool   `json:"restart_needed"`
	} `json:"server"`
	Update struct {
		RestartNeeded *bool `json:"restart_needed"`
	} `json:"update"`
}

func validateStatus(status statusJSON, requested route) (route, error) {
	if status.Client.Version != supportedVersion || status.Client.Channel != "stable" || status.Client.Protocol != supportedProtocol {
		return route{}, fmt.Errorf(
			"unsupported herdr client tuple version=%q channel=%q protocol=%d (required: version=%s channel=stable protocol=%d)",
			status.Client.Version,
			status.Client.Channel,
			status.Client.Protocol,
			supportedVersion,
			supportedProtocol,
		)
	}
	if status.Client.Session == nil || *status.Client.Session != requested.session {
		return route{}, fmt.Errorf("herdr client session is %q, want %q", optionalString(status.Client.Session), requested.session)
	}
	if status.Server.Status != "running" || !status.Server.Running {
		return route{}, fmt.Errorf("herdr named session %q is not running", requested.session)
	}
	if status.Server.Version == nil || *status.Server.Version != supportedVersion ||
		status.Server.Protocol == nil || *status.Server.Protocol != supportedProtocol ||
		status.Server.Compatible == nil || !*status.Server.Compatible {
		return route{}, fmt.Errorf(
			"unsupported herdr server tuple version=%q protocol=%s compatible=%s (required: version=%s protocol=%d compatible=true)",
			optionalString(status.Server.Version),
			optionalInt(status.Server.Protocol),
			optionalBool(status.Server.Compatible),
			supportedVersion,
			supportedProtocol,
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

type schemaJSON struct {
	Protocol      int `json:"protocol"`
	SchemaVersion int `json:"schema_version"`
}

type snapshotEnvelope struct {
	ID     string          `json:"id"`
	Result *snapshotResult `json:"result"`
}

type snapshotResult struct {
	Type     string       `json:"type"`
	Snapshot snapshotJSON `json:"snapshot"`
}

type snapshotJSON struct {
	Version    string             `json:"version"`
	Protocol   int                `json:"protocol"`
	Workspaces *[]workspaceJSON   `json:"workspaces"`
	Tabs       *[]json.RawMessage `json:"tabs"`
	Panes      *[]paneJSON        `json:"panes"`
	Layouts    *[]json.RawMessage `json:"layouts"`
	Agents     *[]agentJSON       `json:"agents"`
}

type workspaceJSON struct {
	WorkspaceID string            `json:"workspace_id"`
	Worktree    *worktreeInfoJSON `json:"worktree"`
}

type worktreeInfoJSON struct {
	CheckoutPath string `json:"checkout_path"`
	RepoRoot     string `json:"repo_root"`
}

type paneJSON struct {
	PaneID       string            `json:"pane_id"`
	TerminalID   string            `json:"terminal_id"`
	WorkspaceID  string            `json:"workspace_id"`
	TabID        string            `json:"tab_id"`
	CWD          *string           `json:"cwd"`
	Title        *string           `json:"title"`
	Focused      *bool             `json:"focused"`
	AgentStatus  string            `json:"agent_status"`
	AgentSession *agentSessionJSON `json:"agent_session"`
	Revision     *uint64           `json:"revision"`
}

type agentJSON struct {
	TerminalID   string            `json:"terminal_id"`
	Name         *string           `json:"name"`
	Agent        *string           `json:"agent"`
	AgentStatus  string            `json:"agent_status"`
	WorkspaceID  string            `json:"workspace_id"`
	TabID        string            `json:"tab_id"`
	PaneID       string            `json:"pane_id"`
	Focused      *bool             `json:"focused"`
	AgentSession *agentSessionJSON `json:"agent_session"`
	Revision     *uint64           `json:"revision"`
}

type agentSessionJSON struct {
	Source *string `json:"source"`
	Agent  *string `json:"agent"`
	Kind   *string `json:"kind"`
	Value  *string `json:"value"`
}

type agentSessionKey struct {
	source string
	agent  string
	kind   string
	value  string
}

func projectSnapshot(envelope snapshotEnvelope, target route) ([]corebackend.LivePane, error) {
	if envelope.ID != "cli:api:snapshot" || envelope.Result == nil || envelope.Result.Type != "session_snapshot" {
		return nil, fmt.Errorf("unexpected herdr snapshot envelope")
	}
	snapshot := envelope.Result.Snapshot
	if snapshot.Version != supportedVersion || snapshot.Protocol != supportedProtocol {
		return nil, fmt.Errorf(
			"unsupported herdr snapshot tuple version=%q protocol=%d (required: version=%s protocol=%d)",
			snapshot.Version,
			snapshot.Protocol,
			supportedVersion,
			supportedProtocol,
		)
	}
	if snapshot.Workspaces == nil || snapshot.Tabs == nil || snapshot.Panes == nil || snapshot.Layouts == nil || snapshot.Agents == nil {
		return nil, fmt.Errorf("herdr snapshot is missing a required collection")
	}

	workspaces := make(map[string]workspaceJSON, len(*snapshot.Workspaces))
	for _, workspace := range *snapshot.Workspaces {
		if strings.TrimSpace(workspace.WorkspaceID) == "" {
			return nil, fmt.Errorf("herdr snapshot contains an empty workspace id")
		}
		if _, duplicate := workspaces[workspace.WorkspaceID]; duplicate {
			return nil, fmt.Errorf("herdr snapshot contains duplicate workspace id %q", workspace.WorkspaceID)
		}
		workspaces[workspace.WorkspaceID] = workspace
	}

	panesByID := make(map[string]paneJSON, len(*snapshot.Panes))
	terminalIDs := make(map[string]string, len(*snapshot.Panes))
	sessionRefs := make(map[agentSessionKey]string, len(*snapshot.Panes))
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
			if previous, duplicate := sessionRefs[ref]; duplicate {
				return nil, fmt.Errorf("herdr panes %q and %q report duplicate agent session refs", previous, pane.PaneID)
			}
			sessionRefs[ref] = pane.PaneID
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
	for paneID := range sessionRefsByPane {
		if _, ok := agentsByPane[paneID]; !ok {
			return nil, fmt.Errorf("herdr pane %q reports an agent session ref without an agent record", paneID)
		}
	}

	live := make([]corebackend.LivePane, 0, len(*snapshot.Panes))
	for _, pane := range *snapshot.Panes {
		workspace := workspaces[pane.WorkspaceID]
		currentPath := optionalString(pane.CWD)
		projectRoot := ""
		worktreePath := ""
		if workspace.Worktree != nil {
			currentPath = workspace.Worktree.CheckoutPath
			projectRoot = workspace.Worktree.RepoRoot
			worktreePath = workspace.Worktree.CheckoutPath
		}
		agentID := ""
		if agent, ok := agentsByPane[pane.PaneID]; ok {
			agentID = optionalString(agent.Name)
			if agentID == "" {
				agentID = optionalString(agent.Agent)
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
			NativeAgentState: pane.AgentStatus,
			TerminalID:       pane.TerminalID,
			AgentID:          agentID,
			ProjectRoot:      projectRoot,
			WorktreePath:     worktreePath,
			SessionID:        target.session,
			SocketPath:       target.socketPath,
		})
	}
	return live, nil
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

func decodeOne(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalInt(value *int) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *value)
}

func optionalBool(value *bool) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%t", *value)
}
