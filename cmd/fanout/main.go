package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/app/stateemitter"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

// version and commit are injected at build time via -ldflags (see release.yml,
// which names main.version). Keep them here in package main; do not move them.
var (
	version = "dev"
	commit  = "none"
)

//nolint:funlen // The composition root keeps the first-match-wins dispatch and launch wiring visible together.
func main() {
	lg := log.New(false)
	commandName := invokedCommandName(os.Args)
	args := os.Args[1:]

	// Subcommand dispatch: an ordered, first-match-wins table. The order is
	// load-bearing (e.g. update before check-update, TUI before the popups) —
	// keep it identical to preserve behavior.
	type dispatch struct {
		match  func([]string) bool
		handle func() exitcode.Code
	}
	table := []dispatch{
		{func([]string) bool { return herdrrun.IsPaneLauncherRequest() }, func() exitcode.Code {
			return exitcode.Code(herdrrun.RunPaneLauncher(os.Stdin, os.Stdout, os.Stderr))
		}},
		{telemetry.IsRequest, func() exitcode.Code {
			return exitcode.Code(stateemitter.Run(os.Args[2:], os.Getenv, herdrEmitterObserver{}, os.Stderr))
		}},
		{hooks.IsBackgroundRunnerRequest, func() exitcode.Code { return exitcode.Code(hooks.RunBackgroundRunner(os.Args[2:], os.Stderr)) }},
		{herdrrun.IsSupervisorRequest, func() exitcode.Code { return exitcode.Code(herdrrun.RunSupervisor(os.Args[2:], os.Stderr)) }},
		{isVersionRequest, func() exitcode.Code { fmt.Fprintln(os.Stdout, versionLine()); return exitcode.OK }},
		{isUpdateRequest, func() exitcode.Code { return cmdUpdate(os.Args[2:], version, ghissue.Runner{}, lg) }},
		{isCheckUpdateRequest, func() exitcode.Code { return cmdCheckUpdate(version, ghissue.Runner{}, lg) }},
		{isHerdrLifecycleRequest, func() exitcode.Code { return cmdHerdrLifecycle(os.Args[2:], lg) }},
		{isTUIRequest, func() exitcode.Code { return cmdTUI(commandName, lg) }},
		{isTUINewPanePopupRequest, func() exitcode.Code { return cmdTUINewPanePopup(os.Args[2:], lg) }},
		{isTUIHelpPopupRequest, func() exitcode.Code { return cmdTUIHelpPopup(os.Args[2:], lg) }},
		{isTUIClosePopupRequest, func() exitcode.Code { return cmdTUIClosePopup(os.Args[2:], lg) }},
		{isTUISettingsPopupRequest, func() exitcode.Code { return cmdTUISettingsPopup(os.Args[2:], lg) }},
		{isCodexPlanTUIRequest, func() exitcode.Code { return cmdCodexPlanTUI(os.Args[2:], lg) }},
		{isCodexTeamTUIRequest, func() exitcode.Code { return cmdCodexTeamTUI(os.Args[2:], lg) }},
		{isPlanRequest, func() exitcode.Code { return cmdPlan(os.Args[2:], lg, commandName) }},
		{isDashboardRequest, func() exitcode.Code { return cmdDashboard(os.Args[2:], lg) }},
		{isFocusConsoleRequest, func() exitcode.Code { return cmdFocusConsole(os.Args[2:], lg) }},
		{isWorktreeActionRequest, func() exitcode.Code { return cmdWorktreeAction(os.Args[1:], lg, commandName) }},
		{isMsgRequest, func() exitcode.Code { return cmdMsg(os.Args[2:], lg) }},
	}
	for _, d := range table {
		if d.match(args) {
			os.Exit(int(d.handle()))
		}
	}

	pr := cliflags.Parse(args, lg, os.Stdout)
	if pr.Code != exitcode.OK || pr.Config == nil {
		os.Exit(int(pr.Code))
	}
	cfg := pr.Config
	if cfg.Debug {
		lg = log.New(true)
	}

	// Which deps each mode needs stays here; the probe and printing are shared
	// (deps.go).
	lifecycle := cfg.CloseNum > 0 || cfg.MergeNum > 0 || cfg.CleanupMode
	needs := depNeeds{
		git: true,
		gh:  cfg.StatusMode || cfg.CleanupMode || !lifecycle,
		// Launch runtime availability is checked after backend selection. Status
		// and lifecycle keep their existing backend-specific paths for now.
		tmux: false,
	}
	if exitOnMissingDeps(missingDeps(needs), lg) {
		os.Exit(int(exitcode.Env))
	}

	if cfg.StatusMode {
		os.Exit(int(cmdStatus(cfg, lg)))
	}
	if cfg.CloseNum > 0 {
		os.Exit(int(cmdClose(cfg, lg)))
	}
	if cfg.MergeNum > 0 {
		os.Exit(int(cmdMerge(cfg, lg)))
	}
	if cfg.CleanupMode {
		os.Exit(int(cmdCleanup(cfg, lg)))
	}

	rt, code := resolveLaunchRuntime(cfg, nil, lg)
	if code != exitcode.OK {
		os.Exit(int(code))
	}
	os.Exit(int(run.Issues(cfg, lg, rt, commandName, bindDashboardKey)))
}

func isVersionRequest(args []string) bool {
	return len(args) == 1 && (args[0] == "--version" || args[0] == "-V")
}

func isCheckUpdateRequest(args []string) bool {
	return len(args) == 1 && (args[0] == "--check-update" || args[0] == "check-update")
}

func isUpdateRequest(args []string) bool {
	return len(args) > 0 && args[0] == "update"
}

func versionLine() string {
	return fmt.Sprintf("fanout %s (%s)", version, commit)
}

func invokedCommandName(args []string) string {
	if len(args) == 0 || args[0] == "" {
		return "fanout"
	}
	name := filepath.Base(args[0])
	if name == "." || name == string(os.PathSeparator) {
		return "fanout"
	}
	return name
}
