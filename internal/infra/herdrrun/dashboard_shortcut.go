package herdrrun

import (
	"context"
	"crypto/sha256"
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
	dashboardGHTokenEnv         = "GH_TOKEN"
	dashboardGitHubTokenEnv     = "GITHUB_TOKEN"
	dashboardRelayTokenEnv      = "FANOUT_HERDR_DASHBOARD_TOKEN"
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
	SchemaID        string           `json:"schema_id"`
	HelperPath      string           `json:"helper_path"`
	HelperSHA256    string           `json:"helper_sha256"`
	DashboardPath   string           `json:"dashboard_path"`
	DashboardSHA256 string           `json:"dashboard_sha256"`
	SessionID       string           `json:"session_id"`
	SocketPath      string           `json:"socket_path"`
	Owners          []dashboardOwner `json:"owners"`
	Environment     []string         `json:"environment"`
}

type dashboardOwner struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	StatePath   string `json:"state_path"`
}

type dashboardAuthentication struct {
	tokenSHA256 string
}

type dashboardReloadEnvelope struct {
	ID     string                 `json:"id"`
	Result *dashboardReloadResult `json:"result"`
	Error  *worktreeMutationError `json:"error"`
}

type dashboardReloadResult struct {
	Type        string             `json:"type"`
	Status      string             `json:"status"`
	Diagnostics *[]json.RawMessage `json:"diagnostics"`
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
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return err
	}
	authErr := validateDashboardAuthentication(admission.marker, options)
	if authErr != nil {
		options.Enabled = false
	}
	options, err = resolveDashboardShortcutOwners(options)
	if err != nil {
		return err
	}
	layout, err := prepareOwnedLayout(filepath.Dir(admission.marker.RuntimeDir), admission.marker.Session)
	if err != nil {
		return err
	}
	if err := stageDashboardShortcutConfig(layout, admission.marker, options); err != nil {
		return errors.Join(authErr, err)
	}
	if err := b.reloadDashboardShortcutConfig(ctx, probed); err != nil {
		return errors.Join(authErr, err)
	}
	return authErr
}

func resolveDashboardShortcutOwners(
	options corebackend.DashboardShortcutOptions,
) (corebackend.DashboardShortcutOptions, error) {
	if !options.Enabled || options.ResolveOwners == nil {
		return options, nil
	}
	owners, err := options.ResolveOwners()
	if err != nil {
		return options, fmt.Errorf("resolve Herdr dashboard shortcut owners: %w", err)
	}
	options.Owners = owners
	return options, nil
}

func stageDashboardShortcutConfig(
	layout ownedLayout,
	marker ownerMarker,
	options corebackend.DashboardShortcutOptions,
) error {
	if err := applyDashboardShortcutConfig(layout, marker, options); err != nil {
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

func (b *Backend) reloadDashboardShortcutConfig(ctx context.Context, probed probeResult) error {
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "server", "reload-config")
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
	descriptor, err := nextDashboardDescriptor(layout, current, marker, options.Owners, options.Environment)
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
	marker ownerMarker,
	owners []corebackend.DashboardShortcutOwner,
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
	descriptor.SessionID, descriptor.SocketPath = marker.Session, marker.SocketPath
	descriptor.Owners, err = dashboardOwnersForRoute(marker, owners)
	if err != nil {
		return dashboardDescriptor{}, err
	}
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
	var envelope dashboardReloadEnvelope
	if err := decodeOne(out, &envelope); err != nil {
		return fmt.Errorf("parse Herdr config reload response: %w", err)
	}
	if envelope.ID != "cli:server:reload-config" || envelope.Result == nil || envelope.Error != nil ||
		envelope.Result.Type != "config_reload" || envelope.Result.Diagnostics == nil {
		return fmt.Errorf("unexpected Herdr config_reload envelope")
	}
	if envelope.Result.Status != "applied" {
		return fmt.Errorf("herdr config reload status is %q, want applied", envelope.Result.Status)
	}
	return nil
}

func filterDashboardEnvironment(environment []string) []string {
	values := dashboardEnvironmentValues(environment)
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

func dashboardEnvironmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

func dashboardAuthenticationFromCaller(environment []string) dashboardAuthentication {
	values := dashboardEnvironmentValues(environment)
	return dashboardAuthentication{
		tokenSHA256: dashboardAuthenticationSHA256(dashboardEffectiveToken(values)),
	}
}

func dashboardAuthenticationFromSupervisor(environment []string) dashboardAuthentication {
	values := dashboardEnvironmentValues(environment)
	return dashboardAuthentication{
		tokenSHA256: dashboardAuthenticationSHA256(values[dashboardRelayTokenEnv]),
	}
}

func dashboardEffectiveToken(values map[string]string) string {
	if token := values[dashboardGHTokenEnv]; token != "" {
		return token
	}
	return values[dashboardGitHubTokenEnv]
}

func dashboardAuthenticationSHA256(value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func dashboardSupervisorEnvironment(environment []string) []string {
	values := dashboardEnvironmentValues(environment)
	return dashboardTokenEnvironment(dashboardEffectiveToken(values), dashboardRelayTokenEnv)
}

func dashboardInheritedAuthenticationEnvironment(environment []string) []string {
	values := dashboardEnvironmentValues(environment)
	return dashboardTokenEnvironment(values[dashboardRelayTokenEnv], dashboardRelayTokenEnv)
}

func dashboardOpenAuthenticationEnvironment(environment []string) []string {
	values := dashboardEnvironmentValues(environment)
	return dashboardTokenEnvironment(values[dashboardRelayTokenEnv], dashboardGHTokenEnv)
}

func dashboardTokenEnvironment(token, name string) []string {
	if token == "" {
		return nil
	}
	return []string{name + "=" + token}
}

func validateDashboardAuthentication(marker ownerMarker, options corebackend.DashboardShortcutOptions) error {
	if !options.Enabled {
		return nil
	}
	caller := dashboardAuthenticationFromCaller(options.Environment)
	if caller.tokenSHA256 != "" && caller.tokenSHA256 != marker.DashboardTokenSHA256 {
		return fmt.Errorf("environment-backed GitHub authentication is not available to the owned Herdr server; close or clean up its rows, run fanout herdr shutdown, then relaunch before enabling F12")
	}
	return nil
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
	if descriptor.SessionID != filepath.Base(layout.runtimeDir) || descriptor.SocketPath != layout.socketPath {
		return fmt.Errorf("herdr dashboard route does not match the owned runtime")
	}
	if err := validateDashboardOwners(descriptor.Owners); err != nil {
		return err
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

func dashboardOwnersForRoute(
	marker ownerMarker,
	owners []corebackend.DashboardShortcutOwner,
) ([]dashboardOwner, error) {
	result := make([]dashboardOwner, 0, len(owners))
	for _, owner := range owners {
		if owner.SessionID != marker.Session || owner.SocketPath != marker.SocketPath {
			continue
		}
		result = append(result, dashboardOwner{
			PaneID: owner.PaneID, WorkspaceID: owner.WorkspaceID, StatePath: owner.StatePath,
		})
	}
	slices.SortFunc(result, compareDashboardOwner)
	if err := validateDashboardOwners(result); err != nil {
		return nil, err
	}
	return result, nil
}

func compareDashboardOwner(left, right dashboardOwner) int {
	if order := strings.Compare(left.WorkspaceID, right.WorkspaceID); order != 0 {
		return order
	}
	if order := strings.Compare(left.PaneID, right.PaneID); order != 0 {
		return order
	}
	return strings.Compare(left.StatePath, right.StatePath)
}

func validateDashboardOwners(owners []dashboardOwner) error {
	if len(owners) == 0 || !slices.IsSortedFunc(owners, compareDashboardOwner) {
		return fmt.Errorf("herdr dashboard owner mapping is empty or non-canonical")
	}
	panes, workspaces := map[string]string{}, map[string]string{}
	for i, owner := range owners {
		if err := validateDashboardOwner(owner); err != nil {
			return err
		}
		if i > 0 && owner == owners[i-1] {
			return fmt.Errorf("herdr dashboard owner mapping contains a duplicate")
		}
		if err := recordDashboardOwner(panes, owner.PaneID, owner.StatePath); err != nil {
			return err
		}
		if err := recordDashboardOwner(workspaces, owner.WorkspaceID, owner.StatePath); err != nil {
			return err
		}
	}
	return nil
}

func validateDashboardOwner(owner dashboardOwner) error {
	if strings.TrimSpace(owner.PaneID) == "" || strings.TrimSpace(owner.WorkspaceID) == "" {
		return fmt.Errorf("herdr dashboard owner identity is incomplete")
	}
	return validateDashboardStatePath(owner.StatePath)
}

func recordDashboardOwner(owners map[string]string, identity, statePath string) error {
	existing, found := owners[identity]
	if found && existing != statePath {
		return fmt.Errorf("herdr dashboard owner mapping is ambiguous")
	}
	owners[identity] = statePath
	return nil
}

func validateDashboardStatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "state.json" ||
		filepath.Base(filepath.Dir(path)) != ".fanout" {
		return fmt.Errorf("herdr dashboard state path is invalid")
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
	if err := validateRunningDashboardHelper(descriptor); err != nil {
		return dashboardDescriptor{}, err
	}
	return descriptor, validateActiveDashboardRoute(descriptor)
}

func execDashboardDescriptor(descriptor dashboardDescriptor) error {
	statePath, err := activeDashboardStatePath(descriptor)
	if err != nil {
		return err
	}
	environment := append([]string{}, descriptor.Environment...)
	environment = append(environment, dashboardOpenAuthenticationEnvironment(os.Environ())...)
	environment = append(environment,
		"FANOUT_BACKEND=herdr", "FANOUT_BIN="+descriptor.DashboardPath,
		"FANOUT_STATE_PATH="+statePath,
	)
	return dashboardExec(descriptor.DashboardPath, []string{
		descriptor.DashboardPath, "dashboard", "--web", "--open", "--no-keybind",
	}, environment)
}

func validateActiveDashboardRoute(descriptor dashboardDescriptor) error {
	if os.Getenv("HERDR_SOCKET_PATH") != descriptor.SocketPath {
		return fmt.Errorf("active Herdr route does not match the dashboard descriptor")
	}
	return nil
}

func activeDashboardStatePath(descriptor dashboardDescriptor) (string, error) {
	paneID := strings.TrimSpace(os.Getenv("HERDR_ACTIVE_PANE_ID"))
	workspaceID := strings.TrimSpace(os.Getenv("HERDR_ACTIVE_WORKSPACE_ID"))
	paneState, workspaceState := "", ""
	for _, owner := range descriptor.Owners {
		if owner.PaneID == paneID {
			paneState = owner.StatePath
		}
		if owner.WorkspaceID == workspaceID {
			workspaceState = owner.StatePath
		}
	}
	if paneState != "" && workspaceState != "" && paneState != workspaceState {
		return "", fmt.Errorf("active Herdr pane and workspace map to different dashboard owners")
	}
	if paneState != "" {
		return paneState, nil
	}
	if workspaceState != "" {
		return workspaceState, nil
	}
	return "", fmt.Errorf("active Herdr pane is not bound to a dashboard state owner")
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
