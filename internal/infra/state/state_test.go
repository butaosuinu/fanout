package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
)

func TestLoadMissingReturnsEmptyStore(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), ".fanout", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("schemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if len(got.Panes) != 0 {
		t.Fatalf("panes = %d, want 0", len(got.Panes))
	}
}

func TestLockContextStopsWaitingAtDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".fanout", "state.json")
	locked, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := LockContext(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockContext() error = %v, want context deadline", err)
	}
}

func TestFannedNumbersForParentDedupesByParentAndIssue(t *testing.T) {
	store := Store{Panes: []Pane{
		{Parent: "300", IssueNum: 501},
		{Parent: "0300", IssueNum: 502},
		{Parent: "400", IssueNum: 601},
		{Parent: "300", IssueNum: 501},
		{Parent: "300", IssueNum: 0, TaskID: "task-a"},
		{Parent: "300", IssueNum: -1},
	}}

	got := store.FannedNumbersForParent("300")

	if !got[501] || !got[502] {
		t.Fatalf("fanned = %#v, want #501 and #502", got)
	}
	if got[601] {
		t.Fatalf("fanned = %#v, did not want #601 from another parent", got)
	}
	if len(got) != 2 {
		t.Fatalf("len(fanned) = %d, want 2", len(got))
	}
}

func TestFannedNumbersForOtherParents(t *testing.T) {
	store := Store{Panes: []Pane{
		{Parent: "300", IssueNum: 501},
		{Parent: "0300", IssueNum: 502},
		{Parent: "400", IssueNum: 501},
		{Parent: "500", IssueNum: 601},
		{Parent: "400", IssueNum: 0, TaskID: "task-a"},
	}}

	got := store.FannedNumbersForOtherParents("300")

	if !got[501] || !got[601] {
		t.Fatalf("other-parent fanned = %#v, want #501 and #601", got)
	}
	if got[502] {
		t.Fatalf("other-parent fanned = %#v, did not want #502 from same normalized parent", got)
	}
	if len(got) != 2 {
		t.Fatalf("len(other-parent fanned) = %d, want 2", len(got))
	}
}

func TestLockedStoreRecordPaneWritesAtomicallyShapedJSON(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })

	err = locked.RecordPane(Pane{
		Parent:       "81",
		IssueNum:     83,
		Slug:         "state-idempotency-83",
		BranchName:   "fanout/state-idempotency-83",
		BaseBranch:   "main",
		PaneID:       "%42",
		Agent:        "codex",
		DisplayName:  "State Idempotency",
		WorktreePath: filepath.Join(root, ".fanout", "worktrees", "state-idempotency-83"),
		Prompt:       "[fanout #83 of #81] state-idempotency-83: read /tmp/fanout-fanout-83.md and begin.",
		CreatedAt:    "2026-06-04T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Store
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("state is invalid JSON: %v\n%s", err, data)
	}
	if decoded.SchemaVersion != SchemaVersion || len(decoded.Panes) != 1 {
		t.Fatalf("decoded state = %+v", decoded)
	}
	if got := decoded.Panes[0].PaneID; got != "%42" {
		t.Fatalf("paneId = %q, want %%42", got)
	}
	if got := decoded.Panes[0].BaseBranch; got != "main" {
		t.Fatalf("baseBranch = %q, want main", got)
	}
}

func TestRecordSequencedClaudePaneFencesLegacyEmitter(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })
	legacy := Pane{
		Parent: "81", IssueNum: 83, Backend: backend.Herdr, Agent: "claude",
		LaunchNonce: strings.Repeat("a", 32), EmitterNonce: strings.Repeat("b", 32),
		ReportedState: "blocked", ReportedStateSeq: 0, StateRefinement: true,
		LaunchArgs: []string{"--settings", `{}`},
	}
	locked.Panes = []Pane{legacy}
	current := Pane{
		Parent: "81", IssueNum: 84, Backend: backend.Herdr, Agent: "claude",
		LaunchNonce: strings.Repeat("c", 32), EmitterNonce: strings.Repeat("d", 32),
		ReportedState: "working", ReportedStateSeq: 2, StateRefinement: true,
		LaunchArgs: []string{"--settings", `{"command":"$FANOUT_EMITTER_STATE_PATH.sequence"}`},
	}
	if err := locked.RecordPane(current); err != nil {
		t.Fatal(err)
	}

	gotLegacy, found := locked.Find("81", 83)
	if !found || gotLegacy.ReportedState != "" || gotLegacy.ReportedStateSeq != 0 ||
		gotLegacy.StateRefinement || gotLegacy.EmitterNonce == legacy.EmitterNonce ||
		!telemetry.ValidNonce(gotLegacy.EmitterNonce) {
		t.Fatalf("legacy emitter = (%+v, %t), want fenced", gotLegacy, found)
	}
	gotCurrent, found := locked.Find("81", 84)
	if !found || gotCurrent.ReportedStateSeq != 2 || gotCurrent.EmitterNonce != current.EmitterNonce {
		t.Fatalf("sequenced emitter = (%+v, %t), want unchanged", gotCurrent, found)
	}
}

func TestLoadLegacyRowWithoutBaseBranchDefaultsToEmpty(t *testing.T) {
	root := t.TempDir()
	legacy := `{
  "schemaVersion": 1,
  "panes": [
    {
      "parent": "81",
      "issueNum": 83,
      "slug": "state-idempotency-83",
      "branchName": "fanout/state-idempotency-83",
      "paneId": "%42",
      "agent": "codex",
      "displayName": "State Idempotency",
      "worktreePath": "/repo/.fanout/worktrees/state-idempotency-83",
      "prompt": "p",
      "createdAt": "2026-06-04T00:00:00Z"
    }
  ]
}`
	if err := os.MkdirAll(filepath.Dir(Path(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Panes) != 1 {
		t.Fatalf("pane count = %d, want 1", len(loaded.Panes))
	}
	if got := loaded.Panes[0].BaseBranch; got != "" {
		t.Fatalf("legacy baseBranch = %q, want empty", got)
	}
}

func TestLegacyStateRoundTripOmitsTaskID(t *testing.T) {
	root := t.TempDir()
	legacy := `{
  "schemaVersion": 1,
  "panes": [
    {
      "parent": "81",
      "issueNum": 83,
      "slug": "state-idempotency-83",
      "branchName": "fanout/state-idempotency-83",
      "paneId": "%42",
      "agent": "codex",
      "displayName": "State Idempotency",
      "worktreePath": "/repo/.fanout/worktrees/state-idempotency-83",
      "prompt": "p",
      "createdAt": "2026-06-04T00:00:00Z"
    }
  ]
}`
	if err := os.MkdirAll(filepath.Dir(Path(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = save(Path(root), loaded); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"taskId"`) {
		t.Fatalf("legacy round-trip added taskId:\n%s", data)
	}
	for _, key := range []string{"backend", "herdrWorkspaceId", "herdrWorkspaceLabel", "herdrTerminalId", "herdrRepoKey", "herdrAgentId", "herdrAgentSession", "herdrSession", "herdrSocketPath"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("legacy round-trip added %s:\n%s", key, data)
		}
	}
	if got, want := string(data), legacy+"\n"; got != want {
		t.Fatalf("legacy round-trip changed bytes:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestBackendMetadataRoundTripsAndOmitsWhenEmpty(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })

	if err = locked.RecordPane(Pane{
		Parent:         "423",
		IssueNum:       425,
		Backend:        backend.Herdr,
		PaneID:         "w1:p1",
		WorkspaceID:    "w1",
		WorkspaceLabel: "owned-label",
		TerminalID:     "term-425",
		RepoKey:        "/repo/.git",
		AgentID:        "agent-425",
		AgentSession: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-425",
		},
		SessionID:  "fanout-dev",
		SocketPath: "/tmp/herdr-fanout-dev.sock",
	}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(Pane{Parent: "423", IssueNum: 426, PaneID: "%42"}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	herdrPane, ok := loaded.Find("423", 425)
	if !ok {
		t.Fatal("herdr pane not found after round trip")
	}
	if herdrPane.Backend != backend.Herdr || herdrPane.PaneID != "w1:p1" ||
		herdrPane.WorkspaceID != "w1" || herdrPane.WorkspaceLabel != "owned-label" ||
		herdrPane.TerminalID != "term-425" || herdrPane.RepoKey != "/repo/.git" ||
		herdrPane.AgentID != "agent-425" || herdrPane.AgentSession == nil ||
		*herdrPane.AgentSession != (backend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-425"}) ||
		herdrPane.SessionID != "fanout-dev" || herdrPane.SocketPath != "/tmp/herdr-fanout-dev.sock" {
		t.Fatalf("herdr metadata = %+v", herdrPane)
	}
	legacyPane, ok := loaded.Find("423", 426)
	if !ok || legacyPane.Backend != "" {
		t.Fatalf("legacy backend = %q (found=%v), want empty", legacyPane.Backend, ok)
	}

	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"backend": "herdr"`,
		`"herdrWorkspaceId": "w1"`,
		`"herdrWorkspaceLabel": "owned-label"`,
		`"herdrTerminalId": "term-425"`,
		`"herdrRepoKey": "/repo/.git"`,
		`"herdrAgentId": "agent-425"`,
		`"herdrAgentSession": {`,
		`"source": "herdr:codex"`,
		`"value": "session-425"`,
		`"herdrSession": "fanout-dev"`,
		`"herdrSocketPath": "/tmp/herdr-fanout-dev.sock"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("state missing %s:\n%s", want, data)
		}
	}
	if n := strings.Count(string(data), `"backend"`); n != 1 {
		t.Fatalf("backend key appears %d times, want only the herdr row:\n%s", n, data)
	}
}

func TestAgentStatusRoundTripsAndOmitsWhenEmpty(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })

	if err = locked.RecordPane(Pane{Parent: "81", IssueNum: 83, AgentStatus: "running"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(Pane{Parent: "81", IssueNum: 84}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Find("81", 83)
	if !ok || got.AgentStatus != "running" {
		t.Fatalf("agentStatus = %q (found=%v), want running", got.AgentStatus, ok)
	}
	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	// omitempty: agentStatus 無しの行にはキー自体が現れない
	if n := strings.Count(string(data), `"agentStatus"`); n != 1 {
		t.Fatalf("agentStatus key appears %d times, want 1:\n%s", n, data)
	}
}

func TestSourceProjectRootNeverPersists(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })

	// SourceProjectRoot は MergedStateLoader だけが設定する非永続フィールド。
	// RecordPane 経由で値が入っても state.json には書き出されず、ロードしても空に戻る。
	if err = locked.RecordPane(Pane{Parent: "81", IssueNum: 83, SourceProjectRoot: "/somewhere/else"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sourceProjectRoot") || strings.Contains(string(data), "/somewhere/else") {
		t.Fatalf("SourceProjectRoot leaked into persisted state:\n%s", data)
	}

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Find("81", 83)
	if !ok || got.SourceProjectRoot != "" {
		t.Fatalf("SourceProjectRoot = %q (found=%v), want empty after load", got.SourceProjectRoot, ok)
	}
}

func TestPlanModeRoundTripsAndOmitsWhenFalse(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })

	if err = locked.RecordPane(Pane{Parent: "81", IssueNum: 83, PlanMode: true}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(Pane{Parent: "81", IssueNum: 84}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Find("81", 83)
	if !ok || !got.PlanMode {
		t.Fatalf("codexPlanMode = %v (found=%v), want true", got.PlanMode, ok)
	}
	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	// omitempty: plan モードでない行にはキー自体が現れない(additive な
	// フィールドなので旧 state との差分も plan ペインの行に限られる)
	if n := strings.Count(string(data), `"codexPlanMode"`); n != 1 {
		t.Fatalf("codexPlanMode key appears %d times, want 1:\n%s", n, data)
	}
}

func TestTaskIDRoundTripsAndOmitsWhenEmpty(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })

	if err = locked.RecordPane(Pane{Parent: "plan:alpha", IssueNum: 0, TaskID: "task-a", PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(Pane{Parent: "plan:alpha", IssueNum: 1, PaneID: "%2"}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.FindTask("plan:alpha", "task-a")
	if !ok || got.PaneID != "%1" {
		t.Fatalf("FindTask() = %+v (found=%v), want pane %%1", got, ok)
	}
	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"taskId"`); n != 1 {
		t.Fatalf("taskId key appears %d times, want 1:\n%s", n, data)
	}
}

func TestRecordPaneReplacesSameParentIssue(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })

	if err = locked.RecordPane(Pane{Parent: "81", IssueNum: 83, PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(Pane{Parent: "81", IssueNum: 83, PaneID: "%2"}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Panes) != 1 {
		t.Fatalf("pane count = %d, want 1", len(loaded.Panes))
	}
	if got := loaded.Panes[0].PaneID; got != "%2" {
		t.Fatalf("paneId = %q, want %%2", got)
	}
}

func TestUpsertTaskReplacesSameParentTaskID(t *testing.T) {
	store := Store{}
	store.UpsertTask(Pane{Parent: "211", IssueNum: 0, TaskID: "task-a", PaneID: "%1"})
	store.UpsertTask(Pane{Parent: "0211", IssueNum: 0, TaskID: "task-b", PaneID: "%2"})
	store.UpsertTask(Pane{Parent: "211", IssueNum: 99, TaskID: "task-a", PaneID: "%3"})

	if len(store.Panes) != 2 {
		t.Fatalf("pane count = %d, want 2: %+v", len(store.Panes), store.Panes)
	}
	got, ok := store.FindTask("211", "task-a")
	if !ok || got.PaneID != "%3" || got.IssueNum != 99 {
		t.Fatalf("FindTask(task-a) = %+v (found=%v), want pane %%3 issue #99", got, ok)
	}
	if _, ok := store.FindTask("211", "task-b"); !ok {
		t.Fatalf("task-b was clobbered: %+v", store.Panes)
	}
	fanned := store.FannedTaskIDsForParent("211")
	if !fanned["task-a"] || !fanned["task-b"] || len(fanned) != 2 {
		t.Fatalf("FannedTaskIDsForParent() = %#v, want task-a and task-b", fanned)
	}
}

func TestUpsertTaskFallsBackToIssueNumberWhenEitherSideLacksTaskID(t *testing.T) {
	store := Store{Panes: []Pane{{Parent: "211", IssueNum: 42, PaneID: "%old"}}}
	store.UpsertTask(Pane{Parent: "0211", IssueNum: 42, TaskID: "task-c", PaneID: "%new"})

	if len(store.Panes) != 1 {
		t.Fatalf("pane count after legacy replacement = %d, want 1: %+v", len(store.Panes), store.Panes)
	}
	got, ok := store.FindTask("211", "task-c")
	if !ok || got.PaneID != "%new" {
		t.Fatalf("legacy row was not replaced by task row: %+v (found=%v)", got, ok)
	}

	store.UpsertTask(Pane{Parent: "211", IssueNum: 42, PaneID: "%legacy"})
	got, ok = store.Find("211", 42)
	if len(store.Panes) != 1 || !ok || got.PaneID != "%legacy" || got.TaskID != "" {
		t.Fatalf("issue fallback replacement = %+v (found=%v), panes=%+v", got, ok, store.Panes)
	}
}

func TestRemoveDeletesAllSameParentIssueRows(t *testing.T) {
	store := Store{Panes: []Pane{
		{Parent: "84", IssueNum: 101, PaneID: "%1"},
		{Parent: "084", IssueNum: 101, PaneID: "%2"},
		{Parent: "84", IssueNum: 102, PaneID: "%3"},
		{Parent: "85", IssueNum: 101, PaneID: "%4"},
	}}

	if !store.Remove("84", 101) {
		t.Fatal("Remove returned false, want true")
	}
	if _, ok := store.Find("84", 101); ok {
		t.Fatalf("parent #84 issue #101 still present: %+v", store.Panes)
	}
	if _, ok := store.Find("84", 102); !ok {
		t.Fatalf("same parent different issue was removed: %+v", store.Panes)
	}
	if _, ok := store.Find("85", 101); !ok {
		t.Fatalf("different parent same issue was removed: %+v", store.Panes)
	}
}

func TestLockedStoreRemoveTaskPane(t *testing.T) {
	root := t.TempDir()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })

	if err = locked.RecordPane(Pane{Parent: "211", IssueNum: 0, TaskID: "task-a", PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(Pane{Parent: "211", IssueNum: 0, TaskID: "task-b", PaneID: "%2"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RemoveTaskPane("0211", "task-a"); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.FindTask("211", "task-a"); ok {
		t.Fatalf("task-a still present: %+v", loaded.Panes)
	}
	if got, ok := loaded.FindTask("211", "task-b"); !ok || got.PaneID != "%2" {
		t.Fatalf("task-b = %+v (found=%v), want pane %%2", got, ok)
	}
}

// RuntimeBinding must stay a verbatim projection: normalizing or cleaning any
// component here would silently widen every liveness and rebinding gate.
func TestRuntimeBindingProjectsRecordedIdentityVerbatim(t *testing.T) {
	session := &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-425",
	}
	pane := Pane{
		Parent: "423", IssueNum: 425, TaskID: "task-a", Kind: PaneKindShell,
		Backend: backend.Herdr, PaneID: "w1:p1",
		WorkspaceID: "w1", WorkspaceLabel: "owned-label",
		TerminalID: "term-425", RepoKey: "/repo/.git",
		AgentID: "agent-425", AgentSession: session,
		SessionID: "fanout-dev", SocketPath: "/tmp/herdr-fanout-dev.sock",
		Agent: "codex", WorktreePath: "/repo/.fanout/worktrees/child/",
		EmitterRowKey: "row-a", LaunchNonce: "nonce-a", EmitterNonce: "emitter-a",
		LaunchExecutable: "/usr/bin/codex", LaunchArgs: []string{"--sandbox"},
	}

	want := backend.PaneBinding{
		Row:       backend.PaneRowKey{Parent: "423", IssueNum: 425, TaskID: "task-a"},
		Ref:       backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		SessionID: "fanout-dev", SocketPath: "/tmp/herdr-fanout-dev.sock",
		WorkspaceLabel: "owned-label", TerminalID: "term-425",
		Agent: "codex", AgentID: "agent-425", AgentSession: session, Shell: true,
		RepoKey: "/repo/.git", WorktreePath: "/repo/.fanout/worktrees/child/",
		Launch: backend.LaunchGeneration{
			RowKey: "row-a", Nonce: "nonce-a", EmitterNonce: "emitter-a",
			Executable: "/usr/bin/codex", Args: []string{"--sandbox"},
		},
	}
	if got := pane.RuntimeBinding(); !got.Equal(want) || got.AgentSession != session {
		t.Fatalf("RuntimeBinding() = %+v, want %+v", got, want)
	}
}

func TestRuntimeBindingKeepsLegacyEmptyBackend(t *testing.T) {
	pane := Pane{Parent: "423", IssueNum: 426, PaneID: "%42"}
	got := pane.RuntimeBinding()
	if got.Ref.Backend != "" || got.Ref.Pane != "%42" || got.Shell {
		t.Fatalf("RuntimeBinding() = %+v, want the recorded empty backend", got)
	}
}
