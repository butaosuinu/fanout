package tmuxbackend_test

import (
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
)

// Console restore is a tmux-only lane, and this is the half that says so: tmux
// carries the capability restore is gated on. The other half is herdr offering
// none (see internal/infra/herdrrun).
func TestRestoreCapabilitiesArePresent(t *testing.T) {
	b := tmuxbackend.New()
	if _, ok := backend.AsRestoreOps(b); !ok {
		t.Fatal("AsRestoreOps(tmux backend) reported no capability")
	}
	if _, ok := backend.AsPaneLocator(b); !ok {
		t.Fatal("AsPaneLocator(tmux backend) reported no capability")
	}
}

// The strict sweep carries the border label restore's adoption check compares
// against; ListLive's mapping drops nothing the identity sweep needs.
func TestRestoreOpsIdentitySweepCarriesPaneLabel(t *testing.T) {
	installTmuxShim(t, `
case "$4" in
  *pane_current_path*) printf '%%7\t/tmp/repo\n' ;;
  *pane_title*) printf '%%7\tRestore API\n' ;;
  *@fanout_shell_key*) printf '%%7\tshell-7\n' ;;
  *@fanout_pane_label*) printf '%%7\t#81 · Restore API\n' ;;
  *) printf '%%7\t\n' ;;
esac
`)
	ops, ok := backend.AsRestoreOps(tmuxbackend.New())
	if !ok {
		t.Fatal("AsRestoreOps(tmux backend) reported no capability")
	}

	got, err := ops.ListLiveForIdentity()
	if err != nil {
		t.Fatalf("ListLiveForIdentity() failed: %v", err)
	}
	want := []backend.LivePane{{
		Ref:          backend.PaneRef{Backend: backend.Tmux, Pane: "%7"},
		CurrentPath:  "/tmp/repo",
		Title:        "Restore API",
		PaneLabel:    "#81 · Restore API",
		ShellKey:     "shell-7",
		ProjectRoot:  "",
		WorktreePath: "",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListLiveForIdentity() = %#v, want %#v", got, want)
	}
}

// Restore decides whether to launch a replacement pane from this sweep, so an
// incomplete listing must error instead of degrading a field to empty.
func TestRestoreOpsIdentitySweepFailsOnIncompleteListing(t *testing.T) {
	installTmuxShim(t, `
case "$4" in
  *pane_current_path*) printf '%%7\t/tmp/repo\n' ;;
  *pane_title*) exit 7 ;;
  *) printf '%%7\t\n' ;;
esac
`)
	ops, _ := backend.AsRestoreOps(tmuxbackend.New())

	if _, err := ops.ListLiveForIdentity(); err == nil {
		t.Fatal("ListLiveForIdentity() succeeded on a failed title listing, want an error")
	}
}

func TestRestoreOpsListPanesQueriesOneTarget(t *testing.T) {
	logPath := installTmuxShim(t, `
case "$1" in
  has-session) exit 0 ;;
  *) printf '%%3:@1:0:1:Restore API\n' ;;
esac
`)
	ops, _ := backend.AsRestoreOps(tmuxbackend.New())

	got, err := ops.ListPanes("fanout")
	if err != nil {
		t.Fatalf("ListPanes() failed: %v", err)
	}
	want := []backend.PaneInfo{{ID: "%3", WindowID: "@1", Index: 0, Active: true, Title: "Restore API"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListPanes() = %#v, want %#v", got, want)
	}
	wantCalls := [][]string{
		{"has-session", "-t", "=fanout"},
		{"list-panes", "-s", "-t", "=fanout", "-F", "#{pane_id}:#{window_id}:#{pane_index}:#{pane_active}:#{pane_title}"},
	}
	if calls := readCalls(t, logPath); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("tmux calls = %#v, want %#v", calls, wantCalls)
	}
}

// The two provenance clocks are what adoption proves ownership with, so both
// are read from the runtime rather than reconstructed by the caller.
func TestRestoreOpsReadsProvenanceClocks(t *testing.T) {
	logPath := installTmuxShim(t, `
case "$*" in
  *pane_pid*) printf '`+strconv.Itoa(os.Getpid())+`\n' ;;
  *) printf '1700000000\n' ;;
esac
`)
	ops, _ := backend.AsRestoreOps(tmuxbackend.New())

	serverStart, err := ops.ServerStartTime()
	if err != nil {
		t.Fatalf("ServerStartTime() failed: %v", err)
	}
	if want := time.Unix(1_700_000_000, 0); !serverStart.Equal(want) {
		t.Fatalf("ServerStartTime() = %v, want %v", serverStart, want)
	}

	// The pane's root process is this test binary, so its start time must be in
	// the recent past rather than the tmux server's epoch.
	paneStart, err := ops.PaneStartTime("%7")
	if err != nil {
		t.Fatalf("PaneStartTime() failed: %v", err)
	}
	if age := time.Since(paneStart); age < 0 || age > time.Hour {
		t.Fatalf("PaneStartTime() = %v (age %v), want a recent process start", paneStart, age)
	}

	calls := readCalls(t, logPath)
	want := [][]string{
		{"display-message", "-p", "#{start_time}"},
		{"display-message", "-p", "-t", "%7", "#{pane_pid}"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("tmux calls = %#v, want %#v", calls, want)
	}
}

// A row's own label has to be canonicalized the way SetPaneLabel stored the
// live one, or a hostile display name would defeat the adoption comparison.
func TestRestoreOpsCanonicalPaneLabelMatchesStoredForm(t *testing.T) {
	ops, _ := backend.AsRestoreOps(tmuxbackend.New())
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain label is stored verbatim", in: "#81 · Restore API", want: "#81 · Restore API"},
		{name: "style sequence is defused", in: "@manual · #[fg=red]x", want: "@manual · [fg=red]x"},
		{name: "doubled hash cannot re-arm the style", in: "##[fg=red]x", want: "[fg=red]x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ops.CanonicalPaneLabel(tt.in); got != tt.want {
				t.Fatalf("CanonicalPaneLabel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPaneLocatorReadsPaneCurrentPath(t *testing.T) {
	logPath := installTmuxShim(t, `printf '/tmp/repo\n'`)
	locator, ok := backend.AsPaneLocator(tmuxbackend.New())
	if !ok {
		t.Fatal("AsPaneLocator(tmux backend) reported no capability")
	}

	got, err := locator.PaneCurrentPath("%7")
	if err != nil {
		t.Fatalf("PaneCurrentPath() failed: %v", err)
	}
	if want := "/tmp/repo"; got != want {
		t.Fatalf("PaneCurrentPath() = %q, want %q", got, want)
	}
	want := [][]string{{"display-message", "-p", "-t", "%7", "#{pane_current_path}"}}
	if calls := readCalls(t, logPath); !reflect.DeepEqual(calls, want) {
		t.Fatalf("tmux calls = %#v, want %#v", calls, want)
	}
}
