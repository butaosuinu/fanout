//go:build !darwin && !linux

package atomicfs

import "fmt"

type compareAndSwapLock struct{}

func acquireCompareAndSwapLock(string) (*compareAndSwapLock, error) {
	return nil, fmt.Errorf("atomic file compare-and-swap is unsupported on this platform")
}

func (*compareAndSwapLock) release() error {
	return nil
}
