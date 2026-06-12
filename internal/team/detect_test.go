package team

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/butaosuinu/fanout/internal/state"
)

func TestParseFanoutTag(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		wantIssue  int
		wantParent string
		wantOK     bool
	}{
		{
			name:       "issue parent",
			prompt:     "[fanout #69 of #68] team-sqlite-base-69: t. read /tmp/x.md and begin.",
			wantIssue:  69,
			wantParent: "68",
			wantOK:     true,
		},
		{name: "tag without parent", prompt: "[fanout #12] do things", wantIssue: 12, wantParent: "", wantOK: true},
		{
			name:       "project URL parent",
			prompt:     "[fanout #5 of #https://github.com/orgs/x/projects/1] x",
			wantIssue:  5,
			wantParent: "https://github.com/orgs/x/projects/1",
			wantOK:     true,
		},
		{name: "no tag", prompt: "hello", wantOK: false},
		{name: "tag not at start", prompt: " [fanout #1] x", wantOK: false},
		{name: "missing issue number", prompt: "[fanout #of #2] x", wantOK: false},
		{name: "empty prompt", prompt: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue, parent, ok := ParseFanoutTag(tt.prompt)
			if ok != tt.wantOK {
				t.Fatalf("ParseFanoutTag(%q) ok = %v, want %v", tt.prompt, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if issue != tt.wantIssue || parent != tt.wantParent {
				t.Errorf("ParseFanoutTag(%q) = (%d, %q), want (%d, %q)",
					tt.prompt, issue, parent, tt.wantIssue, tt.wantParent)
			}
		})
	}
}

func TestIdentifyPane(t *testing.T) {
	recorded := state.Pane{
		Parent:   "68",
		IssueNum: 69,
		Slug:     "team-sqlite-base-69",
		PaneID:   "%5",
		Prompt:   "[fanout #69 of #68] team-sqlite-base-69: t. read /tmp/x.md and begin.",
	}
	degenerate := state.Pane{
		PaneID: "%7",
		Prompt: "[fanout #7 of #3] x: y. read /tmp/z.md and begin.",
	}
	halfRecorded := state.Pane{
		Parent: "68",
		PaneID: "%8",
		Prompt: "[fanout #12] x: y. read /tmp/z.md and begin.",
	}
	stale := state.Pane{
		Parent:   "9",
		IssueNum: 1,
		PaneID:   "%5",
		Prompt:   "[fanout #1 of #9] old: o. read /tmp/o.md and begin.",
	}
	manual := state.Pane{
		Parent:   "@manual",
		IssueNum: 42,
		PaneID:   "%9",
		Prompt:   "do stuff",
	}

	tests := []struct {
		name       string
		paneID     string
		store      state.Store
		wantIssue  int
		wantParent string
		wantErr    error
	}{
		{name: "empty pane id", paneID: "", store: state.Store{}, wantErr: ErrNotInTmux},
		{
			name:       "recorded row wins",
			paneID:     "%5",
			store:      state.Store{Panes: []state.Pane{recorded}},
			wantIssue:  69,
			wantParent: "68",
		},
		{
			name:    "pane not recorded",
			paneID:  "%99",
			store:   state.Store{Panes: []state.Pane{recorded}},
			wantErr: ErrPaneNotFound,
		},
		{name: "empty store (missing state file)", paneID: "%5", store: state.Store{}, wantErr: ErrPaneNotFound},
		{
			name:       "degenerate row falls back to prompt tag",
			paneID:     "%7",
			store:      state.Store{Panes: []state.Pane{recorded, degenerate}},
			wantIssue:  7,
			wantParent: "3",
		},
		{
			name:       "tag fallback fills gaps without clobbering recorded parent",
			paneID:     "%8",
			store:      state.Store{Panes: []state.Pane{halfRecorded}},
			wantIssue:  12,
			wantParent: "68",
		},
		{
			name:       "stale duplicate pane id: newest row wins",
			paneID:     "%5",
			store:      state.Store{Panes: []state.Pane{stale, recorded}},
			wantIssue:  69,
			wantParent: "68",
		},
		{
			name:       "partial row without tag keeps recorded values",
			paneID:     "%9",
			store:      state.Store{Panes: []state.Pane{manual}},
			wantIssue:  42,
			wantParent: "@manual",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := IdentifyPane(tt.paneID, tt.store)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("IdentifyPane error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("IdentifyPane: %v", err)
			}
			if id.Issue != tt.wantIssue || id.Parent != tt.wantParent {
				t.Errorf("IdentifyPane = (#%d of %q), want (#%d of %q)",
					id.Issue, id.Parent, tt.wantIssue, tt.wantParent)
			}
			if id.Pane.PaneID != tt.paneID {
				t.Errorf("Identity.Pane.PaneID = %q, want %q", id.Pane.PaneID, tt.paneID)
			}
		})
	}
}

func TestDetectWithStatePathOverride(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	fixture := `{"schemaVersion":1,"panes":[{"parent":"68","issueNum":69,"slug":"s","paneId":"%5","prompt":"[fanout #69 of #68] s: t. read /tmp/x.md and begin."}]}`
	if err := os.WriteFile(statePath, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("TMUX_PANE", "%5")
	t.Setenv(fanoutStatePathEnv, statePath)

	id, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if id.Issue != 69 || id.Parent != "68" {
		t.Errorf("Detect = (#%d of %q), want (#69 of \"68\")", id.Issue, id.Parent)
	}
}

func TestDetectOutsideTmux(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	if _, err := Detect(); !errors.Is(err, ErrNotInTmux) {
		t.Fatalf("Detect outside tmux = %v, want ErrNotInTmux", err)
	}
}

// runGit fails the test on error; helper for the real-git integration test.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestMainRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	mainRoot := t.TempDir()
	runGit(t, mainRoot, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(mainRoot, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, mainRoot, "add", ".")
	runGit(t, mainRoot, "commit", "-m", "init")

	subdir := filepath.Join(mainRoot, "internal", "team")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	worktree := filepath.Join(mainRoot, ".fanout", "worktrees", "child-1")
	runGit(t, mainRoot, "worktree", "add", worktree, "-b", "child-1")
	worktreeSubdir := filepath.Join(worktree, "internal")
	if err := os.MkdirAll(worktreeSubdir, 0o755); err != nil {
		t.Fatalf("mkdir worktree subdir: %v", err)
	}

	wantRoot, err := filepath.EvalSymlinks(mainRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", mainRoot, err)
	}

	tests := []struct {
		name string
		cwd  string
	}{
		{name: "main repo root", cwd: mainRoot},
		{name: "main repo subdir", cwd: subdir},
		{name: "child worktree root", cwd: worktree},
		{name: "child worktree subdir", cwd: worktreeSubdir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(tt.cwd)
			got, err := MainRepoRoot()
			if err != nil {
				t.Fatalf("MainRepoRoot: %v", err)
			}
			resolved, err := filepath.EvalSymlinks(got)
			if err != nil {
				t.Fatalf("EvalSymlinks(%q): %v", got, err)
			}
			if resolved != wantRoot {
				t.Errorf("MainRepoRoot from %s = %q (resolved %q), want %q", tt.name, got, resolved, wantRoot)
			}
		})
	}
}
