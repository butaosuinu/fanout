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
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
)

func TestShouldBindRuntimeKeys(t *testing.T) {
	tests := []struct {
		name           string
		dryRun         bool
		created        int
		runtimeBackend backend.Name
		want           bool
	}{
		{name: "live tmux launch", created: 1, runtimeBackend: backend.Tmux, want: true},
		{name: "dry run", dryRun: true, created: 1, runtimeBackend: backend.Tmux},
		{name: "no created panes", runtimeBackend: backend.Tmux},
		{name: "herdr launch", created: 1, runtimeBackend: backend.Herdr},
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
	_, recorder, code := LoadState(false, repo, lg)
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

// recordingDecorator captures the dashboard hint a run writes on its own pane.
// The embedded nil Backend supplies the required surface: only the decoration
// methods are ever called.
type recordingDecorator struct {
	backend.Backend
	projectRoots [][2]string
}

func (*recordingDecorator) SetPaneTitle(string, string) error        { return nil }
func (*recordingDecorator) SetPaneLabel(string, string) error        { return nil }
func (*recordingDecorator) EnablePaneBorderTitles(string) error      { return nil }
func (*recordingDecorator) SetPaneWorktreePath(string, string) error { return nil }

func (b *recordingDecorator) SetPaneProjectRoot(paneID, projectRoot string) error {
	b.projectRoots = append(b.projectRoots, [2]string{paneID, projectRoot})
	return nil
}

// undecoratedBackend stands in for a runtime with no pane decoration, herdr today.
type undecoratedBackend struct{ backend.Backend }

func TestMarkCurrentPaneProjectRoot(t *testing.T) {
	tests := []struct {
		name        string
		undecorated bool
		info        *fanoutruntime.Info
		want        [][2]string
	}{
		{
			// --session points Target at a whole session, so the hint must follow
			// the invoking pane instead.
			name: "annotates the invoking pane, not the session target",
			info: &fanoutruntime.Info{Target: "fanout-repo", ProjectRoot: "/repo", InvokingPane: "%9"},
			want: [][2]string{{"%9", "/repo"}},
		},
		{
			name: "environment names no invoking pane",
			info: &fanoutruntime.Info{Target: "%1", ProjectRoot: "/repo"},
		},
		{
			name:        "backend without pane decoration",
			undecorated: true,
			info:        &fanoutruntime.Info{ProjectRoot: "/repo", InvokingPane: "%9"},
		},
	}
	lg := log.NewWith(io.Discard, io.Discard, false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decorator := &recordingDecorator{}
			runtimeBackend := backend.Backend(decorator)
			if tt.undecorated {
				runtimeBackend = undecoratedBackend{}
			}

			markCurrentPaneProjectRoot(runtimeBackend, tt.info, lg)

			if !reflect.DeepEqual(decorator.projectRoots, tt.want) {
				t.Fatalf("markCurrentPaneProjectRoot() hints = %v, want %v", decorator.projectRoots, tt.want)
			}
		})
	}
}
