package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type recordedCommand struct {
	args        []string
	env         []string
	timeout     time.Duration
	hasDeadline bool
}

type fakeSnapshotResult struct {
	output string
	err    error
}

type fakeHerdr struct {
	commands        []recordedCommand
	version         string
	status          string
	schema          string
	snapshot        string
	errors          map[string]error
	snapshotResults []fakeSnapshotResult
	snapshotCall    int
	intercept       func(context.Context, string) error
}

func (f *fakeHerdr) output(ctx context.Context, _ string, env []string, args ...string) ([]byte, error) {
	call := recordedCommand{
		args: slices.Clone(args),
		env:  slices.Clone(env),
	}
	if deadline, ok := ctx.Deadline(); ok {
		call.timeout = time.Until(deadline)
		call.hasDeadline = true
	}
	f.commands = append(f.commands, call)
	key := commandKey(args)
	if f.intercept != nil {
		if err := f.intercept(ctx, key); err != nil {
			return nil, err
		}
	}
	if key == "snapshot" && f.snapshotCall < len(f.snapshotResults) {
		result := f.snapshotResults[f.snapshotCall]
		f.snapshotCall++
		return []byte(result.output), result.err
	}
	if err := f.errors[key]; err != nil {
		return nil, err
	}
	switch key {
	case "version":
		return []byte(f.version), nil
	case "status":
		return []byte(f.status), nil
	case "schema":
		return []byte(f.schema), nil
	case "snapshot":
		return []byte(f.snapshot), nil
	default:
		return nil, fmt.Errorf("unexpected herdr args: %v", args)
	}
}

func commandKey(args []string) string {
	switch {
	case slices.Equal(args, []string{"--version"}):
		return "version"
	case hasSuffix(args, "status", "--json"):
		return "status"
	case hasSuffix(args, "api", "schema", "--json"):
		return "schema"
	case hasSuffix(args, "api", "snapshot"):
		return "snapshot"
	default:
		return ""
	}
}

func hasSuffix(got []string, want ...string) bool {
	return len(got) >= len(want) && slices.Equal(got[len(got)-len(want):], want)
}

func newFakeHerdr(session, socket string) *fakeHerdr {
	return &fakeHerdr{
		version:  "herdr 0.7.3\n",
		status:   validStatus(session, socket),
		schema:   `{"protocol":16,"schema_version":1}` + "\n",
		snapshot: validSnapshot(),
		errors:   map[string]error{},
	}
}

func newTestBackend(t *testing.T, session, socket string, fake *fakeHerdr) *Backend {
	t.Helper()
	b := New(session, socket)
	b.lookPath = func(name string) (string, error) {
		if name != commandName {
			t.Fatalf("LookPath(%q), want %q", name, commandName)
		}
		return "/private/tmp/herdr-0.7.3", nil
	}
	b.output = fake.output
	return b
}

type fakeWaitClock struct {
	now    time.Time
	sleeps []time.Duration
}

const (
	runCommandHelperModeEnv    = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_MODE"
	runCommandHelperPIDsEnv    = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_PIDS"
	runCommandHelperReadyEnv   = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_READY"
	runCommandHelperReleaseEnv = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_RELEASE"
	runCommandHelperLockEnv    = "FANOUT_TEST_HERDR_RUN_COMMAND_HELPER_LOCK"
)

type runCommandHelperPIDs struct {
	direct     int
	descendant int
	group      int
}

func installFakeWaitClock(b *Backend) *fakeWaitClock {
	clock := &fakeWaitClock{now: time.Unix(1_700_000_000, 0)}
	b.now = func() time.Time { return clock.now }
	b.sleep = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clock.sleeps = append(clock.sleeps, delay)
		clock.now = clock.now.Add(delay)
		return nil
	}
	return clock
}

func assertCommandTimeout(t *testing.T, call recordedCommand, want time.Duration) {
	t.Helper()
	if !call.hasDeadline {
		t.Fatalf("%v has no context deadline", call.args)
	}
	const schedulingSlack = 500 * time.Millisecond
	if call.timeout <= want-schedulingSlack || call.timeout > want+10*time.Millisecond {
		t.Fatalf("%v context timeout = %s, want approximately %s", call.args, call.timeout, want)
	}
}

func envWithValue(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, prefix+value)
}

func waitForRunCommandHelperPIDs(path string, timeout time.Duration) (runCommandHelperPIDs, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) != 3 {
				lastErr = fmt.Errorf("helper pid file has %d fields, want 3", len(fields))
			} else {
				values := make([]int, len(fields))
				for i, field := range fields {
					values[i], err = strconv.Atoi(field)
					if err != nil {
						lastErr = fmt.Errorf("parse helper pid %q: %w", field, err)
						break
					}
				}
				if err == nil {
					return runCommandHelperPIDs{direct: values[0], descendant: values[1], group: values[2]}, nil
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("helper pid file was not created")
	}
	return runCommandHelperPIDs{}, lastErr
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("file %q was not created", path)
}

func waitForProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitForHelperLockRelease(path string, timeout time.Duration) (returnErr error) {
	lockFile, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, lockFile.Close())
	}()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		flockErr := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			return syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		}
		if !errors.Is(flockErr, syscall.EWOULDBLOCK) && !errors.Is(flockErr, syscall.EAGAIN) {
			return flockErr
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("descendant still holds helper lock %q", path)
}

func killRunCommandHelper(pids runCommandHelperPIDs) error {
	if pids.group > 1 && pids.group != syscall.Getpgrp() {
		err := syscall.Kill(-pids.group, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if pids.descendant <= 1 || pids.descendant == os.Getpid() {
		return fmt.Errorf("refusing to kill unsafe helper pids %#v", pids)
	}
	err := syscall.Kill(pids.descendant, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func validStatus(session, socket string) string {
	return fmt.Sprintf(`{
  "client":{"version":"0.7.3","channel":"stable","protocol":16,"binary":"/private/tmp/herdr-0.7.3","session":%s},
  "server":{"status":"running","running":true,"version":"0.7.3","protocol":16,"capabilities":{"live_handoff":true,"detached_server_daemon":true},"compatible":true,"socket":%s,"session":%s,"restart_needed":false},
  "update":{"restart_needed":false}
}`+"\n", strconv.Quote(session), strconv.Quote(socket), strconv.Quote(session))
}

func validSnapshot() string {
	return `{
  "id":"cli:api:snapshot",
  "result":{
    "type":"session_snapshot",
    "snapshot":{
      "version":"0.7.3",
      "protocol":16,
      "workspaces":[
        {"workspace_id":"w1","number":1,"label":"root","focused":true,"pane_count":1,"tab_count":1,"active_tab_id":"w1:t1","agent_status":"unknown"},
        {"workspace_id":"w2","number":2,"label":"child","focused":false,"pane_count":1,"tab_count":1,"active_tab_id":"w2:t1","agent_status":"working","worktree":{"repo_key":"/repo/.git","repo_name":"repo","repo_root":"/repo","checkout_path":"/repo/.fanout/worktrees/child","is_linked_worktree":true}}
      ],
      "tabs":[],
      "panes":[
        {"pane_id":"w1:p1","terminal_id":"term-root","workspace_id":"w1","tab_id":"w1:t1","focused":true,"cwd":"/repo","foreground_cwd":"/tmp/foreground","agent_status":"unknown","revision":1},
        {"pane_id":"w2:p1","terminal_id":"term-child","workspace_id":"w2","tab_id":"w2:t1","focused":false,"cwd":"/wrong-saved-cwd","foreground_cwd":"/tmp/other-foreground","title":"child title","agent":"codex","agent_status":"working","revision":2}
      ],
      "layouts":[],
      "agents":[
        {"terminal_id":"term-child","name":"fanout-child","agent":"codex","agent_status":"working","workspace_id":"w2","tab_id":"w2:t1","pane_id":"w2:p1","focused":false,"cwd":"/wrong-saved-cwd","foreground_cwd":"/tmp/other-foreground","revision":2}
      ]
    }
  }
}` + "\n"
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value, true
		}
	}
	return "", false
}

func TestName(t *testing.T) {
	b := New("fanout-test", "/tmp/herdr.sock")
	if got := b.Name(); got != corebackend.Herdr {
		t.Fatalf("Name() = %q, want %q", got, corebackend.Herdr)
	}
}

func TestCheckAvailablePinsVerifiedSocketAndExactTuple(t *testing.T) {
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
	if len(fake.commands) != 3 {
		t.Fatalf("command count = %d, want 3", len(fake.commands))
	}
	if got := []string{
		commandKey(fake.commands[0].args),
		commandKey(fake.commands[1].args),
		commandKey(fake.commands[2].args),
	}; !slices.Equal(got, []string{"version", "status", "schema"}) {
		t.Fatalf("commands = %v, want version/status/schema", got)
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
	schema := fake.commands[2]
	if slices.Contains(schema.args, "--session") {
		t.Fatalf("schema args unexpectedly use --session: %v", schema.args)
	}
	if got, ok := envValue(schema.env, socketEnv); !ok || got != socket {
		t.Fatalf("schema %s = %q (present=%v), want %q", socketEnv, got, ok, socket)
	}

	if err := b.CheckAvailable(); err != nil {
		t.Fatalf("second CheckAvailable() error = %v", err)
	}
	secondStatus := fake.commands[4]
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
			name: "future CLI version",
			mutate: func(fake *fakeHerdr) {
				fake.version = "herdr 0.7.4\n"
			},
			wantErr: "unsupported herdr CLI version",
		},
		{
			name: "preview channel",
			mutate: func(fake *fakeHerdr) {
				fake.status = strings.Replace(fake.status, `"channel":"stable"`, `"channel":"preview"`, 1)
			},
			wantErr: "unsupported herdr client tuple",
		},
		{
			name: "future server with same protocol",
			mutate: func(fake *fakeHerdr) {
				fake.status = strings.Replace(fake.status, `"server":{"status":"running","running":true,"version":"0.7.3"`, `"server":{"status":"running","running":true,"version":"0.7.4"`, 1)
			},
			wantErr: "unsupported herdr server tuple",
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
			name: "future schema",
			mutate: func(fake *fakeHerdr) {
				fake.schema = `{"protocol":16,"schema_version":2}`
			},
			wantErr: "unsupported herdr API tuple",
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

func TestListLiveProjectsSnapshotWithoutUsingForegroundCWD(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)

	got, err := b.ListLive()
	if err != nil {
		t.Fatalf("ListLive() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(ListLive()) = %d, want 2", len(got))
	}
	root := got[0]
	if root.Ref != (corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1"}) {
		t.Fatalf("root Ref = %#v", root.Ref)
	}
	if !root.FocusKnown || !root.Focused {
		t.Fatalf("root focus projection = known:%t focused:%t, want true/true", root.FocusKnown, root.Focused)
	}
	if root.CurrentPath != "/repo" || root.NativeAgentState != "unknown" || root.AgentState != "" || root.TerminalID != "term-root" || root.SessionID != session || root.SocketPath != socket {
		t.Fatalf("root live pane = %#v", root)
	}
	child := got[1]
	if !child.FocusKnown || child.Focused {
		t.Fatalf("child focus projection = known:%t focused:%t, want true/false", child.FocusKnown, child.Focused)
	}
	if child.CurrentPath != "/repo/.fanout/worktrees/child" {
		t.Fatalf("child CurrentPath = %q, want worktree checkout path", child.CurrentPath)
	}
	if child.ProjectRoot != "/repo" || child.WorktreePath != "/repo/.fanout/worktrees/child" {
		t.Fatalf("child worktree projection = %#v", child)
	}
	if child.AgentState != "" || child.NativeAgentState != "working" || child.AgentID != "fanout-child" || child.Title != "child title" || child.SocketPath != socket {
		t.Fatalf("child agent projection = %#v", child)
	}
	if gotCalls := len(fake.commands); gotCalls != 4 || commandKey(fake.commands[3].args) != "snapshot" {
		t.Fatalf("ListLive() calls = %#v", fake.commands)
	}
}

func TestListLiveRejectsMalformedOrIncompatibleSnapshot(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{
			name: "unexpected envelope id",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"id":"cli:api:snapshot"`, `"id":"other"`, 1)
			},
			wantErr: "unexpected herdr snapshot envelope",
		},
		{
			name: "future version",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"version":"0.7.3"`, `"version":"0.7.4"`, 1)
			},
			wantErr: "unsupported herdr snapshot tuple",
		},
		{
			name: "missing required agents collection",
			mutate: func(snapshot string) string {
				prefix, _, ok := strings.Cut(snapshot, `      "agents":[`)
				if !ok {
					t.Fatal("snapshot fixture is missing agents collection")
				}
				return prefix + "      \"unused\":[]\n    }\n  }\n}\n"
			},
			wantErr: "missing a required collection",
		},
		{
			name: "pane missing required focused field",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"tab_id":"w1:t1","focused":true,"cwd":"/repo"`, `"tab_id":"w1:t1","cwd":"/repo"`, 1)
			},
			wantErr: "pane with incomplete identity",
		},
		{
			name: "pane missing required revision field",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `,"revision":1`, ``, 1)
			},
			wantErr: "pane with incomplete identity",
		},
		{
			name: "duplicate pane id",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"pane_id":"w2:p1"`, `"pane_id":"w1:p1"`, 1)
			},
			wantErr: "duplicate pane id",
		},
		{
			name: "unknown native state",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"title":"child title","agent":"codex","agent_status":"working"`, `"title":"child title","agent":"codex","agent_status":"future"`, 1)
			},
			wantErr: "unknown agent status",
		},
		{
			name: "agent disagrees with pane",
			mutate: func(snapshot string) string {
				return strings.Replace(snapshot, `"terminal_id":"term-child","name":"fanout-child"`, `"terminal_id":"other-terminal","name":"fanout-child"`, 1)
			},
			wantErr: "agent identity disagrees",
		},
		{
			name: "agent session ref disagrees with pane",
			mutate: func(snapshot string) string {
				withPaneRef := strings.Replace(snapshot, `"title":"child title","agent":"codex"`, `"title":"child title","agent":"codex","agent_session":{"source":"codex","agent":"codex","kind":"id","value":"session-a"}`, 1)
				return strings.Replace(withPaneRef, `"terminal_id":"term-child","name":"fanout-child","agent":"codex"`, `"terminal_id":"term-child","name":"fanout-child","agent":"codex","agent_session":{"source":"codex","agent":"codex","kind":"id","value":"session-b"}`, 1)
			},
			wantErr: "agent session ref disagrees",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			fake.snapshot = tt.mutate(fake.snapshot)
			b := newTestBackend(t, session, socket, fake)
			_, err := b.ListLive()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ListLive() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestWaitStatusValues(t *testing.T) {
	got := []WaitStatus{WaitMatched, WaitTimedOut, WaitCancelled, WaitFailed}
	want := []WaitStatus{"matched", "timed_out", "cancelled", "failed"} //nolint:misspell // Herdr contract spells the terminal result "cancelled".
	if !slices.Equal(got, want) {
		t.Fatalf("wait statuses = %q, want %q", got, want)
	}
}

func TestWaitRejectsInvalidInputsWithoutInvokingHerdr(t *testing.T) {
	validMatch := func([]corebackend.LivePane) bool { return true }
	tests := []struct {
		name    string
		ctx     context.Context
		timeout time.Duration
		match   func([]corebackend.LivePane) bool
		wantErr string
	}{
		{
			name:    "nil context",
			ctx:     nil,
			timeout: minimumWaitTimeout,
			match:   validMatch,
			wantErr: "requires a context",
		},
		{
			name:    "nil predicate",
			ctx:     context.Background(),
			timeout: minimumWaitTimeout,
			match:   nil,
			wantErr: "requires a snapshot predicate",
		},
		{
			name:    "negative timeout",
			ctx:     context.Background(),
			timeout: -time.Second,
			match:   validMatch,
			wantErr: "whole number of seconds at least 3",
		},
		{
			name:    "timeout below minimum",
			ctx:     context.Background(),
			timeout: 2 * time.Second,
			match:   validMatch,
			wantErr: "whole number of seconds at least 3",
		},
		{
			name:    "fractional timeout",
			ctx:     context.Background(),
			timeout: minimumWaitTimeout + time.Nanosecond,
			match:   validMatch,
			wantErr: "whole number of seconds at least 3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr("fanout-test", "/private/tmp/fanout-test/herdr.sock")
			b := newTestBackend(t, "fanout-test", "/private/tmp/fanout-test/herdr.sock", fake)

			got := b.Wait(tt.ctx, tt.timeout, tt.match)

			if got.Status != WaitFailed || got.Err == nil || !strings.Contains(got.Err.Error(), tt.wantErr) || got.Panes != nil {
				t.Fatalf("Wait() = %#v, want failed with nil panes and error containing %q", got, tt.wantErr)
			}
			if len(fake.commands) != 0 {
				t.Fatalf("invalid Wait() invoked herdr: %#v", fake.commands)
			}
		})
	}
}

func TestWaitImmediateMatchUsesVerifiedSocket(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 5*time.Second, func(panes []corebackend.LivePane) bool {
		matchCalls++
		matched := len(panes) == 2 && panes[0].FocusKnown && panes[0].Focused && panes[1].FocusKnown && !panes[1].Focused
		panes[0] = corebackend.LivePane{}
		panes[1].Title = "mutated by predicate"
		return matched
	})

	if got.Status != WaitMatched || got.Err != nil || len(got.Panes) != 2 {
		t.Fatalf("Wait() = %#v, want matched with two panes and no error", got)
	}
	if got.Panes[0].Ref.Pane != "w1:p1" || got.Panes[1].Title != "child title" {
		t.Fatalf("matched panes were mutated through predicate slice: %#v", got.Panes)
	}
	if matchCalls != 1 {
		t.Fatalf("predicate calls = %d, want 1", matchCalls)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("immediate match sleeps = %v, want none", clock.sleeps)
	}
	wantCommands := []string{"version", "status", "schema", "snapshot"}
	if len(fake.commands) != len(wantCommands) {
		t.Fatalf("command count = %d, want %d", len(fake.commands), len(wantCommands))
	}
	for i, call := range fake.commands {
		if key := commandKey(call.args); key != wantCommands[i] {
			t.Fatalf("command %d = %q (%v), want %q", i, key, call.args, wantCommands[i])
		}
		if slices.Contains(call.args, "--session") {
			t.Fatalf("verified-socket command unexpectedly used --session: %v", call.args)
		}
		if gotSocket, ok := envValue(call.env, socketEnv); !ok || gotSocket != socket {
			t.Fatalf("%v %s = %q (present=%t), want %q", call.args, socketEnv, gotSocket, ok, socket)
		}
	}
}

func TestWaitSnapshotCallLimitsIntervalsAndCommandTimeouts(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name          string
		totalTimeout  time.Duration
		budget        time.Duration
		wantSnapshots int
	}{
		{name: "3 seconds", totalTimeout: 3 * time.Second, budget: 3 * time.Second, wantSnapshots: 2},
		{name: "4 seconds", totalTimeout: 4 * time.Second, budget: 4 * time.Second, wantSnapshots: 2},
		{name: "5 seconds", totalTimeout: 5 * time.Second, budget: 5 * time.Second, wantSnapshots: 3},
		{name: "default 300 seconds", totalTimeout: 0, budget: 300 * time.Second, wantSnapshots: 150},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			b := newTestBackend(t, session, socket, fake)
			clock := installFakeWaitClock(b)
			matchCalls := 0

			got := b.Wait(context.Background(), tt.totalTimeout, func([]corebackend.LivePane) bool {
				matchCalls++
				return false
			})

			if got.Status != WaitTimedOut || got.Err != nil || len(got.Panes) != 2 {
				t.Fatalf("Wait() = %#v, want timed_out with last two panes and no error", got)
			}
			if matchCalls != tt.wantSnapshots {
				t.Fatalf("predicate calls = %d, want %d", matchCalls, tt.wantSnapshots)
			}
			if len(fake.commands) != 3+tt.wantSnapshots {
				t.Fatalf("command count = %d, want %d", len(fake.commands), 3+tt.wantSnapshots)
			}
			for i, wantKey := range []string{"version", "status", "schema"} {
				if key := commandKey(fake.commands[i].args); key != wantKey {
					t.Fatalf("probe command %d = %q, want %q", i, key, wantKey)
				}
				assertCommandTimeout(t, fake.commands[i], commandTimeout)
			}
			for i, call := range fake.commands[3:] {
				if key := commandKey(call.args); key != "snapshot" {
					t.Fatalf("poll command %d = %q (%v), want snapshot", i, key, call.args)
				}
				if gotSocket, ok := envValue(call.env, socketEnv); !ok || gotSocket != socket {
					t.Fatalf("snapshot %d %s = %q (present=%t), want %q", i, socketEnv, gotSocket, ok, socket)
				}
				remaining := tt.budget - time.Duration(i)*waitInterval
				assertCommandTimeout(t, call, min(commandTimeout, remaining))
			}
			if len(clock.sleeps) != tt.wantSnapshots-1 {
				t.Fatalf("sleep count = %d, want %d", len(clock.sleeps), tt.wantSnapshots-1)
			}
			for i, delay := range clock.sleeps {
				if delay != waitInterval {
					t.Fatalf("sleep %d = %s, want %s", i, delay, waitInterval)
				}
			}
		})
	}
}

func TestWaitRetryableSnapshotErrorThenValidSnapshotTimesOut(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.snapshotResults = []fakeSnapshotResult{
		{err: context.DeadlineExceeded},
		{output: validSnapshot()},
	}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 3*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != WaitTimedOut || got.Err != nil || len(got.Panes) != 2 {
		t.Fatalf("Wait() = %#v, want timed_out with recovered snapshot", got)
	}
	if matchCalls != 1 {
		t.Fatalf("predicate calls = %d, want 1", matchCalls)
	}
	if len(fake.commands) != 5 || !slices.Equal(clock.sleeps, []time.Duration{waitInterval}) {
		t.Fatalf("commands = %d sleeps = %v, want 5 commands and one interval", len(fake.commands), clock.sleeps)
	}
}

func TestWaitValidSnapshotThenFinalRetryableErrorFails(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.snapshotResults = []fakeSnapshotResult{
		{output: validSnapshot()},
		{err: context.DeadlineExceeded},
	}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 3*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != WaitFailed || !errors.Is(got.Err, context.DeadlineExceeded) || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want failed with final snapshot error and nil panes", got)
	}
	if matchCalls != 1 {
		t.Fatalf("predicate calls = %d, want 1", matchCalls)
	}
	if len(fake.commands) != 5 || !slices.Equal(clock.sleeps, []time.Duration{waitInterval}) {
		t.Fatalf("commands = %d sleeps = %v, want 5 commands and one interval", len(fake.commands), clock.sleeps)
	}
}

func TestWaitPermanentCommandErrorFailsWithoutRetry(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	permanent := &os.PathError{Op: "fork/exec", Path: "/private/tmp/herdr-0.7.3", Err: syscall.ENOENT}
	fake := newFakeHerdr(session, socket)
	fake.snapshotResults = []fakeSnapshotResult{{err: permanent}}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != WaitFailed || !errors.Is(got.Err, syscall.ENOENT) || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want immediate failed result preserving ENOENT", got)
	}
	if matchCalls != 0 || len(fake.commands) != 4 || len(clock.sleeps) != 0 {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/4/none", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitCommandCleanupFailureOverridesRetryableCommandErrors(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	exitErr := exec.Command("/bin/sh", "-c", "exit 7").Run()
	var typedExitErr *exec.ExitError
	if !errors.As(exitErr, &typedExitErr) {
		t.Fatalf("helper error type = %T, want *exec.ExitError", exitErr)
	}
	for _, tt := range []struct {
		name       string
		commandErr error
	}{
		{name: "non-zero exit", commandErr: exitErr},
		{name: "command deadline", commandErr: context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			fake.snapshotResults = []fakeSnapshotResult{{
				err: errors.Join(tt.commandErr, commandCleanupError{err: syscall.EPERM}),
			}}
			b := newTestBackend(t, session, socket, fake)
			clock := installFakeWaitClock(b)
			matchCalls := 0

			got := b.Wait(context.Background(), 5*time.Second, func([]corebackend.LivePane) bool {
				matchCalls++
				return false
			})

			if got.Status != WaitFailed || !errors.Is(got.Err, syscall.EPERM) || got.Panes != nil {
				t.Fatalf("Wait() = %#v, want immediate cleanup failure preserving EPERM", got)
			}
			if matchCalls != 0 || len(fake.commands) != 4 || len(clock.sleeps) != 0 {
				t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/4/none", matchCalls, len(fake.commands), clock.sleeps)
			}
		})
	}
}

func TestWaitMalformedSnapshotFailsImmediately(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.snapshotResults = []fakeSnapshotResult{{output: "{"}}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != WaitFailed || got.Err == nil || !strings.Contains(got.Err.Error(), "parse herdr api snapshot") || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want immediate parse failure with nil panes", got)
	}
	if matchCalls != 0 || len(fake.commands) != 4 || len(clock.sleeps) != 0 {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/4/none", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitIncompatibleSnapshotFailsImmediately(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	incompatible := strings.Replace(validSnapshot(), `"protocol":16`, `"protocol":17`, 1)
	fake.snapshotResults = []fakeSnapshotResult{{output: incompatible}}
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(context.Background(), 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != WaitFailed || got.Err == nil || !strings.Contains(got.Err.Error(), "unsupported herdr snapshot tuple") || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want immediate compatibility failure with nil panes", got)
	}
	if matchCalls != 0 || len(fake.commands) != 4 || len(clock.sleeps) != 0 {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/4/none", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitPreCancelledContextDoesNotInvokeHerdr(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	matchCalls := 0

	got := b.Wait(ctx, 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != WaitCancelled || !errors.Is(got.Err, context.Canceled) || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want canceled with context.Canceled and nil panes", got)
	}
	if matchCalls != 0 || len(fake.commands) != 0 || len(clock.sleeps) != 0 {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 0/0/none", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitCancellationDuringSleepStopsBeforeNextSnapshot(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeHerdr(session, socket)
	b := newTestBackend(t, session, socket, fake)
	clock := installFakeWaitClock(b)
	b.sleep = func(waitCtx context.Context, delay time.Duration) error {
		clock.sleeps = append(clock.sleeps, delay)
		cancel()
		<-waitCtx.Done()
		return waitCtx.Err()
	}
	matchCalls := 0

	got := b.Wait(ctx, 5*time.Second, func([]corebackend.LivePane) bool {
		matchCalls++
		return false
	})

	if got.Status != WaitCancelled || !errors.Is(got.Err, context.Canceled) || got.Panes != nil {
		t.Fatalf("Wait() = %#v, want canceled with context.Canceled and nil panes", got)
	}
	if matchCalls != 1 || len(fake.commands) != 4 || !slices.Equal(clock.sleeps, []time.Duration{waitInterval}) {
		t.Fatalf("predicate calls = %d commands = %d sleeps = %v, want 1/4/one interval", matchCalls, len(fake.commands), clock.sleeps)
	}
}

func TestWaitCancellationAfterSnapshotOrPredicateCannotMatch(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name           string
		cancelSnapshot bool
		wantMatchCalls int
	}{
		{name: "after successful snapshot", cancelSnapshot: true, wantMatchCalls: 0},
		{name: "inside predicate", wantMatchCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := newFakeHerdr(session, socket)
			if tt.cancelSnapshot {
				fake.intercept = func(_ context.Context, key string) error {
					if key == "snapshot" {
						cancel()
					}
					return nil
				}
			}
			b := newTestBackend(t, session, socket, fake)
			installFakeWaitClock(b)
			matchCalls := 0

			got := b.Wait(ctx, 5*time.Second, func([]corebackend.LivePane) bool {
				matchCalls++
				if !tt.cancelSnapshot {
					cancel()
				}
				return true
			})

			if got.Status != WaitCancelled || !errors.Is(got.Err, context.Canceled) || got.Panes != nil {
				t.Fatalf("Wait() = %#v, want canceled result instead of matched", got)
			}
			if matchCalls != tt.wantMatchCalls || len(fake.commands) != 4 {
				t.Fatalf("predicate calls = %d commands = %d, want %d/4", matchCalls, len(fake.commands), tt.wantMatchCalls)
			}
		})
	}
}

func TestWaitDeadlineCrossingAfterSnapshotOrPredicateCannotMatch(t *testing.T) {
	const (
		session      = "fanout-test"
		socket       = "/private/tmp/fanout-test/herdr.sock"
		totalTimeout = 5 * time.Second
	)
	tests := []struct {
		name            string
		advanceSnapshot bool
		wantMatchCalls  int
	}{
		{name: "after successful snapshot", advanceSnapshot: true, wantMatchCalls: 0},
		{name: "inside predicate", wantMatchCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeHerdr(session, socket)
			b := newTestBackend(t, session, socket, fake)
			clock := installFakeWaitClock(b)
			if tt.advanceSnapshot {
				fake.intercept = func(_ context.Context, key string) error {
					if key == "snapshot" {
						clock.now = clock.now.Add(totalTimeout)
					}
					return nil
				}
			}
			matchCalls := 0

			got := b.Wait(context.Background(), totalTimeout, func([]corebackend.LivePane) bool {
				matchCalls++
				if !tt.advanceSnapshot {
					clock.now = clock.now.Add(totalTimeout)
				}
				return true
			})

			if got.Status != WaitTimedOut || got.Err != nil || len(got.Panes) != 2 {
				t.Fatalf("Wait() = %#v, want timed_out with the last compatible snapshot", got)
			}
			if matchCalls != tt.wantMatchCalls || len(fake.commands) != 4 {
				t.Fatalf("predicate calls = %d commands = %d, want %d/4", matchCalls, len(fake.commands), tt.wantMatchCalls)
			}
		})
	}
}

func TestWaitCancellationStopsProbeOrSnapshotImmediately(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	tests := []struct {
		name         string
		cancelOn     string
		wantCommands []string
	}{
		{name: "during probe", cancelOn: "version", wantCommands: []string{"version"}},
		{name: "during snapshot", cancelOn: "snapshot", wantCommands: []string{"version", "status", "schema", "snapshot"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := newFakeHerdr(session, socket)
			fake.intercept = func(callCtx context.Context, key string) error {
				if key != tt.cancelOn {
					return nil
				}
				cancel()
				<-callCtx.Done()
				return callCtx.Err()
			}
			b := newTestBackend(t, session, socket, fake)
			clock := installFakeWaitClock(b)
			matchCalls := 0

			got := b.Wait(ctx, 5*time.Second, func([]corebackend.LivePane) bool {
				matchCalls++
				return false
			})

			if got.Status != WaitCancelled || !errors.Is(got.Err, context.Canceled) || got.Panes != nil {
				t.Fatalf("Wait() = %#v, want canceled with context.Canceled and nil panes", got)
			}
			if matchCalls != 0 || len(clock.sleeps) != 0 {
				t.Fatalf("predicate calls = %d sleeps = %v, want 0/none", matchCalls, clock.sleeps)
			}
			if len(fake.commands) != len(tt.wantCommands) {
				t.Fatalf("commands = %d, want %d", len(fake.commands), len(tt.wantCommands))
			}
			for i, call := range fake.commands {
				if key := commandKey(call.args); key != tt.wantCommands[i] {
					t.Fatalf("command %d = %q (%v), want %q", i, key, call.args, tt.wantCommands[i])
				}
			}
		})
	}
}

func TestValidNativeAgentState(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "working", want: true},
		{raw: "blocked", want: true},
		{raw: "idle", want: true},
		{raw: "done", want: true},
		{raw: "unknown", want: true},
		{raw: "running", want: false},
	}
	for _, tt := range tests {
		if got := validNativeAgentState(tt.raw); got != tt.want {
			t.Errorf("validNativeAgentState(%q) = %t, want %t", tt.raw, got, tt.want)
		}
	}
}

func TestUnsupportedOperationsNeverInvokeHerdr(t *testing.T) {
	fake := newFakeHerdr("fanout-test", "/private/tmp/fanout-test/herdr.sock")
	b := newTestBackend(t, "fanout-test", "/private/tmp/fanout-test/herdr.sock", fake)
	ref := corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w1", Pane: "w1:p1"}

	var errs []error
	_, err := b.Launch(corebackend.LaunchRequest{})
	errs = append(errs, err)
	errs = append(errs, b.ReleaseStartGate("gate"))
	_, err = b.Read(ref, 100)
	errs = append(errs, err)
	errs = append(errs, b.SendLine(ref, "text"))
	errs = append(errs, b.Focus(ref))
	errs = append(errs, b.Close(ref))

	for i, err := range errs {
		if !errors.Is(err, corebackend.ErrUnsupported) || !corebackend.IsUnsupported(err) {
			t.Errorf("unsupported error %d = %v", i, err)
		}
	}
	if len(fake.commands) != 0 {
		t.Fatalf("unsupported operations invoked herdr: %#v", fake.commands)
	}
}

func TestKillCommandProcessTreeFallsBackWithoutHidingGroupFailure(t *testing.T) {
	tests := []struct {
		name            string
		groupErr        error
		directErr       error
		wantDirectCalls int
		wantErrors      []error
	}{
		{name: "group killed", wantDirectCalls: 0},
		{name: "group gone and direct done", groupErr: syscall.ESRCH, directErr: os.ErrProcessDone, wantDirectCalls: 1, wantErrors: []error{os.ErrProcessDone}},
		{name: "group gone but direct killed", groupErr: syscall.ESRCH, wantDirectCalls: 1, wantErrors: []error{syscall.ESRCH}},
		{name: "group denied but direct killed", groupErr: syscall.EPERM, wantDirectCalls: 1, wantErrors: []error{syscall.EPERM}},
		{name: "both kills denied", groupErr: syscall.EPERM, directErr: syscall.EACCES, wantDirectCalls: 1, wantErrors: []error{syscall.EPERM, syscall.EACCES}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directCalls := 0
			err := killCommandProcessTree(
				func() error { return tt.groupErr },
				func() error {
					directCalls++
					return tt.directErr
				},
			)

			if directCalls != tt.wantDirectCalls {
				t.Fatalf("direct kill calls = %d, want %d", directCalls, tt.wantDirectCalls)
			}
			if len(tt.wantErrors) == 0 && err != nil {
				t.Fatalf("killCommandProcessTree() error = %v, want nil", err)
			}
			for _, wantErr := range tt.wantErrors {
				if !errors.Is(err, wantErr) {
					t.Errorf("killCommandProcessTree() error = %v, want errors.Is(_, %v)", err, wantErr)
				}
			}
		})
	}
}

func TestFinalizeCommandErrorPreservesCleanupFailureOnDeadline(t *testing.T) {
	err := finalizeCommandError(exec.ErrWaitDelay, context.DeadlineExceeded, syscall.EPERM)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, syscall.EPERM) {
		t.Fatalf("finalizeCommandError() = %v, want deadline and cleanup errors", err)
	}
	var cleanupErr commandCleanupError
	if !errors.As(err, &cleanupErr) {
		t.Fatalf("finalizeCommandError() type = %T, want commandCleanupError", err)
	}
	if retryableCommandError(err) {
		t.Fatal("deadline plus cleanup failure classified as retryable")
	}
}

func TestRunCommandBoundsInheritedPipeWaitAndKillsProcessGroup(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	controlDir := t.TempDir()
	pidPath := filepath.Join(controlDir, "helper-pids")
	readyPath := filepath.Join(controlDir, "descendant-ready")
	releasePath := filepath.Join(controlDir, "release-direct")
	lockPath := filepath.Join(controlDir, "descendant.lock")
	env := envWithValue(os.Environ(), runCommandHelperModeEnv, "direct")
	env = envWithValue(env, runCommandHelperPIDsEnv, pidPath)
	env = envWithValue(env, runCommandHelperReadyEnv, readyPath)
	env = envWithValue(env, runCommandHelperReleaseEnv, releasePath)
	env = envWithValue(env, runCommandHelperLockEnv, lockPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type commandResult struct {
		err error
	}
	resultCh := make(chan commandResult, 1)
	go func() {
		_, commandErr := runCommand(ctx, binary, env, "-test.run=^TestRunCommandInheritedPipeHelper$")
		resultCh <- commandResult{err: commandErr}
	}()

	pids, pidErr := waitForRunCommandHelperPIDs(pidPath, 2*time.Second)
	if pidErr != nil {
		cancel()
		select {
		case <-resultCh:
		case <-time.After(2 * time.Second):
		}
		t.Fatalf("wait for helper pids: %v", pidErr)
	}
	if pids.direct <= 0 || pids.descendant <= 0 || pids.group <= 0 {
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean invalid helper process group: %v", cleanupErr)
		}
		t.Fatalf("helper pids = %#v, want positive values", pids)
	}
	if pids.group == syscall.Getpgrp() {
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean unisolated helper process: %v", cleanupErr)
		}
		t.Fatalf("helper process group %d unexpectedly matches test process group", pids.group)
	}
	if pids.group != pids.direct {
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean mismatched helper process group: %v", cleanupErr)
		}
		t.Fatalf("helper process group = %d, want direct child pid %d", pids.group, pids.direct)
	}
	defer func() {
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean helper process group: %v", cleanupErr)
		}
	}()
	select {
	case result := <-resultCh:
		t.Fatalf("runCommand returned before the direct helper was released: %v", result.err)
	default:
	}
	if err := os.WriteFile(releasePath, []byte("release\n"), 0o600); err != nil {
		t.Fatalf("release direct helper: %v", err)
	}
	if !waitForProcessGone(pids.direct, 2*time.Second) {
		t.Fatalf("direct helper process %d was not reaped", pids.direct)
	}

	cancelledAt := time.Now()
	cancel()
	var result commandResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		if cleanupErr := killRunCommandHelper(pids); cleanupErr != nil {
			t.Errorf("clean timed-out helper process group: %v", cleanupErr)
		}
		select {
		case <-resultCh:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("runCommand did not return after its context deadline while a descendant held its output pipes")
	}

	if elapsed := time.Since(cancelledAt); elapsed > 2*time.Second {
		t.Fatalf("runCommand took %s after cancellation, want at most 2s", elapsed)
	}
	if result.err == nil {
		t.Fatal("runCommand error = nil, want a bounded command or pipe-wait error")
	}
	if err := waitForHelperLockRelease(lockPath, 2*time.Second); err != nil {
		t.Fatalf("descendant process was not killed after runCommand returned: %v", err)
	}
}

func TestRunCommandInheritedPipeHelper(t *testing.T) {
	mode := os.Getenv(runCommandHelperModeEnv)
	if mode == "" {
		return
	}
	if mode == "descendant" {
		lockFile, err := os.OpenFile(os.Getenv(runCommandHelperLockEnv), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "open descendant lock: %v\n", err)
			os.Exit(2)
		}
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "lock descendant file: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv(runCommandHelperReadyEnv), []byte("ready\n"), 0o600); err != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "write descendant ready file: %v\n", err)
			os.Exit(2)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if mode != "direct" {
		t.Fatalf("unknown helper mode %q", mode)
	}

	binary, err := os.Executable()
	if err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "os.Executable: %v\n", err)
		os.Exit(2)
	}
	child := exec.Command(binary, "-test.run=^TestRunCommandInheritedPipeHelper$")
	child.Env = envWithValue(os.Environ(), runCommandHelperModeEnv, "descendant")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if startErr := child.Start(); startErr != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "start descendant: %v\n", startErr)
		os.Exit(2)
	}
	group, err := syscall.Getpgid(child.Process.Pid)
	if err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "get descendant process group: %v\n", err)
		if killErr := child.Process.Kill(); killErr != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "kill descendant after group lookup failure: %v\n", killErr)
		}
		os.Exit(2)
	}
	if err := waitForFile(os.Getenv(runCommandHelperReadyEnv), 2*time.Second); err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "wait for descendant readiness: %v\n", err)
		if killErr := syscall.Kill(-group, syscall.SIGKILL); killErr != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "kill unready helper group: %v\n", killErr)
		}
		os.Exit(2)
	}
	pids := fmt.Sprintf("%d %d %d\n", os.Getpid(), child.Process.Pid, group)
	if err := os.WriteFile(os.Getenv(runCommandHelperPIDsEnv), []byte(pids), 0o600); err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "write helper pids: %v\n", err)
		if killErr := syscall.Kill(-group, syscall.SIGKILL); killErr != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "kill helper group after pid write failure: %v\n", killErr)
		}
		os.Exit(2)
	}
	if err := waitForFile(os.Getenv(runCommandHelperReleaseEnv), 5*time.Second); err != nil {
		// The helper exits immediately, so a diagnostic write failure is not recoverable.
		_, _ = fmt.Fprintf(os.Stderr, "wait for direct release: %v\n", err)
		if killErr := syscall.Kill(-group, syscall.SIGKILL); killErr != nil {
			// The helper exits immediately, so a diagnostic write failure is not recoverable.
			_, _ = fmt.Fprintf(os.Stderr, "kill unreleased helper group: %v\n", killErr)
		}
		os.Exit(2)
	}
	os.Exit(0)
}
