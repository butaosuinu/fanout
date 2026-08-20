package sessionbinding

import (
	"path/filepath"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestStateLoaderBindsOnlyFirstLateSession(t *testing.T) {
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

	// This loader owns the first bind only. A conversation the provider
	// replaces afterwards is rebound by the telemetry path, which holds the
	// same lock and observes the pane on every emit; here the row keeps its
	// recorded value and, crucially, stays live rather than falling out of
	// every identity gate until the pane exits.
	second := first
	second.Value = "session-second"
	live.AgentSession = &second
	store, err = StateLoader(root, listLive)()
	if err != nil {
		t.Fatal(err)
	}
	bound := assertStoredSession(t, store, first)
	if !bound.RuntimeBinding().MatchesLive(live) {
		t.Fatal("replaced session dropped the persisted binding")
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
	got := bindingRoots("/repo", []state.Pane{row})
	if len(got) != 2 || got[0] != "/repo/home" || got[1] != "/repo/sibling" {
		t.Fatalf("binding roots = %v, want both owning stores", got)
	}
}

func testHerdrPane(root string) state.Pane {
	return state.Pane{
		Parent: "528", IssueNum: 529, Backend: backend.Herdr,
		PaneID: "workspace-a:p1", Agent: "codex", AgentID: "agent-a",
		WorkspaceID: "workspace-a", WorkspaceLabel: "owned-label-a",
		TerminalID: "terminal-a",
		RepoKey:    "/repo/.git", SessionID: "session-a",
		SocketPath: "/tmp/herdr-a.sock", WorktreePath: filepath.Join(root, "child"),
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
