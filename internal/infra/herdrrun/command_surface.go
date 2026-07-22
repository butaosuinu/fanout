package herdrrun

import (
	"context"
	"fmt"
	"strings"
)

type commandHelpOutput func(context.Context, string, []string, ...string) ([]byte, error)

type commandSurface struct {
	args     []string
	required []string
}

var requiredCommandSurfaces = []commandSurface{
	{args: []string{"pane", "read"}, required: []string{"Usage: herdr pane read", "--source", "--lines", "--format"}},
	{args: []string{"agent", "prompt"}, required: []string{"Usage: herdr agent prompt <TARGET> <TEXT>", "--wait", "--until", "--timeout"}},
	{args: []string{"workspace", "focus"}, required: []string{"Usage: herdr workspace focus <workspace_id>"}},
	{args: []string{"pane", "close"}, required: []string{"Usage: herdr pane close <pane_id>"}},
	{args: []string{"workspace", "close"}, required: []string{"Usage: herdr workspace close <workspace_id>"}},
}

func (b *Backend) validateCommandSurfaces(ctx context.Context, binary string, target route) error {
	for _, surface := range requiredCommandSurfaces {
		callCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		args := append(append([]string(nil), surface.args...), "--help")
		output, err := b.helpOutput(callCtx, binary, routeEnvironment(target, b.control), args...)
		cancel()
		if err != nil {
			return fmt.Errorf("herdr %s --help: %w", strings.Join(surface.args, " "), err)
		}
		text := string(output)
		for _, required := range surface.required {
			if !strings.Contains(text, required) {
				return fmt.Errorf("unsupported herdr command surface %q: help is missing %q", strings.Join(surface.args, " "), required)
			}
		}
	}
	return nil
}

func runCommandHelp(ctx context.Context, binary string, env []string, args ...string) ([]byte, error) {
	return runCommandCombined(ctx, binary, env, args...)
}
