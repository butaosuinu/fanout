package herdrrun

import (
	"strings"
	"testing"
)

func TestValidateCommandSurfaceOutputAcceptsInstalledHerdrSurface(t *testing.T) {
	t.Parallel()
	outputs := installedHerdrCommandSurfaceOutputs()
	for _, requirement := range requiredCommandSurfaces() {
		t.Run(requirement.group, func(t *testing.T) {
			t.Parallel()
			if err := validateCommandSurfaceOutput(requirement, []byte(outputs[requirement.group])); err != nil {
				t.Fatalf("validateCommandSurfaceOutput() error = %v", err)
			}
		})
	}
}

func TestValidateCommandSurfaceOutputAcceptsAdditivePaneReadSource(t *testing.T) {
	t.Parallel()
	requirement := requiredCommandSurfaces()[0]
	output := strings.Replace(
		installedHerdrCommandSurfaceOutputs()["pane"],
		"visible|recent|recent-unwrapped",
		"visible|recent|recent-unwrapped|detection",
		1,
	)
	if err := validateCommandSurfaceOutput(requirement, []byte(output)); err != nil {
		t.Fatalf("validateCommandSurfaceOutput() with additive detection source error = %v", err)
	}
}

func TestCombineCommandSurfaceOutputIncludesStdoutAndStderr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		streams commandStreams
		want    string
	}{
		{name: "stdout only", streams: commandStreams{stdout: []byte("stdout\n")}, want: "stdout\n"},
		{name: "stderr only", streams: commandStreams{stderr: []byte("stderr\n")}, want: "stderr\n"},
		{name: "both with boundary", streams: commandStreams{stdout: []byte("stdout"), stderr: []byte("stderr\n")}, want: "stdout\nstderr\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := string(combineCommandSurfaceOutput(tt.streams)); got != tt.want {
				t.Fatalf("combineCommandSurfaceOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateCommandSurfaceOutputFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		group   string
		mutate  func(string) string
		wantErr string
	}{
		{
			name:  "missing command",
			group: "pane",
			mutate: func(output string) string {
				return strings.Replace(output, "  herdr pane close <pane_id>\n", "", 1)
			},
			wantErr: "pane close",
		},
		{
			name:  "read source changed",
			group: "pane",
			mutate: func(output string) string {
				return strings.Replace(output, "visible|recent|recent-unwrapped", "visible|recent", 1)
			},
			wantErr: "pane read",
		},
		{
			name:  "read argument shape changed",
			group: "pane",
			mutate: func(output string) string {
				return strings.Replace(output, " [--lines N]", " [--lines <N>]", 1)
			},
			wantErr: "pane read",
		},
		{
			name:  "read source duplicated",
			group: "pane",
			mutate: func(output string) string {
				return strings.Replace(output, "visible|recent|recent-unwrapped", "visible|recent|recent-unwrapped|visible", 1)
			},
			wantErr: "pane read",
		},
		{
			name:  "target shape changed",
			group: "workspace",
			mutate: func(output string) string {
				return strings.Replace(output, "workspace focus <workspace_id>", "workspace focus [<workspace_id>]", 1)
			},
			wantErr: "workspace focus",
		},
		{
			name:  "dirty gate removed",
			group: "worktree",
			mutate: func(output string) string {
				return strings.Replace(output, " [--force]", "", 1)
			},
			wantErr: "worktree remove",
		},
		{
			name:  "duplicate command",
			group: "pane",
			mutate: func(output string) string {
				return output + "  herdr pane run <pane_id> <command>\n"
			},
			wantErr: "occurs 2 times",
		},
		{
			name:  "wrong group",
			group: "pane",
			mutate: func(output string) string {
				return strings.Replace(output, "herdr pane commands:", "herdr agent commands:", 1)
			},
			wantErr: "header=",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requirement commandSurfaceRequirement
			for _, candidate := range requiredCommandSurfaces() {
				if candidate.group == tt.group {
					requirement = candidate
					break
				}
			}
			err := validateCommandSurfaceOutput(requirement, []byte(tt.mutate(installedHerdrCommandSurfaceOutputs()[tt.group])))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateCommandSurfaceOutput() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// These are the relevant lines captured from the released herdr 0.7.4
// binary. The parser tests remain hermetic and never invoke an installed CLI.
func installedHerdrCommandSurfaceOutputs() map[string]string {
	return map[string]string{
		"pane": `herdr pane commands:
  herdr pane read <pane_id> [--source visible|recent|recent-unwrapped] [--lines N] [--format text|ansi] [--ansi]
  herdr pane close <pane_id>
  herdr pane run <pane_id> <command>
`,
		"workspace": `herdr workspace commands:
  herdr workspace focus <workspace_id>
  herdr workspace close <workspace_id>
`,
		"worktree": `herdr worktree commands:
  herdr worktree remove --workspace ID [--force] [--json]
`,
	}
}
