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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

const (
	commandName         = "herdr"
	supportedVersion    = "0.7.3"
	supportedProtocol   = 16
	supportedSchema     = 1
	commandTimeout      = 5 * time.Second
	commandCleanupDelay = 100 * time.Millisecond
	minimumWaitTimeout  = 3 * time.Second
	waitInterval        = 2 * time.Second

	// DefaultWaitTimeout is the bounded wait used when a caller omits an
	// explicit total timeout.
	DefaultWaitTimeout = 300 * time.Second

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
	probeGate  chan struct{}
	lookPath   func(string) (string, error)
	output     commandOutput
	now        func() time.Time
	sleep      waitSleep
}

type commandOutput func(context.Context, string, []string, ...string) ([]byte, error)

type waitSleep func(context.Context, time.Duration) error

type route struct {
	session    string
	socketPath string
}

type probeResult struct {
	binary string
	route  route
}

// WaitStatus is the terminal outcome of a bounded snapshot wait.
type WaitStatus string

const (
	WaitMatched   WaitStatus = "matched"
	WaitTimedOut  WaitStatus = "timed_out"
	WaitCancelled WaitStatus = "cancelled" //nolint:misspell // The published terminal-result contract uses this spelling.
	WaitFailed    WaitStatus = "failed"
)

// WaitResult reports one of the four terminal wait outcomes. Panes contains
// the last compatible snapshot only for matched and timed-out results.
type WaitResult struct {
	Status WaitStatus
	Panes  []corebackend.LivePane
	Err    error
}

// New constructs a herdr backend for one named session. socketPath may be
// empty on the first probe; CheckAvailable resolves it through an explicit
// --session status call, then pins subsequent probes to the returned path.
func New(session, socketPath string) *Backend {
	return &Backend{
		session:    strings.TrimSpace(session),
		socketPath: socketPath,
		probeGate:  make(chan struct{}, 1),
		lookPath:   exec.LookPath,
		output:     runCommand,
		now:        time.Now,
		sleep:      sleepContext,
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
	return b.snapshot(context.Background(), commandTimeout, probed)
}

// Wait probes the exact compatibility tuple once, then polls only aggregate
// snapshots until match succeeds or the fixed budget terminates. A zero
// totalTimeout selects DefaultWaitTimeout; non-zero values must be whole
// seconds and at least three seconds. match receives a cloned compatible
// snapshot and should perform only bounded in-memory inspection.
func (b *Backend) Wait(ctx context.Context, totalTimeout time.Duration, match func([]corebackend.LivePane) bool) WaitResult {
	if ctx == nil {
		return failedWait(fmt.Errorf("herdr wait requires a context"))
	}
	totalTimeout, err := normalizeWaitTimeout(totalTimeout)
	if err != nil {
		return failedWait(err)
	}
	if match == nil {
		return failedWait(fmt.Errorf("herdr wait requires a snapshot predicate"))
	}

	waitCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	if cause := waitCtx.Err(); cause != nil {
		return cancelledWait(cause)
	}
	probed, err := b.probeContext(waitCtx)
	if err != nil {
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		return failedWait(err)
	}

	deadline := b.now().Add(totalTimeout)
	callLimit := waitSnapshotCallLimit(totalTimeout)
	var (
		lastStart time.Time
		lastPanes []corebackend.LivePane
		lastErr   error
		lastValid bool
	)
	for attempt := range callLimit {
		if attempt > 0 {
			if result, done := b.waitForNextSnapshot(waitCtx, deadline, lastStart, lastPanes, lastErr, lastValid); done {
				return result
			}
		}
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}

		now := b.now()
		remaining := deadline.Sub(now)
		if remaining <= 0 {
			return finishWait(lastPanes, lastErr, lastValid)
		}
		lastStart = now
		callTimeout := min(commandTimeout, remaining)
		panes, snapshotErr := b.snapshot(waitCtx, callTimeout, probed)
		if snapshotErr != nil {
			if cause := waitCtx.Err(); cause != nil {
				return cancelledWait(cause)
			}
			lastPanes = nil
			lastErr = snapshotErr
			lastValid = false
			var retryable retryableSnapshotError
			if !errors.As(snapshotErr, &retryable) {
				return failedWait(snapshotErr)
			}
			continue
		}

		lastPanes = append(lastPanes[:0], panes...)
		lastErr = nil
		lastValid = true
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		if !b.now().Before(deadline) {
			return finishWait(lastPanes, nil, true)
		}
		matched := match(append([]corebackend.LivePane(nil), panes...))
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		if !b.now().Before(deadline) {
			return finishWait(lastPanes, nil, true)
		}
		if matched {
			return WaitResult{Status: WaitMatched, Panes: append([]corebackend.LivePane(nil), panes...)}
		}
	}
	return finishWait(lastPanes, lastErr, lastValid)
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

func normalizeWaitTimeout(totalTimeout time.Duration) (time.Duration, error) {
	if totalTimeout == 0 {
		return DefaultWaitTimeout, nil
	}
	if totalTimeout < minimumWaitTimeout || totalTimeout%time.Second != 0 {
		return 0, fmt.Errorf("herdr wait total_timeout must be a whole number of seconds at least 3, got %s", totalTimeout)
	}
	return totalTimeout, nil
}

func waitSnapshotCallLimit(totalTimeout time.Duration) int {
	return int((totalTimeout-1)/waitInterval) + 1
}

func (b *Backend) waitForNextSnapshot(
	ctx context.Context,
	deadline time.Time,
	lastStart time.Time,
	lastPanes []corebackend.LivePane,
	lastErr error,
	lastValid bool,
) (WaitResult, bool) {
	now := b.now()
	if !now.Before(deadline) {
		return finishWait(lastPanes, lastErr, lastValid), true
	}
	nextStart := lastStart.Add(waitInterval)
	if !now.Before(nextStart) {
		return WaitResult{}, false
	}
	delay := nextStart.Sub(now)
	if remaining := deadline.Sub(now); delay > remaining {
		delay = remaining
	}
	if err := b.sleep(ctx, delay); err != nil {
		if cause := ctx.Err(); cause != nil {
			return cancelledWait(cause), true
		}
		return failedWait(fmt.Errorf("wait for next herdr snapshot: %w", err)), true
	}
	if cause := ctx.Err(); cause != nil {
		return cancelledWait(cause), true
	}
	if !b.now().Before(deadline) {
		return finishWait(lastPanes, lastErr, lastValid), true
	}
	return WaitResult{}, false
}

func finishWait(lastPanes []corebackend.LivePane, lastErr error, lastValid bool) WaitResult {
	if lastValid {
		return WaitResult{
			Status: WaitTimedOut,
			Panes:  append([]corebackend.LivePane(nil), lastPanes...),
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("herdr wait ended without a compatible snapshot")
	}
	return failedWait(lastErr)
}

func failedWait(err error) WaitResult {
	return WaitResult{Status: WaitFailed, Err: err}
}

func cancelledWait(err error) WaitResult {
	return WaitResult{Status: WaitCancelled, Err: err}
}

type retryableSnapshotError struct {
	err error
}

func (e retryableSnapshotError) Error() string { return e.err.Error() }

func (e retryableSnapshotError) Unwrap() error { return e.err }

type commandCleanupError struct {
	err error
}

func (e commandCleanupError) Error() string { return "herdr command process cleanup: " + e.err.Error() }

func (e commandCleanupError) Unwrap() error { return e.err }

func (b *Backend) snapshot(ctx context.Context, timeout time.Duration, probed probeResult) ([]corebackend.LivePane, error) {
	out, err := b.runContext(ctx, timeout, probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		wrapped := fmt.Errorf("herdr api snapshot: %w", err)
		if retryableCommandError(err) {
			return nil, retryableSnapshotError{err: wrapped}
		}
		return nil, wrapped
	}
	var envelope snapshotEnvelope
	if err := decodeOne(out, &envelope); err != nil {
		return nil, fmt.Errorf("parse herdr api snapshot: %w", err)
	}
	return projectSnapshot(envelope, probed.route)
}

func (b *Backend) probe() (probeResult, error) {
	return b.probeContext(context.Background())
}

func (b *Backend) probeContext(ctx context.Context) (probeResult, error) {
	select {
	case b.probeGate <- struct{}{}:
		defer func() { <-b.probeGate }()
	case <-ctx.Done():
		return probeResult{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return probeResult{}, err
	}

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
	versionOut, err := b.runContext(ctx, commandTimeout, binary, initial, "--version")
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
	statusOut, err := b.runContext(ctx, commandTimeout, binary, initial, statusArgs...)
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

	schemaOut, err := b.runContext(ctx, commandTimeout, binary, verified, "api", "schema", "--json")
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

func (b *Backend) runContext(ctx context.Context, timeout time.Duration, binary string, target route, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return b.output(callCtx, binary, routeEnvironment(target), args...)
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

func runCommand(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cancelCleanup := make(chan error, 1)
	cmd.Cancel = func() error {
		cleanupErr := killCommandProcessGroup(cmd)
		if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
			select {
			case cancelCleanup <- cleanupErr:
			default:
			}
		}
		return cleanupErr
	}
	cmd.WaitDelay = commandCleanupDelay
	out, err := cmd.Output()
	var cleanupErrors []error
	select {
	case cleanupErr := <-cancelCleanup:
		cleanupErrors = append(cleanupErrors, cleanupErr)
	default:
	}
	if err != nil {
		if cleanupErr := killCommandProcessGroup(cmd); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
			cleanupErrors = append(cleanupErrors, cleanupErr)
		}
	}
	err = finalizeCommandError(err, ctx.Err(), cleanupErrors...)
	if err == nil {
		return out, nil
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

func finalizeCommandError(commandErr, contextErr error, cleanupErrors ...error) error {
	cleanupErr := errors.Join(cleanupErrors...)
	if cleanupErr != nil {
		failure := commandCleanupError{err: cleanupErr}
		if contextErr != nil {
			return errors.Join(contextErr, failure)
		}
		return errors.Join(commandErr, failure)
	}
	if contextErr != nil {
		return contextErr
	}
	return commandErr
}

func retryableCommandError(err error) bool {
	var cleanupErr commandCleanupError
	if errors.As(err, &cleanupErr) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, exec.ErrWaitDelay) {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func killCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return killCommandProcessTree(
		func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) },
		cmd.Process.Kill,
	)
}

func killCommandProcessTree(killGroup, killDirect func() error) error {
	groupErr := killGroup()
	if groupErr == nil {
		return nil
	}
	directErr := killDirect()
	if errors.Is(groupErr, syscall.ESRCH) && errors.Is(directErr, os.ErrProcessDone) {
		return os.ErrProcessDone
	}
	groupErr = fmt.Errorf("kill process group: %w", groupErr)
	if directErr == nil || errors.Is(directErr, os.ErrProcessDone) {
		return groupErr
	}
	return errors.Join(groupErr, fmt.Errorf("kill direct process: %w", directErr))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	RepoKey      string `json:"repo_key"`
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
		repoKey := ""
		if workspace.Worktree != nil {
			currentPath = workspace.Worktree.CheckoutPath
			repoKey = workspace.Worktree.RepoKey
			projectRoot = workspace.Worktree.RepoRoot
			worktreePath = workspace.Worktree.CheckoutPath
		}
		agent, agentPresent := agentsByPane[pane.PaneID]
		agentID := ""
		if agentPresent {
			agentID = optionalString(agent.Name)
			if agentID == "" {
				agentID = optionalString(agent.Agent)
			}
		}
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
			AgentState:       corebackend.MapHerdrAgentState(agentPresent, pane.AgentStatus),
			NativeAgentState: pane.AgentStatus,
			TerminalID:       pane.TerminalID,
			AgentID:          agentID,
			AgentSession:     agentSession,
			AgentPresent:     agentPresent,
			RepoKey:          repoKey,
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
