package codexapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Status values for the Codex Plan TUI status file handshake.
const (
	statusReady  = "ready"
	statusFailed = "failed"
)

const codexPlanTUIStartupPoll = 200 * time.Millisecond

var errCodexPlanStartupTimeout = errors.New("timed out waiting for Codex Plan TUI startup")

// Status is the JSON payload the Plan Mode controller writes to its status
// file once the Codex TUI is attached (or startup failed).
type Status struct {
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	ThreadID  string `json:"threadId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Remote    string `json:"remote,omitempty"`
}

// writeStatus atomically writes the status file (tmp + rename). An empty path
// is a no-op. Deliberately not atomicfs.WriteJSON: its exact chmod would widen
// the file mode under a restrictive umask, while os.WriteFile keeps the
// historical umask-masked 0644.
func writeStatus(path string, status Status) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readStatus parses the status file at path.
func readStatus(path string) (Status, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(body, &status); err != nil {
		return Status{}, fmt.Errorf("parse Codex Plan TUI status: %w", err)
	}
	return status, nil
}

// WaitReady polls the status file until it reports ready, reports failed, or
// the timeout elapses.
func WaitReady(statusPath string, timeout time.Duration) (Status, error) {
	if strings.TrimSpace(statusPath) == "" {
		return Status{}, fmt.Errorf("missing Codex Plan TUI status path")
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		status, err := readStatus(statusPath)
		if err == nil {
			switch status.Status {
			case statusReady:
				return status, nil
			case statusFailed:
				if status.Error == "" {
					return Status{}, fmt.Errorf("Codex Plan TUI setup failed") //nolint:staticcheck // ST1005: "Codex Plan TUI" is a proper noun
				}
				return Status{}, errors.New(status.Error)
			default:
				lastErr = fmt.Errorf("unexpected Codex Plan TUI status %q", status.Status)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return Status{}, fmt.Errorf("%w after %s; last status error: %w", errCodexPlanStartupTimeout, timeout, lastErr)
			}
			return Status{}, fmt.Errorf("%w after %s; no status file at %s", errCodexPlanStartupTimeout, timeout, statusPath)
		}
		time.Sleep(codexPlanTUIStartupPoll)
	}
}

func waitForCodexPlanTUIReady(statusPath string, timeout time.Duration) error {
	_, err := WaitReady(statusPath, timeout)
	return err
}
