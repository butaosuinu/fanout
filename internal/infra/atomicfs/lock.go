package atomicfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockRetryInterval is how often a contended lock is retried. The critical
// sections it guards are a read, a decision, and one atomic write, so the wait
// is short by construction.
const lockRetryInterval = 20 * time.Millisecond

// ErrLockBusy says another holder has the lock and the deadline passed.
var ErrLockBusy = errors.New("file lock is held elsewhere")

// Lock takes an exclusive lock on path+".lock" and returns the release.
//
// An atomic write makes each write indivisible, but that is not the same as
// making read-decide-write indivisible: two processes can both read "no claim
// here" and then both write. Anything that decides based on what a file
// currently says needs this around the whole sequence.
//
// The lock file is never removed. Unlinking it would let a second process
// create a fresh one and lock that instead, which is the same as no lock at
// all.
func Lock(path string, wait time.Duration) (release func(), err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = flockUntil(f, wait); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", lockPath, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func flockUntil(f *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if !time.Now().Before(deadline) {
			return ErrLockBusy
		}
		time.Sleep(lockRetryInterval)
	}
}
