package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutnotify "github.com/butaosuinu/fanout/internal/infra/notify"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func TestEnterHerdrTUISessionBootstrapsConsoleAndPrintsAttachCommand(t *testing.T) {
	originalEnsure := ensureOwnedHerdrForTUI
	originalConsole := ensureHerdrConsoleForTUI
	t.Cleanup(func() {
		ensureOwnedHerdrForTUI = originalEnsure
		ensureHerdrConsoleForTUI = originalConsole
	})
	owned := &herdrrun.OwnedSession{Session: "owned-session"}
	ensureOwnedHerdrForTUI = func(root string) (*herdrrun.OwnedSession, error) {
		if root != "/repo" {
			t.Fatalf("ensure root = %q, want /repo", root)
		}
		return owned, nil
	}
	ensureHerdrConsoleForTUI = func(
		_ context.Context,
		root string,
		got *herdrrun.OwnedSession,
		_ []string,
		_ string,
	) (panelaunch.HerdrConsoleResult, error) {
		if root != "/repo" || got != owned {
			t.Fatalf("console input = root:%q session:%p", root, got)
		}
		return panelaunch.HerdrConsoleResult{
			Pane:          state.Pane{PaneID: "pane-1"},
			AttachCommand: "HERDR_SESSION='owned-session' '/owned/herdr'",
		}, nil
	}
	var stdout, stderr bytes.Buffer
	logger := log.NewWith(&stdout, &stderr, false)
	code := enterHerdrTUISession(
		"/repo",
		backend.Selection{Name: backend.Herdr, Reason: backend.ReasonUserConfig},
		logger,
	)
	if code != exitcode.OK || stderr.Len() != 0 {
		t.Fatalf("enterHerdrTUISession() = %d stderr=%q", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "pane-1") ||
		!strings.Contains(out, "HERDR_SESSION='owned-session' '/owned/herdr'") {
		t.Fatalf("console output = %q", out)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if got := lines[len(lines)-1]; got != "HERDR_SESSION='owned-session' '/owned/herdr'" {
		t.Fatalf("attach line = %q, want bare command", got)
	}
}

func TestWireOwnedHerdrTUIEnablesScopedInteractivePorts(t *testing.T) {
	opts := fanouttui.Options{}
	owned := &herdrrun.OwnedSession{Session: "owned-session"}
	wireOwnedHerdrTUI(
		&opts,
		"/repo",
		"owned-session",
		"fanout",
		settings.Defaults(),
		hooks.EmptyConfig(),
		owned,
	)
	if opts.LaunchPane == nil || opts.LaunchAttach == nil || opts.LaunchIssue == nil ||
		opts.LaunchIssuePlan == nil || opts.LaunchShell == nil || opts.FocusHerdrPane == nil ||
		opts.CaptureHerdrPane == nil {
		t.Fatalf("owned Herdr ports are incomplete: %+v", opts)
	}
	if reason := opts.HerdrActionDisabled(state.Pane{}); reason != "" {
		t.Fatalf("owned console launch disabled: %q", reason)
	}
	if opts.RestorePanes != nil || opts.LifecycleCloseOwned != nil {
		t.Fatal("owned Herdr wiring enabled deferred restore/lifecycle ports")
	}
}

func TestSettingsReloadPreservesOnlyAdmittedHerdrIssueLaunch(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	for _, tt := range []struct {
		name     string
		admitted bool
	}{
		{name: "owned", admitted: true},
		{name: "foreign"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reload := newTUISettingsReloadFunc(
				repo, "owned-session", "fanout", hooks.EmptyConfig(),
				backend.Selection{Name: backend.Herdr}, tt.admitted, discardLogger(),
			)
			runtime, err := reload()
			if err != nil {
				t.Fatal(err)
			}
			if (runtime.LaunchIssue != nil) != tt.admitted {
				t.Fatalf("LaunchIssue configured = %t, want %t", runtime.LaunchIssue != nil, tt.admitted)
			}
		})
	}
}

func TestOwnedHerdrPaneIdentitySeparatesGenericCWDFromWorktreeProvenance(t *testing.T) {
	identity, err := ownedHerdrPaneIdentity(state.Pane{
		Backend: backend.Herdr, PaneID: "w1:p1", HerdrWorkspaceID: "w1",
		HerdrWorkspaceLabel: "owned-label", HerdrTerminalID: "term-1",
		HerdrSession: "owned", HerdrSocketPath: "/tmp/owned.sock",
		WorktreePath: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.WorktreePath != "" || identity.CurrentPath != "/repo" {
		t.Fatalf("generic identity = %+v, want cwd without worktree provenance", identity)
	}
}

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

func TestCmdTUIHerdrContextSkipsTmuxComposition(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	t.Chdir(repo)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "dev-session")
	t.Setenv("TMUX", "nested-tmux")
	t.Setenv("TMUX_PANE", "%nested")
	t.Setenv("FANOUT_BACKEND", "")
	t.Setenv("FANOUT_WATCHER", "1")
	t.Setenv("FANOUT_NOTIFICATIONS", "tmux")
	tmuxLogPath := installTUIDashboardTmuxShim(t)
	herdrLogPath := installTUIHerdrShim(t)

	originalRunTUI := runTUI
	originalListLive := runtimeListLiveForProject
	defer func() {
		runTUI = originalRunTUI
		runtimeListLiveForProject = originalListLive
	}()
	includeTmux := true
	runtimeListLiveForProject = func(root string, include bool) func() ([]backend.LivePane, error) {
		if canonicalRuntimeRoot(root) != canonicalRuntimeRoot(repo) {
			t.Fatalf("runtime collector root = %q, want %q", root, repo)
		}
		includeTmux = include
		return func() ([]backend.LivePane, error) { return nil, nil }
	}
	var opts fanouttui.Options
	runTUI = func(got fanouttui.Options) error {
		opts = got
		if got.Notifier == nil {
			t.Fatal("Notifier is nil")
		}
		if err := got.Notifier.Notify([]fanoutnotify.Event{{Kind: fanoutnotify.EventAgentDone, PaneID: "w1:p1"}}); err != nil {
			t.Fatalf("herdr notifier: %v", err)
		}
		return nil
	}

	if code := cmdTUI("fanout", discardLogger()); code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK", code)
	}
	if opts.BackendSelection != (backend.Selection{Name: backend.Herdr, Reason: backend.ReasonHerdrContext}) {
		t.Fatalf("BackendSelection = %+v, want herdr from HERDR_ENV", opts.BackendSelection)
	}
	if opts.Session != "dev-session" {
		t.Fatalf("Session = %q, want herdr session label", opts.Session)
	}
	if includeTmux {
		t.Fatal("runtime collector includeTmux = true, want false for herdr host")
	}
	if opts.Watcher != nil || opts.RestorePanes != nil || opts.Relayout != nil || opts.ActivePane != nil ||
		opts.FocusPane != nil || opts.CapturePaneOutput != nil || opts.ClosePane != nil || opts.LifecycleCloseOwned != nil ||
		opts.NewPanePrompt != nil || opts.HelpPopup != nil || opts.SettingsPopup != nil || opts.LaunchPane != nil ||
		opts.LaunchIssue != nil || opts.LaunchIssuePlan != nil || opts.LaunchAttach != nil || opts.LaunchShell != nil {
		t.Fatalf("herdr TUI has tmux/mutation wiring: %+v", opts)
	}
	if opts.ReloadSettings == nil {
		t.Fatal("ReloadSettings is nil, want runtime-neutral notification reload")
	}
	runtime, err := opts.ReloadSettings()
	if err != nil {
		t.Fatalf("ReloadSettings: %v", err)
	}
	if runtime.Watcher != nil || runtime.LaunchIssue != nil {
		t.Fatalf("herdr reload runtime = %+v, want no watcher/launcher", runtime)
	}
	if _, err := os.Stat(tmuxLogPath); !os.IsNotExist(err) {
		body, _ := os.ReadFile(tmuxLogPath)
		t.Fatalf("herdr TUI invoked tmux:\n%s", body)
	}
	if body, err := os.ReadFile(herdrLogPath); err == nil {
		if strings.Contains(string(body), "notification\nshow") {
			t.Fatalf("herdr TUI invoked forbidden `herdr notification show`:\n%s", body)
		}
		t.Fatalf("herdr TUI notification path invoked herdr:\n%s", body)
	} else if !os.IsNotExist(err) {
		t.Fatalf("read herdr argv log: %v", err)
	}
}

func TestCmdTUIHerdrContextUsesOwnerRootFromChildWorktree(t *testing.T) {
	owner := t.TempDir()
	initTUITestGitRepo(t, owner)
	commitTUITestGitRepo(t, owner)
	writeTUITestStateFile(t, owner)
	child := filepath.Join(owner, ".fanout", "worktrees", "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	initTUITestGitRepo(t, child)
	commitTUITestGitRepo(t, child)
	t.Chdir(child)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "dev-session")
	t.Setenv("TMUX", "nested-tmux")
	t.Setenv("TMUX_PANE", "%nested")
	t.Setenv("FANOUT_BACKEND", "")
	t.Setenv("FANOUT_NOTIFICATIONS", "none")
	tmuxLogPath := installTUIDashboardTmuxShim(t)

	originalRunTUI := runTUI
	defer func() { runTUI = originalRunTUI }()
	var opts fanouttui.Options
	runTUI = func(got fanouttui.Options) error {
		opts = got
		return nil
	}

	if code := cmdTUI("fanout", discardLogger()); code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK", code)
	}
	if canonicalRuntimeRoot(opts.ProjectRoot) != canonicalRuntimeRoot(owner) {
		t.Fatalf("ProjectRoot = %q, want owner %q", opts.ProjectRoot, owner)
	}
	if opts.BackendSelection.Name != backend.Herdr {
		t.Fatalf("BackendSelection = %+v, want herdr", opts.BackendSelection)
	}
	if _, err := os.Stat(tmuxLogPath); !os.IsNotExist(err) {
		body, _ := os.ReadFile(tmuxLogPath)
		t.Fatalf("herdr child-worktree startup invoked tmux:\n%s", body)
	}
}

func TestCmdTUIUserConfiguredHerdrOutsideContextBootstrapsWithoutTmux(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	t.Chdir(repo)
	t.Setenv("HERDR_ENV", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("FANOUT_BACKEND", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tmuxLogPath := installTUIDashboardTmuxShim(t)
	configPath := settings.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"runtimeBackend":"herdr"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	originalRunTUI := runTUI
	originalEnsure := ensureOwnedHerdrForTUI
	originalConsole := ensureHerdrConsoleForTUI
	called := false
	runTUI = func(fanouttui.Options) error {
		called = true
		return nil
	}
	ensureOwnedHerdrForTUI = func(string) (*herdrrun.OwnedSession, error) {
		return &herdrrun.OwnedSession{Session: "owned-session"}, nil
	}
	ensureHerdrConsoleForTUI = func(
		context.Context,
		string,
		*herdrrun.OwnedSession,
		[]string,
		string,
	) (panelaunch.HerdrConsoleResult, error) {
		return panelaunch.HerdrConsoleResult{
			Pane:          state.Pane{PaneID: "console-pane"},
			AttachCommand: "HERDR_SESSION='owned-session' herdr",
		}, nil
	}
	defer func() {
		runTUI = originalRunTUI
		ensureOwnedHerdrForTUI = originalEnsure
		ensureHerdrConsoleForTUI = originalConsole
	}()
	var stdout, stderr strings.Builder
	code := cmdTUI("fanout", log.NewWith(&stdout, &stderr, false))
	if code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK; stderr=%s", code, stderr.String())
	}
	if called {
		t.Fatal("runTUI was called before attaching to the owned Herdr console")
	}
	for _, want := range []string{"console-pane", "HERDR_SESSION='owned-session' herdr"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if _, err := os.Stat(tmuxLogPath); !os.IsNotExist(err) {
		body, _ := os.ReadFile(tmuxLogPath)
		t.Fatalf("Herdr bootstrap invoked tmux:\n%s", body)
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
	if !tmuxLogHasCommand(log, "bind-key\nD\nrun-shell") {
		t.Fatalf("tmux log missing prefix dashboard keybind:\n%s", log)
	}
	if !tmuxLogHasCommand(log, "bind-key\n-n\nF12\nrun-shell") {
		t.Fatalf("tmux log missing direct dashboard keybind:\n%s", log)
	}
}

func TestCmdTUIRegistersConsoleKeybinds(t *testing.T) {
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
	if !tmuxLogHasCommand(log, "bind-key\nT\nrun-shell") {
		t.Fatalf("tmux log missing prefix console keybind:\n%s", log)
	}
	if !tmuxLogHasCommand(log, "bind-key\n-n\nF11\nrun-shell") {
		t.Fatalf("tmux log missing direct console keybind:\n%s", log)
	}
}

func TestCmdTUIWiresActivePaneProvider(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	writeTUITestStateFile(t, repo)
	t.Chdir(repo)
	t.Setenv("TMUX", "tmux-session")
	t.Setenv("TMUX_PANE", "%tui")
	installTUIDashboardTmuxShim(t)
	original := runTUI
	var opts fanouttui.Options
	runTUI = func(o fanouttui.Options) error {
		opts = o
		return nil
	}
	defer func() { runTUI = original }()

	code := cmdTUI("fanout", discardLogger())
	if code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK", code)
	}
	if opts.ActivePane == nil {
		t.Fatal("ActivePane provider is nil, want tmux-backed provider")
	}
	got, err := opts.ActivePane()
	if err != nil {
		t.Fatalf("ActivePane() failed: %v", err)
	}
	if got != "%2" {
		t.Fatalf("ActivePane() = %q, want %%2", got)
	}
}

func TestCmdTUIWiresRuntimeBackendPorts(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	writeTUITestStateFile(t, repo)
	t.Chdir(repo)
	t.Setenv("TMUX", "tmux-session")
	t.Setenv("TMUX_PANE", "%tui")
	installTUIDashboardTmuxShim(t)
	original := runTUI
	originalListLive := runtimeListLiveForProject
	compositeCalls := 0
	var compositeIncludeTmux bool
	runtimeListLiveForProject = func(root string, includeTmux bool) func() ([]backend.LivePane, error) {
		if canonicalRuntimeRoot(root) != canonicalRuntimeRoot(repo) {
			t.Fatalf("runtime collector root = %q, want %q", root, repo)
		}
		compositeIncludeTmux = includeTmux
		return func() ([]backend.LivePane, error) {
			compositeCalls++
			return []backend.LivePane{{Ref: backend.PaneRef{Backend: backend.Herdr, Pane: "w1:p1"}}}, nil
		}
	}
	var opts fanouttui.Options
	runTUI = func(o fanouttui.Options) error {
		opts = o
		return nil
	}
	defer func() {
		runTUI = original
		runtimeListLiveForProject = originalListLive
	}()

	if code := cmdTUI("fanout", discardLogger()); code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK", code)
	}
	if opts.ListLive == nil || opts.LifecycleCloseOwned == nil || opts.ShellPaneAlive == nil || opts.FocusPane == nil || opts.PaneAlive == nil || opts.CapturePaneOutput == nil || opts.ClosePane == nil {
		t.Fatal("runtime backend ports are incomplete")
	}
	observed, err := opts.ListLive()
	if err != nil {
		t.Fatal(err)
	}
	if !compositeIncludeTmux || len(observed) != 1 || observed[0].Ref.Backend != backend.Herdr {
		t.Fatalf("composite ListLive = %+v (includeTmux=%v), want herdr observation with tmux host", observed, compositeIncludeTmux)
	}
	_ = opts.ShellPaneAlive("%2", "shell-key")
	_ = opts.PaneAlive("%2")
	if err := opts.FocusPane("%2"); err != nil {
		t.Fatal(err)
	}
	if _, err := opts.CapturePaneOutput("%2", 10); err != nil {
		t.Fatal(err)
	}
	if err := opts.ClosePane(backend.PaneRef{Backend: backend.Tmux, Pane: "%2"}); err != nil {
		t.Fatal(err)
	}
	if compositeCalls != 1 {
		t.Fatalf("composite ListLive calls = %d, want only the display observation call", compositeCalls)
	}
}

func TestTUIActivePaneProviderIgnoresConsolePane(t *testing.T) {
	installTUIActivePaneTmuxShim(t, "%tui")

	got, err := newTUIActivePaneFunc("%tui")()
	if err != nil {
		t.Fatalf("ActivePane() failed: %v", err)
	}
	if got != "" {
		t.Fatalf("ActivePane() = %q, want empty for console pane", got)
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
	t.Setenv("FANOUT_CONSOLE_KEYBIND", "0")
	argsPath := installTUIDashboardTmuxShim(t)
	restoreRunTUI := stubRunTUI(t)
	defer restoreRunTUI()

	code := cmdTUI("fanout", discardLogger())
	if code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK", code)
	}

	log := readTUITmuxLog(t, argsPath)
	if tmuxLogHasCommand(log, "run-shell") || tmuxLogHasCommand(log, "display-popup") {
		t.Fatalf("tmux log should not register keybind commands when both are disabled:\n%s", log)
	}
	if tmuxLogHasCommand(log, "list-keys") || tmuxLogHasCommand(log, "unbind-key") {
		t.Fatalf("startup opt-out should not cleanup existing key commands:\n%s", log)
	}
}

func TestCmdTUINoDashboardKeybindKeepsConsoleKeybind(t *testing.T) {
	// The other direction of toggle independence: disabling the dashboard
	// keybind alone must suppress the fanout-dashboard binds without
	// taking the console-return registration down with it.
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
	if tmuxLogHasCommand(log, "fanout-dashboard") {
		t.Fatalf("tmux log should not contain dashboard keybinds when disabled:\n%s", log)
	}
	if !tmuxLogHasCommand(log, "bind-key\n-n\nF11\nrun-shell") {
		t.Fatalf("tmux log missing console keybind (must stay registered):\n%s", log)
	}
}

func TestCmdTUINoConsoleKeybindHonorsEnv(t *testing.T) {
	// Console and dashboard keybinds are independently switchable: disabling
	// the console one must not take the dashboard registration down with it.
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	writeTUITestStateFile(t, repo)
	t.Chdir(repo)
	t.Setenv("TMUX", "tmux-session")
	t.Setenv("TMUX_PANE", "%tui")
	t.Setenv("FANOUT_CONSOLE_KEYBIND", "0")
	argsPath := installTUIDashboardTmuxShim(t)
	restoreRunTUI := stubRunTUI(t)
	defer restoreRunTUI()

	code := cmdTUI("fanout", discardLogger())
	if code != exitcode.OK {
		t.Fatalf("cmdTUI() = %d, want OK", code)
	}

	log := readTUITmuxLog(t, argsPath)
	if tmuxLogHasCommand(log, "focus-console") ||
		tmuxLogHasCommand(log, "bind-key\nT\nrun-shell") ||
		tmuxLogHasCommand(log, "bind-key\n-n\nF11\nrun-shell") {
		t.Fatalf("tmux log should not contain console keybinds when disabled:\n%s", log)
	}
	if !tmuxLogHasCommand(log, "bind-key\nD\nrun-shell") {
		t.Fatalf("tmux log missing dashboard keybind (must stay registered):\n%s", log)
	}
}

func TestTUISettingsReloadCleansDisabledKeybinds(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	commitTUITestGitRepo(t, repo)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	configPath := filepath.Join(xdg, "fanout", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"dashboardKeybind": false, "consoleKeybind": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	argsPath := installTUISettingsReloadTmuxShim(t)

	reload := newTUISettingsReloadFunc(repo, "fanout-test", "fanout", hooks.Config{}, backend.Selection{Name: backend.Tmux}, true, discardLogger())
	if _, err := reload(); err != nil {
		t.Fatal(err)
	}

	log := readTUITmuxLog(t, argsPath)
	for _, want := range []string{
		"unbind-key\n-q\nD",
		"unbind-key\n-q\n-n\nF12",
		"unbind-key\n-q\nM",
		"unbind-key\n-q\nT",
		"unbind-key\n-q\n-n\nF11",
	} {
		if !tmuxLogHasCommand(log, want) {
			t.Fatalf("tmux log missing settings cleanup %q:\n%s", want, log)
		}
	}
	if tmuxLogHasCommand(log, "run-shell") || tmuxLogHasCommand(log, "display-popup") {
		t.Fatalf("tmux log should not bind disabled key commands:\n%s", log)
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
	want = tuiNewPanePopupGeometry{PopupWidth: 76, PopupHeight: 22, PromptWidth: 74, PromptHeight: 20}
	if got != want {
		t.Fatalf("80x24 popup geometry = %#v, want %#v", got, want)
	}

	got, err = tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 60, Height: 22})
	if err != nil {
		t.Fatal(err)
	}
	want = tuiNewPanePopupGeometry{PopupWidth: 56, PopupHeight: 22, PromptWidth: 54, PromptHeight: 20}
	if got != want {
		t.Fatalf("60x22 popup geometry = %#v, want %#v", got, want)
	}
	if _, widthErr := tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 59, Height: 22}); widthErr == nil {
		t.Fatal("tuiNewPanePopupGeometryForClient() succeeded without enough prompt width")
	}

	if _, err := tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 20}); err == nil {
		t.Fatal("tuiNewPanePopupGeometryForClient() succeeded without enough prompt height")
	}

	if _, err := tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 40, Height: 20}); err == nil {
		t.Fatal("tuiNewPanePopupGeometryForClient() succeeded for too-small client")
	}
	if _, err := tuiNewPanePopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 19}); err == nil {
		t.Fatal("tuiNewPanePopupGeometryForClient() succeeded without enough prompt height")
	}
}

func TestTUISettingsPopupGeometryUsesSettingsMinimumHeight(t *testing.T) {
	got, err := tuiSettingsPopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 20})
	if err != nil {
		t.Fatal(err)
	}
	want := tuiNewPanePopupGeometry{PopupWidth: 76, PopupHeight: 20, PromptWidth: 74, PromptHeight: 18}
	if got != want {
		t.Fatalf("80x20 settings popup geometry = %#v, want %#v", got, want)
	}

	if _, heightErr := tuiSettingsPopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 19}); heightErr == nil {
		t.Fatal("tuiSettingsPopupGeometryForClient() succeeded without enough settings height")
	}

	got, err = tuiSettingsPopupGeometryForClient(tmuxrun.ClientSize{Width: 60, Height: 20})
	if err != nil {
		t.Fatal(err)
	}
	want = tuiNewPanePopupGeometry{PopupWidth: 56, PopupHeight: 20, PromptWidth: 54, PromptHeight: 18}
	if got != want {
		t.Fatalf("60x20 settings popup geometry = %#v, want %#v", got, want)
	}
	if _, widthErr := tuiSettingsPopupGeometryForClient(tmuxrun.ClientSize{Width: 59, Height: 20}); widthErr == nil {
		t.Fatal("tuiSettingsPopupGeometryForClient() succeeded without enough settings width")
	}
}

func TestTUIHelpPopupGeometryUsesClientDimensions(t *testing.T) {
	got, err := tuiHelpPopupGeometryForClient(tmuxrun.ClientSize{Width: 160, Height: 50})
	if err != nil {
		t.Fatal(err)
	}
	want := tuiHelpPopupGeometry{PopupWidth: 90, PopupHeight: 40, ContentWidth: 88, ContentHeight: 38}
	if got != want {
		t.Fatalf("help popup geometry = %#v, want %#v", got, want)
	}

	got, err = tuiHelpPopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	want = tuiHelpPopupGeometry{PopupWidth: 76, PopupHeight: 23, ContentWidth: 74, ContentHeight: 21}
	if got != want {
		t.Fatalf("80x24 help popup geometry = %#v, want %#v", got, want)
	}

	if _, heightErr := tuiHelpPopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 22}); heightErr == nil {
		t.Fatal("tuiHelpPopupGeometryForClient() succeeded without enough help height")
	}
	if _, err := tuiHelpPopupGeometryForClient(tmuxrun.ClientSize{Width: 40, Height: 20}); err == nil {
		t.Fatal("tuiHelpPopupGeometryForClient() succeeded for too-small client")
	}
	if _, err := tuiHelpPopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 19}); err == nil {
		t.Fatal("tuiHelpPopupGeometryForClient() succeeded without enough help height")
	}
}

func TestTUIClosePopupGeometryUsesClientDimensions(t *testing.T) {
	got, err := tuiClosePopupGeometryForClient(tmuxrun.ClientSize{Width: 160, Height: 50})
	if err != nil {
		t.Fatal(err)
	}
	want := tuiClosePopupGeometry{PopupWidth: 78, PopupHeight: 22, ContentWidth: 76, ContentHeight: 20}
	if got != want {
		t.Fatalf("close popup geometry = %#v, want %#v", got, want)
	}

	got, err = tuiClosePopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	want = tuiClosePopupGeometry{PopupWidth: 76, PopupHeight: 12, ContentWidth: 74, ContentHeight: 10}
	if got != want {
		t.Fatalf("80x24 close popup geometry = %#v, want %#v", got, want)
	}

	got, err = tuiClosePopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 12})
	if err != nil {
		t.Fatal(err)
	}
	want = tuiClosePopupGeometry{PopupWidth: 76, PopupHeight: 12, ContentWidth: 74, ContentHeight: 10}
	if got != want {
		t.Fatalf("small client close popup geometry = %#v, want %#v", got, want)
	}

	if _, err := tuiClosePopupGeometryForClient(tmuxrun.ClientSize{Width: 40, Height: 20}); err == nil {
		t.Fatal("tuiClosePopupGeometryForClient() succeeded for too-small client")
	}
	if _, err := tuiClosePopupGeometryForClient(tmuxrun.ClientSize{Width: 80, Height: 11}); err == nil {
		t.Fatal("tuiClosePopupGeometryForClient() succeeded without enough close popup height")
	}
}

func TestTUIPopupPositionAdjacentToPane(t *testing.T) {
	got := tuiPopupPositionAdjacentToPane(tmuxrun.PaneGeometry{
		Left: 0, Top: 3, Width: 40, Height: 20, ClientWidth: 160, ClientHeight: 40,
	}, 90, 20)
	if got == nil || *got != (tmuxrun.PopupPosition{X: 41, Y: 3}) {
		t.Fatalf("popup position = %#v, want x=41 y=3", got)
	}
}

func TestTUIPopupPositionClampsToClient(t *testing.T) {
	got := tuiPopupPositionAdjacentToPane(tmuxrun.PaneGeometry{
		Left: 0, Top: 18, Width: 40, Height: 6, ClientWidth: 80, ClientHeight: 24,
	}, 76, 20)
	if got == nil || *got != (tmuxrun.PopupPosition{X: 4, Y: 4}) {
		t.Fatalf("clamped popup position = %#v, want x=4 y=4", got)
	}
}

func TestTUIPopupPositionUsesLeftSideForRightPane(t *testing.T) {
	got := tuiPopupPositionAdjacentToPane(tmuxrun.PaneGeometry{
		Left: 80, Width: 80, ClientWidth: 160, ClientHeight: 40,
	}, 70, 20)
	if got == nil || *got != (tmuxrun.PopupPosition{X: 9, Y: 0}) {
		t.Fatalf("left-side popup position = %#v, want x=9 y=0", got)
	}
}

func TestTUIPopupPositionChoosesLowerOverlapEdge(t *testing.T) {
	got := tuiPopupPositionAdjacentToPane(tmuxrun.PaneGeometry{
		Left: 80, Width: 80, ClientWidth: 160, ClientHeight: 40,
	}, 90, 20)
	if got == nil || *got != (tmuxrun.PopupPosition{X: 0, Y: 0}) {
		t.Fatalf("edge popup position = %#v, want x=0 y=0", got)
	}
}

func TestTUIPopupPositionFallsBackWithoutPane(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	if got := tuiPopupPositionForCurrentPane(90, 20); got != nil {
		t.Fatalf("popup position = %#v, want centered fallback", got)
	}

	t.Setenv("TMUX_PANE", "%tui")
	if got := tuiPopupPositionForCurrentPane(90, 20); got != nil {
		t.Fatalf("popup position with malformed pane id = %#v, want centered fallback", got)
	}
}

func TestTUIClosePopupUsesCurrentPanePosition(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "tmux-args.txt")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "$TMUX_SHIM_ARGS"
printf '%s\n' '---' >> "$TMUX_SHIM_ARGS"
case "${1:-}" in
  display-message)
    if [[ "$*" == *pane_left* ]]; then
      printf '0\t3\t40\t20\t160\t50\n'
    elif [[ "$*" == *client_width* ]]; then
      printf '160 50\n'
    fi
    ;;
  display-popup)
    command="${@: -1}"
    if [[ "$command" =~ --result-file[[:space:]]([^[:space:]]+) ]]; then
      printf '{"mode":"pane"}\n' > "${BASH_REMATCH[1]}"
    fi
    ;;
  *)
    ;;
esac
`
	tmuxPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_SHIM_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_PANE", "%7")

	popup := newTUICloseChoicePopupFunc("/tmp/repo", "fanout")
	mode, canceled, err := popup(fanouttui.CloseChoiceRequest{
		PaneLabel:   "#101",
		InitialMode: lifecycle.ClosePaneOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	if canceled {
		t.Fatal("close popup canceled, want selection")
	}
	if mode != lifecycle.ClosePaneOnly {
		t.Fatalf("close popup mode = %v, want pane-only", mode)
	}

	log := readTUITmuxLog(t, argsPath)
	if !tmuxLogHasCommand(log, "display-popup\n-E\n") ||
		!tmuxLogHasCommand(log, "\n-x\n41\n-y\n3\n") ||
		!tmuxLogHasCommand(log, tuiClosePopupCommand) {
		t.Fatalf("close popup display-popup was not positioned next to the current pane:\n%s", log)
	}
}

func TestTUINewPanePopupResultRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		result tuiNewPanePopupResult
	}{
		{
			name:   "prompt mode keeps the legacy fields",
			result: tuiNewPanePopupResult{Prompt: "Inspect API", Agents: []string{"codex"}},
		},
		{
			name: "issue mode carries number and agent overrides",
			result: tuiNewPanePopupResult{
				Mode:           "issue",
				Issue:          42,
				DefaultAgent:   "claude",
				AgentOverrides: map[string]string{"43": "codex"},
			},
		},
		{
			name:   "prompt mode carries the plan fan-out flag",
			result: tuiNewPanePopupResult{Prompt: "Ship search", PlanFanout: true, Agents: []string{"claude"}},
		},
		{
			name: "prompt mode preserves long pasted prompts",
			result: tuiNewPanePopupResult{
				Prompt: strings.Repeat("long pasted prompt line\n", 200) + "final line 201",
				Agents: []string{"codex"},
			},
		},
		{
			name: "issue mode plan fan-out carries the worker agent",
			result: tuiNewPanePopupResult{
				Mode:         "issue",
				Issue:        99,
				PlanFanout:   true,
				DefaultAgent: "claude",
				WorkerAgent:  "codex",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			if err := writeTUINewPanePopupResult(path, tt.result); err != nil {
				t.Fatal(err)
			}
			got, err := readTUINewPanePopupResult(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.result) {
				t.Fatalf("popup result = %#v, want %#v", got, tt.result)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("popup result mode = %o, want 600", got)
			}
		})
	}
}

func TestTUIClosePopupRequestAndResultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	request := tuiClosePopupRequest{
		PaneLabel: "#101", Mode: closeModeName(lifecycle.CloseEverything), RequireWorktree: true,
	}
	if err := writeTUIClosePopupRequest(requestPath, request); err != nil {
		t.Fatal(err)
	}
	gotRequest, err := readTUIClosePopupRequest(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotRequest != request {
		t.Fatalf("close popup request = %#v, want %#v", gotRequest, request)
	}

	resultPath := filepath.Join(dir, "result.json")
	result := tuiClosePopupResult{Mode: closeModeName(lifecycle.CloseWorktree)}
	if writeErr := writeTUIClosePopupResult(resultPath, result); writeErr != nil {
		t.Fatal(writeErr)
	}
	gotResult, err := readTUIClosePopupResult(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotResult != result {
		t.Fatalf("close popup result = %#v, want %#v", gotResult, result)
	}
	info, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("close popup result mode = %o, want 600", got)
	}
}

func TestTUISettingsPopupResultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result.json")
	result := tuiSettingsPopupResult{Saved: true, Scope: "user", Path: "/tmp/fanout/config.json"}
	if writeErr := writeTUISettingsPopupResult(resultPath, result); writeErr != nil {
		t.Fatal(writeErr)
	}
	got, err := readTUISettingsPopupResult(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != result {
		t.Fatalf("settings popup result = %#v, want %#v", got, result)
	}
	info, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("settings popup result mode = %o, want 600", got)
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

func TestWaitForTUIClosePopupResultTreatsDoneWithoutResultAsCancel(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result.json")
	donePath := filepath.Join(dir, "result.done")
	if err := os.WriteFile(donePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := waitForTUIClosePopupResult(resultPath, donePath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Canceled {
		t.Fatalf("close popup result = %#v, want canceled", got)
	}
}

func TestWaitForTUISettingsPopupResultTreatsDoneWithoutResultAsCancel(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "result.json")
	donePath := filepath.Join(dir, "result.done")
	if err := os.WriteFile(donePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := waitForTUISettingsPopupResult(resultPath, donePath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Canceled {
		t.Fatalf("settings popup result = %#v, want canceled", got)
	}
}

func TestTUIHelpPopupShellCommandPropagatesPathAndDimensions(t *testing.T) {
	got := tuiHelpPopupShellCommand("fanout", 80, 18)
	for _, want := range []string{
		tuiHelpPopupCommand,
		"--width 80",
		"--height 18",
		"PATH=" + run.ShellQuote(os.Getenv("PATH")),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("help popup shell command missing %q:\n%s", want, got)
		}
	}
}

func TestTUIClosePopupShellCommandMarksDoneAndPropagatesPath(t *testing.T) {
	got := tuiClosePopupShellCommand("fanout", "/tmp/request.json", "/tmp/result.json", "/tmp/result.done", 72, 9)
	for _, want := range []string{
		"trap ",
		"EXIT HUP INT TERM",
		"/tmp/result.done",
		tuiClosePopupCommand,
		"--request-file /tmp/request.json",
		"--result-file /tmp/result.json",
		"--width 72",
		"--height 9",
		"PATH=" + run.ShellQuote(os.Getenv("PATH")),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("close popup shell command missing %q:\n%s", want, got)
		}
	}
}

func TestTUINewPanePopupShellCommandMarksDoneAndPropagatesEnhancedKeys(t *testing.T) {
	t.Setenv("HOME", "/tmp/fanout-home")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/fanout-xdg")
	for _, value := range []string{"", "0", "1"} {
		t.Setenv(fanouttui.EnhancedKeysEnv, value)
		got := tuiNewPanePopupShellCommand("fanout", "/tmp/repo", "/tmp/result.json", "/tmp/result.done", "codex", 80, 18)
		if !strings.Contains(got, fanouttui.EnhancedKeysEnv+"="+run.ShellQuote(value)+" ") {
			t.Fatalf("popup shell command = %q with %s=%q, want forwarded env prefix", got, fanouttui.EnhancedKeysEnv, value)
		}
	}

	got := tuiNewPanePopupShellCommand("fanout", "/tmp/repo", "/tmp/result.json", "/tmp/result.done", "codex", 80, 18)
	for _, want := range []string{
		"trap ",
		"EXIT HUP INT TERM",
		"/tmp/result.done",
		tuiNewPanePopupCommand,
		"--result-file /tmp/result.json",
		// The popup runs under the tmux server env; PATH must come from the
		// parent so the issue/plan pickers can exec gh.
		"PATH=" + run.ShellQuote(os.Getenv("PATH")),
		"HOME=/tmp/fanout-home",
		"XDG_CONFIG_HOME=/tmp/fanout-xdg",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("popup shell command missing %q:\n%s", want, got)
		}
	}
}

func TestTUISettingsPopupShellCommandMarksDoneAndPropagatesEnhancedKeys(t *testing.T) {
	t.Setenv("HOME", "/tmp/fanout-home")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/fanout-xdg")
	t.Setenv("FANOUT_WATCHER", "1")
	t.Setenv("FANOUT_SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/secret")
	for _, value := range []string{"", "0", "1"} {
		t.Setenv(fanouttui.EnhancedKeysEnv, value)
		got := tuiSettingsPopupShellCommand("fanout", "/tmp/repo", "/tmp/result.json", "/tmp/result.done", 80, 18)
		if !strings.Contains(got, fanouttui.EnhancedKeysEnv+"="+run.ShellQuote(value)+" ") {
			t.Fatalf("settings popup shell command = %q with %s=%q, want forwarded env prefix", got, fanouttui.EnhancedKeysEnv, value)
		}
	}

	got := tuiSettingsPopupShellCommand("fanout", "/tmp/repo", "/tmp/result.json", "/tmp/result.done", 80, 18)
	for _, want := range []string{
		"trap ",
		"EXIT HUP INT TERM",
		"/tmp/result.done",
		tuiSettingsPopupCommand,
		"--project-root /tmp/repo",
		"--result-file /tmp/result.json",
		"PATH=" + run.ShellQuote(os.Getenv("PATH")),
		"HOME=/tmp/fanout-home",
		"XDG_CONFIG_HOME=/tmp/fanout-xdg",
		fanouttui.SettingsEnvOverridesEnv + "=",
		"FANOUT_WATCHER",
		"FANOUT_SLACK_WEBHOOK_URL",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("settings popup shell command missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hooks.slack.com") || strings.Contains(got, "secret") {
		t.Fatalf("settings popup shell command leaked sensitive env value:\n%s", got)
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

func TestManualPaneOptionsForTUILongSingleLineUsesBriefingBody(t *testing.T) {
	t.Setenv("FANOUT_NEW_SESSION_PLAN_MODE", "1")
	prompt := strings.Repeat("x", panelaunch.MaxInlineManualPromptBytes+1)
	opts := manualPaneOptionsForTUI(prompt, "codex")
	wantTitle := strings.Repeat("x", 60)

	if opts.Title != wantTitle || opts.Prompt != wantTitle {
		t.Fatalf("long prompt title/prompt lengths = %d/%d, want bounded title length %d", len(opts.Title), len(opts.Prompt), len(wantTitle))
	}
	if opts.Body != prompt {
		t.Fatalf("long prompt body length = %d, want %d", len(opts.Body), len(prompt))
	}

	cfg := newSessionConfigForTUIAgent(t.TempDir(), "codex", nil)
	cfg.DryRun = true
	req := panelaunch.NewManualRequest(cfg, t.TempDir(), state.Store{}, hooks.EmptyConfig(), opts)
	if req.BriefingPath == "" || !strings.Contains(req.BriefingBody, prompt) {
		t.Fatalf("long Codex prompt briefing = path %q body length %d, want non-empty path containing %d-byte prompt", req.BriefingPath, len(req.BriefingBody), len(prompt))
	}
	if strings.Contains(req.Prompt, prompt) || !strings.Contains(req.Prompt, req.BriefingPath) {
		t.Fatalf("long Codex prompt remained embedded in the launch argument: %d bytes", len(req.Prompt))
	}
}

func TestNewSessionConfigForTUIAgentReloadsPlanMode(t *testing.T) {
	root := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("FANOUT_NEW_SESSION_PLAN_MODE", "")
	configPath := filepath.Join(xdg, "fanout", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"newSessionPlanMode":false}`), 0o644); err != nil {
		t.Fatal(err)
	}

	codex := newSessionConfigForTUIAgent(root, "codex", nil)
	if codex.Agent != "codex" || codex.PlanMode == nil || codex.PlanModeEnabled() {
		t.Fatalf("codex config = %+v, want explicit non-plan mode", codex)
	}

	if err := os.WriteFile(configPath, []byte(`{"newSessionPlanMode":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, agentName := range []string{"claude", "codex", "opencode"} {
		cfg := newSessionConfigForTUIAgent(root, agentName, nil)
		if cfg.Agent != agentName || !cfg.PlanModeEnabled() {
			t.Fatalf("%s config = %+v, want reloaded plan mode", agentName, cfg)
		}
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

	result, err := launchManualPaneFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.LaunchRequest{
		Prompt: "inspect workspace",
		Agents: []string{"claude", "claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.CreatedPaneIDs, []string{"%77", "%77"}) {
		t.Fatalf("created pane ids = %#v, want creation-order ids", result.CreatedPaneIDs)
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

	result, err := launchManualPaneFromTUI(repo, "fanout-test", "fanout", hookConfig, fanouttui.LaunchRequest{
		Prompt: "inspect workspace",
		Agents: []string{"claude", "claude"},
	})
	if err != nil {
		t.Fatalf("launchManualPaneFromTUI() error = %v, want partial success notice", err)
	}
	if !strings.Contains(result.Notice, "created 1 new agent pane(s); stopped after a later pane failed") {
		t.Fatalf("notice = %q, want partial success", result.Notice)
	}
	if !reflect.DeepEqual(result.CreatedPaneIDs, []string{"%77"}) {
		t.Fatalf("created pane ids = %#v, want first successful pane", result.CreatedPaneIDs)
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
	if got.Parent != panelaunch.ManualParentRef || got.IssueNum != -1 {
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

func TestLaunchShellPaneFromTUIRecordsSelectedWorktreeShellInSourceRoot(t *testing.T) {
	repo := t.TempDir()
	sibling := t.TempDir()
	initTUITestGitRepo(t, repo)
	initTUITestGitRepo(t, sibling)
	targetPath := filepath.Join(sibling, ".fanout", "worktrees", "child")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	installTUITmuxShim(t, "%78")

	err := launchShellPaneFromTUI(repo, "fanout-test", fanouttui.ShellLaunchRequest{
		TargetPath:        targetPath,
		SourceProjectRoot: sibling,
		Source:            "#101",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, statErr := os.Stat(state.Path(repo)); !os.IsNotExist(statErr) {
		t.Fatalf("source-root shell wrote state in TUI root or stat failed: %v", statErr)
	}
	store, err := state.LoadProject(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 1 {
		t.Fatalf("sibling state panes = %+v, want one shell pane", store.Panes)
	}
	got := store.Panes[0]
	if got.Kind != state.PaneKindShell || got.PaneID != "%78" || got.WorktreePath != targetPath {
		t.Fatalf("shell state = %+v, want sibling-owned shell pane", got)
	}
}

func TestLaunchAttachedAgentFromTUIRecordsAttachedAgentState(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	targetPath := filepath.Join(repo, ".fanout", "worktrees", "child")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	installFakeExecutable(t, "claude")
	installTUITmuxShim(t, "%88")

	notice, err := launchAttachedAgentFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.AttachLaunchRequest{
		Prompt: "inspect this worktree",
		Agents: []string{"claude"},
		Target: fanouttui.AttachTarget{
			TargetPath:       targetPath,
			SourceParent:     "100",
			SourceIssueNum:   101,
			SourceBranchName: "fanout/child-101",
			SourceLabel:      "#101",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if notice != "" {
		t.Fatalf("notice = %q, want empty success notice", notice)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 1 {
		t.Fatalf("state panes = %+v, want one attached pane", store.Panes)
	}
	got := store.Panes[0]
	if got.Kind != state.PaneKindAttachedAgent || got.Agent != "claude" || got.PaneID != "%88" {
		t.Fatalf("attached state = %+v, want attached claude pane", got)
	}
	if got.Parent != "100" || got.IssueNum != -1 || got.SourceIssueNum != 101 || got.SourceParent != "100" {
		t.Fatalf("attached identity = parent %s issue %d source %s/%d", got.Parent, got.IssueNum, got.SourceParent, got.SourceIssueNum)
	}
	if got.WorktreePath != targetPath || got.BranchName != "fanout/child-101" {
		t.Fatalf("attached worktree/branch = %q/%q", got.WorktreePath, got.BranchName)
	}
	if got.Slug != "child-claude-a1" || got.DisplayName != "claude for #101" {
		t.Fatalf("slug/display = %q/%q", got.Slug, got.DisplayName)
	}
	if _, err := os.Stat(filepath.Join(targetPath, ".fanout", "worktree-metadata.json")); !os.IsNotExist(err) {
		t.Fatalf("attached launch wrote worktree metadata or stat failed: %v", err)
	}
}

func TestLaunchAttachedAgentFromTUIRecordsActualWatcherParent(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	targetPath := filepath.Join(repo, ".fanout", "worktrees", "watched")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	installFakeExecutable(t, "claude")
	installTUITmuxShim(t, "%91")

	_, err := launchAttachedAgentFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.AttachLaunchRequest{
		Prompt: "inspect watched issue",
		Agents: []string{"claude"},
		Target: fanouttui.AttachTarget{
			TargetPath:     targetPath,
			SourceParent:   panelaunch.WatchParentRef,
			SourceIssueNum: 425,
			SourceLabel:    "#425",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 1 {
		t.Fatalf("state panes = %+v, want one attached pane", store.Panes)
	}
	got := store.Panes[0]
	if got.Parent != "425" || got.SourceParent != "425" || got.SourceIssueNum != 425 {
		t.Fatalf("attached provenance = parent %q source %q/%d, want 425/425/425", got.Parent, got.SourceParent, got.SourceIssueNum)
	}
}

func TestLaunchAttachedAgentFromTUIRecordsStateInSourceRoot(t *testing.T) {
	repo := t.TempDir()
	sibling := t.TempDir()
	initTUITestGitRepo(t, repo)
	initTUITestGitRepo(t, sibling)
	targetPath := filepath.Join(sibling, ".fanout", "worktrees", "child")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	installFakeExecutable(t, "claude")
	installTUITmuxShim(t, "%89")

	_, err := launchAttachedAgentFromTUI(repo, "fanout-test", "fanout", hooks.EmptyConfig(), fanouttui.AttachLaunchRequest{
		Prompt: "inspect this sibling worktree",
		Agents: []string{"claude"},
		Target: fanouttui.AttachTarget{
			TargetPath:        targetPath,
			SourceProjectRoot: sibling,
			SourceParent:      "100",
			SourceIssueNum:    101,
			SourceBranchName:  "fanout/child-101",
			SourceLabel:       "#101",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, statErr := os.Stat(state.Path(repo)); !os.IsNotExist(statErr) {
		t.Fatalf("source-root attach wrote state in TUI root or stat failed: %v", statErr)
	}
	store, err := state.LoadProject(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 1 {
		t.Fatalf("sibling state panes = %+v, want one attached pane", store.Panes)
	}
	got := store.Panes[0]
	if got.Kind != state.PaneKindAttachedAgent || got.PaneID != "%89" || got.WorktreePath != targetPath {
		t.Fatalf("attached state = %+v, want sibling-owned attached pane", got)
	}
	if got.SourceParent != "100" || got.SourceIssueNum != 101 {
		t.Fatalf("source identity = %s/%d, want 100/101", got.SourceParent, got.SourceIssueNum)
	}
}

func TestNewAttachedPaneRequestUsesParentScopedBriefingPath(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	cfg := newSessionConfigForTUIAgent(repo, "claude", nil)
	prompt := "inspect this worktree\nthen report"

	first := newAttachedPaneRequest(cfg, repo, state.Store{}, hooks.EmptyConfig(), prompt, filepath.Join(repo, ".fanout", "worktrees", "child"), fanouttui.AttachTarget{
		TargetPath:       filepath.Join(repo, ".fanout", "worktrees", "child"),
		SourceParent:     "100",
		SourceIssueNum:   101,
		SourceBranchName: "fanout/child-101",
		SourceLabel:      "#101",
	})
	second := newAttachedPaneRequest(cfg, repo, state.Store{}, hooks.EmptyConfig(), prompt, filepath.Join(repo, ".fanout", "worktrees", "child"), fanouttui.AttachTarget{
		TargetPath:       filepath.Join(repo, ".fanout", "worktrees", "child"),
		SourceParent:     "200",
		SourceIssueNum:   201,
		SourceBranchName: "fanout/child-201",
		SourceLabel:      "#201",
	})
	if first.Number != -1 || second.Number != -1 {
		t.Fatalf("numbers = %d/%d, want same first synthetic number", first.Number, second.Number)
	}
	if first.BriefingPath == "" || second.BriefingPath == "" || first.BriefingPath == second.BriefingPath {
		t.Fatalf("briefing paths = %q/%q, want non-empty parent-scoped paths", first.BriefingPath, second.BriefingPath)
	}
	if !strings.Contains(first.Prompt, first.BriefingPath) || !strings.Contains(second.Prompt, second.BriefingPath) {
		t.Fatalf("prompts do not reference briefing paths:\n%q\n%q", first.Prompt, second.Prompt)
	}
	if first.TaskID != "" || first.SourceTaskID != "" {
		t.Fatalf("task identity = %q source=%q, want no task collision for issue source", first.TaskID, first.SourceTaskID)
	}
}

func TestNewAttachedPaneRequestKeepsSourceTaskOutOfStateIdentity(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	cfg := newSessionConfigForTUIAgent(repo, "claude", nil)

	got := newAttachedPaneRequest(cfg, repo, state.Store{}, hooks.EmptyConfig(), "inspect", filepath.Join(repo, ".fanout", "worktrees", "task"), fanouttui.AttachTarget{
		TargetPath:       filepath.Join(repo, ".fanout", "worktrees", "task"),
		SourceParent:     "plan:launch-plan",
		SourceTaskID:     "api-client",
		SourceBranchName: "fanout/api-client",
		SourceLabel:      "api-client",
	})

	if got.TaskID != "" {
		t.Fatalf("TaskID = %q, want empty synthetic identity for attached-agent", got.TaskID)
	}
	if got.SourceTaskID != "api-client" {
		t.Fatalf("SourceTaskID = %q, want api-client", got.SourceTaskID)
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
	_, err = launchWatchStandalone(repo, "fanout-test", "fanout", settings.Defaults(), hooks.EmptyConfig(), ghissue.Issue{
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

	if watchPaneMatchesLive(pane, backend.LivePane{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%1"}, CurrentPath: "/repo/other"}) {
		t.Fatal("watchPaneMatchesLive() = true for reused pane id in another worktree")
	}
	if !watchPaneMatchesLive(pane, backend.LivePane{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%1"}, CurrentPath: "/repo/.fanout/worktrees/child-101/subdir"}) {
		t.Fatal("watchPaneMatchesLive() = false for live pane under recorded worktree")
	}
	if watchPaneMatchesLive(pane, backend.LivePane{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%2"}, CurrentPath: "/repo/.fanout/worktrees/child-101"}) {
		t.Fatal("watchPaneMatchesLive() = true for different pane id")
	}
}

func TestWatchPaneMatchesLiveRequiresBackendMatch(t *testing.T) {
	pane := state.Pane{Backend: backend.Herdr, PaneID: "w1:p1", WorktreePath: "/repo/wt"}
	live := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Tmux, Pane: "w1:p1"},
		CurrentPath: "/repo/wt",
	}
	if watchPaneMatchesLive(pane, live) {
		t.Fatal("watchPaneMatchesLive() = true across runtime backends")
	}
}

func TestWatchPaneMatchesLiveRequiresExactHerdrIdentity(t *testing.T) {
	pane := state.Pane{
		Backend: backend.Herdr, PaneID: "w1:p1", Agent: "codex",
		WorktreePath: "/repo/.fanout/worktrees/child", HerdrWorkspaceID: "w1",
		HerdrWorkspaceLabel: "owned-label-1",
		HerdrTerminalID:     "term-1", HerdrRepoKey: "/repo/.git",
		HerdrAgentID: "fanout-child", HerdrSession: "fanout-owned",
		HerdrSocketPath: "/tmp/fanout-owned/herdr.sock",
		HerdrAgentSession: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "thread-1",
		},
	}
	live := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		CurrentPath: pane.WorktreePath, WorkspaceLabel: "owned-label-1", TerminalID: "term-1",
		AgentID: "fanout-child", AgentProvider: "codex", AgentPresent: true,
		RepoKey: "/repo/.git", ProjectRoot: "/repo", WorktreePath: pane.WorktreePath,
		SessionID: "fanout-owned", SocketPath: "/tmp/fanout-owned/herdr.sock",
		AgentSession: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "thread-1",
		},
	}
	if !watchPaneMatchesLive(pane, live) {
		t.Fatal("watchPaneMatchesLive() rejected exact bound Herdr identity")
	}
	live.AgentProvider = "claude"
	if watchPaneMatchesLive(pane, live) {
		t.Fatal("watchPaneMatchesLive() accepted a different Herdr provider")
	}
}

func TestWatchPaneMatchesLiveAllowsLateHerdrSession(t *testing.T) {
	pane := state.Pane{
		Backend: backend.Herdr, PaneID: "w1:p1", Agent: "codex",
		WorktreePath: "/repo/.fanout/worktrees/child", HerdrWorkspaceID: "w1",
		HerdrWorkspaceLabel: "owned-label-1",
		HerdrTerminalID:     "term-1", HerdrRepoKey: "/repo/.git",
		HerdrAgentID: "fanout-child", HerdrSession: "fanout-owned",
		HerdrSocketPath: "/tmp/fanout-owned/herdr.sock",
	}
	live := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		CurrentPath: pane.WorktreePath, WorkspaceLabel: "owned-label-1", TerminalID: "term-1",
		AgentID: "fanout-child", AgentProvider: "codex", AgentPresent: true,
		RepoKey: "/repo/.git", ProjectRoot: "/repo", WorktreePath: pane.WorktreePath,
		SessionID: "fanout-owned", SocketPath: "/tmp/fanout-owned/herdr.sock",
		AgentSession: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "thread-1",
		},
	}
	if !watchPaneMatchesLive(pane, live) {
		t.Fatal("watchPaneMatchesLive() rejected a valid late Herdr agent session")
	}
	live.AgentSession.Source = "herdr:claude"
	if watchPaneMatchesLive(pane, live) {
		t.Fatal("watchPaneMatchesLive() accepted a foreign late Herdr agent session")
	}
}

func TestWatchPaneMatchesLiveRequiresShellKeyForShellRows(t *testing.T) {
	pane := state.Pane{
		Kind:         state.PaneKindShell,
		PaneID:       "%1",
		ShellKey:     "shell-root",
		WorktreePath: "/repo",
	}

	if watchPaneMatchesLive(pane, backend.LivePane{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%1"}, CurrentPath: "/repo", ShellKey: "other-shell"}) {
		t.Fatal("watchPaneMatchesLive() = true for shell row with reused pane id")
	}
	if !watchPaneMatchesLive(pane, backend.LivePane{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%1"}, CurrentPath: "/repo/elsewhere", ShellKey: "shell-root"}) {
		t.Fatal("watchPaneMatchesLive() = false for shell row with matching shell key")
	}
}

func TestWatchPaneMatchesLivePrefersLivenessKeyForOrdinaryRows(t *testing.T) {
	pane := state.Pane{
		PaneID:       "%1",
		ShellKey:     "shell-child",
		WorktreePath: "/repo/.fanout/worktrees/child-101",
	}

	if watchPaneMatchesLive(pane, backend.LivePane{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%1"}, CurrentPath: pane.WorktreePath, ShellKey: "shell-reused"}) {
		t.Fatal("watchPaneMatchesLive() = true for ordinary row with reused pane id")
	}
	if !watchPaneMatchesLive(pane, backend.LivePane{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%1"}, CurrentPath: "/tmp/changed", ShellKey: "shell-child"}) {
		t.Fatal("watchPaneMatchesLive() = false for ordinary row with matching liveness key")
	}
}

func TestWatchLivePaneCacheReusesListingUntilReset(t *testing.T) {
	calls := 0
	cache := &watchLivePaneCache{
		list: func() ([]backend.LivePane, error) {
			calls++
			return []backend.LivePane{
				{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%1"}, CurrentPath: "/repo/.fanout/worktrees/one-501"},
				{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%2"}, CurrentPath: "/repo/.fanout/worktrees/two-502"},
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

func TestWatchLivePaneCacheFailsClosedWhenIdentityListingIsIncomplete(t *testing.T) {
	installTUIIdentityTitleFailureTmuxShim(t)
	cache := &watchLivePaneCache{list: tmuxbackend.New().ListLiveForIdentity}

	alive, err := cache.Alive(state.Pane{PaneID: "%9", ShellKey: "key-nine"})
	if err == nil || !strings.Contains(err.Error(), "titles") {
		t.Fatalf("Alive() = %v, %v, want false and strict title-listing error", alive, err)
	}
	if alive {
		t.Fatal("Alive() = true after incomplete identity sweep")
	}
}

func TestWatchLivePaneCacheFailsClosedWhenRecordedKeyIsUnavailable(t *testing.T) {
	cache := &watchLivePaneCache{
		list: func() ([]backend.LivePane, error) {
			return []backend.LivePane{{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%9"}, CurrentPath: "/wt/nine"}}, nil
		},
	}

	alive, err := cache.Alive(state.Pane{PaneID: "%9", ShellKey: "key-nine", WorktreePath: "/wt/nine"})
	if err == nil || !strings.Contains(err.Error(), "liveness key is unavailable") {
		t.Fatalf("Alive() = %v, %v, want false and unavailable-key error", alive, err)
	}
	if alive {
		t.Fatal("Alive() = true while the recorded key is unavailable")
	}
}

func TestWatchParentLaunchResultCompletesWhenAllTargetsWereCreated(t *testing.T) {
	plan := run.Plan{
		Targets: []ghissue.Issue{{Number: 501}, {Number: 502}},
	}
	if got := watchParentLaunchResult(plan, []int{501, 502}); got.Deferred {
		t.Fatal("watchParentLaunchResult() Deferred = true, want false after all targets were created")
	}
}

func TestWatchParentLaunchResultRequeuesAfterPartialLaunch(t *testing.T) {
	plan := run.Plan{
		Targets: []ghissue.Issue{{Number: 501}, {Number: 502}},
	}
	if got := watchParentLaunchResult(plan, []int{501}); !got.Deferred {
		t.Fatal("watchParentLaunchResult() Deferred = false, want true while an uncreated target remains")
	}
}

func TestWatchParentLaunchResultRequeuesLimitDeferredTargets(t *testing.T) {
	plan := run.Plan{
		Targets:       []ghissue.Issue{{Number: 501}},
		LimitDeferred: []ghissue.Issue{{Number: 502}},
	}
	if got := watchParentLaunchResult(plan, []int{501}); !got.Deferred {
		t.Fatal("watchParentLaunchResult() Deferred = false, want true while a limit-deferred target remains")
	}
}

func TestWatchParentLaunchResultRequeuesBlockedRowsWithoutLimit(t *testing.T) {
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
	cfg := &cliflags.Config{
		Parent:        500,
		ParentRef:     "500",
		ParentMode:    cliflags.ModeIssue,
		UnblockedOnly: true,
	}
	prepared, _, err := prepareWatchParentPlan(repo, ghissue.Runner{}, 500)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildWatchParentPlan(repo, cfg, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if got := watchParentLaunchResult(plan, []int{501}); !got.Deferred {
		t.Fatal("watchParentLaunchResult() Deferred = false, want true while blocked children remain")
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
    elif [[ "$*" == *pane_active* ]]; then
      printf '%%tui:@1:0:0:fanout tui\n%%2:@1:1:1:child\n'
    fi
    ;;
  bind-key|unbind-key|list-keys|set-option|select-pane|select-layout|kill-pane)
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

func installTUIHerdrShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "herdr-args.txt")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "$HERDR_SHIM_ARGS"
printf '%s\n' '---' >> "$HERDR_SHIM_ARGS"
`
	path := filepath.Join(dir, "herdr")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SHIM_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

func installTUIActivePaneTmuxShim(t *testing.T, activePaneID string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  list-panes)
    if [[ "$*" == *pane_active* ]]; then
      if [[ "$TMUX_SHIM_ACTIVE_PANE" == "%tui" ]]; then
        printf '%%tui:@1:0:1:fanout tui\n%%2:@1:1:0:child\n'
      else
        printf '%%tui:@1:0:0:fanout tui\n%%2:@1:1:1:child\n'
      fi
    fi
    ;;
  *)
    ;;
esac
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_SHIM_ACTIVE_PANE", activePaneID)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installTUISettingsReloadTmuxShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "tmux-args.txt")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "$TMUX_SHIM_ARGS"
printf '%s\n' '---' >> "$TMUX_SHIM_ARGS"
case "${1:-}" in
  list-keys)
    if [[ "$*" == *" -T prefix D"* ]]; then
      printf 'bind-key D run-shell -b fanout dashboard --web --open; tmux new-window -n fanout-dashboard\n'
    elif [[ "$*" == *" -T root F12"* ]]; then
      printf 'bind-key -n F12 run-shell -b fanout dashboard --web --open; tmux new-window -n fanout-dashboard\n'
    elif [[ "$*" == *" -T prefix M"* ]]; then
      printf 'bind-key M display-popup -E fanout __worktree-action --pane #{pane_id}\n'
    elif [[ "$*" == *" -T prefix T"* ]]; then
      printf 'bind-key T run-shell fanout focus-console --from "#{pane_id}"; tmux display-message "fanout: focus-console failed"\n'
    elif [[ "$*" == *" -T root F11"* ]]; then
      printf 'bind-key -n F11 run-shell fanout focus-console --from "#{pane_id}"; tmux display-message "fanout: focus-console failed"\n'
    fi
    ;;
  bind-key|unbind-key|set-option|select-pane|select-layout|kill-pane)
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
