// Package atomicfs writes a file atomically: tempfile in the destination
// directory, then rename. Multiple packages need this exact dance for state
// and worktree-metadata updates, so it lives here once.
package atomicfs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// WriteFile creates a sibling tempfile in filepath.Dir(path), writes data,
// closes, then renames over path. perm is applied to the final file via the
// rename target (the tempfile inherits CreateTemp's 0600 until the rename
// resolves the path). On any error the tempfile is removed.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm, false)
}

// WriteFileDurable performs the same atomic replacement as WriteFile and
// returns only after syncing both the replacement file and destination
// directory.
func WriteFileDurable(path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm, true)
}

func writeFile(path string, data []byte, perm os.FileMode, durable bool) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fanout-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Cleanup errors below are discarded deliberately: the write/chmod/sync/
	// close/rename error is the actionable one, and a stray tempfile is harmless.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if durable {
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if durable {
		dirHandle, err := os.Open(dir)
		if err != nil {
			return err
		}
		syncErr := dirHandle.Sync()
		closeErr := dirHandle.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON marshals v with two-space MarshalIndent, appends a trailing
// newline, creates filepath.Dir(path) if needed, and writes the result
// atomically via WriteFile. All errors are returned unwrapped so callers keep
// their own message formats.
func WriteJSON(path string, v any, perm os.FileMode) error {
	return writeJSON(path, v, perm, false)
}

// WriteJSONDurable marshals v like WriteJSON and durably atomically replaces
// path after syncing the file and destination directory. The destination
// directory must already exist.
func WriteJSONDurable(path string, v any, perm os.FileMode) error {
	return writeJSON(path, v, perm, true)
}

func writeJSON(path string, v any, perm os.FileMode, durable bool) error {
	if !durable {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if durable {
		return WriteFileDurable(path, append(out, '\n'), perm)
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
