package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/cliview"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

const msgUsage = `Usage: fanout msg <verb> [options] [body...]

Peer messaging between fanout panes (per-parent SQLite DB, see FANOUT_DB_PATH).

Read verbs:
  peers                          List registered peers.
  inbox [--all] [--mark-read]    Unread 1:1 messages + unread board posts.
  board [--all]                  Unread board posts (cursor-based).

Write verbs:
  send --to <N> [--kind K] <body...>   Send a 1:1 message (body words are joined).
  post [--kind K] <body...>            Post to the shared board.
  mark-read [--id N ... | --all]       Mark 1:1 messages read (--all also advances the board cursor).
  register                             Upsert this pane into the peers table.

Notify verbs:
  nudge <N>                      Best-effort: drop an inbox hint into peer #N's
                                 pane via tmux send-keys, but only when its
                                 agent is running. Never touches the DB (the
                                 message is already persisted by send); a pane
                                 that is gone, idle-unknown, or done is a no-op
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
  -h, --help       Show this help.

Exit codes: 0 success, 2 invalid invocation, 4 backend (SQLite) failure.
nudge is best-effort: operational failures (pane gone, agent not running,
send-keys failure) exit 0 with a warning, never 4.
`

// detectIdentity is swapped by tests to avoid depending on the developer's
// live tmux/state environment (same pattern as sleepBetweenIssues).
var detectIdentity = team.Detect

// nudgeText is the hint `msg nudge` types into a peer pane. It carries no
// message body on purpose — only a pointer to the inbox — so the actual
// content stays in the SQLite DB and the pane never displays a peer's words
// out of context. The em dash is intentional; it is a stable UTF-8 literal.
const nudgeText = "[fanout] peer message in your inbox — run: fanout msg inbox"

// listLivePanes and sendLiteralLine are the tmux seams nudge drives, declared
// as vars so unit tests can swap them without a live tmux server (same pattern
// as detectIdentity).
var (
	listLivePanes   = tmuxrun.ListLivePanes
	sendLiteralLine = tmuxrun.SendLiteralLine
	// loadStateStore resolves and loads the owner checkout's .fanout/state.json
	// read-only — the recipient's recorded pane id lives there, not in the
	// messages DB. It resolves the path the way team.Detect does
	// (FANOUT_STATE_PATH, else OwnerProjectRoot), NOT resolveStateRuntime: nudge
	// is normally run FROM a child worktree pane
	// (<owner>/.fanout/worktrees/<slug>), whose own git toplevel has no
	// state.json. Only OwnerProjectRoot climbs to the owner that holds it — the
	// same resolver every other msg verb uses (openMsgDB) — so resolveStateRuntime
	// would silently load an empty store and report every peer "not recorded".
	loadStateStore = func() (state.Store, error) {
		statePath := os.Getenv(fanoutStatePathEnv)
		if statePath != "" {
			if abs, err := filepath.Abs(statePath); err == nil {
				statePath = abs
			}
		} else {
			root, err := team.OwnerProjectRoot()
			if err != nil {
				return state.Store{}, err
			}
			statePath = state.Path(root)
		}
		return state.Load(statePath)
	}
)

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
	self, parent, pane, code := resolveMsgIdentity(flags, lg)
	if code != exitcode.OK {
		return code
	}

	// nudge reads neither the messages DB nor store identity: it resolves the
	// recipient from state.json and pushes via tmux, so short it out before
	// openMsgDB.
	if flags.verb == "nudge" {
		return runMsgNudge(flags, parent, lg)
	}

	db, code := openMsgDB(flags.verb, parent, lg)
	if code != exitcode.OK {
		return code
	}
	defer func() { _ = db.Close() }()
	store, err := msgstore.New(db, parent)
	if err != nil {
		lg.Err("msg %s: %v", flags.verb, err)
		return exitcode.Backend
	}
	return runMsgVerb(flags, store, self, parent, pane, lg)
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
	f := &msgFlags{verb: args[0], kind: "note"}
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
// plus its integer value (0 when non-numeric); resolveMemberNum picks the
// interpretation once the parent is resolved. Returns "" raw only on a hard
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

// resolveMemberNum resolves a member token (raw + its parsed int) to the peer
// number for the now-known parent. Under a plan parent the token is a task id
// (even an all-digit one), mapped through TaskPeerNum; under an issue/Project
// parent it must be a non-zero integer. This is the single seam where the
// int-vs-task-id ambiguity of an all-digit token is decided.
func resolveMemberNum(verb, flag, raw string, intVal int, parent string, lg *log.Logger) (int, exitcode.Code) {
	if team.IsPlanParent(parent) {
		if !rePlanTaskID.MatchString(raw) {
			lg.Err("msg %s: under a plan parent, %s must be a task id (lowercase kebab-case), got: %s", verb, flag, raw)
			return 0, exitcode.Invocation
		}
		return team.TaskPeerNum(parent, raw), exitcode.OK
	}
	if intVal == 0 {
		// parseMemberToken only yields intVal 0 for a task-id-shaped token, so a
		// 0 here means a task id was passed under a numeric parent.
		lg.Err("msg %s: %s %q is a task id, only valid under a plan parent; issue/project peers are addressed by number", verb, flag, raw)
		return 0, exitcode.Invocation
	}
	return intVal, exitcode.OK
}

// recipientLabel renders the recipient as the user addressed it: the task id
// under a plan parent, "#<issue>" otherwise. f.to must already be resolved.
func recipientLabel(f *msgFlags, parent string) string {
	if team.IsPlanParent(parent) {
		return f.toRaw
	}
	return "#" + strconv.Itoa(f.to)
}

// recipientFlagLabel names the recipient flag for error messages: nudge's
// positional "target", send's "--to".
func recipientFlagLabel(verb string) string {
	if verb == "nudge" {
		return "target"
	}
	return "--to"
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

// resolveMsgIdentity resolves who is speaking (self) and which team DB to use
// (parent). Explicit --self/--parent win per field; missing fields fall back
// to pane detection. peers needs only parent.
func resolveMsgIdentity(f *msgFlags, lg *log.Logger) (int, string, msgstore.Peer, exitcode.Code) {
	self, parent := f.self, f.parent
	pane := msgstore.Peer{Issue: self}
	// nudge, like peers, only needs the parent (to scope state.json) — it
	// targets a peer, never speaks as self — so it does not require self to be
	// detectable.
	needSelf := f.verb != "peers" && f.verb != "nudge"
	// An explicit --self task id (selfRaw) is "self given" too; it is resolved
	// to a synthetic number below once the parent is known.
	haveSelf := f.self != 0 || f.selfRaw != ""
	if (needSelf && !haveSelf) || parent == "" {
		id, err := detectIdentity()
		if err != nil {
			lg.Err("msg %s: could not detect this pane (%v); pass %s", f.verb, err, msgMissingIdentityFlags(needSelf && !haveSelf, parent == ""))
			return 0, "", msgstore.Peer{}, exitcode.Invocation
		}
		if !haveSelf {
			self = id.Issue
			pane = msgstore.Peer{
				Issue:        id.Issue,
				TaskID:       id.TaskID,
				PaneID:       id.Pane.PaneID,
				Slug:         id.Pane.Slug,
				WorktreePath: id.Pane.WorktreePath,
				Agent:        id.Pane.Agent,
				DisplayName:  id.Pane.DisplayName,
			}
		}
		if parent == "" {
			parent = id.Parent
		}
	}
	if parent == "" {
		lg.Err("msg %s: parent is unknown; pass --parent <ref>", f.verb)
		return 0, "", msgstore.Peer{}, exitcode.Invocation
	}
	// An explicit --self resolves against the now-known parent: a task id under
	// a plan parent, a non-zero issue number otherwise.
	if f.selfRaw != "" {
		n, code := resolveMemberNum(f.verb, "--self", f.selfRaw, f.self, parent, lg)
		if code != exitcode.OK {
			return 0, "", msgstore.Peer{}, code
		}
		self, pane.Issue = n, n
		if team.IsPlanParent(parent) {
			pane.TaskID = f.selfRaw
		} else {
			pane.TaskID = ""
		}
	}
	// Negative self is legitimate: manual panes carry synthetic numbers
	// (-1, -2, ...) under the @manual parent, and plan tasks carry synthetic
	// negatives under plan:<slug>. Only zero means unknown.
	if needSelf && self == 0 {
		lg.Err("msg %s: self issue is unknown; pass --self <issue|task-id>", f.verb)
		return 0, "", msgstore.Peer{}, exitcode.Invocation
	}
	// Resolve the --to/nudge recipient now that the parent is known: an
	// all-digit token routes to a task id under a plan parent, an issue number
	// otherwise.
	if f.toRaw != "" {
		n, code := resolveMemberNum(f.verb, recipientFlagLabel(f.verb), f.toRaw, f.to, parent, lg)
		if code != exitcode.OK {
			return 0, "", msgstore.Peer{}, code
		}
		f.to = n
	}
	return self, parent, pane, exitcode.OK
}

// msgMissingIdentityFlags names only the flags the user actually still owes,
// so `--self 70` without --parent is told about --parent alone.
func msgMissingIdentityFlags(needSelf, needParent bool) string {
	switch {
	case needSelf && needParent:
		return "--self <issue|task-id> and --parent <ref>"
	case needSelf:
		return "--self <issue|task-id>"
	default:
		return "--parent <ref>"
	}
}

// openMsgDB resolves the team DB path and opens it with the schema ensured.
// FANOUT_DB_PATH bypasses git entirely so orchestrators outside a checkout
// can still join; otherwise the owning project root names the DB.
func openMsgDB(verb, parent string, lg *log.Logger) (*sql.DB, exitcode.Code) {
	root := ""
	if os.Getenv(team.DBPathEnv) == "" {
		var err error
		root, err = team.OwnerProjectRoot()
		if err != nil {
			lg.Err("msg %s: %v; set %s or run inside the repository", verb, err, team.DBPathEnv)
			return nil, exitcode.Invocation
		}
	}
	path := team.DBPath(root, parent)
	db, err := team.Open(path)
	if err != nil {
		lg.Err("msg %s: %v", verb, err)
		return nil, exitcode.Backend
	}
	if err := team.EnsureSchema(db); err != nil {
		_ = db.Close()
		lg.Err("msg %s: %v", verb, err)
		return nil, exitcode.Backend
	}
	return db, exitcode.OK
}

func runMsgVerb(f *msgFlags, store *msgstore.Store, self int, parent string, pane msgstore.Peer, lg *log.Logger) exitcode.Code {
	now := team.Now()
	switch f.verb {
	case "peers":
		return runMsgPeers(f, store, parent, lg)
	case "inbox":
		msgs, marked, err := store.Inbox(self, f.all, f.markRead, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgMessages(f, store, self, parent, msgs, marked, lg)
	case "board":
		msgs, err := store.Board(self, f.all)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgMessages(f, store, self, parent, msgs, nil, lg)
	case "send":
		msg, err := store.Send(self, f.to, f.kind, f.body, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgResult(f, msgSendView(msg, parent, pane.TaskID, f.toRaw), fmt.Sprintf("sent #%d to %s", msg.ID, recipientLabel(f, parent)), lg)
	case "post":
		msg, err := store.Post(self, f.kind, f.body, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgResult(f, msgSendView(msg, parent, pane.TaskID, ""), fmt.Sprintf("posted #%d to the board", msg.ID), lg)
	case "mark-read":
		return runMsgMarkRead(f, store, self, pane.TaskID, now, lg)
	case "register":
		peer, err := store.Register(pane, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgResult(f, msgRegisterReport{Peer: peer}, fmt.Sprintf("registered peer %s", peerDisplayLabel(peer)), lg)
	}
	// Unreachable while msgVerbFlags and this switch stay in sync; fail loud
	// instead of silently exiting so a half-added verb is caught immediately.
	lg.Err("msg: internal error: verb %s parsed but not implemented", f.verb)
	return exitcode.Invocation
}

type msgMessagesReport struct {
	Self int `json:"self"`
	// SelfTask, FromTask, and ToTask are populated only for plan parents, where
	// the numeric members are negative synthetic peer numbers; they carry the
	// task ids automation actually addresses by. omitempty keeps issue/Project
	// JSON byte-identical.
	SelfTask   string               `json:"selfTask,omitempty"`
	Parent     string               `json:"parent"`
	All        bool                 `json:"all"`
	Messages   []msgMessageView     `json:"messages"`
	MarkedRead *msgstore.MarkResult `json:"marked_read,omitempty"`
}

// msgMessageView is a message plus, for plan parents, the task ids of its
// from/to members. The embedded Message promotes its fields to the top level,
// so issue-mode JSON (empty FromTask/ToTask) matches the bare Message encoding.
type msgMessageView struct {
	msgstore.Message
	FromTask string `json:"fromTask,omitempty"`
	ToTask   string `json:"toTask,omitempty"`
}

// msgSendView is the JSON encoding of a send/post echo: the raw message for
// issue/Project parents (byte-identical), or a task-id-enriched view for plan
// parents so a sending automation can reuse fromTask/toTask from the response
// without reverse-engineering the synthetic numbers. fromTask is the sender's
// task id (the resolved pane's TaskID); toTask is the recipient's (the raw --to
// token), unset for board posts (msg.To == nil).
func msgSendView(msg msgstore.Message, parent, fromTask, toTask string) any {
	if !team.IsPlanParent(parent) {
		return msg
	}
	v := msgMessageView{Message: msg, FromTask: fromTask}
	if msg.To != nil {
		v.ToTask = toTask
	}
	return v
}

// msgMessageViews attaches plan task ids to each message's from/to from labels.
// With a nil labels map (issue/Project parents) the views carry no task ids.
func msgMessageViews(msgs []msgstore.Message, labels map[int]string) []msgMessageView {
	views := make([]msgMessageView, len(msgs))
	for i, m := range msgs {
		v := msgMessageView{Message: m}
		v.FromTask = labels[m.From]
		if m.To != nil {
			v.ToTask = labels[*m.To]
		}
		views[i] = v
	}
	return views
}

type msgPeersReport struct {
	Parent string          `json:"parent"`
	Peers  []msgstore.Peer `json:"peers"`
}

type msgMarkReadReport struct {
	Self        int     `json:"self"`
	SelfTask    string  `json:"selfTask,omitempty"`
	MarkedIDs   []int64 `json:"marked_ids"`
	BoardCursor *int64  `json:"board_cursor,omitempty"`
}

type msgRegisterReport struct {
	Peer msgstore.Peer `json:"peer"`
}

func runMsgPeers(f *msgFlags, store *msgstore.Store, parent string, lg *log.Logger) exitcode.Code {
	peers, err := store.Peers()
	if err != nil {
		return msgBackendErr(f.verb, err, lg)
	}
	if f.json {
		return writeMsgJSON(msgPeersReport{Parent: parent, Peers: peers}, lg)
	}
	writeMsgPeersTable(peers, lg)
	return exitcode.OK
}

// writeMsgMessages is the shared renderer for the inbox and board views.
// marked is non-nil only for `inbox --mark-read`. For a plan parent it maps the
// synthetic peer numbers in From/To back to task ids for a readable table.
func writeMsgMessages(f *msgFlags, store *msgstore.Store, self int, parent string, msgs []msgstore.Message, marked *msgstore.MarkResult, lg *log.Logger) exitcode.Code {
	// labels is nil for issue/Project parents (no peers query, no task ids) and
	// maps synthetic numbers to task ids for plan parents — used by both the
	// JSON enrichment and the human table.
	labels, code := msgMemberLabels(store, parent, lg)
	if code != exitcode.OK {
		return code
	}
	if f.json {
		return writeMsgJSON(msgMessagesReport{
			Self:       self,
			SelfTask:   labels[self],
			Parent:     parent,
			All:        f.all,
			Messages:   msgMessageViews(msgs, labels),
			MarkedRead: marked,
		}, lg)
	}
	writeMsgMessagesTable(msgs, f.all, labels, lg)
	if marked != nil {
		lg.Ok("marked %d message(s) read, board cursor at %d", len(marked.MessageIDs), marked.BoardCursor)
	}
	return exitcode.OK
}

// msgMemberLabels maps synthetic peer numbers to plan task ids so inbox/board
// rows render readable FROM/TO. It returns nil for non-plan parents, so numeric
// issue/Project views keep rendering "#<n>" with no extra DB read.
func msgMemberLabels(store *msgstore.Store, parent string, lg *log.Logger) (map[int]string, exitcode.Code) {
	if !team.IsPlanParent(parent) {
		return nil, exitcode.OK
	}
	peers, err := store.Peers()
	if err != nil {
		return nil, msgBackendErr("inbox", err, lg)
	}
	labels := make(map[int]string, len(peers))
	for _, p := range peers {
		if p.TaskID != "" {
			labels[p.Issue] = p.TaskID
		}
	}
	return labels, exitcode.OK
}

// memberDisplay renders a peer number as its plan task id when known, else as
// "#<n>". A nil labels map (issue/Project parents) always yields "#<n>".
func memberDisplay(num int, labels map[int]string) string {
	if id, ok := labels[num]; ok {
		return id
	}
	return "#" + strconv.Itoa(num)
}

// peerDisplayLabel is the human label for a registered peer: the task id for a
// plan-task peer, "#<issue>" for a numeric issue peer.
func peerDisplayLabel(p msgstore.Peer) string {
	if p.TaskID != "" {
		return p.TaskID
	}
	return "#" + strconv.Itoa(p.Issue)
}

func runMsgMarkRead(f *msgFlags, store *msgstore.Store, self int, selfTask, now string, lg *log.Logger) exitcode.Code {
	// selfTask is the reader's plan task id ("" for issue/Project parents), so
	// plan-mode --json surfaces it alongside the synthetic Self number.
	report := msgMarkReadReport{Self: self, SelfTask: selfTask}
	if f.all {
		marked, cursor, err := store.MarkReadAll(self, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		report.MarkedIDs = marked
		report.BoardCursor = &cursor
	} else {
		marked, err := store.MarkReadIDs(self, f.ids, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		report.MarkedIDs = marked
	}
	summary := fmt.Sprintf("marked %d message(s) read", len(report.MarkedIDs))
	if report.BoardCursor != nil {
		summary += fmt.Sprintf(", board cursor at %d", *report.BoardCursor)
	}
	return writeMsgResult(f, report, summary, lg)
}

func writeMsgResult(f *msgFlags, report any, summary string, lg *log.Logger) exitcode.Code {
	if f.json {
		return writeMsgJSON(report, lg)
	}
	lg.Ok("%s", summary)
	return exitcode.OK
}

func writeMsgJSON(report any, lg *log.Logger) exitcode.Code {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		lg.Err("msg: failed to encode report: %v", err)
		return exitcode.Backend
	}
	fmt.Fprintln(lg.Stdout(), string(out))
	return exitcode.OK
}

func writeMsgMessagesTable(msgs []msgstore.Message, all bool, labels map[int]string, lg *log.Logger) {
	out := lg.Stdout()
	if len(msgs) == 0 {
		if all {
			fmt.Fprintln(out, "no messages")
		} else {
			fmt.Fprintln(out, "no unread messages")
		}
		return
	}
	headers := []string{"ID", "FROM", "TO", "KIND", "CREATED", "BODY"}
	rows := make([][]string, 0, len(msgs))
	for _, m := range msgs {
		to := "board"
		if m.To != nil {
			to = memberDisplay(*m.To, labels)
		}
		rows = append(rows, []string{
			strconv.FormatInt(m.ID, 10),
			memberDisplay(m.From, labels),
			to,
			m.Kind,
			m.CreatedAt,
			msgTableBody(m.Body),
		})
	}
	colors := lg.Colors()
	dims := make([]bool, len(msgs))
	for i, m := range msgs {
		dims[i] = m.ReadAt != nil
	}
	writeMsgTable(out, headers, rows, dims, colors)
}

func writeMsgPeersTable(peers []msgstore.Peer, lg *log.Logger) {
	out := lg.Stdout()
	if len(peers) == 0 {
		fmt.Fprintln(out, "no peers registered")
		return
	}
	headers := []string{"PEER", "SLUG", "AGENT", "DISPLAY_NAME", "PANE", "LAST_SEEN"}
	rows := make([][]string, 0, len(peers))
	for _, p := range peers {
		rows = append(rows, []string{
			peerDisplayLabel(p),
			cliview.DashIfEmpty(p.Slug),
			cliview.DashIfEmpty(p.Agent),
			cliview.DashIfEmpty(p.DisplayName),
			cliview.DashIfEmpty(p.PaneID),
			cliview.DashIfEmpty(p.LastSeen),
		})
	}
	writeMsgTable(out, headers, rows, make([]bool, len(peers)), lg.Colors())
}

// writeMsgTable renders the shared header/separator/rows layout via the
// cliview table primitives. dims[i] dims row i (read messages in --all views).
func writeMsgTable(out io.Writer, headers []string, rows [][]string, dims []bool, colors log.Palette) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, col := range row {
			widths[i] = max(widths[i], len(col))
		}
	}
	fmt.Fprintln(out, cliview.TableLine(headers, widths))
	separators := make([]string, len(headers))
	for i := range headers {
		separators[i] = strings.Repeat("-", widths[i])
	}
	fmt.Fprintln(out, cliview.TableLine(separators, widths))
	for i, row := range rows {
		line := cliview.TableLine(row, widths)
		if dims[i] {
			line = cliview.ColorWrap(colors.Dim, colors.Reset, line)
		}
		fmt.Fprintln(out, line)
	}
}

// msgTableBody flattens a message body to one table cell.
func msgTableBody(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func msgBackendErr(verb string, err error, lg *log.Logger) exitcode.Code {
	lg.Err("msg %s: %v", verb, err)
	return exitcode.Backend
}

// --- nudge -------------------------------------------------------------

// msgNudgeReport is the --json encoding of a nudge attempt. Nudged is true only
// when the send-keys actually went out; Reason explains every other outcome so
// automation can tell a delivered push from a best-effort no-op.
type msgNudgeReport struct {
	Target     int    `json:"target"`
	TargetTask string `json:"targetTask,omitempty"`
	PaneID     string `json:"pane_id"`
	AgentState string `json:"agent_state"`
	Nudged     bool   `json:"nudged"`
	Reason     string `json:"reason"`
}

// shouldNudge reports whether a pane in the given @fanout_agent_state should
// receive a send-keys nudge. Only "running" — a live agent launched by the
// fanout wrapper — qualifies. "done" (agent exited, bare shell), "" (unset:
// legacy or non-fanout pane), and any other value are no-op successes so a
// hint never lands in a shell or an unrelated pane.
//
// This is the deliberate re-design of the original "agentStatus == idle" gate:
// the dmux idle/analyzing/waiting/working enum no longer exists, and the only
// live signal (@fanout_agent_state) cannot distinguish a busy agent from one
// idle at its prompt. Gating on "running" is safe in practice because the
// supported agents (claude, codex) queue typed input during a turn rather than
// aborting it, so a nudge to a busy agent is picked up at its next checkpoint —
// the same pull the message already guarantees, only sooner.
func shouldNudge(agentState string) bool {
	return agentState == "running"
}

// runMsgNudge resolves peer f.to's recorded pane from state.json and pushes the
// inbox hint when its agent is running. Every operational miss (recipient
// absent, no pane id, pane gone, agent not running, send-keys failure) is a
// no-op SUCCESS with a warning/reason: the message is already persisted by
// send, so a failed nudge must never break messaging. Only invocation errors
// (handled earlier) exit non-zero.
func runMsgNudge(f *msgFlags, parent string, lg *log.Logger) exitcode.Code {
	st, err := loadStateStore()
	if err != nil {
		lg.Err("msg nudge: %v", err)
		return exitcode.Invocation
	}
	// Plan recipients are addressed by task id (state rows carry IssueNum 0),
	// so look them up by task id under a plan parent; issue/Project recipients
	// keep the numeric lookup.
	var pane state.Pane
	var found bool
	if team.IsPlanParent(parent) {
		pane, found = st.FindTask(parent, f.toRaw)
	} else {
		pane, found = st.Find(parent, f.to)
	}

	report := msgNudgeReport{Target: f.to, PaneID: pane.PaneID}
	if team.IsPlanParent(parent) {
		report.TargetTask = f.toRaw
	}
	switch {
	case !found:
		report.Reason = "recipient is not recorded in fanout state"
	case pane.PaneID == "":
		report.Reason = "recipient has no recorded pane"
	default:
		report.AgentState, report.Reason, report.Nudged = deliverNudge(pane)
	}
	return writeMsgNudgeResult(f, parent, report, lg)
}

// deliverNudge re-reads the live tmux panes immediately before sending (TOCTOU),
// confirms the recorded pane id STILL belongs to the recipient, and pushes the
// hint only when its agent is running. The recorded id alone is not enough:
// tmux reuses %N ids after a server restart, so an id-only check could nudge an
// unrelated pane that inherited the id — exactly the interruption accident the
// gate must avoid. matchLivePane applies the same id+worktree liveness check the
// dashboard uses (sessionview.paneAlive). It returns the observed agent state, a
// reason when nothing was sent, and whether the push went out; every miss
// (tmux down, pane gone/reused, agent not running, send failure) is a
// best-effort no-op because the message is already persisted.
func deliverNudge(pane state.Pane) (agentState, reason string, nudged bool) {
	panes, err := listLivePanes()
	if err != nil {
		return "", "tmux is unavailable", false
	}
	lp, ok := matchLivePane(panes, pane.PaneID, pane.WorktreePath)
	if !ok {
		return "", "recipient pane is gone or its id was reused", false
	}
	if !shouldNudge(lp.AgentState) {
		return lp.AgentState, fmt.Sprintf("agent is not running (state %q)", lp.AgentState), false
	}
	if err := sendLiteralLine(pane.PaneID, nudgeText); err != nil {
		return lp.AgentState, fmt.Sprintf("send-keys failed: %v", err), false
	}
	return lp.AgentState, "", true
}

// matchLivePane returns the live pane recorded as paneID only when it is still
// the recipient's pane: its id must be live AND it must sit at/under the
// recorded worktree, because tmux reuses %N ids across server restarts. A row
// without a recorded worktree (legacy) falls back to an id-only match. This
// mirrors sessionview.paneAlive — the dashboard's liveness convention — so a
// reused id is treated identically on both surfaces.
func matchLivePane(panes []tmuxrun.LivePane, paneID, worktree string) (tmuxrun.LivePane, bool) {
	if paneID == "" {
		return tmuxrun.LivePane{}, false
	}
	for _, lp := range panes {
		if lp.ID != paneID {
			continue
		}
		if strings.TrimSpace(worktree) == "" {
			return lp, true
		}
		wt := filepath.Clean(worktree)
		cp := filepath.Clean(lp.CurrentPath)
		if cp == wt || strings.HasPrefix(cp, wt+string(filepath.Separator)) {
			return lp, true
		}
		// id matched but the live pane moved off the recorded worktree: tmux
		// handed %N to an unrelated pane. Treat it as gone, never nudge it.
		return tmuxrun.LivePane{}, false
	}
	return tmuxrun.LivePane{}, false
}

func writeMsgNudgeResult(f *msgFlags, parent string, report msgNudgeReport, lg *log.Logger) exitcode.Code {
	if f.json {
		return writeMsgJSON(report, lg)
	}
	// Show the recipient as the user addressed it: "#<issue>" for numeric
	// peers, the task id for plan peers (report.Target holds the synthetic
	// number, which is not user-facing).
	label := recipientLabel(f, parent)
	if report.Nudged {
		lg.Ok("nudged %s", label)
	} else {
		lg.Warn("did not nudge %s: %s", label, report.Reason)
	}
	return exitcode.OK
}
