package run

import (
	"fmt"
	"time"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/fanset"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type executionResult struct {
	Created        int
	Failed         int
	CreatedNums    []int
	CreatedPaneIDs []string
	Notices        []string
}

// IssueExecutionResult reports the launch plan and exact issues and tmux panes
// created by an issue or Project fan-out, in creation order. Dry runs return
// an empty pane-id slice.
type IssueExecutionResult struct {
	CreatedIssueNums []int
	CreatedPaneIDs   []string
	Notices          []string
	Plan             Plan
}

// IssuePlanInput is a caller-prepared issue enumeration and GitHub lookup
// boundary. The watcher uses it to reuse one candidate snapshot between
// capacity planning and the launch performed later in the same cycle.
type IssuePlanInput struct {
	Loaded            ChildLoadResult
	HydrateBodyLabels func(*ghissue.Issue) error
	IssueState        func(int) (string, error)
}

// IssueReadyFunc runs after the child plan and its effective agents validate,
// while the live state recorder remains locked, but before any child pane is
// created. Callers can reserve or attach a parent coordinator in the same
// state transaction without duplicating issue planning.
type IssueReadyFunc func(state.Store, panelaunch.StateRecorder) error

// IssueAfterContext reports the child-launch outcome while the same state
// recorder used by the fan-out remains locked.
type IssueAfterContext struct {
	Created int
	Failed  int
}

// IssueAfterFunc runs after the fail-fast child loop while the launch state is
// still locked. The TUI uses it to start a Herdr parent coordinator only after
// every successfully created child row is committed.
type IssueAfterFunc func(state.Store, panelaunch.StateRecorder, IssueAfterContext) error

// Issues runs the issue / Project fan-out lane against an already-resolved
// runtime. cmd owns parsing, dependency checks, and runtime resolution before
// calling this; bindKeys is the cmd-side dashboard keybinding hook.
func Issues(cfg *cliflags.Config, lg *log.Logger, rt *Runtime, commandName string, bindKeys BindKeysFunc) exitcode.Code {
	_, code := IssuesWithResult(cfg, lg, rt, commandName, bindKeys)
	return code
}

// IssuesWithResult runs the issue / Project fan-out lane and returns the exact
// pane ids created before completion or a fail-fast launch error.
func IssuesWithResult(cfg *cliflags.Config, lg *log.Logger, rt *Runtime, commandName string, bindKeys BindKeysFunc) (IssueExecutionResult, exitcode.Code) {
	return IssuesWithResultWhenReady(cfg, lg, rt, commandName, bindKeys, nil, nil)
}

// IssuesWithResultWhenReady is IssuesWithResult with optional callbacks before
// and after the child loop. They run while the same state recorder is locked.
func IssuesWithResultWhenReady(cfg *cliflags.Config, lg *log.Logger, rt *Runtime, commandName string, bindKeys BindKeysFunc, ready IssueReadyFunc, after IssueAfterFunc) (IssueExecutionResult, exitcode.Code) {
	return issuesWithResultWhenReady(cfg, lg, rt, commandName, bindKeys, nil, ready, after)
}

// IssuesWithPlanInputResultWhenReady is IssuesWithResultWhenReady with a
// caller-prepared issue enumeration and lookup cache.
func IssuesWithPlanInputResultWhenReady(cfg *cliflags.Config, lg *log.Logger, rt *Runtime, commandName string, bindKeys BindKeysFunc, input IssuePlanInput, ready IssueReadyFunc, after IssueAfterFunc) (IssueExecutionResult, exitcode.Code) {
	return issuesWithResultWhenReady(cfg, lg, rt, commandName, bindKeys, &input, ready, after)
}

//nolint:gocognit,gocyclo,funlen // The locked issue fan-out transaction must order planning, child side effects, and its two boundary callbacks together.
func issuesWithResultWhenReady(cfg *cliflags.Config, lg *log.Logger, rt *Runtime, commandName string, bindKeys BindKeysFunc, input *IssuePlanInput, ready IssueReadyFunc, after IssueAfterFunc) (IssueExecutionResult, exitcode.Code) {
	resolvedSettings := settings.Resolve(rt.Info.ProjectRoot, settingsOverrides(cfg), lg.Warn)
	launchCfg := effectiveIssueLaunchConfig(cfg, resolvedSettings)
	hookConfig := hooks.LoadUserConfig(lg)

	var loaded ChildLoadResult
	if input == nil {
		var code exitcode.Code
		loaded, code = loadChildren(cfg, rt.GH, lg)
		if code != exitcode.OK {
			return IssueExecutionResult{}, code
		}
	} else {
		loaded = input.Loaded
	}

	totalChildren := len(loaded.Children)
	if totalChildren == 0 {
		if cfg.ParentMode == cliflags.ModeProject {
			lg.Info("no items in Project (after status/repo filter). nothing to do.")
		} else {
			lg.Info("no sub-issues on #%d. nothing to do.", cfg.Parent)
		}
		return IssueExecutionResult{}, exitcode.OK
	}

	openCount := len(OpenIssues(loaded.Children))
	lg.Info("%s: %d total, %d OPEN", loaded.ChildNoun, totalChildren, openCount)
	if openCount == 0 {
		lg.Info("no OPEN %s. nothing to do.", loaded.ChildNoun)
		return IssueExecutionResult{}, exitcode.OK
	}

	store, recorder, code := LoadState(cfg.DryRun, rt.Info.ProjectRoot, lg)
	if code != exitcode.OK {
		return IssueExecutionResult{}, code
	}
	if recorder != nil {
		defer func() {
			if err := recorder.Unlock(); err != nil {
				lg.Warn("unlock fanout state: %v", err)
			}
		}()
	}
	if rt.VerifyBackend != nil {
		if err := rt.VerifyBackend(cfg.ParentRef, store); err != nil {
			lg.Err("runtime backend: %v", err)
			return IssueExecutionResult{}, exitcode.Env
		}
	}

	sameParentFanned := store.FannedNumbersForParent(cfg.ParentRef)
	otherParentFanned := store.FannedNumbersForOtherParents(cfg.ParentRef)
	worktreeFallbackFanned := ExistingWorktreeFanned(cfg, rt.Info.ProjectRoot, loaded.Children, otherParentFanned)
	// Issues owned by a plan session (an issue-sourced coordinator or its plan
	// task rows) count as fanned: their rows carry no positive IssueNum, so the
	// parent-keyed sets above cannot see them, and launching a plain child pane
	// would decompose the same work twice.
	planOwnedFanned := panelaunch.PlanLinkedIssueNums(rt.Info.ProjectRoot, store)
	// The parent itself being plan-owned aborts the run outright (checked here,
	// under the state lock, so a racing coordinator launch cannot slip past a
	// caller's unlocked pre-check): its children were not part of the plan
	// decomposition, and fanning them out alongside the plan splits the issue
	// across two uncoordinated sessions.
	if cfg.ParentMode == cliflags.ModeIssue && planOwnedFanned[cfg.Parent] {
		lg.Err("issue #%d already has a plan session; close it before fanning out children", cfg.Parent)
		return IssueExecutionResult{}, exitcode.Env
	}

	hydrateBodyLabels := rt.GH.HydrateBodyLabels
	issueState := rt.GH.IssueState
	if input != nil {
		if input.HydrateBodyLabels != nil {
			hydrateBodyLabels = input.HydrateBodyLabels
		}
		if input.IssueState != nil {
			issueState = input.IssueState
		}
	}
	plan := BuildPlan(
		cfg,
		loaded.Children,
		fanset.Union(sameParentFanned, worktreeFallbackFanned, planOwnedFanned),
		loaded.ParentBody,
		func(issue *ghissue.Issue) {
			if err := hydrateBodyLabels(issue); err != nil {
				lg.Warn("#%d: could not fetch body/labels for blocker check; treating as unblocked", issue.Number)
			}
		},
		func(num int) string {
			state, _ := issueState(num)
			return state
		},
	)
	logPlanDetails(plan, lg)

	if plan.OpenAfterFilter == 0 {
		lg.Info("all OPEN sub-issues filtered out by --only/--skip. nothing to do.")
		return IssueExecutionResult{Plan: plan}, exitcode.OK
	}
	if plan.UnfannedCount == 0 {
		if err := prepareIssueCallbacks(rt, ready, after); err != nil {
			lg.Err("runtime backend: %v", err)
			return IssueExecutionResult{Plan: plan}, exitcode.Env
		}
		if !callIssueReady(ready, store, recorder, lg) {
			return IssueExecutionResult{Plan: plan}, exitcode.Env
		}
		if !callIssueAfter(after, store, recorder, IssueAfterContext{}, lg) {
			return IssueExecutionResult{Plan: plan}, exitcode.Env
		}
		lg.Ok("all %d OPEN sub-issue(s) already have a fanout pane. nothing to do.", len(plan.AlreadyFanned))
		return IssueExecutionResult{Plan: plan}, exitcode.OK
	}
	if !prepareIssueLaunch(launchCfg, plan, rt, store, recorder, ready, lg) {
		return IssueExecutionResult{Plan: plan}, exitcode.Env
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

	hydrateLaunchBody := func(issue *ghissue.Issue) {
		if issue.Body != "" {
			return
		}
		if input != nil {
			// BuildPlan already reported a failed prepared lookup. Replaying
			// it here only copies the cached launch body, so avoid a duplicate warning.
			_ = hydrateBodyLabels(issue)
			return
		}
		if detail, err := rt.GH.IssueDetail(issue.Number); err == nil {
			issue.Body = detail.Body
		}
	}
	result := executePlan(launchCfg, lg, rt.Info, rt.Backend, rt.Managed, plan.Targets, hydrateLaunchBody, resolvedSettings, hookConfig, recorder, otherParentFanned, c, commandName, teamCtx)
	printSummary(plan, result, cfg, lg, c, commandName)
	progress := IssueAfterContext{
		Created: result.Created,
		Failed:  result.Failed,
	}
	if len(plan.Targets) > 0 && !callIssueAfter(after, store, recorder, progress, lg) {
		return IssueExecutionResult{CreatedIssueNums: result.CreatedNums, CreatedPaneIDs: result.CreatedPaneIDs, Notices: result.Notices, Plan: plan}, exitcode.Env
	}

	// Register tmux keybindings so the user can pop the read-only dashboard
	// (F12 or prefix + D) and same-worktree action menu (prefix + M) from any
	// fanout pane. The bindings resolve the repo from the pressing pane at
	// keypress, so they work from child worktree panes and across repos.
	// Best-effort, live runs only.
	if shouldBindRuntimeKeys(cfg.DryRun, result.Created, rt.BackendSelection.Name) {
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
		return IssueExecutionResult{CreatedIssueNums: result.CreatedNums, CreatedPaneIDs: result.CreatedPaneIDs, Notices: result.Notices, Plan: plan}, exitcode.Env
	}
	return IssueExecutionResult{CreatedIssueNums: result.CreatedNums, CreatedPaneIDs: result.CreatedPaneIDs, Notices: result.Notices, Plan: plan}, exitcode.OK
}

func prepareIssueCallbacks(rt *Runtime, ready IssueReadyFunc, after IssueAfterFunc) error {
	if ready == nil && after == nil {
		return nil
	}
	return rt.PrepareLaunchBackend()
}

func prepareIssueLaunch(cfg *cliflags.Config, plan Plan, rt *Runtime, store state.Store, recorder *state.LockedStore, ready IssueReadyFunc, lg *log.Logger) bool {
	if err := validateIssueAgents(cfg, plan.Targets, plan.LimitDeferred); err != nil {
		lg.Err("%s", err.Error())
		return false
	}
	if len(plan.Targets) == 0 {
		return true
	}
	if err := rt.PrepareLaunchBackend(); err != nil {
		lg.Err("runtime backend: %v", err)
		return false
	}
	return callIssueReady(ready, store, recorder, lg)
}

func callIssueReady(ready IssueReadyFunc, store state.Store, recorder *state.LockedStore, lg *log.Logger) bool {
	if ready == nil {
		return true
	}
	if recorder != nil {
		store = recorder.Store
	}
	if err := ready(store, recorder); err != nil {
		lg.Err("%s", err.Error())
		return false
	}
	return true
}

func callIssueAfter(after IssueAfterFunc, store state.Store, recorder *state.LockedStore, progress IssueAfterContext, lg *log.Logger) bool {
	if after == nil {
		return true
	}
	if recorder != nil {
		store = recorder.Store
	}
	if err := after(store, recorder, progress); err != nil {
		lg.Err("%s", err.Error())
		return false
	}
	return true
}

// effectiveIssueLaunchConfig overlays resolved launch settings onto a shallow
// copy of the parsed issue config. Keep the parsed config untouched so output
// such as --limit rerun hints only repeats flags the caller explicitly passed.
// Plan tasks, manual/attached panes, and watcher standalone panes use their own
// launch paths and intentionally do not pass through this helper.
func effectiveIssueLaunchConfig(cfg *cliflags.Config, resolvedSettings settings.Settings) *cliflags.Config {
	launchCfg := *cfg
	launchCfg.PlanMode = new(resolvedSettings.ChildPlanMode)
	return &launchCfg
}

func executePlan(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, runtimeBackend backend.Backend, managed panelaunch.ManagedLaunchRuntime, targets []ghissue.Issue, hydrateBody func(*ghissue.Issue), resolvedSettings settings.Settings, hookConfig hooks.Config, recorder panelaunch.StateRecorder, sharedAcrossParents map[int]bool, c log.Palette, commandName string, teamCtx *briefing.TeamContext) executionResult {
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: lg, Info: info, Backend: runtimeBackend, Managed: managed, Recorder: recorder, Palette: c, CommandName: commandName}
	var createdPaneIDs []string
	var notices []string
	created, failed := executeFailFast(
		targets,
		func(issue ghissue.Issue) int { return issue.Number },
		func(issue ghissue.Issue) bool {
			// Hydrate body lazily for issues that came from the Sub-issues API
			// path (body=""), unless --unblocked-only already did it upfront.
			if issue.Body == "" && hydrateBody != nil {
				hydrateBody(&issue)
			}
			result, ok := launcher.LaunchWithResult(panelaunch.NewIssueRequest(cfg, info.ProjectRoot, issue, resolvedSettings, hookConfig, sharedAcrossParents[issue.Number], teamCtx))
			if ok && result.PaneID != "" {
				createdPaneIDs = append(createdPaneIDs, result.PaneID)
			}
			if ok && result.Notice != "" {
				notices = append(notices, result.Notice)
			}
			return ok
		},
		sleepBetweenFunc(cfg),
	)
	return executionResult{Created: len(created), Failed: failed, CreatedNums: created, CreatedPaneIDs: createdPaneIDs, Notices: notices}
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
