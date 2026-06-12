package team

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "team.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return db, path
}

func TestOpenCreatesFileWith0600(t *testing.T) {
	_, path := openTestDB(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
}

func TestOpenKeepsExistingFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "team.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("pre-create: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("perm = %o, want pre-existing 0644 left untouched", perm)
	}
}

func TestOpenMissingParentDirErrors(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "no-such-dir", "team.db")); err == nil {
		t.Fatal("Open under a missing directory succeeded, want error")
	}
}

func TestOpenAppliesPragmaPreamble(t *testing.T) {
	db, path := openTestDB(t)

	pragmas := []struct {
		query string
		want  string
	}{
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA foreign_keys", "1"},
	}
	for _, p := range pragmas {
		var got string
		if err := db.QueryRow(p.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", p.query, err)
		}
		if got != p.want {
			t.Errorf("%s = %q, want %q", p.query, got, p.want)
		}
	}

	// The preamble must hold on every pooled connection, not just the first:
	// hold two connections simultaneously to force two physical conns.
	ctx := context.Background()
	for i := range 2 {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		defer conn.Close()
		var timeout string
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatalf("conn %d busy_timeout: %v", i, err)
		}
		if timeout != "5000" {
			t.Errorf("conn %d busy_timeout = %q, want 5000", i, timeout)
		}
	}

	// WAL persists in the DB file: a second handle must see it too.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()
	var mode string
	if err := db2.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("second handle journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("second handle journal_mode = %q, want wal", mode)
	}
}

// A checkout directory named with URI delimiters reaches DBPath via
// filepath.Base; the DSN must escape them so SQLite opens the right file
// with the pragmas intact instead of treating them as query/fragment.
func TestOpenEscapesURIDelimitersInPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "we?ird#100%.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer db.Close()

	var timeout string
	if err = db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if timeout != "5000" {
		t.Errorf("busy_timeout = %q, want 5000 (pragmas dropped by DSN parsing?)", timeout)
	}

	if err = EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Size() == 0 {
		t.Errorf("DB file %q is empty: SQLite wrote to a different (misparsed) path", path)
	}
}

func TestEnsureSchema(t *testing.T) {
	db, _ := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	for _, object := range []string{"peers", "messages", "board_cursors", "idx_messages_to"} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE name = ?", object).Scan(&name)
		if err != nil {
			t.Errorf("schema object %s missing: %v", object, err)
		}
	}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 1 {
		t.Errorf("user_version = %d, want 1", version)
	}

	// Idempotent: a second run must not error or move the version.
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version after rerun: %v", err)
	}
	if version != 1 {
		t.Errorf("user_version after rerun = %d, want 1", version)
	}
}

func TestEnsureSchemaRejectsNewerVersion(t *testing.T) {
	db, _ := openTestDB(t)
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	err := EnsureSchema(db)
	if err == nil {
		t.Fatal("EnsureSchema on a newer schema succeeded, want error")
	}
	if got := err.Error(); !strings.Contains(got, "99") {
		t.Errorf("error %q does not name the newer version 99", got)
	}
}

func TestMessagesParameterizedRoundTrip(t *testing.T) {
	db, _ := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	hostile := `it's a '; DROP TABLE messages; -- body`
	// Board post: to_issue IS NULL.
	if _, err := db.Exec(
		"INSERT INTO messages(parent, from_issue, to_issue, kind, body, created_at) VALUES(?, ?, ?, ?, ?, ?)",
		"68", 69, nil, "fyi-broadcast", hostile, "2026-01-02T03:04:05Z",
	); err != nil {
		t.Fatalf("insert board message: %v", err)
	}

	var body string
	var toIssue sql.NullInt64
	err := db.QueryRow("SELECT body, to_issue FROM messages WHERE from_issue = ?", 69).Scan(&body, &toIssue)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if body != hostile {
		t.Errorf("body = %q, want %q round-tripped", body, hostile)
	}
	if toIssue.Valid {
		t.Errorf("to_issue = %v, want NULL (board semantics)", toIssue.Int64)
	}
}

func TestWALSidecarPermissions(t *testing.T) {
	db, path := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO messages(parent, from_issue, kind, body, created_at) VALUES(?, ?, ?, ?, ?)",
		"68", 69, "note", "x", "2026-01-02T03:04:05Z",
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil {
		t.Skipf("no -wal sidecar to inspect: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("-wal perm = %o, want 0600", perm)
	}
}
