package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"

	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestWorkloadEnvironmentRemovesControlPlaneAndForcesBackend(t *testing.T) {
	caller := []string{
		"PATH=/caller/bin", "KEEP=value", "HERDR_SESSION=foreign",
		"FANOUT_HERDR_CONTROL_PATH=/foreign", "TMUX=/tmp/tmux", "TMUX_PANE=%1",
		"TMUX_TMPDIR=/tmp", "FANOUT_STATE_PATH=/foreign/state",
		"FANOUT_BACKEND=tmux", "FANOUT_BIN=/foreign/fanout",
		dashboardRelayTokenEnv + "=secret",
		"FANOUT_EMITTER_NONCE=foreign", "FANOUT_EMITTER_STATE_PATH=/foreign/state",
		"FANOUT_CONSOLE_SHELL=/bin/zsh", // console-pane inheritance; the console records its own copy
	}
	got, err := WorkloadEnvironment(caller, "/owned/fanout")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PATH=/caller/bin", "KEEP=value", "FANOUT_BACKEND=herdr", "FANOUT_BIN=/owned/fanout",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}

func TestWorkloadEnvironmentRejectsDuplicateNames(t *testing.T) {
	_, err := WorkloadEnvironment([]string{"PATH=/one", "PATH=/two"}, "/owned/fanout")
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v, want duplicate rejection", err)
	}
}

// The owned socket reaches a workload only where an installed Herdr agent
// integration can use it to report the provider session; every other launch
// keeps the capsule environment untouched.
func TestWorkloadExecEnvironmentRoutesAgentIntegrations(t *testing.T) {
	request := paneLauncherRequest{
		session: "owned-session", socketPath: "/owned/herdr.sock",
		workspaceID: "w1", paneID: "w1:p1",
	}
	base := []string{"PATH=/bin", "FANOUT_BACKEND=herdr"}
	shellRoute := []string{
		"HERDR_ENV=1", "HERDR_SESSION=owned-session", "HERDR_SOCKET_PATH=/owned/herdr.sock",
		"HERDR_WORKSPACE_ID=w1", "HERDR_PANE_ID=w1:p1",
	}
	integrationRoute := []string{
		"HERDR_ENV=1", "HERDR_SOCKET_PATH=/owned/herdr.sock", "HERDR_PANE_ID=w1:p1",
	}
	tests := []struct {
		name   string
		intent state.LaunchIntent
		want   []string
	}{
		{
			name:   "shell pane carries the whole owned route",
			intent: state.LaunchIntent{Launch: &state.LaunchCapsule{}},
			want:   shellRoute,
		},
		{
			name: "direct claude carries the integration route only",
			intent: state.LaunchIntent{
				Kind: state.IntentWorktree, Launch: &state.LaunchCapsule{Agent: "claude"},
			},
			want: integrationRoute,
		},
		{
			name: "direct codex carries the integration route only",
			intent: state.LaunchIntent{
				Kind: state.IntentWorktree, Launch: &state.LaunchCapsule{Agent: "codex"},
			},
			want: integrationRoute,
		},
		{
			name: "resumed codex carries the integration route only",
			intent: state.LaunchIntent{
				Kind: state.IntentResume, Launch: &state.LaunchCapsule{Agent: "codex"},
			},
			want: integrationRoute,
		},
		{
			name: "attached claude carries the integration route only",
			intent: state.LaunchIntent{
				Kind: state.IntentCoordinator, Launch: &state.LaunchCapsule{Agent: "claude"},
			},
			want: integrationRoute,
		},
		{
			name: "attached codex carries the integration route only",
			intent: state.LaunchIntent{
				Kind: state.IntentCoordinator, Launch: &state.LaunchCapsule{Agent: "codex"},
			},
			want: integrationRoute,
		},
		{
			// The Plan Mode and team controllers drive codex through fanout
			// instead of exec-ing its CLI, so no integration hook runs there.
			name: "codex plan controller carries nothing",
			intent: state.LaunchIntent{
				Kind:   state.IntentWorktree,
				Launch: &state.LaunchCapsule{Agent: "codex", CodexPlanStatusPath: "/runtime/plan.json"},
			},
		},
		{
			name: "codex team controller carries nothing",
			intent: state.LaunchIntent{
				Kind:   state.IntentWorktree,
				Launch: &state.LaunchCapsule{Agent: "codex", CodexTeamStatusPath: "/runtime/team.json"},
			},
		},
		{
			name: "resumed claude carries the integration route only",
			intent: state.LaunchIntent{
				Kind: state.IntentResume, Launch: &state.LaunchCapsule{Agent: "claude"},
			},
			want: integrationRoute,
		},
		{
			// The controller exclusion is checked before the agent allowlist, so
			// a claude capsule carrying one is excluded the same way codex is.
			name: "claude plan controller carries nothing",
			intent: state.LaunchIntent{
				Kind:   state.IntentWorktree,
				Launch: &state.LaunchCapsule{Agent: "claude", CodexPlanStatusPath: "/runtime/plan.json"},
			},
		},
		{
			name: "claude team controller carries nothing",
			intent: state.LaunchIntent{
				Kind:   state.IntentWorktree,
				Launch: &state.LaunchCapsule{Agent: "claude", CodexTeamStatusPath: "/runtime/team.json"},
			},
		},
		{
			// opencode's integration is not verified against this launch path.
			name: "opencode carries nothing",
			intent: state.LaunchIntent{
				Kind: state.IntentWorktree, Launch: &state.LaunchCapsule{Agent: "opencode"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := append(append([]string(nil), base...), tt.want...)
			got := workloadExecEnvironment(request, tt.intent, append([]string(nil), base...))
			if !slices.Equal(got, want) {
				t.Fatalf(
					"workloadExecEnvironment(kind=%q, agent=%q) = %q, want %q",
					tt.intent.Kind, tt.intent.Launch.Agent, got, want,
				)
			}
		})
	}
}

func TestDirectAgentIntegrationLaunchRequiresProviderWorkload(t *testing.T) {
	if directAgentIntegrationLaunch(nil) {
		t.Fatal("idle coordinator scaffold received the agent integration grant")
	}
	if directAgentIntegrationLaunch(&state.LaunchCapsule{}) {
		t.Fatal("console or shell workload received the agent integration grant")
	}
}

func TestWorkloadExecEnvironmentBindsEmitterToRealizedCoordinator(t *testing.T) {
	intent := state.LaunchIntent{
		ID: "coordinator:manual:/repo:530", Session: "owned-session", SocketPath: "/owned/herdr.sock",
		WorktreePath: "/repo/worktree",
		Resource: state.RuntimeResource{
			WorkspaceID: "w1", Label: "owned-label-1",
			PaneID: "w1:p1", TerminalID: "terminal-1",
		},
		Launch: &state.LaunchCapsule{
			Nonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
			Agent: "claude", AgentName: "fanout-agent",
		},
	}
	environment := []string{
		telemetry.RowKeyEnv + "=", telemetry.LaunchNonceEnv + "=",
		telemetry.EmitterNonceEnv + "=", telemetry.BackendEnv + "=",
		telemetry.SessionEnv + "=", telemetry.SocketPathEnv + "=",
		telemetry.WorkspaceIDEnv + "=", telemetry.WorkspaceLabelEnv + "=",
		telemetry.WorktreePathEnv + "=", telemetry.PaneIDEnv + "=",
		telemetry.TerminalIDEnv + "=", telemetry.AgentEnv + "=",
		telemetry.AgentIDEnv + "=", "PATH=/bin",
	}
	got := workloadExecEnvironment(paneLauncherRequest{}, intent, environment)
	want := map[string]string{
		telemetry.RowKeyEnv: intent.ID, telemetry.LaunchNonceEnv: intent.Launch.Nonce,
		telemetry.EmitterNonceEnv: intent.Launch.EmitterNonce, telemetry.BackendEnv: "herdr",
		telemetry.SessionEnv: intent.Session, telemetry.SocketPathEnv: intent.SocketPath,
		telemetry.WorkspaceIDEnv:    intent.Resource.WorkspaceID,
		telemetry.WorkspaceLabelEnv: intent.Resource.Label,
		telemetry.WorktreePathEnv:   intent.WorktreePath,
		telemetry.PaneIDEnv:         intent.Resource.PaneID,
		telemetry.TerminalIDEnv:     intent.Resource.TerminalID,
		telemetry.AgentEnv:          intent.Launch.Agent, telemetry.AgentIDEnv: intent.Launch.AgentName,
	}
	for _, entry := range got {
		name, value, _ := strings.Cut(entry, "=")
		if expected, ok := want[name]; ok && value != expected {
			t.Fatalf("environment[%s] = %q, want %q", name, value, expected)
		}
	}
}

func TestWorkloadEnvironmentCapsuleIsOwnerOnlyAndOneShot(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	session := &OwnedSession{RuntimeDir: runtimeDir}
	nonce := strings.Repeat("a", 32)
	environment := []string{"PATH=/caller/bin", "FANOUT_BACKEND=herdr", "FANOUT_BIN=/owned/fanout"}
	path, count, err := session.PrepareWorkloadEnvironment(nonce, environment)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("capsule mode = %v, want owner-only regular", info.Mode())
	}
	got, err := consumeWorkloadEnvironment(&state.LaunchCapsule{
		Nonce: nonce, EnvFilePath: path, EnvNameCount: count,
	}, runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, environment) {
		t.Fatalf("consumed environment = %q, want %q", got, environment)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capsule remains after consume: %v", err)
	}
}

func TestPrepareWorkloadEnvironmentRejectsOversizeBeforePublish(t *testing.T) {
	runtimeDir := t.TempDir()
	session := &OwnedSession{RuntimeDir: runtimeDir}
	nonce := strings.Repeat("b", 32)
	environment := []string{
		"BIG=" + strings.Repeat("x", maxOwnerMarkerBytes),
		"FANOUT_BACKEND=herdr", "FANOUT_BIN=/owned/fanout",
	}
	_, _, err := session.PrepareWorkloadEnvironment(nonce, environment)
	if err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("error = %v, want size rejection", err)
	}
	path := filepath.Join(runtimeDir, "workload-env", "env-"+nonce+".json")
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("oversize capsule was published: %v", statErr)
	}
}

func TestConsumeWorkloadEnvironmentRejectsPathOutsideOwnedRuntime(t *testing.T) {
	runtimeDir := t.TempDir()
	launch := &state.LaunchCapsule{
		Nonce: strings.Repeat("a", 32), EnvFilePath: filepath.Join(t.TempDir(), "capsule.json"), EnvNameCount: 1,
	}
	if _, err := consumeWorkloadEnvironment(launch, runtimeDir); err == nil ||
		!strings.Contains(err.Error(), "outside") {
		t.Fatalf("error = %v, want outside-owned-runtime rejection", err)
	}
}

func TestDiscardWorkloadEnvironmentRequiresOwnedPathAndFileIdentity(t *testing.T) {
	runtimeDir := t.TempDir()
	session := &OwnedSession{RuntimeDir: runtimeDir}
	nonce := strings.Repeat("c", 32)
	path, count, err := session.PrepareWorkloadEnvironment(nonce, []string{
		"PATH=/bin", "FANOUT_BACKEND=herdr", "FANOUT_BIN=/owned/fanout",
	})
	if err != nil {
		t.Fatal(err)
	}
	launch := &state.LaunchCapsule{Nonce: nonce, EnvFilePath: path, EnvNameCount: count}
	if err := DiscardWorkloadEnvironment(runtimeDir, launch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded capsule remains: %v", err)
	}

	foreign := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	launch.EnvFilePath = foreign
	if err := DiscardWorkloadEnvironment(runtimeDir, launch); err == nil {
		t.Fatal("outside capsule path was accepted")
	}
	if _, err := os.Lstat(foreign); err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
}

func TestMatchingPaneLaunchIntentRequiresExactWorkspacePaneAndCWD(t *testing.T) {
	request := paneLauncherRequest{
		session: "owned-session", socketPath: "/owned/herdr.sock",
		workspaceID: "w1", paneID: "w1:p1", cwd: "/repo/child",
	}
	intent := state.LaunchIntent{
		Kind: state.IntentWorktree, Status: state.IntentRealized,
		Session: "owned-session", SocketPath: "/owned/herdr.sock",
		Resource: state.RuntimeResource{
			WorkspaceID: "w1", PaneID: "w1:p1", CurrentPath: "/repo/child",
		},
		Launch: &state.LaunchCapsule{Nonce: strings.Repeat("a", 32)},
	}
	got, found := matchingPaneLaunchIntent(state.LaunchJournal{Intents: []state.LaunchIntent{intent}}, request)
	if !found || got.Resource.PaneID != "w1:p1" {
		t.Fatalf("match = (%+v, %t)", got, found)
	}
	request.cwd = filepath.Clean("/repo/other")
	if _, found := matchingPaneLaunchIntent(state.LaunchJournal{Intents: []state.LaunchIntent{intent}}, request); found {
		t.Fatal("mismatched cwd was adopted")
	}
	request.cwd = "/repo/child"
	request.socketPath = "/foreign/herdr.sock"
	if _, found := matchingPaneLaunchIntent(state.LaunchJournal{Intents: []state.LaunchIntent{intent}}, request); found {
		t.Fatal("mismatched owned route was adopted")
	}
}

func TestMatchingPaneLaunchIntentAcceptsRealizedCoordinatorWithoutAgentLaunch(t *testing.T) {
	request := paneLauncherRequest{
		session: "owned-session", socketPath: "/owned/herdr.sock",
		workspaceID: "w1", paneID: "w1:p1", cwd: "/repo",
	}
	intent := state.LaunchIntent{
		Kind: state.IntentCoordinator, Status: state.IntentRealized,
		Session: "owned-session", SocketPath: "/owned/herdr.sock",
		Resource: state.RuntimeResource{
			WorkspaceID: "w1", PaneID: "w1:p1", CurrentPath: "/repo",
		},
	}
	if _, found := matchingPaneLaunchIntent(state.LaunchJournal{Intents: []state.LaunchIntent{intent}}, request); !found {
		t.Fatal("realized coordinator was not assigned to its launcher")
	}
	intent.Launch = &state.LaunchCapsule{Nonce: strings.Repeat("a", 32)}
	if _, found := matchingPaneLaunchIntent(state.LaunchJournal{Intents: []state.LaunchIntent{intent}}, request); !found {
		t.Fatal("launch-bearing coordinator was not assigned to its launcher")
	}
	intent.Kind = state.IntentRollback
	if _, found := matchingPaneLaunchIntent(state.LaunchJournal{Intents: []state.LaunchIntent{intent}}, request); found {
		t.Fatal("rollback intent was assigned to a pane launcher")
	}
}

func TestCoordinatorLauncherWaitsWithoutStartingAShell(t *testing.T) {
	reader, writer := io.Pipe()
	done := make(chan int)
	go func() { done <- holdCoordinatorLauncher(reader, io.Discard) }()
	select {
	case code := <-done:
		t.Fatalf("coordinator launcher exited early with %d", code)
	case <-time.After(10 * time.Millisecond):
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 0 {
		t.Fatalf("coordinator launcher exit = %d, want 0 after EOF", code)
	}
}

func TestValidateWorktreeRemoveResponseRequiresExactNonForceResult(t *testing.T) {
	valid := []byte(`{"id":"cli:worktree:remove","result":{"type":"worktree_removed","workspace_id":"w2","path":"/repo/child","forced":false}}`)
	if err := validateWorktreeRemoveResponse(valid, "w2", "/repo/child"); err != nil {
		t.Fatal(err)
	}
	forced := []byte(`{"id":"cli:worktree:remove","result":{"type":"worktree_removed","workspace_id":"w2","path":"/repo/child","forced":true}}`)
	if err := validateWorktreeRemoveResponse(forced, "w2", "/repo/child"); err == nil {
		t.Fatal("forced remove result was accepted")
	}
	if err := validateWorktreeRemoveResponse(valid, "w3", "/repo/child"); err == nil {
		t.Fatal("foreign workspace result was accepted")
	}
}

func TestOwnedCleanupMutationsClassifyPreDispatchFailures(t *testing.T) {
	var session *OwnedSession
	for name, mutation := range map[string]func() error{
		"remove nil session": func() error {
			return session.RemoveWorktree(context.Background(), "w2", "/repo/child")
		},
		"close nil session": func() error {
			return session.CloseWorkspace(context.Background(), "w2")
		},
		"remove incomplete identity": func() error {
			return (&OwnedSession{}).RemoveWorktree(context.Background(), "", "/repo/child")
		},
		"close incomplete identity": func() error {
			return (&OwnedSession{}).CloseWorkspace(context.Background(), "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutation(); !errors.Is(err, corebackend.ErrMutationNotIssued) {
				t.Fatalf("error = %v, want corebackend.ErrMutationNotIssued", err)
			}
		})
	}
}

func TestOwnedCloseWorkspaceClassifiesStderrRejection(t *testing.T) {
	h := newOwnedHarness(t)
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"workspace", "close", "w2"}) {
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
		return failedHerdrCommand(t,
			`{"id":"cli:workspace:close","error":{"code":"workspace_not_empty","message":"workspace still has panes"}}`,
		)
	}

	err := h.session.CloseWorkspace(context.Background(), "w2")
	rejected, ok := errors.AsType[corebackend.MutationRejectedError](err)
	if !ok || rejected.Code != "workspace_not_empty" || rejected.Message != "workspace still has panes" {
		t.Fatalf("rejection = (%+v,%t), want decoded workspace close rejection", rejected, ok)
	}
}

func TestOwnedCloseWorkspaceFallsBackToStdoutRejection(t *testing.T) {
	h := newOwnedHarness(t)
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"workspace", "close", "w2"}) {
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
		return []byte(`{"id":"cli:workspace:close","error":{"code":"workspace_not_empty","message":"workspace still has panes"}}`),
			errors.New("exit status 1")
	}

	err := h.session.CloseWorkspace(context.Background(), "w2")
	rejected, ok := errors.AsType[corebackend.MutationRejectedError](err)
	if !ok || rejected.Code != "workspace_not_empty" || rejected.Message != "workspace still has panes" {
		t.Fatalf("rejection = (%+v,%t), want decoded stdout fallback", rejected, ok)
	}
}

func TestOwnedCloseWorkspacePreservesCommandErrorWithoutEnvelope(t *testing.T) {
	h := newOwnedHarness(t)
	out, commandErr := failedHerdrCommand(t, "herdr: transport failed")
	h.fake.respond = func(args []string) ([]byte, error) {
		if !slices.Equal(args, []string{"workspace", "close", "w2"}) {
			return nil, fmt.Errorf("unexpected mutation args %v", args)
		}
		return out, commandErr
	}

	if err := h.session.CloseWorkspace(context.Background(), "w2"); !errors.Is(err, commandErr) {
		t.Fatalf("CloseWorkspace() error = %v, want original command error %v", err, commandErr)
	}
}

// failedHerdrCommand reproduces cmd.Output's stdout/stderr split and the
// runBoundedCommand wrapper while retaining the concrete *exec.ExitError.
func failedHerdrCommand(t *testing.T, stderr string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `printf '%s' "$1" >&2; exit 1`, "herdr-test", stderr)
	out, err := cmd.Output()
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		t.Fatalf("failedHerdrCommand() error type = %T, want *exec.ExitError", err)
	}
	if len(out) != 0 || string(exitErr.Stderr) != stderr {
		t.Fatalf("failedHerdrCommand() = (%q,%q), want empty stdout and stderr %q", out, exitErr.Stderr, stderr)
	}
	return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
}

func TestWaitForLaunchTokenRequiresExactInput(t *testing.T) {
	intent := state.LaunchIntent{
		ExpiresUnixMS: time.Now().Add(time.Second).UnixMilli(),
		Launch:        &state.LaunchCapsule{Nonce: strings.Repeat("b", 32)},
	}
	input := strings.NewReader(launcherStartToken(intent.Launch.Nonce) + "\n")
	if err := waitForLaunchToken(input, io.Discard, intent); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForLaunchTokenRejectsUnexpectedInput(t *testing.T) {
	intent := state.LaunchIntent{
		ExpiresUnixMS: time.Now().Add(time.Second).UnixMilli(),
		Launch:        &state.LaunchCapsule{Nonce: strings.Repeat("b", 32)},
	}
	err := waitForLaunchToken(strings.NewReader("wrong\n"), io.Discard, intent)
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("error = %v, want unexpected-input rejection", err)
	}
}

func TestWaitForLaunchTokenResendsReadyMarker(t *testing.T) {
	intent := state.LaunchIntent{
		ExpiresUnixMS: time.Now().Add(time.Second).UnixMilli(),
		Launch:        &state.LaunchCapsule{Nonce: strings.Repeat("b", 32)},
	}
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close() // The test no longer needs the pipe during cleanup.
	})
	t.Cleanup(func() {
		_ = writer.Close() // The test no longer needs the pipe during cleanup.
	})
	var output strings.Builder
	done := make(chan error)
	go func() {
		done <- waitForLaunchTokenAtInterval(reader, &output, intent, time.Millisecond)
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := fmt.Fprintln(writer, launcherStartToken(intent.Launch.Nonce)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), launcherReadyMarker(intent.Launch.Nonce)) {
		t.Fatalf("output = %q, want ready marker", output.String())
	}
}

func TestOwnedConfigPinsNonLoginLauncher(t *testing.T) {
	got := string(ownedConfigContents("/owned/fanout"))
	for _, want := range []string{
		"default_shell = \"/owned/fanout\"", "shell_mode = \"non_login\"",
		"resume_agents_on_restore = false", "manifest_check = false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config %q does not contain %q", got, want)
		}
	}
}
