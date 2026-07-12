package run

import (
	"os"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/gitroot"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// sleepBetweenIssues is the rate-limit seam shared by both fan-out lanes.
// Tests swap it to observe the between-launch delay without real sleeping.
var sleepBetweenIssues = time.Sleep

// BindKeysFunc registers the dashboard / worktree-action tmux keybindings after
// a live run. cmd owns the implementation (it depends on the dashboard command's
// key constants); the run lanes call it back through this seam.
type BindKeysFunc func(lg *log.Logger, enabled bool)

// Runtime is the resolved tmux/git/gh context both lanes execute against.
type Runtime struct {
	Info *fanoutruntime.Info
	GH   ghissue.Runner
}

// ResolveRuntime resolves the tmux target and git project root, validates the
// agent selection, and records the invoking pane's project-root hint for the
// dashboard. It is shared by the issue lane (cmd main) and the plan lane
// (cmd plan).
func ResolveRuntime(cfg *cliflags.Config, lg *log.Logger) (*Runtime, exitcode.Code) {
	info, err := fanoutruntime.Resolve(cfg.Session)
	if err != nil {
		lg.Err("%s", err.Error())
		return nil, exitcode.Env
	}

	if cfg.Agent == "" {
		cfg.Agent = os.Getenv("FANOUT_AGENT")
	}
	if cfg.Agent == "" && len(cfg.AgentOverrides) == 0 {
		lg.Err("agent is required; pass --agent <name> or set FANOUT_AGENT")
		return nil, exitcode.Env
	}

	lg.Info("tmux session: %s", info.Session)
	lg.Info("tmux target:  %s", info.Target)
	lg.Info("project root: %s", info.ProjectRoot)

	if !gitroot.IsWorkTree(info.ProjectRoot) {
		lg.Err("project root %s is not a git work tree; cannot resolve GitHub repo", info.ProjectRoot)
		return nil, exitcode.Env
	}
	if !cfg.DryRun {
		markCurrentPaneProjectRoot(info.ProjectRoot, lg)
	}
	return &Runtime{
		Info: info,
		GH:   ghissue.Runner{Cwd: info.ProjectRoot},
	}, exitcode.OK
}

func markCurrentPaneProjectRoot(projectRoot string, lg *log.Logger) {
	paneID := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if paneID == "" {
		return
	}
	if err := tmuxrun.SetPaneProjectRoot(paneID, projectRoot); err != nil {
		lg.Debug("dashboard project root hint: %v", err)
	}
}

// settingsOverrides folds the CLI setting toggles into a settings.CLIOverrides
// literal; both lanes resolve settings through the same mapping.
func settingsOverrides(cfg *cliflags.Config) settings.CLIOverrides {
	return settings.CLIOverrides{
		AutoPullRequest:    cfg.AutoPullRequest,
		PRReviewGate:       cfg.PRReviewGate,
		BriefingCodeReview: cfg.BriefingCodeReview,
		AgentTeamsHint:     cfg.AgentTeamsHint,
		CodexPlanMode:      cfg.CodexPlanMode,
		PRVisualization:    cfg.PRVisualization,
		DashboardKeybind:   cfg.DashboardKeybind,
	}
}

// LoadState opens the fanout state store for a run: read-only for dry-runs,
// and lock-backed (after preparing the local git exclude) for live runs. Both
// lanes share it; loadRunState / loadPlanState were byte-identical apart from
// their config type, so only the dry-run flag is threaded here.
func LoadState(dryRun bool, projectRoot string, lg *log.Logger) (state.Store, *state.LockedStore, exitcode.Code) {
	if dryRun {
		store, err := state.LoadProject(projectRoot)
		if err != nil {
			lg.Err("%v", err)
			return state.Store{}, nil, exitcode.Env
		}
		return store, nil, exitcode.OK
	}
	if err := worktree.EnsureLocalExclude(projectRoot); err != nil {
		lg.Err("prepare local git exclude: %v", err)
		return state.Store{}, nil, exitcode.Env
	}
	locked, err := state.LockProject(projectRoot)
	if err != nil {
		lg.Err("%v", err)
		return state.Store{}, nil, exitcode.Env
	}
	return locked.Store, locked, exitcode.OK
}
