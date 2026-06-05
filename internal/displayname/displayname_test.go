package displayname

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFanoutMetadataPreservesExistingFields(t *testing.T) {
	worktree := t.TempDir()
	fanoutDir := filepath.Join(worktree, ".fanout")
	if err := os.MkdirAll(fanoutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fanoutDir, "worktree-metadata.json")
	if err := os.WriteFile(path, []byte("{\"custom\":\"keep\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := WriteFanoutMetadata(worktree, FanoutMetadata{
		Agent:        "codex",
		DisplayName:  "State Idempotency",
		BranchName:   "fanout/state-idempotency-83",
		Slug:         "state-idempotency-83",
		WorktreePath: worktree,
	})
	if err != nil {
		t.Fatalf("WriteFanoutMetadata returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		`"custom": "keep"`,
		`"agent": "codex"`,
		`"displayName": "State Idempotency"`,
		`"branchName": "fanout/state-idempotency-83"`,
		`"slug": "state-idempotency-83"`,
		`"worktreePath":`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metadata missing %s:\n%s", want, text)
		}
	}
}

func TestWriteFanoutMetadataLeavesInvalidJSONUntouched(t *testing.T) {
	worktree := t.TempDir()
	fanoutDir := filepath.Join(worktree, ".fanout")
	if err := os.MkdirAll(fanoutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fanoutDir, "worktree-metadata.json")
	original := []byte("{bad json\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	err := WriteFanoutMetadata(worktree, FanoutMetadata{
		Agent:        "codex",
		DisplayName:  "Should Not Write",
		BranchName:   "fanout/bad-json",
		Slug:         "bad-json",
		WorktreePath: worktree,
	})
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
