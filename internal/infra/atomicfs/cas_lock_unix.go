//go:build darwin || linux

package atomicfs

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

type compareAndSwapLock struct {
	file *os.File
}

func acquireCompareAndSwapLock(path string) (*compareAndSwapLock, error) {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open compare-and-swap lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock compare-and-swap destination: %w", err)
	}
	return &compareAndSwapLock{file: file}, nil
}

func (l *compareAndSwapLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
