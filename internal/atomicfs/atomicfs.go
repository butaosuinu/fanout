// Package atomicfs writes a file atomically: tempfile in the destination
// directory, then rename. Multiple packages need this exact dance for
// dmux.config.json and worktree-metadata.json updates, so it lives here once.
package atomicfs

import (
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
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
