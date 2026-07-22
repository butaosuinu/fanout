package herdrrun

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

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
