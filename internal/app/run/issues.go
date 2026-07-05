package run

import (
	"fmt"
	"time"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/fanset"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
)

type executionResult struct {
	Created     int
	Failed      int
	CreatedNums []int
}

// Issues runs the issue / Project fan-out lane against an already-resolved
// runtime. cmd owns parsing, dependency checks, and runtime resolution before
// calling this; bindKeys is the cmd-side dashboard keybinding hook.
func Issues(cfg *cliflags.Config, lg *log.Logger, rt *Runtime, commandName string, bindKeys BindKeysFunc) exitcode.Code {
	resolvedSettings := settings.Resolve(rt.Info.ProjectRoot, settingsOverrides(cfg), lg.Warn)
	hookConfig := hooks.LoadUserConfig(lg)

	loaded, code := loadChildren(cfg, rt.GH, lg)
	if code != exitcode.OK {
		return code
	}

	totalChildren := len(loaded.Children)
	if totalChildren == 0 {
		if cfg.ParentMode == cliflags.ModeProject {
			lg.Info("no items in Project (after status/repo filter). nothing to do.")
		} else {
			lg.Info("no sub-issues on #%d. nothing to do.", cfg.Parent)
		}
		return exitcode.OK
	}

	openCount := len(OpenIssues(loaded.Children))
	lg.Info("%s: %d total, %d OPEN", loaded.ChildNoun, totalChildren, openCount)
	if openCount == 0 {
		lg.Info("no OPEN %s. nothing to do.", loaded.ChildNoun)
		return exitcode.OK
	}

	store, recorder, code := LoadState(cfg.DryRun, rt.Info.ProjectRoot, lg)
	if code != exitcode.OK {
		return code
	}
	if recorder != nil {
		defer func() {
			if err := recorder.Unlock(); err != nil {
				lg.Warn("unlock fanout state: %v", err)
			}
		}()
	}

	sameParentFanned := store.FannedNumbersForParent(cfg.ParentRef)
	otherParentFanned := store.FannedNumbersForOtherParents(cfg.ParentRef)
	worktreeFallbackFanned := ExistingWorktreeFanned(cfg, rt.Info.ProjectRoot, loaded.Children, otherParentFanned)

	plan := BuildPlan(
		cfg,
		loaded.Children,
		fanset.Union(sameParentFanned, worktreeFallbackFanned),
		loaded.ParentBody,
		func(issue *ghissue.Issue) {
			if err := rt.GH.HydrateBodyLabels(issue); err != nil {
				lg.Warn("#%d: could not fetch body/labels for blocker check; treating as unblocked", issue.Number)
			}
		},
		func(num int) string {
			state, _ := rt.GH.IssueState(num)
			return state
		},
	)
	logPlanDetails(plan, lg)

	if plan.OpenAfterFilter == 0 {
		lg.Info("all OPEN sub-issues filtered out by --only/--skip. nothing to do.")
		return exitcode.OK
	}
	if plan.UnfannedCount == 0 {
		lg.Ok("all %d OPEN sub-issue(s) already have a fanout pane. nothing to do.", len(plan.AlreadyFanned))
		return exitcode.OK
	}
	if err := validateIssueAgents(cfg, plan.Targets, plan.LimitDeferred); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}

	logAlreadyFanned(plan.AlreadyFanned, lg)
	lg.Info("will create %d pane(s); deferred (blocked): %d; deferred (--limit): %d",
		len(plan.Targets), len(plan.BlockedRows), len(plan.LimitDeferred))

	c := lg.Colors()
	if cfg.DryRun {
		printDryRunPlan(plan, lg, c)
	}

	var teamCtx *briefing.TeamContext
	if cfg.Team {
		teamCtx = buildTeamContext(rt.Info.ProjectRoot, cfg.ParentRef, plan.Targets)
	}

	result := executePlan(cfg, lg, rt.Info, rt.GH, plan.Targets, resolvedSettings, hookConfig, recorder, otherParentFanned, c, commandName, teamCtx)
	printSummary(plan, result, cfg, lg, c, commandName)

	// Register tmux keybindings so the user can pop the read-only dashboard
	// (F12 or prefix + D) and same-worktree action menu (prefix + M) from any
	// fanout pane. The bindings resolve the repo from the pressing pane at
	// keypress, so they work from child worktree panes and across repos.
	// Best-effort, live runs only.
	if !cfg.DryRun && result.Created > 0 {
		bindKeys(lg, resolvedSettings.DashboardKeybind)
	}

	// Seed the peers registry for --team runs, fail-fast partial successes
	// included. Best-effort: this runs outside the executePlan loop and a
	// failure only warns, never changes the exit code.
	if cfg.Team && len(result.CreatedNums) > 0 {
		if cfg.DryRun {
			fmt.Fprintf(lg.Stdout(), "%s# would seed team registry: %d peer(s) -> %s%s\n", c.Dim, len(result.CreatedNums), teamCtx.DBPath, c.Reset)
		} else {
			seedTeamRegistry(lg, teamCtx.DBPath, recorder.Store, cfg.ParentRef, result.CreatedNums)
		}
	}

	if result.Failed > 0 {
		return exitcode.Env
	}
	return exitcode.OK
}

func executePlan(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, gh ghissue.Runner, targets []ghissue.Issue, resolvedSettings settings.Settings, hookConfig hooks.Config, recorder panelaunch.StateRecorder, sharedAcrossParents map[int]bool, c log.Palette, commandName string, teamCtx *briefing.TeamContext) executionResult {
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: lg, Info: info, Recorder: recorder, Palette: c, CommandName: commandName}
	created, failed := executeFailFast(
		targets,
		func(issue ghissue.Issue) int { return issue.Number },
		func(issue ghissue.Issue) bool {
			// Hydrate body lazily for issues that came from the Sub-issues API
			// path (body=""), unless --unblocked-only already did it upfront.
			if issue.Body == "" {
				if detail, err := gh.IssueDetail(issue.Number); err == nil {
					issue.Body = detail.Body
				}
			}
			return launcher.LaunchOK(panelaunch.NewIssueRequest(cfg, info.ProjectRoot, issue, resolvedSettings, hookConfig, sharedAcrossParents[issue.Number], teamCtx))
		},
		sleepBetweenFunc(cfg),
	)
	return executionResult{Created: len(created), Failed: failed, CreatedNums: created}
}

// executeFailFast launches targets in order and stops after the first failure,
// preserving the fail-fast contract both lanes rely on. sleepBetween runs only
// between successful launches; it decides internally whether to actually sleep.
func executeFailFast[T any, K comparable](targets []T, key func(T) K, launch func(T) bool, sleepBetween func()) (created []K, failed int) {
	for i, item := range targets {
		if !launch(item) {
			failed++
			break
		}
		created = append(created, key(item))
		if i < len(targets)-1 {
			sleepBetween()
		}
	}
	return created, failed
}

// sleepBetweenFunc yields the between-launch delay closure: a no-op unless
// --sleep is set, matching the original per-lane `if cfg.SleepBetween > 0`
// guard.
func sleepBetweenFunc(cfg *cliflags.Config) func() {
	return func() {
		if cfg.SleepBetween > 0 {
			sleepBetweenIssues(time.Duration(cfg.SleepBetween * float64(time.Second)))
		}
	}
}
