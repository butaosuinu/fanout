// Package msgstore implements the message/peer queries behind `fanout msg`
// (#70) on top of the internal/team v1 schema. internal/team owns the DB
// path convention, the connection helper, and the DDL; this package owns
// every SELECT/INSERT/UPDATE so the CLI surface (cmd/fanout/msg.go) stays a
// thin parse-and-print layer. All statements bind parameters via
// placeholders — never interpolate caller input into SQL.
package msgstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Message is one messages row. To is nil for board posts (to_issue IS NULL);
// Board mirrors that so JSON consumers don't have to test null. The struct
// doubles as the canonical `fanout msg --json` message encoding.
type Message struct {
	ID        int64   `json:"id"`
	From      int     `json:"from"`
	To        *int    `json:"to"`
	Board     bool    `json:"board"`
	Kind      string  `json:"kind"`
	Body      string  `json:"body"`
	CreatedAt string  `json:"created_at"`
	ReadAt    *string `json:"read_at"`
	ReplyTo   *int64  `json:"reply_to"`
}

// Peer is one peers row, also the canonical JSON encoding. Nullable text
// columns scan to "" — register writes "" rather than NULL for unknown
// fields, so the distinction carries no information.
type Peer struct {
	Issue        int    `json:"issue"`
	PaneID       string `json:"pane_id"`
	Slug         string `json:"slug"`
	WorktreePath string `json:"worktree_path"`
	Agent        string `json:"agent"`
	DisplayName  string `json:"display_name"`
	JoinedAt     string `json:"joined_at"`
	LastSeen     string `json:"last_seen"`
}

// MarkResult reports what `inbox --mark-read` actually marked: the 1:1
// message ids whose read_at was set, and the board cursor position after
// advancing (0 when no board post was displayed and no cursor existed).
type MarkResult struct {
	MessageIDs  []int64 `json:"message_ids"`
	BoardCursor int64   `json:"board_cursor"`
}

// Store wraps an open team DB scoped to one parent ref. The parent scopes
// every messages query; peers and board_cursors are per-DB because the v1
// schema (internal/team) keys them by issue alone. That is sound only under
// the one-DB-per-parent convention (team.DBPath), so New enforces it.
type Store struct {
	db     *sql.DB
	parent string
}

// New wraps db scoped to parent. It rejects a DB that already holds another
// parent's messages: peers and board_cursors are keyed by issue alone in the
// v1 schema, so sharing one DB file across parents would leak cursors and
// peer rows across team boundaries even though messages are parent-scoped.
// Per-parent cursors/peers need a v2 schema migration in internal/team.
func New(db *sql.DB, parent string) (*Store, error) {
	var other string
	err := db.QueryRow("SELECT parent FROM messages WHERE parent != ? LIMIT 1", parent).Scan(&other)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// parent is the DB's sole tenant — the v1 single-parent invariant holds.
	case err != nil:
		return nil, fmt.Errorf("check team db parent: %w", err)
	default:
		return nil, fmt.Errorf(
			"team db already holds messages for parent %s; one team DB serves one parent (use a separate FANOUT_DB_PATH per parent)",
			other)
	}
	return &Store{db: db, parent: parent}, nil
}

const messageColumns = "id, from_issue, to_issue, kind, body, created_at, read_at, reply_to"

// boardUnreadCond selects unread board posts: beyond self's cursor, excluding
// self's own posts so `post` followed by `inbox`/`board` doesn't surface the
// poster's own words as unread. Placeholder order: self, self.
const boardUnreadCond = `(to_issue IS NULL AND from_issue != ?
       AND id > COALESCE((SELECT last_read_id FROM board_cursors WHERE issue = ?), 0))`

// unreadCond is the inbox union: unread 1:1 messages plus unread board posts.
// Placeholder order: self, self, self.
const unreadCond = `(to_issue = ? AND read_at IS NULL)
   OR ` + boardUnreadCond

// Send inserts a 1:1 message and returns the stored row.
func (s *Store) Send(from, to int, kind, body, now string) (Message, error) {
	return s.insertMessage(from, &to, kind, body, now)
}

// Post inserts a board post (to_issue IS NULL) and returns the stored row.
func (s *Store) Post(from int, kind, body, now string) (Message, error) {
	return s.insertMessage(from, nil, kind, body, now)
}

func (s *Store) insertMessage(from int, to *int, kind, body, now string) (Message, error) {
	res, err := s.db.Exec(
		"INSERT INTO messages(parent, from_issue, to_issue, kind, body, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		s.parent, from, to, kind, body, now,
	)
	if err != nil {
		return Message{}, fmt.Errorf("insert message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Message{}, fmt.Errorf("insert message: %w", err)
	}
	msg := Message{ID: id, From: from, To: to, Board: to == nil, Kind: kind, Body: body, CreatedAt: now}
	return msg, nil
}

// Inbox returns self's messages ordered by id: the unread union by default,
// or every 1:1/board message addressed to (or visible by) self with all set.
// With markRead, the displayed rows are marked read in one transaction —
// read_at only on the ids actually returned, the board cursor only up to the
// highest returned board id — so messages that arrive between the SELECT and
// the UPDATE stay unread for the next call.
func (s *Store) Inbox(self int, all, markRead bool, now string) ([]Message, *MarkResult, error) {
	if !markRead {
		msgs, err := s.queryMessages(inboxQuery(all), inboxArgs(s.parent, self, all)...)
		return msgs, nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("inbox: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	msgs, err := queryMessagesIn(tx, inboxQuery(all), inboxArgs(s.parent, self, all)...)
	if err != nil {
		return nil, nil, err
	}
	marked, err := s.markDisplayed(tx, self, msgs, now)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("inbox: %w", err)
	}
	return msgs, marked, nil
}

func inboxQuery(all bool) string {
	cond := unreadCond
	if all {
		cond = "(to_issue = ? OR to_issue IS NULL)"
	}
	return "SELECT " + messageColumns + " FROM messages WHERE parent = ? AND (" + cond + ") ORDER BY id"
}

func inboxArgs(parent string, self int, all bool) []any {
	if all {
		return []any{parent, self}
	}
	return []any{parent, self, self, self}
}

// markDisplayed sets read_at on the displayed 1:1 rows and advances the board
// cursor to the highest displayed board id. Cursor updates never regress
// (MAX) so a stale --all view cannot rewind another invocation's progress.
func (s *Store) markDisplayed(tx *sql.Tx, self int, msgs []Message, now string) (*MarkResult, error) {
	marked := &MarkResult{MessageIDs: []int64{}}
	var maxBoardID int64
	for _, m := range msgs {
		if m.Board {
			maxBoardID = max(maxBoardID, m.ID)
			continue
		}
		if m.To != nil && *m.To == self && m.ReadAt == nil {
			marked.MessageIDs = append(marked.MessageIDs, m.ID)
		}
	}
	if err := s.markRead(tx, self, marked.MessageIDs, now); err != nil {
		return nil, err
	}
	if maxBoardID > 0 {
		if err := advanceCursor(tx, self, maxBoardID); err != nil {
			return nil, err
		}
	}
	cursor, err := readBoardCursor(tx, self)
	if err != nil {
		return nil, err
	}
	marked.BoardCursor = cursor
	return marked, nil
}

// markRead flips read_at on the given 1:1 ids, scoped to this Store's parent
// like every other messages statement. No-op on an empty id list.
func (s *Store) markRead(tx *sql.Tx, self int, ids []int64, now string) error {
	if len(ids) == 0 {
		return nil
	}
	query := "UPDATE messages SET read_at = ? WHERE parent = ? AND to_issue = ? AND id IN (" +
		placeholders(len(ids)) + ") AND read_at IS NULL"
	if _, err := tx.Exec(query, append([]any{now, s.parent, self}, int64sToAny(ids)...)...); err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

func readBoardCursor(tx *sql.Tx, self int) (int64, error) {
	var cursor int64
	if err := tx.QueryRow(
		"SELECT COALESCE((SELECT last_read_id FROM board_cursors WHERE issue = ?), 0)", self,
	).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("read board cursor: %w", err)
	}
	return cursor, nil
}

// Board returns board posts ordered by id: unread beyond self's cursor by
// default (own posts excluded), or every post with all set. The cursor is
// never advanced — that is `inbox --mark-read` / `mark-read --all` territory.
func (s *Store) Board(self int, all bool) ([]Message, error) {
	if all {
		return s.queryMessages(
			"SELECT "+messageColumns+" FROM messages WHERE parent = ? AND to_issue IS NULL ORDER BY id",
			s.parent,
		)
	}
	return s.queryMessages(
		"SELECT "+messageColumns+" FROM messages WHERE parent = ? AND "+boardUnreadCond+" ORDER BY id",
		s.parent, self, self,
	)
}

// MarkReadIDs marks the given 1:1 message ids read and returns the ids it
// actually updated. Ids that are board posts, addressed to someone else,
// under another parent, already read, or absent are silently skipped —
// callers retry idempotently.
func (s *Store) MarkReadIDs(self int, ids []int64, now string) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("mark-read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// SELECT before UPDATE so the returned ids are exactly the rows flipped.
	query := "SELECT id FROM messages WHERE parent = ? AND to_issue = ? AND id IN (" +
		placeholders(len(ids)) + ") AND read_at IS NULL ORDER BY id"
	rows, err := tx.Query(query, append([]any{s.parent, self}, int64sToAny(ids)...)...)
	if err != nil {
		return nil, fmt.Errorf("mark-read: %w", err)
	}
	markable, err := scanIDs(rows)
	if err != nil {
		return nil, fmt.Errorf("mark-read: %w", err)
	}
	if err := s.markRead(tx, self, markable, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("mark-read: %w", err)
	}
	return markable, nil
}

// MarkReadAll marks every unread 1:1 message to self read and advances the
// board cursor to the current highest board id, returning the marked ids and
// the cursor position.
func (s *Store) MarkReadAll(self int, now string) ([]int64, int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, fmt.Errorf("mark-read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(
		"SELECT id FROM messages WHERE parent = ? AND to_issue = ? AND read_at IS NULL ORDER BY id",
		s.parent, self,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("mark-read: %w", err)
	}
	markable, err := scanIDs(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("mark-read: %w", err)
	}
	if err = s.markRead(tx, self, markable, now); err != nil {
		return nil, 0, err
	}

	var maxBoardID int64
	if err = tx.QueryRow(
		"SELECT COALESCE(MAX(id), 0) FROM messages WHERE parent = ? AND to_issue IS NULL", s.parent,
	).Scan(&maxBoardID); err != nil {
		return nil, 0, fmt.Errorf("mark-read: %w", err)
	}
	if maxBoardID > 0 {
		if err = advanceCursor(tx, self, maxBoardID); err != nil {
			return nil, 0, err
		}
	}
	cursor, err := readBoardCursor(tx, self)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("mark-read: %w", err)
	}
	return markable, cursor, nil
}

// Register upserts a peer row. joined_at is set on first insert only;
// last_seen is refreshed on every call.
func (s *Store) Register(p Peer, now string) (Peer, error) {
	_, err := s.db.Exec(
		`INSERT INTO peers(issue, pane_id, slug, worktree_path, agent, display_name, joined_at, last_seen)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(issue) DO UPDATE SET
  pane_id=excluded.pane_id, slug=excluded.slug, worktree_path=excluded.worktree_path,
  agent=excluded.agent, display_name=excluded.display_name, last_seen=excluded.last_seen`,
		p.Issue, p.PaneID, p.Slug, p.WorktreePath, p.Agent, p.DisplayName, now, now,
	)
	if err != nil {
		return Peer{}, fmt.Errorf("register peer: %w", err)
	}
	row := s.db.QueryRow(
		"SELECT issue, pane_id, slug, worktree_path, agent, display_name, joined_at, last_seen FROM peers WHERE issue = ?",
		p.Issue,
	)
	stored, err := scanPeer(row)
	if err != nil {
		return Peer{}, fmt.Errorf("register peer: %w", err)
	}
	return stored, nil
}

// Peers returns every registered peer ordered by issue.
func (s *Store) Peers() ([]Peer, error) {
	rows, err := s.db.Query(
		"SELECT issue, pane_id, slug, worktree_path, agent, display_name, joined_at, last_seen FROM peers ORDER BY issue",
	)
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	peers := []Peer{}
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("list peers: %w", err)
		}
		peers = append(peers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	return peers, nil
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func (s *Store) queryMessages(query string, args ...any) ([]Message, error) {
	return queryMessagesIn(s.db, query, args...)
}

func queryMessagesIn(q querier, query string, args ...any) ([]Message, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	msgs := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.From, &m.To, &m.Kind, &m.Body, &m.CreatedAt, &m.ReadAt, &m.ReplyTo); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.Board = m.To == nil
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	return msgs, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPeer(row rowScanner) (Peer, error) {
	var p Peer
	var paneID, slug, worktree, agent, display, lastSeen sql.NullString
	if err := row.Scan(&p.Issue, &paneID, &slug, &worktree, &agent, &display, &p.JoinedAt, &lastSeen); err != nil {
		return Peer{}, err
	}
	p.PaneID = paneID.String
	p.Slug = slug.String
	p.WorktreePath = worktree.String
	p.Agent = agent.String
	p.DisplayName = display.String
	p.LastSeen = lastSeen.String
	return p, nil
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func advanceCursor(tx *sql.Tx, self int, id int64) error {
	if _, err := tx.Exec(
		`INSERT INTO board_cursors(issue, last_read_id) VALUES (?, ?)
ON CONFLICT(issue) DO UPDATE SET last_read_id = MAX(last_read_id, excluded.last_read_id)`,
		self, id,
	); err != nil {
		return fmt.Errorf("advance board cursor: %w", err)
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func int64sToAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
