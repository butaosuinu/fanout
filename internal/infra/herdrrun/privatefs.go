package herdrrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

func ensurePrivateDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return validatePrivateDir(path)
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("herdr owned directory %s is not an owner-only real directory", path)
	}
	if err := validateOwnerUID(path, info); err != nil {
		return err
	}
	return nil
}

func ensurePrivateContents(path string, expected []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	if statErr == nil {
		statErr = validatePrivateRegular(path, info)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, int64(len(expected)+1)))
	if statErr == nil && readErr == nil && len(data) == 0 {
		_, readErr = f.WriteAt(expected, 0)
		if readErr == nil {
			readErr = f.Sync()
		}
		data = expected
	}
	closeErr := f.Close()
	if err := errors.Join(statErr, readErr, closeErr); err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return fmt.Errorf("herdr owned file %s has unexpected contents", path)
	}
	return nil
}

func validatePrivateContents(path string, expected []byte) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	err = validatePrivateRegular(path, info)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(expected)+1)))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return fmt.Errorf("herdr owned file %s has unexpected contents", path)
	}
	return nil
}

func validatePrivateRegular(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("herdr owned file %s is not an owner-only regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("herdr owned file %s has an invalid link identity", path)
	}
	if err := validateOwnerUID(path, info); err != nil {
		return err
	}
	return nil
}

func validatePrivateSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || !isOwnerOnlySocketMode(info.Mode()) {
		return fmt.Errorf("herdr owned socket %s is not an owner-only Unix socket", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("herdr owned socket %s has an invalid link identity", path)
	}
	if err := validateOwnerUID(path, info); err != nil {
		return err
	}
	return nil
}

func isOwnerOnlySocketMode(mode os.FileMode) bool {
	permissions := mode.Perm()
	return permissions == 0o600 || permissions == 0o700
}

// tmux-parity omits extended ACL inspection; see docs/herdr-runtime-backend-spike.ja.md.
func validateOwnerUID(path string, info os.FileInfo) error {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("herdr owned path %s belongs to uid %d, want %d", path, stat.Uid, os.Getuid())
	}
	return nil
}

func lockPrivateFileContext(ctx context.Context, path string) (*os.File, error) {
	return lockPrivateFileContextWithFlags(ctx, path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW)
}

func lockExistingPrivateFileContext(ctx context.Context, path string) (*os.File, error) {
	return lockPrivateFileContextWithFlags(ctx, path, os.O_RDWR|syscall.O_NOFOLLOW)
}

func lockPrivateFileContextWithFlags(ctx context.Context, path string, flags int) (*os.File, error) {
	if ctx == nil {
		return nil, fmt.Errorf("lock private file requires a context")
	}
	f, err := openPrivateLockFile(path, flags)
	if err != nil {
		return nil, err
	}
	return waitForPrivateFileLock(ctx, f)
}

func openPrivateLockFile(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		err = validatePrivateRegular(path, info)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func waitForPrivateFileLock(ctx context.Context, f *os.File) (*os.File, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func unlockPrivateFile(f *os.File) {
	if f == nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		_ = f.Close()
		return
	}
	_ = f.Close()
}

func openPrivateAppendFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		err = validatePrivateRegular(path, info)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
