package herdrrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	frozenDashboardDescriptorSchemaID = "fanout.herdr-dashboard-launcher.v1"
	appliedDashboardReloadEnvelope    = `{"id":"cli:server:reload-config","result":{"type":"config_reload","status":"applied","diagnostics":[]}}`
)

func TestDashboardDescriptorSchemaStaysCompatibleWithPinnedHelper(t *testing.T) {
	if dashboardDescriptorSchemaID != frozenDashboardDescriptorSchemaID {
		t.Fatalf("dashboard descriptor schema = %q, want %q", dashboardDescriptorSchemaID, frozenDashboardDescriptorSchemaID)
	}
}

func TestOwnedDashboardConfigBindsF12InTerminalAndNavigateModes(t *testing.T) {
	layout, pinned := newDashboardShortcutLayout(t)
	config := string(ownedDashboardConfigContents("/owned/default", pinned.path, layout.dashboardDescriptorPath))
	for _, want := range []string{
		`key = ["f12", "prefix+f12"]`,
		`FANOUT_HERDR_PANE_LAUNCHER=0`,
		DashboardOpenCommand,
		`type = "shell"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("dashboard config does not contain %q:\n%s", want, config)
		}
	}
}

func TestDashboardDescriptorKeepsOnlyBrowserContext(t *testing.T) {
	got := filterDashboardEnvironment([]string{
		"PATH=/custom/bin", "DISPLAY=:7", "GH_TOKEN=secret", "GITHUB_TOKEN=secret-2",
		"FANOUT_HERDR_PANE_LAUNCHER=1", "FANOUT_STATE_PATH=/foreign/state",
		"HERDR_CONFIG_PATH=/private/config",
	})
	want := []string{"PATH=/custom/bin", "DISPLAY=:7"}
	if !slices.Equal(got, want) {
		t.Fatalf("filtered environment = %q, want %q", got, want)
	}
	withoutPath := filterDashboardEnvironment([]string{"DISPLAY=:8"})
	if !slices.Contains(withoutPath, "PATH="+dashboardDefaultPath) {
		t.Fatalf("filtered environment has no default PATH: %q", withoutPath)
	}
}

func TestDashboardDescriptorRejectsNULInEnvironment(t *testing.T) {
	layout, pinned := newDashboardShortcutLayout(t)
	descriptor := testDashboardDescriptor(layout, pinned, state.Path("/owner"))
	descriptor.Environment = []string{"HOME=/home/operator\x00bad"}
	if err := validateDashboardDescriptor(layout, descriptor); err == nil {
		t.Fatal("descriptor with NUL environment was accepted")
	}
}

func TestRunDashboardOpenUsesValidatedPinnedBinaryAndCleanEnvironment(t *testing.T) {
	layout, pinned := newDashboardShortcutLayout(t)
	descriptor := testDashboardDescriptor(layout, pinned, state.Path("/owner"))
	descriptor.Environment = []string{"HOME=/home/operator", "PATH=/usr/bin:/bin", "DISPLAY=:1"}
	if err := writeDashboardDescriptor(layout, descriptor); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SOCKET_PATH", layout.socketPath)
	t.Setenv("HERDR_ACTIVE_PANE_ID", "pane-1")
	t.Setenv("HERDR_ACTIVE_WORKSPACE_ID", "workspace-1")
	t.Setenv(dashboardRelayGHTokenEnv, "gh-secret")
	t.Setenv(dashboardRelayGitHubTokenEnv, "github-secret")
	originalExec, originalExecutable := dashboardExec, dashboardExecutable
	t.Cleanup(func() { dashboardExec, dashboardExecutable = originalExec, originalExecutable })
	dashboardExecutable = func() (string, error) { return pinned.path, nil }
	var gotPath string
	var gotArgs, gotEnv []string
	dashboardExec = func(path string, args, env []string) error {
		gotPath, gotArgs, gotEnv = path, slices.Clone(args), slices.Clone(env)
		return errors.New("exec test seam")
	}
	var stderr bytes.Buffer
	code := RunDashboardOpen([]string{layout.dashboardDescriptorPath}, &stderr)
	if code != 1 || gotPath != pinned.path {
		t.Fatalf("RunDashboardOpen() = %d path=%q, want 1/%q", code, gotPath, pinned.path)
	}
	wantArgs := []string{pinned.path, "dashboard", "--web", "--open", "--no-keybind"}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("dashboard argv = %q, want %q", gotArgs, wantArgs)
	}
	for _, want := range []string{
		"HOME=/home/operator", "PATH=/usr/bin:/bin", "DISPLAY=:1",
		"GH_TOKEN=gh-secret", "GITHUB_TOKEN=github-secret",
		"FANOUT_BACKEND=herdr", "FANOUT_BIN=" + pinned.path,
		"FANOUT_STATE_PATH=" + state.Path("/owner"),
	} {
		if !slices.Contains(gotEnv, want) {
			t.Fatalf("dashboard environment %q does not contain %q", gotEnv, want)
		}
	}
	for _, blocked := range []string{
		"FANOUT_HERDR_PANE_LAUNCHER=", "HERDR_CONFIG_PATH=",
		dashboardRelayGHTokenEnv + "=", dashboardRelayGitHubTokenEnv + "=",
	} {
		for _, entry := range gotEnv {
			if strings.HasPrefix(entry, blocked) {
				t.Fatalf("dashboard environment retained blocked value %q", entry)
			}
		}
	}
}

func TestRunDashboardOpenRejectsActiveRouteMismatch(t *testing.T) {
	layout, pinned := newDashboardShortcutLayout(t)
	if err := writeDashboardDescriptor(layout, testDashboardDescriptor(layout, pinned, state.Path("/owner"))); err != nil {
		t.Fatal(err)
	}
	originalExecutable := dashboardExecutable
	t.Cleanup(func() { dashboardExecutable = originalExecutable })
	dashboardExecutable = func() (string, error) { return pinned.path, nil }
	t.Setenv("HERDR_SOCKET_PATH", "/another/server.sock")
	var stderr bytes.Buffer
	if code := RunDashboardOpen([]string{layout.dashboardDescriptorPath}, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "active Herdr route") {
		t.Fatalf("RunDashboardOpen() = %d stderr=%q", code, stderr.String())
	}
}

func TestSyncDashboardShortcutMigratesLiveConfigAndHonorsDisable(t *testing.T) {
	environment := []string{
		"HOME=/home/operator", "PATH=/usr/bin", "GH_TOKEN=secret", "DISPLAY=:4",
	}
	h := newOwnedHarnessWithDashboardEnvironment(t, environment)
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
	options := corebackend.DashboardShortcutOptions{
		Enabled: true, FanoutBin: executable,
		Owners: []corebackend.DashboardShortcutOwner{
			testDashboardShortcutOwner(h, "pane-1", "workspace-1", state.Path(h.checkout)),
			testDashboardShortcutOwner(h, "pane-2", "workspace-2", state.Path(h.checkout+"-linked")),
		},
		Environment: environment,
	}
	if syncErr := h.session.Backend().SyncDashboardShortcut(options); syncErr != nil {
		t.Fatal(syncErr)
	}
	descriptor, found, err := readDashboardDescriptor(h.layout)
	if err != nil || !found {
		t.Fatalf("dashboard descriptor = (%+v, %t, %v)", descriptor, found, err)
	}
	if len(descriptor.Owners) != 2 || descriptor.Owners[0].StatePath != state.Path(h.checkout) ||
		descriptor.Owners[1].StatePath != state.Path(h.checkout+"-linked") {
		t.Fatalf("dashboard descriptor owners = %+v", descriptor.Owners)
	}
	if validateErr := validatePrivateContents(h.layout.configPath, ownedDashboardConfigContents(
		h.session.LauncherPath, descriptor.HelperPath, h.layout.dashboardDescriptorPath,
	)); validateErr != nil {
		t.Fatalf("enabled config: %v", validateErr)
	}
	if slices.Contains(descriptor.Environment, "GH_TOKEN=secret") {
		t.Fatalf("descriptor retained GH_TOKEN: %+v", descriptor)
	}
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found || marker.DashboardGHTokenSHA256 != dashboardAuthenticationSHA256("secret") {
		t.Fatalf("dashboard authentication marker = (%+v, %t, %v)", marker, found, err)
	}
	for _, path := range []string{h.layout.markerPath, h.layout.configPath, h.layout.dashboardDescriptorPath} {
		contents, readErr := os.ReadFile(path)
		if readErr != nil || bytes.Contains(contents, []byte(`"GH_TOKEN":"secret"`)) ||
			bytes.Contains(contents, []byte("GH_TOKEN=secret")) {
			t.Fatalf("dashboard authentication persisted in %s: err=%v", path, readErr)
		}
	}
	if syncErr := h.session.Backend().SyncDashboardShortcut(options); syncErr != nil {
		t.Fatal(syncErr)
	}
	reloadedDescriptor, _, err := readDashboardDescriptor(h.layout)
	if err != nil || reloadedDescriptor.HelperPath != descriptor.HelperPath {
		t.Fatalf("repeat sync changed stable helper: before=%+v after=%+v err=%v", descriptor, reloadedDescriptor, err)
	}
	options.Enabled = false
	if err := h.session.Backend().SyncDashboardShortcut(options); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateContents(h.layout.configPath, ownedConfigContents(h.session.LauncherPath)); err != nil {
		t.Fatalf("disabled config: %v", err)
	}
	if _, err := os.Lstat(h.layout.dashboardDescriptorPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled descriptor still exists: %v", err)
	}
	if got := countRecordedCommand(h.fake.commands, []string{"server", "reload-config"}); got != 3 {
		t.Fatalf("reload-config calls = %d, want 3", got)
	}
}

func TestSyncDashboardShortcutDisablesBindingWhenAuthenticationNeedsRecreate(t *testing.T) {
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
	options := corebackend.DashboardShortcutOptions{
		Enabled: true, FanoutBin: executable,
		Owners:      []corebackend.DashboardShortcutOwner{testDashboardShortcutOwner(h, "pane-1", "workspace-1", state.Path(h.checkout))},
		Environment: []string{"PATH=/usr/bin"},
	}
	if err := h.session.Backend().SyncDashboardShortcut(options); err != nil {
		t.Fatal(err)
	}
	options.Environment = append(options.Environment, "GH_TOKEN=rotated")
	err = h.session.Backend().SyncDashboardShortcut(options)
	if err == nil || !strings.Contains(err.Error(), "fanout herdr shutdown") {
		t.Fatalf("authentication mismatch error = %v", err)
	}
	if err := validatePrivateContents(h.layout.configPath, ownedConfigContents(h.session.LauncherPath)); err != nil {
		t.Fatalf("disabled config: %v", err)
	}
	if _, err := os.Lstat(h.layout.dashboardDescriptorPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authentication mismatch retained descriptor: %v", err)
	}
	if got := countRecordedCommand(h.fake.commands, []string{"server", "reload-config"}); got != 2 {
		t.Fatalf("reload-config calls = %d, want 2", got)
	}
}

func TestSyncDashboardShortcutLeavesDesiredConfigAfterAmbiguousReload(t *testing.T) {
	h := newOwnedHarness(t)
	h.fake.respond = func([]string) ([]byte, error) { return nil, context.DeadlineExceeded }
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	err = h.session.Backend().SyncDashboardShortcut(corebackend.DashboardShortcutOptions{
		Enabled: true, FanoutBin: executable,
		Owners:      []corebackend.DashboardShortcutOwner{testDashboardShortcutOwner(h, "pane-1", "workspace-1", state.Path(h.checkout))},
		Environment: filterDashboardEnvironment(os.Environ()),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SyncDashboardShortcut() error = %v, want deadline", err)
	}
	if err := validateCompatibleOwnedConfig(h.layout, h.session.LauncherPath); err != nil {
		t.Fatalf("desired config was not retained: %v", err)
	}
}

func TestSyncDashboardShortcutRejectsOversizedDescriptorWithoutMutation(t *testing.T) {
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
	options := corebackend.DashboardShortcutOptions{
		Enabled: true, FanoutBin: executable,
		Owners:      []corebackend.DashboardShortcutOwner{testDashboardShortcutOwner(h, "pane-1", "workspace-1", state.Path(h.checkout))},
		Environment: []string{"PATH=/usr/bin"},
	}
	if err := h.session.Backend().SyncDashboardShortcut(options); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(h.layout.configPath)
	if err != nil {
		t.Fatal(err)
	}
	descriptorBefore, err := os.ReadFile(h.layout.dashboardDescriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	options.Environment = []string{"PATH=" + strings.Repeat("x", maxOwnerMarkerBytes)}

	err = h.session.Backend().SyncDashboardShortcut(options)
	if err == nil || !strings.Contains(err.Error(), "descriptor exceeds") {
		t.Fatalf("oversized dashboard descriptor error = %v", err)
	}
	configAfter, configErr := os.ReadFile(h.layout.configPath)
	descriptorAfter, descriptorErr := os.ReadFile(h.layout.dashboardDescriptorPath)
	if configErr != nil || descriptorErr != nil || !slices.Equal(configAfter, configBefore) ||
		!slices.Equal(descriptorAfter, descriptorBefore) {
		t.Fatalf("oversized sync mutated files: config err=%v descriptor err=%v", configErr, descriptorErr)
	}
	if got := countRecordedCommand(h.fake.commands, []string{"server", "reload-config"}); got != 1 {
		t.Fatalf("reload-config calls = %d, want 1", got)
	}
}

func TestSyncDashboardShortcutRejectsRouteDriftBeforeConfigMutation(t *testing.T) {
	h := newOwnedHarness(t)
	configBefore, err := os.ReadFile(h.layout.configPath)
	if err != nil {
		t.Fatal(err)
	}
	h.fake.status = validStatus(h.session.Session, h.layout.clientSocketPath)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	err = h.session.Backend().SyncDashboardShortcut(corebackend.DashboardShortcutOptions{
		Enabled: true, FanoutBin: executable,
		Owners:      []corebackend.DashboardShortcutOwner{testDashboardShortcutOwner(h, "pane-1", "workspace-1", state.Path(h.checkout))},
		Environment: []string{"PATH=/usr/bin"},
	})
	if err == nil || !strings.Contains(err.Error(), "status socket") {
		t.Fatalf("route drift error = %v", err)
	}
	configAfter, readErr := os.ReadFile(h.layout.configPath)
	if readErr != nil || !slices.Equal(configAfter, configBefore) {
		t.Fatalf("route drift mutated config: err=%v", readErr)
	}
	if _, err := os.Lstat(h.layout.dashboardDescriptorPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("route drift wrote dashboard descriptor: %v", err)
	}
	if got := countRecordedCommand(h.fake.commands, []string{"server", "reload-config"}); got != 0 {
		t.Fatalf("reload-config calls = %d, want 0", got)
	}
}

func TestExecDashboardDescriptorPinsStatePathAcrossWorkingDirectories(t *testing.T) {
	descriptor := dashboardDescriptor{
		DashboardPath: "/pinned/fanout",
		Owners: []dashboardOwner{
			{PaneID: "pane-a", WorkspaceID: "workspace-a", StatePath: state.Path("/owner-a")},
			{PaneID: "pane-b", WorkspaceID: "workspace-b", StatePath: state.Path("/owner-b")},
		},
		Environment: []string{"PATH=/usr/bin"},
	}
	originalExec := dashboardExec
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workingDirs := []struct {
		name, path, paneID, workspaceID, statePath string
	}{
		{
			name: "outside repository", path: t.TempDir(), paneID: "pane-a",
			workspaceID: "workspace-a", statePath: state.Path("/owner-a"),
		},
		{
			name: "another checkout", path: t.TempDir(), paneID: "pane-b",
			workspaceID: "workspace-b", statePath: state.Path("/owner-b"),
		},
	}
	t.Cleanup(func() {
		dashboardExec = originalExec
		if err := os.Chdir(originalCwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	var gotEnv []string
	dashboardExec = func(_ string, _ []string, env []string) error {
		gotEnv = slices.Clone(env)
		return errors.New("exec test seam")
	}
	for _, cwd := range workingDirs {
		if err := os.Chdir(cwd.path); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HERDR_ACTIVE_PANE_ID", cwd.paneID)
		t.Setenv("HERDR_ACTIVE_WORKSPACE_ID", cwd.workspaceID)
		if err := execDashboardDescriptor(descriptor); err == nil || err.Error() != "exec test seam" {
			t.Fatalf("exec dashboard from %s: %v", cwd.name, err)
		}
		if !slices.Contains(gotEnv, "FANOUT_STATE_PATH="+cwd.statePath) {
			t.Fatalf("dashboard environment from %s %q = %q", cwd.name, cwd.path, gotEnv)
		}
	}
}

func TestActiveDashboardStatePathRejectsUnmappedAndConflictingIdentity(t *testing.T) {
	descriptor := dashboardDescriptor{Owners: []dashboardOwner{
		{PaneID: "pane-a", WorkspaceID: "workspace-a", StatePath: state.Path("/owner-a")},
		{PaneID: "pane-b", WorkspaceID: "workspace-b", StatePath: state.Path("/owner-b")},
	}}
	tests := []struct {
		name, paneID, workspaceID string
	}{
		{name: "unmapped", paneID: "pane-missing", workspaceID: "workspace-missing"},
		{name: "conflicting", paneID: "pane-a", workspaceID: "workspace-b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERDR_ACTIVE_PANE_ID", tt.paneID)
			t.Setenv("HERDR_ACTIVE_WORKSPACE_ID", tt.workspaceID)
			if _, err := activeDashboardStatePath(descriptor); err == nil {
				t.Fatal("unbound active Herdr identity was accepted")
			}
		})
	}
}

func TestValidateDashboardReloadResponse(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr string
	}{
		{name: "applied", payload: appliedDashboardReloadEnvelope},
		{
			name: "partial",
			payload: `{"id":"cli:server:reload-config","result":` +
				`{"type":"config_reload","status":"partial","diagnostics":[]}}`,
			wantErr: `status is "partial"`,
		},
		{
			name: "failed",
			payload: `{"id":"cli:server:reload-config","result":` +
				`{"type":"config_reload","status":"failed","diagnostics":[]}}`,
			wantErr: `status is "failed"`,
		},
		{
			name:    "wrong envelope",
			payload: `{"id":"cli:server:reload-config","result":{"type":"other","status":"applied","diagnostics":[]}}`,
			wantErr: "unexpected Herdr config_reload envelope",
		},
		{
			name:    "trailing value",
			payload: appliedDashboardReloadEnvelope + ` {}`,
			wantErr: "unexpected trailing JSON value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDashboardReloadResponse([]byte(tt.payload))
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validateDashboardReloadResponse() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func newDashboardShortcutLayout(t *testing.T) (ownedLayout, binaryAdmission) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fds-") //nolint:usetesting // Unix socket paths are capped at 103 bytes.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if chmodErr := os.Chmod(root, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	runtimeBase := filepath.Join(root, "runtime")
	if ensureErr := ensurePrivateDir(runtimeBase); ensureErr != nil {
		t.Fatal(ensureErr)
	}
	layout, err := prepareOwnedLayout(runtimeBase, "fanout-repo-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{layout.runtimeDir, layout.launcherDir} {
		if ensureErr := ensurePrivateDir(dir); ensureErr != nil {
			t.Fatal(ensureErr)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, digest, err := stageExecutable(executable, layout.launcherDir)
	if err != nil {
		t.Fatal(err)
	}
	return layout, binaryAdmission{path: path, sha256: digest}
}

func testDashboardDescriptor(layout ownedLayout, pinned binaryAdmission, statePath string) dashboardDescriptor {
	return dashboardDescriptor{
		SchemaID:   dashboardDescriptorSchemaID,
		HelperPath: pinned.path, HelperSHA256: pinned.sha256,
		DashboardPath: pinned.path, DashboardSHA256: pinned.sha256,
		SessionID: filepath.Base(layout.runtimeDir), SocketPath: layout.socketPath,
		Owners:      []dashboardOwner{{PaneID: "pane-1", WorkspaceID: "workspace-1", StatePath: statePath}},
		Environment: []string{"PATH=" + dashboardDefaultPath},
	}
}

func testDashboardShortcutOwner(
	h *ownedHarness,
	paneID, workspaceID, statePath string,
) corebackend.DashboardShortcutOwner {
	return corebackend.DashboardShortcutOwner{
		PaneID: paneID, WorkspaceID: workspaceID,
		SessionID: h.session.Session, SocketPath: h.session.SocketPath, StatePath: statePath,
	}
}

func countRecordedCommand(commands []recordedCommand, want []string) int {
	count := 0
	for _, command := range commands {
		if slices.Equal(command.args, want) {
			count++
		}
	}
	return count
}
