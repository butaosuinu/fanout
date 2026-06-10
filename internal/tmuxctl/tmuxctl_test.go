package tmuxctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayMessageUsesTmuxStatusLine(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	tmuxPath := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsPath) + "\n"
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write tmux shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := DisplayMessage("fanout", "hello"); err != nil {
		t.Fatalf("DisplayMessage: %v", err)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"display-message", "-t", "=fanout", "hello"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tmux args = %#v, want %#v", got, want)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
