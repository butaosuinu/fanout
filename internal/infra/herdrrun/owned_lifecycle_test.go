package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"

	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestRestartOwnedRejectsLiveGenerationWithoutSpawning(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentRestart, expected)
	previous, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("read old marker = (%+v, %t, %v)", previous, found, err)
	}

	_, err = restartOwned(context.Background(), h.ownedOptions(), expected, h.supervisor.start, h.session.backend)
	if !errors.Is(err, corebackend.ErrOwnedGenerationStillLive) {
		t.Fatalf("restart live generation error = %v", err)
	}
	current, found, readErr := readOwnerMarker(h.layout.markerPath)
	if readErr != nil || !found || current != previous || h.supervisor.starts != 1 {
		t.Fatalf("restart mutated live generation: marker=(%+v, %t, %v), starts=%d", current, found, readErr, h.supervisor.starts)
	}
}

func TestRestartIntentAllowsReadsAndRejectsMutationsAndBootstrap(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	bound, err := h.session.Backend().BindOwnedTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentRestart, expected)

	if _, err := h.session.LivePanes(context.Background()); err != nil {
		t.Fatalf("read-only LivePanes() under restart intent: %v", err)
	}
	if err := bound.Focus(target.Ref); err == nil || !strings.Contains(err.Error(), "restart is pending") {
		t.Fatalf("Focus() under restart intent error = %v", err)
	}
	if _, err := h.session.PrepareNudge(context.Background(), corebackend.NudgeTarget{
		Ref: target.Ref, SessionID: target.SessionID, SocketPath: target.SocketPath,
		TerminalID: target.TerminalID, AgentID: target.AgentID, AgentSession: target.AgentSession,
	}, "nudge"); err == nil || !strings.Contains(err.Error(), "restart is pending") {
		t.Fatalf("PrepareNudge() under restart intent error = %v", err)
	}
	for name, mutation := range map[string]func() error{
		"worktree remove": func() error {
			return h.session.RemoveWorktree(context.Background(), "w2", "/repo/child")
		},
		"workspace close": func() error {
			return h.session.CloseWorkspace(context.Background(), "w2")
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := mutation()
			if !errors.Is(err, corebackend.ErrMutationNotIssued) || !strings.Contains(err.Error(), "restart is pending") {
				t.Fatalf("cleanup mutation under restart intent error = %v", err)
			}
		})
	}
	if _, err := h.tryEnsure(); err == nil || !strings.Contains(err.Error(), "restart is pending") {
		t.Fatalf("EnsureOwned() under restart intent error = %v", err)
	}
}

func TestRestartOwnedSpawnsOnceAfterOldGenerationIsAbsent(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentRestart, expected)
	retireFakeSupervisorForRestart(t, h, false)

	restarted, err := restartOwned(context.Background(), h.ownedOptions(), expected, h.supervisor.start, h.session.backend)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Session != expected.Session || h.supervisor.starts != 2 {
		t.Fatalf("restarted session = %+v, starts=%d", restarted, h.supervisor.starts)
	}
	current := inspectOwnedServerForTest(t, h)
	if current.OwnerNonce == expected.OwnerNonce || current.SupervisorPID == expected.SupervisorPID {
		t.Fatalf("restart retained old generation identity: old=%+v new=%+v", expected, current)
	}
	recovered, err := restartOwned(
		context.Background(), h.ownedOptions(), expected, h.supervisor.start, restarted.backend,
	)
	if err != nil || recovered.Session != expected.Session || h.supervisor.starts != 2 {
		t.Fatalf("restart replay = (%+v, %v), starts=%d", recovered, err, h.supervisor.starts)
	}
}

func TestRestartOwnedRecoversAfterOldMarkerAndLeaseRemoval(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentRestart, expected)
	retireFakeSupervisorForRestart(t, h, true)

	if _, err := restartOwned(context.Background(), h.ownedOptions(), expected, h.supervisor.start, h.session.backend); err != nil {
		t.Fatal(err)
	}
	if h.supervisor.starts != 2 {
		t.Fatalf("supervisor starts = %d, want 2", h.supervisor.starts)
	}
}

func TestRestartOwnedRecoversFromUnpublishedSupervisorLease(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentRestart, expected)
	retireFakeSupervisorForRestart(t, h, true)
	if err := os.WriteFile(h.layout.supervisorLock, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := restartOwned(
		context.Background(), h.ownedOptions(), expected, h.supervisor.start, h.session.backend,
	); err != nil {
		t.Fatal(err)
	}
	if h.supervisor.starts != 2 {
		t.Fatalf("supervisor starts = %d, want 2", h.supervisor.starts)
	}
}

func TestRestartOwnedPinsCurrentLauncherAndRecoversAfterConfigUpdate(t *testing.T) {
	h := newOwnedHarness(t)
	current, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("read current marker = (%+v, %t, %v)", current, found, err)
	}
	previous := installLegacyOwnedLauncher(t, h)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentRestart, expected)
	retireFakeSupervisorForRestart(t, h, true)

	commonDir, commonIdentity, err := openCanonicalGitCommonDir(h.commonDir)
	if err != nil {
		t.Fatal(err)
	}
	admitted := binaryAdmission{
		path: expected.BinaryPath, sha256: expected.BinarySHA256, version: expected.BinaryVersion,
	}
	pinned, err := prepareRestartedLauncher(expected, commonDir, commonIdentity, h.layout, admitted)
	if err != nil || pinned.path != current.LauncherPath || pinned.sha256 != current.LauncherSHA256 {
		t.Fatalf("prepare restarted launcher = (%+v, %v), want current %+v", pinned, err, current)
	}

	restarted, err := restartOwned(
		context.Background(), h.ownedOptions(), expected, h.supervisor.start, h.session.backend,
	)
	if err != nil {
		t.Fatal(err)
	}
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("read restarted marker = (%+v, %t, %v)", marker, found, err)
	}
	if marker.LauncherPath != current.LauncherPath || marker.LauncherSHA256 != current.LauncherSHA256 ||
		restarted.LauncherPath != current.LauncherPath || marker.LauncherPath == previous.path {
		t.Fatalf("restarted launcher = marker:%+v session:%+v, want current %+v", marker, restarted, current)
	}
}

func TestPrepareRestartedLauncherMigratesLegacyOwnedConfig(t *testing.T) {
	h := newOwnedHarness(t)
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("read current marker = (%+v, %t, %v)", marker, found, err)
	}
	if writeErr := atomicfs.WriteFile(h.layout.configPath, legacyOwnedConfigContents(marker.LauncherPath), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	expected := inspectOwnedServerForTest(t, h)
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(h.commonDir)
	if err != nil {
		t.Fatal(err)
	}
	admitted := binaryAdmission{
		path: expected.BinaryPath, sha256: expected.BinarySHA256, version: expected.BinaryVersion,
	}

	pinned, err := prepareRestartedLauncher(expected, commonDir, commonIdentity, h.layout, admitted)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.path != marker.LauncherPath || pinned.sha256 != marker.LauncherSHA256 {
		t.Fatalf("pinned launcher = %+v, want marker launcher", pinned)
	}
	if err := validatePrivateContents(h.layout.configPath, ownedConfigContents(marker.LauncherPath)); err != nil {
		t.Fatalf("migrated config: %v", err)
	}
}

func TestPrepareRestartedLauncherRecoversPlainConfigWithOrphanDashboardDescriptor(t *testing.T) {
	for _, previousLauncher := range []bool{false, true} {
		name := "current launcher"
		if previousLauncher {
			name = "previous launcher"
		}
		t.Run(name, func(t *testing.T) {
			h := newOwnedHarness(t)
			marker, found, err := readOwnerMarker(h.layout.markerPath)
			if err != nil || !found {
				t.Fatalf("read current marker = (%+v, %t, %v)", marker, found, err)
			}
			configLauncher := binaryAdmission{path: marker.LauncherPath, sha256: marker.LauncherSHA256}
			if previousLauncher {
				configLauncher = installLegacyOwnedLauncher(t, h)
			}
			descriptor := dashboardDescriptor{
				SchemaID:   dashboardDescriptorSchemaID,
				HelperPath: configLauncher.path, HelperSHA256: configLauncher.sha256,
				DashboardPath: configLauncher.path, DashboardSHA256: configLauncher.sha256,
				StatePath:   state.Path(h.checkout),
				Environment: []string{"PATH=/usr/bin"},
			}
			if err := writeDashboardDescriptor(h.layout, descriptor); err != nil {
				t.Fatal(err)
			}
			expected := inspectOwnedServerForTest(t, h)
			commonDir, commonIdentity, err := openCanonicalGitCommonDir(h.commonDir)
			if err != nil {
				t.Fatal(err)
			}
			admitted := binaryAdmission{
				path: expected.BinaryPath, sha256: expected.BinarySHA256, version: expected.BinaryVersion,
			}

			pinned, err := prepareRestartedLauncher(expected, commonDir, commonIdentity, h.layout, admitted)
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePrivateContents(h.layout.configPath, ownedConfigContents(pinned.path)); err != nil {
				t.Fatalf("plain config after restart preparation: %v", err)
			}
			if _, err := os.Lstat(h.layout.dashboardDescriptorPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("orphan dashboard descriptor remains: %v", err)
			}
		})
	}
}

func TestRestartOwnedPreservesDashboardShortcutConfig(t *testing.T) {
	h := newOwnedHarness(t)
	h.fake.respond = func(args []string) ([]byte, error) {
		if slices.Equal(args, []string{"server", "reload-config"}) {
			return []byte(appliedDashboardReloadEnvelope), nil
		}
		return nil, errors.New("unexpected command")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if syncErr := h.session.Backend().SyncDashboardShortcut(corebackend.DashboardShortcutOptions{
		Enabled: true, FanoutBin: executable, StatePath: state.Path(h.checkout),
		Environment: []string{"HOME=/home/operator", "PATH=/usr/bin"},
	}); syncErr != nil {
		t.Fatal(syncErr)
	}
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentRestart, expected)
	retireFakeSupervisorForRestart(t, h, false)
	restarted, err := restartOwned(
		context.Background(), h.ownedOptions(), expected, h.supervisor.start, h.session.backend,
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, found, err := readDashboardDescriptor(h.layout)
	if err != nil || !found {
		t.Fatalf("dashboard descriptor after restart = (%+v, %t, %v)", descriptor, found, err)
	}
	expectedConfig := ownedDashboardConfigContents(
		restarted.LauncherPath, descriptor.HelperPath, h.layout.dashboardDescriptorPath,
	)
	if err := validatePrivateContents(h.layout.configPath, expectedConfig); err != nil {
		t.Fatalf("dashboard config after restart: %v", err)
	}
}

func TestShutdownOwnedRejectsResourcesAndDoesNotSignalOnRetry(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentShutdown, expected)
	issued := 0
	signals := 0
	signal := func(int) error { signals++; return nil }
	markIssued := func() error { issued++; return nil }

	err := shutdownOwned(context.Background(), h.ownedOptions(), expected, markIssued, signal, h.session.backend)
	if err == nil || !strings.Contains(err.Error(), "workspace resources") || issued != 0 || signals != 0 {
		t.Fatalf("shutdown with resources = %v, issued=%d, signals=%d", err, issued, signals)
	}
	err = shutdownOwned(context.Background(), h.ownedOptions(), expected, nil, signal, h.session.backend)
	if err == nil || !strings.Contains(err.Error(), "refusing to repeat") || signals != 0 {
		t.Fatalf("shutdown retry = %v, signals=%d", err, signals)
	}
}

func TestShutdownOwnedDoesNotSignalWhenIssuedSaveFails(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentShutdown, expected)
	h.fake.snapshot = emptyOwnedSnapshot(h.fake.snapshot)
	injected := errors.New("save issued shutdown")
	signals := 0

	err := shutdownOwned(
		context.Background(), h.ownedOptions(), expected, func() error { return injected },
		func(int) error { signals++; return nil }, h.session.backend,
	)
	if !errors.Is(err, injected) || signals != 0 {
		t.Fatalf("shutdown after issued save failure = %v, signals=%d", err, signals)
	}
}

func TestShutdownOwnedStopsEmptyGenerationAndVerifiesRetirement(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentShutdown, expected)
	h.fake.snapshot = emptyOwnedSnapshot(h.fake.snapshot)
	issued := false
	signals := 0
	signal := func(pid int) error {
		signals++
		if !issued || pid != expected.SupervisorPID {
			return errors.New("wrong supervisor pid")
		}
		retireFakeOwnedGeneration(t, h)
		return nil
	}

	if err := shutdownOwned(context.Background(), h.ownedOptions(), expected, func() error {
		issued = true
		return nil
	}, signal, h.session.backend); err != nil {
		t.Fatal(err)
	}
	if signals != 1 {
		t.Fatalf("shutdown signals = %d, want 1", signals)
	}
	if err := validateRetiredOwnedSession(h.layout); err != nil {
		t.Fatalf("retired owned session: %v", err)
	}
}

func TestShutdownOwnedAllowsCurrentLauncherBootstrapAfterUpdate(t *testing.T) {
	h := newOwnedHarness(t)
	current, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("read current marker = (%+v, %t, %v)", current, found, err)
	}
	previous := installLegacyOwnedLauncher(t, h)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentShutdown, expected)
	h.fake.snapshot = emptyOwnedSnapshot(h.fake.snapshot)

	err = shutdownOwned(context.Background(), h.ownedOptions(), expected, func() error { return nil }, func(int) error {
		retireFakeOwnedGeneration(t, h)
		return nil
	}, h.session.backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(h.layout.configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired config remains: %v", err)
	}
	clearOwnedServerIntents(t, h)
	restarted := h.ensure()
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("read bootstrapped marker = (%+v, %t, %v)", marker, found, err)
	}
	if marker.LauncherPath != current.LauncherPath || marker.LauncherSHA256 != current.LauncherSHA256 ||
		restarted.LauncherPath != current.LauncherPath || marker.LauncherPath == previous.path {
		t.Fatalf("bootstrapped launcher = marker:%+v session:%+v, want current %+v", marker, restarted, current)
	}
}

func TestShutdownRetryCompletesAbsentGenerationWithoutSignal(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.IntentShutdown, expected)
	retireFakeSupervisorForRestart(t, h, false)
	signals := 0

	err := shutdownOwned(
		context.Background(), h.ownedOptions(), expected, nil,
		func(int) error { signals++; return nil }, h.session.backend,
	)
	if err != nil {
		t.Fatal(err)
	}
	if signals != 0 {
		t.Fatalf("shutdown retry signals = %d, want 0", signals)
	}
	if err = shutdownOwned(
		context.Background(), h.ownedOptions(), expected, nil,
		func(int) error { signals++; return nil }, h.session.backend,
	); err != nil {
		t.Fatalf("shutdown replay after config removal: %v", err)
	}
	if signals != 0 {
		t.Fatalf("shutdown replay signals = %d, want 0", signals)
	}
	if err := validateRetiredOwnedSession(h.layout); err != nil {
		t.Fatalf("retired owned session: %v", err)
	}
}

func TestVerifySavedProcessesAbsentRejectsLiveServerProcessGroup(t *testing.T) {
	identity := state.RuntimeServerIdentity{SupervisorPID: 42, ServerPID: 43}
	var targets []int
	err := verifySavedProcessesAbsentWithProbe(identity, "restart", func(pid int) error {
		targets = append(targets, pid)
		if pid == -identity.ServerPID {
			return nil
		}
		return syscall.ESRCH
	})
	if err == nil || !strings.Contains(err.Error(), "server process group") {
		t.Fatalf("live server process group error = %v", err)
	}
	want := []int{identity.SupervisorPID, identity.ServerPID, -identity.ServerPID}
	if !slices.Equal(targets, want) {
		t.Fatalf("process absence targets = %v, want %v", targets, want)
	}
}

func (h *ownedHarness) ownedOptions() OwnedOptions {
	return OwnedOptions{GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase}
}

func inspectOwnedServerForTest(t *testing.T, h *ownedHarness) state.RuntimeServerIdentity {
	t.Helper()
	identity, err := InspectOwnedServer(h.ownedOptions())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func installLegacyOwnedLauncher(t *testing.T, h *ownedHarness) binaryAdmission {
	t.Helper()
	source := filepath.Join(h.root, "legacy-fanout")
	if err := os.WriteFile(source, []byte("legacy fanout launcher\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, digest, err := stageExecutable(source, h.layout.launcherDir)
	if err != nil {
		t.Fatal(err)
	}
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("read marker for legacy launcher = (%+v, %t, %v)", marker, found, err)
	}
	marker.LauncherPath, marker.LauncherSHA256 = path, digest
	if err = os.Remove(h.layout.markerPath); err != nil {
		t.Fatal(err)
	}
	if err = writeOwnerMarkerExclusive(h.layout.markerPath, marker); err != nil {
		t.Fatal(err)
	}
	if err = atomicfs.WriteFile(h.layout.configPath, ownedConfigContents(path), 0o600); err != nil {
		t.Fatal(err)
	}
	return binaryAdmission{path: path, sha256: digest}
}

func saveOwnedServerIntent(
	t *testing.T,
	h *ownedHarness,
	kind state.LaunchIntentKind,
	identity state.RuntimeServerIdentity,
) {
	t.Helper()
	id, err := state.ServerIntentID(kind)
	if err != nil {
		t.Fatal(err)
	}
	store := state.LaunchJournal{
		SchemaVersion: state.LaunchJournalSchemaVersion,
		Intents: []state.LaunchIntent{{
			ID: id, Kind: kind, Status: state.IntentPlanned, Server: &identity,
		}},
	}
	data, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.commonDir, "fanout", "herdr-intents.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func clearOwnedServerIntents(t *testing.T, h *ownedHarness) {
	t.Helper()
	data, err := json.Marshal(state.LaunchJournal{
		SchemaVersion: state.LaunchJournalSchemaVersion,
		Intents:       []state.LaunchIntent{},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.commonDir, "fanout", "herdr-intents.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func retireFakeSupervisorForRestart(t *testing.T, h *ownedHarness, removeIdentity bool) {
	t.Helper()
	h.supervisor.close()
	removeOwnedSockets(t, h.layout)
	if !removeIdentity {
		return
	}
	for _, path := range []string{h.layout.markerPath, h.layout.supervisorLock} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func retireFakeOwnedGeneration(t *testing.T, h *ownedHarness) {
	t.Helper()
	h.supervisor.close()
	removeOwnedSockets(t, h.layout)
	if err := os.Remove(h.layout.markerPath); err != nil {
		t.Fatal(err)
	}
}

func removeOwnedSockets(t *testing.T, layout ownedLayout) {
	t.Helper()
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func emptyOwnedSnapshot(source string) string {
	return mutateSnapshot(source, func(snapshot *snapshotJSON) {
		workspaces := []workspaceJSON{}
		tabs := []json.RawMessage{}
		panes := []paneJSON{}
		layouts := []json.RawMessage{}
		agents := []agentJSON{}
		snapshot.Workspaces, snapshot.Tabs, snapshot.Panes = &workspaces, &tabs, &panes
		snapshot.Layouts, snapshot.Agents = &layouts, &agents
	})
}
