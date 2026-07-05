package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

// installDepShims populates a PATH-only dir with the named fake binaries.
// tmux answers `tmux -V` with a modern version so CheckMinimumVersion passes.
func installDepShims(t *testing.T, names ...string) {
	t.Helper()
	binDir := t.TempDir()
	for _, name := range names {
		body := "#!/bin/sh\nexit 0\n"
		if name == "tmux" {
			body = "#!/bin/sh\nprintf 'tmux 3.6a\\n'\n"
		}
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)
}

// TestMissingDeps pins the hint strings and the fixed git → gh → tmux probe
// order shared by the issue, plan, and TUI lanes.
func TestMissingDeps(t *testing.T) {
	tests := []struct {
		name      string
		installed []string
		needs     depNeeds
		want      []string
	}{
		{
			name:      "all present yields nothing",
			installed: []string{"git", "gh", "tmux"},
			needs:     depNeeds{git: true, gh: true, tmux: true},
			want:      nil,
		},
		{
			name:      "missing gh yields the brew hint",
			installed: []string{"git", "tmux"},
			needs:     depNeeds{git: true, gh: true, tmux: true},
			want:      []string{"gh (brew install gh)"},
		},
		{
			name:      "unneeded gh is not probed",
			installed: []string{"git", "tmux"},
			needs:     depNeeds{git: true, tmux: true},
			want:      nil,
		},
		{
			name:      "missing git and gh keep the git-first order",
			installed: []string{"tmux"},
			needs:     depNeeds{git: true, gh: true},
			want:      []string{"git", "gh (brew install gh)"},
		},
		{
			name:      "missing tmux reports the version hint last",
			installed: []string{},
			needs:     depNeeds{git: true, tmux: true},
			want:      []string{"git", "tmux " + tmuxrun.MinimumVersion + "+ (brew install tmux)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installDepShims(t, tt.installed...)
			if got := missingDeps(tt.needs); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("missingDeps(%+v) = %v, want %v", tt.needs, got, tt.want)
			}
		})
	}
}

// TestExitOnMissingDeps pins the exact "missing dependencies:" stderr block.
func TestExitOnMissingDeps(t *testing.T) {
	tests := []struct {
		name       string
		missing    []string
		want       bool
		wantStderr string
	}{
		{
			name:       "empty list prints nothing and does not exit",
			missing:    nil,
			want:       false,
			wantStderr: "",
		},
		{
			name:       "each missing dep becomes an indented dash line",
			missing:    []string{"git", "gh (brew install gh)"},
			want:       true,
			wantStderr: "[err ] missing dependencies:\n  - git\n  - gh (brew install gh)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			lg := log.NewWith(&out, &errBuf, false)
			if got := exitOnMissingDeps(tt.missing, lg); got != tt.want {
				t.Fatalf("exitOnMissingDeps(%v) = %v, want %v", tt.missing, got, tt.want)
			}
			if errBuf.String() != tt.wantStderr {
				t.Fatalf("exitOnMissingDeps(%v) stderr = %q, want %q", tt.missing, errBuf.String(), tt.wantStderr)
			}
			if out.Len() != 0 {
				t.Fatalf("exitOnMissingDeps(%v) wrote to stdout: %q", tt.missing, out.String())
			}
		})
	}
}
