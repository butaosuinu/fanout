package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
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
	snapshot        string
	errors          map[string]error
	snapshotResults []fakeSnapshotResult
	snapshotCall    int
	intercept       func(context.Context, string) error
	respond         func([]string) ([]byte, error)
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
	case "snapshot":
		return []byte(f.snapshot), nil
	default:
		if f.respond != nil {
			return f.respond(args)
		}
		return nil, fmt.Errorf("unexpected herdr args: %v", args)
	}
}

func commandKey(args []string) string {
	switch {
	case slices.Equal(args, []string{"--version"}):
		return "version"
	case hasSuffix(args, "status", "--json"):
		return "status"
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
		version:  "herdr 0.7.5\n",
		status:   validStatus(session, socket),
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
		return "/private/tmp/herdr-0.7.5", nil
	}
	b.stageBinary = func(path string) (string, string, error) {
		return path, strings.Repeat("a", 64), nil
	}
	b.output = fake.output
	return b
}

type fakeWaitClock struct {
	now    time.Time
	sleeps []time.Duration
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

func validStatus(session, socket string) string {
	return fmt.Sprintf(`{
	  "client":{"version":"0.7.5","channel":"stable","protocol":17,"binary":"/private/tmp/herdr-0.7.5","session":%s},
	  "server":{"status":"running","running":true,"version":"0.7.5","protocol":17,"capabilities":{"live_handoff":true,"detached_server_daemon":true},"compatible":true,"socket":%s,"session":%s,"restart_needed":false},
  "update":{"restart_needed":false}
}`+"\n", strconv.Quote(session), strconv.Quote(socket), strconv.Quote(session))
}

func validSnapshot() string {
	return `{
  "id":"cli:api:snapshot",
  "result":{
    "type":"session_snapshot",
    "snapshot":{
	      "version":"0.7.5",
	      "protocol":17,
      "workspaces":[
        {"workspace_id":"w1","number":1,"label":"root","focused":true,"pane_count":1,"tab_count":1,"active_tab_id":"w1:t1","agent_status":"unknown"},
        {"workspace_id":"w2","number":2,"label":"child","focused":false,"pane_count":1,"tab_count":1,"active_tab_id":"w2:t1","agent_status":"working","worktree":{"repo_key":"/repo/.git","repo_name":"repo","repo_root":"/repo","checkout_path":"/repo/.fanout/worktrees/child","is_linked_worktree":true}}
      ],
      "tabs":[],
      "panes":[
        {"pane_id":"w1:p1","terminal_id":"term-root","workspace_id":"w1","tab_id":"w1:t1","focused":true,"cwd":"/repo","foreground_cwd":"/tmp/foreground","agent_status":"unknown","revision":1},
        {"pane_id":"w2:p1","terminal_id":"term-child","workspace_id":"w2","tab_id":"w2:t1","focused":false,"cwd":"/wrong-saved-cwd","foreground_cwd":"/tmp/other-foreground","title":"child title","agent":"codex","agent_status":"working","revision":2,"agent_session":{"source":"herdr:codex","agent":"codex","kind":"id","value":"session-a"}}
      ],
      "layouts":[],
      "agents":[
        {"terminal_id":"term-child","name":"fanout-child","agent":"codex","agent_status":"working","workspace_id":"w2","tab_id":"w2:t1","pane_id":"w2:p1","focused":false,"cwd":"/wrong-saved-cwd","foreground_cwd":"/tmp/other-foreground","revision":2,"agent_session":{"source":"herdr:codex","agent":"codex","kind":"id","value":"session-a"}}
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

func TestObservationCommandErrorClassifiesOnlyTransientFailures(t *testing.T) {
	transient := observationCommandError("observe", context.DeadlineExceeded)
	if !corebackend.IsRetryableObservationError(transient) {
		t.Fatalf("deadline error = %v, want retryable observation", transient)
	}
	permanent := observationCommandError("observe", errors.New("malformed response"))
	if corebackend.IsRetryableObservationError(permanent) {
		t.Fatalf("malformed error = %v, want permanent observation failure", permanent)
	}
}

// TestPaneRunResponse pins how Herdr answers `pane run`, the call that hands a
// launcher its start token. The empty success body is captured from real herdr
// 0.7.5 and 0.8.0: both exit 0 and write nothing at all. Demanding an envelope
// rejected every launch, and no test caught it because the envelope this file
// used to assert on was invented here rather than observed.
func TestPaneRunResponse(t *testing.T) {
	for _, tt := range []struct {
		name     string
		out      []byte
		accepted bool
	}{
		{name: "silent success is what 0.7.5 and 0.8.0 return", out: nil, accepted: true},
		{name: "a blank body is a silent success too", out: []byte("  \n"), accepted: true},
		{
			name:     "an explicit ok envelope stays acceptable",
			out:      []byte(`{"id":"cli:pane:run","result":{"type":"ok"}}`),
			accepted: true,
		},
		{
			name:     "another verb's envelope is rejected",
			out:      []byte(`{"id":"cli:pane:get","result":{"type":"ok"}}`),
			accepted: false,
		},
		{
			name:     "a result type other than ok is rejected",
			out:      []byte(`{"id":"cli:pane:run","result":{"type":"unexpected"}}`),
			accepted: false,
		},
		{
			name:     "a rejection envelope carries no result and is rejected",
			out:      []byte(`{"id":"cli:pane:run","error":{"code":"pane_not_found"}}`),
			accepted: false,
		},
		{
			name:     "trailing JSON after the envelope is rejected",
			out:      []byte(`{"id":"cli:pane:run","result":{"type":"ok"}} {"id":"x"}`),
			accepted: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePaneRunResponse(tt.out)
			if accepted := err == nil; accepted != tt.accepted {
				t.Fatalf("validatePaneRunResponse(%q) accepted = %v, want %v (err = %v)",
					tt.out, accepted, tt.accepted, err)
			}
		})
	}
}

func TestIssueRestartResumeTokenDoesNotRunAfterJournalSaveExpires(t *testing.T) {
	commandCalled := false
	now := time.Now()
	session := &OwnedSession{backend: &Backend{herdrCLI: &herdrCLI{now: func() time.Time { return now }, output: func(
		context.Context, string, []string, ...string,
	) ([]byte, error) {
		commandCalled = true
		return nil, nil
	}}}}
	marked := false
	err := session.issueRestartResumeToken(
		context.Background(), probeResult{}, "w1:p1", strings.Repeat("a", 32),
		now.Add(time.Minute),
		func() error {
			marked = true
			now = now.Add(time.Minute)
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "intent expired") {
		t.Fatalf("expiry error = %v", err)
	}
	if !marked || commandCalled {
		t.Fatalf("marked=%t commandCalled=%t", marked, commandCalled)
	}
}

func TestRestartResumeTokenRequiresExactLifecycleAndIntentRoute(t *testing.T) {
	session := &OwnedSession{
		GitCommonDir: "/repo/.git", RuntimeDir: "/runtime", Session: "fanout-owned",
		SocketPath: "/runtime/herdr.sock", ClientSocketPath: "/runtime/client.sock",
	}
	server := &state.RuntimeServerIdentity{
		GitCommonDir: session.GitCommonDir, RuntimeDir: session.RuntimeDir, Session: session.Session,
		SocketPath: session.SocketPath, ClientSocketPath: session.ClientSocketPath,
	}
	if !serverRestartTokenMatches(server, session) {
		t.Fatal("exact restart lifecycle route did not match")
	}
	server.SocketPath = "/runtime/other.sock"
	if serverRestartTokenMatches(server, session) {
		t.Fatal("mismatched restart lifecycle route matched")
	}

	nonce := strings.Repeat("a", 32)
	intent := state.LaunchIntent{
		Kind: state.IntentResume, Status: state.IntentRealized,
		Session: session.Session, SocketPath: session.SocketPath,
		Resource: state.RuntimeResource{PaneID: "w1:p1"},
		Launch:   &state.LaunchCapsule{Nonce: nonce},
	}
	if !exactRestartResumeTokenIntent(intent, session.Session, session.SocketPath, "w1:p1", nonce) {
		t.Fatal("exact resume token intent did not match")
	}
	intent.Launch.TokenIssued = true
	if exactRestartResumeTokenIntent(intent, session.Session, session.SocketPath, "w1:p1", nonce) {
		t.Fatal("issued resume token intent matched")
	}
}
