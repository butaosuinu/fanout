package herdrrun

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

var (
	errOwnedSupervisorNotRunning    = errors.New("herdr owned supervisor is not running; refusing automatic recovery without proof that prior operations are quiescent")
	errUnpublishedSupervisorLease   = errors.New("herdr supervisor lease was not published")
	errPinnedBinaryPhysicalIdentity = errors.New("herdr binary bundle has an invalid physical identity")
)

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
	SupervisorPID        int    `json:"supervisor_pid"`
	SupervisorStartToken string `json:"supervisor_start_token"`
	XDGConfigHome        string `json:"xdg_config_home"`
	XDGStateHome         string `json:"xdg_state_home"`
	XDGDataHome          string `json:"xdg_data_home"`
	XDGCacheHome         string `json:"xdg_cache_home"`
	ConfigPath           string `json:"config_path"`
	LauncherPath         string `json:"launcher_path"`
	LauncherSHA256       string `json:"launcher_sha256"`
	DashboardTokenSHA256 string `json:"dashboard_token_sha256,omitempty"`
}

type supervisorLease struct {
	SchemaID   string `json:"schema_id"`
	OwnerNonce string `json:"owner_nonce"`
	StartToken string `json:"start_token"`
	PID        int    `json:"pid"`
	ServerPID  int    `json:"server_pid,omitempty"`
}

func inspectSupervisorLease(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	err = validatePrivateRegular(path, info)
	if err != nil {
		return false, err
	}
	lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if lockErr == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, nil
	}
	if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
		return false, lockErr
	}
	_, err = readLeaseFromFile(f)
	if err != nil {
		return true, fmt.Errorf("parse herdr supervisor lease: %w", err)
	}
	return true, nil
}

func verifyLiveSupervisor(path string, marker ownerMarker) error {
	lease, running, err := inspectExistingSupervisorLease(path)
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

func inspectExistingSupervisorLease(path string) (supervisorLease, bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
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
		lease, readErr := readRetiredSupervisorLease(f, info)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return lease, false, readErr
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

func readRetiredSupervisorLease(f *os.File, info os.FileInfo) (supervisorLease, error) {
	if info.Size() == 0 {
		return supervisorLease{}, errUnpublishedSupervisorLease
	}
	lease, err := readLeaseFromFile(f)
	if err != nil {
		return supervisorLease{}, fmt.Errorf("parse retired herdr supervisor lease: %w", err)
	}
	return lease, nil
}

func writeSupervisorLease(f *os.File, marker ownerMarker, serverPID int) error {
	lease := supervisorLease{
		SchemaID: ownedMarkerSchemaID, OwnerNonce: marker.OwnerNonce,
		StartToken: marker.SupervisorStartToken, PID: marker.SupervisorPID, ServerPID: serverPID,
	}
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

//nolint:funlen // Keep the strict marker contract and its physical-identity checks together.
func validateOwnedMarker(
	marker ownerMarker,
	layout ownedLayout,
	commonDir string,
	commonIdentity pathIdentity,
	admitted binaryAdmission,
	launcher binaryAdmission,
) error {
	layoutMatches := []bool{
		marker.SchemaID == ownedMarkerSchemaID, marker.GitCommonDir == commonDir,
		marker.Session == filepath.Base(layout.runtimeDir), marker.GitCommonDevice == commonIdentity.device,
		marker.GitCommonInode == commonIdentity.inode, marker.RuntimeDir == layout.runtimeDir,
		marker.SocketPath == layout.socketPath, marker.ClientSocketPath == layout.clientSocketPath,
		marker.XDGConfigHome == layout.xdgConfigHome, marker.XDGStateHome == layout.xdgStateHome,
		marker.XDGDataHome == layout.xdgDataHome, marker.XDGCacheHome == layout.xdgCacheHome,
		marker.ConfigPath == layout.configPath,
	}
	if slices.Contains(layoutMatches, false) {
		return fmt.Errorf("herdr ownership marker does not match this repository and runtime layout")
	}
	binaryMatches := []bool{
		marker.BinaryPath == admitted.path, marker.BinarySHA256 == admitted.sha256,
		marker.BinaryVersion == admitted.version, filepath.IsAbs(marker.BinaryPath),
		filepath.Clean(marker.BinaryPath) == marker.BinaryPath, validHexToken(marker.BinarySHA256),
		validateAdmittedVersion(marker.BinaryVersion) == nil, validHexToken(marker.OwnerNonce),
		validHexToken(marker.SupervisorStartToken), marker.SupervisorPID > 1,
	}
	if slices.Contains(binaryMatches, false) {
		return fmt.Errorf("herdr ownership marker identity does not match admitted binary and supervisor")
	}
	launcherMatches := []bool{
		marker.LauncherPath == launcher.path, marker.LauncherSHA256 == launcher.sha256,
		validHexToken(marker.LauncherSHA256),
	}
	if slices.Contains(launcherMatches, false) {
		return fmt.Errorf("herdr ownership marker does not match the bundled fanout launcher")
	}
	if err := validatePinnedBinary(marker.BinaryPath, marker.BinarySHA256, layout); err != nil {
		return fmt.Errorf("herdr owned binary identity changed: %w", err)
	}
	if err := validatePinnedBinaryInDir(marker.LauncherPath, marker.LauncherSHA256, layout.launcherDir); err != nil {
		return fmt.Errorf("herdr owned launcher identity changed: %w", err)
	}
	return validateOwnedLayout(layout, marker.LauncherPath)
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

func ownedMarkerEnvironment(marker ownerMarker) []string {
	control := &controlPlaneEnvironment{
		xdgConfigHome: marker.XDGConfigHome, xdgStateHome: marker.XDGStateHome,
		xdgDataHome: marker.XDGDataHome, xdgCacheHome: marker.XDGCacheHome,
		configPath: marker.ConfigPath, clientSocketPath: marker.ClientSocketPath,
	}
	environment := routeEnvironment(route{session: marker.Session, socketPath: marker.SocketPath}, control)
	environment = append(environment,
		paneLauncherFlagEnv+"=1",
		paneLauncherPathEnv+"="+marker.LauncherPath,
		paneLauncherControlEnv+"="+filepath.Join(marker.GitCommonDir, "fanout", "herdr-intents.json"),
	)
	return append(environment, dashboardInheritedAuthenticationEnvironment(os.Environ())...)
}
