package herdrrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
)

func TestOpenOwnedDoesNotCreateMissingOwnedLayout(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, "repo.git")
	if err := os.Mkdir(commonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeBase := filepath.Join(root, "runtime")
	if _, err := OpenOwned(context.Background(), OwnedOptions{
		GitCommonDir: commonDir, RuntimeBase: runtimeBase,
	}); err == nil {
		t.Fatal("OpenOwned() succeeded without an existing owner layout")
	}
	if _, err := os.Lstat(runtimeBase); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenOwned() created runtime layout: %v", err)
	}
}

func TestOpenOwnedReadoptsExistingOwnedSession(t *testing.T) {
	h := newOwnedHarness(t)
	observed := New(h.session.Session, h.session.SocketPath)
	observed.output = h.fake.output
	opened, err := openOwned(context.Background(), OwnedOptions{
		GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase,
	}, observed)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Session != h.session.Session || opened.SocketPath != h.session.SocketPath ||
		opened.GitCommonDir != h.session.GitCommonDir {
		t.Fatalf("opened session = %+v, want route from %+v", opened, h.session)
	}
	if opened.EmitterPath != opened.LauncherPath {
		t.Fatalf("opened current launcher route = %+v", opened)
	}
	panes, err := opened.LivePanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) == 0 {
		t.Fatal("opened session returned no live panes")
	}
}

func TestEnsureOwnedCreatesAndIdempotentlyReadoptsSession(t *testing.T) {
	h := newOwnedHarness(t)
	first := h.session
	second := h.ensure()
	third, err := openOwned(context.Background(), OwnedOptions{
		GitCommonDir: h.commonDir,
		RuntimeBase:  h.runtimeBase,
	}, h.backend())
	if err != nil {
		t.Fatal(err)
	}
	if h.supervisor.starts != 1 {
		t.Fatalf("supervisor starts = %d, want 1", h.supervisor.starts)
	}
	if first.Session != second.Session || first.SocketPath != second.SocketPath || first.ClientSocketPath != second.ClientSocketPath {
		t.Fatalf("re-adopted session differs: first=%+v second=%+v", first, second)
	}
	if third.Session != first.Session || third.SocketPath != first.SocketPath {
		t.Fatalf("lifecycle re-adopted session differs: first=%+v third=%+v", first, third)
	}
	command, _, err := second.AttachForms(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, "session attach") || !strings.Contains(command, socketEnv+"='") || !strings.Contains(command, h.layout.binaryDir) {
		t.Fatalf("AttachForms() command = %q", command)
	}
}

func TestOpenOwnedMissingSessionIsReadOnly(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, "repo.git")
	if err := os.Mkdir(commonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	nonce, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	runtimeBase := filepath.Join("/tmp", "fanout-open-owned-"+nonce[:12])
	t.Cleanup(func() {
		_ = os.RemoveAll(runtimeBase) // Clean up only if a failed read-only admission created it.
	})
	_, err = OpenOwned(context.Background(), OwnedOptions{
		GitCommonDir: commonDir,
		RuntimeBase:  runtimeBase,
	})
	if !errors.Is(err, corebackend.ErrOwnedSessionNotFound) {
		t.Fatalf("OpenOwned() error = %v, want corebackend.ErrOwnedSessionNotFound", err)
	}
	if _, statErr := os.Lstat(runtimeBase); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("OpenOwned() created runtime path: %v", statErr)
	}
}

func TestReopenedOwnedBackendAdmitsPinnedBinary(t *testing.T) {
	h := newOwnedHarness(t)
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %t, %v", marker, found, err)
	}
	admitted := binaryAdmission{
		path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
	}
	backend := newReopenedOwnedBackend(h.layout, marker, admitted, nil)
	backend.output = h.fake.output
	probed, err := backend.probeOwned(context.Background(), *backend.owner)
	if err != nil {
		t.Fatal(err)
	}
	if probed.binary != marker.BinaryPath || probed.sha256 != marker.BinarySHA256 {
		t.Fatalf("reopened admission = %+v, want marker binary identity", probed)
	}
}

func TestEnsureOwnedReadoptsPinnedLauncherAfterFanoutUpdate(t *testing.T) {
	h := newOwnedHarness(t)
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", marker, found, err)
	}
	legacySource := filepath.Join(h.root, "legacy-fanout")
	err = os.WriteFile(legacySource, []byte("legacy fanout launcher\n"), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath, legacyHash, err := stageExecutable(legacySource, h.layout.launcherDir)
	if err != nil {
		t.Fatal(err)
	}
	marker.LauncherPath, marker.LauncherSHA256 = legacyPath, legacyHash
	if removeErr := os.Remove(h.layout.markerPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	if markerErr := writeOwnerMarkerExclusive(h.layout.markerPath, marker); markerErr != nil {
		t.Fatal(markerErr)
	}
	if removeErr := os.Remove(h.layout.configPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	if configErr := ensureOwnedConfig(h.layout, legacyPath); configErr != nil {
		t.Fatal(configErr)
	}

	reused := h.ensure()
	if reused.LauncherPath != legacyPath || h.supervisor.starts != 1 {
		t.Fatalf("re-adopted launcher = %q, starts=%d, want %q and one start", reused.LauncherPath, h.supervisor.starts, legacyPath)
	}
	if reused.EmitterPath == legacyPath || reused.EmitterPath == "" {
		t.Fatalf("re-adopted emitter = %q, want current content-addressed fanout", reused.EmitterPath)
	}
	observed := New(reused.Session, reused.SocketPath)
	observed.output = h.fake.output
	opened, err := openOwned(context.Background(), OwnedOptions{
		GitCommonDir: h.commonDir, RuntimeBase: h.runtimeBase,
	}, observed)
	if err != nil {
		t.Fatal(err)
	}
	if opened.EmitterPath == "" || opened.EmitterPath == opened.LauncherPath {
		t.Fatalf("opened route = %+v, want read-only current-launcher mismatch", opened)
	}
}

func TestOwnedReadinessRequiresPrivateServerAndClientSockets(t *testing.T) {
	tests := []struct {
		name string
		path func(ownedLayout) string
	}{
		{name: "server", path: func(layout ownedLayout) string { return layout.socketPath }},
		{name: "client", path: func(layout ownedLayout) string { return layout.clientSocketPath }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newOwnedHarness(t)
			path := test.path(h.layout)
			if err := os.Chmod(path, 0o660); err != nil {
				t.Fatal(err)
			}
			err := validateOwnedReady(context.Background(), h.session.Backend())
			if err == nil || !strings.Contains(err.Error(), "not an owner-only Unix socket") {
				t.Fatalf("validateOwnedReady() error = %v", err)
			}
		})
	}
}

func TestEnsureOwnedFailsClosedAfterDeadSupervisor(t *testing.T) {
	h := newOwnedHarness(t)
	previous, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", previous, found, err)
	}
	h.supervisor.close()
	_, ensureErr := h.tryEnsure()
	if !errors.Is(ensureErr, errOwnedSupervisorNotRunning) {
		t.Fatalf("ensure after dead supervisor error = %v, want fail-closed terminal state", ensureErr)
	}
	current, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found || current != previous || h.supervisor.starts != 1 {
		t.Fatalf("owner marker after refused restart = %+v, %v, %v; starts=%d", current, found, err, h.supervisor.starts)
	}
}

func TestEnsureOwnedRejectsRecreatedGitCommonDirectory(t *testing.T) {
	h := newOwnedHarness(t)
	previous, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", previous, found, err)
	}
	displaced := h.commonDir + "-displaced"
	renameErr := os.Rename(h.commonDir, displaced)
	if renameErr != nil {
		t.Fatal(renameErr)
	}
	mkdirErr := os.Mkdir(h.commonDir, 0o700)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	_, replacementIdentity, err := openCanonicalGitCommonDir(h.commonDir)
	if err != nil {
		t.Fatal(err)
	}
	replacementSession := naming.ManagedSessionName(replacementIdentity.device, replacementIdentity.inode)
	if replacementSession == previous.Session {
		t.Fatalf("recreated repository reused owned session %q", replacementSession)
	}
	current, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found || current != previous {
		t.Fatalf("old repository marker changed = %+v, %v, %v", current, found, err)
	}
}
