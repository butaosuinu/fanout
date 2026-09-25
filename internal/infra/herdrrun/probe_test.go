package herdrrun

import (
	"errors"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCheckAvailablePinsVerifiedSocketAndVersion(t *testing.T) {
	t.Setenv(sessionEnv, "ambient-wrong-session")
	t.Setenv(socketEnv, "/tmp/ambient-wrong.sock")
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)

	if err := b.CheckAvailable(); err != nil {
		t.Fatalf("CheckAvailable() error = %v", err)
	}
	if len(fake.commands) != 2 {
		t.Fatalf("command count = %d, want 2", len(fake.commands))
	}
	if got := []string{
		commandKey(fake.commands[0].args),
		commandKey(fake.commands[1].args),
	}; !slices.Equal(got, []string{"version", "status"}) {
		t.Fatalf("commands = %v, want version/status", got)
	}
	for _, call := range fake.commands {
		if slices.Contains(call.args, "--session") {
			t.Fatalf("verified-socket call unexpectedly used --session: %v", call.args)
		}
		if got, ok := envValue(call.env, sessionEnv); !ok || got != session {
			t.Fatalf("%v %s = %q (present=%v), want %q", call.args, sessionEnv, got, ok, session)
		}
		if got, ok := envValue(call.env, socketEnv); !ok || got != socket {
			t.Fatalf("%v %s = %q (present=%v), want %q", call.args, socketEnv, got, ok, socket)
		}
	}
}

func TestCheckAvailableResolvesNamedSessionThenPinsReturnedSocket(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, "", fake)

	if err := b.CheckAvailable(); err != nil {
		t.Fatalf("CheckAvailable() error = %v", err)
	}
	status := fake.commands[1]
	if !slices.Equal(status.args, []string{"--session", session, "status", "--json"}) {
		t.Fatalf("status args = %v", status.args)
	}
	if _, ok := envValue(status.env, socketEnv); ok {
		t.Fatalf("initial status env contains %s", socketEnv)
	}
	if err := b.CheckAvailable(); err != nil {
		t.Fatalf("second CheckAvailable() error = %v", err)
	}
	secondStatus := fake.commands[3]
	if slices.Contains(secondStatus.args, "--session") {
		t.Fatalf("second status args unexpectedly use --session: %v", secondStatus.args)
	}
	if got, ok := envValue(secondStatus.env, socketEnv); !ok || got != socket {
		t.Fatalf("second status %s = %q (present=%v), want %q", socketEnv, got, ok, socket)
	}
}

func TestCheckAvailableFailsClosed(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name    string
		mutate  func(*fakeHerdr)
		wantErr string
	}{
		{
			name: "version below floor",
			mutate: func(fake *fakeHerdr) {
				fake.version = "herdr 0.7.4\n"
			},
			wantErr: "below floor 0.7.5",
		},
		{
			name: "prerelease version",
			mutate: func(fake *fakeHerdr) {
				fake.version = "herdr 0.7.6-preview.1\n"
			},
			wantErr: "required: stable >=0.7.5",
		},
		{
			name: "preview channel",
			mutate: func(fake *fakeHerdr) {
				fake.status = strings.Replace(fake.status, `"channel":"stable"`, `"channel":"preview"`, 1)
			},
			wantErr: "unsupported herdr client version",
		},
		{
			name: "server version mismatch",
			mutate: func(fake *fakeHerdr) {
				fake.status = strings.Replace(fake.status, `"server":{"status":"running","running":true,"version":"0.7.5"`, `"server":{"status":"running","running":true,"version":"0.7.6"`, 1)
			},
			wantErr: "unsupported herdr server version",
		},
		{
			name: "server not running",
			mutate: func(fake *fakeHerdr) {
				fake.status = strings.Replace(fake.status, `"status":"running","running":true`, `"status":"not_running","running":false`, 1)
			},
			wantErr: "is not running",
		},
		{
			name: "session mismatch",
			mutate: func(fake *fakeHerdr) {
				fake.status = strings.Replace(fake.status, `"session":"fanout-test"`, `"session":"other"`, 1)
			},
			wantErr: "client session",
		},
		{
			name: "socket mismatch",
			mutate: func(fake *fakeHerdr) {
				fake.status = strings.Replace(fake.status, strconv.Quote(socket), strconv.Quote("/private/tmp/other/herdr.sock"), 1)
			},
			wantErr: "status socket",
		},
		{
			name: "restart required",
			mutate: func(fake *fakeHerdr) {
				fake.status = strings.Replace(fake.status, `"restart_needed":false`, `"restart_needed":true`, 1)
			},
			wantErr: "requires a client/server restart",
		},
		{
			name: "trailing status document",
			mutate: func(fake *fakeHerdr) {
				fake.status += `{}`
			},
			wantErr: "unexpected trailing JSON value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			tt.mutate(fake)
			b := newTestBackend(t, session, socket, fake)
			err := b.CheckAvailable()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CheckAvailable() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckAvailableAcceptsHigherStableVersionWithoutCapabilityPreflight(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.version = "herdr 0.8.0\n"
	fake.status = strings.ReplaceAll(fake.status, "0.7.5", "0.8.0")
	b := newTestBackend(t, session, socket, fake)

	if err := b.CheckAvailable(); err != nil {
		t.Fatalf("CheckAvailable() error = %v", err)
	}
	if len(fake.commands) != 2 ||
		commandKey(fake.commands[0].args) != "version" ||
		commandKey(fake.commands[1].args) != "status" {
		t.Fatalf("CheckAvailable() commands = %#v, want version/status only", fake.commands)
	}
}

func TestCheckAvailableRejectsMissingBinaryAndUnnamedSession(t *testing.T) {
	b := New("", "")
	if err := b.CheckAvailable(); err == nil || !strings.Contains(err.Error(), "named session") {
		t.Fatalf("CheckAvailable() unnamed-session error = %v", err)
	}

	b = New("fanout-test", "/private/tmp/fanout-test/herdr.sock")
	b.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if err := b.CheckAvailable(); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("CheckAvailable() missing-binary error = %v, want exec.ErrNotFound", err)
	}
}

func TestPreviewCheckAvailableRequiresOnlyStableCLI(t *testing.T) {
	fake := newFakeHerdr("", "")
	b := NewPreview()
	b.lookPath = func(string) (string, error) { return "/private/tmp/herdr-0.7.5", nil }
	b.stageBinary = func(string) (string, string, error) {
		t.Fatal("preview staged the Herdr binary")
		return "", "", nil
	}
	b.output = fake.output
	if err := b.CheckAvailable(); err != nil {
		t.Fatal(err)
	}
	if len(fake.commands) != 1 || commandKey(fake.commands[0].args) != "version" {
		t.Fatalf("preview commands = %#v, want version only", fake.commands)
	}
	if got, present := envValue(fake.commands[0].env, sessionEnv); present {
		t.Fatalf("preview %s = %q, want absent", sessionEnv, got)
	}
}
