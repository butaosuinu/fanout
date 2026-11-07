package run

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/backendtest"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
)

type dashboardShortcutFake struct{ backend.Backend }

func (dashboardShortcutFake) SyncDashboardShortcut(backend.DashboardShortcutOptions) error {
	return nil
}

func TestShouldBindRuntimeKeys(t *testing.T) {
	tests := []struct {
		name           string
		dryRun         bool
		created        int
		runtimeBackend backend.Backend
		want           bool
	}{
		{name: "live launch on a shortcut-capable backend", created: 1, runtimeBackend: backendtest.NewTmux(), want: true},
		{name: "live launch on a dashboard-only backend", created: 1, runtimeBackend: dashboardShortcutFake{backendtest.New()}, want: true},
		{name: "dry run", dryRun: true, created: 1, runtimeBackend: backendtest.NewTmux()},
		{name: "no created panes", runtimeBackend: backendtest.NewTmux()},
		{name: "backend without global shortcuts", created: 1, runtimeBackend: backendtest.New()},
		{name: "nil backend", created: 1, runtimeBackend: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldBindRuntimeKeys(tt.dryRun, tt.created, tt.runtimeBackend); got != tt.want {
				t.Fatalf("shouldBindRuntimeKeys() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestLoadStateIgnoresLockFileWhenNoWorktreeIsPrepared(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init")

	lg := log.NewWith(io.Discard, io.Discard, false)
	rt := &Runtime{Info: &fanoutruntime.Info{ProjectRoot: repo}}
	_, recorder, code := LoadState(false, rt, lg)
	if code != exitcode.OK {
		t.Fatalf("LoadState code = %d, want %d", code, exitcode.OK)
	}
	if recorder == nil {
		t.Fatal("LoadState returned nil recorder for live run")
	}
	t.Cleanup(func() { _ = recorder.Unlock() })

	if _, err := os.Stat(filepath.Join(repo, ".fanout", "state.json.lock")); err != nil {
		t.Fatalf("state lock was not created: %v", err)
	}
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exclude), ".fanout/state.json.lock\n") {
		t.Fatalf("exclude = %q, want state lock pattern", exclude)
	}
}

func TestLoadStateObservesSessionsBeforeLiveLaunchLock(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init")
	calls := 0
	rt := &Runtime{
		Info: &fanoutruntime.Info{ProjectRoot: repo},
		ListLive: func() ([]backend.LivePane, error) {
			calls++
			return nil, nil
		},
	}
	_, recorder, code := LoadState(false, rt, log.NewWith(io.Discard, io.Discard, false))
	if code != exitcode.OK || recorder == nil || calls != 1 {
		t.Fatalf("LoadState code=%d recorder=%v live calls=%d, want one pre-lock observation", code, recorder, calls)
	}
	t.Cleanup(func() { _ = recorder.Unlock() })
}

func TestLoadStateDryRunDoesNotRebindSession(t *testing.T) {
	repo := t.TempDir()
	calls := 0
	rt := &Runtime{
		Info: &fanoutruntime.Info{ProjectRoot: repo},
		ListLive: func() ([]backend.LivePane, error) {
			calls++
			return nil, nil
		},
	}

	_, recorder, code := LoadState(true, rt, log.NewWith(io.Discard, io.Discard, false))
	if code != exitcode.OK || recorder != nil || calls != 0 {
		t.Fatalf("LoadState code=%d recorder=%v live calls=%d, want read-only load", code, recorder, calls)
	}
}

func TestMarkCurrentPaneProjectRoot(t *testing.T) {
	tests := []struct {
		name        string
		undecorated bool
		info        *fanoutruntime.Info
		want        []backendtest.PaneValue
	}{
		{
			// --session points Target at a whole session, so the hint must follow
			// the invoking pane instead.
			name: "annotates the invoking pane, not the session target",
			info: &fanoutruntime.Info{Target: "fanout-repo", ProjectRoot: "/repo", InvokingPane: "%9"},
			want: []backendtest.PaneValue{{PaneID: "%9", Value: "/repo"}},
		},
		{
			name: "environment names no invoking pane",
			info: &fanoutruntime.Info{Target: "%1", ProjectRoot: "/repo"},
		},
		{
			// A bare fake exposes no PaneDecorator, standing in for herdr today.
			name:        "backend without pane decoration",
			undecorated: true,
			info:        &fanoutruntime.Info{ProjectRoot: "/repo", InvokingPane: "%9"},
		},
	}
	lg := log.NewWith(io.Discard, io.Discard, false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decorator := backendtest.NewDecorator()
			runtimeBackend := backend.Backend(decorator)
			if tt.undecorated {
				runtimeBackend = backendtest.New()
			}

			markCurrentPaneProjectRoot(runtimeBackend, tt.info, lg)

			got := decorator.PaneValues(backendtest.MethodSetPaneProjectRoot)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("markCurrentPaneProjectRoot() hints = %v, want %v", got, tt.want)
			}
		})
	}
}
