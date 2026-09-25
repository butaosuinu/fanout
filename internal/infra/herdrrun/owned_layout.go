package herdrrun

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	configEnv               = "HERDR_CONFIG_PATH"
	clientSocketEnv         = "HERDR_CLIENT_SOCKET_PATH"
	xdgConfigEnv            = "XDG_CONFIG_HOME"
	xdgStateEnv             = "XDG_STATE_HOME"
	xdgDataEnv              = "XDG_DATA_HOME"
	xdgCacheEnv             = "XDG_CACHE_HOME"
)

type controlPlaneEnvironment struct {
	xdgConfigHome    string
	xdgStateHome     string
	xdgDataHome      string
	xdgCacheHome     string
	configPath       string
	clientSocketPath string
}

type ownedLayout struct {
	runtimeBase             string
	runtimeDir              string
	markerPath              string
	lifecycleLock           string
	supervisorLock          string
	socketPath              string
	clientSocketPath        string
	xdgConfigHome           string
	xdgStateHome            string
	xdgDataHome             string
	xdgCacheHome            string
	configPath              string
	dashboardDescriptorPath string
	binaryDir               string
	launcherDir             string
}

type pathIdentity struct {
	device uint64
	inode  uint64
}

func normalizeStatDevice[T ~int32 | ~uint32 | ~uint64](device T) uint64 {
	return uint64(device)
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
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return "", pathIdentity{}, fmt.Errorf("git common directory %s has no physical identity", resolved)
	}
	return resolved, pathIdentity{device: normalizeStatDevice(stat.Dev), inode: stat.Ino}, nil
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
	layout := newOwnedLayout(filepath.Clean(abs), runtimeDir, configHome)
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if len(path) > maxUnixSocketPathBytes {
			return ownedLayout{}, fmt.Errorf("herdr owned socket path is %d bytes, want at most %d: %s", len(path), maxUnixSocketPathBytes, path)
		}
	}
	return layout, nil
}

func newOwnedLayout(runtimeBase, runtimeDir, configHome string) ownedLayout {
	return ownedLayout{
		runtimeBase: runtimeBase, runtimeDir: runtimeDir,
		markerPath: filepath.Join(runtimeDir, ownedMarkerName), lifecycleLock: filepath.Join(runtimeDir, ownedLifecycleLockName),
		supervisorLock: filepath.Join(runtimeDir, ownedSupervisorLockName), socketPath: filepath.Join(runtimeDir, "herdr.sock"),
		clientSocketPath: filepath.Join(runtimeDir, "herdr-client.sock"), xdgConfigHome: configHome,
		xdgStateHome: filepath.Join(runtimeDir, "xdg-state"), xdgDataHome: filepath.Join(runtimeDir, "xdg-data"),
		xdgCacheHome: filepath.Join(runtimeDir, "xdg-cache"), configPath: filepath.Join(configHome, "herdr", "config.toml"),
		dashboardDescriptorPath: filepath.Join(runtimeDir, dashboardDescriptorName),
		binaryDir:               filepath.Join(runtimeDir, "binary"), launcherDir: filepath.Join(runtimeDir, "launcher"),
	}
}

func ensureOwnedLayout(layout ownedLayout) error {
	for _, dir := range []string{layout.runtimeDir, layout.xdgConfigHome, layout.xdgStateHome, layout.xdgDataHome, layout.xdgCacheHome, filepath.Dir(layout.configPath), layout.binaryDir, layout.launcherDir} {
		if err := ensurePrivateDir(dir); err != nil {
			return fmt.Errorf("prepare herdr owned directory: %w", err)
		}
	}
	logFile, err := openPrivateAppendFile(filepath.Join(layout.runtimeDir, ownedSupervisorLogName))
	if err != nil {
		return err
	}
	return logFile.Close()
}

func validateOwnedLayout(layout ownedLayout, launcherPath string) error {
	for _, dir := range []string{layout.runtimeDir, layout.xdgConfigHome, layout.xdgStateHome, layout.xdgDataHome, layout.xdgCacheHome, filepath.Dir(layout.configPath), layout.binaryDir, layout.launcherDir} {
		if err := validatePrivateDir(dir); err != nil {
			return err
		}
	}
	if err := validateCompatibleOwnedConfig(layout, launcherPath); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(layout.runtimeDir, ownedSupervisorLogName))
	if err != nil {
		return err
	}
	return validatePrivateRegular(filepath.Join(layout.runtimeDir, ownedSupervisorLogName), info)
}

func ownedConfigContents(launcherPath string) []byte {
	return []byte("[terminal]\ndefault_shell = " + strconv.Quote(launcherPath) +
		"\nshell_mode = \"non_login\"\n\n[session]\nresume_agents_on_restore = false\n\n" +
		"[update]\nmanifest_check = false\n")
}

func legacyOwnedConfigContents(launcherPath string) []byte {
	return []byte("[terminal]\ndefault_shell = " + strconv.Quote(launcherPath) +
		"\nshell_mode = \"non_login\"\n\n[update]\nmanifest_check = false\n")
}

func validateCompatibleOwnedConfig(layout ownedLayout, launcherPath string) error {
	currentErr := validatePrivateContents(layout.configPath, ownedConfigContents(launcherPath))
	if currentErr == nil {
		return nil
	}
	if err := validatePrivateContents(layout.configPath, legacyOwnedConfigContents(launcherPath)); err == nil {
		return nil
	}
	descriptor, found, err := readDashboardDescriptor(layout)
	if err != nil {
		return err
	}
	if !found {
		return currentErr
	}
	return validatePrivateContents(layout.configPath, ownedDashboardConfigContents(
		launcherPath, descriptor.HelperPath, layout.dashboardDescriptorPath,
	))
}

func ensureOwnedConfig(layout ownedLayout, launcherPath string) error {
	return ensurePrivateContents(layout.configPath, ownedConfigContents(launcherPath))
}

func pinOwnedLauncher(layout ownedLayout) (binaryAdmission, error) {
	executable, err := os.Executable()
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("resolve fanout launcher executable: %w", err)
	}
	path, digest, err := stageExecutable(executable, layout.launcherDir)
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("pin fanout pane launcher: %w", err)
	}
	return binaryAdmission{path: path, sha256: digest}, nil
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
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errPinnedBinaryPhysicalIdentity
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
