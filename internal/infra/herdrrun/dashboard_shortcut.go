package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
)

const (
	// dashboardDescriptorSchemaID is read by the stable helper path embedded in
	// config.toml. Keep v1 readable across upgrades even if later fields appear.
	dashboardDescriptorSchemaID = "fanout.herdr-dashboard-launcher.v1"
	dashboardDescriptorName     = "dashboard-launcher.json"
	dashboardDefaultPath        = "/usr/local/bin:/usr/bin:/bin"
	// DashboardOpenCommand is frozen because an older pinned launcher may keep
	// serving an owned session after the installed fanout binary is upgraded.
	DashboardOpenCommand = "__herdr-dashboard-open"
)

var (
	dashboardExec       = syscall.Exec
	dashboardExecutable = os.Executable
)

var dashboardHostEnvironmentNames = []string{
	"HOME", "PATH", "TMPDIR",
	"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
	"GH_CONFIG_DIR", "LANG", "LC_ALL", "LC_CTYPE",
	"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XAUTHORITY",
	"XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE", "DESKTOP_SESSION",
	"KDE_FULL_SESSION", "GNOME_DESKTOP_SESSION_ID", "WSL_INTEROP", "WSL_DISTRO_NAME",
}

type dashboardDescriptor struct {
	SchemaID        string   `json:"schema_id"`
	HelperPath      string   `json:"helper_path"`
	HelperSHA256    string   `json:"helper_sha256"`
	DashboardPath   string   `json:"dashboard_path"`
	DashboardSHA256 string   `json:"dashboard_sha256"`
	Environment     []string `json:"environment"`
}

type dashboardReloadResponse struct {
	Status string `json:"status"`
}

// SyncDashboardShortcut converges the owned config and asks the live server to
// reload it once. A timeout leaves the desired bytes in place for the next
// caller; repeating a config reload is safe, but guessing that it failed is not.
func (b *Backend) SyncDashboardShortcut(options corebackend.DashboardShortcutOptions) (err error) {
	defer errs.Wrap(&err, "sync Herdr dashboard shortcut")

	ctx, cancel := context.WithTimeout(context.Background(), 3*commandTimeout)
	defer cancel()
	admission, lock, err := b.acquireOwnedMutation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)
	layout, err := prepareOwnedLayout(filepath.Dir(admission.marker.RuntimeDir), admission.marker.Session)
	if err != nil {
		return err
	}
	if err := applyDashboardShortcutConfig(layout, admission.marker, options); err != nil {
		return err
	}
	if err := b.reloadDashboardShortcutConfig(ctx, admission.marker); err != nil {
		return err
	}
	if options.Enabled {
		return nil
	}
	return removeDashboardDescriptor(layout)
}

func applyDashboardShortcutConfig(
	layout ownedLayout,
	marker ownerMarker,
	options corebackend.DashboardShortcutOptions,
) error {
	desired, err := desiredDashboardConfig(layout, marker, options)
	if err != nil {
		return err
	}
	if err := replaceOwnedConfig(layout.configPath, desired); err != nil {
		return err
	}
	return nil
}

func (b *Backend) reloadDashboardShortcutConfig(ctx context.Context, marker ownerMarker) error {
	out, err := b.runContext(ctx, commandTimeout, marker.BinaryPath,
		route{session: marker.Session, socketPath: marker.SocketPath},
		"server", "reload-config")
	if err != nil {
		return fmt.Errorf("reload Herdr dashboard shortcut config: %w", err)
	}
	return validateDashboardReloadResponse(out)
}

func desiredDashboardConfig(
	layout ownedLayout,
	marker ownerMarker,
	options corebackend.DashboardShortcutOptions,
) ([]byte, error) {
	if !options.Enabled {
		return ownedConfigContents(marker.LauncherPath), nil
	}
	current, err := stageDashboardExecutable(options.FanoutBin, layout)
	if err != nil {
		return nil, err
	}
	descriptor, err := nextDashboardDescriptor(layout, current, options.Environment)
	if err != nil {
		return nil, err
	}
	if err := writeDashboardDescriptor(layout, descriptor); err != nil {
		return nil, err
	}
	return ownedDashboardConfigContents(marker.LauncherPath, descriptor.HelperPath, layout.dashboardDescriptorPath), nil
}

func nextDashboardDescriptor(
	layout ownedLayout,
	current binaryAdmission,
	environment []string,
) (dashboardDescriptor, error) {
	descriptor, found, err := readDashboardDescriptor(layout)
	if err != nil {
		return dashboardDescriptor{}, err
	}
	if !found {
		descriptor.HelperPath, descriptor.HelperSHA256 = current.path, current.sha256
	}
	descriptor.SchemaID = dashboardDescriptorSchemaID
	descriptor.DashboardPath, descriptor.DashboardSHA256 = current.path, current.sha256
	descriptor.Environment = filterDashboardEnvironment(environment)
	return descriptor, nil
}

func stageDashboardExecutable(path string, layout ownedLayout) (binaryAdmission, error) {
	if strings.TrimSpace(path) == "" {
		return binaryAdmission{}, fmt.Errorf("dashboard shortcut requires the fanout executable path")
	}
	pinned, digest, err := stageExecutable(path, layout.launcherDir)
	if err != nil {
		return binaryAdmission{}, fmt.Errorf("pin fanout dashboard launcher: %w", err)
	}
	return binaryAdmission{path: pinned, sha256: digest}, nil
}

func replaceOwnedConfig(path string, desired []byte) error {
	if err := validatePrivateContents(path, desired); err == nil {
		return nil
	}
	if err := atomicfs.WriteFile(path, desired, 0o600); err != nil {
		return fmt.Errorf("replace Herdr owned config: %w", err)
	}
	return validatePrivateContents(path, desired)
}

func validateDashboardReloadResponse(out []byte) error {
	var response dashboardReloadResponse
	if err := json.Unmarshal(out, &response); err != nil {
		return fmt.Errorf("parse Herdr config reload response: %w", err)
	}
	if response.Status != "applied" {
		return fmt.Errorf("herdr config reload status is %q, want applied", response.Status)
	}
	return nil
}

func filterDashboardEnvironment(environment []string) []string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	if values["PATH"] == "" {
		values["PATH"] = dashboardDefaultPath
	}
	filtered := make([]string, 0, len(dashboardHostEnvironmentNames))
	for _, name := range dashboardHostEnvironmentNames {
		if value, ok := values[name]; ok {
			filtered = append(filtered, name+"="+value)
		}
	}
	return filtered
}

func writeDashboardDescriptor(layout ownedLayout, descriptor dashboardDescriptor) error {
	if err := validateDashboardDescriptor(layout, descriptor); err != nil {
		return err
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	if len(data) > maxOwnerMarkerBytes {
		return fmt.Errorf("herdr dashboard launcher descriptor exceeds %d bytes", maxOwnerMarkerBytes)
	}
	if writeErr := atomicfs.WriteFile(layout.dashboardDescriptorPath, data, 0o600); writeErr != nil {
		return fmt.Errorf("write Herdr dashboard launcher descriptor: %w", writeErr)
	}
	_, _, err = readDashboardDescriptor(layout)
	return err
}

func removeDashboardDescriptor(layout ownedLayout) error {
	_, found, err := readDashboardDescriptor(layout)
	if err != nil || !found {
		return err
	}
	if err := os.Remove(layout.dashboardDescriptorPath); err != nil {
		return fmt.Errorf("remove Herdr dashboard launcher descriptor: %w", err)
	}
	if _, statErr := os.Lstat(layout.dashboardDescriptorPath); !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("herdr dashboard launcher descriptor remains after removal")
	}
	return nil
}

func readDashboardDescriptor(layout ownedLayout) (dashboardDescriptor, bool, error) {
	f, err := os.OpenFile(layout.dashboardDescriptorPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return dashboardDescriptor{}, false, nil
	}
	if err != nil {
		return dashboardDescriptor{}, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err == nil {
		err = validatePrivateRegular(layout.dashboardDescriptorPath, info)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxOwnerMarkerBytes+1))
	if err = errors.Join(err, readErr); err != nil {
		return dashboardDescriptor{}, true, err
	}
	if len(data) > maxOwnerMarkerBytes {
		return dashboardDescriptor{}, true, fmt.Errorf("herdr dashboard launcher descriptor exceeds %d bytes", maxOwnerMarkerBytes)
	}
	var descriptor dashboardDescriptor
	if err := decodeStrictCanonical(data, &descriptor); err != nil {
		return dashboardDescriptor{}, true, fmt.Errorf("parse Herdr dashboard launcher descriptor: %w", err)
	}
	if err := validateDashboardDescriptor(layout, descriptor); err != nil {
		return dashboardDescriptor{}, true, err
	}
	return descriptor, true, nil
}

func validateDashboardDescriptor(layout ownedLayout, descriptor dashboardDescriptor) error {
	if descriptor.SchemaID != dashboardDescriptorSchemaID {
		return fmt.Errorf("herdr dashboard launcher descriptor schema is invalid")
	}
	if err := validatePinnedBinaryInDir(descriptor.HelperPath, descriptor.HelperSHA256, layout.launcherDir); err != nil {
		return fmt.Errorf("herdr dashboard helper identity changed: %w", err)
	}
	if err := validatePinnedBinaryInDir(descriptor.DashboardPath, descriptor.DashboardSHA256, layout.launcherDir); err != nil {
		return fmt.Errorf("herdr dashboard binary identity changed: %w", err)
	}
	for _, entry := range descriptor.Environment {
		if strings.ContainsRune(entry, '\x00') {
			return fmt.Errorf("herdr dashboard launcher environment contains NUL")
		}
	}
	if !slices.Equal(descriptor.Environment, filterDashboardEnvironment(descriptor.Environment)) {
		return fmt.Errorf("herdr dashboard launcher environment is not canonical")
	}
	return nil
}

func ownedDashboardConfigContents(defaultShell, helperPath, descriptorPath string) []byte {
	command := "FANOUT_HERDR_PANE_LAUNCHER=0 " + shellQuote(helperPath) + " " +
		DashboardOpenCommand + " " + shellQuote(descriptorPath)
	return append(ownedConfigContents(defaultShell), []byte("\n[[keys.command]]\n"+
		"key = [\"f12\", \"prefix+f12\"]\n"+
		"command = "+strconv.Quote(command)+"\n"+
		"type = \"shell\"\n"+
		"description = \"Open fanout dashboard\"\n")...)
}

// IsDashboardOpenRequest recognizes the private command stored in an owned
// Herdr config. Keep the spelling backward-compatible with pinned helpers.
func IsDashboardOpenRequest(args []string) bool {
	return len(args) > 0 && args[0] == DashboardOpenCommand
}

// RunDashboardOpen validates the private descriptor and replaces the helper
// with the current pinned fanout dashboard process under a clean environment.
func RunDashboardOpen(args []string, errw io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(errw, "Herdr dashboard launcher requires one descriptor path")
		return 2
	}
	descriptor, err := dashboardDescriptorForOpen(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(errw, "Herdr dashboard launcher: %v\n", err)
		return 1
	}
	err = execDashboardDescriptor(descriptor)
	_, _ = fmt.Fprintf(errw, "Herdr dashboard launcher: %v\n", err)
	return 1
}

func dashboardDescriptorForOpen(path string) (dashboardDescriptor, error) {
	layout, err := dashboardLayoutForDescriptor(path)
	if err != nil {
		return dashboardDescriptor{}, err
	}
	descriptor, found, err := readDashboardDescriptor(layout)
	if err != nil {
		return dashboardDescriptor{}, err
	}
	if !found {
		return dashboardDescriptor{}, fmt.Errorf("descriptor is missing")
	}
	return descriptor, validateRunningDashboardHelper(descriptor)
}

func execDashboardDescriptor(descriptor dashboardDescriptor) error {
	environment := append([]string{}, descriptor.Environment...)
	environment = append(environment,
		"FANOUT_BACKEND=herdr", "FANOUT_BIN="+descriptor.DashboardPath,
	)
	return dashboardExec(descriptor.DashboardPath, []string{
		descriptor.DashboardPath, "dashboard", "--web", "--open", "--no-keybind",
	}, environment)
}

func dashboardLayoutForDescriptor(path string) (ownedLayout, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != dashboardDescriptorName {
		return ownedLayout{}, fmt.Errorf("descriptor path is invalid")
	}
	runtimeDir := filepath.Dir(path)
	layout, err := prepareOwnedLayout(filepath.Dir(runtimeDir), filepath.Base(runtimeDir))
	if err != nil {
		return ownedLayout{}, err
	}
	if layout.dashboardDescriptorPath != path {
		return ownedLayout{}, fmt.Errorf("descriptor path is outside the owned runtime")
	}
	if err := validateDashboardLayoutDirs(layout); err != nil {
		return ownedLayout{}, err
	}
	return layout, nil
}

func validateDashboardLayoutDirs(layout ownedLayout) error {
	for _, dir := range []string{layout.runtimeBase, layout.runtimeDir, layout.launcherDir} {
		if err := validatePrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func validateRunningDashboardHelper(descriptor dashboardDescriptor) error {
	executable, err := dashboardExecutable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if executable != descriptor.HelperPath {
		return fmt.Errorf("running helper does not match the descriptor")
	}
	return nil
}
