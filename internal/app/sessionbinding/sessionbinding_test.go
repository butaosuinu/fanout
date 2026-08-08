package sessionbinding

import (
	"path/filepath"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
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

	second := first
	second.Value = "session-second"
	live.AgentSession = &second
	store, err = StateLoader(root, listLive)()
	if err != nil {
		t.Fatal(err)
	}
	bound := assertStoredSession(t, store, first)
	if sessionview.HerdrPaneMatches(bound, live) {
		t.Fatal("later session matched the persisted first binding")
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
			if !ok || unbound.HerdrAgentSession != nil {
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
		PaneID: "workspace-a:p1", Agent: "codex", HerdrAgentID: "agent-a",
		HerdrWorkspaceID: "workspace-a", HerdrWorkspaceLabel: "owned-label-a",
		HerdrTerminalID: "terminal-a",
		HerdrRepoKey:    "/repo/.git", HerdrSession: "session-a",
		HerdrSocketPath: "/tmp/herdr-a.sock", WorktreePath: filepath.Join(root, "child"),
	}
}

func testLiveHerdrPane(row state.Pane, session backend.AgentSessionRef) backend.LivePane {
	return backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: row.HerdrWorkspaceID, Pane: row.PaneID,
		},
		WorkspaceLabel: row.HerdrWorkspaceLabel,
		TerminalID:     row.HerdrTerminalID, AgentID: row.HerdrAgentID,
		AgentProvider: row.Agent, AgentSession: &session, AgentPresent: true,
		RepoKey: row.HerdrRepoKey, ProjectRoot: filepath.Dir(row.WorktreePath),
		WorktreePath: row.WorktreePath, SessionID: row.HerdrSession,
		SocketPath: row.HerdrSocketPath,
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
	if !ok || pane.HerdrAgentSession == nil || *pane.HerdrAgentSession != want {
		t.Fatalf("stored session = %+v, want %+v", pane.HerdrAgentSession, want)
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
