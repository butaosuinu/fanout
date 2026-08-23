package state

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNextTelemetrySequenceIncreasesAndWritesPrivateFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), ".fanout", "state.json")
	for _, want := range []uint64{1, 2} {
		got, err := NextTelemetrySequence(context.Background(), statePath)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("sequence = %d, want %d", got, want)
		}
	}
	info, err := os.Stat(statePath + ".sequence")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sequence mode = %o, want 600", info.Mode().Perm())
	}
}

func TestNextTelemetrySequenceRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	for _, targetExists := range []bool{false, true} {
		t.Run(strconv.FormatBool(targetExists), func(t *testing.T) {
			root := t.TempDir()
			statePath := filepath.Join(root, ".fanout", "state.json")
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "outside")
			if targetExists {
				if err := os.WriteFile(target, []byte("7\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, statePath+".sequence"); err != nil {
				t.Fatal(err)
			}
			if _, err := NextTelemetrySequence(context.Background(), statePath); err == nil ||
				!strings.Contains(err.Error(), "regular file") {
				t.Fatalf("symlink sequence error = %v", err)
			}
			contents, err := os.ReadFile(target)
			if targetExists {
				if err != nil || string(contents) != "7\n" {
					t.Fatalf("existing target = %q, %v; want unchanged", contents, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("dangling target was created: %v", err)
			}
		})
	}
}

func TestNextTelemetrySequenceRejectsNonRegularFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), ".fanout", "state.json")
	if err := os.MkdirAll(statePath+".sequence", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NextTelemetrySequence(context.Background(), statePath); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory sequence error = %v", err)
	}
}

func TestReadTelemetrySequenceFileDoesNotBlockAfterFIFOReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sequence")
	if err := os.WriteFile(path, []byte("7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, readErr := readTelemetrySequenceFile(path, expected)
		done <- readErr
	}()
	select {
	case readErr := <-done:
		if readErr == nil || !strings.Contains(readErr.Error(), "regular file") {
			t.Fatalf("replaced FIFO error = %v", readErr)
		}
	case <-time.After(500 * time.Millisecond):
		writer, _ := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if writer != nil {
			_ = writer.Close()
		}
		t.Fatal("readTelemetrySequenceFile() blocked on a replaced FIFO")
	}
}

func TestNextTelemetrySequenceRejectsMalformedAndExhaustedFiles(t *testing.T) {
	for _, contents := range []string{"bad\n", strconv.FormatUint(math.MaxUint64, 10) + "\n"} {
		t.Run(contents, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), ".fanout", "state.json")
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath+".sequence", []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NextTelemetrySequence(context.Background(), statePath); err == nil {
				t.Fatal("NextTelemetrySequence() accepted an unusable file")
			}
			got, err := os.ReadFile(statePath + ".sequence")
			if err != nil || string(got) != contents {
				t.Fatalf("rejected sequence changed to %q, %v", got, err)
			}
		})
	}
}
