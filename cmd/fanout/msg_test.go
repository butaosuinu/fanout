package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/msgstore"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/team"
)

func msgTestLogger() *log.Logger {
	return log.NewWith(&strings.Builder{}, &strings.Builder{}, false)
}

func TestParseMsgFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code exitcode.Code
		want func(*testing.T, *msgFlags)
	}{
		{name: "no verb", args: nil, code: exitcode.Invocation},
		{name: "unknown verb", args: []string{"bogus"}, code: exitcode.Invocation},
		{name: "send happy path", args: []string{"send", "--to", "71", "--self", "70", "--parent", "68", "hello", "world"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.to != 71 || f.self != 70 || f.parent != "68" || f.body != "hello world" || f.kind != "note" {
				t.Errorf("parsed = %+v", f)
			}
		}},
		{name: "send missing --to", args: []string{"send", "--self", "70", "--parent", "68", "hi"}, code: exitcode.Invocation},
		{name: "send non-integer --to", args: []string{"send", "--to", "abc", "--self", "70", "--parent", "68", "hi"}, code: exitcode.Invocation},
		{name: "send empty body", args: []string{"send", "--to", "71", "--self", "70", "--parent", "68"}, code: exitcode.Invocation},
		{name: "send whitespace body", args: []string{"send", "--to", "71", "--self", "70", "--parent", "68", " "}, code: exitcode.Invocation},
		{name: "post custom kind", args: []string{"post", "--kind", "blocker", "watch", "out"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.kind != "blocker" || f.body != "watch out" {
				t.Errorf("parsed = %+v", f)
			}
		}},
		{name: "post empty kind", args: []string{"post", "--kind", "", "hi"}, code: exitcode.Invocation},
		{name: "mark-read ids", args: []string{"mark-read", "--id", "3", "--id", "5"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if len(f.ids) != 2 || f.ids[0] != 3 || f.ids[1] != 5 {
				t.Errorf("ids = %v", f.ids)
			}
		}},
		{name: "mark-read no mode", args: []string{"mark-read"}, code: exitcode.Invocation},
		{name: "mark-read both modes", args: []string{"mark-read", "--id", "3", "--all"}, code: exitcode.Invocation},
		{name: "mark-read bad id", args: []string{"mark-read", "--id", "0"}, code: exitcode.Invocation},
		{name: "inbox flags", args: []string{"inbox", "--all", "--mark-read", "--json"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if !f.all || !f.markRead || !f.json {
				t.Errorf("parsed = %+v", f)
			}
		}},
		{name: "inbox rejects positional", args: []string{"inbox", "hello"}, code: exitcode.Invocation},
		{name: "inbox rejects dry-run", args: []string{"inbox", "--dry-run"}, code: exitcode.Invocation},
		{name: "board rejects mark-read", args: []string{"board", "--mark-read"}, code: exitcode.Invocation},
		{name: "peers rejects --to", args: []string{"peers", "--to", "5"}, code: exitcode.Invocation},
		{name: "unknown option", args: []string{"peers", "--bogus"}, code: exitcode.Invocation},
		{name: "missing value", args: []string{"send", "--to"}, code: exitcode.Invocation},
		{name: "inline value", args: []string{"send", "--to=71", "--self=70", "--parent=68", "hi"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.to != 71 || f.self != 70 || f.parent != "68" {
				t.Errorf("parsed = %+v", f)
			}
		}},
		{name: "inline value on boolean flag", args: []string{"inbox", "--all=1"}, code: exitcode.Invocation},
		{name: "self must be positive", args: []string{"inbox", "--self", "-3"}, code: exitcode.Invocation},
		{name: "empty parent", args: []string{"inbox", "--parent", ""}, code: exitcode.Invocation},
		{name: "prose parent", args: []string{"inbox", "--parent", "not-a-ref"}, code: exitcode.Invocation},
		{name: "parent is canonicalized", args: []string{"inbox", "--self", "70", "--parent", "0068"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.parent != "68" {
				t.Errorf("parent = %q, want %q (leading zeros must collapse)", f.parent, "68")
			}
		}},
		{name: "register dry-run", args: []string{"register", "--dry-run", "--self", "70", "--parent", "68"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if !f.dryRun {
				t.Errorf("parsed = %+v", f)
			}
		}},
		{name: "help as verb", args: []string{"-h"}, code: exitcode.OK},
		{name: "long help flag mid-args", args: []string{"inbox", "--help"}, code: exitcode.OK},
		{name: "short help before body", args: []string{"send", "-h"}, code: exitcode.OK},
		{name: "short help after body word stays in the body", args: []string{"send", "--to", "71", "try", "-h", "now"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.body != "try -h now" {
				t.Errorf("body = %q, want %q", f.body, "try -h now")
			}
		}},
		{name: "long help after body word fails loudly, never silent usage", args: []string{"send", "--to", "71", "ask", "about", "--help"}, code: exitcode.Invocation},
		{name: "terminator lets the body carry flag-like words", args: []string{"send", "--to", "71", "--", "--kind", "is part of the body"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.body != "--kind is part of the body" || f.kind != "note" {
				t.Errorf("body, kind = %q, %q", f.body, f.kind)
			}
		}},
		{name: "terminator on a read verb still rejects positionals", args: []string{"inbox", "--", "x"}, code: exitcode.Invocation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, code := parseMsgFlags(tc.args, msgTestLogger())
			if code != tc.code {
				t.Fatalf("parseMsgFlags(%v) code = %d, want %d", tc.args, code, tc.code)
			}
			if tc.want != nil {
				tc.want(t, f)
			}
		})
	}
}

func TestResolveMsgIdentity(t *testing.T) {
	detected := team.Identity{
		Issue:  70,
		Parent: "68",
		Pane: state.Pane{
			IssueNum: 70, Parent: "68", PaneID: "%1", Slug: "msg-cli-surface-70",
			Agent: "claude", DisplayName: "msg cli", WorktreePath: "/tmp/wt",
		},
	}
	for _, tc := range []struct {
		name       string
		flags      msgFlags
		detect     func() (team.Identity, error)
		code       exitcode.Code
		wantSelf   int
		wantParent string
		wantPane   string // expected pane_id, "" when identity is explicit
	}{
		{
			name:  "both explicit skips detection",
			flags: msgFlags{verb: "send", self: 70, parent: "68"},
			detect: func() (team.Identity, error) {
				t.Error("detect called despite explicit --self/--parent")
				return team.Identity{}, nil
			},
			code: exitcode.OK, wantSelf: 70, wantParent: "68",
		},
		{
			name:   "detection fills both",
			flags:  msgFlags{verb: "inbox"},
			detect: func() (team.Identity, error) { return detected, nil },
			code:   exitcode.OK, wantSelf: 70, wantParent: "68", wantPane: "%1",
		},
		{
			name:   "explicit self keeps detected parent",
			flags:  msgFlags{verb: "send", self: 99},
			detect: func() (team.Identity, error) { return detected, nil },
			code:   exitcode.OK, wantSelf: 99, wantParent: "68",
		},
		{
			name:   "explicit parent keeps detected self",
			flags:  msgFlags{verb: "send", parent: "77"},
			detect: func() (team.Identity, error) { return detected, nil },
			code:   exitcode.OK, wantSelf: 70, wantParent: "77", wantPane: "%1",
		},
		{
			name:   "detection failure",
			flags:  msgFlags{verb: "send"},
			detect: func() (team.Identity, error) { return team.Identity{}, team.ErrPaneNotFound },
			code:   exitcode.Invocation,
		},
		{
			name:   "peers needs only parent",
			flags:  msgFlags{verb: "peers", parent: "68"},
			detect: func() (team.Identity, error) { return team.Identity{}, errors.New("must not be called") },
			code:   exitcode.OK, wantSelf: 0, wantParent: "68",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := detectIdentity
			detectIdentity = tc.detect
			defer func() { detectIdentity = orig }()

			self, parent, pane, code := resolveMsgIdentity(&tc.flags, msgTestLogger())
			if code != tc.code {
				t.Fatalf("code = %d, want %d", code, tc.code)
			}
			if code != exitcode.OK {
				return
			}
			if self != tc.wantSelf || parent != tc.wantParent {
				t.Errorf("self, parent = %d, %q, want %d, %q", self, parent, tc.wantSelf, tc.wantParent)
			}
			if pane.PaneID != tc.wantPane {
				t.Errorf("pane.PaneID = %q, want %q", pane.PaneID, tc.wantPane)
			}
		})
	}
}

func TestMsgDryRunLines(t *testing.T) {
	const now = "2026-06-13T00:00:00Z"
	for _, tc := range []struct {
		name  string
		flags msgFlags
		pane  msgstore.Peer
		want  []string
	}{
		{
			name:  "send",
			flags: msgFlags{verb: "send", to: 71, kind: "note", body: "hello world"},
			want: []string{
				"# would INSERT INTO messages(parent, from_issue, to_issue, kind, body, created_at) VALUES ('68', 70, 71, 'note', 'hello world', '2026-06-13T00:00:00Z')",
			},
		},
		{
			name:  "post escapes quotes and newlines",
			flags: msgFlags{verb: "post", kind: "blocker", body: "it's\nbroken"},
			want: []string{
				`# would INSERT INTO messages(parent, from_issue, to_issue, kind, body, created_at) VALUES ('68', 70, NULL, 'blocker', 'it''s\nbroken', '2026-06-13T00:00:00Z')`,
			},
		},
		{
			name:  "mark-read ids",
			flags: msgFlags{verb: "mark-read", ids: []int64{3, 5}},
			want: []string{
				"# would UPDATE messages SET read_at = '2026-06-13T00:00:00Z' WHERE parent = '68' AND to_issue = 70 AND id IN (3, 5) AND read_at IS NULL",
			},
		},
		{
			name:  "mark-read all",
			flags: msgFlags{verb: "mark-read", all: true},
			want: []string{
				"# would UPDATE messages SET read_at = '2026-06-13T00:00:00Z' WHERE parent = '68' AND to_issue = 70 AND read_at IS NULL",
				"# would UPSERT board_cursors(issue=70, last_read_id=MAX(id) of board posts)",
			},
		},
		{
			name:  "register with pane",
			flags: msgFlags{verb: "register"},
			pane:  msgstore.Peer{Issue: 70, PaneID: "%1", Slug: "s-70", WorktreePath: "/tmp/wt", Agent: "claude", DisplayName: "name"},
			want: []string{
				"# would UPSERT INTO peers(issue, pane_id, slug, worktree_path, agent, display_name, joined_at, last_seen) VALUES (70, '%1', 's-70', '/tmp/wt', 'claude', 'name', '2026-06-13T00:00:00Z', '2026-06-13T00:00:00Z')",
			},
		},
		{
			name:  "register without pane",
			flags: msgFlags{verb: "register"},
			want: []string{
				"# would UPSERT INTO peers(issue, pane_id, slug, worktree_path, agent, display_name, joined_at, last_seen) VALUES (70, '', '', '', '', '', '2026-06-13T00:00:00Z', '2026-06-13T00:00:00Z')",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := msgDryRunLines(&tc.flags, 70, "68", tc.pane, now)
			if len(got) != len(tc.want) {
				t.Fatalf("lines = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestWriteMsgMessagesTable(t *testing.T) {
	to := 70
	read := "2026-06-13T00:00:00Z"
	msgs := []msgstore.Message{
		{ID: 1, From: 71, To: &to, Kind: "note", Body: "multi\nline", CreatedAt: "2026-06-13T00:00:00Z", ReadAt: &read},
		{ID: 3, From: 71, Board: true, Kind: "note", Body: "board post", CreatedAt: "2026-06-13T00:00:00Z"},
	}
	var out strings.Builder
	lg := log.NewWith(&out, &strings.Builder{}, false)
	writeMsgMessagesTable(msgs, true, lg)
	got := out.String()
	for _, want := range []string{
		"ID  FROM  TO     KIND  CREATED               BODY",
		"1   #71   #70    note  2026-06-13T00:00:00Z  multi line",
		"3   #71   board  note  2026-06-13T00:00:00Z  board post",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q in:\n%s", want, got)
		}
	}

	out.Reset()
	writeMsgMessagesTable(nil, false, lg)
	if got := out.String(); got != "no unread messages\n" {
		t.Errorf("empty unread table = %q", got)
	}
	out.Reset()
	writeMsgMessagesTable(nil, true, lg)
	if got := out.String(); got != "no messages\n" {
		t.Errorf("empty all table = %q", got)
	}
}

func TestSQLLiteral(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "'plain'"},
		{"it's", "'it''s'"},
		{"a\nb", `'a\nb'`},
		{"a\r\nb", `'a\r\nb'`},
		{"", "''"},
	} {
		if got := sqlLiteral(tc.in); got != tc.want {
			t.Errorf("sqlLiteral(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
