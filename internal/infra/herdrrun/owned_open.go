package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"

	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/core/naming"
)

type (
	supervisorStarter func(markerPath, nonce, startToken string) (*startedSupervisor, error)
	ownerMarkerWriter func(path string, marker ownerMarker) error
)

func EnsureOwned(ctx context.Context, opts OwnedOptions) (*OwnedSession, error) {
	return ensureOwned(ctx, opts, nil, startOwnedSupervisor)
}

// OpenOwned opens and validates an existing fanout-owned session without
// creating directories, claiming ownership, or starting a supervisor.
func OpenOwned(ctx context.Context, opts OwnedOptions) (_ *OwnedSession, err error) {
	defer errs.Wrap(&err, "open owned Herdr session")
	return openOwned(ctx, opts, nil)
}

func openOwned(ctx context.Context, opts OwnedOptions, backend *Backend) (*OwnedSession, error) {
	if ctx == nil {
		return nil, fmt.Errorf("open owned herdr session requires a context")
	}
	commonDir, layout, marker, admitted, err := existingOwnedAdmission(opts)
	if err != nil {
		return nil, err
	}
	backend, err = reopenOwnedBackend(ctx, layout, marker, admitted, backend)
	if err != nil {
		return nil, err
	}
	return ownedSessionFromMarker(commonDir, marker, currentOwnedEmitterPath(marker), backend), nil
}

func existingOwnedAdmission(
	opts OwnedOptions,
) (string, ownedLayout, ownerMarker, binaryAdmission, error) {
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(opts.GitCommonDir)
	if err != nil {
		return "", ownedLayout{}, ownerMarker{}, binaryAdmission{}, err
	}
	session := naming.ManagedSessionName(commonIdentity.device, commonIdentity.inode)
	layout, err := prepareOwnedLayout(opts.RuntimeBase, session)
	if err != nil {
		return "", ownedLayout{}, ownerMarker{}, binaryAdmission{}, err
	}
	marker, found, err := readOwnerMarker(layout.markerPath)
	if err != nil {
		return "", ownedLayout{}, ownerMarker{}, binaryAdmission{}, err
	}
	if !found {
		return "", ownedLayout{}, ownerMarker{}, binaryAdmission{}, corebackend.ErrOwnedSessionNotFound
	}
	admitted := binaryAdmission{
		path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
	}
	launcher := binaryAdmission{path: marker.LauncherPath, sha256: marker.LauncherSHA256}
	if err := validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, launcher); err != nil {
		return "", ownedLayout{}, ownerMarker{}, binaryAdmission{}, err
	}
	return commonDir, layout, marker, admitted, nil
}

func reopenOwnedBackend(
	ctx context.Context,
	layout ownedLayout,
	marker ownerMarker,
	admitted binaryAdmission,
	backend *Backend,
) (*Backend, error) {
	lock, err := lockExistingPrivateFileContext(ctx, layout.lifecycleLock)
	if err != nil {
		return nil, fmt.Errorf("lock existing herdr owned lifecycle: %w", err)
	}
	defer unlockPrivateFile(lock)
	current, found, err := readOwnerMarker(layout.markerPath)
	if err != nil || !found || current != marker {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("herdr ownership marker changed")
	}
	if err := verifyLiveSupervisor(layout.supervisorLock, marker); err != nil {
		return nil, err
	}
	if err := validatePrivateSocket(marker.SocketPath); err != nil {
		return nil, err
	}
	if err := validatePrivateSocket(marker.ClientSocketPath); err != nil {
		return nil, err
	}
	backend = newReopenedOwnedBackend(layout, marker, admitted, backend)
	if _, err := backend.probeOwned(ctx, *backend.owner); err != nil {
		return nil, err
	}
	return backend, nil
}

func newReopenedOwnedBackend(
	layout ownedLayout,
	marker ownerMarker,
	admitted binaryAdmission,
	backend *Backend,
) *Backend {
	if backend == nil {
		backend = New(marker.Session, layout.socketPath)
	}
	backend.session = marker.Session
	backend.socketPath = marker.SocketPath
	backend.control = &controlPlaneEnvironment{
		xdgConfigHome: layout.xdgConfigHome, xdgStateHome: layout.xdgStateHome,
		xdgDataHome: layout.xdgDataHome, xdgCacheHome: layout.xdgCacheHome,
		configPath: layout.configPath, clientSocketPath: layout.clientSocketPath,
	}
	backend.lookPath = func(string) (string, error) { return admitted.path, nil }
	backend.stageBinary = func(sourcePath string) (string, string, error) {
		if sourcePath != admitted.path {
			return "", "", fmt.Errorf("reopened herdr binary path changed")
		}
		if err := validatePinnedBinary(sourcePath, admitted.sha256, layout); err != nil {
			return "", "", err
		}
		return admitted.path, admitted.sha256, nil
	}
	backend.owner = &ownedAdmission{marker: marker, markerPath: layout.markerPath, lockPath: layout.lifecycleLock}
	return backend
}

//nolint:gocognit,gocyclo,funlen // Admission is one ordered fail-closed transaction; splitting it would obscure cleanup ownership.
func ensureOwned(
	ctx context.Context,
	opts OwnedOptions,
	backend *Backend,
	start supervisorStarter,
	writers ...ownerMarkerWriter,
) (*OwnedSession, error) {
	if ctx == nil {
		return nil, fmt.Errorf("ensure owned herdr session requires a context")
	}
	writeMarker := writeOwnerMarkerExclusive
	if len(writers) > 0 {
		if len(writers) != 1 || writers[0] == nil {
			return nil, fmt.Errorf("ensure owned herdr session received an invalid marker writer")
		}
		writeMarker = writers[0]
	}
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(opts.GitCommonDir)
	if err != nil {
		return nil, err
	}
	session := naming.ManagedSessionName(commonIdentity.device, commonIdentity.inode)
	layout, err := prepareOwnedLayout(opts.RuntimeBase, session)
	if err != nil {
		return nil, err
	}
	if backend == nil {
		backend = New(session, layout.socketPath)
	}
	backend.session = session
	backend.socketPath = layout.socketPath
	backend.control = &controlPlaneEnvironment{
		xdgConfigHome: layout.xdgConfigHome, xdgStateHome: layout.xdgStateHome,
		xdgDataHome: layout.xdgDataHome, xdgCacheHome: layout.xdgCacheHome,
		configPath: layout.configPath, clientSocketPath: layout.clientSocketPath,
	}
	err = ensurePrivateDir(layout.runtimeBase)
	if err != nil {
		return nil, fmt.Errorf("prepare herdr runtime base: %w", err)
	}
	err = ensurePrivateDir(layout.runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("prepare herdr session directory: %w", err)
	}
	lock, err := lockPrivateFileContext(ctx, layout.lifecycleLock)
	if err != nil {
		return nil, fmt.Errorf("lock herdr owned lifecycle: %w", err)
	}
	defer func() { unlockPrivateFile(lock) }()
	err = rejectOwnedServerLifecycle(commonDir)
	if err != nil {
		return nil, err
	}
	err = ensureOwnedLayout(layout)
	if err != nil {
		return nil, err
	}
	marker, found, err := readOwnerMarker(layout.markerPath)
	if err != nil {
		return nil, err
	}
	admitted := binaryAdmission{
		path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
	}
	launcher := binaryAdmission{path: marker.LauncherPath, sha256: marker.LauncherSHA256}
	if !found {
		admitted, err = backend.admitBinaryContext(ctx, route{session: session, socketPath: layout.socketPath})
		if err != nil {
			return nil, err
		}
		launcher, err = pinOwnedLauncher(layout)
		if err != nil {
			return nil, err
		}
		err = ensureOwnedConfig(layout, launcher.path)
		if err != nil {
			return nil, err
		}
		admitted, err = pinOwnedBinary(layout, admitted)
		if err != nil {
			return nil, err
		}
	}
	backend.lookPath = func(string) (string, error) { return admitted.path, nil }
	backend.stageBinary = func(sourcePath string) (string, string, error) {
		return stageExecutable(sourcePath, layout.binaryDir)
	}
	var started *startedSupervisor
	if !found {
		marker, started, err = claimOwnedSession(layout, commonDir, commonIdentity, session, admitted, launcher, start, writeMarker)
	} else {
		err = validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, launcher)
		if err == nil {
			err = verifyLiveSupervisor(layout.supervisorLock, marker)
		}
	}
	if err != nil {
		if started != nil {
			unlockPrivateFile(lock)
			lock = nil
			if stopErr := stopFailedOwnedClaim(layout, started); stopErr != nil {
				return nil, errors.Join(err, fmt.Errorf("stop failed herdr ownership claim: %w", stopErr))
			}
		}
		return nil, err
	}
	backend.owner = &ownedAdmission{marker: marker, markerPath: layout.markerPath, lockPath: layout.lifecycleLock}
	err = waitForOwnedReady(ctx, backend)
	if err != nil {
		if started != nil {
			unlockPrivateFile(lock)
			lock = nil
			if stopErr := stopFreshOwnedSupervisor(layout, marker, started); stopErr != nil {
				return nil, errors.Join(err, fmt.Errorf("stop unready herdr supervisor: %w", stopErr))
			}
		}
		return nil, err
	}
	emitter := launcher
	if found {
		emitter, err = pinOwnedLauncher(layout)
		if err != nil {
			return nil, err
		}
	}
	started.reapAsync()
	return &OwnedSession{
		Session: session, SocketPath: layout.socketPath, ClientSocketPath: layout.clientSocketPath,
		GitCommonDir: commonDir, RuntimeDir: layout.runtimeDir,
		LauncherPath: launcher.path, EmitterPath: emitter.path,
		ControlPath: filepath.Join(commonDir, "fanout", "herdr-intents.json"), backend: backend,
	}, nil
}

//nolint:funlen // The ownership marker is built beside the one supervisor claim whose identity it records.
func claimOwnedSession(
	layout ownedLayout,
	commonDir string,
	commonIdentity pathIdentity,
	session string,
	admitted binaryAdmission,
	launcher binaryAdmission,
	start supervisorStarter,
	writeMarker ownerMarkerWriter,
) (ownerMarker, *startedSupervisor, error) {
	if running, err := inspectSupervisorLease(layout.supervisorLock); err != nil {
		return ownerMarker{}, nil, err
	} else if running {
		return ownerMarker{}, nil, fmt.Errorf("refusing to claim herdr session with a foreign supervisor")
	}
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if _, err := os.Lstat(path); err == nil {
			return ownerMarker{}, nil, fmt.Errorf("refusing to claim herdr session with foreign socket %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return ownerMarker{}, nil, fmt.Errorf("inspect herdr socket %s: %w", path, err)
		}
	}
	nonce, err := randomToken()
	if err != nil {
		return ownerMarker{}, nil, err
	}
	startToken, err := randomToken()
	if err != nil {
		return ownerMarker{}, nil, err
	}
	started, err := start(layout.markerPath, nonce, startToken)
	if err != nil {
		return ownerMarker{}, nil, err
	}
	if started == nil || started.pid <= 1 || started.signal == nil || started.wait == nil {
		return ownerMarker{}, nil, fmt.Errorf("herdr supervisor starter returned an invalid child handle")
	}
	marker := ownerMarker{
		SchemaID: ownedMarkerSchemaID, GitCommonDir: commonDir,
		GitCommonDevice: commonIdentity.device, GitCommonInode: commonIdentity.inode, OwnerNonce: nonce,
		Session: session, RuntimeDir: layout.runtimeDir, SocketPath: layout.socketPath,
		ClientSocketPath: layout.clientSocketPath, BinaryPath: admitted.path,
		BinarySHA256: admitted.sha256, BinaryVersion: admitted.version,
		SupervisorPID: started.pid, SupervisorStartToken: startToken,
		XDGConfigHome: layout.xdgConfigHome, XDGStateHome: layout.xdgStateHome,
		XDGDataHome: layout.xdgDataHome, XDGCacheHome: layout.xdgCacheHome,
		ConfigPath:   layout.configPath,
		LauncherPath: launcher.path, LauncherSHA256: launcher.sha256,
		DashboardTokenSHA256: started.dashboardAuthentication.tokenSHA256,
	}
	if err := writeMarker(layout.markerPath, marker); err != nil {
		return marker, started, err
	}
	return marker, started, nil
}

func stopFailedOwnedClaim(layout ownedLayout, started *startedSupervisor) error {
	stopErr := stopStartedSupervisorGracefully(started)
	return errors.Join(stopErr, validateRetiredOwnedSession(layout))
}

func stopFreshOwnedSupervisor(layout ownedLayout, marker ownerMarker, started *startedSupervisor) error {
	if started == nil || started.pid != marker.SupervisorPID || started.signal == nil || started.wait == nil {
		return fmt.Errorf("unready herdr supervisor child handle does not match ownership marker")
	}
	current, found, err := readOwnerMarker(layout.markerPath)
	if err != nil || !found || current != marker {
		return fmt.Errorf("ownership marker changed before stopping unready supervisor")
	}
	verifyErr := verifyLiveSupervisor(layout.supervisorLock, marker)
	if verifyErr != nil {
		return verifyErr
	}
	stopErr := stopStartedSupervisorGracefully(started)
	return errors.Join(stopErr, validateRetiredOwnedSession(layout))
}

func stopStartedSupervisorGracefully(started *startedSupervisor) error {
	if started == nil || started.pid <= 1 || started.signal == nil || started.wait == nil {
		return fmt.Errorf("herdr supervisor has no valid direct child handle")
	}
	signalErr := started.signal(syscall.SIGTERM)
	if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
		return fmt.Errorf("signal unready herdr supervisor: %w", signalErr)
	}
	waited := make(chan error, 1)
	go func() { waited <- started.wait() }()
	timer := time.NewTimer(ownedShutdownGrace + ownedReadyTimeout)
	defer timer.Stop()
	select {
	case waitErr := <-waited:
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				return nil
			}
			return fmt.Errorf("reap herdr supervisor: %w", waitErr)
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("timed out waiting for herdr supervisor shutdown")
	}
}

func validateRetiredOwnedSession(layout ownedLayout) error {
	if _, found, err := readOwnerMarker(layout.markerPath); err != nil || found {
		if err != nil {
			return err
		}
		return fmt.Errorf("herdr ownership marker remains after supervisor shutdown")
	}
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return fmt.Errorf("herdr owned socket %s remains after supervisor shutdown", path)
		}
	}
	if running, err := inspectSupervisorLease(layout.supervisorLock); err != nil {
		return err
	} else if running {
		return fmt.Errorf("herdr supervisor remains live after shutdown")
	}
	return nil
}

func waitForOwnedReady(ctx context.Context, backend *Backend) error {
	deadline := time.Now().Add(ownedReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		readyErr := validateOwnedReady(ctx, backend)
		if readyErr == nil {
			return nil
		}
		lastErr = readyErr
		timer := time.NewTimer(ownedReadyInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("herdr owned session did not become ready: %w", lastErr)
}

func validateOwnedReady(ctx context.Context, backend *Backend) error {
	if backend == nil || backend.owner == nil {
		return fmt.Errorf("herdr owned readiness requires an ownership admission")
	}
	admission := *backend.owner
	if _, err := backend.probeOwned(ctx, admission); err != nil {
		return err
	}
	marker := admission.marker
	if err := verifyLiveSupervisor(filepath.Join(marker.RuntimeDir, ownedSupervisorLockName), marker); err != nil {
		return err
	}
	if err := validatePrivateSocket(marker.SocketPath); err != nil {
		return err
	}
	return validatePrivateSocket(marker.ClientSocketPath)
}
