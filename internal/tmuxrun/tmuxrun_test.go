package tmuxrun

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func installTmuxShim(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

func readTmuxArgs(t *testing.T, argsPath string) []string {
	t.Helper()
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(body), "\n"), "\n")
}

func assertTmuxArgs(t *testing.T, argsPath string, want []string) {
	t.Helper()
	got := readTmuxArgs(t, argsPath)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tmux args = %#v, want %#v", got, want)
	}
}

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

func TestParseListPanesOutput(t *testing.T) {
	input := "%1:@1:0:1:main\n%2:@1:1:0:title:with:colons\n"

	got, err := parseListPanesOutput(input)
	if err != nil {
		t.Fatalf("parseListPanesOutput() failed: %v", err)
	}

	want := []PaneInfo{
		{ID: "%1", WindowID: "@1", Index: 0, Active: true, Title: "main"},
		{ID: "%2", WindowID: "@1", Index: 1, Active: false, Title: "title:with:colons"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseListPanesOutput() = %#v, want %#v", got, want)
	}
}

func TestParseListPanesOutputRejectsMalformedLines(t *testing.T) {
	for _, input := range []string{
		"%1:@1:0:1",
		"%1:@1:not-int:1:title",
		"%1:@1:0:yes:title",
	} {
		if _, err := parseListPanesOutput(input); err == nil {
			t.Fatalf("parseListPanesOutput(%q) succeeded, want error", input)
		}
	}
}

func TestListPanesBuildsArgsAndParsesOutput(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf '%%3:@2:4:1:fanout pane\n'
`)

	got, err := ListPanes("target-session")
	if err != nil {
		t.Fatalf("ListPanes() failed: %v", err)
	}

	wantPanes := []PaneInfo{{ID: "%3", WindowID: "@2", Index: 4, Active: true, Title: "fanout pane"}}
	if !reflect.DeepEqual(got, wantPanes) {
		t.Fatalf("ListPanes() = %#v, want %#v", got, wantPanes)
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-s", "-t", "=target-session", "-F", paneListFormat})
}

func TestListPanesPreservesPaneTargetArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "has-session" ]; then
	exit 1
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if _, err := ListPanes("%3"); err != nil {
		t.Fatalf("ListPanes() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-t", "%3", "-F", paneListFormat})
}

func TestListPanesPreservesSimpleNonSessionTargetArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "has-session" ]; then
	exit 1
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if _, err := ListPanes("window-name"); err != nil {
		t.Fatalf("ListPanes() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-t", "window-name", "-F", paneListFormat})
}

func TestListPanesUsesSessionFlagForPunctuatedSessionTarget(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "has-session" ]; then
	exit 0
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if _, err := ListPanes("project.dev"); err != nil {
		t.Fatalf("ListPanes() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-s", "-t", "=project.dev", "-F", paneListFormat})
}

func TestCapturePaneOutputUsesAlternateScreenWhenAvailable(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf 'alternate\n'
`)

	got, err := CapturePaneOutput("%7", 25)
	if err != nil {
		t.Fatalf("CapturePaneOutput() failed: %v", err)
	}
	if got != "alternate\n" {
		t.Fatalf("CapturePaneOutput() = %q, want alternate output", got)
	}
	assertTmuxArgs(t, argsPath, []string{"capture-pane", "-a", "-p", "-t", "%7"})
}

func TestCapturePaneOutputFallsBackToNormalCaptureArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$2" = "-a" ]; then
	exit 1
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf 'line 1\nline 2\n'
`)

	got, err := CapturePaneOutput("%7", 25)
	if err != nil {
		t.Fatalf("CapturePaneOutput() failed: %v", err)
	}
	if got != "line 1\nline 2\n" {
		t.Fatalf("CapturePaneOutput() = %q, want captured output", got)
	}
	assertTmuxArgs(t, argsPath, []string{"capture-pane", "-p", "-t", "%7", "-S", "-25"})
}

func TestCapturePaneOutputOmitsStartWhenLinesZero(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$2" = "-a" ]; then
	exit 1
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if _, err := CapturePaneOutput("%8", 0); err != nil {
		t.Fatalf("CapturePaneOutput() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"capture-pane", "-p", "-t", "%8"})
}

func TestSelectPaneBuildsArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SelectPane("%9"); err != nil {
		t.Fatalf("SelectPane() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"select-pane", "-t", "%9"})
}

func TestIsPaneAliveBuildsArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if !IsPaneAlive("%10") {
		t.Fatalf("IsPaneAlive() = false, want true")
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-p", "-t", "%10"})
}

func TestIsPaneAliveReturnsFalseWhenTmuxCannotResolvePane(t *testing.T) {
	installTmuxShim(t, `exit 1
`)

	if IsPaneAlive("%404") {
		t.Fatalf("IsPaneAlive() = true, want false")
	}
}

func TestInsideTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	if !InsideTmux() {
		t.Fatalf("InsideTmux() = false, want true")
	}

	t.Setenv("TMUX", "")
	if InsideTmux() {
		t.Fatalf("InsideTmux() = true, want false")
	}
}

func TestHasSessionBuildsArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if !HasSession("fanout") {
		t.Fatalf("HasSession() = false, want true")
	}
	assertTmuxArgs(t, argsPath, []string{"has-session", "-t", "=fanout"})
}

func TestHasSessionReturnsFalseWhenMissing(t *testing.T) {
	installTmuxShim(t, `exit 1
`)

	if HasSession("missing") {
		t.Fatalf("HasSession() = true, want false")
	}
}

func TestNewSessionBuildsArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := NewSession("fanout", "/tmp/work tree"); err != nil {
		t.Fatalf("NewSession() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"new-session", "-d", "-s", "fanout", "-c", "/tmp/work tree"})
}

func TestAttachOrSwitchAttachesOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := AttachOrSwitch("fanout"); err != nil {
		t.Fatalf("AttachOrSwitch() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"attach-session", "-t", "=fanout"})
}

func TestAttachOrSwitchSwitchesInsideTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := AttachOrSwitch("fanout"); err != nil {
		t.Fatalf("AttachOrSwitch() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"switch-client", "-t", "=fanout"})
}
