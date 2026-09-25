package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type commandOutput func(context.Context, string, []string, ...string) ([]byte, error)

type waitSleep func(context.Context, time.Duration) error

// herdrCLI is the herdr CLI transport: binary admission, the pinned session
// route, and the process runner. Backend embeds it so ownership and target
// state stay separate from how commands reach herdr.
type herdrCLI struct {
	session     string
	socketPath  string
	probeGate   chan struct{}
	lookPath    func(string) (string, error)
	stageBinary func(string) (string, string, error)
	output      commandOutput
	now         func() time.Time
	sleep       waitSleep
	admitted    map[string]binaryAdmission
	control     *controlPlaneEnvironment
}

func newHerdrCLI(session, socketPath string) *herdrCLI {
	return &herdrCLI{
		session:     session,
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

// clone copies the transport for an independently bound handle: a fresh probe
// gate, a copied admission cache, and a copied control-plane environment.
func (b *herdrCLI) clone() *herdrCLI {
	clone := *b
	clone.probeGate = make(chan struct{}, 1)
	clone.admitted = map[string]binaryAdmission{}
	maps.Copy(clone.admitted, b.admitted)
	if b.control != nil {
		control := *b.control
		clone.control = &control
	}
	return &clone
}

type route struct {
	session    string
	socketPath string
}

type retryableObservationError struct {
	err error
}

func (e retryableObservationError) Error() string { return e.err.Error() }

func (e retryableObservationError) Unwrap() error { return e.err }

func (e retryableObservationError) RetryableObservation() bool { return true }

var _ corebackend.RetryableObservation = retryableObservationError{}

type commandCleanupError struct {
	err error
}

func (e commandCleanupError) Error() string { return "herdr command process cleanup: " + e.err.Error() }

func (e commandCleanupError) Unwrap() error { return e.err }

func methodUnavailable(method string) error {
	return fmt.Errorf("herdr method %q is unavailable", method)
}

func commandTimedOut(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

func readMethodError(method string, err error) error {
	if commandTimedOut(err) || errors.Is(err, exec.ErrWaitDelay) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("herdr method %q: %w", method, err)
	}
	return methodUnavailable(method)
}

// runReadContext retries only timed-out reads. Callers with their own polling
// budget use runContext directly; mutations must never enter this retry lane.
func (b *herdrCLI) runReadContext(ctx context.Context, binary string, target route, args ...string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		out, err := b.runContext(ctx, commandTimeout, binary, target, args...)
		if attempt >= readRetryCount || !commandTimedOut(err) || !retryableCommandError(err) || ctx.Err() != nil {
			return out, err
		}
		if err := b.sleep(ctx, readRetryDelay); err != nil {
			return nil, err
		}
	}
}

func (b *herdrCLI) runContext(ctx context.Context, timeout time.Duration, binary string, target route, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := b.output(callCtx, binary, routeEnvironment(target, b.control), args...)
	if commandTimedOut(err) {
		err = fmt.Errorf("timed out after %s: %w", timeout, err)
	} else if errors.Is(err, exec.ErrWaitDelay) {
		err = fmt.Errorf("herdr output pipe cleanup exceeded %s (a child process may still hold stdout/stderr open): %w", commandCleanupDelay, err)
	}
	return out, err
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
	if target.session != "" {
		env = append(env, sessionEnv+"="+target.session)
	}
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
	if commandTimedOut(err) || errors.Is(err, exec.ErrWaitDelay) {
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
