package reviewjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testParentSessionID = "019f5c42-734b-77d2-b935-0f8326bfd572"
	testReviewSessionID = "019f5c78-2577-70f3-bc26-d6f83b2b5d72"
	testOtherSessionID  = "019f5c78-2577-70f3-bc26-d6f83b2b5d73"
	testPreparedAt      = "2026-07-13T17:13:25Z"
	testCreatedAt       = "2026-07-13T17:13:25.763Z"
	testReviewerRole    = "post-work-reviewer"
	testReviewerModel   = "gpt-5.6-sol"
)

func TestAttestWritesActualMetadataAfterProjection(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	fixture.writeRollout(defaultRolloutOptions(fixture.resultMessage))

	if err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		"",
	); err != nil {
		t.Fatalf("Attest() error = %v", err)
	}

	want := map[string]string{
		"valid":                           "",
		"attestation_valid":               "",
		"attestation_version":             AttestationVersion,
		"attested_session_id":             testReviewSessionID,
		"attested_parent_thread_id":       testParentSessionID,
		"attested_agent_role":             testReviewerRole,
		"attested_model":                  testReviewerModel,
		"attested_sandbox_mode":           "read-only",
		"reviewer_session_id":             testReviewSessionID,
		"findings_count":                  "0",
		"finding_count":                   "0",
		"reviewer_sandbox_mode":           "read-only",
		"reviewer_agent":                  testReviewerRole,
		"reviewer_provenance":             "native-subagent-tool",
		"same_agent_review":               "false",
		"reviewer_isolated":               "true",
		"hooks_only_success":              "false",
		"findings_missing_required_count": "0",
	}
	for name, expected := range want {
		got, err := os.ReadFile(filepath.Join(fixture.cacheDir, name))
		if err != nil {
			t.Errorf("ReadFile(%s) error = %v", name, err)
			continue
		}
		if string(got) != expected {
			t.Errorf("cache %s = %q, want %q", name, got, expected)
		}
	}
	for _, name := range []string{"attestation_error", "attestation_error_kind"} {
		if _, err := os.Stat(filepath.Join(fixture.cacheDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s exists after successful attestation: %v", name, err)
		}
	}
}

func TestAttestAcceptsVerifierAgentConfig(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	config := "name = \"post-work-verifier\"\nmodel = \"gpt-5.6-terra\"\nsandbox_mode = \"read-only\"\n"
	if err := os.WriteFile(fixture.agentConfigPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	options := defaultRolloutOptions(fixture.resultMessage)
	options.agentRole = "post-work-verifier"
	options.model = "gpt-5.6-terra"
	fixture.writeRollout(options)

	if err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		"",
	); err != nil {
		t.Fatalf("Attest() error = %v", err)
	}
	for name, expected := range map[string]string{
		"attested_agent_role": "post-work-verifier",
		"attested_model":      "gpt-5.6-terra",
	} {
		got, err := os.ReadFile(filepath.Join(fixture.cacheDir, name))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", name, err)
		}
		if string(got) != expected {
			t.Errorf("cache %s = %q, want %q", name, got, expected)
		}
	}
}

func TestAttestRejectsContradictoryRolloutMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*rolloutOptions)
		wantErr string
	}{
		{
			name: "workspace write sandbox",
			change: func(options *rolloutOptions) {
				options.sandboxMode = "workspace-write"
			},
			wantErr: "sandbox policy",
		},
		{
			name: "null role despite reviewer-like task name",
			change: func(options *rolloutOptions) {
				options.agentRole = nil
			},
			wantErr: "agent_role is null",
		},
		{
			name: "wrong model",
			change: func(options *rolloutOptions) {
				options.model = "gpt-5.6-terra"
			},
			wantErr: "model",
		},
		{
			name: "metadata UUID differs",
			change: func(options *rolloutOptions) {
				options.sessionID = testOtherSessionID
			},
			wantErr: "session_meta.id",
		},
		{
			name: "wrong metadata parent",
			change: func(options *rolloutOptions) {
				options.parentThreadID = testOtherSessionID
			},
			wantErr: "session_meta.parent_thread_id",
		},
		{
			name: "wrong spawn parent",
			change: func(options *rolloutOptions) {
				options.spawnParentThreadID = testOtherSessionID
			},
			wantErr: "thread_spawn.parent_thread_id",
		},
		{
			name: "not a subagent source",
			change: func(options *rolloutOptions) {
				options.threadSource = "cli"
			},
			wantErr: "thread_source",
		},
		{
			name: "session predates prepare",
			change: func(options *rolloutOptions) {
				options.createdAt = testPreparedAt
			},
			wantErr: "not created after prepare",
		},
		{
			name: "another turn uses another model",
			change: func(options *rolloutOptions) {
				options.extraTurnContexts = append(options.extraTurnContexts, rolloutTurnContext{
					model:       "gpt-5.6-terra",
					sandboxMode: "read-only",
				})
			},
			wantErr: "model",
		},
		{
			name: "transcript differs",
			change: func(options *rolloutOptions) {
				options.taskMessages = []any{`{"different":true}`}
			},
			wantErr: "does not match task_complete",
		},
		{
			name: "two completed turns",
			change: func(options *rolloutOptions) {
				options.taskMessages = append(options.taskMessages, options.taskMessages[0])
			},
			wantErr: "2 task_complete",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAttestationFixture(t)
			options := defaultRolloutOptions(fixture.resultMessage)
			test.change(&options)
			fixture.writeRollout(options)

			err := Attest(
				fixture.resultPath,
				fixture.cacheDir,
				fixture.sessionsRoot,
				testParentSessionID,
				testPreparedAt,
				fixture.agentConfigPath,
				"",
			)
			assertAttestationFailure(t, fixture.cacheDir, err, AttestationMismatch, test.wantErr)
		})
	}
}

func TestAttestRejectsNonCanonicalOrReusedReviewerSessionID(t *testing.T) {
	t.Parallel()
	t.Run("arbitrary value", func(t *testing.T) {
		t.Parallel()
		fixture := newAttestationFixtureWithSessionID(t, "/root/post_work_reviewer")

		err := Attest(
			fixture.resultPath,
			fixture.cacheDir,
			fixture.sessionsRoot,
			testParentSessionID,
			testPreparedAt,
			fixture.agentConfigPath,
			"",
		)
		assertAttestationFailure(t, fixture.cacheDir, err, AttestationMismatch, "canonical UUID")
	})

	t.Run("same as parent", func(t *testing.T) {
		t.Parallel()
		fixture := newAttestationFixtureWithSessionID(t, testParentSessionID)

		err := Attest(
			fixture.resultPath,
			fixture.cacheDir,
			fixture.sessionsRoot,
			testParentSessionID,
			testPreparedAt,
			fixture.agentConfigPath,
			"",
		)
		assertAttestationFailure(t, fixture.cacheDir, err, AttestationMismatch, "equals the parent")
	})

	t.Run("already used", func(t *testing.T) {
		t.Parallel()
		fixture := newAttestationFixture(t)
		usedPath := filepath.Join(t.TempDir(), "used-sessions")
		if err := os.WriteFile(usedPath, []byte(testReviewSessionID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		err := Attest(
			fixture.resultPath,
			fixture.cacheDir,
			fixture.sessionsRoot,
			testParentSessionID,
			testPreparedAt,
			fixture.agentConfigPath,
			usedPath,
		)
		assertAttestationFailure(t, fixture.cacheDir, err, AttestationReused, "already used")
	})

	t.Run("used-session file missing", func(t *testing.T) {
		t.Parallel()
		fixture := newAttestationFixture(t)

		err := Attest(
			fixture.resultPath,
			fixture.cacheDir,
			fixture.sessionsRoot,
			testParentSessionID,
			testPreparedAt,
			fixture.agentConfigPath,
			filepath.Join(t.TempDir(), "missing"),
		)
		assertAttestationFailure(t, fixture.cacheDir, err, AttestationUnavailable, "used reviewer session IDs")
	})
}

func TestAttestRejectsMoreThanOneTerminalLF(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	fixture.writeRollout(defaultRolloutOptions(fixture.resultMessage))
	if err := os.WriteFile(fixture.resultPath, []byte(fixture.resultMessage+"\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		"",
	)
	assertAttestationFailure(t, fixture.cacheDir, err, AttestationMismatch, "does not match task_complete")
}

func TestAttestClassifiesUnavailableEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		arrange func(*attestationFixture)
		wantErr string
	}{
		{
			name:    "rollout not found",
			arrange: func(*attestationFixture) {},
			wantErr: "rollout not found",
		},
		{
			name: "ambiguous rollout",
			arrange: func(fixture *attestationFixture) {
				options := defaultRolloutOptions(fixture.resultMessage)
				fixture.writeRolloutAt(options, filepath.Join(fixture.sessionsRoot, "a"))
				fixture.writeRolloutAt(options, filepath.Join(fixture.sessionsRoot, "b"))
			},
			wantErr: "ambiguous",
		},
		{
			name: "malformed rollout",
			arrange: func(fixture *attestationFixture) {
				path := fixture.rolloutPath(filepath.Join(fixture.sessionsRoot, "bad"))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					fixture.t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{bad json\n"), 0o600); err != nil {
					fixture.t.Fatal(err)
				}
			},
			wantErr: "malformed rollout",
		},
		{
			name: "missing turn context",
			arrange: func(fixture *attestationFixture) {
				options := defaultRolloutOptions(fixture.resultMessage)
				options.includeTurnContext = false
				fixture.writeRollout(options)
			},
			wantErr: "no turn_context",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAttestationFixture(t)
			test.arrange(fixture)

			err := Attest(
				fixture.resultPath,
				fixture.cacheDir,
				fixture.sessionsRoot,
				testParentSessionID,
				testPreparedAt,
				fixture.agentConfigPath,
				"",
			)
			assertAttestationFailure(t, fixture.cacheDir, err, AttestationUnavailable, test.wantErr)
		})
	}
}

func TestAttestReadsRolloutRecordsLargerThanScannerLimit(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	options := defaultRolloutOptions(fixture.resultMessage)
	options.largeUnknownRecord = strings.Repeat("x", 256*1024)
	fixture.writeRollout(options)

	if err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		"",
	); err != nil {
		t.Fatalf("Attest() error = %v", err)
	}
}

func TestReadAgentConfigFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "duplicate",
			data: "name = \"post-work-reviewer\"\nname = \"post-work-reviewer\"\nmodel = \"m\"\nsandbox_mode = \"read-only\"\n",
			want: "duplicate top-level name",
		},
		{
			name: "missing",
			data: "name = \"post-work-reviewer\"\nmodel = \"m\"\n",
			want: "missing top-level sandbox_mode",
		},
		{
			name: "unquoted",
			data: "name = post-work-reviewer\nmodel = \"m\"\nsandbox_mode = \"read-only\"\n",
			want: "expected a basic quoted string",
		},
		{
			name: "unterminated multiline",
			data: "name = \"post-work-reviewer\"\nmodel = \"m\"\nsandbox_mode = \"read-only\"\ndeveloper_instructions = \"\"\"\nbody\n",
			want: "unterminated multiline string",
		},
		{
			name: "only nested expected key",
			data: "model = \"m\"\nsandbox_mode = \"read-only\"\n[nested]\nname = \"post-work-reviewer\"\n",
			want: "missing top-level name",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "agent.toml")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readAgentConfig(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readAgentConfig() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

type attestationFixture struct {
	t               *testing.T
	resultPath      string
	cacheDir        string
	sessionsRoot    string
	agentConfigPath string
	resultMessage   string
}

func newAttestationFixture(t *testing.T) *attestationFixture {
	t.Helper()
	return newAttestationFixtureWithSessionID(t, testReviewSessionID)
}

func newAttestationFixtureWithSessionID(t *testing.T, sessionID string) *attestationFixture {
	t.Helper()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	resultMessage := fmt.Sprintf(
		`{"backend":"bounded-isolated-reviewer","review_type":"broad","reviewer_agent":"post-work-reviewer","reviewer_provenance":"native-subagent-tool","reviewer_session_id":%q,"same_agent_review":false,"reviewer_isolated":true,"reviewer_sandbox_mode":"read-only","hooks_only_success":false,"finding_count":0,"findings":[]}`,
		sessionID,
	)
	resultPath := filepath.Join(dir, "review.json")
	// One final LF is the only serialization difference Attest permits.
	if err := os.WriteFile(resultPath, []byte(resultMessage+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentConfigPath := filepath.Join(dir, "agent.toml")
	agentConfig := `name = "post-work-reviewer"
model = "gpt-5.6-sol"
sandbox_mode = "read-only"
developer_instructions = """
name = "forged-inside-instructions"
Return JSON only.
"""
[ignored]
name = "also-not-top-level"
`
	if err := os.WriteFile(agentConfigPath, []byte(agentConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionsRoot := filepath.Join(dir, "sessions")
	if err := os.Mkdir(sessionsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return &attestationFixture{
		t:               t,
		resultPath:      resultPath,
		cacheDir:        cacheDir,
		sessionsRoot:    sessionsRoot,
		agentConfigPath: agentConfigPath,
		resultMessage:   resultMessage,
	}
}

type rolloutOptions struct {
	sessionID           string
	parentThreadID      string
	spawnParentThreadID string
	threadSource        string
	agentRole           any
	model               any
	sandboxMode         any
	createdAt           string
	includeTurnContext  bool
	extraTurnContexts   []rolloutTurnContext
	taskMessages        []any
	largeUnknownRecord  string
}

func defaultRolloutOptions(resultMessage string) rolloutOptions {
	return rolloutOptions{
		sessionID:           testReviewSessionID,
		parentThreadID:      testParentSessionID,
		spawnParentThreadID: testParentSessionID,
		threadSource:        "subagent",
		agentRole:           testReviewerRole,
		model:               testReviewerModel,
		sandboxMode:         "read-only",
		createdAt:           testCreatedAt,
		includeTurnContext:  true,
		taskMessages:        []any{resultMessage},
	}
}

func (fixture *attestationFixture) writeRollout(options rolloutOptions) {
	fixture.t.Helper()
	fixture.writeRolloutAt(options, filepath.Join(fixture.sessionsRoot, "2026", "07", "13"))
}

func (fixture *attestationFixture) writeRolloutAt(options rolloutOptions, dir string) {
	fixture.t.Helper()
	path := fixture.rolloutPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fixture.t.Fatal(err)
	}
	records := make([]map[string]any, 0, 4+len(options.extraTurnContexts)+len(options.taskMessages))
	records = append(records, map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id":               options.sessionID,
			"parent_thread_id": options.parentThreadID,
			"timestamp":        options.createdAt,
			"thread_source":    options.threadSource,
			"source": map[string]any{
				"subagent": map[string]any{
					"thread_spawn": map[string]any{
						"parent_thread_id": options.spawnParentThreadID,
						"agent_path":       "/root/post_work_reviewer",
						"agent_role":       options.agentRole,
					},
				},
			},
		},
	})
	if options.largeUnknownRecord != "" {
		records = append(records, map[string]any{
			"type":    "response_item",
			"payload": map[string]any{"text": options.largeUnknownRecord},
		})
	}
	if options.includeTurnContext {
		records = append(records, turnContextRecord(options.model, options.sandboxMode))
	}
	for _, context := range options.extraTurnContexts {
		records = append(records, turnContextRecord(context.model, context.sandboxMode))
	}
	for _, message := range options.taskMessages {
		records = append(records, map[string]any{
			"type": "event_msg",
			"payload": map[string]any{
				"type":               "task_complete",
				"last_agent_message": message,
			},
		})
	}
	var data strings.Builder
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			fixture.t.Fatal(err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(data.String()), 0o600); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *attestationFixture) rolloutPath(dir string) string {
	fixture.t.Helper()
	return filepath.Join(dir, "rollout-2026-07-13T17-13-25-"+testReviewSessionID+".jsonl")
}

func turnContextRecord(model, sandbox any) map[string]any {
	return map[string]any{
		"type": "turn_context",
		"payload": map[string]any{
			"model": model,
			"sandbox_policy": map[string]any{
				"type": sandbox,
			},
		},
	}
}

func assertAttestationFailure(
	t *testing.T,
	cacheDir string,
	err error,
	wantKind AttestationErrorKind,
	wantText string,
) {
	t.Helper()
	if err == nil {
		t.Fatal("Attest() unexpectedly succeeded")
	}
	var attestationErr *AttestationError
	if !errors.As(err, &attestationErr) {
		t.Fatalf("Attest() error = %T %v, want *AttestationError", err, err)
	}
	if attestationErr.Kind != wantKind {
		t.Errorf("AttestationError.Kind = %q, want %q", attestationErr.Kind, wantKind)
	}
	if !strings.Contains(err.Error(), wantText) {
		t.Errorf("Attest() error = %q, want containing %q", err, wantText)
	}
	if _, statErr := os.Stat(filepath.Join(cacheDir, attestationValidFile)); !os.IsNotExist(statErr) {
		t.Errorf("attestation_valid exists after failure: %v", statErr)
	}
	kind, readErr := os.ReadFile(filepath.Join(cacheDir, "attestation_error_kind"))
	if readErr != nil {
		t.Fatalf("ReadFile(attestation_error_kind) error = %v", readErr)
	}
	if string(kind) != string(wantKind) {
		t.Errorf("attestation_error_kind = %q, want %q", kind, wantKind)
	}
	detail, readErr := os.ReadFile(filepath.Join(cacheDir, "attestation_error"))
	if readErr != nil {
		t.Fatalf("ReadFile(attestation_error) error = %v", readErr)
	}
	if !strings.Contains(string(detail), wantText) {
		t.Errorf("attestation_error = %q, want containing %q", detail, wantText)
	}
}
