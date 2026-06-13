package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/msgstore"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/team"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
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
		{name: "dry-run rejects json", args: []string{"send", "--dry-run", "--json", "--to", "71", "--self", "70", "--parent", "68", "hi"}, code: exitcode.Invocation},
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
		{name: "zero self is rejected", args: []string{"inbox", "--self", "0"}, code: exitcode.Invocation},
		{name: "negative self is a manual-pane synthetic number", args: []string{"inbox", "--self", "-3", "--parent", "68"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.self != -3 {
				t.Errorf("self = %d, want -3", f.self)
			}
		}},
		{name: "negative to targets a manual pane", args: []string{"send", "--to", "-2", "--self", "-1", "--parent", "68", "hi"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.to != -2 || f.self != -1 {
				t.Errorf("to, self = %d, %d, want -2, -1", f.to, f.self)
			}
		}},
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
		{name: "nudge positional target", args: []string{"nudge", "71"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.to != 71 {
				t.Errorf("to = %d, want 71", f.to)
			}
		}},
		{name: "nudge targets a manual pane", args: []string{"nudge", "-2", "--parent", "68"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.to != -2 {
				t.Errorf("to = %d, want -2", f.to)
			}
		}},
		{name: "nudge dry-run with target", args: []string{"nudge", "--dry-run", "71"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if !f.dryRun || f.to != 71 {
				t.Errorf("parsed = %+v", f)
			}
		}},
		{name: "nudge without a target exits invalid", args: []string{"nudge"}, code: exitcode.Invocation},
		{name: "nudge zero target rejected", args: []string{"nudge", "0"}, code: exitcode.Invocation},
		{name: "nudge non-integer target rejected", args: []string{"nudge", "abc"}, code: exitcode.Invocation},
		{name: "nudge rejects a second target", args: []string{"nudge", "71", "72"}, code: exitcode.Invocation},
		{name: "nudge does not accept --to", args: []string{"nudge", "--to", "71"}, code: exitcode.Invocation},
		{name: "nudge dry-run rejects json", args: []string{"nudge", "--dry-run", "--json", "71"}, code: exitcode.Invocation},
		{name: "nudge accepts but does not require --self", args: []string{"nudge", "71", "--self", "5"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.to != 71 || f.self != 5 {
				t.Errorf("parsed = %+v", f)
			}
		}},
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
			name:  "manual pane negative synthetic issue is accepted",
			flags: msgFlags{verb: "inbox"},
			detect: func() (team.Identity, error) {
				return team.Identity{
					Issue:  -1,
					Parent: "@manual",
					Pane:   state.Pane{IssueNum: -1, Parent: "@manual", PaneID: "%9"},
				}, nil
			},
			code: exitcode.OK, wantSelf: -1, wantParent: "@manual", wantPane: "%9",
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
		{
			name:   "nudge needs only parent",
			flags:  msgFlags{verb: "nudge", to: 71, parent: "68"},
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

func TestShouldNudge(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{"running", true},
		{"done", false},
		{"", false},
		{"idle", false},
		{"garbage", false},
	} {
		if got := shouldNudge(tc.state); got != tc.want {
			t.Errorf("shouldNudge(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestNudgeDryRunLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target int
		paneID string
		found  bool
		want   string
	}{
		{
			name: "resolved pane", target: 70, paneID: "%1", found: true,
			want: "# would send-keys -t %1 -l '[fanout] peer message in your inbox — run: fanout msg inbox' then Enter (target #70, only if agent is running)",
		},
		{
			name: "unresolved recipient", target: 99, paneID: "", found: false,
			want: "# would send-keys -t <unknown> -l '[fanout] peer message in your inbox — run: fanout msg inbox' then Enter (target #99, only if agent is running)",
		},
		{
			name: "found row but empty pane id", target: 72, paneID: "", found: true,
			want: "# would send-keys -t <unknown> -l '[fanout] peer message in your inbox — run: fanout msg inbox' then Enter (target #72, only if agent is running)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nudgeDryRunLine(tc.target, tc.paneID, tc.found); got != tc.want {
				t.Errorf("nudgeDryRunLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunMsgNudge(t *testing.T) {
	// withPane is a legacy row (no recorded worktree) so it falls back to an
	// id-only liveness match; withWorktree carries a worktree, so the live pane
	// must also sit at/under it (the reused-%N defense).
	withPane := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 71, PaneID: "%5"}}}
	withWorktree := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 71, PaneID: "%5", WorktreePath: "/wt/recipient"}}}
	noPaneID := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 72, PaneID: ""}}}
	lp := func(id, path, agentState string) tmuxrun.LivePane {
		return tmuxrun.LivePane{ID: id, CurrentPath: path, AgentState: agentState}
	}

	for _, tc := range []struct {
		name           string
		flags          msgFlags
		store          state.Store
		storeErr       error
		live           []tmuxrun.LivePane
		listErr        error
		sendErr        error
		wantCode       exitcode.Code
		wantListed     bool // listLivePanes consulted
		wantSendCalled bool // sendLiteralLine invoked
		wantStdout     string
		wantStderr     string
	}{
		{
			name: "running pane is nudged (legacy id-only match)", flags: msgFlags{verb: "nudge", to: 71}, store: withPane,
			live: []tmuxrun.LivePane{lp("%5", "/anywhere", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "running pane at the recorded worktree is nudged", flags: msgFlags{verb: "nudge", to: 71}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/recipient", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "running pane under the recorded worktree is nudged", flags: msgFlags{verb: "nudge", to: 71}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/recipient/nested", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			// The core Codex P2: tmux reused %5 for a pane sitting elsewhere.
			// It must NOT be nudged even though it reports "running".
			name: "reused id off the recorded worktree is not nudged", flags: msgFlags{verb: "nudge", to: 71}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/someone-else", "running")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "gone or its id was reused",
		},
		{
			name: "pane absent from the live set is a no-op success", flags: msgFlags{verb: "nudge", to: 71}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%9", "/wt/recipient", "running")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "gone or its id was reused",
		},
		{
			// parent "0068" must still resolve the stored "68" pane via Find's
			// numeric canonicalization (parentMatches).
			name: "leading-zero parent still resolves the recipient", flags: msgFlags{verb: "nudge", to: 71, parent: "0068"}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/recipient", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "done pane is a no-op success", flags: msgFlags{verb: "nudge", to: 71}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/recipient", "done")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "agent is not running",
		},
		{
			name: "unset state is a no-op success", flags: msgFlags{verb: "nudge", to: 71}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/recipient", "")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "agent is not running",
		},
		{
			name: "tmux unavailable is a no-op success", flags: msgFlags{verb: "nudge", to: 71}, store: withWorktree,
			listErr: errors.New("tmux down"), wantCode: exitcode.OK, wantListed: true, wantStderr: "tmux is unavailable",
		},
		{
			name: "send-keys failure stays a best-effort success", flags: msgFlags{verb: "nudge", to: 71}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/recipient", "running")}, sendErr: errors.New("boom"), wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStderr: "send-keys failed",
		},
		{
			name: "recipient absent from state is a no-op success", flags: msgFlags{verb: "nudge", to: 99}, store: withWorktree,
			wantCode: exitcode.OK, wantStderr: "not recorded",
		},
		{
			name: "recipient without a recorded pane is a no-op success", flags: msgFlags{verb: "nudge", to: 72}, store: noPaneID,
			wantCode: exitcode.OK, wantStderr: "no recorded pane",
		},
		{
			name: "dry-run prints the would-line and touches no tmux", flags: msgFlags{verb: "nudge", to: 71, dryRun: true}, store: withWorktree,
			wantCode: exitcode.OK, wantStdout: "# would send-keys -t %5",
		},
		{
			name: "dry-run for an unrecorded recipient prints <unknown> and touches no tmux", flags: msgFlags{verb: "nudge", to: 99, dryRun: true}, store: withWorktree,
			wantCode: exitcode.OK, wantStdout: "# would send-keys -t <unknown>",
		},
		{
			name: "state load failure is an invocation error", flags: msgFlags{verb: "nudge", to: 71}, storeErr: errors.New("bad path"),
			wantCode: exitcode.Invocation,
		},
		{
			name: "json reports a delivered nudge", flags: msgFlags{verb: "nudge", to: 71, json: true}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/recipient", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true,
			wantStdout: `"nudged": true`,
		},
		{
			name: "json reports a skipped nudge with a reason", flags: msgFlags{verb: "nudge", to: 71, json: true}, store: withWorktree,
			live: []tmuxrun.LivePane{lp("%5", "/wt/recipient", "done")}, wantCode: exitcode.OK, wantListed: true, wantStdout: `"nudged": false`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origLoad, origList, origSend := loadStateStore, listLivePanes, sendLiteralLine
			defer func() { loadStateStore, listLivePanes, sendLiteralLine = origLoad, origList, origSend }()

			loadStateStore = func() (state.Store, error) { return tc.store, tc.storeErr }
			listed := false
			listLivePanes = func() ([]tmuxrun.LivePane, error) {
				listed = true
				return tc.live, tc.listErr
			}
			sent := false
			var sentPane, sentText string
			sendLiteralLine = func(paneID, text string) error {
				sent = true
				sentPane, sentText = paneID, text
				return tc.sendErr
			}

			var out, errb strings.Builder
			lg := log.NewWith(&out, &errb, false)
			// resolveMsgIdentity supplies parent in the real flow; mirror that
			// here, defaulting to "68" so existing cases need no parent field.
			parent := tc.flags.parent
			if parent == "" {
				parent = "68"
			}
			code := runMsgNudge(&tc.flags, parent, lg)
			if code != tc.wantCode {
				t.Fatalf("runMsgNudge() code = %d, want %d (stderr: %q)", code, tc.wantCode, errb.String())
			}
			if tc.wantCode != exitcode.OK {
				return
			}
			if listed != tc.wantListed {
				t.Errorf("listLivePanes consulted = %v, want %v", listed, tc.wantListed)
			}
			if sent != tc.wantSendCalled {
				t.Errorf("sendLiteralLine called = %v, want %v", sent, tc.wantSendCalled)
			}
			if tc.wantSendCalled {
				if sentPane != "%5" {
					t.Errorf("send pane = %q, want %%5", sentPane)
				}
				if sentText != nudgeText {
					t.Errorf("send text = %q, want nudgeText", sentText)
				}
			}
			if tc.wantStdout != "" && !strings.Contains(out.String(), tc.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", out.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(errb.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", errb.String(), tc.wantStderr)
			}
		})
	}
}

// TestLoadStateStoreResolvesOwnerFromChildWorktree guards the regression Codex
// flagged: nudge is run from a child worktree pane, whose own git toplevel has
// no state.json. loadStateStore must climb to the owner (OwnerProjectRoot) that
// holds the row — resolveStateRuntime would load the child's empty store and
// report every recipient "not recorded".
func TestLoadStateStoreResolvesOwnerFromChildWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	owner := t.TempDir()
	gitCmdTest(t, owner, "init", "-b", "main")
	gitCmdTest(t, owner, "config", "user.email", "fanout@example.invalid")
	gitCmdTest(t, owner, "config", "user.name", "fanout test")
	if err := os.WriteFile(filepath.Join(owner, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, owner, "add", "README.md")
	gitCmdTest(t, owner, "commit", "-m", "base")

	child := filepath.Join(owner, ".fanout", "worktrees", "s-71")
	gitCmdTest(t, owner, "worktree", "add", "-b", "s-71", child)

	// The recipient row lives in the OWNER's state.json, never the child's.
	if err := os.MkdirAll(filepath.Dir(state.Path(owner)), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := `{"schemaVersion":1,"panes":[{"parent":"68","issueNum":71,"slug":"s-71","paneId":"%5"}]}` + "\n"
	if err := os.WriteFile(state.Path(owner), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run from the child pane with no FANOUT_STATE_PATH override (the real
	// in-pane workflow). resolveStateRuntime would resolve the child toplevel
	// and find nothing.
	t.Chdir(child)
	t.Setenv(fanoutStatePathEnv, "")

	st, err := loadStateStore()
	if err != nil {
		t.Fatalf("loadStateStore() failed: %v", err)
	}
	if _, ok := st.Find("68", 71); !ok {
		t.Fatalf("loadStateStore() found %d panes, want the owner's recipient #71 (child-worktree owner resolution regressed)", len(st.Panes))
	}
}

func TestMatchLivePane(t *testing.T) {
	panes := []tmuxrun.LivePane{
		{ID: "%5", CurrentPath: "/wt/recipient", AgentState: "running"},
		{ID: "%6", CurrentPath: "/wt/recipient/nested/deep", AgentState: "done"},
		{ID: "%7", CurrentPath: "/wt/other", AgentState: "running"},
	}
	for _, tc := range []struct {
		name     string
		paneID   string
		worktree string
		wantOK   bool
		wantID   string // matched pane id when wantOK
	}{
		{name: "id + exact worktree", paneID: "%5", worktree: "/wt/recipient", wantOK: true, wantID: "%5"},
		{name: "id + path under worktree", paneID: "%6", worktree: "/wt/recipient", wantOK: true, wantID: "%6"},
		{name: "trailing slash on worktree still matches", paneID: "%5", worktree: "/wt/recipient/", wantOK: true, wantID: "%5"},
		{name: "reused id off the worktree is rejected", paneID: "%7", worktree: "/wt/recipient", wantOK: false},
		{name: "sibling-prefix path is not under the worktree", paneID: "%5", worktree: "/wt/recip", wantOK: false},
		{name: "id absent from the live set", paneID: "%9", worktree: "/wt/recipient", wantOK: false},
		{name: "empty worktree falls back to id-only", paneID: "%7", worktree: "", wantOK: true, wantID: "%7"},
		{name: "empty pane id never matches", paneID: "", worktree: "/wt/recipient", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := matchLivePane(panes, tc.paneID, tc.worktree)
			if ok != tc.wantOK {
				t.Fatalf("matchLivePane(%q, %q) ok = %v, want %v", tc.paneID, tc.worktree, ok, tc.wantOK)
			}
			if ok && got.ID != tc.wantID {
				t.Errorf("matched pane id = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}
