// Package herdrrun implements fanout's herdr runtime backend.
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

var (
	_ corebackend.Backend     = (*Backend)(nil)
	_ corebackend.OwnedCloser = (*Backend)(nil)
)

// Backend observes one named herdr session. New constructs an unowned,
// observation-only backend; EnsureOwned is the only constructor that binds the
// immutable admission required by targeted reads and mutations.
type Backend struct {
	session    string
	socketPath string
	probeGate  chan struct{}
	lookPath   func(string) (string, error)
	hashFile   func(string) (string, error)
	output     commandOutput
	now        func() time.Time
	sleep      waitSleep
	admitted   map[string]binaryAdmission
	control    *controlPlaneEnvironment
	owner      *ownedAdmission

	// targetAdmission is set only on a private clone returned by a
	// herdrrun-specific target binder. It is never populated from a live
	// snapshot or mutated after construction.
	targetAdmission *ownedTargetAdmission
}

type commandOutput func(context.Context, string, []string, ...string) ([]byte, error)

type waitSleep func(context.Context, time.Duration) error

type route struct {
	session    string
	socketPath string
}

type probeResult struct {
	binary   string
	sha256   string
	version  string
	protocol int
	route    route
}

type binaryAdmission struct {
	path     string
	sha256   string
	version  string
	protocol int
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
		hashFile:   sha256File,
		output:     runCommand,
		now:        time.Now,
		sleep:      sleepContext,
		admitted:   make(map[string]binaryAdmission),
	}
}

func (b *Backend) Name() corebackend.Name { return corebackend.Herdr }

// CheckAvailable verifies the admitted CLI capabilities and connected server.
// It never starts or attaches a herdr server.
func (b *Backend) CheckAvailable() error {
	_, err := b.probe()
	return err
}

// ListLive returns the aggregate session.snapshot projection. The connected
// gate is repeated for each call so binary or server drift fails closed.
func (b *Backend) ListLive() ([]corebackend.LivePane, error) {
	probed, err := b.probe()
	if err != nil {
		return nil, err
	}
	return b.snapshot(context.Background(), commandTimeout, probed)
}

// Wait probes the compatibility gate once, then polls only aggregate
// snapshots until match succeeds or the shared fixed budget terminates. The
// budget includes initial admission and any re-admission. The first
// re-admission after connection loss is immediate; further failed attempts use
// the same two-second cadence without consuming the snapshot-call limit. A
// zero totalTimeout selects DefaultWaitTimeout; non-zero values must be whole
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
	deadline := b.now().Add(totalTimeout)
	probeRemaining := deadline.Sub(b.now())
	if probeRemaining <= 0 {
		return failedWait(context.DeadlineExceeded)
	}
	probeCtx, cancelProbe := context.WithTimeout(waitCtx, probeRemaining)
	probed, err := b.probeContext(probeCtx)
	cancelProbe()
	if err != nil {
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		return failedWait(err)
	}
	if !b.now().Before(deadline) {
		return failedWait(context.DeadlineExceeded)
	}
	callLimit := waitSnapshotCallLimit(totalTimeout)
	snapshotCalls := 0
	var (
		lastSnapshotStart time.Time
		lastReadmitStart  time.Time
		lastPanes         []corebackend.LivePane
		lastErr           error
		lastValid         bool
		readmit           bool
	)
	for snapshotCalls < callLimit {
		if readmit {
			if !lastReadmitStart.IsZero() {
				if result, done := b.waitForNextPollCycle(waitCtx, deadline, lastReadmitStart, lastPanes, lastErr, lastValid); done {
					return result
				}
			}
			if cause := waitCtx.Err(); cause != nil {
				return cancelledWait(cause)
			}
			now := b.now()
			if !now.Before(deadline) {
				return finishWait(lastPanes, lastErr, lastValid)
			}
			lastReadmitStart = now
			readmitCtx, cancelReadmit := context.WithTimeout(waitCtx, deadline.Sub(now))
			nextProbe, probeErr := b.probeContext(readmitCtx)
			cancelReadmit()
			if probeErr != nil {
				if cause := waitCtx.Err(); cause != nil {
					return cancelledWait(cause)
				}
				lastPanes = nil
				lastErr = fmt.Errorf("re-admit herdr after connection loss: %w", probeErr)
				lastValid = false
				if retryableCommandError(probeErr) {
					continue
				}
				return failedWait(lastErr)
			}
			probed = nextProbe
			readmit = false
			lastReadmitStart = time.Time{}
		}
		if snapshotCalls > 0 {
			if result, done := b.waitForNextPollCycle(waitCtx, deadline, lastSnapshotStart, lastPanes, lastErr, lastValid); done {
				return result
			}
		}
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}

		now := b.now()
		if !now.Before(deadline) {
			return finishWait(lastPanes, lastErr, lastValid)
		}
		remaining := deadline.Sub(now)
		if remaining <= 0 {
			return finishWait(lastPanes, lastErr, lastValid)
		}
		lastSnapshotStart = now
		snapshotCalls++
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
			if snapshotCalls >= callLimit {
				break
			}
			readmit = true
			lastReadmitStart = time.Time{}
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

func (b *Backend) waitForNextPollCycle(
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
		return failedWait(fmt.Errorf("wait for next herdr poll cycle: %w", err)), true
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
	out, err := b.runAdmittedContext(
		ctx,
		timeout,
		binaryAdmission{path: probed.binary, sha256: probed.sha256, version: probed.version, protocol: probed.protocol},
		probed.route,
		"api",
		"snapshot",
	)
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
	return projectSnapshot(envelope, probed.route, probed.version, probed.protocol)
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
	// An explicit --session intentionally wins over
	// HERDR_SOCKET_PATH. Use it only to resolve the initial named-session socket;
	// an already verified socket is selected through the environment instead.
	if initial.socketPath == "" {
		statusArgs = append([]string{"--session", initial.session}, statusArgs...)
	}
	statusOut, err := b.runAdmittedContext(ctx, commandTimeout, admitted, initial, statusArgs...)
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
		binary:   admitted.path,
		sha256:   admitted.sha256,
		version:  admitted.version,
		protocol: admitted.protocol,
		route:    verified,
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
	hash, err := b.hashFile(binary)
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("hash herdr executable %s: %w", binary, err)
	}
	provisional := binaryAdmission{path: binary, sha256: hash, protocol: supportedProtocol}
	versionOut, err := b.runAdmittedContext(ctx, commandTimeout, provisional, target, "--version")
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("herdr --version: %w", err)
	}
	version, err := parseAdmittedVersion(versionOut)
	if err != nil {
		return binaryAdmission{}, err
	}
	cacheKey := binary + "\x00" + hash
	admitted := binaryAdmission{path: binary, sha256: hash, version: version, protocol: supportedProtocol}
	if cached, ok := b.admitted[cacheKey]; ok {
		if cached != admitted {
			return binaryAdmission{}, fmt.Errorf("herdr admitted binary identity changed")
		}
		return cached, nil
	}
	schemaOut, err := b.runAdmittedContext(ctx, commandTimeout, admitted, target, "api", "schema", "--json")
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("herdr api schema --json: %w", err)
	}
	if err := validateCapabilitySchema(schemaOut); err != nil {
		return binaryAdmission{}, err
	}
	if err := b.validateCommandSurfaces(ctx, admitted, target); err != nil {
		return binaryAdmission{}, err
	}
	b.admitted[cacheKey] = admitted
	return admitted, nil
}

func (b *Backend) verifyExecutableIdentity(admittedPath, admittedHash string) error {
	binary, err := b.lookPath(commandName)
	if err != nil {
		return fmt.Errorf("re-resolve admitted herdr executable: %w", err)
	}
	if !filepath.IsAbs(binary) {
		binary, err = filepath.Abs(binary)
		if err != nil {
			return fmt.Errorf("resolve admitted herdr executable: %w", err)
		}
	}
	hash, err := b.hashFile(binary)
	if err != nil {
		return fmt.Errorf("re-hash admitted herdr executable: %w", err)
	}
	if binary != admittedPath || hash != admittedHash {
		return fmt.Errorf("herdr executable drifted after admission")
	}
	return nil
}

func (b *Backend) runAdmittedContext(
	ctx context.Context,
	timeout time.Duration,
	admitted binaryAdmission,
	target route,
	args ...string,
) ([]byte, error) {
	if err := b.verifyExecutableIdentity(admitted.path, admitted.sha256); err != nil {
		return nil, err
	}
	return b.runContext(ctx, timeout, admitted.path, target, args...)
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

func routeEnvironment(target route, control *controlPlaneEnvironment) []string {
	overrides := map[string]string{
		sessionEnv: target.session,
	}
	if target.socketPath != "" {
		overrides[socketEnv] = target.socketPath
	}
	if control != nil {
		overrides[xdgConfigEnv] = control.xdgConfigHome
		overrides[xdgStateEnv] = control.xdgStateHome
		overrides[xdgDataEnv] = control.xdgDataHome
		overrides[xdgCacheEnv] = control.xdgCacheHome
		overrides[configEnv] = control.configPath
		overrides[clientSocketEnv] = control.clientSocketPath
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		_, overridden := overrides[key]
		if overridden || key == socketEnv || (control != nil && isHerdrControlKey(key)) {
			continue
		}
		env = append(env, entry)
	}
	for _, key := range []string{xdgConfigEnv, xdgStateEnv, xdgDataEnv, xdgCacheEnv, configEnv, sessionEnv, socketEnv, clientSocketEnv} {
		if value, ok := overrides[key]; ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func isHerdrControlKey(key string) bool {
	if strings.HasPrefix(key, "HERDR_") {
		return true
	}
	switch key {
	case xdgConfigEnv, xdgStateEnv, xdgDataEnv, xdgCacheEnv:
		return true
	default:
		return false
	}
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

func validateStatus(status statusJSON, requested route, admitted binaryAdmission) (route, error) {
	clientVersionErr := validateAdmittedVersion(status.Client.Version)
	if clientVersionErr != nil || status.Client.Version != admitted.version || status.Client.Channel != "stable" || status.Client.Protocol != admitted.protocol {
		return route{}, fmt.Errorf(
			"unsupported herdr client tuple version=%q channel=%q protocol=%d (required: stable >=%s, exact admitted version=%s, channel=stable, protocol=%d)",
			status.Client.Version,
			status.Client.Channel,
			status.Client.Protocol,
			minimumVersion,
			admitted.version,
			admitted.protocol,
		)
	}
	if status.Client.Session == nil || *status.Client.Session != requested.session {
		return route{}, fmt.Errorf("herdr client session is %q, want %q", optionalString(status.Client.Session), requested.session)
	}
	if status.Server.Status != "running" || !status.Server.Running {
		return route{}, fmt.Errorf("herdr named session %q is not running", requested.session)
	}
	serverVersion := optionalString(status.Server.Version)
	serverVersionErr := validateAdmittedVersion(serverVersion)
	if status.Server.Version == nil || serverVersionErr != nil || serverVersion != admitted.version ||
		status.Server.Protocol == nil || *status.Server.Protocol != admitted.protocol ||
		status.Server.Compatible == nil || !*status.Server.Compatible {
		return route{}, fmt.Errorf(
			"unsupported herdr server tuple version=%q protocol=%s compatible=%s (required: stable >=%s, exact admitted version=%s, protocol=%d, compatible=true)",
			serverVersion,
			optionalInt(status.Server.Protocol),
			optionalBool(status.Server.Compatible),
			minimumVersion,
			admitted.version,
			admitted.protocol,
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
	Version            string           `json:"version"`
	Protocol           int              `json:"protocol"`
	FocusedWorkspaceID *string          `json:"focused_workspace_id"`
	FocusedTabID       *string          `json:"focused_tab_id"`
	FocusedPaneID      *string          `json:"focused_pane_id"`
	Workspaces         *[]workspaceJSON `json:"workspaces"`
	Tabs               *[]tabJSON       `json:"tabs"`
	Panes              *[]paneJSON      `json:"panes"`
	Layouts            *[]layoutJSON    `json:"layouts"`
	Agents             *[]agentJSON     `json:"agents"`
}

type workspaceJSON struct {
	WorkspaceID string            `json:"workspace_id"`
	Number      *uint64           `json:"number"`
	Label       *string           `json:"label"`
	Focused     *bool             `json:"focused"`
	PaneCount   *uint64           `json:"pane_count"`
	TabCount    *uint64           `json:"tab_count"`
	ActiveTabID *string           `json:"active_tab_id"`
	AgentStatus string            `json:"agent_status"`
	Worktree    *worktreeInfoJSON `json:"worktree"`
}

type worktreeInfoJSON struct {
	RepoKey          string `json:"repo_key"`
	RepoName         string `json:"repo_name"`
	CheckoutPath     string `json:"checkout_path"`
	RepoRoot         string `json:"repo_root"`
	IsLinkedWorktree *bool  `json:"is_linked_worktree"`
}

type tabJSON struct {
	TabID       string  `json:"tab_id"`
	WorkspaceID string  `json:"workspace_id"`
	Number      *uint64 `json:"number"`
	Label       *string `json:"label"`
	Focused     *bool   `json:"focused"`
	PaneCount   *uint64 `json:"pane_count"`
	AgentStatus string  `json:"agent_status"`
}

type layoutJSON struct {
	WorkspaceID string             `json:"workspace_id"`
	TabID       string             `json:"tab_id"`
	Zoomed      *bool              `json:"zoomed"`
	Area        *layoutRectJSON    `json:"area"`
	FocusedPane *string            `json:"focused_pane_id"`
	Panes       *[]layoutPaneJSON  `json:"panes"`
	Splits      *[]layoutSplitJSON `json:"splits"`
}

type layoutRectJSON struct {
	X      *uint16 `json:"x"`
	Y      *uint16 `json:"y"`
	Width  *uint16 `json:"width"`
	Height *uint16 `json:"height"`
}

type layoutPaneJSON struct {
	PaneID  string          `json:"pane_id"`
	Focused *bool           `json:"focused"`
	Rect    *layoutRectJSON `json:"rect"`
}

type layoutSplitJSON struct {
	ID        string          `json:"id"`
	Direction string          `json:"direction"`
	Ratio     *float64        `json:"ratio"`
	Rect      *layoutRectJSON `json:"rect"`
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

func projectSnapshot(envelope snapshotEnvelope, target route, admittedVersion string, admittedProtocol int) ([]corebackend.LivePane, error) {
	if envelope.ID != "cli:api:snapshot" || envelope.Result == nil || envelope.Result.Type != "session_snapshot" {
		return nil, fmt.Errorf("unexpected herdr snapshot envelope")
	}
	snapshot := envelope.Result.Snapshot
	versionErr := validateAdmittedVersion(snapshot.Version)
	if versionErr != nil || snapshot.Version != admittedVersion || snapshot.Protocol != admittedProtocol {
		return nil, fmt.Errorf(
			"unsupported herdr snapshot tuple version=%q protocol=%d (required: stable >=%s, exact admitted version=%s, protocol=%d)",
			snapshot.Version,
			snapshot.Protocol,
			minimumVersion,
			admittedVersion,
			admittedProtocol,
		)
	}
	if snapshot.Workspaces == nil || snapshot.Tabs == nil || snapshot.Panes == nil || snapshot.Layouts == nil || snapshot.Agents == nil {
		return nil, fmt.Errorf("herdr snapshot is missing a required collection")
	}

	workspaces := make(map[string]workspaceJSON, len(*snapshot.Workspaces))
	for _, workspace := range *snapshot.Workspaces {
		if strings.TrimSpace(workspace.WorkspaceID) == "" || workspace.Number == nil || workspace.Label == nil ||
			workspace.Focused == nil || workspace.PaneCount == nil || workspace.TabCount == nil ||
			workspace.ActiveTabID == nil || strings.TrimSpace(*workspace.ActiveTabID) == "" ||
			!validNativeAgentState(workspace.AgentStatus) {
			return nil, fmt.Errorf("herdr snapshot contains a workspace with incomplete required fields")
		}
		if workspace.Worktree != nil &&
			(strings.TrimSpace(workspace.Worktree.RepoKey) == "" ||
				strings.TrimSpace(workspace.Worktree.RepoName) == "" ||
				strings.TrimSpace(workspace.Worktree.CheckoutPath) == "" ||
				strings.TrimSpace(workspace.Worktree.RepoRoot) == "" ||
				workspace.Worktree.IsLinkedWorktree == nil) {
			return nil, fmt.Errorf("herdr workspace %q has incomplete worktree provenance", workspace.WorkspaceID)
		}
		if _, duplicate := workspaces[workspace.WorkspaceID]; duplicate {
			return nil, fmt.Errorf("herdr snapshot contains duplicate workspace id %q", workspace.WorkspaceID)
		}
		workspaces[workspace.WorkspaceID] = workspace
	}

	tabsByID := make(map[string]tabJSON, len(*snapshot.Tabs))
	for _, tab := range *snapshot.Tabs {
		if strings.TrimSpace(tab.TabID) == "" || strings.TrimSpace(tab.WorkspaceID) == "" || tab.Number == nil ||
			tab.Label == nil || tab.Focused == nil || tab.PaneCount == nil || !validNativeAgentState(tab.AgentStatus) {
			return nil, fmt.Errorf("herdr snapshot contains a tab with incomplete required fields")
		}
		if _, duplicate := tabsByID[tab.TabID]; duplicate {
			return nil, fmt.Errorf("herdr snapshot contains duplicate tab id %q", tab.TabID)
		}
		tabsByID[tab.TabID] = tab
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
	if err := validateSnapshotRelationships(snapshot, workspaces, tabsByID, panesByID); err != nil {
		return nil, err
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

func validateSnapshotRelationships(
	snapshot snapshotJSON,
	workspaces map[string]workspaceJSON,
	tabsByID map[string]tabJSON,
	panesByID map[string]paneJSON,
) error {
	tabsByWorkspace := make(map[string]map[string]struct{}, len(workspaces))
	panesByWorkspace := make(map[string]map[string]struct{}, len(workspaces))
	panesByTab := make(map[string]map[string]struct{}, len(tabsByID))
	focusedWorkspaceID := ""
	focusedTabID := ""
	focusedPaneID := ""

	for tabID, tab := range tabsByID {
		if _, ok := workspaces[tab.WorkspaceID]; !ok {
			return fmt.Errorf("herdr tab %q references unknown workspace %q", tabID, tab.WorkspaceID)
		}
		if tabsByWorkspace[tab.WorkspaceID] == nil {
			tabsByWorkspace[tab.WorkspaceID] = make(map[string]struct{})
		}
		tabsByWorkspace[tab.WorkspaceID][tabID] = struct{}{}
		if *tab.Focused {
			if focusedTabID != "" {
				return fmt.Errorf("herdr snapshot contains multiple focused tabs %q and %q", focusedTabID, tabID)
			}
			focusedTabID = tabID
		}
	}

	for paneID, pane := range panesByID {
		tab, ok := tabsByID[pane.TabID]
		if !ok {
			return fmt.Errorf("herdr pane %q references unknown tab %q", paneID, pane.TabID)
		}
		if tab.WorkspaceID != pane.WorkspaceID {
			return fmt.Errorf("herdr pane %q workspace %q disagrees with tab %q workspace %q", paneID, pane.WorkspaceID, pane.TabID, tab.WorkspaceID)
		}
		if panesByWorkspace[pane.WorkspaceID] == nil {
			panesByWorkspace[pane.WorkspaceID] = make(map[string]struct{})
		}
		panesByWorkspace[pane.WorkspaceID][paneID] = struct{}{}
		if panesByTab[pane.TabID] == nil {
			panesByTab[pane.TabID] = make(map[string]struct{})
		}
		panesByTab[pane.TabID][paneID] = struct{}{}
		if *pane.Focused {
			if focusedPaneID != "" {
				return fmt.Errorf("herdr snapshot contains multiple focused panes %q and %q", focusedPaneID, paneID)
			}
			focusedPaneID = paneID
		}
	}

	for workspaceID, workspace := range workspaces {
		activeTab, ok := tabsByID[*workspace.ActiveTabID]
		if !ok || activeTab.WorkspaceID != workspaceID {
			return fmt.Errorf("herdr workspace %q references invalid active tab %q", workspaceID, *workspace.ActiveTabID)
		}
		if got := uint64(len(tabsByWorkspace[workspaceID])); got != *workspace.TabCount {
			return fmt.Errorf("herdr workspace %q tab_count=%d, observed %d", workspaceID, *workspace.TabCount, got)
		}
		if got := uint64(len(panesByWorkspace[workspaceID])); got != *workspace.PaneCount {
			return fmt.Errorf("herdr workspace %q pane_count=%d, observed %d", workspaceID, *workspace.PaneCount, got)
		}
		if *workspace.Focused {
			if focusedWorkspaceID != "" {
				return fmt.Errorf("herdr snapshot contains multiple focused workspaces %q and %q", focusedWorkspaceID, workspaceID)
			}
			focusedWorkspaceID = workspaceID
			if !*activeTab.Focused {
				return fmt.Errorf("herdr focused workspace %q has unfocused active tab %q", workspaceID, *workspace.ActiveTabID)
			}
		}
	}

	for tabID, tab := range tabsByID {
		if got := uint64(len(panesByTab[tabID])); got != *tab.PaneCount {
			return fmt.Errorf("herdr tab %q pane_count=%d, observed %d", tabID, *tab.PaneCount, got)
		}
		workspace := workspaces[tab.WorkspaceID]
		if *tab.Focused && (!*workspace.Focused || *workspace.ActiveTabID != tabID) {
			return fmt.Errorf("herdr focused tab %q disagrees with workspace %q focus", tabID, tab.WorkspaceID)
		}
	}

	for paneID, pane := range panesByID {
		if !*pane.Focused {
			continue
		}
		tab := tabsByID[pane.TabID]
		workspace := workspaces[pane.WorkspaceID]
		if !*tab.Focused || !*workspace.Focused {
			return fmt.Errorf("herdr focused pane %q disagrees with tab or workspace focus", paneID)
		}
	}

	if (focusedWorkspaceID == "") != (focusedTabID == "") || (focusedTabID == "") != (focusedPaneID == "") {
		return fmt.Errorf("herdr snapshot has an incomplete focused workspace/tab/pane chain")
	}
	if err := validateOptionalFocusedID("workspace", snapshot.FocusedWorkspaceID, focusedWorkspaceID); err != nil {
		return err
	}
	if err := validateOptionalFocusedID("tab", snapshot.FocusedTabID, focusedTabID); err != nil {
		return err
	}
	if err := validateOptionalFocusedID("pane", snapshot.FocusedPaneID, focusedPaneID); err != nil {
		return err
	}

	return validateSnapshotLayouts(*snapshot.Layouts, workspaces, tabsByID, panesByID, panesByTab)
}

func validateOptionalFocusedID(kind string, reported *string, observed string) error {
	if reported == nil {
		return nil
	}
	if strings.TrimSpace(*reported) == "" || *reported != observed {
		return fmt.Errorf("herdr snapshot focused_%s_id=%q, observed %q", kind, *reported, observed)
	}
	return nil
}

func validateSnapshotLayouts(
	layouts []layoutJSON,
	workspaces map[string]workspaceJSON,
	tabsByID map[string]tabJSON,
	panesByID map[string]paneJSON,
	panesByTab map[string]map[string]struct{},
) error {
	seenLayouts := make(map[string]bool, len(layouts))
	for _, layout := range layouts {
		if strings.TrimSpace(layout.WorkspaceID) == "" || strings.TrimSpace(layout.TabID) == "" ||
			layout.Zoomed == nil || !completeLayoutRect(layout.Area) || layout.FocusedPane == nil ||
			strings.TrimSpace(*layout.FocusedPane) == "" || layout.Panes == nil || layout.Splits == nil {
			return fmt.Errorf("herdr snapshot contains a layout with incomplete required fields")
		}
		tab, ok := tabsByID[layout.TabID]
		if !ok {
			return fmt.Errorf("herdr layout references unknown tab %q", layout.TabID)
		}
		if _, ok := workspaces[layout.WorkspaceID]; !ok || tab.WorkspaceID != layout.WorkspaceID {
			return fmt.Errorf("herdr layout for tab %q has invalid workspace %q", layout.TabID, layout.WorkspaceID)
		}
		if seenLayouts[layout.TabID] {
			return fmt.Errorf("herdr snapshot contains duplicate layout for tab %q", layout.TabID)
		}
		seenLayouts[layout.TabID] = true

		layoutPanes := make(map[string]bool, len(*layout.Panes))
		for _, layoutPane := range *layout.Panes {
			if strings.TrimSpace(layoutPane.PaneID) == "" || layoutPane.Focused == nil || !completeLayoutRect(layoutPane.Rect) {
				return fmt.Errorf("herdr layout for tab %q contains a pane with incomplete required fields", layout.TabID)
			}
			if layoutPanes[layoutPane.PaneID] {
				return fmt.Errorf("herdr layout for tab %q contains duplicate pane %q", layout.TabID, layoutPane.PaneID)
			}
			pane, ok := panesByID[layoutPane.PaneID]
			if !ok || pane.WorkspaceID != layout.WorkspaceID || pane.TabID != layout.TabID {
				return fmt.Errorf("herdr layout for tab %q references foreign or unknown pane %q", layout.TabID, layoutPane.PaneID)
			}
			if *layoutPane.Focused != *pane.Focused {
				return fmt.Errorf("herdr layout focus disagrees with pane %q", layoutPane.PaneID)
			}
			layoutPanes[layoutPane.PaneID] = true
		}
		if !layoutPanes[*layout.FocusedPane] {
			return fmt.Errorf("herdr layout for tab %q references unknown focused pane %q", layout.TabID, *layout.FocusedPane)
		}
		if len(layoutPanes) != len(panesByTab[layout.TabID]) {
			return fmt.Errorf("herdr layout for tab %q pane set does not match snapshot panes", layout.TabID)
		}
		for paneID := range panesByTab[layout.TabID] {
			if !layoutPanes[paneID] {
				return fmt.Errorf("herdr layout for tab %q is missing pane %q", layout.TabID, paneID)
			}
		}
		if *tab.Focused && !*panesByID[*layout.FocusedPane].Focused {
			return fmt.Errorf("herdr focused tab %q has unfocused layout pane %q", layout.TabID, *layout.FocusedPane)
		}
		for _, split := range *layout.Splits {
			if strings.TrimSpace(split.ID) == "" || split.Ratio == nil || !completeLayoutRect(split.Rect) ||
				(split.Direction != "right" && split.Direction != "down") {
				return fmt.Errorf("herdr layout for tab %q contains a split with incomplete required fields", layout.TabID)
			}
		}
	}
	for tabID := range tabsByID {
		if !seenLayouts[tabID] {
			return fmt.Errorf("herdr snapshot is missing layout for tab %q", tabID)
		}
	}
	return nil
}

func completeLayoutRect(rect *layoutRectJSON) bool {
	return rect != nil && rect.X != nil && rect.Y != nil && rect.Width != nil && rect.Height != nil
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
