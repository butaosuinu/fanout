package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"

	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// InspectOwnedServer returns the marker and lease identity that an explicit
// restart or shutdown must persist before it changes the owned server.
func InspectOwnedServer(opts OwnedOptions) (state.HerdrServerIdentity, error) {
	commonDir, layout, marker, _, err := existingOwnedAdmission(opts)
	if err != nil {
		return state.HerdrServerIdentity{}, err
	}
	lease, _, err := inspectExistingSupervisorLease(layout.supervisorLock)
	if err != nil {
		return state.HerdrServerIdentity{}, fmt.Errorf("inspect Herdr owned supervisor lease: %w", err)
	}
	if err := validateSupervisorLease(marker, lease); err != nil {
		return state.HerdrServerIdentity{}, err
	}
	return serverIdentity(commonDir, marker, lease.ServerPID), nil
}

// RestartOwned replaces a proven-dead owned server generation. The caller must
// hold the repository state/intent lock with the matching restart row saved.
func RestartOwned(
	ctx context.Context,
	opts OwnedOptions,
	expected state.HerdrServerIdentity,
) (*OwnedSession, error) {
	return restartOwned(ctx, opts, expected, startOwnedSupervisor, nil)
}

func restartOwned(
	ctx context.Context,
	opts OwnedOptions,
	expected state.HerdrServerIdentity,
	start supervisorStarter,
	backend *Backend,
) (_ *OwnedSession, err error) {
	commonDir, commonIdentity, layout, err := ownedLifecycleLayout(opts, expected)
	if err != nil {
		return nil, err
	}
	err = requireOwnedServerIntent(commonDir, state.HerdrIntentRestart, expected)
	if err != nil {
		return nil, err
	}
	lock, err := lockExistingPrivateFileContext(ctx, layout.lifecycleLock)
	if err != nil {
		return nil, fmt.Errorf("lock Herdr owned lifecycle for restart: %w", err)
	}
	defer func() { unlockPrivateFile(lock) }()
	adopted, err := reconcileOwnedRestart(ctx, commonDir, commonIdentity, layout, expected, backend)
	if err != nil || adopted != nil {
		return adopted, err
	}
	return spawnRestartedOwned(ctx, commonDir, commonIdentity, layout, expected, start, backend)
}

func reconcileOwnedRestart(
	ctx context.Context,
	commonDir string,
	commonIdentity pathIdentity,
	layout ownedLayout,
	expected state.HerdrServerIdentity,
	backend *Backend,
) (*OwnedSession, error) {
	current, found, err := readOwnerMarker(layout.markerPath)
	if err != nil {
		return nil, err
	}
	if found && !serverIdentityMatchesMarker(expected, current) {
		return adoptRestartedOwned(ctx, commonDir, commonIdentity, layout, current, backend)
	}
	if found {
		if err := validateExpectedOwnedMarker(expected, commonDir, commonIdentity, layout, current); err != nil {
			return nil, err
		}
	}
	return nil, retireAbsentOwnedGeneration(layout, expected, found, "restart")
}

func spawnRestartedOwned(
	ctx context.Context,
	commonDir string,
	commonIdentity pathIdentity,
	layout ownedLayout,
	expected state.HerdrServerIdentity,
	start supervisorStarter,
	backend *Backend,
) (*OwnedSession, error) {
	admitted := binaryAdmission{
		path: expected.BinaryPath, sha256: expected.BinarySHA256, version: expected.BinaryVersion,
	}
	launcher, err := prepareRestartedLauncher(expected, commonDir, commonIdentity, layout, admitted)
	if err != nil {
		return nil, err
	}
	marker, started, err := claimOwnedSession(
		layout, commonDir, commonIdentity, expected.Session, admitted, launcher,
		start, writeOwnerMarkerExclusive,
	)
	if err != nil {
		if started != nil {
			started.reapAsync()
		}
		return nil, fmt.Errorf("restart Herdr owned supervisor: %w", err)
	}
	backend = newReopenedOwnedBackend(layout, marker, admitted, backend)
	if err := waitForOwnedReady(ctx, backend); err != nil {
		started.reapAsync()
		return nil, fmt.Errorf("verify restarted Herdr owned server: %w", err)
	}
	started.reapAsync()
	return ownedSessionFromMarker(commonDir, marker, marker.LauncherPath, backend), nil
}

func prepareRestartedLauncher(
	expected state.HerdrServerIdentity,
	commonDir string,
	commonIdentity pathIdentity,
	layout ownedLayout,
	admitted binaryAdmission,
) (binaryAdmission, error) {
	previous := binaryAdmission{path: expected.LauncherPath, sha256: expected.LauncherSHA256}
	current, err := pinOwnedLauncher(layout)
	if err != nil {
		return binaryAdmission{}, err
	}
	if validatePrivateContents(layout.configPath, ownedConfigContents(current.path)) == nil {
		return current, validateRestartBundle(expected, commonDir, commonIdentity, layout, admitted, current)
	}
	if err := validateRestartBundle(expected, commonDir, commonIdentity, layout, admitted, previous); err != nil {
		return binaryAdmission{}, err
	}
	if err := atomicfs.WriteFile(layout.configPath, ownedConfigContents(current.path), 0o600); err != nil {
		return binaryAdmission{}, fmt.Errorf("replace Herdr owned launcher config for restart: %w", err)
	}
	if err := validatePrivateContents(layout.configPath, ownedConfigContents(current.path)); err != nil {
		return binaryAdmission{}, err
	}
	return current, validateRestartBundle(expected, commonDir, commonIdentity, layout, admitted, current)
}

// ShutdownOwned calls markIssued after its final preflight and immediately
// before stopping one live generation. A nil callback denotes a retry that
// confirms retirement without repeating the ambiguous signal.
func ShutdownOwned(
	ctx context.Context,
	opts OwnedOptions,
	expected state.HerdrServerIdentity,
	markIssued func() error,
) error {
	return shutdownOwned(ctx, opts, expected, markIssued, signalOwnedSupervisor, nil)
}

func shutdownOwned(
	ctx context.Context,
	opts OwnedOptions,
	expected state.HerdrServerIdentity,
	markIssued func() error,
	signal func(int) error,
	backend *Backend,
) error {
	commonDir, commonIdentity, layout, err := ownedLifecycleLayout(opts, expected)
	if err != nil {
		return err
	}
	err = requireOwnedServerIntent(commonDir, state.HerdrIntentShutdown, expected)
	if err != nil {
		return err
	}
	signalErr, err := beginOwnedShutdown(ctx, commonDir, commonIdentity, layout, expected, markIssued, signal, backend)
	if err != nil {
		return err
	}
	return waitForOwnedRetirement(ctx, layout, expected, signalErr, markIssued == nil)
}

func beginOwnedShutdown(
	ctx context.Context,
	commonDir string,
	commonIdentity pathIdentity,
	layout ownedLayout,
	expected state.HerdrServerIdentity,
	markIssued func() error,
	signal func(int) error,
	backend *Backend,
) (error, error) {
	lock, err := lockExistingPrivateFileContext(ctx, layout.lifecycleLock)
	if err != nil {
		return nil, fmt.Errorf("lock Herdr owned lifecycle for shutdown: %w", err)
	}
	defer unlockPrivateFile(lock)
	current, found, err := readOwnerMarker(layout.markerPath)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, verifyRetiredOwnedIdentity(layout, expected)
	}
	if err := validateExpectedOwnedMarker(expected, commonDir, commonIdentity, layout, current); err != nil {
		return nil, err
	}
	if markIssued == nil {
		return nil, reconcileRetriedOwnedShutdown(layout, expected)
	}
	return issueVerifiedOwnedShutdown(ctx, layout, current, expected, markIssued, signal, backend)
}

func issueVerifiedOwnedShutdown(
	ctx context.Context,
	layout ownedLayout,
	current ownerMarker,
	expected state.HerdrServerIdentity,
	markIssued func() error,
	signal func(int) error,
	backend *Backend,
) (error, error) {
	if err := verifyEmptyOwnedServer(ctx, layout, current, expected, backend); err != nil {
		return nil, err
	}
	if err := markIssued(); err != nil {
		return nil, fmt.Errorf("persist issued Herdr shutdown signal: %w", err)
	}
	return signal(expected.SupervisorPID), nil
}

func reconcileRetriedOwnedShutdown(layout ownedLayout, expected state.HerdrServerIdentity) error {
	if err := retireAbsentOwnedGeneration(layout, expected, true, "shutdown"); err != nil {
		return fmt.Errorf("herdr owned server shutdown result is unresolved; refusing to repeat the shutdown signal: %w", err)
	}
	return nil
}

func ownedLifecycleLayout(
	opts OwnedOptions,
	expected state.HerdrServerIdentity,
) (string, pathIdentity, ownedLayout, error) {
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(opts.GitCommonDir)
	if err != nil {
		return "", pathIdentity{}, ownedLayout{}, err
	}
	session := naming.HerdrSessionName(commonIdentity.device, commonIdentity.inode)
	layout, err := prepareOwnedLayout(opts.RuntimeBase, session)
	if err != nil {
		return "", pathIdentity{}, ownedLayout{}, err
	}
	checks := []bool{
		expected.GitCommonDir == commonDir, expected.RuntimeDir == layout.runtimeDir,
		expected.Session == session, expected.SocketPath == layout.socketPath,
		expected.ClientSocketPath == layout.clientSocketPath,
	}
	for _, ok := range checks {
		if !ok {
			return "", pathIdentity{}, ownedLayout{}, fmt.Errorf("herdr server lifecycle identity does not match this repository")
		}
	}
	return commonDir, commonIdentity, layout, nil
}

func requireOwnedServerIntent(
	commonDir string,
	kind state.HerdrIntentKind,
	expected state.HerdrServerIdentity,
) error {
	journal, err := state.LoadHerdrIntentsPath(filepath.Join(commonDir, "fanout", "herdr-intents.json"))
	if err != nil {
		return err
	}
	intent, found, err := journal.ServerLifecycleIntent()
	if err != nil {
		return err
	}
	if !found || intent.Kind != kind || intent.Server == nil || *intent.Server != expected {
		return fmt.Errorf("matching explicit Herdr server %s intent is not saved", lifecycleAction(kind))
	}
	return nil
}

func lifecycleAction(kind state.HerdrIntentKind) string {
	if kind == state.HerdrIntentShutdown {
		return "shutdown"
	}
	return "restart"
}

func serverIdentity(
	commonDir string,
	marker ownerMarker,
	serverPID int,
) state.HerdrServerIdentity {
	return state.HerdrServerIdentity{
		GitCommonDir: commonDir, RuntimeDir: marker.RuntimeDir, Session: marker.Session,
		SocketPath: marker.SocketPath, ClientSocketPath: marker.ClientSocketPath,
		OwnerNonce: marker.OwnerNonce, SupervisorPID: marker.SupervisorPID,
		SupervisorStartToken: marker.SupervisorStartToken, ServerPID: serverPID,
		BinaryPath: marker.BinaryPath, BinarySHA256: marker.BinarySHA256,
		BinaryVersion: marker.BinaryVersion, LauncherPath: marker.LauncherPath,
		LauncherSHA256: marker.LauncherSHA256,
	}
}

func serverIdentityMatchesMarker(expected state.HerdrServerIdentity, marker ownerMarker) bool {
	return expected == serverIdentity(marker.GitCommonDir, marker, expected.ServerPID)
}

func validateSupervisorLease(marker ownerMarker, lease supervisorLease) error {
	if lease.SchemaID != ownedMarkerSchemaID || lease.OwnerNonce != marker.OwnerNonce ||
		lease.StartToken != marker.SupervisorStartToken || lease.PID != marker.SupervisorPID {
		return fmt.Errorf("herdr supervisor lease does not match ownership marker")
	}
	if lease.ServerPID != 0 && lease.ServerPID <= 1 {
		return fmt.Errorf("herdr supervisor lease has an invalid server pid")
	}
	return nil
}

func validateExpectedOwnedMarker(
	expected state.HerdrServerIdentity,
	commonDir string,
	commonIdentity pathIdentity,
	layout ownedLayout,
	marker ownerMarker,
) error {
	if !serverIdentityMatchesMarker(expected, marker) {
		return fmt.Errorf("herdr ownership marker changed before server lifecycle operation")
	}
	admitted := binaryAdmission{
		path: expected.BinaryPath, sha256: expected.BinarySHA256, version: expected.BinaryVersion,
	}
	launcher := binaryAdmission{path: expected.LauncherPath, sha256: expected.LauncherSHA256}
	return validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, launcher)
}

func retireAbsentOwnedGeneration(
	layout ownedLayout,
	expected state.HerdrServerIdentity,
	markerFound bool,
	action string,
) error {
	lease, leaseFound, running, err := inspectLifecycleLease(layout.supervisorLock)
	if err != nil {
		return err
	}
	if err := validateRetiredServerLease(expected, lease, leaseFound, markerFound); err != nil {
		return err
	}
	if running {
		return fmt.Errorf("%w; refusing %s", corebackend.ErrOwnedGenerationStillLive, action)
	}
	if err := verifySavedProcessesAbsent(expected, action); err != nil {
		return err
	}
	if err := verifyOwnedSocketsAbsent(expected, action); err != nil {
		return err
	}
	if err := removeRetiredOwnedMarker(layout.markerPath, markerFound); err != nil {
		return err
	}
	if leaseFound {
		if err := os.Remove(layout.supervisorLock); err != nil {
			return fmt.Errorf("remove retired Herdr supervisor lease: %w", err)
		}
	}
	return nil
}

func validateRetiredServerLease(
	expected state.HerdrServerIdentity,
	lease supervisorLease,
	found bool,
	markerFound bool,
) error {
	if !found && !markerFound {
		return nil
	}
	if !found || lease.ServerPID <= 1 || lease.ServerPID != expected.ServerPID ||
		validateSupervisorLease(markerFromServerIdentity(expected), lease) != nil {
		return fmt.Errorf("saved Herdr supervisor/server lease cannot prove the old generation")
	}
	return nil
}

func removeRetiredOwnedMarker(path string, found bool) error {
	if !found {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove retired Herdr ownership marker: %w", err)
	}
	return nil
}

func inspectLifecycleLease(path string) (supervisorLease, bool, bool, error) {
	lease, running, err := inspectExistingSupervisorLease(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errUnpublishedSupervisorLease) {
		return supervisorLease{}, false, false, nil
	}
	return lease, err == nil, running, err
}

func verifySavedProcessesAbsent(identity state.HerdrServerIdentity, action string) error {
	return verifySavedProcessesAbsentWithProbe(identity, action, func(pid int) error {
		return syscall.Kill(pid, 0)
	})
}

func verifySavedProcessesAbsentWithProbe(
	identity state.HerdrServerIdentity,
	action string,
	probe func(int) error,
) error {
	checks := []struct {
		label    string
		identity int
		target   int
	}{
		{label: "supervisor", identity: identity.SupervisorPID, target: identity.SupervisorPID},
		{label: "server", identity: identity.ServerPID, target: identity.ServerPID},
		{label: "server process group", identity: identity.ServerPID, target: -identity.ServerPID},
	}
	for _, check := range checks {
		if check.identity <= 1 {
			return fmt.Errorf("saved Herdr %s identity is unavailable", check.label)
		}
		err := probe(check.target)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err == nil || errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("saved Herdr %s %d is still live; refusing %s", check.label, check.identity, action)
		}
		return fmt.Errorf("inspect saved Herdr %s %d: %w", check.label, check.identity, err)
	}
	return nil
}

func verifyOwnedSocketsAbsent(identity state.HerdrServerIdentity, action string) error {
	for _, path := range []string{identity.SocketPath, identity.ClientSocketPath} {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil {
			return fmt.Errorf("saved Herdr socket %s is still present; refusing %s", path, action)
		}
		return fmt.Errorf("inspect saved Herdr socket %s: %w", path, err)
	}
	return nil
}

func markerFromServerIdentity(identity state.HerdrServerIdentity) ownerMarker {
	configHome := filepath.Join(identity.RuntimeDir, "xdg-config")
	return ownerMarker{
		SchemaID: ownedMarkerSchemaID, GitCommonDir: identity.GitCommonDir,
		OwnerNonce: identity.OwnerNonce, Session: identity.Session, RuntimeDir: identity.RuntimeDir,
		SocketPath: identity.SocketPath, ClientSocketPath: identity.ClientSocketPath,
		BinaryPath: identity.BinaryPath, BinarySHA256: identity.BinarySHA256,
		BinaryVersion: identity.BinaryVersion, SupervisorPID: identity.SupervisorPID,
		SupervisorStartToken: identity.SupervisorStartToken, XDGConfigHome: configHome,
		XDGStateHome: filepath.Join(identity.RuntimeDir, "xdg-state"),
		XDGDataHome:  filepath.Join(identity.RuntimeDir, "xdg-data"),
		XDGCacheHome: filepath.Join(identity.RuntimeDir, "xdg-cache"),
		ConfigPath:   filepath.Join(configHome, "herdr", "config.toml"),
		LauncherPath: identity.LauncherPath, LauncherSHA256: identity.LauncherSHA256,
	}
}

func validateRestartBundle(
	expected state.HerdrServerIdentity,
	commonDir string,
	commonIdentity pathIdentity,
	layout ownedLayout,
	admitted binaryAdmission,
	launcher binaryAdmission,
) error {
	if err := validatePinnedBinaryInDir(expected.LauncherPath, expected.LauncherSHA256, layout.launcherDir); err != nil {
		return fmt.Errorf("previous Herdr owned launcher identity changed: %w", err)
	}
	marker := markerFromServerIdentity(expected)
	marker.GitCommonDevice = commonIdentity.device
	marker.GitCommonInode = commonIdentity.inode
	marker.LauncherPath = launcher.path
	marker.LauncherSHA256 = launcher.sha256
	return validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, launcher)
}

func adoptRestartedOwned(
	ctx context.Context,
	commonDir string,
	commonIdentity pathIdentity,
	layout ownedLayout,
	marker ownerMarker,
	backend *Backend,
) (*OwnedSession, error) {
	admitted := binaryAdmission{
		path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
	}
	launcher := binaryAdmission{path: marker.LauncherPath, sha256: marker.LauncherSHA256}
	if err := validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, launcher); err != nil {
		return nil, err
	}
	if err := verifyLiveSupervisor(layout.supervisorLock, marker); err != nil {
		return nil, fmt.Errorf("restarted Herdr supervisor is not proven live: %w", err)
	}
	backend = newReopenedOwnedBackend(layout, marker, admitted, backend)
	if err := validateOwnedReady(ctx, backend); err != nil {
		return nil, fmt.Errorf("restarted Herdr server result is unresolved: %w", err)
	}
	return ownedSessionFromMarker(commonDir, marker, currentOwnedEmitterPath(marker), backend), nil
}

func verifyEmptyOwnedServer(
	ctx context.Context,
	layout ownedLayout,
	marker ownerMarker,
	expected state.HerdrServerIdentity,
	backend *Backend,
) error {
	lease, running, err := inspectExistingSupervisorLease(layout.supervisorLock)
	if err != nil || !running {
		return fmt.Errorf("verify live Herdr supervisor before shutdown: %w", errors.Join(err, errOwnedSupervisorNotRunning))
	}
	leaseErr := validateSupervisorLease(marker, lease)
	if leaseErr != nil ||
		expected.ServerPID > 1 && lease.ServerPID != expected.ServerPID {
		return fmt.Errorf("herdr supervisor/server lease changed before shutdown")
	}
	admitted := binaryAdmission{
		path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
	}
	backend = newReopenedOwnedBackend(layout, marker, admitted, backend)
	probed, err := backend.probeOwned(ctx, *backend.owner)
	if err != nil {
		return err
	}
	workspaces, err := backend.observeOwnedWorkspaces(ctx, probed)
	if err != nil {
		return fmt.Errorf("observe Herdr workspaces before shutdown: %w", err)
	}
	if len(workspaces) != 0 {
		return fmt.Errorf("herdr owned server has %d active or foreign workspace resources", len(workspaces))
	}
	return nil
}

func signalOwnedSupervisor(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}

func waitForOwnedRetirement(
	ctx context.Context,
	layout ownedLayout,
	expected state.HerdrServerIdentity,
	signalErr error,
	configMayBeAbsent bool,
) error {
	deadline := time.Now().Add(ownedShutdownGrace + ownedReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return errors.Join(signalErr, err)
		}
		lastErr = verifyRetiredOwnedIdentity(layout, expected)
		if lastErr == nil {
			return removeRetiredOwnedConfig(layout, expected, configMayBeAbsent)
		}
		timer := time.NewTimer(ownedReadyInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(signalErr, ctx.Err())
		case <-timer.C:
		}
	}
	return errors.Join(signalErr, fmt.Errorf("herdr owned server shutdown result is unresolved: %w", lastErr))
}

func removeRetiredOwnedConfig(
	layout ownedLayout,
	expected state.HerdrServerIdentity,
	mayBeAbsent bool,
) error {
	_, err := os.Lstat(layout.configPath)
	if errors.Is(err, os.ErrNotExist) && mayBeAbsent {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect retired Herdr owned config: %w", err)
	}
	if err := validateCompatibleOwnedConfig(layout.configPath, expected.LauncherPath); err != nil {
		return fmt.Errorf("validate retired Herdr owned config: %w", err)
	}
	if err := os.Remove(layout.configPath); err != nil {
		return fmt.Errorf("remove retired Herdr owned config: %w", err)
	}
	if _, err := os.Lstat(layout.configPath); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("verify retired Herdr owned config removal: %w", err)
		}
		return fmt.Errorf("retired Herdr owned config remains after removal")
	}
	return nil
}

func verifyRetiredOwnedIdentity(
	layout ownedLayout,
	expected state.HerdrServerIdentity,
) error {
	if err := validateRetiredOwnedSession(layout); err != nil {
		return err
	}
	for _, pid := range []int{expected.SupervisorPID, expected.ServerPID} {
		if pid <= 1 {
			continue
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("saved Herdr process %d remains after shutdown", pid)
		}
	}
	return nil
}
