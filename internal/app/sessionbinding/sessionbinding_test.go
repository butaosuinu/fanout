package sessionbinding

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestStateLoaderBindsFirstLateSession(t *testing.T) {
	root := t.TempDir()
	row := testHerdrPane(root)
	recordTestPane(t, root, row)

	first := backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-first",
	}
	live := testLiveHerdrPane(row, first)
	listLive := func() ([]backend.LivePane, error) { return []backend.LivePane{live}, nil }
	store, err := StateLoader(root, listLive)()
	if err != nil {
		t.Fatal(err)
	}
	assertStoredSession(t, store, first)
	persisted, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	assertStoredSession(t, persisted, first)

	// A row bound to its pane's conversation keeps matching that pane, and
	// another provider's conversation is still not that pane.
	bound := assertStoredSession(t, store, first)
	if !bound.RuntimeBinding().MatchesLive(live) {
		t.Fatal("bound row stopped matching its own pane")
	}
	foreign := first
	foreign.Source, foreign.Agent, foreign.Value = "herdr:claude", "claude", "session-foreign"
	live.AgentSession = &foreign
	if bound.RuntimeBinding().MatchesLive(live) {
		t.Fatal("another provider's session matched the persisted binding")
	}
}

func TestStateLoaderRejectsInvalidOrAmbiguousBinding(t *testing.T) {
	for _, test := range []struct {
		name string
		live func(state.Pane) []backend.LivePane
	}{
		{name: "unexpected source", live: unexpectedSourcePane},
		{name: "ambiguous observations", live: ambiguousSessionPanes},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			row := testHerdrPane(root)
			recordTestPane(t, root, row)
			panes := test.live(row)
			listLive := func() ([]backend.LivePane, error) { return panes, nil }
			store, err := StateLoader(root, listLive)()
			if err != nil {
				t.Fatal(err)
			}
			unbound, ok := store.Find("528", 529)
			if !ok || unbound.AgentSession != nil {
				t.Fatalf("unsafe late session was persisted: %+v", unbound)
			}
		})
	}
}

func TestBindingRootsIncludesEveryOwningStore(t *testing.T) {
	home, sibling := t.TempDir(), t.TempDir()
	row := testHerdrPane(home)
	row.SourceProjectRoot = home
	row.SourceProjectRoots = []string{home, sibling}
	live := testLiveHerdrPane(row, backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-first",
	})
	for _, root := range row.SourceProjectRoots {
		recordTestPane(t, root, row)
	}
	got, err := bindingRoots(home, []state.Pane{row}, []backend.LivePane{live})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != home || got[1] != sibling {
		t.Fatalf("binding roots = %v, want both owning stores", got)
	}
}

func TestStateLoaderReconcilesMovedSiblingBehindStaleHome(t *testing.T) {
	repo := newSessionBindingRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	runSessionBindingGit(t, repo, "worktree", "add", "-b", "sibling", sibling)
	session := backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-first",
	}
	home := testHerdrPane(repo)
	home.WorkspaceLabel, home.WorktreePath, home.EmitterRowKey = "home-label", filepath.Join(repo, "home-child"), "home-row"
	home.AgentSession = &session
	remote := testHerdrPane(repo)
	remote.WorkspaceID, remote.PaneID, remote.TerminalID = "workspace-sibling-old", "workspace-sibling-old:p1", "terminal-sibling-old"
	remote.WorkspaceLabel, remote.WorktreePath, remote.EmitterRowKey = "sibling-label", filepath.Join(sibling, "child"), "sibling-row"
	remote.AgentSession = &session
	recordTestPane(t, repo, home)
	recordTestPane(t, sibling, remote)
	live := testLiveHerdrPane(remote, session)
	live.Ref.Workspace, live.Ref.Pane, live.TerminalID = "workspace-sibling-next", "workspace-sibling-next:p1", "terminal-sibling-next"
	live.ProjectRoot = remote.RepoRoot

	store, err := StateLoader(repo, func() ([]backend.LivePane, error) {
		return []backend.LivePane{live}, nil
	})()
	if err != nil {
		t.Fatal(err)
	}
	winner, found := store.Find(remote.Parent, remote.IssueNum)
	if !found || winner.WorkspaceLabel != remote.WorkspaceLabel {
		t.Fatalf("merged winner = %#v (found=%t), want sibling row", winner, found)
	}
	assertManagedLocation(t, winner, live)
	savedHome, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, _ := savedHome.Find(home.Parent, home.IssueNum)
	if !reflect.DeepEqual(unchanged, home) {
		t.Fatalf("stale home row changed: got %#v want %#v", unchanged, home)
	}
	savedSibling, err := state.LoadProject(sibling)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, _ := savedSibling.Find(remote.Parent, remote.IssueNum)
	assertManagedLocation(t, reconciled, live)
}

// A direct Codex pane never emits telemetry, so the poll path is the only
// place its recorded conversation can follow a /new.
func TestStateLoaderRebindsReplacedSession(t *testing.T) {
	root := t.TempDir()
	row := testHerdrPane(root)
	recordTestPane(t, root, row)

	first := backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-first",
	}
	live := testLiveHerdrPane(row, first)
	listLive := func() ([]backend.LivePane, error) { return []backend.LivePane{live}, nil }
	if _, err := StateLoader(root, listLive)(); err != nil {
		t.Fatal(err)
	}

	second := first
	second.Value = "session-second"
	live.AgentSession = &second
	store, err := StateLoader(root, listLive)()
	if err != nil {
		t.Fatal(err)
	}
	assertStoredSession(t, store, second)
	persisted, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	assertStoredSession(t, persisted, second)

	// Another provider's conversation is not a replacement, so the recorded
	// value stays put rather than following it.
	foreign := second
	foreign.Source, foreign.Agent, foreign.Value = "herdr:claude", "claude", "session-foreign"
	live.AgentSession = &foreign
	store, err = StateLoader(root, listLive)()
	if err != nil {
		t.Fatal(err)
	}
	assertStoredSession(t, store, second)
}

func TestReloadPaneReconcilesMovedManagedLocation(t *testing.T) {
	root := t.TempDir()
	row := testHerdrPane(root)
	session := backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-first",
	}
	row.AgentSession = &session
	row.EmitterNonce = strings.Repeat("e", 32)
	row.ReportedState, row.ReportedStateSeq, row.StateRefinement = "idle", 7, true
	recordTestPane(t, root, row)
	live := testLiveHerdrPane(row, session)
	live.Ref.Workspace, live.Ref.Pane = "workspace-next", "workspace-next:p1"
	live.TerminalID = "terminal-next"
	listLive := func() ([]backend.LivePane, error) { return []backend.LivePane{live}, nil }

	got, err := ReloadPane(root, row, listLive)
	if err != nil {
		t.Fatal(err)
	}
	assertManagedLocation(t, got, live)
	persisted, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	saved, found := persisted.Find(row.Parent, row.IssueNum)
	if !found {
		t.Fatal("reconciled row was not persisted")
	}
	assertManagedLocation(t, saved, live)
	want := row
	want.WorkspaceID, want.PaneID, want.TerminalID = live.Ref.Workspace, live.Ref.Pane, live.TerminalID
	want.ReportedState, want.ReportedStateSeq, want.StateRefinement = "", 0, false
	if saved.EmitterNonce == row.EmitterNonce || !telemetry.ValidNonce(saved.EmitterNonce) {
		t.Fatalf("persisted emitter nonce = %q, want a fresh valid nonce", saved.EmitterNonce)
	}
	want.EmitterNonce = saved.EmitterNonce
	if !reflect.DeepEqual(saved, want) {
		t.Fatalf("persisted row changed outside location and telemetry fence: got %#v want %#v", saved, want)
	}
}

func testHerdrPane(root string) state.Pane {
	return state.Pane{
		Parent: "528", IssueNum: 529, Backend: backend.Herdr,
		PaneID: "workspace-a:p1", Agent: "codex", AgentID: "agent-a",
		WorkspaceID: "workspace-a", WorkspaceLabel: "owned-label-a",
		TerminalID: "terminal-a",
		RepoKey:    "/repo/.git", RepoRoot: root, SessionID: "session-a",
		SocketPath: "/tmp/herdr-a.sock", WorktreePath: filepath.Join(root, "child"),
		EmitterRowKey: "row-a",
	}
}

func assertManagedLocation(t *testing.T, pane state.Pane, live backend.LivePane) {
	t.Helper()
	if pane.WorkspaceID != live.Ref.Workspace || pane.PaneID != live.Ref.Pane || pane.TerminalID != live.TerminalID {
		t.Fatalf("managed location = (%q, %q, %q), want (%q, %q, %q)",
			pane.WorkspaceID, pane.PaneID, pane.TerminalID,
			live.Ref.Workspace, live.Ref.Pane, live.TerminalID,
		)
	}
}

func newSessionBindingRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runSessionBindingGit(t, "", "init", "-b", "main", repo)
	runSessionBindingGit(t, repo, "config", "user.name", "Fanout Test")
	runSessionBindingGit(t, repo, "config", "user.email", "fanout@example.test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionBindingGit(t, repo, "add", "tracked.txt")
	runSessionBindingGit(t, repo, "commit", "-m", "base")
	return repo
}

func runSessionBindingGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func testLiveHerdrPane(row state.Pane, session backend.AgentSessionRef) backend.LivePane {
	return backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: row.WorkspaceID, Pane: row.PaneID,
		},
		WorkspaceLabel: row.WorkspaceLabel,
		TerminalID:     row.TerminalID, AgentID: row.AgentID,
		AgentProvider: row.Agent, AgentSession: &session, AgentPresent: true,
		RepoKey: row.RepoKey, ProjectRoot: filepath.Dir(row.WorktreePath),
		WorktreePath: row.WorktreePath, SessionID: row.SessionID,
		SocketPath: row.SocketPath,
	}
}

func recordTestPane(t *testing.T, root string, pane state.Pane) {
	t.Helper()
	locked, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.RecordPane(pane); err != nil {
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func assertStoredSession(t *testing.T, store state.Store, want backend.AgentSessionRef) state.Pane {
	t.Helper()
	pane, ok := store.Find("528", 529)
	if !ok || pane.AgentSession == nil || *pane.AgentSession != want {
		t.Fatalf("stored session = %+v, want %+v", pane.AgentSession, want)
	}
	return pane
}

func unexpectedSourcePane(row state.Pane) []backend.LivePane {
	ref := backend.AgentSessionRef{
		Source: "foreign:codex", Agent: "codex", Kind: "id", Value: "session-a",
	}
	return []backend.LivePane{testLiveHerdrPane(row, ref)}
}

func ambiguousSessionPanes(row state.Pane) []backend.LivePane {
	first := backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-a",
	}
	second := first
	second.Value = "session-b"
	return []backend.LivePane{testLiveHerdrPane(row, first), testLiveHerdrPane(row, second)}
}
