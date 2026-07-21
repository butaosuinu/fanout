package herdrrun

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

type commandSurfaceRequirement struct {
	group string
	lines []string
}

// validateCommandSurfaces checks CLI-only operations that cannot be attested
// by api schema. Group help is offline and has a successful exit status; no
// target resource is read or mutated by this gate.
func (b *Backend) validateCommandSurfaces(ctx context.Context, admitted binaryAdmission, target route) error {
	for _, requirement := range requiredCommandSurfaces() {
		output, err := b.runAdmittedContext(
			ctx,
			commandTimeout,
			admitted,
			target,
			requirement.group,
			"--help",
		)
		if err != nil {
			return fmt.Errorf("herdr %s --help: %w", requirement.group, err)
		}
		if err := validateCommandSurfaceOutput(requirement, output); err != nil {
			return fmt.Errorf("unsupported herdr %s command surface: %w", requirement.group, err)
		}
	}
	return nil
}

func requiredCommandSurfaces() []commandSurfaceRequirement {
	return []commandSurfaceRequirement{
		{
			group: "pane",
			lines: []string{
				"  herdr pane read <pane_id> [--source visible|recent|recent-unwrapped] [--lines N] [--format text|ansi] [--ansi]",
				"  herdr pane close <pane_id>",
				"  herdr pane run <pane_id> <command>",
			},
		},
		{
			group: "workspace",
			lines: []string{
				"  herdr workspace focus <workspace_id>",
				"  herdr workspace close <workspace_id>",
			},
		},
		{
			group: "worktree",
			lines: []string{
				"  herdr worktree remove --workspace ID [--force] [--json]",
			},
		},
	}
}

func validateCommandSurfaceOutput(requirement commandSurfaceRequirement, output []byte) error {
	if !utf8.Valid(output) || bytes.IndexByte(output, 0) >= 0 {
		return fmt.Errorf("help output is not valid text")
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	wantHeader := "herdr " + requirement.group + " commands:"
	if len(lines) == 0 || lines[0] != wantHeader {
		return fmt.Errorf("header=%q, want %q", firstLine(lines), wantHeader)
	}
	for _, required := range requirement.lines {
		count := 0
		for _, line := range lines[1:] {
			if line == required {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("required usage line %q occurs %d times", strings.TrimSpace(required), count)
		}
	}
	return nil
}

func firstLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}
