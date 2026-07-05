// Package atomicfs writes a file atomically: tempfile in the destination
// directory, then rename. Multiple packages need this exact dance for state
// and worktree-metadata updates, so it lives here once.
package atomicfs

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteFile creates a sibling tempfile in filepath.Dir(path), writes data,
// closes, then renames over path. perm is applied to the final file via the
// rename target (the tempfile inherits CreateTemp's 0600 until the rename
// resolves the path). On any error the tempfile is removed.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fanout-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Cleanup errors below are discarded deliberately: the write/close/chmod/
	// rename error is the actionable one, and a stray tempfile is harmless.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// WriteJSON marshals v with two-space MarshalIndent, appends a trailing
// newline, creates filepath.Dir(path) if needed, and writes the result
// atomically via WriteFile. All errors are returned unwrapped so callers keep
// their own message formats.
func WriteJSON(path string, v any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, append(out, '\n'), perm)
}

// ReadJSON reads path and unmarshals it into v. A missing file returns
// (false, nil) with v untouched. found reports whether the file was read:
// (false, err) is a read error, (true, err) an unmarshal error, so callers
// can wrap the two stages with distinct messages. Errors are returned
// unwrapped.
func ReadJSON(path string, v any) (found bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return true, err
	}
	return true, nil
}
