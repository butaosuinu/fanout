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

const paneReadUsage = "  herdr pane read <pane_id> [--source visible|recent|recent-unwrapped] [--lines N] [--format text|ansi] [--ansi]"

// validateCommandSurfaces checks CLI-only operations that cannot be attested
// by api schema. Group help is offline and has a successful exit status; no
// target resource is read or mutated by this gate.
func (b *Backend) validateCommandSurfaces(ctx context.Context, admitted binaryAdmission, target route) error {
	for _, requirement := range requiredCommandSurfaces() {
		streams, err := b.runAdmittedStreamsContext(
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
		output := combineCommandSurfaceOutput(streams)
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
				paneReadUsage,
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

func combineCommandSurfaceOutput(streams commandStreams) []byte {
	separator := len(streams.stdout) > 0 && len(streams.stderr) > 0 && streams.stdout[len(streams.stdout)-1] != '\n'
	output := make([]byte, 0, len(streams.stdout)+len(streams.stderr)+1)
	output = append(output, streams.stdout...)
	if separator {
		output = append(output, '\n')
	}
	return append(output, streams.stderr...)
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
			if commandUsageMatches(required, line) {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("required usage line %q occurs %d times", strings.TrimSpace(required), count)
		}
	}
	return nil
}

func commandUsageMatches(required, line string) bool {
	if required != paneReadUsage {
		return line == required
	}
	const (
		prefix = "  herdr pane read <pane_id> [--source "
		suffix = "] [--lines N] [--format text|ansi] [--ansi]"
	)
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return false
	}
	sourceList := strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
	sources := strings.Split(sourceList, "|")
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if !validCommandChoice(source) {
			return false
		}
		if _, duplicate := seen[source]; duplicate {
			return false
		}
		seen[source] = struct{}{}
	}
	for _, requiredSource := range []string{"visible", "recent", "recent-unwrapped"} {
		if _, ok := seen[requiredSource]; !ok {
			return false
		}
	}
	return true
}

func validCommandChoice(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func firstLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}
