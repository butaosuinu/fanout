// Package team is the SQLite foundation for peer messaging between fanout
// panes (#68): the per-parent DB path convention, the WAL connection helper,
// the agreed v1 schema, deterministic timestamps, and self/parent detection
// from the invoking tmux pane. Nothing in cmd/fanout calls this package yet;
// later sub-issues (#70 fanout msg, #71 --team integration) wire it up.
package team

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	// Registers the pure-Go "sqlite" database/sql driver (no cgo, no
	// external sqlite3 binary).
	_ "modernc.org/sqlite"
)

const (
	// driverName is the database/sql driver registered by modernc.org/sqlite.
	driverName = "sqlite"

	// dsnPragmas is the mandatory connection preamble. modernc.org/sqlite
	// executes each _pragma when it opens a new physical connection, so the
	// preamble applies to every pooled connection without restricting the
	// pool, and busy_timeout is always applied first regardless of order.
	dsnPragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"

	// schemaUserVersion is the PRAGMA user_version stamped by EnsureSchema.
	// Migrations bump it and add nullable columns for forward compatibility.
	// v2 adds the nullable peers.task_id column so issue-less plan tasks
	// (string task id, synthetic peer number) carry their task id for display.
	schemaUserVersion = 2
)

// dsnPathEscaper percent-encodes the bytes that would derail file-URI
// parsing of the DSN: '?' starts the query (pragmas dropped, wrong file
// opened), '#' the fragment, and '%' itself because SQLite percent-decodes
// the URI path. A checkout directory named with such bytes reaches DBPath
// via filepath.Base, so the path cannot be assumed clean.
var dsnPathEscaper = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// schemaStatements is the DDL from the #68 design. messages unifies 1:1 and
// board traffic: to_issue IS NULL means a board/broadcast post, and board
// reads track their position per reader in board_cursors instead of mutating
// read_at. Statements run one by one so a failure names the offending
// statement. The nullable peers.task_id column (v2) records the plan task id
// of a synthetic plan-task peer; it stays NULL for numeric issue panes. A
// pre-v2 DB gains the column via the additive ALTER in EnsureSchema.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS peers (
  issue INTEGER PRIMARY KEY, pane_id TEXT, slug TEXT, worktree_path TEXT,
  agent TEXT, display_name TEXT, joined_at TEXT NOT NULL, last_seen TEXT,
  task_id TEXT)`,
	`CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, parent TEXT NOT NULL,
  from_issue INTEGER NOT NULL, to_issue INTEGER,
  kind TEXT NOT NULL DEFAULT 'note', body TEXT NOT NULL,
  created_at TEXT NOT NULL, read_at TEXT, reply_to INTEGER)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_to ON messages(to_issue, read_at)`,
	`CREATE TABLE IF NOT EXISTS board_cursors (
  issue INTEGER PRIMARY KEY, last_read_id INTEGER NOT NULL DEFAULT 0)`,
}

// Open opens the team DB at path, creating it with mode 0600 when missing,
// and returns a pooled handle whose every connection has the WAL /
// busy_timeout / foreign_keys preamble applied via the DSN. The default
// path is predictable and lives in world-writable /tmp, so an existing DB
// file — and any pre-existing WAL/SHM sidecar — is accepted only when it is
// a regular file owned by the current user with no group/other access;
// anything else, including a planted symlink, fails loudly instead of
// letting another local user read or corrupt team messages. Open does not
// create parent directories and does not run EnsureSchema; callers that may
// be first to touch the DB call EnsureSchema next.
func Open(path string) (*sql.DB, error) {
	if err := ensurePrivateDBFile(path); err != nil {
		return nil, fmt.Errorf("open team db: %w", err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := validatePrivateFile(sidecar); err != nil {
			return nil, fmt.Errorf("open team db %s: %w", path, err)
		}
	}

	db, err := sql.Open(driverName, "file:"+dsnPathEscaper.Replace(path)+"?"+dsnPragmas)
	if err != nil {
		return nil, fmt.Errorf("open team db %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open team db %s: %w", path, err)
	}
	return db, nil
}

// ensurePrivateDBFile creates path with 0600 before the driver's own create
// path runs with umask-dependent permissions, and verifies that a
// pre-existing file is private to the current user. O_NOFOLLOW rejects a
// planted symlink atomically, and the fstat-based check leaves no window
// between validation and use of the inode.
func ensurePrivateDBFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil {
		return statErr
	}
	if closeErr != nil {
		return closeErr
	}
	return checkPrivate(path, info)
}

// validatePrivateFile applies checkPrivate to path when it exists; a
// missing file is fine (SQLite creates sidecars with the DB's mode).
func validatePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return checkPrivate(path, info)
}

func checkPrivate(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; remove it and retry", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s is group/world accessible (%04o); chmod 600 or remove it", path, perm)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not the current user; remove it and retry", path, st.Uid)
	}
	return nil
}

// EnsureSchema creates the tables and index idempotently, brings a pre-v2 DB
// forward with the additive peers.task_id column, and stamps PRAGMA
// user_version. A DB already at the current version is left untouched apart
// from the harmless CREATE IF NOT EXISTS re-runs and the no-op column check; a
// DB at a newer version returns an error instead of being downgraded.
func EnsureSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read team db schema version: %w", err)
	}
	if version > schemaUserVersion {
		return fmt.Errorf("team db schema version %d is newer than supported %d", version, schemaUserVersion)
	}
	for _, stmt := range schemaStatements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure team db schema: %w", err)
		}
	}
	if err := ensurePeersTaskIDColumn(db); err != nil {
		return err
	}
	if version < schemaUserVersion {
		// PRAGMA cannot take placeholders; the value is a package const.
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaUserVersion)); err != nil {
			return fmt.Errorf("stamp team db schema version: %w", err)
		}
	}
	return nil
}

// ensurePeersTaskIDColumn adds the nullable peers.task_id column to a pre-v2
// DB. A fresh DB already has it from schemaStatements, so the column check is
// a no-op there; a v1 issue DB gains the nullable column without a destructive
// rebuild and keeps every existing peer row intact. SQLite has no
// ADD COLUMN IF NOT EXISTS, and two panes upgrading the same shared /tmp DB can
// both observe the column absent and race to ALTER it, so a duplicate-column
// error from a concurrent winner is treated as success (the column now exists).
func ensurePeersTaskIDColumn(db *sql.DB) error {
	has, err := columnExists(db, "peers", "task_id")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE peers ADD COLUMN task_id TEXT"); err != nil {
		if isDuplicateColumnErr(err) {
			return nil
		}
		return fmt.Errorf("add peers.task_id column: %w", err)
	}
	return nil
}

// isDuplicateColumnErr reports whether err is SQLite's "duplicate column name"
// from an ADD COLUMN that lost a concurrent race — i.e. the column already
// exists, so the migration is effectively done.
func isDuplicateColumnErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

// columnExists reports whether table has a column named column, via
// PRAGMA table_info. table is a package-internal literal, never caller input,
// so interpolating it into the PRAGMA (which cannot take placeholders) is safe.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("inspect %s columns: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	return false, nil
}
