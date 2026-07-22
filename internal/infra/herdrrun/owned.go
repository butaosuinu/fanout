package herdrrun

import (
	"bytes"
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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/core/naming"
)

const (
	ownedMarkerSchemaID     = "fanout.herdr-owner.v1"
	ownedMarkerName         = "owner.json"
	ownedLifecycleLockName  = "lifecycle.lock"
	ownedSupervisorLockName = "supervisor.lock"
	ownedSupervisorLogName  = "supervisor.log"
	ownedSupervisorCommand  = "__herdr-supervisor"
	ownedSupervisorReadyFD  = 3
	ownedSupervisorReadyACK = "L"
	ownedReadyTimeout       = 5 * time.Second
	ownedReadyInterval      = 50 * time.Millisecond
	ownedShutdownGrace      = 2 * time.Second
	maxOwnerMarkerBytes     = 64 << 10
	maxUnixSocketPathBytes  = 103
	defaultRuntimeParent    = "/tmp"
	ownedConfigContents     = "[update]\nmanifest_check = false\n"
	configEnv               = "HERDR_CONFIG_PATH"
	clientSocketEnv         = "HERDR_CLIENT_SOCKET_PATH"
	xdgConfigEnv            = "XDG_CONFIG_HOME"
	xdgStateEnv             = "XDG_STATE_HOME"
	xdgDataEnv              = "XDG_DATA_HOME"
	xdgCacheEnv             = "XDG_CACHE_HOME"
)

var errOwnedSupervisorNotRunning = errors.New("herdr owned supervisor is not running; refusing automatic recovery without proof that prior operations are quiescent")

type OwnedOptions struct {
	GitCommonDir string
	RuntimeBase  string
}

type OwnedSession struct {
	Session          string
	SocketPath       string
	ClientSocketPath string

	backend *Backend
}

func (s *OwnedSession) Backend() *Backend {
	if s == nil {
		return nil
	}
	return s.backend
}

func (s *OwnedSession) AttachCommand() (string, error) {
	if s == nil || s.backend == nil {
		return "", fmt.Errorf("herdr owned session is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*commandTimeout)
	defer cancel()
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return "", err
	}
	defer unlockPrivateFile(lock)
	if _, err := s.backend.probeOwned(ctx, admission); err != nil {
		return "", fmt.Errorf("verify herdr owned session before attach: %w", err)
	}
	m := admission.marker
	assignments := [][2]string{
		{xdgConfigEnv, m.XDGConfigHome},
		{xdgStateEnv, m.XDGStateHome},
		{xdgDataEnv, m.XDGDataHome},
		{xdgCacheEnv, m.XDGCacheHome},
		{configEnv, m.ConfigPath},
		{sessionEnv, m.Session},
		{socketEnv, m.SocketPath},
		{clientSocketEnv, m.ClientSocketPath},
	}
	parts := make([]string, 0, len(assignments)+1)
	for _, assignment := range assignments {
		parts = append(parts, assignment[0]+"="+shellQuote(assignment[1]))
	}
	return strings.Join(append(parts, shellQuote(m.BinaryPath)), " "), nil
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
	SchemaID             string `json:"schema_id"`
	GitCommonDir         string `json:"git_common_dir"`
	GitCommonDevice      uint64 `json:"git_common_device"`
	GitCommonInode       uint64 `json:"git_common_inode"`
	OwnerNonce           string `json:"owner_nonce"`
	Session              string `json:"session"`
	RuntimeDir           string `json:"runtime_dir"`
	SocketPath           string `json:"socket_path"`
	ClientSocketPath     string `json:"client_socket_path"`
	BinaryPath           string `json:"binary_path"`
	BinarySHA256         string `json:"binary_sha256"`
	BinaryVersion        string `json:"binary_version"`
	SchemaSHA256         string `json:"schema_sha256"`
	BehaviorProfileID    string `json:"behavior_profile_id"`
	BehaviorSource       string `json:"behavior_source"`
	BehaviorPlatform     string `json:"behavior_platform"`
	DetectionFixtureID   string `json:"detection_fixture_id"`
	ManifestSetDigest    string `json:"manifest_set_digest"`
	NoRefreshPolicyID    string `json:"no_refresh_policy_id"`
	BehaviorAdmissionID  string `json:"behavior_admission_id"`
	SupervisorPID        int    `json:"supervisor_pid"`
	SupervisorStartToken string `json:"supervisor_start_token"`
	XDGConfigHome        string `json:"xdg_config_home"`
	XDGStateHome         string `json:"xdg_state_home"`
	XDGDataHome          string `json:"xdg_data_home"`
	XDGCacheHome         string `json:"xdg_cache_home"`
	ConfigPath           string `json:"config_path"`
}

type supervisorLease struct {
	SchemaID   string `json:"schema_id"`
	OwnerNonce string `json:"owner_nonce"`
	StartToken string `json:"start_token"`
	PID        int    `json:"pid"`
}

type ownedAdmission struct {
	marker     ownerMarker
	markerPath string
	lockPath   string
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
	binaryDir        string
}

type pathIdentity struct {
	device uint64
	inode  uint64
}

type supervisorStarter func(markerPath, nonce, startToken string) (int, error)

func EnsureOwned(ctx context.Context, opts OwnedOptions) (*OwnedSession, error) {
	return ensureOwned(ctx, opts, nil, startOwnedSupervisor)
}

func ensureOwned(ctx context.Context, opts OwnedOptions, backend *Backend, start supervisorStarter) (*OwnedSession, error) {
	if ctx == nil {
		return nil, fmt.Errorf("ensure owned herdr session requires a context")
	}
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(opts.GitCommonDir)
	if err != nil {
		return nil, err
	}
	session := naming.HerdrSessionName(commonIdentity.device, commonIdentity.inode)
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
	admitted, err := backend.admitBinaryContext(ctx, route{session: session, socketPath: layout.socketPath})
	if err != nil {
		return nil, err
	}
	behavior, err := backend.admitOwnedBehavior(admitted)
	if err != nil {
		return nil, err
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
	defer unlockPrivateFile(lock)
	err = ensureOwnedLayout(layout)
	if err != nil {
		return nil, err
	}
	admitted, err = pinOwnedBinary(layout, admitted)
	if err != nil {
		return nil, err
	}
	backend.lookPath = func(string) (string, error) { return admitted.path, nil }
	backend.stageBinary = func(sourcePath string) (string, string, error) {
		return stageExecutable(sourcePath, layout.binaryDir)
	}
	marker, found, err := readOwnerMarker(layout.markerPath)
	if err != nil {
		return nil, err
	}
	if !found {
		marker, err = claimOwnedSession(layout, commonDir, commonIdentity, session, admitted, behavior, start)
	} else {
		err = validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, behavior)
		if err == nil {
			err = verifyLiveSupervisor(layout.supervisorLock, marker)
		}
	}
	if err != nil {
		return nil, err
	}
	backend.owner = &ownedAdmission{marker: marker, markerPath: layout.markerPath, lockPath: layout.lifecycleLock}
	if err := waitForOwnedReady(ctx, backend); err != nil {
		return nil, err
	}
	return &OwnedSession{Session: session, SocketPath: layout.socketPath, ClientSocketPath: layout.clientSocketPath, backend: backend}, nil
}

func claimOwnedSession(
	layout ownedLayout,
	commonDir string,
	commonIdentity pathIdentity,
	session string,
	admitted binaryAdmission,
	behavior behaviorAdmission,
	start supervisorStarter,
) (ownerMarker, error) {
	if _, running, err := inspectSupervisorLease(layout.supervisorLock); err != nil {
		return ownerMarker{}, err
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
		return ownerMarker{}, err
	}
	startToken, err := randomToken()
	if err != nil {
		return ownerMarker{}, err
	}
	pid, err := start(layout.markerPath, nonce, startToken)
	if err != nil {
		return ownerMarker{}, err
	}
	marker := ownerMarker{
		SchemaID: ownedMarkerSchemaID, GitCommonDir: commonDir,
		GitCommonDevice: commonIdentity.device, GitCommonInode: commonIdentity.inode, OwnerNonce: nonce,
		Session: session, RuntimeDir: layout.runtimeDir, SocketPath: layout.socketPath,
		ClientSocketPath: layout.clientSocketPath, BinaryPath: admitted.path,
		BinarySHA256: admitted.sha256, BinaryVersion: admitted.version, SchemaSHA256: admitted.schemaSHA256,
		BehaviorProfileID: behavior.profile.id, BehaviorSource: behavior.profile.source,
		BehaviorPlatform:   behavior.profile.goos + "/" + behavior.profile.goarch,
		DetectionFixtureID: behavior.profile.detectionFixture, ManifestSetDigest: behavior.profile.manifestSetDigest,
		NoRefreshPolicyID:   behavior.profile.noRefreshPolicy,
		BehaviorAdmissionID: behaviorAdmissionID(behavior.profile, admitted, commonDir, commonIdentity, layout, nonce),
		SupervisorPID:       pid, SupervisorStartToken: startToken,
		XDGConfigHome: layout.xdgConfigHome, XDGStateHome: layout.xdgStateHome,
		XDGDataHome: layout.xdgDataHome, XDGCacheHome: layout.xdgCacheHome,
		ConfigPath: layout.configPath,
	}
	if err := writeOwnerMarkerExclusive(layout.markerPath, marker); err != nil {
		stopStartedOwnedSupervisor(pid)
		return ownerMarker{}, err
	}
	return marker, nil
}

func waitForOwnedReady(ctx context.Context, backend *Backend) error {
	deadline := time.Now().Add(ownedReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, probeErr := backend.probeOwned(ctx, *backend.owner)
		if probeErr == nil {
			return nil
		}
		lastErr = probeErr
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

func (b *Backend) acquireOwnedOperation(ctx context.Context) (ownedAdmission, *os.File, error) {
	if b == nil || b.owner == nil {
		return ownedAdmission{}, nil, fmt.Errorf("herdr mutation requires a fanout-owned session")
	}
	lock, err := lockPrivateFileContext(ctx, b.owner.lockPath)
	if err != nil {
		return ownedAdmission{}, nil, err
	}
	admission := *b.owner
	marker, found, err := readOwnerMarker(admission.markerPath)
	if err != nil || !found || marker != admission.marker {
		unlockPrivateFile(lock)
		if err != nil {
			return ownedAdmission{}, nil, err
		}
		return ownedAdmission{}, nil, fmt.Errorf("herdr ownership marker changed")
	}
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(marker.GitCommonDir)
	if err == nil && commonDir != marker.GitCommonDir {
		err = fmt.Errorf("herdr ownership marker git common directory is not canonical")
	}
	layout := ownedLayout{}
	if err == nil {
		layout, err = prepareOwnedLayout(filepath.Dir(marker.RuntimeDir), marker.Session)
	}
	if err == nil {
		admitted := binaryAdmission{
			path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
			protocol: supportedProtocol, schemaSHA256: marker.SchemaSHA256,
		}
		var behavior behaviorAdmission
		behavior, err = b.admitOwnedBehavior(admitted)
		if err == nil {
			err = validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, behavior)
		}
	}
	if err == nil {
		err = verifyLiveSupervisor(layout.supervisorLock, marker)
	}
	if err == nil {
		err = validatePrivateSocket(marker.SocketPath)
	}
	if err == nil {
		err = validatePrivateSocket(marker.ClientSocketPath)
	}
	if err != nil {
		unlockPrivateFile(lock)
		return ownedAdmission{}, nil, err
	}
	return admission, lock, nil
}

func (b *Backend) probeOwned(ctx context.Context, admission ownedAdmission) (probeResult, error) {
	if b.session != admission.marker.Session || b.socketPath != admission.marker.SocketPath {
		return probeResult{}, fmt.Errorf("herdr backend route does not match owned admission")
	}
	probed, err := b.probeContext(ctx)
	if err != nil {
		return probeResult{}, err
	}
	if probed.binary != admission.marker.BinaryPath || probed.sha256 != admission.marker.BinarySHA256 ||
		probed.version != admission.marker.BinaryVersion || probed.schemaSHA256 != admission.marker.SchemaSHA256 {
		return probeResult{}, fmt.Errorf("herdr binary identity changed after owned admission")
	}
	if err := b.validateActiveManifestProfile(ctx, probed, b.behavior); err != nil {
		return probeResult{}, err
	}
	return probed, nil
}

func openCanonicalGitCommonDir(raw string) (string, pathIdentity, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", pathIdentity{}, fmt.Errorf("herdr owned session requires a git common directory")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", pathIdentity{}, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", pathIdentity{}, fmt.Errorf("canonicalize git common directory: %w", err)
	}
	resolved = filepath.Clean(resolved)
	dir, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", pathIdentity{}, fmt.Errorf("open git common directory without following links: %w", err)
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		return "", pathIdentity{}, fmt.Errorf("git common directory %s is not a directory", resolved)
	}
	if err := validateOwnerUID(resolved, info); err != nil {
		return "", pathIdentity{}, err
	}
	if err := validateNoExtendedACL(resolved); err != nil {
		return "", pathIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return "", pathIdentity{}, fmt.Errorf("git common directory %s has no physical identity", resolved)
	}
	return resolved, pathIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func prepareOwnedLayout(runtimeBase, session string) (ownedLayout, error) {
	if err := validateSessionName(session); err != nil {
		return ownedLayout{}, err
	}
	if runtimeBase == "" {
		runtimeBase = filepath.Join(defaultRuntimeParent, "fhr-"+strconv.Itoa(os.Getuid()))
	}
	abs, err := filepath.Abs(runtimeBase)
	if err != nil {
		return ownedLayout{}, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return ownedLayout{}, fmt.Errorf("canonicalize herdr runtime parent: %w", err)
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	runtimeDir := filepath.Join(filepath.Clean(abs), session)
	configHome := filepath.Join(runtimeDir, "xdg-config")
	layout := ownedLayout{
		runtimeBase: filepath.Clean(abs), runtimeDir: runtimeDir,
		markerPath: filepath.Join(runtimeDir, ownedMarkerName), lifecycleLock: filepath.Join(runtimeDir, ownedLifecycleLockName),
		supervisorLock: filepath.Join(runtimeDir, ownedSupervisorLockName), socketPath: filepath.Join(runtimeDir, "herdr.sock"),
		clientSocketPath: filepath.Join(runtimeDir, "herdr-client.sock"), xdgConfigHome: configHome,
		xdgStateHome: filepath.Join(runtimeDir, "xdg-state"), xdgDataHome: filepath.Join(runtimeDir, "xdg-data"),
		xdgCacheHome: filepath.Join(runtimeDir, "xdg-cache"), configPath: filepath.Join(configHome, "herdr", "config.toml"),
		binaryDir: filepath.Join(runtimeDir, "binary"),
	}
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if len(path) > maxUnixSocketPathBytes {
			return ownedLayout{}, fmt.Errorf("herdr owned socket path is %d bytes, want at most %d: %s", len(path), maxUnixSocketPathBytes, path)
		}
	}
	return layout, nil
}

func ensureOwnedLayout(layout ownedLayout) error {
	for _, dir := range []string{layout.runtimeDir, layout.xdgConfigHome, layout.xdgStateHome, layout.xdgDataHome, layout.xdgCacheHome, filepath.Dir(layout.configPath), layout.binaryDir} {
		if err := ensurePrivateDir(dir); err != nil {
			return fmt.Errorf("prepare herdr owned directory: %w", err)
		}
	}
	if err := ensurePrivateContents(layout.configPath, []byte(ownedConfigContents)); err != nil {
		return err
	}
	logFile, err := openPrivateAppendFile(filepath.Join(layout.runtimeDir, ownedSupervisorLogName))
	if err != nil {
		return err
	}
	return logFile.Close()
}

func validateOwnedLayout(layout ownedLayout) error {
	for _, dir := range []string{layout.runtimeDir, layout.xdgConfigHome, layout.xdgStateHome, layout.xdgDataHome, layout.xdgCacheHome, filepath.Dir(layout.configPath), layout.binaryDir} {
		if err := validatePrivateDir(dir); err != nil {
			return err
		}
	}
	if err := validatePrivateContents(layout.configPath, []byte(ownedConfigContents)); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(layout.runtimeDir, ownedSupervisorLogName))
	if err != nil {
		return err
	}
	return validatePrivateRegular(filepath.Join(layout.runtimeDir, ownedSupervisorLogName), info)
}

func pinOwnedBinary(layout ownedLayout, admitted binaryAdmission) (binaryAdmission, error) {
	target, gotHash, err := stageExecutable(admitted.path, layout.binaryDir)
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("pin admitted herdr binary: %w", err)
	}
	if gotHash != admitted.sha256 {
		return binaryAdmission{}, fmt.Errorf("admitted herdr binary changed while bundling")
	}
	admitted.path = target
	return admitted, nil
}

func validatePinnedBinary(path, wantHash string, layout ownedLayout) error {
	return validatePinnedBinaryInDir(path, wantHash, layout.binaryDir)
}

func validatePinnedBinaryInDir(path, wantHash, binaryDir string) error {
	wantPath := filepath.Join(binaryDir, "herdr-"+wantHash)
	if path != wantPath || !validHexToken(wantHash) {
		return fmt.Errorf("herdr binary bundle path does not match its content identity")
	}
	bundled, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = bundled.Close() }()
	info, err := bundled.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o500 {
		return fmt.Errorf("herdr binary bundle is not a private read-only executable")
	}
	if err := validateOwnerUID(path, info); err != nil {
		return err
	}
	if err := validateNoExtendedACL(path); err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("herdr binary bundle has an invalid physical identity")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, bundled); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != wantHash {
		return fmt.Errorf("herdr binary bundle content changed")
	}
	return nil
}

func ensurePrivateDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return validatePrivateDir(path)
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("herdr owned directory %s is not an owner-only real directory", path)
	}
	if err := validateOwnerUID(path, info); err != nil {
		return err
	}
	return validateNoExtendedACL(path)
}

func ensurePrivateContents(path string, expected []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	if statErr == nil {
		statErr = validatePrivateRegular(path, info)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, int64(len(expected)+1)))
	if statErr == nil && readErr == nil && len(data) == 0 {
		_, readErr = f.WriteAt(expected, 0)
		if readErr == nil {
			readErr = f.Sync()
		}
		data = expected
	}
	closeErr := f.Close()
	if err := errors.Join(statErr, readErr, closeErr); err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return fmt.Errorf("herdr owned file %s has unexpected contents", path)
	}
	return nil
}

func validatePrivateContents(path string, expected []byte) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	err = validatePrivateRegular(path, info)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(expected)+1)))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return fmt.Errorf("herdr owned file %s has unexpected contents", path)
	}
	return nil
}

func validatePrivateRegular(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("herdr owned file %s is not an owner-only regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("herdr owned file %s has an invalid link identity", path)
	}
	if err := validateOwnerUID(path, info); err != nil {
		return err
	}
	return validateNoExtendedACL(path)
}

func validatePrivateSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("herdr owned socket %s is not an owner-only Unix socket", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("herdr owned socket %s has an invalid link identity", path)
	}
	if err := validateOwnerUID(path, info); err != nil {
		return err
	}
	return validateNoExtendedACL(path)
}

func validateOwnerUID(path string, info os.FileInfo) error {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("herdr owned path %s belongs to uid %d, want %d", path, stat.Uid, os.Getuid())
	}
	return nil
}

func validateNoExtendedACL(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	cmd := exec.Command("/bin/ls", "-lde", path)
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("inspect ACL on herdr owned path %s: %w", path, err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	mode, _, ok := strings.Cut(first, " ")
	if !ok || strings.Contains(mode, "+") {
		return fmt.Errorf("herdr owned path %s has an extended ACL", path)
	}
	return nil
}

func lockPrivateFileContext(ctx context.Context, path string) (*os.File, error) {
	if ctx == nil {
		return nil, fmt.Errorf("lock private file requires a context")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		err = validatePrivateRegular(path, info)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func unlockPrivateFile(f *os.File) {
	if f == nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		_ = f.Close()
		return
	}
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
	err = validatePrivateRegular(path, info)
	if err != nil {
		return supervisorLease{}, false, err
	}
	lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if lockErr == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return supervisorLease{}, false, nil
	}
	if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
		return supervisorLease{}, false, lockErr
	}
	lease, err := readLeaseFromFile(f)
	if err != nil {
		return supervisorLease{}, true, fmt.Errorf("parse herdr supervisor lease: %w", err)
	}
	return lease, true, nil
}

func verifyLiveSupervisor(path string, marker ownerMarker) error {
	lease, running, err := inspectSupervisorLease(path)
	if err != nil {
		return err
	}
	if !running {
		return errOwnedSupervisorNotRunning
	}
	if lease.SchemaID != ownedMarkerSchemaID || lease.OwnerNonce != marker.OwnerNonce || lease.StartToken != marker.SupervisorStartToken || lease.PID != marker.SupervisorPID {
		return fmt.Errorf("herdr supervisor lease does not match ownership marker")
	}
	return nil
}

func writeSupervisorLease(f *os.File, marker ownerMarker) error {
	lease := supervisorLease{SchemaID: ownedMarkerSchemaID, OwnerNonce: marker.OwnerNonce, StartToken: marker.SupervisorStartToken, PID: marker.SupervisorPID}
	data, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteAt(data, 0); err != nil {
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
		return ownerMarker{}, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ownerMarker{}, true, err
	}
	err = validatePrivateRegular(path, info)
	if err != nil {
		return ownerMarker{}, true, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOwnerMarkerBytes+1))
	if err != nil {
		return ownerMarker{}, true, fmt.Errorf("read herdr ownership marker: %w", err)
	}
	if len(data) > maxOwnerMarkerBytes {
		return ownerMarker{}, true, fmt.Errorf("herdr ownership marker exceeds %d bytes", maxOwnerMarkerBytes)
	}
	var marker ownerMarker
	if err := decodeStrictCanonical(data, &marker); err != nil {
		return ownerMarker{}, true, fmt.Errorf("parse herdr ownership marker: %w", err)
	}
	return marker, true, nil
}

func decodeStrictCanonical(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected trailing JSON value")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, canonical) {
		return fmt.Errorf("bytes are not canonical JSON")
	}
	return nil
}

func writeOwnerMarkerExclusive(path string, marker ownerMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".owner-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporaryPath != "" {
			_ = os.Remove(temporaryPath)
		}
	}()
	_, err = temporary.Write(data)
	if err != nil {
		_ = temporary.Close()
		return err
	}
	err = temporary.Sync()
	if err != nil {
		_ = temporary.Close()
		return err
	}
	err = temporary.Close()
	if err != nil {
		return err
	}
	err = os.Link(temporaryPath, path)
	if err != nil {
		return fmt.Errorf("claim herdr ownership marker: %w", err)
	}
	err = os.Remove(temporaryPath)
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("seal herdr ownership marker link identity: %w", err)
	}
	temporaryPath = ""
	stored, found, err := readOwnerMarker(path)
	if err != nil || !found || stored != marker {
		if err != nil {
			return fmt.Errorf("verify claimed herdr ownership marker: %w", err)
		}
		return fmt.Errorf("claimed herdr ownership marker does not match")
	}
	return nil
}

func validateOwnedMarker(
	marker ownerMarker,
	layout ownedLayout,
	commonDir string,
	commonIdentity pathIdentity,
	admitted binaryAdmission,
	behavior behaviorAdmission,
) error {
	if marker.SchemaID != ownedMarkerSchemaID || marker.GitCommonDir != commonDir || marker.Session != filepath.Base(layout.runtimeDir) ||
		marker.GitCommonDevice != commonIdentity.device || marker.GitCommonInode != commonIdentity.inode ||
		marker.RuntimeDir != layout.runtimeDir || marker.SocketPath != layout.socketPath || marker.ClientSocketPath != layout.clientSocketPath ||
		marker.XDGConfigHome != layout.xdgConfigHome || marker.XDGStateHome != layout.xdgStateHome || marker.XDGDataHome != layout.xdgDataHome ||
		marker.XDGCacheHome != layout.xdgCacheHome || marker.ConfigPath != layout.configPath {
		return fmt.Errorf("herdr ownership marker does not match this repository and runtime layout")
	}
	if marker.BinaryPath != admitted.path || marker.BinarySHA256 != admitted.sha256 || marker.BinaryVersion != admitted.version ||
		marker.SchemaSHA256 != admitted.schemaSHA256 ||
		marker.BehaviorProfileID != behavior.profile.id || marker.BehaviorSource != behavior.profile.source ||
		marker.BehaviorPlatform != behavior.profile.goos+"/"+behavior.profile.goarch ||
		marker.DetectionFixtureID != behavior.profile.detectionFixture || marker.ManifestSetDigest != behavior.profile.manifestSetDigest ||
		marker.NoRefreshPolicyID != behavior.profile.noRefreshPolicy ||
		marker.BehaviorAdmissionID != behaviorAdmissionID(behavior.profile, admitted, commonDir, commonIdentity, layout, marker.OwnerNonce) ||
		!filepath.IsAbs(marker.BinaryPath) || filepath.Clean(marker.BinaryPath) != marker.BinaryPath ||
		!validHexToken(marker.BinarySHA256) || !validHexToken(marker.SchemaSHA256) ||
		validateAdmittedVersion(marker.BinaryVersion) != nil ||
		!validHexToken(marker.ManifestSetDigest) || !validHexToken(marker.BehaviorAdmissionID) ||
		!validHexToken(marker.OwnerNonce) || !validHexToken(marker.SupervisorStartToken) || marker.SupervisorPID <= 1 {
		return fmt.Errorf("herdr ownership marker identity does not match admitted binary and supervisor")
	}
	if err := validatePinnedBinary(marker.BinaryPath, marker.BinarySHA256, layout); err != nil {
		return fmt.Errorf("herdr owned binary identity changed: %w", err)
	}
	return validateOwnedLayout(layout)
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func validHexToken(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
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
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func startOwnedSupervisor(markerPath, nonce, startToken string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer func() { _ = reader.Close() }()
	cmd := exec.Command(exe, ownedSupervisorCommand, markerPath, nonce, startToken, strconv.Itoa(ownedSupervisorReadyFD))
	cmd.Env = []string{}
	cmd.ExtraFiles = []*os.File{writer}
	cmd.Dir = filepath.Dir(markerPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logFile, err := openPrivateAppendFile(filepath.Join(filepath.Dir(markerPath), ownedSupervisorLogName))
	if err != nil {
		_ = writer.Close()
		return 0, err
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		_ = logFile.Close()
		return 0, err
	}
	_ = writer.Close()
	_ = logFile.Close()
	if err := reader.SetReadDeadline(time.Now().Add(ownedReadyTimeout)); err != nil {
		stopStartedOwnedCommand(cmd)
		return 0, err
	}
	one := []byte{0}
	if _, err := io.ReadFull(reader, one); err != nil || string(one) != ownedSupervisorReadyACK {
		stopStartedOwnedCommand(cmd)
		return 0, fmt.Errorf("herdr supervisor readiness handshake failed")
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

func stopStartedOwnedCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	stopStartedOwnedSupervisor(cmd.Process.Pid)
	_ = cmd.Wait()
}

func stopStartedOwnedSupervisor(pid int) {
	if pid <= 1 || pid == os.Getpid() {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func IsSupervisorRequest(args []string) bool {
	return len(args) > 0 && args[0] == ownedSupervisorCommand
}

func RunSupervisor(args []string, errw io.Writer) int {
	if len(args) != 4 {
		fmt.Fprintln(errw, "fanout herdr supervisor: expected marker path, nonce, start token, and ready fd")
		return 2
	}
	markerPath, nonce, startToken := args[0], args[1], args[2]
	readyFD, err := strconv.Atoi(args[3])
	if err != nil || readyFD != ownedSupervisorReadyFD || !filepath.IsAbs(markerPath) || filepath.Clean(markerPath) != markerPath ||
		filepath.Base(markerPath) != ownedMarkerName || !validHexToken(nonce) || !validHexToken(startToken) {
		fmt.Fprintln(errw, "fanout herdr supervisor: invalid marker path, nonce, start token, or ready fd")
		return 2
	}
	ready := os.NewFile(uintptr(readyFD), "herdr-supervisor-ready")
	if ready == nil {
		fmt.Fprintln(errw, "fanout herdr supervisor: invalid ready fd")
		return 2
	}
	defer func() { _ = ready.Close() }()
	runtimeDir := filepath.Dir(markerPath)
	err = ensurePrivateDir(runtimeDir)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: runtime directory: %v\n", err)
		return 1
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir, ownedSupervisorLockName), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		var info os.FileInfo
		info, err = lock.Stat()
		if err == nil {
			err = validatePrivateRegular(filepath.Join(runtimeDir, ownedSupervisorLockName), info)
		}
	}
	if err != nil || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		fmt.Fprintln(errw, "fanout herdr supervisor: another supervisor owns this session")
		if lock != nil {
			_ = lock.Close()
		}
		return 1
	}
	defer unlockPrivateFile(lock)
	_, err = ready.Write([]byte(ownedSupervisorReadyACK))
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: ready handshake: %v\n", err)
		return 1
	}
	_ = ready.Close()
	var marker ownerMarker
	deadline := time.Now().Add(ownedReadyTimeout)
	for time.Now().Before(deadline) {
		loaded, found, readErr := readOwnerMarker(markerPath)
		if readErr != nil {
			fmt.Fprintf(errw, "fanout herdr supervisor: marker: %v\n", readErr)
			return 1
		}
		if found && loaded.OwnerNonce == nonce && loaded.SupervisorStartToken == startToken && loaded.SupervisorPID == os.Getpid() {
			marker = loaded
			break
		}
		time.Sleep(ownedReadyInterval)
	}
	if marker.OwnerNonce == "" {
		fmt.Fprintln(errw, "fanout herdr supervisor: timed out waiting for ownership marker")
		return 1
	}
	layout, err := prepareOwnedLayout(filepath.Dir(runtimeDir), marker.Session)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: layout: %v\n", err)
		return 1
	}
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(marker.GitCommonDir)
	if err != nil || commonDir != marker.GitCommonDir {
		fmt.Fprintln(errw, "fanout herdr supervisor: git common directory identity mismatch")
		return 1
	}
	admitted := binaryAdmission{
		path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
		protocol: supportedProtocol, schemaSHA256: marker.SchemaSHA256,
	}
	profile := productionBehaviorProfile()
	if err = validateBehaviorProfile(profile, admitted); err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: behavior profile: %v\n", err)
		return 1
	}
	behavior := behaviorAdmission{profile: profile}
	err = validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, behavior)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: marker identity: %v\n", err)
		return 1
	}
	err = writeSupervisorLease(lock, marker)
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: write lease: %v\n", err)
		return 1
	}
	logFile, err := openPrivateAppendFile(filepath.Join(runtimeDir, ownedSupervisorLogName))
	if err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: log: %v\n", err)
		return 1
	}
	defer func() { _ = logFile.Close() }()
	_ = syscall.Umask(0o077)
	cmd := exec.Command(marker.BinaryPath, "server")
	cmd.Env = ownedMarkerEnvironment(marker)
	cmd.Dir = runtimeDir
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGCHLD)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: start server: %v\n", err)
		return 1
	}
	reaped := false
	defer func() {
		if reaped {
			return
		}
		// The leader has not been reaped, so its PID cannot have been reused.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	code := 0
	received := <-signals
	if received == syscall.SIGCHLD {
		waitErr := cmd.Wait()
		reaped = true
		if waitErr != nil {
			code = 1
		}
		// A spontaneous leader exit cannot prove that its descendants are
		// absent. Retain the marker and sockets so the next adoption fails
		// closed instead of mutating a possibly live old namespace.
		return code
	}
	typed, ok := received.(syscall.Signal)
	if !ok {
		fmt.Fprintln(errw, "fanout herdr supervisor: unsupported shutdown signal")
		return 1
	}
	if killErr := syscall.Kill(-cmd.Process.Pid, typed); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		fmt.Fprintf(errw, "fanout herdr supervisor: signal server process group: %v\n", killErr)
		code = 1
	}
	// Keep the leader unreaped during the grace period. This prevents PID/PGID
	// reuse before the final group kill.
	time.Sleep(ownedShutdownGrace)
	if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		fmt.Fprintf(errw, "fanout herdr supervisor: kill server process group: %v\n", killErr)
		code = 1
	}
	if waitErr := cmd.Wait(); waitErr != nil && code == 0 {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			fmt.Fprintf(errw, "fanout herdr supervisor: reap server: %v\n", waitErr)
			code = 1
		}
	}
	reaped = true
	if err := cleanupOwnedSockets(markerPath, marker, lock); err != nil {
		fmt.Fprintf(errw, "fanout herdr supervisor: socket cleanup: %v\n", err)
		return 1
	}
	return code
}

func cleanupOwnedSockets(markerPath string, marker ownerMarker, lock *os.File) error {
	current, found, err := readOwnerMarker(markerPath)
	if err != nil || !found || current != marker {
		return fmt.Errorf("ownership marker changed before socket cleanup")
	}
	lease, err := readLeaseFromFile(lock)
	if err != nil || lease.PID != marker.SupervisorPID || lease.OwnerNonce != marker.OwnerNonce || lease.StartToken != marker.SupervisorStartToken {
		return fmt.Errorf("supervisor lease changed before socket cleanup")
	}
	for _, path := range []string{marker.SocketPath, marker.ClientSocketPath} {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := validatePrivateSocket(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("herdr owned socket %s still exists after cleanup", path)
		}
	}
	return nil
}

func readLeaseFromFile(f *os.File) (supervisorLease, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return supervisorLease{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOwnerMarkerBytes+1))
	if err != nil {
		return supervisorLease{}, err
	}
	var lease supervisorLease
	return lease, decodeStrictCanonical(data, &lease)
}

func openPrivateAppendFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		err = validatePrivateRegular(path, info)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func ownedMarkerEnvironment(marker ownerMarker) []string {
	control := &controlPlaneEnvironment{
		xdgConfigHome: marker.XDGConfigHome, xdgStateHome: marker.XDGStateHome,
		xdgDataHome: marker.XDGDataHome, xdgCacheHome: marker.XDGCacheHome,
		configPath: marker.ConfigPath, clientSocketPath: marker.ClientSocketPath,
	}
	return routeEnvironment(route{session: marker.Session, socketPath: marker.SocketPath}, control)
}
