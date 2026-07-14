package main

import (
	"bytes"
	"crypto/sha256"
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

func TestCmdPostWorkReviewJSONDigest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bundle := filepath.Join(dir, "review-bundle.md")
	if err := os.WriteFile(bundle, []byte("review bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	if code := cmdPostWorkReviewJSON([]string{"digest", bundle}, &stdout, &stderr); code != exitcode.OK {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d; stderr=%s", code, exitcode.OK, stderr.String())
	}
	const digest = "e0b082ea1630370a8a6ba7e08afdbdbada22a1831a34f6fa7d531cda988f25c9"
	want := postWorkReviewJSONVersionLine + "\nbundle_sha256=" + digest + "\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestCmdPostWorkReviewJSONDigestFailsClosed(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer

	if code := cmdPostWorkReviewJSON([]string{"digest", filepath.Join(t.TempDir(), "missing")}, &stdout, &stderr); code != exitcode.Env {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d", code, exitcode.Env)
	}
	if got := stdout.String(); got != postWorkReviewJSONVersionLine+"\n" {
		t.Fatalf("stdout = %q, want helper version line", got)
	}
	if !strings.Contains(stderr.String(), "digest") {
		t.Fatalf("stderr = %q, want digest failure", stderr.String())
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
		t.Fatalf("stderr = %q, want v5 usage", stderr.String())
	}
}

func TestCmdPostWorkReviewJSONControllerFailsClosed(t *testing.T) {
	t.Parallel()
	const parentID = "019f5c42-734b-77d2-b935-0f8326bfd572"
	dir := t.TempDir()
	sessionsRoot := filepath.Join(dir, "sessions")
	sessions := filepath.Join(sessionsRoot, "2026", "07", "13")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`, parentID),
		`{"timestamp":"2026-07-13T17:13:25.200Z","type":"turn_context","payload":{"turn_id":"019f5c42-734b-77d2-b935-0f8326bfd573","sandbox_policy":{"type":"workspace-write"}}}`,
	}, "\n") + "\n"
	path := filepath.Join(sessions, "rollout-2026-07-13T17-13-24-"+parentID+".jsonl")
	if err := os.WriteFile(path, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := cmdPostWorkReviewJSON(
		[]string{"controller", sessionsRoot, parentID},
		&stdout,
		&stderr,
	); code != exitcode.Env {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d", code, exitcode.Env)
	}
	if got := stdout.String(); got != postWorkReviewJSONVersionLine+"\n" {
		t.Fatalf("stdout = %q, want helper version line only", got)
	}
	if !strings.Contains(stderr.String(), "sandbox is not read-only") {
		t.Fatalf("stderr = %q, want controller sandbox failure", stderr.String())
	}
}

func TestCmdPostWorkReviewJSONExtractPreservesUnicode(t *testing.T) {
	t.Parallel()
	const (
		sessionID = "019f5c78-2577-70f3-bc26-d6f83b2b5d75"
		message   = "修正済み — JSONをそのまま保持"
	)
	dir := t.TempDir()
	sessionsRoot := filepath.Join(dir, "sessions")
	sessions := filepath.Join(sessionsRoot, "2026", "07", "13")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`, sessionID),
		fmt.Sprintf(
			`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":%q}}`,
			message,
		),
	}, "\n") + "\n"
	path := filepath.Join(sessions, "rollout-2026-07-13T17-13-25-"+sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(dir, "review.json")

	var stdout, stderr bytes.Buffer
	if code := cmdPostWorkReviewJSON(
		[]string{"extract", sessionsRoot, sessionID, outputPath},
		&stdout,
		&stderr,
	); code != exitcode.OK {
		t.Fatalf("cmdPostWorkReviewJSON() = %d, want %d; stderr=%s", code, exitcode.OK, stderr.String())
	}
	wantStdout := postWorkReviewJSONVersionLine + "\nextracted_session_id=" + sessionID + "\n"
	if got := stdout.String(); got != wantStdout {
		t.Fatalf("stdout = %q, want %q", got, wantStdout)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != message {
		t.Fatalf("extracted bytes = %q, want %q", data, message)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output mode = %o, want 600", got)
	}
}

func TestCmdPostWorkReviewJSONAttest(t *testing.T) {
	t.Parallel()
	const (
		parentID = "019f5c42-734b-77d2-b935-0f8326bfd572"
		childID  = "019f5c78-2577-70f3-bc26-d6f83b2b5d72"
		turnID   = "019f5c42-734b-77d2-b935-0f8326bfd573"
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
		[]byte("name = \"post-work-reviewer\"\nmodel = \"gpt-5.6-sol\"\nmodel_reasoning_effort = \"xhigh\"\nsandbox_mode = \"read-only\"\napproval_policy = \"never\"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	rollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"parent_thread_id":%q,"timestamp":"2026-07-13T17:13:25.763Z","thread_source":"subagent","agent_role":"post-work-reviewer","multi_agent_version":"v1","source":{"subagent":{"thread_spawn":{"parent_thread_id":%q,"agent_role":"post-work-reviewer"}}}}}`, childID, parentID, parentID),
		fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}}`, bundle),
		`{"type":"turn_context","payload":{"model":"gpt-5.6-sol","effort":"xhigh","approval_policy":"never","sandbox_policy":{"type":"read-only"}}}`,
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
	controllerPayload := fmt.Sprintf(
		`{"turn_id":%q,"sandbox_policy":{"type":"read-only"}}`,
		turnID,
	)
	controllerDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(controllerPayload)))
	parentRollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`, parentID),
		fmt.Sprintf(`{"timestamp":"2026-07-13T17:13:25.200Z","type":"turn_context","payload":%s}`, controllerPayload),
		fmt.Sprintf(`{"timestamp":"2026-07-13T17:13:25.300Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","namespace":"functions","call_id":"call-authorize","arguments":"{}","internal_chat_message_metadata_passthrough":{"turn_id":%q}}}`, turnID),
		fmt.Sprintf(`{"timestamp":"2026-07-13T17:13:25.450Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-authorize","output":"{}","internal_chat_message_metadata_passthrough":{"turn_id":%q}}}`, turnID),
		fmt.Sprintf(`{"timestamp":"2026-07-13T17:13:25.500Z","type":"response_item","payload":{"type":"function_call","name":"spawn_agent","namespace":"multi_agent_v1","call_id":"call-review","arguments":%q,"internal_chat_message_metadata_passthrough":{"turn_id":%q}}}`, spawnArguments, turnID),
		fmt.Sprintf(`{"timestamp":"2026-07-13T17:13:25.800Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-review","output":%q}}`, spawnOutput),
	}, "\n") + "\n"
	parentRolloutPath := filepath.Join(sessions, "rollout-2026-07-13T17-13-24-"+parentID+".jsonl")
	if err := os.WriteFile(parentRolloutPath, []byte(parentRollout), 0o600); err != nil {
		t.Fatal(err)
	}
	var controllerStdout, controllerStderr bytes.Buffer
	controllerArgs := []string{"controller", filepath.Join(dir, "sessions"), parentID}
	if code := cmdPostWorkReviewJSON(
		controllerArgs,
		&controllerStdout,
		&controllerStderr,
	); code != exitcode.OK {
		t.Fatalf(
			"controller cmdPostWorkReviewJSON() = %d, want %d; stderr=%s",
			code,
			exitcode.OK,
			controllerStderr.String(),
		)
	}
	controllerWant := postWorkReviewJSONVersionLine + "\n" +
		"review_controller_turn_id=" + turnID + "\n" +
		"review_controller_context_sha256=" + controllerDigest + "\n" +
		"review_controller_sandbox_mode=read-only\n"
	if got := controllerStdout.String(); got != controllerWant {
		t.Fatalf("controller stdout = %q, want %q", got, controllerWant)
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
		turnID,
		controllerDigest,
		"2026-07-13T17:13:25.400Z",
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
