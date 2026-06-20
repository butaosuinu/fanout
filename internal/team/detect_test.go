package team

import (
	"errors"
	"fmt"
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
	inWorktree := state.Pane{
		Parent:       "68",
		IssueNum:     70,
		PaneID:       "%2",
		WorktreePath: "/repo/.fanout/worktrees/msg-cli-70",
		Prompt:       "[fanout #70 of #68] msg-cli-70: t. read /tmp/y.md and begin.",
	}
	shellInWorktree := state.Pane{
		Parent:       "@manual",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		PaneID:       "%shell",
		WorktreePath: "/repo/.fanout/worktrees/msg-cli-70",
	}

	tests := []struct {
		name       string
		paneID     string
		worktree   string
		store      state.Store
		wantIssue  int
		wantParent string
		wantErr    error
	}{
		{name: "no pane id and no worktree", paneID: "", store: state.Store{}, wantErr: ErrNotInTmux},
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
		{
			name:       "worktree match beats pane id",
			paneID:     "%5",
			worktree:   "/repo/.fanout/worktrees/msg-cli-70",
			store:      state.Store{Panes: []state.Pane{recorded, inWorktree}},
			wantIssue:  70,
			wantParent: "68",
		},
		{
			name:       "shell row does not shadow managed worktree identity",
			paneID:     "%2",
			worktree:   "/repo/.fanout/worktrees/msg-cli-70",
			store:      state.Store{Panes: []state.Pane{inWorktree, shellInWorktree}},
			wantIssue:  70,
			wantParent: "68",
		},
		{
			name:       "shell pane id still identifies shell without managed worktree row",
			paneID:     "%shell",
			worktree:   "/repo/.fanout/worktrees/msg-cli-70",
			store:      state.Store{Panes: []state.Pane{shellInWorktree}},
			wantIssue:  -1,
			wantParent: "@manual",
		},
		{
			name:       "worktree alone identifies without pane id",
			paneID:     "",
			worktree:   "/repo/.fanout/worktrees/msg-cli-70",
			store:      state.Store{Panes: []state.Pane{inWorktree}},
			wantIssue:  70,
			wantParent: "68",
		},
		{
			name:     "worktree alone without matching row",
			paneID:   "",
			worktree: "/repo/.fanout/worktrees/other",
			store:    state.Store{Panes: []state.Pane{inWorktree}},
			wantErr:  ErrPaneNotFound,
		},
		{
			name:     "reused pane id with conflicting worktree is rejected",
			paneID:   "%2",
			worktree: "/repo/.fanout/worktrees/other",
			store:    state.Store{Panes: []state.Pane{inWorktree}},
			wantErr:  ErrPaneNotFound,
		},
		{
			name:       "pane id match tolerates rows without a recorded worktree",
			paneID:     "%5",
			worktree:   "/repo/.fanout/worktrees/other",
			store:      state.Store{Panes: []state.Pane{recorded}},
			wantIssue:  69,
			wantParent: "68",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := IdentifyPane(tt.paneID, tt.worktree, tt.store)
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
		})
	}
}

func TestIdentifyPaneSynthesizesPlanTaskIdentity(t *testing.T) {
	plan := state.Pane{
		Parent:       "plan:launch-plan",
		IssueNum:     0,
		TaskID:       "base-types",
		PaneID:       "%5",
		WorktreePath: "/repo/.fanout/worktrees/launch-plan-base-types",
		Prompt:       "[fanout base-types of plan:launch-plan] launch-plan-base-types: t. read /tmp/x.md and begin.",
	}
	st := state.Store{Panes: []state.Pane{plan}}

	id, err := IdentifyPane("%5", "", st)
	if err != nil {
		t.Fatalf("IdentifyPane: %v", err)
	}
	if id.TaskID != "base-types" {
		t.Errorf("Identity.TaskID = %q, want base-types", id.TaskID)
	}
	if id.Parent != "plan:launch-plan" {
		t.Errorf("Identity.Parent = %q, want plan:launch-plan", id.Parent)
	}
	want := TaskPeerNum("plan:launch-plan", "base-types")
	if id.Issue != want {
		t.Errorf("Identity.Issue = %d, want synthetic %d", id.Issue, want)
	}
	if id.Issue == 0 {
		t.Error("Identity.Issue is 0; a plan pane must self-detect a non-zero peer number")
	}
}

func TestDetectWithStatePathOverride(t *testing.T) {
	// A bare temp dir is not a git work tree, so detection rests on
	// TMUX_PANE + FANOUT_STATE_PATH alone regardless of where tests run.
	t.Chdir(t.TempDir())
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

func TestDetectOutsideTmuxAndWorktree(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TMUX_PANE", "")
	if _, err := Detect(); !errors.Is(err, ErrNotInTmux) {
		t.Fatalf("Detect outside tmux = %v, want ErrNotInTmux", err)
	}
}

// Inside a fanout child worktree the worktree path alone identifies the
// pane — even from a plain shell without TMUX_PANE, and across tmux server
// restarts that reassign pane ids.
func TestDetectFromWorktreeWithoutTmuxPane(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	owner := t.TempDir()
	runGit(t, owner, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(owner, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, owner, "add", ".")
	runGit(t, owner, "commit", "-m", "init")
	child := filepath.Join(owner, ".fanout", "worktrees", "s-7")
	runGit(t, owner, "worktree", "add", child, "-b", "s-7")

	// Record the worktree path the way fanout does, matching what git
	// reports as the toplevel (symlinks resolved, e.g. /var -> /private/var
	// on macOS).
	recordedPath, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", child, err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	fixture := fmt.Sprintf(
		`{"schemaVersion":1,"panes":[{"parent":"3","issueNum":7,"slug":"s-7","paneId":"%%0","worktreePath":%q,"prompt":"[fanout #7 of #3] s-7: t. read /tmp/x.md and begin."}]}`,
		recordedPath,
	)
	if err = os.WriteFile(statePath, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	t.Chdir(child)
	t.Setenv("TMUX_PANE", "")
	t.Setenv(fanoutStatePathEnv, statePath)

	id, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if id.Issue != 7 || id.Parent != "3" {
		t.Errorf("Detect = (#%d of %q), want (#7 of \"3\")", id.Issue, id.Parent)
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

func TestOwnerProjectRoot(t *testing.T) {
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
	childOfMain := filepath.Join(mainRoot, ".fanout", "worktrees", "child-1")
	runGit(t, mainRoot, "worktree", "add", childOfMain, "-b", "child-1")
	childOfMainSubdir := filepath.Join(childOfMain, "internal")
	if err := os.MkdirAll(childOfMainSubdir, 0o755); err != nil {
		t.Fatalf("mkdir worktree subdir: %v", err)
	}

	// A fanout run started from a user's own linked worktree records state
	// under that worktree and creates children there; from such a child the
	// owner must be the linked worktree, not the original checkout.
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, mainRoot, "worktree", "add", linked, "-b", "linked")
	childOfLinked := filepath.Join(linked, ".fanout", "worktrees", "child-2")
	runGit(t, linked, "worktree", "add", childOfLinked, "-b", "child-2")

	resolve := func(path string) string {
		t.Helper()
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", path, err)
		}
		return resolved
	}

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{name: "owner repo root", cwd: mainRoot, want: mainRoot},
		{name: "owner repo subdir", cwd: subdir, want: mainRoot},
		{name: "child worktree root", cwd: childOfMain, want: mainRoot},
		{name: "child worktree subdir", cwd: childOfMainSubdir, want: mainRoot},
		{name: "linked worktree as owner", cwd: linked, want: linked},
		{name: "child of linked worktree owner", cwd: childOfLinked, want: linked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(tt.cwd)
			got, err := OwnerProjectRoot()
			if err != nil {
				t.Fatalf("OwnerProjectRoot: %v", err)
			}
			if resolve(got) != resolve(tt.want) {
				t.Errorf("OwnerProjectRoot from %s = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
