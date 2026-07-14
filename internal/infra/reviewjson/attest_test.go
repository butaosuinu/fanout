package reviewjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testParentSessionID = "019f5c42-734b-77d2-b935-0f8326bfd572"
	testReviewSessionID = "019f5c78-2577-70f3-bc26-d6f83b2b5d72"
	testOtherSessionID  = "019f5c78-2577-70f3-bc26-d6f83b2b5d73"
	testPreparedAt      = "2026-07-13T17:13:25Z"
	testCreatedAt       = "2026-07-13T17:13:25.763Z"
	testReviewerRole    = "post-work-reviewer"
	testReviewerModel   = "gpt-5.6-sol"
	testReviewerEffort  = "xhigh"
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
		fixture.bundlePath,
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
		"attested_history_mode":           attestedNoHistoryMode,
		"attested_reviewer_spawn_calls":   "1",
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
	config := "name = \"post-work-verifier\"\nmodel = \"gpt-5.6-terra\"\nmodel_reasoning_effort = \"high\"\nsandbox_mode = \"read-only\"\n"
	if err := os.WriteFile(fixture.agentConfigPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	options := defaultRolloutOptions(fixture.resultMessage)
	options.agentRole = "post-work-verifier"
	options.model = "gpt-5.6-terra"
	options.reasoningEffort = "high"
	options.parentAgentType = "post-work-verifier"
	fixture.writeRollout(options)

	if err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
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

func TestAttestAcceptsV2NoHistorySpawnWithPlaintextChildTask(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	options := v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
	options.includeForkedFrom = true
	options.forkedFromID = nil
	// Current V2 rollouts may encrypt the parent function-call message. The
	// persisted plaintext child AgentMessage is the runtime-side path evidence.
	options.parentMessage = "encrypted-parent-payload"
	fixture.writeRollout(options)

	if err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
		"",
	); err != nil {
		t.Fatalf("Attest() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(fixture.cacheDir, "attested_history_mode"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != attestedNoHistoryMode {
		t.Fatalf("attested_history_mode = %q, want %q", got, attestedNoHistoryMode)
	}
}

func TestAttestRejectsUnattestedSpawnIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*rolloutOptions, *attestationFixture)
		kind    AttestationErrorKind
		wantErr string
	}{
		{
			name: "child records inherited history",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.includeForkedFrom = true
				options.forkedFromID = testParentSessionID
			},
			kind:    AttestationMismatch,
			wantErr: "forked_from_id",
		},
		{
			name: "V1 fork context true",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.parentForkContext = true
			},
			kind:    AttestationMismatch,
			wantErr: "fork_context",
		},
		{
			name: "V1 fork context omitted",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.includeForkContext = false
			},
			kind:    AttestationMismatch,
			wantErr: "fork_context is missing",
		},
		{
			name: "V1 parent message differs",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.parentMessage = "/tmp/another-bundle.md"
			},
			kind:    AttestationMismatch,
			wantErr: "V1 spawn message",
		},
		{
			name: "V1 parent namespace differs",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.parentNamespace = "collaboration"
			},
			kind:    AttestationMismatch,
			wantErr: "namespace",
		},
		{
			name: "V1 parent overrides reasoning effort",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.parentExtraArguments = map[string]any{"reasoning_effort": "low"}
			},
			kind:    AttestationMismatch,
			wantErr: "unsupported argument reasoning_effort",
		},
		{
			name: "V1 final path input differs",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.userInputs = [][]string{{"startup", "environment"}, {"/tmp/other.md"}}
			},
			kind:    AttestationMismatch,
			wantErr: "unique final path-only",
		},
		{
			name: "duplicate parent spawn mapping",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.duplicateParentSpawn = true
			},
			kind:    AttestationUnavailable,
			wantErr: "2 spawn outputs",
		},
		{
			name: "unrecorded custom role spawn still consumes broad budget",
			change: func(options *rolloutOptions, _ *attestationFixture) {
				options.unrecordedParentMessage = "/tmp/wrong-bundle.md"
			},
			kind:    AttestationMismatch,
			wantErr: "2 fresh native spawn calls",
		},
		{
			name: "V2 fork all",
			change: func(options *rolloutOptions, fixture *attestationFixture) {
				*options = v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
				options.parentForkTurns = "all"
			},
			kind:    AttestationMismatch,
			wantErr: "fork_turns is not none",
		},
		{
			name: "V2 fork turns omitted",
			change: func(options *rolloutOptions, fixture *attestationFixture) {
				*options = v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
				options.includeForkTurns = false
			},
			kind:    AttestationMismatch,
			wantErr: "fork_turns is missing",
		},
		{
			name: "V2 task name differs",
			change: func(options *rolloutOptions, fixture *attestationFixture) {
				*options = v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
				options.parentTaskName = "another_task"
			},
			kind:    AttestationMismatch,
			wantErr: "task_name",
		},
		{
			name: "V2 encrypted child input",
			change: func(options *rolloutOptions, fixture *attestationFixture) {
				*options = v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
				options.agentInputs = []testAgentInput{{
					author:    "/root",
					recipient: options.agentPath,
					texts:     []string{"Message Type: NEW_TASK\nPayload:\n"},
					encrypted: "gAAAA-runtime-ciphertext",
				}}
			},
			kind:    AttestationMismatch,
			wantErr: "encrypted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAttestationFixture(t)
			options := defaultRolloutOptions(fixture.resultMessage)
			test.change(&options, fixture)
			fixture.writeRollout(options)

			err := Attest(
				fixture.resultPath,
				fixture.cacheDir,
				fixture.sessionsRoot,
				testParentSessionID,
				testPreparedAt,
				fixture.agentConfigPath,
				fixture.bundlePath,
				"",
			)
			assertAttestationFailure(t, fixture.cacheDir, err, test.kind, test.wantErr)
		})
	}
}

func TestAttestRejectsContradictoryRolloutMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*rolloutOptions)
		kind    AttestationErrorKind
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
			name: "top-level role missing",
			change: func(options *rolloutOptions) {
				options.includeTopAgentRole = false
			},
			kind:    AttestationUnavailable,
			wantErr: "session_meta.agent_role is missing",
		},
		{
			name: "top-level role null",
			change: func(options *rolloutOptions) {
				options.overrideTopAgentRole = true
				options.topAgentRole = nil
			},
			wantErr: "session_meta.agent_role is null",
		},
		{
			name: "top-level role disagrees",
			change: func(options *rolloutOptions) {
				options.overrideTopAgentRole = true
				options.topAgentRole = "default"
			},
			wantErr: "agent_role fields disagree",
		},
		{
			name: "wrong model",
			change: func(options *rolloutOptions) {
				options.model = "gpt-5.6-terra"
			},
			wantErr: "model",
		},
		{
			name: "wrong reasoning effort",
			change: func(options *rolloutOptions) {
				options.reasoningEffort = "low"
			},
			wantErr: "reasoning effort",
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
			name: "session starts after legacy timestamp but before bundle write",
			change: func(options *rolloutOptions) {
				options.createdAt = "2026-07-13T17:13:25.050Z"
			},
			wantErr: "not created after prepare",
		},
		{
			name: "another turn uses another model",
			change: func(options *rolloutOptions) {
				options.extraTurnContexts = append(options.extraTurnContexts, rolloutTurnContext{
					model:           "gpt-5.6-terra",
					reasoningEffort: testReviewerEffort,
					sandboxMode:     "read-only",
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
				fixture.bundlePath,
				"",
			)
			wantKind := test.kind
			if wantKind == "" {
				wantKind = AttestationMismatch
			}
			assertAttestationFailure(t, fixture.cacheDir, err, wantKind, test.wantErr)
		})
	}
}

func TestAttestRejectsLegacyBundleMtimeWithoutPostPrepareEvidence(t *testing.T) {
	t.Parallel()
	preparedAt := time.Date(2026, 7, 13, 17, 13, 25, 0, time.UTC)
	for _, test := range []struct {
		name  string
		mtime time.Time
	}{
		{name: "equal", mtime: preparedAt},
		{name: "earlier", mtime: preparedAt.Add(-time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAttestationFixture(t)
			if err := os.Chtimes(fixture.bundlePath, test.mtime, test.mtime); err != nil {
				t.Fatal(err)
			}
			fixture.writeRollout(defaultRolloutOptions(fixture.resultMessage))

			err := Attest(
				fixture.resultPath,
				fixture.cacheDir,
				fixture.sessionsRoot,
				testParentSessionID,
				testPreparedAt,
				fixture.agentConfigPath,
				fixture.bundlePath,
				"",
			)
			assertAttestationFailure(
				t,
				fixture.cacheDir,
				err,
				AttestationUnavailable,
				"bundle mtime does not prove completion",
			)
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
			fixture.bundlePath,
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
			fixture.bundlePath,
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
			fixture.bundlePath,
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
			fixture.bundlePath,
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
		fixture.bundlePath,
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
				fixture.bundlePath,
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
		fixture.bundlePath,
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
			data: "name = \"post-work-reviewer\"\nname = \"post-work-reviewer\"\nmodel = \"m\"\nmodel_reasoning_effort = \"high\"\nsandbox_mode = \"read-only\"\n",
			want: "duplicate top-level name",
		},
		{
			name: "missing",
			data: "name = \"post-work-reviewer\"\nmodel = \"m\"\nmodel_reasoning_effort = \"high\"\n",
			want: "missing top-level sandbox_mode",
		},
		{
			name: "unquoted",
			data: "name = post-work-reviewer\nmodel = \"m\"\nmodel_reasoning_effort = \"high\"\nsandbox_mode = \"read-only\"\n",
			want: "expected a basic quoted string",
		},
		{
			name: "unterminated multiline",
			data: "name = \"post-work-reviewer\"\nmodel = \"m\"\nmodel_reasoning_effort = \"high\"\nsandbox_mode = \"read-only\"\ndeveloper_instructions = \"\"\"\nbody\n",
			want: "unterminated multiline string",
		},
		{
			name: "only nested expected key",
			data: "model = \"m\"\nmodel_reasoning_effort = \"high\"\nsandbox_mode = \"read-only\"\n[nested]\nname = \"post-work-reviewer\"\n",
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
	bundlePath      string
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
model_reasoning_effort = "xhigh"
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
	bundlePath := filepath.Join(dir, "review-bundle.md")
	if err := os.WriteFile(bundlePath, []byte("review bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleReadyAt := time.Date(2026, 7, 13, 17, 13, 25, 100_000_000, time.UTC)
	if err := os.Chtimes(bundlePath, bundleReadyAt, bundleReadyAt); err != nil {
		t.Fatal(err)
	}
	return &attestationFixture{
		t:               t,
		resultPath:      resultPath,
		cacheDir:        cacheDir,
		sessionsRoot:    sessionsRoot,
		agentConfigPath: agentConfigPath,
		bundlePath:      bundlePath,
		resultMessage:   resultMessage,
	}
}

type rolloutOptions struct {
	sessionID               string
	parentThreadID          string
	spawnParentThreadID     string
	threadSource            string
	agentRole               any
	includeTopAgentRole     bool
	overrideTopAgentRole    bool
	topAgentRole            any
	agentPath               string
	multiAgentVersion       string
	includeForkedFrom       bool
	forkedFromID            any
	model                   any
	reasoningEffort         any
	sandboxMode             any
	createdAt               string
	includeTurnContext      bool
	extraTurnContexts       []rolloutTurnContext
	taskMessages            []any
	userInputs              [][]string
	agentInputs             []testAgentInput
	parentMessage           any
	parentAgentType         any
	parentNamespace         string
	parentExtraArguments    map[string]any
	includeForkContext      bool
	parentForkContext       any
	includeForkTurns        bool
	parentForkTurns         any
	parentTaskName          any
	unrecordedParentMessage any
	duplicateParentSpawn    bool
	missingParentOutput     bool
	largeUnknownRecord      string
}

type testAgentInput struct {
	author    string
	recipient string
	texts     []string
	encrypted string
}

func defaultRolloutOptions(resultMessage string) rolloutOptions {
	return rolloutOptions{
		sessionID:           testReviewSessionID,
		parentThreadID:      testParentSessionID,
		spawnParentThreadID: testParentSessionID,
		threadSource:        "subagent",
		agentRole:           testReviewerRole,
		includeTopAgentRole: true,
		agentPath:           "/root/post_work_reviewer",
		multiAgentVersion:   "v1",
		model:               testReviewerModel,
		reasoningEffort:     testReviewerEffort,
		sandboxMode:         "read-only",
		createdAt:           testCreatedAt,
		includeTurnContext:  true,
		taskMessages:        []any{resultMessage},
		parentAgentType:     testReviewerRole,
		parentNamespace:     "multi_agent_v1",
		includeForkContext:  true,
		parentForkContext:   false,
		parentTaskName:      "post_work_reviewer",
	}
}

func v2RolloutOptions(resultMessage, bundlePath string) rolloutOptions {
	options := defaultRolloutOptions(resultMessage)
	options.multiAgentVersion = "v2"
	options.parentNamespace = "collaboration"
	options.includeForkContext = false
	options.includeForkTurns = true
	options.parentForkTurns = "none"
	options.userInputs = [][]string{{"startup AGENTS context", "startup environment context"}}
	options.agentInputs = []testAgentInput{{
		author:    "/root",
		recipient: options.agentPath,
		texts:     []string{bundlePath},
	}}
	return options
}

func (fixture *attestationFixture) writeRollout(options rolloutOptions) {
	fixture.t.Helper()
	fixture.writeRolloutAt(options, filepath.Join(fixture.sessionsRoot, "2026", "07", "13"))
	fixture.writeParentRollout(options)
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
			"id":                  options.sessionID,
			"parent_thread_id":    options.parentThreadID,
			"timestamp":           options.createdAt,
			"thread_source":       options.threadSource,
			"multi_agent_version": options.multiAgentVersion,
			"source": map[string]any{
				"subagent": map[string]any{
					"thread_spawn": map[string]any{
						"parent_thread_id": options.spawnParentThreadID,
						"agent_path":       options.agentPath,
						"agent_role":       options.agentRole,
					},
				},
			},
		},
	})
	sessionPayload := records[0]["payload"].(map[string]any)
	if options.includeTopAgentRole {
		topAgentRole := options.agentRole
		if options.overrideTopAgentRole {
			topAgentRole = options.topAgentRole
		}
		sessionPayload["agent_role"] = topAgentRole
	}
	if options.multiAgentVersion == "v2" {
		sessionPayload["agent_path"] = options.agentPath
	}
	if options.includeForkedFrom {
		sessionPayload["forked_from_id"] = options.forkedFromID
	}
	if options.largeUnknownRecord != "" {
		records = append(records, map[string]any{
			"type":    "response_item",
			"payload": map[string]any{"text": options.largeUnknownRecord},
		})
	}
	if options.includeTurnContext {
		records = append(records, turnContextRecord(
			options.model,
			options.reasoningEffort,
			options.sandboxMode,
		))
	}
	for _, context := range options.extraTurnContexts {
		records = append(records, turnContextRecord(
			context.model,
			context.reasoningEffort,
			context.sandboxMode,
		))
	}
	userInputs := options.userInputs
	if userInputs == nil {
		userInputs = [][]string{{"startup AGENTS context", "startup environment context"}, {fixture.bundlePath}}
	}
	for _, texts := range userInputs {
		content := make([]map[string]any, 0, len(texts))
		for _, text := range texts {
			content = append(content, map[string]any{"type": "input_text", "text": text})
		}
		records = append(records, map[string]any{
			"type": "response_item",
			"payload": map[string]any{
				"type":    "message",
				"role":    "user",
				"content": content,
			},
		})
	}
	for _, input := range options.agentInputs {
		content := make([]map[string]any, 0, len(input.texts)+1)
		for _, text := range input.texts {
			content = append(content, map[string]any{"type": "input_text", "text": text})
		}
		if input.encrypted != "" {
			content = append(content, map[string]any{
				"type":              "encrypted_content",
				"encrypted_content": input.encrypted,
			})
		}
		records = append(records, map[string]any{
			"type": "response_item",
			"payload": map[string]any{
				"type":      "agent_message",
				"author":    input.author,
				"recipient": input.recipient,
				"content":   content,
			},
		})
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

func (fixture *attestationFixture) writeParentRollout(options rolloutOptions) {
	fixture.t.Helper()
	dir := filepath.Join(fixture.sessionsRoot, "2026", "07", "13")
	path := fixture.parentRolloutPath(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fixture.t.Fatal(err)
	}
	message := options.parentMessage
	if message == nil {
		message = fixture.bundlePath
	}
	arguments := map[string]any{
		"message":    message,
		"agent_type": options.parentAgentType,
	}
	for key, value := range options.parentExtraArguments {
		arguments[key] = value
	}
	if options.multiAgentVersion == "v1" {
		if options.includeForkContext {
			arguments["fork_context"] = options.parentForkContext
		}
	} else {
		arguments["task_name"] = options.parentTaskName
		if options.includeForkTurns {
			arguments["fork_turns"] = options.parentForkTurns
		}
	}
	argumentData, err := json.Marshal(arguments)
	if err != nil {
		fixture.t.Fatal(err)
	}
	output := map[string]any{"agent_id": options.sessionID}
	if options.multiAgentVersion == "v2" {
		output = map[string]any{"task_name": options.agentPath}
	}
	outputData, err := json.Marshal(output)
	if err != nil {
		fixture.t.Fatal(err)
	}
	records := []map[string]any{
		{
			"timestamp": "2026-07-13T17:12:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":        testParentSessionID,
				"timestamp": "2026-07-13T17:12:00Z",
			},
		},
		{
			"timestamp": options.createdAt,
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "spawn_agent",
				"namespace": options.parentNamespace,
				"call_id":   "call_spawn_1",
				"arguments": string(argumentData),
			},
		},
	}
	if !options.missingParentOutput {
		records = append(records, map[string]any{
			"timestamp": options.createdAt,
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call_spawn_1",
				"output":  string(outputData),
			},
		})
	}
	if options.unrecordedParentMessage != nil {
		extraArguments := make(map[string]any, len(arguments))
		for key, value := range arguments {
			extraArguments[key] = value
		}
		extraArguments["message"] = options.unrecordedParentMessage
		extraArgumentData, err := json.Marshal(extraArguments)
		if err != nil {
			fixture.t.Fatal(err)
		}
		records = append(records, map[string]any{
			"timestamp": options.createdAt,
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "spawn_agent",
				"namespace": options.parentNamespace,
				"call_id":   "call_spawn_unrecorded",
				"arguments": string(extraArgumentData),
			},
		})
	}
	if options.duplicateParentSpawn {
		records = append(records,
			map[string]any{
				"timestamp": options.createdAt,
				"type":      "response_item",
				"payload": map[string]any{
					"type":      "function_call",
					"name":      "spawn_agent",
					"namespace": options.parentNamespace,
					"call_id":   "call_spawn_2",
					"arguments": string(argumentData),
				},
			},
			map[string]any{
				"timestamp": options.createdAt,
				"type":      "response_item",
				"payload": map[string]any{
					"type":    "function_call_output",
					"call_id": "call_spawn_2",
					"output":  string(outputData),
				},
			},
		)
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

func (fixture *attestationFixture) parentRolloutPath(dir string) string {
	fixture.t.Helper()
	return filepath.Join(dir, "rollout-2026-07-13T17-12-00-"+testParentSessionID+".jsonl")
}

func turnContextRecord(model, reasoningEffort, sandbox any) map[string]any {
	return map[string]any{
		"type": "turn_context",
		"payload": map[string]any{
			"model":  model,
			"effort": reasoningEffort,
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
