package herdrrun

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type recordedCommand struct {
	args []string
	env  []string
}

type fakeHerdr struct {
	commands []recordedCommand
	version  string
	status   string
	schema   string
	snapshot string
	errors   map[string]error
}

func (f *fakeHerdr) output(_ string, env []string, args ...string) ([]byte, error) {
	f.commands = append(f.commands, recordedCommand{
		args: slices.Clone(args),
		env:  slices.Clone(env),
	})
	key := commandKey(args)
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
	if root.CurrentPath != "/repo" || root.NativeAgentState != "unknown" || root.AgentState != "" || root.TerminalID != "term-root" || root.SessionID != session || root.SocketPath != socket {
		t.Fatalf("root live pane = %#v", root)
	}
	child := got[1]
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
