package codexapp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteFailedStatusReportsPreControllerFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := WriteFailedStatus(path, errors.New("open watcher")); err != nil {
		t.Fatal(err)
	}

	status, err := readStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != statusFailed || status.Error != "open watcher" {
		t.Fatalf("status = %+v, want failed/open watcher", status)
	}
}

func TestStartupFailureReturnsOnlyFailedStatus(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if err := StartupFailure(missing); err != nil {
		t.Fatalf("missing status error = %v", err)
	}
	ready := filepath.Join(t.TempDir(), "ready.json")
	if err := writeStatus(ready, Status{Status: statusReady}); err != nil {
		t.Fatal(err)
	}
	if err := StartupFailure(ready); err != nil {
		t.Fatalf("ready status error = %v", err)
	}
	failed := filepath.Join(t.TempDir(), "failed.json")
	if err := WriteFailedStatus(failed, errors.New("owner mismatch")); err != nil {
		t.Fatal(err)
	}
	if err := StartupFailure(failed); err == nil || err.Error() != "owner mismatch" {
		t.Fatalf("failed status error = %v", err)
	}
}

func TestWaitForCodexPlanTUIReadyReadsReadyStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := writeStatus(path, Status{Status: statusReady}); err != nil {
		t.Fatal(err)
	}

	if err := waitForCodexPlanTUIReady(path, time.Second); err != nil {
		t.Fatalf("waitForCodexPlanTUIReady() failed: %v", err)
	}
}

func TestWaitForCodexPlanTUIReadyReturnsFailedStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := writeStatus(path, Status{Status: statusFailed, Error: "boom"}); err != nil {
		t.Fatal(err)
	}

	err := waitForCodexPlanTUIReady(path, time.Second)

	if err == nil || err.Error() != "boom" {
		t.Fatalf("waitForCodexPlanTUIReady() error = %v, want boom", err)
	}
}

func TestWaitForCodexPlanTUIReadyTimesOutWithoutStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	err := waitForCodexPlanTUIReady(path, time.Millisecond)

	if err == nil || !strings.Contains(err.Error(), errCodexPlanStartupTimeout.Error()) {
		t.Fatalf("waitForCodexPlanTUIReady() error = %v, want timeout", err)
	}
}
