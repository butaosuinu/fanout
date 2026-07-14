package reviewjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// ExtractLastAgentMessage writes the exact UTF-8 task_complete message from
// one uniquely identified Codex rollout. The destination is published only
// after the complete 0600 file is ready, and an existing path is never
// replaced.
func ExtractLastAgentMessage(
	sessionsRoot string,
	sessionID string,
	outputPath string,
) (string, error) {
	if !isCanonicalUUID(sessionID) {
		return "", unavailable("session ID is not a canonical UUID", nil)
	}
	if outputPath == "" {
		return "", unavailable("extract output path is empty", nil)
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return "", unavailable("extract output path already exists", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", unavailable("inspect extract output path", err)
	}

	rolloutPath, err := findUniqueRollout(sessionsRoot, sessionID)
	if err != nil {
		return "", err
	}
	message, err := readUniqueLastAgentMessage(rolloutPath, sessionID)
	if err != nil {
		return "", err
	}
	if err := writeNewFileAtomically(outputPath, []byte(message), 0o600); err != nil {
		return "", unavailable("write extracted reviewer result", err)
	}
	return sessionID, nil
}

func readUniqueLastAgentMessage(path string, sessionID string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", unavailable("read rollout for extraction", err)
	}
	if !utf8.Valid(data) {
		return "", unavailable("rollout for extraction is not valid UTF-8", nil)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	sessionMetaCount := 0
	taskCompleteCount := 0
	message := ""
	for recordNumber := 1; ; recordNumber++ {
		var record rolloutRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", unavailable(
				fmt.Sprintf("malformed rollout record %d during extraction", recordNumber),
				err,
			)
		}
		switch record.Type {
		case "session_meta":
			sessionMetaCount++
			var payload struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return "", unavailable("malformed extraction session_meta payload", err)
			}
			actualID, err := requiredJSONString(payload.ID, "extraction session_meta.id")
			if err != nil {
				return "", err
			}
			if actualID != sessionID {
				return "", mismatch("extraction session_meta.id does not match the requested session")
			}
		case "event_msg":
			complete, candidate, seen, err := parseTaskCompleteForExtraction(record.Payload)
			if err != nil {
				return "", err
			}
			if complete {
				taskCompleteCount++
				if seen {
					message = candidate
				}
			}
		}
	}
	if sessionMetaCount != 1 {
		return "", unavailable(
			fmt.Sprintf("rollout contains %d session_meta records, want 1", sessionMetaCount),
			nil,
		)
	}
	if taskCompleteCount != 1 {
		return "", mismatch(fmt.Sprintf(
			"rollout contains %d task_complete records, want 1",
			taskCompleteCount,
		))
	}
	return message, nil
}

func parseTaskCompleteForExtraction(raw json.RawMessage) (bool, string, bool, error) {
	var payload struct {
		Type             string          `json:"type"`
		LastAgentMessage json.RawMessage `json:"last_agent_message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, "", false, unavailable("malformed event_msg payload", err)
	}
	if payload.Type != "task_complete" {
		return false, "", false, nil
	}
	if err := validateSurrogatePairs(payload.LastAgentMessage); err != nil {
		return true, "", false, unavailable(
			"task_complete.last_agent_message contains an unpaired UTF-16 surrogate escape",
			err,
		)
	}
	message, err := requiredJSONString(
		payload.LastAgentMessage,
		"task_complete.last_agent_message",
	)
	if err != nil {
		return true, "", false, err
	}
	return true, message, true, nil
}

func writeNewFileAtomically(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fanout-extract-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		// The publish error is the actionable failure; cleanup is best effort.
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	// Linking a complete sibling tempfile publishes it atomically and fails
	// when any file or symlink already occupies the destination.
	if err := os.Link(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	cleanup()
	return nil
}
