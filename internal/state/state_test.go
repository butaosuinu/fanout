package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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

func TestFannedNumbersForParentDedupesByParentAndIssue(t *testing.T) {
	store := Store{Panes: []Pane{
		{Parent: "300", IssueNum: 501},
		{Parent: "0300", IssueNum: 502},
		{Parent: "400", IssueNum: 601},
		{Parent: "300", IssueNum: 501},
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
