package sessionbinding

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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

func TestStateLoaderRebindsOnlyVerifiedCodexResume(t *testing.T) {
	root := t.TempDir()
	row := testCodexResumePane(root)
	recordTestPane(t, root, row)
	observed := []backend.LivePane{testCodexResumeObservation(row)}
	calls := 0
	rebind := func(
		_ context.Context,
		gotRoot string,
		got state.Pane,
		totalTimeout time.Duration,
	) (backend.LivePane, bool) {
		calls++
		if gotRoot != root || !sameCodexResumeBaseline(got, row) || totalTimeout != 0 {
			t.Fatalf("rebind input = %q, %+v, %s", gotRoot, got, totalTimeout)
		}
		observed[0].NativeAgentState = "working"
		observed[0].AgentState = backend.AgentWorking
		process := backend.ProcessIdentity{ShellPID: 52, ForegroundProcessGroup: 52, AgentPID: 53}
		observed[0].ProcessIdentity = &process
		return observed[0], true
	}
	listLive := func() ([]backend.LivePane, error) { return observed, nil }
	store, err := stateLoader(root, listLive, rebind)()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("rebind calls = %d, want 1", calls)
	}
	rebound, ok := store.Find(row.Parent, row.IssueNum)
	if !ok || rebound.HerdrTerminalID != "terminal-b" || rebound.HerdrAgentID != "agent-b" ||
		rebound.HerdrProcessIdentity == nil || rebound.HerdrProcessIdentity.AgentPID != 53 {
		t.Fatalf("rebound row = %+v", rebound)
	}
	if !sessionview.HerdrPaneMatches(rebound, observed[0]) || observed[0].AgentState != backend.AgentWorking {
		t.Fatalf("rebound display did not become running: row=%+v live=%+v", rebound, observed[0])
	}
}

func TestStateLoaderKeepsUnsafeColdRestartStaleWithoutRetry(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*state.Pane, *[]backend.LivePane)
		rebindOK  bool
		wantCalls int
	}{
		{name: "missing ref", mutate: func(_ *state.Pane, live *[]backend.LivePane) { (*live)[0].AgentSession = nil }},
		{name: "mismatched ref", mutate: func(_ *state.Pane, live *[]backend.LivePane) { (*live)[0].AgentSession.Value = "foreign" }},
		{name: "duplicate ref", mutate: func(_ *state.Pane, live *[]backend.LivePane) { *live = append(*live, (*live)[0]) }},
		{name: "unverified provider", mutate: makeClaudeResumeCandidate},
		{name: "unverifiable process", rebindOK: false, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			row := testCodexResumePane(root)
			live := []backend.LivePane{testCodexResumeObservation(row)}
			if test.mutate != nil {
				test.mutate(&row, &live)
			}
			recordTestPane(t, root, row)
			calls := 0
			rebind := func(context.Context, string, state.Pane, time.Duration) (backend.LivePane, bool) {
				calls++
				return live[0], test.rebindOK
			}
			listLive := func() ([]backend.LivePane, error) { return live, nil }
			store, err := stateLoader(root, listLive, rebind)()
			if err != nil {
				t.Fatal(err)
			}
			if calls != test.wantCalls {
				t.Fatalf("rebind calls = %d, want %d", calls, test.wantCalls)
			}
			stale, ok := store.Find(row.Parent, row.IssueNum)
			if !ok || stale.HerdrTerminalID != "terminal-a" {
				t.Fatalf("unsafe candidate changed persisted terminal: %+v", stale)
			}
			if sessionview.HerdrPaneMatches(stale, live[0]) {
				t.Fatal("unsafe candidate became live instead of remaining stale")
			}
			if live[0].NativeAgentState == "idle" && live[0].AgentState == backend.AgentDone {
				t.Fatal("resume placeholder idle was projected as done")
			}
		})
	}
}

func testHerdrPane(root string) state.Pane {
	return state.Pane{
		Parent: "528", IssueNum: 529, Backend: backend.Herdr,
		PaneID: "workspace-a:p1", Agent: "codex", HerdrAgentID: "agent-a",
		HerdrWorkspaceID: "workspace-a", HerdrTerminalID: "terminal-a",
		HerdrRepoKey: "/repo/.git", HerdrSession: "session-a",
		HerdrSocketPath: "/tmp/herdr-a.sock", WorktreePath: filepath.Join(root, "child"),
	}
}

func testCodexResumePane(root string) state.Pane {
	row := testHerdrPane(root)
	row.HerdrAgentSession = &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-a",
	}
	row.HerdrLaunchExecutable = "/opt/codex"
	row.HerdrLaunchArgs = []string{"--no-alt-screen"}
	row.HerdrProcessIdentity = &backend.ProcessIdentity{
		ShellPID: 42, ForegroundProcessGroup: 42, AgentPID: 43,
	}
	return row
}

func testCodexResumeObservation(row state.Pane) backend.LivePane {
	live := testLiveHerdrPane(row, *row.HerdrAgentSession)
	live.TerminalID = "terminal-b"
	live.AgentID = "agent-b"
	live.NativeAgentState = "idle"
	live.AgentState = backend.AgentIdle
	return live
}

func makeClaudeResumeCandidate(row *state.Pane, live *[]backend.LivePane) {
	row.Agent = "claude"
	row.HerdrAgentSession = &backend.AgentSessionRef{
		Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-a",
	}
	(*live)[0].AgentProvider = "claude"
	(*live)[0].AgentSession = row.HerdrAgentSession
}

func testLiveHerdrPane(row state.Pane, session backend.AgentSessionRef) backend.LivePane {
	return backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: row.HerdrWorkspaceID, Pane: row.PaneID,
		},
		TerminalID: row.HerdrTerminalID, AgentID: row.HerdrAgentID,
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
