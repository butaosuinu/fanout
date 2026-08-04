package herdrrun

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestWorkloadEnvironmentRemovesControlPlaneAndForcesBackend(t *testing.T) {
	caller := []string{
		"PATH=/caller/bin", "KEEP=value", "HERDR_SESSION=foreign",
		"FANOUT_HERDR_CONTROL_PATH=/foreign", "TMUX=/tmp/tmux", "TMUX_PANE=%1",
		"TMUX_TMPDIR=/tmp", "FANOUT_STATE_PATH=/foreign/state",
		"FANOUT_BACKEND=tmux", "FANOUT_BIN=/foreign/fanout",
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
	got, err := consumeWorkloadEnvironment(&state.HerdrLaunch{
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

func TestConsumeWorkloadEnvironmentRejectsPathOutsideOwnedRuntime(t *testing.T) {
	runtimeDir := t.TempDir()
	launch := &state.HerdrLaunch{
		Nonce: strings.Repeat("a", 32), EnvFilePath: filepath.Join(t.TempDir(), "capsule.json"), EnvNameCount: 1,
	}
	if _, err := consumeWorkloadEnvironment(launch, runtimeDir); err == nil ||
		!strings.Contains(err.Error(), "outside") {
		t.Fatalf("error = %v, want outside-owned-runtime rejection", err)
	}
}

func TestMatchingPaneLaunchIntentRequiresExactWorkspacePaneAndCWD(t *testing.T) {
	request := paneLauncherRequest{
		session: "owned-session", socketPath: "/owned/herdr.sock",
		workspaceID: "w1", paneID: "w1:p1", cwd: "/repo/child",
	}
	intent := state.HerdrIntent{
		Status:  state.HerdrIntentRealized,
		Session: "owned-session", SocketPath: "/owned/herdr.sock",
		Resource: state.HerdrResource{
			WorkspaceID: "w1", PaneID: "w1:p1", CurrentPath: "/repo/child",
		},
		Launch: &state.HerdrLaunch{Nonce: strings.Repeat("a", 32)},
	}
	got, found := matchingPaneLaunchIntent(state.HerdrIntents{Intents: []state.HerdrIntent{intent}}, request)
	if !found || got.Resource.PaneID != "w1:p1" {
		t.Fatalf("match = (%+v, %t)", got, found)
	}
	request.cwd = filepath.Clean("/repo/other")
	if _, found := matchingPaneLaunchIntent(state.HerdrIntents{Intents: []state.HerdrIntent{intent}}, request); found {
		t.Fatal("mismatched cwd was adopted")
	}
	request.cwd = "/repo/child"
	request.socketPath = "/foreign/herdr.sock"
	if _, found := matchingPaneLaunchIntent(state.HerdrIntents{Intents: []state.HerdrIntent{intent}}, request); found {
		t.Fatal("mismatched owned route was adopted")
	}
}

func TestWaitForLaunchTokenRequiresExactInput(t *testing.T) {
	intent := state.HerdrIntent{
		ExpiresUnixMS: time.Now().Add(time.Second).UnixMilli(),
		Launch:        &state.HerdrLaunch{Nonce: strings.Repeat("b", 32)},
	}
	input := strings.NewReader(launcherStartToken(intent.Launch.Nonce) + "\n")
	if err := waitForLaunchToken(input, io.Discard, intent); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForLaunchTokenRejectsUnexpectedInput(t *testing.T) {
	intent := state.HerdrIntent{
		ExpiresUnixMS: time.Now().Add(time.Second).UnixMilli(),
		Launch:        &state.HerdrLaunch{Nonce: strings.Repeat("b", 32)},
	}
	err := waitForLaunchToken(strings.NewReader("wrong\n"), io.Discard, intent)
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("error = %v, want unexpected-input rejection", err)
	}
}

func TestWaitForLaunchTokenResendsReadyMarker(t *testing.T) {
	intent := state.HerdrIntent{
		ExpiresUnixMS: time.Now().Add(time.Second).UnixMilli(),
		Launch:        &state.HerdrLaunch{Nonce: strings.Repeat("b", 32)},
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
		"default_shell = \"/owned/fanout\"", "shell_mode = \"non_login\"", "manifest_check = false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config %q does not contain %q", got, want)
		}
	}
}
