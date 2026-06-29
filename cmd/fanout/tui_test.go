package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
	"github.com/butaosuinu/fanout/internal/watch"
)

func TestTUIAgentOrDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "codex", raw: "codex", want: "codex"},
		{name: "claude", raw: "claude", want: "claude"},
		{name: "unknown", raw: "other", want: "claude"},
		{name: "empty", raw: "", want: "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tuiAgentOrDefault(tc.raw); got != tc.want {
				t.Fatalf("tuiAgentOrDefault(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCmdTUIRegistersDashboardKeybinds(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	writeTUITestStateFile(t, repo)
	t.Chdir(repo)
	t.Setenv("TMUX", "tmux-session")
	t.Setenv("TMUX_PANE", "%tui")
	argsPath := installTUIDashboardTmuxShim(t)
	restoreRunTUI := stubRunTUI(t)
	defer restoreRunTUI()

	code := cmdTUI("fanout", discardLogger())
	if code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK", code)
	}

	log := readTUITmuxLog(t, argsPath)
	if !tmuxLogHasCommand(log, "bind-key\nD\nnew-window") {
		t.Fatalf("tmux log missing prefix dashboard keybind:\n%s", log)
	}
	if !tmuxLogHasCommand(log, "bind-key\n-n\nF12\nnew-window") {
		t.Fatalf("tmux log missing direct dashboard keybind:\n%s", log)
	}
}

func TestCmdTUINoDashboardKeybindHonorsEnv(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	writeTUITestStateFile(t, repo)
	t.Chdir(repo)
	t.Setenv("TMUX", "tmux-session")
	t.Setenv("TMUX_PANE", "%tui")
	t.Setenv("FANOUT_DASHBOARD_KEYBIND", "0")
	argsPath := installTUIDashboardTmuxShim(t)
	restoreRunTUI := stubRunTUI(t)
	defer restoreRunTUI()

	code := cmdTUI("fanout", discardLogger())
	if code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK", code)
	}

	log := readTUITmuxLog(t, argsPath)
	if strings.Contains(log, "bind-key\n") {
		t.Fatalf("tmux log should not contain dashboard keybinds when disabled:\n%s", log)
	}
}

func TestTUINewPanePopupGeometryUsesClientDimensions(t *testing.T) {
	got, err := tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 160, Height: 50})
	if err != nil {
		t.Fatal(err)
	}
	want := tuiNewPanePopupGeometry{PopupWidth: 90, PopupHeight: 40, PromptWidth: 88, PromptHeight: 38}
	if got != want {
		t.Fatalf("popup geometry = %#v, want %#v", got, want)
	}

	got, err = tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	want = tuiNewPanePopupGeometry{PopupWidth: 76, PopupHeight: 20, PromptWidth: 74, PromptHeight: 18}
	if got != want {
		t.Fatalf("80x24 popup geometry = %#v, want %#v", got, want)
	}

	got, err = tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 20})
	if err != nil {
		t.Fatal(err)
	}
	want = tuiNewPanePopupGeometry{PopupWidth: 76, PopupHeight: 20, PromptWidth: 74, PromptHeight: 18}
	if got != want {
		t.Fatalf("small client popup geometry = %#v, want %#v", got, want)
	}

	if _, err := tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 40, Height: 20}); err == nil {
		t.Fatal("tuiNewPanePopupGeometryForClient() succeeded for too-small client")
	}
	if _, err := tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 19}); err == nil {
		t.Fatal("tuiNewPanePopupGeometryForClient() succeeded without enough prompt height")
	}
}

func TestTUINewPanePopupResultRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	want := tuiNewPanePopupResult{Prompt: "Inspect API", Agents: []string{"codex"}}
	if err := writeTUINewPanePopupResult(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readTUINewPanePopupResult(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != want.Prompt || !reflect.DeepEqual(got.Agents, want.Agents) || got.Canceled {
		t.Fatalf("popup result = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("popup result mode = %o, want 600", got)
	}
}

func TestNewPopupResultPathsUsesPrivateDirectory(t *testing.T) {
	resultPath, donePath, cleanup, err := newPopupResultPaths()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	dir := filepath.Dir(resultPath)
	if filepath.Dir(donePath) != dir {
		t.Fatalf("result dir = %q, done dir = %q, want same private dir", dir, filepath.Dir(donePath))
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("private result dir mode = %o, want 700", got)
	}
	if _, err := os.Stat(resultPath); !os.IsNotExist(err) {
		t.Fatalf("result file stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(donePath); !os.IsNotExist(err) {
		t.Fatalf("done file stat error = %v, want not exist", err)
	}
}

func TestWaitForTUINewPanePopupResultTreatsDoneWithoutResultAsCancel(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result.json")
	donePath := filepath.Join(dir, "result.done")
	if err := os.WriteFile(donePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := waitForTUINewPanePopupResult(resultPath, donePath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Canceled {
		t.Fatalf("popup result = %#v, want canceled", got)
	}
}

func TestTUINewPanePopupShellCommandMarksDoneAndPropagatesEnhancedKeys(t *testing.T) {
	t.Setenv(fanouttui.EnhancedKeysEnv, "1")

	got := tuiNewPanePopupShellCommand("fanout", "/tmp/repo", "/tmp/result.json", "/tmp/result.done", "codex", 80, 18)
	for _, want := range []string{
		"trap ",
		"EXIT HUP INT TERM",
		"/tmp/result.done",
		fanouttui.EnhancedKeysEnv + "=1 ",
		tuiNewPanePopupCommand,
		"--result-file /tmp/result.json",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("popup shell command missing %q:\n%s", want, got)
		}
	}
}

func TestManualPaneOptionsForTUIKeepsSingleLinePromptInline(t *testing.T) {
	opts := manualPaneOptionsForTUI("inspect workspace", "codex")

	if opts.Title != "inspect workspace" || opts.Prompt != "inspect workspace" {
		t.Fatalf("single-line title/prompt = %q/%q, want original", opts.Title, opts.Prompt)
	}
	if opts.Body != "" {
		t.Fatalf("single-line body = %q, want empty", opts.Body)
	}
	// manualPaneOptions no longer carries a slug; newManualPaneRequest always
	// auto-generates a unique synthetic slug from the title and pane number.
	if opts.Agent != "codex" {
		t.Fatalf("agent = %q, want codex", opts.Agent)
	}
}

func TestManualPaneOptionsForTUIMultilinePromptUsesBriefingBody(t *testing.T) {
	prompt := normalizeTUIPrompt("\n  inspect workspace\r\n\ncheck handlers\r")
	opts := manualPaneOptionsForTUI(prompt, "claude")

	if opts.Title != "inspect workspace" || opts.Prompt != "inspect workspace" {
		t.Fatalf("multiline title/prompt = %q/%q, want first non-empty line", opts.Title, opts.Prompt)
	}
	if opts.Body != "inspect workspace\n\ncheck handlers" {
		t.Fatalf("multiline body = %q, want normalized full prompt", opts.Body)
	}
}

func TestManualPaneConfigForTUIAgentEnablesCodexPlanMode(t *testing.T) {
	codex := manualPaneConfigForTUIAgent("codex")
	if codex.Agent != "codex" || !codex.CodexPlanModeEnabled() {
		t.Fatalf("codex config = %+v, want codex Plan Mode", codex)
	}

	claude := manualPaneConfigForTUIAgent("claude")
	if claude.Agent != "claude" || claude.CodexPlanMode != nil {
		t.Fatalf("claude config = %+v, want no Codex Plan Mode override", claude)
	}
}

func TestLaunchManualPaneFromTUIChecksAgentBeforeState(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	_, err := launchManualPaneFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.LaunchRequest{
		Prompt: "inspect workspace",
		Agents: []string{"claude"},
	})

	if err == nil || !strings.Contains(err.Error(), `agent "claude" is not installed`) {
		t.Fatalf("launchManualPaneFromTUI() error = %v, want missing claude", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".fanout")); !os.IsNotExist(statErr) {
		t.Fatalf(".fanout state was touched before agent validation: %v", statErr)
	}
}

func TestLaunchManualPaneFromTUICreatesMultipleAgentPanes(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	installFakeExecutable(t, "claude")
	installTUITmuxShim(t, "%77")

	_, err := launchManualPaneFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.LaunchRequest{
		Prompt: "inspect workspace",
		Agents: []string{"claude", "claude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 2 {
		t.Fatalf("state panes = %+v, want two manual panes", store.Panes)
	}
	// Empty slug → auto-generated; the unique synthetic number keeps the two panes distinct.
	if store.Panes[0].Slug != "manual-1-inspect-workspace-pane" || store.Panes[1].Slug != "manual-2-inspect-workspace-pane" {
		t.Fatalf("slugs = %q/%q, want auto-generated synthetic slugs", store.Panes[0].Slug, store.Panes[1].Slug)
	}
	if store.Panes[0].IssueNum != -1 || store.Panes[1].IssueNum != -2 {
		t.Fatalf("issue nums = %d/%d, want unique synthetic ids", store.Panes[0].IssueNum, store.Panes[1].IssueNum)
	}
}

func TestLaunchManualPaneFromTUIReportsPartialMultipleLaunch(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	installFakeExecutable(t, "claude")
	installTUITmuxShim(t, "%77")
	counter := filepath.Join(t.TempDir(), "hook-count")
	hookConfig := hooks.Config{Events: map[hooks.Type][]hooks.Command{
		hooks.WorktreeCreated: {{
			Command: "count=$(cat " + counter + " 2>/dev/null || printf 0); count=$((count + 1)); printf '%s' \"$count\" > " + counter + "; test \"$count\" -eq 1",
		}},
	}}

	notice, err := launchManualPaneFromTUI(repo, "fanout-test", "fanout", hookConfig, fanouttui.LaunchRequest{
		Prompt: "inspect workspace",
		Agents: []string{"claude", "claude"},
	})
	if err != nil {
		t.Fatalf("launchManualPaneFromTUI() error = %v, want partial success notice", err)
	}
	if !strings.Contains(notice, "created 1 new agent pane(s); stopped after a later pane failed") {
		t.Fatalf("notice = %q, want partial success", notice)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 1 {
		t.Fatalf("state panes = %+v, want first pane to remain recorded", store.Panes)
	}
	if entries, err := os.ReadDir(filepath.Join(repo, ".fanout", "worktrees")); err != nil || len(entries) != 1 {
		t.Fatalf("worktree entries after partial success = %d/%v, want one", len(entries), err)
	}
}

func TestLaunchShellPaneFromTUIRecordsShellState(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	installTUITmuxShim(t, "%77")

	err := launchShellPaneFromTUI(repo, "fanout-test", fanouttui.ShellLaunchRequest{
		TargetPath: repo,
		Root:       true,
	})
	if err != nil {
		t.Fatal(err)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 1 {
		t.Fatalf("state panes = %+v, want one shell pane", store.Panes)
	}
	got := store.Panes[0]
	if got.Kind != state.PaneKindShell || got.Agent != "shell" || got.PaneID != "%77" {
		t.Fatalf("shell state = %+v, want shell kind/agent/pane", got)
	}
	if !strings.HasPrefix(got.ShellKey, "shell-") {
		t.Fatalf("shell key = %q, want generated shell key", got.ShellKey)
	}
	if got.Parent != manualPaneParentRef || got.IssueNum != -1 {
		t.Fatalf("shell identity = %s/%d, want @manual/-1", got.Parent, got.IssueNum)
	}
	if got.WorktreePath != repo || got.DisplayName != "root terminal" || got.Slug != "terminal-root-1" {
		t.Fatalf("shell path/name/slug = %q/%q/%q", got.WorktreePath, got.DisplayName, got.Slug)
	}

	body, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{".fanout/state.json", ".fanout/state.json.lock"} {
		if !strings.Contains(string(body), pattern) {
			t.Fatalf("git exclude missing %q:\n%s", pattern, body)
		}
	}
}

func TestCountOpenChildTargetsIncludesTaskListRefs(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[]]'
  ;;
"issue view 500 --json body -q .body")
  printf '%s\n' '- [ ] #501 task child' '- [ ] #502 closed child'
  ;;
"issue view 501 --json number,title,state,body,labels")
  printf '{"number":501,"title":"task child","state":"OPEN","body":"","labels":[]}'
  ;;
"issue view 502 --json number,title,state,body,labels")
  printf '{"number":502,"title":"closed child","state":"CLOSED","body":"","labels":[]}'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	got, err := countOpenChildTargets(ghissue.Runner{}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("countOpenChildTargets() = %d, want one OPEN task-list child", got)
	}
}

func TestCountOpenChildTargetsFailsWhenParentBodyReadFails(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[]]'
  ;;
"issue view 500 --json body -q .body")
  printf 'temporary gh failure\n' >&2
  exit 1
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	if _, err := countOpenChildTargets(ghissue.Runner{}, 500); err == nil {
		t.Fatal("countOpenChildTargets() error = nil, want parent body failure")
	}
}

func TestCountWatchChildTargetsCountsOnlyLaunchableChildren(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[{"number":501,"title":"ready","state":"open"},{"number":502,"title":"blocked","state":"open"}]]'
  ;;
"issue view 500 --json body -q .body")
  printf '%s\n' '- [ ] #501 ready' '- [ ] #502 blocked (blocked by #600)'
  ;;
"issue view 501 --json body,labels")
  printf '{"body":"","labels":[]}'
  ;;
"issue view 502 --json body,labels")
  printf '{"body":"","labels":[]}'
  ;;
"issue view 600 --json state -q .state")
  printf 'OPEN\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	counts, err := countWatchChildTargets(t.TempDir(), ghissue.Runner{}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Open != 2 || counts.Launchable != 1 || counts.Unfanned != 2 {
		t.Fatalf("countWatchChildTargets() = %+v, want open=2 launchable=1 unfanned=2", counts)
	}
}

func TestCountWatchChildTargetsSkipsUnresolvableTaskListRefs(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[{"number":501,"title":"ready","state":"open"}]]'
  ;;
"issue view 500 --json body -q .body")
  printf '%s\n' '- [ ] #501 ready' '- [ ] #599 stale'
  ;;
"issue view 501 --json body,labels")
  printf '{"body":"","labels":[]}'
  ;;
"issue view 599 --json number,title,state,body,labels")
  printf 'not found\n' >&2
  exit 1
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	counts, err := countWatchChildTargets(t.TempDir(), ghissue.Runner{}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Open != 1 || counts.Launchable != 1 || counts.Unfanned != 1 {
		t.Fatalf("countWatchChildTargets() = %+v, want open=1 launchable=1 unfanned=1", counts)
	}
}

func TestLaunchWatchStandaloneSkipsIssueRecordedUnderLock(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "700", IssueNum: 501, Slug: "existing-501", PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	err = launchWatchStandalone(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), ghissue.Issue{
		Number: 501,
		Title:  "existing",
		State:  "OPEN",
	})
	if !errors.Is(err, watch.ErrAlreadyFanned) {
		t.Fatalf("launchWatchStandalone() error = %v, want ErrAlreadyFanned", err)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 1 || store.Panes[0].PaneID != "%1" {
		t.Fatalf("state panes = %+v, want existing pane only", store.Panes)
	}
}

func TestWatchPaneMatchesLiveRequiresWorktreeMatch(t *testing.T) {
	pane := state.Pane{
		PaneID:       "%1",
		WorktreePath: "/repo/.fanout/worktrees/child-101",
	}

	if watchPaneMatchesLive(pane, tmuxrun.LivePane{ID: "%1", CurrentPath: "/repo/other"}) {
		t.Fatal("watchPaneMatchesLive() = true for reused pane id in another worktree")
	}
	if !watchPaneMatchesLive(pane, tmuxrun.LivePane{ID: "%1", CurrentPath: "/repo/.fanout/worktrees/child-101/subdir"}) {
		t.Fatal("watchPaneMatchesLive() = false for live pane under recorded worktree")
	}
	if watchPaneMatchesLive(pane, tmuxrun.LivePane{ID: "%2", CurrentPath: "/repo/.fanout/worktrees/child-101"}) {
		t.Fatal("watchPaneMatchesLive() = true for different pane id")
	}
}

func TestWatchPaneMatchesLiveRequiresShellKeyForShellRows(t *testing.T) {
	pane := state.Pane{
		Kind:         state.PaneKindShell,
		PaneID:       "%1",
		ShellKey:     "shell-root",
		WorktreePath: "/repo",
	}

	if watchPaneMatchesLive(pane, tmuxrun.LivePane{ID: "%1", CurrentPath: "/repo", ShellKey: "other-shell"}) {
		t.Fatal("watchPaneMatchesLive() = true for shell row with reused pane id")
	}
	if !watchPaneMatchesLive(pane, tmuxrun.LivePane{ID: "%1", CurrentPath: "/repo/elsewhere", ShellKey: "shell-root"}) {
		t.Fatal("watchPaneMatchesLive() = false for shell row with matching shell key")
	}
}

func TestWatchLivePaneCacheReusesListingUntilReset(t *testing.T) {
	calls := 0
	cache := &watchLivePaneCache{
		list: func() ([]tmuxrun.LivePane, error) {
			calls++
			return []tmuxrun.LivePane{
				{ID: "%1", CurrentPath: "/repo/.fanout/worktrees/one-501"},
				{ID: "%2", CurrentPath: "/repo/.fanout/worktrees/two-502"},
			}, nil
		},
	}

	ok, err := cache.Alive(state.Pane{})
	if err != nil {
		t.Fatal(err)
	}
	if ok || calls != 0 {
		t.Fatalf("empty pane alive/calls = %v/%d, want false/0", ok, calls)
	}

	ok, err = cache.Alive(state.Pane{PaneID: "%1", WorktreePath: "/repo/.fanout/worktrees/one-501"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Alive() = false, want true for first live pane")
	}
	ok, err = cache.Alive(state.Pane{PaneID: "%2", WorktreePath: "/repo/.fanout/worktrees/two-502"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Alive() = false, want true for second live pane")
	}
	if calls != 1 {
		t.Fatalf("list calls = %d, want one cached call", calls)
	}

	cache.Reset()
	ok, err = cache.Alive(state.Pane{PaneID: "%1", WorktreePath: "/repo/.fanout/worktrees/one-501"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || calls != 2 {
		t.Fatalf("after reset alive/calls = %v/%d, want true/2", ok, calls)
	}
}

func TestWatchParentResultAfterLaunchRequeuesOnFollowupError(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf 'temporary gh failure\n' >&2
  exit 1
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	cfg := &cliflags.Config{
		Parent:        500,
		ParentRef:     "500",
		ParentMode:    cliflags.ModeIssue,
		Limit:         1,
		UnblockedOnly: true,
	}

	got := watchParentResultAfterLaunch(t.TempDir(), cfg, ghissue.Runner{})
	if !got.Deferred {
		t.Fatal("watchParentResultAfterLaunch() Deferred = false, want true when post-launch check fails")
	}
}

func TestWatchParentHasRemainingTargetsUsesPostLaunchPlan(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[{"number":501,"title":"one","state":"open"},{"number":502,"title":"two","state":"open"}]]'
  ;;
"issue view 500 --json body -q .body")
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	repo := t.TempDir()
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "500", IssueNum: 501, Slug: "one-501", PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "500", IssueNum: 502, Slug: "two-502", PaneID: "%2"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	cfg := &cliflags.Config{
		Parent:        500,
		ParentRef:     "500",
		ParentMode:    cliflags.ModeIssue,
		Limit:         1,
		UnblockedOnly: true,
	}

	deferred, err := watchParentHasRemainingTargets(repo, cfg, ghissue.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	if deferred {
		t.Fatal("watchParentHasRemainingTargets() = true, want false after all children are already fanned")
	}
}

func TestWatchParentHasRemainingTargetsRequeuesAfterPartialLaunch(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[{"number":501,"title":"one","state":"open"},{"number":502,"title":"two","state":"open"},{"number":503,"title":"three","state":"open"}]]'
  ;;
"issue view 500 --json body -q .body")
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	repo := t.TempDir()
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "500", IssueNum: 501, Slug: "one-501", PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "500", IssueNum: 502, Slug: "two-502", PaneID: "%2"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	cfg := &cliflags.Config{
		Parent:        500,
		ParentRef:     "500",
		ParentMode:    cliflags.ModeIssue,
		Limit:         1,
		UnblockedOnly: true,
	}

	deferred, err := watchParentHasRemainingTargets(repo, cfg, ghissue.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	if !deferred {
		t.Fatal("watchParentHasRemainingTargets() = false, want true while an unfanned child remains")
	}
}

func TestWatchParentHasRemainingTargetsRequeuesBlockedRowsWithoutLimit(t *testing.T) {
	installTUIWatcherGHScript(t, `
case "$args" in
"api --paginate --slurp repos/{owner}/{repo}/issues/500/sub_issues?per_page=100")
  printf '[[{"number":501,"title":"one","state":"open"},{"number":502,"title":"blocked","state":"open"}]]'
  ;;
"issue view 500 --json body -q .body")
  printf '%s\n' '- [ ] #501 one' '- [ ] #502 blocked (blocked by #600)'
  ;;
"issue view 501 --json body,labels")
  printf '{"body":"","labels":[]}'
  ;;
"issue view 502 --json body,labels")
  printf '{"body":"","labels":[]}'
  ;;
"issue view 600 --json state -q .state")
  printf 'OPEN\n'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)
	repo := t.TempDir()
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(state.Pane{Parent: "500", IssueNum: 501, Slug: "one-501", PaneID: "%1"}); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	cfg := &cliflags.Config{
		Parent:        500,
		ParentRef:     "500",
		ParentMode:    cliflags.ModeIssue,
		UnblockedOnly: true,
	}

	deferred, err := watchParentHasRemainingTargets(repo, cfg, ghissue.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	if !deferred {
		t.Fatal("watchParentHasRemainingTargets() = false, want true while blocked children remain")
	}
}

func initTUITestGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}

func commitTUITestGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "README.md")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commit := exec.Command("git", "-c", "user.name=fanout-test", "-c", "user.email=fanout@example.invalid", "commit", "-m", "init")
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}
	branch := exec.Command("git", "branch", "-M", "main")
	branch.Dir = dir
	if out, err := branch.CombinedOutput(); err != nil {
		t.Fatalf("git branch failed: %v\n%s", err, out)
	}
}

func installTUITmuxShim(t *testing.T, paneID string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  split-window)
    printf '%s\n' "$TMUX_SHIM_PANE_ID"
    ;;
  display-message)
    # Answer the auto-layout window-geometry query; stay empty for any other.
    if [[ "$*" == *window_width* ]]; then
      printf '@1\t200\t50\n'
    fi
    ;;
  list-panes)
    # Answer the auto-layout per-window roster (a console sidebar + the new pane).
    if [[ "$*" == *fanout_role* ]]; then
      printf '%%0\t0\t1\tconsole\t\n%s\t1\t0\t\t\n' "$TMUX_SHIM_PANE_ID"
    fi
    ;;
  select-pane|set-option|select-layout|kill-pane)
    ;;
  *)
    ;;
esac
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_SHIM_PANE_ID", paneID)
}

func installTUIDashboardTmuxShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "tmux-args.txt")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "$TMUX_SHIM_ARGS"
printf '%s\n' '---' >> "$TMUX_SHIM_ARGS"
case "${1:-}" in
  -V)
    printf 'tmux 3.3\n'
    ;;
  display-message)
    if [[ "$*" == *session_name* ]]; then
      printf 'fanout-test\n'
    elif [[ "$*" == *pane_title* ]]; then
      printf 'old title\n'
    elif [[ "$*" == *window_width* ]]; then
      printf '@1\t200\t50\n'
    elif [[ "$*" == *window_id* ]]; then
      printf '@1\n'
    fi
    ;;
  list-panes)
    if [[ "$*" == *fanout_role* ]]; then
      printf '%%tui\t0\t1\tconsole\t\n'
    fi
    ;;
  bind-key|set-option|select-pane|select-layout|kill-pane)
    ;;
  *)
    ;;
esac
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_SHIM_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

func stubRunTUI(t *testing.T) func() {
	t.Helper()
	original := runTUI
	runTUI = func(fanouttui.Options) error { return nil }
	return func() {
		runTUI = original
	}
}

func writeTUITestStateFile(t *testing.T, repo string) {
	t.Helper()
	dir := filepath.Join(repo, ".fanout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"panes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTUITmuxLog(t *testing.T, argsPath string) string {
	t.Helper()
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func tmuxLogHasCommand(log, needle string) bool {
	for command := range strings.SplitSeq(strings.TrimSuffix(log, "\n---\n"), "\n---\n") {
		if strings.Contains(command, needle) {
			return true
		}
	}
	return false
}

func installTUIWatcherGHScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := filepath.Join(dir, "gh")
	content := `#!/usr/bin/env bash
set -euo pipefail
args="$*"
printf '%s\n' "$args" >> "$GH_FAKE_ARGS"
` + body
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_FAKE_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}
