package sessionview

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/state"
)

func gitInTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitTopIn(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("rev-parse --show-toplevel in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

func newCommittedRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitInTest(t, "", "init", "-b", "main", repo)
	gitInTest(t, repo, "config", "user.name", "Fanout Test")
	gitInTest(t, repo, "config", "user.email", "fanout@example.test")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, repo, "add", "file.txt")
	gitInTest(t, repo, "commit", "-m", "base")
	return repo
}

func recordPaneAt(t *testing.T, root string, p state.Pane) {
	t.Helper()
	locked, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	if err := locked.RecordPane(p); err != nil {
		t.Fatal(err)
	}
}

func writeCorruptState(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".fanout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMergedStateLoaderUnionsWorktreesAndTagsSource(t *testing.T) {
	repo := newCommittedRepo(t)
	top := gitTopIn(t, repo)
	sibling := filepath.Join(t.TempDir(), "sib")
	gitInTest(t, repo, "worktree", "add", "-b", "feat-sib", sibling)
	sibTop := gitTopIn(t, sibling)

	recordPaneAt(t, top, state.Pane{Parent: "100", IssueNum: 101, PaneID: "%1", Agent: "claude"})
	recordPaneAt(t, sibTop, state.Pane{Parent: "200", IssueNum: 202, PaneID: "%2", Agent: "claude"})

	store, err := MergedStateLoader(top)()
	if err != nil {
		t.Fatalf("MergedStateLoader: %v", err)
	}
	if len(store.Panes) != 2 {
		t.Fatalf("want 2 merged panes, got %d: %+v", len(store.Panes), store.Panes)
	}
	source := map[string]string{}
	for _, p := range store.Panes {
		source[p.PaneID] = p.SourceProjectRoot
	}
	if source["%1"] != top {
		t.Errorf("pane %%1 SourceProjectRoot = %q, want %q", source["%1"], top)
	}
	if source["%2"] != sibTop {
		t.Errorf("pane %%2 SourceProjectRoot = %q, want %q", source["%2"], sibTop)
	}
}

func TestMergedStateLoaderDedupesBySameIdentityHomeWins(t *testing.T) {
	repo := newCommittedRepo(t)
	top := gitTopIn(t, repo)
	sibling := filepath.Join(t.TempDir(), "sib")
	gitInTest(t, repo, "worktree", "add", "-b", "feat-sib", sibling)
	sibTop := gitTopIn(t, sibling)

	// Same logical child (#84/#101) recorded in two worktrees with different pane
	// ids: identity dedup collapses to one row, home wins.
	recordPaneAt(t, top, state.Pane{Parent: "84", IssueNum: 101, PaneID: "%1", Agent: "claude"})
	recordPaneAt(t, sibTop, state.Pane{Parent: "84", IssueNum: 101, PaneID: "%2", Agent: "codex"})

	store, err := MergedStateLoader(top)()
	if err != nil {
		t.Fatalf("MergedStateLoader: %v", err)
	}
	count := 0
	var winner state.Pane
	for _, p := range store.Panes {
		if p.Parent == "84" && p.IssueNum == 101 {
			count++
			winner = p
		}
	}
	if count != 1 {
		t.Fatalf("(#84,#101) appears %d times, want 1 (identity dedup): %+v", count, store.Panes)
	}
	if winner.SourceProjectRoot != top || winner.PaneID != "%1" {
		t.Fatalf("winner = %+v, want the home-worktree row (%%1 at %s)", winner, top)
	}
	// Both owning roots are retained so lifecycle can reach the collapsed sibling.
	gotRoots := map[string]bool{}
	for _, r := range winner.SourceProjectRoots {
		gotRoots[r] = true
	}
	if len(winner.SourceProjectRoots) != 2 || !gotRoots[top] || !gotRoots[sibTop] {
		t.Fatalf("winner.SourceProjectRoots = %v, want both %s and %s", winner.SourceProjectRoots, top, sibTop)
	}
}

func TestMergedStateLoaderKeepsReusedPaneIDForDifferentIdentity(t *testing.T) {
	repo := newCommittedRepo(t)
	top := gitTopIn(t, repo)
	sibling := filepath.Join(t.TempDir(), "sib")
	gitInTest(t, repo, "worktree", "add", "-b", "feat-sib", sibling)
	sibTop := gitTopIn(t, sibling)

	// Different children that happen to share a reused tmux pane id (%9) across a
	// server restart. Pane-id dedup would hide one; identity dedup keeps both so
	// Build can apply its worktree-path liveness check.
	recordPaneAt(t, top, state.Pane{Parent: "100", IssueNum: 101, PaneID: "%9", Agent: "claude"})
	recordPaneAt(t, sibTop, state.Pane{Parent: "200", IssueNum: 202, PaneID: "%9", Agent: "codex"})

	store, err := MergedStateLoader(top)()
	if err != nil {
		t.Fatalf("MergedStateLoader: %v", err)
	}
	if len(store.Panes) != 2 {
		t.Fatalf("distinct children sharing a reused pane id must both survive, got %+v", store.Panes)
	}
}

func TestMergedStateLoaderKeepsManualPanesDistinctAcrossWorktrees(t *testing.T) {
	repo := newCommittedRepo(t)
	top := gitTopIn(t, repo)
	sibling := filepath.Join(t.TempDir(), "sib")
	gitInTest(t, repo, "worktree", "add", "-b", "feat-sib", sibling)
	sibTop := gitTopIn(t, sibling)

	// Manual TUI panes carry a per-store synthetic issue number (negative), so
	// @manual/-1 in two worktrees are unrelated panes and must both survive — not
	// collapse into one with a shared SourceProjectRoots (which would let a close
	// remove the sibling's pane too).
	recordPaneAt(t, top, state.Pane{Parent: "@manual", IssueNum: -1, PaneID: "%1", Agent: "claude"})
	recordPaneAt(t, sibTop, state.Pane{Parent: "@manual", IssueNum: -1, PaneID: "%2", Agent: "codex"})

	store, err := MergedStateLoader(top)()
	if err != nil {
		t.Fatalf("MergedStateLoader: %v", err)
	}
	manual := 0
	for _, p := range store.Panes {
		if p.Parent == "@manual" {
			manual++
			if len(p.SourceProjectRoots) != 1 {
				t.Fatalf("manual pane %s has SourceProjectRoots %v, want exactly its own root", p.PaneID, p.SourceProjectRoots)
			}
		}
	}
	if manual != 2 {
		t.Fatalf("distinct manual panes across worktrees collapsed: got %d, want 2 (%+v)", manual, store.Panes)
	}
}

func TestMergedStateLoaderKeepsPlanTasksDistinctAcrossWorktrees(t *testing.T) {
	repo := newCommittedRepo(t)
	top := gitTopIn(t, repo)
	sibling := filepath.Join(t.TempDir(), "sib")
	gitInTest(t, repo, "worktree", "add", "-b", "feat-sib", sibling)
	sibTop := gitTopIn(t, sibling)

	// plan:<slug>/<taskId> is scoped to a spec, not globally stable: two worktrees
	// can carry the same slug+taskId for unrelated work, so they must stay
	// distinct (not collapse into one row whose close removes both stores' rows).
	recordPaneAt(t, top, state.Pane{Parent: "plan:launch", IssueNum: 0, TaskID: "api", PaneID: "%1", Agent: "claude"})
	recordPaneAt(t, sibTop, state.Pane{Parent: "plan:launch", IssueNum: 0, TaskID: "api", PaneID: "%2", Agent: "codex"})

	store, err := MergedStateLoader(top)()
	if err != nil {
		t.Fatalf("MergedStateLoader: %v", err)
	}
	tasks := 0
	for _, p := range store.Panes {
		if p.TaskID == "api" {
			tasks++
			if len(p.SourceProjectRoots) != 1 {
				t.Fatalf("plan task %s has SourceProjectRoots %v, want exactly its own root", p.PaneID, p.SourceProjectRoots)
			}
		}
	}
	if tasks != 2 {
		t.Fatalf("distinct plan tasks across worktrees collapsed: got %d, want 2 (%+v)", tasks, store.Panes)
	}
}

func TestMergedStateLoaderKeepsDistinctIdentities(t *testing.T) {
	root := t.TempDir() // not a git repo: single-root fallback
	recordPaneAt(t, root, state.Pane{Parent: "1", IssueNum: 2, PaneID: ""})
	recordPaneAt(t, root, state.Pane{Parent: "1", IssueNum: 3, PaneID: ""})

	store, err := MergedStateLoader(root)()
	if err != nil {
		t.Fatalf("MergedStateLoader: %v", err)
	}
	if len(store.Panes) != 2 {
		t.Fatalf("distinct (parent,issueNum) rows must not be de-duplicated, got %+v", store.Panes)
	}
}

func TestMergedStateLoaderFallsBackToSingleRootOutsideRepo(t *testing.T) {
	root := t.TempDir() // not a git work tree
	recordPaneAt(t, root, state.Pane{Parent: "1", IssueNum: 2, PaneID: "%1"})

	store, err := MergedStateLoader(root)()
	if err != nil {
		t.Fatalf("fallback must not error: %v", err)
	}
	if len(store.Panes) != 1 || store.Panes[0].PaneID != "%1" {
		t.Fatalf("fallback store = %+v, want the single home pane", store.Panes)
	}
	if store.Panes[0].SourceProjectRoot != root {
		t.Fatalf("SourceProjectRoot = %q, want %q even in fallback", store.Panes[0].SourceProjectRoot, root)
	}
}

func TestMergedStateLoaderPropagatesHomeError(t *testing.T) {
	root := t.TempDir()
	writeCorruptState(t, root)

	if _, err := MergedStateLoader(root)(); err == nil {
		t.Fatal("a corrupt home state.json must surface as an error")
	}
}

func TestMergedStateLoaderSkipsCorruptSibling(t *testing.T) {
	repo := newCommittedRepo(t)
	top := gitTopIn(t, repo)
	sibling := filepath.Join(t.TempDir(), "sib")
	gitInTest(t, repo, "worktree", "add", "-b", "feat-sib", sibling)
	sibTop := gitTopIn(t, sibling)

	recordPaneAt(t, top, state.Pane{Parent: "100", IssueNum: 101, PaneID: "%1"})
	writeCorruptState(t, sibTop)

	store, err := MergedStateLoader(top)()
	if err != nil {
		t.Fatalf("a corrupt sibling store must not fail the merge: %v", err)
	}
	if len(store.Panes) != 1 || store.Panes[0].PaneID != "%1" {
		t.Fatalf("store = %+v, want only the home pane", store.Panes)
	}
}

func TestMergedStateLoaderDoesNotDoubleReadHomeViaSymlink(t *testing.T) {
	repo := newCommittedRepo(t)
	top := gitTopIn(t, repo)
	// A dead/legacy row with an empty pane id is kept (not de-duplicated by id),
	// so a double-read of the home worktree would surface it twice.
	recordPaneAt(t, top, state.Pane{Parent: "100", IssueNum: 101, PaneID: ""})

	// Resolve the home through a symlink: git worktree list reports the canonical
	// path, while projectRoot is the symlinked one. Both point at one state.json.
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(top, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	store, err := MergedStateLoader(link)()
	if err != nil {
		t.Fatalf("MergedStateLoader: %v", err)
	}
	count := 0
	for _, p := range store.Panes {
		if p.IssueNum == 101 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("empty-pane-id home row appears %d times, want 1 (no double-read): %+v", count, store.Panes)
	}
}

func TestMergedStateLoaderExcludesFanoutChildWorktrees(t *testing.T) {
	repo := newCommittedRepo(t)
	top := gitTopIn(t, repo)
	child := filepath.Join(repo, ".fanout", "worktrees", "child-1")
	gitInTest(t, repo, "worktree", "add", "-b", "fanout/child-1", child)
	childTop := gitTopIn(t, child)

	recordPaneAt(t, top, state.Pane{Parent: "100", IssueNum: 101, PaneID: "%1"})
	// A stray state.json inside a fanout child worktree must be ignored: child
	// state is recorded in the owner, not the child.
	recordPaneAt(t, childTop, state.Pane{Parent: "999", IssueNum: 999, PaneID: "%99"})

	store, err := MergedStateLoader(top)()
	if err != nil {
		t.Fatalf("MergedStateLoader: %v", err)
	}
	for _, p := range store.Panes {
		if p.PaneID == "%99" {
			t.Fatalf("fanout child worktree state must be excluded, got %+v", store.Panes)
		}
	}
}
