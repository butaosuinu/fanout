package displayname

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/atomicfs"
)

type FanoutMetadata struct {
	Agent          string
	DisplayName    string
	BranchName     string
	Slug           string
	WorktreePath   string
	CodexThreadID  string
	CodexSessionID string
}

// WriteFanoutMetadata persists the pane name fields beside the generated
// worktree. The JSON keys mirror the legacy worktree-metadata shape, but the
// file lives under .fanout for the direct runtime.
func WriteFanoutMetadata(worktreePath string, meta FanoutMetadata) error {
	if worktreePath == "" {
		return fmt.Errorf("worktreePath is empty")
	}
	dir := filepath.Join(worktreePath, ".fanout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "worktree-metadata.json")
	m := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("parse existing metadata %s: %w", path, err)
		}
		if m == nil {
			m = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	m["agent"] = meta.Agent
	m["displayName"] = meta.DisplayName
	m["branchName"] = meta.BranchName
	m["slug"] = meta.Slug
	m["worktreePath"] = meta.WorktreePath
	if meta.CodexThreadID != "" {
		m["codexThreadId"] = meta.CodexThreadID
	}
	if meta.CodexSessionID != "" {
		m["codexSessionId"] = meta.CodexSessionID
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(path, append(out, '\n'), 0o644)
}
