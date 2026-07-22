package herdrrun

import (
	"strings"
	"testing"
)

func TestParseAdmittedVersionFloorAndStableSemver(t *testing.T) {
	for _, version := range []string{"0.7.5", "0.7.6", "0.10.0", "1.0.0", "0.7.5+build.1"} {
		got, err := parseAdmittedVersion([]byte("herdr " + version + "\n"))
		if err != nil || got != version {
			t.Errorf("parseAdmittedVersion(%q) = %q, %v", version, got, err)
		}
	}
	for _, version := range []string{"0.7.4", "0.7.5-preview.1", "0.7", "v0.7.5", "00.7.5", "0.7.5 other"} {
		if _, err := parseAdmittedVersion([]byte("herdr " + version + "\n")); err == nil {
			t.Errorf("parseAdmittedVersion(%q) accepted an unsupported version", version)
		}
	}
}

func TestValidateCapabilitySchemaRejectsUsedMethodAndFieldRemoval(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{
			name: "method",
			mutate: func(schema string) string {
				return strings.Replace(schema, `"method":{"const":"agent.prompt"}`, `"method":{"const":"agent.future"}`, 1)
			},
			wantErr: "method agent.prompt",
		},
		{
			name: "request field",
			mutate: func(schema string) string {
				return strings.Replace(schema, `"target":{},"text":{},"wait":{}`, `"target":{},"wait":{}`, 1)
			},
			wantErr: `missing required field "text"`,
		},
		{
			name: "snapshot identity field",
			mutate: func(schema string) string {
				return strings.Replace(schema, `"pane_id":{},"terminal_id":{},"workspace_id":{}`, `"pane_id":{},"workspace_id":{}`, 1)
			},
			wantErr: `PaneInfo: missing required field "terminal_id"`,
		},
		{
			name: "protocol",
			mutate: func(schema string) string {
				return strings.Replace(schema, `"protocol":17`, `"protocol":18`, 1)
			},
			wantErr: "unsupported herdr API tuple",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCapabilitySchema([]byte(test.mutate(validCapabilitySchema())))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateCapabilitySchema() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
