package sessionbinding

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
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
	row := testHerdrPane("/repo")
	row.SourceProjectRoot = "/repo/home"
	row.SourceProjectRoots = []string{"/repo/home", "/repo/sibling"}
	live := testLiveHerdrPane(row, backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-first",
	})
	got := bindingRoots("/repo", []state.Pane{row}, []backend.LivePane{live})
	if len(got) != 2 || got[0] != "/repo/home" || got[1] != "/repo/sibling" {
		t.Fatalf("binding roots = %v, want both owning stores", got)
	}
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
	if !reflect.DeepEqual(saved, want) {
		t.Fatalf("persisted row changed outside location fields: got %#v want %#v", saved, want)
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
