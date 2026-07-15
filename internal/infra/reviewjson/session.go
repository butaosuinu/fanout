package reviewjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var sessionUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type childSession struct {
	version, message          string
	meta, contexts, completes int
	taskMatched               bool
}

type sessionRecord struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type inputBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CaptureSession verifies a native reviewer child and exclusively publishes
// its exact task_complete.last_agent_message.
func CaptureSession(root, child, parent, reservedAt, role, bundle, output string) error {
	if !sessionUUID.MatchString(child) || !sessionUUID.MatchString(parent) || child == parent {
		return errors.New("child and parent session IDs must be distinct canonical UUIDs")
	}
	if (role != "post-work-reviewer" && role != "post-work-verifier") || !filepath.IsAbs(bundle) {
		return errors.New("expected role or absolute bundle path is invalid")
	}
	reserved, err := time.Parse(time.RFC3339Nano, reservedAt)
	if err != nil {
		return fmt.Errorf("reservation timestamp is not RFC3339: %w", err)
	}
	path, err := findChildRollout(root, child)
	if err != nil {
		return err
	}
	message, err := inspectChildRollout(path, child, parent, role, bundle, reserved)
	if err != nil {
		return err
	}
	return writeExclusive(output, []byte(message))
}

func findChildRollout(root, child string) (string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), child+".jsonl") {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan sessions root: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("found %d rollouts for child session, want 1", len(matches))
	}
	return matches[0], nil
}

func inspectChildRollout(path, child, parent, role, bundle string, reserved time.Time) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open child rollout: %w", err)
	}
	defer func() { _ = file.Close() }() // The decoder has consumed the read-only file.
	decoder, state := json.NewDecoder(file), childSession{}
	for {
		var record sessionRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("parse child rollout: %w", err)
		}
		switch record.Type {
		case "session_meta":
			state.meta++
			var meta map[string]any
			if err := json.Unmarshal(record.Payload, &meta); err != nil {
				return "", fmt.Errorf("parse session_meta: %w", err)
			}
			spawn := object(object(object(meta["source"])["subagent"])["thread_spawn"])
			created, err := time.Parse(time.RFC3339Nano, text(meta, "timestamp"))
			if err != nil {
				return "", fmt.Errorf("session_meta timestamp is not RFC3339: %w", err)
			}
			if text(meta, "id") != child || text(meta, "parent_thread_id") != parent ||
				text(spawn, "parent_thread_id") != parent || text(meta, "thread_source") != "subagent" {
				return "", errors.New("child rollout does not belong to the reserved parent session")
			}
			if text(meta, "agent_role") != role || text(spawn, "agent_role") != role {
				return "", errors.New("child rollout has an unexpected native agent role")
			}
			state.version = text(meta, "multi_agent_version")
			if (state.version != "v1" && state.version != "v2") || !created.After(reserved) {
				return "", errors.New("child rollout has an unsupported version or is not fresh")
			}
		case "turn_context":
			var context struct {
				Approval string `json:"approval_policy"`
				Sandbox  struct {
					Type string `json:"type"`
				} `json:"sandbox_policy"`
			}
			if err := json.Unmarshal(record.Payload, &context); err != nil {
				return "", fmt.Errorf("parse turn_context: %w", err)
			}
			if context.Sandbox.Type != "read-only" || context.Approval != "never" {
				return "", errors.New("child rollout is not read-only with approval_policy=never")
			}
			state.contexts++
		case "response_item":
			if err := recordTaskInput(record.Payload, bundle, &state); err != nil {
				return "", err
			}
		case "event_msg":
			var event struct {
				Type        string  `json:"type"`
				Message     string  `json:"message"`
				LastMessage *string `json:"last_agent_message"`
			}
			if err := json.Unmarshal(record.Payload, &event); err != nil {
				return "", fmt.Errorf("parse event_msg: %w", err)
			}
			if event.Type == "user_message" {
				state.taskMatched = state.taskMatched || event.Message == bundle
			}
			if event.Type == "task_complete" {
				state.completes++
				if event.LastMessage != nil {
					state.message = *event.LastMessage
				}
			}
		}
	}
	if state.meta != 1 || state.contexts == 0 || state.completes != 1 {
		return "", errors.New("child rollout has incomplete or duplicate terminal metadata")
	}
	if !state.taskMatched {
		return "", errors.New("child rollout does not contain the exact plaintext bundle path")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(state.message), &result); err != nil || text(result, "reviewer_session_id") != child {
		return "", errors.New("reviewer result is invalid or reviewer_session_id does not match the child")
	}
	return state.message, nil
}

func recordTaskInput(raw json.RawMessage, bundle string, state *childSession) error {
	var item struct {
		Type    string       `json:"type"`
		Role    string       `json:"role"`
		Content []inputBlock `json:"content"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return fmt.Errorf("parse response_item: %w", err)
	}
	if item.Type != "agent_message" && (item.Type != "message" || item.Role != "user") {
		return nil
	}
	for _, block := range item.Content {
		state.taskMatched = state.taskMatched || block.Type == "input_text" && block.Text == bundle
	}
	return nil
}

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }

func text(value map[string]any, key string) string { result, _ := value[key].(string); return result }

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create reviewer result: %w", err)
	}
	cleanup := func() {
		_ = file.Close() // Preserve the write failure; cleanup is best effort.
		_ = os.Remove(path)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if written, err := file.Write(data); err != nil || written != len(data) {
		cleanup()
		return errors.New("write complete reviewer result")
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}
	return nil
}
