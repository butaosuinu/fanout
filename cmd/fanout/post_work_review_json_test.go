package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
)

func TestPostWorkReviewJSONRequest(t *testing.T) {
	t.Parallel()
	if !isPostWorkReviewJSONRequest([]string{postWorkReviewJSONCommand, "in", "out"}) {
		t.Fatal("hidden post-work-review JSON command was not recognized")
	}
	if isPostWorkReviewJSONRequest([]string{"post-work-review-json"}) {
		t.Fatal("public-looking command unexpectedly matched hidden helper")
	}
}

func TestCmdPostWorkReviewJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "review.json")
	cache := filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(`{"backend":"bounded-isolated-reviewer","findings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	if code := cmdPostWorkReviewJSON([]string{input, cache}, &stdout, &stderr); code != exitcode.OK {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d; stderr=%s", code, exitcode.OK, stderr.String())
	}
	if got := stdout.String(); got != postWorkReviewJSONVersionLine+"\n" {
		t.Fatalf("stdout = %q, want helper version line", got)
	}
	if _, err := os.Stat(filepath.Join(cache, "valid")); err != nil {
		t.Fatalf("Stat(valid) error = %v", err)
	}
}

func TestCmdPostWorkReviewJSONFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "review.json")
	cache := filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(`{} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	if code := cmdPostWorkReviewJSON([]string{input, cache}, &stdout, &stderr); code != exitcode.Env {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d", code, exitcode.Env)
	}
	if got := stdout.String(); got != postWorkReviewJSONVersionLine+"\n" {
		t.Fatalf("stdout = %q, want helper version line", got)
	}
	if stderr.Len() == 0 {
		t.Fatal("cmdPostWorkReviewJSON() did not explain projection failure")
	}
	if _, err := os.Stat(filepath.Join(cache, "valid")); !os.IsNotExist(err) {
		t.Fatalf("valid marker exists after failure: %v", err)
	}
}
