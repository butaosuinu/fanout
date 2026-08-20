package run

import (
	"fmt"
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/gitroot"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// sleepBetweenIssues is the rate-limit seam shared by both fan-out lanes.
// Tests swap it to observe the between-launch delay without real sleeping.
var sleepBetweenIssues = time.Sleep

// BindKeysFunc registers the runtime's dashboard shortcuts after a live run.
// cmd owns the implementation (it depends on the dashboard command's key
// constants); the run lanes call it back through this seam.
type BindKeysFunc func(lg *log.Logger, enabled bool)

// Runtime is the resolved backend/git/GitHub context both launch lanes use.
type Runtime struct {
	Info             *fanoutruntime.Info
	GH               ghissue.Runner
	Backend          backend.Backend
	Managed          panelaunch.ManagedSessionRuntime
	BackendSelection backend.Selection
	// VerifyBackend re-runs parent stickiness against the state held under the
	// launch lock. cmd closes over the raw CLI/env/config inputs so backend
	// selection and construction remain in the composition root.
	VerifyBackend func(parent string, store state.Store) error
	// PrepareBackend acquires live backend resources after planning confirms at
	// least one launch target and validates its effective agent.
	PrepareBackend func() error
}

// PrepareLaunchBackend acquires live resources for the selected backend.
func (r *Runtime) PrepareLaunchBackend() error {
	if r == nil || r.PrepareBackend == nil {
		return nil
	}
	return r.PrepareBackend()
}

// shouldBindRuntimeKeys gates the dashboard keybind side effect on the
// runtime actually offering global shortcuts, not on its name: only a live
// run that created panes on a shortcut-capable backend binds keys.
func shouldBindRuntimeKeys(dryRun bool, created int, runtimeBackend backend.Backend) bool {
	if dryRun || created == 0 {
		return false
	}
	if _, ok := backend.AsShortcutBinder(runtimeBackend); ok {
		return true
	}
	_, ok := backend.AsDashboardShortcutBinder(runtimeBackend)
	return ok
}

// ResolveRuntime resolves the tmux target and git project root, validates the
// agent selection, and records the invoking pane's project-root hint for the
// dashboard. It is shared by the issue lane (cmd main) and the plan lane
// (cmd plan).
func ResolveRuntime(cfg *cliflags.Config, selection backend.Selection, runtimeBackend backend.Backend, verify func(string, state.Store) error, lg *log.Logger) (*Runtime, exitcode.Code) {
	if runtimeBackend == nil {
		lg.Err("runtime backend is not configured")
		return nil, exitcode.Env
	}
	if runtimeBackend.Name() != selection.Name {
		lg.Err("runtime backend mismatch: selected %s, constructed %s", selection.Name, runtimeBackend.Name())
		return nil, exitcode.Env
	}
	if err := runtimeBackend.CheckAvailable(); err != nil {
		if selection.Name == backend.Tmux {
			// Preserve the established tmux prerequisite diagnostic. Backend
			// selection must happen first, but a selected tmux backend still
			// reports availability through the shared missing-dependency shape.
			lg.Err("missing dependencies:")
			fmt.Fprintf(lg.Stderr(), "  - %s\n", err)
		} else {
			lg.Err("runtime backend %s is not available: %v", selection.Name, err)
		}
		return nil, exitcode.Env
	}
	info, err := fanoutruntime.Resolve(selection.Name, cfg.Session)
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

	if selection.Name == backend.Tmux {
		lg.Info("tmux session: %s", info.Session)
		lg.Info("tmux target:  %s", info.Target)
	}
	lg.Info("project root: %s", info.ProjectRoot)

	if !gitroot.IsWorkTree(info.ProjectRoot) {
		lg.Err("project root %s is not a git work tree; cannot resolve GitHub repo", info.ProjectRoot)
		return nil, exitcode.Env
	}
	if !cfg.DryRun {
		markCurrentPaneProjectRoot(runtimeBackend, info, lg)
	}
	return &Runtime{
		Info:             info,
		GH:               ghissue.Runner{Cwd: info.ProjectRoot},
		Backend:          runtimeBackend,
		BackendSelection: selection,
		VerifyBackend:    verify,
	}, exitcode.OK
}

// markCurrentPaneProjectRoot records fanout's own pane as this project's state
// owner so the dashboard keybinding resolves the repository from that pane. It
// is a best-effort hint: backends without pane decoration, and runs whose
// environment names no invoking pane, leave it unset.
func markCurrentPaneProjectRoot(runtimeBackend backend.Backend, info *fanoutruntime.Info, lg *log.Logger) {
	decorator, ok := backend.AsPaneDecorator(runtimeBackend)
	if !ok || info.InvokingPane == "" {
		return
	}
	if err := decorator.SetPaneProjectRoot(info.InvokingPane, info.ProjectRoot); err != nil {
		lg.Debug("dashboard project root hint: %v", err)
	}
}

// settingsOverrides folds the CLI setting toggles into a settings.CLIOverrides
// literal; both lanes resolve settings through the same mapping. PlanMode is
// an internal launch-lane override and is not a persisted/CLI setting.
func settingsOverrides(cfg *cliflags.Config) settings.CLIOverrides {
	overrides := settings.CLIOverrides{
		AutoPullRequest:    cfg.AutoPullRequest,
		PRReviewGate:       cfg.PRReviewGate,
		BriefingCodeReview: cfg.BriefingCodeReview,
		AgentTeamsHint:     cfg.AgentTeamsHint,
		PRVisualization:    cfg.PRVisualization,
		DashboardKeybind:   cfg.DashboardKeybind,
	}
	if cfg.Backend != "" {
		overrides.RuntimeBackend = new(cfg.Backend)
	}
	return overrides
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
	locked, err := state.LockProjectForLaunch(projectRoot)
	if err != nil {
		lg.Err("%v", err)
		return state.Store{}, nil, exitcode.Env
	}
	return locked.Store, locked, exitcode.OK
}
