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

func TestListLivePanesParsesIDAndPath(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	// Each line is "#{pane_id}\t#{pane_current_path}"; include a blank line and
	// surrounding whitespace to exercise trimming.
	script := "#!/bin/sh\n" +
		`printf '%s\n' "$@" > "$TMUXRUN_ARGS"` + "\n" +
		`printf '%%9\t/wt/nine\n%%10\t/wt/ten\n\n'` + "\n"
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	panes, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes() failed: %v", err)
	}
	if len(panes) != 2 ||
		panes[0] != (LivePane{ID: "%9", CurrentPath: "/wt/nine"}) ||
		panes[1] != (LivePane{ID: "%10", CurrentPath: "/wt/ten"}) {
		t.Fatalf("ListLivePanes() = %#v", panes)
	}

	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	wantArgs := []string{"list-panes", "-a", "-F", "#{pane_id}\t#{pane_current_path}"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("tmux args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestBindDashboardKeyRegistersDetachedWindow(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := BindDashboardKey("D", "/abs/path/fanout"); err != nil {
		t.Fatalf("BindDashboardKey() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	// Each tmux argv on its own line: bind-key D new-window -d -n
	// fanout-dashboard -c #{pane_current_path} <launch>.
	want := []string{"bind-key", "D", "new-window", "-d", "-n", "fanout-dashboard", "-c", "#{pane_current_path}", "/abs/path/fanout dashboard --web --open"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tmux args = %#v, want %#v", got, want)
	}
}

func TestBindDashboardKeyQuotesBinaryPathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := BindDashboardKey("D", "/opt/My Tools/fanout"); err != nil {
		t.Fatalf("BindDashboardKey() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	// new-window runs the launch arg through one shell, so the binary path is
	// single-quoted to survive word-splitting on "My Tools".
	launch := got[len(got)-1]
	if launch != "'/opt/My Tools/fanout' dashboard --web --open" {
		t.Fatalf("launch arg = %q, want the binary path single-quoted", launch)
	}
}

func TestBindDashboardKeyRejectsEmptyArgs(t *testing.T) {
	if err := BindDashboardKey("", "/abs/fanout"); err == nil {
		t.Fatal("BindDashboardKey(empty key) should error")
	}
	if err := BindDashboardKey("D", ""); err == nil {
		t.Fatal("BindDashboardKey(empty bin) should error")
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

func TestListPanesPreservesQualifiedSessionTargetArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "has-session" ]; then
	exit 0
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if _, err := ListPanes("=fanout"); err != nil {
		t.Fatalf("ListPanes() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-s", "-t", "=fanout", "-F", paneListFormat})
}

func TestListPanesPreservesSessionIDTargetArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "has-session" ]; then
	exit 0
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if _, err := ListPanes("$1"); err != nil {
		t.Fatalf("ListPanes() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-s", "-t", "$1", "-F", paneListFormat})
}

func TestListAllPanesBuildsArgsAndParsesOutput(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf '%%4:@3:2:0:other session pane\n'
`)

	got, err := ListAllPanes()
	if err != nil {
		t.Fatalf("ListAllPanes() failed: %v", err)
	}

	wantPanes := []PaneInfo{{ID: "%4", WindowID: "@3", Index: 2, Active: false, Title: "other session pane"}}
	if !reflect.DeepEqual(got, wantPanes) {
		t.Fatalf("ListAllPanes() = %#v, want %#v", got, wantPanes)
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-a", "-F", paneListFormat})
}

func TestNewWindowBuildsArgsAndParsesOutput(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf '%%9:@7:0:1:fanout tui\n'
`)

	got, err := NewWindow("fanout", "fanout tui", "/tmp/work tree")
	if err != nil {
		t.Fatalf("NewWindow() failed: %v", err)
	}

	wantPane := PaneInfo{ID: "%9", WindowID: "@7", Index: 0, Active: true, Title: "fanout tui"}
	if !reflect.DeepEqual(got, wantPane) {
		t.Fatalf("NewWindow() = %#v, want %#v", got, wantPane)
	}
	assertTmuxArgs(t, argsPath, []string{"new-window", "-d", "-P", "-F", paneListFormat, "-t", "=fanout", "-n", "fanout tui", "-c", "/tmp/work tree"})
}

func TestCapturePaneOutputUsesAlternateScreenWhenAvailable(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "display-message" ]; then
	printf '1\n'
	exit 0
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
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
	argsPath := installTmuxShim(t, `if [ "$1" = "display-message" ]; then
	printf '1\n'
	exit 0
fi
if [ "$2" = "-a" ]; then
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

func TestCapturePaneOutputUsesNormalScreenWhenAlternateOff(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "display-message" ]; then
	printf '0\n'
	exit 0
fi
if [ "$2" = "-a" ]; then
	exit 2
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf 'normal\n'
`)

	got, err := CapturePaneOutput("%7", 25)
	if err != nil {
		t.Fatalf("CapturePaneOutput() failed: %v", err)
	}
	if got != "normal\n" {
		t.Fatalf("CapturePaneOutput() = %q, want normal output", got)
	}
	assertTmuxArgs(t, argsPath, []string{"capture-pane", "-p", "-t", "%7", "-S", "-25"})
}

func TestCapturePaneOutputOmitsStartWhenLinesZero(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "display-message" ]; then
	printf '0\n'
	exit 0
fi
if [ "$2" = "-a" ]; then
	exit 1
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if _, err := CapturePaneOutput("%8", 0); err != nil {
		t.Fatalf("CapturePaneOutput() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"capture-pane", "-p", "-t", "%8"})
}

func TestSelectPaneSwitchesClientToPaneTarget(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SelectPane("%9"); err != nil {
		t.Fatalf("SelectPane() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"switch-client", "-t", "%9"})
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

func TestCurrentSessionBuildsArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf 'fanout-dev\n'
`)

	got, err := CurrentSession()
	if err != nil {
		t.Fatalf("CurrentSession() failed: %v", err)
	}
	if got != "fanout-dev" {
		t.Fatalf("CurrentSession() = %q, want fanout-dev", got)
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-p", "#{session_name}"})
}

func TestHasSessionBuildsArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if !HasSession("fanout") {
		t.Fatalf("HasSession() = false, want true")
	}
	assertTmuxArgs(t, argsPath, []string{"has-session", "-t", "=fanout"})
}

func TestHasSessionPreservesQualifiedTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "=fanout", want: "=fanout"},
		{name: "$1", want: "$1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

			if !HasSession(tc.name) {
				t.Fatalf("HasSession() = false, want true")
			}
			assertTmuxArgs(t, argsPath, []string{"has-session", "-t", tc.want})
		})
	}
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

func TestSendKeysBuildsArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SendKeys("fanout", "/tmp/bin/fanout", "Enter"); err != nil {
		t.Fatalf("SendKeys() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"send-keys", "-t", "=fanout", "/tmp/bin/fanout", "Enter"})
}

func TestSendKeysPreservesQualifiedTarget(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SendKeys("=fanout", "q"); err != nil {
		t.Fatalf("SendKeys() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"send-keys", "-t", "=fanout", "q"})
}

func TestSendKeysPreservesPaneTarget(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SendKeys("%1", "q"); err != nil {
		t.Fatalf("SendKeys() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"send-keys", "-t", "%1", "q"})
}

func TestFocusPaneSelectsWindowAndPane(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
`)

	if err := FocusPane(PaneInfo{ID: "%9", WindowID: "@7"}); err != nil {
		t.Fatalf("FocusPane() failed: %v", err)
	}

	assertTmuxArgs(t, argsPath, []string{"select-window", "-t", "@7", "---", "select-pane", "-t", "%9", "---"})
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

func TestAttachOrSwitchPreservesQualifiedTarget(t *testing.T) {
	t.Setenv("TMUX", "")
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := AttachOrSwitch("=fanout"); err != nil {
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

func TestPaneTitleBuildsArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf 'old title with spaces\n'
`)

	got, err := PaneTitle("%1")
	if err != nil {
		t.Fatalf("PaneTitle() failed: %v", err)
	}
	if got != "old title with spaces" {
		t.Fatalf("PaneTitle() = %q, want old title with spaces", got)
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-p", "-t", "%1", "#{pane_title}"})
}

func TestSetPaneTitleAllowsEmptyTitle(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SetPaneTitle("%1", ""); err != nil {
		t.Fatalf("SetPaneTitle() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "select-pane\n-t\n%1\n-T\n\n"; string(body) != want {
		t.Fatalf("tmux args body = %q, want %q", string(body), want)
	}
}
