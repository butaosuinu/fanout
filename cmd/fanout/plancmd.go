package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/atomicfs"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/naming"
	"github.com/butaosuinu/fanout/internal/planspec"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/worktree"
)

const planSubcommand = "plan"

var (
	rePlanPositiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)
	rePlanNumber      = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	rePlanTaskID      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

type planCommandConfig struct {
	SpecArg            string
	SpecPath           string
	Agent              string
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

type taskExecutionResult struct {
	Created    int
	Failed     int
	CreatedIDs []string
}

func isPlanRequest(args []string) bool {
	return len(args) > 0 && args[0] == planSubcommand
}

func cmdPlan(args []string, lg *log.Logger, commandName string) exitcode.Code {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprint(lg.Stdout(), planUsage)
			return exitcode.OK
		}
	}

	cfg, code := parsePlanCommand(args, lg)
	if code != exitcode.OK {
		return code
	}
	if cfg.Debug {
		lg = log.New(true)
	}

	if missing := checkPlanDeps(cfg); len(missing) > 0 {
		lg.Err("missing dependencies:")
		for _, d := range missing {
			fmt.Fprintf(lg.Stderr(), "  - %s\n", d)
		}
		return exitcode.Env
	}

	cliCfg := cfg.cliConfig()
	rt, code := resolveRuntime(cliCfg, lg)
	if code != exitcode.OK {
		return code
	}
	cfg.Agent = cliCfg.Agent

	resolvedSettings := settings.Resolve(rt.info.ProjectRoot, settings.CLIOverrides{
		AutoPullRequest:    cfg.AutoPullRequest,
		PRReviewGate:       cfg.PRReviewGate,
		BriefingCodeReview: cfg.BriefingCodeReview,
		AgentTeamsHint:     cfg.AgentTeamsHint,
		PRVisualization:    cfg.PRVisualization,
		DashboardKeybind:   cfg.DashboardKeybind,
	}, lg.Warn)

	cfg.SpecPath = resolvePlanSpecPath(rt.info.ProjectRoot, cfg.SpecArg)
	spec, err := planspec.LoadWithoutResolvedNameChecks(cfg.SpecPath)
	if err != nil {
		lg.Err("%v", err)
		return exitcode.Env
	}
	cfg.BaseBranch, err = resolvePlanBaseBranch(cfg, spec, rt.info.ProjectRoot)
	if err != nil {
		lg.Err("%v", err)
		return exitcode.Env
	}
	if err := validatePlanExecutionNames(spec, cfg); err != nil {
		lg.Err("validate plan execution names %s: %v", cfg.SpecPath, err)
		return exitcode.Env
	}
	cliCfg = cfg.cliConfig()

	store, recorder, code := loadPlanState(cfg, rt.info.ProjectRoot, lg)
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
	if !cfg.DryRun {
		if err := copyPlanSpec(cfg.SpecPath, rt.info.ProjectRoot, spec.Plan.Slug); err != nil {
			lg.Err("copy plan spec: %v", err)
			return exitcode.Env
		}
	}
	cfg.SpecArg = planRerunSpecArg(cfg, spec)

	parentRef := planParentRef(spec.Plan.Slug)
	fanned := mergeTaskFanned(store.FannedTaskIDsForParent(parentRef), existingPlanWorktreeFanned(rt.info.ProjectRoot, spec))
	plan := buildTaskPlan(cfg, spec, fanned, func(task planspec.Task) bool {
		return planTaskComplete(rt.gh, cliCfg, rt.info.ProjectRoot, store, spec, task, lg)
	})
	logTaskPlanDetails(plan, lg)

	if plan.AfterFilter == 0 {
		lg.Info("all plan tasks filtered out by --only/--skip. nothing to do.")
		return exitcode.OK
	}
	if plan.UnfannedCount == 0 {
		if len(plan.AlreadyComplete) == 0 {
			lg.Ok("all %d plan task(s) already have a fanout pane. nothing to do.", len(plan.AlreadyFanned))
		} else {
			lg.Ok("all %d selected plan task(s) already have a fanout pane or are complete. nothing to do.", plan.AfterFilter)
		}
		return exitcode.OK
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

	result := executeTaskPlan(cliCfg, lg, rt.info, spec, plan.Targets, resolvedSettings, recorder, c, commandName)
	printTaskSummary(plan, result, cfg, lg, c, commandName)

	if !cfg.DryRun && result.Created > 0 {
		bindDashboardKey(lg, resolvedSettings.DashboardKeybind)
	}
	if result.Failed > 0 {
		return exitcode.Env
	}
	return exitcode.OK
}

func parsePlanCommand(args []string, lg *log.Logger) (planCommandConfig, exitcode.Code) {
	cfg := planCommandConfig{SleepBetween: cliflags.DefaultSleepBetween}
	var limitRaw, sleepRaw string

	valueOptions := map[string]func(string){
		"--agent":         func(v string) { cfg.Agent = v },
		"--base-branch":   func(v string) { cfg.BaseBranch = v },
		"--branch-prefix": func(v string) { cfg.BranchPrefix = v },
		"--limit":         func(v string) { limitRaw = v },
		"--only":          func(v string) { cfg.OnlyArg = v },
		"--skip":          func(v string) { cfg.SkipArg = v },
		"--session":       func(v string) { cfg.Session = v },
		"--sleep":         func(v string) { sleepRaw = v },
	}
	boolOptions := map[string]func(){
		"--dry-run":                 func() { cfg.DryRun = true },
		"--debug":                   func() { cfg.Debug = true },
		"--no-refresh":              func() { cfg.NoRefresh = true },
		"--unblocked-only":          func() { cfg.UnblockedOnly = true },
		"--auto-pr":                 func() { cfg.AutoPullRequest = new(true) },
		"--no-auto-pr":              func() { cfg.AutoPullRequest = new(false) },
		"--pr-review-gate":          func() { cfg.PRReviewGate = new(true) },
		"--no-pr-review-gate":       func() { cfg.PRReviewGate = new(false) },
		"--briefing-code-review":    func() { cfg.BriefingCodeReview = new(true) },
		"--no-briefing-code-review": func() { cfg.BriefingCodeReview = new(false) },
		"--agent-teams-hint":        func() { cfg.AgentTeamsHint = new(true) },
		"--no-agent-teams-hint":     func() { cfg.AgentTeamsHint = new(false) },
		"--pr-visualization":        func() { cfg.PRVisualization = new(true) },
		"--no-pr-visualization":     func() { cfg.PRVisualization = new(false) },
		"--dashboard-keybind":       func() { cfg.DashboardKeybind = new(true) },
		"--no-dashboard-keybind":    func() { cfg.DashboardKeybind = new(false) },
	}

	for i := 0; i < len(args); {
		arg := args[i]
		if handle, ok := valueOptions[arg]; ok {
			if i+1 >= len(args) {
				lg.Err("%s requires an argument", arg)
				return planCommandConfig{}, exitcode.Env
			}
			handle(args[i+1])
			i += 2
			continue
		}
		if handle, ok := boolOptions[arg]; ok {
			handle()
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			lg.Err("unknown plan option: %s", arg)
			fmt.Fprint(lg.Stderr(), planUsage)
			return planCommandConfig{}, exitcode.Invocation
		}
		if cfg.SpecArg != "" {
			lg.Err("unexpected plan argument: %s", arg)
			fmt.Fprint(lg.Stderr(), planUsage)
			return planCommandConfig{}, exitcode.Invocation
		}
		cfg.SpecArg = arg
		i++
	}

	if cfg.SpecArg == "" {
		fmt.Fprint(lg.Stderr(), planUsage)
		return planCommandConfig{}, exitcode.Invocation
	}
	if limitRaw != "" {
		n, err := parsePlanPositiveInt("--limit", limitRaw)
		if err != nil {
			lg.Err("%s", err.Error())
			return planCommandConfig{}, exitcode.Env
		}
		cfg.Limit = n
	}
	if sleepRaw != "" {
		if !rePlanNumber.MatchString(sleepRaw) {
			lg.Err("--sleep must be a number, got: %s", sleepRaw)
			return planCommandConfig{}, exitcode.Env
		}
		n, _ := strconv.ParseFloat(sleepRaw, 64)
		cfg.SleepBetween = n
	}
	if err := validatePlanBaseBranch("--base-branch", cfg.BaseBranch); err != nil {
		lg.Err("%v", err)
		return planCommandConfig{}, exitcode.Env
	}
	if cfg.BranchPrefix != "" && strings.ContainsAny(cfg.BranchPrefix, " \t\r\n") {
		lg.Err("--branch-prefix must not contain whitespace, got: %s", cfg.BranchPrefix)
		return planCommandConfig{}, exitcode.Env
	}
	if cfg.OnlyArg != "" && cfg.SkipArg != "" {
		lg.Err("--only and --skip are mutually exclusive")
		return planCommandConfig{}, exitcode.Env
	}
	var err error
	if cfg.OnlyArg != "" {
		cfg.Only, err = parseTaskIDCSV("--only", cfg.OnlyArg)
		if err != nil {
			lg.Err("%s", err.Error())
			return planCommandConfig{}, exitcode.Env
		}
	}
	if cfg.SkipArg != "" {
		cfg.Skip, err = parseTaskIDCSV("--skip", cfg.SkipArg)
		if err != nil {
			lg.Err("%s", err.Error())
			return planCommandConfig{}, exitcode.Env
		}
	}
	return cfg, exitcode.OK
}

func (cfg planCommandConfig) cliConfig() *cliflags.Config {
	return &cliflags.Config{
		ParentRef:          planSubcommand,
		Agent:              cfg.Agent,
		BaseBranch:         cfg.BaseBranch,
		BranchPrefix:       cfg.BranchPrefix,
		Session:            cfg.Session,
		SleepBetween:       cfg.SleepBetween,
		NoRefresh:          cfg.NoRefresh,
		DryRun:             cfg.DryRun,
		Debug:              cfg.Debug,
		UnblockedOnly:      cfg.UnblockedOnly,
		AutoPullRequest:    cfg.AutoPullRequest,
		PRReviewGate:       cfg.PRReviewGate,
		BriefingCodeReview: cfg.BriefingCodeReview,
		AgentTeamsHint:     cfg.AgentTeamsHint,
		PRVisualization:    cfg.PRVisualization,
		DashboardKeybind:   cfg.DashboardKeybind,
	}
}

func parsePlanPositiveInt(flag, raw string) (int, error) {
	if !rePlanPositiveInt.MatchString(raw) {
		return 0, fmt.Errorf("%s must be a positive integer, got: %s", flag, raw)
	}
	n, _ := strconv.Atoi(raw)
	return n, nil
}

func parseTaskIDCSV(flag, csv string) ([]string, error) {
	csv = strings.TrimSuffix(csv, ",")
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if !rePlanTaskID.MatchString(part) {
			return nil, fmt.Errorf("%s: invalid entry '%s' (must be a lowercase kebab-case task id)", flag, part)
		}
		out = append(out, part)
	}
	return out, nil
}

func checkPlanDeps(cfg planCommandConfig) []string {
	var missing []string
	check := func(cmd, hint string) {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, hint)
		}
	}
	check("git", "git")
	check("tmux", "tmux (brew install tmux)")
	if cfg.UnblockedOnly {
		check("gh", "gh (brew install gh)")
	}
	return missing
}

func resolvePlanBaseBranch(cfg planCommandConfig, spec planspec.Spec, projectRoot string) (string, error) {
	if cfg.BaseBranch != "" {
		return cfg.BaseBranch, nil
	}
	if spec.Plan.BaseBranch != "" {
		if err := validatePlanBaseBranch("plan.base_branch", spec.Plan.BaseBranch); err != nil {
			return "", err
		}
		return spec.Plan.BaseBranch, nil
	}
	return worktree.ResolveDefaultBranch(projectRoot), nil
}

func validatePlanBaseBranch(label, value string) error {
	if value != "" && strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("%s must not contain whitespace, got: %s", label, value)
	}
	return nil
}

func planRerunSpecArg(cfg planCommandConfig, spec planspec.Spec) string {
	if cfg.DryRun {
		return cfg.SpecArg
	}
	return spec.Plan.Slug
}

func resolvePlanSpecPath(projectRoot, arg string) string {
	if filepath.IsAbs(arg) || strings.ContainsRune(arg, os.PathSeparator) || strings.HasSuffix(arg, ".json") {
		return arg
	}
	return filepath.Join(projectRoot, ".fanout", "plans", arg+".json")
}

func loadPlanState(cfg planCommandConfig, projectRoot string, lg *log.Logger) (state.Store, *state.LockedStore, exitcode.Code) {
	if cfg.DryRun {
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

func copyPlanSpec(src, projectRoot, slug string) error {
	dst := filepath.Join(projectRoot, ".fanout", "plans", slug+".json")
	srcAbs, srcErr := filepath.Abs(src)
	dstAbs, dstErr := filepath.Abs(dst)
	if srcErr == nil && dstErr == nil && srcAbs == dstAbs {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return atomicfs.WriteFile(dst, data, 0o644)
}

func planParentRef(slug string) string {
	return "plan:" + slug
}

func planTaskSlug(planSlug string, task planspec.Task) string {
	if task.Slug != "" {
		return task.Slug
	}
	slug := planSlug + "-" + task.ResolvedSlug()
	if len(slug) <= naming.MaxSlugLength {
		return slug
	}
	sum := sha1.Sum([]byte(slug))
	suffix := "-" + hex.EncodeToString(sum[:])[:8]
	baseLen := naming.MaxSlugLength - len(suffix)
	return strings.Trim(slug[:baseLen], "-") + suffix
}

func validatePlanExecutionNames(spec planspec.Spec, cfg planCommandConfig) error {
	seenSlugs := map[string]int{}
	seenBranches := map[string]int{}
	for i, task := range spec.Tasks {
		slug := planTaskSlug(spec.Plan.Slug, task)
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

func mergeTaskFanned(primary, fallback map[string]bool) map[string]bool {
	out := map[string]bool{}
	for id := range primary {
		out[id] = true
	}
	for id := range fallback {
		out[id] = true
	}
	return out
}

func existingPlanWorktreeFanned(projectRoot string, spec planspec.Spec) map[string]bool {
	out := map[string]bool{}
	worktreeNames := existingWorktreeNames(filepath.Join(projectRoot, ".fanout", "worktrees"))
	for _, task := range spec.Tasks {
		if worktreeNameMatchesExact(worktreeNames, planTaskSlug(spec.Plan.Slug, task)) {
			out[task.ID] = true
		}
	}
	return out
}

func buildTaskPlan(cfg planCommandConfig, spec planspec.Spec, fanned map[string]bool, taskComplete func(planspec.Task) bool) taskPlan {
	plan := taskPlan{
		TotalTasks:     len(spec.Tasks),
		SourceArgument: cfg.SpecArg,
	}
	filtered, onlyRows, skipRows, missingOnly := filterTaskOnlySkip(spec.Tasks, cfg.Only, cfg.Skip)
	plan.FilteredOnly = onlyRows
	plan.FilteredSkip = skipRows
	plan.MissingOnly = missingOnly
	plan.AfterFilter = len(filtered)
	if plan.AfterFilter == 0 {
		return plan
	}

	targets, skipped := splitAlreadyFannedTasks(filtered, fanned)
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
	plan.Targets, plan.LimitDeferred = applyTaskLimit(plan.Targets, cfg.Limit)
	return plan
}

func filterTaskOnlySkip(tasks []planspec.Task, only, skip []string) (kept, filteredOnly, filteredSkip []planspec.Task, missingOnly []string) {
	if len(only) == 0 && len(skip) == 0 {
		return tasks, nil, nil, nil
	}

	taskSet := map[string]bool{}
	for _, task := range tasks {
		taskSet[task.ID] = true
	}
	for _, id := range only {
		if !taskSet[id] {
			missingOnly = append(missingOnly, id)
		}
	}

	onlySet := stringSet(only)
	skipSet := stringSet(skip)
	for _, task := range tasks {
		switch {
		case len(only) > 0 && !onlySet[task.ID]:
			filteredOnly = append(filteredOnly, task)
		case len(skip) > 0 && skipSet[task.ID]:
			filteredSkip = append(filteredSkip, task)
		default:
			kept = append(kept, task)
		}
	}
	return kept, filteredOnly, filteredSkip, missingOnly
}

func splitAlreadyFannedTasks(tasks []planspec.Task, fanned map[string]bool) (targets []planspec.Task, skipped []string) {
	for _, task := range tasks {
		if fanned[task.ID] {
			skipped = append(skipped, task.ID)
			continue
		}
		targets = append(targets, task)
	}
	return targets, skipped
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
		branch = naming.BranchName("", cfg.BranchPrefix, planTaskSlug(spec.Plan.Slug, task))
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

	parentRef := planParentRef(spec.Plan.Slug)
	if _, ok := store.FindTask(parentRef, task.ID); ok {
		return false
	}
	if worktreeNameMatchesExact(existingWorktreeNames(filepath.Join(projectRoot, ".fanout", "worktrees")), planTaskSlug(spec.Plan.Slug, task)) {
		return false
	}
	return false
}

func applyTaskLimit(tasks []planspec.Task, limit int) (targets, deferred []planspec.Task) {
	if limit > 0 && len(tasks) > limit {
		return tasks[:limit], tasks[limit:]
	}
	return tasks, nil
}

func executeTaskPlan(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, spec planspec.Spec, targets []planspec.Task, resolvedSettings settings.Settings, recorder paneStateRecorder, c log.Palette, commandName string) taskExecutionResult {
	var result taskExecutionResult
	for i, task := range targets {
		req := newTaskPaneRequest(cfg, info.ProjectRoot, spec, task, resolvedSettings)
		if !createPane(cfg, lg, info, req, recorder, c, commandName) {
			result.Failed++
			break
		}
		result.Created++
		result.CreatedIDs = append(result.CreatedIDs, task.ID)
		if i < len(targets)-1 && cfg.SleepBetween > 0 {
			sleepBetweenIssues(time.Duration(cfg.SleepBetween * float64(time.Second)))
		}
	}
	return result
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
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

func printTaskSummary(plan taskPlan, result taskExecutionResult, cfg planCommandConfig, lg *log.Logger, c log.Palette, commandName string) {
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
	cliCfg := cfg.cliConfig()
	fmt.Fprintf(lg.Stdout(), "  %s plan %s --only %s%s%s%s%s%s%s\n",
		shellQuote(commandName),
		shellQuote(cfg.SpecArg),
		shellQuote(ids),
		boolFlag(" --unblocked-only", cfg.UnblockedOnly),
		settingsFlags(cliCfg),
		worktreeFlags(cliCfg),
		optFlag("--agent", cfg.Agent),
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

const planUsage = `Usage: fanout plan <spec.json | plan-slug> [options]

Options:
  --agent <name>              Agent to launch (or FANOUT_AGENT)
  --dry-run                   Print worktree/tmux/agent actions without launching
  --limit <N>                 Create at most N panes
  --only <task-id[,id...]>    Restrict to task IDs
  --skip <task-id[,id...]>    Exclude task IDs
  --unblocked-only            Defer tasks whose blocked_by dependencies are incomplete
  --base-branch <branch>      Override spec plan.base_branch
  --branch-prefix <prefix>    Prefix generated branch names
  --no-refresh                Do not fetch/fast-forward the base branch
  --session <tmux-session>    Target a tmux session instead of the invoking pane
  --sleep <seconds>           Delay between pane launches
  --debug                     Print debug diagnostics
`
