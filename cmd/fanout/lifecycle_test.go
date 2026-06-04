package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/state"
)

func TestCmdCloseRemovesWorktreeKillsPaneAndState(t *testing.T) {
	repo := initLifecycleRepo(t)
	worktreePath := filepath.Join(repo, ".fanout", "worktrees", "close-child-101")
	gitCmdTest(t, repo, "worktree", "add", "-b", "fanout/close-child-101", worktreePath, "HEAD")
	tmuxLog := installLifecycleScript(t, "tmux", `#!/bin/sh
printf '%s ' "$@" >> "$TMUX_LOG"
printf '\n' >> "$TMUX_LOG"
`)
	t.Setenv("TMUX_LOG", tmuxLog)
	writeLifecycleState(t, repo, state.Pane{
		Parent:       "84",
		IssueNum:     101,
		Slug:         "close-child-101",
		BranchName:   "fanout/close-child-101",
		PaneID:       "%42",
		WorktreePath: worktreePath,
	})
	t.Setenv(fanoutStatePathEnv, state.Path(repo))

	code := cmdClose(&cliflags.Config{ParentRef: "84", CloseNum: 101}, discardLogger())

	if code != exitcode.OK {
		t.Fatalf("cmdClose code = %d, want %d", code, exitcode.OK)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists or stat failed unexpectedly: %v", err)
	}
	loaded, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Panes) != 0 {
		t.Fatalf("state panes = %+v, want empty", loaded.Panes)
	}
	body, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "kill-pane -t %42") {
		t.Fatalf("tmux log = %q, want kill-pane for %%42", body)
	}
}

func TestCmdCloseRemovesDuplicateStateRows(t *testing.T) {
	repo := initLifecycleRepo(t)
	firstPath := filepath.Join(repo, ".fanout", "worktrees", "close-child-101-a")
	secondPath := filepath.Join(repo, ".fanout", "worktrees", "close-child-101-b")
	gitCmdTest(t, repo, "worktree", "add", "-b", "fanout/close-child-101-a", firstPath, "HEAD")
	gitCmdTest(t, repo, "worktree", "add", "-b", "fanout/close-child-101-b", secondPath, "HEAD")
	tmuxLog := installLifecycleScript(t, "tmux", `#!/bin/sh
printf '%s ' "$@" >> "$TMUX_LOG"
printf '\n' >> "$TMUX_LOG"
`)
	t.Setenv("TMUX_LOG", tmuxLog)
	writeRawLifecycleState(t, repo,
		state.Pane{Parent: "84", IssueNum: 101, BranchName: "fanout/close-child-101-a", PaneID: "%101", WorktreePath: firstPath},
		state.Pane{Parent: "84", IssueNum: 101, BranchName: "fanout/close-child-101-b", PaneID: "%102", WorktreePath: secondPath},
	)
	t.Setenv(fanoutStatePathEnv, state.Path(repo))

	code := cmdClose(&cliflags.Config{ParentRef: "84", CloseNum: 101}, discardLogger())

	if code != exitcode.OK {
		t.Fatalf("cmdClose code = %d, want %d", code, exitcode.OK)
	}
	for _, path := range []string{firstPath, secondPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("worktree %s still exists or stat failed unexpectedly: %v", path, err)
		}
	}
	loaded, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Find("84", 101); ok {
		t.Fatalf("duplicate issue #101 still present in state: %+v", loaded.Panes)
	}
	body, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "kill-pane -t %101") || !strings.Contains(string(body), "kill-pane -t %102") {
		t.Fatalf("tmux log = %q, want both duplicate panes killed", body)
	}
}

func TestCmdMergeFastForwardsRecordedBranch(t *testing.T) {
	repo := initLifecycleRepo(t)
	baseHead := gitTrimTest(t, repo, "rev-parse", "HEAD")
	gitCmdTest(t, repo, "switch", "-c", "fanout/merge-child-101")
	if err := os.WriteFile(filepath.Join(repo, "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, repo, "add", "child.txt")
	gitCmdTest(t, repo, "commit", "-m", "child")
	childHead := gitTrimTest(t, repo, "rev-parse", "HEAD")
	gitCmdTest(t, repo, "switch", "main")
	if got := gitTrimTest(t, repo, "rev-parse", "HEAD"); got != baseHead {
		t.Fatalf("main HEAD before merge = %s, want base %s", got, baseHead)
	}
	writeLifecycleState(t, repo, state.Pane{Parent: "84", IssueNum: 101, BranchName: "fanout/merge-child-101"})
	t.Setenv(fanoutStatePathEnv, state.Path(repo))

	code := cmdMerge(&cliflags.Config{ParentRef: "84", MergeNum: 101}, discardLogger())

	if code != exitcode.OK {
		t.Fatalf("cmdMerge code = %d, want %d", code, exitcode.OK)
	}
	if got := gitTrimTest(t, repo, "rev-parse", "HEAD"); got != childHead {
		t.Fatalf("main HEAD after merge = %s, want child %s", got, childHead)
	}
}

func TestCmdMergeReportsNonFastForwardOnly(t *testing.T) {
	repo := initLifecycleRepo(t)
	baseHead := gitTrimTest(t, repo, "rev-parse", "HEAD")
	gitCmdTest(t, repo, "switch", "-c", "fanout/diverged-child-101")
	if err := os.WriteFile(filepath.Join(repo, "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, repo, "add", "child.txt")
	gitCmdTest(t, repo, "commit", "-m", "child")
	gitCmdTest(t, repo, "switch", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, repo, "add", "main.txt")
	gitCmdTest(t, repo, "commit", "-m", "main")
	mainHead := gitTrimTest(t, repo, "rev-parse", "HEAD")
	if mainHead == baseHead {
		t.Fatal("main did not advance")
	}
	writeLifecycleState(t, repo, state.Pane{Parent: "84", IssueNum: 101, BranchName: "fanout/diverged-child-101"})
	t.Setenv(fanoutStatePathEnv, state.Path(repo))
	var stderr bytes.Buffer
	lg := log.NewWith(io.Discard, &stderr, false)

	code := cmdMerge(&cliflags.Config{ParentRef: "84", MergeNum: 101}, lg)

	if code != exitcode.Env {
		t.Fatalf("cmdMerge code = %d, want %d", code, exitcode.Env)
	}
	if got := gitTrimTest(t, repo, "rev-parse", "HEAD"); got != mainHead {
		t.Fatalf("main HEAD changed to %s, want unchanged %s", got, mainHead)
	}
	if !strings.Contains(stderr.String(), "no conflict resolution was attempted") {
		t.Fatalf("stderr = %q, want non-interactive failure message", stderr.String())
	}
}

func TestCmdCleanupClosesOnlyMergedOrClosedPanes(t *testing.T) {
	repo := initLifecycleRepo(t)
	closedPath := filepath.Join(repo, ".fanout", "worktrees", "closed-child-101")
	openPath := filepath.Join(repo, ".fanout", "worktrees", "open-child-102")
	gitCmdTest(t, repo, "worktree", "add", "-b", "fanout/closed-child-101", closedPath, "HEAD")
	gitCmdTest(t, repo, "worktree", "add", "-b", "fanout/open-child-102", openPath, "HEAD")
	tmuxLog := installLifecycleScript(t, "tmux", `#!/bin/sh
printf '%s ' "$@" >> "$TMUX_LOG"
printf '\n' >> "$TMUX_LOG"
`)
	ghScript := `#!/bin/sh
case "$1 $2" in
  "repo view")
    printf 'butaosuinu/fanout\n'
    ;;
  "api graphql")
    num=""
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "-F" ]; then
        case "$2" in num=*) num="${2#num=}";; esac
        shift 2
      else
        shift
      fi
    done
    if [ "$num" = "101" ]; then
      printf '{"state":"CLOSED","closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}\n'
    else
      printf '{"state":"OPEN","closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}\n'
    fi
    ;;
esac
`
	installLifecycleScript(t, "gh", ghScript)
	t.Setenv("TMUX_LOG", tmuxLog)
	writeLifecycleState(t, repo,
		state.Pane{Parent: "84", IssueNum: 101, BranchName: "fanout/closed-child-101", PaneID: "%101", WorktreePath: closedPath},
		state.Pane{Parent: "84", IssueNum: 102, BranchName: "fanout/open-child-102", PaneID: "%102", WorktreePath: openPath},
	)
	t.Setenv(fanoutStatePathEnv, state.Path(repo))

	code := cmdCleanup(&cliflags.Config{ParentRef: "84", CleanupMode: true}, discardLogger())

	if code != exitcode.OK {
		t.Fatalf("cmdCleanup code = %d, want %d", code, exitcode.OK)
	}
	if _, err := os.Stat(closedPath); !os.IsNotExist(err) {
		t.Fatalf("closed worktree still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(openPath); err != nil {
		t.Fatalf("open worktree should remain: %v", err)
	}
	loaded, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Find("84", 101); ok {
		t.Fatal("closed issue #101 still present in state")
	}
	if _, ok := loaded.Find("84", 102); !ok {
		t.Fatal("open issue #102 was removed from state")
	}
	body, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "kill-pane -t %101") || strings.Contains(string(body), "%102") {
		t.Fatalf("tmux log = %q, want only %%101 killed", body)
	}
}

func initLifecycleRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "main")
	gitCmdTest(t, repo, "config", "user.email", "fanout@example.invalid")
	gitCmdTest(t, repo, "config", "user.name", "fanout test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, repo, "add", "README.md")
	gitCmdTest(t, repo, "commit", "-m", "base")
	return repo
}

func writeLifecycleState(t *testing.T, repo string, panes ...state.Pane) {
	t.Helper()
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	for _, pane := range panes {
		if err := locked.RecordPane(pane); err != nil {
			t.Fatal(err)
		}
	}
}

func writeRawLifecycleState(t *testing.T, repo string, panes ...state.Pane) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(state.Path(repo)), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state.Store{SchemaVersion: state.SchemaVersion, Panes: panes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.Path(repo), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func installLifecycleScript(t *testing.T, name, script string) string {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(t.TempDir(), name+".log")
	return logPath
}

func discardLogger() *log.Logger {
	return log.NewWith(io.Discard, io.Discard, false)
}

func gitTrimTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
