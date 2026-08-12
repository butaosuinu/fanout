package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestRestartOwnedRejectsLiveGenerationWithoutSpawning(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.HerdrIntentRestart, expected)
	previous, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("read old marker = (%+v, %t, %v)", previous, found, err)
	}

	_, err = restartOwned(context.Background(), h.ownedOptions(), expected, h.supervisor.start, h.session.backend)
	if err == nil || !strings.Contains(err.Error(), "supervisor is still live") {
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
	saveOwnedServerIntent(t, h, state.HerdrIntentRestart, expected)

	if _, err := h.session.LivePanes(context.Background()); err != nil {
		t.Fatalf("read-only LivePanes() under restart intent: %v", err)
	}
	if err := bound.Focus(target.Ref); err == nil || !strings.Contains(err.Error(), "restart is pending") {
		t.Fatalf("Focus() under restart intent error = %v", err)
	}
	if _, err := h.tryEnsure(); err == nil || !strings.Contains(err.Error(), "restart is pending") {
		t.Fatalf("EnsureOwned() under restart intent error = %v", err)
	}
}

func TestRestartOwnedSpawnsOnceAfterOldGenerationIsAbsent(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.HerdrIntentRestart, expected)
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
	saveOwnedServerIntent(t, h, state.HerdrIntentRestart, expected)
	retireFakeSupervisorForRestart(t, h, true)

	if _, err := restartOwned(context.Background(), h.ownedOptions(), expected, h.supervisor.start, h.session.backend); err != nil {
		t.Fatal(err)
	}
	if h.supervisor.starts != 2 {
		t.Fatalf("supervisor starts = %d, want 2", h.supervisor.starts)
	}
}

func TestShutdownOwnedRejectsResourcesAndDoesNotSignalOnRetry(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.HerdrIntentShutdown, expected)
	signals := 0
	signal := func(int) error { signals++; return nil }

	err := shutdownOwned(context.Background(), h.ownedOptions(), expected, true, signal, h.session.backend)
	if err == nil || !strings.Contains(err.Error(), "workspace resources") || signals != 0 {
		t.Fatalf("shutdown with resources = %v, signals=%d", err, signals)
	}
	err = shutdownOwned(context.Background(), h.ownedOptions(), expected, false, signal, h.session.backend)
	if err == nil || !strings.Contains(err.Error(), "refusing to repeat") || signals != 0 {
		t.Fatalf("shutdown retry = %v, signals=%d", err, signals)
	}
}

func TestShutdownOwnedStopsEmptyGenerationAndVerifiesRetirement(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.HerdrIntentShutdown, expected)
	h.fake.snapshot = emptyOwnedSnapshot(h.fake.snapshot)
	signals := 0
	signal := func(pid int) error {
		signals++
		if pid != expected.SupervisorPID {
			return errors.New("wrong supervisor pid")
		}
		retireFakeOwnedGeneration(t, h)
		return nil
	}

	if err := shutdownOwned(context.Background(), h.ownedOptions(), expected, true, signal, h.session.backend); err != nil {
		t.Fatal(err)
	}
	if signals != 1 {
		t.Fatalf("shutdown signals = %d, want 1", signals)
	}
	if err := validateRetiredOwnedSession(h.layout); err != nil {
		t.Fatalf("retired owned session: %v", err)
	}
}

func TestShutdownRetryCompletesAbsentGenerationWithoutSignal(t *testing.T) {
	h := newOwnedHarness(t)
	expected := inspectOwnedServerForTest(t, h)
	saveOwnedServerIntent(t, h, state.HerdrIntentShutdown, expected)
	retireFakeSupervisorForRestart(t, h, false)
	signals := 0

	err := shutdownOwned(
		context.Background(), h.ownedOptions(), expected, false,
		func(int) error { signals++; return nil }, h.session.backend,
	)
	if err != nil {
		t.Fatal(err)
	}
	if signals != 0 {
		t.Fatalf("shutdown retry signals = %d, want 0", signals)
	}
	if err := validateRetiredOwnedSession(h.layout); err != nil {
		t.Fatalf("retired owned session: %v", err)
	}
}

func (h *ownedHarness) ownedOptions() OwnedOptions {
	return OwnedOptions{GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase}
}

func inspectOwnedServerForTest(t *testing.T, h *ownedHarness) state.HerdrServerIdentity {
	t.Helper()
	identity, err := InspectOwnedServer(h.ownedOptions())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func saveOwnedServerIntent(
	t *testing.T,
	h *ownedHarness,
	kind state.HerdrIntentKind,
	identity state.HerdrServerIdentity,
) {
	t.Helper()
	id, err := state.HerdrServerIntentID(kind)
	if err != nil {
		t.Fatal(err)
	}
	store := state.HerdrIntents{
		SchemaVersion: state.HerdrIntentsSchemaVersion,
		Intents: []state.HerdrIntent{{
			ID: id, Kind: kind, Status: state.HerdrIntentPlanned, Server: &identity,
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
