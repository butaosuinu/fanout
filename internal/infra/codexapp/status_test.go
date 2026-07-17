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
