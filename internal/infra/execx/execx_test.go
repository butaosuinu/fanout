package execx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installShim writes an executable shell script named name into a fresh dir
// prepended to PATH, so tests exercise real exec paths deterministically.
func installShim(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestOutputErrorFormatting pins the shared "name args...: stderr" exit-error
// format previously copy-pasted across ghissue and gitstat.
func TestOutputErrorFormatting(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		args    []string
		wantErr string
		wantOut string
	}{
		{
			name:    "success returns stdout untouched",
			script:  "printf 'ok\\n'",
			args:    []string{"view", "1"},
			wantOut: "ok\n",
		},
		{
			name:    "exit error becomes name args colon trimmed stderr",
			script:  "printf '  boom  \\n' >&2; exit 1",
			args:    []string{"do", "thing"},
			wantErr: "fakebin do thing: boom",
		},
		{
			name:    "multiline stderr keeps inner newlines and trims edges",
			script:  "printf 'one\\ntwo\\n' >&2; exit 2",
			args:    []string{"x"},
			wantErr: "fakebin x: one\ntwo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installShim(t, "fakebin", tt.script)
			out, err := Output("", nil, "fakebin", tt.args...)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Output() error = %v, want nil", err)
				}
				if string(out) != tt.wantOut {
					t.Fatalf("Output() = %q, want %q", out, tt.wantOut)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Output() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestOutputNonExitErrorPassesThrough guarantees only ExitErrors are
// reformatted; a missing binary keeps its exec.ErrNotFound identity.
func TestOutputNonExitErrorPassesThrough(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Output("", nil, "no-such-fanout-binary"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("Output() error = %v, want exec.ErrNotFound", err)
	}
}

// TestOutputDirAndExtraEnv guarantees dir becomes the working directory and
// extraEnv entries win over the inherited environment.
func TestOutputDirAndExtraEnv(t *testing.T) {
	installShim(t, "fakebin", `printf '%s %s' "$(pwd)" "$FANOUT_EXECX_TEST"`)
	dir := t.TempDir()
	t.Setenv("FANOUT_EXECX_TEST", "inherited")

	out, err := Output(dir, []string{"FANOUT_EXECX_TEST=extra"}, "fakebin")
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	gotDir, gotEnv, _ := strings.Cut(string(out), " ")
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(gotDir); err != nil || resolved != wantDir {
		t.Fatalf("Output() ran in %q, want %q", gotDir, wantDir)
	}
	if gotEnv != "extra" {
		t.Fatalf("Output() env FANOUT_EXECX_TEST = %q, want extra (extraEnv wins)", gotEnv)
	}
}

func TestOutputExitCode(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		wantOut  string
		wantCode int
		wantErr  string
	}{
		{
			name:     "success",
			script:   "printf 'ok\n'",
			wantOut:  "ok\n",
			wantCode: 0,
		},
		{
			name:     "exit error returns stdout and code",
			script:   "printf 'diff\n'; printf ' changed \n' >&2; exit 1",
			wantOut:  "diff\n",
			wantCode: 1,
			wantErr:  "fakebin --no-index: changed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installShim(t, "fakebin", tt.script)
			out, code, err := OutputExitCode("", nil, "fakebin", "--no-index")
			if string(out) != tt.wantOut {
				t.Fatalf("OutputExitCode() output = %q, want %q", out, tt.wantOut)
			}
			if code != tt.wantCode {
				t.Fatalf("OutputExitCode() code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("OutputExitCode() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("OutputExitCode() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestOutputExitCodeNonExitError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, code, err := OutputExitCode("", nil, "no-such-fanout-binary")
	if code != -1 {
		t.Fatalf("OutputExitCode() code = %d, want -1", code)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("OutputExitCode() error = %v, want exec.ErrNotFound", err)
	}
}

// TestCombinedErrorFormatting pins the "%w: <trimmed combined output>" format
// previously private to internal/infra/worktree.
func TestCombinedErrorFormatting(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr string // "" = want success
		wantOut string
	}{
		{
			name:    "success returns combined stdout and stderr",
			script:  "printf 'out\\n'; printf 'err\\n' >&2",
			wantOut: "out\nerr\n",
		},
		{
			name:    "failure appends trimmed output after the exit error",
			script:  "printf '  warning: bad ref  \\n'; exit 3",
			wantErr: "exit status 3: warning: bad ref",
		},
		{
			name:    "failure with empty output returns the raw error",
			script:  "exit 4",
			wantErr: "exit status 4",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installShim(t, "fakebin", tt.script)
			out, err := Combined(t.TempDir(), "fakebin")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Combined() error = %v, want nil", err)
				}
				if string(out) != tt.wantOut {
					t.Fatalf("Combined() = %q, want %q", out, tt.wantOut)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Combined() error = %v, want %q", err, tt.wantErr)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("Combined() error = %v, want wrapped *exec.ExitError", err)
			}
		})
	}
}

// TestOutputStdin pins that stdin reaches the child and errors share Output's
// exec.ExitError format.
func TestOutputStdin(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		stdin   string
		wantOut string
		wantErr string
	}{
		{
			name:    "stdin is forwarded to the command",
			script:  "cat",
			stdin:   "hello\n",
			wantOut: "hello\n",
		},
		{
			name:    "failure formats name, args, and trimmed stderr",
			script:  "cat >/dev/null\necho ' broken ' >&2\nexit 3",
			stdin:   "ignored",
			wantErr: "fakebin issue comment: broken",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installShim(t, "fakebin", tt.script)
			out, err := OutputStdin(t.TempDir(), tt.stdin, "fakebin", "issue", "comment")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("OutputStdin() error = %v, want nil", err)
				}
				if string(out) != tt.wantOut {
					t.Fatalf("OutputStdin() = %q, want %q", out, tt.wantOut)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("OutputStdin() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
