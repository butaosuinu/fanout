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
		"FANOUT_HERDR_PANE_LAUNCHER=1", "HERDR_CONFIG_PATH=/private/config",
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
	descriptor := dashboardDescriptor{
		SchemaID:   dashboardDescriptorSchemaID,
		HelperPath: pinned.path, HelperSHA256: pinned.sha256,
		DashboardPath: pinned.path, DashboardSHA256: pinned.sha256,
		Environment: []string{"HOME=/home/operator\x00bad"},
	}
	if err := validateDashboardDescriptor(layout, descriptor); err == nil {
		t.Fatal("descriptor with NUL environment was accepted")
	}
}

func TestRunDashboardOpenUsesValidatedPinnedBinaryAndCleanEnvironment(t *testing.T) {
	layout, pinned := newDashboardShortcutLayout(t)
	descriptor := dashboardDescriptor{
		SchemaID:   dashboardDescriptorSchemaID,
		HelperPath: pinned.path, HelperSHA256: pinned.sha256,
		DashboardPath: pinned.path, DashboardSHA256: pinned.sha256,
		Environment: []string{"HOME=/home/operator", "PATH=/usr/bin:/bin", "DISPLAY=:1"},
	}
	if err := writeDashboardDescriptor(layout, descriptor); err != nil {
		t.Fatal(err)
	}
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
	for _, want := range []string{"HOME=/home/operator", "PATH=/usr/bin:/bin", "DISPLAY=:1", "FANOUT_BACKEND=herdr", "FANOUT_BIN=" + pinned.path} {
		if !slices.Contains(gotEnv, want) {
			t.Fatalf("dashboard environment %q does not contain %q", gotEnv, want)
		}
	}
	for _, blocked := range []string{"FANOUT_HERDR_PANE_LAUNCHER=", "HERDR_CONFIG_PATH=", "GH_TOKEN="} {
		for _, entry := range gotEnv {
			if strings.HasPrefix(entry, blocked) {
				t.Fatalf("dashboard environment retained blocked value %q", entry)
			}
		}
	}
}

func TestSyncDashboardShortcutMigratesLiveConfigAndHonorsDisable(t *testing.T) {
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
		Environment: []string{"HOME=/home/operator", "PATH=/usr/bin", "GH_TOKEN=secret", "DISPLAY=:4"},
	}
	if syncErr := h.session.Backend().SyncDashboardShortcut(options); syncErr != nil {
		t.Fatal(syncErr)
	}
	descriptor, found, err := readDashboardDescriptor(h.layout)
	if err != nil || !found {
		t.Fatalf("dashboard descriptor = (%+v, %t, %v)", descriptor, found, err)
	}
	if validateErr := validatePrivateContents(h.layout.configPath, ownedDashboardConfigContents(
		h.session.LauncherPath, descriptor.HelperPath, h.layout.dashboardDescriptorPath,
	)); validateErr != nil {
		t.Fatalf("enabled config: %v", validateErr)
	}
	if slices.Contains(descriptor.Environment, "GH_TOKEN=secret") {
		t.Fatalf("descriptor retained GH_TOKEN: %+v", descriptor)
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

func TestSyncDashboardShortcutLeavesDesiredConfigAfterAmbiguousReload(t *testing.T) {
	h := newOwnedHarness(t)
	h.fake.respond = func([]string) ([]byte, error) { return nil, context.DeadlineExceeded }
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	err = h.session.Backend().SyncDashboardShortcut(corebackend.DashboardShortcutOptions{
		Enabled: true, FanoutBin: executable, Environment: os.Environ(),
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
		Enabled: true, FanoutBin: executable, Environment: []string{"PATH=/usr/bin"},
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

func countRecordedCommand(commands []recordedCommand, want []string) int {
	count := 0
	for _, command := range commands {
		if slices.Equal(command.args, want) {
			count++
		}
	}
	return count
}
