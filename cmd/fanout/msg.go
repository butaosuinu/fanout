package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/msgstore"
	"github.com/butaosuinu/fanout/internal/team"
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

Options:
  --json           Emit JSON instead of the human-readable view.
  --self <N>       Act as child issue N (overrides pane detection).
  --parent <ref>   Parent issue number or Projects URL (overrides pane detection).
  --dry-run        Write verbs only: print what would be written, touch nothing.
  --to <N>         send: recipient child issue number.
  --kind <K>       send/post: message kind (default: note).
  --id <N>         mark-read: message id (repeatable).
  --all            inbox/board: include read messages; mark-read: mark everything.
  --mark-read      inbox: mark the displayed messages read.
  -h, --help       Show this help.

Exit codes: 0 success, 2 invalid invocation, 4 backend (SQLite) failure.
`

// detectIdentity is swapped by tests to avoid depending on the developer's
// live tmux/state environment (same pattern as sleepBetweenIssues).
var detectIdentity = team.Detect

type msgFlags struct {
	verb     string
	json     bool
	dryRun   bool
	self     int // 0 = not given; --self only accepts positive integers
	parent   string
	to       int // 0 = not given; --to only accepts positive integers
	kind     string
	ids      []int64
	all      bool
	markRead bool
	body     string
}

// msgVerbFlags is the per-verb flag allowlist. msgUniversalFlags are accepted
// by every verb; --dry-run is write-verb only per the #70 contract. The known
// flag set is derived from these two tables (msgFlagKnown), so adding a flag
// here is the single registration point for parsing.
var msgVerbFlags = map[string]map[string]bool{
	"peers":     {},
	"inbox":     {"--all": true, "--mark-read": true},
	"board":     {"--all": true},
	"send":      {"--dry-run": true, "--to": true, "--kind": true},
	"post":      {"--dry-run": true, "--kind": true},
	"mark-read": {"--dry-run": true, "--id": true, "--all": true},
	"register":  {"--dry-run": true},
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

	if flags.dryRun {
		return printMsgDryRun(flags, self, parent, pane, lg)
	}

	db, code := openMsgDB(flags.verb, parent, lg)
	if code != exitcode.OK {
		return code
	}
	defer func() { _ = db.Close() }()
	return runMsgVerb(flags, msgstore.New(db, parent), self, parent, pane, lg)
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
			// try -h here` must send the message, not print usage and exit 0.
			if a == "--help" || (a == "-h" && len(body) == 0) {
				fmt.Fprint(lg.Stdout(), msgUsage)
				return nil, exitcode.OK
			}
		}
		if terminated || !strings.HasPrefix(a, "--") {
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
	case "--dry-run":
		f.dryRun = true
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

func setMsgFlagValue(f *msgFlags, flag, value string, lg *log.Logger) exitcode.Code {
	switch flag {
	case "--self":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			lg.Err("msg %s: --self must be a positive issue number, got: %s", f.verb, value)
			return exitcode.Invocation
		}
		f.self = n
	case "--parent":
		// Canonicalize so an explicit ref matches the parent recorded at
		// pane-launch time: "0068" and "68" must scope the same messages.
		ref, ok := cliflags.NormalizeParentRef(value)
		if !ok {
			lg.Err("msg %s: --parent must be an issue number or a GitHub Projects v2 URL, got: %s", f.verb, value)
			return exitcode.Invocation
		}
		f.parent = ref
	case "--to":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			lg.Err("msg %s: --to must be a positive issue number, got: %s", f.verb, value)
			return exitcode.Invocation
		}
		f.to = n
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
		if f.to == 0 {
			lg.Err("msg send: --to <issue> is required")
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
	}
	return exitcode.OK
}

// resolveMsgIdentity resolves who is speaking (self) and which team DB to use
// (parent). Explicit --self/--parent win per field; missing fields fall back
// to pane detection. peers needs only parent.
func resolveMsgIdentity(f *msgFlags, lg *log.Logger) (int, string, msgstore.Peer, exitcode.Code) {
	self, parent := f.self, f.parent
	pane := msgstore.Peer{Issue: self}
	needSelf := f.verb != "peers"
	if (needSelf && self == 0) || parent == "" {
		id, err := detectIdentity()
		if err != nil {
			lg.Err("msg %s: could not detect this pane (%v); pass %s", f.verb, err, msgMissingIdentityFlags(needSelf && self == 0, parent == ""))
			return 0, "", msgstore.Peer{}, exitcode.Invocation
		}
		if self == 0 {
			self = id.Issue
			pane = msgstore.Peer{
				Issue:        id.Issue,
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
	if needSelf && self <= 0 {
		lg.Err("msg %s: self issue is unknown; pass --self <issue>", f.verb)
		return 0, "", msgstore.Peer{}, exitcode.Invocation
	}
	return self, parent, pane, exitcode.OK
}

// msgMissingIdentityFlags names only the flags the user actually still owes,
// so `--self 70` without --parent is told about --parent alone.
func msgMissingIdentityFlags(needSelf, needParent bool) string {
	switch {
	case needSelf && needParent:
		return "--self <issue> and --parent <ref>"
	case needSelf:
		return "--self <issue>"
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
		return writeMsgMessages(f, self, parent, msgs, marked, lg)
	case "board":
		msgs, err := store.Board(self, f.all)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgMessages(f, self, parent, msgs, nil, lg)
	case "send":
		msg, err := store.Send(self, f.to, f.kind, f.body, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgResult(f, msg, fmt.Sprintf("sent #%d to #%d", msg.ID, f.to), lg)
	case "post":
		msg, err := store.Post(self, f.kind, f.body, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgResult(f, msg, fmt.Sprintf("posted #%d to the board", msg.ID), lg)
	case "mark-read":
		return runMsgMarkRead(f, store, self, now, lg)
	case "register":
		peer, err := store.Register(pane, now)
		if err != nil {
			return msgBackendErr(f.verb, err, lg)
		}
		return writeMsgResult(f, msgRegisterReport{Peer: peer}, fmt.Sprintf("registered peer #%d", peer.Issue), lg)
	}
	// Unreachable while msgVerbFlags and this switch stay in sync; fail loud
	// instead of silently exiting so a half-added verb is caught immediately.
	lg.Err("msg: internal error: verb %s parsed but not implemented", f.verb)
	return exitcode.Invocation
}

type msgMessagesReport struct {
	Self       int                  `json:"self"`
	Parent     string               `json:"parent"`
	All        bool                 `json:"all"`
	Messages   []msgstore.Message   `json:"messages"`
	MarkedRead *msgstore.MarkResult `json:"marked_read,omitempty"`
}

type msgPeersReport struct {
	Parent string          `json:"parent"`
	Peers  []msgstore.Peer `json:"peers"`
}

type msgMarkReadReport struct {
	Self        int     `json:"self"`
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
// marked is non-nil only for `inbox --mark-read`.
func writeMsgMessages(f *msgFlags, self int, parent string, msgs []msgstore.Message, marked *msgstore.MarkResult, lg *log.Logger) exitcode.Code {
	if f.json {
		return writeMsgJSON(msgMessagesReport{Self: self, Parent: parent, All: f.all, Messages: msgs, MarkedRead: marked}, lg)
	}
	writeMsgMessagesTable(msgs, f.all, lg)
	if marked != nil {
		lg.Ok("marked %d message(s) read, board cursor at %d", len(marked.MessageIDs), marked.BoardCursor)
	}
	return exitcode.OK
}

func runMsgMarkRead(f *msgFlags, store *msgstore.Store, self int, now string, lg *log.Logger) exitcode.Code {
	report := msgMarkReadReport{Self: self}
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

func writeMsgMessagesTable(msgs []msgstore.Message, all bool, lg *log.Logger) {
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
			to = "#" + strconv.Itoa(*m.To)
		}
		rows = append(rows, []string{
			strconv.FormatInt(m.ID, 10),
			"#" + strconv.Itoa(m.From),
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
	headers := []string{"ISSUE", "SLUG", "AGENT", "DISPLAY_NAME", "PANE", "LAST_SEEN"}
	rows := make([][]string, 0, len(peers))
	for _, p := range peers {
		rows = append(rows, []string{
			"#" + strconv.Itoa(p.Issue),
			dashIfEmpty(p.Slug),
			dashIfEmpty(p.Agent),
			dashIfEmpty(p.DisplayName),
			dashIfEmpty(p.PaneID),
			dashIfEmpty(p.LastSeen),
		})
	}
	writeMsgTable(out, headers, rows, make([]bool, len(peers)), lg.Colors())
}

// writeMsgTable renders the shared header/separator/rows layout via the
// status table primitives. dims[i] dims row i (read messages in --all views).
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
	fmt.Fprintln(out, statusTableLine(headers, widths))
	separators := make([]string, len(headers))
	for i := range headers {
		separators[i] = strings.Repeat("-", widths[i])
	}
	fmt.Fprintln(out, statusTableLine(separators, widths))
	for i, row := range rows {
		line := statusTableLine(row, widths)
		if dims[i] {
			line = colorWrap(colors.Dim, colors.Reset, line)
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

// --- dry-run -----------------------------------------------------------

// printMsgDryRun prints the would-be writes without opening the DB, in the
// pane.go printPaneDryRun "# would ..." idiom. Ids and cursor positions are
// unknown by design (no DB access), so --all renders a symbolic MAX().
func printMsgDryRun(f *msgFlags, self int, parent string, pane msgstore.Peer, lg *log.Logger) exitcode.Code {
	lines := msgDryRunLines(f, self, parent, pane, team.Now())
	if len(lines) == 0 {
		// Unreachable while msgVerbFlags' --dry-run set and msgDryRunLines
		// stay in sync; fail loud so a half-added verb can't fake success.
		lg.Err("msg: internal error: no dry-run rendering for verb %s", f.verb)
		return exitcode.Invocation
	}
	for _, line := range lines {
		lg.Dim("%s", line)
	}
	return exitcode.OK
}

func msgDryRunLines(f *msgFlags, self int, parent string, pane msgstore.Peer, now string) []string {
	switch f.verb {
	case "send":
		return []string{fmt.Sprintf(
			"# would INSERT INTO messages(parent, from_issue, to_issue, kind, body, created_at) VALUES (%s, %d, %d, %s, %s, %s)",
			sqlLiteral(parent), self, f.to, sqlLiteral(f.kind), sqlLiteral(f.body), sqlLiteral(now))}
	case "post":
		return []string{fmt.Sprintf(
			"# would INSERT INTO messages(parent, from_issue, to_issue, kind, body, created_at) VALUES (%s, %d, NULL, %s, %s, %s)",
			sqlLiteral(parent), self, sqlLiteral(f.kind), sqlLiteral(f.body), sqlLiteral(now))}
	case "mark-read":
		if f.all {
			return []string{
				fmt.Sprintf("# would UPDATE messages SET read_at = %s WHERE parent = %s AND to_issue = %d AND read_at IS NULL",
					sqlLiteral(now), sqlLiteral(parent), self),
				fmt.Sprintf("# would UPSERT board_cursors(issue=%d, last_read_id=MAX(id) of board posts)", self),
			}
		}
		ids := make([]string, len(f.ids))
		for i, id := range f.ids {
			ids[i] = strconv.FormatInt(id, 10)
		}
		return []string{fmt.Sprintf(
			"# would UPDATE messages SET read_at = %s WHERE parent = %s AND to_issue = %d AND id IN (%s) AND read_at IS NULL",
			sqlLiteral(now), sqlLiteral(parent), self, strings.Join(ids, ", "))}
	case "register":
		return []string{fmt.Sprintf(
			"# would UPSERT INTO peers(issue, pane_id, slug, worktree_path, agent, display_name, joined_at, last_seen) VALUES (%d, %s, %s, %s, %s, %s, %s, %s)",
			self, sqlLiteral(pane.PaneID), sqlLiteral(pane.Slug), sqlLiteral(pane.WorktreePath),
			sqlLiteral(pane.Agent), sqlLiteral(pane.DisplayName), sqlLiteral(now), sqlLiteral(now))}
	}
	return nil
}

// sqlLiteral renders s as a single-quoted SQL string literal for dry-run
// display only — real statements always bind placeholders. Newlines become
// \n so a dry-run line stays a single golden-friendly line.
func sqlLiteral(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return "'" + s + "'"
}

func msgBackendErr(verb string, err error, lg *log.Logger) exitcode.Code {
	lg.Err("msg %s: %v", verb, err)
	return exitcode.Backend
}
