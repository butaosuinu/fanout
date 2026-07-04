package main

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

// TestPickConsolePane pins the console-return selection order — project root
// beats session beats listing order — and the role + title double match.
func TestPickConsolePane(t *testing.T) {
	console := func(id, root, session string) tmuxrun.LivePane {
		return tmuxrun.LivePane{ID: id, Title: tuiPaneTitle, Role: tmuxrun.RoleConsole, ProjectRoot: root, SessionID: session}
	}
	tests := []struct {
		name  string
		from  tmuxrun.LivePane
		panes []tmuxrun.LivePane
		want  string // pane id; "" means no console found
	}{
		{
			name:  "no live panes yields no console",
			from:  tmuxrun.LivePane{ID: "%1", SessionID: "$1"},
			panes: nil,
			want:  "",
		},
		{
			name: "console role without the TUI title is excluded",
			from: tmuxrun.LivePane{ID: "%1", SessionID: "$1"},
			panes: []tmuxrun.LivePane{
				{ID: "%2", Title: "zsh", Role: tmuxrun.RoleConsole, SessionID: "$1"},
			},
			want: "",
		},
		{
			name: "TUI title without the console role is excluded",
			from: tmuxrun.LivePane{ID: "%1", SessionID: "$1"},
			panes: []tmuxrun.LivePane{
				{ID: "%2", Title: tuiPaneTitle, SessionID: "$1"},
			},
			want: "",
		},
		{
			// Root outranks session: one global key must stay correct when
			// consoles for several repos share a tmux session.
			name: "root match in another session beats same-session other-repo console",
			from: tmuxrun.LivePane{ID: "%1", ProjectRoot: "/repo/a", SessionID: "$1"},
			panes: []tmuxrun.LivePane{
				console("%2", "/repo/b", "$1"),
				console("%3", "/repo/a", "$2"),
			},
			want: "%3",
		},
		{
			name: "multiple root matches prefer the same session",
			from: tmuxrun.LivePane{ID: "%1", ProjectRoot: "/repo/a", SessionID: "$1"},
			panes: []tmuxrun.LivePane{
				console("%2", "/repo/a", "$2"),
				console("%3", "/repo/a", "$1"),
			},
			want: "%3",
		},
		{
			name: "root matches all in other sessions fall back to the first of them",
			from: tmuxrun.LivePane{ID: "%1", ProjectRoot: "/repo/a", SessionID: "$1"},
			panes: []tmuxrun.LivePane{
				console("%2", "/repo/a", "$3"),
				console("%3", "/repo/a", "$2"),
			},
			want: "%2",
		},
		{
			name: "unclean recorded root still matches", // samePath cleans both sides
			from: tmuxrun.LivePane{ID: "%1", ProjectRoot: "/repo/a/", SessionID: "$1"},
			panes: []tmuxrun.LivePane{
				console("%2", "/repo/b", "$1"),
				console("%3", "/repo/a", "$2"),
			},
			want: "%3",
		},
		{
			name: "pressing pane without a recorded root falls back to same session",
			from: tmuxrun.LivePane{ID: "%1", SessionID: "$2"},
			panes: []tmuxrun.LivePane{
				console("%2", "/repo/a", "$1"),
				console("%3", "/repo/b", "$2"),
			},
			want: "%3",
		},
		{
			name: "recorded root with no matching console falls back to same session",
			from: tmuxrun.LivePane{ID: "%1", ProjectRoot: "/repo/c", SessionID: "$2"},
			panes: []tmuxrun.LivePane{
				console("%2", "/repo/a", "$1"),
				console("%3", "/repo/b", "$2"),
			},
			want: "%3",
		},
		{
			name: "no root and no session match falls back to the first candidate",
			from: tmuxrun.LivePane{ID: "%1", SessionID: "$9"},
			panes: []tmuxrun.LivePane{
				console("%2", "/repo/a", "$1"),
				console("%3", "/repo/b", "$2"),
			},
			want: "%2",
		},
		{
			name: "zero pressing pane still lands on the first candidate", // --from pane vanished between keypress and lookup
			from: tmuxrun.LivePane{},
			panes: []tmuxrun.LivePane{
				console("%2", "/repo/a", "$1"),
			},
			want: "%2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pickConsolePane(tt.from, tt.panes)
			if tt.want == "" {
				if ok {
					t.Fatalf("pickConsolePane() = %#v, want no console", got)
				}
				return
			}
			if !ok || got.ID != tt.want {
				t.Fatalf("pickConsolePane() = %q, %v, want %q", got.ID, ok, tt.want)
			}
		})
	}
}

func TestParseFocusConsoleFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		env      string // TMUX_PANE
		want     focusConsoleFlags
		wantCode exitcode.Code
	}{
		{name: "explicit --from wins over TMUX_PANE", args: []string{"--from", "%5"}, env: "%9", want: focusConsoleFlags{fromID: "%5"}, wantCode: exitcode.OK},
		{name: "trims surrounding whitespace", args: []string{"--from", " %5 "}, want: focusConsoleFlags{fromID: "%5"}, wantCode: exitcode.OK},
		{name: "missing --from falls back to TMUX_PANE", env: "%9", want: focusConsoleFlags{fromID: "%9"}, wantCode: exitcode.OK},
		{name: "no --from and no TMUX_PANE proceeds with an empty id", want: focusConsoleFlags{}, wantCode: exitcode.OK},
		{name: "--client records the pressing client", args: []string{"--from", "%5", "--client", "/dev/ttys004"}, want: focusConsoleFlags{fromID: "%5", client: "/dev/ttys004"}, wantCode: exitcode.OK},
		{name: "--help short-circuits without touching tmux", args: []string{"--help"}, want: focusConsoleFlags{help: true}, wantCode: exitcode.OK},
		{name: "--from without a value errors", args: []string{"--from"}, wantCode: exitcode.Invocation},
		{name: "--client without a value errors", args: []string{"--client"}, wantCode: exitcode.Invocation},
		{name: "unknown option errors", args: []string{"--pane", "%5"}, wantCode: exitcode.Invocation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX_PANE", tt.env)
			got, code := parseFocusConsoleFlags(tt.args, log.New(false))
			if code != tt.wantCode {
				t.Fatalf("parseFocusConsoleFlags(%v) code = %v, want %v", tt.args, code, tt.wantCode)
			}
			if code == exitcode.OK && got != tt.want {
				t.Fatalf("parseFocusConsoleFlags(%v) = %#v, want %#v", tt.args, got, tt.want)
			}
		})
	}
}
