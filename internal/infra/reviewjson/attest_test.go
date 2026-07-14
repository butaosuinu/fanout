package reviewjson

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testParentSessionID       = "019f5c42-734b-77d2-b935-0f8326bfd572"
	testReviewSessionID       = "019f5c78-2577-70f3-bc26-d6f83b2b5d72"
	testOtherSessionID        = "019f5c78-2577-70f3-bc26-d6f83b2b5d73"
	testPreparedAt            = "2026-07-13T17:13:25Z"
	testCreatedAt             = "2026-07-13T17:13:25.763Z"
	testReviewerRole          = "post-work-reviewer"
	testReviewerModel         = "gpt-5.6-sol"
	testReviewerEffort        = "xhigh"
	testControllerTurnID      = "019f5c42-734b-77d2-b935-0f8326bfd573"
	testControllerContextAt   = "2026-07-13T17:13:25.250Z"
	testSpawnAuthorizedAt     = "2026-07-13T17:13:25.500Z"
	testAuthorizationCallAt   = "2026-07-13T17:13:25.400Z"
	testAuthorizationOutputAt = "2026-07-13T17:13:25.600Z"
	testSpawnCallAt           = "2026-07-13T17:13:25.700Z"
	testSpawnOutputAt         = "2026-07-13T17:13:25.900Z"
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
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
		"",
	); err != nil {
		t.Fatalf("Attest() error = %v", err)
	}

	want := map[string]string{
		"valid":                              "",
		"attestation_valid":                  "",
		"attestation_version":                AttestationVersion,
		"attested_session_id":                testReviewSessionID,
		"attested_parent_thread_id":          testParentSessionID,
		"attested_agent_role":                testReviewerRole,
		"attested_model":                     testReviewerModel,
		"attested_sandbox_mode":              "read-only",
		"attested_approval_policy":           "never",
		"attested_history_mode":              attestedNoHistoryMode,
		"attested_reviewer_spawn_calls":      "1",
		"attested_controller_turn_id":        testControllerTurnID,
		"attested_controller_context_sha256": testControllerContextSHA256(),
		"attested_controller_sandbox_mode":   controllerReadOnlySandbox,
		"attested_spawn_authorized_at":       testSpawnAuthorizedAt,
		"reviewer_session_id":                testReviewSessionID,
		"findings_count":                     "0",
		"finding_count":                      "0",
		"reviewer_sandbox_mode":              "read-only",
		"reviewer_agent":                     testReviewerRole,
		"reviewer_provenance":                "native-subagent-tool",
		"same_agent_review":                  "false",
		"reviewer_isolated":                  "true",
		"hooks_only_success":                 "false",
		"findings_missing_required_count":    "0",
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

func TestAttestControllerReturnsLatestReadOnlyTurnContext(t *testing.T) {
	t.Parallel()
	const (
		latestTurnID = "019f5c42-734b-77d2-b935-0f8326bfd574"
		latestAt     = "2026-07-13T17:13:25.400Z"
	)
	fixture := newAttestationFixture(t)
	options := defaultRolloutOptions(fixture.resultMessage)
	options.extraParentControllerContexts = []testControllerTurnContext{{
		timestamp:   latestAt,
		turnID:      latestTurnID,
		sandboxMode: controllerReadOnlySandbox,
	}}
	fixture.writeParentRollout(options)

	got, err := AttestController(fixture.sessionsRoot, testParentSessionID)
	if err != nil {
		t.Fatalf("AttestController() error = %v", err)
	}
	if got.TurnID != latestTurnID {
		t.Errorf("TurnID = %q, want %q", got.TurnID, latestTurnID)
	}
	if got.ContextSHA256 != controllerContextSHA256(
		latestAt,
		latestTurnID,
		controllerReadOnlySandbox,
	) {
		t.Errorf("ContextSHA256 = %q, want latest raw payload digest", got.ContextSHA256)
	}
	if got.SandboxMode != controllerReadOnlySandbox {
		t.Errorf("SandboxMode = %q, want read-only", got.SandboxMode)
	}
	wantTimestamp, err := time.Parse(time.RFC3339Nano, latestAt)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Timestamp.Equal(wantTimestamp) {
		t.Errorf("Timestamp = %s, want %s", got.Timestamp, wantTimestamp)
	}
}

func TestAttestControllerFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(*rolloutOptions)
		wantKind AttestationErrorKind
		want     string
	}{
		{
			name: "missing turn context",
			mutate: func(options *rolloutOptions) {
				options.includeParentTurnContext = false
			},
			wantKind: AttestationUnavailable,
			want:     "no controller turn_context",
		},
		{
			name: "noncanonical turn ID",
			mutate: func(options *rolloutOptions) {
				options.parentControllerTurnID = "not-a-uuid"
			},
			wantKind: AttestationMismatch,
			want:     "not a canonical UUID",
		},
		{
			name: "workspace write sandbox",
			mutate: func(options *rolloutOptions) {
				options.parentControllerSandboxMode = "workspace-write"
			},
			wantKind: AttestationMismatch,
			want:     "sandbox is not read-only",
		},
		{
			name: "invalid timestamp",
			mutate: func(options *rolloutOptions) {
				options.parentControllerContextAt = "not-rfc3339"
			},
			wantKind: AttestationUnavailable,
			want:     "timestamp is not RFC3339",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAttestationFixture(t)
			options := defaultRolloutOptions(fixture.resultMessage)
			test.mutate(&options)
			fixture.writeParentRollout(options)

			_, err := AttestController(fixture.sessionsRoot, testParentSessionID)
			if err == nil {
				t.Fatal("AttestController() unexpectedly succeeded")
			}
			var attestationErr *AttestationError
			if !errors.As(err, &attestationErr) {
				t.Fatalf("AttestController() error = %T %v, want *AttestationError", err, err)
			}
			if attestationErr.Kind != test.wantKind {
				t.Errorf("AttestationError.Kind = %q, want %q", attestationErr.Kind, test.wantKind)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("AttestController() error = %q, want containing %q", err, test.want)
			}
		})
	}
}

func TestAttestBindsSpawnToAuthorizedControllerTurn(t *testing.T) {
	t.Parallel()
	versions := []struct {
		name    string
		options func(*attestationFixture) rolloutOptions
	}{
		{
			name: "v1",
			options: func(fixture *attestationFixture) rolloutOptions {
				return defaultRolloutOptions(fixture.resultMessage)
			},
		},
		{
			name: "v2",
			options: func(fixture *attestationFixture) rolloutOptions {
				return v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
			},
		},
	}
	tests := []struct {
		name           string
		mutate         func(*rolloutOptions)
		expectedTurnID string
		expectedDigest func(rolloutOptions) string
		authorizedAt   string
		wantKind       AttestationErrorKind
		want           string
	}{
		{
			name: "missing spawn turn ID",
			mutate: func(options *rolloutOptions) {
				options.includeParentSpawnTurnID = false
			},
			wantKind: AttestationUnavailable,
			want:     "controller turn ID is missing",
		},
		{
			name: "spawn turn ID mismatch",
			mutate: func(options *rolloutOptions) {
				options.parentSpawnTurnID = testOtherSessionID
			},
			wantKind: AttestationMismatch,
			want:     "turn ID does not match authorization",
		},
		{
			name: "workspace write controller",
			mutate: func(options *rolloutOptions) {
				options.parentControllerSandboxMode = "workspace-write"
			},
			expectedDigest: func(options rolloutOptions) string {
				return controllerContextSHA256(
					options.parentControllerContextAt,
					options.parentControllerTurnID,
					options.parentControllerSandboxMode,
				)
			},
			wantKind: AttestationMismatch,
			want:     "controller sandbox is not read-only",
		},
		{
			name:         "authorization after spawn",
			authorizedAt: "2026-07-13T17:13:26Z",
			wantKind:     AttestationMismatch,
			want:         "not created after spawn authorization",
		},
		{
			name: "authorization call missing",
			mutate: func(options *rolloutOptions) {
				options.includeParentAuthorizationCall = false
			},
			wantKind: AttestationUnavailable,
			want:     "no tool invocation containing spawn authorization",
		},
		{
			name: "authorization output missing",
			mutate: func(options *rolloutOptions) {
				options.includeParentAuthorizationOutput = false
			},
			wantKind: AttestationUnavailable,
			want:     "no tool invocation containing spawn authorization",
		},
		{
			name: "multiple authorization intervals",
			mutate: func(options *rolloutOptions) {
				options.duplicateParentAuthorization = true
			},
			wantKind: AttestationUnavailable,
			want:     "2 tool invocations containing spawn authorization",
		},
		{
			name: "authorization call turn mismatch",
			mutate: func(options *rolloutOptions) {
				options.parentAuthorizationTurnID = testOtherSessionID
			},
			wantKind: AttestationMismatch,
			want:     "turn ID does not match the controller turn",
		},
		{
			name: "authorization output turn mismatch",
			mutate: func(options *rolloutOptions) {
				options.parentAuthorizationOutputTurnID = testOtherSessionID
			},
			wantKind: AttestationMismatch,
			want:     "turn ID does not match the controller turn",
		},
		{
			name: "intervening tool invocation",
			mutate: func(options *rolloutOptions) {
				options.includeInterveningParentTool = true
			},
			wantKind: AttestationMismatch,
			want:     "another tool invocation appears between",
		},
	}
	for _, version := range versions {
		version := version
		for _, test := range tests {
			test := test
			t.Run(version.name+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				fixture := newAttestationFixture(t)
				options := version.options(fixture)
				if test.mutate != nil {
					test.mutate(&options)
				}
				fixture.writeRollout(options)
				expectedTurnID := test.expectedTurnID
				if expectedTurnID == "" {
					expectedTurnID = testControllerTurnID
				}
				expectedDigest := testControllerContextSHA256()
				if test.expectedDigest != nil {
					expectedDigest = test.expectedDigest(options)
				}
				authorizedAt := test.authorizedAt
				if authorizedAt == "" {
					authorizedAt = testSpawnAuthorizedAt
				}

				err := Attest(
					fixture.resultPath,
					fixture.cacheDir,
					fixture.sessionsRoot,
					testParentSessionID,
					testPreparedAt,
					fixture.agentConfigPath,
					fixture.bundlePath,
					expectedTurnID,
					expectedDigest,
					authorizedAt,
					"",
				)
				assertAttestationFailure(
					t,
					fixture.cacheDir,
					err,
					test.wantKind,
					test.want,
				)
			})
		}
	}
}

func TestValidateSpawnAuthorizationOrderingUsesSpawnCallRecord(t *testing.T) {
	t.Parallel()
	parseTime := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	invocations := []parentToolInvocation{
		{
			callID:             "call_authorize",
			createdAt:          parseTime(testAuthorizationCallAt),
			turnID:             testControllerTurnID,
			recordNumber:       1,
			outputAt:           parseTime(testAuthorizationOutputAt),
			outputTurnID:       testControllerTurnID,
			outputRecordNumber: 3,
			hasOutput:          true,
		},
		{
			callID:       "call_spawn",
			createdAt:    parseTime(testCreatedAt),
			turnID:       testControllerTurnID,
			recordNumber: 4,
			// The spawn output is deliberately absent. Ordering is proven by
			// the invocation record; child binding validates the output later.
		},
	}
	err := validateSpawnAuthorizationOrdering(
		invocations,
		parentSpawnCall{callID: "call_spawn", recordNumber: 4},
		testControllerTurnID,
		parseTime(testSpawnAuthorizedAt),
	)
	if err != nil {
		t.Fatalf("validateSpawnAuthorizationOrdering() error = %v", err)
	}
}

func TestValidateSpawnAuthorizationOrderingNormalizesRolloutMilliseconds(t *testing.T) {
	t.Parallel()
	parseTime := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	invocations := []parentToolInvocation{
		{
			callID:             "call_authorize",
			createdAt:          parseTime("2026-07-13T17:13:25.500Z"),
			turnID:             testControllerTurnID,
			recordNumber:       1,
			outputAt:           parseTime("2026-07-13T17:13:25.500Z"),
			outputTurnID:       testControllerTurnID,
			outputRecordNumber: 2,
			hasOutput:          true,
		},
		{
			callID:       "call_spawn",
			createdAt:    parseTime(testSpawnCallAt),
			turnID:       testControllerTurnID,
			recordNumber: 3,
		},
	}

	err := validateSpawnAuthorizationOrdering(
		invocations,
		parentSpawnCall{callID: "call_spawn", recordNumber: 3},
		testControllerTurnID,
		parseTime("2026-07-13T17:13:25.500900000Z"),
	)
	if err != nil {
		t.Fatalf("validateSpawnAuthorizationOrdering() error = %v", err)
	}
}

func TestAttestRequiresChildSessionAfterMatchedParentSpawn(t *testing.T) {
	t.Parallel()
	versions := []struct {
		name    string
		options func(*attestationFixture) rolloutOptions
	}{
		{
			name: "v1",
			options: func(fixture *attestationFixture) rolloutOptions {
				return defaultRolloutOptions(fixture.resultMessage)
			},
		},
		{
			name: "v2",
			options: func(fixture *attestationFixture) rolloutOptions {
				return v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
			},
		},
	}
	tests := []struct {
		name      string
		createdAt string
		wantError bool
	}{
		{name: "after spawn", createdAt: testCreatedAt},
		{name: "before spawn", createdAt: "2026-07-13T17:13:25.650Z", wantError: true},
		{name: "equal to spawn", createdAt: testSpawnCallAt, wantError: true},
	}
	for _, version := range versions {
		version := version
		for _, test := range tests {
			test := test
			t.Run(version.name+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				fixture := newAttestationFixture(t)
				options := version.options(fixture)
				options.createdAt = test.createdAt
				fixture.writeRollout(options)

				err := Attest(
					fixture.resultPath,
					fixture.cacheDir,
					fixture.sessionsRoot,
					testParentSessionID,
					testPreparedAt,
					fixture.agentConfigPath,
					fixture.bundlePath,
					testControllerTurnID,
					testControllerContextSHA256(),
					testSpawnAuthorizedAt,
					"",
				)
				if !test.wantError {
					if err != nil {
						t.Fatalf("Attest() error = %v", err)
					}
					return
				}
				assertAttestationFailure(
					t,
					fixture.cacheDir,
					err,
					AttestationMismatch,
					"reviewer session was not created after the parent spawn",
				)
			})
		}
	}
}

func TestAttestAcceptsV1CodeModeSpawnWrapper(t *testing.T) {
	t.Parallel()
	for _, directText := range []bool{false, true} {
		directText := directText
		t.Run(fmt.Sprintf("direct_text_%t", directText), func(t *testing.T) {
			t.Parallel()
			fixture := newAttestationFixture(t)
			options := defaultRolloutOptions(fixture.resultMessage)
			options.parentCodeMode = true
			options.parentCodeModeDirectText = directText
			fixture.writeRollout(options)

			if err := Attest(
				fixture.resultPath,
				fixture.cacheDir,
				fixture.sessionsRoot,
				testParentSessionID,
				testPreparedAt,
				fixture.agentConfigPath,
				fixture.bundlePath,
				testControllerTurnID,
				testControllerContextSHA256(),
				testSpawnAuthorizedAt,
				"",
			); err != nil {
				t.Fatalf("Attest() error = %v", err)
			}
		})
	}
}

func TestAttestRejectsReviewerSandboxPermissionOverride(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "legacy function call",
			payload: map[string]any{
				"type":      "function_call",
				"name":      "exec_command",
				"arguments": `{"cmd":"make check","sandbox_permissions":"require_escalated"}`,
			},
		},
		{
			name: "code mode unquoted key",
			payload: map[string]any{
				"type": "custom_tool_call",
				"name": "exec",
				"input": `const r = await tools.exec_command({
  cmd: "make check",
  sandbox_permissions: "require_escalated"
}); text(r.output);`,
			},
		},
		{
			name: "code mode quoted key",
			payload: map[string]any{
				"type": "custom_tool_call",
				"name": "exec",
				"input": `const r = await tools.exec_command({
  cmd: "make check",
  "sandbox_permissions": "require_escalated"
}); text(r.output);`,
			},
		},
		{
			name: "template interpolation",
			payload: map[string]any{
				"type":  "custom_tool_call",
				"name":  "exec",
				"input": "const value = `${tools.exec_command({sandbox_permissions: \"require_escalated\"})}`; text(value);",
			},
		},
		{
			name: "division after a keyword-named property call",
			payload: map[string]any{
				"type": "custom_tool_call",
				"name": "exec",
				"input": `const o = {for: () => 1};
o.for() / tools.exec_command({
  cmd: "make check",
  sandbox_permissions: "require_escalated"
}) / 1;`,
			},
		},
		{
			name: "division after a keyword-named property",
			payload: map[string]any{
				"type": "custom_tool_call",
				"name": "exec",
				"input": `const o = {return: 1};
o.return / tools.exec_command({
  cmd: "make check",
  sandbox_permissions: "require_escalated"
}) / 1;`,
			},
		},
		{
			name: "division after a trailing-dot number",
			payload: map[string]any{
				"type": "custom_tool_call",
				"name": "exec",
				"input": `1. / tools.exec_command({
  cmd: "make check",
  sandbox_permissions: "require_escalated"
}) / 1;`,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAttestationFixture(t)
			options := defaultRolloutOptions(fixture.resultMessage)
			options.extraResponseItems = []map[string]any{test.payload}
			fixture.writeRollout(options)

			err := Attest(
				fixture.resultPath,
				fixture.cacheDir,
				fixture.sessionsRoot,
				testParentSessionID,
				testPreparedAt,
				fixture.agentConfigPath,
				fixture.bundlePath,
				testControllerTurnID,
				testControllerContextSHA256(),
				testSpawnAuthorizedAt,
				"",
			)
			assertAttestationFailure(
				t,
				fixture.cacheDir,
				err,
				AttestationMismatch,
				"sandbox permission override",
			)
		})
	}
}

func TestSandboxOverrideParserIgnoresNonExecutableText(t *testing.T) {
	t.Parallel()
	inputs := []string{
		`const r = await tools.exec_command({cmd: "rg 'sandbox_permissions require_escalated'"}); text(r.output);`,
		"// sandbox_permissions: \"require_escalated\"\nconst r = await tools.exec_command({cmd: \"git diff\"}); text(r.output);",
		"const query = `sandbox_permissions: \\\"require_escalated\\\"`;\nconst r = await tools.exec_command({cmd: query}); text(r.output);",
		`const marker = /sandbox_permissions/; const r = await tools.exec_command({cmd: "git diff"}); text(r.output);`,
		`const marker = /tools\.exec_command/; const r = await tools.exec_command({cmd: "git diff"}); text(r.output);`,
		`const matcher = () => { return /tools\.exec_command/; }; const r = await tools.exec_command({cmd: "git diff"}); text(r.output);`,
		`if (true) /tools\.exec_command/.test("tools.exec_command"); const r = await tools.exec_command({cmd: "git diff"}); text(r.output);`,
		`const sandbox_permissions = "documentation token"; const r = await tools.exec_command({cmd: "git diff"}); text(r.output);`,
		`const wd = "/tmp/worktree";
const p = "/tmp/review-bundle.md";
const rs = await Promise.all([
  tools.exec_command({cmd:` + "`stat -f '%HT %z %Sp' '${p}'`" + `,workdir:wd,yield_time_ms:10000,max_output_tokens:2000}),
  tools.exec_command({cmd:` + "`head -n 1 '${p}'`" + `,workdir:wd,yield_time_ms:10000,max_output_tokens:2000})
]);
rs.forEach((r,i)=>text(JSON.stringify({i,exit_code:r.exit_code,output:r.output})));`,
		`const rs = await Promise.all([tools.exec_command({cmd: "pwd"})]); text(rs[0].output);`,
		`const value = ((a, b) => b)(0, [0]);
const r = await tools.exec_command({cmd: "pwd"});
text(String(value[0]) + r.output);`,
	}
	for _, input := range inputs {
		payload, err := json.Marshal(map[string]any{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": input,
		})
		if err != nil {
			t.Fatal(err)
		}
		requested, err := parseSandboxOverrideRequest(payload)
		if err != nil {
			t.Fatalf("parseSandboxOverrideRequest() error = %v", err)
		}
		if requested {
			t.Fatalf("parseSandboxOverrideRequest() requested = true for %q", input)
		}
	}
}

func TestSandboxOverrideParserRejectsMalformedToolCalls(t *testing.T) {
	t.Parallel()
	tests := []map[string]any{
		{
			"type":      "function_call",
			"name":      "exec_command",
			"arguments": `{"cmd":`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const r = await tools.exec_command({cmd: "git diff"}); /*`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": "const value = `${tools.exec_command({cmd: \\\"git diff\\\"})`;",
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `const key = "sandbox_permissions";
const args = {cmd: "git diff"};
args[key] = "require_escalated";
const r = await tools.exec_command(args); text(r.output);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const r = await tools.exec_command({["sandbox_" + "permissions"]: "require_escalated", cmd: "git diff"}); text(r.output);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const r = await tools.exec_command({sandbox\u005fpermissions: "require_escalated", cmd: "git diff"}); text(r.output);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const invoke = tools.exec_command; const r = await invoke({cmd: "git diff"}); text(r.output);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const r = await to\u006fls.exec\u005fcommand({cmd: "git diff", sandbox_permissions: "require_escalated"}); text(r.output);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const r = await globalThis["to" + "ols"]["exec_" + "command"]({cmd: "git diff", sandbox_permissions: "require_escalated"}); text(r.output);`,
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `const r = await (() => {})["con" + "structor"]("return tools")()["exec_" + "command"]({
  cmd: "go test ./...",
  sandbox_permissions: "require_escalated"
}); text(r.output);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const r = await tools.apply_patch("*** Begin Patch"); text(r.output);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const r = await tools.exec_command({cmd: "git diff", toJSON: () => ({sandbox_permissions: "require_escalated"})}); text(r.output);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const r = await tools.exec_command({"__proto__": {sandbox_permissions: "require_escalated"}, cmd: "git diff"}); text(r.output);`,
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `const {"constructor": F} = () => {};
const t = F("return tools")();
const r = await t.exec_command({cmd: "go test ./...", sandbox_permissions: "require_escalated"});
text(r.output);`,
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `const {"constr\u0075ctor": F} = () => {};
const t = F("return tools")();
const r = await t.exec_command({cmd: "go test ./...", sandbox_permissions: "require_escalated"});
text(r.output);`,
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `const proto = Object.getPrototypeOf(() => {});
const F = Object.getOwnPropertyDescriptor(proto, "con" + "structor").value;
const t = F("return tools")();
const run = Object.getOwnPropertyDescriptor(t, "exec_" + "command").value;
const r = await run({cmd: "go test ./...", sandbox_permissions: "require_escalated"});
text(r.output);`,
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `const t = (() => {})?.["con" + "structor"]("return tools")();
const r = await t?.["exec_" + "command"]({cmd: "go test ./...", sandbox_permissions: "require_escalated"});
text(r.output);`,
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `let x = 0, foo = 1, bar = 1000;
const r = await tools.exec_command({
  cmd: x++ / foo ? "pwd" : "pwd",
  sandbox_permissions: "require_escalated",
  yield_time_ms: bar / 1
}); text(r.output);`,
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `class C {
  #for() { return 1; }
  static run(c) {
    return c.#for() / tools.exec_command({cmd: "true", sandbox_permissions: "require_escalated"}) / 1;
  }
}
C.run(new C);`,
		},
		{
			"type":  "custom_tool_call",
			"name":  "exec",
			"input": `const π = 1; π / tools.exec_command({cmd: "true", sandbox_permissions: "require_escalated"}) / 1;`,
		},
		{
			"type": "custom_tool_call",
			"name": "exec",
			"input": `const {safe, ["con" + "structor"]: F} = () => {};
const t = F("return tools")();
const run = t["exec_" + "command"];
const r = await run({cmd: "true", sandbox_permissions: "require_escalated"});
text(r.output);`,
		},
	}
	for _, test := range tests {
		payload, err := json.Marshal(test)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseSandboxOverrideRequest(payload); err == nil {
			t.Fatalf("parseSandboxOverrideRequest(%s) error = nil, want non-nil", payload)
		}
	}
}

func TestParseV1CodeModeSpawnWrapperRejectsNoncanonicalJavaScript(t *testing.T) {
	t.Parallel()
	const canonical = `const r = await tools.multi_agent_v1__spawn_agent({
  agent_type: "post-work-reviewer",
  message: "/tmp/review-bundle.md",
  fork_context: false
});
text(JSON.stringify(r));`
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "dynamic agent type",
			input: strings.Replace(canonical, `"post-work-reviewer"`, "agentType", 1),
		},
		{
			name:  "dynamic message",
			input: strings.Replace(canonical, `"/tmp/review-bundle.md"`, "bundlePath", 1),
		},
		{
			name:  "dynamic fork context",
			input: strings.Replace(canonical, "fork_context: false", "fork_context: inherited", 1),
		},
		{
			name:  "different projected variable",
			input: strings.Replace(canonical, "JSON.stringify(r)", "JSON.stringify(other)", 1),
		},
		{
			name:  "extra statement",
			input: canonical + "\nnotify(r);",
		},
		{
			name:  "unsupported argument",
			input: strings.Replace(canonical, "fork_context: false", "fork_context: false, task_name: \"review\"", 1),
		},
		{
			name:  "missing argument",
			input: strings.Replace(canonical, "  fork_context: false\n", "", 1),
		},
		{
			name:  "trailing comma",
			input: strings.Replace(canonical, "fork_context: false\n", "fork_context: false,\n", 1),
		},
		{
			name:  "single quoted literal",
			input: strings.Replace(canonical, `"post-work-reviewer"`, `'post-work-reviewer'`, 1),
		},
		{
			name: "escaped spawn identifier",
			input: `const r = await tools.multi_agent_v1__spawn_\u0061gent({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
});
text(JSON.stringify(r));`,
		},
		{
			name: "computed spawn access",
			input: `const r = await tools["multi_agent_v1__spawn_agent"]({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
});
text(JSON.stringify(r));`,
		},
		{
			name: "spawn hidden in division after an update",
			input: `let x = 0;
const ignored = x++ / await tools.multi_agent_v1__spawn_agent({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
}) / 1;
text(String(ignored));`,
		},
		{
			name: "spawn hidden in division after a keyword-named property",
			input: `const o = {return: 1};
const ignored = o.return / tools.multi_agent_v1__spawn_agent({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
}) / 1;
text(String(ignored));`,
		},
		{
			name: "spawn hidden in division after a trailing-dot number",
			input: `const ignored = 1. / tools.multi_agent_v1__spawn_agent({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
}) / 1;
text(String(ignored));`,
		},
		{
			name: "spawn hidden after a private identifier",
			input: `class C {
  #for() { return 1; }
  static run(c) {
    return c.#for() / tools.multi_agent_v1__spawn_agent({
      agent_type: "post-work-reviewer",
      message: "/tmp/other-bundle.md",
      fork_context: false
    }) / 1;
  }
}
text(String(C.run(new C)));`,
		},
		{
			name: "spawn hidden after a non-ASCII identifier",
			input: `const π = 1;
const ignored = π / tools.multi_agent_v1__spawn_agent({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
}) / 1;
text(String(ignored));`,
		},
		{
			name: "spawn hidden behind computed constructor access",
			input: `const F = (() => {})["con" + "structor"];
const t = F("return tools")();
const spawn = t["multi_agent_v1__spawn_" + "agent"];
const r = await spawn({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
});
text(JSON.stringify(r));`,
		},
		{
			name: "spawn hidden behind a quoted constructor key",
			input: `const {"constructor": F} = () => {};
const t = F("return tools")();
const spawn = t["multi_agent_v1__spawn_" + "agent"];
const r = await spawn({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
});
text(JSON.stringify(r));`,
		},
		{
			name: "spawn hidden behind a later computed object key",
			input: `const {safe, ["con" + "structor"]: F} = () => {};
const t = F("return tools")();
const spawn = t["multi_agent_v1__spawn_" + "agent"];
const r = await spawn({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
});
text(JSON.stringify(r));`,
		},
		{
			name: "spawn hidden behind optional computed access",
			input: `const F = (() => {})?.["constructor"];
const t = F("return tools")();
const spawn = t?.["multi_agent_v1__spawn_agent"];
const r = await spawn({
  agent_type: "post-work-reviewer",
  message: "/tmp/other-bundle.md",
  fork_context: false
});
text(JSON.stringify(r));`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, isSpawn, err := parseV1CodeModeSpawnWrapper(test.input)
			if !isSpawn {
				t.Fatal("parseV1CodeModeSpawnWrapper() did not identify the native spawn attempt")
			}
			if err == nil {
				t.Fatal("parseV1CodeModeSpawnWrapper() error = nil, want non-nil")
			}
		})
	}
}

func TestParseV1CodeModeSpawnWrapperIgnoresUnrelatedControllerTools(t *testing.T) {
	t.Parallel()
	inputs := []string{
		`const r = await tools.exec_command({cmd: "rg spawn_agent"}); text(JSON.stringify(r));`,
		`text(JSON.stringify(ALL_TOOLS.filter(x => /spawn_agent/i.test(x.name))));`,
		`const r = await tools.multi_agent_v1__wait_agent({agent_id: "reviewer"}); text(JSON.stringify(r));`,
		`const r = await tools.update_plan({plan: []}); text(JSON.stringify(r));`,
		`const rs = await Promise.all([tools.exec_command({cmd: "pwd"})]); text(rs[0].output);`,
		`const value = ((a, b) => b)(0, [0]); text(String(value[0]));`,
	}
	for _, input := range inputs {
		arguments, isSpawn, err := parseV1CodeModeSpawnWrapper(input)
		if err != nil {
			t.Fatalf("parseV1CodeModeSpawnWrapper(%q) error = %v", input, err)
		}
		if isSpawn || arguments != nil {
			t.Fatalf(
				"parseV1CodeModeSpawnWrapper(%q) = (%v, %t), want (nil, false)",
				input,
				arguments,
				isSpawn,
			)
		}
	}
}

func TestParseV1CodeModeSpawnOutputRejectsNoncanonicalBlocks(t *testing.T) {
	t.Parallel()
	block := func(text string) map[string]any {
		return map[string]any{"type": "input_text", "text": text}
	}
	status := block("Script completed\nWall time 0.2 seconds\nOutput:\n")
	tests := []struct {
		name   string
		output any
	}{
		{
			name: "extra block",
			output: []any{
				status,
				block(`{"agent_id":"` + testReviewSessionID + `","nickname":"Gibbs"}`),
				block(`{"agent_id":"` + testReviewSessionID + `"}`),
			},
		},
		{
			name: "unknown result field",
			output: []any{
				status,
				block(`{"agent_id":"` + testReviewSessionID + `","task_name":"review"}`),
			},
		},
		{
			name: "duplicate result field",
			output: []any{
				status,
				block(`{"agent_id":"` + testOtherSessionID + `","agent_id":"` + testReviewSessionID + `"}`),
			},
		},
		{
			name: "noncanonical status",
			output: []any{
				block("Script completed\nOutput:\n"),
				block(`{"agent_id":"` + testReviewSessionID + `"}`),
			},
		},
		{
			name: "non-input-text result",
			output: []any{
				status,
				map[string]any{"type": "output_text", "text": `{"agent_id":"` + testReviewSessionID + `"}`},
			},
		},
		{
			name: "extra block field",
			output: []any{
				status,
				map[string]any{
					"type": "input_text", "text": `{"agent_id":"` + testReviewSessionID + `"}`, "extra": true,
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(test.output)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseV1CodeModeSpawnOutput(data); err == nil {
				t.Fatal("parseV1CodeModeSpawnOutput() error = nil, want non-nil")
			}
		})
	}
}

func TestAttestAcceptsVerifierAgentConfig(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	config := "name = \"post-work-verifier\"\nmodel = \"gpt-5.6-terra\"\nmodel_reasoning_effort = \"high\"\nsandbox_mode = \"read-only\"\napproval_policy = \"never\"\n"
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
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
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

func TestAttestRejectsAgentConfigThatCanApproveEscalation(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	config, err := os.ReadFile(fixture.agentConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	config = bytes.ReplaceAll(
		config,
		[]byte(`approval_policy = "never"`),
		[]byte(`approval_policy = "on-request"`),
	)
	if writeErr := os.WriteFile(fixture.agentConfigPath, config, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	err = Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
		"",
	)
	assertAttestationFailure(
		t,
		fixture.cacheDir,
		err,
		AttestationUnavailable,
		"approval_policy is not never",
	)
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
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
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

func TestAttestAcceptsUniqueLegacyV2OutputWithoutStartedActivity(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	options := v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
	options.includeParentStartedActivity = false
	fixture.writeRollout(options)

	if err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
		"",
	); err != nil {
		t.Fatalf("Attest() error = %v", err)
	}
}

func TestAttestBindsRepeatedV2TaskNameToStartedChildSession(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	config := "name = \"post-work-verifier\"\nmodel = \"gpt-5.6-terra\"\nmodel_reasoning_effort = \"high\"\nsandbox_mode = \"read-only\"\napproval_policy = \"never\"\n"
	if err := os.WriteFile(fixture.agentConfigPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	const taskName = "post_work_review_verify"
	first := v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
	first.agentRole = "post-work-verifier"
	first.model = "gpt-5.6-terra"
	first.reasoningEffort = "high"
	first.parentAgentType = "post-work-verifier"
	first.parentTaskName = taskName
	first.agentPath = "/root/" + taskName
	first.agentInputs[0].recipient = first.agentPath
	first.extraParentSpawns = []testParentSpawn{{
		callID:                 "call_spawn_2",
		sessionID:              testOtherSessionID,
		agentPath:              first.agentPath,
		taskName:               taskName,
		createdAt:              "2026-07-13T17:13:26.700Z",
		includeStartedActivity: true,
		authorizationCallAt:    "2026-07-13T17:13:26.400Z",
		authorizationOutputAt:  "2026-07-13T17:13:26.600Z",
	}}

	secondResult := strings.ReplaceAll(
		fixture.resultMessage,
		testReviewSessionID,
		testOtherSessionID,
	)
	second := first
	second.sessionID = testOtherSessionID
	second.rolloutFileSessionID = testOtherSessionID
	second.createdAt = "2026-07-13T17:13:26.763Z"
	second.taskMessages = []any{secondResult}
	second.extraParentSpawns = nil

	dir := filepath.Join(fixture.sessionsRoot, "2026", "07", "13")
	fixture.writeRolloutAt(first, dir)
	fixture.writeRolloutAt(second, dir)
	fixture.writeParentRollout(first)

	if err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
		"",
	); err != nil {
		t.Fatalf("Attest(first) error = %v", err)
	}

	secondResultPath := filepath.Join(filepath.Dir(fixture.resultPath), "review-second.json")
	if err := os.WriteFile(secondResultPath, []byte(secondResult+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondCacheDir := filepath.Join(filepath.Dir(fixture.cacheDir), "cache-second")
	if err := os.Mkdir(secondCacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Attest(
		secondResultPath,
		secondCacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
		testControllerTurnID,
		testControllerContextSHA256(),
		"2026-07-13T17:13:26.500Z",
		"",
	); err != nil {
		t.Fatalf("Attest(second) error = %v", err)
	}

	for name, cacheDir := range map[string]string{
		"first":  fixture.cacheDir,
		"second": secondCacheDir,
	} {
		got, err := os.ReadFile(filepath.Join(cacheDir, "attested_reviewer_spawn_calls"))
		if err != nil {
			t.Fatalf("ReadFile(%s spawn count) error = %v", name, err)
		}
		if string(got) != "2" {
			t.Errorf("%s attested_reviewer_spawn_calls = %q, want 2", name, got)
		}
	}
}

func TestAttestRejectsRepeatedV2TaskNameWithoutStartedActivity(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	options := v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
	options.includeParentStartedActivity = false
	options.extraParentSpawns = []testParentSpawn{{
		callID:    "call_spawn_2",
		sessionID: testOtherSessionID,
		agentPath: options.agentPath,
		taskName:  options.parentTaskName.(string),
		createdAt: "2026-07-13T17:13:26.763Z",
	}}
	fixture.writeRollout(options)

	err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
		"",
	)
	assertAttestationFailure(
		t,
		fixture.cacheDir,
		err,
		AttestationUnavailable,
		"2 spawn outputs corresponding to the child",
	)
}

func TestAttestRejectsV2CandidateWhenChildActivityBelongsToOutputlessCall(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	options := v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
	options.includeParentStartedActivity = false
	options.extraParentSpawns = []testParentSpawn{{
		callID:                 "call_spawn_2",
		sessionID:              testReviewSessionID,
		agentPath:              options.agentPath,
		taskName:               options.parentTaskName.(string),
		createdAt:              "2026-07-13T17:13:26.763Z",
		includeStartedActivity: true,
		missingOutput:          true,
	}}
	fixture.writeRollout(options)

	err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
		"",
	)
	assertAttestationFailure(
		t,
		fixture.cacheDir,
		err,
		AttestationMismatch,
		"activity does not match the child session",
	)
}

func TestAttestRejectsDuplicateStartedActivityForSpawnCall(t *testing.T) {
	t.Parallel()
	fixture := newAttestationFixture(t)
	options := v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
	options.duplicateParentStartedActivity = true
	fixture.writeRollout(options)

	err := Attest(
		fixture.resultPath,
		fixture.cacheDir,
		fixture.sessionsRoot,
		testParentSessionID,
		testPreparedAt,
		fixture.agentConfigPath,
		fixture.bundlePath,
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
		"",
	)
	assertAttestationFailure(
		t,
		fixture.cacheDir,
		err,
		AttestationUnavailable,
		"duplicate started activity",
	)
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
			name: "V2 started activity session differs",
			change: func(options *rolloutOptions, fixture *attestationFixture) {
				*options = v2RolloutOptions(fixture.resultMessage, fixture.bundlePath)
				options.parentStartedSessionID = testOtherSessionID
			},
			kind:    AttestationMismatch,
			wantErr: "activity does not match the child session",
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
				testControllerTurnID,
				testControllerContextSHA256(),
				testSpawnAuthorizedAt,
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
			name: "approval policy permits escalation",
			change: func(options *rolloutOptions) {
				options.approvalPolicy = "on-request"
			},
			wantErr: "approval policy",
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
					approvalPolicy:  "never",
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
				testControllerTurnID,
				testControllerContextSHA256(),
				testSpawnAuthorizedAt,
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
				testControllerTurnID,
				testControllerContextSHA256(),
				testSpawnAuthorizedAt,
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
			testControllerTurnID,
			testControllerContextSHA256(),
			testSpawnAuthorizedAt,
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
			testControllerTurnID,
			testControllerContextSHA256(),
			testSpawnAuthorizedAt,
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
			testControllerTurnID,
			testControllerContextSHA256(),
			testSpawnAuthorizedAt,
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
			testControllerTurnID,
			testControllerContextSHA256(),
			testSpawnAuthorizedAt,
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
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
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
				testControllerTurnID,
				testControllerContextSHA256(),
				testSpawnAuthorizedAt,
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
		testControllerTurnID,
		testControllerContextSHA256(),
		testSpawnAuthorizedAt,
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
approval_policy = "never"
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
	sessionID                        string
	parentThreadID                   string
	spawnParentThreadID              string
	threadSource                     string
	agentRole                        any
	includeTopAgentRole              bool
	overrideTopAgentRole             bool
	topAgentRole                     any
	agentPath                        string
	multiAgentVersion                string
	includeForkedFrom                bool
	forkedFromID                     any
	model                            any
	reasoningEffort                  any
	sandboxMode                      any
	approvalPolicy                   any
	createdAt                        string
	includeTurnContext               bool
	extraTurnContexts                []rolloutTurnContext
	taskMessages                     []any
	userInputs                       [][]string
	agentInputs                      []testAgentInput
	parentMessage                    any
	parentAgentType                  any
	parentNamespace                  string
	parentCodeMode                   bool
	parentCodeModeDirectText         bool
	includeParentTurnContext         bool
	parentControllerTurnID           any
	parentControllerSandboxMode      any
	parentControllerContextAt        string
	extraParentControllerContexts    []testControllerTurnContext
	includeParentSpawnTurnID         bool
	parentSpawnTurnID                any
	includeParentAuthorizationCall   bool
	includeParentAuthorizationOutput bool
	parentAuthorizationCallAt        string
	parentAuthorizationOutputAt      string
	parentAuthorizationTurnID        any
	parentAuthorizationOutputTurnID  any
	parentSpawnCallAt                string
	parentSpawnOutputAt              string
	duplicateParentAuthorization     bool
	includeInterveningParentTool     bool
	interveningParentToolTurnID      any
	extraResponseItems               []map[string]any
	parentExtraArguments             map[string]any
	includeForkContext               bool
	parentForkContext                any
	includeForkTurns                 bool
	parentForkTurns                  any
	parentTaskName                   any
	unrecordedParentMessage          any
	duplicateParentSpawn             bool
	missingParentOutput              bool
	largeUnknownRecord               string
	rolloutFileSessionID             string
	includeParentStartedActivity     bool
	duplicateParentStartedActivity   bool
	parentStartedSessionID           string
	extraParentSpawns                []testParentSpawn
}

type testControllerTurnContext struct {
	timestamp   string
	turnID      any
	sandboxMode any
}

type testParentSpawn struct {
	callID                 string
	sessionID              string
	agentPath              string
	taskName               string
	createdAt              string
	includeStartedActivity bool
	missingOutput          bool
	authorizationCallAt    string
	authorizationOutputAt  string
}

type testAgentInput struct {
	author    string
	recipient string
	texts     []string
	encrypted string
}

func defaultRolloutOptions(resultMessage string) rolloutOptions {
	return rolloutOptions{
		sessionID:                        testReviewSessionID,
		parentThreadID:                   testParentSessionID,
		spawnParentThreadID:              testParentSessionID,
		threadSource:                     "subagent",
		agentRole:                        testReviewerRole,
		includeTopAgentRole:              true,
		agentPath:                        "/root/post_work_reviewer",
		multiAgentVersion:                "v1",
		model:                            testReviewerModel,
		reasoningEffort:                  testReviewerEffort,
		sandboxMode:                      "read-only",
		approvalPolicy:                   "never",
		createdAt:                        testCreatedAt,
		includeTurnContext:               true,
		taskMessages:                     []any{resultMessage},
		parentAgentType:                  testReviewerRole,
		parentNamespace:                  "multi_agent_v1",
		includeParentTurnContext:         true,
		parentControllerTurnID:           testControllerTurnID,
		parentControllerSandboxMode:      controllerReadOnlySandbox,
		parentControllerContextAt:        testControllerContextAt,
		includeParentSpawnTurnID:         true,
		parentSpawnTurnID:                testControllerTurnID,
		includeParentAuthorizationCall:   true,
		includeParentAuthorizationOutput: true,
		parentAuthorizationCallAt:        testAuthorizationCallAt,
		parentAuthorizationOutputAt:      testAuthorizationOutputAt,
		parentAuthorizationTurnID:        testControllerTurnID,
		parentAuthorizationOutputTurnID:  testControllerTurnID,
		parentSpawnCallAt:                testSpawnCallAt,
		parentSpawnOutputAt:              testSpawnOutputAt,
		interveningParentToolTurnID:      testControllerTurnID,
		includeForkContext:               true,
		parentForkContext:                false,
		parentTaskName:                   "post_work_reviewer",
	}
}

func v2RolloutOptions(resultMessage, bundlePath string) rolloutOptions {
	options := defaultRolloutOptions(resultMessage)
	options.multiAgentVersion = "v2"
	options.parentNamespace = "collaboration"
	options.includeForkContext = false
	options.includeForkTurns = true
	options.parentForkTurns = "none"
	options.includeParentStartedActivity = true
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
	fileSessionID := testReviewSessionID
	if options.rolloutFileSessionID != "" {
		fileSessionID = options.rolloutFileSessionID
	}
	path := fixture.rolloutPathForSession(dir, fileSessionID)
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
			options.approvalPolicy,
		))
	}
	for _, context := range options.extraTurnContexts {
		records = append(records, turnContextRecord(
			context.model,
			context.reasoningEffort,
			context.sandboxMode,
			context.approvalPolicy,
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
	for _, payload := range options.extraResponseItems {
		records = append(records, map[string]any{
			"type":    "response_item",
			"payload": payload,
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
	maps.Copy(arguments, options.parentExtraArguments)
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
	spawnPayload := map[string]any{
		"type":      "function_call",
		"name":      "spawn_agent",
		"namespace": options.parentNamespace,
		"call_id":   "call_spawn_1",
		"arguments": string(argumentData),
	}
	spawnOutputPayload := map[string]any{
		"type":    "function_call_output",
		"call_id": "call_spawn_1",
		"output":  string(outputData),
	}
	if options.parentCodeMode {
		agentTypeData, err := json.Marshal(arguments["agent_type"])
		if err != nil {
			fixture.t.Fatal(err)
		}
		messageData, err := json.Marshal(arguments["message"])
		if err != nil {
			fixture.t.Fatal(err)
		}
		forkContextData, err := json.Marshal(arguments["fork_context"])
		if err != nil {
			fixture.t.Fatal(err)
		}
		projection := "JSON.stringify(r)"
		if options.parentCodeModeDirectText {
			projection = "r"
		}
		input := fmt.Sprintf(`
const r = await tools.multi_agent_v1__spawn_agent({
  agent_type: %s,
  message: %s,
  fork_context: %s
});
text(%s);
`, agentTypeData, messageData, forkContextData, projection)
		spawnPayload = map[string]any{
			"type":    "custom_tool_call",
			"name":    "exec",
			"call_id": "call_spawn_1",
			"input":   input,
		}
		spawnOutputPayload = map[string]any{
			"type":    "custom_tool_call_output",
			"call_id": "call_spawn_1",
			"output": []map[string]any{
				{
					"type": "input_text",
					"text": "Script completed\nWall time 0.2 seconds\nOutput:\n",
				},
				{
					"type": "input_text",
					"text": string(outputData),
				},
			},
		}
	}
	if options.includeParentSpawnTurnID {
		spawnPayload["internal_chat_message_metadata_passthrough"] = map[string]any{
			"turn_id": options.parentSpawnTurnID,
		}
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
	}
	if options.includeParentTurnContext {
		records = append(records, controllerTurnContextRecord(
			options.parentControllerContextAt,
			options.parentControllerTurnID,
			options.parentControllerSandboxMode,
		))
	}
	for _, context := range options.extraParentControllerContexts {
		records = append(records, controllerTurnContextRecord(
			context.timestamp,
			context.turnID,
			context.sandboxMode,
		))
	}
	if options.includeParentAuthorizationCall {
		records = append(records, parentToolInvocationRecord(
			"call_authorize_1",
			options.parentAuthorizationCallAt,
			options.parentAuthorizationTurnID,
		))
	}
	if options.duplicateParentAuthorization {
		records = append(records, parentToolInvocationRecord(
			"call_authorize_2",
			"2026-07-13T17:13:25.450Z",
			options.parentAuthorizationTurnID,
		))
	}
	if options.includeParentAuthorizationOutput {
		records = append(records, parentToolOutputRecord(
			"call_authorize_1",
			options.parentAuthorizationOutputAt,
			options.parentAuthorizationOutputTurnID,
		))
	}
	if options.duplicateParentAuthorization {
		records = append(records, parentToolOutputRecord(
			"call_authorize_2",
			"2026-07-13T17:13:25.650Z",
			options.parentAuthorizationOutputTurnID,
		))
	}
	if options.includeInterveningParentTool {
		records = append(records,
			parentToolInvocationRecord(
				"call_intervening",
				"2026-07-13T17:13:25.675Z",
				options.interveningParentToolTurnID,
			),
			parentToolOutputRecord(
				"call_intervening",
				"2026-07-13T17:13:25.700Z",
				options.interveningParentToolTurnID,
			),
		)
	}
	records = append(records, map[string]any{
		"timestamp": options.parentSpawnCallAt,
		"type":      "response_item",
		"payload":   spawnPayload,
	})
	if options.includeParentStartedActivity {
		sessionID := options.sessionID
		if options.parentStartedSessionID != "" {
			sessionID = options.parentStartedSessionID
		}
		records = append(records, parentStartedActivityRecord(
			"call_spawn_1",
			sessionID,
			options.agentPath,
			options.createdAt,
		))
		if options.duplicateParentStartedActivity {
			records = append(records, parentStartedActivityRecord(
				"call_spawn_1",
				sessionID,
				options.agentPath,
				options.createdAt,
			))
		}
	}
	if !options.missingParentOutput {
		records = append(records, map[string]any{
			"timestamp": options.parentSpawnOutputAt,
			"type":      "response_item",
			"payload":   spawnOutputPayload,
		})
	}
	if options.unrecordedParentMessage != nil {
		extraArguments := make(map[string]any, len(arguments))
		maps.Copy(extraArguments, arguments)
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
	for _, extra := range options.extraParentSpawns {
		if extra.authorizationCallAt != "" {
			records = append(records, parentToolInvocationRecord(
				"call_authorize_"+extra.callID,
				extra.authorizationCallAt,
				options.parentSpawnTurnID,
			))
		}
		if extra.authorizationOutputAt != "" {
			records = append(records, parentToolOutputRecord(
				"call_authorize_"+extra.callID,
				extra.authorizationOutputAt,
				options.parentSpawnTurnID,
			))
		}
		extraArguments := make(map[string]any, len(arguments))
		maps.Copy(extraArguments, arguments)
		extraArguments["task_name"] = extra.taskName
		extraArgumentData, err := json.Marshal(extraArguments)
		if err != nil {
			fixture.t.Fatal(err)
		}
		extraOutputData, err := json.Marshal(map[string]any{"task_name": extra.agentPath})
		if err != nil {
			fixture.t.Fatal(err)
		}
		records = append(records, map[string]any{
			"timestamp": extra.createdAt,
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "spawn_agent",
				"namespace": options.parentNamespace,
				"call_id":   extra.callID,
				"arguments": string(extraArgumentData),
				"internal_chat_message_metadata_passthrough": map[string]any{
					"turn_id": options.parentSpawnTurnID,
				},
			},
		})
		if extra.includeStartedActivity {
			records = append(records, parentStartedActivityRecord(
				extra.callID,
				extra.sessionID,
				extra.agentPath,
				extra.createdAt,
			))
		}
		if !extra.missingOutput {
			records = append(records, map[string]any{
				"timestamp": extra.createdAt,
				"type":      "response_item",
				"payload": map[string]any{
					"type":    "function_call_output",
					"call_id": extra.callID,
					"output":  string(extraOutputData),
				},
			})
		}
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
	return fixture.rolloutPathForSession(dir, testReviewSessionID)
}

func (fixture *attestationFixture) rolloutPathForSession(dir, sessionID string) string {
	fixture.t.Helper()
	return filepath.Join(dir, "rollout-2026-07-13T17-13-25-"+sessionID+".jsonl")
}

func (fixture *attestationFixture) parentRolloutPath(dir string) string {
	fixture.t.Helper()
	return filepath.Join(dir, "rollout-2026-07-13T17-12-00-"+testParentSessionID+".jsonl")
}

func turnContextRecord(model, reasoningEffort, sandbox, approvalPolicy any) map[string]any {
	return map[string]any{
		"type": "turn_context",
		"payload": map[string]any{
			"model":           model,
			"effort":          reasoningEffort,
			"approval_policy": approvalPolicy,
			"sandbox_policy": map[string]any{
				"type": sandbox,
			},
		},
	}
}

func controllerTurnContextRecord(timestamp string, turnID, sandbox any) map[string]any {
	return map[string]any{
		"timestamp": timestamp,
		"type":      "turn_context",
		"payload": map[string]any{
			"turn_id": turnID,
			"sandbox_policy": map[string]any{
				"type": sandbox,
			},
		},
	}
}

func parentToolInvocationRecord(callID, timestamp string, turnID any) map[string]any {
	return map[string]any{
		"timestamp": timestamp,
		"type":      "response_item",
		"payload": map[string]any{
			"type":      "function_call",
			"name":      "exec_command",
			"namespace": "functions",
			"call_id":   callID,
			"arguments": "{}",
			"internal_chat_message_metadata_passthrough": map[string]any{
				"turn_id": turnID,
			},
		},
	}
}

func parentToolOutputRecord(callID, timestamp string, turnID any) map[string]any {
	return map[string]any{
		"timestamp": timestamp,
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  "{}",
			"internal_chat_message_metadata_passthrough": map[string]any{
				"turn_id": turnID,
			},
		},
	}
}

func testControllerContextSHA256() string {
	return controllerContextSHA256(
		testControllerContextAt,
		testControllerTurnID,
		controllerReadOnlySandbox,
	)
}

func controllerContextSHA256(timestamp string, turnID, sandbox any) string {
	record := controllerTurnContextRecord(
		timestamp,
		turnID,
		sandbox,
	)
	payload, err := json.Marshal(record["payload"])
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest)
}

func parentStartedActivityRecord(callID, sessionID, agentPath, timestamp string) map[string]any {
	return map[string]any{
		"timestamp": timestamp,
		"type":      "event_msg",
		"payload": map[string]any{
			"type":            "sub_agent_activity",
			"event_id":        callID,
			"agent_thread_id": sessionID,
			"agent_path":      agentPath,
			"kind":            "started",
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
