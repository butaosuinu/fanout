package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
)

const maxTelemetrySequenceBytes = 21

// NextTelemetrySequence allocates one repository-local telemetry sequence
// while holding the state lock shared with emitter updates and older writers.
func NextTelemetrySequence(ctx context.Context, statePath string) (sequence uint64, err error) {
	defer errs.Wrap(&err, "allocate telemetry sequence for %s", statePath)
	locked, err := LockContext(ctx, statePath)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()

	sequencePath := statePath + ".sequence"
	previous, err := readTelemetrySequence(sequencePath)
	if err != nil {
		return 0, err
	}
	if previous == math.MaxUint64 {
		return 0, fmt.Errorf("sequence is exhausted")
	}
	sequence = previous + 1
	data := []byte(strconv.FormatUint(sequence, 10) + "\n")
	if writeErr := atomicfs.WriteFile(sequencePath, data, 0o600); writeErr != nil {
		return 0, fmt.Errorf("write sequence: %w", writeErr)
	}
	return sequence, nil
}

func readTelemetrySequence(path string) (uint64, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect sequence: %w", err)
	}
	if err := validateTelemetrySequenceFile(info); err != nil {
		return 0, err
	}
	data, err := readTelemetrySequenceFile(path, info)
	if err != nil {
		return 0, err
	}
	return parseTelemetrySequence(data)
}

func readTelemetrySequenceFile(path string, expected os.FileInfo) (data []byte, err error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open sequence: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	current, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened sequence: %w", err)
	}
	if err := validateOpenedTelemetrySequence(expected, current); err != nil {
		return nil, err
	}
	data, err = io.ReadAll(io.LimitReader(file, maxTelemetrySequenceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read sequence: %w", err)
	}
	if len(data) > maxTelemetrySequenceBytes {
		return nil, fmt.Errorf("sequence is invalid")
	}
	return data, nil
}

func validateOpenedTelemetrySequence(expected, current os.FileInfo) error {
	if err := validateTelemetrySequenceFile(current); err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return fmt.Errorf("sequence changed during read")
	}
	return nil
}

func validateTelemetrySequenceFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("sequence must be a regular file")
	}
	if info.Size() > maxTelemetrySequenceBytes {
		return fmt.Errorf("sequence is invalid")
	}
	return nil
}

func parseTelemetrySequence(data []byte) (uint64, error) {
	raw := strings.TrimSuffix(string(data), "\n")
	if raw == "" || string(data) != raw+"\n" {
		return 0, fmt.Errorf("sequence is invalid")
	}
	sequence, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sequence is invalid")
	}
	return sequence, nil
}
