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

// DefaultWatchInterval and MaxWatchInterval bound the watch poll interval in
// seconds. The CLI parser (cmd/fanout) validates against them and
// watchTickInterval clamps in-process callers with the same values, so the
// two layers cannot drift.
const (
	DefaultWatchInterval = 2
	MaxWatchInterval     = 86400
)

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
// (and the codex bridge injects into an agent turn): sanitizeWatchLine
// collapses the body to a single line (\r/\n are C0 runes) and blanks control
// bytes so a peer-controlled body or kind cannot smuggle terminal escape
// sequences (or forge "[fanout msg #N]" framing via cursor movement) into the
// watching terminal or an agent prompt.
func (e WatchEvent) HumanLine() string {
	return sanitizeWatchLine(fmt.Sprintf("[fanout msg #%d] %s -> %s (%s): %s",
		e.Msg.ID, e.FromLabel, e.ToLabel, e.Msg.Kind, e.Msg.Body))
}

// sanitizeWatchLine blanks the runes a terminal (or a text-rendering agent
// surface) could interpret as control state: C0 bytes, DEL, C1 controls
// (U+0080-U+009F — xterm-family terminals honor the single-rune CSI U+009B and
// OSC U+009D in UTF-8 mode), and Unicode bidi embedding/override/isolate
// characters (visual reordering can spoof the from/to framing). Without the
// introducers the printable remainder of any escape sequence is inert.
func sanitizeWatchLine(s string) string {
	// Zero-width joiners/non-joiners and soft hyphens stay: they carry no
	// hidden content of their own and blanking them breaks emoji sequences and
	// Indic scripts. The tag block, by contrast, encodes an entire invisible
	// text channel and has no legitimate use in a message line.
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7F, // C0 + DEL
			r >= 0x80 && r <= 0x9F,       // C1 (CSI, OSC, ...)
			r >= 0x202A && r <= 0x202E,   // bidi embeddings/overrides
			r >= 0x2066 && r <= 0x2069,   // bidi isolates
			r == 0x2028 || r == 0x2029,   // Unicode line/paragraph separators
			r >= 0xE0000 && r <= 0xE007F: // tag characters (invisible text)
			return ' '
		}
		return r
	}, s)
}

// Watcher follows one pane's inbox (1:1 + board) on the per-parent team DB.
// It owns its DB handle — Close releases it — so it can outlive the opening
// call frame; peermsg.Run's own defer db.Close() never applies to it.
// A Watcher is not goroutine-safe: serialize Poll and Close on one goroutine.
// Consumers are listed on OpenWatcher.
type Watcher struct {
	db     *sql.DB
	store  *msgstore.Store
	self   int
	parent string
	// labels caches the last successful plan task-id resolution so a transient
	// Peers failure after the mark-read commit degrades to stale labels instead
	// of dropping already-marked messages.
	labels map[int]string
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
	self, parent, _, code := resolveMsgIdentity(&req, deps.withDefaults(), lg)
	if code != exitcode.OK {
		return nil, code
	}
	return openWatcher(req.Verb, self, parent, lg)
}

// openWatcher is the shared constructor: OpenWatcher resolves identity first,
// Run's watch path arrives with it already resolved.
func openWatcher(verb string, self int, parent string, lg *log.Logger) (*Watcher, exitcode.Code) {
	db, store, code := openMsgStore(verb, parent, lg)
	if code != exitcode.OK {
		return nil, code
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
// Poll returning and the output landing loses the batch. Plan task-id labels
// are therefore resolved best-effort AFTER the commit — resolving them fresh
// each poll picks up a sender that registered just before sending, and a
// transient Peers failure falls back to the previous poll's cache (degraded
// "#<n>" labels at worst) instead of erroring out messages that are already
// marked read.
func (w *Watcher) Poll() ([]WatchEvent, error) {
	now := team.Now()
	msgs, marked, err := w.store.Inbox(w.self, false, true, now)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	// The rows were selected before the same transaction set read_at, so stamp
	// the committed value back on: an emitted event must report itself read
	// (emit = delivered = read), or a stream consumer reconciling against
	// `msg inbox --all` sees every delivered message as still-unread.
	if marked != nil {
		ids := make(map[int64]bool, len(marked.MessageIDs))
		for _, id := range marked.MessageIDs {
			ids[id] = true
		}
		for i := range msgs {
			if ids[msgs[i].ID] {
				msgs[i].ReadAt = &now
			}
		}
	}
	labels := w.labels
	if fresh, err := planPeerLabels(w.store, w.parent); err == nil {
		labels, w.labels = fresh, fresh
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
	// Relay the first signal and immediately restore the default disposition:
	// if the loop is stuck in a blocked stdout write (full pipe, stalled
	// consumer) it never reaches its select, and without the reset a second
	// Ctrl-C / SIGTERM would queue unread instead of killing the process. The
	// relay goroutine may stay blocked on a clean loop exit; the CLI process
	// terminates right after Run returns, so it never outlives anything.
	loopSig := make(chan os.Signal, 1)
	go func() {
		s := <-sig
		signal.Reset(os.Interrupt, syscall.SIGTERM)
		loopSig <- s
	}()

	fmt.Fprintf(lg.Stderr(), "msg watch: watching as %s under parent %s (interval %ds)\n",
		peerDisplayLabel(pane), parent, int(watchTickInterval(req.Interval)/time.Second))
	return watchLoop(w, req, loopSig, deps, lg)
}

// watchLoop polls immediately once (backlog delivery), then on every tick.
// Transient poll failures warn on stderr and keep the loop alive; the
// watchMaxFailures-th consecutive failure exits 4. A signal exits 0.
func watchLoop(w watchPoller, req *Request, sig <-chan os.Signal, deps Deps, lg *log.Logger) exitcode.Code {
	maxPolls := watchMaxPolls()
	if maxPolls > 0 {
		// The bound silently turns a blocking follower into a finite run, so a
		// leaked env var (.envrc, CI job) must not masquerade as a healthy
		// watch: announce it on stderr.
		lg.Warn("msg watch: %s=%d is set (test-only), exiting after %d poll(s)", watchPollsEnv, maxPolls, maxPolls)
	}
	interval := watchTickInterval(req.Interval)
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

// watchTickInterval clamps the poll interval for in-process callers of the
// exported Request (the CLI parser enforces 1-86400 before it gets here): a
// zero-value Interval falls to the documented default instead of busy-looping
// on time.After(0), and the ceiling keeps the duration far from int64
// overflow (which would go negative and also busy-loop).
func watchTickInterval(seconds int) time.Duration {
	if seconds < 1 {
		seconds = DefaultWatchInterval
	}
	if seconds > MaxWatchInterval {
		seconds = MaxWatchInterval
	}
	return time.Duration(seconds) * time.Second
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
//
// Write errors are fatal (exit 4), not warnings: the batch is already marked
// read, so a persistently broken stdout (ENOSPC, closed pipe without SIGPIPE)
// silently discarding every future message would be worse than stopping —
// unread state is preserved for everything not yet polled.
func emitWatchEvents(req *Request, events []WatchEvent, lg *log.Logger) exitcode.Code {
	for _, e := range events {
		line := ""
		if req.JSON {
			out, err := json.Marshal(msgMessageView{Message: e.Msg, FromTask: e.fromTask, ToTask: e.toTask})
			if err != nil {
				lg.Err("msg watch: failed to encode message: %v", err)
				return exitcode.Backend
			}
			line = string(out)
		} else {
			line = e.HumanLine()
		}
		if _, err := fmt.Fprintln(lg.Stdout(), line); err != nil {
			lg.Err("msg watch: failed to write to stdout: %v", err)
			return exitcode.Backend
		}
	}
	return exitcode.OK
}
