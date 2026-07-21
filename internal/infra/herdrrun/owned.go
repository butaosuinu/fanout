package herdrrun

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
)

const (
	ownedMarkerSchema       = 1
	ownedLeaseSchema        = 1
	ownedMarkerName         = "owner.json"
	ownedLifecycleLockName  = "lifecycle.lock"
	ownedSupervisorLockName = "supervisor.lock"
	ownedSupervisorCommand  = "__herdr-supervisor"
	ownedReadyTimeout       = 5 * time.Second
	ownedReadyInterval      = 50 * time.Millisecond
	ownedLockRetryInterval  = 25 * time.Millisecond
	ownedShutdownGrace      = 2 * time.Second
	ownedShutdownKillWait   = 2 * time.Second
	maxUnixSocketPathBytes  = 103
	ownedConfigContents     = ""
	ownedServerLogName      = "supervisor.log"

	configEnv       = "HERDR_CONFIG_PATH"
	clientSocketEnv = "HERDR_CLIENT_SOCKET_PATH"
	xdgConfigEnv    = "XDG_CONFIG_HOME"
	xdgStateEnv     = "XDG_STATE_HOME"
	xdgDataEnv      = "XDG_DATA_HOME"
	xdgCacheEnv     = "XDG_CACHE_HOME"
)

// OwnedOptions identifies the repository and optional private runtime base for
// one fanout-owned herdr session. GitCommonDir is canonicalized before naming
// and persisted in the ownership marker. RuntimeBase is primarily a test seam;
// production callers normally leave it empty.
type OwnedOptions struct {
	GitCommonDir string
	RuntimeBase  string
}

// OwnedSession is the admitted per-repository herdr session. Backend is the
// only backend instance authorized to use mutation primitives for this marker.
type OwnedSession struct {
	Session          string
	SocketPath       string
	ClientSocketPath string

	backend *Backend
}

// Backend returns the runtime adapter bound to this immutable admission.
func (s *OwnedSession) Backend() *Backend {
	if s == nil {
		return nil
	}
	return s.backend
}

// AttachCommand returns a shell-quoted, argument-free herdr command routed to
// the owned socket. It deliberately never emits `session attach` or --session.
func (s *OwnedSession) AttachCommand() (string, error) {
	if s == nil || s.backend == nil {
		return "", fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(context.Background())
	if err != nil {
		return "", err
	}
	defer unlockPrivateFile(lock)
	if _, err := s.backend.probeOwned(context.Background(), admission); err != nil {
		return "", fmt.Errorf("verify herdr owned session before attach: %w", err)
	}
	marker := admission.marker
	assignments := [][2]string{
		{xdgConfigEnv, marker.XDGConfigHome},
		{xdgStateEnv, marker.XDGStateHome},
		{xdgDataEnv, marker.XDGDataHome},
		{xdgCacheEnv, marker.XDGCacheHome},
		{configEnv, marker.ConfigPath},
		{sessionEnv, marker.Session},
		{socketEnv, marker.SocketPath},
		{clientSocketEnv, marker.ClientSocketPath},
	}
	parts := make([]string, 0, len(assignments)+1)
	for _, assignment := range assignments {
		parts = append(parts, assignment[0]+"="+shellQuote(assignment[1]))
	}
	parts = append(parts, shellQuote(marker.BinaryPath))
	return strings.Join(parts, " "), nil
}

type controlPlaneEnvironment struct {
	xdgConfigHome    string
	xdgStateHome     string
	xdgDataHome      string
	xdgCacheHome     string
	configPath       string
	clientSocketPath string
}

type ownerMarker struct {
	SchemaVersion        int    `json:"schema_version"`
	GitCommonDir         string `json:"git_common_dir"`
	OwnerNonce           string `json:"owner_nonce"`
	Session              string `json:"session"`
	RuntimeDir           string `json:"runtime_dir"`
	SocketPath           string `json:"socket_path"`
	ClientSocketPath     string `json:"client_socket_path"`
	BinaryPath           string `json:"binary_path"`
	BinarySHA256         string `json:"binary_sha256"`
	BinaryVersion        string `json:"binary_version"`
	SupervisorPID        int    `json:"supervisor_pid"`
	SupervisorStartToken string `json:"supervisor_start_token"`
	XDGConfigHome        string `json:"xdg_config_home"`
	XDGStateHome         string `json:"xdg_state_home"`
	XDGDataHome          string `json:"xdg_data_home"`
	XDGCacheHome         string `json:"xdg_cache_home"`
	ConfigPath           string `json:"config_path"`
}

type ownedAdmission struct {
	marker     ownerMarker
	markerPath string
	lockPath   string
}

type supervisorLease struct {
	SchemaVersion int    `json:"schema_version"`
	OwnerNonce    string `json:"owner_nonce"`
	StartToken    string `json:"start_token"`
	PID           int    `json:"pid"`
}

type ownedLayout struct {
	runtimeBase      string
	runtimeDir       string
	markerPath       string
	lifecycleLock    string
	supervisorLock   string
	socketPath       string
	clientSocketPath string
	xdgConfigHome    string
	xdgStateHome     string
	xdgDataHome      string
	xdgCacheHome     string
	configPath       string
}

type supervisorStarter func(markerPath, nonce, startToken string) (int, error)

// EnsureOwned creates or idempotently re-adopts the fanout-owned herdr session
// for one canonical git common directory.
func EnsureOwned(ctx context.Context, opts OwnedOptions) (*OwnedSession, error) {
	return ensureOwned(ctx, opts, nil, startOwnedSupervisor)
}

func ensureOwned(ctx context.Context, opts OwnedOptions, backend *Backend, start supervisorStarter) (*OwnedSession, error) {
	if ctx == nil {
		return nil, fmt.Errorf("ensure owned herdr session requires a context")
	}
	commonDir, err := canonicalGitCommonDir(opts.GitCommonDir)
	if err != nil {
		return nil, err
	}
	session := naming.HerdrSessionName(commonDir)
	layout, err := prepareOwnedLayout(opts.RuntimeBase, session)
	if err != nil {
		return nil, err
	}
	// The lifecycle lock lives below the private session directory. Create and
	// validate only those two ancestors before locking; every other owned path
	// is prepared while the lock is held.
	if baseErr := ensurePrivateDir(layout.runtimeBase); baseErr != nil {
		return nil, fmt.Errorf("prepare herdr runtime base: %w", baseErr)
	}
	if dirErr := ensurePrivateDir(layout.runtimeDir); dirErr != nil {
		return nil, fmt.Errorf("prepare herdr session directory: %w", dirErr)
	}
	lock, err := lockPrivateFileContext(ctx, layout.lifecycleLock)
	if err != nil {
		return nil, fmt.Errorf("lock herdr owned lifecycle: %w", err)
	}
	defer unlockPrivateFile(lock)

	if setupErr := ensureOwnedDirectories(layout); setupErr != nil {
		return nil, setupErr
	}
	if backend == nil {
		backend = New(session, layout.socketPath)
	}
	backend.session = session
	backend.socketPath = layout.socketPath
	backend.control = &controlPlaneEnvironment{
		xdgConfigHome:    layout.xdgConfigHome,
		xdgStateHome:     layout.xdgStateHome,
		xdgDataHome:      layout.xdgDataHome,
		xdgCacheHome:     layout.xdgCacheHome,
		configPath:       layout.configPath,
		clientSocketPath: layout.clientSocketPath,
	}

	admitted, err := backend.admitBinaryContext(ctx, route{session: session, socketPath: layout.socketPath})
	if err != nil {
		return nil, err
	}
	marker, found, err := readOwnerMarker(layout.markerPath)
	if err != nil {
		return nil, err
	}
	if !found {
		marker, err = claimOwnedSession(layout, commonDir, session, admitted, start)
		if err != nil {
			return nil, err
		}
	} else {
		marker, err = reconcileOwnedSession(layout, commonDir, session, admitted, marker, start)
		if err != nil {
			return nil, err
		}
	}

	backend.owner = &ownedAdmission{marker: marker, markerPath: layout.markerPath, lockPath: layout.lifecycleLock}
	if err := waitForOwnedReady(ctx, backend); err != nil {
		return nil, err
	}
	if err := backend.verifyOwnedBinding(); err != nil {
		return nil, err
	}
	return &OwnedSession{
		Session:          session,
		SocketPath:       layout.socketPath,
		ClientSocketPath: layout.clientSocketPath,
		backend:          backend,
	}, nil
}

func claimOwnedSession(layout ownedLayout, commonDir, session string, admitted binaryAdmission, start supervisorStarter) (ownerMarker, error) {
	if _, running, err := inspectSupervisorLease(layout.supervisorLock); err != nil {
		return ownerMarker{}, fmt.Errorf("inspect unclaimed herdr supervisor: %w", err)
	} else if running {
		return ownerMarker{}, fmt.Errorf("refusing to claim herdr session with a foreign supervisor")
	}
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if _, err := os.Lstat(path); err == nil {
			return ownerMarker{}, fmt.Errorf("refusing to claim herdr session with foreign socket %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return ownerMarker{}, fmt.Errorf("inspect herdr socket %s: %w", path, err)
		}
	}
	nonce, err := randomToken()
	if err != nil {
		return ownerMarker{}, fmt.Errorf("generate herdr owner nonce: %w", err)
	}
	startToken, err := randomToken()
	if err != nil {
		return ownerMarker{}, fmt.Errorf("generate herdr supervisor token: %w", err)
	}
	pid, err := start(layout.markerPath, nonce, startToken)
	if err != nil {
		return ownerMarker{}, err
	}
	marker := markerFor(layout, commonDir, session, admitted, nonce, pid, startToken)
	if err := writeOwnerMarkerExclusive(layout.markerPath, marker); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return ownerMarker{}, err
	}
	return marker, nil
}

func reconcileOwnedSession(layout ownedLayout, commonDir, session string, admitted binaryAdmission, marker ownerMarker, start supervisorStarter) (ownerMarker, error) {
	if err := validateMarkerLayout(marker, layout, commonDir, session); err != nil {
		return ownerMarker{}, err
	}
	lease, running, err := inspectSupervisorLease(layout.supervisorLock)
	if err != nil {
		return ownerMarker{}, err
	}
	if running {
		if leaseErr := validateSupervisorLease(marker, lease); leaseErr != nil {
			return ownerMarker{}, leaseErr
		}
		if marker.BinaryPath != admitted.path || marker.BinarySHA256 != admitted.sha256 || marker.BinaryVersion != admitted.version {
			return ownerMarker{}, fmt.Errorf("herdr owned session binary drift: marker=%s/%s current=%s/%s", marker.BinaryPath, marker.BinarySHA256, admitted.path, admitted.sha256)
		}
		return marker, nil
	}
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if _, statErr := os.Lstat(path); statErr == nil {
			return ownerMarker{}, fmt.Errorf("herdr owned socket %s exists without its verified supervisor; refusing foreign socket", path)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return ownerMarker{}, fmt.Errorf("inspect stopped herdr socket %s: %w", path, statErr)
		}
	}
	startToken, err := randomToken()
	if err != nil {
		return ownerMarker{}, fmt.Errorf("rotate herdr supervisor token: %w", err)
	}
	pid, err := start(layout.markerPath, marker.OwnerNonce, startToken)
	if err != nil {
		return ownerMarker{}, err
	}
	updated := markerFor(layout, commonDir, session, admitted, marker.OwnerNonce, pid, startToken)
	if err := replaceOwnerMarker(layout.markerPath, marker, updated); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return ownerMarker{}, err
	}
	return updated, nil
}

func markerFor(layout ownedLayout, commonDir, session string, admitted binaryAdmission, nonce string, pid int, startToken string) ownerMarker {
	return ownerMarker{
		SchemaVersion:        ownedMarkerSchema,
		GitCommonDir:         commonDir,
		OwnerNonce:           nonce,
		Session:              session,
		RuntimeDir:           layout.runtimeDir,
		SocketPath:           layout.socketPath,
		ClientSocketPath:     layout.clientSocketPath,
		BinaryPath:           admitted.path,
		BinarySHA256:         admitted.sha256,
		BinaryVersion:        admitted.version,
		SupervisorPID:        pid,
		SupervisorStartToken: startToken,
		XDGConfigHome:        layout.xdgConfigHome,
		XDGStateHome:         layout.xdgStateHome,
		XDGDataHome:          layout.xdgDataHome,
		XDGCacheHome:         layout.xdgCacheHome,
		ConfigPath:           layout.configPath,
	}
}

func waitForOwnedReady(ctx context.Context, backend *Backend) error {
	readyCtx, cancel := context.WithTimeout(ctx, ownedReadyTimeout)
	defer cancel()
	var lastErr error
	for {
		if err := readyCtx.Err(); err != nil {
			if parentErr := ctx.Err(); parentErr != nil {
				return parentErr
			}
			if lastErr == nil {
				lastErr = err
			}
			return fmt.Errorf("herdr owned session %q did not become ready: %w", backend.session, lastErr)
		}
		lease, running, err := inspectSupervisorLease(filepath.Join(backend.owner.marker.RuntimeDir, ownedSupervisorLockName))
		if err != nil {
			lastErr = err
		} else if !running {
			return fmt.Errorf("herdr owned supervisor exited before session became ready")
		} else if err := validateSupervisorLease(backend.owner.marker, lease); err != nil {
			lastErr = err
		} else if err := validatePrivateSocket(backend.owner.marker.SocketPath); err != nil {
			lastErr = err
		} else if _, err := backend.probeOwned(readyCtx, *backend.owner); err == nil {
			return nil
		} else {
			lastErr = err
		}
		timer := time.NewTimer(ownedReadyInterval)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			continue
		case <-timer.C:
		}
	}
}

func (b *Backend) verifyOwnedBinding() error {
	if b == nil || b.owner == nil {
		return fmt.Errorf("herdr mutation requires a fanout-owned session")
	}
	marker, found, err := readOwnerMarker(b.owner.markerPath)
	if err != nil {
		return err
	}
	if !found || marker != b.owner.marker {
		return fmt.Errorf("herdr ownership marker changed; refusing operation")
	}
	if b.session != marker.Session || b.socketPath != marker.SocketPath || b.control == nil ||
		b.control.xdgConfigHome != marker.XDGConfigHome || b.control.xdgStateHome != marker.XDGStateHome ||
		b.control.xdgDataHome != marker.XDGDataHome || b.control.xdgCacheHome != marker.XDGCacheHome ||
		b.control.configPath != marker.ConfigPath || b.control.clientSocketPath != marker.ClientSocketPath {
		return fmt.Errorf("herdr owned backend route changed; refusing operation")
	}
	for _, dir := range []string{marker.RuntimeDir, marker.XDGConfigHome, marker.XDGStateHome, marker.XDGDataHome, marker.XDGCacheHome, filepath.Dir(marker.ConfigPath)} {
		if dirErr := validatePrivateDir(dir); dirErr != nil {
			return dirErr
		}
	}
	if fileErr := validatePrivateFile(marker.ConfigPath); fileErr != nil {
		return fileErr
	}
	hash, err := b.hashFile(marker.BinaryPath)
	if err != nil {
		return fmt.Errorf("hash herdr owned binary: %w", err)
	}
	if hash != marker.BinarySHA256 {
		return fmt.Errorf("herdr owned binary identity changed")
	}
	if socketErr := validatePrivateSocket(marker.SocketPath); socketErr != nil {
		return socketErr
	}
	if _, statErr := os.Lstat(marker.ClientSocketPath); statErr == nil {
		if socketErr := validatePrivateSocket(marker.ClientSocketPath); socketErr != nil {
			return socketErr
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	lease, running, err := inspectSupervisorLease(filepath.Join(marker.RuntimeDir, ownedSupervisorLockName))
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("herdr owned supervisor identity is no longer live")
	}
	if err := validateSupervisorLease(marker, lease); err != nil {
		return err
	}
	if syscall.Kill(marker.SupervisorPID, 0) != nil {
		return fmt.Errorf("herdr owned supervisor process is no longer live")
	}
	return nil
}

func canonicalGitCommonDir(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("herdr owned session requires a git common directory")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve git common directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("canonicalize git common directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect git common directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("git common directory %s is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

func prepareOwnedLayout(runtimeBase, session string) (ownedLayout, error) {
	if err := validateSessionName(session); err != nil {
		return ownedLayout{}, err
	}
	if runtimeBase == "" {
		runtimeBase = filepath.Join(string(os.PathSeparator), "tmp", "fanout-herdr-"+strconv.Itoa(os.Getuid()))
	}
	abs, err := filepath.Abs(runtimeBase)
	if err != nil {
		return ownedLayout{}, fmt.Errorf("resolve herdr runtime base: %w", err)
	}
	runtimeDir := filepath.Join(filepath.Clean(abs), session)
	configHome := filepath.Join(runtimeDir, "xdg-config")
	layout := ownedLayout{
		runtimeBase:      filepath.Clean(abs),
		runtimeDir:       runtimeDir,
		markerPath:       filepath.Join(runtimeDir, ownedMarkerName),
		lifecycleLock:    filepath.Join(runtimeDir, ownedLifecycleLockName),
		supervisorLock:   filepath.Join(runtimeDir, ownedSupervisorLockName),
		socketPath:       filepath.Join(runtimeDir, "herdr.sock"),
		clientSocketPath: filepath.Join(runtimeDir, "herdr-client.sock"),
		xdgConfigHome:    configHome,
		xdgStateHome:     filepath.Join(runtimeDir, "xdg-state"),
		xdgDataHome:      filepath.Join(runtimeDir, "xdg-data"),
		xdgCacheHome:     filepath.Join(runtimeDir, "xdg-cache"),
		configPath:       filepath.Join(configHome, "herdr", "config.toml"),
	}
	for _, socketPath := range []string{layout.socketPath, layout.clientSocketPath} {
		if len(socketPath) > maxUnixSocketPathBytes {
			return ownedLayout{}, fmt.Errorf(
				"herdr owned socket path is %d bytes, want at most %d: %s",
				len(socketPath),
				maxUnixSocketPathBytes,
				socketPath,
			)
		}
	}
	return layout, nil
}

func ensureOwnedDirectories(layout ownedLayout) error {
	if err := ensurePrivateDir(layout.runtimeBase); err != nil {
		return fmt.Errorf("prepare herdr runtime base: %w", err)
	}
	for _, dir := range []string{layout.runtimeDir, layout.xdgConfigHome, layout.xdgStateHome, layout.xdgDataHome, layout.xdgCacheHome, filepath.Dir(layout.configPath)} {
		if err := ensurePrivateDir(dir); err != nil {
			return fmt.Errorf("prepare herdr owned directory: %w", err)
		}
	}
	if err := ensurePrivateConfig(layout.configPath); err != nil {
		return err
	}
	return nil
}

func ensurePrivateDir(path string) error {
	err := os.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return validatePrivateDir(path)
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("herdr owned path %s is not a real directory", path)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("herdr owned directory %s has mode %04o, want 0700", path, info.Mode().Perm())
	}
	return validateOwnerUID(path, info)
}

func ensurePrivateConfig(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create herdr owned config: %w", err)
	}
	info, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil {
		return statErr
	}
	if closeErr != nil {
		return closeErr
	}
	if validateErr := validatePrivateRegular(path, info); validateErr != nil {
		return validateErr
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(data) != ownedConfigContents {
		return fmt.Errorf("herdr owned config %s drifted from the empty fanout config", path)
	}
	return nil
}

func validatePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private file %s: %w", path, err)
	}
	return validatePrivateRegular(path, info)
}

func validatePrivateRegular(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("herdr owned path %s is not a regular file", path)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("herdr owned file %s has mode %04o, want 0600", path, info.Mode().Perm())
	}
	return validateOwnerUID(path, info)
}

func validatePrivateSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect herdr owned socket %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("herdr owned socket path %s is not a Unix socket", path)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("herdr owned socket %s has mode %04o, want 0600", path, info.Mode().Perm())
	}
	return validateOwnerUID(path, info)
}

func validateOwnerUID(path string, info os.FileInfo) error {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("herdr owned path %s belongs to uid %d, want %d", path, stat.Uid, os.Getuid())
	}
	return nil
}

func lockPrivateFile(path string) (*os.File, error) {
	return lockPrivateFileContext(context.Background(), path)
}

func lockPrivateFileContext(ctx context.Context, path string) (*os.File, error) {
	if ctx == nil {
		return nil, fmt.Errorf("lock private file requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if validateErr := validatePrivateRegular(path, info); validateErr != nil {
		_ = f.Close()
		return nil, validateErr
	}
	for {
		flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			if err := ctx.Err(); err != nil {
				unlockPrivateFile(f)
				return nil, err
			}
			return f, nil
		}
		if !errors.Is(flockErr, syscall.EWOULDBLOCK) && !errors.Is(flockErr, syscall.EAGAIN) {
			_ = f.Close()
			return nil, flockErr
		}

		timer := time.NewTimer(ownedLockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func tryLockPrivateFile(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	if validateErr := validatePrivateRegular(path, info); validateErr != nil {
		_ = f.Close()
		return nil, false, validateErr
	}
	if flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		_ = f.Close()
		if errors.Is(flockErr, syscall.EWOULDBLOCK) || errors.Is(flockErr, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, flockErr
	}
	return f, true, nil
}

func unlockPrivateFile(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func inspectSupervisorLease(path string) (supervisorLease, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return supervisorLease{}, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return supervisorLease{}, false, err
	}
	if validateErr := validatePrivateRegular(path, info); validateErr != nil {
		return supervisorLease{}, false, validateErr
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return supervisorLease{}, false, nil
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		return supervisorLease{}, false, err
	}
	var lease supervisorLease
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lease); err != nil {
		return supervisorLease{}, true, fmt.Errorf("read herdr supervisor lease: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return supervisorLease{}, true, fmt.Errorf("read herdr supervisor lease: unexpected trailing JSON value")
		}
		return supervisorLease{}, true, fmt.Errorf("read herdr supervisor lease: %w", err)
	}
	return lease, true, nil
}

func validateSupervisorLease(marker ownerMarker, lease supervisorLease) error {
	if lease.SchemaVersion != ownedLeaseSchema || lease.OwnerNonce != marker.OwnerNonce ||
		lease.StartToken != marker.SupervisorStartToken || lease.PID != marker.SupervisorPID {
		return fmt.Errorf("herdr supervisor lease does not match the ownership marker")
	}
	return nil
}

func writeSupervisorLease(f *os.File, marker ownerMarker) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	lease := supervisorLease{
		SchemaVersion: ownedLeaseSchema,
		OwnerNonce:    marker.OwnerNonce,
		StartToken:    marker.SupervisorStartToken,
		PID:           marker.SupervisorPID,
	}
	if err := json.NewEncoder(f).Encode(lease); err != nil {
		return err
	}
	return f.Sync()
}

func readOwnerMarker(path string) (ownerMarker, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return ownerMarker{}, false, nil
	}
	if err != nil {
		return ownerMarker{}, false, fmt.Errorf("open herdr ownership marker: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ownerMarker{}, true, err
	}
	if err := validatePrivateRegular(path, info); err != nil {
		return ownerMarker{}, true, err
	}
	var marker ownerMarker
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return ownerMarker{}, true, fmt.Errorf("parse herdr ownership marker: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ownerMarker{}, true, fmt.Errorf("parse herdr ownership marker: unexpected trailing JSON value")
		}
		return ownerMarker{}, true, fmt.Errorf("parse herdr ownership marker: %w", err)
	}
	return marker, true, nil
}

func writeOwnerMarkerExclusive(path string, marker ownerMarker) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".owner-*.tmp")
	if err != nil {
		return fmt.Errorf("prepare herdr ownership marker: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }() // Private staging file is never authoritative.
	if err := writeMarkerFile(temporaryPath, temporary, marker); err != nil {
		return err
	}
	// Linking a fully synced private inode publishes the initial marker in one
	// step while preserving O_EXCL semantics if another owner already claimed it.
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("claim herdr ownership marker: %w", err)
	}
	return validatePrivateFile(path)
}

func replaceOwnerMarker(path string, old, next ownerMarker) error {
	current, found, err := readOwnerMarker(path)
	if err != nil {
		return err
	}
	if !found || current != old {
		return fmt.Errorf("herdr ownership marker changed during reconciliation")
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfs.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("replace herdr ownership marker: %w", err)
	}
	return validatePrivateFile(path)
}

func writeMarkerFile(path string, f *os.File, marker ownerMarker) (returnErr error) {
	defer func() { returnErr = errors.Join(returnErr, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := validatePrivateRegular(path, info); err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(marker); err != nil {
		return err
	}
	return f.Sync()
}

func validateMarkerLayout(marker ownerMarker, layout ownedLayout, commonDir, session string) error {
	if marker.SchemaVersion != ownedMarkerSchema || marker.GitCommonDir != commonDir || marker.Session != session ||
		marker.RuntimeDir != layout.runtimeDir || marker.SocketPath != layout.socketPath || marker.ClientSocketPath != layout.clientSocketPath ||
		marker.XDGConfigHome != layout.xdgConfigHome || marker.XDGStateHome != layout.xdgStateHome || marker.XDGDataHome != layout.xdgDataHome ||
		marker.XDGCacheHome != layout.xdgCacheHome || marker.ConfigPath != layout.configPath {
		return fmt.Errorf("herdr ownership marker does not match this repository and runtime layout")
	}
	if !validHexToken(marker.OwnerNonce) || !validHexToken(marker.SupervisorStartToken) || marker.SupervisorPID <= 1 ||
		!validHexToken(marker.BinarySHA256) || !filepath.IsAbs(marker.BinaryPath) || validateAdmittedVersion(marker.BinaryVersion) != nil {
		return fmt.Errorf("herdr ownership marker has incomplete identity")
	}
	return nil
}

func validHexToken(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func startOwnedSupervisor(markerPath, nonce, startToken string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("resolve fanout executable for herdr supervisor: %w", err)
	}
	logPath := filepath.Join(filepath.Dir(markerPath), ownedServerLogName)
	logFile, err := openPrivateAppendFile(logPath)
	if err != nil {
		return 0, fmt.Errorf("open herdr supervisor log: %w", err)
	}
	cmd := exec.Command(exe, ownedSupervisorCommand, markerPath, nonce, startToken)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Dir = filepath.Dir(markerPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start herdr supervisor: %w", err)
	}
	_ = logFile.Close()
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// IsSupervisorRequest reports whether args target the hidden owned herdr
// supervisor command.
func IsSupervisorRequest(args []string) bool {
	return len(args) > 0 && args[0] == ownedSupervisorCommand
}

// RunSupervisor owns one foreground `herdr server` child until it exits.
func RunSupervisor(args []string, errw io.Writer) int {
	if len(args) != 3 {
		fmt.Fprintln(errw, "fanout herdr supervisor: expected marker path, nonce, and start token")
		return 2
	}
	markerPath, nonce, startToken := args[0], args[1], args[2]
	if !filepath.IsAbs(markerPath) || filepath.Clean(markerPath) != markerPath || filepath.Base(markerPath) != ownedMarkerName ||
		!validHexToken(nonce) || !validHexToken(startToken) {
		fmt.Fprintln(errw, "fanout herdr supervisor: invalid marker path, nonce, or start token")
		return 2
	}
	runtimeDir := filepath.Dir(markerPath)
	if err := validatePrivateDir(runtimeDir); err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: runtime directory: %v\n", err)
		return 1
	}
	lock, acquired, err := tryLockPrivateFile(filepath.Join(runtimeDir, ownedSupervisorLockName))
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: lock: %v\n", err)
		return 1
	}
	if !acquired {
		fmt.Fprintln(errw, "fanout herdr supervisor: another supervisor owns this session")
		return 1
	}
	defer unlockPrivateFile(lock)

	var marker ownerMarker
	deadline := time.Now().Add(commandTimeout)
	for time.Now().Before(deadline) {
		loaded, found, readErr := readOwnerMarker(markerPath)
		if readErr == nil && found && loaded.OwnerNonce == nonce && loaded.SupervisorStartToken == startToken && loaded.SupervisorPID == os.Getpid() {
			marker = loaded
			break
		}
		if readErr != nil {
			fmt.Fprintf(errw, "fanout herdr supervisor: marker: %v\n", readErr)
			return 1
		}
		time.Sleep(ownedReadyInterval)
	}
	if marker.OwnerNonce == "" {
		fmt.Fprintln(errw, "fanout herdr supervisor: timed out waiting for matching ownership marker")
		return 1
	}
	if markerErr := validateSupervisorMarker(markerPath, marker); markerErr != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: marker identity: %v\n", markerErr)
		return 1
	}
	hash, err := sha256File(marker.BinaryPath)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: hash binary: %v\n", err)
		return 1
	}
	if hash != marker.BinarySHA256 {
		fmt.Fprintln(errw, "fanout herdr supervisor: binary identity mismatch")
		return 1
	}
	if leaseErr := writeSupervisorLease(lock, marker); leaseErr != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: write lease: %v\n", leaseErr)
		return 1
	}

	logFile, err := openPrivateAppendFile(filepath.Join(runtimeDir, ownedServerLogName))
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: open log: %v\n", err)
		return 1
	}
	defer func() { _ = logFile.Close() }()
	_ = syscall.Umask(0o077)
	cmd := exec.Command(marker.BinaryPath, "server")
	cmd.Env = ownedMarkerEnvironment(marker)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Dir = runtimeDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: start server: %v\n", err)
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return finishOwnedServerExit(cmd, signals, err, errw)
	case sig := <-signals:
		return shutdownOwnedServer(cmd, done, signals, sig, errw)
	}
}

func finishOwnedServerExit(cmd *exec.Cmd, signals <-chan os.Signal, serverErr error, errw io.Writer) int {
	cleanupCode := 0
	if err := stopResidualOwnedProcessGroup(cmd.Process.Pid, signals); err != nil {
		cleanupCode = reportOwnedShutdown([]error{err}, errw)
	}
	if serverErr != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: server exited: %v\n", serverErr)
		return 1
	}
	return cleanupCode
}

func shutdownOwnedServer(
	cmd *exec.Cmd,
	done <-chan error,
	signals <-chan os.Signal,
	firstSignal os.Signal,
	errw io.Writer,
) int {
	var cleanupErrors []error
	if err := signalOwnedProcessGroup(cmd, firstSignal); err != nil && !errors.Is(err, os.ErrProcessDone) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("forward %s to server process group: %w", firstSignal, err))
	}

	graceTimer := time.NewTimer(ownedShutdownGrace)
	defer graceTimer.Stop()
	waited := false
	force := false
	for !waited && !force {
		select {
		case <-done:
			waited = true
		case <-graceTimer.C:
			force = true
		case <-signals:
			force = true
		}
	}

	if force {
		if err := killCommandProcessGroup(cmd); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if !waited {
		killTimer := time.NewTimer(ownedShutdownKillWait)
		defer killTimer.Stop()
		for !waited {
			select {
			case <-done:
				waited = true
			case <-killTimer.C:
				cleanupErrors = append(cleanupErrors, fmt.Errorf("server did not exit within %s after SIGKILL", ownedShutdownKillWait))
				return reportOwnedShutdown(cleanupErrors, errw)
			case <-signals:
				if err := killCommandProcessGroup(cmd); err != nil && !errors.Is(err, os.ErrProcessDone) {
					cleanupErrors = append(cleanupErrors, err)
				}
			}
		}
	}

	// A server can exit while a worker in its process group ignores the graceful
	// signal. Kill and verify the residual group after the direct child is reaped.
	if err := stopResidualOwnedProcessGroup(cmd.Process.Pid, signals); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	// A signal-requested shutdown is successful once cleanup is confirmed; the
	// direct child's signal exit status is expected and intentionally ignored.
	return reportOwnedShutdown(cleanupErrors, errw)
}

func signalOwnedProcessGroup(cmd *exec.Cmd, signalValue os.Signal) error {
	sig, ok := signalValue.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported process signal %T", signalValue)
	}
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	return killCommandProcessTree(
		func() error { return syscall.Kill(-cmd.Process.Pid, sig) },
		func() error { return cmd.Process.Signal(sig) },
	)
}

func stopResidualOwnedProcessGroup(pid int, signals <-chan os.Signal) error {
	running, err := ownedProcessGroupRunning(pid)
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	var permissionErr error
	killGroup := func(action string) (bool, error) {
		err := syscall.Kill(-pid, syscall.SIGKILL)
		switch {
		case err == nil:
			permissionErr = nil
			return false, nil
		case errors.Is(err, syscall.ESRCH):
			return true, nil
		case errors.Is(err, syscall.EPERM):
			// As with a signal-0 probe, Darwin can report EPERM while the
			// killed group is being reaped. Keep it as a pending error and
			// require a later ESRCH before reporting successful cleanup.
			permissionErr = fmt.Errorf("%s: %w", action, err)
			return false, nil
		default:
			return false, fmt.Errorf("%s: %w", action, err)
		}
	}
	if gone, err := killGroup("kill residual herdr server process group"); err != nil {
		return err
	} else if gone {
		return nil
	}

	deadline := time.NewTimer(ownedShutdownKillWait)
	defer deadline.Stop()
	poll := time.NewTicker(ownedLockRetryInterval)
	defer poll.Stop()
	for {
		select {
		case <-deadline.C:
			running, err := ownedProcessGroupRunning(pid)
			if err != nil {
				return err
			}
			if !running {
				return nil
			}
			timeoutErr := fmt.Errorf("herdr server process group did not exit within %s after SIGKILL", ownedShutdownKillWait)
			return errors.Join(timeoutErr, permissionErr)
		case <-signals:
			if gone, err := killGroup("re-kill residual herdr server process group"); err != nil {
				return err
			} else if gone {
				return nil
			}
		case <-poll.C:
			running, err := ownedProcessGroupRunning(pid)
			if err != nil {
				return err
			}
			if !running {
				return nil
			}
		}
	}
}

func ownedProcessGroupRunning(pid int) (bool, error) {
	if pid <= 1 {
		return false, fmt.Errorf("invalid herdr server process group id %d", pid)
	}
	running, err := classifyOwnedProcessGroupProbe(syscall.Kill(-pid, 0))
	if err != nil {
		return false, fmt.Errorf("inspect herdr server process group: %w", err)
	}
	return running, nil
}

func classifyOwnedProcessGroupProbe(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	// Darwin can transiently return EPERM while a SIGKILLed process group is
	// being reaped. EPERM still proves the group exists, so keep polling (and
	// never report cleanup success) until the probe returns ESRCH.
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, err
}

func reportOwnedShutdown(cleanupErrors []error, errw io.Writer) int {
	if err := errors.Join(cleanupErrors...); err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: shutdown cleanup: %v\n", err)
		return 1
	}
	return 0
}

func openPrivateAppendFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := validatePrivateRegular(path, info); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func validateSupervisorMarker(markerPath string, marker ownerMarker) error {
	runtimeDir := filepath.Dir(markerPath)
	layout, err := prepareOwnedLayout(filepath.Dir(runtimeDir), marker.Session)
	if err != nil {
		return err
	}
	commonDir, err := canonicalGitCommonDir(marker.GitCommonDir)
	if err != nil {
		return err
	}
	if err := validateMarkerLayout(marker, layout, commonDir, marker.Session); err != nil {
		return err
	}
	for _, dir := range []string{
		marker.RuntimeDir,
		marker.XDGConfigHome,
		marker.XDGStateHome,
		marker.XDGDataHome,
		marker.XDGCacheHome,
		filepath.Dir(marker.ConfigPath),
	} {
		if err := validatePrivateDir(dir); err != nil {
			return err
		}
	}
	return validatePrivateFile(marker.ConfigPath)
}

func ownedMarkerEnvironment(marker ownerMarker) []string {
	control := &controlPlaneEnvironment{
		xdgConfigHome:    marker.XDGConfigHome,
		xdgStateHome:     marker.XDGStateHome,
		xdgDataHome:      marker.XDGDataHome,
		xdgCacheHome:     marker.XDGCacheHome,
		configPath:       marker.ConfigPath,
		clientSocketPath: marker.ClientSocketPath,
	}
	return routeEnvironment(route{session: marker.Session, socketPath: marker.SocketPath}, control)
}
