// Package execx runs external commands with the two error formats the infra
// layer shares: Output reformats an exec.ExitError as
// "name args...: <trimmed stderr>", Combined appends the trimmed combined
// output as "%w: <output>". Both formats are locked in by callers' user-visible
// error strings; do not change them.
package execx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Output runs the command and returns its stdout. dir, when non-empty, becomes
// the process working directory. extraEnv entries, when present, are appended
// to os.Environ() (later entries win). On exec.ExitError the error becomes
// "name args...: <trimmed stderr>"; any other error passes through unchanged.
func Output(dir string, extraEnv []string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	return capture(cmd, name, args)
}

// OutputStdin is Output with stdin attached; it shares the same exec.ExitError
// formatting.
func OutputStdin(dir, stdin, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = strings.NewReader(stdin)
	return capture(cmd, name, args)
}

func capture(cmd *exec.Cmd, name string, args []string) ([]byte, error) {
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return out, err
	}
	return out, nil
}

// Combined runs the command in dir and returns its combined stdout+stderr. On
// failure the trimmed output is appended as "%w: <output>"; when the output is
// empty the raw error is returned unchanged.
func Combined(dir string, name string, args ...string) ([]byte, error) {
	return CombinedContext(context.Background(), dir, name, args...)
}

// CombinedContext is Combined with cancellation and deadline support.
func CombinedContext(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return out, contextErr
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return out, err
		}
		return out, fmt.Errorf("%w: %s", err, msg)
	}
	return out, nil
}
