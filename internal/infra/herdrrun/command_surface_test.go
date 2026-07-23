package herdrrun

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const official075AgentPromptHelp = `Submit a prompt to an agent

Usage: herdr agent prompt <TARGET> <TEXT> [OPTIONS]

Arguments:
  <TARGET>

  <TEXT>

Options:
      --wait
          Wait for the first matching state observed after submission

      --until <STATUS>
          State to match after --wait; repeat for more than one state

          [possible values: idle, working, blocked, done, unknown]

      --timeout <MS>
          Fail after this many milliseconds
`

func TestValidateCommandSurfacesAcceptsOfficial075AgentPromptHelp(t *testing.T) {
	b := New("fanout-test", "/private/tmp/fanout-test/herdr.sock")
	b.helpOutput = func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		for _, surface := range requiredCommandSurfaces {
			if slices.Equal(args[:len(args)-1], surface.args) {
				if slices.Equal(surface.args, []string{"agent", "prompt"}) {
					return []byte(official075AgentPromptHelp), nil
				}
				return []byte(strings.Join(surface.required, "\n")), nil
			}
		}
		return nil, fmt.Errorf("unexpected args %v", args)
	}
	if err := b.validateCommandSurfaces(context.Background(), "/private/tmp/herdr", route{session: "fanout-test", socketPath: "/private/tmp/fanout-test/herdr.sock"}); err != nil {
		t.Fatalf("validateCommandSurfaces() error = %v", err)
	}
}

func TestValidateCommandSurfacesRejectsMissingHelpContract(t *testing.T) {
	b := New("fanout-test", "/private/tmp/fanout-test/herdr.sock")
	b.helpOutput = func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		for _, surface := range requiredCommandSurfaces {
			if slices.Equal(args[:len(args)-1], surface.args) {
				text := strings.Join(surface.required, "\n")
				if slices.Equal(surface.args, []string{"agent", "prompt"}) {
					text = strings.Replace(text, "--timeout", "", 1)
				}
				return []byte(text), nil
			}
		}
		return nil, fmt.Errorf("unexpected args %v", args)
	}
	err := b.validateCommandSurfaces(context.Background(), "/private/tmp/herdr", route{session: "fanout-test", socketPath: "/private/tmp/fanout-test/herdr.sock"})
	if err == nil || !strings.Contains(err.Error(), `help is missing "--timeout"`) {
		t.Fatalf("validateCommandSurfaces() error = %v", err)
	}
}
