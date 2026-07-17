package main

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
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
		{name: "send invalid --to token", args: []string{"send", "--to", "ABC", "--self", "70", "--parent", "68", "hi"}, code: exitcode.Invocation},
		{name: "send task-id --to parses (semantic check is deferred)", args: []string{"send", "--to", "api-client", "--self", "70", "--parent", "68", "hi"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.toRaw != "api-client" || f.to != 0 {
				t.Errorf("parsed = %+v, want toRaw=api-client to=0", f)
			}
		}},
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
		{name: "numeric-zero self parses as a task-id shape (rejected later under a numeric parent)", args: []string{"inbox", "--self", "0"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.selfRaw != "0" || f.self != 0 {
				t.Errorf("parsed = %+v, want selfRaw=0 self=0", f)
			}
		}},
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
		{name: "nudge without a target exits invalid", args: []string{"nudge"}, code: exitcode.Invocation},
		{name: "numeric-zero nudge target parses as a task-id shape (rejected later under a numeric parent)", args: []string{"nudge", "0"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.toRaw != "0" || f.to != 0 {
				t.Errorf("parsed = %+v, want toRaw=0 to=0", f)
			}
		}},
		{name: "nudge invalid target token rejected", args: []string{"nudge", "ABC"}, code: exitcode.Invocation},
		{name: "nudge task-id target parses (semantic check is deferred)", args: []string{"nudge", "api-client"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.toRaw != "api-client" || f.to != 0 {
				t.Errorf("parsed = %+v, want toRaw=api-client to=0", f)
			}
		}},
		{name: "nudge rejects a second target", args: []string{"nudge", "71", "72"}, code: exitcode.Invocation},
		{name: "nudge does not accept --to", args: []string{"nudge", "--to", "71"}, code: exitcode.Invocation},
		{name: "nudge accepts but does not require --self", args: []string{"nudge", "71", "--self", "5"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.to != 71 || f.self != 5 {
				t.Errorf("parsed = %+v", f)
			}
		}},
		{name: "watch defaults the interval to 2", args: []string{"watch", "--self", "70", "--parent", "68"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.interval != 2 {
				t.Errorf("interval = %d, want 2", f.interval)
			}
		}},
		{name: "watch parses --interval", args: []string{"watch", "--interval", "5"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.interval != 5 {
				t.Errorf("interval = %d, want 5", f.interval)
			}
		}},
		{name: "watch parses an inline --interval value", args: []string{"watch", "--interval=3"}, code: exitcode.OK, want: func(t *testing.T, f *msgFlags) {
			t.Helper()
			if f.interval != 3 {
				t.Errorf("interval = %d, want 3", f.interval)
			}
		}},
		{name: "watch rejects a zero interval", args: []string{"watch", "--interval", "0"}, code: exitcode.Invocation},
		// 86401 pins the overflow guard: a huge interval would wrap
		// time.Duration negative and busy-loop the watch tick.
		{name: "watch rejects an interval above one day", args: []string{"watch", "--interval", "86401"}, code: exitcode.Invocation},
		{name: "watch rejects a negative interval", args: []string{"watch", "--interval", "-1"}, code: exitcode.Invocation},
		{name: "watch rejects a non-integer interval", args: []string{"watch", "--interval", "abc"}, code: exitcode.Invocation},
		{name: "watch rejects a positional argument", args: []string{"watch", "hello"}, code: exitcode.Invocation},
		{name: "watch does not accept --to", args: []string{"watch", "--to", "5"}, code: exitcode.Invocation},
		{name: "--interval is watch-only", args: []string{"inbox", "--interval", "2"}, code: exitcode.Invocation},
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
