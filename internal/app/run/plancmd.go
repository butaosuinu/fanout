package run

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/fanset"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const planSubcommand = "plan"

// PlanCommandConfig is the parsed `fanout plan` configuration. cmd owns
// parsing and CLI validation; the run lane consumes it to execute the plan.
type PlanCommandConfig struct {
	SpecArg            string
	SpecPath           string
	SpecSnapshot       *planspec.Snapshot
	Agent              string
	Backend            backend.Name
	AgentOverrides     []cliflags.AgentOverride
	BaseBranch         string
	BranchPrefix       string
	Limit              int
	OnlyArg            string
	SkipArg            string
	Only               []string
	Skip               []string
	Session            string
	SleepBetween       float64
	NoRefresh          bool
	DryRun             bool
	Debug              bool
	UnblockedOnly      bool
	Team               bool
	StatusMode         bool
	Format             string
	CloseTaskID        string
	MergeTaskID        string
	CleanupMode        bool
	FormatExplicit     bool
	AutoPullRequest    *bool
	PRReviewGate       *bool
	BriefingCodeReview *bool
	AgentTeamsHint     *bool
	PRVisualization    *bool
	DashboardKeybind   *bool
}

type taskPlan struct {
	TotalTasks      int
	AfterFilter     int
	UnfannedCount   int
	Targets         []planspec.Task
	AlreadyFanned   []string
	AlreadyComplete []string
	FilteredOnly    []planspec.Task
	FilteredSkip    []planspec.Task
	MissingOnly     []string
	BlockedRows     []taskBlockedRow
	LimitDeferred   []planspec.Task
	SourceArgument  string
}

type taskBlockedRow struct {
	Task planspec.Task
	Refs string
}

type TaskExecutionResult struct {
	Created    int
	Failed     int
	CreatedIDs []string
}

// PlanTasks runs the live/dry-run plan lane against an already resolved
// runtime. cmd owns parsing, dependency checks, and runtime resolution before
// calling this; bindKeys is the cmd-side dashboard keybinding hook.
func PlanTasks(cfg PlanCommandConfig, rt *Runtime, lg *log.Logger, commandName string, bindKeys BindKeysFunc) (TaskExecutionResult, exitcode.Code) {
	resolvedSettings := settings.Resolve(rt.Info.ProjectRoot, settingsOverrides(cfg.CLIConfig()), lg.Warn)
	hookConfig := hooks.LoadUserConfig(lg)

	snapshot, err := resolvePlanSpecSnapshot(cfg, rt.Info.ProjectRoot)
	if err != nil {
		lg.Err("%v", err)
		return TaskExecutionResult{}, exitcode.Env
	}
	cfg.SpecPath = snapshot.Path()
	cfg.SpecSnapshot = &snapshot
	spec := snapshot.Spec()
	var specCopy planSpecCopyTarget
	if !cfg.DryRun {
		specCopy, err = preparePlanSpecCopy(snapshot, rt.Info.ProjectRoot, spec.Plan.Slug)
		if err != nil {
			lg.Err("prepare plan spec copy: %v", err)
			return TaskExecutionResult{}, exitcode.Env
		}
	}
	cfg.BaseBranch, err = resolvePlanBaseBranch(cfg, spec, rt.Info.ProjectRoot)
	if err != nil {
		lg.Err("%v", err)
		return TaskExecutionResult{}, exitcode.Env
	}
	if err := ValidatePlanExecutionNames(spec, cfg); err != nil {
		lg.Err("validate plan execution names %s: %v", cfg.SpecPath, err)
		return TaskExecutionResult{}, exitcode.Env
	}
	cliCfg := cfg.CLIConfig()

	store, recorder, code := LoadState(cfg.DryRun, rt.Info.ProjectRoot, lg)
	if code != exitcode.OK {
		return TaskExecutionResult{}, code
	}
	if recorder != nil {
		defer func() {
			if err := recorder.Unlock(); err != nil {
				lg.Warn("unlock fanout state: %v", err)
			}
		}()
	}
	parentRef := panelaunch.PlanParentRef(spec.Plan.Slug)
	if rt.VerifyBackend != nil {
		backendParent := panelaunch.PlanRuntimeParentRef(spec.Plan.Slug, spec.Plan.Source)
		if err := rt.VerifyBackend(backendParent, store); err != nil {
			lg.Err("runtime backend: %v", err)
			return TaskExecutionResult{}, exitcode.Env
		}
	}
	copyLivePlanSpec := func() exitcode.Code {
		if cfg.DryRun {
			return exitcode.OK
		}
		if err := copyPlanSpec(snapshot.Bytes(), specCopy); err != nil {
			lg.Err("copy plan spec: %v", err)
			return exitcode.Env
		}
		return exitcode.OK
	}
	cfg.SpecArg = planRerunSpecArg(cfg, spec)

	fanned := fanset.Union(store.FannedTaskIDsForParent(parentRef), existingPlanWorktreeFanned(rt.Info.ProjectRoot, spec))
	plan := buildTaskPlan(cfg, spec, fanned, func(task planspec.Task) bool {
		return planTaskComplete(rt.GH, cliCfg, rt.Info.ProjectRoot, store, spec, task, lg)
	})
	logTaskPlanDetails(plan, lg)

	if plan.AfterFilter == 0 {
		if code := copyLivePlanSpec(); code != exitcode.OK {
			return TaskExecutionResult{}, code
		}
		lg.Info("all plan tasks filtered out by --only/--skip. nothing to do.")
		return TaskExecutionResult{}, exitcode.OK
	}
	if plan.UnfannedCount == 0 {
		if code := copyLivePlanSpec(); code != exitcode.OK {
			return TaskExecutionResult{}, code
		}
		if len(plan.AlreadyComplete) == 0 {
			lg.Ok("all %d plan task(s) already have a fanout pane. nothing to do.", len(plan.AlreadyFanned))
		} else {
			lg.Ok("all %d selected plan task(s) already have a fanout pane or are complete. nothing to do.", plan.AfterFilter)
		}
		return TaskExecutionResult{}, exitcode.OK
	}
	if err := validateTaskAgents(cliCfg, plan.Targets, plan.LimitDeferred); err != nil {
		lg.Err("%s", err.Error())
		return TaskExecutionResult{}, exitcode.Env
	}
	if code := copyLivePlanSpec(); code != exitcode.OK {
		return TaskExecutionResult{}, code
	}

	logAlreadyFannedTasks(plan.AlreadyFanned, lg)
	logAlreadyCompleteTasks(plan.AlreadyComplete, lg)
	lg.Info("plan %s: %d task(s)", spec.Plan.Slug, plan.TotalTasks)
	lg.Info("will create %d pane(s); deferred (blocked): %d; deferred (--limit): %d",
		len(plan.Targets), len(plan.BlockedRows), len(plan.LimitDeferred))

	c := lg.Colors()
	if cfg.DryRun {
		printTaskDryRunPlan(plan, lg, c)
	}

	var teamCtx *briefing.TeamContext
	if cfg.Team {
		teamCtx = buildTaskTeamContext(rt.Info.ProjectRoot, parentRef, plan.Targets)
	}
	codexTeamPreseeded := cfg.Team && !cfg.DryRun && !resolvedSettings.ChildPlanMode && planHasCodexTeamBridge(cliCfg, plan.Targets)
	if codexTeamPreseeded {
		if err := preseedTaskTeamRegistry(teamCtx.DBPath, parentRef, plan.Targets, cliCfg); err != nil {
			lg.Err("team: %v", err)
			return TaskExecutionResult{}, exitcode.Env
		}
	}

	result := executeTaskPlan(cliCfg, lg, rt, spec, plan.Targets, resolvedSettings, hookConfig, recorder, c, commandName, teamCtx)
	printTaskSummary(plan, result, cfg, lg, c, commandName)

	if !cfg.DryRun && result.Created > 0 {
		bindKeys(lg, resolvedSettings.DashboardKeybind)
	}

	// Seed the peers registry for --team runs, fail-fast partial successes
	// included. Best-effort: this runs outside the executeTaskPlan loop and a
	// failure only warns, never changes the exit code.
	if cfg.Team && len(result.CreatedIDs) > 0 {
		if cfg.DryRun {
			fmt.Fprintf(lg.Stdout(), "%s# would seed team registry: %d peer(s) -> %s%s\n", c.Dim, len(result.CreatedIDs), teamCtx.DBPath, c.Reset)
		} else {
			seedTaskTeamRegistry(lg, teamCtx.DBPath, recorder.Store, parentRef, result.CreatedIDs)
		}
	}
	if codexTeamPreseeded {
		if err := cleanupUncreatedTaskPeers(teamCtx.DBPath, parentRef, plan.Targets, result.CreatedIDs); err != nil {
			lg.Warn("team: cleanup provisional peers: %v", err)
		}
	}

	if result.Failed > 0 {
		return result, exitcode.Env
	}
	return result, exitcode.OK
}

// CLIConfig projects the plan config onto the shared cliflags.Config the
// launch lane and settings/agent helpers consume.
func (cfg PlanCommandConfig) CLIConfig() *cliflags.Config {
	return &cliflags.Config{
		ParentRef:          planSubcommand,
		PlanSpecIdentity:   planSpecIdentity(cfg),
		Agent:              cfg.Agent,
		Backend:            cfg.Backend,
		AgentOverrides:     cfg.AgentOverrides,
		BaseBranch:         cfg.BaseBranch,
		BranchPrefix:       cfg.BranchPrefix,
		Session:            cfg.Session,
		SleepBetween:       cfg.SleepBetween,
		NoRefresh:          cfg.NoRefresh,
		DryRun:             cfg.DryRun,
		Debug:              cfg.Debug,
		UnblockedOnly:      cfg.UnblockedOnly,
		Team:               cfg.Team,
		AutoPullRequest:    cfg.AutoPullRequest,
		PRReviewGate:       cfg.PRReviewGate,
		BriefingCodeReview: cfg.BriefingCodeReview,
		AgentTeamsHint:     cfg.AgentTeamsHint,
		PRVisualization:    cfg.PRVisualization,
		DashboardKeybind:   cfg.DashboardKeybind,
	}
}

func resolvePlanSpecSnapshot(cfg PlanCommandConfig, projectRoot string) (planspec.Snapshot, error) {
	specPath := ResolvePlanSpecPath(projectRoot, cfg.SpecArg)
	if cfg.SpecSnapshot == nil {
		return planspec.LoadWithoutResolvedNameChecksSnapshot(specPath)
	}
	if cfg.SpecSnapshot.Path() != specPath {
		return planspec.Snapshot{}, fmt.Errorf(
			"prepared plan spec path %s does not match resolved path %s",
			cfg.SpecSnapshot.Path(),
			specPath,
		)
	}
	return *cfg.SpecSnapshot, nil
}

func planSpecIdentity(cfg PlanCommandConfig) string {
	if cfg.SpecSnapshot == nil {
		return ""
	}
	return cfg.SpecSnapshot.Identity()
}

// ActionMode reports whether the plan invocation is a status / lifecycle
// action rather than a launch.
func (cfg PlanCommandConfig) ActionMode() bool {
	return cfg.StatusMode || cfg.CloseTaskID != "" || cfg.MergeTaskID != "" || cfg.CleanupMode
}

// ResolvePlanSpecPath maps a spec argument (path or plan slug) to a spec file
// path under the project's .fanout/plans directory.
func ResolvePlanSpecPath(projectRoot, arg string) string {
	if filepath.IsAbs(arg) || strings.ContainsRune(arg, os.PathSeparator) || strings.HasSuffix(arg, ".json") {
		return arg
	}
	return filepath.Join(projectRoot, ".fanout", "plans", arg+".json")
}

func resolvePlanBaseBranch(cfg PlanCommandConfig, spec planspec.Spec, projectRoot string) (string, error) {
	if cfg.BaseBranch != "" {
		return cfg.BaseBranch, nil
	}
	if spec.Plan.BaseBranch != "" {
		if err := ValidatePlanBaseBranch("plan.base_branch", spec.Plan.BaseBranch); err != nil {
			return "", err
		}
		return spec.Plan.BaseBranch, nil
	}
	return worktree.ResolveDefaultBranchAllowMissingOrigin(projectRoot), nil
}

// ValidatePlanBaseBranch rejects a base branch containing whitespace. Shared by
// the plan CLI parser and the run lane's base-branch resolution.
func ValidatePlanBaseBranch(label, value string) error {
	if value != "" && strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("%s must not contain whitespace, got: %s", label, value)
	}
	return nil
}

func planRerunSpecArg(cfg PlanCommandConfig, spec planspec.Spec) string {
	if cfg.DryRun {
		return cfg.SpecArg
	}
	return spec.Plan.Slug
}

type planSpecCopyTarget struct {
	path  string
	found bool
	data  []byte
	mode  os.FileMode
}

func preparePlanSpecCopy(
	snapshot planspec.Snapshot,
	projectRoot, slug string,
) (planSpecCopyTarget, error) {
	dst := filepath.Join(projectRoot, ".fanout", "plans", slug+".json")
	target := planSpecCopyTarget{path: dst}
	current, err := os.ReadFile(dst)
	if errors.Is(err, os.ErrNotExist) {
		return target, nil
	}
	if err != nil {
		return planSpecCopyTarget{}, err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return planSpecCopyTarget{}, err
	}
	target.found = true
	target.data = current
	target.mode = info.Mode().Perm()
	source, err := samePlanSpecFile(snapshot.Path(), dst)
	if err != nil {
		return planSpecCopyTarget{}, err
	}
	if source && !bytes.Equal(current, snapshot.Bytes()) {
		return planSpecCopyTarget{}, fmt.Errorf(
			"plan spec source %s changed after snapshot; refusing to publish stale bytes",
			dst,
		)
	}
	return target, nil
}

func copyPlanSpec(data []byte, target planSpecCopyTarget) error {
	if err := os.MkdirAll(filepath.Dir(target.path), 0o755); err != nil {
		return err
	}
	current, err := os.ReadFile(target.path)
	if !target.found {
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return err
		default:
			return fmt.Errorf(
				"plan spec destination %s appeared after preflight; refusing to overwrite it",
				target.path,
			)
		}
		if err := atomicfs.WriteFileExclusive(target.path, data, 0o644); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf(
					"plan spec destination %s appeared after preflight; refusing to overwrite it",
					target.path,
				)
			}
			return err
		}
		return nil
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"plan spec destination %s disappeared after preflight; refusing to overwrite it",
				target.path,
			)
		}
		return err
	}
	info, err := os.Stat(target.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, target.data) || info.Mode().Perm() != target.mode {
		return fmt.Errorf(
			"plan spec destination %s changed after preflight; refusing to overwrite it",
			target.path,
		)
	}
	if bytes.Equal(current, data) {
		return nil
	}
	if err := atomicfs.CompareAndSwapFile(
		target.path,
		target.data,
		data,
		target.mode,
	); err != nil {
		return fmt.Errorf("publish plan spec destination %s: %w", target.path, err)
	}
	return nil
}

func samePlanSpecFile(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	if filepath.Clean(absA) == filepath.Clean(absB) {
		return true, nil
	}
	infoA, err := os.Stat(absA)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	infoB, err := os.Stat(absB)
	if err != nil {
		return false, err
	}
	return os.SameFile(infoA, infoB), nil
}

// ValidatePlanExecutionNames rejects duplicate final slugs / branches across
// the spec's tasks. Shared by the plan CLI status path and the run lane.
func ValidatePlanExecutionNames(spec planspec.Spec, cfg PlanCommandConfig) error {
	seenSlugs := map[string]int{}
	seenBranches := map[string]int{}
	for i, task := range spec.Tasks {
		slug := panelaunch.PlanTaskSlug(spec.Plan.Slug, task)
		if prev, ok := seenSlugs[slug]; ok {
			return fmt.Errorf("tasks[%d] final slug %q duplicates tasks[%d]", i, slug, prev)
		}
		seenSlugs[slug] = i

		branch := task.Branch
		if branch == "" {
			branch = naming.BranchName("", cfg.BranchPrefix, slug)
		}
		if prev, ok := seenBranches[branch]; ok {
			return fmt.Errorf("tasks[%d] final branch %q duplicates tasks[%d]", i, branch, prev)
		}
		seenBranches[branch] = i
	}
	return nil
}

func existingPlanWorktreeFanned(projectRoot string, spec planspec.Spec) map[string]bool {
	out := map[string]bool{}
	worktreeNames := existingWorktreeNames(filepath.Join(projectRoot, ".fanout", "worktrees"))
	for _, task := range spec.Tasks {
		if worktreeNameMatchesExact(worktreeNames, panelaunch.PlanTaskSlug(spec.Plan.Slug, task)) {
			out[task.ID] = true
		}
	}
	return out
}

func taskID(task planspec.Task) string { return task.ID }

func buildTaskPlan(cfg PlanCommandConfig, spec planspec.Spec, fanned map[string]bool, taskComplete func(planspec.Task) bool) taskPlan {
	plan := taskPlan{
		TotalTasks:     len(spec.Tasks),
		SourceArgument: cfg.SpecArg,
	}
	filtered, onlyRows, skipRows, missingOnly := fanset.FilterOnlySkip(spec.Tasks, taskID, cfg.Only, cfg.Skip)
	plan.FilteredOnly = onlyRows
	plan.FilteredSkip = skipRows
	plan.MissingOnly = missingOnly
	plan.AfterFilter = len(filtered)
	if plan.AfterFilter == 0 {
		return plan
	}

	targets, skipped := fanset.SplitFanned(filtered, taskID, fanned)
	plan.Targets = targets
	plan.AlreadyFanned = skipped
	plan.UnfannedCount = len(targets)
	if plan.UnfannedCount == 0 {
		return plan
	}

	if cfg.UnblockedOnly {
		complete := cachedTaskComplete(taskComplete)
		plan.Targets, plan.AlreadyComplete = splitCompleteTasks(plan.Targets, complete)
		plan.UnfannedCount = len(plan.Targets)
		if plan.UnfannedCount == 0 {
			return plan
		}
		plan.Targets, plan.BlockedRows = splitPlanBlocked(plan.Targets, spec.Tasks, complete)
	}
	plan.Targets, plan.LimitDeferred = fanset.ApplyLimit(plan.Targets, cfg.Limit)
	return plan
}

func splitCompleteTasks(tasks []planspec.Task, taskComplete func(planspec.Task) bool) (targets []planspec.Task, skipped []string) {
	for _, task := range tasks {
		if taskComplete(task) {
			skipped = append(skipped, task.ID)
			continue
		}
		targets = append(targets, task)
	}
	return targets, skipped
}

func splitPlanBlocked(targets, allTasks []planspec.Task, taskComplete func(planspec.Task) bool) (kept []planspec.Task, blocked []taskBlockedRow) {
	byID := map[string]planspec.Task{}
	for _, task := range allTasks {
		byID[task.ID] = task
	}
	targetIDs := map[string]bool{}
	for _, task := range targets {
		targetIDs[task.ID] = true
	}
	cache := map[string]bool{}
	for _, task := range targets {
		var openDeps []string
		for _, depID := range task.BlockedBy {
			dep, ok := byID[depID]
			if !ok {
				continue
			}
			complete, ok := cache[depID]
			if !ok {
				complete = taskComplete(dep)
				cache[depID] = complete
			}
			if complete {
				continue
			}
			if targetIDs[depID] {
				openDeps = append(openDeps, depID)
				continue
			}
			openDeps = append(openDeps, depID)
		}
		if len(openDeps) > 0 {
			blocked = append(blocked, taskBlockedRow{Task: task, Refs: strings.Join(openDeps, ", ")})
			continue
		}
		kept = append(kept, task)
	}
	return kept, blocked
}

func cachedTaskComplete(taskComplete func(planspec.Task) bool) func(planspec.Task) bool {
	cache := map[string]bool{}
	return func(task planspec.Task) bool {
		complete, ok := cache[task.ID]
		if !ok {
			complete = taskComplete(task)
			cache[task.ID] = complete
		}
		return complete
	}
}

func planTaskComplete(gh ghissue.Runner, cfg *cliflags.Config, projectRoot string, store state.Store, spec planspec.Spec, task planspec.Task, lg *log.Logger) bool {
	branch := task.Branch
	if branch == "" {
		branch = naming.BranchName("", cfg.BranchPrefix, panelaunch.PlanTaskSlug(spec.Plan.Slug, task))
	}
	prs, err := gh.PRsForBranch(branch)
	if err != nil {
		lg.Warn("%s: could not resolve blocker PRs for branch %s; treating as incomplete: %v", task.ID, branch, err)
		return false
	}
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "MERGED") || pr.MergedAt != nil {
			return true
		}
	}
	if len(prs) > 0 {
		return false
	}

	parentRef := panelaunch.PlanParentRef(spec.Plan.Slug)
	if _, ok := store.FindTask(parentRef, task.ID); ok {
		return false
	}
	if worktreeNameMatchesExact(existingWorktreeNames(filepath.Join(projectRoot, ".fanout", "worktrees")), panelaunch.PlanTaskSlug(spec.Plan.Slug, task)) {
		return false
	}
	return false
}

func executeTaskPlan(cfg *cliflags.Config, lg *log.Logger, rt *Runtime, spec planspec.Spec, targets []planspec.Task, resolvedSettings settings.Settings, hookConfig hooks.Config, recorder panelaunch.StateRecorder, c log.Palette, commandName string, teamCtx *briefing.TeamContext) TaskExecutionResult {
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: lg, Info: rt.Info, Backend: rt.Backend, Recorder: recorder, Palette: c, CommandName: commandName}
	created, failed := executeFailFast(
		targets,
		taskID,
		func(task planspec.Task) bool {
			return launcher.LaunchOK(panelaunch.NewTaskRequest(cfg, rt.Info.ProjectRoot, spec, task, resolvedSettings, hookConfig, teamCtx))
		},
		sleepBetweenFunc(cfg),
	)
	return TaskExecutionResult{Created: len(created), Failed: failed, CreatedIDs: created}
}

func logTaskPlanDetails(plan taskPlan, lg *log.Logger) {
	for _, id := range plan.MissingOnly {
		lg.Warn("--only: %s not in plan task set (ignored)", id)
	}
	for _, row := range plan.BlockedRows {
		lg.Info("deferred: %s (blocked by %s)", row.Task.ID, row.Refs)
	}
}

func logAlreadyFannedTasks(skipped []string, lg *log.Logger) {
	if len(skipped) == 0 {
		return
	}
	slices.Sort(skipped)
	lg.Info("already fanned-out (skipping): %s", strings.Join(skipped, " "))
}

func logAlreadyCompleteTasks(skipped []string, lg *log.Logger) {
	if len(skipped) == 0 {
		return
	}
	slices.Sort(skipped)
	lg.Info("already complete (skipping): %s", strings.Join(skipped, " "))
}

func printTaskDryRunPlan(plan taskPlan, lg *log.Logger, c log.Palette) {
	if len(plan.FilteredOnly) > 0 || len(plan.FilteredSkip) > 0 {
		fmt.Fprintf(lg.Stdout(), "\n%sfiltered out:%s\n", c.Info, c.Reset)
		for _, row := range plan.FilteredOnly {
			fmt.Fprintf(lg.Stdout(), "  %s %s - not in --only\n", row.ID, row.Title)
		}
		for _, row := range plan.FilteredSkip {
			fmt.Fprintf(lg.Stdout(), "  %s %s - in --skip\n", row.ID, row.Title)
		}
		fmt.Fprintln(lg.Stdout())
	}

	if len(plan.BlockedRows) > 0 {
		fmt.Fprintf(lg.Stdout(), "\n%sdeferred (blocked):%s\n", c.Info, c.Reset)
		for _, row := range plan.BlockedRows {
			fmt.Fprintf(lg.Stdout(), "  %s %s - blocked by %s\n", row.Task.ID, row.Task.Title, row.Refs)
		}
		fmt.Fprintln(lg.Stdout())
	}
}

func printTaskSummary(plan taskPlan, result TaskExecutionResult, cfg PlanCommandConfig, lg *log.Logger, c log.Palette, commandName string) {
	fmt.Fprintln(lg.Stdout())
	if result.Created > 0 {
		lg.Ok("created: %d", result.Created)
	}
	if result.Failed > 0 {
		lg.Err("failed:  %d", result.Failed)
	}
	if len(plan.AlreadyFanned) > 0 {
		lg.Info("skipped (already fanned-out): %d", len(plan.AlreadyFanned))
	}
	if len(plan.AlreadyComplete) > 0 {
		lg.Info("skipped (complete): %d", len(plan.AlreadyComplete))
	}
	if total := len(plan.FilteredOnly) + len(plan.FilteredSkip); total > 0 {
		lg.Info("skipped (filtered): %d", total)
	}
	if len(plan.BlockedRows) > 0 {
		lg.Info("deferred (blocked): %d", len(plan.BlockedRows))
	}

	if len(plan.LimitDeferred) == 0 {
		return
	}
	if result.Failed > 0 {
		lg.Info("deferred (--limit): %d", len(plan.LimitDeferred))
		lg.Warn("not printing --limit rerun hint because this run failed before all selected targets completed")
		return
	}
	fmt.Fprintf(lg.Stdout(), "\n%sDeferred %d task(s) due to --limit. Rerun with:%s\n", c.Info, len(plan.LimitDeferred), c.Reset)
	ids := taskIDCSV(plan.LimitDeferred)
	fmt.Fprintf(lg.Stdout(), "  %s\n", strings.Join(taskIDs(plan.LimitDeferred), " "))
	cliCfg := cfg.CLIConfig()
	fmt.Fprintf(lg.Stdout(), "  %s plan %s --only %s%s%s%s%s%s%s%s%s\n",
		ShellQuote(commandName),
		ShellQuote(cfg.SpecArg),
		ShellQuote(ids),
		boolFlag(" --unblocked-only", cfg.UnblockedOnly),
		boolFlag(" --team", cfg.Team),
		settingsFlags(cliCfg),
		worktreeFlags(cliCfg),
		agentFlagsForTasks(cfg.Agent, cfg.AgentOverrides, plan.LimitDeferred),
		optFlag("--backend", string(cfg.Backend)),
		optFlag("--session", cfg.Session),
		optFlag("--sleep", sleepFlagValue(cfg.SleepBetween)))
}

func taskIDCSV(tasks []planspec.Task) string {
	return strings.Join(taskIDs(tasks), ",")
}

func taskIDs(tasks []planspec.Task) []string {
	ids := make([]string, len(tasks))
	for i, task := range tasks {
		ids[i] = task.ID
	}
	return ids
}

func sleepFlagValue(v float64) string {
	if v == cliflags.DefaultSleepBetween {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
