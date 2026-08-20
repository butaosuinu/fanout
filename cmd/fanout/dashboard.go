package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/app/prmerge"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/browser"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
	"github.com/butaosuinu/fanout/internal/ui/dashboard"
)

// defaultDashboardKey is the tmux prefix-table key fanout binds to open the dashboard.
const defaultDashboardKey = "D"

// defaultDashboardDirectKey opens the dashboard without tmux's prefix key.
const defaultDashboardDirectKey = "F12"

// defaultWorktreeActionKey is the tmux prefix key fanout binds to open the
// focused pane's same-worktree action popup.
const defaultWorktreeActionKey = "M"

// dashboardHostRuntime is the runtime whose shortcut opened this dashboard: it
// both registers the keys and carries the viewer the notice goes back to.
var dashboardHostRuntime = func() backend.Backend { return paneruntime.NewTmux() }

var (
	openDashboardBrowser = openBrowser
	showDashboardStatus  = func(msg string) error {
		host, ok := backend.AsPopupHost(dashboardHostRuntime())
		if !ok {
			return nil
		}
		// The viewer that pressed the key is named by the environment the shortcut
		// set on this process; unset, the runtime picks its own current viewer.
		return host.NotifyViewer(os.Getenv(backend.NotifyViewerEnv), msg)
	}
	openBrowserWaitPeriod = 2 * time.Second
)

type dashboardFlags struct {
	port      int
	open      bool
	noToken   bool
	noKeybind bool
}

// isDashboardRequest detects the `fanout dashboard ...` subcommand, dispatched
// before cliflags.Parse (which requires a <parent> positional).
func isDashboardRequest(args []string) bool {
	return len(args) > 0 && args[0] == "dashboard"
}

// cmdDashboard starts the localhost web dashboard. It accepts --web
// (the only mode today; a no-op kept for forward-compat with a future --tui),
// --port, --open, --no-token, and --no-keybind.
//
//nolint:gocognit,gocyclo,funlen // Dashboard startup is one ordered lock, run-file, server, health, and shutdown transaction.
func cmdDashboard(args []string, lg *log.Logger) exitcode.Code {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprint(lg.Stdout(), dashboardUsage)
			return exitcode.OK
		}
	}
	flags, code := parseDashboardFlags(args, lg)
	if code != exitcode.OK {
		return code
	}

	root, err := dashboardProjectRoot()
	if err != nil {
		lg.Err("dashboard: %v", err)
		return exitcode.Env
	}

	// Install local excludes BEFORE creating any .fanout artifact (the startup
	// lock, the run file), so none of them ever surface in `git status` even if
	// startup fails early (e.g. a port-in-use bind error).
	if err = worktree.EnsureLocalExclude(root); err != nil {
		lg.Debug("dashboard: ensure local exclude: %v", err)
	}

	// Serialize startup so two near-simultaneous keypress launches reuse one
	// server instead of each binding its own port.
	// Best-effort: if the lock can't be taken, proceed unsynchronized.
	startupLock, lockErr := dashboard.LockStartup(root)
	if lockErr != nil {
		lg.Debug("dashboard: startup lock unavailable (%v); proceeding unsynchronized", lockErr)
	}
	lockReleased := false
	releaseStartup := func() {
		if !lockReleased {
			dashboard.UnlockStartup(startupLock)
			lockReleased = true
		}
	}
	defer releaseStartup()

	// Reuse a live server instead of starting a second one (the keybind may be
	// pressed repeatedly).
	if rf, _ := dashboard.ReadRunFile(root); rf != nil && rf.IsLive() {
		releaseStartup()
		lg.Info("dashboard already running: %s", rf.URL)
		if flags.open {
			openBrowserBestEffort(rf.URL, lg)
		}
		return exitcode.OK
	}

	token := ""
	if !flags.noToken {
		token, err = randomToken()
		if err != nil {
			lg.Err("dashboard: generate token: %v", err)
			return exitcode.Env
		}
	}
	ownsManagedPane, readManagedPane := dashboardManagedPeekPorts(root, paneruntime.OpenProject)
	srv, err := dashboard.New(dashboard.Options{
		ProjectRoot: root,
		Port:        flags.port,
		Token:       token,
		// Resolved lazily on the poller's gh goroutine so a slow `gh repo view`
		// never delays binding localhost or the state-only paint.
		ResolveGH:       dashboardGHResolver(root, lg),
		ListLive:        runtimeListLiveForProject(root, false),
		OwnsManagedPane: ownsManagedPane,
		ReadManagedPane: readManagedPane,
		MergePR:         dashboardMergePort(root),
		DeleteBranch:    prmerge.Service{GH: ghissue.Runner{Cwd: root}}.DeleteBranch,
	})
	if err != nil {
		lg.Err("dashboard: bind 127.0.0.1: %v", err)
		return exitcode.Env
	}
	url := srv.URL()

	if err := dashboard.WriteRunFile(root, dashboard.RunFile{
		URL:       url,
		PID:       os.Getpid(),
		Token:     token,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		lg.Warn("dashboard: write run file: %v", err)
	}
	defer func() {
		// Only remove the run file if it still records this process: a newer
		// dashboard may have replaced it after we stopped serving.
		if err := dashboard.RemoveOwnRunFile(root, token, os.Getpid()); err != nil {
			lg.Warn("dashboard: remove run file: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.Start(ctx)
	// Hold the startup lock until /healthz actually answers: only then will a
	// concurrent launch's liveness probe succeed and reuse this server instead
	// of binding a duplicate. The browser-open and keybind steps below can take
	// longer than the probe timeout, so they must happen after the release.
	waitDashboardHealthy(srv.HealthURL())
	releaseStartup()

	lg.Ok("dashboard: %s", url)
	lg.Info("press Ctrl-C to stop")
	if flags.open {
		openBrowserBestEffort(url, lg)
	}

	if !flags.noKeybind {
		keybindEnabled := settings.Resolve(root, settings.CLIOverrides{}, lg.Warn).DashboardKeybind
		selection, selectionErr := resolveDisplayBackendSelection(root)
		if selectionErr != nil {
			lg.Warn("dashboard: runtime backend selection unavailable (%v); skipping keybind", selectionErr)
		} else {
			bindDashboardKeyForBackend(lg, keybindEnabled, selection, root)
		}
	}

	if err := srv.Wait(ctx); err != nil {
		lg.Err("dashboard: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func dashboardManagedPeekPorts(
	projectRoot string,
	open func(string) (paneruntime.ManagedSession, error),
) (func(sessionview.PaneView) bool, func(sessionview.PaneView, int) (string, error)) {
	bind := func(pv sessionview.PaneView) (backend.Backend, backend.PaneRef, error) {
		owned, err := open(projectRoot)
		if err != nil {
			return nil, backend.PaneRef{}, err
		}
		return bindManagedPane(owned, pv.SavedPane)
	}
	owns := func(pv sessionview.PaneView) bool {
		_, _, err := bind(pv)
		return err == nil
	}
	read := func(pv sessionview.PaneView, lines int) (string, error) {
		bound, ref, err := bind(pv)
		if err != nil {
			return "", err
		}
		return bound.Read(ref, lines)
	}
	return owns, read
}

// bindDashboardKeyForBackend registers the shortcut through the resolved
// runtime's own capability. A managed runtime must first reopen the exact
// repository-owned session whose private config carries the key table.
func bindDashboardKeyForBackend(lg *log.Logger, enabled bool, selection backend.Selection, projectRoot string) {
	selected, err := paneruntime.NewBackend(selection.Name, "", "")
	if err != nil {
		return
	}
	if _, ok := backend.AsShortcutBinder(selected); ok {
		bindDashboardKey(lg, enabled)
		return
	}
	if _, ok := backend.AsDashboardShortcutBinder(selected); !ok {
		return
	}
	owned, err := paneruntime.OpenProject(projectRoot)
	if err != nil {
		lg.Debug("dashboard keybind: open managed session: %v", err)
		return
	}
	syncOwnedDashboardKey(lg, enabled, owned)
}

// waitDashboardHealthy polls the token-free /healthz endpoint until it answers
// 200 or a short deadline passes. Best-effort: on timeout it returns anyway so
// startup never hangs; the caller only uses it to gate the lock release.
func waitDashboardHealthy(healthURL string) {
	client := http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := client.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func parseDashboardFlags(args []string, lg *log.Logger) (dashboardFlags, exitcode.Code) {
	f := dashboardFlags{port: 0}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--web":
			// only mode today; explicit no-op
		case "--open":
			f.open = true
		case "--no-token":
			f.noToken = true
		case "--no-keybind":
			f.noKeybind = true
		case "--port":
			if i+1 >= len(args) {
				lg.Err("dashboard: --port requires an argument")
				return f, exitcode.Env
			}
			i++
			n, err := parsePort(args[i])
			if err != nil {
				lg.Err("dashboard: %v", err)
				return f, exitcode.Env
			}
			f.port = n
		default:
			lg.Err("dashboard: unknown option: %s", a)
			fmt.Fprint(lg.Stderr(), dashboardUsage)
			return f, exitcode.Invocation
		}
	}
	return f, exitcode.OK
}

func parsePort(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("--port must be a non-negative integer, got: %s", s)
		}
		n = n*10 + int(r-'0')
		if n > 65535 {
			return 0, fmt.Errorf("--port out of range (0-65535), got: %s", s)
		}
	}
	if s == "" {
		return 0, fmt.Errorf("--port must be a non-negative integer")
	}
	return n, nil
}

// dashboardGHResolver returns the lazy GitHub resolver the poller runs on its
// background goroutine. A failure is not fatal: the dashboard degrades to a
// state-only view, so we return a sticky error that marks Degraded.GitHub
// instead of aborting. Returning an untyped nil provider on error keeps the
// poller's gh interface truly nil (no typed-nil-pointer panic).
func dashboardGHResolver(root string, lg *log.Logger) func() (string, dashboard.GHProvider, error) {
	return func() (string, dashboard.GHProvider, error) {
		resolved, err := sessionview.ResolveGH(root)
		if err != nil {
			lg.Warn("dashboard: GitHub unavailable (%v); showing state-only view", err)
			return "", nil, err
		}
		return resolved.NameWithOwner(), resolved, nil
	}
}

func bindDashboardKey(lg *log.Logger, enabled bool) {
	if !enabled {
		return
	}
	syncDashboardKey(lg, enabled, false)
}

func bindRuntimeDashboardKey(lg *log.Logger, enabled bool, runtimeBackend backend.Backend) {
	if _, ok := backend.AsShortcutBinder(runtimeBackend); ok {
		bindDashboardKey(lg, enabled)
		return
	}
	syncRuntimeDashboardKey(lg, enabled, runtimeBackend)
}

func runtimeDashboardKeyBinder(rt *run.Runtime) func(*log.Logger, bool) {
	return func(lg *log.Logger, enabled bool) {
		bindRuntimeDashboardKey(lg, enabled, rt.Backend)
	}
}

func syncRuntimeDashboardKey(lg *log.Logger, enabled bool, runtimeBackend backend.Backend) {
	if runtimeBackend == nil {
		return
	}
	binder, ok := backend.AsDashboardShortcutBinder(runtimeBackend)
	if !ok {
		return
	}
	bin, err := os.Executable()
	if err != nil {
		lg.Warn("dashboard keybind: resolve fanout binary: %v", err)
		return
	}
	err = binder.SyncDashboardShortcut(backend.DashboardShortcutOptions{
		Enabled: enabled, FanoutBin: bin, Environment: os.Environ(),
	})
	if err != nil {
		lg.Warn("dashboard keybind: %v", err)
		return
	}
	if enabled {
		lg.Info("dashboard keybind: press %s to open the dashboard", defaultDashboardDirectKey)
	}
}

func syncOwnedDashboardKey(lg *log.Logger, enabled bool, owned paneruntime.ManagedSession) {
	if owned == nil || owned.Backend() == nil {
		return
	}
	syncRuntimeDashboardKey(lg, enabled, owned.Backend())
}

func syncDashboardKey(lg *log.Logger, enabled bool, cleanupDisabled bool) {
	binder, ok := backend.AsShortcutBinder(dashboardHostRuntime())
	if !ok {
		return
	}
	if !enabled {
		if cleanupDisabled {
			unbindDashboardKeys(binder, lg)
		}
		return
	}
	bin, err := os.Executable()
	if err != nil {
		lg.Debug("dashboard keybind: cannot resolve fanout binary path: %v", err)
		return
	}
	// The binding resolves the repo from the pressing pane at keypress time
	// (@fanout_project_root when fanout recorded it, otherwise
	// #{pane_current_path}) and cmdDashboard maps that to the main worktree, so
	// no repo root needs to be baked in here.
	if err := binder.BindDashboardShortcuts(defaultDashboardKey, defaultDashboardDirectKey, bin); err != nil {
		lg.Debug("dashboard keybind: %v (not in tmux?)", err)
		return
	}
	lg.Info("tmux keybind: press %s or prefix + %s to open the dashboard", defaultDashboardDirectKey, defaultDashboardKey)
	bindWorktreeActionKey(binder, lg, bin)
}

func unbindDashboardKeys(binder backend.ShortcutBinder, lg *log.Logger) {
	if err := binder.UnbindDashboardShortcuts(defaultDashboardKey, defaultDashboardDirectKey); err != nil {
		lg.Debug("dashboard keybind cleanup: %v (not in tmux?)", err)
	}
	if err := binder.UnbindWorktreeActionShortcut(defaultWorktreeActionKey); err != nil {
		lg.Debug("worktree action keybind cleanup: %v (not in tmux?)", err)
	}
}

func bindWorktreeActionKey(binder backend.ShortcutBinder, lg *log.Logger, bin string) {
	if err := binder.BindWorktreeActionShortcut(defaultWorktreeActionKey, bin); err != nil {
		lg.Debug("worktree action keybind: %v (tmux too old or not in tmux?)", err)
		return
	}
	lg.Info("tmux keybind: press prefix + %s for worktree actions", defaultWorktreeActionKey)
}

// dashboardProjectRoot resolves the repo root whose .fanout/state.json the
// dashboard should read. fanout records state at the toplevel of the worktree it
// ran in, and its fanned child panes live at <root>/.fanout/worktrees/<slug>.
// So: use the current worktree toplevel when it has state; if instead we are
// inside a .fanout/worktrees/<slug> child (a fanned pane, e.g. via a keybind),
// recover that parent root. Crucially the climb is bounded to that one structure
// — it never escapes into an unrelated ancestor checkout that merely happens to
// have its own .fanout/state.json. A never-fanned worktree falls back to itself.
func dashboardProjectRoot() (string, error) {
	return resolveDisplayProjectRoot()
}

func resolveRootFromTop(top string, hasState func(string) bool) string {
	if hasState(top) {
		return top
	}
	marker := string(filepath.Separator) + filepath.Join(".fanout", "worktrees") + string(filepath.Separator)
	if i := strings.LastIndex(top, marker); i >= 0 {
		if parent := top[:i]; hasState(parent) {
			return parent
		}
	}
	return top
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func openBrowserBestEffort(url string, lg *log.Logger) {
	if err := openDashboardBrowser(url); err != nil {
		lg.Warn("dashboard: could not open browser (%v); visit %s manually", err, url)
		showDashboardStatusBestEffort("fanout dashboard: browser open failed; visit "+url, lg)
		return
	}
	showDashboardStatusBestEffort("fanout dashboard: "+url, lg)
}

func showDashboardStatusBestEffort(msg string, lg *log.Logger) {
	if err := showDashboardStatus(msg); err != nil {
		lg.Debug("dashboard status line: %v", err)
	}
}

func openBrowser(url string) error {
	return browser.Open(url, openBrowserWaitPeriod)
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// dashboardMergePort grants the dashboard its single mutation capability.
// dashboard.New has no default for this field, so a server assembled without it
// answers POST /api/pr/merge with 503 — a silent failure with no other symptom.
// Keeping the grant in a named function lets a test pin that cmd still makes it.
func dashboardMergePort(root string) func(context.Context, prmerge.Request) (prmerge.Result, error) {
	return prmerge.Service{GH: ghissue.Runner{Cwd: root}}.Merge
}

const dashboardUsage = `Usage: fanout dashboard [--web] [--port N] [--open] [--no-token] [--no-keybind]

Start a localhost web dashboard that visualizes fanout Sessions (panes grouped
by parent issue) live: pane liveness, issue state, and PR merge status. The
server binds 127.0.0.1 only and reads through GET-only endpoints. Its one
mutation is the merge button: it merges a pull request on GitHub and can delete
that PR's remote branch. It never changes your working tree, local branches,
worktrees, or panes.

Options:
  --web           Web dashboard mode (default; reserved for a future --tui).
  --port N        TCP port to bind on 127.0.0.1. Default 0 (OS-assigned).
  --open          Open the dashboard URL in the default browser.
  --no-token      Disable the access token (loopback-only, single-user laptops).
                  The merge button is refused while it is off: the loopback port
                  is reachable by every local process, and merging is the one
                  thing this server can change outside your machine.
                  By default a random token gates /api/* and is embedded in the
                  printed URL.
  --no-keybind    Do not register runtime dashboard shortcuts this run. Existing
                  bindings are left unchanged.
  -h, --help      Show this message.

The dashboard reuses a server that is already running (recorded in
.fanout/dashboard.json) instead of starting a second one.
`
