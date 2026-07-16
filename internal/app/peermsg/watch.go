package peermsg

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// watchPollsEnv bounds the watch loop to N polls before a clean exit 0. It is
// a test-only seam (bats goldens run one poll and terminate) and deliberately
// absent from the usage text; invalid or non-positive values are ignored.
const watchPollsEnv = "FANOUT_WATCH_POLLS"

// watchMaxFailures is the consecutive-poll-failure budget: transient DB errors
// warn and continue, the Nth in a row exits 4.
const watchMaxFailures = 5

// WatchEvent is one delivered message with its display labels resolved the
// way inbox renders them (memberDisplay): plan task ids where known, "#<n>"
// otherwise, and "board" as the recipient of a board post. Consumers are
// listed on OpenWatcher.
type WatchEvent struct {
	Msg       msgstore.Message
	FromLabel string
	ToLabel   string
	// fromTask/toTask are the plan task ids for the JSON view (empty outside
	// plan parents), carried from Poll's labels so the emitter never has to
	// reverse-engineer them from the display labels.
	fromTask string
	toTask   string
}

// HumanLine flattens the event to the one-line form watch emits per message
// (and the codex bridge injects into an agent turn): the body is collapsed to
// a single line the way the inbox table renders it, and control bytes are
// blanked so a peer-controlled body or kind cannot smuggle terminal escape
// sequences (or forge "[fanout msg #N]" framing via cursor movement) into the
// watching terminal or an agent prompt.
func (e WatchEvent) HumanLine() string {
	return sanitizeWatchLine(fmt.Sprintf("[fanout msg #%d] %s -> %s (%s): %s",
		e.Msg.ID, e.FromLabel, e.ToLabel, e.Msg.Kind, msgTableBody(e.Msg.Body)))
}

// sanitizeWatchLine blanks C0 control bytes and DEL. Without their ESC/BEL
// introducers the printable remainder of any escape sequence is inert.
func sanitizeWatchLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F {
			return ' '
		}
		return r
	}, s)
}

// Watcher follows one pane's inbox (1:1 + board) on the per-parent team DB.
// It owns its DB handle — Close releases it — so it can outlive the opening
// call frame; peermsg.Run's own defer db.Close() never applies to it.
// Consumers are listed on OpenWatcher.
type Watcher struct {
	db     *sql.DB
	store  *msgstore.Store
	self   int
	parent string
}

// OpenWatcher resolves the watching pane's identity (explicit --self/--parent
// win, else pane detection — same wording and Invocation code as the other msg
// verbs) and opens the per-parent team DB. Callers must Close the returned
// Watcher.
//
// This is the in-process entry point for the codex team bridge
// (codex-appserver-push); the `fanout msg watch` CLI reaches the same
// constructor through Run.
func OpenWatcher(req Request, deps Deps, lg *log.Logger) (*Watcher, exitcode.Code) {
	req.Verb = "watch"
	self, parent, _, code := resolveMsgIdentity(&req, deps, lg)
	if code != exitcode.OK {
		return nil, code
	}
	return openWatcher(req.Verb, self, parent, lg)
}

// openWatcher is the shared constructor: OpenWatcher resolves identity first,
// Run's watch path arrives with it already resolved.
func openWatcher(verb string, self int, parent string, lg *log.Logger) (*Watcher, exitcode.Code) {
	db, code := openMsgDB(verb, parent, lg)
	if code != exitcode.OK {
		return nil, code
	}
	store, err := msgstore.New(db, parent)
	if err != nil {
		_ = db.Close()
		lg.Err("msg %s: %v", verb, err)
		return nil, exitcode.Backend
	}
	return &Watcher{db: db, store: store, self: self, parent: parent}, exitcode.OK
}

// Poll returns the unread messages addressed to the watcher (1:1 + board
// union) and marks them read in the same transaction inbox --mark-read uses:
// emit = delivered = read, and the board cursor advances to the highest
// delivered board post. An empty slice means nothing new.
//
// The ADR trade-off (Refs #496): reusing that transaction means messages are
// committed read before the caller writes them anywhere, so a crash between
// Poll returning and the output landing loses the batch. Labels are resolved
// BEFORE the marking read so at least no fallible step remains between the
// commit and the return.
func (w *Watcher) Poll() ([]WatchEvent, error) {
	labels, err := planPeerLabels(w.store, w.parent)
	if err != nil {
		return nil, err
	}
	msgs, _, err := w.store.Inbox(w.self, false, true, team.Now())
	if err != nil {
		return nil, err
	}
	events := make([]WatchEvent, len(msgs))
	for i, m := range msgs {
		ev := WatchEvent{Msg: m, FromLabel: memberDisplay(m.From, labels), ToLabel: "board", fromTask: labels[m.From]}
		if m.To != nil {
			ev.ToLabel = memberDisplay(*m.To, labels)
			ev.toTask = labels[*m.To]
		}
		events[i] = ev
	}
	return events, nil
}

// Close releases the Watcher's DB handle.
func (w *Watcher) Close() { _ = w.db.Close() }

// watchPoller is watchLoop's seam over *Watcher so tests can fake poll
// failures without a real DB.
type watchPoller interface {
	Poll() ([]WatchEvent, error)
}

// runMsgWatch executes the blocking `fanout msg watch` verb: construct the
// Watcher from the already-resolved identity, announce on stderr (stdout
// carries message lines only), and follow the inbox until a signal.
func runMsgWatch(req *Request, self int, parent string, pane msgstore.Peer, deps Deps, lg *log.Logger) exitcode.Code {
	w, code := openWatcher(req.Verb, self, parent, lg)
	if code != exitcode.OK {
		return code
	}
	defer w.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	fmt.Fprintf(lg.Stderr(), "msg watch: watching as %s under parent %s (interval %ds)\n",
		peerDisplayLabel(pane), parent, req.Interval)
	return watchLoop(w, req, sig, deps, lg)
}

// watchLoop polls immediately once (backlog delivery), then on every tick.
// Transient poll failures warn on stderr and keep the loop alive; the
// watchMaxFailures-th consecutive failure exits 4. A signal exits 0.
func watchLoop(w watchPoller, req *Request, sig <-chan os.Signal, deps Deps, lg *log.Logger) exitcode.Code {
	maxPolls := watchMaxPolls()
	interval := time.Duration(req.Interval) * time.Second
	failures := 0
	for polls := 1; ; polls++ {
		events, err := w.Poll()
		if err != nil {
			failures++
			lg.Warn("msg watch: poll failed (%d/%d): %v", failures, watchMaxFailures, err)
			if failures >= watchMaxFailures {
				lg.Err("msg watch: %d consecutive poll failures, giving up", watchMaxFailures)
				return exitcode.Backend
			}
		} else {
			failures = 0
			if code := emitWatchEvents(req, events, lg); code != exitcode.OK {
				return code
			}
		}
		if maxPolls > 0 && polls >= maxPolls {
			// A bounded run must not mask a broken backend as success: exit 4
			// when the run ends on unrecovered poll failures.
			if failures > 0 {
				return exitcode.Backend
			}
			return exitcode.OK
		}
		select {
		case <-sig:
			return exitcode.OK
		case <-deps.Tick(interval):
		}
	}
}

// watchMaxPolls reads the test-only poll bound; 0 means unbounded.
func watchMaxPolls() int {
	n, err := strconv.Atoi(os.Getenv(watchPollsEnv))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// emitWatchEvents writes one line per message to stdout: HumanLine, or with
// --json one compact JSON object per line (json.Marshal — deliberately not the
// indented writeMsgJSON form, so a line-oriented consumer never has to join
// fragments). The JSON schema is the shared msgMessageView.
func emitWatchEvents(req *Request, events []WatchEvent, lg *log.Logger) exitcode.Code {
	for _, e := range events {
		if !req.JSON {
			fmt.Fprintln(lg.Stdout(), e.HumanLine())
			continue
		}
		v := msgMessageView{Message: e.Msg, FromTask: e.fromTask, ToTask: e.toTask}
		out, err := json.Marshal(v)
		if err != nil {
			lg.Err("msg watch: failed to encode message: %v", err)
			return exitcode.Backend
		}
		fmt.Fprintln(lg.Stdout(), string(out))
	}
	return exitcode.OK
}
