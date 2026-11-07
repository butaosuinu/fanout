package paneruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
)

// TestNewBackendConstructsEverySupportedRuntime pins the one switch on concrete
// backends: every supported name builds, and an unknown one fails closed.
func TestNewBackendConstructsEverySupportedRuntime(t *testing.T) {
	tests := []struct {
		name    string
		in      backend.Name
		want    backend.Name
		wantErr string
	}{
		{name: "tmux", in: backend.Tmux, want: backend.Tmux},
		{name: "herdr", in: backend.Herdr, want: backend.Herdr},
		{name: "unknown name fails closed", in: backend.Name("wat"), wantErr: `unknown runtime backend "wat"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewBackend(tt.in, "session", "/tmp/socket")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NewBackend(%q) error = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil || got == nil || got.Name() != tt.want {
				t.Fatalf("NewBackend(%q) = (%v, %v), want %s", tt.in, got, err, tt.want)
			}
		})
	}
}

// TestNewTmuxExposesHostCapabilities guarantees the console keeps the layout
// and owned-close capabilities the plain Backend interface does not carry.
func TestNewTmuxExposesHostCapabilities(t *testing.T) {
	host := NewTmux()
	if host == nil || host.Name() != backend.Tmux {
		t.Fatalf("NewTmux() = %v, want the tmux host runtime", host)
	}
	var _ backend.OwnedCloser = host
	var _ backend.LayoutManager = host
}

// TestNewLaunchBackendDefersLiveOwnedSession pins that a live owned-session
// launch resolves and validates before anything acquires a session: the
// returned runtime is the mutation-free preview and ownership waits on prepare.
func TestNewLaunchBackendDefersLiveOwnedSession(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		selected    backend.Name
		wantPrepare bool
	}{
		{
			name:        "live owned session defers ownership",
			cfg:         Config{ProjectRoot: "/repo"},
			selected:    backend.Herdr,
			wantPrepare: true,
		},
		{
			name:     "dry run never prepares",
			cfg:      Config{ProjectRoot: "/repo", DryRun: true},
			selected: backend.Herdr,
		},
		{
			name:     "tmux needs no preparation",
			cfg:      Config{ProjectRoot: "/repo"},
			selected: backend.Tmux,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, prepare, err := NewLaunchBackend(tt.cfg, tt.selected, Inputs{ProjectRoot: tt.cfg.ProjectRoot})
			if err != nil || got == nil || got.Name() != tt.selected {
				t.Fatalf("NewLaunchBackend() = (%v, %v), want %s", got, err, tt.selected)
			}
			if (prepare != nil) != tt.wantPrepare {
				t.Fatalf("prepare != nil = %t, want %t", prepare != nil, tt.wantPrepare)
			}
		})
	}
}

// TestEntriesKeepSelfExecTokens pins the hidden subcommand recognition the
// runtime bakes into processes it spawns, plus the first-match-wins order the
// composition root iterates.
func TestEntriesKeepSelfExecTokens(t *testing.T) {
	entries := Entries()
	if len(entries) != 4 {
		t.Fatalf("Entries() length = %d, want 4", len(entries))
	}
	if entries[0].Name != "herdr-agent-session-relay" || entries[1].Name != "herdr-pane-launcher" ||
		entries[2].Name != "herdr-dashboard-open" || entries[3].Name != "herdr-supervisor" {
		t.Fatalf("Entries() names = %q/%q/%q/%q", entries[0].Name, entries[1].Name, entries[2].Name, entries[3].Name)
	}
	t.Setenv("FANOUT_HERDR_AGENT_SESSION_RELAY", "bootstrap")
	if !entries[0].Match(nil) {
		t.Fatal("agent-session relay entry rejected its inherited environment marker")
	}
	if !entries[2].Match([]string{"__herdr-dashboard-open", "descriptor"}) {
		t.Fatal("dashboard entry rejected its hidden token")
	}
	if entries[3].Match([]string{"__herdr-supervisor", "marker"}) != true {
		t.Fatal("supervisor entry rejected its hidden token")
	}
	if entries[3].Match([]string{"herdr", "restart"}) {
		t.Fatal("supervisor entry matched the user-facing herdr verb")
	}
	if entries[1].Match(nil) {
		t.Fatal("pane launcher matched without its environment flag")
	}
	for _, entry := range entries {
		if entry.Match == nil || entry.Run == nil {
			t.Fatalf("entry %q is incomplete", entry.Name)
		}
	}
}

// TestObserveManagedRejectsDriftedOwnerRoute keeps telemetry from being
// attributed to a replacement session that reused the repository's owner slot.
func TestObserveManagedRejectsDriftedOwnerRoute(t *testing.T) {
	tests := []struct {
		name    string
		session string
		socket  string
		wantErr string
	}{
		{name: "session drift", session: "other", socket: "/tmp/a.sock", wantErr: "does not match launch binding"},
		{name: "socket drift", session: "owner", socket: "/tmp/b.sock", wantErr: "does not match launch binding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreOpenSession(t, func(context.Context, herdrrun.OwnedOptions) (*herdrrun.OwnedSession, error) {
				return &herdrrun.OwnedSession{Session: "owner", SocketPath: "/tmp/a.sock"}, nil
			})
			_, err := ObserveManaged(context.Background(), ObservationRequest{
				GitCommonDir: "/repo/.git", Session: tt.session, SocketPath: tt.socket,
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ObserveManaged() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestObserveManagedOpensOwnerGitCommonDir pins that the observation opens the
// owner named by the persisted binding, not the ambient one.
func TestObserveManagedOpensOwnerGitCommonDir(t *testing.T) {
	wantErr := errors.New("stop after admission input")
	var got string
	restoreOpenSession(t, func(_ context.Context, opts herdrrun.OwnedOptions) (*herdrrun.OwnedSession, error) {
		got = opts.GitCommonDir
		return nil, wantErr
	})

	_, err := ObserveManaged(context.Background(), ObservationRequest{GitCommonDir: "/repo/.git"})
	if !errors.Is(err, wantErr) || got != "/repo/.git" {
		t.Fatalf("ObserveManaged() = (%q, %v), want owner Git common dir and sentinel", got, err)
	}
}

func restoreOpenSession(
	t *testing.T,
	stub func(context.Context, herdrrun.OwnedOptions) (*herdrrun.OwnedSession, error),
) {
	t.Helper()
	previous := openSession
	openSession = stub
	t.Cleanup(func() { openSession = previous })
}
