package atomicfs

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestLockExcludesASecondHolder is the whole point: an atomic write makes each
// write indivisible, but read-decide-write is only indivisible with this.
func TestLockExcludesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims.json")
	release, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, busyErr := Lock(path, 50*time.Millisecond); !errors.Is(busyErr, ErrLockBusy) {
		t.Fatalf("second Lock() error = %v, want ErrLockBusy", busyErr)
	}

	release()
	second, err := Lock(path, time.Second)
	if err != nil {
		t.Fatalf("Lock() after release = %v, want it granted", err)
	}
	second()
}

func TestLockCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "claims.json")
	release, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
