package reviewjson

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseControllerTurnContextAcceptsEffectiveReadOnlyPermissions(t *testing.T) {
	t.Parallel()
	record := testControllerPermissionRecord(t, map[string]any{
		"permission_profile": map[string]any{
			"type": "managed",
			"file_system": map[string]any{
				"type": "restricted",
				"entries": []any{
					map[string]any{
						"path": map[string]any{
							"type":  "special",
							"value": map[string]any{"kind": "root"},
						},
						"access": "read",
					},
				},
			},
			"network": "restricted",
		},
		"file_system_sandbox_policy": map[string]any{
			"kind": "restricted",
			"entries": []any{
				map[string]any{
					"path": map[string]any{
						"type":  "special",
						"value": map[string]any{"kind": "root"},
					},
					"access": "read",
				},
			},
		},
	})

	got, err := parseControllerTurnContext(record)
	if err != nil {
		t.Fatalf("parseControllerTurnContext() error = %v", err)
	}
	if got.SandboxMode != controllerReadOnlySandbox {
		t.Errorf("SandboxMode = %q, want %q", got.SandboxMode, controllerReadOnlySandbox)
	}
}

func TestParseControllerTurnContextRejectsEffectiveWritePermissions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{
			name: "permission profile worktree write",
			extra: map[string]any{
				"permission_profile": map[string]any{
					"type": "managed",
					"file_system": map[string]any{
						"type": "restricted",
						"entries": []any{
							map[string]any{
								"path": map[string]any{
									"type": "path",
									"path": "/worktree",
								},
								"access": "write",
							},
						},
					},
				},
			},
			want: "grants non-read-only access \"write\"",
		},
		{
			name: "file system sandbox worktree write",
			extra: map[string]any{
				"file_system_sandbox_policy": map[string]any{
					"kind": "restricted",
					"entries": []any{
						map[string]any{
							"path": map[string]any{
								"type": "path",
								"path": "/worktree",
							},
							"access": "write",
						},
					},
				},
			},
			want: "grants non-read-only access \"write\"",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := testControllerPermissionRecord(t, test.extra)

			_, err := parseControllerTurnContext(record)
			if err == nil {
				t.Fatal("parseControllerTurnContext() unexpectedly succeeded")
			}
			var attestationErr *AttestationError
			if !errors.As(err, &attestationErr) {
				t.Fatalf("error = %T %v, want *AttestationError", err, err)
			}
			if attestationErr.Kind != AttestationMismatch {
				t.Errorf("AttestationError.Kind = %q, want %q", attestationErr.Kind, AttestationMismatch)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseControllerTurnContextRejectsPresentUnknownPermissionShape(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"permission_profile", "file_system_sandbox_policy"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			record := testControllerPermissionRecord(t, map[string]any{field: nil})

			_, err := parseControllerTurnContext(record)
			if err == nil || !strings.Contains(err.Error(), field+" is null") {
				t.Fatalf("parseControllerTurnContext() error = %v, want null %s failure", err, field)
			}
		})
	}
}

func testControllerPermissionRecord(t *testing.T, extra map[string]any) rolloutRecord {
	t.Helper()
	payload := map[string]any{
		"turn_id": "019f5c42-734b-77d2-b935-0f8326bfd573",
		"sandbox_policy": map[string]any{
			"type": controllerReadOnlySandbox,
		},
	}
	for key, value := range extra {
		payload[key] = value
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return rolloutRecord{
		Timestamp: json.RawMessage(`"2026-07-13T17:13:25.250Z"`),
		Type:      "turn_context",
		Payload:   data,
	}
}
