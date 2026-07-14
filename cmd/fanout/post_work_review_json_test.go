package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
)

func TestPostWorkReviewJSONRequest(t *testing.T) {
	t.Parallel()
	if !isPostWorkReviewJSONRequest([]string{postWorkReviewJSONCommand, "project", "in", "out"}) {
		t.Fatal("hidden post-work-review JSON command was not recognized")
	}
	if isPostWorkReviewJSONRequest([]string{"post-work-review-json"}) {
		t.Fatal("public-looking command unexpectedly matched hidden helper")
	}
}

func TestCmdPostWorkReviewJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "review.json")
	cache := filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(`{"backend":"bounded-isolated-reviewer","findings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	if code := cmdPostWorkReviewJSON([]string{"project", input, cache}, &stdout, &stderr); code != exitcode.OK {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d; stderr=%s", code, exitcode.OK, stderr.String())
	}
	if got := stdout.String(); got != postWorkReviewJSONVersionLine+"\n" {
		t.Fatalf("stdout = %q, want helper version line", got)
	}
	if _, err := os.Stat(filepath.Join(cache, "valid")); err != nil {
		t.Fatalf("Stat(valid) error = %v", err)
	}
}

func TestCmdPostWorkReviewJSONFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "review.json")
	cache := filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(`{} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	if code := cmdPostWorkReviewJSON([]string{"project", input, cache}, &stdout, &stderr); code != exitcode.Env {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d", code, exitcode.Env)
	}
	if got := stdout.String(); got != postWorkReviewJSONVersionLine+"\n" {
		t.Fatalf("stdout = %q, want helper version line", got)
	}
	if stderr.Len() == 0 {
		t.Fatal("cmdPostWorkReviewJSON() did not explain projection failure")
	}
	if _, err := os.Stat(filepath.Join(cache, "valid")); !os.IsNotExist(err) {
		t.Fatalf("valid marker exists after failure: %v", err)
	}
}

func TestCmdPostWorkReviewJSONTimestamp(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer

	if code := cmdPostWorkReviewJSON([]string{"timestamp"}, &stdout, &stderr); code != exitcode.OK {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d; stderr=%s", code, exitcode.OK, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 2 || lines[0] != postWorkReviewJSONVersionLine ||
		!strings.HasPrefix(lines[1], "timestamp=") {
		t.Fatalf("stdout = %q, want version and timestamp lines", stdout.String())
	}
	value := strings.TrimPrefix(lines[1], "timestamp=")
	if _, err := time.Parse(postWorkReviewTimestampLayout, value); err != nil {
		t.Fatalf("timestamp = %q: %v", value, err)
	}
	if len(value) != len("2006-01-02T15:04:05.000000000Z") {
		t.Fatalf("timestamp = %q, want fixed nanosecond precision", value)
	}
}

func TestCmdPostWorkReviewJSONRejectsOldProtocol(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer

	if code := cmdPostWorkReviewJSON([]string{"in", "out"}, &stdout, &stderr); code != exitcode.Invocation {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d", code, exitcode.Invocation)
	}
	if got := stdout.String(); got != postWorkReviewJSONVersionLine+"\n" {
		t.Fatalf("stdout = %q, want helper version line", got)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("expected timestamp")) {
		t.Fatalf("stderr = %q, want v3 usage", stderr.String())
	}
}

func TestCmdPostWorkReviewJSONAttest(t *testing.T) {
	t.Parallel()
	const (
		parentID = "019f5c42-734b-77d2-b935-0f8326bfd572"
		childID  = "019f5c78-2577-70f3-bc26-d6f83b2b5d72"
	)
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	sessions := filepath.Join(dir, "sessions", "2026", "07", "13")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	result := fmt.Sprintf(`{"reviewer_session_id":%q,"findings":[]}`, childID)
	bundle := filepath.Join(dir, "review-bundle.md")
	input := filepath.Join(dir, "review.json")
	if err := os.WriteFile(bundle, []byte("review bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleReadyAt := time.Date(2026, 7, 13, 17, 13, 25, 100_000_000, time.UTC)
	if err := os.Chtimes(bundle, bundleReadyAt, bundleReadyAt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
	agentConfig := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(
		agentConfig,
		[]byte("name = \"post-work-reviewer\"\nmodel = \"gpt-5.6-sol\"\nmodel_reasoning_effort = \"xhigh\"\nsandbox_mode = \"read-only\"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	rollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"parent_thread_id":%q,"timestamp":"2026-07-13T17:13:25.763Z","thread_source":"subagent","agent_role":"post-work-reviewer","multi_agent_version":"v1","source":{"subagent":{"thread_spawn":{"parent_thread_id":%q,"agent_role":"post-work-reviewer"}}}}}`, childID, parentID, parentID),
		fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}}`, bundle),
		`{"type":"turn_context","payload":{"model":"gpt-5.6-sol","effort":"xhigh","sandbox_policy":{"type":"read-only"}}}`,
		fmt.Sprintf(`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":%q}}`, result),
	}, "\n") + "\n"
	rolloutPath := filepath.Join(sessions, "rollout-2026-07-13T17-13-25-"+childID+".jsonl")
	if err := os.WriteFile(rolloutPath, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	spawnArguments := fmt.Sprintf(
		`{"agent_type":"post-work-reviewer","fork_context":false,"message":%q}`,
		bundle,
	)
	spawnOutput := fmt.Sprintf(`{"agent_id":%q}`, childID)
	parentRollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`, parentID),
		fmt.Sprintf(`{"timestamp":"2026-07-13T17:13:25.500Z","type":"response_item","payload":{"type":"function_call","name":"spawn_agent","namespace":"multi_agent_v1","call_id":"call-review","arguments":%q}}`, spawnArguments),
		fmt.Sprintf(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-review","output":%q}}`, spawnOutput),
	}, "\n") + "\n"
	parentRolloutPath := filepath.Join(sessions, "rollout-2026-07-13T17-13-24-"+parentID+".jsonl")
	if err := os.WriteFile(parentRolloutPath, []byte(parentRollout), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{
		"attest",
		input,
		cache,
		filepath.Join(dir, "sessions"),
		parentID,
		"2026-07-13T17:13:25Z",
		agentConfig,
		bundle,
	}
	if code := cmdPostWorkReviewJSON(args, &stdout, &stderr); code != exitcode.OK {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d; stderr=%s", code, exitcode.OK, stderr.String())
	}
	if got := stdout.String(); got != postWorkReviewJSONVersionLine+"\n" {
		t.Fatalf("stdout = %q, want helper version line", got)
	}
	if _, err := os.Stat(filepath.Join(cache, "attestation_valid")); err != nil {
		t.Fatalf("Stat(attestation_valid) error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(cache, "attested_history_mode")); err != nil ||
		string(got) != "no-history" {
		t.Fatalf("attested_history_mode = %q, %v", got, err)
	}
}
