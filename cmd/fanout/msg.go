package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/peermsg"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

const msgUsage = `Usage: fanout msg <verb> [options] [body...]

Peer messaging between fanout panes (per-parent SQLite DB, see FANOUT_DB_PATH).

Read verbs:
  peers                          List registered peers.
  inbox [--all] [--mark-read]    Unread 1:1 messages + unread board posts.
  board [--all]                  Unread board posts (cursor-based).
  watch [--interval S]           Block and emit new inbox messages (1:1 +
                                 board) one per line as they arrive; emitted
                                 messages are marked read. Ctrl-C to stop.

Write verbs:
  send --to <N> [--kind K] <body...>   Send a 1:1 message (body words are joined).
  post [--kind K] <body...>            Post to the shared board.
  mark-read [--id N ... | --all]       Mark 1:1 messages read (--all also advances the board cursor).
  register                             Upsert this pane into the peers table.

Notify verbs:
  nudge <N>                      Best-effort: drop an inbox hint into peer #N's
                                 pane via tmux send-keys, but only when its
                                 agent can take queued input (state running /
                                 working / plan / idle). Never touches the DB
                                 (the message is already persisted by send); a
                                 pane that is gone, blocked on a permission
                                 prompt, state-unknown, or done is a no-op
                                 success, so a failed nudge never breaks
                                 messaging.

Options:
  --json           Emit JSON instead of the human-readable view.
  --self <N>       Act as child issue N (overrides pane detection).
  --parent <ref>   Parent issue number or Projects URL (overrides pane detection).
  --to <N>         send: recipient child issue number.
  --kind <K>       send/post: message kind (default: note).
  --id <N>         mark-read: message id (repeatable).
  --all            inbox/board: include read messages; mark-read: mark everything.
  --mark-read      inbox: mark the displayed messages read.
  --interval <S>   watch: poll interval in seconds, 1-86400 (default: 2).
  -h, --help       Show this help.

Exit codes: 0 success, 2 invalid invocation, 4 backend (SQLite) failure.
nudge is best-effort: operational failures (pane gone, agent not nudgeable,
send-keys failure) exit 0 with a warning, never 4.
`

type msgFlags struct {
	verb string
	json bool
	self int // 0 = not given; resolved synthetic number for selfRaw
	// selfRaw holds an explicit --self task id (plan parents) until the parent
	// is known and it can be mapped to a synthetic number; "" otherwise.
	selfRaw string
	parent  string
	to      int // 0 = not given; resolved synthetic number for toRaw
	// toRaw holds a --to/nudge task id (plan parents) until the parent is known
	// and it can be mapped to a synthetic number; "" otherwise.
	toRaw    string
	kind     string
	ids      []int64
	all      bool
	markRead bool
	body     string
	interval int // watch: poll seconds; defaulted to 2 at parse start
}

// request converts the parsed argv into the peermsg execution request.
func (f *msgFlags) request() peermsg.Request {
	return peermsg.Request{
		Verb:     f.verb,
		JSON:     f.json,
		Self:     f.self,
		SelfRaw:  f.selfRaw,
		Parent:   f.parent,
		To:       f.to,
		ToRaw:    f.toRaw,
		Kind:     f.kind,
		IDs:      f.ids,
		All:      f.all,
		MarkRead: f.markRead,
		Body:     f.body,
		Interval: f.interval,
	}
}

// msgVerbFlags is the per-verb flag allowlist. msgUniversalFlags are accepted
// by every verb. The known flag set is derived from these two tables
// (msgFlagKnown), so adding a flag here is the single registration point for
// parsing.
var msgVerbFlags = map[string]map[string]bool{
	"peers":     {},
	"inbox":     {"--all": true, "--mark-read": true},
	"board":     {"--all": true},
	"send":      {"--to": true, "--kind": true},
	"post":      {"--kind": true},
	"mark-read": {"--id": true, "--all": true},
	"register":  {},
	"watch":     {"--interval": true},
	// nudge's target is a positional <N>, not --to; universal
	// --json/--self/--parent still apply.
	"nudge": {},
}

var msgUniversalFlags = map[string]bool{"--json": true, "--self": true, "--parent": true}

func msgFlagKnown(flag string) bool {
	if msgUniversalFlags[flag] {
		return true
	}
	for _, allowed := range msgVerbFlags {
		if allowed[flag] {
			return true
		}
	}
	return false
}

// msgBodyVerbs are the verbs whose trailing positionals form the message body.
var msgBodyVerbs = map[string]bool{"send": true, "post": true}

// isMsgRequest detects the `fanout msg ...` subcommand, dispatched before
// cliflags.Parse (which requires an integer/Project-URL <parent> positional).
func isMsgRequest(args []string) bool {
	return len(args) > 0 && args[0] == "msg"
}

func cmdMsg(args []string, lg *log.Logger) exitcode.Code {
	flags, code := parseMsgFlags(args, lg)
	if flags == nil {
		return code // help (OK) or a parse error; either way the message is out
	}
	return peermsg.Run(flags.request(), peermsg.DefaultDeps(), lg)
}

// parseMsgFlags parses `msg` argv. A nil msgFlags means "stop with this
// code": exitcode.OK after printing help, exitcode.Invocation on any error.
func parseMsgFlags(args []string, lg *log.Logger) (*msgFlags, exitcode.Code) {
	if len(args) == 0 {
		fmt.Fprint(lg.Stderr(), msgUsage)
		return nil, exitcode.Invocation
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(lg.Stdout(), msgUsage)
		return nil, exitcode.OK
	}
	f := &msgFlags{verb: args[0], kind: "note", interval: 2}
	allowed, ok := msgVerbFlags[f.verb]
	if !ok {
		lg.Err("msg: unknown verb: %s", f.verb)
		fmt.Fprint(lg.Stderr(), msgUsage)
		return nil, exitcode.Invocation
	}
	var body []string
	terminated := false // a `--` ends flag parsing so bodies can carry --words
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !terminated {
			if a == "--" {
				terminated = true
				continue
			}
			// Help wins only while no body word has been seen; `send --to 71
			// ask about --help` must not silently print usage and exit 0
			// instead of sending. (A post-body --help still fails loudly as
			// an unknown option — use `--` to put flag-like words in a body.)
			if (a == "--help" || a == "-h") && len(body) == 0 {
				fmt.Fprint(lg.Stdout(), msgUsage)
				return nil, exitcode.OK
			}
		}
		if terminated || !strings.HasPrefix(a, "--") {
			if f.verb == "nudge" {
				if code := setNudgeTarget(f, a, lg); code != exitcode.OK {
					return nil, code
				}
				continue
			}
			if !msgBodyVerbs[f.verb] {
				lg.Err("msg %s: unexpected argument: %s", f.verb, a)
				return nil, exitcode.Invocation
			}
			body = append(body, a)
			continue
		}
		consumed, code := parseMsgFlag(f, allowed, args, i, lg)
		if code != exitcode.OK {
			return nil, code
		}
		i += consumed
	}
	f.body = strings.TrimSpace(strings.Join(body, " "))
	if code := validateMsgFlags(f, lg); code != exitcode.OK {
		return nil, code
	}
	return f, exitcode.OK
}

// parseMsgFlag consumes args[i] (and a value argument when the flag takes
// one), returning how many extra args were consumed.
func parseMsgFlag(f *msgFlags, allowed map[string]bool, args []string, i int, lg *log.Logger) (int, exitcode.Code) {
	a := args[i]
	name, inlineValue, hasInline := strings.Cut(a, "=")
	if hasInline {
		a = name
	}
	if !msgFlagKnown(a) {
		lg.Err("msg %s: unknown option: %s", f.verb, name)
		fmt.Fprint(lg.Stderr(), msgUsage)
		return 0, exitcode.Invocation
	}
	if !msgUniversalFlags[a] && !allowed[a] {
		lg.Err("msg %s: %s is not supported", f.verb, a)
		return 0, exitcode.Invocation
	}

	switch a {
	case "--json":
		f.json = true
	case "--all":
		f.all = true
	case "--mark-read":
		f.markRead = true
	default:
		value := inlineValue
		consumed := 0
		if !hasInline {
			if i+1 >= len(args) {
				lg.Err("msg %s: %s requires an argument", f.verb, a)
				return 0, exitcode.Invocation
			}
			value = args[i+1]
			consumed = 1
		}
		if code := setMsgFlagValue(f, a, value, lg); code != exitcode.OK {
			return 0, code
		}
		return consumed, exitcode.OK
	}
	if hasInline {
		lg.Err("msg %s: %s does not take a value", f.verb, name)
		return 0, exitcode.Invocation
	}
	return 0, exitcode.OK
}

// setNudgeTarget parses nudge's single positional target into f.to/f.toRaw.
// Like --to it accepts a non-zero issue number (manual @manual panes use
// negative synthetic numbers) or a plan task id, but rejects zero and other
// tokens; a second positional is an error — nudge targets exactly one peer.
// Reusing f.to/f.toRaw keeps the recipient field shared with send.
func setNudgeTarget(f *msgFlags, arg string, lg *log.Logger) exitcode.Code {
	if f.to != 0 || f.toRaw != "" {
		lg.Err("msg nudge: takes exactly one target (issue number or plan task id), got extra argument: %s", arg)
		return exitcode.Invocation
	}
	n, raw, code := parseMemberToken(f.verb, "target", arg, lg)
	if code != exitcode.OK {
		return code
	}
	f.to, f.toRaw = n, raw
	return exitcode.OK
}

// parseMemberToken parses a peer member token that is either a non-zero issue
// number (manual panes use negative synthetic numbers) or a lowercase
// kebab-case plan task id. It is parent-agnostic on purpose: an all-digit token
// like "123" is BOTH a valid issue number and a valid task id, and only the
// (not-yet-known) parent decides which. So it returns the raw token verbatim
// plus its integer value (0 when non-numeric); peermsg resolves the
// interpretation once the parent is known. Returns "" raw only on a hard
// parse error (a token that is neither a non-zero int nor a task-id shape).
func parseMemberToken(verb, flag, value string, lg *log.Logger) (int, string, exitcode.Code) {
	if n, err := strconv.Atoi(value); err == nil {
		// A bare numeric 0 is neither a valid issue number nor — unless it is
		// also a task-id shape ("0") — a valid token.
		if n == 0 && !rePlanTaskID.MatchString(value) {
			lg.Err("msg %s: %s must be a non-zero issue number or a plan task id, got: %s", verb, flag, value)
			return 0, "", exitcode.Invocation
		}
		return n, value, exitcode.OK
	}
	if rePlanTaskID.MatchString(value) {
		return 0, value, exitcode.OK
	}
	lg.Err("msg %s: %s must be a non-zero issue number or a plan task id, got: %s", verb, flag, value)
	return 0, "", exitcode.Invocation
}

// normalizeMsgParentRef canonicalizes a --parent value. A plan parent
// (plan:<slug>) is accepted verbatim so plan panes can scope their own DB;
// everything else defers to cliflags.NormalizeParentRef (issue number /
// Projects v2 URL).
func normalizeMsgParentRef(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if slug, ok := strings.CutPrefix(raw, "plan:"); ok {
		if rePlanTaskID.MatchString(slug) {
			return "plan:" + slug, true
		}
		return "", false
	}
	return cliflags.NormalizeParentRef(raw)
}

func setMsgFlagValue(f *msgFlags, flag, value string, lg *log.Logger) exitcode.Code {
	switch flag {
	case "--self":
		n, raw, code := parseMemberToken(f.verb, "--self", value, lg)
		if code != exitcode.OK {
			return code
		}
		f.self, f.selfRaw = n, raw
	case "--parent":
		// Canonicalize so an explicit ref matches the parent recorded at
		// pane-launch time: "0068" and "68" must scope the same messages, and
		// plan:<slug> is accepted verbatim for issue-less plan runs.
		ref, ok := normalizeMsgParentRef(value)
		if !ok {
			lg.Err("msg %s: --parent must be an issue number, a GitHub Projects v2 URL, or plan:<slug>, got: %s", f.verb, value)
			return exitcode.Invocation
		}
		f.parent = ref
	case "--to":
		n, raw, code := parseMemberToken(f.verb, "--to", value, lg)
		if code != exitcode.OK {
			return code
		}
		f.to, f.toRaw = n, raw
	case "--kind":
		if strings.TrimSpace(value) == "" {
			lg.Err("msg %s: --kind must not be empty", f.verb)
			return exitcode.Invocation
		}
		f.kind = value
	case "--id":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n <= 0 {
			lg.Err("msg %s: --id must be a positive message id, got: %s", f.verb, value)
			return exitcode.Invocation
		}
		f.ids = append(f.ids, n)
	case "--interval":
		// The upper bound keeps time.Duration(interval)*time.Second far from
		// int64 overflow (which would yield a negative duration and turn the
		// watch tick into a busy loop); a day-long poll interval is already
		// past any sensible use.
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 86400 {
			lg.Err("msg %s: --interval must be an integer between 1 and 86400 (seconds), got: %s", f.verb, value)
			return exitcode.Invocation
		}
		f.interval = n
	}
	return exitcode.OK
}

func validateMsgFlags(f *msgFlags, lg *log.Logger) exitcode.Code {
	switch f.verb {
	case "send":
		if f.to == 0 && f.toRaw == "" {
			lg.Err("msg send: --to <issue|task-id> is required")
			return exitcode.Invocation
		}
		fallthrough
	case "post":
		if f.body == "" {
			lg.Err("msg %s: message body is required", f.verb)
			return exitcode.Invocation
		}
	case "mark-read":
		if f.all == (len(f.ids) > 0) {
			lg.Err("msg mark-read: pass either --id <n> (repeatable) or --all")
			return exitcode.Invocation
		}
	case "nudge":
		// setNudgeTarget already rejected zero/unparseable tokens; a still-empty
		// recipient means no positional target was supplied at all.
		if f.to == 0 && f.toRaw == "" {
			lg.Err("msg nudge: target <issue|task-id> is required")
			return exitcode.Invocation
		}
	}
	return exitcode.OK
}
