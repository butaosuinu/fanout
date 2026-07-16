package peermsg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// TestWatchEventHumanLine pins the one-line format watch emits per message
// (the codex bridge injects these lines into an agent turn verbatim).
func TestWatchEventHumanLine(t *testing.T) {
	for _, tt := range []struct {
		name string
		ev   WatchEvent
		want string
	}{
		{
			name: "numeric 1:1 message",
			ev: WatchEvent{
				Msg:       msgstore.Message{ID: 2, From: 71, To: new(70), Kind: "blocker", Body: "waiting on your schema"},
				FromLabel: "#71", ToLabel: "#70",
			},
			want: "[fanout msg #2] #71 -> #70 (blocker): waiting on your schema",
		},
		{
			name: "board post renders the board recipient",
			ev: WatchEvent{
				Msg:       msgstore.Message{ID: 3, From: 71, Board: true, Kind: "note", Body: "board update"},
				FromLabel: "#71", ToLabel: "board",
			},
			want: "[fanout msg #3] #71 -> board (note): board update",
		},
		{
			name: "plan task labels replace synthetic numbers",
			ev: WatchEvent{
				Msg:       msgstore.Message{ID: 1, From: -5, To: new(-6), Kind: "note", Body: "hi"},
				FromLabel: "task-b", ToLabel: "task-a",
			},
			want: "[fanout msg #1] task-b -> task-a (note): hi",
		},
		{
			name: "multiline body flattens to one line",
			ev: WatchEvent{
				Msg:       msgstore.Message{ID: 4, From: 71, To: new(70), Kind: "note", Body: "line one\r\nline two"},
				FromLabel: "#71", ToLabel: "#70",
			},
			want: "[fanout msg #4] #71 -> #70 (note): line one  line two",
		},
		{
			name: "control bytes in body and kind are blanked (no terminal escapes)",
			ev: WatchEvent{
				Msg:       msgstore.Message{ID: 5, From: 71, To: new(70), Kind: "no\x1bte", Body: "ok\x1b]0;title\x07\x1b[2Kdone"},
				FromLabel: "#71", ToLabel: "#70",
			},
			want: "[fanout msg #5] #71 -> #70 (no te): ok ]0;title  [2Kdone",
		},
		{
			name: "C1 controls and bidi overrides are blanked (single-rune CSI, RTL spoofing)",
			ev: WatchEvent{
				Msg:       msgstore.Message{ID: 6, From: 71, To: new(70), Kind: "note", Body: "a\u009b2Jb\u202ereversed\u202cc"},
				FromLabel: "#71", ToLabel: "#70",
			},
			want: "[fanout msg #6] #71 -> #70 (note): a 2Jb reversed c",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ev.HumanLine(); got != tt.want {
				t.Errorf("HumanLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// newTestWatcher opens a real temp SQLite team DB and returns a Watcher for
// self under parent, plus the store for seeding.
func newTestWatcher(t *testing.T, self int, parent string) *Watcher {
	t.Helper()
	db, err := team.Open(filepath.Join(t.TempDir(), "team.db"))
	if err != nil {
		t.Fatalf("team.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = team.EnsureSchema(db); err != nil {
		t.Fatalf("team.EnsureSchema: %v", err)
	}
	store, err := msgstore.New(db, parent)
	if err != nil {
		t.Fatalf("msgstore.New: %v", err)
	}
	return &Watcher{db: db, store: store, self: self, parent: parent}
}

// TestWatcherPollMarksRead guarantees emit = delivered = read: the first Poll
// returns the unread union and marks it read, so the second Poll is empty and
// the board cursor has advanced past the delivered post.
func TestWatcherPollMarksRead(t *testing.T) {
	w := newTestWatcher(t, 70, "68")
	now := "2026-06-13T00:00:00Z"
	if _, err := w.store.Send(71, 70, "note", "first note", now); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := w.store.Post(71, "note", "board update", now); err != nil {
		t.Fatalf("Post: %v", err)
	}

	events, err := w.Poll()
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Poll() returned %d events, want 2", len(events))
	}
	if events[0].FromLabel != "#71" || events[0].ToLabel != "#70" {
		t.Errorf("events[0] labels = %q -> %q, want #71 -> #70", events[0].FromLabel, events[0].ToLabel)
	}
	if events[1].ToLabel != "board" {
		t.Errorf("events[1].ToLabel = %q, want board", events[1].ToLabel)
	}

	again, err := w.Poll()
	if err != nil {
		t.Fatalf("second Poll() error = %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second Poll() returned %d events, want 0 (delivered messages must be read)", len(again))
	}
}

// TestWatcherPollPlanLabels guarantees plan parents surface task ids: labels
// on the events and fromTask/toTask in the JSON lines.
func TestWatcherPollPlanLabels(t *testing.T) {
	const parent = "plan:demo"
	numA := team.TaskPeerNum(parent, "task-a")
	numB := team.TaskPeerNum(parent, "task-b")
	w := newTestWatcher(t, numA, parent)
	now := "2026-06-13T00:00:00Z"
	for _, p := range []msgstore.Peer{
		{Issue: numA, TaskID: "task-a"},
		{Issue: numB, TaskID: "task-b"},
	} {
		if _, err := w.store.Register(p, now); err != nil {
			t.Fatalf("Register(%s): %v", p.TaskID, err)
		}
	}
	if _, err := w.store.Send(numB, numA, "note", "hello from b", now); err != nil {
		t.Fatalf("Send: %v", err)
	}

	events, err := w.Poll()
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Poll() returned %d events, want 1", len(events))
	}
	if events[0].FromLabel != "task-b" || events[0].ToLabel != "task-a" {
		t.Errorf("labels = %q -> %q, want task-b -> task-a", events[0].FromLabel, events[0].ToLabel)
	}

	var out, errb strings.Builder
	lg := log.NewWith(&out, &errb, false)
	if code := emitWatchEvents(&Request{Verb: "watch", JSON: true}, events, lg); code != exitcode.OK {
		t.Fatalf("emitWatchEvents code = %d, want OK", code)
	}
	line := out.String()
	if !strings.Contains(line, `"fromTask":"task-b"`) || !strings.Contains(line, `"toTask":"task-a"`) {
		t.Errorf("JSON line = %q, want fromTask/toTask task ids", line)
	}
}

// TestWatcherPollLateRegistration pins the label-resolution ordering: labels
// are resolved after the mark-read commit, so a sender that registers before
// the poll (even after sending) is labeled, while a still-unregistered sender
// degrades to its raw synthetic number — delivered, never lost.
func TestWatcherPollLateRegistration(t *testing.T) {
	const parent = "plan:demo"
	numA := team.TaskPeerNum(parent, "task-a")
	numB := team.TaskPeerNum(parent, "task-b")
	w := newTestWatcher(t, numA, parent)
	now := "2026-06-13T00:00:00Z"

	// send lands before the sender's register: degraded numeric label.
	if _, err := w.store.Send(numB, numA, "note", "sent before register", now); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events, err := w.Poll()
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(events) != 1 || strings.HasPrefix(events[0].FromLabel, "task-") {
		t.Fatalf("events = %+v, want 1 event with a raw numeric FromLabel", events)
	}

	// once registered, the next message resolves to the task id.
	if _, err = w.store.Register(msgstore.Peer{Issue: numB, TaskID: "task-b"}, now); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err = w.store.Send(numB, numA, "note", "sent after register", now); err != nil {
		t.Fatalf("Send: %v", err)
	}
	events, err = w.Poll()
	if err != nil {
		t.Fatalf("second Poll() error = %v", err)
	}
	if len(events) != 1 || events[0].FromLabel != "task-b" {
		t.Fatalf("events = %+v, want 1 event from task-b", events)
	}
}

// failWriter fails every write, standing in for a full disk or a closed
// no-SIGPIPE stdout sink.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// TestEmitWatchEventsWriteFailureIsFatal guarantees a broken stdout exits 4
// instead of silently discarding already-marked-read messages forever.
func TestEmitWatchEventsWriteFailureIsFatal(t *testing.T) {
	events := []WatchEvent{{Msg: msgstore.Message{ID: 1, From: 71, To: new(70), Kind: "note", Body: "a"}, FromLabel: "#71", ToLabel: "#70"}}
	for _, tt := range []struct {
		name string
		req  Request
	}{
		{name: "human line write failure", req: Request{Verb: "watch"}},
		{name: "json line write failure", req: Request{Verb: "watch", JSON: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var errb strings.Builder
			lg := log.NewWith(failWriter{}, &errb, false)
			if code := emitWatchEvents(&tt.req, events, lg); code != exitcode.Backend {
				t.Errorf("emitWatchEvents code = %d, want Backend", code)
			}
			if !strings.Contains(errb.String(), "failed to write to stdout") {
				t.Errorf("stderr = %q, want a write-failure error", errb.String())
			}
		})
	}
}

// TestWatchTickInterval pins the in-process clamp: the CLI enforces 1-86400
// at parse time, but exported-Request callers get the documented default for
// a zero value and never a negative (overflowed) duration.
func TestWatchTickInterval(t *testing.T) {
	for _, tt := range []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{name: "zero value falls to the default", seconds: 0, want: 2 * time.Second},
		{name: "negative falls to the default", seconds: -7, want: 2 * time.Second},
		{name: "in-range passes through", seconds: 5, want: 5 * time.Second},
		{name: "above the ceiling clamps instead of overflowing", seconds: 9999999999, want: 86400 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := watchTickInterval(tt.seconds); got != tt.want {
				t.Errorf("watchTickInterval(%d) = %v, want %v", tt.seconds, got, tt.want)
			}
		})
	}
}

// TestEmitWatchEventsJSONIsCompact pins the JSON Lines contract: one compact
// object per line (json.Marshal), unlike every other verb's indented
// writeMsgJSON output — a line-oriented consumer must never have to join
// fragments.
func TestEmitWatchEventsJSONIsCompact(t *testing.T) {
	events := []WatchEvent{
		{Msg: msgstore.Message{ID: 1, From: 71, To: new(70), Kind: "note", Body: "a"}, FromLabel: "#71", ToLabel: "#70"},
		{Msg: msgstore.Message{ID: 2, From: 71, Board: true, Kind: "note", Body: "b"}, FromLabel: "#71", ToLabel: "board"},
	}
	var out, errb strings.Builder
	lg := log.NewWith(&out, &errb, false)
	if code := emitWatchEvents(&Request{Verb: "watch", JSON: true}, events, lg); code != exitcode.OK {
		t.Fatalf("emitWatchEvents code = %d, want OK", code)
	}
	got := out.String()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("emitted %d lines, want 2 (one per event):\n%s", len(lines), got)
	}
	for _, line := range lines {
		if strings.Contains(line, "  ") {
			t.Errorf("line contains MarshalIndent-style spacing: %q", line)
		}
		if !strings.HasPrefix(line, `{"id":`) {
			t.Errorf("line is not a compact JSON object: %q", line)
		}
	}
}

// fakePoller scripts Poll outcomes for watchLoop tests; after the script is
// exhausted it keeps returning the last entry.
type fakePoller struct {
	script []error
	calls  int
}

func (p *fakePoller) Poll() ([]WatchEvent, error) {
	i := min(p.calls, len(p.script)-1)
	p.calls++
	return nil, p.script[i]
}

// tickDeps returns Deps whose Tick fires immediately so watchLoop never
// sleeps in tests.
func tickDeps() Deps {
	return Deps{Tick: func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}}
}

func TestWatchLoop(t *testing.T) {
	errPoll := errors.New("db gone")
	for _, tt := range []struct {
		name       string
		script     []error
		maxPolls   string // FANOUT_WATCH_POLLS value ("" = unbounded)
		wantCode   exitcode.Code
		wantCalls  int
		wantGiveUp bool // the "giving up" line: only the consecutive-failure budget prints it
	}{
		{name: "poll bound exits 0 after n polls", script: []error{nil}, maxPolls: "3", wantCode: exitcode.OK, wantCalls: 3},
		{name: "poll bound ending on failures exits 4, not a masked success", script: []error{nil, errPoll}, maxPolls: "3", wantCode: exitcode.Backend, wantCalls: 3},
		{name: "five consecutive failures exit 4", script: []error{errPoll}, wantCode: exitcode.Backend, wantCalls: 5, wantGiveUp: true},
		{name: "a success resets the failure counter", script: []error{errPoll, errPoll, errPoll, errPoll, nil, errPoll, errPoll, errPoll, errPoll, errPoll}, wantCode: exitcode.Backend, wantCalls: 10, wantGiveUp: true},
		{name: "invalid poll bound is ignored (failures still exit 4)", script: []error{errPoll}, maxPolls: "zero", wantCode: exitcode.Backend, wantCalls: 5, wantGiveUp: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(watchPollsEnv, tt.maxPolls)
			p := &fakePoller{script: tt.script}
			var out, errb strings.Builder
			lg := log.NewWith(&out, &errb, false)
			code := watchLoop(p, &Request{Verb: "watch", Interval: 1}, nil, tickDeps(), lg)
			if code != tt.wantCode {
				t.Errorf("watchLoop code = %d, want %d", code, tt.wantCode)
			}
			if p.calls != tt.wantCalls {
				t.Errorf("Poll called %d times, want %d", p.calls, tt.wantCalls)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty (warnings belong on stderr)", out.String())
			}
			if tt.wantCode == exitcode.Backend && !strings.Contains(errb.String(), "poll failed") {
				t.Errorf("stderr = %q, want poll-failure warnings", errb.String())
			}
			if got := strings.Contains(errb.String(), "giving up"); got != tt.wantGiveUp {
				t.Errorf("stderr giving-up line present = %v, want %v (stderr: %q)", got, tt.wantGiveUp, errb.String())
			}
		})
	}
}

// TestOpenWatcher exercises the exported in-process entry point (the codex
// team bridge's contract): explicit identity opens a pollable watcher, a
// failed detection returns the msg-verb Invocation code.
func TestOpenWatcher(t *testing.T) {
	t.Run("explicit identity opens a pollable watcher", func(t *testing.T) {
		t.Setenv(team.DBPathEnv, filepath.Join(t.TempDir(), "team.db"))
		w, code := OpenWatcher(Request{Self: 70, Parent: "68"}, Deps{}, msgTestLogger())
		if code != exitcode.OK {
			t.Fatalf("OpenWatcher code = %d, want OK", code)
		}
		defer w.Close()
		events, err := w.Poll()
		if err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
		if len(events) != 0 {
			t.Errorf("Poll() on a fresh DB returned %d events, want 0", len(events))
		}
	})
	t.Run("failed detection exits with the shared Invocation code", func(t *testing.T) {
		deps := Deps{DetectIdentity: func() (team.Identity, error) { return team.Identity{}, errors.New("no pane") }}
		var out, errb strings.Builder
		lg := log.NewWith(&out, &errb, false)
		w, code := OpenWatcher(Request{}, deps, lg)
		if w != nil || code != exitcode.Invocation {
			t.Fatalf("OpenWatcher = (%v, %d), want (nil, Invocation)", w, code)
		}
		if !strings.Contains(errb.String(), "msg watch: could not detect this pane") {
			t.Errorf("stderr = %q, want the shared msg-verb detection wording", errb.String())
		}
	})
}

// TestWatchLoopSignalExitsClean guarantees SIGINT/SIGTERM end the loop with
// exit 0: the tick never fires, so only the signal channel can unblock it.
func TestWatchLoopSignalExitsClean(t *testing.T) {
	t.Setenv(watchPollsEnv, "")
	sig := make(chan os.Signal, 1)
	sig <- os.Interrupt
	deps := Deps{Tick: func(time.Duration) <-chan time.Time { return nil }}
	p := &fakePoller{script: []error{nil}}
	code := watchLoop(p, &Request{Verb: "watch", Interval: 1}, sig, deps, msgTestLogger())
	if code != exitcode.OK {
		t.Errorf("watchLoop code = %d, want OK on signal", code)
	}
	if p.calls != 1 {
		t.Errorf("Poll called %d times, want 1 (initial poll, then the signal wins)", p.calls)
	}
}
