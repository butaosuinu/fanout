package displayname

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeWorktreeMetadataPreservesExistingFields(t *testing.T) {
	worktree := t.TempDir()
	dmuxDir := filepath.Join(worktree, ".dmux")
	if err := os.MkdirAll(dmuxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dmuxDir, "worktree-metadata.json")
	if err := os.WriteFile(path, []byte("{\"branch\":\"feature\",\"agent\":\"codex\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mergeWorktreeMetadata(worktree, "Review Pane"); err != nil {
		t.Fatalf("mergeWorktreeMetadata returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{`"branch": "feature"`, `"agent": "codex"`, `"displayName": "Review Pane"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("metadata missing %s:\n%s", want, text)
		}
	}
}

func TestMergeWorktreeMetadataLeavesInvalidJSONUntouched(t *testing.T) {
	worktree := t.TempDir()
	dmuxDir := filepath.Join(worktree, ".dmux")
	if err := os.MkdirAll(dmuxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dmuxDir, "worktree-metadata.json")
	original := []byte("{bad json\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	err := mergeWorktreeMetadata(worktree, "Should Not Write")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse existing metadata") {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("metadata changed after parse failure:\nwant %q\ngot  %q", original, got)
	}
}
