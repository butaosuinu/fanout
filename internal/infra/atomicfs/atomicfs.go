// Package atomicfs writes a file atomically: tempfile in the destination
// directory, then rename. Multiple packages need this exact dance for state
// and worktree-metadata updates, so it lives here once.
package atomicfs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// WriteFileExclusive atomically publishes a complete file only when path does
// not exist. The sibling tempfile and hard link keep an existing destination
// from being replaced between a caller's preflight and publication.
func WriteFileExclusive(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fanout-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Cleanup errors below are discarded deliberately: the write/chmod/close/link
	// error is actionable, and a stray tempfile is harmless.
	cleanup := func() {
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	cleanup()
	return nil
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
	return readJSON(path, v, false)
}

// ReadJSONStrict reads path like ReadJSON but rejects unknown fields at every
// decoded struct level. Callers use it when preserving unrecognized state
// across a read-modify-write cycle cannot be proven safe.
func ReadJSONStrict(path string, v any) (found bool, err error) {
	return readJSON(path, v, true)
}

func readJSON(path string, v any, strict bool) (found bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !strict {
		if err := json.Unmarshal(data, v); err != nil {
			return true, err
		}
		return true, nil
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return true, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return true, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return true, errors.New("JSON contains multiple values")
		}
		return true, err
	}
	return true, nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains multiple values")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}
