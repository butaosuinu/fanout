package tmuxrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitPaneTargetsSession(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf '%%42\n'
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	paneID, err := SplitPane("target-session", "/tmp/work tree")
	if err != nil {
		t.Fatalf("SplitPane() failed: %v", err)
	}
	if paneID != "%42" {
		t.Fatalf("SplitPane() paneID = %q, want %%42", paneID)
	}

	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	want := []string{"split-window", "-t", "target-session", "-d", "-h", "-P", "-F", "#{pane_id}", "-c", "/tmp/work tree"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tmux args = %#v, want %#v", got, want)
	}
}

func TestSplitPaneWithAgentCommandPassesWrappedLaunchCommandToTmux(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf '%%43\n'
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	command := "PATH='/very/long/path:/usr/bin' /tmp/bin/codex '[fanout #1] prompt'"
	launchCommand := BuildPaneLaunchCommand(command)
	paneID, err := SplitPaneWithAgentCommand("%1", "/tmp/work tree", command)
	if err != nil {
		t.Fatalf("SplitPaneWithAgentCommand() failed: %v", err)
	}
	if paneID != "%43" {
		t.Fatalf("SplitPaneWithAgentCommand() paneID = %q, want %%43", paneID)
	}

	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	want := []string{"split-window", "-t", "%1", "-d", "-h", "-P", "-F", "#{pane_id}", "-c", "/tmp/work tree", launchCommand}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tmux args = %#v, want %#v", got, want)
	}
}

func TestBuildPaneLaunchCommandUsesUserShellAndKeepsPaneOpen(t *testing.T) {
	got := BuildPaneLaunchCommand("PATH='/very/long/path:/usr/bin' /tmp/bin/codex '[fanout #1] prompt'")
	for _, want := range []string{
		`exec /bin/sh -lc `,
		`/tmp/bin/codex`,
		`[fanout #1] prompt`,
		`__fanout_status=$?`,
		`returning to shell`,
		`exec "${SHELL:-/bin/sh}" -l`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildPaneLaunchCommand() = %q, want substring %q", got, want)
		}
	}
}

func TestCapturePanePrefersAlternateScreen(t *testing.T) {
	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
has_alt=0
for arg in "$@"; do
	if [ "$arg" = "-a" ]; then
		has_alt=1
	fi
done
if [ "$1" = "capture-pane" ]; then
	if [ "$has_alt" = "1" ]; then
		printf 'ALTSCREEN Ready\n'
	else
		printf 'NORMALSCREEN\n'
	fi
	exit 0
fi
exit 2
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := CapturePane("%1")
	if err != nil {
		t.Fatalf("CapturePane() failed: %v", err)
	}
	if got != "ALTSCREEN Ready\n" {
		t.Fatalf("CapturePane() = %q, want alternate screen", got)
	}
}

func TestCapturePaneFallsBackToNormalScreen(t *testing.T) {
	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
for arg in "$@"; do
	if [ "$arg" = "-a" ]; then
		exit 1
	fi
done
if [ "$1" = "capture-pane" ]; then
	printf 'NORMALSCREEN Ready\n'
	exit 0
fi
exit 2
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := CapturePane("%1")
	if err != nil {
		t.Fatalf("CapturePane() failed: %v", err)
	}
	if got != "NORMALSCREEN Ready\n" {
		t.Fatalf("CapturePane() = %q, want normal screen fallback", got)
	}
}
