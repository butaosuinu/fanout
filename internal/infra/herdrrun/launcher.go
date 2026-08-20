package herdrrun

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	paneLauncherFlagEnv    = "FANOUT_HERDR_PANE_LAUNCHER"
	paneLauncherPathEnv    = "FANOUT_HERDR_LAUNCHER_PATH"
	paneLauncherControlEnv = "FANOUT_HERDR_CONTROL_PATH"
	paneIDEnv              = "HERDR_PANE_ID"
	workspaceIDEnv         = "HERDR_WORKSPACE_ID"
	workloadEnvSchema      = 1
	launcherPollInterval   = time.Second
)

var workloadLaunchNonce = regexp.MustCompile(`^[0-9a-f]{32}$`)

type workloadEnvCapsule struct {
	SchemaVersion int      `json:"schemaVersion"`
	LaunchNonce   string   `json:"launchNonce"`
	Environment   []string `json:"environment"`
}

// IsPaneLauncherRequest reports whether Herdr started this executable as the
// configured non-login terminal shell.
func IsPaneLauncherRequest() bool {
	return os.Getenv(paneLauncherFlagEnv) == "1"
}

// RunPaneLauncher waits for the operation-bound intent. A coordinator without
// a launch remains inert; launch-bearing coordinators and children consume one
// token and exec their recorded workload.
func RunPaneLauncher(in io.Reader, out, errOut io.Writer) int {
	request, err := paneLauncherRequestFromEnvironment()
	if err != nil {
		fmt.Fprintf(errOut, "fanout herdr pane launcher: %v\n", err)
		return 2
	}
	intent, err := waitForPaneLaunchIntent(request)
	if err != nil {
		fmt.Fprintf(errOut, "fanout herdr pane launcher: %v\n", err)
		return 1
	}
	if intent.Kind == state.IntentCoordinator && intent.Launch == nil {
		return holdCoordinatorLauncher(in, errOut)
	}
	return runWorkloadPaneLauncher(in, out, errOut, request, intent)
}

func runWorkloadPaneLauncher(
	in io.Reader,
	out, errOut io.Writer,
	request paneLauncherRequest,
	intent state.LaunchIntent,
) int {
	marker := launcherReadyMarker(intent.Launch.Nonce)
	if err := writeLauncherReady(out, marker); err != nil {
		fmt.Fprintf(errOut, "fanout herdr pane launcher: %v\n", err)
		return 1
	}
	if err := waitForLaunchToken(in, out, intent); err != nil {
		fmt.Fprintf(errOut, "fanout herdr pane launcher: %v\n", err)
		return 1
	}
	environment, err := consumeWorkloadEnvironment(intent.Launch, launcherRuntimeDir(request.launcherPath))
	if err != nil {
		fmt.Fprintf(errOut, "fanout herdr pane launcher: %v\n", err)
		return 1
	}
	environment = workloadExecEnvironment(request, intent, environment)
	argv := append([]string{intent.Launch.Executable}, intent.Launch.Args...)
	if err := syscall.Exec(intent.Launch.Executable, argv, environment); err != nil {
		fmt.Fprintf(errOut, "fanout herdr pane launcher: exec workload: %v\n", err)
		return 1
	}
	panic("unreachable")
}

func workloadExecEnvironment(
	request paneLauncherRequest,
	intent state.LaunchIntent,
	environment []string,
) []string {
	if intent.Launch.Agent == "" {
		return append(environment,
			"HERDR_ENV=1",
			sessionEnv+"="+request.session,
			socketEnv+"="+request.socketPath,
			workspaceIDEnv+"="+request.workspaceID,
			paneIDEnv+"="+request.paneID,
		)
	}
	if directAgentIntegrationLaunch(intent) {
		environment = append(environment,
			"HERDR_ENV=1", socketEnv+"="+request.socketPath, paneIDEnv+"="+request.paneID,
		)
	}
	return bindHerdrEmitterEnvironment(intent, environment)
}

// directAgentIntegrationLaunch reports whether this worktree or resume launch
// is one an installed Herdr agent integration is granted the owned socket for.
// The integration reports the provider session over that socket from an
// agent-side hook, so exactly these workloads receive HERDR_ENV, the socket
// path, and their own pane id; the session and workspace route stay with the
// launcher. A workload that never gets them keeps an agent name and no
// session, which focusOwned then refuses as a partial live-agent identity.
//
// The grant is narrower than "execs the provider CLI", and each exclusion is
// deliberate. Coordinator launches exec the CLI too, but the grant has never
// covered them, so an attached agent's row keeps the one-sided identity. The
// Codex Plan Mode and team controllers exec fanout rather than the provider,
// so no integration hook could run inside them. OpenCode ships an integration
// too, but it has not been measured on this path.
func directAgentIntegrationLaunch(intent state.LaunchIntent) bool {
	launch := intent.Launch
	directKind := intent.Kind == state.IntentWorktree || intent.Kind == state.IntentResume
	if !directKind || launch == nil {
		return false
	}
	// A controller-bearing capsule execs fanout rather than the provider, so it
	// is excluded ahead of the allowlist: the property belongs to the workload,
	// not to the agent name that happens to carry it today.
	if launch.CodexPlanStatusPath != "" || launch.CodexTeamStatusPath != "" {
		return false
	}
	switch launch.Agent {
	case "claude", "codex":
		return true
	default:
		return false
	}
}

func bindHerdrEmitterEnvironment(intent state.LaunchIntent, environment []string) []string {
	launch := intent.Launch
	if launch == nil || launch.EmitterNonce == "" {
		return environment
	}
	bindings := map[string]string{
		telemetry.RowKeyEnv: intent.ID, telemetry.LaunchNonceEnv: launch.Nonce,
		telemetry.EmitterNonceEnv: launch.EmitterNonce, telemetry.BackendEnv: "herdr",
		telemetry.SessionEnv: intent.Session, telemetry.SocketPathEnv: intent.SocketPath,
		telemetry.WorkspaceIDEnv: intent.Resource.WorkspaceID,
		telemetry.PaneIDEnv:      intent.Resource.PaneID,
		telemetry.TerminalIDEnv:  intent.Resource.TerminalID,
		telemetry.AgentEnv:       launch.Agent, telemetry.AgentIDEnv: launch.AgentName,
	}
	for i, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if value, ok := bindings[name]; ok {
			environment[i] = name + "=" + value
		}
	}
	return environment
}

func holdCoordinatorLauncher(in io.Reader, errOut io.Writer) int {
	var input [1]byte
	_, err := in.Read(input[:])
	if errors.Is(err, io.EOF) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(errOut, "fanout herdr coordinator launcher: %v\n", err)
		return 1
	}
	fmt.Fprintln(errOut, "fanout herdr coordinator launcher: unexpected input")
	return 1
}

type paneLauncherRequest struct {
	controlPath  string
	launcherPath string
	session      string
	socketPath   string
	workspaceID  string
	paneID       string
	cwd          string
}

func paneLauncherRequestFromEnvironment() (paneLauncherRequest, error) {
	executable, err := os.Executable()
	if err != nil {
		return paneLauncherRequest{}, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return paneLauncherRequest{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return paneLauncherRequest{}, err
	}
	request := paneLauncherRequest{
		controlPath:  filepath.Clean(os.Getenv(paneLauncherControlEnv)),
		launcherPath: filepath.Clean(os.Getenv(paneLauncherPathEnv)),
		session:      strings.TrimSpace(os.Getenv(sessionEnv)),
		socketPath:   filepath.Clean(os.Getenv(socketEnv)),
		workspaceID:  strings.TrimSpace(os.Getenv(workspaceIDEnv)),
		paneID:       strings.TrimSpace(os.Getenv(paneIDEnv)), cwd: filepath.Clean(cwd),
	}
	requirements := []bool{
		filepath.IsAbs(request.controlPath), filepath.IsAbs(request.launcherPath),
		request.launcherPath == executable, request.session != "", filepath.IsAbs(request.socketPath),
		request.workspaceID != "", request.paneID != "",
		filepath.IsAbs(request.cwd), filepath.Base(filepath.Dir(request.launcherPath)) == "launcher",
	}
	if slices.Contains(requirements, false) {
		return paneLauncherRequest{}, fmt.Errorf("invalid launcher environment")
	}
	return request, nil
}

func launcherRuntimeDir(launcherPath string) string {
	return filepath.Dir(filepath.Dir(launcherPath))
}

func waitForPaneLaunchIntent(request paneLauncherRequest) (state.LaunchIntent, error) {
	deadline := time.Now().Add(corebackend.DefaultWaitTimeout)
	for time.Now().Before(deadline) {
		store, err := state.LoadLaunchJournalPath(request.controlPath)
		if err != nil {
			return state.LaunchIntent{}, fmt.Errorf("read launch intent: %w", err)
		}
		if intent, found := matchingPaneLaunchIntent(store, request); found {
			if time.Now().UnixMilli() >= intent.ExpiresUnixMS {
				return state.LaunchIntent{}, fmt.Errorf("launch intent expired")
			}
			return intent, nil
		}
		time.Sleep(launcherPollInterval)
	}
	return state.LaunchIntent{}, fmt.Errorf("timed out waiting for launch intent")
}

func matchingPaneLaunchIntent(
	store state.LaunchJournal,
	request paneLauncherRequest,
) (state.LaunchIntent, bool) {
	for _, intent := range store.Intents {
		if intent.Status == state.IntentRealized && paneLauncherIntentReady(intent) &&
			intent.Session == request.session && intent.SocketPath == request.socketPath &&
			intent.Resource.WorkspaceID == request.workspaceID &&
			intent.Resource.PaneID == request.paneID &&
			filepath.Clean(intent.Resource.CurrentPath) == request.cwd {
			return intent, true
		}
	}
	return state.LaunchIntent{}, false
}

func paneLauncherIntentReady(intent state.LaunchIntent) bool {
	return intent.Kind == state.IntentCoordinator ||
		(intent.Kind == state.IntentWorktree || intent.Kind == state.IntentResume) &&
			intent.Launch != nil
}

func launcherReadyMarker(nonce string) string {
	return "FANOUT_HERDR_READY:" + nonce
}

func launcherStartToken(nonce string) string {
	return "FANOUT_HERDR_START:" + nonce
}

func writeLauncherReady(out io.Writer, marker string) error {
	if _, err := fmt.Fprintln(out, marker); err != nil {
		return fmt.Errorf("write launcher ready marker: %w", err)
	}
	return nil
}

type launcherInput struct {
	line string
	err  error
}

func waitForLaunchToken(in io.Reader, out io.Writer, intent state.LaunchIntent) error {
	return waitForLaunchTokenAtInterval(in, out, intent, launcherPollInterval)
}

func waitForLaunchTokenAtInterval(
	in io.Reader,
	out io.Writer,
	intent state.LaunchIntent,
	markerInterval time.Duration,
) error {
	deadline := time.UnixMilli(intent.ExpiresUnixMS)
	want := launcherStartToken(intent.Launch.Nonce)
	lines := make(chan launcherInput)
	go scanLauncherInput(in, lines)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	markerTicker := time.NewTicker(markerInterval)
	defer markerTicker.Stop()
	for {
		select {
		case input, ok := <-lines:
			return validateLauncherInput(input, ok, want)
		case <-markerTicker.C:
			if err := writeLauncherReady(out, launcherReadyMarker(intent.Launch.Nonce)); err != nil {
				return err
			}
		case <-timer.C:
			return fmt.Errorf("timed out waiting for start token")
		}
	}
}

func validateLauncherInput(input launcherInput, open bool, want string) error {
	if !open {
		return fmt.Errorf("launcher input closed before start token")
	}
	if input.err != nil {
		return fmt.Errorf("read launcher input: %w", input.err)
	}
	if input.line != want {
		return fmt.Errorf("unexpected launcher input")
	}
	return nil
}

func scanLauncherInput(in io.Reader, lines chan<- launcherInput) {
	defer close(lines)
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		lines <- launcherInput{line: scanner.Text()}
	}
	if err := scanner.Err(); err != nil {
		lines <- launcherInput{err: err}
	}
}

// WorkloadEnvironment snapshots the caller environment before owned routing
// values are introduced, then adds only the explicit fanout workload values.
func WorkloadEnvironment(caller []string, fanoutPath string) ([]string, error) {
	if err := validateWorkloadExecutable(fanoutPath); err != nil {
		return nil, err
	}
	kept, err := filterCallerEnvironment(caller)
	if err != nil {
		return nil, err
	}
	kept = append(kept, "FANOUT_BACKEND=herdr", "FANOUT_BIN="+fanoutPath)
	if err := validateWorkloadEnvironment(kept); err != nil {
		return nil, err
	}
	return kept, nil
}

// WorkloadEnvironment lets a caller holding only this session build the
// capsule contents through the same filter the package function applies.
func (s *OwnedSession) WorkloadEnvironment(caller []string, fanoutPath string) ([]string, error) {
	return WorkloadEnvironment(caller, fanoutPath)
}

func validateWorkloadExecutable(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00') {
		return fmt.Errorf("fanout workload executable must be a canonical absolute path")
	}
	return nil
}

func filterCallerEnvironment(caller []string) ([]string, error) {
	kept := make([]string, 0, len(caller)+2)
	seen := map[string]bool{}
	for _, entry := range caller {
		name, _, err := parseEnvironmentEntry(entry, seen)
		if err != nil {
			return nil, err
		}
		if !blockedCallerEnvironmentName(name) {
			kept = append(kept, entry)
		}
	}
	return kept, nil
}

func blockedCallerEnvironmentName(name string) bool {
	return blockedWorkloadEnvironmentName(name) || strings.HasPrefix(name, "FANOUT_EMITTER_")
}

func blockedWorkloadEnvironmentName(name string) bool {
	return strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "FANOUT_HERDR_") ||
		slices.Contains([]string{"TMUX", "TMUX_PANE", "TMUX_TMPDIR", "FANOUT_STATE_PATH", "FANOUT_BACKEND", "FANOUT_BIN"}, name)
}

func validateWorkloadEnvironment(environment []string) error {
	seen := map[string]bool{}
	for _, entry := range environment {
		name, value, err := parseEnvironmentEntry(entry, seen)
		if err != nil {
			return err
		}
		allowedRouting := (name == "FANOUT_BACKEND" && value == "herdr") || name == "FANOUT_BIN"
		if blockedWorkloadEnvironmentName(name) && !allowedRouting {
			return fmt.Errorf("workload environment contains control-plane name %q", name)
		}
	}
	if !seen["FANOUT_BACKEND"] || !seen["FANOUT_BIN"] {
		return fmt.Errorf("workload environment is missing fanout routing")
	}
	return nil
}

func parseEnvironmentEntry(entry string, seen map[string]bool) (string, string, error) {
	name, value, ok := strings.Cut(entry, "=")
	if !ok || name == "" || strings.ContainsRune(entry, '\x00') || seen[name] {
		return "", "", fmt.Errorf("invalid or duplicate workload environment name %q", name)
	}
	seen[name] = true
	return name, value, nil
}

// PrepareWorkloadEnvironment writes an owner-only, one-shot environment
// capsule under the owned runtime directory.
func (s *OwnedSession) PrepareWorkloadEnvironment(
	nonce string,
	environment []string,
) (string, int, error) {
	if s == nil || !workloadLaunchNonce.MatchString(nonce) {
		return "", 0, fmt.Errorf("invalid Herdr workload environment request")
	}
	if err := validateWorkloadEnvironment(environment); err != nil {
		return "", 0, err
	}
	dir := filepath.Join(s.RuntimeDir, "workload-env")
	if err := ensurePrivateDir(dir); err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, "env-"+nonce+".json")
	capsule := workloadEnvCapsule{SchemaVersion: workloadEnvSchema, LaunchNonce: nonce, Environment: environment}
	data, err := json.Marshal(capsule)
	if err != nil {
		return "", 0, err
	}
	if len(data) > maxOwnerMarkerBytes {
		return "", 0, fmt.Errorf("workload environment capsule exceeds size limit")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", 0, err
	}
	writeErr := writeAndSync(file, data)
	if writeErr != nil {
		return "", 0, errors.Join(writeErr, os.Remove(path))
	}
	return path, len(environment), nil
}

func writeAndSync(file *os.File, data []byte) error {
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func consumeWorkloadEnvironment(launch *state.LaunchCapsule, runtimeDir string) ([]string, error) {
	if err := validateWorkloadEnvironmentLocation(runtimeDir, launch); err != nil {
		return nil, err
	}
	data, err := readWorkloadEnvironmentCapsule(launch.EnvFilePath)
	if err != nil {
		return nil, err
	}
	environment, err := decodeWorkloadEnvironmentCapsule(data, launch)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(launch.EnvFilePath); err != nil {
		return nil, err
	}
	return environment, nil
}

// DiscardWorkloadEnvironment removes an unconsumed capsule only after its
// persisted nonce, owned runtime location, and file identity all match.
func DiscardWorkloadEnvironment(runtimeDir string, launch *state.LaunchCapsule) (err error) {
	defer errs.Wrap(&err, "discard Herdr workload environment")

	if validationErr := validateWorkloadEnvironmentLocation(runtimeDir, launch); validationErr != nil {
		return validationErr
	}
	file, err := os.OpenFile(launch.EnvFilePath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(launch.EnvFilePath)
	identityErr := validateWorkloadEnvironmentFileIdentity(launch.EnvFilePath, opened, pathInfo, statErr, pathErr)
	closeErr := file.Close()
	if err := errors.Join(identityErr, closeErr); err != nil {
		return err
	}
	return os.Remove(launch.EnvFilePath)
}

// DiscardWorkloadEnvironment lets a caller holding only this session drop an
// unconsumed capsule through the same identity checks the package function
// applies.
func (s *OwnedSession) DiscardWorkloadEnvironment(runtimeDir string, launch *state.LaunchCapsule) error {
	return DiscardWorkloadEnvironment(runtimeDir, launch)
}

func validateWorkloadEnvironmentLocation(runtimeDir string, launch *state.LaunchCapsule) error {
	if launch == nil || !filepath.IsAbs(runtimeDir) || filepath.Clean(runtimeDir) != runtimeDir ||
		!workloadLaunchNonce.MatchString(launch.Nonce) {
		return fmt.Errorf("workload environment capsule has an invalid owned runtime identity")
	}
	expectedPath := filepath.Join(runtimeDir, "workload-env", "env-"+launch.Nonce+".json")
	if launch.EnvFilePath != expectedPath {
		return fmt.Errorf("workload environment capsule is outside the owned runtime")
	}
	return nil
}

func validateWorkloadEnvironmentFileIdentity(
	path string,
	opened, current os.FileInfo,
	statErr, pathErr error,
) error {
	if err := errors.Join(statErr, pathErr); err != nil {
		return err
	}
	if err := validatePrivateRegular(path, opened); err != nil {
		return err
	}
	if err := validatePrivateRegular(path, current); err != nil {
		return err
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf("workload environment capsule identity changed before removal")
	}
	return nil
}

func readWorkloadEnvironmentCapsule(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	if statErr == nil {
		statErr = validatePrivateRegular(path, info)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxOwnerMarkerBytes+1))
	closeErr := file.Close()
	if err := errors.Join(statErr, readErr, closeErr); err != nil {
		return nil, err
	}
	if len(data) > maxOwnerMarkerBytes {
		return nil, fmt.Errorf("workload environment capsule exceeds size limit")
	}
	return data, nil
}

func decodeWorkloadEnvironmentCapsule(data []byte, launch *state.LaunchCapsule) ([]string, error) {
	var capsule workloadEnvCapsule
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capsule); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("workload environment capsule has trailing data")
	}
	if capsule.SchemaVersion != workloadEnvSchema || capsule.LaunchNonce != launch.Nonce ||
		len(capsule.Environment) != launch.EnvNameCount {
		return nil, fmt.Errorf("workload environment capsule does not match launch intent")
	}
	if err := validateWorkloadEnvironment(capsule.Environment); err != nil {
		return nil, err
	}
	return capsule.Environment, nil
}
