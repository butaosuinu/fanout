// Package herdrrun implements fanout's herdr runtime backend.
package herdrrun

import (
	"bytes"
	"cmp"
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

var (
	_ corebackend.Backend     = (*Backend)(nil)
	_ corebackend.OwnedCloser = (*Backend)(nil)
)

// Backend observes one named herdr session. New returns an unowned handle, so
// targeted reads and mutations remain disabled until EnsureOwned and an
// immutable target admission explicitly bind them.
type Backend struct {
	session     string
	socketPath  string
	previewOnly bool
	probeGate   chan struct{}
	lookPath    func(string) (string, error)
	stageBinary func(string) (string, string, error)
	output      commandOutput
	now         func() time.Time
	sleep       waitSleep
	admitted    map[string]binaryAdmission
	control     *controlPlaneEnvironment
	owner       *ownedAdmission
	target      *ownedTargetAdmission
}

type commandOutput func(context.Context, string, []string, ...string) ([]byte, error)

type waitSleep func(context.Context, time.Duration) error

type route struct {
	session    string
	socketPath string
}

type probeResult struct {
	binary  string
	sha256  string
	version string
	route   route
}

type binaryAdmission struct {
	path    string
	sha256  string
	version string
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
		session:     strings.TrimSpace(session),
		socketPath:  socketPath,
		probeGate:   make(chan struct{}, 1),
		lookPath:    exec.LookPath,
		stageBinary: stageAdmissionBinary,
		output:      runCommand,
		now:         time.Now,
		sleep:       sleepContext,
		admitted:    map[string]binaryAdmission{},
	}
}

// NewPreview constructs a mutation-free launch preview backend. Availability
// checks only the CLI version and does not require or probe a named session.
func NewPreview() *Backend {
	backend := New("", "")
	backend.previewOnly = true
	return backend
}

func (b *Backend) Name() corebackend.Name { return corebackend.Herdr }

// CheckAvailable verifies the stable version floor. Normal backends also
// require a connected server; preview backends stop after the CLI check.
func (b *Backend) CheckAvailable() error {
	if b.previewOnly {
		return b.checkPreviewAvailable()
	}
	_, err := b.probe()
	return err
}

func (b *Backend) checkPreviewAvailable() error {
	binary, err := b.lookPath(commandName)
	if err != nil {
		return fmt.Errorf("herdr stable >=%s is required: %w", minimumVersion, err)
	}
	versionOut, err := b.runContext(context.Background(), commandTimeout, binary, route{}, "--version")
	if err != nil {
		return fmt.Errorf("herdr --version: %w", err)
	}
	_, err = parseAdmittedVersion(versionOut)
	return err
}

// ListLive returns the aggregate session.snapshot projection. Connected status
// is rechecked for each call; binary version admission is digest-cached.
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
			if !IsRetryableObservationError(snapshotErr) {
				return failedWait(snapshotErr)
			}
			continue
		}

		lastPanes = cloneLivePanes(panes)
		lastErr = nil
		lastValid = true
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		if !b.now().Before(deadline) {
			return finishWait(lastPanes, nil, true)
		}
		matched := match(cloneLivePanes(panes))
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		if !b.now().Before(deadline) {
			return finishWait(lastPanes, nil, true)
		}
		if matched {
			return WaitResult{Status: WaitMatched, Panes: cloneLivePanes(panes)}
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

func (b *Backend) Read(ref corebackend.PaneRef, lines int) (string, error) {
	return b.readCore(ref, lines)
}

func (b *Backend) SendLine(ref corebackend.PaneRef, line string) error {
	return b.sendLineCore(ref, line)
}

func (b *Backend) Focus(ref corebackend.PaneRef) error {
	return b.focusCore(ref)
}

func (b *Backend) Close(ref corebackend.PaneRef) error {
	return b.closeCore(ref)
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
			Panes:  cloneLivePanes(lastPanes),
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("herdr wait ended without a compatible snapshot")
	}
	return failedWait(lastErr)
}

func cloneLivePanes(panes []corebackend.LivePane) []corebackend.LivePane {
	if panes == nil {
		return nil
	}
	cloned := append([]corebackend.LivePane(nil), panes...)
	for i := range cloned {
		if cloned[i].AgentSession == nil {
			continue
		}
		session := *cloned[i].AgentSession
		cloned[i].AgentSession = &session
	}
	return cloned
}

func failedWait(err error) WaitResult {
	return WaitResult{Status: WaitFailed, Err: err}
}

func cancelledWait(err error) WaitResult {
	return WaitResult{Status: WaitCancelled, Err: err}
}

type retryableObservationError struct {
	err error
}

func (e retryableObservationError) Error() string { return e.err.Error() }

func (e retryableObservationError) Unwrap() error { return e.err }

func (e retryableObservationError) RetryableObservation() bool { return true }

type retryableObservation interface {
	error
	RetryableObservation() bool
}

// IsRetryableObservationError reports whether a read-only Herdr command may be
// retried within the caller's fixed observation budget.
func IsRetryableObservationError(err error) bool {
	retryable, ok := errors.AsType[retryableObservation](err)
	return ok && retryable.RetryableObservation()
}

type commandCleanupError struct {
	err error
}

func (e commandCleanupError) Error() string { return "herdr command process cleanup: " + e.err.Error() }

func (e commandCleanupError) Unwrap() error { return e.err }

func (b *Backend) snapshot(ctx context.Context, timeout time.Duration, probed probeResult) ([]corebackend.LivePane, error) {
	out, err := b.runContext(ctx, timeout, probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		wrapped := methodUnavailable("session.snapshot")
		if retryableCommandError(err) {
			return nil, retryableObservationError{err: wrapped}
		}
		return nil, wrapped
	}
	var envelope snapshotEnvelope
	if parseErr := decodeOne(out, &envelope); parseErr != nil {
		return nil, methodUnavailable("session.snapshot")
	}
	panes, err := projectSnapshot(envelope, probed)
	if err != nil {
		return nil, methodUnavailable("session.snapshot")
	}
	return panes, nil
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
	initial := route{session: b.session, socketPath: b.socketPath}
	admitted, err := b.admitBinaryContext(ctx, initial)
	if err != nil {
		return probeResult{}, err
	}

	statusArgs := []string{"status", "--json"}
	// Use --session only to discover an external named session. Once a
	// socket is known, every call selects it explicitly because HERDR_SOCKET_PATH
	// takes precedence over HERDR_SESSION.
	if initial.socketPath == "" {
		statusArgs = append([]string{"--session", initial.session}, statusArgs...)
	}
	statusOut, err := b.runContext(ctx, commandTimeout, admitted.path, initial, statusArgs...)
	if err != nil {
		return probeResult{}, fmt.Errorf("herdr status --json: %w", err)
	}
	var status statusJSON
	if decodeErr := decodeOne(statusOut, &status); decodeErr != nil {
		return probeResult{}, fmt.Errorf("parse herdr status --json: %w", decodeErr)
	}
	verified, err := validateStatus(status, initial, admitted)
	if err != nil {
		return probeResult{}, err
	}
	if b.socketPath == "" {
		b.socketPath = verified.socketPath
	}
	return probeResult{
		binary:  admitted.path,
		sha256:  admitted.sha256,
		version: admitted.version,
		route:   verified,
	}, nil
}

func (b *Backend) admitBinaryContext(ctx context.Context, target route) (binaryAdmission, error) {
	binary, err := b.lookPath(commandName)
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("herdr stable >=%s is required: %w", minimumVersion, err)
	}
	if !filepath.IsAbs(binary) {
		binary, err = filepath.Abs(binary)
		if err != nil {
			return binaryAdmission{}, fmt.Errorf("resolve herdr executable: %w", err)
		}
	}
	binary, hash, err := b.stageBinary(binary)
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("stage herdr executable before admission: %w", err)
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || !validHexToken(hash) {
		return binaryAdmission{}, fmt.Errorf("staged herdr executable has an invalid content identity")
	}
	versionOut, err := b.runContext(ctx, commandTimeout, binary, target, "--version")
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("herdr --version: %w", err)
	}
	version, err := parseAdmittedVersion(versionOut)
	if err != nil {
		return binaryAdmission{}, err
	}
	admitted := binaryAdmission{path: binary, sha256: hash, version: version}
	key := binary + "\x00" + hash
	if cached, ok := b.admitted[key]; ok {
		if cached != admitted {
			return binaryAdmission{}, fmt.Errorf("herdr admitted binary identity changed")
		}
		return cached, nil
	}
	b.admitted[key] = admitted
	return admitted, nil
}

func methodUnavailable(method string) error {
	return fmt.Errorf("herdr method %q is unavailable", method)
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
	return b.output(callCtx, binary, routeEnvironment(target, b.control), args...)
}

func routeEnvironment(target route, controls ...*controlPlaneEnvironment) []string {
	var control *controlPlaneEnvironment
	if len(controls) > 0 {
		control = controls[0]
	}
	env := make([]string, 0, len(os.Environ())+8)
	if control != nil {
		env = append(env,
			xdgConfigEnv+"="+control.xdgConfigHome,
			xdgStateEnv+"="+control.xdgStateHome,
			xdgDataEnv+"="+control.xdgDataHome,
			xdgCacheEnv+"="+control.xdgCacheHome,
			configEnv+"="+control.configPath,
			clientSocketEnv+"="+control.clientSocketPath,
		)
	} else {
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if key == sessionEnv || key == socketEnv {
				continue
			}
			env = append(env, entry)
		}
	}
	env = append(env, sessionEnv+"="+target.session)
	if target.socketPath != "" {
		env = append(env, socketEnv+"="+target.socketPath)
	}
	return env
}

func runCommand(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
	return runBoundedCommand(ctx, binary, env, false, args...)
}

func runCommandCombined(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
	return runBoundedCommand(ctx, binary, env, true, args...)
}

func runBoundedCommand(ctx context.Context, binary string, env []string, combined bool, args ...string) ([]byte, error) {
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
	var out []byte
	var err error
	if combined {
		out, err = cmd.CombinedOutput()
	} else {
		out, err = cmd.Output()
	}
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
	if !combined && errors.As(err, &exitErr) {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			return out, fmt.Errorf("%w: %s", err, stderr)
		}
	}
	if combined {
		if message := strings.TrimSpace(string(out)); message != "" {
			return out, fmt.Errorf("%w: %s", err, message)
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
		Version string  `json:"version"`
		Channel string  `json:"channel"`
		Session *string `json:"session"`
	} `json:"client"`
	Server struct {
		Status        string  `json:"status"`
		Running       bool    `json:"running"`
		Version       *string `json:"version"`
		Socket        string  `json:"socket"`
		Session       *string `json:"session"`
		RestartNeeded *bool   `json:"restart_needed"`
	} `json:"server"`
	Update struct {
		RestartNeeded *bool `json:"restart_needed"`
	} `json:"update"`
}

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
	Workspaces *[]workspaceJSON   `json:"workspaces"`
	Tabs       *[]json.RawMessage `json:"tabs"`
	Panes      *[]paneJSON        `json:"panes"`
	Layouts    *[]json.RawMessage `json:"layouts"`
	Agents     *[]agentJSON       `json:"agents"`
}

type workspaceJSON struct {
	WorkspaceID string            `json:"workspace_id"`
	Label       string            `json:"label"`
	Focused     *bool             `json:"focused"`
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
		agentID, agentProvider := projectAgentIdentity(agent, agentPresent)
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

func projectAgentIdentity(agent agentJSON, present bool) (string, string) {
	if !present {
		return "", ""
	}
	provider := optionalString(agent.Agent)
	return cmp.Or(optionalString(agent.Name), provider), provider
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
