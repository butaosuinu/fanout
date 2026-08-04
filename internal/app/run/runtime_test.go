package run

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
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
