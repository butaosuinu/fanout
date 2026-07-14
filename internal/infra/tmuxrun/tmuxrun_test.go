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

func TestCheckMinimumVersionOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		out     string
		wantErr string
	}{
		{name: "3.3 exact", out: "tmux 3.3\n"},
		{name: "suffix", out: "tmux 3.6a\n"},
		{name: "new major", out: "tmux 4.0\n"},
		{name: "old", out: "tmux 3.2\n", wantErr: "tmux 3.3+ (found 3.2; brew upgrade tmux)"},
		{name: "unparseable", out: "tmux next\n", wantErr: "could not parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMinimumVersionOutput(tc.out)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkMinimumVersionOutput() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("checkMinimumVersionOutput() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestCurrentClientSize(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf '132 41\n'
`)

	got, err := CurrentClientSize()
	if err != nil {
		t.Fatalf("CurrentClientSize() failed: %v", err)
	}
	if got != (ClientSize{Width: 132, Height: 41}) {
		t.Fatalf("CurrentClientSize() = %#v, want 132x41", got)
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-p", "#{client_width} #{client_height}"})
}

func TestPaneGeometryForPane(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf '10\t2\t40\t20\t132\t41\n'
`)

	got, err := PaneGeometryForPane("%7")
	if err != nil {
		t.Fatalf("PaneGeometryForPane() failed: %v", err)
	}
	want := PaneGeometry{Left: 10, Top: 2, Width: 40, Height: 20, ClientWidth: 132, ClientHeight: 41}
	if got != want {
		t.Fatalf("PaneGeometryForPane() = %#v, want %#v", got, want)
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-p", "-t", "%7", "-F", paneGeometryFormat})
}

func TestPaneGeometryForPaneRejectsBadOutput(t *testing.T) {
	installTmuxShim(t, `printf '10\t2\t40\n'
`)

	if _, err := PaneGeometryForPane("%7"); err == nil {
		t.Fatal("PaneGeometryForPane() succeeded for malformed output")
	}
}

func TestPaneGeometryForPaneRejectsMalformedPaneID(t *testing.T) {
	if _, err := PaneGeometryForPane("pane"); err == nil {
		t.Fatal("PaneGeometryForPane() succeeded for malformed pane id")
	}
}

func TestDisplayPopupBuildsCenteredArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	err := DisplayPopup(PopupOptions{
		Width:    90,
		Height:   32,
		StartDir: "/tmp/work tree",
		Title:    "New agent pane",
		Command:  "/tmp/fanout __tui-new-pane-popup",
	})
	if err != nil {
		t.Fatalf("DisplayPopup() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{
		"display-popup", "-E",
		"-b", popupBorderLines,
		"-S", popupBorderStyle,
		"-w", "90",
		"-h", "32",
		"-d", "/tmp/work tree",
		"-x", "C",
		"-y", "C",
		"-T", "New agent pane",
		"/tmp/fanout __tui-new-pane-popup",
	})
}

func TestDisplayPopupBuildsPositionedArgs(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	err := DisplayPopup(PopupOptions{
		Width:    76,
		Height:   20,
		StartDir: "/tmp/repo",
		Title:    "Keyboard shortcuts",
		Command:  "/tmp/fanout __tui-help-popup",
		Position: &PopupPosition{X: 41, Y: 0},
	})
	if err != nil {
		t.Fatalf("DisplayPopup() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{
		"display-popup", "-E",
		"-b", popupBorderLines,
		"-S", popupBorderStyle,
		"-w", "76",
		"-h", "20",
		"-d", "/tmp/repo",
		"-x", "41",
		"-y", "0",
		"-T", "Keyboard shortcuts",
		"/tmp/fanout __tui-help-popup",
	})
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

// installLivePanesShim installs a tmux shim that answers the
// `list-panes -a -F <format>` calls ListLivePanes makes, dispatching on the
// format argument ($4): pathBody for livePanePathFormat, titleBody for
// livePaneTitleFormat, agentStateBody for livePaneAgentStateFormat, and
// optional shell/project/worktree/role/session bodies. Each argv is appended
// to the args file separated by "---" lines.
func installLivePanesShim(t *testing.T, pathBody, titleBody, agentStateBody string, optionBodies ...string) string {
	t.Helper()
	shellBody := `printf ''`
	if len(optionBodies) > 0 {
		shellBody = optionBodies[0]
	}
	projectRootBody := `printf ''`
	if len(optionBodies) > 1 {
		projectRootBody = optionBodies[1]
	}
	worktreePathBody := `printf ''`
	if len(optionBodies) > 2 {
		worktreePathBody = optionBodies[2]
	}
	roleBody := `printf ''`
	if len(optionBodies) > 3 {
		roleBody = optionBodies[3]
	}
	sessionIDBody := `printf ''`
	if len(optionBodies) > 4 {
		sessionIDBody = optionBodies[4]
	}
	script := `printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
case "$4" in
*pane_current_path*)
	` + pathBody + `
	;;
*fanout_agent_state*)
	` + agentStateBody + `
	;;
*fanout_shell_key*)
	` + shellBody + `
	;;
*fanout_project_root*)
	` + projectRootBody + `
	;;
*fanout_worktree_path*)
	` + worktreePathBody + `
	;;
*fanout_role*)
	` + roleBody + `
	;;
*session_id*)
	` + sessionIDBody + `
	;;
*pane_title*)
	` + titleBody + `
	;;
esac
`
	return installTmuxShim(t, script)
}

func TestListLivePanesJoinsPathTitleAndAgentStateOutputsByID(t *testing.T) {
	// Path and title are each the last field of their own listing, so embedded
	// tabs in either survive the strings.Cut split. An empty title stays empty,
	// and an id absent from the agent-state listing (a pane not launched by the
	// fanout wrapper) degrades to an empty agent state.
	argsPath := installLivePanesShim(t,
		`printf '%%9\t/wt/nine\n%%10\t/wt/ten\twith\ttabs\n%%11\t/wt/eleven\n\n'`,
		`printf '%%9\tnine: child\n%%10\ttitle\twith\ttabs\n%%11\t\n'`,
		`printf '%%9\trunning\n%%10\tdone\n'`,
		`printf '%%9\tshell-nine\n'`,
		`printf '%%9\t/repo\n%%10\t/repo\n'`,
		`printf '%%9\t/wt/nine\n%%10\t/wt/ten\twith\ttabs\n'`,
		`printf '%%9\tconsole\n'`,
		`printf '%%9\t$1\n%%10\t$1\n%%11\t$2\n'`)

	panes, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes() failed: %v", err)
	}
	want := []LivePane{
		{ID: "%9", CurrentPath: "/wt/nine", Title: "nine: child", AgentState: "running", ShellKey: "shell-nine", ProjectRoot: "/repo", WorktreePath: "/wt/nine", Role: "console", SessionID: "$1"},
		{ID: "%10", CurrentPath: "/wt/ten\twith\ttabs", Title: "title\twith\ttabs", AgentState: "done", ProjectRoot: "/repo", WorktreePath: "/wt/ten\twith\ttabs", SessionID: "$1"},
		{ID: "%11", CurrentPath: "/wt/eleven", Title: "", AgentState: "", SessionID: "$2"},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListLivePanes() = %#v, want %#v", panes, want)
	}

	assertTmuxArgs(t, argsPath, []string{
		"list-panes", "-a", "-F", "#{pane_id}\t#{pane_current_path}", "---",
		"list-panes", "-a", "-F", "#{pane_id}\t#{pane_title}", "---",
		"list-panes", "-a", "-F", "#{pane_id}\t#{@fanout_agent_state}", "---",
		"list-panes", "-a", "-F", "#{pane_id}\t#{@fanout_shell_key}", "---",
		"list-panes", "-a", "-F", "#{pane_id}\t#{@fanout_project_root}", "---",
		"list-panes", "-a", "-F", "#{pane_id}\t#{@fanout_worktree_path}", "---",
		"list-panes", "-a", "-F", "#{pane_id}\t#{@fanout_role}", "---",
		"list-panes", "-a", "-F", "#{pane_id}\t#{session_id}", "---",
	})
}

func TestListLivePanesDropsForgedPathLineAbsentFromTitleOutput(t *testing.T) {
	// A pane whose cwd is a crafted directory named "/tmp/evil\n%5\t/wt/recorded"
	// makes the path listing emit a forged "%5\t/wt/recorded" line. The title
	// listing cannot be forged (tmux rejects newlines in titles), so %5 is
	// absent there and the phantom pane must be dropped; the real pane keeps
	// only the pre-newline fragment of its crafted path.
	installLivePanesShim(t,
		`printf '%%9\t/tmp/evil\n%%5\t/wt/recorded\n'`,
		`printf '%%9\tnine\n'`,
		`printf '%%9\trunning\n%%5\trunning\n'`)

	panes, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes() failed: %v", err)
	}
	want := []LivePane{{ID: "%9", CurrentPath: "/tmp/evil", Title: "nine", AgentState: "running"}}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListLivePanes() = %#v, want %#v", panes, want)
	}
}

func TestListLivePanesReturnsTitlelessPanesWhenTitleCallFails(t *testing.T) {
	// Titles are cosmetic, liveness is not: a failing title listing degrades
	// to panes with empty titles instead of failing the sweep. The agent-state
	// listing is never reached on this early-return path, so agent states stay
	// empty even though the shim would answer.
	installLivePanesShim(t,
		`printf '%%9\t/wt/nine\n%%10\t/wt/ten\n'`,
		`exit 1`,
		`printf '%%9\trunning\n%%10\trunning\n'`)

	panes, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes() failed: %v", err)
	}
	want := []LivePane{
		{ID: "%9", CurrentPath: "/wt/nine", Title: ""},
		{ID: "%10", CurrentPath: "/wt/ten", Title: ""},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListLivePanes() = %#v, want %#v", panes, want)
	}
}

func TestListLivePanesFailsWhenPathCallFails(t *testing.T) {
	installLivePanesShim(t, `exit 1`, `printf '%%9\tnine\n'`, `printf '%%9\trunning\n'`)

	if _, err := ListLivePanes(); err == nil {
		t.Fatal("ListLivePanes() succeeded, want error when the path listing fails")
	}
}

func TestParseLivePanePaths(t *testing.T) {
	input := "%1\t/wt/one\n" +
		"%2\t/wt/two\twith\ttabs\n" + // path is the last field, so its tabs survive
		"%3\t\n" + // empty path kept
		"no-tab-at-all\n" + // missing path field: skipped
		"\t/wt/orphan\n" + // empty id: skipped
		"junk fragment of a newline-bearing path\t/x\n" + // non-%N id: skipped
		"%x\t/wt/bad\n" + // malformed pane id: skipped
		"   \n"

	got := parseLivePanePaths(input)

	want := []LivePane{
		{ID: "%1", CurrentPath: "/wt/one"},
		{ID: "%2", CurrentPath: "/wt/two\twith\ttabs"},
		{ID: "%3", CurrentPath: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLivePanePaths() = %#v, want %#v", got, want)
	}
}

func TestParseLivePanePathsTrimsCarriageReturns(t *testing.T) {
	got := parseLivePanePaths("%5\t/wt/five\r\n")

	want := []LivePane{{ID: "%5", CurrentPath: "/wt/five"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLivePanePaths() = %#v, want %#v", got, want)
	}
}

func TestParseLivePaneField(t *testing.T) {
	input := "%1\tfanout: child #12\n" +
		"%2\ttitle\twith\ttabs\n" + // field is the last value, so its tabs survive
		"%3\t\n" + // empty field kept
		"no-tab-at-all\n" + // missing field: skipped
		"not-an-id\ttitle\n" + // non-%N id: skipped
		"%6\tcrlf title\r\n"

	got := parseLivePaneField(input)

	want := map[string]string{
		"%1": "fanout: child #12",
		"%2": "title\twith\ttabs",
		"%3": "",
		"%6": "crlf title",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLivePaneField() = %#v, want %#v", got, want)
	}
}

func TestParseLivePaneFieldDropsDuplicateIDs(t *testing.T) {
	// 同一 id が複数回現れたら丸ごと捨てる(3 回以上でも復活しない): 本物と
	// 偽装行(改行入り option 値が注入した "%N\t<field>")は区別できないため、
	// last-wins で偽装行に上書きさせず保守的に「不明」へ degrade する。
	input := "%1\tgenuine\n" +
		"%2\tkept\n" +
		"%1\tforged\n" +
		"%1\tforged again\n"

	got := parseLivePaneField(input)

	want := map[string]string{"%2": "kept"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLivePaneField() = %#v, want %#v (duplicate %%1 dropped)", got, want)
	}
}

func TestListLivePanesDegradesToEmptyAgentStateWhenAgentStateCallFails(t *testing.T) {
	// agent 状態は表示専用で liveness の根拠ではない: agent-state listing の
	// 失敗は空値へ degrade し、sweep 自体は成功する。
	installLivePanesShim(t,
		`printf '%%9\t/wt/nine\n'`,
		`printf '%%9\tnine\n'`,
		`exit 1`)

	panes, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes() failed: %v", err)
	}
	want := []LivePane{{ID: "%9", CurrentPath: "/wt/nine", Title: "nine", AgentState: ""}}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListLivePanes() = %#v, want %#v", panes, want)
	}
}

func TestListLivePanesDropsAgentStateRowsForUnjoinedIDs(t *testing.T) {
	// path+title のクロスチェックを通過していない id の agent-state 行は捨て
	// られる: agent-state listing は検証済み id のルックアップ専用で、pane を
	// 生まない。
	installLivePanesShim(t,
		`printf '%%9\t/wt/nine\n'`,
		`printf '%%9\tnine\n'`,
		`printf '%%9\trunning\n%%5\tforged\n'`)

	panes, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes() failed: %v", err)
	}
	want := []LivePane{{ID: "%9", CurrentPath: "/wt/nine", Title: "nine", AgentState: "running"}}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListLivePanes() = %#v, want %#v", panes, want)
	}
}

func TestListLivePanesDropsForgedAgentStateLineForAnotherPane(t *testing.T) {
	// pane user option の値は pane 内プロセスが自由に設定できる(改行入り含む):
	// %2 が自分の @fanout_agent_state を "x\n%1\tdone" にすると、agent-state
	// listing に偽の "%1\tdone" 行が混ざり、%1 は 2 回現れる。duplicate-id drop
	// により %1 の agent 状態は ""(不明)へ degrade し、偽装行が running 中の
	// %1 を done に見せることはできない。%2 自身は "x" のままで、これは
	// sessionview 側の正規化で ""(不明)に落ちる。
	installLivePanesShim(t,
		`printf '%%1\t/wt/one\n%%2\t/wt/two\n'`,
		`printf '%%1\tone\n%%2\ttwo\n'`,
		`printf '%%1\trunning\n%%2\tx\n%%1\tdone\n'`)

	panes, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes() failed: %v", err)
	}
	want := []LivePane{
		{ID: "%1", CurrentPath: "/wt/one", Title: "one", AgentState: ""},
		{ID: "%2", CurrentPath: "/wt/two", Title: "two", AgentState: "x"},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListLivePanes() = %#v, want %#v", panes, want)
	}
}

func TestBindDashboardKeysRegistersRunShellWrappers(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := BindDashboardKeys("D", "F12", "/abs/path/fanout"); err != nil {
		t.Fatalf("BindDashboardKeys() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSuffix(string(body), "\n---\n"), "\n---\n")
	if len(commands) != 2 {
		t.Fatalf("tmux command count = %d, want 2\n%s", len(commands), body)
	}
	gotPrefix := strings.Split(commands[0], "\n")
	gotDirect := strings.Split(commands[1], "\n")
	launch := `__fanout_start_dir=#{?@fanout_project_root,#{q:@fanout_project_root},#{q:pane_current_path}}; __fanout_start_dir=$(printf '%s' "$__fanout_start_dir" | sed 's/#/####/g'); tmux -S #{q:socket_path} new-window -d -n fanout-dashboard -t #{q:session_id}: -c "$__fanout_start_dir" -e 'FANOUT_DASHBOARD_NOTIFY_CLIENT=#{client_tty}' '/abs/path/fanout dashboard --web --open'`
	wantPrefix := []string{
		"bind-key", "D", "run-shell", "-b", launch,
	}
	wantDirect := []string{
		"bind-key", "-n", "F12", "run-shell", "-b", launch,
	}
	if strings.Join(gotPrefix, "\x00") != strings.Join(wantPrefix, "\x00") {
		t.Fatalf("prefix tmux args = %#v, want %#v", gotPrefix, wantPrefix)
	}
	if strings.Join(gotDirect, "\x00") != strings.Join(wantDirect, "\x00") {
		t.Fatalf("direct tmux args = %#v, want %#v", gotDirect, wantDirect)
	}
}

func TestDashboardRunShellCommandCarriesExpandedClientAndStartDir(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "work tree")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	command := dashboardNewWindowShellCommand("/opt/My Tools/fanout", shellQuote(workDir))
	command = strings.ReplaceAll(command, "#{client_tty}", "/dev/ttys123")
	wantLaunch := shellQuote(shellQuote("/opt/My Tools/fanout") + " dashboard --web --open")

	wantParts := []string{
		"__fanout_start_dir=" + shellQuote(workDir),
		`__fanout_start_dir=$(printf '%s' "$__fanout_start_dir" | sed 's/#/####/g')`,
		"tmux -S #{q:socket_path} new-window -d -n fanout-dashboard",
		"-t #{q:session_id}:",
		`-c "$__fanout_start_dir"`,
		"-e 'FANOUT_DASHBOARD_NOTIFY_CLIENT=/dev/ttys123'",
		wantLaunch,
	}
	for _, part := range wantParts {
		if !strings.Contains(command, part) {
			t.Fatalf("dashboard command %q missing %q", command, part)
		}
	}
	if strings.Contains(command, "#{client_tty}") || !strings.Contains(command, "#{q:socket_path}") {
		t.Fatalf("dashboard command should carry expanded client and socket target: %q", command)
	}
}

func TestDashboardRunShellCommandEscapesHashForTmuxFormatLayer(t *testing.T) {
	command := dashboardNewWindowShellCommand("/opt/proj#2/fanout", "/repo")
	wantLaunch := shellQuote(shellQuote("/opt/proj##2/fanout") + " dashboard --web --open")
	if !strings.Contains(command, wantLaunch) {
		t.Fatalf("dashboard command = %q, want escaped launch %q", command, wantLaunch)
	}
	if strings.Contains(command, "/opt/proj#2/fanout") {
		t.Fatalf("dashboard command = %q, want literal '#' doubled for run-shell format expansion", command)
	}
	if !strings.Contains(command, "#{q:socket_path}") || !strings.Contains(command, "#{client_tty}") {
		t.Fatalf("dashboard command = %q, want intentional tmux formats preserved", command)
	}
}

func TestDashboardRunShellCommandEscapesStartDirHashForNestedTmux(t *testing.T) {
	startDir := shellQuote("/tmp/project #(demo)")
	command := dashboardNewWindowShellCommand("/opt/fanout", startDir)

	wantParts := []string{
		"__fanout_start_dir=" + startDir,
		`__fanout_start_dir=$(printf '%s' "$__fanout_start_dir" | sed 's/#/####/g')`,
		`-c "$__fanout_start_dir"`,
	}
	for _, part := range wantParts {
		if !strings.Contains(command, part) {
			t.Fatalf("dashboard command %q missing %q", command, part)
		}
	}
	if strings.Contains(command, "-c "+startDir) {
		t.Fatalf("dashboard command = %q, want start dir passed through escaped variable", command)
	}
}

func TestBindDashboardKeysRejectsEmptyArgs(t *testing.T) {
	if err := BindDashboardKeys("", "F12", "/abs/fanout"); err == nil {
		t.Fatal("BindDashboardKeys(empty prefix key) should error")
	}
	if err := BindDashboardKeys("D", "", "/abs/fanout"); err == nil {
		t.Fatal("BindDashboardKeys(empty direct key) should error")
	}
	if err := BindDashboardKeys("D", "F12", ""); err == nil {
		t.Fatal("BindDashboardKeys(empty bin) should error")
	}
}

func TestUnbindDashboardKeysRemovesPrefixAndDirectBindings(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
if [ "$1" = list-keys ] && [ "$3" = prefix ] && [ "$4" = D ]; then
  printf 'bind-key D run-shell -b /abs/fanout dashboard --web --open; tmux new-window -n fanout-dashboard\n'
fi
if [ "$1" = list-keys ] && [ "$3" = root ] && [ "$4" = F12 ]; then
  printf 'bind-key -n F12 run-shell -b /abs/fanout dashboard --web --open; tmux new-window -n fanout-dashboard\n'
fi
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := UnbindDashboardKeys("D", "F12"); err != nil {
		t.Fatalf("UnbindDashboardKeys() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSuffix(string(body), "\n---\n"), "\n---\n")
	if len(commands) != 4 {
		t.Fatalf("tmux command count = %d, want 4\n%s", len(commands), body)
	}
	if got := strings.Split(commands[0], "\n"); strings.Join(got, "\x00") != strings.Join([]string{"list-keys", "-T", "prefix", "D"}, "\x00") {
		t.Fatalf("prefix list tmux args = %#v", got)
	}
	if got := strings.Split(commands[1], "\n"); strings.Join(got, "\x00") != strings.Join([]string{"unbind-key", "-q", "D"}, "\x00") {
		t.Fatalf("prefix unbind tmux args = %#v", got)
	}
	if got := strings.Split(commands[2], "\n"); strings.Join(got, "\x00") != strings.Join([]string{"list-keys", "-T", "root", "F12"}, "\x00") {
		t.Fatalf("direct list tmux args = %#v", got)
	}
	if got := strings.Split(commands[3], "\n"); strings.Join(got, "\x00") != strings.Join([]string{"unbind-key", "-q", "-n", "F12"}, "\x00") {
		t.Fatalf("direct unbind tmux args = %#v", got)
	}
}

func TestUnbindDashboardKeysKeepsNonFanoutBindings(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
if [ "$1" = list-keys ] && [ "$3" = prefix ] && [ "$4" = D ]; then
  printf 'bind-key D send-prefix\n'
fi
if [ "$1" = list-keys ] && [ "$3" = root ] && [ "$4" = F12 ]; then
  printf 'bind-key -n F12 display-message user-binding\n'
fi
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := UnbindDashboardKeys("D", "F12"); err != nil {
		t.Fatalf("UnbindDashboardKeys() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "unbind-key") {
		t.Fatalf("tmux log unbound non-fanout bindings:\n%s", body)
	}
}

func TestBindConsoleKeysRegistersRunShellBindings(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := BindConsoleKeys("T", "F11", "/abs/path/fanout"); err != nil {
		t.Fatalf("BindConsoleKeys() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSuffix(string(body), "\n---\n"), "\n---\n")
	if len(commands) != 2 {
		t.Fatalf("tmux command count = %d, want 2\n%s", len(commands), body)
	}
	gotPrefix := strings.Split(commands[0], "\n")
	gotDirect := strings.Split(commands[1], "\n")
	// Each tmux argv on its own line: bind-key T run-shell <launch>, with
	// #{pane_id} / #{client_name} left for tmux to expand at keypress time and
	// a display-message tail that keeps a failed keypress out of tmux's error
	// view.
	launch := `/abs/path/fanout focus-console --from "#{pane_id}" --client "#{client_name}" >/dev/null 2>&1` +
		` || tmux display-message "fanout: focus-console failed; restart fanout to refresh this key"`
	wantPrefix := []string{"bind-key", "T", "run-shell", launch}
	wantDirect := []string{"bind-key", "-n", "F11", "run-shell", launch}
	if strings.Join(gotPrefix, "\x00") != strings.Join(wantPrefix, "\x00") {
		t.Fatalf("prefix tmux args = %#v, want %#v", gotPrefix, wantPrefix)
	}
	if strings.Join(gotDirect, "\x00") != strings.Join(wantDirect, "\x00") {
		t.Fatalf("direct tmux args = %#v, want %#v", gotDirect, wantDirect)
	}
}

func TestBindConsoleKeysQuotesBinaryPathWithSpaces(t *testing.T) {
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

	if err := BindConsoleKeys("T", "F11", "/opt/My Tools/fanout"); err != nil {
		t.Fatalf("BindConsoleKeys() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	// run-shell runs the launch arg through one shell, so the binary path is
	// single-quoted to survive word-splitting on "My Tools".
	launch := got[len(got)-1]
	if !strings.HasPrefix(launch, `'/opt/My Tools/fanout' focus-console --from "#{pane_id}"`) {
		t.Fatalf("launch arg = %q, want the binary path single-quoted", launch)
	}
}

func TestBindConsoleKeysEscapesHashForTmuxFormatLayer(t *testing.T) {
	// run-shell format-expands the launch string at keypress time; shell
	// quotes do not stop that layer, so a '#' in the install path must be
	// doubled or tmux mangles the path before the shell ever runs.
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

	if err := BindConsoleKeys("T", "F11", "/opt/proj#2/fanout"); err != nil {
		t.Fatalf("BindConsoleKeys() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	launch := got[len(got)-1]
	if !strings.HasPrefix(launch, `'/opt/proj##2/fanout' focus-console`) {
		t.Fatalf("launch arg = %q, want '#' doubled for the format layer", launch)
	}
}

func TestBindConsoleKeysRejectsEmptyArgs(t *testing.T) {
	if err := BindConsoleKeys("", "F11", "/abs/fanout"); err == nil {
		t.Fatal("BindConsoleKeys(empty prefix key) should error")
	}
	if err := BindConsoleKeys("T", "", "/abs/fanout"); err == nil {
		t.Fatal("BindConsoleKeys(empty direct key) should error")
	}
	if err := BindConsoleKeys("T", "F11", ""); err == nil {
		t.Fatal("BindConsoleKeys(empty bin) should error")
	}
}

func TestUnbindConsoleKeysRemovesPrefixAndDirectBindings(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
if [ "$1" = list-keys ] && [ "$3" = prefix ] && [ "$4" = T ]; then
  printf 'bind-key T run-shell /abs/fanout focus-console --from "#{pane_id}"; tmux display-message "fanout: focus-console failed"\n'
fi
if [ "$1" = list-keys ] && [ "$3" = root ] && [ "$4" = F11 ]; then
  printf 'bind-key -n F11 run-shell /abs/fanout focus-console --from "#{pane_id}"; tmux display-message "fanout: focus-console failed"\n'
fi
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := UnbindConsoleKeys("T", "F11"); err != nil {
		t.Fatalf("UnbindConsoleKeys() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSuffix(string(body), "\n---\n"), "\n---\n")
	if len(commands) != 4 {
		t.Fatalf("tmux command count = %d, want 4\n%s", len(commands), body)
	}
	if got := strings.Split(commands[0], "\n"); strings.Join(got, "\x00") != strings.Join([]string{"list-keys", "-T", "prefix", "T"}, "\x00") {
		t.Fatalf("prefix list tmux args = %#v", got)
	}
	if got := strings.Split(commands[1], "\n"); strings.Join(got, "\x00") != strings.Join([]string{"unbind-key", "-q", "T"}, "\x00") {
		t.Fatalf("prefix unbind tmux args = %#v", got)
	}
	if got := strings.Split(commands[2], "\n"); strings.Join(got, "\x00") != strings.Join([]string{"list-keys", "-T", "root", "F11"}, "\x00") {
		t.Fatalf("direct list tmux args = %#v", got)
	}
	if got := strings.Split(commands[3], "\n"); strings.Join(got, "\x00") != strings.Join([]string{"unbind-key", "-q", "-n", "F11"}, "\x00") {
		t.Fatalf("direct unbind tmux args = %#v", got)
	}
}

func TestBindWorktreeActionKeyRegistersPopup(t *testing.T) {
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

	if err := BindWorktreeActionKey("M", "/abs/path/fanout"); err != nil {
		t.Fatalf("BindWorktreeActionKey() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	want := []string{
		"bind-key", "M", "display-popup", "-E",
		"-b", popupBorderLines,
		"-S", popupBorderStyle,
		"-d", "#{?@fanout_project_root,#{@fanout_project_root},#{pane_current_path}}",
		"/abs/path/fanout __worktree-action --pane #{pane_id}",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tmux args = %#v, want %#v", got, want)
	}
}

func TestBindWorktreeActionKeyRejectsEmptyArgs(t *testing.T) {
	if err := BindWorktreeActionKey("", "/abs/fanout"); err == nil {
		t.Fatal("BindWorktreeActionKey(empty key) should error")
	}
	if err := BindWorktreeActionKey("M", ""); err == nil {
		t.Fatal("BindWorktreeActionKey(empty bin) should error")
	}
}

func TestUnbindWorktreeActionKeyRemovesPrefixBinding(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
if [ "$1" = list-keys ] && [ "$3" = prefix ] && [ "$4" = M ]; then
  printf 'bind-key M display-popup -E /abs/fanout __worktree-action --pane #{pane_id}\n'
fi
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXRUN_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := UnbindWorktreeActionKey("M"); err != nil {
		t.Fatalf("UnbindWorktreeActionKey() failed: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	want := []string{"unbind-key", "-q", "M"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tmux args = %#v, want %#v", got, want)
	}
}

func TestSetPaneProjectRoot(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SetPaneProjectRoot("%42", "/tmp/My Repo"); err != nil {
		t.Fatalf("SetPaneProjectRoot() failed: %v", err)
	}

	assertTmuxArgs(t, argsPath, []string{
		"set-option", "-p", "-t", "%42", "@fanout_project_root", "/tmp/My Repo",
	})
}

func TestSetPaneWorktreePath(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SetPaneWorktreePath("%42", "/tmp/My Worktree"); err != nil {
		t.Fatalf("SetPaneWorktreePath() failed: %v", err)
	}

	assertTmuxArgs(t, argsPath, []string{
		"set-option", "-p", "-t", "%42", "@fanout_worktree_path", "/tmp/My Worktree",
	})
}

func TestSetPaneShellKey(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SetPaneShellKey("%42", "shell-token"); err != nil {
		t.Fatalf("SetPaneShellKey() failed: %v", err)
	}

	assertTmuxArgs(t, argsPath, []string{
		"set-option", "-p", "-t", "%42", "@fanout_shell_key", "shell-token",
	})
}

func TestWaitForLockCommandQuotesChannel(t *testing.T) {
	got := WaitForLockCommand("gate; touch /tmp/untrusted")
	want := "tmux wait-for -L 'gate; touch /tmp/untrusted' && tmux wait-for -U 'gate; touch /tmp/untrusted'"
	if got != want {
		t.Fatalf("WaitForLockCommand() = %q, want %q", got, want)
	}
	if got := WaitForLockCommand("  "); got != "" {
		t.Fatalf("WaitForLockCommand(empty) = %q, want empty", got)
	}
}

func TestWaitChannelLockAndUnlock(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(string) error
		flag string
	}{
		{name: "lock", run: LockWaitChannel, flag: "-L"},
		{name: "unlock", run: UnlockWaitChannel, flag: "-U"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)
			if err := tt.run("fanout-start-shell-token"); err != nil {
				t.Fatalf("wait channel %s failed: %v", tt.name, err)
			}
			assertTmuxArgs(t, argsPath, []string{"wait-for", tt.flag, "fanout-start-shell-token"})
		})
	}
}

func TestSetPaneAgentState(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SetPaneAgentState("%42", "plan"); err != nil {
		t.Fatalf("SetPaneAgentState() failed: %v", err)
	}

	assertTmuxArgs(t, argsPath, []string{
		"set-option", "-p", "-t", "%42", "@fanout_agent_state", "plan",
	})
}

// TestSetPaneAgentStateSkipsEmptyArgs pins the deliberate divergence from the
// other SetPane* helpers: the state is display-only telemetry, so missing
// arguments are a silent no-op instead of an error, and tmux is never invoked.
func TestSetPaneAgentStateSkipsEmptyArgs(t *testing.T) {
	tests := []struct {
		name   string
		paneID string
		state  string
	}{
		{name: "empty pane id", paneID: "", state: "plan"},
		{name: "empty state", paneID: "%42", state: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

			if err := SetPaneAgentState(tt.paneID, tt.state); err != nil {
				t.Fatalf("SetPaneAgentState(%q, %q) = %v, want nil no-op", tt.paneID, tt.state, err)
			}
			if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
				t.Fatalf("tmux was invoked (args file %v), want no invocation", err)
			}
		})
	}
}

func TestSetPaneLabel(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := SetPaneLabel("%42", "#123 · fix-login-bug-123"); err != nil {
		t.Fatalf("SetPaneLabel() failed: %v", err)
	}

	assertTmuxArgs(t, argsPath, []string{
		"set-option", "-p", "-t", "%42", "@fanout_pane_label", "#123 · fix-login-bug-123",
	})
}

func TestSetPaneLabelRequiresPaneID(t *testing.T) {
	if err := SetPaneLabel("", "label"); err == nil {
		t.Fatal("SetPaneLabel(empty pane id) should error")
	}
}

func TestNeutralizePaneLabel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain label is untouched", "#123 · fix-login-bug-123", "#123 · fix-login-bug-123"},
		{"single style sequence is defused", "#123 · v2 #[fg=red]ship", "#123 · v2 [fg=red]ship"},
		// A leading extra "#" must not let the style survive: ReplaceAll("#[","[")
		// would leave "#[fg=red]" here, so the run-stripping form is required.
		{"overlapping hashes are fully defused", "#123 · ##[fg=red]x", "#123 · [fg=red]x"},
		{"a run of hashes before a bracket collapses", "###[bold]", "[bold]"},
		{"a bracket without a leading hash is kept", "name[0]", "name[0]"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeutralizePaneLabel(tc.in); got != tc.want {
				t.Errorf("NeutralizePaneLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSetPaneLabelNeutralizesStyleSequence(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	// A "#[" from a --name override must not reach the border as a tmux style.
	if err := SetPaneLabel("%42", "#123 · v2 #[fg=red]ship"); err != nil {
		t.Fatalf("SetPaneLabel() failed: %v", err)
	}

	assertTmuxArgs(t, argsPath, []string{
		"set-option", "-p", "-t", "%42", "@fanout_pane_label", "#123 · v2 [fg=red]ship",
	})
}

func TestEnablePaneBorderTitles(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
`)

	if err := EnablePaneBorderTitles("%42"); err != nil {
		t.Fatalf("EnablePaneBorderTitles() failed: %v", err)
	}

	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"set-option", "-w", "-t", "%42", "pane-border-status", "top", "---",
		"set-option", "-w", "-t", "%42", "pane-border-format", paneBorderFormat, "---",
		"set-option", "-w", "-t", "%42", "pane-active-border-style", paneActiveBorderStyle, "---",
		"set-option", "-w", "-t", "%42", "pane-border-style", paneBorderStyle, "---",
	}, "\n") + "\n"
	if string(body) != want {
		t.Fatalf("tmux args body = %q, want %q", string(body), want)
	}
}

func TestEnablePaneBorderTitlesRequiresPaneID(t *testing.T) {
	if err := EnablePaneBorderTitles(""); err == nil {
		t.Fatal("EnablePaneBorderTitles(empty pane id) should error")
	}
}

// TestAgentStateSetCommand pins the shared one-liner every in-pane state write
// embeds (the launch wrapper and the Claude hooks injected by internal/agent):
// contract values stay bare tokens, hostile values are quoted so a state can
// never break out of the command.
func TestAgentStateSetCommand(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{
			name:  "contract value passes bare",
			state: "working",
			want:  `tmux set-option -p -t "$TMUX_PANE" @fanout_agent_state working 2>/dev/null`,
		},
		{
			name:  "hostile value is quoted",
			state: "pwned; rm -rf /",
			want:  `tmux set-option -p -t "$TMUX_PANE" @fanout_agent_state 'pwned; rm -rf /' 2>/dev/null`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AgentStateSetCommand(tt.state); got != tt.want {
				t.Fatalf("AgentStateSetCommand(%q) = %q, want %q", tt.state, got, tt.want)
			}
		})
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

func TestBuildPaneLaunchCommandReportsAgentStateAroundAgentRun(t *testing.T) {
	// ラッパーは agent 実行状態を pane user option で明示する: agent 起動前に
	// "running"、終了ステータス取得後に "done"。#{pane_current_command} は
	// 非対話 sh ラッパー経由だと agent 実行中もシェル名を返すため使えない。
	got := BuildPaneLaunchCommand("/tmp/bin/claude 'prompt'")
	// -t "$TMUX_PANE" は必須: 素の set-option -p はウィンドウの active pane
	// (= split-window -d では呼び出し元 pane)に解決されてしまう。
	setRunning := `tmux set-option -p -t "$TMUX_PANE" @fanout_agent_state running 2>/dev/null; `
	setDone := `tmux set-option -p -t "$TMUX_PANE" @fanout_agent_state done 2>/dev/null; `
	runningAt := strings.Index(got, setRunning)
	agentAt := strings.Index(got, "/tmp/bin/claude")
	statusAt := strings.Index(got, "__fanout_status=$?")
	doneAt := strings.Index(got, setDone)
	if runningAt < 0 || agentAt < 0 || statusAt < 0 || doneAt < 0 {
		t.Fatalf("BuildPaneLaunchCommand() = %q, want running/agent/status/done markers", got)
	}
	if runningAt >= agentAt || agentAt >= statusAt || statusAt >= doneAt {
		t.Fatalf("BuildPaneLaunchCommand() = %q, want order running < agent < status capture < done", got)
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

func TestActivePaneInWindowReturnsActivePaneForTargetWindow(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "has-session" ]; then
	exit 1
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
printf '%%1:@2:0:0:fanout tui\n%%2:@2:1:1:child pane\n'
`)

	got, err := ActivePaneInWindow("%1")
	if err != nil {
		t.Fatalf("ActivePaneInWindow() failed: %v", err)
	}
	if got != "%2" {
		t.Fatalf("ActivePaneInWindow() = %q, want %%2", got)
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-t", "%1", "-F", paneListFormat})
}

func TestActivePaneInWindowReturnsEmptyForNoTargetOrNoActivePane(t *testing.T) {
	if got, err := ActivePaneInWindow(""); err != nil || got != "" {
		t.Fatalf("ActivePaneInWindow(empty) = %q, %v; want empty nil", got, err)
	}

	installTmuxShim(t, `if [ "$1" = "has-session" ]; then
	exit 1
fi
printf '%%1:@2:0:0:fanout tui\n%%2:@2:1:0:child pane\n'
`)
	got, err := ActivePaneInWindow("%1")
	if err != nil {
		t.Fatalf("ActivePaneInWindow() failed: %v", err)
	}
	if got != "" {
		t.Fatalf("ActivePaneInWindow() = %q, want empty when no pane is active", got)
	}
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

func TestZoomPaneZoomsWhenWindowNotZoomed(t *testing.T) {
	argsPath := installTmuxShim(t, `if [ "$1" = "display-message" ]; then
	printf '0\n'
	exit 0
fi
printf '%s\n' "$@" > "$TMUXRUN_ARGS"
`)

	if err := ZoomPane("%9"); err != nil {
		t.Fatalf("ZoomPane() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"resize-pane", "-Z", "-t", "%9"})
}

// Guards that ZoomPane never toggles an already-zoomed window back to the
// split layout: the shim fails any command other than the zoom-flag query.
func TestZoomPaneSkipsToggleWhenWindowAlreadyZoomed(t *testing.T) {
	installTmuxShim(t, `if [ "$1" = "display-message" ]; then
	printf '1\n'
	exit 0
fi
exit 1
`)

	if err := ZoomPane("%9"); err != nil {
		t.Fatalf("ZoomPane() on zoomed window failed: %v", err)
	}
}

func TestZoomPaneRejectsEmptyPaneID(t *testing.T) {
	installTmuxShim(t, `exit 0
`)

	if err := ZoomPane("  "); err == nil {
		t.Fatal("ZoomPane(\"  \") = nil, want error")
	}
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

func TestSendLiteralLineTypesTextThenEnter(t *testing.T) {
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
`)

	if err := SendLiteralLine("%3", "[fanout] peer message"); err != nil {
		t.Fatalf("SendLiteralLine() failed: %v", err)
	}
	// Two calls: the literal text (-l so key names stay text; -- so a leading
	// dash is not parsed as a flag) then a bare Enter, both targeting the pane
	// id directly (no "=" session prefix).
	assertTmuxArgs(t, argsPath, []string{
		"send-keys", "-t", "%3", "-l", "--", "[fanout] peer message", "---",
		"send-keys", "-t", "%3", "Enter", "---",
	})
}

func TestSendLiteralLineTypesDashLeadingTextLiterally(t *testing.T) {
	// The "--" terminator lets a payload beginning with "-" through as literal
	// text instead of an unknown flag — the hardening that matters for future
	// reusers of this primitive.
	argsPath := installTmuxShim(t, `printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
`)

	if err := SendLiteralLine("%3", "-n dash-leading"); err != nil {
		t.Fatalf("SendLiteralLine() failed: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{
		"send-keys", "-t", "%3", "-l", "--", "-n dash-leading", "---",
		"send-keys", "-t", "%3", "Enter", "---",
	})
}

func TestSendLiteralLineRejectsEmptyPaneID(t *testing.T) {
	if err := SendLiteralLine("", "hi"); err == nil {
		t.Fatal("SendLiteralLine(empty pane id) should error")
	}
}

func TestSendLiteralLineErrorsWhenLiteralSendFails(t *testing.T) {
	installTmuxShim(t, `exit 1
`)

	if err := SendLiteralLine("%3", "hi"); err == nil {
		t.Fatal("SendLiteralLine() succeeded, want error when the literal send-keys fails")
	}
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

func TestListLivePanesDropsDuplicateForgedIDs(t *testing.T) {
	// A newline-bearing cwd can forge a second "%5" row pointing at a recorded
	// worktree. %5 exists in the (unforgeable) title listing, so presence
	// alone would admit the forgery — a duplicated id must be dropped entirely
	// because the genuine row cannot be told apart from the forged one.
	installLivePanesShim(t,
		`printf '%%5\t/real/path\n%%5\t/wt/recorded\n%%7\t/other\n'`,
		`printf '%%5\treal title\n%%7\tother title\n'`,
		`printf '%%5\trunning\n%%7\tdone\n'`)

	panes, err := ListLivePanes()
	if err != nil {
		t.Fatalf("ListLivePanes() failed: %v", err)
	}
	want := []LivePane{{ID: "%7", CurrentPath: "/other", Title: "other title", AgentState: "done"}}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListLivePanes() = %#v, want only %%7 (duplicate %%5 dropped): %#v", panes, want)
	}
}

// installPlanCaptureShim installs a tmux shim answering CapturePlanSource's
// calls: display-message (alternate_on の問い合わせ) は altOn を、履歴 capture
// は histBody を、alternate screen capture は altBody を返す。argv は呼び出し
// ごとに "---" 行で区切って args ファイルへ追記される。
func installPlanCaptureShim(t *testing.T, altOn, histBody, altBody string) string {
	t.Helper()
	script := `printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf '%s\n' '---' >> "$TMUXRUN_ARGS"
case "$1" in
display-message)
	printf '%s\n' '` + altOn + `'
	;;
capture-pane)
	if [ "$2" = "-a" ]; then
		printf '%s\n' '` + altBody + `'
	else
		printf '%s\n' '` + histBody + `'
	fi
	;;
esac
`
	return installTmuxShim(t, script)
}

// splitShimInvocations は "---" 区切りの args ファイル行を呼び出しごとの argv
// に分解する。
func splitShimInvocations(lines []string) [][]string {
	var out [][]string
	var cur []string
	for _, line := range lines {
		if line == "---" {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return out
}

func TestCapturePlanSourceCapturesJoinedHistory(t *testing.T) {
	argsPath := installPlanCaptureShim(t, "0", "history with plan", "alt screen")

	out, err := CapturePlanSource("%7", 2000)
	if err != nil {
		t.Fatalf("CapturePlanSource() failed: %v", err)
	}
	if out != "history with plan\n" {
		t.Fatalf("output = %q, want history only (alternate screen off)", out)
	}
	calls := splitShimInvocations(readTmuxArgs(t, argsPath))
	if len(calls) != 2 {
		t.Fatalf("tmux calls = %d (%#v), want history capture + alternate_on probe", len(calls), calls)
	}
	// -J が折返し行を結合し、ペイン幅で分断された <proposed_plan> タグを
	// 一行に戻す。-S -<lines> は通常スクリーンの履歴側にだけ付く。
	wantHist := []string{"capture-pane", "-p", "-J", "-t", "%7", "-S", "-2000"}
	if strings.Join(calls[0], "\x00") != strings.Join(wantHist, "\x00") {
		t.Fatalf("history capture args = %#v, want %#v", calls[0], wantHist)
	}
}

func TestCapturePlanSourceAppendsAlternateScreenLast(t *testing.T) {
	argsPath := installPlanCaptureShim(t, "1", "history with plan", "alt screen render")

	out, err := CapturePlanSource("%7", 2000)
	if err != nil {
		t.Fatalf("CapturePlanSource() failed: %v", err)
	}
	// alternate screen は末尾連結 — last-block 検索で最新レンダが勝つ。
	if out != "history with plan\n\nalt screen render\n" {
		t.Fatalf("output = %q, want history + alternate screen appended last", out)
	}
	calls := splitShimInvocations(readTmuxArgs(t, argsPath))
	if len(calls) != 3 {
		t.Fatalf("tmux calls = %d (%#v), want history + probe + alternate capture", len(calls), calls)
	}
	wantAlt := []string{"capture-pane", "-a", "-p", "-J", "-t", "%7"}
	if strings.Join(calls[2], "\x00") != strings.Join(wantAlt, "\x00") {
		t.Fatalf("alternate capture args = %#v, want %#v", calls[2], wantAlt)
	}
}

func TestCapturePlanSourceRejectsBadArgs(t *testing.T) {
	if _, err := CapturePlanSource("", 100); err == nil {
		t.Fatal("CapturePlanSource(\"\") = nil error, want pane-id error")
	}
	if _, err := CapturePlanSource("%1", -1); err == nil {
		t.Fatal("CapturePlanSource(lines=-1) = nil error, want lines error")
	}
}

func TestExtendedKeysNeedsEnable(t *testing.T) {
	// An explicit on/always is preserved; everything else (off, unknown, empty)
	// is treated as "needs enabling".
	for _, keep := range []string{"on", "always", "ON", " Always "} {
		if extendedKeysNeedsEnable(keep) {
			t.Fatalf("extendedKeysNeedsEnable(%q) = true, want false (preserve explicit value)", keep)
		}
	}
	for _, enable := range []string{"off", "", "   ", "garbage"} {
		if !extendedKeysNeedsEnable(enable) {
			t.Fatalf("extendedKeysNeedsEnable(%q) = false, want true", enable)
		}
	}
}

func TestTerminalFeaturesHaveExtkeys(t *testing.T) {
	feats := "terminal-features[0] xterm*:clipboard:title\nterminal-features[4] xterm-256color:extkeys"
	if !terminalFeaturesHaveExtkeys(feats, "xterm-256color") {
		t.Fatal("terminalFeaturesHaveExtkeys() = false, want true when the entry is present")
	}
	if terminalFeaturesHaveExtkeys(feats, "xterm-ghostty") {
		t.Fatal("terminalFeaturesHaveExtkeys() = true for a term that is not advertised")
	}
	if terminalFeaturesHaveExtkeys("terminal-features[0] xterm*:clipboard", "xterm-256color") {
		t.Fatal("terminalFeaturesHaveExtkeys() = true when extkeys is absent")
	}
	if terminalFeaturesHaveExtkeys("anything:extkeys", "") {
		t.Fatal("terminalFeaturesHaveExtkeys() = true for an empty term")
	}
}
