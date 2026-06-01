package dmuxconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetDisplayNameBySlugMatchesEvenWhenPromptIsRewritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dmux.config.json")
	// Pane has a slug "alpha-slug" but its prompt no longer carries the
	// `[fanout #N]` tag — e.g. dmux or a user edited it. Slug-based targeting
	// must still find and update it.
	in := `{
  "panes": [
    {"slug": "alpha-slug", "prompt": "renamed by hand", "agent": "claude"},
    {"slug": "beta-slug", "prompt": "[fanout #2] beta", "agent": "claude"}
  ]
}`
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetDisplayNameBySlug(path, "alpha-slug", "Alpha Pane"); err != nil {
		t.Fatalf("SetDisplayNameBySlug failed: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Panes []map[string]any `json:"panes"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("config no longer parses after write: %v\n%s", err, out)
	}
	if len(parsed.Panes) != 2 {
		t.Fatalf("pane count changed: got %d, want 2", len(parsed.Panes))
	}
	if got := parsed.Panes[0]["displayName"]; got != "Alpha Pane" {
		t.Errorf("alpha pane displayName: got %v, want Alpha Pane", got)
	}
	if got := parsed.Panes[0]["agent"]; got != "claude" {
		t.Errorf("alpha pane lost agent field: got %v", got)
	}
	if _, has := parsed.Panes[1]["displayName"]; has {
		t.Errorf("beta pane should not have been touched: %v", parsed.Panes[1])
	}
}

func TestSetDisplayNameBySlugReturnsErrorWhenSlugMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dmux.config.json")
	in := `{"panes":[{"slug":"only-slug","prompt":"x"}]}`
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}

	err := SetDisplayNameBySlug(path, "missing-slug", "x")
	if err == nil {
		t.Fatal("expected error for missing slug, got nil")
	}
	if !strings.Contains(err.Error(), "missing-slug") {
		t.Errorf("error should mention slug name: %v", err)
	}
}
